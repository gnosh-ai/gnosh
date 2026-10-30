package agent

import (
	"context"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// idleAgent is a session agent on a model with a 1M-token window, so that
// a turn's usage reaches the idle-compaction size without auto-compacting
// during the turn, which waits wait for idle compaction.
func idleAgent(t *testing.T, usage int64, wait time.Duration) (*sessionAgent, *requestRecordingModel, string) {
	t.Helper()
	env := testEnv(t)
	llm := &requestRecordingModel{usage: fantasy.Usage{InputTokens: usage, OutputTokens: 10}}
	model := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 1_000_000, DefaultMaxTokens: 1000}}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:   model,
		SmallModel:   model,
		SystemPrompt: "test",
		IsYolo:       true,
		Sessions:     env.sessions,
		Messages:     env.messages,
	}).(*sessionAgent)
	a.idleCompactAfter = wait
	sess, err := env.sessions.Create(t.Context(), "idle")
	require.NoError(t, err)
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier work"}},
	})
	require.NoError(t, err)
	return a, llm, sess.ID
}

func summaryRequests(llm *requestRecordingModel) int {
	n := 0
	for _, req := range llm.requests() {
		if req.ToolChoice != nil && *req.ToolChoice == fantasy.ToolChoiceNone {
			n++
		}
	}
	return n
}

// A long conversation left idle is summarized when the wait is over, as
// Claude Code's idle compaction does, so the next prompt sends a summary;
// a short one, a gnosh run's, and one whose next turn starts in time are
// not.
func TestIdleCompaction(t *testing.T) {
	t.Setenv("CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS", "")

	a, llm, sessionID := idleAgent(t, 250_000, 50*time.Millisecond)
	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "go on"})
	require.NoError(t, err)
	require.Zero(t, summaryRequests(llm), "not during the turn")
	require.Eventually(t, func() bool {
		sess, err := a.sessions.Get(context.Background(), sessionID)
		return err == nil && sess.SummaryMessageID != ""
	}, 5*time.Second, 20*time.Millisecond, "the idle conversation was summarized")
	require.Equal(t, 1, summaryRequests(llm))

	for name, run := range map[string]func(*sessionAgent, string) error{
		"short": nil,
		"gnosh run": func(a *sessionAgent, sessionID string) error {
			_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "go on", NonInteractive: true})
			return err
		},
	} {
		usage := int64(250_000)
		if name == "short" {
			usage = 150_000
		}
		a, llm, sessionID := idleAgent(t, usage, 20*time.Millisecond)
		if run == nil {
			_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "go on"})
		} else {
			err = run(a, sessionID)
		}
		require.NoError(t, err, name)
		time.Sleep(300 * time.Millisecond)
		require.Zero(t, summaryRequests(llm), name)
	}

	// The next turn's end starts the wait again, in place of the first
	// turn's, which would have run out 400ms after that turn ended.
	a, llm, sessionID = idleAgent(t, 250_000, 400*time.Millisecond)
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "first"})
	require.NoError(t, err)
	firstEnded := time.Now()
	time.Sleep(200 * time.Millisecond)
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "second"})
	require.NoError(t, err)
	time.Sleep(time.Until(firstEnded.Add(500 * time.Millisecond)))
	require.Zero(t, summaryRequests(llm), "the first turn's wait was replaced")
	require.Eventually(t, func() bool { return summaryRequests(llm) == 1 }, 5*time.Second, 20*time.Millisecond, "the second's runs out")

	// Turned off, with auto-summarize or on its own, it does not wait.
	a, _, sessionID = idleAgent(t, 250_000, 20*time.Millisecond)
	a.disableIdleCompaction = true
	a.armIdleCompaction(sessionID, nil)
	_, armed := a.idleCompaction.Get(sessionID)
	require.False(t, armed)
	a.disableIdleCompaction, a.disableAutoSummarize = false, true
	a.armIdleCompaction(sessionID, nil)
	_, armed = a.idleCompaction.Get(sessionID)
	require.False(t, armed)
}

// CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS sets the size, as in Claude Code: a
// positive whole number, raised to 100000; anything else leaves 200000.
func TestIdleCompactMinTokens(t *testing.T) {
	for value, want := range map[string]int64{
		"":       200_000,
		"150000": 150_000,
		"50000":  100_000,
		"0":      200_000,
		"lots":   200_000,
		"999999": 999_999,
	} {
		t.Setenv("CLAUDE_CODE_IDLE_COMPACT_MIN_TOKENS", value)
		require.Equal(t, want, idleCompactMinTokens(), value)
	}
}
