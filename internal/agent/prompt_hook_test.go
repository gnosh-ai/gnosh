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
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// promptRecordingModel answers every turn with "ok" and keeps the prompt
// (the whole message list) each turn was sent. Title requests are
// filtered out: they are the agent's own side effect, not prompts the
// test sent.
type promptRecordingModel struct {
	fakeLanguageModel
	mu      sync.Mutex
	prompts []fantasy.Prompt
}

func (m *promptRecordingModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	if isTitleRequest(call) {
		return func(yield func(fantasy.StreamPart) bool) {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t1"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: "title"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
		}, nil
	}
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

// isTitleRequest reports whether call is the agent's own title
// generation, which carries that prompt in its last user message.
func isTitleRequest(call fantasy.Call) bool {
	for i := len(call.Prompt) - 1; i >= 0; i-- {
		msg := call.Prompt[i]
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				return strings.HasPrefix(text.Text, "Generate a concise title for the following content:")
			}
		}
	}
	return false
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
	cfg       *config.Config
	notifier  *recordingNotifier
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
	notifier := &recordingNotifier{}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  config.NewTestStore(cfg),
		Notify:               notifier,
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "prompt hooks")
	require.NoError(t, err)
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)
	return promptHookRun{agent: a, env: env, model: llm, sessionID: sess.ID, cfg: cfg, notifier: notifier}
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

// A UserPromptSubmit hook that could not check blocks the prompt as
// exit 2 does, and the user reads why: a gate that cannot run does not
// wave the prompt through.
func TestRun_UserPromptSubmitHookCouldNotEvaluate(t *testing.T) {
	t.Parallel()

	r := promptHookAgent(t, promptHooks(`echo "the secret scanner is not installed" >&2; exit 3`))
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "my key is sk-123"})
	require.ErrorIs(t, err, ErrPromptBlocked)
	require.Contains(t, err.Error(), "could not check this: the secret scanner is not installed")
	require.Empty(t, r.model.recorded(), "a prompt no check could clear never reaches the model")
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
	prompt, _ := payload["prompt"].(string)
	require.True(t, strings.HasPrefix(prompt, "fix the build"), prompt)
	require.Contains(t, prompt, "FAIL", "with the attached file's text, as the model reads it")
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

// What mem recalls for a prompt is read as the prompt goes out: one sent
// while a turn runs, and queued, is not handed what that turn erased from
// mem meanwhile.
func TestRun_QueuedPromptRecallsWhenItGoesOut(t *testing.T) {
	t.Parallel()

	r := promptHookAgent(t, nil)
	gated := &gatedModel{entered: make(chan struct{}), release: make(chan struct{})}
	r.agent.largeModel.Set(Model{Model: gated, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}})
	var mu sync.Mutex
	recall := "<mem>\n- fact `db/staging` = the password is hunter22\n</mem>"
	r.agent.memRecall = func(_ context.Context, _, prompt string) string {
		mu.Lock()
		defer mu.Unlock()
		if prompt == "first" {
			return ""
		}
		return recall
	}

	done := make(chan error, 1)
	go func() {
		_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
		done <- err
	}()
	<-gated.entered
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "and the staging db?"})
	require.NoError(t, err)
	require.Equal(t, []string{"and the staging db?"}, r.agent.QueuedPromptsList(r.sessionID))
	// The turn running redacts the password.
	mu.Lock()
	recall = "<mem>\n- fact `db/staging` = the password is (redacted)\n</mem>"
	mu.Unlock()
	close(gated.release)
	require.NoError(t, <-done)

	prompts := gated.recorded()
	require.Len(t, prompts, 2)
	texts := userTexts(prompts[1])
	last := texts[len(texts)-1]
	require.Contains(t, last, "the password is (redacted)")
	require.NotContains(t, last, "hunter22")
}

// An Esc while a prompt is in its UserPromptSubmit hooks stops the hooks,
// and the prompt is not sent. The session reads as busy meanwhile, which
// is what sends the TUI's Esc to Cancel at all. Before, a prompt in its
// hooks was registered nowhere: the Cancel found nothing, and the prompt
// ran when the hooks finished.
func TestRun_CancelWhileAPromptHookRuns(t *testing.T) {
	t.Parallel()

	r := promptHookAgent(t, promptHooks(`sleep 5; echo "late context"`))
	done := make(chan error, 1)
	go func() {
		_, err := r.agent.Run(context.Background(), SessionAgentCall{SessionID: r.sessionID, Prompt: "do the thing"})
		done <- err
	}()
	require.Eventually(t, func() bool { return r.agent.IsSessionBusy(r.sessionID) }, 3*time.Second, 10*time.Millisecond,
		"a prompt in its hooks makes the session busy")

	start := time.Now()
	r.agent.Cancel(r.sessionID)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(4 * time.Second):
		t.Fatal("Run did not return after the cancel")
	}
	require.Less(t, time.Since(start), 4*time.Second, "the cancel stops the hook rather than waiting it out")
	require.Empty(t, r.model.recorded(), "the cancelled prompt is not sent")
	require.False(t, r.agent.IsSessionBusy(r.sessionID))
}

// The hooks run when a prompt is sent. One queued while no hook was
// configured is not checked by a hook added before its turn comes: that
// blocked it at the turn-end handoff, without releasing its accept
// reservation or ending its run, and made the turn before it report the
// block as its own error.
func TestRun_HookAddedWhileAPromptIsQueuedDoesNotRunForIt(t *testing.T) {
	t.Parallel()

	r := promptHookAgent(t, map[string][]config.HookConfig{})
	gated := &gatedModel{entered: make(chan struct{}), release: make(chan struct{})}
	r.agent.largeModel.Set(Model{Model: gated, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}})

	done := make(chan error, 1)
	go func() {
		_, err := r.agent.Run(context.Background(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
		done <- err
	}()
	<-gated.entered
	_, err := r.agent.Run(context.Background(), SessionAgentCall{SessionID: r.sessionID, Prompt: "second"})
	require.NoError(t, err)

	r.cfg.Hooks[hooks.EventUserPromptSubmit] = []config.HookConfig{{Command: `echo "no" >&2; exit 2`}}
	close(gated.release)
	require.NoError(t, <-done, "the first turn ends without the second prompt's block as its error")

	prompts := gated.recorded()
	require.Len(t, prompts, 2, "the queued prompt runs")
	texts := userTexts(prompts[1])
	require.Equal(t, "second", texts[len(texts)-1])
	accepted, _ := r.agent.acceptedRuns.Get(r.sessionID)
	require.Zero(t, accepted, "no accept reservation is left behind")
}

// The hook reads the prompt as the model will, pasted text included: a
// paste of more than a few lines becomes a text attachment.
func TestRun_UserPromptSubmitHookSeesPastedText(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	r := promptHookAgent(t, promptHooks(`cat > `+payloadPath))
	_, err := r.agent.Run(t.Context(), SessionAgentCall{
		SessionID:   r.sessionID,
		Prompt:      "why does this fail?",
		Attachments: []message.Attachment{{FileName: "paste_1.txt", FilePath: "paste_1.txt", MimeType: "text/plain", Content: []byte("OPENAI_API_KEY=sk-live-123")}},
	})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(readFileForTest(t, payloadPath)), &payload))
	require.Contains(t, payload["prompt"], "why does this fail?")
	require.Contains(t, payload["prompt"], "sk-live-123", "the pasted text is in the prompt the hook reads")
}

// A channel message (Signal, say) was written by someone else, and a block
// would drop it with no reply, so it does not fire the hook.
func TestRun_ChannelMessagesSkipPromptHooks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	r := promptHookAgent(t, promptHooks(`echo x >> `+count))
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: `<channel source="signal">hi</channel>`, Channel: "signal"})
	require.NoError(t, err)
	_, statErr := os.Stat(count)
	require.True(t, os.IsNotExist(statErr), "the hook did not run")
}

// A continuation is the agent's own and carries none of the user's hook
// context; the auto-summarize continuation is not hidden, so it re-sent
// the first prompt's context.
func TestQueueContinuationDropsPromptHookContext(t *testing.T) {
	t.Parallel()

	a := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	a.queueContinuation(SessionAgentCall{SessionID: "s1", Prompt: "go on", promptHookContext: "The build is red.", promptHooksRan: true})
	queued, _ := a.messageQueue.Get("s1")
	require.Len(t, queued, 1)
	require.Empty(t, queued[0].promptHookContext)
	require.True(t, queued[0].promptHooksRan)
}

// When one hook blocks the prompt, another hook's failure is not reported
// as "the prompt was sent without it": nothing was sent.
func TestBlockedPromptIsNotReportedAsSent(t *testing.T) {
	t.Parallel()

	r := promptHookAgent(t, promptHooks(`echo "no" >&2; exit 2`, `exit 1`))
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "hi"})
	require.ErrorIs(t, err, ErrPromptBlocked)
	for _, n := range r.notifier.warnings() {
		require.NotContains(t, n.Message, "sent without it")
	}
}

// A prompt with space around it reaches the model as it will be rebuilt
// from the session on the next turn, so the server's prompt cache holds.
func TestRun_PromptIsSentAsItIsRebuilt(t *testing.T) {
	t.Parallel()

	r := promptHookAgent(t, map[string][]config.HookConfig{})
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "  fix the build\n\n"})
	require.NoError(t, err)
	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "and the tests"})
	require.NoError(t, err)

	prompts := r.model.recorded()
	require.Len(t, prompts, 2)
	first := userTexts(prompts[0])
	require.Equal(t, "fix the build", first[len(first)-1])
	require.Contains(t, userTexts(prompts[1]), "fix the build")

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: " \n "})
	require.ErrorIs(t, err, ErrEmptyPrompt, "a prompt of only space is empty")
}

// A prompt still in its UserPromptSubmit hooks does not stop a summary:
// IsSessionBusy counts it, but it will queue behind the summary as behind
// a turn, and the summary at the end of a turn must not fail as busy.
func TestSummarizeIsNotBlockedByAPromptInItsHooks(t *testing.T) {
	t.Parallel()

	r := promptHookAgent(t, promptHooks(`sleep 5`))
	done := make(chan error, 1)
	go func() {
		_, err := r.agent.Run(context.Background(), SessionAgentCall{SessionID: r.sessionID, Prompt: "next prompt"})
		done <- err
	}()
	require.Eventually(t, func() bool { return r.agent.isSubmitting(r.sessionID) }, 3*time.Second, 10*time.Millisecond)

	err := r.agent.Summarize(t.Context(), r.sessionID, "", nil, nil, preCompactTriggerAuto)
	require.NotErrorIs(t, err, ErrSessionBusy)

	r.agent.Cancel(r.sessionID)
	<-done
}
