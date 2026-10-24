package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// A search fills the dialog's input and filters its list as typing it
// would, with the first match chosen, whichever session was current.
func TestSessionsSearch(t *testing.T) {
	t.Parallel()

	sessions := []session.Session{
		{ID: "s1", Title: "fix-login-bug"},
		{ID: "s2", Title: "add-auth"},
		{ID: "s3", Title: "refactor-api-client"},
	}
	dialog := newSessionMouseDialog(t, sessions, "s3")
	dialog.Search("auth")
	require.Equal(t, "auth", dialog.input.Value())
	require.Equal(t, ActionSelectSession{Session: sessions[1]}, dialog.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
}
