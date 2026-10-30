package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// NoticeItem is a notice from gnosh the agent was sent, such as a
// background job's end: one subdued line in the chat, as Claude Code
// shows its task notifications, so the turn it starts does not seem to
// come from nowhere.
type NoticeItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	id   string
	text string
	sty  *styles.Styles
}

// NewNoticeItem is the notice text of the message id.
func NewNoticeItem(sty *styles.Styles, id, text string) MessageItem {
	v := list.NewVersioned()
	return &NoticeItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		id:                       id,
		text:                     text,
		sty:                      sty,
	}
}

// ID implements MessageItem.
func (n *NoticeItem) ID() string { return n.id }

// Finished implements list.Item: a notice does not change.
func (n *NoticeItem) Finished() bool { return true }

// RawRender implements MessageItem.
func (n *NoticeItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)
	content, height, ok := n.getCachedRender(cappedWidth)
	if !ok {
		content = n.sty.Messages.AssistantInfoProvider.Width(cappedWidth).Render("⎿ " + strings.TrimSpace(n.text))
		height = lipgloss.Height(content)
		n.setCachedRender(content, cappedWidth, height)
	}
	return n.renderHighlighted(content, cappedWidth, height)
}

// Render implements MessageItem.
func (n *NoticeItem) Render(width int) string {
	prefix := n.sty.Messages.AssistantBlurred.Render()
	if n.focused {
		prefix = n.sty.Messages.AssistantFocused.Render()
	}
	lines := strings.Split(n.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
