package model

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

type pickSessionWorkspace struct {
	workspace.Workspace
	sessions []session.Session
	err      error
}

func (w *pickSessionWorkspace) ListSessions(context.Context) ([]session.Session, error) {
	return w.sessions, w.err
}

// A bare `klash -r` opens the sessions list as the UI starts, as Claude
// Code's `claude -r` opens its picker; with no session to pick, it says so
// instead of opening an empty list.
func TestStartupSessionPicker(t *testing.T) {
	t.Parallel()

	start := func(ws *pickSessionWorkspace, pick bool) any {
		u := newTestUI()
		u.com.Workspace = ws
		u.state = uiLanding
		if pick {
			u.PickSessionAtStart("")
		}
		cmd := u.startupSessionPicker()
		if cmd == nil {
			return nil
		}
		return cmd()
	}

	ws := &pickSessionWorkspace{sessions: []session.Session{{ID: "s1", Title: "Fix the build"}}}
	require.Equal(t, pickSessionMsg{}, start(ws, true))
	require.Nil(t, start(ws, false), "without -r the UI starts as before")

	msg, ok := start(&pickSessionWorkspace{}, true).(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeInfo, msg.Type)
	require.Contains(t, msg.Msg, "No sessions to resume")

	msg, ok = start(&pickSessionWorkspace{err: errors.New("database is locked")}, true).(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, msg.Type)
	require.Contains(t, msg.Msg, "database is locked")
}

// Before onboarding is done there is no session to go back to.
func TestStartupSessionPickerWaitsOutOnboarding(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.com.Workspace = &pickSessionWorkspace{sessions: []session.Session{{ID: "s1"}}}
	u.state = uiOnboarding
	u.PickSessionAtStart("")
	require.Nil(t, u.startupSessionPicker())
}

// A session asked for on the command line is not put behind the question
// whether to initialize the project: answering it either way dropped the
// session, so `klash -C` in such a project came up without it.
func TestStartStateLetsAnAskedForSessionPastTheInitQuestion(t *testing.T) {
	t.Parallel()

	needsInit := func() bool { return true }
	noInit := func() bool { return false }
	require.Equal(t, uiOnboarding, startState(false, needsInit, true))
	require.Equal(t, uiInitialize, startState(true, needsInit, false))
	require.Equal(t, uiLanding, startState(true, needsInit, true))
	require.Equal(t, uiLanding, startState(true, noInit, false))

	u := newTestUI()
	u.com.Workspace = &pickSessionWorkspace{sessions: []session.Session{{ID: "s1"}}}
	u.state = uiInitialize
	u.PickSessionAtStart("")
	require.Equal(t, uiLanding, u.state)
	require.NotNil(t, u.startupSessionPicker())
}

// The startup message reaches the screen: Update opens the sessions list
// with no other dialog open. dialog.ActionOpenDialog, sent the same way,
// was dropped, since only an open dialog's answers are acted on.
func TestPickSessionMsgOpensTheSessionsList(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.com.Workspace = &pickSessionWorkspace{sessions: []session.Session{{ID: "s1", Title: "Fix the build"}}}
	u.state = uiLanding
	require.False(t, u.dialog.HasDialogs())

	_, _ = u.Update(pickSessionMsg{})
	require.True(t, u.dialog.ContainsDialog(dialog.SessionsID))
}

// --permission-mode plan shows the TUI in plan mode from the start.
func TestStartInPlanMode(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	require.Equal(t, uiInputModeCode, u.mode)
	u.StartInPlanMode()
	require.Equal(t, uiInputModePlan, u.mode)
}

// `klash -r <name>` with a name no session or several have opens the list
// filtered by the name, as Claude Code's --resume opens its picker on the
// search; the filter reaches the list, so Enter takes the first match.
func TestStartupSessionPickerSearches(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	sessions := []session.Session{{ID: "s1", Title: "fix-login-bug"}, {ID: "s2", Title: "add-auth"}}
	u.com.Workspace = &pickSessionWorkspace{sessions: sessions}
	u.state = uiLanding
	u.PickSessionAtStart("auth")
	msg := u.startupSessionPicker()()
	require.Equal(t, pickSessionMsg{search: "auth"}, msg)

	_, _ = u.Update(msg)
	require.True(t, u.dialog.ContainsDialog(dialog.SessionsID))
	require.Equal(t, dialog.ActionSelectSession{Session: sessions[1]}, u.dialog.Update(tea.KeyPressMsg{Code: tea.KeyEnter}))
}
