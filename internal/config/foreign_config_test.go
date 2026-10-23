package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A project config of your own is read as before.
func TestCheckProjectConfigOwnersAcceptsYourOwn(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, "klash.json"), []byte(`{}`), 0o644))
	require.NoError(t, checkProjectConfigOwners(project))
}

// A project config that someone other than the project directory's owner
// owns is skipped by the lookup. On the AI box a klash.json written as
// root into a directory of the crush user's was skipped that way, so its
// disabled_tools did not apply and the run there had every tool. Loading
// now fails, naming the file and the fix, and the same file given back to
// its owner loads.
func TestLoadRefusesProjectConfigOwnedBySomeoneElse(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("giving a file to another user needs root")
	}
	t.Setenv("CRUSH_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("CRUSH_GLOBAL_DATA", t.TempDir())
	resetProviderState()
	t.Cleanup(resetProviderState)

	project := t.TempDir()
	path := filepath.Join(project, "klash.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"options":{"disabled_tools":["bash"]}}`), 0o644))
	require.NoError(t, os.Chown(path, 65534, 65534))

	_, err := Load(project, t.TempDir(), false)
	require.Error(t, err)
	require.Contains(t, err.Error(), path)
	require.Contains(t, err.Error(), "chown root "+path)

	require.NoError(t, os.Chown(path, 0, 0))
	store, err := Load(project, t.TempDir(), false)
	require.NoError(t, err)
	require.Contains(t, store.Config().Options.DisabledTools, "bash")
}
