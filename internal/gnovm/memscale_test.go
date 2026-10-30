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
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMemRealmScale measures mem at the size GNOMEM_SCALE gives (items), in
// the shape a long-used mem has: most goals closed, a thousand open, and one
// goal of a thousand steps. It builds the mem through Apply, opens it again,
// and times the calls a session makes. It is skipped unless GNOMEM_SCALE is
// set; run it on the devbox.
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

	// Goals of 10 steps each, all done but the newest thousand, applied 100
	// goals per call; then the goal of a thousand steps.
	const steps, open, big = 10, 1000, 1000
	goals := (n - big - 1) / (steps + 1)
	var ops []memOp
	flush := func() {
		require.NoError(t, r.applyNow(ops, time.Now().Unix()))
		ops = ops[:0]
	}
	start = time.Now()
	for g := 1; g <= goals; g++ {
		name := "goal-" + strconv.Itoa(g)
		ops = append(ops, memOp{Kind: "add", Name: name, New: "goal number " + strconv.Itoa(g), By: "user"})
		for s := 1; s <= steps; s++ {
			ops = append(ops, memOp{Kind: "add", Name: name + "/" + strconv.Itoa(s), New: "step " + strconv.Itoa(s), By: "user"})
		}
		if g <= goals-open {
			for s := 1; s <= steps; s++ {
				ops = append(ops, memOp{Kind: "set", Name: name + "/" + strconv.Itoa(s), Field: "status", New: "done"})
			}
			ops = append(ops, memOp{Kind: "set", Name: name, Field: "status", New: "done"})
		}
		if g%100 == 0 {
			flush()
		}
	}
	ops = append(ops, memOp{Kind: "add", Name: "big", New: "a goal of a thousand steps", By: "user"})
	for s := 1; s <= big; s++ {
		ops = append(ops, memOp{Kind: "add", Name: "big/" + strconv.Itoa(s), New: "step " + strconv.Itoa(s), By: "user"})
	}
	flush()
	items := goals*(steps+1) + big + 1
	build := time.Since(start)
	t.Logf("built %d items (%d goals open) in %v (%.0f items/s), store %s", items, min(goals, open)+1, build,
		float64(items)/build.Seconds(), dirSize(t, dir))

	start = time.Now()
	require.NoError(t, r.close())
	r, err = openMemRealm(b, dir)
	require.NoError(t, err)
	defer r.close()
	t.Logf("reopen (same base): %v", time.Since(start))

	timed := func(what, code string) string {
		start := time.Now()
		call, err := r.run(code, "sess-1")
		require.NoError(t, err, call.Output)
		t.Logf("%s: %v, %d bytes out, %d Ops", what, time.Since(start), len(call.Output), len(call.Ops))
		return call.Output
	}
	mid := "goal-" + strconv.Itoa(goals-min(goals, open)/2)
	timed("a call adding a step", `mem.Steps("`+mid+`", "one more")`)
	timed("a call adding a step", `mem.Steps("`+mid+`", "and one more")`)
	require.Contains(t, timed("Show(one goal)", `println(mem.Show("`+mid+`"))`), mid+"/12")
	timed("Show()", `println(mem.Show())`)
	timed("JSON()", `println(mem.JSON())`)
	timed("Show(the goal of a thousand steps)", `println(mem.Show("big"))`)
	timed("Next(it)", `println(mem.Next("big"))`)
	timed("Done(its step 1)", `mem.Done("big/1", "ok")`)
	timed("Done(its step 999)", `mem.Done("big/999", "ok")`)
	timed("Drop(it, with 998 open steps)", `mem.Drop("big", "measured")`)
	timed("Forget(it)", `mem.Forget("big")`)
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
