package tools

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// deniedFS answers as a filesystem where everything under closed is
// refused, the way a directory with no permissions behaves.
func deniedFS(closed string) (func(string) (fs.FileInfo, error), func(string) ([]os.DirEntry, error)) {
	under := func(p string) bool {
		return p == closed || strings.HasPrefix(p, closed+string(filepath.Separator))
	}
	lstat := func(p string) (fs.FileInfo, error) {
		if under(p) {
			return nil, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrPermission}
		}
		return nil, nil
	}
	readDir := func(p string) ([]os.DirEntry, error) {
		if under(p) {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrPermission}
		}
		return nil, nil
	}
	return lstat, readDir
}

// A path a tool cannot reach because a directory above it is closed says
// which directory refused, and that nothing can be told about what is
// under it. The bare "permission denied" named the path asked for, which
// reads as though it is there and merely locked: on the AI box the agent
// read that as a sudo problem and spent its turn hunting for sudo, when
// /root/renders is not on that machine at all.
func TestDeniedPath(t *testing.T) {
	t.Parallel()

	closed := filepath.Join("/root")
	inside := filepath.Join(closed, "renders")
	lstat, readDir := deniedFS(closed)

	msg := deniedPathWith(inside, lstat, readDir)
	require.True(t, strings.HasPrefix(msg, "Permission denied: "+closed+" cannot be read"),
		"the directory that refused is the one named, not the path asked for: %s", msg)
	require.Contains(t, msg, "including "+inside, "and the path asked for is named as unreachable")
	require.Contains(t, msg, "do not assume the path exists")

	deeper := deniedPathWith(filepath.Join(inside, "video_a_v6.mp4"), lstat, readDir)
	require.True(t, strings.HasPrefix(deeper, "Permission denied: "+closed+" cannot be read"),
		"however deep the path, the closed directory is the one named: %s", deeper)

	atClosed := deniedPathWith(closed, lstat, readDir)
	require.Contains(t, atClosed, closed)
	require.NotContains(t, atClosed, "including", "the closed directory is the path asked for")

	// Nothing to explain when the path is readable, or missing, or empty.
	ok := func(string) (fs.FileInfo, error) { return nil, nil }
	missing := func(p string) (fs.FileInfo, error) {
		return nil, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
	}
	require.Equal(t, "", deniedPathWith(inside, ok, readDir), "a readable path is not denied")
	require.Equal(t, "", deniedPathWith(inside, missing, readDir), "a missing path is not a denied one")
	// Nothing is asked about an empty path, even where everything is
	// refused: there is no path to explain.
	denyAll := func(p string) (fs.FileInfo, error) {
		return nil, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrPermission}
	}
	denyAllDir := func(p string) ([]os.DirEntry, error) {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrPermission}
	}
	require.Equal(t, "", deniedPathWith("", denyAll, denyAllDir))
}

// The same over the real filesystem, where the test user can be refused.
func TestDeniedPathOnDisk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root is refused nothing, so nothing is denied")
	}
	t.Parallel()

	closed := filepath.Join(t.TempDir(), "closed")
	require.NoError(t, os.Mkdir(closed, 0o700))
	inside := filepath.Join(closed, "renders")
	require.NoError(t, os.Mkdir(inside, 0o755))
	require.NoError(t, os.Chmod(closed, 0o000))
	t.Cleanup(func() { _ = os.Chmod(closed, 0o700) })

	msg := deniedPath(inside)
	require.Contains(t, msg, closed)
	require.Contains(t, msg, inside)
	require.Equal(t, "", deniedPath(t.TempDir()), "a readable directory is not denied")
}
