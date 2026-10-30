package workspace

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/stretchr/testify/require"
)

// In process, /doctor's report is gnosh doctor's check of the store the
// session runs with.
func TestAppWorkspaceDoctorReport(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("qwen", config.ProviderConfig{ID: "qwen", BaseURL: "http://127.0.0.1:8080/v1", Models: []catwalk.Model{{ID: "qwen3.8-27b"}}})
	w := &AppWorkspace{store: config.NewTestStore(&config.Config{Providers: providers})}
	report, err := w.DoctorReport(t.Context())
	require.NoError(t, err)
	require.Contains(t, report, "## Doctor\n")
	require.Contains(t, report, "- ✓ provider qwen: 1 models (http://127.0.0.1:8080/v1)\n")
}
