package shelltool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// Task is a background command as it stands at one moment.
type Task struct {
	// ID is the id the model was given, such as "bg1".
	ID string
	// Command is the command exactly as it was run.
	Command string
	// Description is what the model said the command does; it may be empty.
	Description string
	// PID is the process id of the command's shell. It is also the id of
	// the shell's process group and session.
	PID int
	// OutputFile is the file that collects the command's output.
	OutputFile string
	// OutputBytes is how much the command has printed.
	OutputBytes int64
	// Started is when the command started.
	Started time.Time
	// Duration is how long the command has run, or ran.
	Duration time.Duration
	// Running says whether the command's shell is still running. The
	// fields below are set only when it is not.
	Running bool
	// Reason is why the command ended: one of the Reason constants.
	Reason string
	// ExitCode is the shell's exit code: 128+n when signal n killed it, and
	// -1 when it is not known.
	ExitCode int
	// Signal names the signal that killed the shell, such as "SIGKILL"; it
	// is empty when the shell exited by itself.
	Signal string
	// LeftRunning lists the process groups of what the command left
	// running, still holding its output open, when it ended.
	LeftRunning []int
	// Monitor says the command is a Monitor's: what it prints is passed on
	// as it comes.
	Monitor bool
	// Owner is the sub-agent that started the command, by its session id
	// (core.ToolEnv.SessionID of a sub-agent's call), or "" when the
	// session's own model did.
	Owner string
}

// Ended is the news that a background command has ended, as Options.OnEnd
// gets it.
type Ended struct {
	// Task is the command as it ended.
	Task
	// Summary is one line that says which command ended, by its id and
	// its description (or, when it has none, its first line), and how:
	// "bg1 (run the tests) failed with exit code 2 after 41.3 s".
	Summary string
	// Tail is the end of what the command printed: at most
	// Options.TailChars characters, starting at the start of a line.
	Tail string
}

// task returns the command as it stands at now.
func (j *job) task(now time.Time) Task {
	total, _, _ := j.out.size()
	t := Task{
		ID:          j.taskID(),
		Command:     j.command,
		Description: j.description,
		PID:         j.pid,
		OutputFile:  j.out.path,
		OutputBytes: total,
		Started:     j.started,
		Duration:    now.Sub(j.started),
		Running:     true,
		Monitor:     j.mon != nil,
		Owner:       j.owner,
	}
	if e := j.ending(); e != nil {
		t.Running = false
		t.Duration = e.at.Sub(j.started)
		t.Reason = e.reason.String()
		t.ExitCode = e.code
		if e.sig != 0 {
			t.Signal = signalName(e.sig)
		}
		if !e.swept {
			_, t.LeftRunning = describeGroups(e.left)
		}
	}
	return t
}

// how says in a few words how the command ended: "finished with exit code
// 0 after 3.2 s".
func (j *job) how(e *ending) string {
	took := seconds(e.at.Sub(j.started))
	text := ""
	switch e.reason {
	case endTimeout:
		text = "was stopped when its time, " + seconds(e.limit) + ", ran out"
	case endStopped:
		text = "was stopped after " + took
	case endClosed:
		text = "was stopped after " + took + " because the session is ending"
	case endCancelled:
		text = "was interrupted after " + took + " and stopped"
	case endOutputLimit:
		text = "was stopped after printing more than " + byteSize(j.m.opt.MaxOutputBytes)
	default:
		switch {
		case !e.reaped || e.waitErr != nil:
			text = "ended after " + took + ", but its exit status could not be read"
		case e.sig != 0:
			text = fmt.Sprintf("was killed by %s (%s) after %s", signalName(e.sig), e.sig, took)
		case e.code == 0:
			text = "finished with exit code 0 after " + took
		default:
			text = fmt.Sprintf("failed with exit code %d after %s", e.code, took)
		}
	}
	if len(e.alive) > 0 {
		text += ", but these could not be killed and are still running: " + describeProcs(e.alive)
	}
	if e.escaped {
		text += "; " + escapedNote
	}
	if e.held {
		switch groups, _ := describeGroups(e.left); {
		case e.swept:
			text += "; it left a process running, which was stopped"
		case closed(j.pumped) && j.letGo.Load():
			text += "; the process it left running let go of its output since, and runs on: it will be stopped when the session ends"
		case closed(j.pumped):
			text += "; the process it left running has ended since"
		case groups != "":
			text += "; it left a process running that still holds its output open: " + groups
		default:
			text += "; a process it started still holds its output open"
		}
	}
	return text
}

// status says in a few words where the command stands, given how it ended
// (nil while it runs).
func (j *job) status(e *ending, now time.Time) string {
	if e == nil {
		return fmt.Sprintf("%s is still running, %s so far", j.taskID(), seconds(now.Sub(j.started)))
	}
	return j.taskID() + " " + j.how(e)
}

// stopAndWait asks for the command to be stopped and waits until it has
// ended, or until ctx is cancelled.
func (j *job) stopAndWait(ctx context.Context) error {
	j.ask(endStopped)
	select {
	case <-j.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("shelltool: waiting for %s to stop: %w", j.label(), context.Cause(ctx))
	}
}

// unknownTask is what a call that names no known background command is
// told: the commands there are, so that it can name the right one.
func (m *Manager) unknownTask(id string) string {
	tasks := m.Tasks()
	if len(tasks) == 0 {
		return fmt.Sprintf("There is no background command %q: this session has started none. %s starts one when %s is true.", id, BashName, argBackground)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "There is no background command %q. This session's background commands are:", id)
	const show = 30
	if len(tasks) > show {
		fmt.Fprintf(&b, "\n(the %d before these have all ended)", len(tasks)-show)
		older := tasks[:len(tasks)-show]
		tasks = tasks[len(tasks)-show:]
		for _, t := range older {
			if t.Running {
				tasks = append([]Task{t}, tasks...)
			}
		}
	}
	for _, t := range tasks {
		state := "running"
		if !t.Running {
			state = "ended (" + t.Reason
			if t.Reason == ReasonExited {
				state += fmt.Sprintf(", exit code %d", t.ExitCode)
			}
			state += ")"
		}
		fmt.Fprintf(&b, "\n- %s: %s: %s", t.ID, state, firstLine(t.Command, 80))
	}
	return b.String()
}

// outputTool is the TaskOutput tool: it returns what a background command
// has printed since it was last asked.
type outputTool struct{ m *Manager }

// Spec says what the model is told about the tool.
func (t outputTool) Spec() core.ToolSpec { return t.m.outputSpec() }

// Describe says what a call would do. Reading the output of the session's
// own command touches nothing, so it names no path to ask about.
func (t outputTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	a, err := taskArgsOf(call.Input)
	if err != nil {
		return core.Action{}, err
	}
	return core.Action{Kind: core.ActionRead, Summary: "Read the new output of background command " + a.id}, nil
}

// Run returns the command's new output and where the command stands.
func (t outputTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	m := t.m
	a, err := taskArgsOf(call.Input)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	j := m.find(a.id)
	if j == nil {
		return core.ErrorResult(m.unknownTask(a.id)), nil
	}
	// How the command stands is looked at before its output is read. A
	// command seen to have ended has all its output in, so the model is
	// never told of an end without being given the last of the output.
	e := j.ending()
	running := e == nil
	r := j.read(a.filter, m.opt.MaxOutputChars)
	if r.err != nil {
		return core.ErrorResult(fmt.Sprintf("The output of %s could not be read from %s: %v", a.id, j.out.path, r.err)), nil
	}
	if !running {
		// The model now knows that it ended; it need not be told again.
		j.claim()
	}
	body := r.text
	if strings.TrimRight(body, "\n") == "" {
		switch {
		case a.filter != nil && r.passed > 0:
			body = fmt.Sprintf("(no new line matches the filter; %s passed over)", plural(r.passed, "new line"))
		case running || !closed(j.pumped):
			body = "(no new output)"
		default:
			body = "(no more output)"
		}
	}
	meta := map[string]any{
		metaTaskIDPlain: a.id,
		metaRunning:     running,
		metaOutputFile:  j.out.path,
		metaOutputBytes: r.total,
		metaTruncated:   r.cut,
	}
	if e != nil {
		meta[metaExitCode] = e.code
		meta[metaReason] = e.reason.String()
		if e.sig != 0 {
			meta[metaSignal] = signalName(e.sig)
		}
	}
	m.where(meta, j.host, j.dir)
	text := m.stamp(j.host, j.dir) + "\n" + compose("", body, []string{j.status(e, m.opt.Now())}, true)
	return core.ToolResult{Content: []core.Block{core.Text(text)}, Meta: meta}, nil
}

// stopTool is the TaskStop tool: it stops a background command.
type stopTool struct{ m *Manager }

// Spec says what the model is told about the tool.
func (t stopTool) Spec() core.ToolSpec { return t.m.stopSpec() }

// Describe says what a call would do.
func (t stopTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	a, err := taskArgsOf(call.Input)
	if err != nil {
		return core.Action{}, err
	}
	summary := "Stop background command " + a.id
	if j := t.m.find(a.id); j != nil {
		summary += ": " + firstLine(j.command, 120)
	}
	return core.Action{Kind: core.ActionOther, Summary: summary}, nil
}

// Run stops the command and says how it ended.
func (t stopTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	m := t.m
	a, err := taskArgsOf(call.Input)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	j := m.find(a.id)
	if j == nil {
		return core.ErrorResult(m.unknownTask(a.id)), nil
	}
	// Whatever happens next, the model learns of the end from this call.
	j.claim()
	already := j.ending() != nil
	if !already {
		if err := j.stopAndWait(ctx); err != nil {
			return core.ErrorResult(fmt.Sprintf("The call was interrupted while %s was being stopped. It has been told to stop and is on its way out.", a.id)), nil
		}
	}
	e := j.ending()
	how := j.how(e)
	var text string
	switch {
	case already:
		text = fmt.Sprintf("%s had already ended: it %s.", a.id, how)
	case e.reason != endStopped:
		text = fmt.Sprintf("%s ended before it could be stopped: it %s.", a.id, how)
	default:
		text = fmt.Sprintf("Stopped %s (%s): it %s; %s.", a.id, firstLine(j.command, 80), how, stopDetail(e))
	}
	j.readMu.Lock()
	total, _, _ := j.out.size()
	unread := total - j.offset
	j.readMu.Unlock()
	switch {
	case j.mon != nil:
		// A Monitor's lines came as notes, not by reading: what is left
		// of them is in the file.
		text += fmt.Sprintf(" Everything it printed is in %s.", j.out.path)
	case unread > 0:
		text += fmt.Sprintf(" %s of its output have not been read; the whole output is in %s.", byteSize(unread), j.out.path)
	}
	meta := map[string]any{
		metaTaskIDPlain: a.id,
		metaTaskType:    taskTypeShell,
		metaCommand:     j.command,
		metaRunning:     false,
		metaExitCode:    e.code,
		metaReason:      e.reason.String(),
		metaOutputFile:  j.out.path,
	}
	if e.sig != 0 {
		meta[metaSignal] = signalName(e.sig)
	}
	return core.ToolResult{
		Content: []core.Block{core.Text(text)},
		IsError: len(e.alive) > 0,
		Meta:    meta,
	}, nil
}

// stopDetail says how a command that was told to stop took it.
func stopDetail(e *ending) string {
	switch {
	case !e.reaped:
		return "its shell had not ended when this was written"
	case e.sig == syscall.SIGKILL:
		return "it did not exit when asked and was killed"
	case e.sig != 0:
		return "its shell ended on " + signalName(e.sig)
	}
	return fmt.Sprintf("its shell exited with code %d when asked", e.code)
}

// reading is what one TaskOutput call got.
type reading struct {
	text   string // the new output, as the model is shown it
	cut    bool   // some of it was left out of text
	total  int64  // how much the command has printed in all
	passed int64  // new lines the filter passed over
	err    error  // why the output file could not be read
}

// read returns the output the model has not been given yet, and moves the
// mark of what it has been given. With a filter only matching lines are
// returned. A last line that has no newline yet is held back for the next
// read while more output can still come, so that no line is ever seen in
// two halves.
func (j *job) read(filter *regexp.Regexp, limit int) reading {
	j.readMu.Lock()
	defer j.readMu.Unlock()

	// Whether more can come must be known before looking at what is there:
	// the other way round, output could arrive in between and a line still
	// growing would be taken as finished.
	final := closed(j.pumped)
	fileEnd, rest, lost, total := j.out.from(j.offset)
	_, spooled, _ := j.out.size()

	view := newCapture(2 * limit)
	var in intake = &passAll{out: view}
	if filter != nil {
		in = &lineFilter{re: filter, out: view}
	}
	if j.offset < fileEnd {
		file, err := os.Open(j.out.path)
		if err != nil {
			return reading{err: err}
		}
		_, err = io.Copy(in, io.NewSectionReader(file, j.offset, fileEnd-j.offset))
		file.Close()
		if err != nil {
			return reading{err: err}
		}
	}
	if lost > 0 {
		in.gap()
		fmt.Fprintf(view, "[%s of output here were not kept: the output file is full]\n", byteSize(lost))
	}
	in.Write(rest)
	j.offset = total - int64(in.finish(final))

	text, cut := view.view(limit, func(lines int64) string {
		return gapLine(lines, j.out.path, spooled, total)
	})
	r := reading{text: text, cut: cut, total: total}
	if f, ok := in.(*lineFilter); ok {
		r.passed = f.passed
	}
	return r
}

// intake takes a stretch of a command's output on its way to the model.
type intake interface {
	io.Writer
	// gap says that some output is missing at this point of the stream.
	gap()
	// finish ends the stretch. With final set nothing more can follow, so
	// everything is passed on. Otherwise an unfinished last piece is held
	// back, and finish returns how many bytes of the stream that is.
	finish(final bool) (held int)
}

// passAll passes output on as it is, taking care only never to split a
// character.
type passAll struct {
	out  *capture
	held []byte // the start of a character whose end has not come
}

func (p *passAll) Write(b []byte) (int, error) {
	n := len(b)
	if len(p.held) > 0 {
		b = append(p.held, b...)
		p.held = nil
	}
	k := incompleteSuffix(b)
	p.out.Write(b[:len(b)-k])
	if k > 0 {
		p.held = bytes.Clone(b[len(b)-k:])
	}
	return n, nil
}

func (p *passAll) gap() {
	p.out.Write(p.held)
	p.held = nil
}

func (p *passAll) finish(final bool) int {
	if !final {
		return len(p.held)
	}
	p.gap()
	return 0
}

// lineFilter passes on the lines that match a regular expression.
type lineFilter struct {
	re     *regexp.Regexp
	out    *capture
	line   []byte // the line being put together, cut at maxLineBytes
	size   int    // how many bytes the line has, cut or not
	broken bool   // the line's start is missing, so it is dropped
	passed int64  // lines that did not match
}

func (f *lineFilter) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			f.grow(b)
			break
		}
		f.grow(b[:i])
		f.end(true)
		b = b[i+1:]
	}
	return n, nil
}

// grow adds to the line being put together.
func (f *lineFilter) grow(b []byte) {
	f.size += len(b)
	if room := maxLineBytes - len(f.line); len(b) > room {
		b = b[:max(room, 0)]
	}
	f.line = append(f.line, b...)
}

// end closes the line being put together and passes it on if it matches.
func (f *lineFilter) end(newline bool) {
	switch {
	case f.broken:
		f.broken = false
	case f.re.Match(bytes.TrimSuffix(f.line, []byte{'\r'})):
		f.out.Write(f.line)
		if newline {
			f.out.Write([]byte{'\n'})
		}
	default:
		f.passed++
	}
	f.line, f.size = f.line[:0], 0
}

func (f *lineFilter) gap() {
	if f.size > 0 {
		f.end(false)
	}
	f.broken = true
}

func (f *lineFilter) finish(final bool) int {
	switch {
	case f.size == 0:
		return 0
	case final || f.broken:
		f.end(false)
		return 0
	}
	return f.size
}
