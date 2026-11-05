package model

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/x/ansi"
)

// ShowFrame marks the items ids with a gutter, in place of those marked
// before: the rows the explorer shows, a frame's or one. nil clears the
// marks.
func (m *Chat) ShowFrame(ids []string) {
	old := m.framed
	m.framed = nil
	for _, id := range ids {
		if m.framed == nil {
			m.framed = map[string]bool{}
		}
		m.framed[id] = true
	}
	for _, marks := range []map[string]bool{old, m.framed} {
		for id := range marks {
			if item := m.MessageItem(id); item != nil {
				m.list.Invalidate(item)
			}
		}
	}
}

// ScrollToItem scrolls the chat so that it starts at the item id, where
// the chat has it.
func (m *Chat) ScrollToItem(id string) {
	if idx, ok := m.idInxMap[id]; ok {
		m.ScrollToIndex(idx)
	}
}

// frameGutter is the render callback that marks the items ShowFrame
// marked.
func (m *Chat) frameGutter(idx, selectedIdx int, item list.Item) list.Item {
	if len(m.framed) == 0 || !m.framed[m.idAt(idx)] {
		return item
	}
	// A focused item draws its focus bar in the gutter's column.
	if _, ok := m.list.ItemAt(idx).(list.Focusable); ok && m.list.Focused() && idx == selectedIdx {
		return item
	}
	gutter := lipgloss.NewStyle().Foreground(m.com.Styles.Header.LogoGradToColor).Render("▌")
	return framedItem{Item: item, gutter: gutter}
}

// framedItem is an item whose first column is a gutter.
type framedItem struct {
	list.Item
	gutter string
}

func (f framedItem) Render(width int) string {
	lines := strings.Split(f.Item.Render(width), "\n")
	for i, line := range lines {
		lines[i] = f.gutter + ansi.TruncateLeft(line, 1, "")
	}
	return strings.Join(lines, "\n")
}
