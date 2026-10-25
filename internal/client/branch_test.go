package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// A branch through the server: the title asked for goes in the body, the
// new session comes back, and a failure's message is the error.
func TestBranchSession(t *testing.T) {
	t.Parallel()

	var asked proto.BranchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/ws1/sessions/s1/branch" {
			require.NoError(t, json.NewDecoder(r.Body).Decode(&asked))
			require.NoError(t, json.NewEncoder(w).Encode(proto.Session{ID: "b1", Title: "fix (Branch)", MessageCount: 3}))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		require.NoError(t, json.NewEncoder(w).Encode(proto.Error{Message: "no messages to branch"}))
	}))
	defer srv.Close()

	branch, err := captureClient(t, srv).BranchSession(t.Context(), "ws1", "s1", "try-the-cache")
	require.NoError(t, err)
	require.Equal(t, &proto.Session{ID: "b1", Title: "fix (Branch)", MessageCount: 3}, branch)
	require.Equal(t, "try-the-cache", asked.Title)
	_, err = captureClient(t, srv).BranchSession(t.Context(), "ws1", "empty", "")
	require.EqualError(t, err, "no messages to branch")
}
