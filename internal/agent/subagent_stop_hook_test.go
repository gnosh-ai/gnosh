package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// subagentStopAgent builds a sessionAgent for a sub-agent (IsSubAgent,
// with a session service) whose only configured feature is the given
// SubagentStop hook commands, through the config-load path so a hook
// ValidateHooks would reject can't reach a test.
func subagentStopAgent(t *testing.T, env fakeEnv, commands ...string) *sessionAgent {
	t.Helper()
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd}
	}
	cfg := &config.Config{}
	if len(commands) > 0 {
		cfg.Hooks = map[string][]config.HookConfig{hooks.EventSubagentStop: hookCfgs}
	}
	require.NoError(t, cfg.ValidateHooks())
	return NewSessionAgent(SessionAgentOptions{
		IsSubAgent: true,
		AgentType:  "task",
		Sessions:   env.sessions,
		Messages:   env.messages,
		Cfg:        config.NewTestStore(cfg),
		Notify:     &recordingNotifier{},
	}).(*sessionAgent)
}

// A sub-agent's own gate is the SubagentStop event: a hook that refuses
// its stop sends it back to work, with its reason as the next
// instruction.
func TestSubagentStopContinuation_BlockQueuesAFollowUp(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := subagentStopAgent(t, env, `echo "the tests still fail" >&2; exit 2`)
	call := SessionAgentCall{
		SessionID:   "s1",
		RunID:       "run-7",
		Prompt:      "fix the build",
		Attachments: []message.Attachment{{FilePath: "/tmp/x.png"}},
	}

	next, blocked := a.stopHookContinuation(t.Context(), call, "all finished")
	require.True(t, blocked, "exit 2 from a SubagentStop hook must keep the sub-agent working")
	require.Contains(t, next.Prompt, "the tests still fail",
		"the hook's reason is what tells the model why it isn't done")
	require.Contains(t, next.Prompt, "SubagentStop",
		"the prompt must name the event that blocked it")
	require.Equal(t, "run-7", next.RunID, "the turn is not over; the continuation keeps its RunID")
	require.Equal(t, "s1", next.SessionID)
	require.Equal(t, 1, next.stopHookDepth)
	require.True(t, next.HiddenUserMessage)
	require.Nil(t, next.Attachments)
}

// No SubagentStop hooks, no gate: the sub-agent finishes untouched, and
// Stop hooks do not stand in for it.
func TestSubagentStopContinuation_NoHooksConfigured(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	stopMarker := filepath.Join(t.TempDir(), "stop-ran")
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			hooks.EventStop: {{Command: "touch " + stopMarker + "; exit 2"}},
		},
	}
	require.NoError(t, cfg.ValidateHooks())
	a := NewSessionAgent(SessionAgentOptions{
		IsSubAgent: true,
		Sessions:   env.sessions,
		Messages:   env.messages,
		Cfg:        config.NewTestStore(cfg),
	}).(*sessionAgent)

	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.False(t, blocked)
	_, err := os.Stat(stopMarker)
	require.ErrorIs(t, err, os.ErrNotExist, "a sub-agent must not run Stop hooks")
}

// The main agent's gate is the Stop event: a SubagentStop hook does not
// stand in for it.
func TestSubagentStopContinuation_MainAgentSkips(t *testing.T) {
	t.Parallel()

	subMarker := filepath.Join(t.TempDir(), "subagent-ran")
	a := stopHookAgentOpts(t, SessionAgentOptions{}, "unused")
	a.cfg = config.NewTestStore(&config.Config{
		Hooks: map[string][]config.HookConfig{
			hooks.EventSubagentStop: {{Command: "touch " + subMarker + "; exit 2"}},
		},
	})
	require.NoError(t, a.cfg.Config().ValidateHooks())

	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.False(t, blocked)
	_, err := os.Stat(subMarker)
	require.ErrorIs(t, err, os.ErrNotExist, "the main agent must not run SubagentStop hooks")
}

// The payload is the hook's whole view of the sub-agent: assert its
// shape, not just the decision it produced.
func TestSubagentStopContinuation_PayloadFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "payload.json")
	env := testEnv(t)
	a := subagentStopAgent(t, env, "cat > "+out)

	// The sub-agent's session exists, with the parent that started it.
	parent, err := env.sessions.Create(t.Context(), "parent")
	require.NoError(t, err)
	sub, err := env.sessions.CreateTaskSession(t.Context(), "tc-1", parent.ID, "sub")
	require.NoError(t, err)

	_, blocked := a.stopHookContinuation(t.Context(),
		SessionAgentCall{SessionID: sub.ID}, "I rewrote the parser.")
	require.False(t, blocked)

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"hook_event_name":"SubagentStop"`,
		"the hook must be able to tell the event it is")
	require.Contains(t, string(raw), `"event":"SubagentStop"`)
	require.Contains(t, string(raw), `"session_id":"`+parent.ID+`"`,
		"session_id is the session the sub-agent works for, as in Claude Code")
	require.Contains(t, string(raw), `"agent_id":"`+sub.ID+`"`, "and agent_id the sub-agent's own")
	require.Contains(t, string(raw), `"agent_type":"task"`)
	require.Contains(t, string(raw), "I rewrote the parser.")
	require.NotContains(t, string(raw), `"stop_hook_active"`,
		"the first pass is not a re-entry, so the flag is omitted")
}

// A hook that wants to object once and then stand down needs to know it
// is only running because it blocked the sub-agent itself.
func TestSubagentStopContinuation_PassesStopHookActive(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "active.txt")
	env := testEnv(t)
	a := subagentStopAgent(t, env, `printf '%s' "$GNOSH_STOP_HOOK_ACTIVE" > `+out+`; exit 2`)

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

// The sub-agent's hooks get the same persistence as the main agent's
// Stop hooks: no cap, and a reflection checkpoint every
// stopHookReflectEvery-th objection, where the agent may overrule the
// hook.
func TestSubagentStopContinuation_ReflectsAtEveryCheckpoint(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := subagentStopAgent(t, env, `echo "3 tests failing" >&2; exit 2`)
	call := SessionAgentCall{SessionID: "s1"}
	var reflected []int
	for range 2 * stopHookReflectEvery {
		next, blocked := a.stopHookContinuation(t.Context(), call, "still working on it")
		require.True(t, blocked, "an agent that reflects and keeps going must not be stopped")
		if next.stopHookReflecting {
			reflected = append(reflected, next.stopHookDepth)
			require.Contains(t, next.Prompt, "step back")
			require.Contains(t, next.Prompt, StopMarker)
		}
		call = next
	}
	require.Equal(t, []int{stopHookReflectEvery, 2 * stopHookReflectEvery}, reflected)

	// At a checkpoint the agent may judge it should stop, and the hook
	// is not consulted again.
	marker := filepath.Join(t.TempDir(), "ran")
	b := subagentStopAgent(t, env, "touch "+marker+`; echo "never done" >&2; exit 2`)
	reply := "The task needs a database this machine does not have.\n" + StopMarker
	_, blocked := b.stopHookContinuation(t.Context(),
		SessionAgentCall{SessionID: "s1", stopHookDepth: stopHookReflectEvery, stopHookReflecting: true}, reply)
	require.False(t, blocked, "the agent's judgement at a checkpoint ends the turn")
	_, err := os.Stat(marker)
	require.ErrorIs(t, err, os.ErrNotExist, "the hook must not be consulted once the agent has judged")
}

// A hook that blocks without explaining still has to produce a usable
// prompt, or the model is told to keep working with an empty reason.
func TestSubagentStopContinuation_BlockWithoutReason(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := subagentStopAgent(t, env, `exit 2`)
	next, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.True(t, blocked)
	require.NotEmpty(t, strings.TrimSpace(next.Prompt))
	require.Contains(t, next.Prompt, "SubagentStop")
}

func TestSubagentStopContinuation_NilConfigSkips(t *testing.T) {
	t.Parallel()

	a := NewSessionAgent(SessionAgentOptions{IsSubAgent: true}).(*sessionAgent)
	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.False(t, blocked, "no config store means no hooks to consult")
}

// subagentStopRun drives a whole turn through a sub-agent's
// sessionAgent.Run with the given SubagentStop hooks, against a model
// that always answers "I'm all done." Run drains its own queue, so by
// the time it returns every continuation the hooks forced has already
// run.
type subagentStopRun struct {
	agent     *sessionAgent
	sessionID string
	parentID  string
	notifier  *recordingNotifier
	// turns counts how many times the model was asked to answer: one
	// for the original prompt, plus one per continuation a
	// SubagentStop hook forced.
	turns *atomic.Int64
	// result is what Run returned, from which the agent tool takes the
	// answer it hands the parent (subAgentOutput).
	result *fantasy.AgentResult
}

func runSubagentTurn(t *testing.T, commands ...string) subagentStopRun {
	t.Helper()
	var turns atomic.Int64
	return runSubagentTurnModel(t, textOnlyModel("I'm all done.", &turns), &turns, commands...)
}

func runSubagentTurnModel(t *testing.T, llm fantasy.LanguageModel, turns *atomic.Int64, commands ...string) subagentStopRun {
	t.Helper()

	env := testEnv(t)
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd}
	}
	cfg := &config.Config{}
	if len(commands) > 0 {
		cfg.Hooks = map[string][]config.HookConfig{hooks.EventSubagentStop: hookCfgs}
	}
	require.NoError(t, cfg.ValidateHooks())

	notifier := &recordingNotifier{}
	model := Model{
		Model:      llm,
		CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000},
	}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsSubAgent:           true,
		AgentType:            "task",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  config.NewTestStore(cfg),
		Notify:               notifier,
	}).(*sessionAgent)

	parent, err := env.sessions.Create(t.Context(), "parent session")
	require.NoError(t, err)
	sub, err := env.sessions.CreateTaskSession(t.Context(), "tc-1", parent.ID, "sub-agent session")
	require.NoError(t, err)

	// Seed a user message so Run skips title generation, like
	// runTurnWithStopHookModel.
	_, err = env.messages.Create(t.Context(), sub.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)

	result, err := a.Run(t.Context(), SessionAgentCall{
		SessionID: sub.ID,
		RunID:     "run-1",
		Prompt:    "do the thing",
	})
	require.NoError(t, err)
	return subagentStopRun{agent: a, sessionID: sub.ID, parentID: parent.ID, notifier: notifier, turns: turns, result: result}
}

func requireSubagentQueueEmpty(t *testing.T, r subagentStopRun) {
	t.Helper()
	queued, ok := r.agent.messageQueue.Get(r.sessionID)
	require.False(t, ok && len(queued) > 0,
		"the turn must not leave work stranded on the queue")
}

// The feature, end to end: a hook that objects once sends the sub-agent
// back to work exactly once, and the answer it gives after that is the
// one the agent tool hands to the parent.
func TestSubagentStopHookBlockOnceCostsOneMoreTurn(t *testing.T) {
	r := runSubagentTurn(t,
		`[ "$GNOSH_STOP_HOOK_ACTIVE" = "1" ] && exit 0; echo "you forgot the tests" >&2; exit 2`)

	require.Equal(t, int64(2), r.turns.Load(),
		"the sub-agent must answer again after the block, then be allowed to finish")
	requireSubagentQueueEmpty(t, r)
	require.Empty(t, r.notifier.warnings(),
		"the hook was satisfied, not overruled, so there is nothing to warn about")

	// The agent tool returns the sub-agent's last assistant text, so the
	// post-block answer is what the parent reads.
	result, err := r.agent.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)
	var last string
	for _, m := range result {
		if m.Role == message.Assistant {
			last = m.Content().String()
		}
	}
	require.Contains(t, last, "I'm all done.")

	// The reason reached the model as a prompt, hidden from the
	// transcript the user reads.
	var found bool
	for _, m := range result {
		if strings.Contains(m.Content().String(), "you forgot the tests") {
			found = true
		}
	}
	require.True(t, found, "the hook's reason must reach the model as the continuation prompt")
}

// What the agent tool hands the parent is the sub-agent's answer after
// its SubagentStop hooks let it finish, not the one a hook turned back.
func TestSubagentStopParentGetsTheAnswerAfterTheBlock(t *testing.T) {
	var turns atomic.Int64
	llm := &promptAwareModel{
		turns: &turns,
		answer: func(lastUser string) string {
			if strings.Contains(lastUser, "you forgot the tests") {
				return "Tests added; all done."
			}
			return "All done."
		},
	}
	r := runSubagentTurnModel(t, llm, &turns,
		`[ "$GNOSH_STOP_HOOK_ACTIVE" = "1" ] && exit 0; echo "you forgot the tests" >&2; exit 2`)

	require.Equal(t, int64(2), turns.Load())
	require.Equal(t, "Tests added; all done.", subAgentOutput(r.result))
}

// The hook's whole view of the sub-agent, as the real flow builds it:
// hook_event_name SubagentStop, session_id the session it works for,
// agent_id its own session and agent_type its type (as Claude Code names
// them), stop_hook_active (false on the first pass) and the
// last_assistant_message it is judging.
func TestSubagentStopRunPayloadFields(t *testing.T) {
	out := filepath.Join(t.TempDir(), "payload.json")
	r := runSubagentTurn(t, "cat > "+out+"; exit 0")
	requireSubagentQueueEmpty(t, r)

	raw := readFileForTest(t, out)
	require.Contains(t, raw, `"hook_event_name":"SubagentStop"`)
	require.Contains(t, raw, `"event":"SubagentStop"`)
	require.Contains(t, raw, `"session_id":"`+r.parentID+`"`)
	require.Contains(t, raw, `"agent_id":"`+r.sessionID+`"`)
	require.Contains(t, raw, `"agent_type":"task"`)
	require.Contains(t, raw, "I'm all done.")
	require.NotContains(t, raw, `"stop_hook_active"`)
}

// No cap: a task that genuinely needs more attempts gets them, passing
// a reflection checkpoint on the way, exactly as the main agent's Stop
// hooks do.
func TestSubagentStopHookHasNoCap(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs")
	r := runSubagentTurn(t,
		`n=$(cat `+counter+` 2>/dev/null || echo 0); n=$((n+1)); echo $n > `+counter+`; `+
			`if [ "$n" -le 12 ]; then echo "attempt $n: not done" >&2; exit 2; fi`)

	require.Equal(t, int64(13), r.turns.Load(),
		"the original turn plus one per objection, with nothing cutting it short")
	requireSubagentQueueEmpty(t, r)
	require.Empty(t, r.notifier.warnings())

	msgs, err := r.agent.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)
	var checkpoints int
	for _, m := range msgs {
		if m.Role == message.User && strings.Contains(m.Content().String(), StopMarker) {
			checkpoints++
		}
	}
	require.Equal(t, 1, checkpoints,
		"the agent is asked to reflect at the 10th objection, and only then")
}

// A SubagentStop hook that exits non-zero without a word, a test that
// came out false, has no opinion: the sub-agent finishes, with no
// warning.
func TestSubagentStopHookSilentExitLetsTheSubAgentFinish(t *testing.T) {
	r := runSubagentTurn(t, `[ -f /no/such/marker/for/subagent/tests ] && { echo "not done" >&2; exit 2; }`)

	require.Equal(t, int64(1), r.turns.Load(), "a test that came out false must not cost the sub-agent a turn")
	requireSubagentQueueEmpty(t, r)
	require.Empty(t, r.notifier.warnings())
}

// A SubagentStop hook that could not check (exit 3) keeps the sub-agent
// working, as exit 2 does: a check that did not run has not passed. Here
// it can check on its second try, and the user is told why the first
// held the sub-agent.
func TestSubagentStopHookCouldNotEvaluateKeepsTheSubAgentWorking(t *testing.T) {
	for code, want := range map[string]string{
		"3": "could not evaluate: ffprobe is not installed",
		"1": "could not evaluate: exit 1: ffprobe is not installed",
	} {
		ran := filepath.Join(t.TempDir(), "ran")
		r := runSubagentTurn(t,
			`if [ -f `+ran+` ]; then exit 0; fi; touch `+ran+`; echo "ffprobe is not installed" >&2; exit `+code)

		require.Equal(t, int64(2), r.turns.Load(),
			"exit %s: the hook that could not check holds the sub-agent for one more turn", code)
		requireSubagentQueueEmpty(t, r)
		warnings := r.notifier.warnings()
		require.Len(t, warnings, 1)
		require.Contains(t, warnings[0].Message, want)
		require.Contains(t, warnings[0].Message, "the turn goes on")
		require.Equal(t, r.parentID, warnings[0].SessionID,
			"in the session the user is in: gnosh run prints only that session's warnings")
	}
}

// A SubagentStop hook that halts (exit 49) ends the sub-agent's turn
// now, even over its own earlier block.
func TestSubagentStopHookHaltEndsTheTurn(t *testing.T) {
	r := runSubagentTurn(t,
		`[ "$GNOSH_STOP_HOOK_ACTIVE" = "1" ] && { echo "enough" >&2; exit 49; }; echo "keep going" >&2; exit 2`)

	require.Equal(t, int64(2), r.turns.Load(),
		"the block costs one turn; the halt ends the continuation")
	requireSubagentQueueEmpty(t, r)
	require.Empty(t, r.notifier.warnings())
}

// The other half of the checkpoint story, end to end: asked at a
// checkpoint, the sub-agent judges there is a genuine reason to stop.
// The turn ends there, the hook is not consulted again, and the user is
// told why.
func TestSubagentStopAgentJudgesItShouldStopAtACheckpoint(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs")
	var turns atomic.Int64
	llm := &promptAwareModel{
		turns: &turns,
		answer: func(lastUser string) string {
			if strings.Contains(lastUser, StopMarker) {
				return "These tests need a staging database this machine cannot reach.\n" + StopMarker
			}
			return "I'm all done."
		},
	}
	const giveUpAfter = 3 * stopHookReflectEvery
	r := runSubagentTurnModel(t, llm, &turns,
		`n=$(cat `+counter+` 2>/dev/null || echo 0); n=$((n+1)); echo $n > `+counter+`; `+
			`if [ "$n" -le `+strconv.Itoa(giveUpAfter)+` ]; then echo "never done" >&2; exit 2; fi`)

	require.Equal(t, int64(stopHookReflectEvery+1), r.turns.Load(),
		"the original turn, nine ordinary retries, then the checkpoint turn where the agent judged")
	requireSubagentQueueEmpty(t, r)

	raw, err := os.ReadFile(counter)
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(stopHookReflectEvery), strings.TrimSpace(string(raw)),
		"the hook is not consulted after the agent's judgement")

	warnings := r.notifier.warnings()
	require.Len(t, warnings, 1, "the user must be told their gate was overruled")
	require.Contains(t, warnings[0].Message, "SubagentStop")
	require.Contains(t, warnings[0].Message, "staging database")
	require.Contains(t, warnings[0].Message, "never done")
	require.Equal(t, r.parentID, warnings[0].SessionID)
}

// A Claude Code SubagentStop hook blocks with JSON, not an exit code:
// the same contract as Stop.
func TestSubagentStopClaudeCodeStyleHookKeepsTheSubAgentWorking(t *testing.T) {
	r := runSubagentTurn(t,
		`[ "$GNOSH_STOP_HOOK_ACTIVE" = "1" ] && exit 0; echo '{"decision":"block","reason":"run the tests first"}'`)

	require.Equal(t, int64(2), r.turns.Load(),
		"the JSON block must cost exactly one more turn, like exit 2 does")
	requireSubagentQueueEmpty(t, r)
	require.Empty(t, r.notifier.warnings())
}

// The sub-agent's turn ends on the hook's reason reaching it as a user
// message, and the reflection prompt on a checkpoint names the event.
func TestSubagentStopPromptsNameTheEvent(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := subagentStopAgent(t, env, `echo "nope" >&2; exit 2`)
	// The reflection prompt is sent on a continuation that reflects,
	// not on the call that already reflects: the previous answer is
	// what it judges.
	next, blocked := a.stopHookContinuation(t.Context(),
		SessionAgentCall{SessionID: "s1", stopHookDepth: stopHookReflectEvery - 1}, "done")
	require.True(t, blocked)
	require.True(t, next.stopHookReflecting)
	require.Contains(t, next.Prompt, "step back", "the reflection prompt asks the agent to judge")
	require.Contains(t, next.Prompt, "A SubagentStop hook has now refused")
	require.Contains(t, next.Prompt, StopMarker, "and tells it how to say it should stop")

	next, blocked = a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: "s1"}, "done")
	require.True(t, blocked)
	require.Contains(t, next.Prompt, "A SubagentStop hook blocked the end of this turn")
}

// The env vars a SubagentStop hook reads: the event name, the session the
// sub-agent works for, its own ID and type, and the active flag under
// its GNOSH_ and CRUSH_ names.
func TestSubagentStopHookEnvVars(t *testing.T) {
	t.Parallel()

	envDir := t.TempDir()
	envLine := filepath.Join(envDir, "env.txt")
	env := testEnv(t)
	a := subagentStopAgent(t, env, `printf '%s|%s|%s|%s|%s|%s' "$GNOSH_EVENT" "$GNOSH_SESSION_ID" "$GNOSH_AGENT_ID" "$GNOSH_AGENT_TYPE" "$GNOSH_STOP_HOOK_ACTIVE" "$CRUSH_STOP_HOOK_ACTIVE" > `+envLine+`; exit 0`)

	parent, err := env.sessions.Create(t.Context(), "parent")
	require.NoError(t, err)
	sub, err := env.sessions.CreateTaskSession(t.Context(), "tc", parent.ID, "sub")
	require.NoError(t, err)

	_, blocked := a.stopHookContinuation(t.Context(), SessionAgentCall{SessionID: sub.ID, stopHookDepth: 3}, "done")
	require.False(t, blocked)

	got, err := os.ReadFile(envLine)
	require.NoError(t, err)
	require.Equal(t, "SubagentStop|"+parent.ID+"|"+sub.ID+"|task|1|1", string(got))
}
