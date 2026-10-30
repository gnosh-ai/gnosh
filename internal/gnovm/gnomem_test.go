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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The tests here run klash/gnomem/v1 (klashlibs/klash/gnomem/v1) in the mem
// VM, as the agent does.

const memBoard = `mem.Add("release v2", "bump the version", "update CHANGELOG", "tag v2.0.0", "CI is green")
mem.Done(2, "now 2.0.0")
mem.Note(3, "generating it from git log was too noisy")
mem.Add("Q: why is login slow?")
mem.Note(6, "auth.go:88 runs one query per user")
mem.Done(6, "an N+1 query in auth.go:88")
mem.Facts["deploy cmd"] = "make ship ENV=prod"`

const memBoardShow = `board: 4 open, 2 done · next: 3 "update CHANGELOG" · facts: deploy cmd
facts:
  deploy cmd = make ship ENV=prod
[ ] 1 release v2
  [x] 2 bump the version → now 2.0.0
  [ ] 3 update CHANGELOG  ← next
      · generating it from git log was too noisy
  [ ] 4 tag v2.0.0
  [ ] 5 CI is green
[x] 6 Q: why is login slow? → an N+1 query in auth.go:88 (1 note)`

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
	// Each change says what it did, with the ids it made and what is next.
	require.Equal(t, `added 1 "release v2": steps 2 "bump the version", 3 "update CHANGELOG", 4 "tag v2.0.0", 5 "CI is green"
done 2 "bump the version"; next: 3 "update CHANGELOG"
added 6 "Q: why is login slow?"
done 6 "Q: why is login slow?"; next: 3 "update CHANGELOG"
`, run(t, h, MemVM, memBoard).Output)
	require.Equal(t, memBoardShow, run(t, h, MemVM, `mem.Show()`).Value)
	require.Equal(t, memBoardShow, run(t, h, MemVM, `mem`).Value, "a board on its own shows as its outline")

	res := run(t, h, MemVM, `mem.Done(3, "written by hand")`)
	require.Equal(t, "done 3 \"update CHANGELOG\"; next: 4 \"tag v2.0.0\"\n", res.Output)
	require.Empty(t, res.Value)
	require.Equal(t, `[x] 6 Q: why is login slow?
  done: an N+1 query in auth.go:88
  note 1: auth.go:88 runs one query per user`, run(t, h, MemVM, `mem.Show(6)`).Value)
	require.Equal(t, "added 7 \"note the breaking changes\", a step of 1 \"release v2\"\n",
		run(t, h, MemVM, `mem.Step(1, "note the breaking changes")`).Output)
	// What came of it may go unsaid.
	require.Equal(t, "done 7 \"note the breaking changes\"; next: 4 \"tag v2.0.0\"\n", run(t, h, MemVM, `mem.Done(7)`).Output)

	// Items and facts are plain data.
	require.Equal(t, `"tag v2.0.0"`, run(t, h, MemVM, `mem.Items[4].Title`).Value)
	require.Equal(t, `"make ship ENV=prod"`, run(t, h, MemVM, `mem.Facts["deploy cmd"]`).Value)
}

func TestMemRefusalsSayWhatToDo(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, MemVM, memBoard)
	require.Contains(t, runErr(t, h, MemVM, `mem.Done(1, "shipped")`).Err.Error(),
		`item 1 "release v2" has open steps 3 "update CHANGELOG", 4 "tag v2.0.0", 5 "CI is green": finish or drop them first`)
	require.Contains(t, runErr(t, h, MemVM, `mem.Done(99, "x")`).Err.Error(),
		`there is no item 99; open: 1 "release v2", 3 "update CHANGELOG", 4 "tag v2.0.0", 5 "CI is green"`)
	require.Contains(t, runErr(t, h, MemVM, `mem.Step(6, "more")`).Err.Error(),
		`item 6 "Q: why is login slow?" is already done; Reopen(6) first`)
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
	require.Equal(t, "dropped 1 \"release v2\"; nothing is open\n", run(t, h, MemVM, `mem.Drop(1, "postponed")`).Output)
	out := run(t, h, MemVM, `mem.Show()`).Value
	require.Contains(t, out, "board: 0 open, 2 done, 4 dropped · facts: deploy cmd\n")
	require.Contains(t, out, "[-] 1 release v2 · dropped: postponed (4 steps)\n")

	// Reopening a step reopens what it belongs to, and the steps dropped
	// along with it; what came of each is kept as a note.
	require.Equal(t, "reopened 3 \"update CHANGELOG\"; next: 3 \"update CHANGELOG\"\n", run(t, h, MemVM, `mem.Reopen(3)`).Output)
	require.Equal(t, `"was dropped: postponed"`, run(t, h, MemVM, `mem.Items[1].Notes[0]`).Value)
	require.Equal(t, `""`, run(t, h, MemVM, `mem.Items[4].Status`).Value)
	require.Contains(t, run(t, h, MemVM, `mem.Show()`).Value, "board: 4 open, 2 done · next")

	// So a goal reopened is not done until its condition is.
	run(t, h, MemVM, `mem.Add("ship", "CI green")
mem.Drop(7, "later")
mem.Reopen(7)`)
	require.Contains(t, runErr(t, h, MemVM, `mem.Done(7, "shipped")`).Err.Error(), `has open steps 8 "CI green"`)
	require.Contains(t, runErr(t, h, MemVM, `mem.Drop(8, " ")`).Err.Error(), `say why: Drop(8, "why")`)

	// Any other word closes an item too, and is shown as itself.
	run(t, h, MemVM, `mem.Items[4].Status = "blocked"`)
	out = run(t, h, MemVM, `mem.Show()`).Value
	require.Contains(t, out, "board: 5 open, 2 done, 1 blocked")
	require.Contains(t, out, "  [-] 4 tag v2.0.0 · blocked\n")
}

func TestMemShowCapsClosedSteps(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, MemVM, `g := mem.Add("fix until green")
for i := 0; i < 40; i++ {
	s := mem.Step(g, "try "+strconv.Itoa(i))
	mem.Done(s, strings.Repeat("y", 80))
}
s := mem.Step(g, "try -race")
mem.Note(s, "flakes")
mem.Facts["cmd"] = "go test"`)
	out := run(t, h, MemVM, `mem.Show()`).Value
	require.Contains(t, out, "facts:\n  cmd = go test\n")
	require.Contains(t, out, "  … 35 earlier closed steps\n")
	require.Contains(t, out, "  [ ] 42 try -race  ← next\n      · flakes")
	require.Less(t, len(out), 1500)
}

func TestMemGuards(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	run(t, h, MemVM, memBoard)

	// A call that leaves no board keeps nothing.
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Name: MemVM, Code: `if true { mem = nil }`})
	require.NoError(t, err)
	require.ErrorContains(t, res.Err, "set it to nil")
	require.Contains(t, run(t, h, MemVM, `mem.Show()`).Value, "board: 4 open")

	// Only a board's outline is shown unquoted.
	require.Equal(t, `"a\nb"`, run(t, h, MemVM, `"a\nb"`).Value)

	// mem is always persistent.
	persist := false
	_, _, err = h.New(context.Background(), NewRequest{Project: "/proj", Name: MemVM, Persist: &persist})
	require.ErrorContains(t, err, "always persistent")
}

// A VM named mem from before the board is refused, not quietly used.
func TestMemOlderVMIsRefused(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	require.NoError(t, h.ensureMem(projectKey("/proj")))
	s, err := h.lookup(projectKey("/proj"), MemVM)
	require.NoError(t, err)
	require.NoError(t, s.j.append(&entry{Seq: 1, Time: testNow().UnixNano(), Ops: []op{{File: "var mem = map[string]string{}"}}}))
	_, err = h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Name: MemVM, Code: `mem.Show()`})
	require.ErrorContains(t, err, "VM mem predates the board")
}

func TestMemReadsAreNotJournaled(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, MemVM, memBoard+"\nfmt.Sprint()") // fmt's first use imports it, a change
	for _, code := range []string{`mem`, `mem.Show()`, `mem.Show(1, 3)`, `mem.String()`, `println(mem)`, `fmt.Println("board:", mem)`, `mem.Facts["deploy cmd"]`, `len(mem.Items)`} {
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
	require.NoError(t, h1.Close())

	h2 := newTestHost(t, dir)
	require.Equal(t, before, run(t, h2, MemVM, `mem.Show()`).Value)
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
	require.Contains(t, out, "facts:\n", "facts come before the outline, so a cut keeps them")
	require.Contains(t, out, "  … 20 more\n")
	require.Contains(t, out, "… 45 older closed items (Show(id) shows one)\n")
	require.Contains(t, out, "next: 222 ")
}

// memCorpus is a fixed history of calls that exercises every rule of what
// klash/gnomem/v1 stores.
var memCorpus = []string{
	`mem.Add("goal: ship the parser", "lex", "parse", "go test ./... passes")`,
	`mem.Step(3, "parse expressions")`,
	`mem.Step(3, "parse statements")`,
	`mem.Done(2, "a hand-written lexer")`,
	`mem.Note(3, "Pratt parsing for expressions")`,
	`mem.Note(3, "line one\nline two")`,
	`mem.Done(5, "Pratt")`,
	`mem.Add("Q: which license?")`,
	`mem.Note(7, "the repo's LICENSE is MIT — LICENSE:1")`,
	`mem.Done(7, "MIT")`,
	`mem.Note(7, "noted after it closed")`,
	`mem.Add("` + strings.Repeat("x", 199) + `é")`,
	`mem.Note(8, "` + strings.Repeat("y", 999) + `é")`,
	`mem.Drop(1, "not this release")`,
	`mem.Reopen(6)`,
	`mem.Reopen(1)`,
	`mem.Done(6, "statements too")`,
	`mem.Done(3, "parser done")`,
	`mem.Items[4].Status = "blocked"`,
	`mem.Facts["go version"] = "1.22"`,
	`mem.Facts["bad\nkey"] = "multi\nline"`,
	`delete(mem.Facts, "bad\nkey")`,
	`mem.Facts["a"] = "1"`,
	`func dumpBoard() string {
	s := ""
	for i, it := range mem.Items {
		s += strconv.Itoa(i) + " " + strconv.Quote(it.Title) + " parent " + strconv.Itoa(it.Parent) +
			" status " + strconv.Quote(it.Status) + " result " + strconv.Quote(it.Result) + "\n"
		for _, n := range it.Notes {
			s += "  note " + strconv.Quote(n) + "\n"
		}
	}
	var keys []string
	for k := range mem.Facts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s += "fact " + strconv.Quote(k) + " = " + strconv.Quote(mem.Facts[k]) + "\n"
	}
	return s
}`,
}

// TestMemV1IsFrozen replays memCorpus and compares the board with golden
// files. A persistent board is rebuilt by running its calls again, so
// data.golden, what the calls store, must never change: if it differs, the
// change belongs in klash/gnomem/v2. The other files pin what the board
// prints and shows; those may change, after review, by writing them again
// with GNOMEM_V1_FORMAT_WRITE=1.
func TestMemV1IsFrozen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A clock that stands still: the shared one's times depend on the other
	// tests, and a ticking one on how often the host reads it.
	clock := func() time.Time { return time.Unix(1_800_000_000, 0) }
	host := func() *Host {
		h, err := NewHost(Options{
			Dir: dir, Client: NewClientID(), Now: clock, BudgetBytes: 32 << 30,
			Probe: func() (HostMemory, error) { return HostMemory{Total: 128 << 30, Available: 64 << 30}, nil },
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = h.Close() })
		return h
	}
	h1 := host()
	var printed strings.Builder
	for _, code := range memCorpus {
		printed.WriteString(run(t, h1, MemVM, code).Output)
	}
	require.NoError(t, h1.Close())

	h2 := host()
	data, err := strconv.Unquote(run(t, h2, MemVM, `dumpBoard()`).Value)
	require.NoError(t, err)
	files := map[string]struct {
		got    string
		format bool
	}{
		"data.golden":    {data, false},
		"show.golden":    {run(t, h2, MemVM, `mem.Show()`).Value, true},
		"items.golden":   {run(t, h2, MemVM, `mem.Show(1, 2, 3, 4, 5, 6, 7, 8)`).Value, true},
		"printed.golden": {printed.String(), true},
	}
	for name, f := range files {
		path := filepath.Join("testdata", "gnomem", "v1", name)
		if os.Getenv("GNOMEM_V1_FORMAT_WRITE") == "1" {
			// data.golden is written only where there is none.
			if _, err := os.Stat(path); f.format || os.IsNotExist(err) {
				require.NoError(t, os.WriteFile(path, []byte(f.got), 0o644))
				continue
			}
		}
		want, err := os.ReadFile(path)
		require.NoError(t, err)
		if f.format {
			require.Equal(t, string(want), f.got, "%s: what klash/gnomem/v1 shows changed; if that is meant, write it again with GNOMEM_V1_FORMAT_WRITE=1", name)
		} else {
			require.Equal(t, string(want), f.got, "%s: what klash/gnomem/v1 stores changed; that belongs in v2", name)
		}
	}
}

func TestMemTwoKlashesMakeItOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h1, h2 := newTestHost(t, dir), newTestHost(t, dir)
	errs := make(chan error, 2)
	for _, h := range []*Host{h1, h2} {
		go func() {
			res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s", Name: MemVM, Code: `mem.Add("x")`})
			if err == nil && res.Err != nil {
				err = res.Err
			}
			errs <- err
		}()
	}
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	h3 := newTestHost(t, dir)
	require.Equal(t, "2", run(t, h3, MemVM, `len(mem.Items) - 1`).Value)
	list, err := h3.List("/proj")
	require.NoError(t, err)
	require.Equal(t, int64(3), list.VMs[0].Entries) // the board, then two adds
}

var changedRe = regexp.MustCompile(`last changed \d{4}-\d\d-\d\d \d\d:\d\d UTC`)

// A mem VM keeps its board's first line in a file, so a new session can be
// told what is open without a worker or a rebuild.
func TestMemBoardSummary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	run(t, h, MemVM, `mem.Show()`)
	require.Empty(t, BoardSummary(dir, "/proj"), "an empty board says nothing")

	run(t, h, MemVM, memBoard)
	got := changedRe.ReplaceAllString(BoardSummary(dir, "/proj"), "last changed (time)")
	require.Equal(t, "<gno-board>\nYour Gno board mem, from earlier sessions (last changed (time)): "+
		`board: 4 open, 2 done · next: 3 "update CHANGELOG" · facts: deploy cmd`+
		". `mem.Show()` (gnovm_run name \"mem\") shows it; use it only where this task is part of it, and change a fact by setting it again under its name.\n</gno-board>", got)

	// Reads leave it as it is; a change rewrites it. With nothing open, the
	// facts are still worth saying: a session that did not see their names
	// added a second fact for one that changed.
	run(t, h, MemVM, `mem.Show()`)
	run(t, h, MemVM, `mem.Drop(1, "postponed")`)
	require.Contains(t, BoardSummary(dir, "/proj"), "board: 0 open, 2 done, 4 dropped · facts: deploy cmd")
	run(t, h, MemVM, `delete(mem.Facts, "deploy cmd")`)
	require.Empty(t, BoardSummary(dir, "/proj"), "nothing open and no facts: nothing said")
	require.Empty(t, BoardSummary(dir, "/elsewhere"))
}
