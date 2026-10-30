package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// streamingInputModel streams one tool call's input in two pieces, waiting
// on proceed between them, then answers "done".
type streamingInputModel struct {
	fakeLanguageModel
	calls   atomic.Int64
	proceed chan struct{}
}

func (m *streamingInputModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	if m.calls.Add(1) == 1 {
		return func(yield func(fantasy.StreamPart) bool) {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputStart, ID: "call-1", ToolCallName: "note"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputDelta, ID: "call-1", Delta: `{"note":"wr`})
			<-m.proceed
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputDelta, ID: "call-1", Delta: `iting"}`})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputEnd, ID: "call-1"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolCall, ID: "call-1", ToolCallName: "note", ToolCallInput: `{"note":"writing"}`})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls})
		}, nil
	}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: "done"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

type noteParams struct {
	Note string `json:"note"`
}

// A tool call's input reaches the session as the model streams it, so the
// chat can show the call being written (a gnovm_run call's code, a grep's
// pattern) before it is whole; the finished call then carries the input
// the model completed.
func TestRun_ToolCallInputStreamsIntoTheMessage(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	proceed := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	note := fantasy.NewAgentTool("note", "Take a note.",
		func(context.Context, noteParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("ok"), nil
		})
	model := Model{Model: &streamingInputModel{proceed: proceed}, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Tools:                []fantasy.AgentTool{note},
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "streaming input")
	require.NoError(t, err)
	// A message already there keeps Run from asking the model for a title.
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "note it"})
		done <- err
	}()
	call := func() (message.ToolCall, bool) {
		msgs, err := env.messages.List(t.Context(), sess.ID)
		if err != nil {
			return message.ToolCall{}, false
		}
		for _, m := range msgs {
			for _, c := range m.ToolCalls() {
				if c.ID == "call-1" {
					return c, true
				}
			}
		}
		return message.ToolCall{}, false
	}
	require.Eventually(t, func() bool {
		c, ok := call()
		return ok && !c.Finished && c.Input == `{"note":"wr`
	}, 10*time.Second, 10*time.Millisecond, "the input so far reaches the session before the call is whole")

	release()
	require.NoError(t, <-done)
	c, ok := call()
	require.True(t, ok)
	require.True(t, c.Finished)
	require.Equal(t, `{"note":"writing"}`, c.Input)
}
