package dialog

import (
	"image"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Secret asks for one line the user types blind, such as a key's password:
// the free-text question with question.Question.Secret set. One masked
// line, no newline, nothing trimmed. The answer goes to the tool that
// asked (see tools.NewGnokeyTool), which keeps it from the model.
type Secret struct {
	Styles  *styles.Styles
	Request question.Question
	focused bool

	input    textinput.Model
	keyEnter key.Binding
	keyClose key.Binding

	lastResponse question.Answer
	lastWidth    int
}

// NewSecret creates a masked single-line question component.
func NewSecret(sty *styles.Styles, req question.Question) *Secret {
	ti := textinput.New()
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	ti.Prompt = ""
	ti.Placeholder = "password"
	ti.CharLimit = 1000
	ti.SetVirtualCursor(false)
	ti.Blur()
	return &Secret{
		Styles:   sty,
		Request:  req,
		input:    ti,
		keyEnter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit")),
		keyClose: CloseKey,
	}
}

// HandleKey processes a key press. Returns true when the user has
// submitted or dismissed the question. An empty line is not an answer, so
// a stray enter cannot send an empty password.
func (d *Secret) HandleKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, d.keyClose):
		d.lastResponse = question.Answer{QuestionID: d.Request.ID}
		return true, nil
	case key.Matches(msg, d.keyEnter):
		if val := d.input.Value(); val != "" {
			d.lastResponse = question.Answer{QuestionID: d.Request.ID, FillInText: val}
			return true, nil
		}
		return false, nil
	default:
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return false, cmd
	}
}

// Response returns the submitted answer. Unlike the free-text question it
// does not return what is still being typed.
func (d *Secret) Response() question.Answer { return d.lastResponse }

// GetRequest returns the underlying question request.
func (d *Secret) GetRequest() question.Question { return d.Request }

// ShortHelp returns key bindings for the status bar.
func (d *Secret) ShortHelp() []key.Binding {
	return []key.Binding{d.keyEnter, d.keyClose}
}

// lines renders the question at width: header, description, the masked
// input line, and a blank line of bottom padding. inputRow is the index
// of the input line.
func (d *Secret) lines(width int) (lines []string, inputRow int) {
	iconPrompt := questionIconPrompt(d.Styles, d.focused)
	header := iconPrompt + d.Styles.Editor.QuestionUnselected.Render(
		ansi.Wrap(d.Request.Text, width-lipgloss.Width(iconPrompt), ""),
	)
	lines = append(lines, strings.Split(header, "\n")...)
	lines = append(lines, "")
	if d.Request.Description != "" {
		r := common.MarkdownRenderer(d.Styles, width)
		mu := common.LockMarkdownRenderer(r)
		mu.Lock()
		desc, err := r.Render(d.Request.Description)
		mu.Unlock()
		if err != nil {
			desc = d.Request.Description
		}
		lines = append(lines, strings.Split(strings.TrimSuffix(desc, "\n"), "\n")...)
		lines = append(lines, "")
	}
	bar := "  "
	if d.focused {
		bar = d.Styles.Editor.QuestionCursorBar.Render("┃ ")
	}
	d.input.SetWidth(max(1, width-lipgloss.Width(bar)-1))
	inputRow = len(lines)
	lines = append(lines, bar+d.input.View())
	lines = append(lines, "")
	return lines, inputRow
}

// Height returns the visual height at the given width.
func (d *Secret) Height(width int) int {
	w := width
	if w <= 0 {
		w = d.lastWidth
	}
	if w <= 0 {
		w = choiceListMaxWidth
	}
	lines, _ := d.lines(w)
	return len(lines)
}

// Draw renders the question directly to screen and returns the cursor
// position on the input line, relative to the area, or nil.
func (d *Secret) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	d.lastWidth = area.Dx()
	width := area.Dx()
	lines, inputRow := d.lines(width)
	for i, ln := range lines {
		if i >= area.Dy() {
			break
		}
		y := area.Min.Y + i
		drawStyledText(scr, image.Rect(area.Min.X, y, area.Min.X+width, y+1), ln)
	}
	if !d.focused || inputRow >= area.Dy() {
		return nil
	}
	c := d.input.Cursor()
	if c == nil {
		return nil
	}
	cur := *c
	bar := "  "
	if d.focused {
		bar = d.Styles.Editor.QuestionCursorBar.Render("┃ ")
	}
	cur.X += lipgloss.Width(bar)
	cur.Y = inputRow
	return &cur
}

// HeightChanged reports whether the height changed; a single line never
// does.
func (d *Secret) HeightChanged() bool { return false }

// SetFocused updates focus state.
func (d *Secret) SetFocused(focused bool) {
	d.focused = focused
	if focused {
		d.input.Focus()
	} else {
		d.input.Blur()
	}
}

// SetHover is a no-op for secret questions.
func (d *Secret) SetHover(x, y int) {}

// HandleMouseClick is a no-op for secret questions.
func (d *Secret) HandleMouseClick(x, y int) (bool, bool) { return false, false }

// HandleWheel is a no-op for secret questions.
func (d *Secret) HandleWheel(deltaX, deltaY float64) {}

// HandlePaste forwards a paste to the input, since passwords are pasted.
func (d *Secret) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return cmd
}
