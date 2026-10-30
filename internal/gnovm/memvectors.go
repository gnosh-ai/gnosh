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
	"regexp"
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
	// memEmbedMax cuts what is embedded to about 500 tokens, and
	// memQueryMax each part of a search: a prompt of pasted logs is
	// searched by its start.
	memEmbedMax = 2000
	memQueryMax = 1000
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

// query is the vectors of a search's words (memQueries), or an error when
// the server does not answer within memEmbedWait. A request that takes
// longer is not cut: it goes on in the background, up to memFillWait, and
// keeps its connection. Over a slow link, a tunnel where a request takes
// 490-660 ms on a new connection and 220-300 on an open one, cutting it
// closed the connection, so every next search paid for a new one, ran out
// of time too, and none found by meaning (QA, 2026-10-30).
func (e *memEmbedder) query(ctx context.Context, words string) ([][]int8, string, error) {
	var texts []string
	for _, q := range memQueries(words) {
		texts = append(texts, memQuery+memCut(q, memQueryMax))
	}
	type answer struct {
		vecs  [][]int8
		model string
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), memFillWait)
		defer cancel()
		vecs, model, err := e.embed(ctx, texts)
		done <- answer{vecs, model, err}
	}()
	wait := time.NewTimer(memEmbedWait)
	defer wait.Stop()
	select {
	case a := <-done:
		memLate.Store(e.base, a.err != nil)
		if a.err != nil {
			slog.Debug("mem searches by words alone", "error", a.err)
		}
		return a.vecs, a.model, a.err
	case <-wait.C:
	case <-ctx.Done():
	}
	memLate.Store(e.base, true)
	if _, warned := memLateWarned.LoadOrStore(e.base, true); !warned {
		slog.Warn("mem's embedding server did not answer a search in time; mem searched by words alone, and the request goes on so its connection is kept",
			"server", e.base, "wait", memEmbedWait)
	}
	return nil, "", errMemEmbedLate
}

// errMemEmbedLate is a search the embedding server did not answer within
// memEmbedWait.
var errMemEmbedLate = fmt.Errorf("the embedding server did not answer within %s", memEmbedWait)

// memLate is, for each embedding server, whether its last search went
// unanswered in time, which /mem says; memLateWarned, whether this process
// has said so in its log, which it does once.
var memLate, memLateWarned sync.Map

// memWordEnd reports whether b can end a word before a sentence's dot.
func memWordEnd(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == ')' || b == '"' || b == '\'' || b >= 0x80
}

// memQueryParts is how many of a search's sentences or clauses are
// searched as well as the whole.
const memQueryParts = 4

// memClauseRe is where a search's words may break into sentences and
// clauses: after a sentence's end, at a new line, a semicolon, or a comma
// before "and", "but", "then" or "also". memQueries keeps a break at a dot
// only after a word, so "./..." and "v2.0.0" stay whole.
var memClauseRe = regexp.MustCompile(`[.?!;]+(\s+|$)|\n+|,\s+(and|but|then|also)\s+`)

// memQueries is what words are searched as: the whole of them, and, when
// they hold more than one sentence or clause of three words or more, each
// of those too. A prompt asking two things is close to neither as a whole:
// "How do I push this to production, and who has to check it first?" came
// to 0.48 and 0.45 against the facts that answer it, asked whole, and
// 0.55 and 0.60, asked in halves (the recall-beyond-the-block scenario's
// mem, 2026-10-30).
func memQueries(words string) []string {
	var parts []string
	keep := func(p string) {
		if p = strings.TrimSpace(p); len(strings.Fields(p)) >= 3 {
			parts = append(parts, p)
		}
	}
	start := 0
	for _, at := range memClauseRe.FindAllStringIndex(words, -1) {
		if words[at[0]] == '.' && (at[0] == 0 || !memWordEnd(words[at[0]-1])) {
			continue
		}
		keep(words[start:at[0]])
		start = at[1]
	}
	keep(words[start:])
	if len(parts) < 2 {
		return []string{words}
	}
	return append([]string{words}, parts[:min(len(parts), memQueryParts)]...)
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

// norm is a vector's length; dot is two vectors' dot product, 0 for two
// that different models made. Their cosine, dot over both norms, is how
// close they are in meaning, from -1 to 1. A byte per number keeps both in
// 32 bits for vectors of up to 130,000 numbers.
func norm(v []int8) float64 {
	var n int32
	for _, x := range v {
		n += int32(x) * int32(x)
	}
	return math.Sqrt(float64(n))
}

func dot(a, b []int8) int32 {
	if len(a) != len(b) {
		return 0
	}
	b = b[:len(a)]
	var d int32
	for i, x := range a {
		d += int32(x) * int32(b[i])
	}
	return d
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
	kind, name, title, result, wait, notes, status string
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

// version counts the writes to the vectors, each in its own write's
// transaction: what a reader keeps is stale when it has moved.
const memVectorsSchema = `
CREATE TABLE IF NOT EXISTS model(id INTEGER PRIMARY KEY CHECK (id = 1), name TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS vecs(hash BLOB PRIMARY KEY, vec BLOB NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS version(id INTEGER PRIMARY KEY CHECK (id = 1), n INTEGER NOT NULL);
INSERT OR IGNORE INTO version(id, n) VALUES(1, 0);
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
	if _, err := tx.Exec(`UPDATE version SET n = n + 1 WHERE id = 1`); err != nil {
		return err
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

// keepOnly deletes the vectors of texts no row has now, in one write.
func (v *memVectors) keepOnly(rows []memRow) error {
	have, err := v.hashes()
	if err != nil {
		return err
	}
	for _, r := range rows {
		delete(have, r.hash())
	}
	if len(have) == 0 {
		return nil
	}
	tx, err := v.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for h := range have {
		if _, err := tx.Exec(`DELETE FROM vecs WHERE hash = ?`, []byte(h)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE version SET n = n + 1 WHERE id = 1`); err != nil {
		return err
	}
	return tx.Commit()
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
// under mem's lock; hashes, when kept, are the rows' hashes, in order.
type memSnapshot struct {
	gen    string
	rows   []memRow
	hashes []string
	vecs   map[string][]int8
	model  string
}

// hash is row i's hash.
func (s memSnapshot) hash(i int) string {
	if s.hashes != nil {
		return s.hashes[i]
	}
	return s.rows[i].hash()
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
	for i := range s.rows {
		if s.vecs[s.hash(i)] == nil {
			return true
		}
	}
	return false
}

// version is how many writes the vectors have had, and the model that
// made them.
func (v *memVectors) version() (int64, string, error) {
	model, err := v.model()
	if err != nil {
		return 0, "", err
	}
	var n int64
	err = v.db.QueryRow(`SELECT n FROM version WHERE id = 1`).Scan(&n)
	return n, model, err
}

// memSnapshotKey is what a snapshot of mem was read at: the journal's
// generation, the entry the index holds, and the vectors' version and
// model. Any change to mem, or any write to its vectors, changes it: a
// count of the vectors would not, when a fill adds the new text's vector
// and then prunes the old text's.
type memSnapshotKey struct {
	gen   string
	seq   int64
	end   int64
	vecs  int64
	model string
}

// memSnaps keeps, per mem directory, the last snapshot recall read, while
// mem has not changed (keptMemSnapshot). A change made through this
// process, an erasure first of all, drops it at once (Client.memChanged);
// one made elsewhere moves its key.
var memSnaps = struct {
	sync.Mutex
	m map[string]memSnapshotEntry
}{m: map[string]memSnapshotEntry{}}

// dropMemSnapshot forgets the snapshot kept of mem in dir.
func dropMemSnapshot(dir string) {
	memSnaps.Lock()
	defer memSnaps.Unlock()
	delete(memSnaps.m, dir)
}

// keptMemSnapshot is readMemSnapshot for recall, which reads with every
// prompt: the last one read, while mem and its vectors have not changed
// since; read again, and kept, when they have. Reading 10k rows and their
// vectors took 109 ms; seeing that they have not changed, two queries.
func keptMemSnapshot(ctx context.Context, dir string) (s memSnapshot, ok bool, err error) {
	ok, err = atMem(ctx, dir, "", func(gen string, x *memIndex, v *memVectors) error {
		st, _, err := x.stamp()
		if err != nil {
			return err
		}
		n, model, err := v.version()
		if err != nil {
			return err
		}
		key := memSnapshotKey{gen: gen, seq: st.Seq, end: st.End, vecs: n, model: model}
		memSnaps.Lock()
		kept, hit := memSnaps.m[dir]
		memSnaps.Unlock()
		if hit && kept.key == key {
			s = kept.s
			return nil
		}
		s.gen = gen
		if s.rows, err = x.rows(); err != nil {
			return err
		}
		if s.vecs, s.model, err = v.all(); err != nil {
			return err
		}
		s.hashes = make([]string, len(s.rows))
		for i, r := range s.rows {
			s.hashes[i] = r.hash()
		}
		memSnaps.Lock()
		memSnaps.m[dir] = memSnapshotEntry{key, s}
		memSnaps.Unlock()
		return nil
	})
	return s, ok && err == nil, err
}

// memSnapshotEntry is a snapshot kept, with what it was read at.
type memSnapshotEntry struct {
	key memSnapshotKey
	s   memSnapshot
}

// memScored is a row and how close it is to what was searched.
type memScored struct {
	memRow
	score float64
}

// nearest is the rows of s whose kind is in kinds, closest first, at least
// least close to one of qs, which model made.
func (s memSnapshot) nearest(qs [][]int8, model string, least float64, kinds ...string) []memScored {
	if qs == nil || model != s.model {
		return nil
	}
	if s.hashes == nil {
		s.hashes = make([]string, len(s.rows))
		for i, r := range s.rows {
			s.hashes[i] = r.hash()
		}
	}
	qn := make([]float64, len(qs))
	for i, q := range qs {
		qn[i] = norm(q)
	}
	var out []memScored
	for i, r := range s.rows {
		if !slices.Contains(kinds, r.kind) || r.status == "forgotten" {
			continue
		}
		// Each row's length once, then a dot product per query: a recall
		// over 10k rows with a prompt of five parts ranked in 114 ms with
		// both lengths worked out per query.
		v, best := s.vecs[s.hash(i)], 0.0
		if vn := norm(v); vn > 0 {
			best = -1
			for i, q := range qs {
				if qn[i] > 0 {
					best = max(best, float64(dot(q, v))/(qn[i]*vn))
				}
			}
		}
		if best >= least {
			out = append(out, memScored{r, best})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

// finder answers the find native from the index x of journal j: the rows
// holding all the words, then those closest in meaning, and, when neither
// finds any, the rows holding one of the words. how says how it looked
// (see nearest). An index that cannot answer finds nothing, and the error
// is logged.
func (h *memHost) finder(ctx context.Context, x *memIndex, j *journal) func(words string, max int) (names, kinds []string, how string) {
	return func(words string, max int) (names, kinds []string, how string) {
		all, any, err := x.find(words, max)
		if err != nil {
			slog.Warn("mem's index could not search", "error", err)
			return nil, nil, ""
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
		near, how := h.nearest(ctx, x, j, words)
		for _, r := range near {
			add(r.memRow)
		}
		if len(names) == 0 {
			for _, r := range any {
				add(r)
			}
		}
		return names, kinds, how
	}
}

// nearest is the rows of x close in meaning to words, closest first. how is
// "meaning" when it looked by meaning; "words" when the embedding server
// did not answer the search, so that only the words were matched; and ""
// when there is no server, no vectors yet, or vectors of another model.
func (h *memHost) nearest(ctx context.Context, x *memIndex, j *journal, words string) (rows []memScored, how string) {
	if h.embed == nil {
		return nil, ""
	}
	v, err := openMemVectors(memVectorsPath(h.dir, j.generation))
	if err != nil {
		slog.Warn("mem's vectors could not be read", "error", err)
		return nil, ""
	}
	defer v.close()
	vecs, had, err := v.all()
	if err != nil || len(vecs) == 0 {
		return nil, ""
	}
	all, err := x.rows()
	if err != nil {
		slog.Warn("mem's index could not be read", "error", err)
		return nil, ""
	}
	qs, model, err := h.embed.query(ctx, words)
	switch {
	case err != nil:
		return nil, "words"
	case model != had:
		return nil, ""
	}
	s := memSnapshot{gen: j.generation, rows: all, vecs: vecs, model: had}
	return s.nearest(qs, model, memFindScore, "item", "fact", "rule"), "meaning"
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
	var line string
	switch {
	case err != nil || !ok || all == 0:
		return ""
	case read == 0:
		line = fmt.Sprintf("mem will find by meaning too once the embedding server has read it (0 of %d entries so far)", all)
	case read < all:
		line = fmt.Sprintf("mem also finds by meaning, with %s: %d of %d entries read, the rest on their way", model, read, all)
	default:
		line = fmt.Sprintf("mem also finds by meaning, with %s (all %d entries read)", model, all)
	}
	if late, _ := memLate.Load(h.embed.base); late == true {
		line += fmt.Sprintf("; but the server did not answer its last search within %s, which matched words only", memEmbedWait)
	}
	return line
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
// until it lacks none the server will make, and then deletes those of texts
// no row has any more. It holds mem's lock only to read and to write, and
// checks the journal's generation just before each batch goes out and
// again before it is kept: an erasure starts the vectors afresh, and what
// it erased is not sent to the server after it.
func fillMemVectorsNow(ctx context.Context, dir string, e *memEmbedder) error {
	refused := map[string]bool{}
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
				if h := r.hash(); !have[h] && !refused[h] {
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
			if hdr, _, err := readJournalHeader(filepath.Join(dir, "journal.jsonl")); err != nil || hdr.Generation != gen {
				return err
			}
			texts := make([]string, len(batch))
			for i, r := range batch {
				texts[i] = r.text()
			}
			vecs, model, err := e.embedEach(ctx, texts)
			if err != nil {
				return err
			}
			var hashes []string
			var made [][]int8
			for i, r := range batch {
				if vecs[i] == nil {
					refused[r.hash()] = true
					continue
				}
				hashes, made = append(hashes, r.hash()), append(made, vecs[i])
			}
			kept, err := atMem(ctx, dir, gen, func(_ string, _ *memIndex, v *memVectors) error {
				return v.put(model, hashes, made)
			})
			if err != nil || !kept {
				return err
			}
		}
	}
}

// embedEach is embed for a fill: when the server refuses the batch, each
// text is sent alone, and one it refuses alone is sent again cut to a
// quarter (a server's batch can be shorter than memEmbedMax's tokens; a
// text of code or logs runs long). A text it still refuses has no vector,
// and nil in its place; when it refuses them all, it is down, and that is
// the error.
func (e *memEmbedder) embedEach(ctx context.Context, texts []string) ([][]int8, string, error) {
	embed := func(texts []string) ([][]int8, string, error) {
		ectx, cancel := context.WithTimeout(ctx, memFillWait)
		defer cancel()
		return e.embed(ectx, texts)
	}
	vecs, model, err := embed(texts)
	if err == nil || len(texts) == 1 && len(texts[0]) <= memEmbedMax/4 {
		return vecs, model, err
	}
	vecs = make([][]int8, len(texts))
	made := 0
	for i, t := range texts {
		for _, try := range []string{t, memCut(t, memEmbedMax/4)} {
			one, m, err1 := embed([]string{try})
			if err1 == nil {
				vecs[i], model = one[0], m
				made++
				break
			}
			if len(try) <= memEmbedMax/4 {
				break
			}
		}
	}
	if made == 0 {
		return nil, "", err
	}
	return vecs, model, nil
}
