// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	// Another klash process finds it, through the journal and the store.
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

// An entry in the journal that the store lacks, as when klash died between
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
// klash wrote is refused.
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
	require.Contains(t, err.Error(), "a newer klash wrote mem")
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

// Two klash processes take turns through mem's lock, and every input is
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
