package app

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// The workspace's permission service is made with what the config and the
// flags say. --permission-mode acceptEdits reaches it as the mode, which
// lets edits in the project run unasked, not as edit tools allowed
// everywhere.
func TestPermissionOptions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := config.NewTestStoreIn(&config.Config{Permissions: &config.Permissions{AllowedTools: []string{"view"}}}, dir)
	store.Overrides().AcceptEdits = true
	store.Overrides().AddedDirs = []string{"/other"}
	store.Overrides().SkipPermissionRequests = true
	require.Equal(t, permission.Options{
		WorkingDir:     dir,
		Skip:           true,
		AllowedTools:   []string{"view"},
		AddedDirs:      []string{"/other"},
		AcceptEdits:    true,
		ProtectedPaths: []string{config.SessionEnvDir()},
	}, permissionOptions(store))
}
