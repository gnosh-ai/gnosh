package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/agenttest"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// stubNameCoordinator names every session name, or fails with err.
type stubNameCoordinator struct {
	agenttest.NopCoordinator
	name string
	err  error
}

func (s *stubNameCoordinator) GenerateSessionName(context.Context, string) (string, error) {
	return s.name, s.err
}

// POST .../agent/sessions/{sid}/name answers the name the session was
// given from its conversation, and a failure with its own message, which
// a client shows as it would in process.
func TestPostSessionNameAnswersTheName(t *testing.T) {
	t.Parallel()

	post := func(coord *stubNameCoordinator) *httptest.ResponseRecorder {
		b := backend.New(t.Context(), nil, nil)
		wsID := uuid.New().String()
		ws := &backend.Workspace{ID: wsID, Path: t.TempDir(), App: &app.App{AgentCoordinator: coord}}
		backend.InsertWorkspaceForTest(b, ws)
		c := &controllerV1{backend: b, server: &Server{backend: b}}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/workspaces/"+wsID+"/agent/sessions/s1/name", nil)
		req.SetPathValue("id", wsID)
		req.SetPathValue("sid", "s1")
		rec := httptest.NewRecorder()
		c.handlePostWorkspaceAgentSessionName(rec, req)
		return rec
	}

	rec := post(&stubNameCoordinator{name: "fix-login-bug"})
	require.Equal(t, http.StatusOK, rec.Code)
	var named proto.SessionNameResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &named))
	require.Equal(t, "fix-login-bug", named.Name)

	rec = post(&stubNameCoordinator{err: errors.New("no conversation context yet")})
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	var failed proto.Error
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &failed))
	require.Equal(t, "no conversation context yet", failed.Message)
}
