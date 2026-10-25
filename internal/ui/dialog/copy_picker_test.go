package dialog

import (
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The copy picker lists the choices, each with its kind and size; ↓ and ↑
// choose, Enter copies the chosen one, Esc closes.
func TestCopyPicker(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	choices := []CopyChoice{
		{Label: "Full response", Meta: "40 chars, 4 lines", Text: "whole", FileName: "response.md"},
		{Label: "npm test", Meta: "bash", Text: "npm test", FileName: "copy.bash"},
	}
	d := NewCopyPicker(&common.Common{Styles: &sty}, choices)
	scr := uv.NewScreenBuffer(100, 30)
	d.Draw(scr, image.Rect(0, 0, 100, 30))
	view := ansi.Strip(scr.Render())
	for _, want := range []string{"Copy", "Select content to copy", "Full response  40 chars, 4 lines", "npm test  bash"} {
		require.Contains(t, view, want)
	}

	require.Equal(t, ActionCopyText{Text: "whole", FileName: "response.md"}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	require.Equal(t, ActionCopyText{Text: "npm test", FileName: "copy.bash"}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}), "stays on the last")
	d.HandleMsg(tea.KeyPressMsg{Code: 'k', Text: "k"})
	require.Equal(t, ActionCopyText{Text: "whole", FileName: "response.md"}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
	require.Equal(t, ActionClose{}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}
