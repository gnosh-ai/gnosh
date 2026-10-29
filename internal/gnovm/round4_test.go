// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

// Review round 4's probes, kept as regression tests: each one asserts the
// behaviour the round's fixes give, where the review found a bug.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var bg = context.Background()

// P1: the replay-wall limit ("keeps nothing more") is bypassed by ending an
// input with a pure expression. Not parallel: shortens a package limit.
func TestRound4ReplayWallBypassedByTrailingPureExpr(t *testing.T) {
	save := maxReplayWall
	t.Cleanup(func() { maxReplayWall = save })
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `n := 0`)
	maxReplayWall = time.Nanosecond
	_, err := h.Run(bg, RunRequest{Project: "/proj", Session: "s1", Code: "n = 7"})
	require.ErrorContains(t, err, "keeps nothing more")
	res, err := h.Run(bg, RunRequest{Project: "/proj", Session: "s1", Code: "n = 7\nn"})
	if err == nil && res.Err == nil {
		t.Logf("statement + pure eval: journaled=%v seq=%d value=%s", res.Journaled, res.Seq, res.Value)
	}
	require.Error(t, err, "a statement followed by a pure expression is kept past maxReplayWall")
}

// P2: a builtin shadowed in the same input makes a state-changing eval look
// pure, so it is not journaled and a rebuild loses its effect.
func TestRound4ShadowedBuiltinInSameInput(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	res := run(t, h, "", `var hits int
func min(a, b int) int { hits++; return a }
min(1, 2)`)
	t.Logf("journaled=%v value=%s", res.Journaled, res.Value)
	live := run(t, h, "", `hits`).Value
	require.NoError(t, h.Drop(bg, "/proj", ScratchName("s1"), "evict"))
	replayed := run(t, h, "", `hits`).Value
	require.Equal(t, live, replayed, "live hits=%s, after rebuild hits=%s", live, replayed)
}

// P3: persistence is read from machine.json, not from where the VM lives. A
// crash between persist()'s rename and its machine.json rewrite leaves a VM
// under vms/ that every host treats as memory-only: no lock, no catch-up,
// and Close deletes it.
func TestRound4PersistCrashWindowLosesVM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// The "crashing" host: never closed (a SIGKILL closes nothing).
	h1, err := NewHost(Options{
		Dir: dir, Client: NewClientID(), Now: testNow,
		Probe: func() (HostMemory, error) { return HostMemory{Total: 16 << 30, Available: 8 << 30}, nil },
	})
	require.NoError(t, err)
	_, _, err = h1.New(bg, NewRequest{Project: "/proj", Name: "keep"})
	require.NoError(t, err)
	run(t, h1, "keep", `v := 42`)
	s := h1.vms[projectKey("/proj")+"/keep"]
	require.NoError(t, h1.persist(s)) // the rename; the crash comes before writeMachineFile
	vmdir := filepath.Join(dir, projectKey("/proj"), "vms", "keep")
	_, err = os.Stat(vmdir)
	require.NoError(t, err, "the VM is under vms/ now")

	// Another klash process lists the VMs, then exits.
	h2, err := NewHost(Options{
		Dir: dir, Client: NewClientID(), Now: testNow,
		Probe: func() (HostMemory, error) { return HostMemory{Total: 16 << 30, Available: 8 << 30}, nil },
	})
	require.NoError(t, err)
	list, err := h2.List("/proj")
	require.NoError(t, err)
	for _, v := range list.VMs {
		t.Logf("listed %s persist=%v", v.Name, v.Options.Persist)
	}
	require.NoError(t, h2.Close())
	_, err = os.Stat(vmdir)
	require.NoError(t, err, "listing a persistent VM and exiting deleted it")
}

// P3b: the same state lets two hosts write one journal without its lock or
// catch-up: the second writer's WriteAt lands on the first's entry.
func TestRound4PersistFalseInVmsCorruptsJournal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h1 := newTestHost(t, dir)
	persist := true
	_, _, err := h1.New(bg, NewRequest{Project: "/proj", Name: "shared", Persist: &persist})
	require.NoError(t, err)
	run(t, h1, "shared", `v := 1`)
	require.NoError(t, h1.Close())
	// machine.json says persist false (the crash window above, or a failed
	// writeMachineFile after the rename).
	mfPath := filepath.Join(dir, projectKey("/proj"), "vms", "shared", "machine.json")
	b, err := os.ReadFile(mfPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mfPath, []byte(strings.Replace(string(b), `"persist": true`, `"persist": false`, 1)), 0o600))

	a, c := newTestHost(t, dir), newTestHost(t, dir)
	run(t, a, "shared", `v = 2`)                // a: seq 2
	run(t, c, "shared", `v = 3333333333333333`) // c: reads the journal now, seq 3
	run(t, a, "shared", `v = 4`)                // a: stale view, writes "seq 3" at its old end
	path := filepath.Join(dir, projectKey("/proj"), "vms", "shared", "journal.jsonl")
	_, entries, torn, err := readJournal(path)
	var seqs []int64
	for _, e := range entries {
		seqs = append(seqs, e.Seq)
	}
	t.Logf("after interleaved writes: entries %v torn=%v err=%v", seqs, torn, err)
	require.NoError(t, err, "journal broken by two unlocked writers")
	require.False(t, torn, "journal torn by two unlocked writers")
	require.Len(t, entries, 4, "an entry was overwritten")
}

// P4: a second repair in another process, which still remembers the VM as
// broken at seq K, cuts entries the first repair's process added since.
func TestRound4StaleRepairCutsNewEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, b := newTestHost(t, dir), newTestHost(t, dir)
	persist := true
	_, _, err := a.New(bg, NewRequest{Project: "/proj", Name: "g", Persist: &persist})
	require.NoError(t, err)
	run(t, a, "g", `x := 1`)
	run(t, a, "g", `x = 2`)
	// Entry 2 now fails on replay (a gno upgrade changed its meaning, say).
	path := filepath.Join(dir, projectKey("/proj"), "vms", "g", "journal.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(data), `"x = 2"`, `"panic(\"diverged\")"`, 1)), 0o600))
	require.NoError(t, a.Drop(bg, "/proj", "g", "evict"))
	_, err = a.Run(bg, RunRequest{Project: "/proj", Name: "g", Code: `x`})
	require.ErrorContains(t, err, "replaying entry 2")
	_, err = b.Run(bg, RunRequest{Project: "/proj", Name: "g", Code: `x`})
	require.ErrorContains(t, err, "replaying entry 2")

	// b repairs and carries on: entries 2 and 3 are new, good work.
	kept, cut, err := b.Repair(bg, "/proj", "g")
	require.NoError(t, err)
	t.Logf("b repaired: kept %d cut %d", kept, cut)
	run(t, b, "g", `x = 10`)
	run(t, b, "g", `x = 11`)

	// a's user, who also saw "broken", repairs too.
	kept, cut, err = a.Repair(bg, "/proj", "g")
	t.Logf("a repaired: kept %d cut %d err %v", kept, cut, err)
	require.Equal(t, "11", run(t, b, "g", `x`).Value, "a's stale repair cut b's new entries")
}

// P6: a leftover temp dir from a crashed create shows up as a VM and counts
// toward the 64-VM limit.
func TestRound4LeftoverTempDirIsListed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	persist := true
	_, _, err := h.New(bg, NewRequest{Project: "/proj", Name: "real", Persist: &persist})
	require.NoError(t, err)
	src := filepath.Join(dir, projectKey("/proj"), "vms", "real")
	dst := filepath.Join(dir, projectKey("/proj"), "vms", ".new-ghost-123")
	require.NoError(t, os.MkdirAll(dst, 0o700))
	for _, f := range []string{"machine.json", "journal.jsonl"} {
		b, err := os.ReadFile(filepath.Join(src, f))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dst, f), b, 0o600))
	}
	h2 := newTestHost(t, dir)
	list, err := h2.List("/proj")
	require.NoError(t, err)
	var names []string
	for _, v := range list.VMs {
		names = append(names, v.Name)
	}
	n, _ := h2.countNamed(projectKey("/proj"))
	t.Logf("listed %v; countNamed=%d", names, n)
	require.Equal(t, []string{"real"}, names, "a crashed create's temp dir is listed as a VM")
}

// P9: closures made by statements keep their syntax trees too, but are
// neither charged (400x declaration bytes) nor counted against the
// per-call and per-VM declaration caps. Not parallel: heap measure.
func TestRound4ClosureSyntaxTreesUncharged(t *testing.T) {
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `var fns []func() int`)
	var body strings.Builder
	for i := range 150 {
		fmt.Fprintf(&body, "\t\tif a > %d { a -= %d } else { a += %d * 2 }\n", i, i, i)
	}
	before := heapAlloc()
	src := 0
	for k := range 20 {
		code := fmt.Sprintf("fns = append(fns, func() int {\n\t\ta := %d\n%s\t\treturn a\n\t})", k, body.String())
		src += len(code)
		res := run(t, h, "", code)
		require.True(t, res.Journaled)
	}
	after := heapAlloc()
	s := h.vms[projectKey("/proj")+"/"+ScratchName("s1")]
	t.Logf("%d KB of closure source in statements: heap +%d MB (%.0fx); declBytes charged %d; VM charge %d MB; allocator %d MB",
		src>>10, (int64(after)-int64(before))>>20, float64(int64(after)-int64(before))/float64(src), s.cost.declBytes, h.charge(s)>>20, func() int64 { _, u := s.res.alloc.Status(); return u >> 20 }())
	require.Equal(t, "20", run(t, h, "", `len(fns)`).Value)
}

// P10: Run, Drop, Repair and Kept take a VM name without checking it, so
// "./goals" passes the goals-VM refusal, and "../../<other key>/vms/x"
// reaches another project's VM, under a lock path that is not that VM's.
func TestRound4VMNameTraversal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	// The goals VM, as phase 3 will make it.
	gs, err := h.create(projectKey("/proj"), GoalsVM, VMOptions{MemoryMB: 16, Gas: defaultGas, NsPerGas: 1, Persist: true})
	require.NoError(t, err)
	_ = gs
	res, err := h.Run(bg, RunRequest{Project: "/proj", Name: "./goals", Code: `forged := "closed as met"`})
	if err == nil {
		t.Logf("./goals: journaled=%v err=%v", res.Journaled, res.Err)
	}
	if err == nil {
		t.Errorf("./goals passes the goals-VM refusal")
	}

	// Another project's VM.
	persist := true
	_, _, err = h.New(bg, NewRequest{Project: "/other", Name: "secret", Persist: &persist})
	require.NoError(t, err)
	run2 := func(project, name, code string) (*RunResult, error) {
		return h.Run(bg, RunRequest{Project: project, Name: name, Code: code})
	}
	_, err = run2("/other", "secret", `token := "s3cr3t"`)
	require.NoError(t, err)
	res, err = run2("/proj", "../../"+projectKey("/other")+"/vms/secret", `token`)
	if err == nil {
		t.Logf("cross-project read: %q err=%v", res.Value, res.Err)
	}
	if err == nil {
		t.Errorf("a VM name reaches another project's VM")
	}
	// ...and holds a lock file that is not that VM's lock.
	locks, _ := filepath.Glob(filepath.Join(dir, "*", "vms", "*.lock"))
	t.Logf("stray lock files beside VM dirs: %v", locks)
}

// P12: every library path loads into the base (or is refused with a hint),
// and a failed load does not poison a retry.
func TestRound4EveryLibraryLoads(t *testing.T) {
	h := newTestHost(t, t.TempDir())
	for _, p := range libraryPaths() {
		for attempt := 1; attempt <= 2; attempt++ {
			res, err := h.Run(bg, RunRequest{Project: "/proj", Session: "lib-" + p, Code: fmt.Sprintf("import _ %q", p)})
			switch {
			case err != nil:
				t.Logf("%-28s try %d: refused: %v", p, attempt, truncate(err.Error(), 120))
			case res.Err != nil:
				t.Logf("%-28s try %d: failed: %v", p, attempt, truncate(res.Err.Error(), 120))
			default:
				if attempt == 1 {
					continue
				}
			}
			if attempt == 1 && err == nil && res.Err == nil {
				break
			}
		}
	}
}

// P13: the budget is consulted only when a VM becomes resident; a resident
// VM that grows (declarations, imports) never makes anything evict.
func TestRound4BudgetNotEnforcedOnGrowth(t *testing.T) {
	t.Parallel()
	h, err := NewHost(Options{
		Dir: t.TempDir(), Client: NewClientID(), BudgetBytes: 80 << 20, Now: testNow,
		Probe: func() (HostMemory, error) { return HostMemory{Available: 8 << 30}, nil },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = h.Close() })
	mb := 4
	for _, name := range []string{"a", "b"} {
		_, _, err := h.New(bg, NewRequest{Project: "/proj", Name: name, MemoryMB: &mb})
		require.NoError(t, err)
		run(t, h, name, `v := 1`)
	}
	for k := range 5 {
		var src strings.Builder
		for i := 0; src.Len() < 31<<10; i++ {
			fmt.Fprintf(&src, "func g%d_%d() int { return %d }\n", k, i, i)
		}
		run(t, h, "a", src.String())
	}
	list, err := h.List("/proj")
	require.NoError(t, err)
	for _, v := range list.VMs {
		t.Logf("%s resident=%v charge=%d MB", v.Name, v.Resident, v.Charge>>20)
	}
	t.Logf("used %d MB of a %d MB budget", list.Used>>20, list.Budget>>20)
	require.LessOrEqual(t, list.Used, list.Budget, "resident VMs grew past the budget and nothing was evicted")
}

// P14: deeply nested input must fail cleanly, not with a Go fatal.
func TestRound4DeepNesting(t *testing.T) {
	h := newTestHost(t, t.TempDir())
	for _, depth := range []int{10000, 60000, 99000} {
		code := "x = " + strings.Repeat("(", depth) + "1" + strings.Repeat(")", depth)
		_, _ = h.Run(bg, RunRequest{Project: "/proj", Session: fmt.Sprint("d", depth), Code: "x := 0"})
		start := time.Now()
		res, err := h.Run(bg, RunRequest{Project: "/proj", Session: fmt.Sprint("d", depth), Code: code, Gas: maxGas})
		msg := ""
		if err != nil {
			msg = "refused: " + truncate(err.Error(), 100)
		} else if res.Err != nil {
			msg = "failed: " + truncate(res.Err.Error(), 100)
		} else {
			msg = fmt.Sprintf("ok gas=%d", res.Gas)
		}
		t.Logf("depth %d (%d KB): %s in %s", depth, len(code)>>10, msg, time.Since(start).Round(time.Millisecond))
	}
	for _, depth := range []int{20000, 40000} {
		code := "y = []any{" + strings.Repeat("[]any{", depth) + strings.Repeat("}", depth) + "}"
		_, _ = h.Run(bg, RunRequest{Project: "/proj", Session: fmt.Sprint("c", depth), Code: "var y []any"})
		start := time.Now()
		res, err := h.Run(bg, RunRequest{Project: "/proj", Session: fmt.Sprint("c", depth), Code: code, Gas: maxGas})
		msg := ""
		if err != nil {
			msg = "refused: " + truncate(err.Error(), 100)
		} else if res.Err != nil {
			msg = "failed: " + truncate(res.Err.Error(), 100)
		} else {
			msg = fmt.Sprintf("ok gas=%d", res.Gas)
		}
		t.Logf("composite depth %d (%d KB): %s in %s", depth, len(code)>>10, msg, time.Since(start).Round(time.Millisecond))
	}
}

// P3c: with persist false in its machine.json, reset: true on a VM under
// vms/ deletes it, instead of the refusal a persistent VM gets.
func TestRound4ResetDeletesMislabelledPersistentVM(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h1 := newTestHost(t, dir)
	persist := true
	_, _, err := h1.New(bg, NewRequest{Project: "/proj", Name: "kept", Persist: &persist})
	require.NoError(t, err)
	run(t, h1, "kept", `v := 1`)
	require.NoError(t, h1.Close())
	mfPath := filepath.Join(dir, projectKey("/proj"), "vms", "kept", "machine.json")
	b, err := os.ReadFile(mfPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mfPath, []byte(strings.Replace(string(b), `"persist": true`, `"persist": false`, 1)), 0o600))
	h2 := newTestHost(t, dir)
	_, err = h2.Run(bg, RunRequest{Project: "/proj", Name: "kept", Code: `1`, Reset: true})
	t.Logf("reset: err=%v", err)
	_, statErr := os.Stat(filepath.Join(dir, projectKey("/proj"), "vms", "kept"))
	require.NoError(t, statErr, "reset deleted a VM that lives under vms/")
}

// A top-level := that both declares and assigns is refused before it runs:
// the new name would come from a statement, and gno panics when a later var
// initializer depends on it (review round 3, S2).
func TestRound4MixedShortDeclIsRefused(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `x := 1`)
	_, err := h.Run(bg, RunRequest{Project: "/proj", Session: "s1", Code: `x, y := 2, 3`})
	var ref *refusal
	require.ErrorAs(t, err, &ref)
	require.Contains(t, err.Error(), "declare y with var first")
	run(t, h, "", `var y int
x, y = 2, 3
var z = y + 1`)
	require.Equal(t, "4", run(t, h, "", `z`).Value)
}

// chain.Emit does nothing: events would sit in Go heap outside the VM's cap.
func TestRound4EmitDoesNothing(t *testing.T) {
	t.Parallel()
	f := nativeResolver("chain", "emit")
	require.NotNil(t, f)
	f(nil) // touches nothing, not even the machine
	h := newTestHost(t, t.TempDir())
	res := run(t, h, "", `import "chain"
for i := 0; i < 1000; i++ { chain.Emit("e", "k", "v") }
"done"`)
	require.Equal(t, `"done"`, res.Value)
}

// A VM name is a name, never a path, at every entry point.
func TestRound4NamesAreNotPaths(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	for _, name := range []string{"./goals", "goals/", "../x", "../../k/vms/x", "a/b", ".", "..", "X"} {
		_, err := h.Run(bg, RunRequest{Project: "/proj", Name: name, Code: `1`})
		require.Error(t, err, name)
		require.Error(t, h.Drop(bg, "/proj", name, "evict"), name)
		_, err = h.Kept("/proj", name, "r")
		require.Error(t, err, name)
		_, _, err = h.Repair(bg, "/proj", name)
		require.Error(t, err, name)
	}
}
