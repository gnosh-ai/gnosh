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
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The user's /mem redact of a text that 5,000 steps and a goal's name hold,
// the plan's erasure at full size: the journal is written again, a fresh
// store is built from it batch by batch, and no file of mem holds the text
// after; the goal is named anew and every step keeps its place. It is
// skipped unless GNOMEM_SCALE is set; run it on the devbox. On 2026-10-30
// it took 15 s to write, 17 s to redact and 6 s to build the store. It
// found the erasure's own entry, 365 KB, past the cap on an append: the
// store's stamp could not be checked against a line that long, so the
// store was built afresh at every call, 76 calls over 13m45s.
func TestMemHostErasesAtScale(t *testing.T) {
	if os.Getenv("GNOMEM_SCALE") == "" {
		t.Skip("set GNOMEM_SCALE to run mem at scale; run it on the devbox")
	}
	t.Parallel()
	dir := t.TempDir()
	h := newTestMemHost(t, dir)
	const secret = "sk-scale-5f2e8a"
	start := time.Now()
	memOK(t, h, `mem.Goal("rotate `+secret+`")`)
	for g := 1; g <= 5; g++ {
		memOK(t, h, fmt.Sprintf(`var titles []string
for i := 1; i <= 1000; i++ {
	titles = append(titles, "use %s in place "+strconv.Itoa(i))
}
mem.Goal("batch %d").Steps(titles...)`, secret, g))
	}
	t.Logf("5,001 items written in %s", time.Since(start).Round(time.Millisecond))

	start = time.Now()
	res, err := h.call(context.Background(), memRequest{Code: `mem.Redact("` + secret + `")`, User: true})
	require.NoError(t, err, res.Output)
	require.Contains(t, res.Output, "redacted it in 5001 places")
	require.Equal(t, map[string]string{"rotate-sk-scale-5f2e8a": "forgotten-1"}, res.Renamed)
	t.Logf("redacted in %s", time.Since(start).Round(time.Millisecond))
	requireNowhere(t, dir, secret)

	start = time.Now()
	calls := 0
	for {
		calls++
		res, err = h.call(context.Background(), memRequest{Code: `print(mem.Check()); println(mem.Show("batch-5/1000")); println(mem.Show("forgotten-1"))`, Session: "s1"})
		if busy := (*busyError)(nil); errors.As(err, &busy) {
			continue
		}
		require.NoError(t, err, res.Output)
		break
	}
	t.Logf("the store built afresh in %s, %d calls", time.Since(start).Round(time.Millisecond), calls)
	require.Contains(t, res.Output, "use (redacted) in place 1000")
	require.Contains(t, res.Output, "rotate (redacted)")
	requireNowhere(t, dir, secret)
}
