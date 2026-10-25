package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/skills"
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

	_, ok := u.slashCommand("/tmp is it full?", "tmp", nil)
	require.False(t, ok, "a path that is there is a prompt")
}

// What is typed after a command's name is its arguments, as in Claude
// Code: a custom command runs with them in its text (here the text is
// itself /help, which toggles the help), and a skill is attached with
// them left in the editor as its prompt, ready to send.
func TestTypedSlashCommandArgs(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.com.Workspace = &slashWorkspace{}
	u.customCommands = []commands.CustomCommand{
		{ID: "project:check", Name: "project:check", Content: "/help $NOTE", Arguments: []commands.Argument{{ID: "NOTE", Title: "NOTE", Required: true}}},
		{ID: "user:gno", Name: "user:gno", Skill: &skills.Skill{Name: "gno-dev", SkillFilePath: "/s/gno/SKILL.md"}},
	}

	u.sendMessage("/check later")
	require.True(t, u.status.ShowingAll(), "ran as /help later")
	require.False(t, u.dialog.ContainsDialog(dialog.ArgumentsID), "its argument was given")
	u.sendMessage("/check")
	require.True(t, u.dialog.ContainsDialog(dialog.ArgumentsID), "without one it is asked for")
	u.dialog.CloseDialog(dialog.ArgumentsID)

	u.textarea.Reset()
	require.NotNil(t, u.sendMessage("/gno-dev deploy the counter realm"))
	require.Equal(t, "deploy the counter realm", u.textarea.Value())
}
