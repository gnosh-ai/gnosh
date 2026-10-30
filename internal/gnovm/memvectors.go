// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mem's vectors (MEMPLAN §5): what an embedding server makes of each row of
// the index, so that mem.Find, and recall with each prompt, find by meaning
// as well as by words. They live in vectors-<generation>.db beside the
// index, each under the hash of the text it was made from, so a vector is
// never stale, a rebuilt index finds its rows' vectors again, and an
// erasure, which starts a new generation, starts them afresh. gnosh's main
// process makes them in the background after mem changes. With no
// embedding server set, or with it down, mem finds by words alone.

// memEmbedder is the embedding server: an OpenAI-compatible /embeddings
// endpoint, as llama-server --embedding serves.
type memEmbedder struct {
	base   string
	client *http.Client
}

// newMemEmbedder is the embedder at base, as "http://127.0.0.1:8091/v1";
// nil for "", and mem finds by words alone.
func newMemEmbedder(base string) *memEmbedder {
	if base == "" {
		return nil
	}
	return &memEmbedder{base: strings.TrimSuffix(base, "/"), client: &http.Client{}}
}

const (
	// memEmbedWait is how long a search waits for its words' vector, and a
	// recall for all of its work, before going on without.
	memEmbedWait = 500 * time.Millisecond
	// memEmbedBatch is how many texts one request embeds, and memFillWait
	// how long the request may take.
	memEmbedBatch = 32
	memFillWait   = time.Minute
	// memEmbedMax cuts what is embedded to about 500 tokens.
	memEmbedMax = 2000
	// memQuery goes before a search's words, as Qwen3-Embedding wants of a
	// query; what is searched is embedded as it is.
	memQuery = "Instruct: Given a request to a coding agent, retrieve the notes from its memory that bear on it\nQuery:"
	// memRecallScore and memFindScore are how close in meaning a row must
	// be to be recalled with a prompt, or found by mem.Find after the rows
	// holding the words: cosines of Qwen3-Embedding-0.6B's whole vectors.
	// TestMemRecallLive measures them on the AI box's server: at 0.52, 12
	// of 16 prompts mem had an answer for were given it, and none of 10
	// without one was given anything (2026-10-30). 0.45, a search's, finds
	// more and lets a little noise in.
	memRecallScore = 0.52
	memFindScore   = 0.45
)

// embed is the vectors of texts, a byte per number, and the name of the
// model that made them.
func (e *memEmbedder) embed(ctx context.Context, texts []string) ([][]int8, string, error) {
	body, err := json.Marshal(map[string]any{"input": texts})
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	// What a server says back may quote the texts: it is left out.
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("the embedding server answered %s", resp.Status)
	}
	var out struct {
		Model string `json:"model"`
		Data  []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("the embedding server's answer: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, "", fmt.Errorf("the embedding server gave %d vectors for %d texts", len(out.Data), len(texts))
	}
	vecs := make([][]int8, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) || vecs[d.Index] != nil || len(d.Embedding) == 0 {
			return nil, "", errors.New("the embedding server's vectors are out of order")
		}
		vecs[d.Index] = quantize(d.Embedding)
	}
	return vecs, out.Model, nil
}

// query is the vector of a search's words, or nil when the server does not
// answer within memEmbedWait.
func (e *memEmbedder) query(ctx context.Context, words string) ([]int8, string) {
	ctx, cancel := context.WithTimeout(ctx, memEmbedWait)
	defer cancel()
	vecs, model, err := e.embed(ctx, []string{memQuery + memCut(words, memEmbedMax)})
	if err != nil {
		slog.Debug("mem searches by words alone", "error", err)
		return nil, ""
	}
	return vecs[0], model
}

// quantize keeps a vector as a byte per number, scaled so that its largest
// is ±127. Cosines come out as they were: the same rows are recalled.
func quantize(v []float32) []int8 {
	var top float64
	for _, x := range v {
		top = math.Max(top, math.Abs(float64(x)))
	}
	q := make([]int8, len(v))
	if top == 0 {
		return q
	}
	for i, x := range v {
		q[i] = int8(math.Round(float64(x) / top * 127))
	}
	return q
}

// cosine is how close two vectors are in meaning, from -1 to 1; 0 for two
// that different models made.
func cosine(a, b []int8) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb int64
	for i := range a {
		x, y := int64(a[i]), int64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float64(dot) / math.Sqrt(float64(na)*float64(nb))
}

// memCut is s cut to at most n bytes, at a character.
func memCut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// memRow is a row of mem's index: an item, fact or rule, with its text.
type memRow struct {
	kind, name, title, result, notes, status string
}

// text is what a row is embedded as: its name and text, then its result
// and notes.
func (r memRow) text() string {
	s := r.name + ": " + r.title
	for _, more := range []string{r.result, r.notes} {
		if more != "" {
			s += "\n" + more
		}
	}
	return memCut(s, memEmbedMax)
}

// hash is what the row's vector is kept under.
func (r memRow) hash() string {
	h := sha256.Sum256([]byte(r.text()))
	return string(h[:])
}

// memVectors is vectors-<generation>.db: each vector under the hash of the
// text it was made from, and the model that made them all.
type memVectors struct {
	db *sql.DB
}

const memVectorsSchema = `
CREATE TABLE IF NOT EXISTS model(id INTEGER PRIMARY KEY CHECK (id = 1), name TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS vecs(hash BLOB PRIMARY KEY, vec BLOB NOT NULL) WITHOUT ROWID;
`

func memVectorsPath(dir, gen string) string {
	return filepath.Join(dir, "vectors-"+gen+".db")
}

func openMemVectors(path string) (*memVectors, error) {
	db, err := openSQLite(path, map[string]string{
		"journal_mode":  "WAL",
		"secure_delete": "ON",
		"busy_timeout":  "5000",
	})
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(memVectorsSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("mem's vectors: %w", err)
	}
	return &memVectors{db: db}, nil
}

func (v *memVectors) close() error { return v.db.Close() }

// all is every vector, by hash, and the model that made them.
func (v *memVectors) all() (map[string][]int8, string, error) {
	var model string
	err := v.db.QueryRow(`SELECT name FROM model WHERE id = 1`).Scan(&model)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, "", err
	}
	rows, err := v.db.Query(`SELECT hash, vec FROM vecs`)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := map[string][]int8{}
	for rows.Next() {
		var h, b []byte
		if err := rows.Scan(&h, &b); err != nil {
			return nil, "", err
		}
		vec := make([]int8, len(b))
		for i, c := range b {
			vec[i] = int8(c)
		}
		out[string(h)] = vec
	}
	return out, model, rows.Err()
}

// put keeps vecs, made by model, under hashes. Vectors another model made
// go first: two models' vectors cannot be compared.
func (v *memVectors) put(model string, hashes []string, vecs [][]int8) error {
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var had string
	err = tx.QueryRow(`SELECT name FROM model WHERE id = 1`).Scan(&had)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if had != model {
		if _, err := tx.Exec(`DELETE FROM vecs`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO model(id, name) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET name = excluded.name`, model); err != nil {
			return err
		}
	}
	for i, h := range hashes {
		b := make([]byte, len(vecs[i]))
		for j, x := range vecs[i] {
			b[j] = byte(x)
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO vecs(hash, vec) VALUES(?, ?)`, []byte(h), b); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// model is the model that made the vectors; "" for none yet.
func (v *memVectors) model() (string, error) {
	var model string
	err := v.db.QueryRow(`SELECT name FROM model WHERE id = 1`).Scan(&model)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return model, err
}

// hashes is the hashes of the texts that have vectors.
func (v *memVectors) hashes() (map[string]bool, error) {
	rows, err := v.db.Query(`SELECT hash FROM vecs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[string(h)] = true
	}
	return out, rows.Err()
}

// keepOnly deletes the vectors of texts no row has now.
func (v *memVectors) keepOnly(rows []memRow) error {
	have, err := v.hashes()
	if err != nil {
		return err
	}
	for _, r := range rows {
		delete(have, r.hash())
	}
	for h := range have {
		if _, err := v.db.Exec(`DELETE FROM vecs WHERE hash = ?`, []byte(h)); err != nil {
			return err
		}
	}
	return nil
}

// atMem runs f on the index and the vectors of mem in dir, under its lock,
// waiting for it no longer than ctx allows: those of the journal's
// generation, which must be gen unless gen is "". ok is false when f did
// not run: there is no mem, the journal has moved on to another
// generation, or it has no index yet. Where there is no mem, nothing is
// locked or made.
func atMem(ctx context.Context, dir, gen string, f func(gen string, x *memIndex, v *memVectors) error) (ok bool, err error) {
	journal := filepath.Join(dir, "journal.jsonl")
	if _, err := os.Stat(journal); err != nil {
		return false, nil
	}
	release, err := lockMemDir(ctx, dir)
	if err != nil {
		return false, err
	}
	defer release()
	hdr, _, err := readJournalHeader(journal)
	if err != nil {
		return false, err
	}
	if gen != "" && hdr.Generation != gen {
		return false, nil
	}
	path := filepath.Join(dir, "index-"+hdr.Generation+".db")
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	x, err := openMemIndex(path)
	if err != nil {
		return false, err
	}
	defer x.close()
	v, err := openMemVectors(memVectorsPath(dir, hdr.Generation))
	if err != nil {
		return false, err
	}
	defer v.close()
	return true, f(hdr.Generation, x, v)
}

// memSnapshot is mem's rows and their vectors, as they were read together
// under mem's lock.
type memSnapshot struct {
	gen   string
	rows  []memRow
	vecs  map[string][]int8
	model string
}

// readMemSnapshot reads the rows and vectors of mem in dir (see atMem).
func readMemSnapshot(ctx context.Context, dir string) (s memSnapshot, ok bool, err error) {
	ok, err = atMem(ctx, dir, "", func(gen string, x *memIndex, v *memVectors) (err error) {
		s.gen = gen
		if s.rows, err = x.rows(); err != nil {
			return err
		}
		s.vecs, s.model, err = v.all()
		return err
	})
	return s, ok && err == nil, err
}

// lacksVectors reports whether a row of s has no vector yet.
func (s memSnapshot) lacksVectors() bool {
	for _, r := range s.rows {
		if s.vecs[r.hash()] == nil {
			return true
		}
	}
	return false
}

// memScored is a row and how close it is to what was searched.
type memScored struct {
	memRow
	score float64
}

// nearest is the rows of s whose kind is in kinds, closest first, at least
// least close to q, which model made.
func (s memSnapshot) nearest(q []int8, model string, least float64, kinds ...string) []memScored {
	if q == nil || model != s.model {
		return nil
	}
	var out []memScored
	for _, r := range s.rows {
		if !slices.Contains(kinds, r.kind) || r.status == "forgotten" {
			continue
		}
		if c := cosine(q, s.vecs[r.hash()]); c >= least {
			out = append(out, memScored{r, c})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

// finder answers the find native from the index x of journal j: the rows
// holding all the words, then those closest in meaning, and, when neither
// finds any, the rows holding one of the words; meaning is whether it
// could look by meaning. An index that cannot answer finds nothing, and the
// error is logged.
func (h *memHost) finder(ctx context.Context, x *memIndex, j *journal) func(words string, max int) (names, kinds []string, meaning bool) {
	return func(words string, max int) (names, kinds []string, meaning bool) {
		all, any, err := x.find(words, max)
		if err != nil {
			slog.Warn("mem's index could not search", "error", err)
			return nil, nil, false
		}
		seen := map[string]bool{}
		add := func(r memRow) {
			if !seen[r.kind+" "+r.name] && len(names) < max {
				seen[r.kind+" "+r.name] = true
				names, kinds = append(names, r.name), append(kinds, r.kind)
			}
		}
		for _, r := range all {
			add(r)
		}
		if near, ok := h.nearest(ctx, x, j, words); ok {
			meaning = true
			for _, r := range near {
				add(r.memRow)
			}
		}
		if len(names) == 0 {
			for _, r := range any {
				add(r)
			}
		}
		return names, kinds, meaning
	}
}

// nearest is the rows of x close in meaning to words, closest first; ok is
// false when there is no embedding server, no vectors yet, or the server
// does not answer, or is another model now.
func (h *memHost) nearest(ctx context.Context, x *memIndex, j *journal, words string) ([]memScored, bool) {
	if h.embed == nil {
		return nil, false
	}
	v, err := openMemVectors(memVectorsPath(h.dir, j.generation))
	if err != nil {
		slog.Warn("mem's vectors could not be read", "error", err)
		return nil, false
	}
	defer v.close()
	vecs, had, err := v.all()
	if err != nil || len(vecs) == 0 {
		return nil, false
	}
	rows, err := x.rows()
	if err != nil {
		slog.Warn("mem's index could not be read", "error", err)
		return nil, false
	}
	q, model := h.embed.query(ctx, words)
	if q == nil || model != had {
		return nil, false
	}
	s := memSnapshot{gen: j.generation, rows: rows, vecs: vecs, model: had}
	return s.nearest(q, model, memFindScore, "item", "fact", "rule"), true
}

// meaningLine is what /mem says of finding by meaning: which model, and how
// much of mem it has read; "" with no embedding server, or an empty mem.
func (h *memHost) meaningLine(ctx context.Context) string {
	if h.embed == nil {
		return ""
	}
	var all, read int
	var model string
	ok, err := atMem(ctx, h.dir, "", func(_ string, x *memIndex, v *memVectors) error {
		rows, err := x.rows()
		if err != nil {
			return err
		}
		have, err := v.hashes()
		if err != nil {
			return err
		}
		all = len(rows)
		for _, r := range rows {
			if have[r.hash()] {
				read++
			}
		}
		model, err = v.model()
		return err
	})
	switch {
	case err != nil || !ok || all == 0:
		return ""
	case read == 0:
		return fmt.Sprintf("mem will find by meaning too once the embedding server has read it (0 of %d entries so far)", all)
	case read < all:
		return fmt.Sprintf("mem also finds by meaning, with %s: %d of %d entries read, the rest on their way", model, read, all)
	}
	return fmt.Sprintf("mem also finds by meaning, with %s (all %d entries read)", model, all)
}

// memFills keeps one fill of mem's vectors running per mem directory in
// this process, asks a running one to go round once more when mem changes
// under it, and says when fills start failing, once.
var memFills = struct {
	sync.Mutex
	running, again, failing map[string]bool
}{running: map[string]bool{}, again: map[string]bool{}, failing: map[string]bool{}}

// fillMemVectors makes, in the background, the vectors mem in dir lacks.
func fillMemVectors(dir string, e *memEmbedder) {
	if e == nil {
		return
	}
	memFills.Lock()
	defer memFills.Unlock()
	if memFills.running[dir] {
		memFills.again[dir] = true
		return
	}
	memFills.running[dir] = true
	go func() {
		for {
			err := fillMemVectorsNow(context.Background(), dir, e)
			memFills.Lock()
			if err != nil && !memFills.failing[dir] {
				slog.Warn("mem's vectors could not be made; mem finds by words until they are", "error", err)
			}
			memFills.failing[dir] = err != nil
			if !memFills.again[dir] {
				delete(memFills.running, dir)
				memFills.Unlock()
				return
			}
			delete(memFills.again, dir)
			memFills.Unlock()
		}
	}()
}

// fillMemVectorsNow makes the vectors mem in dir lacks, a batch at a time,
// until it lacks none, and then deletes those of texts no row has any more.
// It holds mem's lock only to read and to write, and stops when the
// generation changes: an erasure starts the vectors afresh, and what it
// erased is never embedded after it.
func fillMemVectorsNow(ctx context.Context, dir string, e *memEmbedder) error {
	for {
		var gen string
		var todo []memRow
		ok, err := atMem(ctx, dir, "", func(g string, x *memIndex, v *memVectors) error {
			gen = g
			rows, err := x.rows()
			if err != nil {
				return err
			}
			have, err := v.hashes()
			if err != nil {
				return err
			}
			for _, r := range rows {
				if h := r.hash(); !have[h] {
					have[h] = true
					todo = append(todo, r)
				}
			}
			if len(todo) == 0 {
				return v.keepOnly(rows)
			}
			return nil
		})
		if err != nil || !ok || len(todo) == 0 {
			return err
		}
		// What changed meanwhile, or another model, which clears the
		// vectors before it, is seen the next time round.
		for len(todo) > 0 {
			batch := todo[:min(len(todo), memEmbedBatch)]
			todo = todo[len(batch):]
			texts := make([]string, len(batch))
			hashes := make([]string, len(batch))
			for i, r := range batch {
				texts[i], hashes[i] = r.text(), r.hash()
			}
			ectx, cancel := context.WithTimeout(ctx, memFillWait)
			vecs, model, err := e.embed(ectx, texts)
			cancel()
			if err != nil {
				return err
			}
			kept, err := atMem(ctx, dir, gen, func(_ string, _ *memIndex, v *memVectors) error {
				return v.put(model, hashes, vecs)
			})
			if err != nil || !kept {
				return err
			}
		}
	}
}
