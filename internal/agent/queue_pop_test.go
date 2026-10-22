package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The queue also holds the agent's own hidden continuations (a Stop hook's
// or the loop detector's follow-up), which the turn-end handoff picks up
// after the current run. QueuedPrompts and QueuedPromptsList count and list
// only the prompts the user sent, so the queue pill and the editor's Up key
// never show the agent's internal follow-up as if the user had typed it.
func TestQueuedPrompts_ExcludesHiddenContinuations(t *testing.T) {
	t.Parallel()

	a := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	const sessionID = "s1"

	// The stop-hook continuation is queued at the end of a turn, while the
	// session still looks busy.
	a.queueContinuation(SessionAgentCall{SessionID: sessionID, Prompt: "stop hook follow-up", HiddenUserMessage: true})
	// User prompts arrive through the busy-session branch of Run.
	mu := a.sessionMu(sessionID)
	mu.Lock()
	a.enqueueCall(SessionAgentCall{SessionID: sessionID, Prompt: "first prompt"})
	a.enqueueCall(SessionAgentCall{SessionID: sessionID, Prompt: "second prompt"})
	mu.Unlock()

	require.Equal(t, 2, a.QueuedPrompts(sessionID),
		"the hidden continuation must not count as a queued user prompt")
	require.Equal(t, []string{"first prompt", "second prompt"}, a.QueuedPromptsList(sessionID),
		"the hidden continuation must not list as a queued user prompt")
}

// PopQueuedPrompts takes the user's queued prompts out of the queue, oldest
// first, leaving the hidden continuation queued so the turn-end handoff can
// still run it.
func TestPopQueuedPrompts_TakesUserPromptsKeepsContinuation(t *testing.T) {
	t.Parallel()

	a := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	const sessionID = "s1"

	a.queueContinuation(SessionAgentCall{SessionID: sessionID, Prompt: "stop hook follow-up", HiddenUserMessage: true})
	mu := a.sessionMu(sessionID)
	mu.Lock()
	a.enqueueCall(SessionAgentCall{SessionID: sessionID, Prompt: "first prompt"})
	a.enqueueCall(SessionAgentCall{SessionID: sessionID, Prompt: "second prompt"})
	mu.Unlock()

	popped := a.PopQueuedPrompts(sessionID)
	require.Equal(t, []string{"first prompt", "second prompt"}, popped,
		"popped prompts must be the user's, oldest first")
	require.Empty(t, a.QueuedPromptsList(sessionID),
		"the user's prompts must be out of the queue after the pop")

	queued, ok := a.messageQueue.Get(sessionID)
	require.True(t, ok)
	require.Len(t, queued, 1)
	require.True(t, queued[0].HiddenUserMessage,
		"the hidden continuation must stay queued for the turn-end handoff")
}

// A session whose queue holds only hidden continuations pops to empty and
// ends up with no queue entry at all.
func TestPopQueuedPrompts_OnlyContinuationsLeftsQueueEmpty(t *testing.T) {
	t.Parallel()

	a := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	const sessionID = "s1"

	a.queueContinuation(SessionAgentCall{SessionID: sessionID, Prompt: "stop hook follow-up", HiddenUserMessage: true})

	require.Empty(t, a.PopQueuedPrompts(sessionID))
	remaining, ok := a.messageQueue.Get(sessionID)
	require.True(t, ok)
	require.Len(t, remaining, 1)
	require.True(t, remaining[0].HiddenUserMessage)

	// A session with nothing queued pops to nil without touching the map.
	require.Nil(t, a.PopQueuedPrompts("empty"))
}

// PopQueuedPrompts runs under the per-session dispatch mutex, the same lock
// enqueueCall and queueContinuation append under; with -race this pins the
// read-modify-write as safe against a concurrent prompt arrival.
func TestPopQueuedPrompts_DoesNotRaceEnqueueCall(t *testing.T) {
	t.Parallel()

	a := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	const sessionID = "s1"

	mu := a.sessionMu(sessionID)
	mu.Lock()
	for range 20 {
		a.enqueueCall(SessionAgentCall{SessionID: sessionID, Prompt: "user prompt"})
	}
	mu.Unlock()

	done := make(chan []string, 1)
	go func() {
		done <- a.PopQueuedPrompts(sessionID)
	}()
	for range 20 {
		mu.Lock()
		a.enqueueCall(SessionAgentCall{SessionID: sessionID, Prompt: "user prompt"})
		mu.Unlock()
	}

	popped := <-done
	require.Equal(t, 40, len(popped)+a.QueuedPrompts(sessionID),
		"the pop and the concurrent appends must account for every prompt; a lost entry is a silently dropped prompt")
}
