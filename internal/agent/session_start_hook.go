package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

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

// sessionStartState is what the SessionStart hooks need to know of the
// sessions a process sends prompts to: which it started, which were
// summarized since, and what the hooks printed for a prompt that was then
// refused. A coordinator's main agents share one, so a session started in
// code mode counts as started in plan mode, and a summary made in either
// fires "compact" for the next prompt in either.
type sessionStartState struct {
	mu sync.Mutex
	// started records the sessions this process has sent a prompt to, so
	// their hooks run for the first prompt only. A summarized session is
	// recorded as false, not started: its next prompt runs them again,
	// with source "compact".
	started map[string]bool
	// pending holds what the hooks printed for a session whose prompt a
	// UserPromptSubmit hook refused, for the next prompt to carry: the
	// session counts as started, so the hooks do not run again for it.
	pending map[string]string
}

func newSessionStartState() *sessionStartState {
	return &sessionStartState{
		started: make(map[string]bool),
		pending: make(map[string]string),
	}
}

func (s *sessionStartState) isStarted(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started[sessionID]
}

// markStarted records that the session's hooks ran for its start, so they
// do not run again until it is summarized.
func (s *sessionStartState) markStarted(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started[sessionID] = true
}

// markSummarized records that the session was summarized, so the next
// prompt to it runs the SessionStart hooks again, with source "compact",
// as in Claude Code.
func (s *sessionStartState) markSummarized(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started[sessionID] = false
}

// takePending returns what the hooks printed for the session's refused
// prompt, and forgets it.
func (s *sessionStartState) takePending(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.pending[sessionID]
	delete(s.pending, sessionID)
	return out
}

// setPending keeps what the hooks printed for a prompt that was refused,
// for the next prompt to the session to carry.
func (s *sessionStartState) setPending(sessionID, context string) {
	if context == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[sessionID] = context
}

// sourceFor reports what started the session when the process sends it
// its first prompt: compact if it was summarized and this is the prompt
// that follows, resume if the session already has messages, clear if the
// process has started another session, startup if not.
func (s *sessionStartState) sourceFor(sessionID string, hasMessages bool) sessionStartSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	if started, ok := s.started[sessionID]; ok && !started {
		return sessionStartSourceCompact
	}
	if hasMessages {
		return sessionStartSourceResume
	}
	for _, started := range s.started {
		if started {
			return sessionStartSourceClear
		}
	}
	return sessionStartSourceStartup
}

// wantsSessionStartHooks reports whether call is the first prompt this
// process sends to its session, visible or hidden, and SessionStart
// hooks are configured. A sub-agent's session fires no hooks at all. Run
// asks again once it holds the submit lock, so two prompts sent at once
// run the hooks once.
func (a *sessionAgent) wantsSessionStartHooks(call SessionAgentCall) bool {
	if a.isSubAgent || a.cfg == nil {
		return false
	}
	if len(a.cfg.Config().Hooks[hooks.EventSessionStart]) == 0 {
		return false
	}
	return !a.sessionStarts.isStarted(call.SessionID)
}

// runSessionStartHooks runs the SessionStart hooks for the session,
// once, before the process's first prompt to it goes out, with a source
// naming what started the session. As in Claude Code a hook cannot stop
// the prompt: a block, a halt or a failure is shown to the user as a
// hook warning, and the prompt goes ahead. What the hooks print goes to
// the model with the prompt, before what its UserPromptSubmit hooks
// print, whether the prompt is visible or a hidden continuation.
//
// It returns whether the hooks ran for this call. When they ran, call
// carries what they printed (and carriesSessionStart), and the caller
// marks the session started, so the hooks run once per start. When they
// did not, call is left unchanged.
func (a *sessionAgent) runSessionStartHooks(ctx context.Context, call *SessionAgentCall) (ran bool) {
	if a.isSubAgent || a.cfg == nil {
		return false
	}
	hookCfgs := a.cfg.Config().Hooks[hooks.EventSessionStart]
	if len(hookCfgs) == 0 {
		return false
	}

	// The session's messages are not loaded yet: the hooks run before the
	// prompt is dispatched. Its count, kept by the database, is enough.
	sess, err := a.sessions.Get(ctx, call.SessionID)
	source := a.sessionStarts.sourceFor(call.SessionID, err == nil && sess.MessageCount > 0)

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
			return false
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
		return false
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
	call.carriesSessionStart = true
	call.promptHookContext = joinHookContext(call.promptHookContext, result.Context)
	return true
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
