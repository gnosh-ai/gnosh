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
	"encoding/json"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/require"
)

// fakeEmbedder is an embedding server for tests. A text's vector counts the
// concepts its words name, so texts that share a concept are close in
// meaning whatever their words. It answers 503 while down, and keeps every
// text it was given.
type fakeEmbedder struct {
	*httptest.Server
	model atomic.Value
	down  atomic.Bool
	mu    sync.Mutex
	texts []string
}

var fakeConcepts = [][]string{
	{"test", "tests", "testing", "suite"},
	{"deploy", "ship", "production", "site"},
	{"tea", "kettle", "boil"},
	{"password", "credentials", "staging"},
}

func newFakeEmbedder(t *testing.T) *fakeEmbedder {
	t.Helper()
	f := &fakeEmbedder{}
	f.model.Store("fake-embed")
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() || r.URL.Path != "/v1/embeddings" {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var in struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type datum struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		out := struct {
			Model string  `json:"model"`
			Data  []datum `json:"data"`
		}{Model: f.model.Load().(string)}
		f.mu.Lock()
		f.texts = append(f.texts, in.Input...)
		f.mu.Unlock()
		for i, text := range in.Input {
			out.Data = append(out.Data, datum{i, fakeVector(text)})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.Close)
	return f
}

// fakeVector is a text's concepts, counted, and a little of each other
// word, so that texts sharing no concept are far apart. As a model trained
// for it does, it reads a query for what follows memQuery's instruction.
func fakeVector(text string) []float32 {
	_, query, ok := strings.Cut(text, "\nQuery:")
	if ok {
		text = query
	}
	v := make([]float32, len(fakeConcepts)+64)
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		i := slices.IndexFunc(fakeConcepts, func(c []string) bool { return slices.Contains(c, w) })
		if i < 0 {
			h := fnv.New32a()
			h.Write([]byte(w))
			v[len(fakeConcepts)+int(h.Sum32()%64)] += 0.3
			continue
		}
		v[i]++
	}
	return v
}

// embedded is how many texts f was given, and whether any held s.
func (f *fakeEmbedder) embedded(s string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	held := false
	for _, t := range f.texts {
		held = held || strings.Contains(t, s)
	}
	return len(f.texts), held
}

// memVectorCount is how many vectors mem in dir holds for its journal's
// generation, and the model that made them.
func memVectorCount(t *testing.T, dir string) (int, string) {
	t.Helper()
	s, ok, err := readMemSnapshot(context.Background(), dir)
	require.NoError(t, err)
	require.True(t, ok)
	return len(s.vecs), s.model
}

// waitMemFills waits for the fills of mem's vectors this process runs in
// the background for dir, so a test's directory is not deleted under one.
func waitMemFills(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		memFills.Lock()
		running := memFills.running[dir]
		memFills.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("mem's vectors were still being made after 30 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// mem.Find looks by meaning once the rows have vectors: after the rows
// holding the words, those close in meaning; with the server down, by the
// words alone, as before it had any.
func TestMemFindsByMeaning(t *testing.T) {
	t.Parallel()
	f := newFakeEmbedder(t)
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	h.embed = newMemEmbedder(f.URL + "/v1")
	memOK(t, h, `mem.Fact("cmd/test", "go test -race ./...")`)
	memOK(t, h, `mem.Goal("make tea").Steps("boil water")`)
	memOK(t, h, `mem.Fact("tea/kind", "green, from the tin")`)
	find := func(words string) string {
		t.Helper()
		return memOK(t, h, `print(mem.Find("`+words+`"))`).Output
	}
	require.Contains(t, find("testing suite"), `nothing in mem matches "testing suite": Find matches the words as they were stored`,
		"no vectors yet")

	require.NoError(t, fillMemVectorsNow(context.Background(), dir, h.embed))
	n, model := memVectorCount(t, dir)
	require.Equal(t, 4, n, "two facts, a goal and its step")
	require.Equal(t, "fake-embed", model)
	out := find("testing suite")
	require.Contains(t, out, "fact `cmd/test` = go test -race ./...")
	require.NotContains(t, out, "tea")

	out = find("boil")
	require.Contains(t, out, "`make-tea/1`")
	require.Contains(t, out, "fact `tea/kind`")
	require.Less(t, strings.Index(out, "`make-tea/1`"), strings.Index(out, "fact `tea/kind`"),
		"the row holding the word comes before those close in meaning")

	require.Contains(t, find("deploy production"), `nothing in mem matches "deploy production", in its words or in meaning`)

	f.down.Store(true)
	require.Contains(t, find("testing suite"), "Find matches the words as they were stored", "the server down: words alone")
	require.Contains(t, find("race"), "fact `cmd/test`")
}

// A vector is kept under its text's hash: a rebuilt index finds its rows'
// vectors again, a changed text gets a new one and its old one goes, and
// another model's vectors are made again.
func TestMemVectorsFollowTheText(t *testing.T) {
	t.Parallel()
	f := newFakeEmbedder(t)
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	h.embed = newMemEmbedder(f.URL + "/v1")
	memOK(t, h, `mem.Fact("cmd/test", "go test ./...")`)
	memOK(t, h, `mem.Fact("cmd/build", "make build")`)
	require.NoError(t, fillMemVectorsNow(context.Background(), dir, h.embed))
	sent, _ := f.embedded("")
	require.Equal(t, 2, sent)

	idx, err := filepath.Glob(filepath.Join(dir, "index-*"))
	require.NoError(t, err)
	for _, p := range idx {
		require.NoError(t, os.Remove(p))
	}
	memOK(t, h, `print(mem.Show())`)
	require.NoError(t, fillMemVectorsNow(context.Background(), dir, h.embed))
	again, _ := f.embedded("")
	require.Equal(t, sent, again, "a rebuilt index finds its vectors")

	memOK(t, h, `mem.Fact("cmd/test", "go test -race ./...")`)
	require.NoError(t, fillMemVectorsNow(context.Background(), dir, h.embed))
	n, _ := memVectorCount(t, dir)
	require.Equal(t, 2, n, "the old text's vector goes")
	_, raced := f.embedded("go test -race")
	require.True(t, raced)

	f.model.Store("another-model")
	memOK(t, h, `mem.Fact("cmd/lint", "golangci-lint run")`)
	before, _ := f.embedded("")
	require.NoError(t, fillMemVectorsNow(context.Background(), dir, h.embed))
	n, model := memVectorCount(t, dir)
	require.Equal(t, 3, n)
	require.Equal(t, "another-model", model)
	after, _ := f.embedded("")
	require.Equal(t, before+3, after, "another model's vectors are made again: each row's, once")
}

// An erasure starts the vectors afresh: the old generation's go with its
// store and index, a fill that began before it keeps nothing, and what was
// erased is never sent to the server after it. A recall reads only the
// journal's generation: while that has no index yet, it recalls nothing,
// though an old index lies about.
func TestMemVectorsFollowAnErasure(t *testing.T) {
	t.Parallel()
	f := newFakeEmbedder(t)
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	h.embed = newMemEmbedder(f.URL + "/v1")
	const secret = "hunter22-staging"
	memOK(t, h, `mem.Fact("db/staging", "the staging password is `+secret+`")`)
	memOK(t, h, `mem.Fact("cmd/test", "go test ./...")`)
	require.NoError(t, fillMemVectorsNow(context.Background(), dir, h.embed))
	s, ok, err := readMemSnapshot(context.Background(), dir)
	require.NoError(t, err)
	require.True(t, ok)
	oldGen := s.gen
	saved := t.TempDir()
	for _, name := range []string{"index-" + oldGen + ".db", "vectors-" + oldGen + ".db"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(saved, name), b, 0o600))
	}

	res, err := h.call(context.Background(), memRequest{Code: `mem.Redact("` + secret + `")`, User: true})
	require.NoError(t, err, res.Output)
	_, err = os.Stat(memVectorsPath(dir, oldGen))
	require.ErrorIs(t, err, os.ErrNotExist, "the old generation's vectors go with it")
	// A fill that read the rows before the erasure keeps nothing after it.
	kept, err := atMem(context.Background(), dir, oldGen, func(string, *memIndex, *memVectors) error { return nil })
	require.NoError(t, err)
	require.False(t, kept)
	_, err = os.Stat(memVectorsPath(dir, oldGen))
	require.ErrorIs(t, err, os.ErrNotExist)

	sent, _ := f.embedded("")
	require.NoError(t, fillMemVectorsNow(context.Background(), dir, h.embed))
	all, held := f.embedded(secret)
	require.Equal(t, sent+2, all, "the new generation's vectors are made afresh")
	require.True(t, held, "before the erasure, the fact was embedded as it was")
	f.mu.Lock()
	for _, text := range f.texts[sent:] {
		require.NotContains(t, text, secret)
	}
	f.mu.Unlock()
	requireNowhere(t, dir, secret)

	// Another process's recall, between the erasure's journal and its
	// index: the old index, with the secret, is still there.
	s, ok, err = readMemSnapshot(context.Background(), dir)
	require.NoError(t, err)
	require.True(t, ok)
	for _, name := range []string{"index-" + s.gen + ".db", "vectors-" + s.gen + ".db"} {
		require.NoError(t, os.Remove(filepath.Join(dir, name)))
	}
	for _, name := range []string{"index-" + oldGen + ".db", "vectors-" + oldGen + ".db"} {
		b, err := os.ReadFile(filepath.Join(saved, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o600))
	}
	_, ok, err = readMemSnapshot(context.Background(), dir)
	require.NoError(t, err)
	require.False(t, ok, "recall reads the journal's generation only")
}

// newRecallHost is a host whose mem finds by meaning with f, and a Lazy, as
// gnosh's main process holds one, recalling from the same directory.
func newRecallHost(t *testing.T, f *fakeEmbedder) (*Host, *Lazy, func(code string)) {
	t.Helper()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	h.opts.Embeddings = f.URL + "/v1"
	l := NewLazy(ClientOptions{Options: Options{Dir: dir, Embeddings: f.URL + "/v1"}})
	t.Cleanup(func() { waitMemFills(t, memDir(dir, "/proj")) })
	return h, l, func(code string) {
		t.Helper()
		res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s0", Name: MemVM, Code: code})
		require.NoError(t, err, code)
		require.NoError(t, res.Err, code)
	}
}

// A prompt the user types brings what mem holds close to it in meaning, at
// most five items or facts, each once until the session's next summary;
// what the <mem> block names is not brought again. A prompt close to
// nothing brings nothing, and so does any prompt with the server down,
// within half a second.
func TestMemRecall(t *testing.T) {
	t.Parallel()
	f := newFakeEmbedder(t)
	_, l, mem := newRecallHost(t, f)
	ctx := context.Background()
	mem(`mem.Fact("cmd/test", "go test -race ./...")`)
	// The block shows the newest facts: these keep cmd/test out of it.
	for i := range 25 {
		mem(`mem.Fact("notes/n` + strconv.Itoa(i) + `", "a note on nothing much")`)
	}
	mem(`mem.Goal("ship the site").Steps("deploy to production")`)
	mem(`mem.Done("ship-site/1", "deployed with scripts/deploy.sh")`)
	dir := memDir(l.opts.Dir, "/proj")
	require.NoError(t, fillMemVectorsNow(ctx, dir, newMemEmbedder(f.URL+"/v1")))

	got := l.MemRecall(ctx, "/proj", "s1", "how do we deploy the site?")
	require.Equal(t, "<mem>\n"+
		"From your memory (mem), what may bear on what the user just wrote, found by meaning; `mem.Show(\"name\")` shows one in full:\n"+
		"- [x] `ship-site/1` \"deploy to production\" → deployed with scripts/deploy.sh\n"+
		"</mem>", got, "the goal itself is in the <mem> block the session is given")
	require.Empty(t, l.MemRecall(ctx, "/proj", "s1", "deploy it again"), "once until the next summary")
	require.Contains(t, l.MemRecall(ctx, "/proj", "s2", "deploy it again"), "`ship-site/1`", "another session")
	l.MemSummarized("s1")
	require.Contains(t, l.MemRecall(ctx, "/proj", "s1", "deploy it again"), "`ship-site/1`")

	require.Empty(t, l.MemRecall(ctx, "/proj", "s3", "write a haiku about autumn"))
	require.Contains(t, l.MemRecall(ctx, "/proj", "s4", "run the testing suite"), "- fact `cmd/test` = go test -race ./...\n")
	block := l.MemBlock("/proj", "s5")
	require.Contains(t, block, "`ship-site`")
	got = l.MemRecall(ctx, "/proj", "s5", "ship the site to production")
	require.Contains(t, got, "`ship-site/1`")
	require.NotContains(t, got, "`ship-site`", "the block named it")

	f.down.Store(true)
	start := time.Now()
	require.Empty(t, l.MemRecall(ctx, "/proj", "s6", "run the testing suite"))
	require.Less(t, time.Since(start), 2*time.Second)
	require.Empty(t, NewLazy(ClientOptions{Options: Options{Dir: l.opts.Dir}}).MemRecall(ctx, "/proj", "s7", "run the testing suite"),
		"no server set")
	f.down.Store(false)
	require.Empty(t, l.MemRecall(ctx, "/elsewhere", "s8", "run the testing suite"))
	_, err := os.Stat(memDir(l.opts.Dir, "/elsewhere"))
	require.ErrorIs(t, err, os.ErrNotExist, "a project with no mem gets none made")
	sent, _ := f.embedded("")
	l.MemRecall(ctx, "/elsewhere", "s8", "run the testing suite")
	again, _ := f.embedded("")
	require.Equal(t, sent, again, "nor is its prompt sent anywhere")
}

// A session the user comes back to is shown mem again when it changed after
// the session last did anything, and only then.
func TestMemChanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	l := NewLazy(ClientOptions{Options: Options{Dir: dir}})
	require.Empty(t, l.MemChanged("/proj", "s1", time.Time{}), "no mem")
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s2", Name: MemVM, Code: `mem.Fact("cmd/test", "go test ./...")`})
	require.NoError(t, err)
	require.NoError(t, res.Err)
	st, ok := readMemStatus(memDir(dir, "/proj"))
	require.True(t, ok)
	require.InDelta(t, testNow().Unix(), st.At, 60, "when mem changed, by the host's clock")

	require.Contains(t, l.MemChanged("/proj", "s1", time.Unix(st.At-60, 0)), "`cmd/test` = go test ./...", "changed since")
	require.Empty(t, l.MemChanged("/proj", "s1", time.Unix(st.At, 0)), "not since")
}

// /mem says whether mem finds by meaning too, and how much of it the
// embedding server has read; nothing, when there is no server.
func TestMemCommandSaysHowItFindsByMeaning(t *testing.T) {
	t.Parallel()
	f := newFakeEmbedder(t)
	h, l, mem := newRecallHost(t, f)
	ctx := context.Background()
	mem(`mem.Fact("cmd/test", "go test ./...")`)
	mem(`mem.Goal("ship the site")`)
	show := func(args string) string {
		t.Helper()
		out, _, err := h.memCommand(ctx, "/proj", "s1", args, nil)
		require.NoError(t, err)
		return out
	}
	require.Contains(t, show(""), "\nmem will find by meaning too once the embedding server has read it (0 of 2 entries so far)\n")
	require.NoError(t, fillMemVectorsNow(ctx, memDir(l.opts.Dir, "/proj"), newMemEmbedder(f.URL+"/v1")))
	require.Contains(t, show(""), "\nmem also finds by meaning, with fake-embed (all 2 entries read)\n")
	require.NotContains(t, show("cmd/test"), "by meaning", "only the outline says it")

	plain := newTestHost(t, l.opts.Dir)
	out, _, err := plain.memCommand(ctx, "/proj", "s1", "", nil)
	require.NoError(t, err)
	require.NotContains(t, out, "by meaning")
}

// A search is made whole, and by each of its sentences or clauses of three
// words or more, when it has two or more; never at a dot inside a path or
// a version.
func TestMemQueries(t *testing.T) {
	t.Parallel()
	q := "How do I push this to production, and who has to check it first?"
	require.Equal(t, []string{q, "How do I push this to production", "who has to check it first"}, memQueries(q))
	require.Equal(t, []string{"run the tests"}, memQueries("run the tests"))
	require.Equal(t, []string{"go test ./... then tag v2.0.0"}, memQueries("go test ./... then tag v2.0.0"))
	q = "Fix the build. It fails on CI since yesterday. ok"
	require.Equal(t, []string{q, "Fix the build", "It fails on CI since yesterday"}, memQueries(q))
	q = "one two three. four five six. seven eight nine. ten eleven twelve. more words here."
	require.Len(t, memQueries(q), 1+memQueryParts)
}

// TestMemRecallScale times what recall does with each typed prompt at the
// size GNOMEM_SCALE gives (rows: facts, with whole 1024-number vectors):
// reading every row and vector under mem's lock, and ranking them against
// a prompt of five clauses. It is skipped unless GNOMEM_SCALE is set; run it
// on the devbox.
func TestMemRecallScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("GNOMEM_SCALE"))
	if n == 0 {
		t.Skip("set GNOMEM_SCALE to a number of rows")
	}
	dir := t.TempDir()
	path, err := createMemJournal(filepath.Join(dir, "journal.jsonl"), time.Now())
	require.NoError(t, err)
	hdr, _, err := readJournalHeader(filepath.Join(dir, "journal.jsonl"))
	require.NoError(t, err)
	_ = path
	x, err := openMemIndex(filepath.Join(dir, "index-"+hdr.Generation+".db"))
	require.NoError(t, err)
	var ops []memOp
	for i := range n {
		ops = append(ops, memOp{Kind: "fact", Name: "group-" + strconv.Itoa(i%50) + "/fact-" + strconv.Itoa(i), New: "fact number " + strconv.Itoa(i) + " about something"})
	}
	require.NoError(t, x.apply([][]memOp{ops}, memStamp{Layout: memIndexLayout, Generation: hdr.Generation}))
	rows, err := x.rows()
	require.NoError(t, err)
	require.NoError(t, x.close())
	v, err := openMemVectors(memVectorsPath(dir, hdr.Generation))
	require.NoError(t, err)
	vec := func(seed int) []int8 {
		out := make([]int8, 1024)
		for i := range out {
			out[i] = int8((seed*31 + i*17) % 255)
		}
		return out
	}
	for start := 0; start < len(rows); start += 1000 {
		var hashes []string
		var vecs [][]int8
		for i := start; i < min(start+1000, len(rows)); i++ {
			hashes, vecs = append(hashes, rows[i].hash()), append(vecs, vec(i))
		}
		require.NoError(t, v.put("scale-model", hashes, vecs))
	}
	require.NoError(t, v.close())

	start := time.Now()
	s, ok, err := readMemSnapshot(context.Background(), dir)
	require.NoError(t, err)
	require.True(t, ok)
	read := time.Since(start)
	qs := [][]int8{vec(1), vec(2), vec(3), vec(4), vec(5)}
	start = time.Now()
	near := s.nearest(qs, "scale-model", memRecallScore, "item", "fact")
	t.Logf("%d rows: reading them and their vectors %v, ranking against 5 queries %v (%d over the score)", len(s.rows), read, time.Since(start), len(near))

	// Recall's own read, kept between prompts while mem has not changed.
	var l Lazy
	for _, what := range []string{"recall's first read", "recall's next read, mem unchanged"} {
		start = time.Now()
		s, ok, err := l.snapshot(context.Background(), dir)
		require.NoError(t, err)
		require.True(t, ok)
		read := time.Since(start)
		start = time.Now()
		s.nearest(qs, "scale-model", memRecallScore, "item", "fact")
		t.Logf("%s %v, ranking %v", what, read, time.Since(start))
	}
}

// Recall keeps what it read of mem between prompts, while mem has not
// changed: a fact changed is recalled as it is now, and a redacted one
// never again.
func TestMemRecallKeepsNothingStale(t *testing.T) {
	t.Parallel()
	f := newFakeEmbedder(t)
	_, l, mem := newRecallHost(t, f)
	ctx := context.Background()
	dir := memDir(l.opts.Dir, "/proj")
	e := newMemEmbedder(f.URL + "/v1")
	mem(`mem.Fact("cmd/test", "go test -race ./...")`)
	require.NoError(t, fillMemVectorsNow(ctx, dir, e))
	l.show("s1", []string{"-"}) // shown nothing of mem yet, the block aside
	require.Contains(t, l.MemRecall(ctx, "/proj", "s1", "run the testing suite"), "go test -race")

	mem(`mem.Fact("cmd/test", "make testing-check")`)
	require.NoError(t, fillMemVectorsNow(ctx, dir, e))
	l.show("s2", []string{"-"})
	got := l.MemRecall(ctx, "/proj", "s2", "run the testing suite")
	require.Contains(t, got, "make testing-check")
	require.NotContains(t, got, "go test -race")

	mem(`mem.Redact("testing-check")`)
	require.NoError(t, fillMemVectorsNow(ctx, dir, e))
	l.show("s3", []string{"-"})
	require.NotContains(t, l.MemRecall(ctx, "/proj", "s3", "run the testing suite"), "testing-check")
}

// Recall's kept read is read again after any write to the vectors, even
// one that leaves as many as there were: a fill that adds a changed fact's
// vector and prunes the old text's.
func TestMemRecallSeesAFillThatKeepsTheCount(t *testing.T) {
	t.Parallel()
	f := newFakeEmbedder(t)
	_, l, mem := newRecallHost(t, f)
	ctx := context.Background()
	dir := memDir(l.opts.Dir, "/proj")
	e := newMemEmbedder(f.URL + "/v1")
	mem(`mem.Fact("cmd/test", "go test -race ./...")`)
	require.NoError(t, fillMemVectorsNow(ctx, dir, e))
	mem(`mem.Fact("cmd/test", "make testing-check")`)
	// A recall before the fill: the new text has no vector yet, and the
	// read is kept. It starts a fill, which is let finish.
	l.show("s1", []string{"-"})
	require.Empty(t, l.MemRecall(ctx, "/proj", "s1", "run the testing suite"))
	waitMemFills(t, dir)
	require.NoError(t, fillMemVectorsNow(ctx, dir, e))
	n, _ := memVectorCount(t, dir)
	require.Equal(t, 1, n, "as many vectors as before: the new text's in, the old one's out")
	l.show("s2", []string{"-"})
	require.Contains(t, l.MemRecall(ctx, "/proj", "s2", "run the testing suite"), "make testing-check")
}

// A text the server refuses (too long for its batch, say) is sent again
// cut, and one it still refuses is passed over: it does not keep every row
// after it from its vector, nor the fill from ending.
func TestMemFillPassesOverARefusedText(t *testing.T) {
	t.Parallel()
	f := newFakeEmbedder(t)
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &in)
		for _, text := range in.Input {
			if strings.Contains(text, "unembeddable") {
				http.Error(w, "input is too large to process", http.StatusInternalServerError)
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
		f.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(refuse.Close)
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	h.embed = newMemEmbedder(refuse.URL + "/v1")
	memOK(t, h, `mem.Fact("logs/crash", "unembeddable stack trace")`)
	memOK(t, h, `mem.Fact("cmd/test", "go test ./...")`)
	memOK(t, h, `mem.Fact("cmd/build", "make build")`)
	require.NoError(t, fillMemVectorsNow(context.Background(), dir, h.embed))
	n, _ := memVectorCount(t, dir)
	require.Equal(t, 2, n, "every row but the refused one")
}
