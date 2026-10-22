package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// stopHookAgent builds a sessionAgent whose only configured feature is
// the given Stop hook commands, through the config-load path so a hook
// ValidateHooks would reject can't reach a test.
func stopHookAgent(t *testing.T, commands ...string) *sessionAgent {
	t.Helper()
	return stopHookAgentOpts(t, SessionAgentOptions{}, commands...)
}

func stopHookAgentOpts(t *testing.T, opts SessionAgentOptions, commands ...string) *sessionAgent {
	t.Helper()
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd}
	}
	cfg := &config.Config{}
	if len(commands) > 0 {
		cfg.Hooks = map[string][]config.HookConfig{hooks.EventStop: hookCfgs}
	}
	require.NoError(t, cfg.ValidateHooks())
	opts.Cfg = config.NewTestStore(cfg)
	return NewSessionAgent(opts).(*sessionAgent)
}

func TestStopHookContinuation_NoHooksConfigured(t *testing.T) {
	t.Parallel()

	a := stopHookAgent(t)
	_, blocked := a.stopHookContinuation(t.Context(),
		SessionAgentCall{SessionID: "s1", Prompt: "do the thing"}, "done")
	require.False(t, blocked, "a session with no Stop hooks must end its turn untouched")
}

// The whole point of the event: a hook that refuses the stop sends the
// agent back to work instead of letting the turn finish.
func TestStopHookContinuation_BlockQueuesAFollowUp(t *testing.T) {
	t.Parallel()

	a := stopHookAgent(t, `echo "the tests still fail" >&2; exit 2`)
	call := SessionAgentCall{
		SessionID:   "s1",
		RunID:       "run-7",
		Channel:     "slack",
		Prompt:      "fix the build",
		Attachments: []message.Attachment{{FilePath: "/tmp/x.png"}},
	}

	next, blocked := a.stopHookContinuation(t.Context(), call, "all finished")
	require.True(t, blocked, "exit 2 from a Stop hook must keep the turn alive")
	require.Contains(t, next.Prompt, "the tests still fail",
		"the hook's reason is what tells the model why it isn't done")
	require.Equal(t, "run-7", next.RunID,
		"the turn is not over, so the continuation keeps its RunID and the caller still gets one terminal event")
	require.Equal(t, "s1", next.SessionID)
	require.Equal(t, "slack", next.Channel, "the continuation must still know where to reply")
	require.Equal(t, 1, next.stopHookDepth)
	require.True(t, next.HiddenUserMessage,
		"a generated continuation should not read as something the user typed")
	require.Nil(t, next.Attachments,
		"attachments were delivered on the turn that just ran; resending would duplicate them")
}

func TestStopHookContinuation_AllowLetsTheTurnEnd(t *testing.T) {
	t.Parallel()

	a := stopHookAgent(t, `exit 0`)
	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.False(t, blocked)
}

// Exit 49 is the explicit "end now", so it must win over a sibling
// hook's block — otherwise a hook could never call off a loop it started.
func TestStopHookContinuation_HaltBeatsBlock(t *testing.T) {
	t.Parallel()

	a := stopHookAgent(t,
		`echo "keep going" >&2; exit 2`,
		`echo "no, stop" >&2; exit 49`,
	)
	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.False(t, blocked, "a halt must end the turn even when another hook blocked it")
}

// There is no cap: while the hook objects, the agent keeps working,
// however many attempts that takes. Ending on a counter would throw away
// exactly the persistence a Stop hook exists to add.
func TestStopHookContinuation_NoCap(t *testing.T) {
	t.Parallel()

	a := stopHookAgent(t, `echo "not yet" >&2; exit 2`)
	for _, depth := range []int{9, 10, 11, 25, 99, 1000} {
		next, blocked := a.stopHookContinuation(t.Context(),
			SessionAgentCall{SessionID: "s1", stopHookDepth: depth}, "done")
		require.True(t, blocked, "after %d objections the hook still objects, so the agent keeps going", depth)
		require.Equal(t, depth+1, next.stopHookDepth)
	}
}

// The counter has to survive the trip through the queue, or the
// reflection checkpoints would never come round.
func TestStopHookContinuation_DepthAccumulates(t *testing.T) {
	t.Parallel()

	a := stopHookAgent(t, `echo "not yet" >&2; exit 2`)
	call := SessionAgentCall{SessionID: "s1", Prompt: "original"}
	for want := 1; want <= 3; want++ {
		next, blocked := a.stopHookContinuation(t.Context(), call, "done")
		require.True(t, blocked)
		require.Equal(t, want, next.stopHookDepth)
		call = next
	}
}

// Every stopHookReflectEvery-th objection the agent is asked to judge
// whether there is a genuine reason to stop; in between it just keeps
// working. An agent that reflects and carries on (no marker) is sent
// round again, and reaches the next checkpoint in turn.
func TestStopHookContinuation_ReflectsAtEveryCheckpoint(t *testing.T) {
	t.Parallel()

	a := stopHookAgent(t, `echo "3 tests failing" >&2; exit 2`)
	call := SessionAgentCall{SessionID: "s1"}
	var reflected []int
	for range 3 * stopHookReflectEvery {
		next, blocked := a.stopHookContinuation(t.Context(), call, "still working on it")
		require.True(t, blocked, "an agent that reflects and keeps going must not be stopped")
		if next.stopHookReflecting {
			reflected = append(reflected, next.stopHookDepth)
			require.Contains(t, next.Prompt, "step back")
			require.Contains(t, next.Prompt, StopMarker, "the agent must be told how to say it should stop")
			require.Contains(t, next.Prompt, "3 tests failing", "and what it is weighing")
			require.Contains(t, next.Prompt, "do not stop", "and that the default is to keep going")
		} else {
			require.NotContains(t, next.Prompt, StopMarker,
				"an ordinary continuation must not invite the agent to stop")
		}
		call = next
	}
	require.Equal(t, []int{10, 20, 30}, reflected)
}

// At a checkpoint, an agent that judges it should stop ends the turn —
// and the hook is not consulted again, since it would only repeat the
// objection the agent has just weighed.
func TestStopHookContinuation_AgentCanStopAtACheckpoint(t *testing.T) {
	t.Parallel()

	ran := filepath.Join(t.TempDir(), "ran")
	a := stopHookAgent(t, "touch "+ran+`; echo "3 tests failing" >&2; exit 2`)

	call := SessionAgentCall{SessionID: "s1", stopHookDepth: 10, stopHookReflecting: true}
	reply := "The suite needs a database this machine does not have.\n" + StopMarker
	_, blocked := a.stopHookContinuation(t.Context(), call, reply)
	require.False(t, blocked)

	_, err := os.Stat(ran)
	require.ErrorIs(t, err, os.ErrNotExist, "the hook must not be consulted once the agent has judged")
}

// Outside a checkpoint the hook stays in charge: the marker is ignored.
func TestStopHookContinuation_MarkerOutsideACheckpointIsIgnored(t *testing.T) {
	t.Parallel()

	a := stopHookAgent(t, `echo "not yet" >&2; exit 2`)
	_, blocked := a.stopHookContinuation(t.Context(),
		SessionAgentCall{SessionID: "s1", stopHookDepth: 4}, "I'd like to stop.\n"+StopMarker)
	require.True(t, blocked, "the agent may only overrule the hook when it was asked to judge")
}

func TestStopMarkerPresent(t *testing.T) {
	t.Parallel()

	require.True(t, stopMarkerPresent("reason\n"+StopMarker))
	require.True(t, stopMarkerPresent("reason\n  "+StopMarker+"  \n"), "surrounding whitespace is fine")
	require.True(t, stopMarkerPresent("reason\n`"+StopMarker+"`"), "some models wrap it in backticks")
	require.False(t, stopMarkerPresent("I could write "+StopMarker+" but I won't"),
		"a mention in prose is not a decision")
	require.False(t, stopMarkerPresent("no marker here"))
	require.False(t, stopMarkerPresent(""))
}

func TestStripStopMarker(t *testing.T) {
	t.Parallel()

	require.Equal(t, "The reason.", StripStopMarker("The reason.\n"+StopMarker))
	require.Equal(t, "The reason.", StripStopMarker("The reason.\n`"+StopMarker+"`\n"))
	require.Equal(t, "untouched", StripStopMarker("untouched"))
	prose := "mentions " + StopMarker + " inline"
	require.Equal(t, prose, StripStopMarker(prose), "only a marker line is removed")
}

// A hook that wants to object once and then stand down needs to know it
// is only running because it blocked the turn itself.
func TestStopHookContinuation_PassesStopHookActive(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "active.txt")
	a := stopHookAgent(t, `printf '%s' "$CRUSH_STOP_HOOK_ACTIVE" > `+out+`; exit 2`)

	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.True(t, blocked)
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "0", string(got), "the first pass is not a re-entry")

	_, blocked = a.stopHookContinuation(t.Context(),
		SessionAgentCall{SessionID: "s1", stopHookDepth: 2}, "done")
	require.True(t, blocked)
	got, err = os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "1", string(got), "a continuation must announce itself as one")
}

// The hook judges the answer the turn is about to end on.
func TestStopHookContinuation_PassesLastAssistantMessage(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "payload.json")
	a := stopHookAgent(t, "cat > "+out+"; exit 0")

	_, blocked := a.stopHookContinuation(t.Context(),
		SessionAgentCall{SessionID: "s1"}, "I rewrote the parser.")
	require.False(t, blocked)

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Contains(t, string(raw), "I rewrote the parser.")
}

// Sub-agents never fire hooks, matching wrapToolsWithHooks: the
// top-level turn is where the gate belongs.
func TestStopHookContinuation_SubAgentSkips(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "ran")
	a := stopHookAgentOpts(t, SessionAgentOptions{IsSubAgent: true},
		"touch "+marker+"; exit 2")

	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.False(t, blocked)
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "a sub-agent must not run Stop hooks")
}

// A hook that blocks without explaining still has to produce a usable
// prompt, or the model is told to keep working with an empty reason.
func TestStopHookContinuation_BlockWithoutReason(t *testing.T) {
	t.Parallel()

	a := stopHookAgent(t, `exit 2`)
	next, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.True(t, blocked)
	require.NotEmpty(t, strings.TrimSpace(next.Prompt))
	require.Contains(t, next.Prompt, "Stop hook")
}

// A hook Crush can't launch must not read as approval to stop.
func TestStopHookContinuation_NilConfigSkips(t *testing.T) {
	t.Parallel()

	a := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.False(t, blocked, "no config store means no hooks to consult")
}

// readFileForTest reads a file a hook wrote and fails the test if it
// isn't there, which usually means the hook never ran.
func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "expected the hook to have written %s", path)
	return string(raw)
}
