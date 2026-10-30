package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/shell"
)

// jobEndGrace is how long after a background job ends gnosh waits before
// telling the agent. A job_output already waiting on the job returns at
// once and takes the job's end first, so the agent is not told twice. It
// is a var so tests can shorten it.
var jobEndGrace = 2 * time.Second

// jobWatcher tells a session when a background job its agent started
// ends, as Claude Code tells the agent when a background command ends:
// between its tool calls while it works, or in a turn of its own once it
// has replied. It tells only of an end nothing else took first
// (shell.BackgroundShell.ClaimEnd): not one the agent read with
// job_output, stopped with job_kill, or one gnosh stopped as it exited.
type jobWatcher struct {
	// run sends the notice to the session as a hidden prompt the chat
	// shows as a notice, which the running turn takes at its next step
	// or which starts a turn.
	run func(ctx context.Context, sessionID, prompt string) error
	// closing is set as gnosh shuts down; then no job's end starts a turn.
	closing atomic.Bool
}

// watch is the bash tool's BackgroundWatcher: it waits for job to end
// and tells the session of the bash call that started it.
func (w *jobWatcher) watch(ctx context.Context, job *shell.BackgroundShell) {
	sessionID := tools.GetSessionFromContext(ctx)
	if sessionID == "" {
		return
	}
	grace := jobEndGrace
	go func() {
		job.Wait()
		time.Sleep(grace)
		if w.closing.Load() || !job.ClaimEnd() {
			return
		}
		ctx := message.WithNoticeUserMessage(message.WithHiddenUserMessage(context.Background()))
		if err := w.run(ctx, sessionID, tools.JobEndNotice(job)); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("Could not tell the agent a background job ended", "session_id", sessionID, "job", job.ID, "error", err)
		}
	}()
}
