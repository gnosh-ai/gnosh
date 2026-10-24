package dialog

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The export dialog asks as Claude Code's /export does: Enter on "Copy
// to clipboard" copies; "Save to file" asks for the file's name,
// suggesting one, and Enter saves it there; Esc while naming goes back
// to the choice, and Esc at the choice closes the dialog.
func TestExportDialog(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{Styles: &sty}
	const suggested = "2026-10-24-221500-fix-the-login-bug.txt"
	key := func(d *Export, k string) Action {
		switch k {
		case "enter":
			return d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
		case "esc":
			return d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
		case "up":
			return d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
		case "down":
			return d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
		case "backspace":
			return d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyBackspace})
		}
		var last Action
		for _, r := range k {
			last = d.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		return last
	}
	draw := func(d *Export) (string, *tea.Cursor) {
		scr := uv.NewScreenBuffer(100, 30)
		cur := d.Draw(scr, uv.Rect(0, 0, 100, 30))
		return ansi.Strip(scr.Render()), cur
	}

	d := NewExport(com, suggested)
	view, cur := draw(d)
	for _, want := range []string{"Export Conversation", "Select export method", "Copy to clipboard", "Copy the conversation to your system clipboard", "Save to file", "Save the conversation to a file in the current directory"} {
		require.Contains(t, view, want)
	}
	require.Nil(t, cur)
	require.Equal(t, ActionExportConversation{Clipboard: true}, key(d, "enter"))

	// Down to "Save to file": its name is asked for, the suggestion
	// filled in. j and k are typed, not moves.
	key(d, "down")
	_, naming := key(d, "enter").(ActionCmd)
	require.True(t, naming)
	view, cur = draw(d)
	require.Contains(t, view, suggested)
	require.Contains(t, view, "Enter filename")
	require.NotNil(t, cur)
	require.Equal(t, ActionExportConversation{Filename: suggested}, key(d, "enter"))
	for range len(".txt") {
		key(d, "backspace")
	}
	key(d, ".jk")
	require.Equal(t, ActionExportConversation{Filename: strings.TrimSuffix(suggested, ".txt") + ".jk"}, key(d, "enter"))

	// No name, no file.
	for range len(suggested) {
		key(d, "backspace")
	}
	_, warned := key(d, "enter").(ActionCmd)
	require.True(t, warned)

	// Esc goes back to the choice, still on "Save to file"; Up and Enter
	// copy instead; Esc there closes.
	require.Nil(t, key(d, "esc"))
	view, _ = draw(d)
	require.Contains(t, view, "Select export method")
	_, naming = key(d, "enter").(ActionCmd)
	require.True(t, naming)
	require.Nil(t, key(d, "esc"))
	key(d, "up")
	require.Equal(t, ActionExportConversation{Clipboard: true}, key(d, "enter"))
	require.Equal(t, ActionClose{}, key(d, "esc"))
}
