package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

// TestJobOutputTool_WaitIsBounded pins that job_output with wait:true cannot
// block forever on a background job that never finishes: the model called it
// on a long-running job and the turn wedged with the model idle. With the cap
// the tool returns the output-so-far as still running instead of hanging.
func TestJobOutputTool_WaitIsBounded(t *testing.T) {
	// Overrides the package-level cap, so it must not run in parallel.
	orig := maxJobOutputWait
	maxJobOutputWait = 100 * time.Millisecond
	t.Cleanup(func() { maxJobOutputWait = orig })

	bgManager := shell.GetBackgroundShellManager()
	bgShell, err := bgManager.Start(context.Background(), t.TempDir(), nil, "sleep 60", "", testMaxJobs)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bgManager.Kill(bgShell.ID) })

	input, err := json.Marshal(JobOutputParams{ShellID: bgShell.ID, Wait: true})
	require.NoError(t, err)
	call := fantasy.ToolCall{ID: "test-call", Name: JobOutputToolName, Input: string(input)}
	tool := NewJobOutputTool(t.TempDir(), t.TempDir())

	type result struct {
		resp fantasy.ToolResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := tool.Run(context.Background(), call)
		ch <- result{resp, err}
	}()

	select {
	case r := <-ch:
		require.NoError(t, r.err)
		// sleep 60 is still running, so a bounded wait must report it as
		// running rather than blocking until the job finishes.
		require.Contains(t, r.resp.Content, "Status: running")
	case <-time.After(5 * time.Second):
		t.Fatal("job_output with wait:true blocked well past the cap; the wait is unbounded")
	}
}

// An agent on the AI box asked job_output for "shell_1" after its bash command
// had finished in the foreground, and another killed "g1". The answer must say
// how jobs come about and which jobs there are, those started in the tools'
// directory, so the model does not go on guessing IDs.
func TestJobToolsExplainAnUnknownID(t *testing.T) {
	t.Parallel()

	dir, elsewhere := t.TempDir(), t.TempDir()
	run := func(t *testing.T, tool fantasy.AgentTool, name, id string) fantasy.ToolResponse {
		t.Helper()
		resp, err := tool.Run(context.Background(), fantasy.ToolCall{
			ID: "test-call", Name: name, Input: `{"shell_id":"` + id + `"}`,
		})
		require.NoError(t, err)
		return resp
	}
	tools := map[string]fantasy.AgentTool{
		JobOutputToolName: NewJobOutputTool(dir, t.TempDir()),
		JobKillToolName:   NewJobKillTool(dir),
	}
	for name, tool := range tools {
		resp := run(t, tool, name, "shell_1")
		require.True(t, resp.IsError, name)
		require.Contains(t, resp.Content, "background shell not found: shell_1. There are no background jobs now. ", name)
		require.Contains(t, resp.Content, "already returned its output", name)
	}

	bgManager := shell.GetBackgroundShellManager()
	start := func(dir, command string) *shell.BackgroundShell {
		job, err := bgManager.Start(context.Background(), dir, nil, command, "", testMaxJobs)
		require.NoError(t, err)
		t.Cleanup(func() { _ = bgManager.Kill(job.ID) })
		return job
	}
	ended := start(dir, "exit 3")
	ended.Wait()
	long := start(dir, "sleep 60 # "+strings.Repeat("é", 60))
	lines := start(dir, "sleep 60\necho after")
	other := start(elsewhere, "sleep 60")
	for name, tool := range tools {
		resp := run(t, tool, name, "g1")
		require.True(t, resp.IsError, name)
		require.Contains(t, resp.Content, "background shell not found: g1. The background jobs are ", name)
		require.Contains(t, resp.Content, ended.ID+" (exited 3: exit 3)", name)
		require.Contains(t, resp.Content, long.ID+" (running: sleep 60 # "+strings.Repeat("é", 49)+"…)", name)
		require.Contains(t, resp.Content, lines.ID+" (running: sleep 60…)", name)
		require.NotContains(t, resp.Content, other.ID+" (", name)
	}
}

// job_kill says a job that had already ended was only removed, with its
// exit code, rather than that it terminated it.
func TestJobKillSaysAJobHadEnded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tool := NewJobKillTool(dir)
	bgManager := shell.GetBackgroundShellManager()
	kill := func(id string) string {
		resp, err := tool.Run(context.Background(), fantasy.ToolCall{
			ID: "test-call", Name: JobKillToolName, Input: `{"shell_id":"` + id + `"}`,
		})
		require.NoError(t, err)
		require.False(t, resp.IsError)
		_, ok := bgManager.Get(id)
		require.False(t, ok, "removed")
		return resp.Content
	}

	ended, err := bgManager.Start(context.Background(), dir, nil, "exit 3", "", testMaxJobs)
	require.NoError(t, err)
	ended.Wait()
	require.Equal(t, "Background shell "+ended.ID+" had already exited with code 3; it is removed.", kill(ended.ID))

	running, err := bgManager.Start(context.Background(), dir, nil, "sleep 60", "", testMaxJobs)
	require.NoError(t, err)
	require.Equal(t, "Background shell "+running.ID+" terminated successfully", kill(running.ID))
	require.True(t, running.IsDone())
}
