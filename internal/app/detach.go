package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/shellquote"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// This file is /background (also /bg), as Claude Code has it: the session
// goes on without this terminal. It ends here as /exit ends it, and gnosh
// starts again in it as a run of its own (gnosh -p, resumed, with the
// session's flags as they stand) in a process that outlives this one, on
// the prompt given; once the run is done, gnosh -r opens the session again
// to read and go on. The run's permission questions wait for an answer in
// gnosh agents (runasks.go); its output goes to a file beside the
// session's.

// detachPlan is what /background goes on with once the session is closed.
type detachPlan struct {
	session, name, prompt, log string
	// cwd is the folder the run works in, and path the session's file,
	// which the run holds while it works.
	cwd, path string
}

// detachedPlan is the plan /background made, or nil.
func (p *program) detachedPlan() *detachPlan {
	p.restartMu.Lock()
	defer p.restartMu.Unlock()
	return p.detached
}

// detach starts the run /background asked for and says how to come back
// to the session, or, when it cannot, how to go on by hand.
func (p *program) detach(args []string, d *detachPlan) int {
	run := append(append(args, "--print", "--"), d.prompt)
	p.logHead(d.log, d.session, d.name, d.cwd, d.path, d.prompt)
	pid, err := p.o.Detach(run, prepareRun(p.o.Environ, d.log), d.log)
	if err != nil {
		p.errf("the session could not go on in the background (%v). It is kept: gnosh %s runs the prompt, and gnosh -r %s opens it.", err, shellquote.Words(run), d.session)
		return cli.ExitFailure
	}
	writeRunPID(d.log, pid)
	fmt.Fprintf(p.stderr, "gnosh: the session goes on in the background (process %d). What it prints is in %s; gnosh -r %s opens it again once it is done.\n", pid, d.log, d.session)
	return cli.ExitOK
}

// slashBackground is /background <prompt>.
func (l *live) slashBackground(ctx context.Context, c *SlashContext) error {
	prompt := strings.TrimSpace(c.Args)
	switch {
	case l.p.o.Detach == nil:
		return errors.New("this gnosh cannot go on without its terminal")
	case prompt == "":
		return errors.New("say what the session is to do on its own: /background <prompt>. It goes on as a run of its own, whose questions wait for you in gnosh agents, and gnosh -r opens it again once that is done")
	}
	args, err := l.goOnArgs(c, "background")
	if err != nil {
		return err
	}
	id := l.ts.ID()
	// (A name the person gave; one gnosh made is the first prompt's line.)
	name, byPerson := l.title()
	if !byPerson {
		name = ""
	}
	d := &detachPlan{session: id, name: name, prompt: prompt, log: filepath.Join(l.crashDir(), id+".background.log"), cwd: l.cfg.Paths.Cwd, path: l.ts.Path()}
	p := l.p
	p.restartMu.Lock()
	p.restart, p.detached = args, d
	p.restartMu.Unlock()
	c.Say("The session goes on in the background, on its own, with: %s", oneLine(prompt, 200))
	return nil
}

// slashFork is /fork <prompt>, as Claude Code has it: the conversation so
// far is copied into a new session, which goes on in the background as a
// run of its own on the prompt, while this session goes on here. The copy
// works in the same folder.
func (l *live) slashFork(ctx context.Context, c *SlashContext) error {
	return l.forkRun(ctx, c, false)
}

// slashSubtask runs /subtask <task>, as Claude Code's: a copy of the
// conversation works on the task in the background, as /fork's does, and
// when it ends what it answered comes back to this conversation, for the
// model to tell the person (DECISIONS 141). It is named after the task's
// first words.
func (l *live) slashSubtask(ctx context.Context, c *SlashContext) error {
	return l.forkRun(ctx, c, true)
}

// forkRun is /fork, and with subtask /subtask.
func (l *live) forkRun(ctx context.Context, c *SlashContext, subtask bool) error {
	prompt := strings.TrimSpace(c.Args)
	what := "fork"
	if subtask {
		what = "subtask"
	}
	switch {
	case l.p.o.Detach == nil:
		return errors.New("this gnosh cannot start a session that goes on without a terminal")
	case prompt == "" && subtask:
		return errors.New("say what the copy is to do: /subtask <task>. A copy of this conversation works on it in the background, and what it answers comes back here when it is done")
	case prompt == "":
		return errors.New("say what the copy is to do: /fork <prompt>. It goes on in the background as a run of its own, whose questions wait for you in gnosh agents, while this session goes on here")
	case l.busy(ctx):
		return fmt.Errorf("a turn is running. Wait for it, or stop it, then /%s", what)
	case !slices.ContainsFunc(l.ts.Entries(), func(e transcript.Entry) bool { return e.Type == transcript.TypeUser }):
		return errors.New("there is nothing to copy yet: the conversation has not begun")
	}
	id, path, err := l.copySession(what)
	if err != nil {
		return fmt.Errorf("the conversation could not be copied: %v", err)
	}
	args, err := l.relaunchFor(id, false)
	if err != nil {
		return fmt.Errorf("the copy %s was made, and the command line to start it could not be (%v): gnosh -r %s opens it", id, err, id)
	}
	run := append(args, "--print", "--", prompt)
	log := filepath.Join(l.crashDir(), id+".background.log")
	// (The copy's file is under the folder the session began in, which
	// is not this one for a session of another folder opened by its id.)
	name, byPerson := l.title()
	switch {
	case subtask:
		name = firstWords(prompt, 6)
	case !byPerson:
		name = ""
	default:
		name += " (fork)" // (as copySession names the copy)
	}
	l.p.logHead(log, id, name, l.cfg.Paths.Cwd, path, prompt)
	pid, err := l.p.o.Detach(run, prepareRun(l.p.o.Environ, log), log)
	if err != nil {
		return fmt.Errorf("the copy %s was made, and could not be started (%v): gnosh -r %s opens it", id, err, id)
	}
	writeRunPID(log, pid)
	if t := l.st.tree; t != nil {
		// The run works in this session's work tree, which must outlive
		// this session.
		t.forked.Store(true)
	}
	task := ""
	if subtask {
		task = prompt
	}
	l.background(func(ctx context.Context) { l.watchRun(ctx, id, log, path, task) })
	if subtask {
		c.Say("A copy of this conversation, %s, works on it in the background (process %d): %s", id, pid, oneLine(prompt, 200))
		c.Say("What it answers comes back here when it is done; meanwhile ← ← shows it among the runs, and what it prints is in %s.", l.tilde(log))
		return nil
	}
	c.Say("A copy of this conversation, %s, goes on in the background (process %d) with: %s", id, pid, oneLine(prompt, 200))
	c.Say("It works in this same folder, beside this session. gnosh -r %s opens it once it is done; what it prints is in %s.", id, l.tilde(log))
	return nil
}

// firstWords is a name made of a text's first words, at most n of them
// and about 48 bytes.
func firstWords(text string, n int) string {
	words := strings.Fields(text)
	if len(words) > n {
		words = words[:n]
	}
	return oneLine(strings.Join(words, " "), 48)
}

// watchRun tells the person, while this session is open, when a run /fork
// started is done, as Claude Code's agent view does: the run works while
// its process does, holding the copy's session file (path), and lets go
// when it ends. A run that never took the file is said to have ended
// without starting, with the last line of its log, once it has had
// runStart to take it.
//
// task is what a run /subtask started was set to do, empty for /fork's:
// what such a run printed goes to the model when it ends.
func (l *live) watchRun(ctx context.Context, id, logPath, path, task string) {
	started := l.p.now()
	what := "fork"
	if task != "" {
		what = "subtask"
	}
	took := false
	tick := time.NewTicker(runLook)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if transcript.InUse(path) {
			took = true
			continue
		}
		if runAlive(id, logPath, path) {
			continue
		}
		r := readRunLog(logPath, id, started)
		// A run that said how it ended took its session, even one that
		// began and ended between two looks.
		took = took || r.ended != "" || r.stopped
		if !took && l.p.now().Sub(started) < runStart {
			continue
		}
		said := ""
		if r.last != "" {
			said = " It ended with: " + oneLine(r.last, 160)
		}
		end := prompt.SubtaskEnd{ID: id, Task: task}
		if took {
			level, how := agent.LevelInfo, "is done"
			switch {
			case r.stopped || r.ended == "stopped":
				how, end.State = "was stopped", prompt.AgentStopped
			case r.ended == "failed":
				level, how = agent.LevelWarning, "failed, "+strings.TrimSuffix(r.failure, ".")
				end.State, end.Why = prompt.AgentFailed, r.failure
			case r.ended == "":
				level, how = agent.LevelWarning, "ended without a word: it was killed, or its machine stopped"
				end.State, end.Why = prompt.AgentFailed, "it ended without a word: it was killed, or its machine stopped"
			}
			l.notice(level, what, fmt.Sprintf("The %s %s %s (gnosh -r %s opens it).%s", what, RunShort(id), how, id, said))
		} else {
			l.notice(agent.LevelWarning, what, fmt.Sprintf("The %s %s ended without taking its session (what it printed is in %s).%s", what, RunShort(id), l.tilde(logPath), said))
			end.State, end.Why = prompt.AgentFailed, "it ended without starting"
		}
		if task != "" {
			l.subtaskEnded(end, r.output)
		}
		return
	}
}

// subtaskEnded gives the model what a run /subtask started printed, once
// it has ended, for it to tell the person: a note that starts a turn of
// its own when the session is idle, as a background sub-agent's report
// does.
func (l *live) subtaskEnded(end prompt.SubtaskEnd, printed []string) {
	if l.agent == nil || l.closing.Load() {
		return
	}
	report := strings.TrimSpace(strings.Join(printed, "\n"))
	if report != "" && len(printed) >= runOutputKept {
		report = fmt.Sprintf("[the last %d lines it printed]\n%s", runOutputKept, report)
	}
	end.Report = prompt.Defuse(report)
	_ = l.agent.Tell(agent.Note{
		Text:   l.texts.SubtaskEnded(end),
		Source: "task-notification",
		Notice: fmt.Sprintf("What the subtask %s answered is in front of the model.", RunShort(end.ID)),
		Wake:   true,
	})
}

// runLook is how often watchRun looks at a run's session, and runStart how
// long a run has to take it before it is said not to have started.
const (
	runLook  = 2 * time.Second
	runStart = 30 * time.Second
)

// copySession copies the conversation into a new session, named after
// this one with what the copy is for, and returns its id and its file.
func (l *live) copySession(what string) (id, path string, err error) {
	copied, err := l.store.Branch(l.ts, "")
	if err != nil {
		return "", "", err
	}
	if was, _ := l.title(); was != "" {
		_, err = copied.Append(transcript.TitleEntry(was+" ("+what+")", transcript.SourceAuto))
	}
	id, path = copied.ID(), copied.Path()
	if cerr := copied.Close(); err == nil {
		err = cerr
	}
	return id, path, err
}

// logHead begins a run's log with what it is: when it started, the
// session it goes on with, its prompt, its name when it has one, the
// folder it works in, and the session's file (which the run holds while
// it works). The run's own output follows. A log that cannot be written
// to is left to the run, which says why.
func (p *program) logHead(log, session, name, cwd, path, prompt string) {
	f, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "gnosh: %s the session %s goes on in the background with: %s\n", p.now().UTC().Format(time.RFC3339), session, oneLine(prompt, 400))
	if name = oneLine(name, 120); name != "" {
		fmt.Fprintf(f, "%s%s\n", runNameLine, name)
	}
	if cwd != "" {
		fmt.Fprintf(f, "%s%s\n", runFolderLine, cwd)
	}
	if path != "" {
		fmt.Fprintf(f, "%s%s\n", runFileLine, path)
	}
}

// runFileLine begins the line of a run's log that names its session's
// file, runFolderLine the one that names the folder it works in, and
// runNameLine the one with its name. runStopped ends the line gnosh stop
// adds to the log of a run it stopped.
const (
	runFileLine   = "gnosh: its session file is "
	runFolderLine = "gnosh: its folder is "
	runNameLine   = "gnosh: its name is "
	runStopped    = " the run was stopped; what it did so far is kept."
	// runRenamed begins, after the time, the line that gives a run a new
	// name (the agent view's Ctrl+R); empty after it takes the name away.
	runRenamed = " its name is now: "
	// runDone is, after the time, the line a run ends its log with when
	// it ends well, and runFailed and runSignalled begin the one it ends
	// it with when it ends with an error, or on a signal (runEnded).
	runDone      = " the run is done."
	runFailed    = " the run failed: "
	runSignalled = " the run was stopped by a signal"
)

// renameRun gives the run of the session id whose log is log a new name,
// as a line of the log's own that names the session (a line the run
// printed is not taken for one), which readRunLog reads wherever it
// stands. A run that is over keeps the time its log was last written,
// which is when it ended.
func renameRun(log, id, name string, now time.Time, over bool) error {
	info, err := os.Stat(log)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "gnosh: %s %s%s%s\n", now.UTC().Format(time.RFC3339), id, runRenamed, oneLine(name, 120))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && over {
		err = os.Chtimes(log, info.ModTime(), info.ModTime())
	}
	return err
}

// backgroundRun is a run in the background that gnosh --bg, /background or
// /fork started: the session it goes on with, its prompt and name, its log, the
// folder it works in, the session's file, when it started, whether it
// still runs or was stopped, the last line it printed, and the last
// runOutputKept lines that were not empty.
type backgroundRun struct {
	session, prompt, name, log, cwd, file string
	started                               time.Time
	running, stopped                      bool
	// ended is how the run said it ended, as the last line of its log:
	// "done", "failed" (and failure the rest of that line), or "stopped"
	// (by a signal); empty when it said nothing (it runs, or it was
	// killed). died is true of one whose process is gone and that said
	// nothing.
	ended, failure string
	died           bool
	// endedAt is when the line that says how it ended, or that gnosh stop
	// stopped it, was written.
	endedAt time.Time
	last    string
	output  []string
	// asking is the permission question a working run waits on for the
	// agent view to answer (runasks.go), if any.
	asking *runAsk
}

// runOutputKept is how many of the last lines a run printed readRunLog
// keeps: what the agent view shows of a run that is over.
const runOutputKept = 40

// runsShown is how many runs in the background /tasks lists, the newest
// first, and runsKept how old a run's log may be to be among them, and
// among gnosh agents' without --all.
const (
	runsShown = 10
	runsKept  = 7 * 24 * time.Hour
)

// backgroundRuns are the runs in the background of this folder's sessions
// whose logs were written in the last week, the newest first.
func (l *live) backgroundRuns() []backgroundRun {
	dir := l.crashDir()
	if dir == "" {
		return nil
	}
	out := runsIn([]string{dir}, l.p.now().Add(-runsKept))
	if len(out) > runsShown {
		out = out[:runsShown]
	}
	return out
}

// runsIn are the runs in the background whose logs are in the folders
// dirs and were written to since since, the newest first. A log a session
// was sent to the background more than once holds every run; the last is
// the one told. A session sent off from two folders has a log in each:
// the run told is the one that works, or else the newer.
func runsIn(dirs []string, since time.Time) []backgroundRun {
	var out []backgroundRun
	at := map[string]int{} // a session's place in out
	for _, dir := range dirs {
		paths, _ := filepath.Glob(filepath.Join(dir, "*.background.log"))
		for _, p := range paths {
			info, err := os.Stat(p)
			if err != nil || info.ModTime().Before(since) {
				continue
			}
			id := strings.TrimSuffix(filepath.Base(p), ".background.log")
			r := readRunLog(p, id, info.ModTime())
			r.session = id
			r.file = firstOf(r.file, filepath.Join(dir, id+".jsonl"))
			r.running = runAlive(id, p, r.file)
			if r.running {
				r.asking = readAsk(p)
			}
			// (A run gets its process id beside its log the moment it
			// starts, so one that has none yet is starting, or an old one.)
			r.died = !r.running && !r.stopped && r.ended == "" && runPID(p) != 0
			if r.cwd == "" {
				r.cwd = sessionFolder(r.file)
			}
			if i, ok := at[id]; ok {
				if was := out[i]; was.running || !r.running && was.started.After(r.started) {
					continue
				}
				out[i] = r
				continue
			}
			at[id] = len(out)
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b backgroundRun) int { return b.started.Compare(a.started) })
	return out
}

// sessionFolder is the folder the session in file was started in, as its
// first entry says, for the log of a run that does not name it (logHead
// has named it since 2026-11-18).
func sessionFolder(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	line, _ := bufio.NewReader(io.LimitReader(f, runLogRead)).ReadBytes('\n')
	var first struct {
		Cwd string `json:"cwd"`
	}
	if json.Unmarshal(line, &first) != nil {
		return ""
	}
	return first.Cwd
}

// runLogRead is how much of the end of a run's log is read: its last
// run's first line and last line are in it, but for a run that printed
// more, which then shows no prompt.
const runLogRead = 1 << 20

// readRunLog reads the last run in the log of the session id's runs: the
// prompt its first line gives (logHead), when it started (started, when
// the line gives no time), its folder and its session's file when the
// lines right after it name them, whether gnosh stop stopped it, and the
// last line it printed. Only gnosh's own lines are read as such: a first
// line of this session's, the lines that follow it at once, and gnosh
// stop's line with its time; the run's output, which may quote any of
// them, is not.
func readRunLog(path, id string, started time.Time) backgroundRun {
	r := backgroundRun{log: path, started: started}
	f, err := os.Open(path)
	if err != nil {
		return r
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > runLogRead {
		_, _ = f.Seek(info.Size()-runLogRead, io.SeekStart)
	}
	data, _ := io.ReadAll(io.LimitReader(f, runLogRead))
	head := false // the lines logHead wrote after a first line go on
	for _, line := range strings.Split(string(data), "\n") {
		if head {
			if p, ok := strings.CutPrefix(line, runNameLine); ok {
				r.name = p
				continue
			}
			if p, ok := strings.CutPrefix(line, runFolderLine); ok {
				r.cwd = p
				continue
			}
			if p, ok := strings.CutPrefix(line, runFileLine); ok {
				r.file, head = p, false // (the last line logHead writes)
				continue
			}
			head = false
		}
		if rest, ok := strings.CutPrefix(line, "gnosh: "); ok {
			// gnosh's own lines: the time, then what happened.
			at, after, _ := strings.Cut(rest, " ")
			t, terr := time.Parse(time.RFC3339, at)
			after = " " + after
			failure, failed := strings.CutPrefix(after, runFailed)
			name, renamed := strings.CutPrefix(after, " "+id+runRenamed)
			prompt, begun := strings.CutPrefix(after, " the session "+id+" goes on in the background with: ")
			switch {
			case terr != nil:
			case after == runStopped:
				r.stopped, r.endedAt = true, t
				continue
			case after == runDone:
				r.ended, r.failure, r.endedAt = "done", "", t
				continue
			case failed:
				r.ended, r.failure, r.endedAt = "failed", failure, t
				continue
			case strings.HasPrefix(after, runSignalled):
				r.ended, r.failure, r.endedAt = "stopped", "", t
				continue
			case renamed:
				r.name = strings.TrimSpace(name)
				continue
			case begun:
				// A run of the session that went on again: all of it is
				// this one's from here.
				r, head = backgroundRun{log: path, prompt: prompt, started: t, output: r.output[:0]}, true
				continue
			}
		}
		if strings.TrimSpace(line) != "" {
			// A run's own end line is the last it writes: one with what it
			// printed after it was printed, a quote of another's.
			r.ended, r.failure, r.endedAt = "", "", time.Time{}
			r.last = strings.TrimSpace(line)
			if len(r.output) == runOutputKept {
				r.output = append(r.output[:0], r.output[1:]...)
			}
			r.output = append(r.output, strings.TrimRight(line, " \t\r"))
		}
	}
	return r
}
