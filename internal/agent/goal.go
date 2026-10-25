package agent

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// Goal is a session's goal, as Claude Code's /goal sets one: a condition
// the agent keeps working toward. Each time the agent's turn would end, a
// separate check asks the small model whether the conversation shows the
// condition holds; until it does, the turn goes on. Met, the goal is
// cleared.
type Goal struct {
	Condition string
	// Checks is how many times the goal was checked, and LastCheck what
	// the last check found still missing ("" before the first).
	Checks    int
	LastCheck string
}

// GoalConditionLimit bounds a goal's condition, in characters.
const GoalConditionLimit = 2000

// goalTranscriptLimit bounds how much of the conversation, in characters,
// the goal check is shown: its end, where the agent's latest work is.
const goalTranscriptLimit = 24_000

//go:embed templates/goal_check.md
var goalCheckPrompt []byte

// GoalDirective is the prompt that sets the agent working toward a goal
// once it is set, as Claude Code words it for its /goal.
func GoalDirective(condition string) string {
	return fmt.Sprintf("A goal is now set for this session: %q. Briefly acknowledge the goal, "+
		"then immediately start (or continue) working toward it; treat the condition itself as "+
		"your directive and do not pause to ask the user what to do. Each time your turn would "+
		"end, a separate check decides whether the condition holds, and you are kept working "+
		"until it does. The goal clears itself once it is met: do not tell the user to run "+
		"/goal clear after success; that is only for clearing a goal early.", condition)
}

// goalContinuation checks a main agent's session goal when its turn would
// otherwise end, and reports the follow-up call to queue while the goal
// does not hold: this same call, keeping the turn's RunID, with what the
// check found missing as a hidden prompt. A goal found met is cleared, and
// the user told. Every stopHookReflectEvery checks in a row the agent is
// asked whether there is a genuine reason to stop, as a Stop hook asks it,
// with no cap; when it judges there is, or the check cannot be made, the
// turn ends with the goal still set, and the user is told. ok is false
// when the turn ends.
func (a *sessionAgent) goalContinuation(ctx context.Context, call SessionAgentCall, lastAssistantMessage string) (SessionAgentCall, bool) {
	if a.isSubAgent || a.goals == nil {
		return SessionAgentCall{}, false
	}
	goal, ok := a.goals.Get(call.SessionID)
	if !ok {
		return SessionAgentCall{}, false
	}
	if call.goalReflecting && stopMarkerPresent(lastAssistantMessage) {
		a.notifyGoal(call.SessionID, notify.TypeHookWarning, fmt.Sprintf(
			"The goal was found not met %d times in a row; the agent judged it should stop: %s. The goal stays set (/goal clear clears it)",
			call.goalChecks, cmp.Or(strings.TrimSuffix(compactReason(StripStopMarker(lastAssistantMessage)), "."), "it gave no reason")))
		return SessionAgentCall{}, false
	}
	met, reason, err := a.checkGoal(ctx, call.SessionID, goal.Condition)
	if err != nil {
		slog.Warn("The goal check could not be made; letting the turn end", "session_id", call.SessionID, "error", err)
		a.notifyGoal(call.SessionID, notify.TypeHookWarning, "The goal check could not be made, so the turn ended with the goal still set: "+err.Error())
		return SessionAgentCall{}, false
	}
	// A goal cleared or set anew while it was checked is left as the user
	// left it: a new one is checked when the turn its directive starts ends.
	if now, ok := a.goals.Get(call.SessionID); !ok || now.Condition != goal.Condition {
		return SessionAgentCall{}, false
	}
	goal.Checks++
	if met {
		a.goals.Del(call.SessionID)
		a.notifyGoal(call.SessionID, notify.TypeGoalMet, fmt.Sprintf("Goal met: %s (%s)", goal.Condition, compactReason(reason)))
		return SessionAgentCall{}, false
	}
	goal.LastCheck = reason
	a.goals.Set(call.SessionID, goal)
	next := call
	next.goalChecks = call.goalChecks + 1
	next.goalReflecting = next.goalChecks%stopHookReflectEvery == 0
	if next.goalReflecting {
		next.Prompt = goalReflectionPrompt(goal.Condition, next.goalChecks, reason)
	} else {
		next.Prompt = goalContinuationPrompt(goal.Condition, reason)
	}
	// Generated, not typed, and the attachments went with the prompt.
	next.HiddenUserMessage = true
	next.Attachments = nil
	slog.Info("The goal does not hold yet; continuing", "session_id", call.SessionID, "check", next.goalChecks, "reason", reason)
	return next, true
}

// notifyGoal tells the user, in session sessionID, how the goal stands.
func (a *sessionAgent) notifyGoal(sessionID string, kind notify.Type, msg string) {
	if a.notify == nil {
		return
	}
	a.notify.Publish(pubsub.CreatedEvent, notify.Notification{SessionID: sessionID, Type: kind, Message: msg})
}

// checkGoal asks the small model, without tools, whether the end of
// session sessionID's conversation shows that condition holds; reason is
// what it found still missing, or what shows it holds.
func (a *sessionAgent) checkGoal(ctx context.Context, sessionID, condition string) (met bool, reason string, err error) {
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return false, "", err
	}
	prompt := fmt.Sprintf("The goal:\n%s\n\nThe end of the conversation:\n%s\n\nDoes the conversation show that the goal holds?",
		condition, goalTranscript(msgs))
	resp, _, err := a.askForName(ctx, sessionID, "goal check", goalCheckPrompt, prompt, 300)
	if err != nil {
		return false, "", err
	}
	return parseGoalCheck(resp.Response.Content.Text())
}

// parseGoalCheck reads the goal check's verdict: the JSON object it was
// asked for, anywhere in its answer, past any thinking.
func parseGoalCheck(answer string) (met bool, reason string, err error) {
	answer = orphanThinkTagRegex.ReplaceAllString(thinkTagRegex.ReplaceAllString(answer, ""), "")
	start, end := strings.Index(answer, "{"), strings.LastIndex(answer, "}")
	var verdict struct {
		OK     *bool  `json:"ok"`
		Reason string `json:"reason"`
	}
	if start < 0 || end < start || json.Unmarshal([]byte(answer[start:end+1]), &verdict) != nil || verdict.OK == nil {
		return false, "", errors.New("its answer was not a verdict: " + compactReason(answer))
	}
	return *verdict.OK, strings.TrimSpace(verdict.Reason), nil
}

// goalTranscript is the end of a conversation as the goal check reads it,
// at most goalTranscriptLimit characters: the prompts typed, what the
// agent said and called, and what its tools returned, each cut short.
func goalTranscript(msgs []message.Message) string {
	transcript := strings.Join(transcriptParts(msgs, clip), "\n\n")
	if r := []rune(transcript); len(r) > goalTranscriptLimit {
		transcript = "…" + string(r[len(r)-goalTranscriptLimit:])
	}
	return transcript
}

// transcriptParts is a conversation as text, a part each: the prompts
// typed, what the agent said and called, and what its tools returned, each
// passed through cut with the length the goal check keeps of it.
func transcriptParts(msgs []message.Message, cut func(s string, n int) string) []string {
	var parts []string
	for _, msg := range msgs {
		switch msg.Role {
		case message.User:
			if text := msg.Content(); !text.Hidden && text.Text != "" {
				parts = append(parts, "User: "+cut(text.Text, 2_000))
			}
		case message.Assistant:
			if text := strings.TrimSpace(msg.Content().Text); text != "" {
				parts = append(parts, "Agent: "+cut(text, 4_000))
			}
			for _, call := range msg.ToolCalls() {
				parts = append(parts, fmt.Sprintf("Agent called %s: %s", call.Name, cut(call.Input, 500)))
			}
		case message.Tool:
			for _, result := range msg.ToolResults() {
				parts = append(parts, fmt.Sprintf("%s returned: %s", result.Name, cut(result.Content, 2_000)))
			}
		}
	}
	return parts
}

// clip cuts s to its first n characters, saying how much it left out.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + fmt.Sprintf(" … (%d more characters)", len(r)-n)
}

// goalContinuationPrompt is the hidden prompt that keeps the agent working
// toward a goal the check found not met.
func goalContinuationPrompt(condition, reason string) string {
	return fmt.Sprintf("The goal for this session is not met yet: %s\n\nThe goal: %s\n\nKeep working toward it.",
		cmp.Or(reason, "the check gave no reason"), condition)
}

// goalReflectionPrompt is sent every stopHookReflectEvery checks in a row
// that found the goal not met. It asks the agent to judge whether there is
// a genuine reason to stop, and how to say so; absent one, it keeps going.
func goalReflectionPrompt(condition string, checks int, reason string) string {
	return fmt.Sprintf(
		"The goal check has now found the goal not met %d times in a row. Its latest finding:\n\n%s\n\nThe goal: %s\n\n"+
			"Before continuing, step back and judge honestly whether there is a genuine reason to stop: for example, "+
			"the goal cannot be reached as set; it is blocked on something only the user can provide or decide; the "+
			"check is wrong or cannot pass here; or you are repeating the same attempts without making progress.\n\n"+
			"If there is such a reason, explain it clearly for the user, then end your reply with this line on its own:\n%s\n\n"+
			"Otherwise, do not stop: keep working toward the goal. Taking many attempts is fine.",
		checks, cmp.Or(reason, "the check gave no reason"), condition, StopMarker)
}
