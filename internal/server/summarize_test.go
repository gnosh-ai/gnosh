package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/agenttest"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// stubSummarizeCoordinator keeps the instructions of each summary asked
// for.
type stubSummarizeCoordinator struct {
	agenttest.NopCoordinator
	instructions []string
}

func (s *stubSummarizeCoordinator) Summarize(_ context.Context, _, instructions string) error {
	s.instructions = append(s.instructions, instructions)
	return nil
}

// POST .../summarize takes /compact's instructions in an optional body;
// with none, the summary has no instructions, and a body that is not
// JSON is refused.
func TestPostSummarizeTakesInstructions(t *testing.T) {
	t.Parallel()

	b := backend.New(t.Context(), nil, nil)
	wsID := uuid.New().String()
	coord := &stubSummarizeCoordinator{}
	backend.InsertWorkspaceForTest(b, &backend.Workspace{ID: wsID, Path: t.TempDir(), App: &app.App{AgentCoordinator: coord}})
	c := &controllerV1{backend: b, server: &Server{backend: b}}
	post := func(body string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/workspaces/"+wsID+"/agent/sessions/s1/summarize", strings.NewReader(body))
		req.SetPathValue("id", wsID)
		req.SetPathValue("sid", "s1")
		rec := httptest.NewRecorder()
		c.handlePostWorkspaceAgentSessionSummarize(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, post(`{"instructions":"keep the API decisions"}`))
	require.Equal(t, http.StatusOK, post(""))
	require.Equal(t, http.StatusBadRequest, post("{"))
	require.Equal(t, []string{"keep the API decisions", ""}, coord.instructions)
}
