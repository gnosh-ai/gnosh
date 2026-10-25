package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// failingSummaryModel answers ordinary turns with "ok" and fails every
// summary request, the way a local model server does when the router has
// swapped its model out from under an idle compaction.
type failingSummaryModel struct {
	fakeLanguageModel
	mu    sync.Mutex
	turns []string
}

func (m *failingSummaryModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	if isTitleRequest(call) {
		return wedgeReply("title"), nil
	}
	if isSummaryRequest(call) {
		return nil, errors.New("the model server is not up")
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

func (m *failingSummaryModel) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.turns...)
}

// isSummaryRequest reports whether call is the agent's own compaction
// request, which carries the summary prompt as its last user message.
func isSummaryRequest(call fantasy.Call) bool {
	for i := len(call.Prompt) - 1; i >= 0; i-- {
		msg := call.Prompt[i]
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				return len(text.Text) > 40 && text.Text[:40] == "Stop work on the task here. Your task is"
			}
		}
	}
	return false
}

func wedgeReply(text string) fantasy.StreamResponse {
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: text})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}
}

func wedgeAgent(t *testing.T, llm fantasy.LanguageModel) (*sessionAgent, string, fakeEnv) {
	t.Helper()
	model := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:   model,
		SmallModel:   model,
		SystemPrompt: "test",
		IsYolo:       true,
		Sessions:     env.sessions,
		Messages:     env.messages,
		Cfg:          config.NewTestStore(&config.Config{}),
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "long session")
	require.NoError(t, err)
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)
	return a, sess.ID, env
}

// A compaction takes the session, so a prompt sent while it runs is
// queued behind it — summarize drains that queue when it is done. When
// the summary request fails instead, every error return sits above the
// drain, so the queued prompt is left in the queue with nothing that will
// ever run it: Jae's "the first command works but the second command
// always queues and nothing happens".
func TestSummarize_DrainsQueueWhenTheSummaryFails(t *testing.T) {
	t.Parallel()

	llm := &failingSummaryModel{}
	a, sessionID, _ := wedgeAgent(t, llm)

	// A prompt that arrived while the compaction held the session.
	mu := a.sessionMu(sessionID)
	mu.Lock()
	a.enqueueCall(SessionAgentCall{SessionID: sessionID, Prompt: "second command"})
	mu.Unlock()
	require.Equal(t, 1, a.QueuedPrompts(sessionID))

	err := a.summarize(t.Context(), sessionID, "", "", nil, nil, preCompactTriggerAuto)
	require.Error(t, err, "the summary request fails in this test")

	require.False(t, a.hasActiveRun(sessionID), "the failed compaction must release the session")
	require.Equal(t, 0, a.QueuedPrompts(sessionID),
		"a prompt queued behind a compaction that then failed must still be run, not stranded in the queue forever")
	require.Contains(t, llm.seen(), "second command",
		"the queued prompt must reach the model once the compaction gave up")
}

// The session must be idle once a turn that auto-compacted is over, and
// the next prompt must run rather than queue: a leftover activeRequests
// entry makes Run queue every later prompt forever.
func TestRun_SecondPromptRunsAfterAutoCompaction(t *testing.T) {
	t.Parallel()

	llm := &failingSummaryModel{}
	a, sessionID, _ := wedgeAgent(t, llm)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "one"})
	require.NoError(t, err)
	require.False(t, a.hasActiveRun(sessionID), "the session must be idle after the first turn")

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "two"})
	require.NoError(t, err)
	require.Equal(t, 0, a.QueuedPrompts(sessionID), "the second prompt must run, not queue")
	require.Equal(t, []string{"one", "two"}, llm.seen(), "both prompts must reach the model")
}
