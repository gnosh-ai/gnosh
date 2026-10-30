package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// A session's goal through the server: read, set with the condition in
// the body (answering the goal it had), and a failure's message as the
// error.
func TestSessionGoal(t *testing.T) {
	t.Parallel()

	var asked proto.GoalRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces/ws1/sessions/s1/goal":
			require.NoError(t, json.NewEncoder(w).Encode(proto.GoalStatus{Goal: &proto.Goal{Condition: "the tests pass", Checks: 1}}))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/workspaces/ws1/sessions/s1/goal":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&asked))
			require.NoError(t, json.NewEncoder(w).Encode(proto.GoalStatus{Goal: &proto.Goal{Condition: asked.Condition, Name: "docs-build"}, Directive: "work toward docs-build"}))
		default:
			w.WriteHeader(http.StatusBadRequest)
			require.NoError(t, json.NewEncoder(w).Encode(proto.Error{Message: "the agent is not ready"}))
		}
	}))
	defer srv.Close()

	goal, err := captureClient(t, srv).SessionGoal(t.Context(), "ws1", "s1")
	require.NoError(t, err)
	require.Equal(t, &proto.Goal{Condition: "the tests pass", Checks: 1}, goal)
	status, err := captureClient(t, srv).SetSessionGoal(t.Context(), "ws1", "s1", "the docs build")
	require.NoError(t, err)
	require.Equal(t, proto.GoalStatus{Goal: &proto.Goal{Condition: "the docs build", Name: "docs-build"}, Directive: "work toward docs-build"}, status)
	require.Equal(t, "the docs build", asked.Condition)
	_, err = captureClient(t, srv).SessionGoal(t.Context(), "ws1", "other")
	require.EqualError(t, err, "the agent is not ready")
}
