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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTestMemHost is a mem in dir, on a clock that moves a minute per call.
func newTestMemHost(t *testing.T, dir string) *memHost {
	h := newMemHost(newBase(), dir)
	var mu sync.Mutex
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	h.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Minute)
		return now
	}
	return h
}

func memOK(t *testing.T, h *memHost, code string) memResult {
	t.Helper()
	res, err := h.call(context.Background(), memRequest{Code: code, Session: "s1"})
	require.NoError(t, err, res.Output)
	return res
}

// memStoreStamp reads the stamp of mem's only store in dir.
func memStoreStamp(t *testing.T, dir string) memStamp {
	t.Helper()
	stores, err := filepath.Glob(filepath.Join(dir, "store-*"))
	require.NoError(t, err)
	require.Len(t, stores, 1)
	r, err := openMemRealm(newBase(), stores[0])
	require.NoError(t, err)
	defer r.close()
	st, ok, err := r.stamp()
	require.NoError(t, err)
	require.True(t, ok)
	return st
}

func TestMemHostKeepsWhatChanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	res := memOK(t, h, `mem.Goal("ship it").Steps("build", "test")`)
	require.EqualValues(t, 1, res.Seq)
	require.Len(t, res.Ops, 3)
	require.Contains(t, res.Output, "added goal `ship`")

	// A read keeps nothing.
	res = memOK(t, h, `println(mem.Show())`)
	require.Zero(t, res.Seq)
	require.Contains(t, res.Output, "`ship/1` \"build\"")

	// A failed input keeps nothing, though it printed a receipt first.
	res, err := h.call(context.Background(), memRequest{Code: `mem.Goal("half"); panic("no")`, Session: "s1"})
	require.ErrorContains(t, err, "nothing was kept")
	require.Zero(t, res.Seq)

	// Another gnosh process finds it, through the journal and the store.
	other := newTestMemHost(t, dir)
	res, err = other.call(context.Background(), memRequest{Code: `println(mem.Show())`, User: true})
	require.NoError(t, err)
	require.Contains(t, res.Output, "`ship`")
	require.NotContains(t, res.Output, "half")
	require.Empty(t, res.Notes)

	j, entries, torn, err := readMemJournal(filepath.Join(dir, "journal.jsonl"))
	require.NoError(t, err)
	require.False(t, torn)
	require.NotEmpty(t, j.instance)
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, "agent", e.Who)
	require.Equal(t, "s1", e.Session)
	require.Equal(t, `mem.Goal("ship it").Steps("build", "test")`, e.Code)
	require.Equal(t, memOp{Kind: "add", Name: "ship", New: "ship it", By: "s1"}, e.Ops[0])
	st := memStoreStamp(t, dir)
	require.Equal(t, memStamp{Layout: memLayout, Gno: stdlibVersion(), Generation: j.generation, Seq: 1, End: j.size, Hash: j.lastHash}, st)
}

// An entry in the journal that the store lacks, as when gnosh died between
// journaling a call and writing its batch, or another process journaled it,
// is applied at the next call.
func TestMemHostCatchesUp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	memOK(t, h, `mem.Goal("first")`)
	path := filepath.Join(dir, "journal.jsonl")
	j, _, _, err := readMemJournal(path)
	require.NoError(t, err)
	require.NoError(t, j.append(&memEntry{Seq: 2, Time: 1793350000, Who: "agent", Code: `mem.Goal("second")`, Ops: []memOp{{Kind: "add", Name: "second", New: "second", By: "s2"}}}))

	res := memOK(t, h, `println(mem.Show())`)
	require.Equal(t, []string{"mem's store caught up with 1 journal entry"}, res.Notes)
	require.Contains(t, res.Output, "`second` \"second\"")
	require.EqualValues(t, 2, memStoreStamp(t, dir).Seq)

	// The next input is entry 3.
	require.EqualValues(t, 3, memOK(t, h, `mem.Goal("third")`).Seq)
}

// A store of an older layout is rebuilt from the journal; one a newer
// gnosh wrote is refused.
func TestMemHostRebuildsAnOlderStore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	memOK(t, h, `mem.Goal("keep me").Steps("one")`)
	memOK(t, h, `mem.Done("keep-me/1", "ok")`)
	before := memOK(t, h, `println(mem.JSON("keep-me"))`).Output

	setStamp := func(edit func(*memStamp)) {
		stores, _ := filepath.Glob(filepath.Join(dir, "store-*"))
		r, err := openMemRealm(newBase(), stores[0])
		require.NoError(t, err)
		st, _, err := r.stamp()
		require.NoError(t, err)
		edit(&st)
		b, err := json.Marshal(st)
		require.NoError(t, err)
		r.kv.Set(nil, memStampKey, b)
		require.NoError(t, r.close())
	}

	setStamp(func(st *memStamp) { st.Layout = 0 })
	res := memOK(t, h, `println(mem.JSON("keep-me"))`)
	require.Equal(t, []string{"mem's store was rebuilt from its journal"}, res.Notes)
	require.Equal(t, before, res.Output)

	// A build on another gno version, which may store objects otherwise.
	setStamp(func(st *memStamp) { st.Gno = "v0.0.0-other" })
	res = memOK(t, h, `println(mem.JSON("keep-me"))`)
	require.Equal(t, []string{"mem's store was rebuilt from its journal"}, res.Notes)
	require.Equal(t, before, res.Output)

	setStamp(func(st *memStamp) { st.Layout = memLayout + 1 })
	_, err := h.call(context.Background(), memRequest{Code: `println(mem.Show())`})
	var ref *refusal
	require.ErrorAs(t, err, &ref)
	require.Contains(t, err.Error(), "a newer gnosh wrote mem")
}

// A catch-up cut short keeps every batch it applied, so a rebuild of a
// long journal goes on across calls, each saying to call again. (Not
// parallel: it shortens memBatchOps.)
func TestMemHostResumesACutCatchUp(t *testing.T) {
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	for i := 1; i <= 10; i++ {
		memOK(t, h, fmt.Sprintf(`mem.Goal("goal %d")`, i))
	}
	stores, _ := filepath.Glob(filepath.Join(dir, "store-*"))
	require.NoError(t, os.RemoveAll(stores[0]))

	defer func(n int) { memBatchOps = n }(memBatchOps)
	memBatchOps = 3
	h.slice = 0
	busy := 0
	for {
		res, err := h.call(context.Background(), memRequest{Code: `println(mem.Show())`})
		var b *busyError
		if errors.As(err, &b) {
			require.Contains(t, err.Error(), "call again")
			busy++
			require.Less(t, busy, 10)
			continue
		}
		require.NoError(t, err)
		for i := 1; i <= 10; i++ {
			require.Contains(t, res.Output, fmt.Sprintf("`goal-%d`", i))
		}
		break
	}
	require.Equal(t, 3, busy, "a batch of 3 entries per call, then the last 1")
}

// The end of an append that did not finish is cut at the next call.
func TestMemHostCutsATornTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	memOK(t, h, `mem.Goal("whole")`)
	path := filepath.Join(dir, "journal.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	// Longer than the next entry, which would not cover it.
	_, err = f.WriteString(`{"seq":2,"time":17933` + strings.Repeat("x", 2000))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.EqualValues(t, 2, memOK(t, h, `mem.Goal("after")`).Seq)
	_, entries, torn, err := readMemJournal(path)
	require.NoError(t, err)
	require.False(t, torn)
	require.Len(t, entries, 2)
}

// mem.Find searches the index, which follows every change: titles, notes,
// facts; a forgotten item leaves it. Deleted, it is built again from the
// journal.
func TestMemHostFinds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	memOK(t, h, `mem.Goal("fix the CI").Steps("TestFoo flakes under -race")`)
	memOK(t, h, `mem.Note("fix-ci/1", "only on the AI box")`)
	memOK(t, h, `mem.Fact("cmd/test", "go test -race ./...")`)
	find := func(words string) string {
		t.Helper()
		return memOK(t, h, `print(mem.Find("`+words+`"))`).Output
	}
	require.Contains(t, find("flak"), "`fix-ci/1`")
	out := find("race")
	require.Contains(t, out, "`fix-ci/1`")
	require.Contains(t, out, "fact `cmd/test` = go test -race ./...")
	require.Contains(t, find("box"), "`fix-ci/1`")
	require.Contains(t, find("nothing here"), "nothing in mem matches")

	memOK(t, h, `mem.Drop("fix-ci", "moved")`)
	memOK(t, h, `mem.Forget("fix-ci")`)
	require.Contains(t, find("flak"), "nothing in mem matches")

	idx, err := filepath.Glob(filepath.Join(dir, "index-*"))
	require.NoError(t, err)
	require.NotEmpty(t, idx)
	for _, p := range idx {
		require.NoError(t, os.Remove(p))
	}
	require.Contains(t, find("race"), "fact `cmd/test`")
}

// requireNowhere fails the test if a file under dir holds text in any form
// a file there would hold it: as typed, Go-quoted, or JSON-encoded either
// way.
func requireNowhere(t *testing.T, dir, text string) {
	t.Helper()
	forms := []string{text, quotedInner(text)}
	for _, f := range forms[:2] {
		b, err := json.Marshal(f)
		require.NoError(t, err)
		forms = append(forms, string(b[1:len(b)-1]))
	}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, f := range forms {
			if bytes.Contains(b, []byte(f)) {
				t.Errorf("%s holds %q", p, f)
			}
		}
		return nil
	}))
}

// Redact takes a secret out of every file of mem, with its quote, its
// backslash and its newline: the journal is written again without it, and
// the store and the index are built afresh.
func TestMemHostRedacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	secret := "k3y\"\\x\nz9q"
	lit := strconv.Quote(secret)
	memOK(t, h, `mem.Goal("rotate the token")`)
	memOK(t, h, `mem.Note("rotate-token", "it was " + `+lit+`)`)
	memOK(t, h, `mem.Fact("tokens/old", "the old token: " + `+lit+`)`)
	// Held in an input's code alone.
	memOK(t, h, `token := `+lit+`; _ = token; mem.Goal("other work")`)
	res, err := h.call(context.Background(), memRequest{Code: `mem.Redact(` + lit + `)`, User: true})
	require.NoError(t, err, res.Output)
	require.Contains(t, res.Output, "redacted it in 2 places")
	requireNowhere(t, dir, secret)

	out := memOK(t, h, `print(mem.Check()); println(mem.Show("rotate-token")); println(mem.Show("tokens/old"))`).Output
	require.Contains(t, out, "note 1: it was (redacted)")
	require.Contains(t, out, "the old token: (redacted)")
}

// An erasure that crashed after writing the journal again, before it
// deleted the store and the index that hold the text, is finished by the
// next call: they are of the old generation, which the next open deletes.
func TestMemHostFinishesACrashedErasure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	const secret = "sk-crash-7f3e91"
	memOK(t, h, `mem.Goal("rotate the key")`)
	memOK(t, h, `mem.Note("rotate-key", "the old key was `+secret+`")`)
	// copyPath copies a store's directory or an index's file.
	copyPath := func(from, to string) {
		t.Helper()
		fi, err := os.Stat(from)
		require.NoError(t, err)
		if fi.IsDir() {
			require.NoError(t, os.CopyFS(to, os.DirFS(from)))
			return
		}
		b, err := os.ReadFile(from)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(to, b, 0o600))
	}
	saved := t.TempDir()
	stores, err := filepath.Glob(filepath.Join(dir, "store-*"))
	require.NoError(t, err)
	indexes, err := filepath.Glob(filepath.Join(dir, "index-*"))
	require.NoError(t, err)
	old := append(stores, indexes...)
	require.Len(t, stores, 1)
	require.NotEmpty(t, indexes)
	for _, p := range old {
		copyPath(p, filepath.Join(saved, filepath.Base(p)))
	}

	res, err := h.call(context.Background(), memRequest{Code: `mem.Redact("` + secret + `")`, User: true})
	require.NoError(t, err, res.Output)
	requireNowhere(t, dir, secret)
	// The crash: the old generation's store and index are back.
	for _, p := range old {
		copyPath(filepath.Join(saved, filepath.Base(p)), p)
	}
	held := false
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		held = held || bytes.Contains(b, []byte(secret))
		return err
	}))
	require.True(t, held, "the old generation's files hold the secret again")

	require.Contains(t, memOK(t, h, `mem.Show("rotate-key")`).Output, "note 1: the old key was (redacted)")
	requireNowhere(t, dir, secret)
}

// Forget takes a goal's text, and its name, out of every file of mem; the
// name is free again.
func TestMemHostForgets(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	memOK(t, h, `mem.Goal("plan the surprise party").Steps("book the venue", "invite everyone")`)
	memOK(t, h, `mem.Note("plan-surprise-party/1", "the venue is Moonlight Hall")`)
	memOK(t, h, `mem.Done("plan-surprise-party/1", "booked Moonlight Hall")`)
	memOK(t, h, `mem.Goal("buy milk")`)
	memOK(t, h, `mem.Drop("plan-surprise-party", "called off")`)
	res := memOK(t, h, `mem.Forget("plan-surprise-party")`)
	require.Equal(t, map[string]string{"plan-surprise-party": "forgotten-1"}, res.Renamed)
	for _, s := range []string{"surprise", "venue", "Moonlight", "invite everyone", "called off"} {
		requireNowhere(t, dir, s)
	}

	out := memOK(t, h, `print(mem.Check()); println(mem.Show())`).Output
	require.Contains(t, out, "`buy-milk` \"buy milk\"")
	require.NotContains(t, out, "forgotten-1")
	require.Contains(t, memOK(t, h, `mem.Goal("plan the surprise party")`).Output, "added goal `plan-surprise-party`")
}

// A fact forgotten right after it was set leaves entries with no Ops,
// which a store built afresh steps past: it used to have nothing to commit
// its stamp with, and the worker panicked.
func TestMemHostForgetsAFactAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	memOK(t, h, `mem.Fact("phone/alice", "555-0142")`)
	res, err := h.call(context.Background(), memRequest{Code: `mem.Forget("phone/alice")`, User: true})
	require.NoError(t, err, res.Output)
	require.Contains(t, res.Output, "forgot fact `phone/alice`")
	requireNowhere(t, dir, "555-0142")
	require.Contains(t, memOK(t, h, `mem.Show()`).Output, "mem is empty")
	_, err = h.repair(context.Background())
	require.NoError(t, err)
	require.Contains(t, memOK(t, h, `print(mem.Check()); println(mem.Show())`).Output, "mem is empty")
}

// Two gnosh processes take turns through mem's lock, and every input is
// kept once, in one order.
func TestMemHostTwoProcessesTakeTurns(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	hosts := []*memHost{newTestMemHost(t, dir), newTestMemHost(t, dir)}
	var wg sync.WaitGroup
	for p, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 1; i <= 5; i++ {
				_, err := h.call(context.Background(), memRequest{Code: fmt.Sprintf(`mem.Goal("from %d number %d")`, p, i), Session: "s"})
				require.NoError(t, err)
			}
		}()
	}
	wg.Wait()
	res := memOK(t, hosts[0], `print(mem.Check()); println(mem.Show())`)
	require.Equal(t, 10, strings.Count(res.Output, `"from `), res.Output)
	_, entries, _, err := readMemJournal(filepath.Join(dir, "journal.jsonl"))
	require.NoError(t, err)
	require.Len(t, entries, 10)
}

// gnovm_run name "mem" reaches mem through the host: a change is kept, a
// trailing read is shown, an older mem's habit gets a hint, gnovm_new and
// gnovm_drop refuse mem, gnovm_list and Kept know it, and a new session's
// block says what it holds.
func TestHostRunsMem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	ctx := context.Background()
	call := func(code string) *RunResult {
		t.Helper()
		res, err := h.Run(ctx, RunRequest{Project: "/proj", Session: "s1", Name: MemVM, Code: code, Req: "req-" + code})
		require.NoError(t, err, code)
		return res
	}
	res := call(`mem.Goal("ship it").Steps("build", "test")`)
	require.NoError(t, res.Err)
	require.True(t, res.Journaled)
	require.EqualValues(t, 1, res.Seq)
	require.Contains(t, res.Output, "added goal `ship` \"ship it\"")

	res = call(`mem.Show()`)
	require.NoError(t, res.Err)
	require.False(t, res.Journaled)
	require.Contains(t, res.Output, "[ ] `ship/1` \"build\"  ← next")

	res = call(`mem.Add("x", "y")`)
	require.ErrorContains(t, res.Err, "missing field Add in *klash/gnomem/v2.Mem")
	require.Contains(t, res.Notes, `mem.Goal("what to get done").Steps("a step", "another") plans a task, and names its steps after it`)
	res = call(`mem.Done(1, "built")`)
	require.Contains(t, res.Notes, `items are named, not numbered: type the name mem printed in backticks, as mem.Done("release-v2/2", "…")`)
	res = call(`mem.Done(ship/1, "built")`)
	require.Contains(t, res.Notes, `a name is a string: quote it, as mem.Done("release-v2/1", "…")`)
	// The real-model pass's slips: a call chained off one that returns
	// nothing, a Goal where a name goes, mem.List, and mem imported.
	res = call(`mem.Fact("staging-db", "db-stg-7").Fact("release-manager", "Priya")`)
	require.EqualError(t, res.Err, "only mem.Goal(…) chains (.Steps, .Wait, .Ask): mem's other calls return nothing, so put each on its own line")
	res = call("g := mem.Goal(\"count the funcs\")\nmem.Note(g, \"b.go has 5\")")
	require.Contains(t, res.Notes, "mem.Goal(…) gives a Goal, not a name: pass its name, g.Name, or the name mem printed in backticks")
	res = call(`mem.List("ship")`)
	require.Contains(t, res.Notes, `mem.Show() lists what mem holds, and mem.Show("name") shows a goal with its steps`)
	// From a launch-video take: a plan begun in one call, a step guessed
	// for another goal's, and calls joined with commas, as a list.
	res = call(`mem.Goal("add Median to stats").Steps("implement Median", "add tests").Start()`)
	require.NoError(t, res.Err)
	require.Contains(t, res.Output, "started `add-median-stats/1` \"implement Median\"")
	res = call(`mem.Goal("tidy the tests").Steps("a", "b").Start("tests/1")`)
	require.ErrorContains(t, res.Err, "`tests/1` is not a step of `tidy-tests`, whose steps are `tidy-tests/1` to `tidy-tests/2`: .Start() with no name starts the next")
	res = call("mem.Done(\"add-median-stats/1\", \"done\"),\nmem.Done(\"add-median-stats/2\", \"done\")")
	require.ErrorContains(t, res.Err, "(mem's calls go one per line, with no comma after them)")
	res = call(`mem.Fact("release/qa-signoff")`)
	require.EqualError(t, res.Err, "mem.Fact takes 2 arguments, not 1: mem.Fact(name, text) sets a fact, and mem.Show(name) shows one")
	// From the QA's release-waits-for-signoff on the box's Qwen: a step
	// marked done on the plan, a question given a name, a name made from a
	// title by hand. What a call takes, and what a Goal has, are read from
	// gnomem's source (memAPI).
	res = call(`mem.Goal("release 1.3.0").Steps("tag it", "sign off").Done("x")`)
	require.ErrorContains(t, res.Err, "missing field Done in klash/gnomem/v2.Goal")
	require.Contains(t, res.Notes, "a Goal, which mem.Goal(…) gives, has Steps, Wait, Start and Ask; Done is mem's: mem.Done(name, result...)")
	res = call(`mem.Goal("release 1.3.0").Steps("tag it").Ask("release-1-3-0/1", "who signs?")`)
	require.EqualError(t, res.Err, "a Goal's Ask takes 1 argument, not 2: mem.Goal(…).Ask(question), or mem.Ask(name, question) for an item by its name")
	res = call("mem.Goal(\"release 1.3.0\").Steps(\"tag it\")\nmem.Done(\"release-1.3.0/1\", \"tagged\")")
	require.ErrorContains(t, res.Err, "no item `release-1.3.0/1` (a name joins its words with \"-\": did you mean `release-1-3-0/1`?); nothing was kept")
	res = call(`mem.Drop("ship")`)
	require.EqualError(t, res.Err, "mem.Drop takes 2 arguments, not 1: mem.Drop(name, why)")
	res = call(`mem.Frobnicate("x")`)
	require.Contains(t, res.Notes, "mem's calls are Goal, Steps, Ask, Start, Done, Drop, Reopen, Note, Wait, Fact, Rule, Forget, Redact, JSON, Next, Show and Find")
	res = call("import \"mem\"\nmem.Fact(\"cmd/test\", \"go test ./...\")")
	require.NoError(t, res.Err)
	require.Equal(t, "fact `cmd/test` is new\n", res.Output)
	res = call("mem.Goal(\"x\").Steps(\n\t\"a\",\n\t\"b\"\n)")
	require.ErrorContains(t, res.Err, "(a call over several lines needs a comma after its last argument too, as in Go)")
	require.Contains(t, call(`mem.Find("push to production")`).Output, `nothing in mem matches "push to production": Find matches the words as they were stored; mem.Show() lists 2 goals, 0 rules and 1 fact`)
	res = call("mem.Fact(\"cmd/deploy\", \"make ship ENV=prod\")\nmem.Note(\"qa\", \"Lin does QA\")")
	require.EqualError(t, res.Err, "no item `qa`; mem.Show() lists them; nothing was kept, so fact `cmd/deploy`, set above, is not in mem")

	_, _, err := h.New(ctx, NewRequest{Project: "/proj", Name: MemVM})
	require.ErrorContains(t, err, "mem is your memory")
	require.ErrorContains(t, h.Drop(ctx, "/proj", MemVM, "delete"), "never dropped")

	list, err := h.List("/proj")
	require.NoError(t, err)
	require.Len(t, list.VMs, 1)
	require.Equal(t, MemVM, list.VMs[0].Name)
	require.EqualValues(t, 3, list.VMs[0].Entries)
	kept, err := h.Kept("/proj", MemVM, `req-mem.Goal("ship it").Steps("build", "test")`)
	require.NoError(t, err)
	require.True(t, kept)

	block := MemBlock(dir, "/proj")
	require.Contains(t, block, "<mem>\n")
	require.Contains(t, block, "- `ship` \"ship it\" (0/2 done · ")
	require.Contains(t, block, "next: `ship/1` \"build\"")
	require.Empty(t, MemBlock(dir, "/elsewhere"))
}

// A failed input's refusal names what its receipts said it added and set.
func TestMemNotKept(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		ops  []memOp
		want string
	}{
		{nil, ""},
		{[]memOp{{Kind: "note", Name: "a", New: "x"}}, ""},
		{[]memOp{{Kind: "add", Name: "ship"}}, ", so `ship`, added above, is not in mem"},
		{[]memOp{{Kind: "fact", Name: "cmd/deploy", New: "make ship"}}, ", so fact `cmd/deploy`, set above, is not in mem"},
		{[]memOp{{Kind: "fact", Name: "cmd/old", Old: "x"}}, ""},
		{[]memOp{{Kind: "fact", Name: "a", New: "1"}, {Kind: "fact", Name: "a", New: "2"}, {Kind: "rule", Name: "b", New: "3"}}, ", so fact `a` and rule `b`, set above, are not in mem"},
		{[]memOp{{Kind: "add", Name: "t"}, {Kind: "add", Name: "t/1"}, {Kind: "add", Name: "t/2"}, {Kind: "add", Name: "t/3"}}, ", so `t`, `t/1`, `t/2` and the rest, added above, are not in mem"},
		{[]memOp{{Kind: "add", Name: "t"}, {Kind: "fact", Name: "cmd/test", New: "go test"}}, ", so `t` (added above) and fact `cmd/test` (set above) are not in mem"},
	} {
		require.Equal(t, tc.want, memNotKept(tc.ops))
	}
}

// Show lists the newest facts, and the rest by their groups, so a model
// looking for a deploy command among many facts finds `cmd/` in one look;
// a Find that matches nothing says exactly that.
func TestMemShowGroupsHiddenFacts(t *testing.T) {
	t.Parallel()
	h := newTestMemHost(t, t.TempDir())
	for _, name := range []string{"cmd/deploy", "infra/a", "infra/b", "people/lin", "staging-db"} {
		memOK(t, h, `mem.Fact("`+name+`", "x")`)
	}
	for i := 1; i <= 10; i++ {
		memOK(t, h, fmt.Sprintf(`mem.Fact("notes/n%02d", "y")`, i))
	}
	out := memOK(t, h, `mem.Show()`).Output
	require.Contains(t, out, "  `notes/n01` = y\n")
	require.NotContains(t, out, "`cmd/deploy` =")
	require.Contains(t, out, "  … 5 more facts, in `cmd/` (1), `infra/` (2), `people/` (1), `staging-db`: mem.Show(\"cmd\") lists a group\n")
	require.Contains(t, memOK(t, h, `mem.Show("infra")`).Output, "under `infra/`:\n  `infra/a` = x\n  `infra/b` = x\n")
	require.Equal(t, "nothing in mem matches \"production\": Find matches the words as they were stored; mem.Show() lists 0 goals, 0 rules and the newest 10 of 15 facts, with the groups of the rest\n",
		memOK(t, h, `mem.Find("production")`).Output)
}

// A repository's worktrees and subdirectories share one mem: it is keyed
// by the git common dir.
func TestMemRootIsTheRepository(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "wt"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "worktrees", "wt", "commondir"), []byte("../..\n"), 0o644))
	wt := filepath.Join(t.TempDir(), "wt")
	require.NoError(t, os.MkdirAll(filepath.Join(wt, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "pkg", "deep"), 0o755))

	want, err := filepath.EvalSymlinks(filepath.Join(repo, ".git"))
	require.NoError(t, err)
	for _, p := range []string{repo, filepath.Join(repo, "pkg", "deep"), wt, filepath.Join(wt, "sub")} {
		require.Equal(t, want, memRoot(p), p)
	}
	outside := t.TempDir()
	got, err := filepath.EvalSymlinks(outside)
	require.NoError(t, err)
	require.Equal(t, got, memRoot(outside), "outside git, the directory itself")
}

// The <mem> block a new session is given, from status.json: the user's
// rules in full, first; then what is due on the session's day, though
// status.json was written days before; what waits on the user; the open
// goals with their next; and the newest facts. An empty mem gives none.
func TestMemBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	_, ok := readMemStatus(dir)
	require.False(t, ok)
	for _, q := range []memRequest{
		{Code: `mem.Rule("push-after-landing", "push to GitHub only after landed:")`, User: true},
		{Code: `mem.Goal("release v2").Steps("bump the version", "tag v2.0.0")`, Session: "s1"},
		{Code: `mem.Ask("release-v2/2", "which key signs the <tag>?")`, Session: "s1"},
		{Code: `mem.Goal("renew the TLS cert").Wait("2026-12-01")`, Session: "s1"},
		{Code: `mem.Fact("cmd/test", "go test -race ./...")`, Session: "s1"},
	} {
		res, err := h.call(context.Background(), q)
		require.NoError(t, err, res.Output)
	}
	st, ok := readMemStatus(dir)
	require.True(t, ok)
	require.EqualValues(t, 5, st.Seq)
	require.Equal(t, "2026-10-30", st.Day)

	block := memBlock(st, time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC))
	require.Equal(t, "<mem>\n"+
		"Rules the user gave you; follow them (the user's words today come first):\n"+
		"- `push-after-landing` (set by the user, 2026-10-30): push to GitHub only after landed:\n"+
		"From your memory (mem), kept by gnosh for this project: what you found and were told in earlier sessions. `mem.Show()` shows the outline; `mem.Find(\"words\")` searches everything.\n"+
		"mem: 5 items open · 1 question for the user · 1 fact\n"+
		"due: `renew-tls-cert` \"renew the TLS cert\" (since 2026-12-01)\n"+
		"waiting on the user: `release-v2/2/1` \"which key signs the &lt;tag&gt;?\"\n"+
		"open goals, newest first:\n"+
		"- `renew-tls-cert` \"renew the TLS cert\" (32d ago) due since 2026-12-01\n"+
		"- `release-v2` \"release v2\" (0/2 done · 32d ago) next: `release-v2/1` \"bump the version\"\n"+
		"facts, newest first: `cmd/test` = go test -race ./...\n"+
		"</mem>", block)
}

// The user is told at a session's start what in mem is due that day and
// what waits on them; nothing, when nothing does.
func TestMemNotice(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	for _, code := range []string{
		`mem.Goal("renew the TLS cert").Wait("2026-12-01")`,
		`mem.Goal("plan the release").Ask("which day suits you?")`,
		`mem.Fact("cmd/test", "go test ./...")`,
	} {
		res, err := h.call(context.Background(), memRequest{Code: code, Session: "s1"})
		require.NoError(t, err, res.Output)
	}
	st, ok := readMemStatus(dir)
	require.True(t, ok)
	require.Equal(t, `mem waiting on you: "which day suits you?" (plan-release/1)`,
		memNotice(st, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)))
	require.Equal(t, `mem due: "renew the TLS cert" (renew-tls-cert) (since 2026-12-01) · waiting on you: "which day suits you?" (plan-release/1)`,
		memNotice(st, time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC)))

	res, err := h.call(context.Background(), memRequest{Code: `mem.Done("plan-release/1", "Tuesday")`, User: true})
	require.NoError(t, err, res.Output)
	st, _ = readMemStatus(dir)
	require.Empty(t, memNotice(st, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)), "nothing due, nothing waiting")
}
