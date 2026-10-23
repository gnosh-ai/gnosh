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

// preCompactTrigger names what set a summary going, as the PreCompact
// payload and its matcher carry it.
type preCompactTrigger string

const (
	// preCompactTriggerManual is the user asking for the summary.
	preCompactTriggerManual preCompactTrigger = hooks.PreCompactTriggerManual
	// preCompactTriggerAuto is the context filling up.
	preCompactTriggerAuto preCompactTrigger = hooks.PreCompactTriggerAuto
)

// runPreCompactHooks runs the PreCompact hooks for a summary of the
// given session just before it is summarized, with trigger naming what
// set it going. As in Claude Code a hook cannot stop the summary: a
// block, a halt or a failure is shown to the user as a hook warning, and
// the summary goes ahead.
func (a *sessionAgent) runPreCompactHooks(ctx context.Context, sessionID string, trigger preCompactTrigger) {
	// Sub-agents never fire hooks, mirroring wrapToolsWithHooks; cfg is
	// nil in tests and wherever config-backed features are disabled.
	if a.isSubAgent || a.cfg == nil {
		return
	}
	hookCfgs := a.cfg.Config().Hooks[hooks.EventPreCompact]
	if len(hookCfgs) == 0 {
		return
	}

	// Every hook command needs a directory to run in. A store without
	// one would make each hook fail to launch, so resolve it explicitly
	// and bail loudly rather than summarizing silently unchecked.
	workingDir := a.cfg.WorkingDir()
	if workingDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			slog.Warn("PreCompact hooks are configured but no working directory is available; summarizing without them",
				"session_id", sessionID,
				"error", err,
			)
			return
		}
		workingDir = wd
	}

	runner := hooks.NewRunner(hookCfgs, workingDir, workingDir)
	result, err := runner.RunPreCompact(ctx, sessionID, string(trigger))
	a.warnFailedPreCompactHooks(sessionID, result.Hooks)
	if err != nil {
		slog.Warn("PreCompact hook execution error; summarizing without it",
			"session_id", sessionID,
			"error", err,
		)
		return
	}
	if result.Halt || result.Decision == hooks.DecisionDeny {
		reason := result.Reason
		if reason == "" {
			reason = "a hook objected"
		}
		slog.Info("PreCompact hook objected to the summary; summarizing anyway",
			"session_id", sessionID,
			"trigger", trigger,
			"reason", reason,
		)
		if a.notify != nil {
			a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
				SessionID: sessionID,
				Type:      notify.TypeHookWarning,
				Message:   fmt.Sprintf("PreCompact hook objected to the %s summary: %s; the summary went ahead", trigger, reason),
			})
		}
	}
}

// warnFailedPreCompactHooks tells the user about each PreCompact hook
// that did not run to a verdict. The summary goes ahead regardless, so
// without the warning a broken gate would summarize as if it had passed.
func (a *sessionAgent) warnFailedPreCompactHooks(sessionID string, ran []hooks.HookInfo) {
	if a.notify == nil {
		return
	}
	for _, h := range ran {
		if h.Error == "" {
			continue
		}
		a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID: sessionID,
			Type:      notify.TypeHookWarning,
			Message:   fmt.Sprintf("PreCompact hook %q failed: %s; the summary went ahead without it", h.Name, h.Error),
		})
	}
}
