package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

// noticeSent is a notice a jobWatcher sent.
type noticeSent struct {
	sessionID, prompt string
	hidden            bool
}

// recordingJobWatcher is a jobWatcher whose notices go to the channel it
// returns, after a short grace; tests using it must not run in parallel.
func recordingJobWatcher(t *testing.T) (*jobWatcher, chan noticeSent) {
	old := jobEndGrace
	jobEndGrace = 10 * time.Millisecond
	t.Cleanup(func() { jobEndGrace = old })
	sent := make(chan noticeSent, 10)
	return &jobWatcher{run: func(ctx context.Context, sessionID, prompt string) error {
		sent <- noticeSent{sessionID, prompt, message.HiddenUserMessage(ctx)}
		return nil
	}}, sent
}

// startJob starts command as a background job in dir, stopped when the
// test ends.
func startJob(t *testing.T, dir, command string) *shell.BackgroundShell {
	bg := shell.GetBackgroundShellManager()
	job, err := bg.Start(context.Background(), dir, nil, command, "", 50)
	require.NoError(t, err)
	t.Cleanup(func() {
		job.ClaimEnd()
		_ = bg.Kill(job.ID)
	})
	return job
}

// When a background job ends, the session whose bash call started it is
// sent the notice, as a hidden prompt, as Claude Code tells the agent. A
// job whose end something took first (job_output, job_kill, klash's
// exit) is not told of, nor one started with no session, nor one that
// ends once klash is shutting down.
func TestJobWatcherTellsTheSessionOfAJobsEnd(t *testing.T) {
	w, sent := recordingJobWatcher(t)
	dir := t.TempDir()
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, "s1")

	job := startJob(t, dir, "sleep 0.1; exit 3")
	w.watch(ctx, job)
	select {
	case got := <-sent:
		require.Equal(t, noticeSent{"s1", tools.JobEndNotice(job), true}, got)
		require.Contains(t, got.prompt, "failed with exit code 3")
	case <-time.After(10 * time.Second):
		t.Fatal("the session was not told the job ended")
	}

	taken := startJob(t, dir, "sleep 0.1")
	w.watch(ctx, taken)
	require.True(t, taken.ClaimEnd(), "as job_output does")
	unsessioned := startJob(t, dir, "sleep 0.1")
	w.watch(t.Context(), unsessioned)
	closed, _ := recordingJobWatcher(t)
	closed.run = w.run
	closed.closing.Store(true)
	late := startJob(t, dir, "sleep 0.1")
	closed.watch(ctx, late)
	for _, job := range []*shell.BackgroundShell{taken, unsessioned, late} {
		job.Wait()
	}
	select {
	case got := <-sent:
		t.Fatalf("told %+v", got)
	case <-time.After(300 * time.Millisecond):
	}
	require.True(t, late.ClaimEnd(), "a closing watcher leaves the end untaken")
}

// CancelAll, which klash calls as it shuts down, stops the job watcher:
// the jobs stopped on the way out start no turn.
func TestCancelAllClosesTheJobWatcher(t *testing.T) {
	t.Parallel()

	coord := &coordinator{jobs: &jobWatcher{}, mainAgent: &mockSessionAgent{}}
	require.False(t, coord.jobs.closing.Load())
	coord.CancelAll()
	require.True(t, coord.jobs.closing.Load())
}

// The main agent of an interactive klash gets a bash tool that hands its
// background jobs to the watcher; a sub-agent, whose turn is over when
// its job ends, and `klash run`, which exits with its turn, do not.
func TestBuildToolsWatchesTheMainAgentsJobs(t *testing.T) {
	// No user tool descriptions, which wrap a tool again.
	t.Setenv("KLASH_GLOBAL_CONFIG", t.TempDir())
	w, _ := recordingJobWatcher(t)
	const providerID = "test-provider"
	env := testEnv(t)
	coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
	coord.jobs = w
	coord.permissions = env.permissions
	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, "s1")
	bashResult := func(isSubAgent bool) string {
		built, err := coord.buildTools(t.Context(), config.Agent{ID: config.AgentCoder, AllowedTools: []string{tools.BashToolName}}, isSubAgent)
		require.NoError(t, err)
		require.Len(t, built, 1)
		input, err := json.Marshal(tools.BashParams{Description: "wait", Command: "sleep 60", RunInBackground: true})
		require.NoError(t, err)
		resp, err := built[0].Run(ctx, fantasy.ToolCall{ID: "call-1", Name: tools.BashToolName, Input: string(input)})
		require.NoError(t, err)
		require.Contains(t, resp.Content, "Background shell started with ID: ")
		return resp.Content
	}

	coord.interactive = true
	require.Contains(t, bashResult(false), "You will be told when it ends")
	require.NotContains(t, bashResult(true), "You will be told", "a sub-agent's")
	coord.interactive = false
	require.NotContains(t, bashResult(false), "You will be told", "klash run's")

	bg := shell.GetBackgroundShellManager()
	for _, job := range bg.Jobs(env.workingDir) {
		if s, ok := bg.Get(job.ID); ok {
			s.ClaimEnd()
		}
		_ = bg.Kill(job.ID)
	}
}
