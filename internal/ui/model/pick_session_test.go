package model

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
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

// The UI starts on onboarding until a provider can be used, and then on
// the landing page, never on the question whether to initialize the
// project: a prompt typed at once answered it.
func TestStartStateLetsAnAskedForSessionPastTheInitQuestion(t *testing.T) {
	t.Parallel()

	require.Equal(t, uiOnboarding, startState(false))
	require.Equal(t, uiLanding, startState(true))

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

// initTipWorkspace is a project never initialized: its config names the
// context file /init writes, and it records being marked as told.
type initTipWorkspace struct {
	workspace.Workspace
	marked int
}

func (w *initTipWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{InitializeAs: "KLASH.md"}}
}

func (w *initTipWorkspace) MarkProjectInitialized() error {
	w.marked++
	return nil
}

// A project never initialized gets Claude Code's tip to run /init as the
// UI starts, once, with the editor ready, and is not asked whether to
// initialize it.
func TestInitTip(t *testing.T) {
	t.Parallel()

	needs := func() (bool, error) { return true, nil }
	initialized := func() (bool, error) { return false, nil }
	require.True(t, initTipWanted(uiLanding, false, needs))
	require.False(t, initTipWanted(uiLanding, true, needs), "a session asked for")
	require.False(t, initTipWanted(uiOnboarding, false, needs), "before onboarding is done")
	require.False(t, initTipWanted(uiLanding, false, initialized))

	u := newTestUI()
	require.Nil(t, u.initTipCmd(), "no tip unless wanted")
	ws := &initTipWorkspace{}
	u.com.Workspace = ws
	u.initTip = true
	batch, ok := u.initTipCmd()().(tea.BatchMsg)
	require.True(t, ok)
	var infos []string
	for _, cmd := range batch {
		if info, ok := cmd().(util.InfoMsg); ok {
			infos = append(infos, info.Msg)
		}
	}
	require.Equal(t, []string{"Run /init to create a KLASH.md file with instructions for Klash"}, infos)
	require.Equal(t, 1, ws.marked, "told once")
}
