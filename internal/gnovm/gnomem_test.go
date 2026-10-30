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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The tests here run klash/gnomem/v1 (klashlibs/klash/gnomem/v1) in the mem
// VM, as the agent does.

const memBoard = `mem.Add("release v2; done when CI is green", "bump the version", "update CHANGELOG", "tag v2.0.0")
mem.Done(2, "now 2.0.0")
mem.Note(3, "generating it from git log was too noisy")
q := mem.Add("Q: why is login slow?")
mem.Note(q, "auth.go:88 runs one query per user")
mem.Done(q, "an N+1 query in auth.go:88")
mem.Facts["deploy cmd"] = "make ship ENV=prod"`

var changedRe = regexp.MustCompile(`changed \d{4}-\d\d-\d\d \d\d:\d\d UTC`)

// show is mem.Show() with its time made fixed.
func show(t *testing.T, h *Host, code string) string {
	t.Helper()
	return changedRe.ReplaceAllString(run(t, h, MemVM, code).Value, "changed (time)")
}

func TestMemIsMadeOnFirstUse(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	res := run(t, h, MemVM, `mem.Show()`)
	require.Contains(t, res.Notes, "made VM mem, your board (persistent): mem.Show() shows it")
	require.Equal(t, "the board is empty: Add(title, steps...) starts a checklist, and Facts[name] = value keeps a fact", res.Value)
	require.False(t, res.Journaled)

	res = run(t, h, MemVM, `mem.Show()`)
	require.Empty(t, res.Notes)
	list, err := h.List("/proj")
	require.NoError(t, err)
	require.Len(t, list.VMs, 1)
	require.Equal(t, MemVM, list.VMs[0].Name)
	require.True(t, list.VMs[0].Options.Persist)
}

func TestMemBoard(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, MemVM, memBoard)
	require.Equal(t, `board: 3 open, 2 done · next: 3 "update CHANGELOG" · changed (time)
[ ] 1 release v2; done when CI is green
  [x] 2 bump the version → now 2.0.0
  [ ] 3 update CHANGELOG  ← next
      · generating it from git log was too noisy
  [ ] 4 tag v2.0.0
[x] 5 Q: why is login slow? → an N+1 query in auth.go:88 (1 note)
facts:
  deploy cmd = make ship ENV=prod`, show(t, h, `mem.Show()`))

	// A change says what is next.
	require.Equal(t, `"done 3 \"update CHANGELOG\"; next: 4 \"tag v2.0.0\""`, run(t, h, MemVM, `mem.Done(3, "written by hand")`).Value)
	require.Equal(t, `[x] 5 Q: why is login slow?
  done: an N+1 query in auth.go:88
  note 1: auth.go:88 runs one query per user`, run(t, h, MemVM, `mem.Show(5)`).Value)

	// Items and facts are plain data.
	require.Equal(t, `"tag v2.0.0"`, run(t, h, MemVM, `mem.Items[4].Title`).Value)
	require.Equal(t, `"make ship ENV=prod"`, run(t, h, MemVM, `mem.Facts["deploy cmd"]`).Value)
}

func TestMemRefusalsSayWhatToDo(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, MemVM, memBoard)
	require.Contains(t, runErr(t, h, MemVM, `mem.Done(1, "shipped")`).Err.Error(),
		`item 1 "release v2; done when CI is green" has open steps 3 "update CHANGELOG", 4 "tag v2.0.0": finish or drop them first`)
	require.Contains(t, runErr(t, h, MemVM, `mem.Done(99, "x")`).Err.Error(),
		`there is no item 99; open: 1 "release v2; done when CI is green", 3 "update CHANGELOG", 4 "tag v2.0.0"`)
	require.Contains(t, runErr(t, h, MemVM, `mem.Step(5, "more")`).Err.Error(),
		`item 5 "Q: why is login slow?" is already done; Reopen(5) first`)
	require.Contains(t, runErr(t, h, MemVM, `mem.Add("  ")`).Err.Error(), "an item needs a title")

	// The board itself cannot be replaced.
	_, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Name: MemVM, Code: `mem = gnomem.New()`})
	require.ErrorContains(t, err, "mem is your board")
	_, err = h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Name: MemVM, Code: `mem := gnomem.New()`})
	require.ErrorContains(t, err, "mem is your board")

	// Code meant for mem, run elsewhere, is pointed there.
	res := runErr(t, h, "", `mem.Show()`)
	require.Contains(t, res.Notes, `mem is your board, kept in VM mem: run this with name "mem"`)
}

func TestMemDropAndReopen(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, MemVM, memBoard)
	require.Equal(t, `"dropped 1 \"release v2; done when CI is green\"; nothing is open"`,
		run(t, h, MemVM, `mem.Drop(1, "postponed")`).Value)
	out := show(t, h, `mem.Show()`)
	require.Contains(t, out, "board: 0 open, 2 done, 3 dropped")
	require.Contains(t, out, "[-] 1 release v2; done when CI is green · dropped: postponed (3 steps)\n")

	// Reopening a step reopens what it belongs to; what came of it is kept
	// as a note.
	require.Equal(t, `"reopened 3 \"update CHANGELOG\""`, run(t, h, MemVM, `mem.Reopen(3)`).Value)
	require.Equal(t, `"was dropped: postponed"`, run(t, h, MemVM, `mem.Items[1].Notes[0]`).Value)
	require.Contains(t, show(t, h, `mem.Show()`), `next: 3 "update CHANGELOG"`)
}

func TestMemReadsAreNotJournaled(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, MemVM, memBoard+"\nfmt.Sprint()") // fmt's first use imports it, a change
	for _, code := range []string{`mem.Show()`, `mem.Show(1, 3)`, `mem.String()`, `println(mem)`, `fmt.Println("board:", mem)`, `mem.Facts["deploy cmd"]`, `len(mem.Items)`} {
		res := run(t, h, MemVM, code)
		require.False(t, res.Journaled, code)
	}
	require.True(t, run(t, h, MemVM, `mem.Note(4, "tagging needs the release key")`).Journaled)

	// Show is a read only on a board.
	run(t, h, "", `type T struct{ n *int }
func (t T) Show() string { *t.n++; return "shown" }
var x int
var t = T{&x}`)
	require.True(t, run(t, h, "", `t.Show()`).Journaled)
	require.True(t, run(t, h, "", `println(t)`).Journaled)
}

func TestMemSurvivesARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h1 := newTestHost(t, dir)
	run(t, h1, MemVM, memBoard)
	before := run(t, h1, MemVM, `mem.Show()`).Value
	changed := run(t, h1, MemVM, `mem.Changed.UnixNano()`).Value
	require.NoError(t, h1.Close())

	h2 := newTestHost(t, dir)
	require.Equal(t, before, run(t, h2, MemVM, `mem.Show()`).Value)
	require.Equal(t, changed, run(t, h2, MemVM, `mem.Changed.UnixNano()`).Value)
}

func TestMemShowStaysShort(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, MemVM, `for i := 0; i < 60; i++ {
	g := mem.Add("goal number "+strconv.Itoa(i)+" with a fairly long title to fill the outline", "a", "b", "c")
	if i < 55 {
		for s := g + 1; s <= g+3; s++ {
			mem.Done(s, "finished with a result that takes some room")
		}
		mem.Done(g, "all three steps done")
	}
}
for i := 0; i < 40; i++ {
	mem.Facts["fact "+strconv.Itoa(i)] = strings.Repeat("x", 40)
}`)
	out := run(t, h, MemVM, `mem.Show()`).Value
	require.LessOrEqual(t, len(out), 3000+64)
	require.Contains(t, out, "… 45 older closed items (Show(id) shows one)\n")
	require.Contains(t, out, "← next")
	require.Equal(t, 1, strings.Count(out, "← next"))
}

// TestMemV1IsFrozen replays a fixed board against golden files. klash/
// gnomem/v1 only ever grows: persistent boards are rebuilt by running their
// calls again, so what a call does may not change. If this test fails, the
// change belongs in klash/gnomem/v2; never regenerate the golden files to
// make it pass. (GNOMEM_V1_GOLDEN_WRITE=1 wrote them, once.)
func TestMemV1IsFrozen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Its own clock: the shared one's times depend on the other tests.
	var tick atomic.Int64
	clock := func() time.Time { return time.Unix(1_800_000_000, 0).Add(time.Duration(tick.Add(1)) * time.Second) }
	host := func() *Host {
		h, err := NewHost(Options{
			Dir: dir, Client: NewClientID(), Now: clock, BudgetBytes: 2 << 30,
			Probe: func() (HostMemory, error) { return HostMemory{Total: 16 << 30, Available: 8 << 30}, nil },
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = h.Close() })
		return h
	}
	h1 := host()
	for _, code := range []string{
		`mem.Add("goal: ship the parser; done when go test passes", "lex", "parse", "go test ./... passes")`,
		`mem.Step(1, "error messages")`,
		`mem.Done(2, "a hand-written lexer")`,
		`mem.Note(3, "Pratt parsing for expressions")`,
		`mem.Note(3, "line one\nline two")`,
		`mem.Add("Q: which license?")`,
		`mem.Note(6, "the repo's LICENSE is MIT — LICENSE:1")`,
		`mem.Done(6, "MIT")`,
		`mem.Add("` + strings.Repeat("long title ", 30) + `")`,
		`mem.Drop(7, "not needed")`,
		`mem.Reopen(7)`,
		`mem.Drop(7, "really not needed")`,
		`mem.Facts["go version"] = "1.22"`,
		`mem.Facts["bad\nkey"] = "multi\nline"`,
		`delete(mem.Facts, "bad\nkey")`,
	} {
		run(t, h1, MemVM, code)
	}
	require.NoError(t, h1.Close())

	h2 := host()
	got := map[string]string{
		"show.golden":  run(t, h2, MemVM, `mem.Show()`).Value,
		"items.golden": run(t, h2, MemVM, `mem.Show(1, 2, 3, 4, 5, 6, 7)`).Value,
		"data.golden":  run(t, h2, MemVM, `*mem`).Value,
	}
	for name, value := range got {
		path := filepath.Join("testdata", "gnomem", "v1", name)
		if os.Getenv("GNOMEM_V1_GOLDEN_WRITE") == "1" {
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte(value), 0o644))
			continue
		}
		want, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, string(want), value, "%s: klash/gnomem/v1 changed; the change belongs in v2", name)
	}
}
