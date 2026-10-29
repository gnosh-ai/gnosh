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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRealWorker runs VMs in a real worker process: a klash binary built
// from this tree, never the test binary. It is skipped unless asked for;
// run it on the devbox, with both binaries given the same build id so their
// handshake matches, and a cap on processes:
//
//	ID=-X=github.com/charmbracelet/crush/internal/version.BuildID=gnovm-it
//	go build -ldflags "$ID" -o /root/scratch/klash-worker .
//	GNOVM_WORKER_BIN=/root/scratch/klash-worker systemd-run --scope -p TasksMax=64 -- \
//	  go test -ldflags "$ID" -run TestRealWorker -v ./internal/gnovm/
//
// (Not parallel: the client is process-wide.)
func TestRealWorker(t *testing.T) {
	bin := os.Getenv("GNOVM_WORKER_BIN")
	if bin == "" {
		t.Skip("set GNOVM_WORKER_BIN to a klash binary built from this tree")
	}
	dir := t.TempDir()
	c, err := Acquire(ClientOptions{Options: Options{Dir: dir}, Binary: bin})
	require.NoError(t, err)
	defer c.Release()
	ctx := context.Background()
	runOK := func(code, req string) *RunResult {
		t.Helper()
		res, err := c.Run(ctx, RunRequest{Project: "/p", Session: "s", Code: code, Req: req})
		require.NoError(t, err, code)
		require.NoError(t, res.Err, code)
		return res
	}

	runOK(`import "strings"
x := strings.Repeat("ab", 2)`, "r1")
	require.Equal(t, `"abab"`, runOK(`x`, "").Value)
	kept, err := c.Kept(ctx, "/p", ScratchName("s"), "r1")
	require.NoError(t, err)
	require.True(t, kept)
	_, err = os.Stat(filepath.Join(dir, "worker.log"))
	require.NoError(t, err, "the worker's stderr goes to its log")

	// A metered run of more than 15 s is not taken for a stuck worker:
	// heartbeats keep it alive until its gas runs out.
	start := time.Now()
	res, err := c.Run(ctx, RunRequest{Project: "/p", Session: "s", Code: `for i := 0; ; i++ {}`, Gas: 30_000_000_000})
	require.NoError(t, err)
	var lim *limitError
	require.ErrorAs(t, res.Err, &lim)
	t.Logf("long run: %s, %s", time.Since(start).Round(time.Second), lim.kind)
	require.Empty(t, c.Deaths(), "no worker was killed")

	// The worker dies (here: killed); the memory-only VM is on disk, so the
	// next worker rebuilds it.
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	conn.kill("killed by the test")
	<-conn.dead
	require.Equal(t, `"abab"`, runOK(`x`, "").Value)
	deaths := c.Deaths()
	require.Len(t, deaths, 1)
	require.True(t, strings.Contains(deaths[0], "killed by the test"), deaths[0])
}
