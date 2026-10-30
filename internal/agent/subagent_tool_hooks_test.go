package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/stretchr/testify/require"
)

// A sub-agent's tool calls go past the PreToolUse hooks too, as in Claude
// Code, and the hooks see whose calls they are: the session the
// sub-agent works for as session_id, its own session as agent_id and its
// type as agent_type. Sub-agents' calls skipped every hook before, so a
// hook keeping the agent from reading a key did not keep the agent's
// sub-agent from reading it.
func TestSubAgentToolCallsGoPastToolHooks(t *testing.T) {
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	out := filepath.Join(t.TempDir(), "payload.json")
	coord.cfg.Config().Hooks = map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: `cat > ` + out + `; echo "no reading keys" >&2; exit 2`}},
	}
	require.NoError(t, coord.cfg.Config().ValidateHooks())

	view := &fakeTool{name: tools.ViewToolName}
	subTools := wrapToolsWithHooks([]fantasy.AgentTool{view}, coord.buildToolHooks())
	parent, err := env.sessions.Create(t.Context(), "Parent")
	require.NoError(t, err)

	var (
		childID string
		resp    fantasy.ToolResponse
		runErr  error
	)
	agent := newMockAgent(providerID, 4096, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		childID = call.SessionID
		// The sub-agent's call, in the context its turn runs in.
		ctx = context.WithValue(ctx, tools.SessionIDContextKey, call.SessionID)
		resp, runErr = subTools[0].Run(ctx, fantasy.ToolCall{ID: "call-view", Name: tools.ViewToolName, Input: `{"file_path": "/home/me/.ssh/id_ed25519"}`})
		return agentResultWithText("done"), nil
	})
	_, err = coord.runSubAgent(t.Context(), subAgentParams{
		Agent: agent, SessionID: parent.ID, AgentMessageID: "msg-1",
		ToolCallID: "call-1", Prompt: "read the key", SessionTitle: "Sub", AgentType: config.AgentTask,
	})
	require.NoError(t, err)
	require.NoError(t, runErr)

	require.False(t, view.called, "the hook kept the sub-agent from reading")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "no reading keys")

	raw, err := os.ReadFile(out)
	require.NoError(t, err)
	var got hooks.Payload
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, parent.ID, got.SessionID, "session_id is the session the sub-agent works for")
	require.Equal(t, childID, got.AgentID)
	require.Equal(t, config.AgentTask, got.AgentType)
}

// buildTools wraps a sub-agent's tools in the tool hooks as it does the
// main agent's.
func TestBuildToolsHooksASubAgentsTools(t *testing.T) {
	// No user tool descriptions, which wrap a tool again.
	t.Setenv("GNOSH_GLOBAL_CONFIG", t.TempDir())
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	coord.cfg.Config().Hooks = map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Command: "exit 0"}},
	}
	require.NoError(t, coord.cfg.Config().ValidateHooks())

	built, err := coord.buildTools(t.Context(), config.Agent{ID: config.AgentTask, AllowedTools: []string{"glob", "grep", "view"}}, true)
	require.NoError(t, err)
	require.Len(t, built, 3)
	for _, tool := range built {
		_, hooked := tool.(*hookedTool)
		require.Truef(t, hooked, "%s is not hooked", tool.Info().Name)
	}
}
