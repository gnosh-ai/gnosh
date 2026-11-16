package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/shellquote"
)

// This file is /restart (also /update), as Claude Code has it: the
// session ends as /exit ends it, and gnosh starts again in it, from the
// program file there is now, which is a newer gnosh when one has been
// installed since this one started. The new start has the same flags,
// with --resume=<this session> in place of what chose the conversation
// (cli.Table.RelaunchArgs), so the conversation, mem's place in it, the
// permission mode and the work tree go on as they were. A turn that runs,
// and work in the background, would be stopped with the session, so
// /restart waits for them; and a newer gnosh is said once, at the end of
// a turn, so that the person knows /restart has something to bring.

// restartArgs is the command line /restart asked to start again with, or
// nil.
func (p *program) restartArgs() []string {
	p.restartMu.Lock()
	defer p.restartMu.Unlock()
	return p.restart
}

// restartedEnv is set for the gnosh that /restart starts, so that it knows
// the conversation of the session it opens is still on the screen (and
// draws no recap of it). gnosh takes it out of its environment as it
// starts, so nothing it runs sees it.
const restartedEnv = "GNOSH_RESTARTED"

// relaunch starts gnosh again with args, in place of this process. It
// returns only when that could not be done, with the exit code.
func (p *program) relaunch(args []string) int {
	err := p.o.Relaunch(args, append(slices.Clone(p.o.Environ), restartedEnv+"=1"))
	p.errf("gnosh could not start again (%v). The session is kept: gnosh %s opens it again.", err, shellquote.Words(args))
	return cli.ExitFailure
}

// newBuild reports whether the program file gnosh started from has been
// replaced since: by another file put in its place, as an install by
// rename does, or by other contents written over it.
func (p *program) newBuild() bool {
	if p.started == nil {
		return false
	}
	now, err := os.Stat(p.o.Executable)
	if err != nil {
		// (Between the two steps of an install, say: not yet.)
		return false
	}
	return !os.SameFile(p.started, now) || !now.ModTime().Equal(p.started.ModTime()) || now.Size() != p.started.Size()
}

// tellNewBuild says once, as a turn of an interactive session ends, that a
// newer gnosh is installed than the one running.
func (l *live) tellNewBuild() {
	if !l.interactive || l.p.o.Relaunch == nil || l.newBuildSaid.Load() || !l.p.newBuild() {
		return
	}
	if l.newBuildSaid.CompareAndSwap(false, true) {
		l.notice(agent.LevelInfo, "restart", "A newer gnosh has been installed since this one started: /restart runs it, in this session.")
	}
}

// runningWork lists what runs in the background: commands, Monitors and
// sub-agents, each as its id and what it is.
func (l *live) runningWork() []string {
	var running []string
	if l.shell != nil {
		for _, t := range l.shell.Tasks() {
			if t.Running {
				running = append(running, t.ID+" ("+oneLine(firstOf(t.Description, firstLine(t.Command)), 80)+")")
			}
		}
	}
	for _, b := range l.backgroundAgents() {
		running = append(running, b.id+" ("+oneLine(b.desc, 80)+")")
	}
	return running
}

// slashRestart is /restart: it works out the command line to start again
// with, and ends the session; the program starts again once the session
// is closed (Main).
func (l *live) slashRestart(ctx context.Context, c *SlashContext) error {
	if l.p.o.Relaunch == nil {
		return errors.New("this gnosh cannot start itself again. /exit ends the session, and " + l.info().Resume + " opens it again")
	}
	args, err := l.goOnArgs(c, "restart")
	if err != nil {
		return err
	}
	p := l.p
	p.restartMu.Lock()
	p.restart = args
	p.restartMu.Unlock()
	if p.newBuild() {
		c.Say("gnosh starts again, the newer one, in this session.")
	} else {
		c.Say("gnosh starts again, in this session.")
	}
	return nil
}

// goOnArgs is the command line that opens this session again, with its
// flags as they stand, for a command (restart, background) that ends the
// session for another gnosh to go on with it. It refuses while a turn or
// work in the background runs, which ending the session would stop.
func (l *live) goOnArgs(c *SlashContext, name string) ([]string, error) {
	if c.Agent.State().Busy {
		return nil, fmt.Errorf("a turn is running, and would be stopped: /%s once it has ended, or Esc stops it", name)
	}
	if work := l.runningWork(); len(work) > 0 {
		return nil, fmt.Errorf("this is still running in the background, and would be stopped: %s. /%s once it has ended, or /tasks stop <id> stops it", strings.Join(work, ", "), name)
	}
	session := l.ts.ID()
	if l.inv.ResumeIsFile && !l.inv.ForkSession {
		// A session opened from its file, which may lie outside this
		// folder's sessions, is opened from its file again.
		session = l.ts.Path()
	}
	return l.relaunchFor(session)
}

// relaunchFor is the command line that opens session with this session's
// flags as they stand: its permission mode, model, thinking level and work
// tree.
func (l *live) relaunchFor(session string) ([]string, error) {
	p := l.p
	r := cli.Relaunch{Session: session, Mode: string(l.engine.Mode()), Effort: l.agent.Model().Level}
	if ref, ok := l.modelRef(); ok {
		r.Model = ref.String()
	}
	if t := l.st.tree; t != nil {
		r.Tree = t.name
	}
	args, err := p.table.RelaunchArgs(p.o.Args, r)
	if err != nil {
		return nil, fmt.Errorf("the command line to go on with could not be made (%v). /exit ends the session, and %s opens it again", err, l.info().Resume)
	}
	return args, nil
}
