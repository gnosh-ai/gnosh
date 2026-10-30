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

// stopHookReflectEvery is how many consecutive "not done"s a Stop hook
// may give before the agent is asked to step back and judge whether it
// should keep going.
//
// There is deliberately no cap. A task can genuinely need any number of
// attempts, and ending the turn on a counter would throw away exactly
// the persistence a Stop hook exists to add. Instead, at each checkpoint
// the agent itself decides whether there is a real reason to stop; if
// there is not, it keeps working. The user can always cancel.
const stopHookReflectEvery = 10

// StopMarker is the sentinel an agent puts on a line by itself, at a
// reflection checkpoint, to say it has judged that it should stop even
// though the Stop hook still objects. The rest of its reply is its
// reason, written for the user. Like the plan markers it is an HTML
// comment, and the UI strips it before rendering (see StripStopMarker).
const StopMarker = "<!-- GNOSH_STOP -->"

// legacyStopMarker is the marker from before Crush was renamed Gnosh. An
// agent may still write it, from a session or a prompt that predates the
// rename, and it counts the same.
const legacyStopMarker = "<!-- CRUSH_STOP -->"

// maxStopReasonLen bounds how much of the agent's reason is quoted in
// the one-line status-bar warning. The full reason is in its reply.
const maxStopReasonLen = 200

// stopHookContinuation runs the configured Stop hooks for a main
// agent's turn that is otherwise finished, or the SubagentStop hooks
// for a sub-agent's, and reports the follow-up call to queue when a
// hook refuses to let the turn end.
//
// The returned call is this same call with its prompt replaced, so it
// keeps the turn's RunID: the turn is not over, and the terminal
// RunComplete is owed by whichever turn finally ends.
//
// ok is false when there are no hooks for the event, when they allow
// the stop, when one halts the turn outright (exit 49), or when the
// agent, asked at a reflection checkpoint, concluded that there is a
// genuine reason to stop.
func (a *sessionAgent) stopHookContinuation(
	ctx context.Context,
	call SessionAgentCall,
	lastAssistantMessage string,
) (SessionAgentCall, bool) {
	if a.cfg == nil {
		return SessionAgentCall{}, false
	}
	// Main-agent turns are gated by Stop; a sub-agent's are gated by
	// SubagentStop, which works the same way.
	eventName := hooks.EventStop
	if a.isSubAgent {
		eventName = hooks.EventSubagentStop
	}
	hooksForEvent := a.cfg.Config().Hooks[eventName]
	if len(hooksForEvent) == 0 {
		return SessionAgentCall{}, false
	}
	// A sub-agent's hooks, as in Claude Code, see the session it works
	// for as session_id, and its own as agent_id; their warnings go to
	// that session too, the one the user is in, which is all `gnosh run`
	// prints warnings for.
	userSession := call.SessionID
	if a.isSubAgent && a.sessions != nil {
		sess, err := a.sessions.Get(ctx, call.SessionID)
		switch {
		case err != nil:
			slog.Warn("A sub-agent's session could not be read; its SubagentStop hooks see it as the session",
				"session_id", call.SessionID,
				"error", err,
			)
		case sess.ParentSessionID != "":
			userSession = sess.ParentSessionID
		}
	}

	// At a reflection checkpoint the agent was asked whether there is a
	// good reason to stop. If it concluded there is, that judgement ends
	// the turn. The hook is not consulted again: it would only repeat
	// the objection the agent has just weighed. The marker counts only at
	// a checkpoint, so the rest of the time the hook stays in charge.
	if call.stopHookReflecting && stopMarkerPresent(lastAssistantMessage) {
		slog.Info("Agent judged at a Stop hook checkpoint that it should stop",
			"session_id", call.SessionID,
			"attempts", call.stopHookDepth,
		)
		a.warnAgentChoseToStop(call, userSession, eventName, StripStopMarker(lastAssistantMessage))
		return SessionAgentCall{}, false
	}

	// Every hook command needs a directory to run in. A store without
	// one would make each hook fail to launch, which is indistinguishable
	// from a hook that approved the stop, so resolve it explicitly and
	// bail loudly rather than silently letting the turn end.
	workingDir := a.cfg.WorkingDir()
	if workingDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			slog.Warn("Stop hooks are configured but no working directory is available; letting the turn end",
				"session_id", call.SessionID,
				"error", err,
			)
			return SessionAgentCall{}, false
		}
		workingDir = wd
	}

	runner := hooks.NewRunner(hooksForEvent, workingDir, workingDir)
	var (
		result hooks.AggregateResult
		err    error
	)
	if a.isSubAgent {
		result, err = runner.RunSubagentStop(ctx, userSession, call.SessionID, a.agentType, lastAssistantMessage, call.stopHookDepth > 0)
	} else {
		result, err = runner.RunStop(ctx, call.SessionID, lastAssistantMessage, call.stopHookDepth > 0)
	}
	a.warnFailedStopHooks(userSession, eventName, result.Hooks, err != nil || result.Halt || result.Decision != hooks.DecisionDeny)
	a.warnHookMessages(userSession, eventName, result.Hooks)
	if err != nil {
		slog.Warn("Stop hook execution error; letting the turn end",
			"session_id", call.SessionID,
			"error", err,
		)
		return SessionAgentCall{}, false
	}
	// Halt is the explicit "end now" answer and wins over a deny, so a
	// hook can call off a continuation it started itself.
	if result.Halt || result.Decision != hooks.DecisionDeny {
		return SessionAgentCall{}, false
	}

	reason := result.Reason
	if reason == "" {
		reason = fmt.Sprintf("A %s hook blocked the end of this turn.", eventName)
	}
	next := call
	next.stopHookDepth = call.stopHookDepth + 1
	next.stopHookReason = reason
	next.stopHookReflecting = next.stopHookDepth%stopHookReflectEvery == 0
	if next.stopHookReflecting {
		next.Prompt = stopHookReflectionPrompt(eventName, next.stopHookDepth, reason)
	} else {
		next.Prompt = stopHookContinuationPrompt(eventName, reason)
	}
	// The continuation is generated, not typed: keep it out of the
	// transcript the user reads.
	next.HiddenUserMessage = true
	// Attachments belong to the user's original prompt and were already
	// delivered on this turn; re-sending them would duplicate them.
	next.Attachments = nil

	slog.Info("Stop hook blocked the end of the turn; continuing",
		"event", eventName,
		"session_id", call.SessionID,
		"attempt", next.stopHookDepth,
		"reflecting", next.stopHookReflecting,
		"reason", reason,
	)
	return next, true
}

// stopHookContinuationPrompt is the ordinary "not done yet" prompt, for
// event's hooks (Stop or SubagentStop).
func stopHookContinuationPrompt(event, reason string) string {
	return fmt.Sprintf(
		"A %s hook blocked the end of this turn:\n\n%s\n\n"+
			"Keep working on the original request until this is resolved.",
		event, reason,
	)
}

// stopHookReflectionPrompt is sent every stopHookReflectEvery
// consecutive blocks. It asks the agent to judge whether there is a
// genuine reason to stop, and tells it how to say so; absent such a
// reason it is told plainly to keep going.
func stopHookReflectionPrompt(event string, attempts int, reason string) string {
	return fmt.Sprintf(
		"A %s hook has now refused to let this turn end %d times in a row. "+
			"Its latest objection:\n\n%s\n\n"+
			"Before continuing, step back and judge honestly whether there is a "+
			"genuine reason to stop — for example: the task cannot be done as "+
			"asked; it is blocked on something only the user can provide or "+
			"decide; the hook's check is wrong or cannot pass in this "+
			"environment; or you are repeating the same attempts without making "+
			"progress.\n\n"+
			"If there is such a reason, explain it clearly for the user, then end "+
			"your reply with this line on its own:\n%s\n\n"+
			"Otherwise, do not stop: keep working on the original request. Taking "+
			"many attempts is fine.",
		event, attempts, reason, StopMarker,
	)
}

// stopMarkerPresent reports whether text contains StopMarker on a line by
// itself. Line-scoped, like the plan markers, so an agent merely
// mentioning the marker in prose does not end the turn; a line wrapped in
// inline-code backticks still counts, since some models add them.
func stopMarkerPresent(text string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		if stopMarkerLine(line) {
			return true
		}
	}
	return false
}

func stopMarkerLine(line string) bool {
	marker := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "`"))
	return marker == StopMarker || marker == legacyStopMarker
}

// StripStopMarker removes lines consisting solely of StopMarker, so the
// sentinel never shows in rendered output. Mentions inside prose are left
// alone, matching stopMarkerPresent.
func StripStopMarker(text string) string {
	if !strings.Contains(text, StopMarker) && !strings.Contains(text, legacyStopMarker) {
		return text
	}
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !stopMarkerLine(line) {
			kept = append(kept, line)
		}
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n")
}

// warnFailedStopHooks tells the user about any Stop or SubagentStop hook
// that did not run to a verdict; ended says whether the turn ended.
//
// A PreToolUse hook that fails shows on its tool's line, but a Stop hook
// has no line of its own: it runs as the turn ends, and its objections
// reach the model as hidden prompts. A hook that could not check blocks,
// so the turn goes on, unless another hook halts it; without this warning
// the user would not know why either way.
func (a *sessionAgent) warnFailedStopHooks(sessionID, event string, ran []hooks.HookInfo, ended bool) {
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
			Message:   fmt.Sprintf("%s hook %q could not evaluate: %s; %s", event, h.Name, h.Error, stopHookOutcome(ended)),
		})
	}
}

// stopHookOutcome says what became of the turn after a hook that could
// not check ran as it ended: it goes on, unless another hook halted it.
func stopHookOutcome(ended bool) string {
	if ended {
		return "the turn ended without its check"
	}
	return "the turn goes on, since a check that could not run does not pass"
}

// warnAgentChoseToStop tells the user, in sessionID, the agent ended the
// turn over event's hook's objection, having been asked to judge whether
// it should. The hook's objections reached the model as hidden prompts,
// so this is the only place the user learns their gate was overruled,
// and why.
func (a *sessionAgent) warnAgentChoseToStop(call SessionAgentCall, sessionID, event, agentReason string) {
	if a.notify == nil {
		return
	}
	msg := fmt.Sprintf(
		"The %s hook said \"not done\" %d times; the agent judged it should stop",
		event, call.stopHookDepth,
	)
	if r := compactReason(agentReason); r != "" {
		msg += ": " + r
	} else {
		msg += " without giving a reason"
	}
	if call.stopHookReason != "" {
		msg += ". Hook's last objection: " + call.stopHookReason
	}
	a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
		SessionID: sessionID,
		Type:      notify.TypeHookWarning,
		Message:   msg,
	})
}

// compactReason flattens the agent's reason onto one line and bounds it
// by rune, so a cut never splits a character.
func compactReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxStopReasonLen {
		s = string(r[:maxStopReasonLen]) + "…"
	}
	return s
}
