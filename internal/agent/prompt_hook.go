package agent

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// wantsPromptHooks reports whether call is a prompt the user sent whose
// UserPromptSubmit hooks have yet to run. A hidden continuation is the
// agent's own, a sub-agent's prompt comes from its parent, and a queued
// call already went past the hooks when it was sent.
func (a *sessionAgent) wantsPromptHooks(call SessionAgentCall) bool {
	if call.HiddenUserMessage || call.promptHooksRan || a.isSubAgent || a.cfg == nil {
		return false
	}
	return len(a.cfg.Config().Hooks[hooks.EventUserPromptSubmit]) > 0
}

// sessionSubmitMu is the per-session lock a prompt holds while its
// UserPromptSubmit hooks run and until its dispatch has begun, so prompts
// sent one after another are dispatched in that order however long each
// one's hooks take. Cancel does not take it.
func (a *sessionAgent) sessionSubmitMu(sessionID string) *sync.Mutex {
	a.dispatchMuCreate.Lock()
	defer a.dispatchMuCreate.Unlock()
	if mu, ok := a.submitMu.Get(sessionID); ok {
		return mu
	}
	mu := &sync.Mutex{}
	a.submitMu.Set(sessionID, mu)
	return mu
}

// runPromptHooks runs the UserPromptSubmit hooks for call, a prompt the
// user sent. It returns an error wrapping ErrPromptBlocked, with the
// hooks' reason, when one blocks the prompt; otherwise it records on call
// the context the hooks added, which goes to the model with the prompt.
// A hook that fails is reported and the prompt is sent without it, as a
// failed Stop hook lets the turn end.
func (a *sessionAgent) runPromptHooks(ctx context.Context, call *SessionAgentCall) error {
	call.promptHooksRan = true
	workingDir := a.cfg.WorkingDir()
	if workingDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			slog.Warn("UserPromptSubmit hooks are configured but no working directory is available; sending the prompt without them",
				"session_id", call.SessionID,
				"error", err,
			)
			return nil
		}
		workingDir = wd
	}

	runner := hooks.NewRunner(a.cfg.Config().Hooks[hooks.EventUserPromptSubmit], workingDir, workingDir)
	var attachments []string
	for _, att := range call.Attachments {
		attachments = append(attachments, cmp.Or(att.FileName, filepath.Base(att.FilePath)))
	}
	result, err := runner.RunUserPromptSubmit(ctx, call.SessionID, call.Prompt, attachments)
	a.warnFailedPromptHooks(call.SessionID, result.Hooks)
	if err != nil {
		slog.Warn("UserPromptSubmit hook execution error; sending the prompt without it",
			"session_id", call.SessionID,
			"error", err,
		)
		return nil
	}
	if result.Halt || result.Decision == hooks.DecisionDeny {
		reason := strings.TrimSpace(result.Reason)
		if reason == "" {
			reason = "a hook refused it"
		}
		slog.Info("UserPromptSubmit hook blocked the prompt", "session_id", call.SessionID, "reason", reason)
		return fmt.Errorf("%w: %s", ErrPromptBlocked, reason)
	}
	call.promptHookContext = strings.TrimSpace(result.Context)
	return nil
}

// warnFailedPromptHooks tells the user about each UserPromptSubmit hook
// that did not run to a verdict, since the prompt went out without what
// it would have added or checked.
func (a *sessionAgent) warnFailedPromptHooks(sessionID string, ran []hooks.HookInfo) {
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
				"UserPromptSubmit hook %q failed: %s; the prompt was sent without it",
				h.Name, h.Error,
			),
		})
	}
}

// promptHookBlock is what call's UserPromptSubmit hooks added, as the
// model reads it, or "" for none. Continuations copied from the call are
// the agent's own and carry nothing of the user's hooks.
func promptHookBlock(call SessionAgentCall) string {
	if call.HiddenUserMessage || call.promptHookContext == "" {
		return ""
	}
	return message.PromptHookContext(call.promptHookContext)
}
