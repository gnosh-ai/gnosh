package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// cacheMarks returns the tools a request marks for prompt caching, by
// name, and how many of its messages it marks.
func cacheMarks(call fantasy.Call) (tools []string, messages int) {
	for _, tool := range call.Tools {
		if ft, ok := tool.(fantasy.FunctionTool); ok && len(ft.ProviderOptions) > 0 {
			tools = append(tools, ft.Name)
		}
	}
	for _, msg := range call.Prompt {
		if len(msg.ProviderOptions) > 0 {
			messages++
		}
	}
	return tools, messages
}

// Each request marks its own last tool for prompt caching, and no other.
// The mark was set on the agent's tool, which every request shares, and
// stayed there: once a request's last tool was another (the tools had
// changed, or a turn in a channel left some out), it carried two tool
// marks, five in all, one more than Anthropic takes.
func TestRequestTools_MarksOnlyTheRequestsLastTool(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{}
	a, sessionID, _ := summaryRequestAgent(t, llm)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "first"})
	require.NoError(t, err)

	probe := a.tools.Copy()[0]
	zeta := fantasy.NewAgentTool("zeta", "Another tool.",
		func(context.Context, probeParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("ok"), nil
		})
	a.SetTools([]fantasy.AgentTool{probe, zeta})
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "second"})
	require.NoError(t, err)

	reqs := llm.requests()
	require.Len(t, reqs, 2)
	tools, _ := cacheMarks(reqs[0])
	require.Equal(t, []string{"probe"}, tools)
	tools, messages := cacheMarks(reqs[1])
	require.Equal(t, []string{"zeta"}, tools, "a tool kept an earlier request's mark")
	require.LessOrEqual(t, len(tools)+messages, 4, "more cache marks than Anthropic takes")
	require.Empty(t, probe.ProviderOptions(), "a request marked the agent's own tool")
}
