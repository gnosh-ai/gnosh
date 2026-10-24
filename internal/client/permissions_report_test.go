package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

// The /permissions report through the server, and a failure's message as
// the error.
func TestPermissionsReport(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/workspaces/broken/permissions/report" {
			w.WriteHeader(http.StatusBadRequest)
			require.NoError(t, json.NewEncoder(w).Encode(proto.Error{Message: "agent coordinator not initialized"}))
			return
		}
		require.Equal(t, "/v1/workspaces/ws1/permissions/report", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(proto.PermissionsReport{Markdown: "## Permissions"}))
	}))
	defer srv.Close()

	report, err := captureClient(t, srv).PermissionsReport(t.Context(), "ws1")
	require.NoError(t, err)
	require.Equal(t, "## Permissions", report)

	_, err = captureClient(t, srv).PermissionsReport(t.Context(), "broken")
	require.EqualError(t, err, "agent coordinator not initialized")
}
