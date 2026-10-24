package tools

import (
	"context"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// capturingPermissions denies every request and keeps the last one.
type capturingPermissions struct {
	recordingPermissionService
	last permission.CreatePermissionRequest
}

func (c *capturingPermissions) Request(_ context.Context, req permission.CreatePermissionRequest) (bool, error) {
	c.last = req
	return false, nil
}

// download's permission request names its call, so the approval that
// answered a hook's question about the call (keyed by its ID) covers it,
// and the user is not asked a second time; and the file it would write,
// so a protected one is always asked about.
func TestDownloadPermissionRequestNamesTheCall(t *testing.T) {
	t.Parallel()

	perms := &capturingPermissions{}
	dir := t.TempDir()
	tool := NewDownloadTool(perms, dir, nil)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-7", Name: DownloadToolName, Input: `{"url":"https://example.com/a.tgz","file_path":"a.tgz"}`})
	require.NoError(t, err)
	require.True(t, resp.IsError, "denied, so nothing is fetched")
	require.Equal(t, "call-7", perms.last.ToolCallID)
	require.Equal(t, filepath.Join(dir, "a.tgz"), perms.last.FilePath)
}
