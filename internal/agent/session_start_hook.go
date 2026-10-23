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

// sessionStartState is the SessionStart hooks' state for the sessions a
// process sends prompts to: whether each is started, whether each was
// summarized, and what a hook's output for it was. It is shared by every
// main agent the process runs, so a session started by either counts as
// started for both, and a summary made in either mode fires "compact"
// for the next prompt in either. Each process has one; sub-agents fire
// no SessionStart hooks and never touch it.
type sessionStartState struct {
	// mu guards started, summarized, and context.
	mu sync.Mutex
	// started records, per session, that this process has sent a prompt
	// to it, so its SessionStart hooks run once, for the first prompt,
	// and not for later ones. A summarized session reads as not
	// started: its next prompt runs the hooks again, with source
	// "compact".
	started map[string]bool
	// summarized records, per session, that it was summarized, so the
	// next prompt to it fires the SessionStart hooks with source
	// "compact". Cleared when the next prompt runs the hooks.
	summarized map[string]bool
	// context holds what the SessionStart hooks printed for a session
	// whose first prompt was refused before it reached the model. It is
	// sent out with the next prompt to that session instead, so what the
	// hooks printed is neither lost nor sent twice.
	context map[string]string
}

func newSessionStartState() *sessionStartState {
	return &sessionStartState{
		started:    make(map[string]bool),
		summarized: make(map[string]bool),
		context:    make(map[string]string),
	}
}

// wants reports whether call's prompt runs the SessionStart hooks: the
// first prompt the process sends to the session, whether it is visible
// or hidden. A prompt the user sent in the same process that has already
// started the session is a later prompt, and a sub-agent's session fires
// no hooks at all. A summarized session reads as not started: its next
// prompt runs the hooks again, with source "compact".
func (s *sessionStartState) wants(agent *sessionAgent, call SessionAgentCall) bool {
	if agent == nil || agent.isSubAgent || agent.cfg == nil {
		return false
	}
	if len(agent.cfg.Config().Hooks[hooks.EventSessionStart]) == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.started[call.SessionID]
}

// markStarted records that the process has sent a prompt to the session,
// so its SessionStart hooks do not run again for it. It also clears the
// summarized flag, since the hooks have now run for this start.
func (s *sessionStartState) markStarted(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started[sessionID] = true
	s.summarized[sessionID] = false
}

// markSummarized records that the session was summarized, so the next
// prompt to it runs the SessionStart hooks again, with source "compact",
// as in Claude Code.
func (s *sessionStartState) markSummarized(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started[sessionID] = false
	s.summarized[sessionID] = true
}

// takePendingContext returns what the hooks printed for the session
// whose first prompt was refused, and clears it, so it is sent out with
// the next prompt to the session and not again.
func (s *sessionStartState) takePendingContext(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.context[sessionID]
	delete(s.context, sessionID)
	return out
}

// setPendingContext keeps what the hooks printed for a session whose
// first prompt was refused, so it is sent out with the next prompt to
// the session instead.
func (s *sessionStartState) setPendingContext(sessionID, context string) {
	if context == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.context[sessionID] = context
}

// sourceFor reports what started the session when the process sends it
// its first prompt: compact if it was summarized and this is the prompt
// that follows, resume if the session already has messages, clear if the
// process has started another session, startup if not.
func (s *sessionStartState) sourceFor(sessionID string, hasMessages bool) sessionStartSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.summarized[sessionID] {
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
// process has sent to its session, and SessionStart hooks are
// configured. The decision is re-checked by Run once it holds the
// submit lock, so two prompts sent at once run the hooks once.
func (a *sessionAgent) wantsSessionStartHooks(call SessionAgentCall) bool {
	if a.sessionStartState != nil {
		return a.sessionStartState.wants(a, call)
	}
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
// session, so its SessionStart hooks do not run again for it. It also
// clears the summarized flag, since the hooks have now run for this
// start.
func (a *sessionAgent) markSessionStarted(sessionID string) {
	if a.sessionStartState != nil {
		a.sessionStartState.markStarted(sessionID)
		return
	}
	a.sessionStartMu.Lock()
	defer a.sessionStartMu.Unlock()
	a.sessionStarted.Set(sessionID, true)
	a.summarized.Del(sessionID)
}

// markSessionSummarized records that the session was summarized, so the
// next prompt to it runs the SessionStart hooks again, with source
// "compact", as in Claude Code.
func (a *sessionAgent) markSessionSummarized(sessionID string) {
	if a.sessionStartState != nil {
		a.sessionStartState.markSummarized(sessionID)
		return
	}
	a.sessionStartMu.Lock()
	defer a.sessionStartMu.Unlock()
	a.sessionStarted.Set(sessionID, false)
	a.summarized.Set(sessionID, true)
}

// takeSessionStartPendingContext returns what a SessionStart hook printed
// for the session whose earlier first prompt was refused, and clears it.
func (a *sessionAgent) takeSessionStartPendingContext(sessionID string) string {
	if a.sessionStartState != nil {
		return a.sessionStartState.takePendingContext(sessionID)
	}
	a.sessionStartMu.Lock()
	defer a.sessionStartMu.Unlock()
	out, _ := a.sessionStartedContext.Get(sessionID)
	a.sessionStartedContext.Del(sessionID)
	return out
}

// setSessionStartPendingContext keeps what a SessionStart hook printed
// for a prompt that was refused before it reached the model, so the next
// prompt to the session carries it.
func (a *sessionAgent) setSessionStartPendingContext(sessionID, context string) {
	if context == "" {
		return
	}
	if a.sessionStartState != nil {
		a.sessionStartState.setPendingContext(sessionID, context)
		return
	}
	a.sessionStartMu.Lock()
	defer a.sessionStartMu.Unlock()
	a.sessionStartedContext.Set(sessionID, context)
}

// sessionStartSourceFor reports what started the session when the
// process sends it its first prompt: compact if it was summarized and
// this is the prompt that follows, resume if the session already has
// messages, clear if the process has started another session, startup
// if not.
func (a *sessionAgent) sessionStartSourceFor(sessionID string, hasMessages bool) sessionStartSource {
	if a.sessionStartState != nil {
		return a.sessionStartState.sourceFor(sessionID, hasMessages)
	}
	a.sessionStartMu.Lock()
	defer a.sessionStartMu.Unlock()
	if wasSummarized, _ := a.summarized.Get(sessionID); wasSummarized {
		return sessionStartSourceCompact
	}
	if hasMessages {
		return sessionStartSourceResume
	}
	for _, started := range a.sessionStarted.Copy() {
		if started {
			return sessionStartSourceClear
		}
	}
	return sessionStartSourceStartup
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
// carries what they printed with it (and the flag that says so), and the
// caller marks the session started, so the hooks run once per start and
// the prompt that follows does not run them again. When they did not
// run for it, call is left unchanged: the prompt that runs them,
// whenever that is, carries what they print.
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
	source := a.sessionStartSourceFor(call.SessionID, err == nil && sess.MessageCount > 0)

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
	call.sessionStartHookRan = true
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
