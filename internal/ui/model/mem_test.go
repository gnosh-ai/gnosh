package model

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// memWorkspace answers /mem with what it was asked, and mem's notice with
// notice.
type memWorkspace struct {
	workspace.Workspace
	args, notice string
	err          error
}

func (w *memWorkspace) MemCommand(_ context.Context, _, args string) (string, error) {
	w.args = args
	return "mem: 1 goal open\ngoals:\n  [ ] `ship` \"ship it\"", w.err
}

func (w *memWorkspace) MemNotice(context.Context) (string, error) { return w.notice, w.err }

func (w *memWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// /mem, typed or chosen in the palette, with what follows it, asks the
// workspace and shows mem's text as it is, not as Markdown; a failure is
// reported.
func TestMemCommandOpensTheDialog(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &memWorkspace{}
	u.com.Workspace = ws
	want := reportMsg{title: "Mem", report: "mem: 1 goal open\ngoals:\n  [ ] `ship` \"ship it\"", plain: true}

	require.Equal(t, want, u.sendMessage("/mem")())
	require.Empty(t, ws.args)
	require.Equal(t, want, u.sendMessage("/mem log 5")())
	require.Equal(t, "log 5", ws.args)

	c, err := dialog.NewCommands(u.com, "", false, false, false, nil, nil)
	require.NoError(t, err)
	u.dialog.OpenDialog(c)
	for _, r := range "mem undo 3" {
		u.handleDialogMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	require.Contains(t, messagesOf(u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})), tea.Msg(want))
	require.Equal(t, "undo 3", ws.args)
	require.False(t, u.dialog.ContainsDialog(dialog.CommandsID))

	_, _ = u.Update(want)
	require.True(t, u.dialog.ContainsDialog(dialog.ReportID))

	ws.err = errors.New("mem is in use by another gnosh process; try again shortly")
	info, ok := u.memCommand("")().(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, info.Type)
}

// As gnosh starts, mem's notice is shown when there is one.
func TestMemNotice(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	ws := &memWorkspace{}
	u.com.Workspace = ws
	require.Nil(t, u.memNotice()())
	ws.notice = `mem waiting on you: "which day suits you?" (plan-release/1)`
	info, ok := u.memNotice()().(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, ws.notice, info.Msg)
	require.Equal(t, util.InfoTypeInfo, info.Type)
}

// mem's notice comes again with a prompt: a new session's first, unless
// the notice shown as gnosh started stands for it, and the first of a new
// day; not with every prompt.
func TestMemNoticeOnPrompt(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.com.Workspace = &memWorkspace{notice: `mem waiting on you: "which day suits you?" (plan-release/1)`}
	u.memNoticeDay, u.memNoticeFresh = time.Now().Format(time.DateOnly), true // as Init leaves them
	require.Nil(t, u.memNoticeOnPrompt(), "the first session's first prompt: the start's notice stands")
	require.NotNil(t, u.memNoticeOnPrompt(), "a later new session's first prompt")
	u.session = &session.Session{ID: "s1"}
	require.Nil(t, u.memNoticeOnPrompt(), "a session's later prompt, the same day")
	u.memNoticeDay = "2026-10-29"
	cmd := u.memNoticeOnPrompt()
	require.NotNil(t, cmd, "the first prompt of a new day")
	info, ok := cmd().(util.InfoMsg)
	require.True(t, ok)
	require.Contains(t, info.Msg, "which day suits you?")
	require.Nil(t, u.memNoticeOnPrompt(), "once a day")
}

// Esc Esc keeps what the prompt held for Up to bring back, but not a /mem
// redact: its text is a secret.
func TestClearPromptForgetsMemRedact(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.textarea.SetValue("/mem redact sk-live-7d1e9a")
	u.clearPrompt()
	require.Empty(t, u.promptHistory.cleared)
	u.textarea.SetValue("/mem log")
	u.clearPrompt()
	require.Len(t, u.promptHistory.cleared, 1)
	require.Equal(t, "/mem log", u.promptHistory.cleared[0].text)
}
