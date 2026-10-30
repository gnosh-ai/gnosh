package model

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// attachWorkspace has a config for the palette and an agent that is not
// ready, so a prompt sent comes back as not sent, with what it carried.
type attachWorkspace struct {
	workspace.Workspace
}

func (w *attachWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
}

func (w *attachWorkspace) AgentReadyErr() error { return errors.New("not ready") }

// What is attached to a command typed in the editor is not lost: a
// command of gnosh's own, the palette's or one no command has leaves it
// in the editor for the prompt it was meant for, and a custom command's
// prompt takes it, as Claude Code sends what was pasted with one, even
// once its arguments are asked for, while it waits in the editor. A
// prompt takes it, as before, one starting with a path too.
func TestCommandsKeepAttachments(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.attachments = attachments.New(nil, attachments.Keymap{})
	u.com.Workspace = &attachWorkspace{}
	review := commands.CustomCommand{ID: "project:review", Name: "project:review", Content: "Review $PR", Arguments: []commands.Argument{{ID: "PR", Title: "PR", Required: true}}}
	u.customCommands = []commands.CustomCommand{review}
	shot := message.Attachment{FilePath: "/tmp/shot.png", FileName: "shot.png", MimeType: "image/png", Content: []byte("png")}
	kept := func() []message.Attachment {
		got := u.attachments.List()
		u.attachments.Reset()
		return got
	}
	sent := func(cmd tea.Cmd) agentRunSubmittedMsg {
		t.Helper()
		msg, ok := cmd().(agentRunSubmittedMsg)
		require.True(t, ok)
		return msg
	}

	u.sendMessage("/branch", shot)
	require.Equal(t, []message.Attachment{shot}, kept(), "a command of gnosh's own")
	u.sendMessage("/help", shot)
	require.Equal(t, []message.Attachment{shot}, kept(), "the palette's")
	require.Equal(t, util.NewWarnMsg("Unknown command: /nosuch"), u.sendMessage("/nosuch", shot)())
	require.Equal(t, "/nosuch", u.textarea.Value())
	require.Equal(t, []message.Attachment{shot}, kept(), "one no command has")
	u.textarea.Reset()

	run := sent(u.sendMessage("/review 42", shot))
	require.Equal(t, "Review 42", run.prompt)
	require.Equal(t, []message.Attachment{shot}, run.attachments, "a custom command's prompt")
	require.Empty(t, kept())

	u.sendMessage("/review", shot)
	require.True(t, u.dialog.ContainsDialog(dialog.ArgumentsID))
	require.Equal(t, []message.Attachment{shot}, u.attachments.List(), "waiting while its arguments are asked")
	run = sent(u.handleDialogAction(dialog.ActionRunCustomCommand{Content: review.Content, Arguments: review.Arguments, Args: map[string]string{"PR": "7"}}))
	require.Equal(t, "Review 7", run.prompt)
	require.Equal(t, []message.Attachment{shot}, run.attachments)
	require.Empty(t, kept())

	run = sent(u.sendMessage("what is in it?", shot))
	require.Equal(t, []message.Attachment{shot}, run.attachments, "a prompt")
	require.Empty(t, kept())
	run = sent(u.sendMessage("/tmp is it full?", shot))
	require.Equal(t, []message.Attachment{shot}, run.attachments, "a prompt that starts with a path")
	require.Empty(t, kept())
}
