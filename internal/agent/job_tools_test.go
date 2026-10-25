package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

// The agent's job_output and job_kill answer an ID that names no job with
// the jobs started in its working directory, not one started in another,
// as a workspace's /tasks lists only its own.
func TestJobToolsNameTheWorkspacesJobs(t *testing.T) {
	// No user tool descriptions, which wrap a tool again.
	t.Setenv("KLASH_GLOBAL_CONFIG", t.TempDir())
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	built, err := coord.buildTools(t.Context(), config.Agent{ID: config.AgentCoder, AllowedTools: []string{tools.JobOutputToolName, tools.JobKillToolName}}, false)
	require.NoError(t, err)
	require.Len(t, built, 2)

	bg := shell.GetBackgroundShellManager()
	start := func(dir string) *shell.BackgroundShell {
		job, err := bg.Start(t.Context(), dir, nil, "sleep 60", "", 50)
		require.NoError(t, err)
		t.Cleanup(func() { _ = bg.Kill(job.ID) })
		return job
	}
	here, elsewhere := start(env.workingDir), start(t.TempDir())
	for _, tool := range built {
		name := tool.Info().Name
		resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-1", Name: name, Input: `{"shell_id":"g1"}`})
		require.NoError(t, err)
		require.True(t, resp.IsError, name)
		require.Contains(t, resp.Content, here.ID+" (running: sleep 60)", name)
		require.NotContains(t, resp.Content, elsewhere.ID+" (", name)
	}
}
