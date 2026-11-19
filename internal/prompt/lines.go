package prompt

import (
	"strconv"
	"strings"
	"time"
)

// A value put into a text is cut when it is longer than its kind allows,
// so that nothing quoted in a line can flood the conversation. The sizes
// are in bytes.
const (
	maxName   = 300   // a name, a path, a todo's id
	maxTitle  = 240   // a todo's title
	maxQuote  = 600   // a call or a command quoted in a sentence
	maxReason = 8000  // a hook's reason, a refusal, a status listing
	maxBlock  = 24000 // a list handed in whole
)

// The markers a model ends a reply with, on a line of their own, to tell
// gnosh something. The texts that ask for them are given them by name, so
// a replaced text cannot change what gnosh then looks for.
const (
	// StopMarker stops the turn at a checkpoint.
	StopMarker = "<!-- GNOSH_STOP -->"
	// LoopDone says the task /loop is working on is done.
	LoopDone = "LOOP_DONE"
	// LoopStop stops /loop at a check-in.
	LoopStop = "LOOP_STOP"
)

// Environment is where a session runs, as its first prompt tells the
// model. Every field is handed in: this package reads no clock and no
// disk. A field left empty is left out of the text.
type Environment struct {
	// Now is the time the session began, in the person's own time zone.
	// The model is given the day as 2026-10-30, the one form its tools
	// take, and the weekday.
	Now time.Time
	// Cwd is the working directory.
	Cwd string
	// User and Machine say who gnosh runs as, and on which machine.
	User    string
	Machine string
	// Platform names the system, such as "linux, arm64".
	Platform string
	// Git says how the working directory stands in git.
	Git Git
	// AddedDirs are the directories added with --add-dir.
	AddedDirs []string
	// Hosts are the other machines declared for commands.
	Hosts []Host
}

// GitState says what gnosh knows of git in the working directory.
type GitState int

const (
	// GitUnknown says nothing was looked up, and nothing is said.
	GitUnknown GitState = iota
	// GitNone says the working directory is not in a repository.
	GitNone
	// GitRepo says it is in one. Branch and Status say more.
	GitRepo
	// GitUnread says it is in one whose status gnosh did not read, and
	// Why says why not: the repository's own settings name a program that
	// a status would run, in a folder not yet trusted.
	GitUnread
)

// Git is the state of git as a session began.
type Git struct {
	State GitState
	// Branch is the branch checked out, or empty when there is none.
	Branch string
	// Status is a short account of what is changed, already summed up by
	// the caller: the lines of a short status, or the word "clean".
	Status string
	// Why says why the status was not read, for GitUnread.
	Why string
}

// Host is another machine the model may run commands on.
type Host struct {
	// Name is the name the model passes as host.
	Name string
	// Machine is who and where: root@203.0.113.10.
	Machine string
	// Description is what the person wrote about it.
	Description string
}

// Environment returns the block that goes with a session's first prompt,
// ahead of the prompt's own text. It holds what differs from session to
// session, which is why none of it is in the system prompt.
func (s *Set) Environment(e Environment) string {
	vals := values{
		"cwd":      oneLine(e.Cwd, maxName),
		"platform": oneLine(e.Platform, maxName),
		"dirs":     oneLine(strings.Join(e.AddedDirs, ", "), maxReason),
	}
	if !e.Now.IsZero() {
		vals["date"] = e.Now.Format("2006-01-02")
		vals["weekday"] = e.Now.Weekday().String()
	}
	vals["user"] = oneLine(e.User, maxName)
	vals["machine"] = oneLine(e.Machine, maxName)
	vals.flag("user_only", vals["machine"] == "")
	vals.flag("machine_only", vals["user"] == "")
	switch e.Git.State {
	case GitNone:
		vals.flag("nogit", true)
	case GitRepo, GitUnread:
		vals["branch"] = oneLine(e.Git.Branch, maxName)
		vals.flag("repo", vals["branch"] == "")
		if e.Git.State == GitRepo {
			vals["status"] = lines(e.Git.Status, maxReason)
		} else {
			vals.flag("unread", true)
			vals["why"] = strings.TrimRight(oneLine(e.Git.Why, maxQuote), ". ")
		}
	}
	var hosts []string
	for _, h := range e.Hosts {
		line := "  - " + oneLine(h.Name, maxName)
		if m := oneLine(h.Machine, maxName); m != "" {
			line += " (" + m + ")"
		}
		if d := oneLine(h.Description, maxQuote); d != "" {
			line += ": " + d
		}
		hosts = append(hosts, line)
	}
	vals["hosts"] = strings.Join(hosts, "\n")
	return s.fill(textEnvironment, vals)
}

// MemLine returns the line that goes last on every prompt a person types
// in a session that has mem. The words are Jae's.
func (s *Set) MemLine() string {
	return s.fill(textMemLine, nil)
}

// Frame is a todo a session stands in.
type Frame struct {
	// ID is its name in mem, a path of names: fix-flaky/find-cause.
	ID string
	// Purpose is its title.
	Purpose string
}

// Slide tells of a part of the conversation that was taken out of the view
// to make room, without a summary.
type Slide struct {
	// Frame is the todo whose rounds went: the start of its work, or, with
	// Before set, the rounds that came before it was entered.
	Frame  string
	Before bool
	// Files are the files the rounds that went had edited, as the model
	// is told of them, so that it does not go back to work it has done.
	Files []string
}

// FrameLine returns the line that follows the mem line while the session
// stands in a todo. stack is the todos it has entered and not left, from
// the first entered to the one it is in now; with an empty stack there is
// no line. slides are the slides the model has not been told of, or that
// concern a todo still on the stack.
func (s *Set) FrameLine(stack []Frame, slides []Slide) string {
	if len(stack) == 0 {
		return ""
	}
	return Wrap(s.frameLine(stack, slides, true))
}

// frameLine is the frame line's text, not yet wrapped. Without more it is
// the first sentence alone.
func (s *Set) frameLine(stack []Frame, slides []Slide, more bool) string {
	top := stack[len(stack)-1]
	vals := values{
		"id":      oneLine(top.ID, maxName),
		"purpose": oneLine(top.Purpose, maxTitle),
		"slides":  s.slides(slides),
	}
	if len(stack) > 1 {
		ids := make([]string, len(stack))
		for i, f := range stack {
			ids[i] = "`" + oneLine(f.ID, maxName) + "`"
		}
		vals["stack"] = strings.Join(ids, ", ")
	}
	vals.flag("more", more)
	return s.bare(textFrameLine, vals)
}

// slides is the slide's line for each of list, not wrapped.
func (s *Set) slides(list []Slide) string {
	var out []string
	for _, sl := range list {
		id := oneLine(sl.Frame, maxName)
		if id == "" {
			continue
		}
		vals := values{"show": id}
		if len(sl.Files) > 0 {
			files := make([]string, len(sl.Files))
			for i, f := range sl.Files {
				files[i] = oneLine(f, maxName)
			}
			vals["files"] = strings.Join(files, ", ")
		}
		if sl.Before {
			vals["before"] = id
		} else {
			vals["frame"] = id
		}
		out = append(out, s.bare(textSlide, vals))
	}
	return strings.Join(out, " ")
}

// SlideLine returns the slide's line as a note of its own, for when no
// frame line is about to carry it: before a prompt of gnosh's own, or
// between two rounds of a turn. With no slides there is no line.
func (s *Set) SlideLine(slides []Slide) string {
	return Wrap(s.slides(slides))
}

// LargeStep returns the line that tells the model, once per todo, that
// much has come in since it entered the todo called frame: about tokens
// tokens.
func (s *Set) LargeStep(frame string, tokens int) string {
	return s.fill(textLargeStep, values{
		"id":     oneLine(frame, maxName),
		"tokens": tokenCount(tokens),
	})
}

// Todo is one line of the session's task list, as mem has it.
type Todo struct {
	// Title is the todo's title.
	Title string
	// Status is how it stands, in the task list's own words: completed,
	// in progress, pending.
	Status string
}

// MemReminder returns the reminder a session gets after ten replies with
// no mem call. With a stack, the frame line's first sentence follows;
// with todos, where they stand.
func (s *Set) MemReminder(stack []Frame, todos []Todo) string {
	vals := values{}
	if len(stack) > 0 {
		vals["frame"] = s.frameLine(stack, nil, false)
	}
	var list []string
	for i, t := range todos {
		line := strconv.Itoa(i+1) + ". " + oneLine(t.Title, maxTitle)
		if st := oneLine(t.Status, maxName); st != "" {
			line += " (" + st + ")"
		}
		list = append(list, line)
	}
	vals["todos"] = lines(strings.Join(list, "\n"), maxBlock)
	return s.fill(textMemReminder, vals)
}

// Repeat returns the nudge for a model that keeps making the same call.
// calls are the one or two calls it keeps making, each as a short line a
// person could read: the tool and its arguments. The turn goes on.
func (s *Set) Repeat(calls []string) string {
	var list []string
	for _, c := range calls {
		if c = oneLine(c, maxQuote); c != "" {
			list = append(list, "- "+c)
		}
	}
	return s.fill(textRepeat, values{"calls": strings.Join(list, "\n")})
}

// RepeatRefusedClose returns the nudge for a model that keeps sending a
// close that is refused. refusal is the refusal it keeps getting.
func (s *Set) RepeatRefusedClose(refusal string) string {
	return s.fill(textRepeatClose, values{"refusal": lines(refusal, maxReason)})
}

// EmptyReply returns what a model is asked after a reply with no text and
// no tool call.
func (s *Set) EmptyReply() string {
	return s.fill(textEmptyReply, nil)
}

// TextCall returns what a model is told after a reply whose text is a tool
// call written out as text, which runs nothing.
func (s *Set) TextCall() string {
	return s.fill(textTextCall, nil)
}

// StopHook returns what the model reads when a Stop hook, or a
// SubagentStop hook, will not let the turn end. reason is what the hook
// said.
func (s *Set) StopHook(reason string) string {
	return s.fill(textStopHook, values{"reason": lines(reason, maxReason)})
}

// Continuation returns what the model reads when something other than a
// Stop hook sends it back to work as its turn would end. reason says what
// is still to do. A driver whose words are already whole, such as the goal
// directive the mem code writes, uses Wrap instead.
func (s *Set) Continuation(reason string) string {
	return s.fill(textContinuation, values{"reason": lines(reason, maxReason)})
}

// CheckCouldNotRun returns what the model reads when a check that runs as
// a turn would end could not run at all. name is the check and err is
// what went wrong. The turn goes on, because a check that could not run
// has not passed.
func (s *Set) CheckCouldNotRun(name, err string) string {
	return s.fill(textCheckFailed, values{
		"name":  oneLine(name, maxName),
		"error": strings.TrimRight(oneLine(err, maxQuote), ". "),
	})
}

// Checkpoint returns the question put in place of a send-back every so
// many times in a row: is there a real reason to stop? count is how many
// times in a row the model has been sent back, objection is what holds the
// turn open, and marker is the line that stops it (StopMarker when empty).
// The marker counts only in the reply to this question.
//
// The question comes first and what holds the turn open last, so that the
// model answers the question rather than carrying on. A caller that
// already has the objection as a line of its own may leave it empty and
// put that line after this one.
func (s *Set) Checkpoint(count int, objection, marker string) string {
	if marker == "" {
		marker = StopMarker
	}
	vals := values{
		"objection": lines(objection, maxReason),
		"marker":    marker,
	}
	if count > 0 {
		vals["count"] = strconv.Itoa(count)
	}
	return s.fill(textCheckpoint, vals)
}

// Loop returns the line /loop sends as each turn ends: keep going, and
// write LoopDone alone on a line when everything is done.
func (s *Set) Loop() string {
	return s.fill(textLoop, values{"marker": LoopDone})
}

// LoopProve returns what /loop asks the first time a reply says the task
// is done: show it. Only a LoopDone in the reply to this ends the loop.
func (s *Set) LoopProve() string {
	return s.fill(textLoopProve, values{"marker": LoopDone})
}

// LoopCheckIn returns the question /loop asks every 25 turns in place of
// its usual line. turns is how many turns the loop has run. LoopStop in
// the reply to it stops the loop.
func (s *Set) LoopCheckIn(turns int) string {
	vals := values{"marker": LoopStop}
	if turns > 0 {
		vals["turns"] = strconv.Itoa(turns)
	}
	return s.fill(textLoopCheckIn, vals)
}

// CloseSendBack returns what the model reads when its turn would end in
// the todo called todo, whose last close in this turn was refused. With no
// refusal given, the todo was refused as never worked on; otherwise
// refusal is the refusal's own line.
func (s *Set) CloseSendBack(todo, refusal string) string {
	vals := values{
		"todo":    oneLine(todo, maxName),
		"refusal": oneLine(refusal, maxReason),
	}
	vals.flag("undone", vals["refusal"] == "")
	return s.fill(textCloseSendBack, vals)
}

// GoalNotMet returns what the model reads when the judge finds that the
// goal or task called name, closed as done, does not hold. condition is
// the goal in the person's words, and reason is what the judge found
// missing.
func (s *Set) GoalNotMet(name, condition, reason string) string {
	return s.fill(textGoalNotMet, values{
		"name":      oneLine(name, maxName),
		"condition": lines(condition, maxReason),
		"reason":    oneLine(reason, maxReason),
	})
}

// Cause says what stopped a turn before it finished.
type Cause int

const (
	// UserStopped: the person pressed Esc, or sent the run a signal.
	UserStopped Cause = iota
	// GnoshStopped: gnosh itself was stopped, by a crash, a kill or a
	// restart, and the session is being opened again.
	GnoshStopped
)

// set marks, for a text with a part for each cause, which cause applies.
func (c Cause) set(vals values) values {
	vals.flag("user", c == UserStopped)
	vals.flag("exit", c != UserStopped)
	return vals
}

// Interrupted returns the notice put where a turn was stopped before it
// finished, so that the model does not read a reply cut short as one it
// chose to end.
func (s *Set) Interrupted(by Cause) string {
	return s.fill(textInterrupted, by.set(values{}))
}

// BackgroundState says how a background command ended.
type BackgroundState int

const (
	// BackgroundFinished: it exited with code 0.
	BackgroundFinished BackgroundState = iota
	// BackgroundFailed: it exited with another code.
	BackgroundFailed
	// BackgroundStopped: it was stopped.
	BackgroundStopped
	// BackgroundNotRun: it could not be started.
	BackgroundNotRun
)

// BackgroundEnd is the end of a background command.
type BackgroundEnd struct {
	// ID is the job's id.
	ID string
	// Command is what the model called it: the description it gave, or
	// the command's first line.
	Command string
	// State says how it ended. Code is the exit code of one that failed,
	// and Why says why one could not run.
	State BackgroundState
	Code  int
	Why   string
	// Output is the file its output is in.
	Output string
	// Monitor says a Monitor call watched it.
	Monitor bool
}

// BackgroundEnded returns the notice that a background command has ended.
// Nobody typed it, and it says so.
func (s *Set) BackgroundEnded(b BackgroundEnd) string {
	vals := values{
		"command": oneLine(b.Command, maxQuote),
		"id":      oneLine(b.ID, maxName),
		"output":  oneLine(b.Output, maxName),
	}
	switch b.State {
	case BackgroundFailed:
		vals["failed"] = strconv.Itoa(b.Code)
	case BackgroundStopped:
		vals.flag("stopped", true)
	case BackgroundNotRun:
		vals.flag("not_run", true)
		vals["why"] = strings.TrimRight(oneLine(b.Why, maxQuote), ". ")
	default:
		vals.flag("finished", true)
	}
	vals.flag("monitor", b.Monitor)
	return s.fill(textBackgroundEnded, vals)
}

// MonitorNews is what a command that Monitor watches printed since it was
// last passed on.
type MonitorNews struct {
	// ID is the command's id, and Description what the call said it
	// watches.
	ID, Description string
	// Lines are the lines it printed, and Left how many more did not fit.
	Lines []string
	Left  int
	// Output is the file that holds all it printed.
	Output string
	// Last says the command has ended: these are its last lines.
	Last bool
}

// MonitorLines returns the notice that passes on what a command that
// Monitor watches printed. Nobody typed it, and it says so.
func (s *Set) MonitorLines(n MonitorNews) string {
	vals := values{
		"id":          oneLine(n.ID, maxName),
		"description": oneLine(n.Description, maxQuote),
		"lines":       strings.Join(n.Lines, "\n"),
		"output":      oneLine(n.Output, maxName),
	}
	if n.Left > 0 {
		vals["left"] = strconv.Itoa(n.Left)
	}
	vals.flag("last", n.Last)
	return s.fill(textMonitorLines, vals)
}

// AgentState says how a background sub-agent ended.
type AgentState int

const (
	// AgentFinished: it wrote its report.
	AgentFinished AgentState = iota
	// AgentFailed: it stopped before it finished: its model failed, or it
	// reached the most replies its kind may write.
	AgentFailed
	// AgentStopped: the person, the model or the session stopped it.
	AgentStopped
)

// AgentEnd is the end of a sub-agent that ran in the background.
type AgentEnd struct {
	// ID is the sub-agent's id, and Description what the model called its
	// task.
	ID, Description string
	// State says how it ended, and Why says more of a failure.
	State AgentState
	Why   string
	// Report is what it wrote last, which is its own words: Defuse has
	// been applied to it.
	Report string
	// Caution is the note ReportCaution made of the report, if any.
	Caution string
	// Output is its session file.
	Output string
}

// AgentEnded returns the notice that a background sub-agent has ended,
// with its report. Nobody typed it, and it says so.
func (s *Set) AgentEnded(a AgentEnd) string {
	vals := values{
		"description": oneLine(a.Description, maxQuote),
		"id":          oneLine(a.ID, maxName),
		"output":      oneLine(a.Output, maxName),
		"report":      strings.TrimSpace(a.Report),
		"caution":     a.Caution,
		"why":         strings.TrimRight(oneLine(a.Why, maxQuote), ". "),
	}
	switch a.State {
	case AgentFailed:
		vals.flag("failed", true)
	case AgentStopped:
		vals.flag("stopped", true)
	default:
		vals.flag("finished", true)
	}
	return s.fill(textAgentEnded, vals)
}

// SubtaskEnd is how a run the person started with /subtask ended: a copy
// of the conversation that worked on a task in the background.
type SubtaskEnd struct {
	// ID is the copy's session, and Task what the person set it to do.
	ID, Task string
	// State says how it ended, and Why says more of a failure.
	State AgentState
	Why   string
	// Report is the answer it printed, which is its own words: Defuse has
	// been applied to it.
	Report string
}

// SubtaskEnded returns the notice that a run started with /subtask has
// ended, with its answer. Nobody typed it, and it says so.
func (s *Set) SubtaskEnded(e SubtaskEnd) string {
	vals := values{
		"task":   oneLine(e.Task, maxQuote),
		"id":     oneLine(e.ID, maxName),
		"report": strings.TrimSpace(e.Report),
		"why":    strings.TrimRight(oneLine(e.Why, maxQuote), ". "),
	}
	switch e.State {
	case AgentFailed:
		vals.flag("failed", true)
	case AgentStopped:
		vals.flag("stopped", true)
	default:
		vals.flag("finished", true)
	}
	return s.fill(textSubtaskEnded, vals)
}

// Btw returns the line that stands before a side question the person asks
// with /btw: answer from the conversation, call no tool, keep nothing.
// The question follows it, in the person's own words.
func (s *Set) Btw() string {
	return s.fill(textBtw, nil)
}

// MentionKind says what a mention of a path with @ came to.
type MentionKind int

const (
	// MentionFile: what Read returns for the file follows the line.
	MentionFile MentionKind = iota
	// MentionDir: a listing of the directory follows the line.
	MentionDir
	// MentionDenied: a deny rule keeps the path from being read.
	MentionDenied
	// MentionFailed: the path could not be read; why says why.
	MentionFailed
	// MentionResource: what an MCP server's resource holds follows the
	// line; the path is server:uri.
	MentionResource
)

// Mention returns the line that stands before what a person's @ mention
// of a path brings: a file as Read returns it, a directory's listing, or
// why nothing came.
func (s *Set) Mention(kind MentionKind, path, why string) string {
	vals := values{"path": oneLine(path, maxQuote)}
	switch kind {
	case MentionDir:
		vals.flag("dir", true)
	case MentionDenied:
		vals.flag("denied", true)
	case MentionFailed:
		vals.flag("failed", true)
		vals["why"] = strings.TrimRight(oneLine(why, maxQuote), ". ")
	case MentionResource:
		vals.flag("resource", true)
	default:
		vals.flag("file", true)
	}
	return s.fill(textMention, vals)
}

// StructuredOutput returns the request for a run's result as JSON that
// conforms to schema (--json-schema); problem, when not empty, is what was
// wrong with the value given before.
func (s *Set) StructuredOutput(schema, problem string) string {
	vals := values{"schema": strings.TrimSpace(schema)}
	if problem = strings.TrimSpace(problem); problem != "" {
		vals["problem"] = strings.TrimRight(oneLine(problem, maxQuote), ". ")
	}
	return s.fill(textStructured, vals)
}

// AgentMention returns the line for a kind of sub-agent a person named in
// a prompt with @agent-: hand the task to that kind.
func (s *Set) AgentMention(name string) string {
	return s.fill(textAgentMention, values{"name": oneLine(name, maxName)})
}

// Recap returns the question of /recap: the session in one line, for the
// person, in a request beside the session's that is not kept.
func (s *Set) Recap() string {
	return s.fill(textRecap, nil)
}

// ThinkingBudget returns the words put at the end of a model's thinking
// when its budget for thinking runs out.
func (s *Set) ThinkingBudget() string {
	return s.fill(textThinkingBudget, nil)
}

// NextPrompt returns the question asked after a turn when prompt
// suggestions are on: the person's likely next prompt, in their words, in
// a request beside the session's that is not kept.
func (s *Set) NextPrompt() string {
	return s.fill(textNextPrompt, nil)
}

// StatusLine returns the prompt of /statusline: what the user wants the
// status line to show, where the script and the settings file are, and the
// session's own JSON as a status line command is handed it.
func (s *Set) StatusLine(wish, script, settings, sample string) string {
	return s.fill(textStatusLine, values{"wish": oneLine(wish, maxQuote), "script": oneLine(script, maxName), "settings": oneLine(settings, maxName), "sample": sample})
}

// PrintWrapUp returns what a run (-p) with a time budget tells the model
// once most of the budget is used: wrap up and end the turn.
func (s *Set) PrintWrapUp(used, budget string) string {
	return s.fill(textPrintWrapUp, values{"used": oneLine(used, maxName), "budget": oneLine(budget, maxName)})
}

// PrintWait returns the checkpoint of a run (-p) that has waited long for
// background work it started: how long it has waited, and what still runs.
func (s *Set) PrintWait(waited, work string) string {
	return s.fill(textPrintWait, values{"waited": oneLine(waited, maxName), "work": oneLine(work, maxQuote)})
}

// PrintLeftovers returns the line that tells the model, once, as a run is
// about to end, what its commands left running, which the end stops: left
// is the process groups, as the commands' results named them.
func (s *Set) PrintLeftovers(left string) string {
	return s.fill(textPrintLeftovers, values{"left": oneLine(left, maxQuote)})
}

// SummaryIntro returns the line that stands just before a summary, where
// the summary takes the place of the conversation it replaced. The summary
// itself follows it, outside the marker: it is the model's own text.
func (s *Set) SummaryIntro() string {
	return s.fill(textSummaryIntro, nil)
}

// AfterSummary returns what the model is told right after a summary: carry
// on from it, without asking. path is the file the replaced conversation
// was written to, if it was.
func (s *Set) AfterSummary(path string) string {
	return s.fill(textAfterSummary, values{"path": oneLine(path, maxName)})
}

// FileShownBack returns the line before a file given back after a summary,
// which what Read returns for it follows.
func (s *Set) FileShownBack(path string) string {
	vals := values{"path": oneLine(path, maxName)}
	vals.flag("shown", true)
	return s.fill(textFilesBack, vals)
}

// FilesNotShownBack returns the line that names the files read or changed
// before a summary that are not given back after it.
func (s *Set) FilesNotShownBack(paths []string) string {
	vals := values{"path": oneLine(strings.Join(paths, ", "), maxQuote)}
	vals.flag("named", true)
	return s.fill(textFilesBack, vals)
}

// EarlierLeftOut returns the note that opens a summary request whose
// earliest turns had to be left out for the request to fit.
func (s *Set) EarlierLeftOut() string {
	return s.fill(textEarlierLeftOut, nil)
}

// tokenCount writes a number of tokens the way gnosh shows them: 850, 44K,
// 1.2M.
func tokenCount(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	if k := (n + 500) / 1000; k < 1000 {
		return strconv.Itoa(k) + "K"
	}
	return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
}
