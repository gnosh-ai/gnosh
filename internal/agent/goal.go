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
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
)

// Goal is a session's goal, as /goal sets one: a condition the agent keeps
// working toward, bound to a goal in the project's mem, which the agent
// plans as steps and closes once the condition holds (MEMPLAN §7). As each
// of the session's turns ends, mem says what comes next: the agent goes on
// with the next step, the turn ends while the goal waits, or, the goal
// closed, a separate check asks the small model whether the condition
// holds. Met, the goal is cleared; not, it is reopened.
type Goal struct {
	Condition string
	// Name and Title are the goal's in mem.
	Name, Title string
	// Checks is how many times the goal was checked, and LastCheck what
	// the last check found ("" before the first).
	Checks    int
	LastCheck string
}

// GoalChange is what setting or clearing a session's goal did: the goal
// set, and Directive, what starts the agent on it; or the goal cleared,
// and Notice, what clearing it did. Had is false when there was no goal to
// clear.
type GoalChange struct {
	Goal              Goal
	Had               bool
	Directive, Notice string
}

// GoalConditionLimit bounds a goal's condition, in characters.
const GoalConditionLimit = 2000

// goalTranscriptLimit bounds how much of the conversation, in characters,
// the goal check is shown: its end, where the agent's latest work is.
const goalTranscriptLimit = 24_000

//go:embed templates/goal_check.md
var goalCheckPrompt []byte

var (
	errNoMem       = errors.New("/goal keeps its goal in mem, which is not available here")
	errGoalChanged = errors.New("the session's goal changed meanwhile; try again")
)

// goalBindings keeps the sessions' /goal bindings, each in its session's row
// (session.Service.Goal), and reaches the project's mem their goals are
// in. A coordinator and its session agents share one; nil keeps none.
type goalBindings struct {
	sessions session.Service
	gnovms   *gnovm.Lazy
	project  string
}

// binding is the session's binding, and the stored text it was read from,
// which swap compares against; ok is false when it has none.
func (g *goalBindings) binding(ctx context.Context, sessionID string) (b gnovm.GoalBinding, stored string, ok bool) {
	if g == nil {
		return b, "", false
	}
	stored, err := g.sessions.Goal(ctx, sessionID)
	if err != nil || stored == "" {
		return b, stored, false
	}
	if err := json.Unmarshal([]byte(stored), &b); err != nil {
		slog.Warn("A session's goal could not be read", "session_id", sessionID, "error", err)
		return b, stored, false
	}
	return b, stored, true
}

// swap stores b as the session's binding, or none for nil, if what is
// stored is still stored, and reports whether it was: a /goal set or
// cleared meanwhile wins.
func (g *goalBindings) swap(ctx context.Context, sessionID, stored string, b *gnovm.GoalBinding) (bool, error) {
	text := ""
	if b != nil {
		data, err := json.Marshal(b)
		if err != nil {
			return false, err
		}
		text = string(data)
	}
	return g.sessions.SwapGoal(ctx, sessionID, stored, text)
}

func (g *goalBindings) client() (*gnovm.Client, error) {
	if g == nil || g.gnovms == nil {
		return nil, errNoMem
	}
	return g.gnovms.Get()
}

func goalOf(b gnovm.GoalBinding) Goal {
	return Goal{Condition: b.Condition, Name: b.Name, Title: b.Title, Checks: b.Checks, LastCheck: b.Verdict}
}

// set binds the session's goal to condition, in place of any it had.
func (g *goalBindings) set(ctx context.Context, sessionID, condition string) (GoalChange, error) {
	client, err := g.client()
	if err != nil {
		return GoalChange{}, err
	}
	prev, stored, had := g.binding(ctx, sessionID)
	var p *gnovm.GoalBinding
	if had {
		p = &prev
	}
	b, directive, err := client.BindGoal(ctx, g.project, sessionID, condition, p)
	if err != nil {
		return GoalChange{}, err
	}
	if ok, err := g.swap(ctx, sessionID, stored, &b); err != nil || !ok {
		return GoalChange{}, cmp.Or(err, errGoalChanged)
	}
	return GoalChange{Goal: goalOf(b), Had: true, Directive: directive}, nil
}

// get is the session's goal, and false when it has none.
func (g *goalBindings) get(ctx context.Context, sessionID string) (Goal, bool) {
	b, _, ok := g.binding(ctx, sessionID)
	return goalOf(b), ok
}

// clear unbinds the session's goal; mem drops the goal if binding added it.
func (g *goalBindings) clear(ctx context.Context, sessionID string) (GoalChange, error) {
	b, stored, ok := g.binding(ctx, sessionID)
	if !ok {
		return GoalChange{}, nil
	}
	notice := "/goal is cleared"
	if client, err := g.client(); err == nil {
		if text, err := client.GoalClear(ctx, g.project, sessionID, b); err != nil {
			slog.Warn("The goal in mem was left as it is", "session_id", sessionID, "error", err)
		} else {
			notice = text
		}
	}
	if ok, err := g.swap(ctx, sessionID, stored, nil); err != nil || !ok {
		return GoalChange{}, cmp.Or(err, errGoalChanged)
	}
	return GoalChange{Goal: goalOf(b), Had: true, Notice: notice}, nil
}

// boundName is the name of the goal in mem the session's /goal is bound
// to, for its mem calls; "" for none.
func (g *goalBindings) boundName(ctx context.Context, sessionID string) string {
	b, _, _ := g.binding(ctx, sessionID)
	return b.Name
}

// goalContinuation reads a main agent's session goal in mem when its turn
// would otherwise end, and reports the follow-up call to queue while the
// goal is open: this same call, keeping the turn's RunID, with what comes
// next as a hidden prompt (gnovm.GoalTurn). Every stopHookReflectEvery
// continuations in a row the agent is asked instead whether there is a
// real reason to stop, with no cap; a reply that says so with StopMarker
// ends the turn with the goal still set. Once the agent closes the goal,
// the small model checks it: met, it is cleared and the user told; not, it
// is reopened in mem and the agent goes on. A goal that waits, or that
// cannot be read or checked, ends the turn still set, and the user is
// told why. ok is false when the turn ends.
func (a *sessionAgent) goalContinuation(ctx context.Context, call SessionAgentCall, lastAssistantMessage string) (SessionAgentCall, bool) {
	if a.isSubAgent || a.goals == nil {
		return SessionAgentCall{}, false
	}
	b, stored, ok := a.goals.binding(ctx, call.SessionID)
	if !ok {
		return SessionAgentCall{}, false
	}
	if call.goalReflecting && stopMarkerPresent(lastAssistantMessage) {
		a.notifyGoal(call.SessionID, notify.TypeHookWarning, fmt.Sprintf(
			"The agent judged it should stop working toward the goal: %s. The goal stays set (/goal clear clears it)",
			cmp.Or(strings.TrimSuffix(compactReason(StripStopMarker(lastAssistantMessage)), "."), "it gave no reason")))
		return SessionAgentCall{}, false
	}
	if a.permissionMode(call.SessionID) == config.PermissionModePlan {
		a.notifyGoal(call.SessionID, notify.TypeHookWarning, "The goal stays set; plan mode does not go on toward it")
		return SessionAgentCall{}, false
	}
	client, err := a.goals.client()
	var turn gnovm.GoalTurn
	next := b
	if err == nil {
		turn, next, err = client.GoalTurnEnd(ctx, a.goals.project, call.SessionID, b, StopMarker)
	}
	if err != nil {
		slog.Warn("The goal could not be read in mem; letting the turn end", "session_id", call.SessionID, "error", err)
		a.notifyGoal(call.SessionID, notify.TypeHookWarning, "The goal could not be read in mem, so the turn ended with the goal still set: "+err.Error())
		return SessionAgentCall{}, false
	}
	cont := call
	cont.goalChecks = call.goalChecks + 1
	cont.goalReflecting = false
	switch turn.Do {
	case "continue":
		cont.Prompt = turn.Prompt
		if cont.goalChecks%stopHookReflectEvery == 0 {
			cont.goalReflecting = true
			cont.Prompt = gnovm.GoalCheckpoint(next, cont.goalChecks, StopMarker)
		}
	case "checkpoint":
		cont.goalReflecting = true
		cont.Prompt = turn.Prompt
	case "judge":
		met, reason, err := a.checkGoal(ctx, call.SessionID, b.Condition, turn.Show)
		if err != nil {
			slog.Warn("The goal check could not be made; letting the turn end", "session_id", call.SessionID, "error", err)
			a.notifyGoal(call.SessionID, notify.TypeHookWarning, "The goal check could not be made, so the turn ended with the goal still set: "+err.Error())
			return SessionAgentCall{}, false
		}
		next.Checks++
		next.Verdict = reason
		if met {
			if ok, _ := a.goals.swap(ctx, call.SessionID, stored, nil); ok {
				a.notifyGoal(call.SessionID, notify.TypeGoalMet, fmt.Sprintf("Goal met: %s (%s)", b.Condition, compactReason(reason)))
			}
			return SessionAgentCall{}, false
		}
		if err := client.GoalReopen(ctx, a.goals.project, next, reason); err != nil {
			a.notifyGoal(call.SessionID, notify.TypeHookWarning, "The goal was found not met, but could not be reopened in mem, so the turn ended with it still set: "+err.Error())
			return SessionAgentCall{}, false
		}
		cont.Prompt = goalNotMetPrompt(next, reason)
	case "rebind":
		rebound, directive, err := client.BindGoal(ctx, a.goals.project, call.SessionID, b.Condition, nil)
		if err != nil {
			a.notifyGoal(call.SessionID, notify.TypeHookWarning, "mem was made anew, and the goal could not be set in it again: "+err.Error())
			return SessionAgentCall{}, false
		}
		next = rebound
		cont.Prompt = directive
	case "clear":
		if ok, _ := a.goals.swap(ctx, call.SessionID, stored, nil); ok {
			a.notifyGoal(call.SessionID, notify.TypeHookWarning, turn.Notice)
		}
		return SessionAgentCall{}, false
	default: // "end"
		_, _ = a.goals.swap(ctx, call.SessionID, stored, &next)
		a.notifyGoal(call.SessionID, notify.TypeHookWarning, turn.Notice)
		return SessionAgentCall{}, false
	}
	// A goal set anew or cleared while this turn's end read it is left as
	// the user left it.
	if ok, err := a.goals.swap(ctx, call.SessionID, stored, &next); err != nil || !ok {
		return SessionAgentCall{}, false
	}
	// Generated, not typed, and the attachments went with the prompt.
	cont.HiddenUserMessage = true
	cont.Attachments = nil
	slog.Info("The goal is open; continuing", "session_id", call.SessionID, "goal", next.Name, "do", turn.Do, "continuation", cont.goalChecks)
	return cont, true
}

// notifyGoal tells the user, in session sessionID, how the goal stands.
func (a *sessionAgent) notifyGoal(sessionID string, kind notify.Type, msg string) {
	if a.notify == nil {
		return
	}
	a.notify.Publish(pubsub.CreatedEvent, notify.Notification{SessionID: sessionID, Type: kind, Message: msg})
}

// checkGoal asks the small model, without tools, whether session
// sessionID's goal holds, from condition, what mem shows of the goal (its
// steps, results and notes: the agent's claims) and the end of the
// conversation; reason is what it found still missing, or what shows it
// holds.
func (a *sessionAgent) checkGoal(ctx context.Context, sessionID, condition, show string) (met bool, reason string, err error) {
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return false, "", err
	}
	prompt := fmt.Sprintf("The goal:\n%s\n\nWhat the agent's memory says of it (its claims, not proof):\n%s\n\nThe end of the conversation:\n%s\n\nDoes the conversation show that the goal holds?",
		condition, cmp.Or(strings.TrimSpace(show), "(nothing)"), goalTranscript(msgs))
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

// goalNotMetPrompt is the hidden prompt that sends the agent back to a
// goal it closed, which the check found not met, and reopened.
func goalNotMetPrompt(b gnovm.GoalBinding, reason string) string {
	return fmt.Sprintf("The check found the goal `%s` not met: %s\n\nThe goal: %s\n\nIt is reopened in mem. Do what is missing, then close it again with `mem.Done(%q, \"<command> → <result>\")`.",
		b.Name, cmp.Or(reason, "the check gave no reason"), b.Condition, b.Name)
}
