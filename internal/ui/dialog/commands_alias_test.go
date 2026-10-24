package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

type paletteWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *paletteWorkspace) Config() *config.Config { return w.cfg }

// Claude Code's slash command names, typed after "/" from habit, select the
// command that does the same here; "resume" and "compact" used to match
// nothing.
func TestPaletteFindsClaudeCodeSlashNames(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	for typed, want := range map[string]string{
		"resume":   "Sessions",
		"continue": "Sessions",
		"compact":  "Summarize Session",
		"rename":   "Rename Session",
		"name":     "Rename Session",
		"clear":    "New Session",
		"new":      "New Session",
		"reset":    "New Session",
		"model":    "Switch Model",
		"init":     "Initialize Project",
		"help":     "Toggle Help",
		"theme":    "Themes",
		"exit":     "Quit",
		"quit":     "Quit",
	} {
		c, err := NewCommands(com, "s1", true, false, false, nil, nil)
		require.NoError(t, err)
		for _, r := range typed {
			c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		item, ok := c.list.SelectedItem().(*CommandItem)
		require.True(t, ok, "%q selects nothing", typed)
		require.Equal(t, want, item.title, "typed %q", typed)
	}
}

// Claude Code's /rename (or /name) takes the name after it: typed into
// the palette with it, Enter names the session; with none, the UI has one
// made from the conversation.
func TestPaletteRenameTakesTheName(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	for typed, want := range map[string]string{
		"rename fix the login bug": "fix the login bug",
		"rename":                   "",
		"name the alias":           "the alias",
	} {
		c, err := NewCommands(com, "s1", true, false, false, nil, nil)
		require.NoError(t, err)
		for _, r := range typed {
			c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		action := c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
		require.Equal(t, ActionRenameSession{SessionID: "s1", Name: want}, action, "typed %q", typed)
	}

	// With no session there is nothing to rename, and the palette has no
	// such command.
	c, err := NewCommands(com, "", false, false, false, nil, nil)
	require.NoError(t, err)
	for _, r := range "rename x" {
		c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, renamed := c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}).(ActionRenameSession)
	require.False(t, renamed)
}

// Claude Code's /resume (or /continue) takes the session to go to, by ID
// or name: typed into the palette with it, Enter asks for that session;
// alone, it is the Sessions item.
func TestPaletteResumeTakesTheSession(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	enter := func(typed string) Action {
		c, err := NewCommands(com, "s1", true, false, false, nil, nil)
		require.NoError(t, err)
		for _, r := range typed {
			c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		return c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	require.Equal(t, ActionResumeSession{Ref: "fix-login-bug"}, enter("resume fix-login-bug"))
	require.Equal(t, ActionResumeSession{Ref: "the auth work"}, enter("continue the auth work"))
	require.Equal(t, ActionOpenDialog{DialogID: SessionsID}, enter("resume"))
}
