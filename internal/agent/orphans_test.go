package agent

import (
	"context"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

type orphanFakeStore struct {
	msgs    []message.Message
	updated []message.Message
}

func (f *orphanFakeStore) List(_ context.Context, _ string) ([]message.Message, error) {
	return f.msgs, nil
}

func (f *orphanFakeStore) Update(_ context.Context, msg message.Message) error {
	f.updated = append(f.updated, msg)
	return nil
}

func assistantMsg(id string, finished bool) message.Message {
	m := message.Message{ID: id, SessionID: "s", Role: message.Assistant}
	if finished {
		m.AddFinish(message.FinishReasonEndTurn, "", "")
	}
	return m
}

func TestFinalizeOrphanedAssistants(t *testing.T) {
	t.Parallel()
	user := message.Message{ID: "u1", SessionID: "s", Role: message.User}
	store := &orphanFakeStore{msgs: []message.Message{
		user,
		assistantMsg("done", true),    // finished normally: untouched
		assistantMsg("ghost", false),  // killed mid-turn: must be finalized
		assistantMsg("ghost2", false), // a second ghost further on
	}}

	n := finalizeOrphanedAssistants(context.Background(), store, "s")

	require.Equal(t, 2, n)
	require.Len(t, store.updated, 2)
	for _, m := range store.updated {
		require.True(t, m.IsFinished(), "updated message %s must now be finished", m.ID)
		require.Equal(t, message.FinishReasonCanceled, m.FinishReason())
		require.Equal(t, orphanNote, m.FinishPart().Message)
	}
	require.Equal(t, "ghost", store.updated[0].ID)
	require.Equal(t, "ghost2", store.updated[1].ID)
}

func TestFinalizeOrphanedAssistantsNothingToDo(t *testing.T) {
	t.Parallel()
	store := &orphanFakeStore{msgs: []message.Message{
		{ID: "u1", SessionID: "s", Role: message.User},
		assistantMsg("done", true),
	}}
	require.Equal(t, 0, finalizeOrphanedAssistants(context.Background(), store, "s"))
	require.Empty(t, store.updated, "a healthy transcript must not be written to")
}
