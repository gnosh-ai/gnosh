package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// runSubagentStartHooks runs the SubagentStart hooks for a sub-agent of
// type agentType starting in its own session, agentID, to work for
// sessionID, and returns what they added for it to read with its first
// prompt, as Claude Code does. A hook cannot stop the sub-agent; one that
// did not run to a verdict is shown to the user as a hook warning, and
// the sub-agent starts without it. A Claude Code config with SubagentStart
// hooks used to stop Gnosh at startup, as an event it did not fire.
func (c *coordinator) runSubagentStartHooks(ctx context.Context, sessionID, agentID, agentType string) string {
	if c.cfg == nil {
		return ""
	}
	hookCfgs := c.cfg.Config().Hooks[hooks.EventSubagentStart]
	if len(hookCfgs) == 0 {
		return ""
	}
	workingDir := c.cfg.WorkingDir()
	if workingDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			slog.Warn("SubagentStart hooks are configured but no working directory is available; starting the sub-agent without them",
				"session_id", sessionID, "error", err)
			return ""
		}
		workingDir = wd
	}
	runner := hooks.NewRunner(hookCfgs, workingDir, workingDir)
	result, err := runner.RunSubagentStart(ctx, sessionID, agentID, agentType)
	if err != nil {
		slog.Warn("SubagentStart hook execution error; starting the sub-agent without it", "session_id", sessionID, "error", err)
		return ""
	}
	if c.notify != nil {
		for _, h := range result.Hooks {
			if h.Error == "" {
				continue
			}
			c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
				SessionID: sessionID,
				Type:      notify.TypeWarning,
				Message:   fmt.Sprintf("SubagentStart hook %q could not evaluate: %s; the sub-agent started without it", h.Name, h.Error),
			})
		}
	}
	return result.Context
}
