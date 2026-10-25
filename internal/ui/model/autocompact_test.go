package model

import (
	"errors"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// configWorkspace has a config, and records what is set in and removed
// from the global one.
type configWorkspace struct {
	workspace.Workspace
	cfg     *config.Config
	set     map[string]any
	removed []string
	err     error
}

func (w *configWorkspace) Config() *config.Config { return w.cfg }

func (w *configWorkspace) SetConfigField(scope config.Scope, key string, value any) error {
	if scope != config.ScopeGlobal {
		return errors.New("not global")
	}
	w.set[key] = value
	return w.err
}

func (w *configWorkspace) RemoveConfigField(scope config.Scope, key string) error {
	if scope != config.ScopeGlobal {
		return errors.New("not global")
	}
	w.removed = append(w.removed, key)
	return w.err
}

// /autocompact, as in Claude Code: with a window, typed or from the
// palette, it keeps it in the global config and says so, capped to the
// model's window when over it; auto takes the setting away; alone, it
// says what is set; what is no window is said not to be one. When the
// variable is set it says that takes precedence, and when auto-compact is
// off it says so.
func TestAutocompact(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &configWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}, set: map[string]any{}}
	u.com.Workspace = ws
	u.agentReady = true
	u.agentModel = workspace.AgentModel{CatwalkCfg: catwalk.Model{ContextWindow: 262_144}}

	require.Equal(t, util.NewInfoMsg("Auto-compact window: auto. Set it with /autocompact 150k, or /autocompact auto."), u.sendMessage("/autocompact")())
	require.Equal(t, util.NewInfoMsg("Auto-compact window set to 150K tokens."), u.sendMessage("/autocompact 150k")())
	require.Equal(t, map[string]any{"options.auto_compact_window": int64(150_000)}, ws.set)
	require.Equal(t, util.NewInfoMsg("Auto-compact window set to 1M tokens (capped to model limit of 262.1K)."), u.handleDialogAction(dialog.ActionAutocompact{Window: "1M"})())
	require.Equal(t, util.NewInfoMsg("Auto-compact window set to auto."), u.sendMessage("/autocompact auto")())
	require.Equal(t, []string{"options.auto_compact_window"}, ws.removed)
	require.Equal(t, util.NewWarnMsg("Couldn't parse 'lots'. Expected 'auto' or 100k–1M tokens (e.g. 500k, 200000, or 200 as shorthand)"), u.sendMessage("/autocompact lots")())

	ws.cfg.Options.AutoCompactWindow = 150_000
	ws.cfg.Options.DisableAutoSummarize = true
	require.Equal(t, util.NewInfoMsg("Auto-compact window: 150K tokens. Set it with /autocompact 150k, or /autocompact auto. Auto-compact is turned off (disable_auto_summarize in the config)."), u.handleDialogAction(dialog.ActionAutocompact{})())

	ws.err = errors.New("read-only")
	info, ok := u.sendMessage("/autocompact 200")().(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Equal(t, "couldn't save setting: read-only", info.Msg)

	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "200k")
	require.Equal(t, util.NewWarnMsg("CLAUDE_CODE_AUTO_COMPACT_WINDOW is set and takes precedence. Unset it to change this setting."), u.sendMessage("/autocompact 150k")())
}
