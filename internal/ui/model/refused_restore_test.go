package model

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/message"
)

// submitted runs cmd's tree and returns the agentRunSubmittedMsg it
// produces, as the Bubble Tea runtime would deliver it.
func submitted(t *testing.T, cmd tea.Cmd) agentRunSubmittedMsg {
	t.Helper()
	var find func(tea.Cmd) (agentRunSubmittedMsg, bool)
	find = func(c tea.Cmd) (agentRunSubmittedMsg, bool) {
		if c == nil {
			return agentRunSubmittedMsg{}, false
		}
		switch msg := c().(type) {
		case agentRunSubmittedMsg:
			return msg, true
		case tea.BatchMsg:
			for _, sub := range msg {
				if found, ok := find(sub); ok {
					return found, true
				}
			}
		}
		return agentRunSubmittedMsg{}, false
	}
	msg, ok := find(cmd)
	require.True(t, ok, "the send reports how it went")
	return msg
}

// A prompt sent before the agent is ready is refused, and it goes back
// into the editor instead of being lost with the editor Enter cleared.
func TestPromptRefusedBeforeTheAgentIsReadyReturnsToTheEditor(t *testing.T) {
	m := newPendingUI(t)
	m.com.Workspace.(*countingWorkspace).ready = false
	paste := message.Attachment{FileName: "paste_1.txt", FilePath: "paste_1.txt", MimeType: "text/plain", Content: []byte("log")}

	msg := submitted(t, m.sendMessage("fix the build", paste))
	require.Error(t, msg.err)
	_, _ = m.Update(msg)
	require.Equal(t, "fix the build", m.textarea.Value())
	require.Equal(t, []message.Attachment{paste}, m.attachments.List())
}

// A prompt a UserPromptSubmit hook blocked was never stored: it comes back
// too, so the user can change what the hook objected to and send it again.
func TestBlockedPromptReturnsToTheEditor(t *testing.T) {
	m := newPendingUI(t)
	m.com.Workspace.(*countingWorkspace).runErr = errors.New("prompt blocked by a UserPromptSubmit hook: no keys in prompts")

	msg := submitted(t, m.sendMessage("my key is sk-1"))
	_, _ = m.Update(msg)
	require.Equal(t, "my key is sk-1", m.textarea.Value())
}

// A prompt the session stored, whose turn failed later, is in the chat; it
// is not put back into the editor as well.
func TestStoredPromptThatFailedStaysOut(t *testing.T) {
	m := newPendingUI(t)
	m.com.Workspace.(*countingWorkspace).runErr = errors.New("model server returned 500")

	msg := submitted(t, m.sendMessage("fix the build"))
	m.dropPendingEcho() // the stored user message arrived during the turn
	_, _ = m.Update(msg)
	require.Empty(t, m.textarea.Value())
}

// What was typed since is never overwritten, and the agent's own hidden
// continuations never come back.
func TestRefusedPromptLeavesNewTypingAndHiddenSendsAlone(t *testing.T) {
	m := newPendingUI(t)
	m.com.Workspace.(*countingWorkspace).ready = false

	msg := submitted(t, m.sendMessage("first"))
	m.textarea.InsertString("typed since")
	_, _ = m.Update(msg)
	require.Equal(t, "typed since", m.textarea.Value())

	m.textarea.Reset()
	msg = submitted(t, m.sendMessageInternal("Continue.", true))
	_, _ = m.Update(msg)
	require.Empty(t, m.textarea.Value())
}
