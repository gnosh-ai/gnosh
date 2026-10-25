package dialog

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// CopyPickerID is the identifier for the copy picker dialog.
const CopyPickerID = "copy_picker"

// CopyChoice is a part of a response /copy can copy: the whole response,
// or one of its code blocks.
type CopyChoice struct {
	// Label is what the list shows, and Meta its kind and size.
	Label, Meta string
	Text        string
	// FileName is the file the copy is also written to, as Claude Code
	// names it.
	FileName string
}

// CopyPicker asks which part of a response to copy, as Claude Code's
// /copy does when the response has code blocks.
type CopyPicker struct {
	com      *common.Common
	choices  []CopyChoice
	selected int
	help     help.Model
	keyMap   struct {
		Up, Down, Select, Close key.Binding
	}
}

var _ Dialog = (*CopyPicker)(nil)

// NewCopyPicker creates the dialog on choices, the full response first.
func NewCopyPicker(com *common.Common, choices []CopyChoice) *CopyPicker {
	d := &CopyPicker{com: com, choices: choices}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Up = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	d.keyMap.Down = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	d.keyMap.Select = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "copy"))
	d.keyMap.Close = CloseKey
	return d
}

// ID implements [Dialog].
func (*CopyPicker) ID() string {
	return CopyPickerID
}

// HandleMsg implements [Dialog].
func (d *CopyPicker) HandleMsg(msg tea.Msg) Action {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(press, d.keyMap.Close):
		return ActionClose{}
	case key.Matches(press, d.keyMap.Up):
		d.selected = max(0, d.selected-1)
	case key.Matches(press, d.keyMap.Down):
		d.selected = min(len(d.choices)-1, d.selected+1)
	case key.Matches(press, d.keyMap.Select):
		if d.selected < len(d.choices) {
			c := d.choices[d.selected]
			return ActionCopyText{Text: c.Text, FileName: c.FileName}
		}
	}
	return nil
}

// Draw implements [Dialog].
func (d *CopyPicker) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	width := max(0, min(reportMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	rc := NewRenderContext(t, width)
	rc.Title = "Copy"
	rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render("Select content to copy"))
	for i, c := range d.choices {
		style := t.Dialog.NormalItem
		if i == d.selected {
			style = t.Dialog.SelectedItem
		}
		room := innerWidth - style.GetHorizontalFrameSize()
		line := ansi.Truncate(c.Label, room, "…")
		if c.Meta != "" && ansi.StringWidth(line)+2+ansi.StringWidth(c.Meta) <= room {
			line += "  " + c.Meta
		}
		rc.AddPart(style.Width(innerWidth).Render(line))
	}
	rc.Help = renderDialogHelp(t, &d.help, d, innerWidth)
	DrawCenter(scr, area, rc.Render())
	return nil
}

// ShortHelp implements [help.KeyMap].
func (d *CopyPicker) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Up, d.keyMap.Down, d.keyMap.Select, d.keyMap.Close}
}

// FullHelp implements [help.KeyMap].
func (d *CopyPicker) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}
