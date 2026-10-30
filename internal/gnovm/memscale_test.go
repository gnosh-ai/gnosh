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
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMemRealmScale measures mem at the size GNOMEM_SCALE gives (items):
// building it through Apply, opening it again, a call, and showing one goal.
// It is skipped unless GNOMEM_SCALE is set; run it on the devbox.
func TestMemRealmScale(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("GNOMEM_SCALE"))
	if n == 0 {
		t.Skip("set GNOMEM_SCALE to a number of items")
	}
	dir := t.TempDir()
	start := time.Now()
	b := newBase()
	r, err := openMemRealm(b, dir)
	require.NoError(t, err)
	t.Logf("open, new base: %v", time.Since(start))

	// Goals of 10 steps each, applied 1100 Ops (100 goals) per call.
	const steps, perCall = 10, 100
	start = time.Now()
	var ops []memOp
	goals := n / (steps + 1)
	for g := 1; g <= goals; g++ {
		name := "goal-" + strconv.Itoa(g)
		ops = append(ops, memOp{Kind: "add", Name: name, New: "goal number " + strconv.Itoa(g), By: "user"})
		for s := 1; s <= steps; s++ {
			ops = append(ops, memOp{Kind: "add", Name: name + "/" + strconv.Itoa(s), New: "step " + strconv.Itoa(s), By: "user"})
		}
		if g%perCall == 0 || g == goals {
			require.NoError(t, r.apply(ops, time.Now().Unix()))
			ops = ops[:0]
		}
	}
	build := time.Since(start)
	t.Logf("built %d items in %v (%.0f items/s), store %s", goals*(steps+1), build,
		float64(goals*(steps+1))/build.Seconds(), dirSize(t, dir))

	start = time.Now()
	require.NoError(t, r.close())
	r, err = openMemRealm(b, dir)
	require.NoError(t, err)
	defer r.close()
	t.Logf("reopen (same base): %v", time.Since(start))

	mid := "goal-" + strconv.Itoa(goals/2)
	for i := 0; i < 3; i++ {
		start = time.Now()
		call, err := r.run(`mem.Steps("`+mid+`", "one more")`, "sess-1")
		require.NoError(t, err, call.Output)
		t.Logf("a call adding a step: %v", time.Since(start))
	}
	start = time.Now()
	call, err := r.run(`println(mem.Show("`+mid+`"))`, "sess-1")
	require.NoError(t, err, call.Output)
	require.Contains(t, call.Output, mid+"/13")
	t.Logf("Show(one goal): %v", time.Since(start))
}

func dirSize(t *testing.T, dir string) string {
	var total int64
	require.NoError(t, filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return err
	}))
	return strconv.FormatFloat(float64(total)/(1<<20), 'f', 1, 64) + " MB"
}
