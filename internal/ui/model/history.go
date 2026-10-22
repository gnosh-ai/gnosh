package model

import (
	"context"
	"log/slog"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/message"
)

// promptHistoryLoadedMsg is sent when prompt history is loaded.
type promptHistoryLoadedMsg struct {
	messages []string
}

// loadPromptHistory loads user messages for history navigation. Both queries
// stop at the 200 most recent entries; recall only steps back one at a time.
func (m *UI) loadPromptHistory() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		var messages []message.Message
		var err error

		if m.session != nil {
			messages, err = m.com.Workspace.ListUserMessages(ctx, m.session.ID)
		} else {
			messages, err = m.com.Workspace.ListAllUserMessages(ctx)
		}
		if err != nil {
			slog.Error("Failed to load prompt history", "error", err)
			return promptHistoryLoadedMsg{messages: nil}
		}

		return promptHistoryLoadedMsg{messages: historyTexts(messages)}
	}
}

// historyTexts turns stored user messages into the prompts Up recalls. Hidden
// messages are left out: they are continuations the agent or /loop sent (a
// Stop hook's objection, /loop's "Continue working" and its check-in), not
// what the user typed, and the chat does not show them either.
func historyTexts(messages []message.Message) []string {
	texts := make([]string, 0, len(messages))
	for _, msg := range messages {
		if content := msg.Content(); content.Text != "" && !content.Hidden {
			texts = append(texts, content.Text)
		}
		for _, sc := range msg.ShellCommands() {
			texts = append(texts, "!"+sc.Command)
		}
	}
	return texts
}

// handleHistoryUp handles up arrow for history navigation.
func (m *UI) handleHistoryUp(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Navigate to older history entry from cursor position (0,0).
	if m.textarea.Length() == 0 || m.isAtEditorStart() {
		// A prompt queued while a turn is running is editable in place:
		// pulling it (and any earlier queued prompts) out of the queue and
		// into the editor means Enter sends one edited prompt instead of
		// queueing a second one behind the first.
		if m.textarea.Length() == 0 && m.promptQueue > 0 {
			return m.dispatchPromptQueuePop()
		}
		if m.historyPrev() {
			// we send this so that the textarea moves the view to the correct position
			// without this the cursor will show up in the wrong place.
			return m.updateTextareaWithPrevHeight(nil, prevHeight)
		}
	}

	// First move cursor to start before entering history.
	if m.textarea.Line() == 0 {
		m.textarea.CursorStart()
		return nil
	}

	// Let textarea handle normal cursor movement.
	return m.updateTextarea(msg)
}

// handleHistoryDown handles down arrow for history navigation.
func (m *UI) handleHistoryDown(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Navigate to newer history entry from end of text.
	if m.isAtEditorEnd() {
		if m.historyNext() {
			// we send this so that the textarea moves the view to the correct position
			// without this the cursor will show up in the wrong place.
			return m.updateTextareaWithPrevHeight(nil, prevHeight)
		}
	}

	// First move cursor to end before navigating history.
	if m.textarea.Line() == max(m.textarea.LineCount()-1, 0) {
		m.textarea.MoveToEnd()
		return m.updateTextarea(nil)
	}

	// Let textarea handle normal cursor movement.
	return m.updateTextarea(msg)
}

// handleHistoryEscape handles escape for exiting history navigation.
func (m *UI) handleHistoryEscape(msg tea.Msg) tea.Cmd {
	prevHeight := m.textarea.Height()
	// Return to current draft when browsing history.
	if m.promptHistory.index >= 0 {
		m.promptHistory.index = -1
		m.textarea.Reset()
		m.textarea.InsertString(m.promptHistory.draft)
		m.syncBangModeFromTextarea()
		return m.updateTextareaWithPrevHeight(nil, prevHeight)
	}

	// Let textarea handle escape normally.
	return m.updateTextarea(msg)
}

// updateHistoryDraft updates history state when text is modified.
func (m *UI) updateHistoryDraft(oldValue string) {
	if m.textarea.Value() != oldValue {
		m.promptHistory.draft = m.textarea.Value()
		m.promptHistory.index = -1
	}
}

// syncBangModeFromTextarea engages or disengages bang mode based on
// whether the current textarea value starts with "!". The "!" prefix
// is stripped when entering bang mode and re-added when leaving it so
// the visible text always reflects the correct state.
func (m *UI) syncBangModeFromTextarea() {
	val := m.textarea.Value()
	hasBang := strings.HasPrefix(val, "!")
	if hasBang {
		if !m.bangMode {
			m.bangMode = true
			m.bangWasEmpty = false
		}
		m.textarea.SetValue(strings.TrimPrefix(val, "!"))
		m.textarea.MoveToBegin()
	} else if m.bangMode {
		m.bangMode = false
		m.bangWasEmpty = false
	}
	m.setEditorPrompt(m.yoloModeCached())
}

// historyEntries returns what Up and Down step through: the user messages
// loaded from the database, newest first. Prompts still queued are left out.
// Up in an empty editor takes the ones the user typed out of the queue to
// edit (dispatchPromptQueuePop), and a copy of a queued prompt offered here,
// edited and sent, would run it twice: once from the queue, once as typed.
func (m *UI) historyEntries() []string {
	return m.promptHistory.messages
}

// historyPrev changes the text area content to the previous message in the history
// it returns false if it could not find the previous message.
func (m *UI) historyPrev() bool {
	entries := m.historyEntries()
	if len(entries) == 0 {
		return false
	}
	if m.promptHistory.index == -1 {
		m.promptHistory.draft = m.textarea.Value()
	}
	nextIndex := m.promptHistory.index + 1
	if nextIndex >= len(entries) {
		return false
	}
	m.promptHistory.index = nextIndex
	m.textarea.Reset()
	m.textarea.InsertString(entries[nextIndex])
	m.textarea.MoveToBegin()
	m.syncBangModeFromTextarea()
	return true
}

// historyNext changes the text area content to the next message in the history
// it returns false if it could not find the next message.
func (m *UI) historyNext() bool {
	if m.promptHistory.index < 0 {
		return false
	}
	nextIndex := m.promptHistory.index - 1
	if nextIndex < 0 {
		m.promptHistory.index = -1
		m.textarea.Reset()
		m.textarea.InsertString(m.promptHistory.draft)
		m.syncBangModeFromTextarea()
		return true
	}
	entries := m.historyEntries()
	if nextIndex >= len(entries) {
		return false
	}
	m.promptHistory.index = nextIndex
	m.textarea.Reset()
	m.textarea.InsertString(entries[nextIndex])
	m.syncBangModeFromTextarea()
	return true
}

// historyReset resets the history, but does not clear the message
// it just sets the current draft to empty and the position in the history.
func (m *UI) historyReset() {
	m.promptHistory.index = -1
	m.promptHistory.draft = ""
}

// isAtEditorStart returns true if we are at the 0 line and 0 col in the textarea.
func (m *UI) isAtEditorStart() bool {
	return m.textarea.Line() == 0 && m.textarea.LineInfo().ColumnOffset == 0
}

// isAtEditorEnd returns true if we are in the last line and the last column in the textarea.
func (m *UI) isAtEditorEnd() bool {
	lineCount := m.textarea.LineCount()
	if lineCount == 0 {
		return true
	}
	if m.textarea.Line() != lineCount-1 {
		return false
	}
	info := m.textarea.LineInfo()
	return info.CharOffset >= info.CharWidth-1 || info.CharWidth == 0
}
