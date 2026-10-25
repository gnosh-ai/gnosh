package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// A conversation already past the point at which it is compacted on the
// model it now runs on, as after /model to a model with a smaller window,
// is summarized before the prompt goes out, as Claude Code compacts
// before a request: the summary first, without the prompt, then the
// prompt after the summary. On the AI box it went out whole: gpt-oss read
// 127,722 tokens for six minutes, and only then was it summarized.
func TestRun_CompactsBeforeTheRequestAfterAModelSwitch(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{
		replies: []string{"done", "the summary", "done again"},
		usage:   fantasy.Usage{InputTokens: 1_000, OutputTokens: 10},
	}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	big := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 400_000, DefaultMaxTokens: 1000}}
	a.SetModels(big, big)
	// 170,010 tokens: under the 400,000-token model's point, 380,000.
	_, err := a.sessions.UpdateUsage(t.Context(), sessionID, 170_000, 10, 0, false)
	require.NoError(t, err)
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "first"})
	require.NoError(t, err)
	require.Len(t, llm.requests(), 1, "no summary under the point")

	// The same count on a model with a 200,000-token window is past its
	// point, 160,000.
	_, err = a.sessions.UpdateUsage(t.Context(), sessionID, 170_000, 10, 0, false)
	require.NoError(t, err)
	small := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200_000, DefaultMaxTokens: 1000}}
	a.SetModels(small, small)
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "second"})
	require.NoError(t, err)

	reqs := llm.requests()
	require.Len(t, reqs, 3, "the first turn, the summary, then the second")
	summary, turn := reqs[1], reqs[2]
	require.Equal(t, fantasy.ToolChoiceNone, *summary.ToolChoice, "the summary goes first")
	summarized := userTexts(summary.Prompt)
	require.Contains(t, summarized, "first")
	require.NotContains(t, summarized, "second", "the prompt is not summarized")
	require.Equal(t, buildSummaryPrompt(nil, ""), summarized[len(summarized)-1])
	texts := userTexts(turn.Prompt)
	require.NotContains(t, texts, "first", "the turn goes on from the summary")
	at := slices.Index(texts, continuedSummary+"\n\nthe summary")
	require.GreaterOrEqual(t, at, 0)
	require.Greater(t, slices.Index(texts, "second"), at, "the prompt goes after the summary")

	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	msgs, err := a.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	summaryAt := slices.IndexFunc(msgs, func(m message.Message) bool { return m.ID == sess.SummaryMessageID })
	require.GreaterOrEqual(t, summaryAt, 0)
	require.Greater(t, slices.IndexFunc(msgs, func(m message.Message) bool { return m.Content().Text == "second" }), summaryAt,
		"the prompt is kept after the summary")
}

// Esc while the conversation is summarized before a prompt ends the turn
// there: no summary replaces the conversation, and the prompt is kept as
// a cancelled turn, not sent.
func TestRun_CancelWhileCompactingBeforeTheRequest(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{replies: []string{"hang"}, entered: make(chan struct{})}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	_, err := a.sessions.UpdateUsage(t.Context(), sessionID, 170_000, 10, 0, false)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "next"})
		done <- err
	}()
	select {
	case <-llm.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the summary was not asked for")
	}
	a.Cancel(sessionID)
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn did not end")
	}
	require.ErrorIs(t, err, context.Canceled)

	reqs := llm.requests()
	require.Len(t, reqs, 1, "the summary only")
	require.Equal(t, fantasy.ToolChoiceNone, *reqs[0].ToolChoice, "it was the summary that was cancelled")
	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	require.Empty(t, sess.SummaryMessageID)
	msgs, err := a.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	require.False(t, slices.ContainsFunc(msgs, func(m message.Message) bool { return m.IsSummaryMessage }), "the cancelled summary is removed")
	require.Equal(t, "next", msgs[len(msgs)-2].Content().Text, "the prompt is kept")
	require.Equal(t, message.FinishReasonCanceled, msgs[len(msgs)-1].FinishReason())
}

// A summary before the prompt that fails leaves the conversation as it
// was, and the prompt goes out whole, as Claude Code goes on when
// compacting fails.
func TestRun_FailedCompactionBeforeTheRequestSendsThePromptWhole(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{
		replies: []string{"call probe", "done"},
		usage:   fantasy.Usage{InputTokens: 1_000, OutputTokens: 10},
	}
	a, sessionID, runs := summaryRequestAgent(t, llm)
	_, err := a.sessions.UpdateUsage(t.Context(), sessionID, 170_000, 10, 0, false)
	require.NoError(t, err)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "next"})
	require.NoError(t, err)
	require.Zero(t, runs.Load(), "the summary's tool call did not run")
	reqs := llm.requests()
	require.Len(t, reqs, 2, "the summary that wrote none, then the turn")
	texts := userTexts(reqs[1].Prompt)
	require.Equal(t, "next", texts[len(texts)-1])
	require.True(t, slices.ContainsFunc(texts, func(text string) bool { return strings.HasPrefix(text, "a page said") }),
		"the conversation goes whole")
	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	require.Empty(t, sess.SummaryMessageID)
}

// The prompt after a summary made before it runs the SessionStart hooks
// with source "compact", as the first prompt after any summary does, and
// what they print goes with it.
func TestRun_SessionStartAfterCompactingBeforeTheRequest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "source")
	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo "$KLASH_SESSION_START_SOURCE" > `+out+`; echo "source: $KLASH_SESSION_START_SOURCE"`)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
	require.NoError(t, err)
	r.agent.disableAutoSummarize = false
	_, err = r.env.sessions.UpdateUsage(t.Context(), r.sessionID, 170_000, 10, 0, false)
	require.NoError(t, err)

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "second"})
	require.NoError(t, err)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "compact\n", string(data))
	prompts := r.model.recorded()
	require.Len(t, prompts, 3, "the first turn, the summary, the second turn")
	texts := userTexts(prompts[2])
	require.Equal(t, "second\n\n<user-prompt-submit-hook>\nsource: compact\n</user-prompt-submit-hook>", texts[len(texts)-1])

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "third"})
	require.NoError(t, err)
	prompts = r.model.recorded()
	texts = userTexts(prompts[len(prompts)-1])
	require.Equal(t, "third", texts[len(texts)-1], "they run once after the summary")
}
