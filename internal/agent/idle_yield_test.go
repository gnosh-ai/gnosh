package agent

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// blockingSummaryModel holds its summary request open until the request
// is cancelled or release is closed, so a test can watch what happens to
// a prompt sent while a compaction is running. Ordinary turns answer at
// once. It stands in for the AI box, whose router holds one model slot:
// a summary that cannot get the model back sits in its reconnect window
// for minutes.
type blockingSummaryModel struct {
	fakeLanguageModel
	mu      sync.Mutex
	turns   []string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingSummaryModel() *blockingSummaryModel {
	return &blockingSummaryModel{started: make(chan struct{}), release: make(chan struct{})}
}

func (m *blockingSummaryModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	if isTitleRequest(call) {
		return wedgeReply("title"), nil
	}
	if isSummaryRequest(call) {
		m.once.Do(func() { close(m.started) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-m.release:
			return wedgeReply("summary"), nil
		}
	}
	texts := userTexts(call.Prompt)
	last := ""
	if len(texts) > 0 {
		last = texts[len(texts)-1]
	}
	m.mu.Lock()
	m.turns = append(m.turns, last)
	m.mu.Unlock()
	return wedgeReply("ok"), nil
}

func (m *blockingSummaryModel) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.turns...)
}

// startSummary runs a compaction in the background and waits until its
// request has actually begun, so the caller knows the session is held.
func startSummary(t *testing.T, a *sessionAgent, llm *blockingSummaryModel, sessionID string, yieldable bool) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- a.summarize(context.Background(), sessionID, "", "", nil, nil, preCompactTriggerAuto, yieldable)
	}()
	select {
	case <-llm.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the summary request never began")
	}
	require.True(t, a.hasActiveRun(sessionID), "the summary must hold the session")
	return done
}

// Idle compaction is opportunistic housekeeping, so it gives way when
// the user sends a prompt rather than making them wait behind it.
func TestRun_IdleCompactionGivesWayToAPrompt(t *testing.T) {
	t.Parallel()

	llm := newBlockingSummaryModel()
	a, sessionID, _ := wedgeAgent(t, llm)
	done := startSummary(t, a, llm, sessionID, true)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "urgent"})
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the idle compaction did not give way")
	}

	require.Eventually(t, func() bool {
		return slices.Contains(llm.seen(), "urgent")
	}, 10*time.Second, 10*time.Millisecond,
		"the prompt must run rather than wait for the idle compaction")

	require.Eventually(t, func() bool {
		_, armed := a.idleCompaction.Get(sessionID)
		return armed
	}, 10*time.Second, 10*time.Millisecond,
		"the compaction that gave way must be armed again, not lost")
}

// A summary the context window forced must not give way: the next turn
// cannot proceed without it, so the prompt waits as it would for a turn.
func TestRun_ForcedCompactionStillHoldsThePrompt(t *testing.T) {
	t.Parallel()

	llm := newBlockingSummaryModel()
	a, sessionID, _ := wedgeAgent(t, llm)
	done := startSummary(t, a, llm, sessionID, false)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "waits"})
	require.NoError(t, err)

	require.Equal(t, 1, a.QueuedPrompts(sessionID),
		"a forced summary must not give way; the prompt queues behind it")
	require.NotContains(t, llm.seen(), "waits",
		"the prompt must not run while the forced summary is still going")
	require.True(t, a.hasActiveRun(sessionID), "the forced summary still holds the session")

	close(llm.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the summary never finished")
	}
}

// Escape must stop a summary in flight. summarize registers under the
// session\x27s own key, which is what Cancel reads; a separate
// "<sessionID>-summarize" key was read there for a while but never
// written by anything.
func TestCancel_StopsASummaryInFlight(t *testing.T) {
	t.Parallel()

	llm := newBlockingSummaryModel()
	a, sessionID, _ := wedgeAgent(t, llm)
	done := startSummary(t, a, llm, sessionID, false)

	a.Cancel(sessionID)

	// A cancelled summary reports nothing to say: it drops its
	// half-written summary message and returns nil. Stopping promptly
	// is the whole claim here -- the request is held open until it is
	// cancelled, so returning at all proves Cancel reached it.
	select {
	case err := <-done:
		require.NoError(t, err, "a cancelled summary drops its message and reports nothing")
	case <-time.After(10 * time.Second):
		t.Fatal("Cancel did not stop the summary in flight")
	}
	require.False(t, a.hasActiveRun(sessionID), "the cancelled summary must release the session")
}
