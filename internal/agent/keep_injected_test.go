package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// What earlier steps put in goes back where they put it, after the first
// at messages of the step's input, in the order it was put in.
func TestKeepInjected(t *testing.T) {
	t.Parallel()

	msg := func(text string) fantasy.Message { return fantasy.NewUserMessage(text) }
	texts := func(msgs []fantasy.Message) string {
		var b strings.Builder
		for _, m := range msgs {
			text, _ := fantasy.AsMessagePart[fantasy.TextPart](m.Content[0])
			b.WriteString(text.Text)
		}
		return b.String()
	}
	input := []fantasy.Message{msg("a"), msg("b"), msg("c"), msg("d")}
	require.Equal(t, "abcd", texts(keepInjected(input, nil)))
	require.Equal(t, "aXbYZcdW", texts(keepInjected(input, []injectedMessages{
		{at: 1, messages: []fantasy.Message{msg("X")}},
		{at: 2, messages: []fantasy.Message{msg("Y"), msg("Z")}},
		{at: 4, messages: []fantasy.Message{msg("W")}},
	})))
}

// A prompt typed while the agent works, folded into the running turn, is
// in each of the turn's later steps, once, where it was put in, as it is
// in the conversation kept: it was in the step it was folded into and
// gone from the next.
func TestRunKeepsAPromptFoldedIntoTheTurn(t *testing.T) {
	env := testEnv(t)
	llm := &countingModel{steps: 5}
	model := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Tools: []fantasy.AgentTool{fantasy.NewAgentTool("count", "Count.",
			func(_ context.Context, p countParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
				return fantasy.NewTextResponse(fmt.Sprint(p.N)), nil
			})},
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "counting")
	require.NoError(t, err)
	// A user message already there skips title generation, which would
	// call the same model from another goroutine.
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)
	const followUp = "also count backwards"
	llm.onStep = func(n int) {
		if n == 2 {
			a.messageQueue.Set(sess.ID, []SessionAgentCall{{SessionID: sess.ID, Prompt: followUp}})
		}
	}

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "count", NonInteractive: true})
	require.NoError(t, err)

	require.Len(t, llm.prompts, 6)
	at := -1
	for i, prompt := range llm.prompts {
		var found []int
		for j, m := range prompt {
			for _, part := range m.Content {
				if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && m.Role == fantasy.MessageRoleUser && strings.Contains(text.Text, followUp) {
					found = append(found, j)
				}
			}
		}
		if i < 2 {
			require.Empty(t, found, "request %d, before it was typed", i+1)
			continue
		}
		require.Len(t, found, 1, "request %d", i+1)
		if at < 0 {
			at = found[0]
		}
		require.Equal(t, at, found[0], "request %d has it where it was put in", i+1)
	}
}
