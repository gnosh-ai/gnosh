package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGlobFilesScopedPrefixMatchesUnscoped(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mkfile := func(rel string) {
		full := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("x"), 0o644))
	}
	mkfile("a/b/one.go")
	mkfile("a/b/c/two.go")
	mkfile("a/other.txt")
	mkfile("z/three.go")

	got, _, err := globFiles(context.Background(), "a/**/*.go", root, 100)
	require.NoError(t, err)
	require.Len(t, got, 2)
	for _, p := range got {
		require.Contains(t, p, filepath.Join("a", "b"))
	}
}

func TestGlobFilesDoesNotFollowSymlinkEscape(t *testing.T) {
	t.Parallel()

	// Build a project dir with a symlink pointing outside it. With symlink
	// following disabled, the glob must not pick up files behind the link.
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.go"), []byte("x"), 0o644))

	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, "in.go"), []byte("x"), 0o644))
	link := filepath.Join(project, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	got, _, err := globFiles(context.Background(), "**/*.go", project, 100)
	require.NoError(t, err)
	for _, p := range got {
		require.NotContains(t, p, "secret.go", "glob followed a symlink out of the search root")
	}
}

func TestGlobFilesCapsResultsOnLargeTree(t *testing.T) {
	t.Parallel()

	// A tree with far more matches than the limit must still return at
	// most `limit` results and report truncation, regardless of which
	// backend (ripgrep or the doublestar fallback) runs.
	root := t.TempDir()
	for i := range 500 {
		dir := filepath.Join(root, "pkg")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		name := filepath.Join(dir, "file"+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+"_"+string(rune('0'+i%10))+".go")
		require.NoError(t, os.WriteFile(name, []byte("x"), 0o644))
	}

	got, truncated, err := globFiles(context.Background(), "**/*.go", root, 10)
	require.NoError(t, err)
	require.LessOrEqual(t, len(got), 10, "must not exceed limit")
	require.True(t, truncated, "should report truncation on an over-limit tree")
}

// runGlob runs the glob tool in workingDir and returns its text and
// whether it was an error.
func runGlob(t *testing.T, workingDir string, params GlobParams) (string, bool) {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := NewGlobTool(workingDir, config.ToolGlob{}).Run(context.Background(), fantasy.ToolCall{ID: "call-1", Name: GlobToolName, Input: string(input)})
	require.NoError(t, err)
	return resp.Content, resp.IsError
}

// mkfiles makes each file, with its directories, under root.
func mkfiles(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		full := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("x"), 0o644))
	}
}

// An agent on the AI box globbed "/usr/local/bin/crush*" and
// "/home/crush/.local/bin/*", and glob looked for those directories inside
// the project, failing each time. An absolute pattern searches where it
// points, whatever the path, in a directory named bin too.
func TestGlobAbsolutePatternSearchesWhereItPoints(t *testing.T) {
	t.Parallel()

	elsewhere := t.TempDir()
	mkfiles(t, elsewhere, "bin/klash", "bin/klash-bin", "bin/other")
	project := t.TempDir()
	mkfiles(t, project, "bin/klash-not-this")

	for _, params := range []GlobParams{
		{Pattern: filepath.Join(elsewhere, "bin", "klash*")},
		{Pattern: filepath.Join(elsewhere, "bin", "klash*"), Path: "bin"},
	} {
		out, isErr := runGlob(t, project, params)
		require.False(t, isErr, out)
		got := strings.Split(out, "\n")
		sort.Strings(got)
		require.Equal(t, []string{filepath.ToSlash(filepath.Join(elsewhere, "bin", "klash")), filepath.ToSlash(filepath.Join(elsewhere, "bin", "klash-bin"))}, got)
	}
	out, isErr := runGlob(t, project, GlobParams{Pattern: filepath.Join(elsewhere, "bin", "klash")})
	require.False(t, isErr, out)
	require.Equal(t, filepath.ToSlash(filepath.Join(elsewhere, "bin", "klash")), out, "a plain absolute path")
}

// A path to search that is not there is said plainly, in Claude Code's
// words, with the directory likely meant when a path leaves out the
// project's own name; a path that is a file is not a directory. A
// pattern whose own directory is not there matches nothing.
func TestGlobSaysASearchPathIsNotThere(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	project := filepath.Join(parent, "proj")
	mkfiles(t, project, "scripts/run.sh", "notes.txt")

	out, isErr := runGlob(t, project, GlobParams{Pattern: "*", Path: "nope"})
	require.True(t, isErr)
	require.Equal(t, "Directory does not exist: nope. Note: your current working directory is "+project+".", out)

	out, isErr = runGlob(t, project, GlobParams{Pattern: "*", Path: filepath.Join(parent, "scripts")})
	require.True(t, isErr)
	require.Equal(t, "Directory does not exist: "+filepath.Join(parent, "scripts")+". Note: your current working directory is "+project+". Did you mean "+filepath.Join(project, "scripts")+"?", out)

	out, isErr = runGlob(t, project, GlobParams{Pattern: "*", Path: filepath.Join(parent, "nothing")})
	require.True(t, isErr)
	require.NotContains(t, out, "Did you mean", "nothing like it in the project")

	out, isErr = runGlob(t, project, GlobParams{Pattern: "*", Path: filepath.Join(parent, "notes.txt")})
	require.True(t, isErr)
	require.NotContains(t, out, "Did you mean", "a file is no directory to search")

	out, isErr = runGlob(t, project, GlobParams{Pattern: "*", Path: "notes.txt"})
	require.True(t, isErr)
	require.Equal(t, "Path is not a directory: notes.txt", out)

	out, isErr = runGlob(t, project, GlobParams{Pattern: "missing/*.go"})
	require.False(t, isErr)
	require.Equal(t, "No files found", out)
	out, isErr = runGlob(t, project, GlobParams{Pattern: filepath.Join(parent, "missing", "*.go")})
	require.False(t, isErr)
	require.Equal(t, "No files found", out)
}

// ripgrep's matches are filtered for hidden and ignored directories below
// the search root only: a project under a directory named out (or build,
// bin, logs) had every file dropped.
func TestRipgrepMatchesKeepARootUnderAnIgnoredName(t *testing.T) {
	t.Parallel()

	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := filepath.Join(t.TempDir(), "out", "proj")
	mkfiles(t, root, "a.go", "node_modules/x.go")
	cmd := exec.Command(rg, "--files", "--null", "--glob", "/**/*.go")
	cmd.Dir = root
	got, err := runRipgrep(cmd, root, 100)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "a.go")}, got)
}
