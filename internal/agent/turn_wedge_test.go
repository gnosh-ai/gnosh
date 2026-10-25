package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// failingStreamModel answers the first ordinary turn with a stream that
// dies, and every later one with "ok". While the first turn is in
// flight it runs onFirst, which is where the test queues the prompt that
// must survive the failure.
//
// This is the shape seen on Jae's box: llama-server held the response
// open, sent nothing, and the request then died. A goroutine dump taken
// while his session was wedged showed the stream gone and the session
// idle, with his second prompt still sitting in the queue.
type failingStreamModel struct {
	fakeLanguageModel
	mu      sync.Mutex
	turns   []string
	tripped bool
	onFirst func()
	// byReturn fails by returning an error from Stream itself rather
	// than yielding an error part, the other way a dead request shows up.
	byReturn bool
}

func (m *failingStreamModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	if isTitleRequest(call) {
		return wedgeReply("title"), nil
	}
	texts := userTexts(call.Prompt)
	last := ""
	if len(texts) > 0 {
		last = texts[len(texts)-1]
	}
	m.mu.Lock()
	m.turns = append(m.turns, last)
	first := !m.tripped
	m.tripped = true
	m.mu.Unlock()
	if !first {
		return wedgeReply("ok"), nil
	}
	if m.onFirst != nil {
		m.onFirst()
	}
	if m.byReturn {
		return nil, errors.New("the model server stopped sending")
	}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: errors.New("the model server stopped sending")})
	}, nil
}

func (m *failingStreamModel) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.turns...)
}

// A turn takes the session, so a prompt sent while it runs is queued
// behind it and the handoff at the end of the turn runs it. When the
// turn's stream dies instead, every error return sits above that handoff,
// so the queued prompt is left with nothing that will ever run it: the
// session goes idle, the prompt stays shown as queued, and it never runs.
//
// This is the normal-turn twin of the summarize defect fixed in d82838a,
// and it is what Jae actually hit: "the first command works but the
// second command always queues and nothing happens".
func TestRun_DrainsQueueWhenTheStreamFails(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		byReturn bool
	}{
		{"stream yields an error part", false},
		{"Stream returns an error", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			llm := &failingStreamModel{byReturn: tc.byReturn}
			a, sessionID, _ := wedgeAgent(t, llm)
			llm.onFirst = func() {
				mu := a.sessionMu(sessionID)
				mu.Lock()
				a.enqueueCall(SessionAgentCall{SessionID: sessionID, Prompt: "second command"})
				mu.Unlock()
			}

			_, _ = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "first command"})

			require.False(t, a.hasActiveRun(sessionID), "a turn whose stream died must release the session")
			require.Equal(t, 0, a.QueuedPrompts(sessionID),
				"a prompt queued behind a turn whose stream died must still be run, not stranded in the queue forever")
			require.Contains(t, llm.seen(), "second command",
				"the queued prompt must reach the model once the failed turn gave up")
		})
	}
}
