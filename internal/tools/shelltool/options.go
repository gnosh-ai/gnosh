package shelltool

import (
	"os"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/sandbox"
)

// Options sets up a Manager. The zero value is usable: every field has a
// default, given in its comment. For the time and size limits, zero means
// the default.
type Options struct {
	// Shell is the shell that runs commands: a POSIX shell that takes -c
	// (bash, sh, dash, zsh). Empty means /bin/bash when it exists and
	// /bin/sh when it does not.
	Shell string

	// Environ returns the environment commands start from. Nil means
	// os.Environ, read anew for each command.
	Environ func() []string

	// Env is laid over that environment last, after the manager's own
	// settings (see the package comment for the list). "NAME=value" sets a
	// variable; "NAME" alone removes it.
	Env []string

	// Scrub takes the credentials out of each command's environment as it
	// is in the end, Env's among them (envscrub;
	// CLAUDE_CODE_SUBPROCESS_ENV_SCRUB).
	Scrub bool
	// PersonSandboxed runs the person's own commands (PersonsBash) in the
	// sandbox too, where they otherwise run outside it, as Claude Code
	// runs them with the scrub on, on Linux.
	PersonSandboxed bool
	// ScriptCaps are, by a text a command holds, how many of the model's
	// commands holding it may run in the session (Claude Code's
	// CLAUDE_CODE_SCRIPT_CAPS, with the scrub on): one more is refused.
	// The text is matched as it is written, so "deploy.sh $(x)" counts
	// for deploy.sh; what runs it again by other means (xargs, find -exec)
	// is not seen.
	ScriptCaps map[string]int
	// Isolate, when set, is given the program line of each command on this
	// machine that runs outside the sandbox, and returns the one to run in
	// its place: on Linux with the scrub on, one with a PID namespace of
	// its own, so that it reads no other process's environment through
	// /proc, as Claude Code runs it.
	Isolate func(argv []string) []string

	// EnvFile is a shell file that runs before each command on this
	// machine, when it is there and not empty: where SessionStart hooks
	// write the variables later commands are to have (Claude Code's
	// CLAUDE_ENV_FILE). Empty means none.
	EnvFile string

	// Sandbox, when not nil, runs this machine's commands inside the
	// operating system's sandbox (internal/sandbox), but those it says run
	// outside it.
	Sandbox *Sandbox

	// SpoolDir is the directory in which the manager makes a directory of
	// its own for output files. Close removes what the manager made, never
	// SpoolDir itself. Empty means os.TempDir().
	SpoolDir string

	// Now is the clock used for start times and durations. Nil means
	// time.Now. Timeouts and graces always run on real time.
	Now func() time.Time

	// DefaultTimeout is how long a foreground command may run when the
	// model names no timeout. Default: 2 minutes.
	DefaultTimeout time.Duration

	// MaxTimeout is the longest a foreground command may run. A call that
	// asks for more gets this much. It is never less than DefaultTimeout.
	// Default: 10 minutes.
	MaxTimeout time.Duration

	// TimeoutToBackground says what happens to a foreground command whose
	// time runs out. False, the default: it is stopped, and the result
	// carries the output so far. True: it keeps running as a background
	// command and the result says so; that is what Claude Code does, and it
	// suits a session that can pass OnEnd's news on to the model. A command
	// that starts with sleep is stopped either way.
	TimeoutToBackground bool

	// BackgroundTimeout is how long a background command may run when the
	// model names no timeout. Default: no limit.
	BackgroundTimeout time.Duration

	// MaxBackgroundTimeout is the longest a background command may be
	// given; a call that asks for more gets this much, and so does a call
	// that names no timeout when BackgroundTimeout is not set. Default: no
	// ceiling.
	MaxBackgroundTimeout time.Duration

	// MaxBackground is how many background commands may run at once; ones
	// that have ended do not count. Default: 50.
	MaxBackground int

	// NoBackground turns background commands off (--bare): a call that
	// asks for one is refused, a command whose time is up is stopped
	// whatever TimeoutToBackground says, and the person's control that
	// moves a command to the background does nothing.
	NoBackground bool

	// NoCarryCwd keeps every command in the session's working directory:
	// a cd in one does not carry over to the next, as Claude Code's
	// CLAUDE_BASH_MAINTAIN_PROJECT_WORKING_DIR=1 has it.
	NoCarryCwd bool

	// TermGrace is how long a command being stopped has between SIGTERM and
	// SIGKILL. Default: 2 seconds.
	TermGrace time.Duration

	// KillWait is how long to wait, after SIGKILL, for the processes to be
	// gone before saying that they could not be stopped. Default: 5
	// seconds.
	KillWait time.Duration

	// PipeGrace is how long to wait for a command's output to end once its
	// shell has exited, when a process the command started still holds the
	// output open. It is what leaving such a process behind costs a call.
	// Default: 500 milliseconds.
	PipeGrace time.Duration

	// RunningAfter is how long a command in the foreground runs before the
	// person watching it is told which programs its shell runs (Progress,
	// core.StreamRunning), and RunningEvery how often that is looked at
	// again after. A program is named once it has been seen twice running,
	// so that a quick one does not flash past. Defaults: 2 seconds, and 1
	// second.
	RunningAfter time.Duration
	RunningEvery time.Duration

	// MaxOutputChars is how many characters of a command's output the model
	// is shown. Longer output is shown as its start and its end, this many
	// characters together, around a line that says what was left out.
	// Default: 30,000.
	MaxOutputChars int

	// MaxFailedOutputChars is the same limit for a command that failed. It
	// is never more than MaxOutputChars. Default: 10,000.
	MaxFailedOutputChars int

	// MaxSpoolBytes is how much of one command's output is kept in its
	// output file. Output past it still counts and still shows in the end
	// of what the model sees, but is not kept. Negative means no limit.
	// Default: 64 MiB.
	MaxSpoolBytes int64

	// MaxOutputBytes is how much one command may print in all before it is
	// stopped. Negative means no limit. Default: 5 GiB.
	MaxOutputBytes int64

	// SpoolBudget is how much disk the output files of finished foreground
	// commands may take together; the oldest are removed to stay under it.
	// The output files of background commands are kept until Close.
	// Negative means no limit. Default: 1 GiB.
	SpoolBudget int64

	// TailChars is how many characters of a background command's last
	// output OnEnd is given. Default: 2,000.
	TailChars int

	// NoOutputTool leaves the TaskOutput tool out of Tools. The model then
	// reads a background command's output file with the file-reading tool,
	// which is how Claude Code has worked since 2.1.277.
	NoOutputTool bool

	// OnEnd, when not nil, is told that a background command has ended, so
	// that the agent can tell the model. It is called from a goroutine of
	// the manager's, one call at a time; it must not block, and must not
	// call Close. An end the model has already been shown (by TaskOutput or
	// TaskStop), and an end caused by Close, is not announced.
	OnEnd func(Ended)

	// OnMonitor, when not nil, is offered what the commands of Monitor
	// calls print, a few lines at a time (monitor.go), and the Monitor
	// tool is among Tools. It is called from a goroutine of the manager's,
	// one call at a time for each command, and always before the news of
	// that command's end; it must not block. It reports whether it took
	// the lines: lines it does not take are offered again a moment later,
	// with what came after them. An offer with Watched.Last set must be
	// taken.
	OnMonitor func(Watched) bool

	// Hosts are the other machines a Bash call may name as its host. A
	// call that names one runs its command there through ssh. With none,
	// every command runs here.
	Hosts []Host

	// Local says who and where this machine is, the way results write it:
	// "dev@box". Empty means the current user's name and the machine's
	// name, as the system gives them when the manager is made.
	Local string

	// SSH is the program that runs a command on a host: a path, or a name
	// that is looked up on the PATH of Environ. Empty means "ssh".
	SSH string

	// ConnectTimeout is how long ssh may take to reach a host and log in.
	// ssh takes it in whole seconds, so it is rounded up to one. Default:
	// 20 seconds.
	ConnectTimeout time.Duration

	// ToolNames are the names of the session's tools. A command that
	// fails because its first word is one of them and no such program
	// exists is answered with word that it is a tool, to be called as one.
	ToolNames []string

	// Misread, when not nil, says what was meant by a command on this
	// machine when it can tell: code written for one of the session's tools
	// and typed into a shell, say. It is asked before the command runs,
	// about each program of its first line that is not installed, one at a
	// time, and what it says then is the refusal: the command does not
	// run. It is asked again after a command fails, with the programs the
	// first line runs, as the shell would find them, in order, and what it
	// says then is the note on the failure, in place of the others. ""
	// leaves the command, or the notes, as they are.
	Misread func(command string, programs []string) string

	// LongSleep, when not zero, refuses a foreground command of the
	// model's that begins by sleeping for this long or longer, and says
	// what to do instead. It is for a session that tells the model when a
	// background command ends (OnEnd), which is what makes waiting in the
	// foreground needless. Zero, the default, refuses no command for
	// sleeping.
	LongSleep time.Duration
}

// Host is another machine commands may run on.
type Host struct {
	// Name is what a call gives as its host argument.
	Name string
	// Target is the destination ssh is given: user@address, or a Host
	// alias of the person's own ssh configuration.
	Target string
	// Description is what the person wrote about the machine.
	Description string
}

// The defaults, and the fixed sizes that are not worth an option.
const (
	defaultTimeout        = 2 * time.Minute
	defaultMaxTimeout     = 10 * time.Minute
	defaultMaxBackground  = 50
	defaultTermGrace      = 2 * time.Second
	defaultKillWait       = 5 * time.Second
	defaultPipeGrace      = 500 * time.Millisecond
	defaultRunningAfter   = 2 * time.Second
	defaultRunningEvery   = time.Second
	defaultOutputChars    = 30_000
	defaultFailedChars    = 10_000
	defaultSpoolBytes     = 64 << 20
	defaultOutputBytes    = 5 << 30
	defaultSpoolBudget    = 1 << 30
	defaultTailChars      = 2_000
	defaultConnectTimeout = 20 * time.Second
	defaultSSH            = "ssh"
	maxRemoteCommand      = 120 << 10              // the longest text ssh is handed as one argument: under what a system takes
	sideKeep              = 8 << 10                // how much of what ssh itself says is kept, before and after the command starts
	defaultMaxLingering   = 32                     // processes left by ended commands that are listened to at once
	drainWait             = 30 * time.Second       // how long output already in the pipe is waited for
	pipeHolds             = 1 << 20                // more than a pipe holds at once, on any system in use
	longCommand           = 64 << 10               // a longer command reaches the shell through a file
	maxLineBytes          = 1 << 20                // a longer line is cut for the filter
	pumpBufferBytes       = 32 << 10               // how much output is read at a time
	progressBacklogBytes  = 256 << 10              // live output held for a slow listener
	progressPartialLinger = 100 * time.Millisecond // how long a line waits for its newline
)

// withDefaults returns o with every unset field filled in.
func (o Options) withDefaults() Options {
	if o.Environ == nil {
		o.Environ = os.Environ
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DefaultTimeout <= 0 {
		o.DefaultTimeout = defaultTimeout
	}
	if o.MaxTimeout <= 0 {
		o.MaxTimeout = defaultMaxTimeout
	}
	o.MaxTimeout = max(o.MaxTimeout, o.DefaultTimeout)
	o.BackgroundTimeout = max(o.BackgroundTimeout, 0)
	o.MaxBackgroundTimeout = max(o.MaxBackgroundTimeout, 0)
	if o.MaxBackgroundTimeout > 0 && (o.BackgroundTimeout == 0 || o.BackgroundTimeout > o.MaxBackgroundTimeout) {
		o.BackgroundTimeout = o.MaxBackgroundTimeout
	}
	if o.MaxBackground <= 0 {
		o.MaxBackground = defaultMaxBackground
	}
	if o.TermGrace <= 0 {
		o.TermGrace = defaultTermGrace
	}
	if o.KillWait <= 0 {
		o.KillWait = defaultKillWait
	}
	if o.PipeGrace <= 0 {
		o.PipeGrace = defaultPipeGrace
	}
	if o.RunningAfter <= 0 {
		o.RunningAfter = defaultRunningAfter
	}
	if o.RunningEvery <= 0 {
		o.RunningEvery = defaultRunningEvery
	}
	if o.MaxOutputChars <= 0 {
		o.MaxOutputChars = defaultOutputChars
	}
	if o.MaxFailedOutputChars <= 0 {
		o.MaxFailedOutputChars = defaultFailedChars
	}
	o.MaxFailedOutputChars = min(o.MaxFailedOutputChars, o.MaxOutputChars)
	if o.MaxSpoolBytes == 0 {
		o.MaxSpoolBytes = defaultSpoolBytes
	}
	if o.MaxOutputBytes == 0 {
		o.MaxOutputBytes = defaultOutputBytes
	}
	if o.SpoolBudget == 0 {
		o.SpoolBudget = defaultSpoolBudget
	}
	if o.TailChars <= 0 {
		o.TailChars = defaultTailChars
	}
	if o.SSH == "" {
		o.SSH = defaultSSH
	}
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = defaultConnectTimeout
	}
	o.LongSleep = max(o.LongSleep, 0)
	return o
}

// Sandbox is how commands run inside the operating system's sandbox.
type Sandbox struct {
	// Wrap returns the program and arguments that run argv, in dir, inside
	// the sandbox, for a session whose folders are roots, reaching the
	// files of gnosh's own given (the one its shell writes its last
	// directory to, the scripts it reads), and the variables to set beside
	// the command's own (where its temporary files and caches go). An
	// error is why the command cannot be sandboxed.
	Wrap func(dir string, roots, argv []string, expose []sandbox.Exposed) (wrapped []string, env []string, err error)
	// Outside reports whether a command runs outside the sandbox: one
	// excludedCommands names, or, with asked, one the model asked to
	// (dangerouslyDisableSandbox), when Unsandboxed lets it.
	Outside func(command string, asked bool) bool
	// Unsandboxed offers dangerouslyDisableSandbox to the model.
	Unsandboxed bool
	// Writes says where a sandboxed command may write, for the model; ""
	// when the sandbox leaves the files open (filesystem.disabled).
	Writes string
	// NoTrust says the command cannot reach macOS's certificate trust
	// service, which Go's programs check certificates with.
	NoTrust bool
	// HidesReads says a read the sandbox refuses is told as a refused
	// write is (macOS's "Operation not permitted"), and there are paths
	// it refuses to read.
	HidesReads bool
	// Ignored are the reports of what the sandbox stopped that are not
	// made (ignoreViolations).
	Ignored []Ignored
	// Hosts are the hosts a sandboxed command may reach at once, through a
	// proxy its HTTP_PROXY names, and Network says what of the network it
	// reaches, for the model ("" for: no network at all).
	Hosts   []string
	Network string
	// Before is called as a sandboxed command starts, for the session's
	// folders, and After as it ends, with what Before returned: what the
	// sandbox could not keep from being made (a path that was not there to
	// mount read-only) is taken away there, and After says so, for the
	// model. Either may be nil.
	Before func(roots []string) any
	After  func(state any) string
}

// Ignored is a report of the sandbox's that is not made: for a command
// holding Command ("*" for every command), of a stop whose line of output
// holds What.
type Ignored struct {
	Command, What string
}

// network says what of the network a sandboxed command reaches, for the
// model.
func (sb *Sandbox) network() string {
	switch {
	case sb.Network != "":
		return sb.Network
	case len(sb.Hosts) == 0:
		return "reach no network"
	}
	return "reach on the network only " + strings.Join(sb.Hosts, ", ") + ", through a proxy that HTTP_PROXY and HTTPS_PROXY name (a program that ignores those reaches nothing)"
}
