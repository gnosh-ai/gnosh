package model

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/crush/internal/workspace"
)

// openStatusReport shows the session's status, as Claude Code's /status
// does.
func (m *UI) openStatusReport() {
	m.dialog.OpenDialog(dialog.NewReport(m.com, "Status", m.statusReport()))
}

// statusReport is the session's status as Markdown for /status, as
// Claude Code's /status shows it: the version, the session, the working
// directory, the models and how full the context is, whether requests
// are asked about, and the MCP and LSP servers and their state.
func (m *UI) statusReport() string {
	cfg := m.com.Config()
	var sb strings.Builder
	sb.WriteString("## Status\n\n| | |\n|---|---|\n")
	row := func(name, value string) {
		fmt.Fprintf(&sb, "| %s | %s |\n", name, statusCell(value))
	}
	row("Version", version.Name+" "+version.Version)
	if m.hasSession() {
		row("Session", fmt.Sprintf("%s (%s)", cmp.Or(m.session.Title, "untitled"), m.session.ID))
	} else {
		row("Session", "none yet: one starts with the first prompt")
	}
	row("Working directory", m.com.Workspace.WorkingDir())
	if model := m.selectedLargeModel(); model != nil {
		row("Model", providerModel(cfg, model.CatwalkCfg.Name, model.ModelCfg.Provider))
		if m.hasSession() {
			row("Context", contextFill(m.session.PromptTokens+m.session.CompletionTokens, model.CatwalkCfg.ContextWindow, m.session.Cost))
		}
	} else {
		row("Model", "not ready yet")
	}
	if small, ok := cfg.Models[config.SelectedModelTypeSmall]; ok && small.Model != "" {
		row("Small model", providerModel(cfg, small.Model, small.Provider))
	}
	if m.com.Workspace.PermissionSkipRequests() {
		row("Permissions", "everything runs without asking (yolo); /permissions has the rules")
	} else {
		row("Permissions", "what the rules do not allow is asked about; /permissions has the mode and rules")
	}

	sb.WriteString("\n### MCP servers\n\n")
	if servers := cfg.MCP.Sorted(); len(servers) == 0 {
		sb.WriteString("None configured.\n")
	} else {
		sb.WriteString("| Server | State |\n|--------|-------|\n")
		for _, server := range servers {
			info, known := m.mcpStates[server.Name]
			fmt.Fprintf(&sb, "| %s | %s |\n", statusCell(server.Name), statusCell(mcpStatus(info, known, server.MCP.Disabled)))
		}
	}

	sb.WriteString("\n### LSP servers\n\n")
	if len(m.lspStates) == 0 {
		sb.WriteString("None running.\n")
	} else {
		sb.WriteString("| Server | State |\n|--------|-------|\n")
		for _, name := range slices.Sorted(maps.Keys(m.lspStates)) {
			fmt.Fprintf(&sb, "| %s | %s |\n", statusCell(name), statusCell(lspStatus(m.lspStates[name])))
		}
	}
	return sb.String()
}

// mcpStatus is an MCP server's state as the sidebar words it, with its
// tool count when connected: disabled in config whatever its state, and
// not started while it has reported none (known is false), as its zero
// state would read disabled.
func mcpStatus(info mcp.ClientInfo, known, disabled bool) string {
	switch {
	case disabled:
		return "disabled"
	case !known:
		return "not started"
	}
	text := mcpStatusText(info)
	if info.State == mcp.StateConnected {
		text += fmt.Sprintf(", %d tools", info.Counts.Tools)
	}
	return text
}

// lspStatus is an LSP server's state as the sidebar words it, with its
// diagnostics when it is ready.
func lspStatus(info workspace.LSPClientInfo) string {
	switch info.State {
	case lsp.StateUnstarted:
		return "unstarted"
	case lsp.StateStopped:
		return "stopped"
	case lsp.StateStarting:
		return "starting"
	case lsp.StateReady:
		return fmt.Sprintf("ready, %d diagnostics", info.DiagnosticCount)
	case lsp.StateError:
		if info.Error != nil {
			return "error: " + info.Error.Error()
		}
		return "error"
	case lsp.StateDisabled:
		return "disabled"
	}
	return "offline"
}

// providerModel is a model and the provider serving it, by the
// provider's name where the config has one.
func providerModel(cfg *config.Config, model, providerID string) string {
	provider := providerID
	if p, ok := cfg.Providers.Get(providerID); ok && p.Name != "" {
		provider = p.Name
	}
	return model + " via " + provider
}

// contextFill is how much of the context window a session's tokens
// take, and what the session has cost.
func contextFill(tokens, window int64, cost float64) string {
	if window <= 0 {
		return fmt.Sprintf("%s tokens of an unknown window, $%.2f", shortTokens(tokens), cost)
	}
	return fmt.Sprintf("%s of %s tokens (%.0f%%), $%.2f", shortTokens(tokens), shortTokens(window), float64(tokens)*100/float64(window), cost)
}

// shortTokens writes a token count as the sidebar does: 950, 12.3K, 1M.
func shortTokens(n int64) string {
	var s string
	switch {
	case n >= 1_000_000:
		s = fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		s = fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
	return strings.Replace(strings.Replace(s, ".0M", "M", 1), ".0K", "K", 1)
}

// statusCell is s as one Markdown table cell: on one line, its pipes
// escaped.
func statusCell(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	return strings.ReplaceAll(s, "|", `\|`)
}
