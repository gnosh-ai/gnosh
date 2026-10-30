package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/backend"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// POST .../mem takes /mem's arguments in its body, and they reach no log,
// not even when the request fails: /mem redact carries a secret. A
// workspace whose agent is not ready is refused.
func TestPostMem(t *testing.T) {
	t.Parallel()

	b := backend.New(t.Context(), nil, nil)
	wsID := uuid.New().String()
	backend.InsertWorkspaceForTest(b, &backend.Workspace{ID: wsID, Path: t.TempDir()})
	var logs bytes.Buffer
	srv := &Server{backend: b}
	srv.SetLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	srv.installHandler()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	do := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, hs.URL+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		rsp, err := hs.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { rsp.Body.Close() })
		return rsp
	}
	const secret = "sk-live-7d1e9a"
	body := `{"session_id":"s1","args":"redact ` + secret + `"}`
	require.Equal(t, http.StatusBadRequest, do(http.MethodPost, "/v1/workspaces/"+wsID+"/mem", body).StatusCode, "agent not ready")
	require.Equal(t, http.StatusNotFound, do(http.MethodPost, "/v1/workspaces/"+uuid.New().String()+"/mem", body).StatusCode)
	require.Equal(t, http.StatusBadRequest, do(http.MethodPost, "/v1/workspaces/"+wsID+"/mem", "{"+secret).StatusCode, "a body that is not JSON")
	require.Equal(t, http.StatusBadRequest, do(http.MethodGet, "/v1/workspaces/"+wsID+"/mem/notice", "").StatusCode)
	require.NotEmpty(t, logs.String())
	require.NotContains(t, logs.String(), secret)
}
