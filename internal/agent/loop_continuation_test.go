package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

const (
	loopingInput  = `{"file_path":"permission.go","offset":233,"limit":8}`
	loopingAnswer = "permission.go:233 stats the path before choosing its directory."
)

// loopingModel re-reads the same lines of a file on every step, as a
// sub-agent on the AI box did, until it is told it is repeating itself;
// then it answers. Nothing caps the loop, so past giveUpAfter turns it
// answers anyway: a broken nudge then fails the test instead of hanging it.
type loopingModel struct {
	fakeLanguageModel
	turns       atomic.Int64
	giveUpAfter int64
}

func (m *loopingModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	n := m.turns.Add(1)
	nudged := strings.Contains(lastUserText(call.Prompt), "will not tell you anything new")
	return func(yield func(fantasy.StreamPart) bool) {
		if nudged || n > m.giveUpAfter {
			answer := loopingAnswer
			if !nudged {
				answer = "gave up without being told"
			}
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t", Delta: answer})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
			return
		}
		id := fmt.Sprintf("call-%d", n)
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputStart, ID: id, ToolCallName: "view"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputDelta, ID: id, Delta: loopingInput})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputEnd, ID: id})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolCall, ID: id, ToolCallName: "view", ToolCallInput: loopingInput})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls})
	}, nil
}

type loopingViewParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

// The loop that cost a sub-agent 45 minutes, replayed. The agent must be
// told it is repeating itself and kept going, and the answer it then gives
// must be what Run returns, which is what a parent agent receives, instead
// of the turn ending silently on a tool call with no output at all.
func TestRun_RepeatingAgentIsToldAndKeepsGoing(t *testing.T) {
	env := testEnv(t)
	llm := &loopingModel{giveUpAfter: 3 * loopDetectionWindowSize}
	view := fantasy.NewAgentTool("view", "Read a file.",
		func(context.Context, loopingViewParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("234|\tfileInfo, err := os.Stat(opts.Path)"), nil
		})
	model := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Tools:                []fantasy.AgentTool{view},
	}).(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "looping")
	require.NoError(t, err)
	// Seed a user message so Run skips title generation, which would call
	// the same model from another goroutine.
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)

	result, err := a.Run(t.Context(), SessionAgentCall{
		SessionID:      sess.ID,
		RunID:          "run-1",
		Prompt:         "find a bug in permission.go",
		NonInteractive: true,
	})
	require.NoError(t, err)

	require.Equal(t, loopingAnswer, subAgentOutput(result),
		"the answer given after the nudge is what Run returns")
	require.Equal(t, int64(loopDetectionWindowSize+1), llm.turns.Load(),
		"the repeated steps until the detector fires, then one answer to the nudge")
	queued, ok := a.messageQueue.Get(sess.ID)
	require.False(t, ok && len(queued) > 0, "the turn must not leave work stranded on the queue")

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var nudges int
	for _, m := range msgs {
		if m.Role != message.User {
			continue
		}
		for _, part := range m.Parts {
			text, ok := part.(message.TextContent)
			if !ok || !strings.Contains(text.Text, "will not tell you anything new") {
				continue
			}
			nudges++
			require.True(t, text.Hidden, "the nudge is generated, not typed, so it stays out of the transcript")
			require.Contains(t, text.Text, "view "+loopingInput, "the nudge names the call being repeated")
		}
	}
	require.Equal(t, 1, nudges)
}
