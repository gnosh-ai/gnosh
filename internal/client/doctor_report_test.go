package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// The /doctor report through the server, and a failure's message as the
// error.
func TestDoctorReport(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/workspaces/broken/doctor" {
			w.WriteHeader(http.StatusNotFound)
			require.NoError(t, json.NewEncoder(w).Encode(proto.Error{Message: "workspace not found"}))
			return
		}
		require.Equal(t, "/v1/workspaces/ws1/doctor", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(proto.DoctorReport{Markdown: "## Doctor"}))
	}))
	defer srv.Close()

	report, err := captureClient(t, srv).DoctorReport(t.Context(), "ws1")
	require.NoError(t, err)
	require.Equal(t, "## Doctor", report)

	_, err = captureClient(t, srv).DoctorReport(t.Context(), "broken")
	require.EqualError(t, err, "workspace not found")
}
