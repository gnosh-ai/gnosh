package agent

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// A continuation is queued at the end of a turn, while the session still
// looks busy — which is exactly when an arriving prompt goes to
// enqueueCall. Both append to the same slice, so without a shared lock
// one of them is lost: a prompt the user already sent, gone, with
// nothing to show for it.
//
// The prompt side mirrors Run, which holds the session dispatch mutex
// across enqueueCall; queueContinuation takes that same mutex, which is
// what makes the two safe against each other. Run with -race this
// catches the unsynchronized read-modify-write itself, not just the
// dropped entry.
func TestQueueContinuation_DoesNotRaceEnqueueCall(t *testing.T) {
	t.Parallel()

	const (
		continuations = 50
		prompts       = 50
	)
	a := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	const sessionID = "s1"

	var wg sync.WaitGroup
	wg.Add(continuations + prompts)
	for i := range continuations {
		go func() {
			defer wg.Done()
			a.queueContinuation(SessionAgentCall{SessionID: sessionID, Prompt: "continuation", stopHookDepth: i})
		}()
	}
	for range prompts {
		go func() {
			defer wg.Done()
			// Mirror Run's busy-session branch, which holds the session
			// dispatch mutex across the append.
			mu := a.sessionMu(sessionID)
			mu.Lock()
			a.enqueueCall(SessionAgentCall{SessionID: sessionID, Prompt: "user prompt"})
			mu.Unlock()
		}()
	}
	wg.Wait()

	queued, ok := a.messageQueue.Get(sessionID)
	require.True(t, ok)
	require.Len(t, queued, continuations+prompts,
		"every queued call must survive; a lost entry is a prompt silently dropped")

	var gotContinuations, gotPrompts int
	for _, q := range queued {
		switch q.Prompt {
		case "continuation":
			gotContinuations++
		case "user prompt":
			gotPrompts++
		}
	}
	require.Equal(t, continuations, gotContinuations)
	require.Equal(t, prompts, gotPrompts)
}

func TestQueueContinuation_AppendsInOrder(t *testing.T) {
	t.Parallel()

	a := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	const sessionID = "s1"

	for i := range 3 {
		a.queueContinuation(SessionAgentCall{SessionID: sessionID, stopHookDepth: i})
	}

	queued, ok := a.messageQueue.Get(sessionID)
	require.True(t, ok)
	require.Len(t, queued, 3)
	for i, q := range queued {
		require.Equal(t, i, q.stopHookDepth, "continuations must drain in the order they were queued")
	}
}

func TestQueueContinuation_KeepsSessionsApart(t *testing.T) {
	t.Parallel()

	a := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	a.queueContinuation(SessionAgentCall{SessionID: "a", Prompt: "for a"})
	a.queueContinuation(SessionAgentCall{SessionID: "b", Prompt: "for b"})

	qa, _ := a.messageQueue.Get("a")
	qb, _ := a.messageQueue.Get("b")
	require.Len(t, qa, 1)
	require.Len(t, qb, 1)
	require.Equal(t, "for a", qa[0].Prompt)
	require.Equal(t, "for b", qb[0].Prompt)
}
