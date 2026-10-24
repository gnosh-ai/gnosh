package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// GET .../permissions/report answers the workspace's /permissions report:
// its rules, and yolo as bypassPermissions.
func TestGetPermissionsReport(t *testing.T) {
	t.Parallel()

	b := backend.New(t.Context(), nil, nil)
	wsID := uuid.New().String()
	cfg := &config.Config{Permissions: &config.Permissions{Allow: []string{"Bash(git diff *)"}}}
	ws := &backend.Workspace{
		ID:   wsID,
		Path: t.TempDir(),
		App:  &app.App{Permissions: permission.NewPermissionService(t.TempDir(), true, nil)},
		Cfg:  config.NewTestStore(cfg),
	}
	backend.InsertWorkspaceForTest(b, ws)
	c := &controllerV1{backend: b, server: &Server{backend: b}}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/workspaces/"+wsID+"/permissions/report", nil)
	req.SetPathValue("id", wsID)
	rec := httptest.NewRecorder()
	c.handleGetWorkspacePermissionsReport(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var report proto.PermissionsReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	require.Contains(t, report.Markdown, "**Mode:** bypassPermissions:")
	require.Contains(t, report.Markdown, "| Bash(git diff *) | permissions.allow |")
}
