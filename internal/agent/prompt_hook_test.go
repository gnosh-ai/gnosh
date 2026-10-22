package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// promptRecordingModel answers every turn with "ok" and keeps the prompt
// (the whole message list) each turn was sent.
type promptRecordingModel struct {
	fakeLanguageModel
	mu      sync.Mutex
	prompts []fantasy.Prompt
}

func (m *promptRecordingModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, call.Prompt)
	m.mu.Unlock()
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: "ok"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

func (m *promptRecordingModel) recorded() []fantasy.Prompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]fantasy.Prompt(nil), m.prompts...)
}

// userTexts returns the text of every user message in prompt, in order.
func userTexts(prompt fantasy.Prompt) []string {
	var out []string
	for _, msg := range prompt {
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		var b strings.Builder
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				b.WriteString(text.Text)
			}
		}
		out = append(out, b.String())
	}
	return out
}

type promptHookRun struct {
	agent     *sessionAgent
	env       fakeEnv
	model     *promptRecordingModel
	sessionID string
}

// promptHookAgent builds a session agent with the given hooks configured,
// through ValidateHooks, and a session with an earlier prompt in it so Run
// does not ask the model for a title.
func promptHookAgent(t *testing.T, cfgHooks map[string][]config.HookConfig) promptHookRun {
	t.Helper()
	env := testEnv(t)
	cfg := &config.Config{Hooks: cfgHooks}
	require.NoError(t, cfg.ValidateHooks())
	llm := &promptRecordingModel{}
	model := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  config.NewTestStore(cfg),
		Notify:               &recordingNotifier{},
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "prompt hooks")
	require.NoError(t, err)
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)
	return promptHookRun{agent: a, env: env, model: llm, sessionID: sess.ID}
}

func promptHooks(commands ...string) map[string][]config.HookConfig {
	cfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		cfgs[i] = config.HookConfig{Command: cmd}
	}
	return map[string][]config.HookConfig{hooks.EventUserPromptSubmit: cfgs}
}

// What a UserPromptSubmit hook prints reaches the model after the prompt,
// in Claude Code's tag, while the chat keeps only what the user typed. On
// the next turn the model is sent the same text for that prompt, rebuilt
// from the session, so the server's prompt cache still matches.
func TestRun_UserPromptSubmitHookAddsContext(t *testing.T) {
	t.Parallel()

	r := promptHookAgent(t, promptHooks(`echo "The build on main is red."`))
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "fix the build"})
	require.NoError(t, err)

	want := "fix the build\n\n<user-prompt-submit-hook>\nThe build on main is red.\n</user-prompt-submit-hook>"
	prompts := r.model.recorded()
	require.Len(t, prompts, 1)
	first := userTexts(prompts[0])
	require.Equal(t, want, first[len(first)-1])

	msgs, err := r.env.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)
	var stored *message.Message
	for i := range msgs {
		if msgs[i].Role == message.User && msgs[i].Content().Text == "fix the build" {
			stored = &msgs[i]
		}
	}
	require.NotNil(t, stored, "the chat keeps the prompt as typed")
	require.Equal(t, []string{message.PromptHookContext("The build on main is red.")}, stored.PromptHookBlocks())

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "and the tests"})
	require.NoError(t, err)
	prompts = r.model.recorded()
	require.Len(t, prompts, 2)
	require.Contains(t, userTexts(prompts[1]), want, "the rebuilt history sends the first prompt exactly as it went out")
}

// A hook that exits 2 blocks the prompt: Run reports its reason, the
// model is never asked, and nothing is added to the session.
func TestRun_UserPromptSubmitHookBlocks(t *testing.T) {
	t.Parallel()

	r := promptHookAgent(t, promptHooks(`echo "no secrets in prompts" >&2; exit 2`))
	before, err := r.env.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "my key is sk-123"})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPromptBlocked))
	require.Contains(t, err.Error(), "no secrets in prompts")
	require.Empty(t, r.model.recorded(), "a blocked prompt never reaches the model")

	after, err := r.env.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)
	require.Len(t, after, len(before), "a blocked prompt is not added to the session")
	require.False(t, r.agent.IsSessionBusy(r.sessionID))
}

// Claude Code's JSON forms work too: "decision": "block" blocks with its
// reason, and hookSpecificOutput.additionalContext adds context.
func TestRun_UserPromptSubmitHookClaudeCodeJSON(t *testing.T) {
	t.Parallel()

	r := promptHookAgent(t, promptHooks(`echo '{"decision":"block","reason":"not on a Friday"}'`))
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "deploy"})
	require.ErrorIs(t, err, ErrPromptBlocked)
	require.Contains(t, err.Error(), "not on a Friday")

	r = promptHookAgent(t, promptHooks(`echo '{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"Today is Thursday."}}'`))
	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "deploy"})
	require.NoError(t, err)
	texts := userTexts(r.model.recorded()[0])
	require.Equal(t, "deploy\n\n<user-prompt-submit-hook>\nToday is Thursday.\n</user-prompt-submit-hook>", texts[len(texts)-1])
}

// The hook reads the prompt on stdin, as in Claude Code.
func TestRun_UserPromptSubmitHookSeesThePrompt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	r := promptHookAgent(t, promptHooks(`cat > `+payloadPath))
	_, err := r.agent.Run(t.Context(), SessionAgentCall{
		SessionID:   r.sessionID,
		Prompt:      "fix the build",
		Attachments: []message.Attachment{{FileName: "build.log", FilePath: "/tmp/build.log", MimeType: "text/plain", Content: []byte("FAIL")}},
	})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(readFileForTest(t, payloadPath)), &payload))
	require.Equal(t, "UserPromptSubmit", payload["hook_event_name"])
	require.Equal(t, "fix the build", payload["prompt"])
	require.Equal(t, r.sessionID, payload["session_id"])
	require.Equal(t, []any{"build.log"}, payload["attachments"])
}

// The agent's own continuations are not prompts the user sent: a Stop
// hook that sends the agent back to work once does not run the
// UserPromptSubmit hook a second time, and adds no second context.
func TestRun_UserPromptSubmitHookSkipsContinuations(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	stopped := filepath.Join(dir, "stopped")
	cfgHooks := promptHooks(`echo x >> ` + count + `; echo "context"`)
	cfgHooks[hooks.EventStop] = []config.HookConfig{{
		Command: `[ -f ` + stopped + ` ] || { touch ` + stopped + `; echo "one more check" >&2; exit 2; }`,
	}}
	r := promptHookAgent(t, cfgHooks)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "fix the build"})
	require.NoError(t, err)

	require.Len(t, r.model.recorded(), 2, "the Stop hook's continuation is a second turn")
	data, err := os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data), "the hook ran once, for the prompt the user sent")
	for _, text := range userTexts(r.model.recorded()[1]) {
		if strings.Contains(text, "one more check") {
			require.NotContains(t, text, "<user-prompt-submit-hook>", "the continuation carries no hook context")
		}
	}
}

// gatedModel is promptRecordingModel whose first turn waits for release,
// so a test can send another prompt while it runs.
type gatedModel struct {
	promptRecordingModel
	entered chan struct{}
	release chan struct{}
	first   sync.Once
}

func (m *gatedModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.first.Do(func() {
		close(m.entered)
		<-m.release
	})
	return m.promptRecordingModel.Stream(ctx, call)
}

// A prompt sent while a turn runs waits in the queue. Its hook runs when
// it is sent, once, and what it added goes with it when it runs.
func TestRun_UserPromptSubmitHookRunsOnceForAQueuedPrompt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	r := promptHookAgent(t, promptHooks(`echo x >> `+count+`; echo "hook context"`))
	gated := &gatedModel{entered: make(chan struct{}), release: make(chan struct{})}
	r.agent.largeModel.Set(Model{Model: gated, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}})

	done := make(chan error, 1)
	go func() {
		_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
		done <- err
	}()
	<-gated.entered

	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "second"})
	require.NoError(t, err)
	require.Equal(t, []string{"second"}, r.agent.QueuedPromptsList(r.sessionID), "sent while the turn runs, it is queued")
	data, err := os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, "x\nx\n", string(data), "its hook ran when it was sent")

	close(gated.release)
	require.NoError(t, <-done)

	data, err = os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, "x\nx\n", string(data), "running it later does not run the hook again")
	prompts := gated.recorded()
	require.Len(t, prompts, 2)
	texts := userTexts(prompts[1])
	require.Equal(t, "second\n\n<user-prompt-submit-hook>\nhook context\n</user-prompt-submit-hook>", texts[len(texts)-1])
}
