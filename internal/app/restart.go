package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/buildinfo"
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

// RestartedEnv is set for the gnosh that /restart starts, so that it knows
// the conversation of the session it opens is still on the screen (and
// draws no recap of it), and which build it took over from: the variable
// holds that build's BuildID ("1" from a gnosh too old to say). gnosh takes
// it out of its environment as it starts (Options.Environ here, the
// process's own in main), so nothing it runs sees it.
const RestartedEnv = "GNOSH_RESTARTED"

// relaunch starts gnosh again with args, in place of this process. It
// returns only when that could not be done, with the exit code.
func (p *program) relaunch(args []string) int {
	if _, err := os.Getwd(); err != nil {
		// The folder this gnosh was started in is gone, and a start in it
		// would not know where it is (gnosh never leaves it, and a /cd
		// before named the session's folder with --cwd): it starts in that
		// one instead (the cd review of 2026-11-19).
		for _, a := range args {
			if dir, ok := strings.CutPrefix(a, "--cwd="); ok && filepath.IsAbs(dir) {
				_ = os.Chdir(dir)
			}
		}
	}
	env := append(slices.Clone(p.o.Environ), RestartedEnv+"="+firstOf(buildinfo.BuildID, "1"))
	err := p.o.Relaunch(args, append(env, p.movingEnv()...))
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
	l.carryTreeLocked()
	p.restartMu.Unlock()
	if p.newBuild() {
		c.Say("gnosh starts again, the newer one, in this session." + l.loopEndsNote())
	} else {
		c.Say("gnosh starts again, in this session." + l.loopEndsNote())
	}
	return nil
}

// carryTreeLocked has the gnosh that starts again in this session keep
// the work tree the session is in as what it is (treeMove.Carried): one
// EnterWorktree moved it into stays one ExitWorktree may take it back
// out of, and one --worktree made under a made-up name stays one that
// goes at the end when left as made (treeMove.Made). The caller holds
// restartMu.
func (l *live) carryTreeLocked() {
	switch t := l.st.tree; {
	case t != nil && t.entered:
		move, _ := json.Marshal(treeMove{Entered: t.path, Named: t.named, Carried: true})
		l.p.treeMoving = string(move)
	case t != nil && !t.named && !t.reused:
		move, _ := json.Marshal(treeMove{Made: t.path, Carried: true})
		l.p.treeMoving = string(move)
	}
}

// goOnArgs is the command line that opens this session again, with its
// flags as they stand, for a command (restart, background, cd) that ends
// the session for another gnosh to go on with it; adjust, when given,
// changes what it is made from. It refuses while a turn or work in the
// background runs, which ending the session would stop.
func (l *live) goOnArgs(c *SlashContext, name string, adjust ...func(*cli.Relaunch)) ([]string, error) {
	if c.Agent.State().Busy {
		return nil, fmt.Errorf("a turn is running, and would be stopped: /%s once it has ended, or Esc stops it", name)
	}
	if work := l.runningWork(); len(work) > 0 {
		return nil, fmt.Errorf("this is still running in the background, and would be stopped: %s. /%s once it has ended, or /tasks stop <id> stops it", strings.Join(work, ", "), name)
	}
	return l.goOnWith(adjust...)
}

// goOnWith is goOnArgs's command line, asked for where nothing is to be
// refused first: by the Restart tool, whose own turn ends before the
// session does.
func (l *live) goOnWith(adjust ...func(*cli.Relaunch)) ([]string, error) {
	session, fresh := l.ts.ID(), false
	switch _, err := os.Stat(l.ts.Path()); {
	case errors.Is(err, fs.ErrNotExist):
		// Nothing written yet (a session just begun, or just cleared):
		// there is no file to resume, so the new start makes the session
		// under its id.
		fresh = true
	case l.inv.ResumeIsFile && !l.inv.ForkSession:
		// A session opened from its file, which may lie outside this
		// folder's sessions, is opened from its file again.
		session = l.ts.Path()
	}
	return l.relaunchFor(session, fresh, adjust...)
}

// relaunchFor is the command line that opens session with this session's
// flags as they stand: its permission mode, model, thinking level and work
// tree, and the folders /add-dir added that are still there. fresh says
// the session has no file yet.
func (l *live) relaunchFor(session string, fresh bool, adjust ...func(*cli.Relaunch)) ([]string, error) {
	p := l.p
	r := cli.Relaunch{Session: session, Fresh: fresh, Mode: string(l.engine.Mode()), Effort: firstOf(l.agent.Model().Level, cli.EffortDefault)}
	for _, d := range l.addedDirs {
		// (A folder gone since would stop the new start: --add-dir takes
		// folders that are there.)
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			r.AddDirs = append(r.AddDirs, d)
		}
	}
	if t := l.st.tree; t != nil {
		r.Tree = t.name
	}
	for _, f := range adjust {
		f(&r)
	}
	if ref, ok := l.modelRef(); ok && (l.inv.Model != "" || l.modelPicked) {
		// The model the command line named, or the one picked as the
		// session started, which is kept nowhere; one from the settings,
		// or saved by /model, the new start finds there itself.
		r.Model = ref.String()
	}
	args, err := p.table.RelaunchArgs(p.o.Args, r)
	if err != nil {
		return nil, fmt.Errorf("the command line to go on with could not be made (%v). /exit ends the session, and %s opens it again", err, l.info().Resume)
	}
	return args, nil
}

// whatsNew says, in a session /restart opened in a newer build, what
// changed since the build it took over from: the subjects of the commits
// between them, the newest first, as many as the build carries
// (buildinfo.Changes; a build of the tree as it stands carries none).
func (l *live) whatsNew() {
	from := l.p.restartedFrom
	if !l.interactive || from == "" || strings.HasPrefix(buildinfo.BuildID, from) || strings.HasPrefix(from, buildinfo.BuildID) {
		return
	}
	changes, found := buildinfo.Since(from)
	if len(changes) == 0 {
		return
	}
	l.notice(agent.LevelInfo, "restart", whatsNewText(from, changes, found))
}

// whatsNewShown is how many changes whatsNew lists; /release-notes lists
// them all.
const whatsNewShown = 8

// whatsNewText is whatsNew's words: found says the old build is among the
// changes the build carries, so that the count is the whole of them.
func whatsNewText(from string, changes []buildinfo.Change, found bool) string {
	count := fmt.Sprintf("%d", len(changes))
	if !found {
		count = "more than " + count
	}
	var b strings.Builder
	fmt.Fprintf(&b, "gnosh %s took over from %s: %s %s since.", buildinfo.BuildID, from, count, map[bool]string{true: "change", false: "changes"}[len(changes) == 1 && found])
	for i, c := range changes {
		if i == whatsNewShown {
			fmt.Fprintf(&b, "\n  … and %d more: /release-notes lists them.", len(changes)-whatsNewShown)
			break
		}
		fmt.Fprintf(&b, "\n  %s %s", c.ID, oneLine(c.Subject, 100))
	}
	return b.String()
}

// slashReleaseNotes is /release-notes: the changes this build was made
// from, newest first, as Claude Code's lists its own.
func (l *live) slashReleaseNotes(ctx context.Context, c *SlashContext) error {
	changes := buildinfo.Changes()
	if len(changes) == 0 {
		c.Say("gnosh %s carries no list of its changes: it was built from the tree as it stands, not for installing.", buildinfo.BuildID)
		return nil
	}
	c.Say("gnosh %s, its newest changes first:", buildinfo.BuildID)
	for _, ch := range changes {
		c.Say("  %s %s", ch.ID, oneLine(ch.Subject, 120))
	}
	return nil
}
