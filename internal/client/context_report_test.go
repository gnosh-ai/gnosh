package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// The /context report through the server: the session goes as
// session_id, and a failure's message is the error.
func TestAgentContextReport(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/workspaces/ws1/agent/context", r.URL.Path)
		if r.URL.Query().Get("session_id") == "broken" {
			w.WriteHeader(http.StatusInternalServerError)
			require.NoError(t, json.NewEncoder(w).Encode(proto.Error{Message: "agent coordinator not initialized"}))
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(proto.ContextReport{Markdown: "## Context Usage for " + r.URL.Query().Get("session_id")}))
	}))
	defer srv.Close()

	report, err := captureClient(t, srv).AgentContextReport(t.Context(), "ws1", "s1")
	require.NoError(t, err)
	require.Equal(t, "## Context Usage for s1", report)

	_, err = captureClient(t, srv).AgentContextReport(t.Context(), "ws1", "broken")
	require.EqualError(t, err, "agent coordinator not initialized")
}
