package agent

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// wantsPromptHooks reports whether call is a prompt the user sent whose
// UserPromptSubmit hooks have yet to run. A hidden continuation is the
// agent's own, a sub-agent's prompt comes from its parent, a queued call
// already went past the hooks when it was sent, and a channel message
// (Signal, say) was written by someone else: a block would drop it with
// no reply to its sender.
func (a *sessionAgent) wantsPromptHooks(call SessionAgentCall) bool {
	if call.HiddenUserMessage || call.promptHooksRan || call.Channel != "" || a.isSubAgent || a.cfg == nil {
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
	// The hooks read the prompt as the model will: with the text of any
	// pasted or attached text file after it. A paste of more than a few
	// lines becomes an attachment, and a hook that saw only its name
	// missed a key in a pasted log.
	prompt := message.PromptWithTextAttachments(call.Prompt, call.Attachments)
	result, err := runner.RunUserPromptSubmit(ctx, call.SessionID, prompt, attachments)
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
	a.warnFailedPromptHooks(call.SessionID, result.Hooks)
	call.promptHookContext = joinHookContext(call.promptHookContext, result.Context)
	return nil
}

// beginSubmitting records that a prompt sent to the session is about to
// go through its UserPromptSubmit hooks, and returns the context to run
// them under, which Cancel ends. Until endSubmitting the session reads as
// busy. Without it a prompt in its hooks was registered nowhere: an Esc
// found nothing to cancel, and the prompt ran when the hooks finished.
func (a *sessionAgent) beginSubmitting(ctx context.Context, sessionID string) (context.Context, *activeCancel) {
	hookCtx, cancel := context.WithCancel(ctx)
	s := &activeCancel{cancel: cancel}
	mu := a.sessionMu(sessionID)
	mu.Lock()
	defer mu.Unlock()
	list, _ := a.submitting.Get(sessionID)
	a.submitting.Set(sessionID, append(slices.Clip(list), s))
	return hookCtx, s
}

// endSubmitting removes s from the session's prompts in their hooks and
// releases its context. lock takes the dispatch lock; Run passes false
// when it already holds it.
func (a *sessionAgent) endSubmitting(sessionID string, s *activeCancel, lock bool) {
	if lock {
		mu := a.sessionMu(sessionID)
		mu.Lock()
		defer mu.Unlock()
	}
	list, _ := a.submitting.Get(sessionID)
	list = slices.DeleteFunc(slices.Clone(list), func(x *activeCancel) bool { return x == s })
	if len(list) == 0 {
		a.submitting.Del(sessionID)
	} else {
		a.submitting.Set(sessionID, list)
	}
}

// cancelSubmitting stops the hooks of every prompt being sent to the
// session. Cancel calls it holding the dispatch lock.
func (a *sessionAgent) cancelSubmitting(sessionID string) {
	list, _ := a.submitting.Get(sessionID)
	for _, s := range list {
		s.cancel()
	}
}

// isSubmitting reports whether a prompt sent to the session is in its
// UserPromptSubmit hooks.
func (a *sessionAgent) isSubmitting(sessionID string) bool {
	list, ok := a.submitting.Get(sessionID)
	return ok && len(list) > 0
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

// promptHookBlock is what call's hooks added, as the model reads it, or
// "" for none. A prompt that ran its own SessionStart hooks carries
// what they printed even when it is hidden, since the prompt goes out
// and the model must read it; a continuation copied from a call (a Stop
// hook's, a loop's) is the agent's own and carries nothing of the
// user's hooks, and it never ran the hooks itself.
func promptHookBlock(call SessionAgentCall) string {
	if call.promptHookContext == "" {
		return ""
	}
	if call.HiddenUserMessage && !call.carriesSessionStart {
		return ""
	}
	return message.PromptHookContext(call.promptHookContext)
}
