package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// A rewind through the server: what to restore goes in the body, what it
// did comes back, and a failure's message is the error.
func TestRewind(t *testing.T) {
	t.Parallel()

	var asked proto.RewindRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/ws1/sessions/s1/rewind" {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&asked))
			require.NoError(t, json.NewEncoder(w).Encode(proto.RewindResult{Files: []string{"/w/a.go"}, Additions: 2, Removals: 1, Kept: &proto.Session{ID: "k1"}}))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		require.NoError(t, json.NewEncoder(w).Encode(proto.Error{Message: "message not found"}))
	}))
	defer srv.Close()

	req := proto.RewindRequest{MessageID: "m2", Conversation: true, Code: true}
	result, err := captureClient(t, srv).Rewind(t.Context(), "ws1", "s1", req)
	require.NoError(t, err)
	require.Equal(t, &proto.RewindResult{Files: []string{"/w/a.go"}, Additions: 2, Removals: 1, Kept: &proto.Session{ID: "k1"}}, result)
	require.Equal(t, req, asked)
	_, err = captureClient(t, srv).Rewind(t.Context(), "ws1", "gone", req)
	require.EqualError(t, err, "message not found")
}
