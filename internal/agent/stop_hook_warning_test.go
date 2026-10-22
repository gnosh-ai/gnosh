package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// recordingNotifier captures what the agent publishes.
type recordingNotifier struct {
	mu   sync.Mutex
	sent []notify.Notification
}

func (r *recordingNotifier) Publish(_ pubsub.EventType, n notify.Notification) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, n)
}

func (r *recordingNotifier) PublishMustDeliver(_ context.Context, t pubsub.EventType, n notify.Notification) {
	r.Publish(t, n)
}

func (r *recordingNotifier) warnings() []notify.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []notify.Notification
	for _, n := range r.sent {
		if n.Type == notify.TypeHookWarning {
			out = append(out, n)
		}
	}
	return out
}

func stopHookAgentWithNotifier(t *testing.T, hookCfgs ...config.HookConfig) (*sessionAgent, *recordingNotifier) {
	t.Helper()
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{hooks.EventStop: hookCfgs}}
	require.NoError(t, cfg.ValidateHooks())
	n := &recordingNotifier{}
	a := NewSessionAgent(SessionAgentOptions{
		Cfg:    config.NewTestStore(cfg),
		Notify: n,
	}).(*sessionAgent)
	return a, n
}

// A Stop hook has no line on screen of its own. When one fails to run —
// here, a "tests must pass" gate pointed at a script that is not there —
// the turn ends as if it had passed. The warning is the only sign.
func TestStopHook_FailedHookWarnsTheUser(t *testing.T) {
	t.Parallel()

	a, n := stopHookAgentWithNotifier(t,
		config.HookConfig{Name: "tests-must-pass", Command: "./hooks/tests-must-pass.sh"})

	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.False(t, blocked, "a hook that never ran cannot hold the turn open; it is non-blocking")

	warnings := n.warnings()
	require.Len(t, warnings, 1, "the user must be told the gate did not run")
	require.Equal(t, "s1", warnings[0].SessionID)
	require.Contains(t, warnings[0].Message, "tests-must-pass", "name the hook")
	require.Contains(t, warnings[0].Message, "no such file or directory", "say why")
	require.Contains(t, warnings[0].Message, "without its check", "say what it cost")
}

func TestStopHook_CleanHookDoesNotWarn(t *testing.T) {
	t.Parallel()

	for _, cmd := range []string{"exit 0", `echo "not yet" >&2; exit 2`, `exit 49`} {
		a, n := stopHookAgentWithNotifier(t, config.HookConfig{Command: cmd})
		a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
		require.Empty(t, n.warnings(), "%q reached a verdict, so there is nothing to warn about", cmd)
	}
}

// Each failed hook gets its own warning; a working one beside it is not
// blamed.
func TestStopHook_WarnsOncePerFailedHook(t *testing.T) {
	t.Parallel()

	a, n := stopHookAgentWithNotifier(t,
		config.HookConfig{Name: "ok", Command: "exit 0"},
		config.HookConfig{Name: "broken-a", Command: "exit 3"},
		config.HookConfig{Name: "broken-b", Command: "./missing.sh"},
	)
	a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")

	warnings := n.warnings()
	require.Len(t, warnings, 2)
	var joined string
	for _, w := range warnings {
		joined += w.Message + "\n"
	}
	require.Contains(t, joined, "broken-a")
	require.Contains(t, joined, "broken-b")
	require.NotContains(t, joined, `"ok"`)
}

// Agents built without a notifier (sub-agents, tests) must not panic.
func TestStopHook_NoNotifierIsFine(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Hooks: map[string][]config.HookConfig{
		hooks.EventStop: {{Command: "./missing.sh"}},
	}}
	a := NewSessionAgent(SessionAgentOptions{Cfg: config.NewTestStore(cfg)}).(*sessionAgent)
	require.NotPanics(t, func() {
		a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	})
}

// When the agent judges at a checkpoint that it should stop, it is ending
// the turn over the hook's objection. That has to be said out loud — the
// objections reached the model as hidden prompts — with the agent's
// reason and the hook's, or the user sees an ordinary finish for work
// their own gate said was incomplete.
func TestStopHook_AgentChoosingToStopWarns(t *testing.T) {
	t.Parallel()

	a, n := stopHookAgentWithNotifier(t, config.HookConfig{Command: `echo "3 tests failing" >&2; exit 2`})
	call := SessionAgentCall{
		SessionID:          "s1",
		stopHookDepth:      10,
		stopHookReason:     "3 tests failing",
		stopHookReflecting: true,
	}
	reply := "The failing tests need a staging database\nthat only you can provision.\n" + StopMarker
	_, blocked := a.stopHookContinuation(t.Context(), call, reply)
	require.False(t, blocked)

	warnings := n.warnings()
	require.Len(t, warnings, 1)
	msg := warnings[0].Message
	require.Contains(t, msg, `said "not done" 10 times`)
	require.Contains(t, msg, "the agent judged it should stop")
	require.Contains(t, msg, "staging database that only you can provision",
		"the agent's reason, flattened onto one line")
	require.NotContains(t, msg, StopMarker, "the sentinel is for the harness, not the reader")
	require.Contains(t, msg, "Hook's last objection: 3 tests failing")
}

// An ordinary block — and a checkpoint the agent answers by carrying on —
// is the agent doing its job, not an override; nothing to warn about.
func TestStopHook_KeepingGoingDoesNotWarn(t *testing.T) {
	t.Parallel()

	a, n := stopHookAgentWithNotifier(t, config.HookConfig{Command: `echo "not yet" >&2; exit 2`})
	_, blocked := a.stopHookContinuation(t.Context(),
		SessionAgentCall{SessionID: "s1", stopHookDepth: 9}, "done")
	require.True(t, blocked)

	_, blocked = a.stopHookContinuation(t.Context(),
		SessionAgentCall{SessionID: "s1", stopHookDepth: 10, stopHookReflecting: true}, "pressing on")
	require.True(t, blocked)
	require.Empty(t, n.warnings())
}

// The objection has to ride along with the continuation, or there is
// nothing to quote if the agent later judges it should stop.
func TestStopHook_ContinuationCarriesTheReason(t *testing.T) {
	t.Parallel()

	a, _ := stopHookAgentWithNotifier(t, config.HookConfig{Command: `echo "lint is red" >&2; exit 2`})
	next, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.True(t, blocked)
	require.Equal(t, "lint is red", next.stopHookReason)
}

// An agent that stops without explaining still produces a warning; it
// just has nothing of its own to quote.
func TestStopHook_AgentStopsWithoutReason(t *testing.T) {
	t.Parallel()

	a, n := stopHookAgentWithNotifier(t, config.HookConfig{Command: "exit 2"})
	a.stopHookContinuation(t.Context(),
		SessionAgentCall{SessionID: "s1", stopHookDepth: 10, stopHookReflecting: true}, StopMarker)

	warnings := n.warnings()
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0].Message, "without giving a reason")
	require.NotContains(t, warnings[0].Message, "Hook's last objection",
		"no objection was recorded, so none is quoted")
}

func TestCompactReason(t *testing.T) {
	t.Parallel()

	require.Equal(t, "a b c", compactReason("  a\n\n b\t c  "))
	long := strings.Repeat("é", maxStopReasonLen+20)
	got := compactReason(long)
	require.True(t, utf8.ValidString(got), "a cut must never split a character")
	require.True(t, strings.HasSuffix(got, "…"))
}

// A Stop hook answering with a word Crush does not know has failed to
// reach a verdict, and says so — the same as one that crashed.
func TestStopHook_UnknownDecisionWarns(t *testing.T) {
	t.Parallel()

	a, n := stopHookAgentWithNotifier(t,
		config.HookConfig{Name: "gate", Command: `echo '{"decision":"denied","reason":"not done"}'`})
	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.False(t, blocked, "an unrecognised answer is non-blocking, like any failed hook")

	warnings := n.warnings()
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0].Message, `unknown decision "denied"`)
}
