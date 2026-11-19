package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/markdown"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
)

// What becomes of each thing the session reports.
//
// Everything that is finished goes into the scrollback at once, as a block
// of its own, in the order it happened: a prompt when it goes out to the
// model, the answer line by line as the Markdown renderer declares each
// line final, a tool call when it ends, a notice when it is given, a
// turn's end when it ends. Everything that is still under way lives in
// the live region and nowhere else. A thing moves from the one to the
// other in a single step of this file, so it is never in both and never
// in neither.

// statusFacts is what an event of the session's may have to put facts on
// the status line that the loop's own events do not carry. The facts are
// named: "frame" (the frame the session is in), "background" (how many
// commands run in the background), "agents" (how many runs in the
// background wait on the person), "goal" and "session" (the session's
// name). A fact that is left out stays as it was; an empty one is taken
// off. (The task list has an event of its own, app.TasksEvent.)
type statusFacts interface {
	StatusFacts() map[string]string
}

// thinkKeep is how much of the end of the model's reasoning is kept at
// hand for the live region while the reasoning is shown folded.
const thinkKeep = 1 << 16

// metaPastes is the key under which a prompt carries the parts of its
// text that were pasted and shown folded in the prompt box, as a
// []editor.Span, in Prompt.Meta. The session code may use it to tell the
// model what was pasted; the interface uses it to show the prompt the way
// the box showed it.
const metaPastes = "pastes"

// handle takes in one thing the session reported.
func (s *session) handle(ev any) {
	s.dirty = true
	s.heard = s.now()
	// A question or a turn that comes while a picker's list is read goes
	// first.
	defer s.listCheck()
	if e, ok := ev.(agent.Event); ok {
		if m := e.EventMeta(); m != nil && m.Agent != "" {
			s.subAgent(ev, m)
			return
		}
	}
	switch ev := ev.(type) {
	case *agent.BusyEvent:
		s.busy, s.stopping = true, false
		s.idleSince = time.Time{}
		s.setPhase(phStarting)
	case *agent.IdleEvent:
		s.endReply()
		s.goIdle()
	case *agent.TurnStartEvent:
		s.busy = true
		s.setPhase(phStarting)
	case *agent.PromptEvent:
		s.prompt(ev)
	case *agent.RequestEvent:
		s.request(ev)
	case *app.ServerLoadEvent:
		// What the model server is doing while the reply has not begun.
		s.load = ev.Text
	case *agent.ThinkingEvent:
		s.thinking(ev.Text)
	case *agent.TextEvent:
		s.text(ev.Text)
	case *agent.ToolCallEvent:
		s.alive()
		s.endThinking()
		s.reply.chars += len(ev.Name)
		if c := s.call(ev.CallID); c != nil {
			c.name = ev.Name
		} else {
			s.calls = append(s.calls, &call{id: ev.CallID, name: ev.Name})
		}
		s.setPhase(phCall)
	case *agent.ToolInputEvent:
		s.reply.chars += len(ev.Text)
		if c := s.call(ev.CallID); c != nil && !c.started && len(c.input) < 1<<20 {
			c.input = append(c.input, ev.Text...)
		}
	case *agent.ReplyEvent:
		s.replied(ev)
	case *agent.RetryEvent:
		s.retry(ev)
	case *agent.ToolStartEvent:
		c := s.call(ev.CallID)
		if c == nil {
			c = &call{id: ev.CallID}
			s.calls = append(s.calls, c)
		}
		c.name, c.input, c.action = ev.Name, ev.Input, ev.Action
		c.started = true
		s.setPhase(phTools)
	case *agent.ToolProgressEvent:
		if c := s.call(ev.CallID); c != nil {
			c.tail.add(ev.Text)
		}
	case *agent.ToolEndEvent:
		s.toolEnd(ev)
	case *agent.QuestionEvent:
		s.ask(ev.Question, "")
	case *agent.AnswerEvent:
		s.answered(ev)
	case *agent.NoticeEvent:
		if ev.TurnEnd {
			// turnEnd says how the turn ended, with its times, right after.
			break
		}
		s.notice(ev.Level, ev.Text)
	case *agent.NoteEvent:
		if ev.Note.Notice != "" {
			s.say(ev.Note.Notice)
		}
	case *agent.DriverEvent:
		switch {
		case ev.Action == agent.Halt:
			// The turn's end says who ended it and why.
		case ev.Checkpoint:
			s.say(fmt.Sprintf("The turn goes on: %s objects, %d times in a row now, so the model is asked whether there is a real reason to stop: %s",
				ev.Name, ev.Streak, oneLine(ev.Objection)))
		case s.hookSaid != "" && oneLine(ev.Objection) == s.hookSaid:
			s.say(fmt.Sprintf("The turn goes on: %s objects, as the hook above says.", ev.Name))
		default:
			s.say(fmt.Sprintf("The turn goes on: %s objects: %s", ev.Name, oneLine(ev.Objection)))
		}
		s.hookSaid = ""
	case *agent.ContextEvent:
		s.ctxs = ev.Context
	case *agent.RoomEvent:
		s.say(firstNonEmpty(ev.Notice, fmt.Sprintf("Room was made in the model's window: about %s tokens left its view.", tokens(ev.Freed))))
		s.ctxs = s.c.Context()
	case *agent.SummaryEvent:
		s.summary(ev)
	case *agent.LoopEvent:
		s.loop = ev.On
		switch {
		case ev.On:
			s.say("/loop is on: gnosh keeps the session working until the model shows the task is done. /noloop turns it off.")
		case ev.Why != "":
			s.say(fmt.Sprintf("/loop is off after %s: %s.", count(ev.Turns, "turn", "turns"), ev.Why))
		}
	case *agent.TurnEndEvent:
		s.turnEnd(ev.Result)
	case *agent.QueueEvent:
		s.queue = ev.Queued
		s.queued()
	case *agent.PromptReturnedEvent:
		s.returned(ev)
	case *agent.LostEvent:
		// Pieces of live output were thrown away because the screen fell
		// behind. The answer is completed from the reply's end.
		s.reply.lossy = true
	case *app.HookEvent:
		s.hooks(ev)
	case *app.HookStatusEvent:
		s.hookStatus = oneLine(ev.Status)
	case *app.TasksEvent:
		s.tasks = ev.Tasks
		s.dirty = true
	case *app.PushEvent:
		ev.Reply <- s.push(ev.Message)
	case *app.RestartEvent:
		// A tool moved the session (EnterWorktree, ExitWorktree): it
		// closes, and gnosh starts again where it moved, as after
		// /restart.
		if ev.Why != "" {
			s.say(ev.Why)
		}
		s.restarting = true
		s.leave(0, "other")
	case *app.DeliverablesEvent:
		s.deliv = ev.List
		s.dirty = true
	case *app.SuggestionEvent:
		// The person's likely next prompt (promptSuggestionEnabled): shown
		// in the box while it is empty and nothing runs.
		if !s.busy && s.ed.IsEmpty() {
			s.suggestion = oneLine(ev.Text)
			s.dirty = true
		}
	case *app.StatusLineEvent:
		s.custom, s.customPad = ev.Lines, ev.Padding
		s.dirty = true
	case statusFacts:
		for name, value := range ev.StatusFacts() {
			if value == "" {
				delete(s.facts, name)
			} else {
				s.facts[name] = value
			}
		}
		s.retitle()
	}
}

// subAgent takes in what a sub-agent did. Its words are not shown; its
// tool calls are counted under the call that started it, its questions
// are put to the person, and its notices are given.
func (s *session) subAgent(ev any, m *agent.Meta) {
	switch ev := ev.(type) {
	case *agent.ToolStartEvent:
		if c := s.call(m.Parent); c != nil {
			h := s.headOf(ev.Name, ev.Input, &ev.Action)
			c.subs++
			c.subLast = strings.TrimSpace(oneLine(ev.Name) + " " + firstNonEmpty(h.arg, strings.Join(h.body, " ")))
		}
	case *agent.QuestionEvent:
		s.ask(ev.Question, m.Agent)
	case *agent.AnswerEvent:
		s.answered(ev)
	case *agent.NoticeEvent:
		s.notice(ev.Level, ev.Text)
	}
}

// call finds a call in flight by its id.
func (s *session) call(id string) *call {
	for _, c := range s.calls {
		if c.id == id {
			return c
		}
	}
	return nil
}

// drop takes a call out of the calls in flight.
func (s *session) drop(c *call) {
	for i, x := range s.calls {
		if x == c {
			s.calls = append(s.calls[:i], s.calls[i+1:]...)
			return
		}
	}
}

// prompt writes a prompt into the scrollback as it goes out to the model.
// That is the one moment a prompt is committed: until then it stands in
// the list of what is queued.
func (s *session) prompt(ev *agent.PromptEvent) {
	if ev.Prompt.Hidden {
		// One of gnosh's own. When it carries a line that says why a
		// turn started, the loop gives that line as a notice right after
		// this event, and it is shown then.
		return
	}
	d := editor.Draft{Text: ev.Prompt.Text}
	if spans, ok := ev.Prompt.Meta[metaPastes].([]editor.Span); ok {
		d.Pastes = spans
	}
	for i, p := range s.sending {
		if p.id == ev.ID {
			d = p.draft
			s.sending = append(s.sending[:i], s.sending[i+1:]...)
			break
		}
	}
	th := &s.th
	s.begin(blkPrompt)
	mark := th.Prompt.Render(th.PromptMark)
	indent := strings.Repeat(" ", s.m.Width(th.PromptMark))
	lines := safeLines(d.Collapsed())
	if n := len(ev.Prompt.Attachments); n > 0 {
		lines = append(lines, "["+count(n, "attachment", "attachments")+"]")
	}
	if said := mentionsSaid(ev.Parts); said != "" {
		lines = append(lines, "["+said+"]")
	}
	for i, l := range lines {
		first := indent
		if i == 0 {
			first = mark
		}
		s.put(wrap(s.m, th.PromptText.Render(l), s.cols, first, indent)...)
	}
	// Only now is it history: the history must not offer a second copy
	// of a prompt that still waits in the queue.
	s.ed.AddHistory(d)
	s.queued()
}

// mentionsSaid says what the paths a prompt mentioned with @ brought, from
// the Source of their parts ("mention:file:notes.txt"): "@notes.txt read ·
// @docs listed · @.env denied".
func mentionsSaid(parts []agent.Part) string {
	words := map[string]string{"file": "read", "dir": "listed", "denied": "denied by a rule", "failed": "could not be read", "agent": "asked for", "resource": "read"}
	var out []string
	for _, p := range parts {
		rest, ok := strings.CutPrefix(p.Source, "mention:")
		if !ok {
			continue
		}
		kind, path, ok := strings.Cut(rest, ":")
		if kind == "agent" {
			path = "agent-" + path
		}
		if w := words[kind]; ok && w != "" {
			out = append(out, "@"+oneLine(path)+" "+w)
		}
	}
	return strings.Join(out, " · ")
}

// request notes that a request is on its way to a model.
func (s *session) request(ev *agent.RequestEvent) {
	switch ev.Purpose {
	case "main":
		s.suggestion = ""
		s.endReply()
		s.reply = reply{}
		s.model, s.estimate, s.attempt = ev.Model, ev.Estimate, ev.Attempt
		s.load = ""
		s.busy = true
		s.setPhase(phRequest)
		s.since = s.now()
	case "summary":
		s.setPhase(phSummary)
	}
}

// alive notes that the provider has answered: a run of failed tries is
// over, and the person is told that it is.
func (s *session) alive() {
	if s.retries == 0 {
		return
	}
	s.say(fmt.Sprintf("%s answers again, after %s and %s.",
		firstNonEmpty(oneLine(s.model), "The model"), count(s.retries, "failed try", "failed tries"), span(s.now().Sub(s.retrySince))))
	s.retries, s.retryKind = 0, ""
}

// thinking takes the next piece of the model's reasoning. It is shown
// folded: its last rows in the live region while it arrives, and one line
// in the scrollback when it is over. With detail on it is written into
// the scrollback in full, line by line.
func (s *session) thinking(piece string) {
	s.alive()
	rp := &s.reply
	if !rp.thinking {
		rp.thinking, rp.thinkSince = true, s.now()
	}
	s.setPhase(phThinking)
	rp.chars += len(piece)
	if rp.think.Len() < 1<<20 {
		rp.think.WriteString(piece)
	}
	rp.thinkRest += piece
	if !s.detail {
		if len(rp.thinkRest) > thinkKeep {
			rp.thinkRest = rp.thinkRest[len(rp.thinkRest)-thinkKeep:]
		}
		return
	}
	if i := strings.LastIndexByte(rp.thinkRest, '\n'); i >= 0 {
		s.thought(rp.thinkRest[:i])
		rp.thinkRest = rp.thinkRest[i+1:]
	}
}

// thought writes reasoning into the scrollback.
func (s *session) thought(words string) {
	rows := s.thinkRows(words, s.cols)
	if len(rows) == 0 {
		return
	}
	if !s.reply.thinkBegun {
		s.reply.thinkBegun = true
		s.begin(blkThinking)
		s.put(s.th.Thinking.Render(s.th.ThinkMark + " thinking"))
	}
	s.put(rows...)
}

// endThinking closes the reasoning that was arriving.
func (s *session) endThinking() {
	rp := &s.reply
	if !rp.thinking {
		return
	}
	rp.thinking = false
	took := s.now().Sub(rp.thinkSince)
	if s.detail {
		s.thought(rp.thinkRest)
		rp.thinkRest = ""
		return
	}
	// Folded: one line says that the model thought and for how long. The
	// words are in the session's file, where the transcript view (Ctrl+O)
	// shows them. In focus view, the time goes into the run's fold line.
	rp.thinkRest = ""
	if s.focus {
		s.fold.thought += took
		return
	}
	said := s.th.ThinkMark + " thought for " + span(took)
	if s.thoughtToBudget() {
		// The server cut the thought at its budget and put gnosh's note
		// at its end: so that a thought that stops short reads as that.
		said += fmt.Sprintf(", up to its budget of %d tokens", s.info.ThinkingBudget)
	}
	line := s.th.Thinking.Render(said) +
		s.th.Quiet.Render(s.th.Sep+"Ctrl+O shows it")
	s.begin(blkThinking)
	s.put(fit(s.m, line, s.cols))
}

// text takes the next piece of the answer. The lines the Markdown renderer
// declares final are committed; the rest stays in the live region.
func (s *session) text(piece string) {
	s.alive()
	s.endThinking()
	rp := &s.reply
	s.setPhase(phWriting)
	rp.chars += len(piece)
	if rp.lossy {
		// Pieces before this one were lost. Feeding it now would leave a
		// hole in the answer; the whole text comes with the reply's end.
		return
	}
	if rp.md == nil {
		rp.md = markdown.New(markdown.Options{
			Width: s.cols, Profile: s.th.Profile, Theme: s.th.Markdown, Metrics: s.m,
		})
	}
	rp.fed += len(piece)
	s.answer(rp.md.Write(piece))
}

// answer commits lines of the answer.
func (s *session) answer(lines []string) {
	if len(lines) == 0 {
		return
	}
	if !s.reply.begun || s.last != blkAnswer {
		// The answer's first lines, or its next ones after something
		// else was written in between.
		s.reply.begun = true
		s.begin(blkAnswer)
	}
	s.put(lines...)
}

// endReply closes the answer that was arriving: whatever of it was still
// live becomes final and is committed.
func (s *session) endReply() {
	s.endThinking()
	rp := &s.reply
	if rp.md == nil {
		return
	}
	s.answer(rp.md.Close())
	rp.md = nil
}

// replied takes in the end of a reply.
func (s *session) replied(ev *agent.ReplyEvent) {
	if ev.Purpose != "main" {
		return
	}
	if ev.StopReason != core.StopError && ev.StopReason != core.StopCancelled {
		s.alive()
	}
	rp := &s.reply
	if rp.lossy {
		// Complete the answer from the reply itself: everything after
		// what was fed before the loss.
		full := ev.Message.Text()
		if rp.md == nil && strings.TrimSpace(full) != "" {
			rp.md = markdown.New(markdown.Options{
				Width: s.cols, Profile: s.th.Profile, Theme: s.th.Markdown, Metrics: s.m,
			})
		}
		if rp.md != nil && rp.fed <= len(full) {
			s.endThinking()
			s.answer(rp.md.Write(full[rp.fed:]))
		}
	}
	s.endReply()
	if ev.StopReason == core.StopCancelled || ev.StopReason == core.StopError {
		// The reply was cut short: the calls it had begun to write will
		// never run.
		for _, c := range append([]*call(nil), s.calls...) {
			if !c.started {
				s.cut(c, "not run: the reply was cut off before this call was complete")
			}
		}
	}
	s.reply = reply{}
	s.setPhase(phWorking)
}

// cut writes a call that never got a result into the scrollback, with the
// reason, and takes it out of the calls in flight.
func (s *session) cut(c *call, why string) {
	if s.focus {
		s.fold.add(&agent.ToolEndEvent{CallID: c.id, Name: c.name, Denied: true})
		s.drop(c)
		s.dirty = true
		return
	}
	var action *core.Action
	if c.started {
		action = &c.action
	}
	rows := s.headRows(s.headOf(c.name, c.input, action), true, why)
	s.begin(blkTool)
	s.put(rows...)
	s.drop(c)
}

// retry takes in a failed request that will be tried again. The first
// wait of a run of them is written into the scrollback, and so is every
// one whose reason differs from the one before; the others only move the
// line that says what gnosh is waiting for.
func (s *session) retry(ev *agent.RetryEvent) {
	if ev.Purpose != "main" && ev.Purpose != "summary" {
		return
	}
	now := s.now()
	if ev.Discarded {
		// What had arrived of the reply is thrown away by the session and
		// written afresh by the next try. What is on the screen stays on
		// the screen, so the part that was still live is committed too,
		// and a line says what it is.
		had := s.reply.begun || s.reply.md != nil
		s.endReply()
		for _, c := range append([]*call(nil), s.calls...) {
			if !c.started {
				s.drop(c)
			}
		}
		if had {
			s.say("The reply above was cut off when the request failed. The model is asked again and writes it afresh.")
		}
		s.reply = reply{}
	} else {
		s.endThinking()
	}
	if ev.FellBack != "" {
		// Not a wait: the fallback model is asked at once.
		s.notice(agent.LevelWarning, ev.Message)
		s.retries, s.retryKind = 0, ""
		return
	}
	kind := fmt.Sprintf("%s/%d", ev.Kind, ev.Status)
	if s.retries == 0 {
		s.retrySince = now
	}
	if s.retries == 0 || kind != s.retryKind {
		s.notice(agent.LevelWarning, ev.Message)
	}
	s.retries++
	s.retryKind, s.retryUntil = kind, now.Add(ev.Wait)
	s.setPhase(phRetry)
}

// toolEnd commits a call that has its result.
func (s *session) toolEnd(ev *agent.ToolEndEvent) {
	c := s.call(ev.CallID)
	if c == nil {
		c = &call{id: ev.CallID, name: ev.Name, input: ev.Input, started: true}
	} else {
		s.drop(c)
	}
	s.noteChange(ev)
	if s.foldsCall(c, ev) {
		s.fold.add(ev)
		s.dirty = true
	} else {
		rows, compact := s.endBlock(c, ev)
		kind := blkTool
		if compact {
			kind = blkToolLine
		}
		s.begin(kind)
		s.put(rows...)
	}
	started := false
	for _, x := range s.calls {
		started = started || x.started
	}
	if !started {
		s.setPhase(phWorking)
	}
	if ev.Name == "Bash" && s.branchOf != nil && s.cwd != "" {
		// A command may have changed the branch.
		s.branch = s.branchOf(s.cwd)
	}
}

// hooks takes in what the person's hooks did at one of the session's
// moments. No hook is invisible: the ones that ran on a tool call stand
// under that call, and the others are given as notices when they had
// anything to say. With detail on, the ones that had nothing to say are
// shown as well.
func (s *session) hooks(ev *app.HookEvent) {
	c := s.call(ev.CallID)
	silent := 0
	for _, r := range ev.Records {
		// The words are the hook package's own account of the record.
		line := "hook: " + oneLine(r.String())
		if reason := oneLine(r.Reason); string(r.Result) == "blocked" && reason != "" && strings.HasSuffix(line, reason) {
			s.hookSaid = reason
		}
		// "none" is that package's word for a hook that ran and had
		// nothing to say, and "background" for one started in the
		// background, which decides nothing.
		quiet := string(r.Result) == "none" || string(r.Result) == "background"
		switch {
		case quiet && !s.detail:
			silent++
		case c != nil:
			c.hooks = append(c.hooks, line)
		case quiet:
			s.say(line)
		default:
			s.notice(agent.LevelWarning, line)
		}
	}
	if silent > 0 && c != nil && !s.focus {
		// (In focus view a call whose hooks said nothing folds.)
		c.hooks = append(c.hooks, fmt.Sprintf("%s ran on %s and had nothing to say",
			count(silent, "hook", "hooks"), oneLine(fmt.Sprint(ev.Event))))
	}
}

// summary takes in the start or the end of a summary of the conversation.
func (s *session) summary(ev *agent.SummaryEvent) {
	switch {
	case ev.Phase == "start":
		s.say(fmt.Sprintf("Summarizing the conversation to make room (%s). On a local model this can take minutes; Esc stops it.", ev.Trigger))
		s.setPhase(phSummary)
	case ev.Err != nil:
		s.notice(agent.LevelWarning, fmt.Sprintf("No summary was made: %v", ev.Err))
		s.setPhase(phWorking)
	default:
		s.say(fmt.Sprintf("The conversation was summarized: about %s tokens before, about %s after.", tokens(ev.PreTokens), tokens(ev.PostTokens)))
		s.setPhase(phWorking)
		s.ctxs = s.c.Context()
	}
}

// turnEnd writes how a turn ended. Every way a turn can end is said in
// plain words; a stopped turn says that the person stopped it.
func (s *session) turnEnd(res agent.TurnResult) {
	// (The model may have made or removed files: the fuzzy @ search lists
	// the project again when it is next used.)
	s.index.stale = true
	s.suggest.answers = nil
	s.endReply()
	for _, c := range append([]*call(nil), s.calls...) {
		s.cut(c, "no result: the turn ended first")
	}
	s.reply = reply{}
	s.stopping = false
	s.retries, s.retryKind = 0, ""
	s.failHint = ""

	th := &s.th
	var stats []string
	if res.Duration > 0 {
		stats = append(stats, "in "+span(res.Duration))
	}
	if res.Rounds > 0 {
		stats = append(stats, count(res.Rounds, "reply", "replies"))
	}
	if in, out := res.Usage.PromptTokens(), res.Usage.OutputTokens; in > 0 || out > 0 {
		stats = append(stats, tokens(in)+" tokens read, "+tokens(out)+" written")
	}
	tail := ""
	if len(stats) > 0 {
		tail = " " + strings.Join(stats, th.Sep)
	}
	detail := strings.TrimSpace(res.Detail)
	switch res.Reason {
	case agent.EndCompleted:
		if s.info.HideTurnDuration {
			// (The showTurnDuration option is off.)
			break
		}
		s.begin(blkTurn)
		s.put(wrap(s.m, th.Quiet.Render("done"+tail), s.cols, "", "  ")...)
	case agent.EndStopped:
		s.begin(blkTurn)
		s.put(wrap(s.m, th.WarnLabel.Render("stopped")+th.Quiet.Render(" by you"+tail+th.Sep+"what was done is kept"), s.cols, "", "  ")...)
	case agent.EndHalted:
		level := agent.LevelWarning
		if res.Driver == "EnterWorktree" || res.Driver == "ExitWorktree" {
			// (A move the model made, which ends the turn on purpose:
			// gnosh starts again where the session moved.)
			level = agent.LevelInfo
		}
		s.notice(level, "The turn was ended"+tail+": "+firstNonEmpty(detail, "something ended it and gave no reason")+".")
	case agent.EndModelStopped:
		words := "The model chose to stop" + tail + ". Its reason: " + firstNonEmpty(detail, "(it gave none)")
		if res.Objection != "" {
			words += "\nIt had been sent back by " + firstNonEmpty(res.Driver, "a check") + ": " + res.Objection
		}
		s.notice(agent.LevelWarning, words)
	case agent.EndFailed:
		words := detail
		if res.Err != nil {
			words = res.Err.Error()
		}
		s.notice(agent.LevelError, firstNonEmpty(words, "The turn failed, and nothing said why."))
		s.failHint = failHintFor(res.ErrorKind)
	}
	s.setPhase(phWorking)
	s.refresh()
}

// returned takes back a prompt that will not go out: it is put in the
// prompt box again, in front of anything typed since, and a line says
// why.
func (s *session) returned(ev *agent.PromptReturnedEvent) {
	for i, p := range s.sending {
		if p.id == ev.ID {
			s.sending = append(s.sending[:i], s.sending[i+1:]...)
			break
		}
	}
	s.queued()
	if ev.Prompt.Hidden {
		return
	}
	d := editor.Draft{Text: ev.Prompt.Text}
	if spans, ok := ev.Prompt.Meta[metaPastes].([]editor.Span); ok {
		d.Pastes = spans
	}
	s.restore(d)
	s.attach = append(s.attach, ev.Prompt.Attachments...)
	s.notice(agent.LevelWarning, "Your prompt was not sent: "+firstNonEmpty(ev.Reason, string(ev.Why))+"\nIt is back in the prompt box.")
}

// restore puts a draft back in the prompt box, in front of whatever has
// been typed since, so that neither is lost.
func (s *session) restore(d editor.Draft) {
	if strings.TrimSpace(d.Text) == "" {
		return
	}
	if !s.ed.IsEmpty() {
		typed := s.ed.Draft()
		at := len(d.Text) + 2
		d.Text += "\n\n" + typed.Text
		for _, sp := range typed.Pastes {
			d.Pastes = append(d.Pastes, editor.Span{Start: sp.Start + at, End: sp.End + at})
		}
	}
	s.ed.SetDraft(d)
}

// queued tells the prompt box whether prompts wait that Up could take
// back.
func (s *session) queued() {
	s.ed.SetQueued(len(s.personQueue())+len(s.cmdq) > 0)
	s.dirty = true
}

// retitle sets the terminal's title from the session's name, once it has
// one, as Claude Code does: "gnosh: " and the name.
func (s *session) retitle() {
	name := oneLine(s.facts["session"])
	if name == "" {
		return
	}
	want := "gnosh: " + name
	if want == s.titled {
		return
	}
	s.titled = want
	_ = s.host.title(want)
}

// thoughtToBudget reports whether the thought of the reply now arriving
// was cut at its budget: it ends with the note the server puts there.
func (s *session) thoughtToBudget() bool {
	note := strings.TrimSpace(s.info.BudgetNote)
	return note != "" && s.info.ThinkingBudget > 0 && strings.HasSuffix(strings.TrimSpace(s.reply.think.String()), note)
}
