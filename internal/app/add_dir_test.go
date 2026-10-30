package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/agenttest"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// promptCoordinator counts the times the main agents' prompts are built
// again.
type promptCoordinator struct {
	agenttest.NopCoordinator
	rebuilt int
}

func (c *promptCoordinator) RebuildSystemPrompts(context.Context) error {
	c.rebuilt++
	return nil
}

// /add-dir through the app, in Claude Code's words: a directory is added
// for the session, to the permissions and the agents' prompts; one that
// is not there, a file, the working directory or a directory already in
// reach is not, and says why.
func TestAddDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workingDir := filepath.Join(root, "proj")
	other := filepath.Join(root, "other")
	for _, dir := range []string{filepath.Join(workingDir, "sub"), filepath.Join(other, "deep")} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}
	file := filepath.Join(root, "notes.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	store := config.NewTestStoreIn(&config.Config{}, workingDir)
	perms := permission.New(permission.Options{WorkingDir: workingDir})
	coord := &promptCoordinator{}
	a := &App{config: store, Permissions: perms, AgentCoordinator: coord}
	add := func(path string) (string, bool) {
		said, added, err := a.AddDir(t.Context(), path)
		require.NoError(t, err)
		return said, added
	}

	said, added := add("../other")
	require.True(t, added)
	require.Equal(t, "Added "+other+" as a working directory for this session.", said)
	require.Equal(t, []string{other}, store.AddedDirs())
	require.Equal(t, 1, coord.rebuilt)
	ok, err := perms.Request(t.Context(), permission.CreatePermissionRequest{SessionID: "s1", ToolName: "view", Action: "read", Path: filepath.Join(other, "deep", "a.go")})
	require.NoError(t, err)
	require.True(t, ok, "read there without asking")

	for path, want := range map[string]string{
		"":                              "Please provide a directory path.",
		"  ":                            "Please provide a directory path.",
		"../missing":                    "Path " + filepath.Join(root, "missing") + " was not found.",
		"~/no-such-dir-for-gnosh-tests": "Path " + filepath.Join(home.Dir(), "no-such-dir-for-gnosh-tests") + " was not found.",
		file:                            file + " is not a directory. Did you mean to add the parent directory " + root + "?",
		".":                             ". is already the current working directory.",
		"sub":                           "sub is already accessible within the current working directory " + workingDir + ".",
		other:                           other + " is already added as a working directory.",
		filepath.Join(other, "deep"):    filepath.Join(other, "deep") + " is already accessible within the additional working directory " + other + ".",
	} {
		said, added := add(path)
		require.False(t, added, path)
		require.Equal(t, want, said, path)
	}
	require.Equal(t, []string{other}, store.AddedDirs(), "nothing more added")
	require.Equal(t, 1, coord.rebuilt)
}
