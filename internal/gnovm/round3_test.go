// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

package gnovm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The cases review round 3 found the design would get wrong.

// An import is bound package-wide, so a statement, an evaluation, a later
// declaration and a later call all see it.
func TestImportIsSeenEverywhere(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	res := run(t, h, "", `import "strings"
s := strings.ToUpper("x")
strings.ToLower("Y") + s`)
	require.Equal(t, `"yX"`, res.Value)
	run(t, h, "", `func up(v string) string { return strings.ToUpper(v) }
var shouted = up("later")`)
	require.Equal(t, `"LATER"`, run(t, h, "", `shouted`).Value)
	require.Equal(t, `"ok"`, run(t, h, "", `strings.TrimSpace(" ok ")`).Value)
	// Importing it again is harmless.
	require.Equal(t, `"A"`, run(t, h, "", `import "strings"
strings.ToUpper("a")`).Value)
}

// A file's var initializer may depend on a name a top-level := declared:
// the := runs as a var declaration (gno panics otherwise).
func TestVarDependsOnShortDeclaration(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `x := 1`)
	run(t, h, "", `var y = x + 1`)
	require.Equal(t, "2", run(t, h, "", `y`).Value)
	run(t, h, "", `func gx() int { return x }
var gy = gx() * 10`)
	require.Equal(t, "10", run(t, h, "", `gy`).Value)
	// A := that reuses a name still assigns, as gno runs it.
	run(t, h, "", `x := 5`)
	require.Equal(t, "5", run(t, h, "", `x`).Value)
	// And a redeclaration in one input is refused before anything runs.
	_, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: `z := 1
func z() {}`})
	require.Error(t, err)
	require.Regexp(t, `z (is already declared|redeclared)`, err.Error())
}

// A trailing expression that changes state through a builtin is journaled,
// so a rebuild keeps the change.
func TestMutatingBuiltinsAreJournaled(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `m := map[string]int{"a": 1, "b": 2}
s := make([]int, 1, 4)
d := []int{0, 0}`)
	require.True(t, run(t, h, "", `delete(m, "a")`).Journaled)
	require.True(t, run(t, h, "", `append(s, 7)`).Journaled)
	require.True(t, run(t, h, "", `copy(d, []int{3, 4})`).Journaled)
	require.False(t, run(t, h, "", `len(m) + cap(s)`).Journaled, "len and cap change nothing")

	require.NoError(t, h.Drop(context.Background(), "/proj", ScratchName("s1"), "evict"))
	require.Equal(t, "1", run(t, h, "", `len(m)`).Value)
	require.Equal(t, "[0, 7]", run(t, h, "", `s[:2]`).Value)
	require.Equal(t, "[3, 4]", run(t, h, "", `d`).Value)
}

// A call to a redeclared builtin name is not pure.
func TestShadowedBuiltinIsJournaled(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: `var hits int
func min(a, b int) int { hits++; return a }`})
	require.NoError(t, err)
	if res.Err != nil {
		t.Skipf("gno does not let a package redeclare min: %v", res.Err)
	}
	require.True(t, run(t, h, "", `min(1, 2)`).Journaled)
	require.NoError(t, h.Drop(context.Background(), "/proj", ScratchName("s1"), "evict"))
	require.Equal(t, "1", run(t, h, "", `hits`).Value)
}

// A typo in an expression costs no rebuild; a failure that ran code does.
func TestCompileErrorInExpressionKeepsTheVM(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `n := 3`)
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: `nope + 1`})
	require.NoError(t, err)
	require.Error(t, res.Err)
	list, err := h.List("/proj")
	require.NoError(t, err)
	require.True(t, list.VMs[0].Resident, "still resident after a compile error in an expression")
	require.Equal(t, "3", run(t, h, "", `n`).Value)
}

// What a VM is charged for its declarations covers the heap they keep.
// Not parallel: other tests' allocations would muddy the heap measure.
func TestDeclarationChargeCoversHeap(t *testing.T) {
	h := newTestHost(t, t.TempDir())
	var src strings.Builder
	for i := range 120 {
		fmt.Fprintf(&src, "func f%d(a, b int) int {\n\tif a > b {\n\t\treturn a - b + %d\n\t}\n\tfor i := 0; i < 3; i++ {\n\t\ta += i * b\n\t}\n\treturn a\n}\n", i, i)
	}
	run(t, h, "", `warm := 1`)
	before := heapAlloc()
	res := run(t, h, "", src.String())
	after := heapAlloc()
	grew := int64(after) - int64(before)
	require.True(t, res.Journaled)
	charged := declHeapFactor * int64(src.Len())
	t.Logf("%d bytes of declarations: heap +%d (%.0fx), charged %d", src.Len(), grew, float64(grew)/float64(src.Len()), charged)
	require.LessOrEqual(t, grew, charged)
	runtime.KeepAlive(h)
}

// Declaration limits are checked before anything runs.
func TestDeclarationLimitPerCall(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	var src strings.Builder
	for i := 0; src.Len() <= maxDeclPerEntry; i++ {
		fmt.Fprintf(&src, "func g%d() int { return %d }\n", i, i)
	}
	_, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: src.String()})
	var ref *refusal
	require.ErrorAs(t, err, &ref)
	require.Contains(t, err.Error(), "Split it up")
}

// A replay runs in slices within the caller's budget, keeping what it built,
// so every call makes progress and a long journal still finishes.
func TestReplayIsResumable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h1 := newTestHost(t, dir)
	persist := true
	_, _, err := h1.New(context.Background(), NewRequest{Project: "/proj", Name: "slow", Persist: &persist})
	require.NoError(t, err)
	run(t, h1, "slow", `total := 0`)
	for i := 1; i <= 4; i++ {
		run(t, h1, "slow", fmt.Sprintf("total += %d", i))
	}
	require.NoError(t, h1.Close())

	// Pretend each entry took 2 s to run: 3x that per entry is more than
	// the 5 s budget of a default call, so each call replays one entry.
	path := filepath.Join(dir, projectKey("/proj"), "vms", "slow", "journal.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i := 1; i < len(lines); i++ {
		var e entry
		require.NoError(t, json.Unmarshal([]byte(lines[i]), &e))
		e.WallNS = int64(2 * time.Second)
		b, err := json.Marshal(e)
		require.NoError(t, err)
		lines[i] = string(b)
	}
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))

	h2 := newTestHost(t, dir)
	busy := 0
	for {
		res, err := h2.Run(context.Background(), RunRequest{Project: "/proj", Name: "slow", Code: `total`})
		var b *busyError
		if err != nil && strings.Contains(err.Error(), "call again") {
			require.ErrorAs(t, err, &b)
			busy++
			require.Less(t, busy, 10)
			continue
		}
		require.NoError(t, err)
		require.NoError(t, res.Err)
		require.Equal(t, "10", res.Value)
		break
	}
	require.Equal(t, 4, busy, "one entry of progress per call")
}

// A call that ran too long to replay is shown but not kept; a VM whose kept
// calls ran too long in all keeps no more. (Not parallel: it shortens the
// package's limits while it runs.)
func TestReplayCostLimits(t *testing.T) {
	saveEntry, saveReplay := maxEntryWall, maxReplayWall
	t.Cleanup(func() { maxEntryWall, maxReplayWall = saveEntry, saveReplay })
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `n := 0`)

	maxEntryWall = time.Nanosecond
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: `n = 5
n`})
	require.NoError(t, err)
	require.Equal(t, "5", res.Value, "the result is shown")
	require.Error(t, res.Err)
	require.Contains(t, res.Err.Error(), "not kept")
	require.Equal(t, "0", run(t, h, "", `n`).Value, "but not kept")

	maxEntryWall = saveEntry
	maxReplayWall = time.Nanosecond
	_, err = h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Code: `n = 7`})
	require.Error(t, err)
	require.Contains(t, err.Error(), "keeps nothing more")
	require.Equal(t, "0", run(t, h, "", `n`).Value, "expressions still answer")
}
