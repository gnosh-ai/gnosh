package shelltool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// bashTool is the Bash tool: it runs one shell command, on this machine or
// on the declared host the call names.
type bashTool struct {
	m *Manager
	// person says the commands are the person's own, typed into the
	// session: the rules and notes that steer a model are left out.
	person bool
}

// Spec says what the model is told about the tool.
func (t bashTool) Spec() core.ToolSpec { return t.m.bashSpec() }

// Describe says what a call would run, for permission rules and for the
// person who may be asked. Its error is the same refusal Run would give,
// and beside it the Action still names the command, as far as it could be
// read. A call for another machine carries that machine's name in its
// host argument, which is where the permission rules read it, and in the
// Action's summary, which is what a person is shown.
func (t bashTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	a, err := t.m.bashArgs(call.Input)
	if err == nil {
		err = t.m.rules(a, env, t.person)
	}
	if a.command == "" {
		return core.Action{}, err
	}
	return core.Action{Kind: core.ActionExecute, Command: a.command, Summary: a.summary()}, err
}

// Run runs the command. In the foreground it returns when the command has
// ended, been stopped, or been moved to the background; in the background
// it returns as soon as the command has started.
func (t bashTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	m := t.m
	a, err := m.bashArgs(call.Input)
	if err == nil {
		err = m.rules(a, env, t.person)
	}
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	if env == nil || !filepath.IsAbs(env.Cwd) {
		return core.ToolResult{}, errors.New("shelltool: the tool was given no working directory to run the command in")
	}
	if !a.background && a.host == nil && call.ID != "" {
		defer m.expect(call.ID)()
	}
	if ctx.Err() != nil {
		return core.ErrorResult("The command was not started: the call was cancelled first."), nil
	}
	j, err := m.start(a, env, call.ID)
	if err != nil {
		var no refusal
		if errors.As(err, &no) {
			return core.ErrorResult(string(no)), nil
		}
		return core.ToolResult{}, err
	}
	if a.background {
		return m.startedResult(j, a), nil
	}
	outcome := j.attend(ctx, a.limit)
	j.detach()
	m.unlist(j)
	if outcome != attendEnded {
		return m.movedResult(j, a, outcome == attendTimedOut), nil
	}
	return m.finishedResult(j, a, env, t.person), nil
}

// start starts a command. A refusal is for the model; any other error is
// the harness's own.
func (m *Manager) start(a bashArgs, env *core.ToolEnv, callID string) (*job, error) {
	cwd := env.Cwd
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		if cwd = nearestStanding(env); cwd == "" {
			return nil, refusal(fmt.Sprintf("The command was not run: the working directory %s is not there any more, and neither is any folder of the session's.", stampText(env.Cwd)))
		}
	}
	ssh := ""
	if a.host != nil {
		var err error
		if ssh, err = m.sshProgram(a.host); err != nil {
			return nil, err
		}
	}
	dir, err := m.spoolDir()
	if err != nil {
		return nil, err
	}
	j := newJob(m, a, cwd)
	if cwd != env.Cwd {
		j.gone = env.Cwd
	}
	j.ssh = ssh
	if env.ParentSessionID != "" {
		// A sub-agent's: the news of its end is the sub-agent's.
		j.owner = env.SessionID
	}
	name, err := m.admit(j, a.background, callID)
	if err != nil {
		return nil, err
	}
	if env.Progress != nil && !a.background {
		j.sink.Store(newProgressSink(env.Progress, callID))
	}
	if a.watch && m.opt.OnMonitor != nil {
		j.mon = newWatcher(j, m.opt.OnMonitor)
		j.sink.Store(newProgressSink(j.mon.take, callID))
	}
	if err := j.launch(dir, name); err != nil {
		j.detach()
		if j.mon != nil {
			j.mon.close()
		}
		m.forget(j)
		close(j.done)
		return nil, err
	}
	if a.background {
		m.publish(j)
		m.wg.Go(func() { j.background(a.limit) })
	}
	return j, nil
}

// finishedResult is the result of a foreground command that has ended.
// person says the command was the person's own, which gets no notes that
// are there to steer a model.
func (m *Manager) finishedResult(j *job, a bashArgs, env *core.ToolEnv, person bool) core.ToolResult {
	e := j.ending()
	failed := e.failed()

	// For a command on another machine: whether it started there, and
	// whether the exit code is the command's own. ssh passes the command's
	// exit code on only when the command ended by itself over a connection
	// that held; what ssh ends with when it is stopped, or when it fails,
	// says nothing of the command.
	var far farEnd
	dir, own, shell := j.dir, true, "the shell"
	if j.host != nil {
		far = j.farEnd(e)
		dir = far.dir
		failed = failed || far.unsure
		own = e.reason == endExited && far.reason == ""
		shell = "the ssh on this machine"
	}

	meaning := ""
	if e.reason == endExited && e.code == 1 && own {
		if meaning = harmlessExit(a.command); meaning != "" {
			failed = false
		}
	}
	limit := m.opt.MaxOutputChars
	if failed {
		limit = m.opt.MaxFailedOutputChars
	}
	body, cut, total, spooled := j.out.view(limit)
	_, _, spoolErr := j.out.size()
	took := e.at.Sub(j.started)

	meta := map[string]any{
		metaReason:      e.reason.String(),
		metaInterrupted: e.reason == endCancelled || e.reason == endClosed,
		metaDuration:    took.Milliseconds(),
		metaOutputBytes: total,
		metaTruncated:   cut,
	}
	m.where(meta, j.host, dir)
	if own {
		meta[metaExitCode] = e.code
	}
	if far.reason != "" {
		meta[metaReason] = far.reason
	}
	res := core.ToolResult{IsError: failed, Meta: meta}

	// What happened to the command, when it did not simply end: this goes
	// above the output.
	headline := far.headline
	switch e.reason {
	case endTimeout:
		headline = "timed out after " + seconds(e.limit) + " and was stopped; the output so far is below"
		if a.asked > 0 {
			headline += fmt.Sprintf(" (the %s asked for is over the maximum, so it was lowered to %s)", seconds(a.asked), seconds(e.limit))
		}
		if e.limit < time.Second {
			headline += fmt.Sprintf(" (%s is in milliseconds)", argTimeout)
		}
		meta[metaTimedOutAfter] = e.limit.Milliseconds()
	case endCancelled:
		headline = "interrupted after " + seconds(took) + " and stopped; the output so far is below"
	case endClosed:
		headline = "stopped after " + seconds(took) + " because the session is ending; the output so far is below"
	case endOutputLimit:
		headline = "stopped after printing more than " + byteSize(m.opt.MaxOutputBytes) + "; the start and the end of the output are below"
	}

	// The rest goes under the output.
	var notes []string
	switch {
	case !e.reaped:
		notes = append(notes, fmt.Sprintf("%s (pid %d) had not ended when this was written", shell, j.pid))
	case !own:
		// How ssh ended is not how the command ended.
	case e.waitErr != nil:
		notes = append(notes, fmt.Sprintf("the exit status could not be read: %v", e.waitErr))
	case e.sig != 0 && e.reason == endExited:
		notes = append(notes, fmt.Sprintf("%s was killed by %s (%s)", shell, signalName(e.sig), e.sig))
		meta[metaSignal] = signalName(e.sig)
	case e.sig != 0:
		meta[metaSignal] = signalName(e.sig)
	case meaning != "":
		notes = append(notes, "exit code 1: "+meaning)
		meta[metaExitMeaning] = meaning
	case e.code == 255 && j.host != nil:
		notes = append(notes, "exit code 255 (the command's own, as far as ssh said; it is also what ssh ends with when a connection breaks and ssh is set to say nothing)")
	case e.code != 0:
		notes = append(notes, fmt.Sprintf("exit code %d", e.code))
	}
	if !person {
		notes = append(notes, m.hints(a, e, body, own && !far.unsure && e.reaped && e.sig == 0)...)
	}
	notes = append(notes, far.notes...)
	if len(e.alive) > 0 {
		notes = append(notes, "could not stop: "+describeProcs(e.alive)+"; they are still running")
	}
	if e.escaped {
		notes = append(notes, escapedNote)
	}
	if e.held {
		note, pgids := m.leftNote(j, e)
		notes = append(notes, note)
		if len(pgids) > 0 {
			meta[metaLeftRunning] = pgids
		}
	}
	if groups, pgids := describeGroups(e.detached); groups != "" {
		targets := make([]string, len(pgids))
		for i, pgid := range pgids {
			targets[i] = fmt.Sprintf("-%d", pgid)
		}
		notes = append(notes, fmt.Sprintf("the command ended and left a process running that let go of its output: %s. It will be stopped when the session ends; one started with setsid is left running. To stop it now: kill -- %s",
			groups, strings.Join(targets, " ")))
		meta[metaLeftRunning] = pgids
	}
	if e.unread && !e.held && !e.escaped {
		notes = append(notes, "the last of the output had not been read when this was written; this machine is very slow just now, or the disk the output file is on has stopped taking it")
	}
	if cut && spoolErr != nil {
		notes = append(notes, fmt.Sprintf("the output file stopped at %s: %v", byteSize(spooled), spoolErr))
	}

	// Where the session is now. A command on another machine never moves
	// it: a directory there is not a directory here.
	ownExit := e.reason == endExited && e.reaped && e.sig == 0
	if j.gone != "" {
		notes = append(notes, fmt.Sprintf("the working directory %s was not there any more, so the command ran in %s", stampText(j.gone), stampText(j.dir)))
	}
	// (Folder names go into the notes as the stamp's do: a name with a
	// line break in it could otherwise pass for a note of gnosh's.)
	switch where := place(e.finalDir, env); {
	case j.host != nil:
	case ownExit && e.finalDir == "" && !isDir(j.dir):
		// The command took its own working directory away (a checkout
		// without the folder, a clean): the session goes to the nearest
		// folder above it that is there.
		gone := *env
		gone.Cwd = j.dir
		if alt := nearestStanding(&gone); alt != "" {
			res.Cwd = alt
			notes = append(notes, fmt.Sprintf("the command took away its working directory %s, so the working directory is now %s", stampText(j.dir), stampText(alt)))
		} else {
			notes = append(notes, fmt.Sprintf("the command took away its working directory %s, and no folder of the session's is there", stampText(j.dir)))
		}
	case ownExit && e.finalDir == "":
		notes = append(notes, "the shell did not say where it ended (the command replaced it with exec, or set an EXIT trap of its own), so the working directory is still "+stampText(env.Cwd))
	case ownExit && where.to != "":
		res.Cwd = where.to
		notes = append(notes, "working directory is now "+stampText(where.to))
	case ownExit && where.checkout:
		notes = append(notes, fmt.Sprintf("working directory reset to %s: %s is in the checkout your work tree was made from, so the session does not follow the command there", stampText(env.Cwd), stampText(where.outside)))
	case ownExit && where.outside != "":
		notes = append(notes, fmt.Sprintf("working directory reset to %s: %s is outside the project, so the session does not follow the command there", stampText(env.Cwd), stampText(where.outside)))
	case !ownExit && (where.to != "" || where.outside != ""):
		notes = append(notes, "the command did not end by itself, so its change of directory is not kept: the working directory is still "+stampText(env.Cwd))
	}

	// The output file is kept only when it holds something the model was
	// not shown, or is still being written to.
	if cut || e.held {
		meta[metaOutputFile] = j.out.path
		meta[metaOutputFileSize] = spooled
		m.retain(j.out.path, spooled)
	} else {
		os.Remove(j.out.path)
	}

	// A command that never started has no output to speak of.
	text := compose(headline, body, notes, far.reason != ReasonNoConnection)
	res.Content = []core.Block{core.Text(m.stamp(j.host, dir) + "\n" + text)}
	res.Display = &core.Display{Kind: "command", Title: a.summary(), Text: text}
	return res
}

// where puts into a result's Meta which machine the command was for and in
// which directory it started there.
func (m *Manager) where(meta map[string]any, h *Host, dir string) {
	meta[metaHost], meta[metaMachine] = LocalName, m.local
	if h != nil {
		meta[metaHost], meta[metaMachine] = h.Name, h.Target
	}
	if dir != "" {
		meta[metaDir] = dir
	}
}

// escapedNote is the news that stopping a command did not reach everything
// it had started.
const escapedNote = "a process the command started had moved to a session of its own, where stopping the command does not reach; it still held the command's output open, is probably still running, and is no longer listened to"

// leftNote words the news that an ended command left something running
// that still holds its output, and returns the process groups it found.
func (m *Manager) leftNote(j *job, e *ending) (string, []int) {
	groups, pgids := describeGroups(e.left)
	if groups == "" {
		// The session could not be listed, or holds nothing: ask after the
		// shell's own group, which is where such a process usually is.
		if err := m.signal(-j.pid, 0); err == nil {
			groups, pgids = fmt.Sprintf("process group %d", j.pid), []int{j.pid}
		}
	}
	if groups == "" {
		if e.swept {
			return "the command ended, but a process it started, which has since left its session, still held its output open; that process was not stopped, cannot be named, and is no longer listened to", nil
		}
		return "the command ended, but a process it started, which has since left its session, still holds its output open; that process was not stopped and cannot be named", nil
	}
	if e.swept {
		// It was stopped, so it is not reported as running.
		return fmt.Sprintf("the command ended but left a process running that still held its output open: %s. Too many processes left like that are already being listened to, so this one was stopped. For a process that should keep running, use %s.",
			groups, argBackground), nil
	}
	targets := make([]string, len(pgids))
	for i, pgid := range pgids {
		targets[i] = fmt.Sprintf("-%d", pgid)
	}
	return fmt.Sprintf("the command ended but left a process running that still holds its output open: %s. What that prints from now on goes only to %s. It will be stopped when the session ends; to stop it now: kill -- %s",
		groups, j.out.path, strings.Join(targets, " ")), pgids
}

// startedResult is the result of a command started in the background.
func (m *Manager) startedResult(j *job, a bashArgs) core.ToolResult {
	id := j.taskID()
	var b strings.Builder
	fmt.Fprintf(&b, "Started in the background as %s (process group %d).\n", id, j.pid)
	b.WriteString(m.backgroundHelp(j, a.limit))
	text := b.String()
	meta := map[string]any{
		metaTaskID:     id,
		metaOutputFile: j.out.path,
	}
	m.where(meta, j.host, j.dir)
	return core.ToolResult{
		Content: []core.Block{core.Text(m.stamp(j.host, j.dir) + "\n" + text)},
		Display: &core.Display{Kind: "command", Title: a.summary(), Text: text},
		Meta:    meta,
	}
}

// backgroundHelp says where a background command's output is, how to read
// it and stop it, and whether its end will be announced.
func (m *Manager) backgroundHelp(j *job, limit time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Its output is collected in %s.", j.out.path)
	if m.opt.NoOutputTool {
		fmt.Fprintf(&b, " Read that file to see it; %s stops the command.", StopName)
	} else {
		fmt.Fprintf(&b, " %s returns what it has printed since you last asked; %s stops it.", OutputName, StopName)
	}
	if limit > 0 {
		fmt.Fprintf(&b, " It is stopped if it is still running after %s.", seconds(limit))
	}
	if m.opt.OnEnd != nil {
		b.WriteString(" You will be told when it ends, so there is no need to wait for it or to keep checking.")
	}
	return b.String()
}

// movedResult is the result of a foreground command that was moved to the
// background, where it is still running. timedOut says whether its time
// ran out; otherwise the person asked for the move.
func (m *Manager) movedResult(j *job, a bashArgs, timedOut bool) core.ToolResult {
	id := j.taskID()
	// From here on TaskOutput returns only what the model has not been
	// shown in this result.
	j.readMu.Lock()
	body, cut, total, _ := j.out.view(m.opt.MaxOutputChars)
	j.offset = total
	j.readMu.Unlock()

	meta := map[string]any{
		metaTaskID:      id,
		metaOutputFile:  j.out.path,
		metaOutputBytes: total,
		metaTruncated:   cut,
	}
	m.where(meta, j.host, j.dir)
	headline := fmt.Sprintf("moved to the background as %s at the person's request, where it is still running; what it has printed so far is below", id)
	if timedOut {
		headline = fmt.Sprintf("still running after %s, so it was moved to the background as %s; what it has printed so far is below", seconds(a.limit), id)
		meta[metaTimedOutAfter] = a.limit.Milliseconds()
	}
	notes := []string{
		m.backgroundHelp(j, m.opt.BackgroundTimeout),
		"a change of directory it makes will not carry over: the working directory is still " + j.dir,
	}
	text := compose(headline, body, notes, true)
	return core.ToolResult{
		Content: []core.Block{core.Text(m.stamp(j.host, j.dir) + "\n" + text)},
		Display: &core.Display{Kind: "command", Title: a.summary(), Text: text},
		Meta:    meta,
	}
}

// compose lays out a result under its first line, which says where the
// command ran: a line about what happened to the command, if there is one;
// the output; then the notes, each in brackets so that they cannot be taken
// for output. ran is false for a command that never started, which has no
// output to show or to call empty.
func compose(headline, body string, notes []string, ran bool) string {
	var parts []string
	if headline != "" {
		parts = append(parts, "["+headline+"]")
	}
	switch body = strings.TrimRight(body, "\n"); {
	case body != "":
		parts = append(parts, body)
	case ran:
		parts = append(parts, "(no output)")
	}
	text := strings.Join(parts, "\n")
	if len(notes) > 0 && (body != "" || ran) {
		// A blank line sets the notes apart from the output.
		text += "\n"
	}
	for _, note := range notes {
		if text != "" {
			text += "\n"
		}
		text += "[" + note + "]"
	}
	return text
}
