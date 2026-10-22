package agent

import (
	"context"
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

// textOnlyModel answers with one line of text and no tool calls, which
// is what makes a turn end, and counts how many turns it was asked for.
func textOnlyModel(answer string, turns *atomic.Int64) *fakeLanguageModel {
	return &fakeLanguageModel{
		stream: func(yield func(fantasy.StreamPart) bool) {
			turns.Add(1)
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t1"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: answer})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"})
			yield(fantasy.StreamPart{
				Type:         fantasy.StreamPartTypeFinish,
				FinishReason: fantasy.FinishReasonStop,
			})
		},
	}
}

type stopHookRun struct {
	agent     *sessionAgent
	sessionID string
	notifier  *recordingNotifier
	// turns counts how many times the model was asked to answer: one
	// for the original prompt, plus one per continuation a Stop hook
	// forced.
	turns *atomic.Int64
}

// promptAwareModel answers each turn according to the latest user
// message it was sent, so a test can script how the agent responds to a
// reflection checkpoint. It counts turns like textOnlyModel.
type promptAwareModel struct {
	fakeLanguageModel
	turns  *atomic.Int64
	answer func(lastUserText string) string
}

func (m *promptAwareModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.turns.Add(1)
	text := m.answer(lastUserText(call.Prompt))
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: text})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

// lastUserText returns the text of the most recent user message.
func lastUserText(prompt fantasy.Prompt) string {
	for i := len(prompt) - 1; i >= 0; i-- {
		if prompt[i].Role != fantasy.MessageRoleUser {
			continue
		}
		var b strings.Builder
		for _, part := range prompt[i].Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				b.WriteString(text.Text)
			}
		}
		return b.String()
	}
	return ""
}

// runTurnWithStopHook drives a whole turn through sessionAgent.Run with
// the given Stop hook commands configured, against a model that always
// answers "I'm all done." Run drains its own queue and recurses, so by
// the time it returns every continuation the hooks forced has already
// run.
func runTurnWithStopHook(t *testing.T, commands ...string) stopHookRun {
	t.Helper()
	var turns atomic.Int64
	return runTurnWithStopHookModel(t, textOnlyModel("I'm all done.", &turns), &turns, commands...)
}

// runTurnWithStopHookModel is runTurnWithStopHook with the model chosen
// by the caller; turns must be the counter that model increments.
func runTurnWithStopHookModel(t *testing.T, llm fantasy.LanguageModel, turns *atomic.Int64, commands ...string) stopHookRun {
	t.Helper()

	env := testEnv(t)
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd}
	}
	cfg := &config.Config{}
	if len(commands) > 0 {
		cfg.Hooks = map[string][]config.HookConfig{hooks.EventStop: hookCfgs}
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
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  config.NewTestStore(cfg),
		Notify:               notifier,
	}).(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "stop hook turn")
	require.NoError(t, err)

	// Seed a user message so Run skips title generation. That runs on a
	// detached goroutine against the same fake model, and would both
	// race the turn counter and inflate it.
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)

	_, err = a.Run(t.Context(), SessionAgentCall{
		SessionID: sess.ID,
		RunID:     "run-1",
		Prompt:    "do the thing",
	})
	require.NoError(t, err)
	return stopHookRun{agent: a, sessionID: sess.ID, notifier: notifier, turns: turns}
}

func TestRun_NoStopHooksEndsAfterOneTurn(t *testing.T) {
	r := runTurnWithStopHook(t)
	require.Equal(t, int64(1), r.turns.Load())
	requireQueueEmpty(t, r)
}

func TestRun_StopHookAllowEndsAfterOneTurn(t *testing.T) {
	r := runTurnWithStopHook(t, `exit 0`)
	require.Equal(t, int64(1), r.turns.Load(),
		"a hook that allows the stop must not cost an extra turn")
	requireQueueEmpty(t, r)
}

// The feature, end to end: a hook that objects once sends the agent back
// to work exactly once, then lets the turn end.
func TestRun_StopHookBlockOnceCostsOneMoreTurn(t *testing.T) {
	r := runTurnWithStopHook(t,
		`[ "$CRUSH_STOP_HOOK_ACTIVE" = "1" ] && exit 0; echo "you forgot the tests" >&2; exit 2`)

	require.Equal(t, int64(2), r.turns.Load(),
		"the agent must answer again after the block, then be allowed to stop")
	requireQueueEmpty(t, r)
	require.Empty(t, r.notifier.warnings(),
		"the hook was satisfied, not overruled, so there is nothing to warn about")

	// The reason reached the model as a prompt, and is hidden from the
	// transcript the user reads.
	msgs, err := r.agent.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)
	var found bool
	for _, m := range msgs {
		if strings.Contains(m.Content().String(), "you forgot the tests") {
			found = true
		}
	}
	require.True(t, found, "the hook's reason must reach the model as the continuation prompt")
}

// No cap: a task that genuinely needs 25 more attempts gets them. The
// agent here never judges that it should stop, so the turn runs until the
// hook is satisfied — passing two reflection checkpoints on the way.
func TestRun_StopHookHasNoCap(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "runs")
	r := runTurnWithStopHook(t,
		`n=$(cat `+counter+` 2>/dev/null || echo 0); n=$((n+1)); echo $n > `+counter+`; `+
			`if [ "$n" -le 25 ]; then echo "attempt $n: not done" >&2; exit 2; fi`)

	require.Equal(t, int64(26), r.turns.Load(),
		"the original turn plus one per objection, with nothing cutting it short")
	requireQueueEmpty(t, r)
	require.Empty(t, r.notifier.warnings(),
		"the hook was eventually satisfied; nothing was overruled")

	msgs, err := r.agent.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)
	var checkpoints int
	for _, m := range msgs {
		if m.Role == message.User && strings.Contains(m.Content().String(), StopMarker) {
			checkpoints++
		}
	}
	require.Equal(t, 2, checkpoints,
		"the agent is asked to reflect at the 10th and 20th objection, and only then")
}

// The other half: the agent, asked at a checkpoint, judges there is a
// genuine reason to stop. The turn ends there, the hook is not consulted
// again, and the user is told why.
func TestRun_AgentJudgesItShouldStopAtACheckpoint(t *testing.T) {
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
	// With no cap, nothing but the agent's judgement ends this turn — so if
	// the checkpoint ever broke, the test would spin forever instead of
	// failing. The hook gives in after 3 checkpoints' worth of objections,
	// which a working checkpoint never reaches, so a regression fails on
	// the counts below rather than hanging the run.
	const giveUpAfter = 3 * stopHookReflectEvery
	r := runTurnWithStopHookModel(t, llm, &turns,
		`n=$(cat `+counter+` 2>/dev/null || echo 0); n=$((n+1)); echo $n > `+counter+`; `+
			`if [ "$n" -le `+strconv.Itoa(giveUpAfter)+` ]; then echo "never done" >&2; exit 2; fi`)

	require.Equal(t, int64(stopHookReflectEvery+1), r.turns.Load(),
		"the original turn, nine ordinary retries, then the checkpoint turn where the agent judged")
	requireQueueEmpty(t, r)

	raw, err := os.ReadFile(counter)
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(stopHookReflectEvery), strings.TrimSpace(string(raw)),
		"the hook is not consulted after the agent's judgement")

	warnings := r.notifier.warnings()
	require.Len(t, warnings, 1, "the user must be told their gate was overruled")
	require.Contains(t, warnings[0].Message, "staging database", "with the agent's reason")
	require.Contains(t, warnings[0].Message, "never done", "and the hook's last objection")
}

// The hook judges the answer the turn is about to end on.
func TestRun_StopHookSeesTheFinalAnswer(t *testing.T) {
	out := filepath.Join(t.TempDir(), "payload.json")
	r := runTurnWithStopHook(t, "cat > "+out+"; exit 0")
	requireQueueEmpty(t, r)

	raw := readFileForTest(t, out)
	require.Contains(t, raw, "I'm all done.",
		"last_assistant_message must carry the turn's real final text")
	require.Contains(t, raw, `"event":"Stop"`)
}

func requireQueueEmpty(t *testing.T, r stopHookRun) {
	t.Helper()
	queued, ok := r.agent.messageQueue.Get(r.sessionID)
	require.False(t, ok && len(queued) > 0,
		"the turn must not leave work stranded on the queue")
}

// A Claude Code Stop hook blocks with JSON, not an exit code. Before
// Crush understood "block", this hook was read as no opinion and the
// turn ended at once — the Stop hook feature silently did nothing for
// the hooks people were most likely to bring with them.
func TestRun_ClaudeCodeStyleStopHookKeepsTheAgentWorking(t *testing.T) {
	r := runTurnWithStopHook(t,
		`[ "$CRUSH_STOP_HOOK_ACTIVE" = "1" ] && exit 0; echo '{"decision":"block","reason":"run the tests first"}'`)

	require.Equal(t, int64(2), r.turns.Load(),
		"the JSON block must cost exactly one more turn, like exit 2 does")
	requireQueueEmpty(t, r)
	require.Empty(t, r.notifier.warnings(), "the hook worked, so nothing to warn about")
}
