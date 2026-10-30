package model

import (
	"errors"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// statusWorkspace has a config, a working directory and the yolo switch.
type statusWorkspace struct {
	workspace.Workspace
	cfg  *config.Config
	yolo bool
}

func (w *statusWorkspace) Config() *config.Config       { return w.cfg }
func (w *statusWorkspace) WorkingDir() string           { return "/home/jae/project" }
func (w *statusWorkspace) PermissionSkipRequests() bool { return w.yolo }

// /status reports the version, the session, the working directory, the
// models by their providers' names, how full the context is, whether
// requests are asked about, and each MCP and LSP server's state.
func TestStatusReport(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("standin", config.ProviderConfig{ID: "standin", Name: "Stand-in", Models: []catwalk.Model{{ID: "qwen3.8-4b", Name: "Qwen 3.8 4B"}}})
	cfg := &config.Config{
		Providers: providers,
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeSmall: {Model: "qwen3.8-4b", Provider: "standin"},
		},
		MCP: config.MCPs{
			"gno":  {},
			"docs": {},
			"old":  {Disabled: true},
			"new":  {},
			"one":  {},
		},
	}
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &statusWorkspace{cfg: cfg, yolo: true}
	u.com.Workspace = ws
	u.agentReady = true
	u.agentModel = workspace.AgentModel{
		CatwalkCfg: catwalk.Model{Name: "Qwen 3.8", ContextWindow: 1_000_000},
		ModelCfg:   config.SelectedModel{Model: "qwen3.8-27b", Provider: "standin"},
	}
	u.session = &session.Session{ID: "s1", Title: "fix-login-bug", PromptTokens: 17_000, CompletionTokens: 900, Cost: 0}
	u.mcpStates = map[string]mcp.ClientInfo{
		"gno":  {Name: "gno", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 12}},
		"docs": {Name: "docs", State: mcp.StateError, Error: errors.New("connection refused")},
		"one":  {Name: "one", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 1}},
	}
	u.lspStates = map[string]workspace.LSPClientInfo{
		"gopls": {Name: "gopls", State: lsp.StateReady, DiagnosticCount: 3},
	}

	report := u.statusReport()
	for _, want := range []string{
		"## Status\n",
		"| Version | Gnosh " + version.Version + " |",
		"| Session | fix-login-bug (s1) |",
		"| Working directory | /home/jae/project |",
		"| Model | Qwen 3.8 via Stand-in |",
		"| Context | 17.9K of 1M tokens (2%), $0.00 |",
		"| Small model | Qwen 3.8 4B via Stand-in |",
		"| Permissions | everything runs without asking (yolo); /permissions has the rules |",
		"| docs | error: connection refused |",
		"| gno | connected, 12 tools |",
		"| one | connected, 1 tool |",
		"| old | disabled |",
		"| new | not started |",
		"| gopls | ready, 3 diagnostics |",
	} {
		require.Contains(t, report, want)
	}

	ws.yolo = false
	u.session = nil
	u.mcpStates, u.lspStates = nil, nil
	cfg.MCP = nil
	report = u.statusReport()
	require.Contains(t, report, "| Session | none yet: one starts with the first prompt |")
	require.Contains(t, report, "| Permissions | what the rules do not allow is asked about; /permissions has the mode and rules |")
	require.NotContains(t, report, "| Context |")
	require.Contains(t, report, "### MCP servers\n\nNone configured.\n")
	require.Contains(t, report, "### LSP servers\n\nNone running.\n")

	u.openStatusReport()
	require.True(t, u.dialog.ContainsDialog(dialog.ReportID))
}
