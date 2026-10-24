package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// A summary asked for through the server carries /compact's instructions.
func TestAgentSummarizeSessionSendsInstructions(t *testing.T) {
	t.Parallel()

	var got proto.SummarizeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/workspaces/ws1/agent/sessions/s1/summarize", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
	}))
	defer srv.Close()

	require.NoError(t, captureClient(t, srv).AgentSummarizeSession(t.Context(), "ws1", "s1", "keep the API decisions"))
	require.Equal(t, "keep the API decisions", got.Instructions)
}
