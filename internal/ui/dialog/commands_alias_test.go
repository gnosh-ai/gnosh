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
		"resume":        "Sessions",
		"continue":      "Sessions",
		"compact":       "Summarize Session",
		"context":       "Context Usage",
		"hooks":         "Hooks",
		"permissions":   "Permissions",
		"allowed-tools": "Permissions",
		"status":        "Status",
		"export":        "Export Conversation",
		"doctor":        "Doctor",
		"copy":          "Copy Last Response",
		"diff":          "Diff",
		"tasks":         "Background Tasks",
		"branch":        "Branch Conversation",
		"rewind":        "Rewind",
		"autocompact":   "Auto-compact Window",
		"checkpoint":    "Rewind",
		"undo":          "Rewind",
		"bashes":        "Background Tasks",
		"checkup":       "Doctor",
		"rename":        "Rename Session",
		"name":          "Rename Session",
		"clear":         "New Session",
		"new":           "New Session",
		"reset":         "New Session",
		"model":         "Switch Model",
		"init":          "Initialize Project",
		"help":          "Toggle Help",
		"theme":         "Themes",
		"exit":          "Quit",
		"quit":          "Quit",
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

// Claude Code's /compact takes what the summary should keep: typed into
// the palette with it, Enter summarizes with those instructions; alone,
// it is the Summarize Session item. With no session there is nothing to
// summarize.
func TestPaletteCompactTakesInstructions(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	enter := func(sessionID, typed string) Action {
		c, err := NewCommands(com, sessionID, sessionID != "", false, false, nil, nil)
		require.NoError(t, err)
		for _, r := range typed {
			c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		return c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	require.Equal(t, ActionSummarize{SessionID: "s1", Instructions: "keep the API decisions"}, enter("s1", "compact keep the API decisions"))
	require.Equal(t, ActionSummarize{SessionID: "s1"}, enter("s1", "compact"))
	_, summarized := enter("", "compact keep it").(ActionSummarize)
	require.False(t, summarized)
}

// Claude Code's /context, with or without its "all", opens the report;
// what follows it is not used, even text that matches no command.
func TestPaletteContextOpensTheReport(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	for _, typed := range []string{"context", "context all", "context qz"} {
		c, err := NewCommands(com, "", false, false, false, nil, nil)
		require.NoError(t, err)
		for _, r := range typed {
			c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		require.Equal(t, ActionContextUsage{}, c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}), "typed %q", typed)
	}
}

// Claude Code's /hooks lists the hooks.
func TestPaletteHooksOpensTheReport(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	c, err := NewCommands(com, "", false, false, false, nil, nil)
	require.NoError(t, err)
	for _, r := range "hooks" {
		c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	require.Equal(t, ActionHooksReport{}, c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
}

// Claude Code's /export takes the file to write: typed into the palette
// with it, Enter saves the conversation there; alone, it is the Export
// Conversation item, which asks how. With no session there is nothing to
// export.
func TestPaletteExportTakesTheFile(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	enter := func(sessionID, typed string) Action {
		c, err := NewCommands(com, sessionID, sessionID != "", false, false, nil, nil)
		require.NoError(t, err)
		for _, r := range typed {
			c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		return c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	require.Equal(t, ActionExportConversation{Filename: "notes.md"}, enter("s1", "export notes.md"))
	require.Equal(t, ActionExportConversation{}, enter("s1", "export"))
	_, exported := enter("", "export notes.md").(ActionExportConversation)
	require.False(t, exported)
}

// Claude Code's /copy takes the N of the Nth-latest response: typed into
// the palette with it, Enter copies that one; alone, it is the Copy Last
// Response item. With no session there is nothing to copy.
func TestPaletteCopyTakesN(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	enter := func(sessionID, typed string) Action {
		c, err := NewCommands(com, sessionID, sessionID != "", false, false, nil, nil)
		require.NoError(t, err)
		for _, r := range typed {
			c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		return c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	require.Equal(t, ActionCopyResponse{N: "2"}, enter("s1", "copy 2"))
	require.Equal(t, ActionCopyResponse{}, enter("s1", "copy"))
	_, copied := enter("", "copy 2").(ActionCopyResponse)
	require.False(t, copied)
}

// Claude Code's /branch takes the branch's name: typed into the palette
// with it, Enter branches with that name; alone, it is the Branch
// Conversation item. With no session there is nothing to branch. A slip
// in the name is told what was meant.
func TestPaletteBranchTakesTheName(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	enter := func(sessionID, typed string) Action {
		c, err := NewCommands(com, sessionID, sessionID != "", false, false, nil, nil)
		require.NoError(t, err)
		for _, r := range typed {
			c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		return c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	require.Equal(t, ActionBranchSession{SessionID: "s1", Name: "try-the-cache"}, enter("s1", "branch try-the-cache"))
	require.Equal(t, ActionBranchSession{SessionID: "s1"}, enter("s1", "branch"))
	_, branched := enter("", "branch try-the-cache").(ActionBranchSession)
	require.False(t, branched)

	c, err := NewCommands(com, "s1", true, false, false, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "branch", c.Suggest("brnch"))
}

// Claude Code's /autocompact takes the window: typed into the palette with
// it, Enter sets it, with or without a session; alone, it is the
// Auto-compact Window item.
func TestPaletteAutocompactTakesTheWindow(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	enter := func(sessionID, typed string) Action {
		c, err := NewCommands(com, sessionID, sessionID != "", false, false, nil, nil)
		require.NoError(t, err)
		for _, r := range typed {
			c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		return c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	require.Equal(t, ActionAutocompact{Window: "150k"}, enter("", "autocompact 150k"))
	require.Equal(t, ActionAutocompact{Window: "auto"}, enter("s1", "autocompact auto"))
	require.Equal(t, ActionAutocompact{}, enter("", "autocompact"))
}
