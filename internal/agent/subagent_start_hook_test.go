package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/stretchr/testify/require"
)

// A SubagentStart hook runs when a sub-agent starts, matched on its type,
// as in Claude Code, and what it adds goes to the sub-agent with its
// first prompt. Its payload names the session the sub-agent works for,
// the sub-agent's own session and its type.
func TestSubagentStartHookAddsContext(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	out := filepath.Join(t.TempDir(), "payload.json")
	coord.cfg.Config().Hooks = map[string][]config.HookConfig{
		hooks.EventSubagentStart: {
			{Matcher: "task", Command: `cat > ` + out + `; echo '{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"Follow the security guidelines."}}'`},
			{Matcher: "agentic_fetch", Command: `echo '{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"for the fetch helper only"}}'`},
		},
	}
	require.NoError(t, coord.cfg.Config().ValidateHooks())
	parent, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	var got SessionAgentCall
	agent := newMockAgent(providerID, 4096, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		got = call
		return agentResultWithText("done"), nil
	})
	_, err = coord.runSubAgent(t.Context(), subAgentParams{
		Agent: agent, SessionID: parent.ID, AgentMessageID: "msg-1",
		ToolCallID: "call-1", Prompt: "look into it", SessionTitle: "Sub", AgentType: config.AgentTask,
	})
	require.NoError(t, err)
	require.Equal(t, "look into it", got.Prompt)
	require.Equal(t, "Follow the security guidelines.", got.promptHookContext, "the fetch helper's hook did not match")
	require.Contains(t, promptHookBlock(got), "Follow the security guidelines.", "it reaches the model with the first prompt")

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var payload hooks.Payload
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.Equal(t, hooks.EventSubagentStart, payload.HookEventName)
	require.Equal(t, parent.ID, payload.SessionID, "session_id is the session the sub-agent works for")
	require.Equal(t, got.SessionID, payload.AgentID)
	require.Equal(t, config.AgentTask, payload.AgentType)
}

// A SubagentStart hook cannot stop the sub-agent: one that could not run
// leaves it to start without the hook's word.
func TestSubagentStartHookThatFailsDoesNotStopTheSubAgent(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	coord.cfg.Config().Hooks = map[string][]config.HookConfig{
		hooks.EventSubagentStart: {{Command: `exit 3`}},
	}
	require.NoError(t, coord.cfg.Config().ValidateHooks())
	parent, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	ran := false
	agent := newMockAgent(providerID, 4096, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		ran = true
		require.Empty(t, call.promptHookContext)
		return agentResultWithText("done"), nil
	})
	_, err = coord.runSubAgent(t.Context(), subAgentParams{
		Agent: agent, SessionID: parent.ID, AgentMessageID: "msg-1",
		ToolCallID: "call-1", Prompt: "look into it", SessionTitle: "Sub", AgentType: config.AgentTask,
	})
	require.NoError(t, err)
	require.True(t, ran)
}
