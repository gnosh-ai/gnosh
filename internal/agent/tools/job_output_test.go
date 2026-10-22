package tools

import (
	"context"
	"encoding/json"
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
	tool := NewJobOutputTool(t.TempDir())

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
// had finished in the foreground. The answer must say how jobs come about, so
// it does not go on guessing IDs.
func TestJobToolsExplainAnUnknownID(t *testing.T) {
	t.Parallel()

	for name, tool := range map[string]fantasy.AgentTool{
		JobOutputToolName: NewJobOutputTool(t.TempDir()),
		JobKillToolName:   NewJobKillTool(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp, err := tool.Run(context.Background(), fantasy.ToolCall{
				ID: "test-call", Name: name, Input: `{"shell_id":"shell_1"}`,
			})
			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Contains(t, resp.Content, "background shell not found: shell_1")
			require.Contains(t, resp.Content, "already returned its output")
		})
	}
}
