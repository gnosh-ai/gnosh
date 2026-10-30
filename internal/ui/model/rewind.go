package model

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// rewindPromptsMsg is the prompts of session sessionID /rewind can go
// back to.
type rewindPromptsMsg struct {
	sessionID string
	prompts   []dialog.RewindPrompt
}

// rewindPreviewMsg is what restoring the code to the point before prompt
// changes.
type rewindPreviewMsg struct {
	sessionID string
	prompt    dialog.RewindPrompt
	result    app.RewindResult
}

// rewoundMsg is a rewind done: of the conversation, the code or both, to
// the point before prompt.
type rewoundMsg struct {
	sessionID          string
	prompt             dialog.RewindPrompt
	conversation, code bool
	result             app.RewindResult
}

// said is what gnosh says of a rewind done: what it restored, where the
// whole conversation is kept, and the files it left as they are.
func (msg rewoundMsg) said() string {
	var said []string
	if msg.conversation {
		said = append(said, fmt.Sprintf("Rewound the conversation to before your message; the whole of it is kept as %q (/resume %s).", msg.result.Kept.Title, msg.result.Kept.ID))
	}
	if msg.code {
		if n := len(msg.result.Files); n > 0 {
			said = append(said, fmt.Sprintf("Restored the code in %s (+%d -%d).", files(n), msg.result.Additions, msg.result.Removals))
		} else {
			said = append(said, "The code had not changed.")
		}
	}
	if n := len(msg.result.Skipped); n > 0 {
		skipped := msg.result.Skipped[0]
		if n > 1 {
			skipped += fmt.Sprintf(", and %d more", n-1)
		}
		said = append(said, fmt.Sprintf("Skipped %s, left as they are: %s.", files(n), skipped))
	}
	return strings.Join(said, " ")
}

// files is "1 file" or "n files".
func files(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// rewindPrompts are the prompts of msgs /rewind can go back to, oldest
// first: those the model still reads, after the summary of a compacted
// session, and not hidden ones.
func rewindPrompts(msgs []message.Message, summaryID string) []dialog.RewindPrompt {
	start := 0
	if i := slices.IndexFunc(msgs, func(msg message.Message) bool { return msg.ID == summaryID }); i >= 0 {
		start = i + 1
	}
	var prompts []dialog.RewindPrompt
	for _, msg := range msgs[start:] {
		text := msg.Content()
		if msg.Role != message.User || text.Hidden {
			continue
		}
		prompt := dialog.RewindPrompt{MessageID: msg.ID, Text: text.Text}
		for _, b := range msg.BinaryContent() {
			prompt.Attachments = append(prompt.Attachments, message.Attachment{FilePath: b.Path, FileName: filepath.Base(b.Path), MimeType: b.MIMEType, Content: b.Data})
		}
		if prompt.Text != "" || len(prompt.Attachments) > 0 {
			prompts = append(prompts, prompt)
		}
	}
	return prompts
}

// openRewind lists the session's prompts to go back to, as Claude Code's
// /rewind does; with none, or while the agent works, it says why not.
func (m *UI) openRewind() tea.Cmd {
	if !m.hasSession() {
		return util.ReportWarn("Nothing to rewind to yet.")
	}
	if m.isAgentBusy() {
		return util.ReportWarn("Agent is busy, please wait before rewinding...")
	}
	sess := *m.session
	return func() tea.Msg {
		msgs, err := m.com.Workspace.ListMessages(context.Background(), sess.ID)
		if err != nil {
			return util.ReportError(fmt.Errorf("failed to list the conversation: %w", err))()
		}
		prompts := rewindPrompts(msgs, sess.SummaryMessageID)
		if len(prompts) == 0 {
			return util.NewWarnMsg("Nothing to rewind to yet.")
		}
		return rewindPromptsMsg{sessionID: sess.ID, prompts: prompts}
	}
}

// rewindEscWindow is how soon after an Esc a second one is Claude Code's
// double press.
const rewindEscWindow = 800 * time.Millisecond

// escapeTwice takes an Esc in the editor, as Claude Code does: a second
// Esc within rewindEscWindow of the first opens /rewind on an empty
// prompt in a session, and clears a prompt with text in it, keeping the
// text for Up; on the first there, it says "Esc again to clear". An
// alt+esc is two Escs that reached gnosh together. Esc while browsing
// history goes back to the draft, as before. It reports whether it took
// the key.
func (m *UI) escapeTwice(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	now := time.Now()
	twice := func(at *time.Time) bool {
		if !msg.Mod.Contains(tea.ModAlt) && (at.IsZero() || now.Sub(*at) > rewindEscWindow) {
			*at = now
			return false
		}
		*at = time.Time{}
		return true
	}
	switch {
	case m.promptHistory.index >= 0:
		m.rewindEscAt, m.clearEscAt = time.Time{}, time.Time{}
		return nil, false
	case m.textarea.Value() != "":
		m.rewindEscAt = time.Time{}
		if !twice(&m.clearEscAt) {
			return util.ReportInfo("Esc again to clear"), false
		}
		return m.clearPrompt(), true
	case !m.hasSession():
		m.rewindEscAt, m.clearEscAt = time.Time{}, time.Time{}
		return nil, false
	}
	m.clearEscAt = time.Time{}
	if !twice(&m.rewindEscAt) {
		return nil, false
	}
	return m.openRewind(), true
}

// previewRewind finds what restoring the code to the point before prompt
// changes, for the dialog that asks what to restore.
func (m *UI) previewRewind(sessionID string, prompt dialog.RewindPrompt) tea.Cmd {
	return func() tea.Msg {
		result, err := m.com.Workspace.Rewind(context.Background(), sessionID, prompt.MessageID, app.RewindOptions{DryRun: true})
		if err != nil {
			return util.ReportError(fmt.Errorf("failed to rewind: %w", err))()
		}
		return rewindPreviewMsg{sessionID: sessionID, prompt: prompt, result: result}
	}
}

// rewindTo restores what action says to the point before its prompt.
func (m *UI) rewindTo(action dialog.ActionRewindTo) tea.Cmd {
	if m.isAgentBusy() {
		return util.ReportWarn("Agent is busy, please wait before rewinding...")
	}
	opts := app.RewindOptions{Conversation: action.Conversation, Code: action.Code}
	return func() tea.Msg {
		result, err := m.com.Workspace.Rewind(context.Background(), action.SessionID, action.Prompt.MessageID, opts)
		if err != nil {
			return util.ReportError(fmt.Errorf("failed to rewind: %w", err))()
		}
		return rewoundMsg{sessionID: action.SessionID, prompt: action.Prompt, conversation: action.Conversation, code: action.Code, result: result}
	}
}

// rewound goes on after a rewind: with the conversation rewound, the
// session is loaded again and the prompt, with what was attached to it,
// goes back in the editor, as Claude Code puts it back.
func (m *UI) rewound(msg rewoundMsg) tea.Cmd {
	var cmds []tea.Cmd
	if msg.conversation && m.hasSession() && m.session.ID == msg.sessionID {
		cmds = append(cmds, m.loadSession(msg.sessionID))
		m.textarea.SetValue(msg.prompt.Text)
		m.attachments.Reset()
		for _, attachment := range msg.prompt.Attachments {
			m.attachments.Update(attachment)
		}
	}
	if len(msg.result.Skipped) > 0 {
		return tea.Batch(append(cmds, util.ReportWarn(msg.said()))...)
	}
	return tea.Batch(append(cmds, util.ReportInfo(msg.said()))...)
}
