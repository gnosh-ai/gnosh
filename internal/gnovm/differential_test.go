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
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	gno "github.com/gnolang/gno/gnovm/pkg/gnolang"
	"github.com/stretchr/testify/require"
)

// dumpGlobals renders every package-level value of a VM, by name.
func dumpGlobals(t *testing.T, v *vm) string {
	t.Helper()
	pb := v.m.Package.GetBlock(v.m.Store)
	var b strings.Builder
	for i, name := range v.pn.GetBlockNames() {
		if i >= len(pb.Values) {
			break
		}
		tv := pb.Values[i]
		if tv.T != nil && (tv.T.Kind() == gno.FuncKind || tv.T.Kind() == gno.TypeKind || tv.T.Kind() == gno.PackageKind) {
			fmt.Fprintf(&b, "%s: %s\n", name, tv.T.Kind())
			continue
		}
		fmt.Fprintf(&b, "%s = %s\n", name, renderValue(tv))
	}
	return b.String()
}

// The state of a VM must be the same live and rebuilt from its journal,
// whatever the calls were: random workloads at a small cap, with failures,
// rebuilds and evictions midway.
func TestDifferentialReplay(t *testing.T) {
	t.Parallel()
	for seed := uint64(1); seed <= 4; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			h := newTestHost(t, dir)
			persist := true
			mb := 2
			_, _, err := h.New(context.Background(), NewRequest{Project: "/proj", Name: "diff", Persist: &persist, MemoryMB: &mb})
			require.NoError(t, err)
			run(t, h, "diff", `import "strings"
n := 0
s := []int{}
m := map[string]int{}
words := ""
type point struct{ X, Y int }
var pts []point
func bump(k int) int { n += k; return n }`)

			rng := rand.New(rand.NewPCG(seed, seed*7919))
			steps := []func(i int) string{
				func(i int) string { return fmt.Sprintf("n += %d", rng.IntN(100)) },
				func(i int) string { return fmt.Sprintf("s = append(s, n+%d)", i) },
				func(i int) string { return fmt.Sprintf("m[\"k%d\"] = %d", rng.IntN(20), i) },
				func(i int) string { return fmt.Sprintf("delete(m, \"k%d\")", rng.IntN(20)) },
				func(i int) string { return fmt.Sprintf("words = strings.Repeat(\"ab\", %d)", rng.IntN(50)) },
				func(i int) string { return fmt.Sprintf("pts = append(pts, point{%d, n})", i) },
				func(i int) string { return fmt.Sprintf("bump(%d)", rng.IntN(10)) },
				func(i int) string { return fmt.Sprintf("v%d := len(s) + len(m)", i) },
				func(i int) string { return fmt.Sprintf("func f%d() int { return n + %d }", i, i) },
				// Garbage, so the 2 MB cap collects.
				func(i int) string { return "for j := 0; j < 200; j++ { _ = make([]byte, 1000) }" },
				// Failures: a panic after a change, and a compile error.
				func(i int) string { return "n = -1\npanic(\"no\")" },
				func(i int) string { return "undefinedName + 1" },
				// Pure and impure evaluations.
				func(i int) string { return "len(s) + n" },
				func(i int) string { return fmt.Sprintf("copy(s, []int{%d})", i) },
			}
			for i := 0; i < 150; i++ {
				code := steps[rng.IntN(len(steps))](i)
				_, err := h.Run(context.Background(), RunRequest{Project: "/proj", Name: "diff", Code: code})
				require.NoError(t, err, code)
				if i%50 == 49 {
					require.NoError(t, h.Drop(context.Background(), "/proj", "diff", "evict"))
				}
			}
			res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Name: "diff", Code: `n`})
			require.NoError(t, err)
			require.NoError(t, res.Err)
			s := h.vms[projectKey("/proj")+"/diff"]
			live := dumpGlobals(t, s.res)
			for _, name := range []string{"n = ", "s = ", "m = ", "words = ", "pts = ", "bump: "} {
				require.Contains(t, live, name, "the dump compares real state")
			}

			// A fresh host replays the journal from nothing.
			h2 := newTestHost(t, dir)
			res, err = h2.Run(context.Background(), RunRequest{Project: "/proj", Name: "diff", Code: `n`})
			require.NoError(t, err)
			require.NoError(t, res.Err)
			replayed := dumpGlobals(t, h2.vms[projectKey("/proj")+"/diff"].res)
			require.Equal(t, live, replayed)
		})
	}
}
