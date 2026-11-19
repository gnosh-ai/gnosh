package shelltool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The script every shell is given. It keeps the command out of the script
// text altogether: the command arrives as an argument and is handed to
// eval as it is, so nothing in it is ever quoted a second time, and a
// mistake in it is the shell's to report, as at a prompt.
//
// The trap is how a cd outlives the shell: when the shell exits, by
// reaching the end, by exit, or by an error that ends it, it writes the
// directory it is in to a file. The trap is written so that it can neither
// print anything nor change the shell's exit code, whatever state the
// command left the shell in.
//
// The script is one line, so that the line numbers in the shell's messages
// are the command's own. Its third argument is the environment file
// (Options.EnvFile), which runs first when it is not empty. It leaves three
// variables of its own in the shell, __gnosh_cwd, __gnosh_cmd and
// __gnosh_env, and no arguments.
const (
	exitTrap = `trap '{ command pwd -P >"${__gnosh_cwd:-/dev/null}"; } 2>/dev/null || :' EXIT`
	envFirst = `if [ -n "$__gnosh_env" ] && [ -s "$__gnosh_env" ]; then . "$__gnosh_env"; fi`
	// wrapper takes the file for the final directory, the command and the
	// environment file.
	wrapper = `__gnosh_cwd=$1 __gnosh_cmd=$2 __gnosh_env=$3; set --; ` + exitTrap + `; ` + envFirst + `; eval "$__gnosh_cmd"`
	// fileWrapper takes the file for the final directory, a file that
	// holds the command and the environment file. It is for commands too
	// long to be an argument.
	fileWrapper = `__gnosh_cwd=$1 __gnosh_env=$3; __gnosh_cmd=$(command cat "$2") || exit 126; set --; ` + exitTrap + `; ` + envFirst + `; eval "$__gnosh_cmd"`
)

// endReason says why a command ended.
type endReason int

const (
	endExited      endReason = iota // the shell ended by itself
	endTimeout                      // its time ran out
	endCancelled                    // the call waiting for it was cancelled
	endStopped                      // the stop tool stopped it
	endClosed                       // the manager was closed
	endOutputLimit                  // it printed too much
)

// String returns the reason as results and Task spell it.
func (r endReason) String() string {
	switch r {
	case endTimeout:
		return ReasonTimeout
	case endCancelled:
		return ReasonCancelled
	case endStopped:
		return ReasonStopped
	case endClosed:
		return ReasonClosed
	case endOutputLimit:
		return ReasonOutputLimit
	}
	return ReasonExited
}

// ending is how a command ended.
type ending struct {
	reason   endReason
	at       time.Time
	limit    time.Duration  // the time limit that ran out, when reason is endTimeout
	reaped   bool           // the shell's exit status was read
	code     int            // the exit code: 128+n when signal n killed the shell, -1 when not known
	sig      syscall.Signal // the signal that killed the shell, or 0
	waitErr  error          // why the exit status could not be read
	alive    []procInfo     // processes that were still alive after SIGKILL
	held     bool           // a process the command left behind still holds its output open
	left     []procInfo     // those processes, as far as they could be found
	detached []procInfo     // what the command left running in its session that let go of its output (cmd >log 2>&1 &)
	escaped  bool           // after the command was stopped, a process outside its session still held its output open
	swept    bool           // what the command left behind was stopped at once instead of being left running
	unread   bool           // the last of the output had not been read when the command's end was recorded
	finalDir string         // the directory the shell said it ended in, or empty
}

// failed reports whether the command counts as having failed.
func (e *ending) failed() bool {
	return e.reason != endExited || e.code != 0
}

// job is one command: its shell process, everything it prints, and how it
// ended. For a command on another machine the process is the ssh that
// carries it, and what is said here of the shell is true of that ssh.
type job struct {
	m           *Manager
	callID      string // the tool call waiting for it, if it started in the foreground
	command     string
	description string
	// owner is the sub-agent that started it, by its session id, or ""
	// for the session's own command.
	owner string
	dir   string // the directory it started in, on this machine
	// gone is the session's working directory when it was not there any
	// more and the command started in dir in its place (nearestStanding).
	gone    string
	started time.Time

	host *Host        // the machine the command is for; nil for this one
	ssh  string       // the ssh program that carries it there
	side *sideChannel // what that ssh itself says, apart from the command's output

	spool int64 // how much of the output the output file may hold, when the command has a limit of its own; 0 for the manager's

	cmd     *exec.Cmd
	pid     int      // the shell's process id, which is also its process group and its session
	pipe    *os.File // our end of the pipe the command's output comes through
	out     *output
	cwdFile string // where the shell writes the directory it ends in
	cmdFile string // the command itself, when it was too long to pass as an argument

	sink atomic.Pointer[progressSink] // where live output goes while a call is listening
	mon  *watcher                     // a Monitor's command: what passes its lines on; nil for any other

	// letGo says that what the command left holding its output let go of
	// it later and runs on, kept on the books to be stopped at Close.
	letGo atomic.Bool

	// intake is held while a piece of output is read from the pipe and
	// taken in, so that whoever else holds it knows that none is on its way.
	intake sync.Mutex

	stop   chan endReason // asks that the command be stopped; holds the first reason given
	move   chan struct{}  // asks that a foreground command go to the background
	exited chan struct{}  // closed once the shell has been reaped
	pumped chan struct{}  // closed once no more output will be read
	done   chan struct{}  // closed once end is set

	waitErr error // from waiting for the shell; set before exited closes

	mu        sync.Mutex
	id        string  // its background id; empty while it is a foreground command
	end       *ending // how it ended; nil while it runs
	announced bool    // the model has been shown that it ended, or will not be

	readMu sync.Mutex // one reader of new output at a time
	offset int64      // how much of the output has been returned to the model
}

// newJob makes a command that has not started.
func newJob(m *Manager, a bashArgs, dir string) *job {
	return &job{
		m:           m,
		command:     a.command,
		description: a.description,
		dir:         dir,
		host:        a.host,
		spool:       a.spool,
		stop:        make(chan endReason, 1),
		move:        make(chan struct{}, 1),
		exited:      make(chan struct{}),
		pumped:      make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// launch starts the command's shell and the two goroutines that follow it:
// one reads its output, one waits for it to exit. name is what its files in
// dir are called.
func (j *job) launch(dir, name string) error {
	m := j.m
	path := filepath.Join(dir, name+".out")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("shelltool: creating the output file: %w", err)
	}
	limit := m.opt.MaxSpoolBytes
	if j.spool > 0 {
		// A copy of a file from another machine is kept whole, whatever
		// the limit on a command's output file is.
		limit = j.spool
	}
	j.out = newOutput(path, file, limit, 2*m.opt.MaxOutputChars)
	fail := func(err error) error {
		file.Close()
		os.Remove(path)
		if j.cmdFile != "" {
			os.Remove(j.cmdFile)
		}
		return err
	}

	// One pipe takes both stdout and stderr, so the two stay in the order
	// they were written in.
	r, w, err := os.Pipe()
	if err != nil {
		return fail(fmt.Errorf("shelltool: making the output pipe: %w", err))
	}
	var cmd *exec.Cmd
	var said, saidEnd *os.File // the pipe for what ssh itself says, and its writing end
	if j.host == nil {
		j.cwdFile = filepath.Join(dir, name+".cwd")
		script, arg := wrapper, j.command
		if len(j.command) > longCommand {
			j.cmdFile = filepath.Join(dir, name+".sh")
			if err := os.WriteFile(j.cmdFile, []byte(j.command), 0o600); err != nil {
				r.Close()
				w.Close()
				return fail(fmt.Errorf("shelltool: writing the command to a file: %w", err))
			}
			script, arg = fileWrapper, j.cmdFile
		}
		cmd = exec.Command(m.shell, "-c", script, filepath.Base(m.shell), j.cwdFile, arg, m.opt.EnvFile)
		cmd.Stdout, cmd.Stderr = w, w
	} else {
		// The command's own two streams are joined on the other machine
		// and come down ssh's standard output. ssh's standard error is
		// kept apart: it carries the word that the command started, and
		// whatever ssh itself has to say.
		if said, saidEnd, err = os.Pipe(); err != nil {
			r.Close()
			w.Close()
			return fail(fmt.Errorf("shelltool: making the pipe for what ssh says: %w", err))
		}
		cmd = exec.Command(j.ssh, m.sshArgs(j.host, j.command)...)
		cmd.Stdout, cmd.Stderr = w, saidEnd
		// The file that holds the output is on this machine, whatever
		// machine the output came from.
		j.out.where = " on this machine"
	}
	cmd.Dir = j.dir
	// The mark goes last, where the settings' own env cannot take it out.
	cmd.Env = commandEnv(m.opt.Environ(), j.dir, append(slices.Clip(m.opt.Env), m.markEntry()))
	cmd.Stdin = nil // the null device
	// A session of its own: the shell leads a new process group, which is
	// what gets signalled, and has no terminal, so nothing in it can stop
	// to ask the person a question behind gnosh's back.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	j.started = m.opt.Now()
	err = cmd.Start()
	w.Close()
	if saidEnd != nil {
		saidEnd.Close()
	}
	if err != nil {
		r.Close()
		if said != nil {
			said.Close()
		}
		return fail(j.startError(err))
	}
	j.cmd, j.pid, j.pipe = cmd, cmd.Process.Pid, r
	if pgid, err := syscall.Getpgid(j.pid); (err == nil && pgid != j.pid) || j.pid <= 1 {
		// It cannot happen on a system where Setsid works; if it does, the
		// group could not be signalled safely, so nothing is run.
		cmd.Process.Kill()
		cmd.Wait()
		r.Close()
		if said != nil {
			said.Close()
		}
		return fail(fmt.Errorf("shelltool: the shell (pid %d) did not get a process group of its own", j.pid))
	}
	if said != nil {
		j.side = newSideChannel(said)
		m.wg.Go(j.side.run)
	}
	m.wg.Add(2)
	go j.pump()
	go j.reap()
	return nil
}

// startError words a failure to start the shell, or the ssh that carries a
// command to another machine.
func (j *job) startError(err error) error {
	if errors.Is(err, syscall.E2BIG) {
		return refusal(fmt.Sprintf("The command could not be started: with the environment, it is too much for the system to pass to a program (%v). Put less in the environment, or put the work in a script file and run that.", err))
	}
	if _, statErr := os.Stat(j.dir); statErr != nil {
		return refusal(fmt.Sprintf("The command was not run: the working directory %s cannot be used (%v).", j.dir, statErr))
	}
	program := j.m.shell
	if j.host != nil {
		program = j.ssh
	}
	return fmt.Errorf("shelltool: starting %s: %w", program, err)
}

// pump reads the command's output until there is no more: until every
// process that holds the other end has closed it, or until our end is
// closed.
func (j *job) pump() {
	// These run from the last to the first: the pipe and the file are
	// closed, then pumped says so, then the manager takes the command off
	// its list of those still holding a pipe.
	defer j.m.wg.Done()
	defer j.m.settle(j)
	defer close(j.pumped)
	defer j.out.closeFile()
	defer j.pipe.Close()
	if j.m.pumpDelay > 0 {
		time.Sleep(j.m.pumpDelay)
	}
	j.readOutput(make([]byte, pumpBufferBytes))
}

// take takes in a piece of the command's output: into the output, to
// whoever is watching live, and against the limit on how much a command
// may print. It is called with intake held.
func (j *job) take(p []byte) {
	total := j.out.add(p)
	if s := j.sink.Load(); s != nil {
		s.write(p)
	}
	if limit := j.m.opt.MaxOutputBytes; limit >= 0 && total > limit {
		j.ask(endOutputLimit)
	}
}

// reap waits for the shell to exit.
func (j *job) reap() {
	defer j.m.wg.Done()
	j.waitErr = j.cmd.Wait()
	close(j.exited)
}

// ask requests that the command be stopped, for the reason given. Only the
// first request counts.
func (j *job) ask(why endReason) {
	select {
	case j.stop <- why:
	default:
	}
}

// attended says how a foreground command's wait ended.
type attended int

const (
	attendEnded    attended = iota // the command has ended
	attendTimedOut                 // its time ran out and it was moved to the background
	attendAsked                    // the person asked for it to be moved to the background
)

// attend waits on a foreground command for the tool call that started it,
// with limit as the time the command may run. It returns when the command
// has ended, or when the command has been moved to the background, where
// it runs on.
func (j *job) attend(ctx context.Context, limit time.Duration) attended {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		why, asked := j.await(ctx, timer.C, j.move)
		if asked && j.host != nil {
			// A command on another machine stays where it is: see
			// Manager.Background.
			continue
		}
		if !asked && (why != endTimeout || !j.m.opt.TimeoutToBackground || j.m.opt.NoBackground || j.host != nil || startsWithSleep(j.command)) {
			j.finish(why, limit)
			return attendEnded
		}
		if j.m.adopt(j) {
			j.m.wg.Go(func() { j.background(j.m.opt.BackgroundTimeout) })
			if asked {
				return attendAsked
			}
			return attendTimedOut
		}
		if !asked {
			// It cannot go to the background: the manager is closing, or
			// has as many background commands as it may. Its time is up.
			j.finish(why, limit)
			return attendEnded
		}
		// A request to move it that cannot be met is dropped, and the
		// command stays where it is.
	}
}

// background waits on a background command, with limit as the time it may
// run from now, or 0 for as long as it likes. It returns when the command
// has ended and the news of it has been passed on.
func (j *job) background(limit time.Duration) {
	var timeout <-chan time.Time
	if limit > 0 {
		timer := time.NewTimer(limit)
		defer timer.Stop()
		timeout = timer.C
	}
	why, _ := j.await(context.Background(), timeout, nil)
	j.finish(why, limit)
	if j.mon != nil {
		// A watched command's last lines go before the news of its end:
		// the sink is given the time to hand them over (its listener only
		// keeps them), and the watcher passes them on.
		if s := j.sink.Swap(nil); s != nil {
			s.closeWithin(5 * time.Second)
		}
		j.mon.close()
	}
	j.m.notes.send(j)
}

// await blocks until something happens to the command: it exits, someone
// asks for it to be stopped, ctx is cancelled, its time runs out, or, when
// move is not nil, someone asks for it to go to the background.
func (j *job) await(ctx context.Context, timeout <-chan time.Time, move <-chan struct{}) (why endReason, moved bool) {
	select {
	case <-j.exited:
		return endExited, false
	case why = <-j.stop:
	case <-ctx.Done():
		why = endCancelled
	case <-timeout:
		why = endTimeout
	case <-move:
		moved = true
	}
	// A command that has just ended by itself is not stopped, timed out or
	// moved, even if the other news came in the same instant.
	if closed(j.exited) {
		return endExited, false
	}
	return why, moved
}

// finish brings the command to its end and records it. If why is anything
// but endExited the command is stopped first. Then the last of the output
// is collected, the exit status and the final directory are read, and done
// is closed. limit is the time limit that was in force.
func (j *job) finish(why endReason, limit time.Duration) {
	m := j.m
	e := &ending{reason: why}
	if why == endTimeout {
		e.limit = limit
	}
	if why != endExited {
		j.tell(stopNotice(why, limit, m.opt.MaxOutputBytes))
		e.alive = j.kill()
	}

	// The shell is gone. Its output ends when the last process holding the
	// pipe lets go, which for an ordinary command is now.
	if j.outputHeld() {
		if why == endExited {
			// The classic: the command left something running (cmd &) that
			// still has the pipe. The command itself is over, so the call
			// must not wait on that process.
			e.held = true
			e.left, _ = m.procs(j.pid)
		} else {
			// Everything in the command's session was killed, so whatever
			// holds the pipe now has left the session (or would not die).
			// Stop listening to it.
			e.escaped = len(e.alive) == 0
			j.pipe.Close()
			waitFor(j.pumped, m.opt.PipeGrace)
		}
	}
	e.unread = !closed(j.pumped)
	if j.side != nil && !waitFor(j.side.done, m.opt.PipeGrace) {
		// ssh is gone, and something it started still holds its standard
		// error open. What ssh had to say has been said: stop listening.
		j.side.r.Close()
		waitFor(j.side.done, m.opt.PipeGrace)
	}

	e.code = -1
	if e.reaped = closed(j.exited); e.reaped {
		if st := j.cmd.ProcessState; st != nil {
			e.code = st.ExitCode()
			if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				e.sig = ws.Signal()
				e.code = 128 + int(e.sig)
			}
		} else {
			e.waitErr = j.waitErr
		}
	}
	e.finalDir = j.finalDir()
	if j.cmdFile != "" {
		os.Remove(j.cmdFile)
	}
	e.at = m.opt.Now()

	if why == endExited && !e.held {
		// What the command left running that let go of its output (cmd
		// >log 2>&1 &, a server started so) is still in its session: it is
		// kept on the books and stopped when the session ends, as what
		// holds the output is. One that started a session of its own
		// (setsid) is not in it, and is left alone.
		if left, err := m.procs(j.pid); err == nil && len(left) > 0 {
			// A process that let go of the output as it ended may not have
			// gone at that look: it was left running only if it is still
			// there a moment later.
			if left = m.stillThere(j.pid, left); len(left) > 0 {
				e.detached = left
				if !m.keepDetached(j.pid, left) {
					m.sweep(j.pid, left) // (the manager is closing)
				}
			}
		}
	}
	if e.held && !m.linger(j, e.left) {
		// The manager is closing, or is already listening to as many
		// processes left behind as it will: this one is stopped here and
		// now rather than kept on the books.
		e.swept = true
		m.sweep(j.pid, e.left)
		j.pipe.Close()
		waitFor(j.pumped, m.opt.PipeGrace)
	}

	j.mu.Lock()
	j.end = e
	j.mu.Unlock()
	m.release(j)
	close(j.done)
}

// outputHeld waits for the rest of the command's output once its shell has
// gone, and reports whether, after the grace given for that, a process
// still holds the output open.
//
// How long the output takes to end says little by itself: on a busy machine
// the pump may be slow to read out what is already in the pipe. So the
// kernel is asked whether anything still has the other end open. If
// nothing has, what is in the pipe is all there will ever be, and it is
// simply waited for. If something has, the grace is given; and if the
// output is still held after that, the pump is at least given the time to
// take in what the pipe holds by then, so that what the command printed
// before it ended is in the result however slow the machine.
func (j *job) outputHeld() bool {
	if closed(j.pumped) {
		return false
	}
	if _, _, writers, known := j.pipeState(); writers || !known {
		if waitFor(j.pumped, j.m.opt.PipeGrace) {
			return false
		}
		if _, _, writers, known = j.pipeState(); writers || !known {
			j.catchUp()
			return true
		}
	}
	// It is bounded all the same, like every wait here: a disk that has
	// stopped taking the output file must not hold the call for ever.
	waitFor(j.pumped, drainWait)
	return false
}

// catchUp waits until the pump has taken in what was in the pipe when it
// was called: until the pipe is seen empty, or, since a process that keeps
// writing never lets it be empty, until more has been taken in than a pipe
// can hold. Like every wait here, it does not wait for ever.
func (j *job) catchUp() {
	deadline := time.NewTimer(drainWait)
	defer deadline.Stop()
	pause := time.Millisecond
	var start int64 = -1
	for {
		waiting, taken, _, known := j.pipeState()
		if start < 0 {
			start = taken
		}
		if !known || !waiting || taken-start > pipeHolds {
			return
		}
		wait := time.NewTimer(pause)
		select {
		case <-deadline.C:
			wait.Stop()
			return
		case <-j.pumped:
			wait.Stop()
			return
		case <-wait.C:
			pause = min(2*pause, 20*time.Millisecond)
		}
	}
}

// maxCwdFile is the most of the file in which a shell says where it ended
// that is read: a path is far shorter.
const maxCwdFile = 64 << 10

// finalDir reads, and removes, the file in which the shell said where it
// ended. It returns that directory with its symbolic links resolved, or ""
// when the shell said nothing usable. The file is written by the shell the
// command ran in, so it is checked, not believed: only a directory that is
// there counts, and it is resolved here again, so that nothing written into
// the file can pass a link off as a place inside the session's roots.
func (j *job) finalDir() string {
	data, err := readCwdFile(j.cwdFile)
	os.Remove(j.cwdFile)
	if err != nil {
		return ""
	}
	dir := strings.TrimSuffix(string(data), "\n")
	if !filepath.IsAbs(dir) {
		return ""
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return ""
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ""
	}
	return dir
}

// tell sends a status line to whoever is watching the command live.
func (j *job) tell(text string) {
	if s := j.sink.Load(); s != nil && text != "" {
		s.note(text)
	}
}

// detach ends live output to the session, handing over what still waits.
func (j *job) detach() {
	if s := j.sink.Swap(nil); s != nil {
		s.close()
	}
}

// ending returns how the command ended, or nil while it runs.
func (j *job) ending() *ending {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.end
}

// taskID returns the command's background id, or "" while it is a
// foreground command.
func (j *job) taskID() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.id
}

// setTaskID makes the command a background command with the given id.
func (j *job) setTaskID(id string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.id = id
}

// claim marks the command's end as shown to the model and reports whether
// this is the first time, so that each end is announced once.
func (j *job) claim() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.announced {
		return false
	}
	j.announced = true
	return true
}

// label names the command in messages: its background id if it has one,
// and the start of the command.
func (j *job) label() string {
	what := firstLine(j.command, 60)
	if id := j.taskID(); id != "" {
		return id + " (" + what + ")"
	}
	return "the command (" + what + ")"
}

// startsWithSleep reports whether a command begins with sleep. Such a
// command only waits, so when its time is up it is stopped rather than
// moved to the background.
func startsWithSleep(command string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(command), "sleep")
	return ok && (rest == "" || strings.ContainsAny(rest[:1], " \t\n;&|"))
}

// stopNotice is the status line shown live when a command is being
// stopped.
func stopNotice(why endReason, limit time.Duration, most int64) string {
	switch why {
	case endTimeout:
		return "time is up (" + seconds(limit) + "): stopping the command"
	case endCancelled:
		return "interrupted: stopping the command"
	case endClosed:
		return "the session is ending: stopping the command"
	case endOutputLimit:
		return "it printed more than " + byteSize(most) + ": stopping the command"
	}
	return ""
}

// closed reports whether ch has been closed.
func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// waitFor waits up to d for ch to be closed, and reports whether it was.
func waitFor(ch <-chan struct{}, d time.Duration) bool {
	if closed(ch) {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}
