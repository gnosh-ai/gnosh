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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMeminfo(t *testing.T) {
	t.Parallel()
	data := []byte("MemTotal:       131596288 kB\nMemFree:         1048576 kB\nMemAvailable:   20971520 kB\nBuffers:            1024 kB\n")
	total, avail, ok := parseMeminfo(data)
	require.True(t, ok)
	require.Equal(t, uint64(131596288*1024), total)
	require.Equal(t, uint64(20971520*1024), avail)

	_, _, ok = parseMeminfo([]byte("MemTotal: 100 kB\n"))
	require.False(t, ok, "no MemAvailable")
}

func TestCgroupV2Path(t *testing.T) {
	t.Parallel()
	require.Equal(t, "/user.slice/user-1000.slice/session-3.scope",
		cgroupV2Path([]byte("0::/user.slice/user-1000.slice/session-3.scope\n")))
	require.Equal(t, "", cgroupV2Path([]byte("12:memory:/docker/abc\n")), "cgroup v1 only")
}

func TestCgroupHeadroom(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(rel, name, body string) {
		dir := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	// The leaf has no limit; its parent allows 8 GiB with 6 GiB used, the
	// root's child slice allows 64 GiB with 10 GiB used: the parent is tightest.
	write("system.slice/gnosh.service", "memory.max", "max\n")
	write("system.slice/gnosh.service", "memory.current", "1073741824\n")
	write("system.slice", "memory.max", "8589934592\n")
	write("system.slice", "memory.current", "6442450944\n")

	limit, headroom, ok := cgroupHeadroom(root, "/system.slice/gnosh.service")
	require.True(t, ok)
	require.Equal(t, uint64(8589934592), limit)
	require.Equal(t, uint64(8589934592-6442450944), headroom)

	// Over the limit: no headroom, not a negative number.
	write("tight", "memory.max", "100\n")
	write("tight", "memory.current", "200\n")
	_, headroom, ok = cgroupHeadroom(root, "/tight")
	require.True(t, ok)
	require.Zero(t, headroom)

	_, _, ok = cgroupHeadroom(root, "/nolimits")
	require.False(t, ok)

	// Inactive page cache is reclaimable, so it is not counted as used.
	write("cached", "memory.max", "1000\n")
	write("cached", "memory.current", "900\n")
	write("cached", "memory.stat", "anon 100\nfile 800\ninactive_file 700\nactive_file 100\n")
	limit, headroom, ok = cgroupHeadroom(root, "/cached")
	require.True(t, ok)
	require.Equal(t, uint64(1000), limit)
	require.Equal(t, uint64(1000-(900-700)), headroom)

	// memory.high, when lower than memory.max (or alone), is the limit.
	write("high", "memory.max", "max\n")
	write("high", "memory.high", "500\n")
	write("high", "memory.current", "100\n")
	limit, headroom, ok = cgroupHeadroom(root, "/high")
	require.True(t, ok)
	require.Equal(t, uint64(500), limit)
	require.Equal(t, uint64(400), headroom)
}
