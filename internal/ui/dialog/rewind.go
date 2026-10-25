package dialog

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

const (
	// RewindID is the identifier for the dialog listing the prompts
	// /rewind can go back to.
	RewindID = "rewind"
	// RewindConfirmID is the identifier for the dialog asking what to
	// restore to the point before one of them.
	RewindConfirmID = "rewind_confirm"
)

// rewindVisible is how many prompts the rewind dialog shows at once.
const rewindVisible = 12

// RewindPrompt is a prompt of the session's that /rewind can go back to.
type RewindPrompt struct {
	MessageID string
	Text      string
	// Attachments are what was attached to it.
	Attachments []message.Attachment
}

// line is the prompt in one line: its first, or what was attached.
func (p RewindPrompt) line() string {
	first, _, more := strings.Cut(strings.TrimSpace(p.Text), "\n")
	switch {
	case first == "":
		return fmt.Sprintf("(%d attached)", len(p.Attachments))
	case more:
		return first + " …"
	}
	return first
}

// Rewind lists the session's prompts, oldest first, with the latest
// chosen, as Claude Code's /rewind does: Enter asks what to restore to
// the point before the one chosen.
type Rewind struct {
	com       *common.Common
	sessionID string
	prompts   []RewindPrompt
	selected  int
	// offset is the first prompt shown.
	offset int
	help   help.Model
	keyMap struct {
		Up, Down, Select, Close key.Binding
	}
}

var _ Dialog = (*Rewind)(nil)

// NewRewind creates the dialog on the prompts of session sessionID.
func NewRewind(com *common.Common, sessionID string, prompts []RewindPrompt) *Rewind {
	d := &Rewind{com: com, sessionID: sessionID, prompts: prompts, selected: len(prompts) - 1}
	d.offset = max(0, len(prompts)-rewindVisible)
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Up = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	d.keyMap.Down = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	d.keyMap.Select = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "continue"))
	d.keyMap.Close = CloseKey
	return d
}

// ID implements [Dialog].
func (*Rewind) ID() string {
	return RewindID
}

// HandleMsg implements [Dialog].
func (d *Rewind) HandleMsg(msg tea.Msg) Action {
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
		d.selected = min(len(d.prompts)-1, d.selected+1)
	case key.Matches(press, d.keyMap.Select):
		if d.selected >= 0 && d.selected < len(d.prompts) {
			return ActionRewindPick{SessionID: d.sessionID, Prompt: d.prompts[d.selected]}
		}
	}
	d.offset = min(d.offset, d.selected)
	d.offset = max(d.offset, d.selected-rewindVisible+1)
	return nil
}

// Draw implements [Dialog].
func (d *Rewind) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	width := max(0, min(reportMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	rc := NewRenderContext(t, width)
	rc.Title = "Rewind"
	rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render("Restore the code and/or conversation to the point before…"))
	for i := d.offset; i < min(len(d.prompts), d.offset+rewindVisible); i++ {
		style := t.Dialog.NormalItem
		if i == d.selected {
			style = t.Dialog.SelectedItem
		}
		room := innerWidth - style.GetHorizontalFrameSize()
		rc.AddPart(style.Width(innerWidth).Render(ansi.Truncate(d.prompts[i].line(), room, "…")))
	}
	rc.Help = renderDialogHelp(t, &d.help, d, innerWidth)
	DrawCenter(scr, area, rc.Render())
	return nil
}

// ShortHelp implements [help.KeyMap].
func (d *Rewind) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Up, d.keyMap.Down, d.keyMap.Select, d.keyMap.Close}
}

// FullHelp implements [help.KeyMap].
func (d *Rewind) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}

// rewindOption is what RewindConfirm can restore: the code and the
// conversation, one of them, or nothing.
type rewindOption struct {
	label              string
	conversation, code bool
}

var (
	rewindBoth         = rewindOption{label: "Restore code and conversation", conversation: true, code: true}
	rewindConversation = rewindOption{label: "Restore conversation", conversation: true}
	rewindCode         = rewindOption{label: "Restore code", code: true}
	rewindNevermind    = rewindOption{label: "Never mind"}
)

// RewindConfirm asks what to restore to the point before a prompt, as
// Claude Code's /rewind does: the code and the conversation, either, or
// nothing. The code is offered when restoring it changes files.
type RewindConfirm struct {
	com       *common.Common
	sessionID string
	prompt    RewindPrompt
	// files are the files restoring the code changes, and additions and
	// removals its lines.
	files               []string
	additions, removals int
	options             []rewindOption
	selected            int
	help                help.Model
	keyMap              struct {
		Up, Down, Select, Close key.Binding
	}
}

var _ Dialog = (*RewindConfirm)(nil)

// NewRewindConfirm creates the dialog for prompt of session sessionID,
// where restoring the code changes files, adding and removing the lines
// counted.
func NewRewindConfirm(com *common.Common, sessionID string, prompt RewindPrompt, files []string, additions, removals int) *RewindConfirm {
	d := &RewindConfirm{com: com, sessionID: sessionID, prompt: prompt, files: files, additions: additions, removals: removals}
	d.options = []rewindOption{rewindConversation, rewindNevermind}
	if len(files) > 0 {
		d.options = []rewindOption{rewindBoth, rewindConversation, rewindCode, rewindNevermind}
	}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Up = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	d.keyMap.Down = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	d.keyMap.Select = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm"))
	d.keyMap.Close = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))
	return d
}

// ID implements [Dialog].
func (*RewindConfirm) ID() string {
	return RewindConfirmID
}

// HandleMsg implements [Dialog].
func (d *RewindConfirm) HandleMsg(msg tea.Msg) Action {
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
		d.selected = min(len(d.options)-1, d.selected+1)
	case key.Matches(press, d.keyMap.Select):
		o := d.options[d.selected]
		if o == rewindNevermind {
			return ActionClose{}
		}
		return ActionRewindTo{SessionID: d.sessionID, Prompt: d.prompt, Conversation: o.conversation, Code: o.code}
	}
	return nil
}

// said is what the dialog says of option o, as Claude Code says it: what
// becomes of the conversation, and of the code.
func (d *RewindConfirm) said(o rewindOption) []string {
	conversation := "The conversation will be unchanged."
	if o.conversation {
		conversation = "The conversation will be forked."
	}
	code := "The code will be unchanged."
	if o.code {
		code = fmt.Sprintf("The code will be restored +%d -%d in %s.", d.additions, d.removals, d.where())
	}
	return []string{conversation, code}
}

// where names the files restoring the code changes, as Claude Code
// names them.
func (d *RewindConfirm) where() string {
	switch len(d.files) {
	case 1:
		return filepath.Base(d.files[0])
	case 2:
		return filepath.Base(d.files[0]) + " and " + filepath.Base(d.files[1])
	}
	return fmt.Sprintf("%s and %d other files", filepath.Base(d.files[0]), len(d.files)-1)
}

// Draw implements [Dialog].
func (d *RewindConfirm) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := d.com.Styles
	width := max(0, min(reportMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()
	rc := NewRenderContext(t, width)
	rc.Title = "Rewind"
	what := "the conversation "
	if len(d.files) > 0 {
		what = ""
	}
	rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render("Confirm you want to restore " + what + "to the point before you sent this message:"))
	rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render(ansi.Truncate("│ "+d.prompt.line(), innerWidth, "…")))
	for i, o := range d.options {
		style := t.Dialog.NormalItem
		if i == d.selected {
			style = t.Dialog.SelectedItem
		}
		rc.AddPart(style.Width(innerWidth).Render(o.label))
	}
	for _, line := range d.said(d.options[d.selected]) {
		rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render(line))
	}
	if len(d.files) > 0 {
		rc.AddPart(t.Dialog.SecondaryText.Width(innerWidth).Render("⚠ Rewinding does not affect files edited manually, via bash, or downloaded."))
	}
	rc.Help = renderDialogHelp(t, &d.help, d, innerWidth)
	DrawCenter(scr, area, rc.Render())
	return nil
}

// ShortHelp implements [help.KeyMap].
func (d *RewindConfirm) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Up, d.keyMap.Down, d.keyMap.Select, d.keyMap.Close}
}

// FullHelp implements [help.KeyMap].
func (d *RewindConfirm) FullHelp() [][]key.Binding {
	return [][]key.Binding{d.ShortHelp()}
}
