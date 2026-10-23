package agent

import (
	"context"
	"errors"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// A tool result that ends the sub-agent's turn (a hook's halt, a
// permission the user denied) ends the parent's: the agent tool's answer
// stops the turn and says why. The parent got only the sub-agent's last
// text before, and went on.
func TestRunSubAgentPassesAHaltOn(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	parent, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	run := func(callID string, result *fantasy.AgentResult) fantasy.ToolResponse {
		t.Helper()
		agent := newMockAgent(providerID, 4096, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
			return result, nil
		})
		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent: agent, SessionID: parent.ID, AgentMessageID: "msg-1",
			ToolCallID: callID, Prompt: "read the key", SessionTitle: "Sub",
		})
		require.NoError(t, err)
		return resp
	}

	halted := agentResultWithText("Let me read it.")
	halted.Steps = []fantasy.StepResult{{Response: fantasy.Response{Content: fantasy.ResponseContent{
		fantasy.ToolResultContent{
			ToolCallID: "c1", ToolName: "view", StopTurn: true,
			Result: fantasy.ToolResultOutputContentError{Error: errors.New("Turn halted by hook. Reason: no keys")},
		},
	}}}}
	resp := run("call-1", halted)
	require.True(t, resp.StopTurn, "the parent's turn ends too")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "no keys")

	resp = run("call-2", agentResultWithText("Two files."))
	require.False(t, resp.StopTurn)
	require.Equal(t, "Two files.", resp.Content)
}

// The stop marker a sub-agent ends on when it overrules its SubagentStop
// hooks is not part of what the parent reads.
func TestSubAgentOutputLeavesOutTheStopMarker(t *testing.T) {
	t.Parallel()
	require.Equal(t, "This needs a database the box cannot reach.", subAgentOutput(agentResultWithText("This needs a database the box cannot reach.\n"+StopMarker)))
}

// A disabled MCP server's tools are left out however they are wrapped, as
// they are once hooks or user tool descriptions are configured; the filter
// took a wrapped tool for a built-in one.
func TestFilterDisabledMCPToolsSeesThroughWrappers(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	require.NoError(t, env.sessions.SetMCPServerDisabled(t.Context(), "github", true))
	a := &sessionAgent{sessions: env.sessions}

	wrapped := applyHooksAndDescription(&channelTestTool{name: "mcp_github_create_issue", mcpName: "github"}, newRunner(t, "exit 0"))
	plain := applyHooksAndDescription(&fakeTool{name: "view"}, newRunner(t, "exit 0"))
	kept := a.filterDisabledMCPTools(t.Context(), []fantasy.AgentTool{wrapped, plain})
	require.Len(t, kept, 1)
	require.Equal(t, "view", kept[0].Info().Name)
}
