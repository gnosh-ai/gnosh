// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

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
	write("system.slice/klash.service", "memory.max", "max\n")
	write("system.slice/klash.service", "memory.current", "1073741824\n")
	write("system.slice", "memory.max", "8589934592\n")
	write("system.slice", "memory.current", "6442450944\n")

	limit, headroom, ok := cgroupHeadroom(root, "/system.slice/klash.service")
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
}
