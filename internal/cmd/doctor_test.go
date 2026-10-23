package cmd

import (
	"errors"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/stretchr/testify/require"
)

func doctorConfig() *config.Config {
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("qwen", config.ProviderConfig{ID: "qwen", BaseURL: "http://127.0.0.1:8080/v1", Models: []catwalk.Model{{ID: "qwen3.8-27b"}}})
	return &config.Config{
		Providers: providers,
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {Provider: "qwen", Model: "qwen3.8-27b"},
			config.SelectedModelTypeSmall: {Provider: "qwen", Model: "qwen3.8-27b"},
		},
		Hooks:   map[string][]config.HookConfig{config.EventStop: {{Command: "true"}}, config.EventUserPromptSubmit: {{Command: "a"}, {Command: "b"}}},
		LSP:     config.LSPs{"gopls": {Command: "gopls"}, "off": {Command: "nope", Disabled: true}},
		Options: &config.Options{DataDirectory: "/tmp/proj/.crash"},
	}
}

func found(name string) (string, error) { return "/usr/bin/" + name, nil }

// A setup that can run a turn reports each part as fine.
func TestDoctorReportOnAWorkingSetup(t *testing.T) {
	t.Parallel()

	lines, ok := doctorReport(config.NewTestStore(doctorConfig()), found)
	report := strings.Join(lines, "\n")
	require.True(t, ok, report)
	require.Contains(t, report, "✓ provider qwen: 1 models (http://127.0.0.1:8080/v1)")
	require.Contains(t, report, "✓ large model: qwen/qwen3.8-27b")
	require.Contains(t, report, "✓ hooks: UserPromptSubmit 2, Stop 1")
	require.Contains(t, report, "✓ ripgrep: /usr/bin/rg")
	require.Contains(t, report, "✓ lsp gopls: /usr/bin/gopls")
	require.NotContains(t, report, "nope", "a disabled LSP is not checked")
	require.NotContains(t, report, "✗")
}

// A chosen model no provider offers means no turn can run; a missing
// ripgrep or LSP binary is worth knowing but does not.
func TestDoctorReportSaysWhatIsWrong(t *testing.T) {
	t.Parallel()

	cfg := doctorConfig()
	cfg.Models[config.SelectedModelTypeLarge] = config.SelectedModel{Provider: "qwen", Model: "gone"}
	missing := func(name string) (string, error) { return "", errors.New("not found") }

	lines, ok := doctorReport(config.NewTestStore(cfg), missing)
	report := strings.Join(lines, "\n")
	require.False(t, ok)
	require.Contains(t, report, "✗ large model: qwen/gone is not offered by an available provider")
	require.Contains(t, report, "! ripgrep (rg) not found")
	require.Contains(t, report, "! lsp gopls: gopls not found")

	empty := doctorConfig()
	empty.Providers = csync.NewMap[string, config.ProviderConfig]()
	lines, ok = doctorReport(config.NewTestStore(empty), found)
	require.False(t, ok)
	require.Contains(t, strings.Join(lines, "\n"), "✗ providers: none configured")
}
