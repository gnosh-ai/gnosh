package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHistoryEntries_IncludesQueuedPrompts pins that a prompt queued while a
// turn is running can be recalled from the editor. A queued prompt lives only
// in the agent's in-memory queue until its turn runs, so it appeared in the
// queue pill but was missing from the database-backed prompt history.
func TestHistoryEntries_IncludesQueuedPrompts(t *testing.T) {
	t.Parallel()

	m := &UI{}
	// Database history is newest first (ORDER BY created_at DESC).
	m.promptHistory.messages = []string{"second sent", "first sent"}
	// The queue is held in submission order, oldest first.
	m.promptQueueItems = []string{"queued one", "queued two"}

	require.Equal(t,
		[]string{"queued two", "queued one", "second sent", "first sent"},
		m.historyEntries(),
		"the most recently queued prompt must be the first thing Up recalls")
}

func TestHistoryEntries_NoQueueIsUnchanged(t *testing.T) {
	t.Parallel()

	m := &UI{}
	m.promptHistory.messages = []string{"b", "a"}
	require.Equal(t, []string{"b", "a"}, m.historyEntries())
}
