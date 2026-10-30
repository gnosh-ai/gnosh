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
func TestHistoryItems_LeavesHiddenPromptsOut(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Continue working on the task until it is complete.", Hidden: true}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "fix the parser"}}},
	}
	texts, _ := historyItems(msgs)
	require.Equal(t, []string{"fix the parser"}, texts)
}

// Prompts Esc Esc cleared are offered by Up among the prompts sent, by
// when each was cleared or sent, newest first; with no times known, the
// cleared ones come first.
func TestHistoryEntriesPutsClearedPromptsByTime(t *testing.T) {
	t.Parallel()

	m := &UI{}
	m.promptHistory.messages = []string{"sent later", "sent before"}
	m.promptHistory.times = []int64{1000, 900}
	m.promptHistory.cleared = []clearedPrompt{{"cleared between", 950}, {"cleared first", 800}}
	require.Equal(t, []string{"sent later", "cleared between", "sent before", "cleared first"}, m.historyEntries())

	m.promptHistory.times = nil
	require.Equal(t, []string{"cleared between", "cleared first", "sent later", "sent before"}, m.historyEntries())

	texts, times := historyItems([]message.Message{
		{Role: message.User, CreatedAt: 1000, Parts: []message.ContentPart{message.TextContent{Text: "sent later"}}},
		{Role: message.User, CreatedAt: 950, Parts: []message.ContentPart{message.TextContent{Text: "hidden", Hidden: true}}},
		{Role: message.User, CreatedAt: 900, Parts: []message.ContentPart{message.ShellCommand{Command: "ls"}}},
	})
	require.Equal(t, []string{"sent later", "!ls"}, texts)
	require.Equal(t, []int64{1000, 900}, times)
}
