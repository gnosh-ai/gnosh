package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// Options is what the process hands the program: its arguments, its three
// streams, its environment and the few facts about itself that the program
// must not look up behind a test's back. The program's main fills it in
// from the operating system; a test fills it in with temporary directories
// and buffers, and the program then touches nothing else.
type Options struct {
	// Args are the arguments after the program's name.
	Args []string
	// Stdin, Stdout and Stderr are the process's streams. Nothing but the
	// asked output format is written to Stdout.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// StdinIsTerminal and StdoutIsTerminal say whether a person is at the
	// other end. With both, `gnosh` opens a session to type into; without
	// the second it answers once and exits.
	StdinIsTerminal  bool
	StdoutIsTerminal bool

	// Environ is the environment, as os.Environ gives it.
	Environ []string
	// Cwd is the directory the process was started in, as an absolute
	// path.
	Cwd string

	// Features are the parts of gnosh that are joined to the wiring at
	// program start, in the order they are joined in (see Feature).
	Features []Feature

	// Signals delivers SIGINT and SIGTERM. The first of them is handled as
	// the session's front end decides; once the program has begun to shut
	// down, another one calls Exit at once. Nil means no signals arrive.
	Signals <-chan os.Signal
	// Exit, when not nil, ends the process at once with the code it is
	// given. It is called for a second signal, when the first is already
	// being acted on, and for nothing else.
	Exit func(code int)

	// Build says which build this is, for --version, and BuildID names it
	// for the parts that compare builds.
	Build   cli.Build
	BuildID string

	// Executable is the path of the program file gnosh was started from.
	// A file there that is not the one gnosh started from is a newer
	// gnosh, installed since, and the session says so once (/restart).
	Executable string
	// Relaunch, when not nil, puts gnosh started anew in place of this
	// process: the program file at Executable, with these arguments after
	// its name, in the same folder and environment. It is /restart's last
	// step, taken once the session is closed, and it returns only when it
	// could not be done, in the environment env. Nil means gnosh cannot
	// start itself again.
	Relaunch func(args, env []string) error
	// Detach, when not nil, starts the program file at Executable with
	// these arguments as a process of its own, which goes on when this
	// one and its terminal are gone, writing what it prints to the file at
	// log; it returns the process's id. It is /background's last step,
	// taken once the session is closed. Nil means gnosh cannot do that.
	Detach func(args, env []string, log string) (int, error)

	// Now is the clock. Nil means time.Now.
	Now func() time.Time

	// Interactive, when not nil, makes the front end of an interactive
	// session in place of the plain one that reads lines. It is where a
	// full interface is attached.
	Interactive func(FrontendInfo) Frontend

	// OpenTerminal, when not nil, opens the terminal the process is
	// attached to for reading, whatever standard input is: it is how a
	// person goes on typing in a session whose first prompt was piped in.
	// It fails when there is no such terminal. Nil means there is none.
	OpenTerminal func() (io.ReadCloser, error)

	// StdinWait is how long a run that was given a prompt on its command
	// line waits for piped standard input to say anything before it goes
	// on without it. Zero means two seconds.
	StdinWait time.Duration
	// StopGrace is how long a stopped turn waits for a tool, a hook or a
	// request that has not stopped. Zero means the loop's own ten seconds.
	StopGrace time.Duration
	// Retry says how long the loop waits on a provider that fails. The
	// zero value gives the loop's defaults.
	Retry agent.RetryPolicy
	// ModelWait is how long the start of a session waits for a model
	// server to list its models. Zero means ten seconds.
	ModelWait time.Duration
}

// program is one run of gnosh: what Options gave it, and what is worked
// out once and used everywhere.
type program struct {
	o      Options
	env    map[string]string
	home   string
	now    func() time.Time
	start  time.Time
	stdout *lockedWriter
	stderr *lockedWriter
	table  *cli.Table

	// closing is closed when the program has begun to shut down. From
	// then on a signal ends the process at once.
	closing     chan struct{}
	closingOnce sync.Once

	// lastStop, when not nil, tells every command the open session
	// started to stop. It is what a second signal does on its way out.
	lastMu   sync.Mutex
	lastStop func()

	// started is the program file as gnosh started from it (nil when it
	// is not known), and restart the command line /restart asked to start
	// again with, once the session is closed. restarted says this gnosh is
	// one that /restart started (restartedEnv): the session it opens is
	// still on the screen.
	restarted bool
	started   os.FileInfo
	restartMu sync.Mutex
	restart   []string
	// detached is what /background asked to go on with, once the session
	// is closed, instead of a restart: the run's log, its prompt and the
	// session it resumes.
	detached *detachPlan
}

// lockedWriter lets several goroutines write whole lines to one stream.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// Write writes p in one piece.
func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Main runs gnosh and returns the process's exit code. It is the whole
// program but for the GnoVM worker dispatch, which the program's main does
// before it calls Main.
//
// ctx is for the program that embeds gnosh, and for tests. When it ends
// while a session is open, the session's file is closed at once, exactly as
// a kill would leave it, and then everything the session started is
// stopped: a process that dies is what ctx stands for. A person stops a
// turn or the program with a signal (Options.Signals), which is handled in
// the order the specs give.
func Main(ctx context.Context, o Options) (code int) {
	p := newProgram(o)
	defer func() {
		if v := recover(); v != nil {
			code = p.crashed("", v, debug.Stack())
		}
	}()
	code = p.run(ctx)
	if args := p.restartArgs(); args != nil && code == cli.ExitOK {
		// Everything of the session is closed: gnosh starts again in it,
		// here or on its own.
		if d := p.detachedPlan(); d != nil {
			return p.detach(args, d)
		}
		return p.relaunch(args)
	}
	return code
}

// newProgram fills in what Options leaves out.
func newProgram(o Options) *program {
	if o.Stdin == nil {
		o.Stdin = strings.NewReader("")
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	p := &program{
		o:       o,
		env:     map[string]string{},
		now:     o.Now,
		stdout:  &lockedWriter{w: o.Stdout},
		stderr:  &lockedWriter{w: o.Stderr},
		closing: make(chan struct{}),
	}
	if p.now == nil {
		p.now = time.Now
	}
	p.start = p.now()
	for _, kv := range o.Environ {
		if name, value, ok := strings.Cut(kv, "="); ok && name != "" {
			p.env[name] = value
		}
	}
	p.home = p.env["HOME"]
	if p.env[restartedEnv] != "" {
		// /restart's word to the gnosh it starts, and no one else's: it
		// is gone before anything reads the environment.
		p.restarted = p.env[restartedEnv] == "1"
		delete(p.env, restartedEnv)
		p.o.Environ = slices.DeleteFunc(slices.Clone(p.o.Environ), func(kv string) bool { return strings.HasPrefix(kv, restartedEnv+"=") })
	}
	if o.Executable != "" {
		p.started, _ = os.Stat(o.Executable)
	}
	return p
}

// getenv looks a variable up in the environment the program was given.
func (p *program) getenv(name string) string { return p.env[name] }

// errf prints one line about something that stopped gnosh, on stderr.
func (p *program) errf(format string, args ...any) {
	fmt.Fprintln(p.stderr, "gnosh: "+strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
}

// warnf prints one line worth knowing that stops nothing, on stderr.
func (p *program) warnf(format string, args ...any) {
	fmt.Fprintln(p.stderr, "gnosh: "+strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
}

// built lists what this wiring makes work beyond the table's own P0
// entries, by the names cli.Table.SetStatus takes. What is then left in
// the table is a feature's to mark (Feature.Built): where the feature is
// not joined, typing it stops startup with a message that says it is not
// built. The table holds nothing else: a command or a flag whose part is
// not built has no row (spec/DECISIONS.md, 26).
var built = []string{
	"doctor", "doctor --hosts", "trust", "trust --list", "trust --forget",
	"dirs", "logs", "logs --tail", "logs --follow", "models", "ssh", "--worktree",
	"session list", "session show", "session last", "session rename",
	"session --json",
	"--fork-session", "--name", "--no-session-persistence", "--include-partial-messages",
	"--reasoning-effort", "--small-model",
	"--permission-mode",
	"--permission-mode=default", "--permission-mode=plan", "--permission-mode=acceptEdits",
	"--permission-mode=dontAsk", "--permission-mode=bypassPermissions",
	"--allow-dangerously-skip-permissions",
	"--allowedTools", "--disallowedTools", "--tools", "--add-dir", "--trust-folder",
	"--system-prompt", "--system-prompt-file", "--append-system-prompt", "--append-system-prompt-file",
	"--setting-sources", "--safe-mode", "--bare", "--disable-slash-commands", "--agents", "--agent",
	"--debug", "--debug-file", "--verbose",
	"--output-format=json", "--output-format=stream-json", "--input-format=stream-json", "--replay-user-messages", "--json-schema",
	"--permission-prompts", "--permission-prompts=host", "--permission-prompts=none",
	"mcp add", "mcp add-json", "mcp get", "mcp list", "mcp remove",
	"mcp --scope", "mcp --scope=local", "mcp --scope=user", "mcp --scope=project",
	"mcp --transport", "mcp --transport=stdio", "mcp --transport=http",
	"mcp --env", "mcp --header",
	"--mcp-config", "--strict-mcp-config",
}

// buildTable makes the command-line table of this build: the table as it
// ships, with what the wiring and the features have built marked so.
func (p *program) buildTable() (*cli.Table, error) {
	t := cli.Default()
	for _, name := range built {
		if err := t.SetStatus(name, cli.Built); err != nil {
			return nil, fmt.Errorf("the command-line table could not be set up: %w", err)
		}
	}
	for _, f := range p.o.Features {
		for _, name := range f.Built {
			if err := t.SetStatus(name, cli.Built); err != nil {
				return nil, fmt.Errorf("the feature %q names something the command line does not have: %w", f.Name, err)
			}
		}
	}
	return t, nil
}

// run is Main without the net under it.
func (p *program) run(ctx context.Context) int {
	for _, f := range p.o.Features {
		if f.Boot == nil {
			continue
		}
		if code, done := f.Boot(p.o.Args); done {
			return code
		}
	}
	defer p.shutdownFeatures()

	table, err := p.buildTable()
	if err != nil {
		p.errf("%v", err)
		return cli.ExitFailure
	}
	p.table = table
	inv, err := table.Parse(p.o.Args, cli.Env{StdinIsTerminal: p.o.StdinIsTerminal, StdoutIsTerminal: p.o.StdoutIsTerminal})
	if err != nil {
		p.errf("%v", err)
		return cli.ExitFailure
	}
	if !filepath.IsAbs(p.o.Cwd) {
		p.errf("the directory gnosh was started in is not known (%q is not an absolute path), so nothing was started.", p.o.Cwd)
		return cli.ExitFailure
	}
	if !filepath.IsAbs(p.home) && inv.Command != cli.CmdHelp && inv.Command != cli.CmdVersion {
		p.errf("the home directory is not known: the HOME variable is not set to an absolute path. gnosh keeps its config and its sessions under it, so nothing was started.")
		return cli.ExitFailure
	}

	switch inv.Command {
	case cli.CmdHelp:
		text, err := table.Help(inv.HelpFor...)
		if err != nil {
			p.errf("%v", err)
			return cli.ExitFailure
		}
		fmt.Fprint(p.stdout, text)
		if !strings.HasSuffix(text, "\n") {
			fmt.Fprintln(p.stdout)
		}
		return cli.ExitOK
	case cli.CmdVersion:
		fmt.Fprintln(p.stdout, p.versionLine())
		return cli.ExitOK
	case cli.CmdDoctor:
		return p.doctorCommand(ctx, inv)
	case cli.CmdTrust:
		return p.trustCommand(ctx, inv)
	case cli.CmdInteractive, cli.CmdPrint:
		return p.sessionCommand(ctx, inv)
	case cli.CmdDirs:
		return p.dirsCommand(ctx, inv)
	case cli.CmdLogs:
		return p.logsCommand(ctx, inv)
	case cli.CmdModels:
		return p.modelsCommand(ctx, inv)
	case cli.CmdSessionList, cli.CmdSessionShow, cli.CmdSessionLast, cli.CmdSessionRename:
		return p.sessionsCommand(ctx, inv)
	case cli.CmdMCPAdd, cli.CmdMCPAddJSON, cli.CmdMCPGet, cli.CmdMCPList, cli.CmdMCPRemove:
		return p.mcpCommand(ctx, inv)
	case cli.CmdSSH:
		// main runs gnosh ssh before anything is read; it comes here only
		// after other words.
		p.errf("gnosh ssh takes none of gnosh's own flags: it comes right after gnosh, and every word after it is ssh's (gnosh ssh [ssh arguments...]).")
		return cli.ExitFailure
	}
	for _, f := range p.o.Features {
		for _, c := range f.Commands {
			if c.Command == inv.Command && c.Run != nil {
				return p.featureCommand(ctx, c, inv)
			}
		}
	}
	p.errf("gnosh %s is in the command-line table as built, and nothing in this program runs it. This is a fault in gnosh, not in what you typed.", inv.Command)
	return cli.ExitFailure
}

// shutdownFeatures tells every feature that the program is ending, in
// reverse order. By now the last session's file is flushed and released.
func (p *program) shutdownFeatures() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, f := range slices.Backward(p.o.Features) {
		if f.Shutdown == nil {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			f.Shutdown(ctx)
		}()
	}
}

// beginClosing marks the program as shutting down. From here on a signal is
// not handled a second time: it ends the process at once. On the way out
// the commands the session started are told to stop, and given half a
// second to be told: the person will not wait for more, and a command
// that outlives the session has nobody left to stop it.
func (p *program) beginClosing(code int) {
	p.closingOnce.Do(func() {
		close(p.closing)
		if p.o.Signals == nil || p.o.Exit == nil {
			return
		}
		go func() {
			<-p.o.Signals
			p.lastMu.Lock()
			stop := p.lastStop
			p.lastMu.Unlock()
			if stop != nil {
				done := make(chan struct{})
				go func() {
					defer close(done)
					defer func() { _ = recover() }()
					stop()
				}()
				select {
				case <-done:
				case <-time.After(500 * time.Millisecond):
				}
			}
			p.o.Exit(code)
		}()
	})
}

// setLastStop says what stops the commands of the session that is open.
func (p *program) setLastStop(stop func()) {
	p.lastMu.Lock()
	p.lastStop = stop
	p.lastMu.Unlock()
}

// versionLine is what --version prints, and what the log and /status name
// the build by. A build made from a source archive has no commit recorded
// in it; when it was given a name at link time (buildinfo.BuildID), the
// name stands where the commit would.
func (p *program) versionLine() string {
	b := p.o.Build
	if b.Commit == "" && p.o.BuildID != "" && p.o.BuildID != "devel" {
		b.Commit = p.o.BuildID
	}
	return cli.VersionLine(b)
}

// signalExit is the exit code of a program that a signal stopped: 130 for
// SIGINT, 143 for SIGTERM, and 128 plus the signal's number for another.
func signalExit(sig os.Signal) int {
	switch sig {
	case os.Interrupt:
		return cli.ExitInterrupted
	case syscall.SIGTERM:
		return cli.ExitTerminated
	}
	if s, ok := sig.(syscall.Signal); ok && s > 0 {
		return 128 + int(s)
	}
	return cli.ExitTerminated
}

// featureCommand runs a subcommand that a feature brought. No model is
// made for it. The folder's config is loaded as for doctor: with the trust
// the record gives it and without asking anybody.
func (p *program) featureCommand(ctx context.Context, c Command, inv cli.Invocation) int {
	st, code := p.prepare(ctx, inv, false)
	cc := &CommandContext{
		Invocation: inv,
		Stdin:      p.o.Stdin,
		Stdout:     p.stdout,
		Stderr:     p.stderr,
		Getenv:     p.getenv,
	}
	if st == nil {
		// The folder itself could not be used; the reason has been
		// printed.
		return code
	}
	// With mistakes in the configuration the command still runs: Config is
	// then nil and Problems says why, and what the command does about it
	// is its own business. The directories are filled in either way, from
	// the environment and the folder alone when there is no config to
	// read them from: a command that needs no model (gnosh mem) works
	// with a config that has a mistake in it.
	cc.Config, cc.Problems = st.cfg, st.problems
	if st.cfg != nil {
		cc.Dirs = st.dirs("")
		cc.Sessions = st.sessions()
	} else {
		sessions := p.stateDir()
		cc.Dirs = Dirs{
			Home:        p.home,
			Cwd:         st.folder.Start,
			ProjectRoot: st.folder.Root,
			Config:      p.configDir(),
			State:       p.stateDir(),
			Cache:       p.cacheDir(),
			Sessions:    sessions,
		}
		cc.Sessions = &transcript.Store{Dir: sessions, Clock: p.now}
	}
	return c.Run(ctx, cc)
}
