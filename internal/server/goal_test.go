package server

import (
	"context"
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

// goalCoordinator keeps the sessions' goals, as the coordinator does,
// naming each after its condition, as mem would.
type goalCoordinator struct {
	agenttest.NopCoordinator
	goals map[string]agent.Goal
}

func (c *goalCoordinator) SetGoal(_ context.Context, sessionID, condition string) (agent.GoalChange, error) {
	goal := agent.Goal{Condition: condition, Name: strings.ReplaceAll(condition, " ", "-")}
	c.goals[sessionID] = goal
	return agent.GoalChange{Goal: goal, Had: true, Directive: "work toward " + goal.Name}, nil
}

func (c *goalCoordinator) Goal(_ context.Context, sessionID string) (agent.Goal, bool) {
	goal, ok := c.goals[sessionID]
	return goal, ok
}

func (c *goalCoordinator) ClearGoal(_ context.Context, sessionID string) (agent.GoalChange, error) {
	goal, ok := c.goals[sessionID]
	delete(c.goals, sessionID)
	if !ok {
		return agent.GoalChange{}, nil
	}
	return agent.GoalChange{Goal: goal, Had: true, Notice: "/goal is cleared"}, nil
}

// GET .../sessions/{sid}/goal answers the session's goal, none when it
// has none; PUT sets it, answering the goal set and the directive to
// send, or clears it with no condition, answering the goal cleared and
// what that did. A body that is not JSON is refused, and an unknown
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
	require.Equal(t, proto.GoalStatus{Goal: &proto.Goal{Condition: "the tests pass", Name: "the-tests-pass"}, Directive: "work toward the-tests-pass"}, status)
	require.Equal(t, map[string]agent.Goal{"s1": {Condition: "the tests pass", Name: "the-tests-pass"}}, coordinator.goals)
	coordinator.goals["s1"] = agent.Goal{Condition: "the tests pass", Name: "the-tests-pass", Checks: 2, LastCheck: "the tests still fail"}
	_, status = do(http.MethodGet, ws.ID, "")
	require.Equal(t, &proto.Goal{Condition: "the tests pass", Name: "the-tests-pass", Checks: 2, LastCheck: "the tests still fail"}, status.Goal)
	_, status = do(http.MethodPut, ws.ID, `{"condition":""}`)
	require.Equal(t, "the tests pass", status.Goal.Condition)
	require.Equal(t, "/goal is cleared", status.Notice)
	require.Empty(t, coordinator.goals)
	_, status = do(http.MethodPut, ws.ID, `{"condition":""}`)
	require.Nil(t, status.Goal, "none to clear")

	code, _ = do(http.MethodPut, ws.ID, `{"condition":`)
	require.Equal(t, http.StatusBadRequest, code)
	code, _ = do(http.MethodGet, uuid.New().String(), "")
	require.Equal(t, http.StatusNotFound, code)
}
