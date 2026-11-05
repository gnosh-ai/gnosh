package model

import (
	"image"
	"strings"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/list"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// poppedItemIDs is the chat items that show row: its own (a reply or a
// prompt), a reply's model line, its shell commands, and the tool items
// of the calls it made or answered.
func poppedItemIDs(row message.Message) []string {
	ids := []string{row.ID}
	if row.Role == message.Assistant {
		ids = append(ids, chat.AssistantInfoID(row.ID))
	}
	for i, part := range row.Parts {
		if _, ok := part.(message.ShellCommand); ok {
			ids = append(ids, chat.ShellItemID(row.ID, i))
		}
	}
	for _, call := range row.ToolCalls() {
		ids = append(ids, call.ID)
	}
	for _, result := range row.ToolResults() {
		ids = append(ids, result.ToolCallID)
	}
	return ids
}

// MarkPopped takes in row's mark (message.Popped): the items that show a
// row out of the model's context are drawn faint, under a line naming
// the pop or slide that took them out.
func (m *Chat) MarkPopped(row message.Message) {
	for _, id := range poppedItemIDs(row) {
		if m.popped[id] == row.Popped {
			continue
		}
		if row.Popped == "" {
			delete(m.popped, id)
		} else {
			if m.popped == nil {
				m.popped = map[string]string{}
			}
			m.popped[id] = row.Popped
		}
		if item := m.MessageItem(id); item != nil {
			m.list.Invalidate(item)
		}
	}
}

// markAt is the mark of the item at idx, "" for one the model still has.
func (m *Chat) markAt(idx int) string {
	return m.popped[m.idAt(idx)]
}

// idAt is the id of the item at idx, "" for none.
func (m *Chat) idAt(idx int) string {
	if idx < 0 || idx >= m.list.Len() {
		return ""
	}
	item, ok := m.list.ItemAt(idx).(chat.Identifiable)
	if !ok {
		return ""
	}
	return item.ID()
}

// dimPopped is the render callback that draws an item out of the model's
// context faint.
func (m *Chat) dimPopped(idx, _ int, item list.Item) list.Item {
	if m.markAt(idx) == "" {
		return item
	}
	return fadedItem{item}
}

// poppedLabel is the gap row above the first item of a run out of the
// model's context: which pop took the run out, or that a slide did.
func (m *Chat) poppedLabel(after int) string {
	mark := m.markAt(after + 1)
	if mark == "" || m.markAt(after) == mark {
		return ""
	}
	name := mark
	if strings.Count(mark, "/") > 1 {
		name = "…" + mark[strings.LastIndex(mark, "/"):]
	}
	text := "popped by `" + name + "` · out of the model's context"
	if _, slid := agent.SlidFrame(mark); slid {
		text = "slid out of the model's context"
	}
	line := strings.Repeat(" ", chat.MessageLeftPaddingTotal) + "┄ " + text
	return m.com.Styles.Files.TruncationHint.Render(ansi.Truncate(line, max(m.list.Width(), 1), "…"))
}

// fadedItem is an item drawn faint, its colors kept.
type fadedItem struct{ list.Item }

func (f fadedItem) Render(width int) string {
	out := f.Item.Render(width)
	height := strings.Count(out, "\n") + 1
	return list.Highlight(out, image.Rect(0, 0, width, height), 0, 0, -1, -1, faint)
}

func faint(_, _ int, c *uv.Cell) *uv.Cell {
	if c != nil {
		c.Style.Attrs |= uv.AttrFaint
	}
	return c
}
