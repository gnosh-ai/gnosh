package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

const deniedWriteInput = `{"path":"a.txt"}`

// deniedToolModel asks for one write; the user's answer to it ends the turn.
type deniedToolModel struct {
	fakeLanguageModel
	calls atomic.Int64
}

func (m *deniedToolModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	m.calls.Add(1)
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputStart, ID: "call-1", ToolCallName: "write"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputDelta, ID: "call-1", Delta: deniedWriteInput})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputEnd, ID: "call-1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolCall, ID: "call-1", ToolCallName: "write", ToolCallInput: deniedWriteInput})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls})
	}, nil
}

type deniedWriteParams struct {
	Path string `json:"path"`
}

// A turn the user ended by denying a permission is over, but not done: its
// RunComplete says Halted, so /loop stops instead of sending the next prompt
// into the user's "no".
func TestRun_ADeniedPermissionMarksTheTurnHalted(t *testing.T) {
	env := testEnv(t)
	broker := pubsub.NewBroker[notify.RunComplete]()
	t.Cleanup(broker.Shutdown)

	llm := &deniedToolModel{}
	write := fantasy.NewAgentTool("write", "Write a file.",
		func(context.Context, deniedWriteParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return tools.NewPermissionDeniedResponse(), nil
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
		Tools:                []fantasy.AgentTool{write},
		RunComplete:          broker,
	}).(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "denied")
	require.NoError(t, err)
	// Seed a user message so Run skips title generation, which would call
	// the same model from another goroutine.
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)

	events := broker.Subscribe(t.Context())
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "write a.txt"})
	require.NoError(t, err)

	select {
	case ev := <-events:
		require.True(t, ev.Payload.Halted, "the denial ended the turn")
		require.False(t, ev.Payload.Cancelled)
		require.Empty(t, ev.Payload.Error)
	case <-time.After(5 * time.Second):
		t.Fatal("no RunComplete")
	}
	require.Equal(t, int64(1), llm.calls.Load(), "a halted turn does not ask the model again")
}

// A turn the user ended by denying a permission is not handed to the Stop
// hooks: their "not done" would send the agent straight back to what was just
// refused. (Claude Code does not run Stop hooks when the user stops the
// agent either.) The hook here would block once, so a regression costs one
// extra turn and fails, instead of hanging.
func TestRun_ADeniedPermissionIsNotHandedToTheStopHooks(t *testing.T) {
	env := testEnv(t)
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{hooks.EventStop: {{
		Command: `[ "$CRUSH_STOP_HOOK_ACTIVE" = "1" ] && exit 0; echo "not done" >&2; exit 2`,
	}}}}
	require.NoError(t, cfg.ValidateHooks())

	llm := &deniedToolModel{}
	write := fantasy.NewAgentTool("write", "Write a file.",
		func(context.Context, deniedWriteParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return tools.NewPermissionDeniedResponse(), nil
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
		Tools:                []fantasy.AgentTool{write},
		Cfg:                  config.NewTestStore(cfg),
	}).(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "denied with a stop hook")
	require.NoError(t, err)
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, RunID: "run-1", Prompt: "write a.txt"})
	require.NoError(t, err)
	require.Equal(t, int64(1), llm.calls.Load(), "the Stop hook must not resume a turn the user halted")
}
