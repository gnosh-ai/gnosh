package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// slashWorkspace has a config for the palette.
type slashWorkspace struct {
	workspace.Workspace
}

func (w *slashWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// A slash command typed in the prompt runs the palette's command of that
// name, as Claude Code runs one wherever it is typed: /help toggles the
// help, and a custom command asks for its arguments. One no command has
// is said to be unknown, with the one likely meant, and goes back in the
// editor instead of to the model; a path is a prompt.
func TestTypedSlashCommands(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.com.Workspace = &slashWorkspace{}
	u.customCommands = []commands.CustomCommand{{ID: "project:review", Name: "project:review", Content: "Review $PR", Arguments: []commands.Argument{{ID: "PR", Title: "PR", Required: true}}}}

	require.False(t, u.status.ShowingAll())
	u.sendMessage("/help")
	require.True(t, u.status.ShowingAll())
	u.sendMessage(" /HELP")
	require.False(t, u.status.ShowingAll())

	u.sendMessage("/review")
	require.True(t, u.dialog.ContainsDialog(dialog.ArgumentsID))
	u.dialog.CloseDialog(dialog.ArgumentsID)

	u.textarea.Reset()
	require.Equal(t, util.NewWarnMsg("Unknown command: /stauts. Did you mean /status?"), u.sendMessage("/stauts now")())
	require.Equal(t, "/stauts now", u.textarea.Value(), "the line is not lost")
	u.textarea.Reset()
	require.Equal(t, util.NewWarnMsg("Unknown command: /zqxzqx"), u.sendMessage("/zqxzqx")())

	_, ok := u.slashCommand("/tmp is it full?", "tmp")
	require.False(t, ok, "a path that is there is a prompt")
}
