package agent

import (
	"context"
	"sync"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// finished is an assistant message with the given parts and finish reason.
func finished(reason message.FinishReason, parts ...message.ContentPart) *message.Message {
	msg := &message.Message{Role: message.Assistant, Parts: parts}
	msg.AddFinish(reason, "", "")
	return msg
}

// Only the model's own silence counts as answering nothing: no tool call
// and no text. A turn that was cancelled, errored, or ran out of tokens is
// genuinely over, and a reply with either a tool call or text is an answer.
func TestAnsweredNothing(t *testing.T) {
	t.Parallel()

	require.True(t, answeredNothing(finished(message.FinishReasonEndTurn)), "nothing at all")
	require.True(t, answeredNothing(finished(message.FinishReasonEndTurn,
		message.ReasoningContent{Thinking: "we should list the files"})), "reasoning only")
	require.True(t, answeredNothing(finished(message.FinishReasonEndTurn,
		message.TextContent{Text: "  \n\t "})), "whitespace is not an answer")

	require.False(t, answeredNothing(finished(message.FinishReasonEndTurn,
		message.TextContent{Text: "Done."})), "text is an answer")
	require.False(t, answeredNothing(finished(message.FinishReasonToolUse,
		message.ToolCall{ID: "c1", Name: "ls"})), "a tool call is an answer")
	require.False(t, answeredNothing(finished(message.FinishReasonEndTurn,
		message.ToolCall{ID: "c1", Name: "ls"})),
		"a tool call is still an answer when the turn ends on it, as it does when a tool result halts the turn")
	require.False(t, answeredNothing(finished(message.FinishReasonCanceled)), "the user stopped it")
	require.False(t, answeredNothing(finished(message.FinishReasonError)), "the request failed")
	require.False(t, answeredNothing(finished(message.FinishReasonMaxTokens)),
		"out of budget is a different fault, with a different fix")
	require.False(t, answeredNothing(nil))
}

// The continuation is the same call with its prompt replaced, hidden from
// the transcript, and it gives up after emptyAnswerMaxTries so a wedged
// model cannot spin all night.
func TestEmptyAnswerContinuation(t *testing.T) {
	t.Parallel()

	call := SessionAgentCall{
		SessionID:   "s1",
		Prompt:      "list the files",
		Attachments: []message.Attachment{{FileName: "a.png"}},
	}
	next, ok := emptyAnswerContinuation(call, finished(message.FinishReasonEndTurn))
	require.True(t, ok)
	require.Equal(t, 1, next.emptyAnswerDepth)
	require.Equal(t, emptyAnswerPrompt, next.Prompt)
	require.True(t, next.HiddenUserMessage, "the user did not type it")
	require.Empty(t, next.Attachments, "they went out with the real prompt")
	require.Equal(t, "s1", next.SessionID)

	_, ok = emptyAnswerContinuation(call, finished(message.FinishReasonEndTurn, message.TextContent{Text: "hi"}))
	require.False(t, ok, "an answer needs no continuation")

	capped := call
	capped.emptyAnswerDepth = emptyAnswerMaxTries
	_, ok = emptyAnswerContinuation(capped, finished(message.FinishReasonEndTurn))
	require.False(t, ok, "it gives up rather than spin")
}

// scriptedModel streams the parts of one script entry per request.
type scriptedModel struct {
	fakeLanguageModel
	mu    sync.Mutex
	steps [][]fantasy.StreamPart
	calls int
}

func (m *scriptedModel) Stream(_ context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	parts := m.steps[min(m.calls, len(m.steps)-1)]
	m.calls++
	return func(yield func(fantasy.StreamPart) bool) {
		for _, p := range parts {
			if !yield(p) {
				return
			}
		}
	}, nil
}

func (m *scriptedModel) requests() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// reasoningOnly is what the AI box's gpt-oss returns about one time in
// seven: it thinks, then ends the turn without saying anything.
func reasoningOnly() []fantasy.StreamPart {
	return []fantasy.StreamPart{
		{Type: fantasy.StreamPartTypeReasoningStart, ID: "r"},
		{Type: fantasy.StreamPartTypeReasoningDelta, ID: "r", Delta: "I should answer."},
		{Type: fantasy.StreamPartTypeReasoningEnd, ID: "r"},
		{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
	}
}

func saying(text string) []fantasy.StreamPart {
	return []fantasy.StreamPart{
		{Type: fantasy.StreamPartTypeTextStart, ID: "t"},
		{Type: fantasy.StreamPartTypeTextDelta, ID: "t", Delta: text},
		{Type: fantasy.StreamPartTypeTextEnd, ID: "t"},
		{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
	}
}

// A model that answers with nothing is asked again, and the turn ends with
// the answer it then gives, instead of ending in silence.
func TestRun_EmptyAnswerIsAskedAgain(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	llm := &scriptedModel{steps: [][]fantasy.StreamPart{reasoningOnly(), saying("There are two files.")}}
	a := testSessionAgent(env, llm, llm, "test")
	sess, err := env.sessions.Create(t.Context(), "empty answer")
	require.NoError(t, err)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "list the files"})
	require.NoError(t, err)

	require.Equal(t, 2, llm.requests(), "the empty reply is retried once")
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var said []string
	var hidden int
	for _, m := range msgs {
		if m.Role == message.Assistant {
			if text := m.Content().String(); text != "" {
				said = append(said, text)
			}
		}
		for _, p := range m.Parts {
			if tc, ok := p.(message.TextContent); ok && tc.Hidden {
				hidden++
			}
		}
	}
	require.Equal(t, []string{"There are two files."}, said, "the turn ends with a real answer")
	require.Equal(t, 1, hidden, "the nudge is hidden from the transcript")
}

// A model that never answers is let go after emptyAnswerMaxTries rather
// than asked forever.
func TestRun_EmptyAnswerGivesUp(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	llm := &scriptedModel{steps: [][]fantasy.StreamPart{reasoningOnly()}}
	a := testSessionAgent(env, llm, llm, "test")
	sess, err := env.sessions.Create(t.Context(), "never answers")
	require.NoError(t, err)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "list the files"})
	require.NoError(t, err)
	require.Equal(t, emptyAnswerMaxTries+1, llm.requests(), "the first ask plus its retries")
}
