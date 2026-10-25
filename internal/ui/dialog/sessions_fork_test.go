package dialog

import (
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/session"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// A list opened for --fork-session picks, by Enter or a double click, the
// session to go on in a copy of.
func TestSessionsFork(t *testing.T) {
	t.Parallel()

	sessions := []session.Session{{ID: "s1", Title: "First"}, {ID: "s2", Title: "Second"}}
	dialog := newSessionMouseDialog(t, sessions, "s1")
	dialog.Fork()
	dialog.Draw(uv.NewScreenBuffer(80, 30), image.Rect(0, 0, 80, 30))
	listArea := dialog.sessionListArea()
	click := tea.MouseClickMsg(tea.Mouse{X: listArea.Min.X, Y: listArea.Min.Y + 1, Button: tea.MouseLeft})

	require.Nil(t, dialog.HandleMsg(click))
	require.Equal(t, ActionSelectSession{Session: sessions[1], Fork: true}, dialog.HandleMsg(click))
	require.Equal(t, ActionSelectSession{Session: sessions[1], Fork: true}, dialog.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))

	// With --name, the session picked, or its copy, is to be named.
	dialog.Name("nightly check")
	require.Nil(t, dialog.HandleMsg(click))
	require.Equal(t, ActionSelectSession{Session: sessions[1], Fork: true, Name: "nightly check"}, dialog.HandleMsg(click))
	require.Equal(t, ActionSelectSession{Session: sessions[1], Fork: true, Name: "nightly check"}, dialog.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
}
