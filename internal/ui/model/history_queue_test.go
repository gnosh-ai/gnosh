package model

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/message"
)

// TestHistoryEntries_LeavesQueuedPromptsOut pins that history recall offers
// only prompts already sent. Up in an empty editor takes the user's queued
// prompts out of the queue to edit; a copy of one offered by history, edited
// and sent, would run that prompt twice.
func TestHistoryEntries_LeavesQueuedPromptsOut(t *testing.T) {
	t.Parallel()

	m := &UI{}
	// Database history is newest first (ORDER BY created_at DESC).
	m.promptHistory.messages = []string{"second sent", "first sent"}
	m.promptQueueItems = []string{"queued one", "queued two"}

	require.Equal(t, []string{"second sent", "first sent"}, m.historyEntries())
}

func TestHistoryEntries_NoQueueIsUnchanged(t *testing.T) {
	t.Parallel()

	m := &UI{}
	m.promptHistory.messages = []string{"b", "a"}
	require.Equal(t, []string{"b", "a"}, m.historyEntries())
}

// TestHistoryTexts_LeavesHiddenPromptsOut pins that Up recalls only what the
// user typed. A hidden message is a continuation the agent or /loop sent;
// recalled and sent, it would show up as if the user had typed it.
func TestHistoryTexts_LeavesHiddenPromptsOut(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Continue working on the task until it is complete.", Hidden: true}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "fix the parser"}}},
	}
	require.Equal(t, []string{"fix the parser"}, historyTexts(msgs))
}
