package message

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// A message a frame's pop removed from the model's context is left out of
// what the model is sent, before and after a summary, and kept in what the
// human reads; the mark is published, and put back with "".
func TestSetPopped(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)
	ids := make([]string, 0, 5)
	for range 5 {
		msg, err := svc.Create(t.Context(), sessionID, CreateMessageParams{
			Role:  User,
			Parts: []ContentPart{TextContent{Text: "hi"}},
		})
		require.NoError(t, err)
		ids = append(ids, msg.ID)
	}
	subCtx, unsubscribe := context.WithCancel(t.Context())
	events := svc.Subscribe(subCtx)

	require.NoError(t, svc.SetPopped(t.Context(), ids[1:3], "fix-flaky/find-cause"))

	got, err := svc.Get(t.Context(), ids[1])
	require.NoError(t, err)
	require.Equal(t, "fix-flaky/find-cause", got.Popped)
	for range 2 {
		ev := <-events
		require.Equal(t, "fix-flaky/find-cause", ev.Payload.Popped, "the mark is published with the row")
	}
	unsubscribe()

	all, err := svc.List(t.Context(), sessionID)
	require.NoError(t, err)
	require.Len(t, all, 5, "the human keeps every message")

	model, err := svc.ListFromSummary(t.Context(), sessionID, "")
	require.NoError(t, err)
	require.Equal(t, []string{ids[0], ids[3], ids[4]}, messageIDs(model), "the model's list skips the popped rows")

	model, err = svc.ListFromSummary(t.Context(), sessionID, ids[0])
	require.NoError(t, err)
	require.NotContains(t, messageIDs(model), ids[1], "after a summary too")
	require.NotContains(t, messageIDs(model), ids[2])
	require.Contains(t, messageIDs(model), ids[4])

	model, err = svc.ListFromSummary(t.Context(), sessionID, "does-not-exist")
	require.NoError(t, err)
	require.Equal(t, []string{ids[0], ids[3], ids[4]}, messageIDs(model), "the fallback for a dangling summary is filtered too")

	// A copy, as /branch makes one, carries the mark.
	other, err := svc.Copy(t.Context(), sessionID, got)
	require.NoError(t, err)
	require.Equal(t, "fix-flaky/find-cause", other.Popped)

	require.NoError(t, svc.SetPopped(t.Context(), ids[1:3], ""))
	model, err = svc.ListFromSummary(t.Context(), sessionID, "")
	require.NoError(t, err)
	require.Len(t, model, 5, "put back; the copy keeps its own mark")
}

func messageIDs(msgs []Message) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}
