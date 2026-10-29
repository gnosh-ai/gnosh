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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func entryLine(t *testing.T, seq int64, stmts string) string {
	t.Helper()
	b, err := json.Marshal(entry{Seq: seq, Time: 1, WallNS: 1000, Ops: []op{{Stmts: stmts}}})
	require.NoError(t, err)
	return string(b) + "\n"
}

func header(t *testing.T, gen string) string {
	t.Helper()
	b, err := json.Marshal(journalHeader{Version: journalVersion, Generation: gen, Gno: stdlibVersion()})
	require.NoError(t, err)
	return string(b) + "\n"
}

func TestParseJournal(t *testing.T) {
	t.Parallel()
	good := header(t, "g1") + entryLine(t, 1, "a := 1") + entryLine(t, 2, "a++")

	j, entries, torn, err := parseJournal("p", []byte(good))
	require.NoError(t, err)
	require.False(t, torn)
	require.Len(t, entries, 2)
	require.EqualValues(t, 2, j.seq)
	require.EqualValues(t, len(good), j.size)
	require.Equal(t, "g1", j.generation)

	// A torn last line is reported, and the journal ends before it.
	j, entries, torn, err = parseJournal("p", []byte(good+`{"seq":3,"ti`))
	require.NoError(t, err)
	require.True(t, torn)
	require.Len(t, entries, 2)
	require.EqualValues(t, len(good), j.size)

	// A bad line in the middle, a gap in seq, a bad header, another format:
	// broken.
	for name, data := range map[string]string{
		"corrupt middle": header(t, "g1") + entryLine(t, 1, "a := 1") + "not json\n" + entryLine(t, 3, "a++"),
		"seq gap":        header(t, "g1") + entryLine(t, 1, "a := 1") + entryLine(t, 3, "a++"),
		"bad header":     "{}\n" + entryLine(t, 1, "a := 1"),
		"other format":   `{"klash_gnovm_journal":99,"generation":"g"}` + "\n",
		"empty":          "",
	} {
		_, _, _, err := parseJournal("p", []byte(data))
		require.ErrorIs(t, err, errBrokenJournal, name)
	}
}

func TestJournalNoticesAppendAndRewrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := createJournal(path, time.Unix(0, 0))
	require.NoError(t, err)
	require.NoError(t, j.append(&entry{Seq: 1, Ops: []op{{Stmts: "a := 1"}}}))
	appended, rewritten, err := j.changed()
	require.NoError(t, err)
	require.False(t, appended)
	require.False(t, rewritten)

	// Another process appends.
	other, _, _, err := readJournal(path)
	require.NoError(t, err)
	require.NoError(t, other.append(&entry{Seq: 2, Ops: []op{{Stmts: "a++"}}}))
	appended, rewritten, err = j.changed()
	require.NoError(t, err)
	require.True(t, appended)
	require.False(t, rewritten)
	tail, torn, err := j.readFrom()
	require.NoError(t, err)
	require.False(t, torn)
	require.Len(t, tail, 1)
	j.saw(tail[0])
	appended, _, err = j.changed()
	require.NoError(t, err)
	require.False(t, appended)

	// Another process rewrites it (a repair: a new generation).
	_, err = createJournal(path, time.Unix(0, 0))
	require.NoError(t, err)
	_, rewritten, err = j.changed()
	require.NoError(t, err)
	require.True(t, rewritten)
}

func TestRepairCutsAtTheFirstBadEntry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	persist := true
	_, _, err := h.New(context.Background(), NewRequest{Project: "/proj", Name: "fixme", Persist: &persist})
	require.NoError(t, err)
	run(t, h, "fixme", `a := 1`)
	run(t, h, "fixme", `a = 2`)
	run(t, h, "fixme", `a = 3`)
	require.NoError(t, h.Close())

	path := filepath.Join(dir, projectKey("/proj"), "vms", "fixme", "journal.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.SplitAfter(string(data), "\n")
	lines[2] = "garbage\n" // entry 2
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600))

	h2 := newTestHost(t, dir)
	_, err = h2.Run(context.Background(), RunRequest{Project: "/proj", Name: "fixme", Code: `a`})
	require.ErrorIs(t, err, errBrokenJournal)
	kept, cut, err := h2.Repair(context.Background(), "/proj", "fixme")
	require.NoError(t, err)
	require.Equal(t, 1, kept)
	require.Equal(t, 2, cut)
	require.Equal(t, "1", run(t, h2, "fixme", `a`).Value)
	baks, err := filepath.Glob(path + ".bak-*")
	require.NoError(t, err)
	require.Len(t, baks, 1)

	_, _, err = h2.Repair(context.Background(), "/proj", "fixme")
	require.ErrorContains(t, err, "not broken")
}

func TestSessionMayResizeItsScratchVM(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `v := 1`)
	mb := 32
	info, created, err := h.New(context.Background(), NewRequest{Project: "/proj", Session: "s1", Name: "scratch", MemoryMB: &mb})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, 32, info.Options.MemoryMB)
	require.Equal(t, "1", run(t, h, "", `v`).Value, "state kept across the rebuild at the new cap")

	// Another session's scratch name is still reserved.
	_, _, err = h.New(context.Background(), NewRequest{Project: "/proj", Session: "s2", Name: ScratchName("s1"), MemoryMB: &mb})
	require.ErrorContains(t, err, "reserved")
	// Memory only grows.
	small := 8
	_, _, err = h.New(context.Background(), NewRequest{Project: "/proj", Session: "s1", Name: "scratch", MemoryMB: &small})
	require.ErrorContains(t, err, "only grow")
}

// A long journal of statements replays in one call: statements cost little.
func TestLongStatementJournalReplays(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	persist := true
	_, _, err := h.New(context.Background(), NewRequest{Project: "/proj", Name: "long", Persist: &persist})
	require.NoError(t, err)
	require.NoError(t, h.Close())

	const n = 50_000
	path := filepath.Join(dir, projectKey("/proj"), "vms", "long", "journal.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var b strings.Builder
	b.Write(data)
	b.WriteString(entryLine(t, 1, "count := 0"))
	for seq := int64(2); seq <= n; seq++ {
		b.WriteString(entryLine(t, seq, "count++"))
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))

	// One call usually does it; a slow machine (or -race) may need a few,
	// each resuming where the last stopped.
	h2 := newTestHost(t, dir)
	start := time.Now()
	for calls := 1; ; calls++ {
		res, err := h2.Run(context.Background(), RunRequest{Project: "/proj", Name: "long", Code: `count`})
		if err != nil && strings.Contains(err.Error(), "call again") {
			require.Less(t, calls, 30)
			continue
		}
		require.NoError(t, err)
		require.NoError(t, res.Err)
		require.Equal(t, fmt.Sprint(n-1), res.Value)
		t.Logf("replayed %d entries in %s over %d calls", n, time.Since(start), calls)
		break
	}
}
