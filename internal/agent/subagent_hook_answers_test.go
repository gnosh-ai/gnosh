package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// A hook's approval of the agent tool's call is not its sub-agent's: the
// sub-agent's calls can carry the same ID, as a provider that numbers
// calls tool-call-0 in each response gives them, and one of them ran
// unasked. The sub-agent's call goes through the usual permission flow.
func TestSubAgentGetsNoHookApprovalOfItsCall(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	parent, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	perms := permission.NewPermissionService(t.TempDir(), false, nil)
	var (
		granted bool
		reqErr  error
	)
	agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		// No one answers here, so a request that is asked ends with the
		// cancelled wait; one that was approved returns at once.
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		granted, reqErr = perms.Request(cancelled, permission.CreatePermissionRequest{
			SessionID: call.SessionID, ToolCallID: "tool-call-0", ToolName: "bash", Action: "execute", Path: t.TempDir(),
		})
		return agentResultWithText("done"), nil
	})
	ctx := permission.WithHookApproval(t.Context(), "tool-call-0")
	_, err = coord.runSubAgent(ctx, subAgentParams{
		Agent: agent, SessionID: parent.ID, AgentMessageID: "msg-1",
		ToolCallID: "tool-call-0", Prompt: "go", SessionTitle: "Sub", AgentType: config.AgentTask,
	})
	require.NoError(t, err)
	require.False(t, granted, "the approval of the agent tool's call did not carry over")
	require.ErrorIs(t, reqErr, context.Canceled)
}
