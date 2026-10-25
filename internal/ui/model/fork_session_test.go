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

// forkWorkspace lists and gets its sessions and branches any of them,
// recording which it was asked to branch.
type forkWorkspace struct {
	workspace.Workspace
	sessions []session.Session
	branched []string
}

func (w *forkWorkspace) ListSessions(context.Context) ([]session.Session, error) {
	return w.sessions, nil
}

func (w *forkWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	for _, s := range w.sessions {
		if s.ID == id {
			return s, nil
		}
	}
	return session.Session{}, errors.New("no such session")
}

func (w *forkWorkspace) BranchSession(_ context.Context, id, title string) (session.Session, error) {
	w.branched = append(w.branched, id)
	return session.Session{ID: "b-" + id, Title: id + " (Branch)"}, nil
}

func (w *forkWorkspace) SetCurrentSession(context.Context, string) error { return nil }

// forkTestSessions are the sessions of a forkWorkspace, the most recent
// first, as ListSessions orders them.
var forkTestSessions = []session.Session{{ID: "s2", Title: "add-auth"}, {ID: "s1", Title: "fix-login-bug"}}

// Claude Code's --fork-session: the UI goes on in a copy of the session it
// starts in, the one named on the command line or the most recent one,
// and says so as /branch does; without it, the UI loads the session
// itself. A session that cannot be read is not copied.
func TestForkSessionAtStart(t *testing.T) {
	t.Parallel()

	start := func(id string, last, fork bool) (*forkWorkspace, tea.Msg) {
		ws := &forkWorkspace{sessions: forkTestSessions}
		u := newTestUI()
		u.com.Workspace = ws
		u.state = uiLanding
		u.initialSessionID, u.continueLastSession = id, last
		if fork {
			u.ForkSessionAtStart()
		}
		return ws, u.loadInitialSession()()
	}

	ws, msg := start("s1", false, true)
	require.Equal(t, branchedMsg{branch: session.Session{ID: "b-s1", Title: "s1 (Branch)"}, from: forkTestSessions[1]}, msg)
	require.Equal(t, []string{"s1"}, ws.branched)

	ws, msg = start("", true, true)
	require.Equal(t, branchedMsg{branch: session.Session{ID: "b-s2", Title: "s2 (Branch)"}, from: forkTestSessions[0]}, msg, "the most recent session")
	require.Equal(t, []string{"s2"}, ws.branched)

	for _, tc := range []struct {
		id   string
		last bool
	}{{"s1", false}, {"", true}} {
		ws, msg = start(tc.id, tc.last, false)
		require.IsType(t, tea.BatchMsg{}, msg, "the session is loaded, %+v", tc)
		require.Empty(t, ws.branched, "%+v", tc)
	}

	ws, msg = start("gone", false, true)
	info, ok := msg.(util.InfoMsg)
	require.True(t, ok, "%#v", msg)
	require.Equal(t, util.InfoTypeError, info.Type)
	require.Equal(t, "no such session", info.Msg)
	require.Empty(t, ws.branched)
}

// With -r and --fork-session, the session picked from the list opened at
// the start is the one copied; a session picked from a list opened later
// is gone to, as before.
func TestForkSessionPickedAtStart(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &forkWorkspace{sessions: forkTestSessions}
	u.com.Workspace = ws
	u.state = uiLanding
	u.PickSessionAtStart("")
	u.ForkSessionAtStart()
	_, _ = u.Update(u.startupSessionPicker()())
	picked := u.dialog.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, dialog.ActionSelectSession{Session: forkTestSessions[0], Fork: true}, picked)
	require.Equal(t, branchedMsg{branch: session.Session{ID: "b-s2", Title: "s2 (Branch)"}, from: forkTestSessions[0]}, u.handleDialogAction(picked)())
	require.Equal(t, []string{"s2"}, ws.branched)
	require.False(t, u.dialog.ContainsDialog(dialog.SessionsID))

	require.Nil(t, u.openSessionsDialog(""))
	picked = u.dialog.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, dialog.ActionSelectSession{Session: forkTestSessions[0]}, picked)
	require.IsType(t, tea.BatchMsg{}, u.handleDialogAction(picked)(), "the session is loaded")
	require.Equal(t, []string{"s2"}, ws.branched, "and not copied")
}
