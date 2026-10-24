package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// The name endpoint's answer is the name, and a failure's message is the
// error, so "no conversation context yet" reads the same as in process.
func TestAgentGenerateSessionName(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/workspaces/ws1/agent/sessions/s1/name", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(proto.SessionNameResponse{Name: "fix-login-bug"}))
	}))
	defer srv.Close()
	name, err := captureClient(t, srv).AgentGenerateSessionName(t.Context(), "ws1", "s1")
	require.NoError(t, err)
	require.Equal(t, "fix-login-bug", name)

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		require.NoError(t, json.NewEncoder(w).Encode(proto.Error{Message: "no conversation context yet"}))
	}))
	defer failing.Close()
	_, err = captureClient(t, failing).AgentGenerateSessionName(t.Context(), "ws1", "s1")
	require.EqualError(t, err, "no conversation context yet")
}
