package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	gnovmtools "github.com/charmbracelet/crush/internal/gnovm/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func gnovmRunTool() fantasy.AgentTool {
	return fantasy.NewAgentTool(gnovmtools.RunToolName, "Run Gno.",
		func(context.Context, countParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("ok"), nil
		})
}

// A prompt the user sends a session with mem and Gno goes out with memLine
// last, after what mem recalled for it, is kept with it hidden, and goes
// out again the same way in later turns' history; a hidden prompt, as a
// continuation is, carries none.
func TestRun_MemLineGoesLastOnATypedPrompt(t *testing.T) {
	t.Parallel()

	r := sessionStartAgent(t, false, "")
	r.agent.SetTools([]fantasy.AgentTool{gnovmRunTool()})
	recall := "<mem>\n- fact `cmd/test` = go test -race ./...\n</mem>"
	r.agent.memRecall = func(context.Context, string, string) string { return recall }

	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "run the tests"})
	require.NoError(t, err)
	want := "run the tests\n\n" + message.PromptHookContext(recall) + "\n\n" + memLine
	prompts := r.model.recorded()
	require.Len(t, prompts, 1)
	texts := userTexts(prompts[0])
	require.Equal(t, want, texts[len(texts)-1], "last, after what mem recalled")

	msgs, err := r.env.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)
	require.Equal(t, message.User, msgs[0].Role)
	last, ok := msgs[0].Parts[len(msgs[0].Parts)-1].(message.TextContent)
	require.True(t, ok)
	require.Equal(t, message.TextContent{Text: memLine, Hidden: true}, last, "kept with the prompt, out of the chat")

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "go on", HiddenUserMessage: true})
	require.NoError(t, err)
	prompts = r.model.recorded()
	require.Len(t, prompts, 2)
	texts = userTexts(prompts[1])
	require.Contains(t, texts, want, "the first prompt goes out again as it did")
	require.Equal(t, "go on", texts[len(texts)-1], "a hidden prompt carries none")

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "and the tag"})
	require.NoError(t, err)
	prompts = r.model.recorded()
	require.Len(t, prompts, 3)
	texts = userTexts(prompts[2])
	require.Equal(t, "and the tag\n\n"+message.PromptHookContext(recall)+"\n\n"+memLine, texts[len(texts)-1])
}

// Without gnovm_run among the tools there is no mem to point at, and a
// sub-agent's prompt is the main agent's, which mem does not remind.
func TestRun_NoMemLineWithoutGnovmOrForASubAgent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		subAgent bool
		tools    []fantasy.AgentTool
	}{
		{name: "no gnovm_run"},
		{name: "sub-agent", subAgent: true, tools: []fantasy.AgentTool{gnovmRunTool()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := sessionStartAgent(t, tc.subAgent, "")
			r.agent.SetTools(tc.tools)
			_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "run the tests"})
			require.NoError(t, err)
			prompts := r.model.recorded()
			require.Len(t, prompts, 1)
			texts := userTexts(prompts[0])
			require.Equal(t, "run the tests", texts[len(texts)-1])
		})
	}
}
