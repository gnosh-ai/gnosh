package memjoin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/gnovm"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// This file is /goal (integration-contract.md section 9, mem.md section
// 13): a session is bound to a top-level todo in mem, and as each of its
// turns would end, the goal table that came with mem says what comes
// next: the agent goes on, the turn ends, or a judge is asked whether the
// goal, closed as done, holds. No count ever ends the work.

// maxCondition is the most characters a goal's condition may have.
const maxCondition = 2000

// goalState is the session's binding as the join holds it. It is guarded
// by the join's lock.
type goalState struct {
	// raw is the binding as it is stored with the session: a GoalBinding
	// as JSON, or "" when the session is bound to nothing. It is changed
	// only by compare-and-set.
	raw string
	// asked says the agent was last sent the question whether what the
	// goal waits for has happened: a reply to it that ends with the stop
	// marker ends the turn with the goal still set.
	asked bool
	// directive is the directive of the goal that `gnosh -p --goal`
	// bound as the run began, until it has gone out with the run's first
	// prompt or as that prompt.
	directive string
}

// load reads the binding in force from the session's log: the newest one
// written under the session's own id. A copy of another session's rows
// carries that session's binding, and a copy is bound to nothing.
func (g *goalState) load(session string, entries []transcript.Entry) {
	for i := len(entries) - 1; i >= 0; i-- {
		if e := &entries[i]; e.Type == transcript.TypeMeta && e.Key == metaGoal && e.SessionID == session {
			g.raw = e.Value
			return
		}
	}
}

// binding returns the goal the session is bound to, the text it is stored
// as, and whether it is bound at all.
func (j *join) binding() (gnovm.GoalBinding, string, bool) {
	j.mu.Lock()
	raw := j.goal.raw
	j.mu.Unlock()
	var b gnovm.GoalBinding
	if raw == "" || json.Unmarshal([]byte(raw), &b) != nil || b.Name == "" {
		return gnovm.GoalBinding{}, raw, false
	}
	return b, raw, true
}

// swapGoal stores a new binding in place of old, and reports whether it
// did. It does nothing when the binding is no longer old: whoever changed
// it meanwhile (the person, with /goal) is left with what they set. The
// binding is written to the session's log as an entry of its own, which
// no other write of the session touches.
func (j *join) swapGoal(old, next string) bool {
	j.mu.Lock()
	if j.goal.raw != old {
		j.mu.Unlock()
		return false
	}
	if old != next {
		if _, err := j.ts.Append(transcript.MetaEntry(metaGoal, next)); err != nil {
			j.mu.Unlock()
			return false
		}
		j.goal.raw = next
	}
	j.mu.Unlock()
	// The front end's status line names the goal the session is bound to.
	j.showGoal()
	return true
}

// encodeGoal writes a binding as it is stored.
func encodeGoal(b gnovm.GoalBinding) string {
	raw, err := json.Marshal(b)
	if err != nil {
		return ""
	}
	return string(raw)
}

// boundGoal is the tools' BoundGoal slot: the name of the todo the
// session's /goal binds, which the session's own calls may not drop or
// forget.
func (j *join) boundGoal(context.Context, string) string {
	b, _, ok := j.binding()
	if !ok {
		return ""
	}
	return b.Name
}

// slash lists the slash commands the join adds.
func (j *join) slash() []app.SlashCommand {
	return []app.SlashCommand{
		{
			Name:    "mem",
			Args:    "[show <name> | find <words> | log [n] | undo [entry] | forget <name> | redact <text> | repair [vm] | help]",
			Summary: "See and correct what mem holds for this project.",
			InRun:   true,
			// What follows these two words is what the person wants
			// gone: a secret's own text, and the name of what is erased
			// with its history. A front end shows and remembers such a
			// line only as far as the word.
			Sensitive: []string{"redact", "forget"},
			Run:       j.memSlash,
		},
		{
			Name:    "goal",
			Args:    "[condition | clear]",
			Summary: "Keep working until a condition holds; a separate check confirms it. /goal alone shows the goal, /goal clear ends it.",
			Run:     j.goalSlash,
		},
	}
}

// goalSlash is /goal.
func (j *join) goalSlash(ctx context.Context, c *app.SlashContext) error {
	args := strings.TrimSpace(c.Args)
	switch strings.ToLower(args) {
	case "":
		b, _, ok := j.binding()
		if !ok {
			c.Say("No goal is set. /goal <condition> sets one: /goal go test ./... passes")
			return nil
		}
		c.Say("Goal: %s", b.Condition)
		c.Say("In mem it is `%s` %q.", b.Name, b.Title)
		switch {
		case b.Verdict != "":
			c.Say("The check has run %d time(s), and last said: %s", b.Checks, b.Verdict)
		default:
			c.Say("The check has not run yet: it runs when the agent closes the goal.")
		}
		return nil
	case "clear", "off", "reset", "none", "cancel":
		b, raw, ok := j.binding()
		if !ok {
			c.Say("No goal is set.")
			return nil
		}
		client, err := j.vms.Get()
		if err != nil {
			return fmt.Errorf("mem cannot be reached (%v), so the goal stays set", err)
		}
		notice, err := client.GoalClear(ctx, j.project, j.id, b)
		if err != nil {
			return fmt.Errorf("the goal could not be cleared in mem (%v), so it stays set", err)
		}
		if !j.swapGoal(raw, "") {
			return errors.New("the goal changed while it was being cleared, so it was left as it is now; /goal shows it")
		}
		c.Say("%s", notice)
		return nil
	}
	if !j.run {
		return errors.New("this session has no gnovm_run tool, so it cannot work a goal in mem; nothing was set")
	}
	if n := utf8.RuneCountInString(args); n > maxCondition {
		return fmt.Errorf("a goal's condition may be %d characters, and this one is %d; nothing was set", maxCondition, n)
	}
	b, directive, err := j.bind(ctx, args)
	if err != nil {
		return err
	}
	c.Say("Goal set: %s (`%s` in mem)", b.Condition, b.Name)
	if j.planMode() {
		// The goal is the person's to set in any mode. What plan mode
		// holds back is the work: the model may plan for the goal, and
		// is not sent back to it as its turns end (mem.md section 17).
		c.Say("Plan mode is on: the agent may plan for the goal, and is not sent back to it until you leave plan mode.")
	}
	return j.sendDirective(c.Agent, directive)
}

// bind binds the session to the goal for condition and stores the
// binding. It returns the binding and the directive to send the agent.
func (j *join) bind(ctx context.Context, condition string) (gnovm.GoalBinding, string, error) {
	client, err := j.vms.Get()
	if err != nil {
		return gnovm.GoalBinding{}, "", fmt.Errorf("mem cannot be reached (%v), so no goal was set", err)
	}
	prev, raw, bound := j.binding()
	var earlier *gnovm.GoalBinding
	if bound {
		earlier = &prev
	}
	b, directive, err := client.BindGoal(ctx, j.project, j.id, condition, earlier)
	if err != nil {
		return gnovm.GoalBinding{}, "", fmt.Errorf("the goal could not be set in mem: %v", err)
	}
	if !j.swapGoal(raw, encodeGoal(b)) {
		return gnovm.GoalBinding{}, "", errors.New("the goal changed while it was being set, so it was left as it is now; /goal shows it")
	}
	j.mu.Lock()
	j.goal.asked = false
	j.mu.Unlock()
	return b, directive, nil
}

// sendDirective sends the agent the goal's directive as a prompt of
// gnosh's own. While the agent is busy it waits in the queue.
func (j *join) sendDirective(a *agent.Agent, directive string) error {
	if a == nil {
		a = j.theAgent()
	}
	if a == nil {
		return errors.New("the goal is set, and the agent could not be told of it: the session has no agent yet")
	}
	if _, err := a.Submit(agent.Prompt{Text: prompt.Wrap(directive), Hidden: true, Source: srcGoal}); err != nil {
		return fmt.Errorf("the goal is set, and the agent could not be told of it: %v", err)
	}
	return nil
}

// armGoalFlag is `gnosh -p --goal`: the goal is bound before the run's
// prompt is composed. Its directive then goes out with that prompt
// (promptLines), or, when the run was given no prompt at all, as the
// prompt (runPrompt).
func (j *join) armGoalFlag() {
	condition := strings.TrimSpace(j.s.Invocation.Goal)
	if condition == "" || j.s.Invocation.Command != cli.CmdPrint {
		return
	}
	notice := func(text string) { j.tell(agent.LevelWarning, "goal", text) }
	switch {
	case !j.run:
		notice("--goal needs the gnovm_run tool, which this run does not have, so no goal was set.")
		return
	case utf8.RuneCountInString(condition) > maxCondition:
		notice(fmt.Sprintf("--goal: a goal's condition may be %d characters, and this one is longer, so no goal was set.", maxCondition))
		return
	}
	b, directive, err := j.bind(context.Background(), condition)
	if err != nil {
		notice("--goal: " + err.Error())
		return
	}
	notice(fmt.Sprintf("Goal set: %s (`%s` in mem)", b.Condition, b.Name))
	j.mu.Lock()
	j.goal.directive = directive
	j.mu.Unlock()
}

// takeDirective returns the directive of the goal that --goal bound, if
// it has not gone out yet, and from then on it has.
func (j *join) takeDirective() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	text := j.goal.directive
	j.goal.directive = ""
	return text
}

// runPrompt is Contribution.RunPrompt: a run that was given no prompt, on
// its command line or on standard input, asks for one. A run started
// with --goal has one: the goal's directive, sent as /goal sends it in a
// session, as a prompt of gnosh's own. So `gnosh -p --goal "the tests
// pass"` is a whole command line.
func (j *join) runPrompt(context.Context) (agent.Prompt, bool) {
	text := j.takeDirective()
	if text == "" {
		return agent.Prompt{}, false
	}
	return agent.Prompt{Text: prompt.Wrap(text), Hidden: true, Source: srcGoal}, true
}

// runEnd is Contribution.RunEnd: a run that ends while its goal is still
// set says so and exits non-zero.
func (j *join) runEnd(context.Context, app.RunEnd) int {
	b, _, ok := j.binding()
	if !ok || strings.TrimSpace(j.s.Invocation.Goal) == "" {
		return cli.ExitOK
	}
	text := fmt.Sprintf("The goal is still set: %s (`%s` in mem).", b.Condition, b.Name)
	if b.Verdict != "" {
		text += " The check last said: " + b.Verdict
	}
	j.tell(agent.LevelWarning, "goal", text)
	return cli.ExitFailure
}

// goalDriver is the turn-end check of /goal. It is asked after the
// close-check's send-back and before the person's Stop hooks.
func (j *join) goalDriver() agent.Driver {
	return agent.Driver{Name: "mem-goal", Every: 10, Marker: prompt.StopMarker, Decide: j.goalDecide}
}

// goalDecide says what happens as a turn would end.
//
// First, every top-level todo the session closed as done in this turn is
// judged, the bound one and those the close review approved apart: all
// goals are judged before they count as done. Then the goal table is
// asked about the bound todo, and what it says is done: go on, ask
// whether there is a reason to stop, end the turn, judge, bind again, or
// clear.
func (j *join) goalDecide(ctx context.Context, end agent.TurnEnd) (agent.Verdict, error) {
	b, raw, bound := j.binding()
	if j.planMode() {
		// In plan mode /goal sends no continuations, and nothing is
		// judged: the model could not act on either.
		return agent.Verdict{Action: agent.End}, nil
	}
	// A session that is bound to no goal and called mem in this turn not
	// once has closed nothing: mem is not asked, and so nothing is
	// started for a session that never uses it.
	j.mu.Lock()
	used := j.turn.memCalls > 0
	j.mu.Unlock()
	if !bound && !used {
		return agent.Verdict{Action: agent.End}, nil
	}
	client, err := j.vms.Get()
	if err != nil {
		if bound {
			return agent.Verdict{Action: agent.End, Notice: fmt.Sprintf("The goal `%s` stays set, and nothing was done about it as this turn ended: mem cannot be reached (%v).", b.Name, err)}, nil
		}
		return agent.Verdict{Action: agent.End}, nil
	}

	except := ""
	if bound {
		except = b.Name
	}
	if v, sent := j.judgeClosed(ctx, client, except); sent {
		return v, nil
	}
	if !bound {
		return agent.Verdict{Action: agent.End}, nil
	}

	// A reply to the question whether there is a reason to stop that ends
	// with the stop marker ends the turn, with the goal still set.
	j.mu.Lock()
	asked := j.goal.asked
	j.goal.asked = false
	j.mu.Unlock()
	if asked && prompt.EndsWithMarker(end.Text, end.Marker) {
		return agent.Verdict{Action: agent.End, Notice: fmt.Sprintf(
			"The agent chose to stop working toward the goal `%s` for now; it stays set. Its reason is in its reply.", b.Name)}, nil
	}

	turn, nb, err := client.GoalTurnEnd(ctx, j.project, j.id, b, end.Marker)
	if err != nil {
		if ctx.Err() != nil {
			return agent.Verdict{}, ctx.Err()
		}
		return agent.Verdict{Action: agent.End, Notice: fmt.Sprintf("The goal `%s` stays set, and nothing was done about it as this turn ended: mem could not be read (%v).", b.Name, err)}, nil
	}
	if next := encodeGoal(nb); next != raw {
		if !j.swapGoal(raw, next) {
			// The person changed the goal meanwhile: it is left as they
			// left it, and the turn ends.
			return agent.Verdict{Action: agent.End}, nil
		}
		raw = next
	}
	objection := fmt.Sprintf("the goal `%s` is not met yet", nb.Name)
	switch turn.Do {
	case "continue":
		return agent.Verdict{
			Action:     agent.Continue,
			Prompt:     prompt.Wrap(turn.Prompt),
			Checkpoint: prompt.Wrap(gnovm.GoalCheckpoint(nb, end.Streak+1, end.Marker)),
			Objection:  objection,
		}, nil
	case "checkpoint":
		j.mu.Lock()
		j.goal.asked = true
		j.mu.Unlock()
		return agent.Verdict{Action: agent.Continue, Prompt: prompt.Wrap(turn.Prompt), Checkpoint: prompt.Wrap(turn.Prompt), Objection: objection}, nil
	case "judge":
		return j.judgeGoal(ctx, client, nb, raw, turn.Show), nil
	case "rebind":
		rb, directive, err := j.bind(ctx, nb.Condition)
		if err != nil {
			return agent.Verdict{Action: agent.End, Notice: fmt.Sprintf("mem was made anew, and the goal could not be bound again (%v).", err)}, nil
		}
		return agent.Verdict{
			Action:    agent.Continue,
			Prompt:    prompt.Wrap(directive),
			Objection: fmt.Sprintf("the goal `%s` was bound again", rb.Name),
			Notice:    firstOf(turn.Notice, "mem was made anew, so the goal is bound again"),
		}, nil
	case "clear":
		j.swapGoal(raw, "")
		return agent.Verdict{Action: agent.End, Notice: firstOf(turn.Notice, "/goal is cleared")}, nil
	}
	// "end", and anything a later goal table may say that this build does
	// not know: the turn ends, and the goal stays set.
	return agent.Verdict{Action: agent.End, Notice: turn.Notice}, nil
}

// judgeClosed judges the top-level todos the session closed as done in
// this turn. It returns the verdict that sends the agent back when one of
// them does not hold.
func (j *join) judgeClosed(ctx context.Context, client *gnovm.Client, except string) (agent.Verdict, bool) {
	j.mu.Lock()
	since := j.turn.since()
	j.mu.Unlock()
	closed, err := client.ClosedGoals(ctx, j.project, j.id, since, except)
	if err != nil || len(closed) == 0 {
		return agent.Verdict{}, false
	}
	var told []string
	for _, g := range closed {
		j.mu.Lock()
		seq, done := j.review.judged[g.Name]
		j.mu.Unlock()
		if (done && seq == g.Seq) || j.reviewed(g.Name, g.Show) {
			continue
		}
		v, err := j.judge(ctx, g.Title, g.Show)
		if ctx.Err() != nil {
			return agent.Verdict{}, false
		}
		j.mu.Lock()
		if j.review.judged == nil {
			j.review.judged = map[string]int64{}
		}
		j.review.judged[g.Name] = g.Seq
		j.mu.Unlock()
		switch {
		case err != nil && j.sentBackUnchecked(g.Name):
			told = append(told, fmt.Sprintf("`%s` %q was closed as done, and the check of it could not run (%v), so it stands unchecked.", g.Name, g.Title, err))
		case err != nil:
			// A close nothing checked does not end the turn the first
			// time: the agent is sent back once to show it with its
			// tools, and the next close of it stands, checked or not, so
			// a judge that cannot run never traps the turn.
			reason := fmt.Sprintf("its check could not run (%v), so nothing has shown that it holds. Show it with your tools, running what proves it and reading what that returns", err)
			notice := fmt.Sprintf("`%s` %q was closed as done, and the check of it could not run (%v). It is open again, and the agent goes back to show it.", g.Name, g.Title, err)
			if rerr := client.GoalReopen(ctx, j.project, gnovm.GoalBinding{Name: g.Name, Title: g.Title}, reason); rerr != nil {
				told = append(told, fmt.Sprintf("`%s` %q was closed as done, and the check of it could not run (%v), so it stands unchecked.", g.Name, g.Title, err))
				continue
			}
			told = append(told, notice)
			return agent.Verdict{
				Action:    agent.Continue,
				Prompt:    j.texts.GoalNotMet(g.Name, g.Title, reason),
				Objection: fmt.Sprintf("the check of `%s` could not run: %v", g.Name, err),
				Notice:    strings.Join(told, "\n"),
			}, true
		case v.ok:
			told = append(told, fmt.Sprintf("Task done: %q (`%s`): %s", g.Title, g.Name, v.shown()))
		default:
			notice := fmt.Sprintf("The check of `%s` %q found it not met: %s. It is open again, and the agent goes back to it.", g.Name, g.Title, strings.TrimRight(v.reason, ". "))
			if err := client.GoalReopen(ctx, j.project, gnovm.GoalBinding{Name: g.Name, Title: g.Title}, v.reason); err != nil {
				notice = fmt.Sprintf("The check of `%s` %q found it not met: %s. It could not be opened again in mem (%v); the agent goes back to it.", g.Name, g.Title, strings.TrimRight(v.reason, ". "), err)
			}
			told = append(told, notice)
			return agent.Verdict{
				Action:    agent.Continue,
				Prompt:    j.texts.GoalNotMet(g.Name, g.Title, v.reason),
				Objection: fmt.Sprintf("the check of `%s` found it not met: %s", g.Name, v.reason),
				Notice:    strings.Join(told, "\n"),
			}, true
		}
	}
	for _, line := range told {
		j.tell(agent.LevelWarning, "goal", line)
	}
	return agent.Verdict{}, false
}

// sentBackUnchecked reports whether the agent was already sent back once
// for a close of the todo that its check could not judge, and records
// that it now is.
func (j *join) sentBackUnchecked(name string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.review.unchecked[name] {
		return true
	}
	if j.review.unchecked == nil {
		j.review.unchecked = map[string]bool{}
	}
	j.review.unchecked[name] = true
	return false
}

// judgeGoal asks the judge about the bound goal, which the agent closed
// as done, and acts on the verdict: met, the session is unbound and the
// person told; not met, the todo is opened again and the agent sent back;
// and when the judge cannot be reached, the turn ends with the goal still
// set.
func (j *join) judgeGoal(ctx context.Context, client *gnovm.Client, b gnovm.GoalBinding, raw, show string) agent.Verdict {
	v, err := j.judge(ctx, b.Condition, show)
	if err != nil {
		return agent.Verdict{Action: agent.End, Notice: fmt.Sprintf(
			"The goal `%s` was closed as done, and the check of it could not run (%v). It stays set; the check runs again as the next turn ends.", b.Name, err)}
	}
	b.Checks++
	if v.ok {
		j.swapGoal(raw, "")
		return agent.Verdict{Action: agent.End, Notice: fmt.Sprintf("Goal met: %s (%s)", b.Condition, v.shown())}
	}
	b.Verdict = v.reason
	notice := fmt.Sprintf("Goal not met yet: %s. The agent goes back to it.", strings.TrimRight(v.reason, ". "))
	if err := client.GoalReopen(ctx, j.project, b, v.reason); err != nil {
		notice = fmt.Sprintf("Goal not met yet: %s. It could not be opened again in mem (%v); the agent goes back to it.", strings.TrimRight(v.reason, ". "), err)
	}
	j.swapGoal(raw, encodeGoal(b))
	return agent.Verdict{
		Action:    agent.Continue,
		Prompt:    j.texts.GoalNotMet(b.Name, b.Condition, v.reason),
		Objection: fmt.Sprintf("the check of the goal `%s` found it not met: %s", b.Name, v.reason),
		Notice:    notice,
	}
}

// goalVerdict is what the judge of a goal said.
type goalVerdict struct {
	ok     bool
	basis  string
	reason string
}

// shown is a met verdict's reason as the person reads it. That the goal
// holds on the user's own word is said by gnosh, from the verdict's
// field: a small model asked to write the phrase did not.
func (v goalVerdict) shown() string {
	reason := strings.TrimSpace(v.reason)
	if v.basis == "user" {
		return "on the user's word: " + reason
	}
	return reason
}

// judgeTokens is the room the goal judge's answer is given, and
// judgeRetryTokens the room it is given once more when that was cut off.
// The answer is one line of JSON, but a model that thinks spends its
// room on thinking first: 300 was cut off on long tasks.
const (
	judgeTokens      = 1500
	judgeRetryTokens = 6000
)

// judge asks a model whether condition holds, showing it what mem says of
// the todo and the end of the whole conversation, the work of popped
// todos included: that is where the evidence is.
func (j *join) judge(ctx context.Context, condition, show string) (goalVerdict, error) {
	a := j.theAgent()
	if a == nil {
		return goalVerdict{}, errors.New("there is no model to ask")
	}
	events := layout(j.ts.Entries(), j.id)
	req := j.texts.GoalJudge(prompt.GoalJudgeInput{
		Condition:    condition,
		Mem:          show,
		Conversation: tail(written(events, clipLevels[0]), maxShown),
	})
	res, err := a.Side(ctx, agent.SideRequest{Purpose: "goal-judge", System: req.System, Prompt: req.Prompt, MaxTokens: judgeTokens})
	if errors.Is(err, agent.ErrCutOff) {
		// A model that thinks before it answers, or a provider that makes
		// it, can spend the room on thinking: asked once more with more
		// room, the check runs instead of standing unchecked.
		res, err = a.Side(ctx, agent.SideRequest{Purpose: "goal-judge", System: req.System, Prompt: req.Prompt, MaxTokens: judgeRetryTokens})
	}
	if err != nil {
		return goalVerdict{}, err
	}
	obj, ok := agent.JSONObject(res.Text)
	if !ok {
		return goalVerdict{}, fmt.Errorf("the judge's answer could not be read: %s", clipText(res.Text, 200))
	}
	var v struct {
		OK     *bool  `json:"ok"`
		Basis  string `json:"basis"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(obj), &v) != nil || v.OK == nil {
		return goalVerdict{}, fmt.Errorf("the judge's answer could not be read: %s", clipText(res.Text, 200))
	}
	return goalVerdict{ok: *v.OK, basis: v.Basis, reason: strings.TrimSpace(v.Reason)}, nil
}

// turnEnded is Contribution.TurnEnded: the turn is over. What was
// remembered for it alone is let go.
func (j *join) turnEnded(context.Context, agent.TurnResult) {
	j.mu.Lock()
	j.review.passed = nil
	j.mu.Unlock()
}

// firstOf returns the first of its arguments that is not blank.
func firstOf(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
