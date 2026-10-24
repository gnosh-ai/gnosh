package dialog

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
)

// ExportID is the identifier for the export dialog.
const ExportID = "export"

// exportMethods are the ways Claude Code's /export offers to export the
// conversation, each with what it does.
var exportMethods = [...]struct{ label, description string }{
	{"Copy to clipboard", "Copy the conversation to your system clipboard"},
	{"Save to file", "Save the conversation to a file in the current directory"},
}

// Export asks how to export the conversation, as Claude Code's /export
// does when no file is named: copied to the clipboard, or saved to a file
// whose name it asks for, suggesting one. Esc while naming the file goes
// back to the choice.
type Export struct {
	com *common.Common
	// selected is the method chosen, an index in exportMethods; naming is
	// whether the file's name is being asked for.
	selected int
	naming   bool
	input    textinput.Model
	help     help.Model
	keyMap   struct {
		Up, Down, Select, Save, Back, Close key.Binding
	}
}

var _ Dialog = (*Export)(nil)

// NewExport creates the export dialog, suggesting filename for the file.
func NewExport(com *common.Common, filename string) *Export {
	d := &Export{com: com}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Up = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	d.keyMap.Down = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	d.keyMap.Select = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select"))
	d.keyMap.Save = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "save"))
	d.keyMap.Back = key.NewBinding(key.WithKeys("esc", "alt+esc"), key.WithHelp("esc", "go back"))
	d.keyMap.Close = CloseKey
	d.input = textinput.New()
	d.input.SetVirtualCursor(false)
	d.input.SetStyles(com.Styles.TextInput)
	d.input.Prompt = "> "
	d.input.SetValue(filename)
	return d
}

// ID implements [Dialog].
func (*Export) ID() string {
	return ExportID
}

// HandleMsg implements [Dialog].
func (d *Export) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if d.naming {
			switch {
			case key.Matches(msg, d.keyMap.Back):
				d.naming = false
				d.input.Blur()
				return nil
			case key.Matches(msg, d.keyMap.Save):
				filename := strings.TrimSpace(d.input.Value())
				if filename == "" {
					return ActionCmd{Cmd: util.ReportWarn("Type the name of the file to save the conversation to")}
				}
				return ActionExportConversation{Filename: filename}
			}
			var cmd tea.Cmd
			d.input, cmd = d.input.Update(msg)
			return ActionCmd{Cmd: cmd}
		}
		switch {
		case key.Matches(msg, d.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, d.keyMap.Up):
			d.selected = max(0, d.selected-1)
		case key.Matches(msg, d.keyMap.Down):
			d.selected = min(len(exportMethods)-1, d.selected+1)
		case key.Matches(msg, d.keyMap.Select):
			if d.selected == 0 {
				return ActionExportConversation{Clipboard: true}
			}
			d.naming = true
			return ActionCmd{Cmd: d.input.Focus()}
		}
	case tea.PasteMsg:
		if d.naming {
			var cmd tea.Cmd
			d.input, cmd = d.input.Update(msg)
			return ActionCmd{Cmd: cmd}
		}
	}
	return nil
}

// Draw implements [Dialog].
func (d *Export) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	width := max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	rc := NewRenderContext(t, width)
	rc.Title = "Export Conversation"
	var cur *tea.Cursor
	if d.naming {
		// The input comes first, right under the title, where
		// InputCursor puts the cursor.
		d.input.SetWidth(dialogInputTextWidth(t, d.input, innerWidth))
		rc.AddPart(t.Dialog.InputPrompt.Render(d.input.View()))
		rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render("Enter filename: .txt is added when it has no extension."))
		cur = InputCursor(t, d.input.Cursor())
	} else {
		rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render("Select export method"))
		for i, method := range exportMethods {
			style := t.Dialog.NormalItem
			if i == d.selected {
				style = t.Dialog.SelectedItem
			}
			rc.AddPart(style.Width(innerWidth).Render(method.label))
			rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render(method.description))
		}
	}
	rc.Help = renderDialogHelp(t, &d.help, d, innerWidth)
	DrawCenterCursor(scr, area, rc.Render(), cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (d *Export) ShortHelp() []key.Binding {
	if d.naming {
		return []key.Binding{d.keyMap.Save, d.keyMap.Back}
	}
	return []key.Binding{d.keyMap.Up, d.keyMap.Down, d.keyMap.Select, d.keyMap.Close}
}

// FullHelp implements [help.KeyMap].
func (d *Export) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}
