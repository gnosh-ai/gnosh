package model

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// resumeWorkspace lists sessions and keeps the IDs of those asked for, to
// load, and loads none.
type resumeWorkspace struct {
	workspace.Workspace
	sessions []session.Session
	loaded   []string
}

func (w *resumeWorkspace) ListSessions(context.Context) ([]session.Session, error) {
	return w.sessions, nil
}

func (w *resumeWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	w.loaded = append(w.loaded, id)
	return session.Session{}, errors.New("not loaded in this test")
}

// /resume goes to a session by its ID, its hash ID, or the name only it
// has, ignoring case; with nothing, or a name no session or several have,
// it opens the sessions list, filtered by the name.
func TestResumeSession(t *testing.T) {
	t.Parallel()

	sessions := []session.Session{
		{ID: "s1", Title: "fix-login-bug", UpdatedAt: 1},
		{ID: "s2", Title: "add-auth", UpdatedAt: 2},
		{ID: "s3", Title: "Fix-Login-Bug", UpdatedAt: 3},
	}
	resume := func(ref string) (*UI, *resumeWorkspace) {
		u := newTestUI()
		u.dialog = dialog.NewOverlay()
		ws := &resumeWorkspace{sessions: sessions}
		u.com.Workspace = ws
		if cmd := u.resumeSession(ref); cmd != nil {
			batch, ok := cmd().(tea.BatchMsg)
			require.True(t, ok, "ref %q", ref)
			batch[0]()
		}
		return u, ws
	}

	for _, ref := range []string{"s2", session.HashID("s2"), "ADD-AUTH"} {
		u, ws := resume(ref)
		require.Equal(t, []string{"s2"}, ws.loaded, "ref %q", ref)
		require.False(t, u.dialog.ContainsDialog(dialog.SessionsID), "ref %q", ref)
	}

	u, ws := resume("")
	require.Empty(t, ws.loaded)
	require.True(t, u.dialog.ContainsDialog(dialog.SessionsID))

	u, ws = resume("fix-login-bug")
	require.Empty(t, ws.loaded, "two sessions have that name")
	require.True(t, u.dialog.ContainsDialog(dialog.SessionsID))
	picked, ok := u.dialog.Update(tea.KeyPressMsg{Code: tea.KeyEnter}).(dialog.ActionSelectSession)
	require.True(t, ok)
	require.Contains(t, []string{"s1", "s3"}, picked.Session.ID, "the list is filtered by the name")

	u, ws = resume("auth")
	require.Empty(t, ws.loaded, "part of a name is no name")
	require.Equal(t, dialog.ActionSelectSession{Session: sessions[1]}, u.dialog.Update(tea.KeyPressMsg{Code: tea.KeyEnter}))
}
