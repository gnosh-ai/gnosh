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

	for tool, action := range map[string]string{"view": "read", "ls": "list"} {
		ok, err := s.Request(context.Background(), CreatePermissionRequest{
			SessionID: "s1",
			ToolName:  tool,
			Action:    action,
			Path:      filepath.Join(added, "pkg", "a.go"),
		})
		require.NoError(t, err)
		require.True(t, ok, tool)
	}
}

// Only view's reads and ls's listings are approved in an added directory.
// The MCP resource tools and gnokey ask with the same action names for a
// path that is no file read from disk (an MCP URI joined to the working
// directory, a directory the model chose); with the working directory
// under an added one (`klash --add-dir ..`), every MCP resource read and
// every `gnokey list` went through unasked.
func TestOtherReadsInAddedDirsStillAsk(t *testing.T) {
	t.Parallel()

	added := t.TempDir()
	under := filepath.Join(added, "proj", "postgres:", "prod", "users")
	for _, req := range []CreatePermissionRequest{
		{ToolName: "read_mcp_resource", Action: "read", Path: under},
		{ToolName: "list_mcp_resources", Action: "list", Path: under},
		{ToolName: "gnokey", Action: "list", Path: added},
		{ToolName: "view", Action: "list", Path: added},
		{ToolName: "made_up", Path: added},
	} {
		t.Run(req.ToolName+" "+req.Action, func(t *testing.T) {
			t.Parallel()
			s := NewPermissionService(t.TempDir(), false, nil, added)
			events := s.Subscribe(t.Context())
			req.SessionID, req.ToolCallID = "s1", "call-1"
			var (
				granted bool
				err     error
			)
			done := make(chan struct{})
			go func() {
				defer close(done)
				granted, err = s.Request(t.Context(), req)
			}()
			select {
			case event := <-events: // it asked
				s.Deny(event.Payload)
				<-done
			case <-done:
			}
			require.NoError(t, err)
			require.False(t, granted, "approved without asking")
		})
	}
}
