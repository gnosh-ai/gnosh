package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// judgeModel is a goal check that answers the verdicts given, one per
// check and the last over again, and counts the checks; during, when
// set, runs as each check is made.
type judgeModel struct {
	fakeLanguageModel
	verdicts []string
	checks   atomic.Int64
	during   func()
}

func (m *judgeModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	n := int(m.checks.Add(1))
	if m.during != nil {
		m.during()
	}
	answer := m.verdicts[min(n, len(m.verdicts))-1]
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: answer})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

// goalRun is a turn run with a goal set.
type goalRun struct {
	stopHookRun
	goals *csync.Map[string, Goal]
}

// runGoalTurn drives a whole turn through Run, with goal set for its
// session, against main, the agent's model, and judge, the small model
// that makes the goal check.
func runGoalTurn(t *testing.T, goal string, main fantasy.LanguageModel, turns *atomic.Int64, judge fantasy.LanguageModel) goalRun {
	t.Helper()
	return runGoalTurnWith(t, goal, main, turns, judge, nil)
}

// runGoalTurnWith is runGoalTurn telling before the goals and the session
// once the goal is set, before the turn runs.
func runGoalTurnWith(t *testing.T, goal string, main fantasy.LanguageModel, turns *atomic.Int64, judge fantasy.LanguageModel, before func(*csync.Map[string, Goal], string)) goalRun {
	t.Helper()

	env := testEnv(t)
	notifier := &recordingNotifier{}
	goals := csync.NewMap[string, Goal]()
	model := func(llm fantasy.LanguageModel) Model {
		return Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
	}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model(main),
		SmallModel:           model(judge),
		SystemPrompt:         "test",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  config.NewTestStore(&config.Config{}),
		Notify:               notifier,
		Goals:                goals,
	}).(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "goal turn")
	require.NoError(t, err)
	// A prompt before, so Run makes no title: that would ask the judge.
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)
	goals.Set(sess.ID, Goal{Condition: goal})
	if before != nil {
		before(goals, sess.ID)
	}

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, RunID: "run-1", Prompt: "do the thing"})
	require.NoError(t, err)
	return goalRun{stopHookRun{agent: a, sessionID: sess.ID, notifier: notifier, turns: turns}, goals}
}

// notices are the notifications of type kind the run sent.
func (r goalRun) notices(kind notify.Type) []string {
	r.notifier.mu.Lock()
	defer r.notifier.mu.Unlock()
	var out []string
	for _, n := range r.notifier.sent {
		if n.Type == kind {
			out = append(out, n.Message)
		}
	}
	return out
}

// The feature, end to end, as in Claude Code: a goal the check finds not
// met keeps the turn going, with what it found missing as a hidden
// prompt, until a check finds it met; then the goal is cleared, and the
// user told.
func TestGoalKeepsTheTurnGoingUntilMet(t *testing.T) {
	var turns atomic.Int64
	judge := &judgeModel{verdicts: []string{
		`{"ok": false, "reason": "the tests still fail"}`,
		`<think>they ran</think> Verdict: {"ok": true, "reason": "the test run passes"}`,
	}}
	r := runGoalTurn(t, "the tests pass", textOnlyModel("Working.", &turns), &turns, judge)

	require.Equal(t, int64(2), turns.Load(), "one more turn, for the check that found the goal not met")
	require.Equal(t, int64(2), judge.checks.Load())
	requireQueueEmpty(t, r.stopHookRun)
	_, set := r.goals.Get(r.sessionID)
	require.False(t, set, "a goal met is cleared")
	require.Equal(t, []string{"Goal met: the tests pass (the test run passes)"}, r.notices(notify.TypeGoalMet))
	require.Empty(t, r.notifier.warnings())

	msgs, err := r.agent.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)
	var prompt message.TextContent
	for _, msg := range msgs {
		if msg.Role == message.User && strings.Contains(msg.Content().Text, "not met yet") {
			prompt = msg.Content()
		}
	}
	require.Equal(t, goalContinuationPrompt("the tests pass", "the tests still fail"), prompt.Text)
	require.True(t, prompt.Hidden, "kept out of the transcript the user reads")
}

// No cap: every stopHookReflectEvery checks in a row the agent is asked
// whether there is a genuine reason to stop; when it judges there is, the
// turn ends there, the goal still set with what the checks found, and the
// user is told why.
func TestGoalReflectsEveryTenChecks(t *testing.T) {
	var turns atomic.Int64
	main := &promptAwareModel{turns: &turns, answer: func(last string) string {
		if strings.Contains(last, "step back") {
			return "I cannot: the test server is down.\n" + StopMarker
		}
		return "Working."
	}}
	judge := &judgeModel{verdicts: []string{`{"ok": false, "reason": "the tests still fail"}`}}
	r := runGoalTurn(t, "the tests pass", main, &turns, judge)

	require.Equal(t, int64(stopHookReflectEvery+1), turns.Load(), "the turn, a continuation per check, the last a checkpoint")
	require.Equal(t, int64(stopHookReflectEvery), judge.checks.Load(), "no check after the agent judged it should stop")
	goal, set := r.goals.Get(r.sessionID)
	require.True(t, set, "the goal stays set")
	require.Equal(t, Goal{Condition: "the tests pass", Checks: stopHookReflectEvery, LastCheck: "the tests still fail"}, goal)
	require.Len(t, r.notifier.warnings(), 1)
	require.Equal(t, "The goal was found not met 10 times in a row; the agent judged it should stop: I cannot: the test server is down. The goal stays set (/goal clear clears it)", r.notifier.warnings()[0].Message)
	require.Empty(t, r.notices(notify.TypeGoalMet))
}

// A check that answers no verdict ends the turn, the goal still set, and
// says why.
func TestGoalCheckWithNoVerdictEndsTheTurn(t *testing.T) {
	var turns atomic.Int64
	judge := &judgeModel{verdicts: []string{"maybe"}}
	r := runGoalTurn(t, "the tests pass", textOnlyModel("Working.", &turns), &turns, judge)

	require.Equal(t, int64(1), turns.Load())
	goal, set := r.goals.Get(r.sessionID)
	require.True(t, set)
	require.Zero(t, goal.Checks)
	require.Len(t, r.notifier.warnings(), 1)
	require.Equal(t, "The goal check could not be made, so the turn ended with the goal still set: its answer was not a verdict: maybe", r.notifier.warnings()[0].Message)
}

// A goal set anew while the old one was checked is left as set: the
// check's verdict was about the old one.
func TestGoalSetAnewWhileCheckedIsLeft(t *testing.T) {
	var turns atomic.Int64
	var goals *csync.Map[string, Goal]
	var sessionID string
	// Met the second time, so a check that ignored the goal set anew would
	// not keep the turn going for ever.
	judge := &judgeModel{verdicts: []string{`{"ok": false, "reason": "the tests still fail"}`, `{"ok": true, "reason": "the test run passes"}`}}
	// The check runs in the turn, once before has said where the goal is.
	judge.during = func() { goals.Set(sessionID, Goal{Condition: "the docs build"}) }
	r := runGoalTurnWith(t, "the tests pass", textOnlyModel("Working.", &turns), &turns, judge, func(g *csync.Map[string, Goal], id string) { goals, sessionID = g, id })

	require.Equal(t, int64(1), turns.Load())
	goal, set := r.goals.Get(r.sessionID)
	require.True(t, set)
	require.Equal(t, Goal{Condition: "the docs build"}, goal)
}

// The verdict is the JSON object in the check's answer, past any thinking
// and around any words; an answer without one, or without ok, is none.
func TestParseGoalCheck(t *testing.T) {
	t.Parallel()

	for answer, want := range map[string]bool{
		`{"ok": true, "reason": "passes"}`:                                              true,
		`{"ok":false,"reason":"passes"}`:                                                false,
		"<think>is it {\"ok\": false}?</think>\n{\"ok\": true, \"reason\": \"passes\"}": true,
		"Verdict:\n```json\n{\"ok\": true, \"reason\": \"passes\"}\n```":                true,
	} {
		met, reason, err := parseGoalCheck(answer)
		require.NoError(t, err, answer)
		require.Equal(t, want, met, answer)
		require.Equal(t, "passes", reason, answer)
	}
	for _, answer := range []string{"", "maybe", `{"reason": "no ok"}`, `{"ok": "yes"}`, `}{`} {
		_, _, err := parseGoalCheck(answer)
		require.Error(t, err, answer)
	}
}

// The check is shown the end of the conversation: the prompts typed (not
// the hidden ones), what the agent said and called, and what its tools
// returned, each cut short, and the whole cut to its end.
func TestGoalTranscript(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "make the tests pass"}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "A goal is now set", Hidden: true}}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "Running them."},
			message.ToolCall{ID: "c1", Name: "bash", Input: `{"command":"go test ./..."}`},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "c1", Name: "bash", Content: "ok  \tpkg\t0.1s"}}},
	}
	require.Equal(t, "User: make the tests pass\n\nAgent: Running them.\n\nAgent called bash: {\"command\":\"go test ./...\"}\n\nbash returned: ok  \tpkg\t0.1s", goalTranscript(msgs))

	long := []message.Message{{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{Name: "view", Content: strings.Repeat("x", 3_000)}}}}
	require.Equal(t, "view returned: "+strings.Repeat("x", 2_000)+" … (1000 more characters)", goalTranscript(long))
	var many []message.Message
	for range 20 {
		many = append(many, message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: strings.Repeat("y", 3_000)}}})
	}
	got := goalTranscript(many)
	require.Equal(t, goalTranscriptLimit+1, len([]rune(got)), "cut to its end")
	require.True(t, strings.HasPrefix(got, "…"))
	require.True(t, strings.HasSuffix(got, strings.Repeat("y", 100)))
}

// The coordinator keeps the sessions' goals: set, read and cleared.
func TestCoordinatorGoals(t *testing.T) {
	t.Parallel()

	c := &coordinator{goals: csync.NewMap[string, Goal]()}
	_, ok := c.Goal("s1")
	require.False(t, ok)
	c.SetGoal("s1", "the tests pass")
	c.SetGoal("s1", "the docs build")
	goal, ok := c.Goal("s1")
	require.True(t, ok)
	require.Equal(t, Goal{Condition: "the docs build"}, goal, "in place of the one before")
	cleared, ok := c.ClearGoal("s1")
	require.True(t, ok)
	require.Equal(t, "the docs build", cleared.Condition)
	_, ok = c.Goal("s1")
	require.False(t, ok)
}

// The stop marker counts only at a checkpoint, as with Stop hooks: one the
// agent writes elsewhere does not end a turn the goal check would go on.
func TestGoalIgnoresAStopMarkerOutsideACheckpoint(t *testing.T) {
	var turns atomic.Int64
	judge := &judgeModel{verdicts: []string{
		`{"ok": false, "reason": "the tests still fail"}`,
		`{"ok": true, "reason": "the test run passes"}`,
	}}
	r := runGoalTurn(t, "the tests pass", textOnlyModel("Done, I think.\n"+StopMarker, &turns), &turns, judge)

	require.Equal(t, int64(2), turns.Load())
	require.Equal(t, []string{"Goal met: the tests pass (the test run passes)"}, r.notices(notify.TypeGoalMet))
	require.Empty(t, r.notifier.warnings())
}

// Every agent the coordinator builds checks the goals the coordinator
// keeps: the coder's and the plan agent's are the same.
func TestCoordinatorAgentsShareItsGoals(t *testing.T) {
	env := testEnv(t)
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(t.TempDir(), "crush.json"))
	crushJSON := `{
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true},
  "providers": {"mock": {"id": "mock", "name": "Mock", "type": "openai",
    "base_url": "http://127.0.0.1:9/v1", "api_key": "test-key",
    "models": [{"id": "mock-model", "name": "Mock", "context_window": 8192, "default_max_tokens": 128}]}},
  "models": {"large": {"provider": "mock", "model": "mock-model"},
             "small": {"provider": "mock", "model": "mock-model"}}
}`
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "crush.json"), []byte(crushJSON), 0o644))
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	cfg.SetupAgents()
	c, err := NewCoordinator(t.Context(), CoordinatorOptions{
		Config:      cfg,
		Sessions:    env.sessions,
		Messages:    env.messages,
		Permissions: env.permissions,
		History:     env.history,
		FileTracker: env.filetracker,
	})
	require.NoError(t, err)
	coord := c.(*coordinator)
	require.NoError(t, coord.readyWg.Wait())

	c.SetGoal("s1", "the tests pass")
	for _, name := range []string{config.AgentCoder, config.AgentPlan} {
		goal, ok := coord.agents[name].(*sessionAgent).goals.Get("s1")
		require.True(t, ok, name)
		require.Equal(t, "the tests pass", goal.Condition, name)
	}
}
