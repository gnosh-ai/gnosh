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

// runPostCompactHooks runs the PostCompact hooks once the given session's
// conversation has been summarized to summary, with trigger naming what
// set the summary going, as Claude Code runs them: to log the summary or
// update something outside Gnosh. A hook has no say over the summary,
// which is done; one that did not run to a verdict is shown to the user
// as a hook warning. A Claude Code config with PostCompact hooks used to
// stop Gnosh at startup, as an event it did not fire.
func (a *sessionAgent) runPostCompactHooks(ctx context.Context, sessionID string, trigger preCompactTrigger, summary string) {
	// A sub-agent's summary runs no PostCompact hooks, as it runs no
	// PreCompact ones. cfg is nil in tests and wherever config-backed
	// features are disabled.
	if a.isSubAgent || a.cfg == nil {
		return
	}
	hookCfgs := a.cfg.Config().Hooks[hooks.EventPostCompact]
	if len(hookCfgs) == 0 {
		return
	}
	workingDir := a.cfg.WorkingDir()
	if workingDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			slog.Warn("PostCompact hooks are configured but no working directory is available; not running them",
				"session_id", sessionID,
				"error", err,
			)
			return
		}
		workingDir = wd
	}

	runner := hooks.NewRunner(hookCfgs, workingDir, workingDir)
	result, err := runner.RunPostCompact(ctx, sessionID, string(trigger), summary)
	a.warnHookMessages(sessionID, hooks.EventPostCompact, result.Hooks)
	if err != nil {
		slog.Warn("PostCompact hook execution error", "session_id", sessionID, "error", err)
		return
	}
	if a.notify == nil {
		return
	}
	for _, h := range result.Hooks {
		if h.Error == "" {
			continue
		}
		a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID: sessionID,
			Type:      notify.TypeWarning,
			Message:   fmt.Sprintf("PostCompact hook %q could not evaluate: %s", h.Name, h.Error),
		})
	}
}
