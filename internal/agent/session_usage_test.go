package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// savingSessions counts the times a whole session is saved.
type savingSessions struct {
	session.Service
	saves atomic.Int32
}

func (s *savingSessions) Save(ctx context.Context, sess session.Session) (session.Session, error) {
	s.saves.Add(1)
	return s.Service.Save(ctx, sess)
}

// A step's usage is written alone, never the whole session: saving the
// session read at the step's end put its title back over the title
// model's, written meanwhile, or the user's /rename. One run in five of
// TestRunTitlesOnlyTopLevelSessions lost its title that way.
func TestRunWritesOnlyTheStepsUsage(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	spy := &savingSessions{Service: env.sessions}
	env.sessions = spy
	a := testSessionAgent(env, fastModel{}, fastModel{}, "system")
	sess, err := env.sessions.Create(t.Context(), "")
	require.NoError(t, err)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hello", NonInteractive: true})
	require.NoError(t, err)
	require.Zero(t, spy.saves.Load(), "no whole session saved")
	got, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.NotZero(t, got.PromptTokens, "the step's usage was written")
}

// A sub-agent's cost is added to its parent's alone, keeping the
// parent's title and counters.
func TestParentSessionCostIsAddedAlone(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	spy := &savingSessions{Service: env.sessions}
	coord := &coordinator{sessions: spy}
	parent, err := spy.Create(t.Context(), "Parent")
	require.NoError(t, err)
	_, err = spy.UpdateUsage(t.Context(), parent.ID, 500, 40, 1, false)
	require.NoError(t, err)
	child, err := spy.CreateTaskSession(t.Context(), "call-1", parent.ID, "Child")
	require.NoError(t, err)
	_, err = spy.UpdateUsage(t.Context(), child.ID, 100, 10, 0.25, false)
	require.NoError(t, err)

	require.NoError(t, coord.updateParentSessionCost(t.Context(), child.ID, parent.ID))
	got, err := spy.Get(t.Context(), parent.ID)
	require.NoError(t, err)
	require.InDelta(t, 1.25, got.Cost, 1e-9)
	require.Equal(t, "Parent", got.Title)
	require.Equal(t, [2]int64{500, 40}, [2]int64{got.PromptTokens, got.CompletionTokens})
	require.Zero(t, spy.saves.Load(), "no whole session saved")
}

// usageModel answers as fastModel does, reporting 1000 tokens in and 100
// out.
type usageModel struct{ fastModel }

func (usageModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "1", Delta: "done"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "1"})
		yield(fantasy.StreamPart{
			Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop,
			Usage: fantasy.Usage{InputTokens: 1000, OutputTokens: 100},
		})
	}, nil
}

// Each step adds its own cost, not the session's total so far: two
// turns at $0.0012 a step cost $0.0024.
func TestRunAddsEachStepsCost(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	a := testSessionAgent(env, usageModel{}, fastModel{}, "system")
	priced := Model{Model: usageModel{}, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000, CostPer1MIn: 1, CostPer1MOut: 2}}
	a.SetModels(priced, Model{Model: fastModel{}, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000}})
	sess, err := env.sessions.Create(t.Context(), "")
	require.NoError(t, err)
	for _, prompt := range []string{"one", "two"} {
		_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: prompt, NonInteractive: true})
		require.NoError(t, err)
	}
	got, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.0024, got.Cost, 1e-9)
	require.Equal(t, [2]int64{1000, 100}, [2]int64{got.PromptTokens, got.CompletionTokens})
	require.False(t, got.EstimatedUsage)
}
