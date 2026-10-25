package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/agenttest"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// goalCoordinator keeps the sessions' goals, as the coordinator does.
type goalCoordinator struct {
	agenttest.NopCoordinator
	goals map[string]agent.Goal
}

func (c *goalCoordinator) SetGoal(sessionID, condition string) {
	c.goals[sessionID] = agent.Goal{Condition: condition}
}

func (c *goalCoordinator) Goal(sessionID string) (agent.Goal, bool) {
	goal, ok := c.goals[sessionID]
	return goal, ok
}

func (c *goalCoordinator) ClearGoal(sessionID string) (agent.Goal, bool) {
	goal, ok := c.goals[sessionID]
	delete(c.goals, sessionID)
	return goal, ok
}

// GET .../sessions/{sid}/goal answers the session's goal, none when it
// has none; PUT sets it, or clears it with no condition, answering the
// goal it had. A body that is not JSON is refused, and an unknown
// workspace is not found.
func TestSessionGoalEndpoints(t *testing.T) {
	coordinator := &goalCoordinator{goals: map[string]agent.Goal{}}
	b := backend.New(t.Context(), nil, nil)
	ws := &backend.Workspace{ID: uuid.New().String(), Path: t.TempDir(), App: &app.App{AgentCoordinator: coordinator}}
	backend.InsertWorkspaceForTest(b, ws)
	backend.SetWorkspaceShutdownFnForTest(ws, func() {})
	srv := &Server{backend: b}
	srv.installHandler()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	do := func(method, wsID, body string) (int, proto.GoalStatus) {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, hs.URL+"/v1/workspaces/"+wsID+"/sessions/s1/goal", r)
		require.NoError(t, err)
		rsp, err := hs.Client().Do(req)
		require.NoError(t, err)
		defer rsp.Body.Close()
		var status proto.GoalStatus
		if rsp.StatusCode == http.StatusOK {
			require.NoError(t, json.NewDecoder(rsp.Body).Decode(&status))
		}
		return rsp.StatusCode, status
	}

	code, status := do(http.MethodGet, ws.ID, "")
	require.Equal(t, http.StatusOK, code)
	require.Nil(t, status.Goal)
	code, status = do(http.MethodPut, ws.ID, `{"condition":"the tests pass"}`)
	require.Equal(t, http.StatusOK, code)
	require.Nil(t, status.Goal, "it had none")
	require.Equal(t, map[string]agent.Goal{"s1": {Condition: "the tests pass"}}, coordinator.goals)
	coordinator.goals["s1"] = agent.Goal{Condition: "the tests pass", Checks: 2, LastCheck: "the tests still fail"}
	_, status = do(http.MethodGet, ws.ID, "")
	require.Equal(t, &proto.Goal{Condition: "the tests pass", Checks: 2, LastCheck: "the tests still fail"}, status.Goal)
	_, status = do(http.MethodPut, ws.ID, `{"condition":""}`)
	require.Equal(t, "the tests pass", status.Goal.Condition)
	require.Empty(t, coordinator.goals)

	code, _ = do(http.MethodPut, ws.ID, `{"condition":`)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = do(http.MethodGet, uuid.New().String(), "")
	require.Equal(t, http.StatusNotFound, code)
}
