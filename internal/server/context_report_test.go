package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/agenttest"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// stubContextCoordinator reports a fixed model, and keeps the sessions
// asked about.
type stubContextCoordinator struct {
	agenttest.NopCoordinator
	sessions []string
}

func (s *stubContextCoordinator) ContextUsage(_ context.Context, sessionID string) (agent.ContextUsage, error) {
	s.sessions = append(s.sessions, sessionID)
	return agent.ContextUsage{Model: "qwen3.8-27b", Window: 1000}, nil
}

// GET .../agent/context answers the /context report for session_id, or
// for a new session without one.
func TestGetContextReport(t *testing.T) {
	t.Parallel()

	b := backend.New(t.Context(), nil, nil)
	wsID := uuid.New().String()
	coord := &stubContextCoordinator{}
	backend.InsertWorkspaceForTest(b, &backend.Workspace{ID: wsID, Path: t.TempDir(), App: &app.App{AgentCoordinator: coord}})
	c := &controllerV1{backend: b, server: &Server{backend: b}}
	get := func(query string) proto.ContextReport {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/workspaces/"+wsID+"/agent/context"+query, nil)
		req.SetPathValue("id", wsID)
		rec := httptest.NewRecorder()
		c.handleGetWorkspaceAgentContext(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var report proto.ContextReport
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
		return report
	}

	require.Contains(t, get("?session_id=s1").Markdown, "**Model:** qwen3.8-27b")
	get("")
	require.Equal(t, []string{"s1", ""}, coord.sessions)
}
