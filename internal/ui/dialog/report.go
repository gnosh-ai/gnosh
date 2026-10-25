package dialog

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
)

// ReportID is the identifier for the report dialog.
const ReportID = "report"

// reportMaxWidth is the widest the dialog draws: a report's tables read
// well at it, and wider only spreads them out.
const reportMaxWidth = 90

// Report shows a report in Markdown, as Claude Code shows what /context
// and /hooks find: rendered as the chat renders Markdown, scrolled with
// the arrow and page keys or the mouse wheel.
type Report struct {
	com    *common.Common
	title  string
	report string
	help   help.Model
	// offset is the first line shown, and lines how many the last Draw
	// could show, for paging.
	offset, lines int
	keyMap        struct {
		Up, Down, PageUp, PageDown, Close key.Binding
	}
}

var _ Dialog = (*Report)(nil)

// NewReport creates a dialog titled title showing report, Markdown.
func NewReport(com *common.Common, title, report string) *Report {
	d := &Report{com: com, title: title, report: report}
	d.help = help.New()
	d.help.Styles = com.Styles.DialogHelpStyles()
	d.keyMap.Up = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	d.keyMap.Down = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	d.keyMap.PageUp = key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up"))
	d.keyMap.PageDown = key.NewBinding(key.WithKeys("pgdown", " "), key.WithHelp("pgdn", "page down"))
	d.keyMap.Close = CloseKey
	return d
}

// ID implements [Dialog].
func (*Report) ID() string {
	return ReportID
}

// HandleMsg implements [Dialog].
func (d *Report) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case common.CoalescedWheelMsg:
		d.scroll(int(msg.DeltaY))
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, d.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, d.keyMap.Up):
			d.scroll(-1)
		case key.Matches(msg, d.keyMap.Down):
			d.scroll(1)
		case key.Matches(msg, d.keyMap.PageUp):
			d.scroll(-max(1, d.lines-1))
		case key.Matches(msg, d.keyMap.PageDown):
			d.scroll(max(1, d.lines-1))
		}
	}
	return nil
}

// scroll moves the first line shown by n; Draw keeps it in range.
func (d *Report) scroll(n int) {
	d.offset = max(0, d.offset+n)
}

// Draw implements [Dialog].
func (d *Report) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	sty := d.com.Styles
	width := max(20, min(area.Dx(), reportMaxWidth))
	rc := NewRenderContext(sty, width)
	rc.Title = d.title
	innerWidth := width - sty.Dialog.View.GetHorizontalFrameSize() - 1 // the scrollbar
	rc.Help = renderDialogHelp(sty, &d.help, d, innerWidth)

	lines := strings.Split(strings.TrimRight(d.rendered(innerWidth), "\n"), "\n")
	// Room for the report: the area less the title, the help line and
	// the dialog's frame.
	d.lines = max(1, area.Dy()-sty.Dialog.View.GetVerticalFrameSize()-sty.Dialog.Title.GetVerticalFrameSize()-3)
	d.offset = min(d.offset, max(0, len(lines)-d.lines))
	shown := lines[d.offset:min(len(lines), d.offset+d.lines)]
	view := joinScrollbar(sty, strings.Join(shown, "\n"), len(shown), len(lines), d.lines, d.offset)
	rc.AddPart(view)
	DrawCenter(scr, area, rc.Render())
	return nil
}

// rendered is the report as the chat renders Markdown, at width; the
// Markdown as written when the renderer fails. A line break made with two
// trailing spaces, as /context writes its header after Claude Code's, is
// lost by the renderer and ran its lines together, so it is a paragraph
// break here.
func (d *Report) rendered(width int) string {
	r := common.MarkdownRenderer(d.com.Styles, width)
	mu := common.LockMarkdownRenderer(r)
	mu.Lock()
	defer mu.Unlock()
	out, err := r.Render(strings.ReplaceAll(d.report, "  \n", "\n\n"))
	if err != nil {
		return d.report
	}
	return strings.Trim(out, "\n")
}

// ShortHelp implements [help.KeyMap].
func (d *Report) ShortHelp() []key.Binding {
	return []key.Binding{d.keyMap.Up, d.keyMap.Down, d.keyMap.PageDown, d.keyMap.Close}
}

// FullHelp implements [help.KeyMap].
func (d *Report) FullHelp() [][]key.Binding {
	return [][]key.Binding{{d.keyMap.Up, d.keyMap.Down, d.keyMap.PageUp, d.keyMap.PageDown, d.keyMap.Close}}
}
