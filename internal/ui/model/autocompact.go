package model

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// autoCompactWindowKey is where /autocompact keeps its setting, in the
// global config.
const autoCompactWindowKey = "options.auto_compact_window"

// autocompact sets how full the context gets before it is summarized, as
// Claude Code's /autocompact does: arg is auto or a token count, kept as
// the auto_compact_window option in the global config, for every session
// and model from their next step. With no arg it says what is set and how
// to change it. CLAUDE_CODE_AUTO_COMPACT_WINDOW, when set, takes
// precedence, as in Claude Code.
func (m *UI) autocompact(arg string) tea.Cmd {
	if os.Getenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW") != "" {
		return util.ReportWarn("CLAUDE_CODE_AUTO_COMPACT_WINDOW is set and takes precedence. Unset it to change this setting.")
	}
	cfg := m.com.Workspace.Config()
	var limit int64
	if model := m.selectedLargeModel(); model != nil {
		limit = model.CatwalkCfg.ContextWindow
	}
	off := ""
	if cfg.Options != nil && cfg.Options.DisableAutoSummarize {
		off = " Auto-compact is turned off (disable_auto_summarize in the config)."
	}
	if strings.TrimSpace(arg) == "" {
		var window int64
		if cfg.Options != nil {
			window = cfg.Options.AutoCompactWindow
		}
		return util.ReportInfo("Auto-compact window: " + compactWindowSaid(window, limit) + ". Set it with /autocompact 150k, or /autocompact auto." + off)
	}
	window, ok := config.ParseAutoCompactWindow(arg)
	if !ok {
		return util.ReportWarn(fmt.Sprintf("Couldn't parse '%s'. Expected 'auto' or 100k–1M tokens (e.g. 500k, 200000, or 200 as shorthand)", strings.TrimSpace(arg)))
	}
	return func() tea.Msg {
		var err error
		if window == 0 {
			err = m.com.Workspace.RemoveConfigField(config.ScopeGlobal, autoCompactWindowKey)
		} else {
			err = m.com.Workspace.SetConfigField(config.ScopeGlobal, autoCompactWindowKey, window)
		}
		if err != nil {
			return util.ReportError(fmt.Errorf("couldn't save setting: %w", err))()
		}
		return util.NewInfoMsg("Auto-compact window set to " + compactWindowSaid(window, limit) + "." + off)
	}
}

// compactWindowSaid says an auto-compact window as Claude Code says it:
// auto, or the tokens, capped to the model's window, limit, when over it.
func compactWindowSaid(window, limit int64) string {
	if window == 0 {
		return "auto"
	}
	said := shortTokens(window) + " tokens"
	if limit > 0 && window > limit {
		said += " (capped to model limit of " + shortTokens(limit) + ")"
	}
	return said
}
