package model

import (
	"cmp"
	"context"
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// branchWorkspace makes a branch of a session, recording what it was
// asked for, and the sessions asked for to load, and loads none; it has a
// config for the palette.
type branchWorkspace struct {
	workspace.Workspace
	from, title string
	err         error
	loaded      []string
}

func (w *branchWorkspace) BranchSession(_ context.Context, sessionID, title string) (session.Session, error) {
	w.from, w.title = sessionID, title
	return session.Session{ID: "b1", Title: cmp.Or(title, "fix-login-bug (Branch)")}, w.err
}

func (w *branchWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	w.loaded = append(w.loaded, id)
	return session.Session{}, errors.New("not loaded in this test")
}

func (w *branchWorkspace) SetCurrentSession(context.Context, string) error { return nil }

func (w *branchWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// /branch, typed or from the palette, has the session's conversation
// branched, with the name given kept as /rename keeps one; with no
// session, or a busy agent, there is nothing to branch yet.
func TestBranchSession(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &branchWorkspace{}
	u.com.Workspace = ws

	require.Equal(t, util.NewWarnMsg("No conversation to branch"), u.sendMessage("/branch")())

	from := session.Session{ID: "s1", Title: "fix-login-bug"}
	u.session = &from
	u.agentBusyCache.set(true)
	require.Equal(t, util.NewWarnMsg("Agent is busy, please wait before branching the conversation..."), u.sendMessage("/branch")())
	u.agentBusyCache.set(false)

	require.Equal(t, branchedMsg{branch: session.Session{ID: "b1", Title: "try the cache"}, from: from, named: true}, u.sendMessage("/branch  try\tthe cache ")())
	require.Equal(t, "s1", ws.from)
	require.Equal(t, "try the cache", ws.title)

	unnamed := branchedMsg{branch: session.Session{ID: "b1", Title: "fix-login-bug (Branch)"}, from: from}
	require.Equal(t, unnamed, u.handleDialogAction(dialog.ActionBranchSession{SessionID: "s1"})())
	require.Equal(t, "", ws.title, "the palette item, with no name")

	ws.err = errors.New("disk full")
	info, ok := u.sendMessage("/branch")().(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Equal(t, "failed to branch conversation: disk full", info.Msg)
}

// What /branch says is what Claude Code says: the branch's name only when
// it was given one, and the original's title only when it has one.
func TestBranchedSaid(t *testing.T) {
	t.Parallel()

	run := "`" + version.Command + " -r s1`"
	from := session.Session{ID: "s1", Title: "fix-login-bug"}
	require.Equal(t, `Branched conversation "try the cache". You are now in the new branch (session b1). Use /resume s1 ("fix-login-bug") to return to the original, or run `+run+` in a new terminal.`,
		branchedMsg{branch: session.Session{ID: "b1", Title: "try the cache"}, from: from, named: true}.said())
	require.Equal(t, `Branched conversation. You are now in the new branch (session b1). Use /resume s1 ("fix-login-bug") to return to the original, or run `+run+` in a new terminal.`,
		branchedMsg{branch: session.Session{ID: "b1", Title: "fix-login-bug (Branch)"}, from: from}.said())
	require.Equal(t, `Branched conversation. You are now in the new branch (session b1). Use /resume s1 to return to the original, or run `+run+` in a new terminal.`,
		branchedMsg{branch: session.Session{ID: "b1", Title: "Branched conversation (Branch)"}, from: session.Session{ID: "s1"}}.said())
}

// Once the branch is made, gnosh goes on in it and says so.
func TestBranchedGoesOnInTheBranch(t *testing.T) {
	pinTTLs(t)
	u := newTestUI()
	ws := &branchWorkspace{}
	u.com.Workspace = ws
	warmCaches(u, false)
	u.session = &session.Session{ID: "s1", Title: "fix-login-bug"}

	msg := branchedMsg{branch: session.Session{ID: "b1", Title: "fix-login-bug (Branch)"}, from: *u.session}
	_, cmd := u.Update(msg)
	require.Contains(t, infoMsgs(cmd), msg.said())
	require.Equal(t, []string{"b1"}, ws.loaded)
}
