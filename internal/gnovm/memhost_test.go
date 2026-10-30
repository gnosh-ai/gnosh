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

	_, _, err := h.New(ctx, NewRequest{Project: "/proj", Name: MemVM})
	require.ErrorContains(t, err, "mem is your memory")
	require.ErrorContains(t, h.Drop(ctx, "/proj", MemVM, "delete"), "never dropped")

	list, err := h.List("/proj")
	require.NoError(t, err)
	require.Len(t, list.VMs, 1)
	require.Equal(t, MemVM, list.VMs[0].Name)
	require.EqualValues(t, 1, list.VMs[0].Entries)
	kept, err := h.Kept("/proj", MemVM, `req-mem.Goal("ship it").Steps("build", "test")`)
	require.NoError(t, err)
	require.True(t, kept)

	block := MemBlock(dir, "/proj")
	require.Contains(t, block, "<mem>\n")
	require.Contains(t, block, "- `ship` \"ship it\" (0/2 done · ")
	require.Contains(t, block, "next: `ship/1` \"build\"")
	require.Empty(t, MemBlock(dir, "/elsewhere"))
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
