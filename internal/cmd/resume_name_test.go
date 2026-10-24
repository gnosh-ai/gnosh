package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

type namedSessionsWorkspace struct {
	workspace.Workspace
	sessions []session.Session
	err      error
}

func (w *namedSessionsWorkspace) ListSessions(context.Context) ([]session.Session, error) {
	return w.sessions, w.err
}

// `klash -r <name>`, as Claude Code's --resume: the one session with that
// name, ignoring case; a name no session or several have is the search
// to open the list on.
func TestResumeByName(t *testing.T) {
	t.Parallel()

	ws := &namedSessionsWorkspace{sessions: []session.Session{
		{ID: "s1", Title: "fix-login-bug"},
		{ID: "s2", Title: "add-auth"},
		{ID: "s3", Title: "fix-login-bug"},
	}}
	sess, search, err := resumeByName(t.Context(), ws, " Add-Auth ")
	require.NoError(t, err)
	require.Equal(t, "s2", sess.ID)
	require.Empty(t, search)

	for _, name := range []string{"fix-login-bug", "auth"} {
		sess, search, err = resumeByName(t.Context(), ws, name)
		require.NoError(t, err)
		require.Empty(t, sess.ID, name)
		require.Equal(t, name, search)
	}

	_, _, err = resumeByName(t.Context(), &namedSessionsWorkspace{err: errors.New("database is locked")}, "x")
	require.ErrorContains(t, err, "database is locked")
}
