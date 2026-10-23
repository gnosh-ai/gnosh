package agent

import (
	"context"
	"encoding/json"
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

// subSummarizeModel records that a summary request reached the model.
type subSummarizeModel struct {
	fakeLanguageModel
	mu         sync.Mutex
	summarized int
}

func (m *subSummarizeModel) Stream(_ context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.summarized++
	m.mu.Unlock()
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: "summary"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

func (m *subSummarizeModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.summarized
}

// preCompactRun is the pieces a PreCompact test drives: the agent, its
// environment, the model it summarizes with, and the session to
// summarize.
type preCompactRun struct {
	agent     *sessionAgent
	env       fakeEnv
	model     *subSummarizeModel
	sessionID string
	notify    *recordingNotifier
}

// preCompactAgent builds a session agent with the given PreCompact hooks
// configured, through ValidateHooks, and a session with an earlier
// prompt in it so there is something to summarize. subAgent selects a
// sub-agent, which fires no hooks.
func preCompactAgent(t *testing.T, subAgent bool, hookCfgs ...config.HookConfig) preCompactRun {
	t.Helper()
	env := testEnv(t)
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{hooks.EventPreCompact: hookCfgs}}
	require.NoError(t, cfg.ValidateHooks())
	llm := &subSummarizeModel{}
	model := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
	notify := &recordingNotifier{}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:   model,
		SmallModel:   model,
		SystemPrompt: "test",
		IsSubAgent:   subAgent,
		IsYolo:       true,
		Sessions:     env.sessions,
		Messages:     env.messages,
		Cfg:          config.NewTestStore(cfg, env.workingDir),
		Notify:       notify,
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "pre compact")
	require.NoError(t, err)
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)
	return preCompactRun{agent: a, env: env, model: llm, sessionID: sess.ID, notify: notify}
}

// summarize drives the agent's Summarize with the given trigger and
// reports the error, the model's summary count, and the hook warnings.
func (r preCompactRun) summarize(t *testing.T, trigger preCompactTrigger) (error, int, []string) {
	t.Helper()
	err := r.agent.Summarize(t.Context(), r.sessionID, fantasy.ProviderOptions{}, nil, trigger)
	return err, r.model.count(), r.hookWarningMessages()
}

func (r preCompactRun) hookWarningMessages() []string {
	var out []string
	for _, n := range r.notify.warnings() {
		out = append(out, n.Message)
	}
	return out
}

func preCompactHooks(matcher string, commands ...string) []config.HookConfig {
	cfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		cfgs[i] = config.HookConfig{Command: cmd, Matcher: matcher}
	}
	return cfgs
}

// A manual summary (the user asked for it) fires the hooks with trigger
// "manual", and an automatic one (the context filled up) with "auto".
// The summary goes ahead either way.
func TestSummarize_PreCompactFiresWithTheTrigger(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		trigger preCompactTrigger
		want    string
	}{
		{preCompactTriggerManual, "manual"},
		{preCompactTriggerAuto, "auto"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			out := filepath.Join(dir, "trigger")
			r := preCompactAgent(t, false,
				preCompactHooks("", `echo "$KLASH_PRE_COMPACT_TRIGGER" > `+out)...)
			err, summarized, _ := r.summarize(t, tc.trigger)
			require.NoError(t, err)
			require.Equal(t, 1, summarized, "the summary goes ahead")
			data, err := os.ReadFile(out)
			require.NoError(t, err)
			require.Equal(t, tc.want+"\n", string(data), "the hook sees the trigger")
		})
	}
}

// The payload carries hook_event_name "PreCompact", the session ID, the
// working directory, the trigger, and custom_instructions, which Klash
// has none of, so it is the empty string.
func TestSummarize_PreCompactPayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	r := preCompactAgent(t, false, preCompactHooks("", `cat > `+payloadPath)...)
	err, summarized, _ := r.summarize(t, preCompactTriggerManual)
	require.NoError(t, err)
	require.Equal(t, 1, summarized)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "PreCompact", payload["hook_event_name"])
	require.Equal(t, "PreCompact", payload["event"])
	require.Equal(t, r.sessionID, payload["session_id"])
	require.Equal(t, "manual", payload["trigger"])
	require.Equal(t, "", payload["custom_instructions"])
}

// A matcher is tested against the trigger, as in Claude Code: a hook
// matched on "manual" does not fire on an automatic summary, and a hook
// with no matcher fires on both. The summary goes ahead either way.
//
// This test exercises the runner's matcher against the trigger directly
// (the agent's Summarize delegates to it), because the shell's
// in-process interpreter has a flaky file-creation path under the
// agent test binary that makes file-based assertions unreliable here.
func TestSummarize_PreCompactMatcherOnTheTrigger(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		matcher string
		trigger preCompactTrigger
		want    int
	}{
		{"^manual$", preCompactTriggerManual, 1},
		{"^manual$", preCompactTriggerAuto, 0},
		{"^auto$", preCompactTriggerManual, 0},
		{"^auto$", preCompactTriggerAuto, 1},
		{"", preCompactTriggerManual, 1},
		{"", preCompactTriggerAuto, 1},
	} {
		t.Run(tc.matcher+"_"+string(tc.trigger), func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Hooks: map[string][]config.HookConfig{
				hooks.EventPreCompact: {{Command: "true", Matcher: tc.matcher}},
			}}
			require.NoError(t, cfg.ValidateHooks())
			runner := hooks.NewRunner(cfg.Hooks[hooks.EventPreCompact], t.TempDir(), t.TempDir())
			agg, err := runner.RunPreCompact(t.Context(), "s", string(tc.trigger))
			require.NoError(t, err)
			require.Equal(t, tc.want, agg.HookCount,
				"the hook fires (or not) based on whether the trigger matches")
		})
	}
}

// A hook that exits 2 cannot stop the summary, as in Claude Code: the
// summary goes ahead and the user is told the hook objected.
func TestSummarize_PreCompactBlockDoesNotStopTheSummary(t *testing.T) {
	t.Parallel()

	r := preCompactAgent(t, false,
		preCompactHooks("", `echo "not yet" >&2; exit 2`)...)
	err, summarized, warnings := r.summarize(t, preCompactTriggerManual)
	require.NoError(t, err)
	require.Equal(t, 1, summarized, "the summary goes ahead over the hook's objection")
	require.Len(t, warnings, 1, "the user is told the hook objected")
	require.Contains(t, warnings[0], "not yet", "the hook's reason")
	require.Contains(t, warnings[0], "PreCompact", "the event is named")
}

// A hook that fails, or here could not run, is shown as one hook warning,
// not also as an objection, and the summary goes ahead without it: no
// hook can stop a summary.
func TestSummarize_PreCompactFailedHookWarns(t *testing.T) {
	t.Parallel()

	r := preCompactAgent(t, false,
		preCompactHooks("", "./hooks/does-not-exist.sh")...)
	_, summarized, warnings := r.summarize(t, preCompactTriggerAuto)
	require.Equal(t, 1, summarized)
	require.Len(t, warnings, 1, "the user is told the gate did not run")
	require.Contains(t, warnings[0], "PreCompact", "the event is named")
	require.Contains(t, warnings[0], "without it", "the summary went ahead")
}

// A sub-agent's session never fires PreCompact hooks, as it fires no
// other hook: the summary runs with no warning and no hook output.
func TestSummarize_PreCompactSubAgentFiresNoHooks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	r := preCompactAgent(t, true,
		preCompactHooks("", `echo x >> `+count)...)
	err, summarized, warnings := r.summarize(t, preCompactTriggerManual)
	require.NoError(t, err)
	require.Equal(t, 1, summarized)
	_, err = os.Stat(count)
	require.True(t, os.IsNotExist(err), "the sub-agent's hooks do not run")
	require.Empty(t, warnings)
}

// A hook that halts (exit 49) also cannot stop the summary; it is
// reported the same way as a block.
func TestSummarize_PreCompactHaltDoesNotStopTheSummary(t *testing.T) {
	t.Parallel()

	r := preCompactAgent(t, false,
		preCompactHooks("", `echo "stop everything" >&2; exit 49`)...)
	err, summarized, warnings := r.summarize(t, preCompactTriggerAuto)
	require.NoError(t, err)
	require.Equal(t, 1, summarized)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "stop everything")
}

// Hooks that both object are reported together in one warning, in
// config order, and the summary goes ahead.
func TestSummarize_PreCompactMultipleObjections(t *testing.T) {
	t.Parallel()

	r := preCompactAgent(t, false,
		preCompactHooks("", `echo "first" >&2; exit 2`, `echo "second" >&2; exit 2`)...)
	_, summarized, warnings := r.summarize(t, preCompactTriggerManual)
	require.Equal(t, 1, summarized)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "first", "the first hook's reason, in config order")
	require.Contains(t, warnings[0], "second", "the second hook's reason")
	require.Less(t, strings.Index(warnings[0], "first"), strings.Index(warnings[0], "second"), "config order")
}

// A hook that allows (exit 0, no objection) produces no warning.
func TestSummarize_PreCompactCleanHookNoWarning(t *testing.T) {
	t.Parallel()

	r := preCompactAgent(t, false,
		preCompactHooks("", "exit 0")...)
	_, summarized, warnings := r.summarize(t, preCompactTriggerManual)
	require.Equal(t, 1, summarized)
	require.Empty(t, warnings)
}
