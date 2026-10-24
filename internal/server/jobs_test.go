package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// GET .../jobs lists the workspace's background jobs, and POST
// .../jobs/{job}/kill stops one; a job started for another workspace is
// neither listed nor stopped.
func TestWorkspaceJobs(t *testing.T) {
	dir, elsewhere := t.TempDir(), t.TempDir()
	m := shell.GetBackgroundShellManager()
	mine, err := m.Start(t.Context(), dir, nil, "echo hi; sleep 30", "says hi", 50)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Kill(mine.ID) })
	theirs, err := m.Start(t.Context(), elsewhere, nil, "sleep 30", "", 50)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Kill(theirs.ID) })

	b := backend.New(t.Context(), nil, nil)
	wsID := uuid.New().String()
	backend.InsertWorkspaceForTest(b, &backend.Workspace{ID: wsID, Path: dir})
	srv := &Server{backend: b}
	srv.installHandler()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	do := func(method, path string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, hs.URL+path, nil)
		require.NoError(t, err)
		rsp, err := hs.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { rsp.Body.Close() })
		return rsp
	}

	rsp := do(http.MethodGet, "/v1/workspaces/"+wsID+"/jobs")
	require.Equal(t, http.StatusOK, rsp.StatusCode)
	var jobs []proto.BackgroundJob
	require.NoError(t, json.NewDecoder(rsp.Body).Decode(&jobs))
	require.Len(t, jobs, 1)
	require.Equal(t, mine.ID, jobs[0].ID)
	require.Equal(t, "echo hi; sleep 30", jobs[0].Command)
	require.Equal(t, "says hi", jobs[0].Description)
	require.Equal(t, dir, jobs[0].WorkingDir)
	require.False(t, jobs[0].Done)

	require.NotEqual(t, http.StatusOK, do(http.MethodPost, "/v1/workspaces/"+wsID+"/jobs/"+theirs.ID+"/kill").StatusCode)
	require.False(t, theirs.IsDone())
	require.Equal(t, http.StatusOK, do(http.MethodPost, "/v1/workspaces/"+wsID+"/jobs/"+mine.ID+"/kill").StatusCode)
	require.True(t, mine.IsDone())
	require.Equal(t, http.StatusNotFound, do(http.MethodGet, "/v1/workspaces/"+uuid.New().String()+"/jobs").StatusCode)
}
