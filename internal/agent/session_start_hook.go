package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// sessionStartSource names what started a session, as the SessionStart
// payload and its matcher carry it.
type sessionStartSource string

const (
	// sessionStartSourceStartup is the first prompt to a session in a
	// process that has started no session of its own yet.
	sessionStartSourceStartup sessionStartSource = hooks.SessionStartSourceStartup
	// sessionStartSourceResume is the first prompt to a session that
	// already has messages.
	sessionStartSourceResume sessionStartSource = hooks.SessionStartSourceResume
	// sessionStartSourceClear is the first prompt to a fresh session in
	// a process that has already started another session: the TUI's New
	// Session, say.
	sessionStartSourceClear sessionStartSource = hooks.SessionStartSourceClear
	// sessionStartSourceCompact is the first prompt after a session's
	// conversation was summarized, as Claude Code's SessionStart fires
	// with "compact" after a /compact or an automatic compaction.
	sessionStartSourceCompact sessionStartSource = hooks.SessionStartSourceCompact
)

// wantsSessionStartHooks reports whether call is the first prompt this
// process has sent to its session, and SessionStart hooks are configured.
// A prompt the user sent in the same process that has already started the
// session is a later prompt, and a sub-agent's session fires no hooks at
// all. A summarized session counts as not started: its next prompt runs
// the hooks again, with source "compact".
func (a *sessionAgent) wantsSessionStartHooks(call SessionAgentCall) bool {
	if a.isSubAgent || a.cfg == nil {
		return false
	}
	if len(a.cfg.Config().Hooks[hooks.EventSessionStart]) == 0 {
		return false
	}
	a.sessionStartMu.Lock()
	defer a.sessionStartMu.Unlock()
	started, ok := a.sessionStarted.Get(call.SessionID)
	return !ok || !started
}

// markSessionStarted records that this process has sent a prompt to the
// session, so its SessionStart hooks do not run again for it.
func (a *sessionAgent) markSessionStarted(sessionID string) {
	a.sessionStartMu.Lock()
	defer a.sessionStartMu.Unlock()
	a.sessionStarted.Set(sessionID, true)
}

// markSessionSummarized records that the session was summarized, so the
// next prompt to it runs the SessionStart hooks again, with source
// "compact", as in Claude Code.
func (a *sessionAgent) markSessionSummarized(sessionID string) {
	a.sessionStartMu.Lock()
	defer a.sessionStartMu.Unlock()
	a.sessionStarted.Set(sessionID, false)
}

// sessionStartSourceFor reports what started the session when the
// process sends it its first prompt: resume if the session already has
// messages, compact if it was summarized and this is the prompt that
// follows, clear if the process has started another session, startup if
// not.
func (a *sessionAgent) sessionStartSourceFor(sessionID string, hasMessages bool) sessionStartSource {
	a.sessionStartMu.Lock()
	defer a.sessionStartMu.Unlock()
	_, started := a.sessionStarted.Get(sessionID)
	if hasMessages {
		if started {
			return sessionStartSourceCompact
		}
		return sessionStartSourceResume
	}
	for _, started := range a.sessionStarted.Copy() {
		if started {
			return sessionStartSourceClear
		}
	}
	return sessionStartSourceStartup
}

// runSessionStartHooks runs the SessionStart hooks for the session, once,
// before the process's first prompt to it goes out, with a source naming
// what started the session. As in Claude Code a hook cannot stop the
// prompt: a block, a halt or a failure is shown to the user as a hook
// warning, and the prompt goes ahead. What the hooks print goes to the
// model with the prompt, before what its UserPromptSubmit hooks print.
func (a *sessionAgent) runSessionStartHooks(ctx context.Context, call *SessionAgentCall) {
	if a.isSubAgent || a.cfg == nil {
		return
	}
	// The session's messages are not loaded yet: the hooks run before the
	// prompt is dispatched. Its count, kept by the database, is enough.
	sess, err := a.sessions.Get(ctx, call.SessionID)
	source := a.sessionStartSourceFor(call.SessionID, err == nil && sess.MessageCount > 0)
	hookCfgs := a.cfg.Config().Hooks[hooks.EventSessionStart]
	if len(hookCfgs) == 0 {
		return
	}

	// Every hook command needs a directory to run in. A store without
	// one would make each hook fail to launch, so resolve it explicitly
	// and bail loudly rather than send the prompt silently unchecked.
	workingDir := a.cfg.WorkingDir()
	if workingDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			slog.Warn("SessionStart hooks are configured but no working directory is available; sending the prompt without them",
				"session_id", call.SessionID,
				"error", err,
			)
			return
		}
		workingDir = wd
	}

	runner := hooks.NewRunner(hookCfgs, workingDir, workingDir)
	model := a.largeModel.Get()
	result, err := runner.RunSessionStart(ctx, call.SessionID, model.ModelCfg.Model, string(source))
	a.warnFailedSessionStartHooks(call.SessionID, result.Hooks)
	if err != nil {
		slog.Warn("SessionStart hook execution error; sending the prompt without it",
			"session_id", call.SessionID,
			"error", err,
		)
		return
	}
	if result.Halt || result.Decision == hooks.DecisionDeny {
		reason := result.Reason
		if reason == "" {
			reason = "a hook objected"
		}
		slog.Info("SessionStart hook objected to the prompt; sending it anyway",
			"session_id", call.SessionID,
			"source", source,
			"reason", reason,
		)
		if a.notify != nil {
			a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
				SessionID: call.SessionID,
				Type:      notify.TypeHookWarning,
				Message:   fmt.Sprintf("SessionStart hook objected to the prompt: %s; the prompt went ahead", reason),
			})
		}
	}
	call.promptHookContext = joinHookContext(call.promptHookContext, result.Context)
}

// joinHookContext adds what a hook event printed for a prompt to what
// earlier hooks printed for it, a blank line between them.
func joinHookContext(earlier, more string) string {
	more = strings.TrimSpace(more)
	switch {
	case earlier == "":
		return more
	case more == "":
		return earlier
	default:
		return earlier + "\n\n" + more
	}
}

// warnFailedSessionStartHooks tells the user about each SessionStart hook
// that did not run to a verdict. The prompt goes ahead regardless, so
// without the warning a broken gate would go unnoticed.
func (a *sessionAgent) warnFailedSessionStartHooks(sessionID string, ran []hooks.HookInfo) {
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
			Message: fmt.Sprintf(
				"SessionStart hook %q failed: %s; the prompt went ahead without it",
				h.Name, h.Error,
			),
		})
	}
}
