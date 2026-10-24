package agent

import (
	"context"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// renamingModel names the session while it answers, as /rename typed
// during a long summary or title request does, and then answers with
// text and finish.
type renamingModel struct {
	fakeLanguageModel
	text   string
	finish fantasy.FinishReason
	rename func()
}

func (m *renamingModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return func(yield func(fantasy.StreamPart) bool) {
		m.rename()
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t", Delta: m.text})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: m.finish})
	}, nil
}

// A summary on the AI box takes minutes. The session it read before
// asking was saved whole afterwards, so a name given meanwhile was put
// back to the old one.
func TestSummarizeKeepsANameGivenMeanwhile(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	llm := &renamingModel{text: "the summary", finish: fantasy.FinishReasonStop}
	model := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel: model,
		SmallModel: model,
		Sessions:   env.sessions,
		Messages:   env.messages,
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "the old name")
	require.NoError(t, err)
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	})
	require.NoError(t, err)
	var renameErr error
	llm.rename = func() { renameErr = env.sessions.Rename(context.Background(), sess.ID, "named meanwhile") }

	require.NoError(t, a.Summarize(t.Context(), sess.ID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual))
	require.NoError(t, renameErr)

	got, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.NotEmpty(t, got.SummaryMessageID, "the summary was not saved")
	require.Equal(t, "named meanwhile", got.Title)
}

// A session's first prompt has its title made while the turn runs, and
// on a busy model server that can take a while. A name given meanwhile
// is kept, whether a title was made or the start of the prompt stood in.
func TestGenerateTitleKeepsANameGivenMeanwhile(t *testing.T) {
	t.Parallel()

	for _, finish := range []fantasy.FinishReason{fantasy.FinishReasonStop, fantasy.FinishReasonLength} {
		llm := &renamingModel{text: "A made-up title", finish: finish}
		a, env, id := titleAgent(t, llm)
		var renameErr error
		llm.rename = func() { renameErr = env.sessions.Rename(context.Background(), id, "named meanwhile") }

		a.GenerateTitle(t.Context(), id, "please fix the flaky test")
		require.NoError(t, renameErr)

		sess, err := env.sessions.Get(t.Context(), id)
		require.NoError(t, err)
		require.Equal(t, "named meanwhile", sess.Title, "finish %s", finish)
	}
}
