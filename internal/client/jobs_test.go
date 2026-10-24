package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// The background jobs through the server, a stop of one, and a failure's
// message as the error.
func TestBackgroundJobs(t *testing.T) {
	t.Parallel()

	var stopped string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces/ws1/jobs":
			require.NoError(t, json.NewEncoder(w).Encode([]proto.BackgroundJob{{ID: "001", Command: "make"}}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/ws1/jobs/001/kill":
			stopped = "001"
		default:
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(proto.Error{Message: "background shell not found: 009"}))
		}
	}))
	defer srv.Close()

	jobs, err := captureClient(t, srv).BackgroundJobs(t.Context(), "ws1")
	require.NoError(t, err)
	require.Equal(t, []proto.BackgroundJob{{ID: "001", Command: "make"}}, jobs)
	require.NoError(t, captureClient(t, srv).KillBackgroundJob(t.Context(), "ws1", "001"))
	require.Equal(t, "001", stopped)
	require.EqualError(t, captureClient(t, srv).KillBackgroundJob(t.Context(), "ws1", "009"), "background shell not found: 009")
	_, err = captureClient(t, srv).BackgroundJobs(t.Context(), "gone")
	require.EqualError(t, err, "background shell not found: 009")
}
