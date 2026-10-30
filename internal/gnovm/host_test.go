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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The base loads stdlibs once per process; tests share one to stay fast.
var testClock atomic.Int64

func testNow() time.Time {
	return time.Unix(1_800_000_000, 0).Add(time.Duration(testClock.Add(1)) * time.Millisecond)
}

func newTestHost(t *testing.T, dir string) *Host {
	t.Helper()
	h, err := NewHost(Options{
		Dir:    dir,
		Client: NewClientID(),
		Probe:  func() (HostMemory, error) { return HostMemory{Total: 128 << 30, Available: 64 << 30}, nil },
		Now:    testNow,
		// A base measures the heap it grows by (base.ensure), and parallel
		// tests' allocations land in each other's measurements: on the AI
		// box's 32 cores they came to more than 2 GB. Room enough keeps that
		// noise from refusing VMs; the budget's own tests set small ones.
		BudgetBytes: 32 << 30,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func run(t *testing.T, h *Host, name, code string) *RunResult {
	t.Helper()
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Name: name, Code: code, Req: "r"})
	require.NoError(t, err, code)
	require.NoError(t, res.Err, code)
	return res
}

func TestScratchKeepsStateBetweenCalls(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	res := run(t, h, "", `x := 40`)
	require.True(t, res.Journaled)
	require.Contains(t, strings.Join(res.Notes, "\n"), "new scratch VM")

	res = run(t, h, "", `x += 2
x`)
	require.Equal(t, "42", res.Value)

	// A pure evaluation is not journaled.
	res = run(t, h, "", `x * 2`)
	require.Equal(t, "84", res.Value)
	require.False(t, res.Journaled)
}

func TestMixedInputAndForwardReferences(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	res := run(t, h, "", `import "strings"

func main() { println(shout("hi")) }

func shout(s string) string { return strings.ToUpper(s) + "!" }

type P struct{ W, H int }

func (p P) Area() int { return p.W * p.H }

const (
	A = iota
	B
	C
)

func even(n int) bool { if n == 0 { return true }; return odd(n - 1) }
func odd(n int) bool  { if n == 0 { return false }; return even(n - 1) }`)
	require.Equal(t, "HI!\n", res.Output)

	res = run(t, h, "", `P{W: 3, H: 4}.Area() + C`)
	require.Equal(t, "14", res.Value)
	res = run(t, h, "", `even(10)`)
	require.Equal(t, "true", res.Value)
	// An import binds package-wide: later calls use it without importing.
	res = run(t, h, "", `strings.Repeat("ab", 2)`)
	require.Equal(t, `"abab"`, res.Value)

	// A second main runs too: each input's main is renamed.
	res = run(t, h, "", `func main() { println("second") }`)
	require.Equal(t, "second\n", res.Output)
}

func TestXIsNotShadowed(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `x := 1`)
	require.Equal(t, "1", run(t, h, "", `x`).Value)
}

func TestRedeclarationIsRefused(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `func helper() int { return 41 }`)
	_, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: `func helper() int { return 99 }`})
	var ref *refusal
	require.ErrorAs(t, err, &ref)
	require.Contains(t, err.Error(), "helper is already declared")
	require.Equal(t, "41", run(t, h, "", `helper()`).Value)

	// reset starts the scratch VM over.
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: `func helper() int { return 99 }`, Reset: true})
	require.NoError(t, err)
	require.NoError(t, res.Err)
	require.Equal(t, "99", run(t, h, "", `helper()`).Value)
}

func TestProgramsRunFresh(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	prog := `package main

import "fmt"

var n = 2

func main() { fmt.Println("n is", n) }
`
	for range 2 {
		res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: prog})
		require.NoError(t, err)
		require.NoError(t, res.Err)
		require.True(t, res.Program)
		require.Equal(t, "n is 2\n", res.Output)
	}
}

func TestFailedCallKeepsNothing(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `count := 1`)
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: `count = 5
panic("boom")`})
	require.NoError(t, err)
	var p *panicked
	require.ErrorAs(t, res.Err, &p)
	require.Contains(t, p.msg, "boom")
	// The VM was rebuilt from its journal, without the failed call.
	require.Equal(t, "1", run(t, h, "", `count`).Value)
}

func TestGasLimitStopsALoop(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: `for {}`, Gas: 2_000_000})
	require.NoError(t, err)
	var lim *limitError
	require.ErrorAs(t, res.Err, &lim)
	require.Equal(t, "gas", lim.kind)
	require.GreaterOrEqual(t, res.Gas, int64(2_000_000))
}

func TestMemoryCapStopsGrowth(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	mb := 1
	_, _, err := h.New(context.Background(), NewRequest{Project: "/proj", Name: "small", MemoryMB: &mb})
	require.NoError(t, err)
	// Garbage is collected under the cap...
	run(t, h, "small", `for i := 0; i < 2000; i++ { _ = make([]byte, 10000) }`)
	// ...but live data past it is refused.
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Name: "small", Code: `var keep [][]byte
for i := 0; i < 300; i++ { keep = append(keep, make([]byte, 10000)) }`})
	require.NoError(t, err)
	var lim *limitError
	require.ErrorAs(t, res.Err, &lim)
	require.Equal(t, "memory", lim.kind)
}

func TestPersistentVMSurvivesANewHost(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h1 := newTestHost(t, dir)
	persist := true
	_, created, err := h1.New(context.Background(), NewRequest{Project: "/proj", Name: "notes", Persist: &persist})
	require.NoError(t, err)
	require.True(t, created)
	run(t, h1, "notes", `import "strings"
var items []string
func add(s string) { items = append(items, strings.TrimSpace(s)) }`)
	run(t, h1, "notes", `add(" one ")
add("two")`)
	require.NoError(t, h1.Close())

	h2 := newTestHost(t, dir)
	res := run(t, h2, "notes", `len(items)`)
	require.Equal(t, "2", res.Value)
	require.Contains(t, strings.Join(res.Notes, "\n"), "rebuilt VM notes from 2 journal entries")
	require.Equal(t, `"one"`, run(t, h2, "notes", `items[0]`).Value)
}

func TestTimeIsReplayedFromTheJournal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h1 := newTestHost(t, dir)
	persist := true
	_, _, err := h1.New(context.Background(), NewRequest{Project: "/proj", Name: "clock", Persist: &persist})
	require.NoError(t, err)
	run(t, h1, "clock", `import "time"
var at = time.Now().UnixNano()`)
	first := run(t, h1, "clock", `at`).Value
	require.NoError(t, h1.Close())

	h2 := newTestHost(t, dir)
	require.Equal(t, first, run(t, h2, "clock", `at`).Value)
}

func TestTwoHostsCatchUp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, b := newTestHost(t, dir), newTestHost(t, dir)
	persist := true
	_, _, err := a.New(context.Background(), NewRequest{Project: "/proj", Name: "shared", Persist: &persist})
	require.NoError(t, err)
	run(t, a, "shared", `n := 1`)
	run(t, b, "shared", `n += 10`)
	// a's resident VM catches up with b's entry before running.
	require.Equal(t, "11", run(t, a, "shared", `n`).Value)
	run(t, a, "shared", `n += 100`)
	require.Equal(t, "111", run(t, b, "shared", `n`).Value)
}

func TestDeletedVMIsNoticed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, b := newTestHost(t, dir), newTestHost(t, dir)
	persist := true
	_, _, err := a.New(context.Background(), NewRequest{Project: "/proj", Name: "gone", Persist: &persist})
	require.NoError(t, err)
	run(t, a, "gone", `v := 1`)
	require.NoError(t, b.Drop(context.Background(), "/proj", "gone", "delete"))
	_, err = a.Run(context.Background(), RunRequest{Project: "/proj", Name: "gone", Code: `v`})
	require.Error(t, err)
	require.Contains(t, err.Error(), "deleted")

	// Made again under the same name, it starts empty.
	_, _, err = b.New(context.Background(), NewRequest{Project: "/proj", Name: "gone", Persist: &persist})
	require.NoError(t, err)
	res, err := a.Run(context.Background(), RunRequest{Project: "/proj", Name: "gone", Code: `v`})
	require.NoError(t, err)
	require.Error(t, res.Err, "the new VM has no v")
}

func TestTornTailIsCut(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	persist := true
	_, _, err := h.New(context.Background(), NewRequest{Project: "/proj", Name: "torn", Persist: &persist})
	require.NoError(t, err)
	run(t, h, "torn", `a := 1`)
	require.NoError(t, h.Close())
	path := filepath.Join(dir, projectKey("/proj"), "vms", "torn", "journal.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`{"seq":2,"time":1,"ops":[{"stmts":"a = 9`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	h2 := newTestHost(t, dir)
	require.Equal(t, "1", run(t, h2, "torn", `a`).Value)
	run(t, h2, "torn", `a = 2`)
	_, entries, torn, err := readJournal(path)
	require.NoError(t, err)
	require.False(t, torn)
	require.Len(t, entries, 2)
}

func TestBudgetEvictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()
	h, err := NewHost(Options{
		Dir:         t.TempDir(),
		Client:      NewClientID(),
		BudgetBytes: 80 << 20,
		Probe:       func() (HostMemory, error) { return HostMemory{Available: 8 << 30}, nil },
		Now:         testNow,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	for _, name := range []string{"a", "b"} {
		_, _, err := h.New(context.Background(), NewRequest{Project: "/proj", Name: name})
		require.NoError(t, err)
		run(t, h, name, `v := "`+name+`"`)
	}
	// a and b at 16 MB each, plus the reserve, do not fit in 80 MB: a went.
	list, err := h.List("/proj")
	require.NoError(t, err)
	resident := map[string]bool{}
	for _, vm := range list.VMs {
		resident[vm.Name] = vm.Resident
	}
	require.False(t, resident["a"])
	require.True(t, resident["b"])
	// a comes back from its journal when used.
	require.Equal(t, `"a"`, run(t, h, "a", `v`).Value)
}

func TestUnavailablePackageIsExplained(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	_, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: `import "os"
os.Exit(1)`})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no files, network or processes")
}
