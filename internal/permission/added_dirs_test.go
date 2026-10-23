package permission

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Reading or listing under a directory added with --add-dir needs no
// permission, as under the working directory; a sibling that only shares
// its name's start, or a path that climbs out, is not in it.
func TestReadsInAddedDirsNeedNoPermission(t *testing.T) {
	t.Parallel()

	added := t.TempDir()
	s := NewPermissionService(t.TempDir(), false, nil, added).(*permissionService)

	require.True(t, s.inAddedDir(added))
	require.True(t, s.inAddedDir(filepath.Join(added, "pkg", "a.go")))
	require.False(t, s.inAddedDir(added+"-other"))
	require.False(t, s.inAddedDir(filepath.Join(added, "..", "elsewhere")))
	require.False(t, NewPermissionService(t.TempDir(), false, nil).(*permissionService).inAddedDir(added))

	for _, action := range []string{"read", "list"} {
		ok, err := s.Request(context.Background(), CreatePermissionRequest{
			SessionID: "s1",
			ToolName:  "view",
			Action:    action,
			Path:      filepath.Join(added, "pkg", "a.go"),
		})
		require.NoError(t, err)
		require.True(t, ok, action)
	}
}
