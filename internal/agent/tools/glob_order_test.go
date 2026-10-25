package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The ripgrep path gives the newest file first, as the walker does, as
// Claude Code's Glob does and as the tool's description says: by path
// length it put any short name before the file just edited. A stand-in
// for ripgrep prints the two paths, the longer one the newer.
func TestGlobRipgrepGivesTheNewestFirst(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	newer := filepath.Join(dir, "deep", "inside", "edited.go")
	older := filepath.Join(dir, "a.go")
	require.NoError(t, os.MkdirAll(filepath.Dir(newer), 0o755))
	require.NoError(t, os.WriteFile(newer, []byte("package x\n"), 0o644))
	require.NoError(t, os.WriteFile(older, []byte("package x\n"), 0o644))
	day := time.Now().Add(-24 * time.Hour)
	require.NoError(t, os.Chtimes(older, day, day))

	cmd := exec.Command("printf", `a.go\0deep/inside/edited.go\0`) // NUL-separated, as rg --null prints
	cmd.Dir = dir
	matches, err := runRipgrep(cmd, dir, 100)
	require.NoError(t, err)
	require.Equal(t, []string{newer, older}, matches)
}
