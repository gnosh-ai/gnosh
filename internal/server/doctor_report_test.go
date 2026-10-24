package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// GET .../doctor answers the check of the workspace's setup that klash
// doctor prints: here a provider it can use and a model none was chosen
// for. A workspace with no config yet is refused.
func TestGetDoctorReport(t *testing.T) {
	t.Parallel()

	b := backend.New(t.Context(), nil, nil)
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("qwen", config.ProviderConfig{ID: "qwen", BaseURL: "http://127.0.0.1:8080/v1", Models: []catwalk.Model{{ID: "qwen3.8-27b"}}})
	wsID, bare := uuid.New().String(), uuid.New().String()
	backend.InsertWorkspaceForTest(b, &backend.Workspace{ID: wsID, Path: t.TempDir(), Cfg: config.NewTestStore(&config.Config{Providers: providers})})
	backend.InsertWorkspaceForTest(b, &backend.Workspace{ID: bare, Path: t.TempDir()})
	srv := &Server{backend: b}
	srv.installHandler()
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	get := func(id string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, hs.URL+"/v1/workspaces/"+id+"/doctor", nil)
		require.NoError(t, err)
		rsp, err := hs.Client().Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { rsp.Body.Close() })
		return rsp
	}
	rsp := get(wsID)
	require.Equal(t, http.StatusOK, rsp.StatusCode)
	var report proto.DoctorReport
	require.NoError(t, json.NewDecoder(rsp.Body).Decode(&report))
	require.Contains(t, report.Markdown, "## Doctor\n")
	require.Contains(t, report.Markdown, "- ✓ provider qwen: 1 models (http://127.0.0.1:8080/v1)\n")
	require.Contains(t, report.Markdown, "- ✗ large model: none chosen\n")

	require.Equal(t, http.StatusNotFound, get(uuid.New().String()).StatusCode, "no such workspace")
	require.Equal(t, http.StatusBadRequest, get(bare).StatusCode, "no config yet")
}
