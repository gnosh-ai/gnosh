package app

import (
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// The events already waiting when a run ends are all handled, in order,
// and the drain does not wait for more, even on a closed channel.
func TestDrainMessageEvents(t *testing.T) {
	t.Parallel()

	events := make(chan pubsub.Event[message.Message], 4)
	for _, id := range []string{"m1", "m2", "m3"} {
		events <- pubsub.Event[message.Message]{Payload: message.Message{ID: id}}
	}
	var got []string
	handle := func(msg message.Message) error {
		got = append(got, msg.ID)
		return nil
	}
	require.NoError(t, drainMessageEvents(events, handle))
	require.Equal(t, []string{"m1", "m2", "m3"}, got)
	require.NoError(t, drainMessageEvents(events, handle), "an empty channel returns at once")

	close(events)
	require.NoError(t, drainMessageEvents(events, handle), "a closed channel returns at once")
	require.Len(t, got, 3)

	failing := make(chan pubsub.Event[message.Message], 2)
	failing <- pubsub.Event[message.Message]{Payload: message.Message{ID: "bad"}}
	failing <- pubsub.Event[message.Message]{Payload: message.Message{ID: "after"}}
	err := drainMessageEvents(failing, func(msg message.Message) error {
		if msg.ID == "bad" {
			return errors.New("content is shorter than read bytes")
		}
		return nil
	})
	require.EqualError(t, err, "content is shorter than read bytes")
}
