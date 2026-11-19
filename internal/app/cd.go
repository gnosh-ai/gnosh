package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/trust"
)

// This file is /cd <folder>, as Claude Code has it: the session moves to
// another folder, and the conversation goes on there (DECISIONS 77).
// Claude Code applies the new folder's project settings, permission
// rules, hooks, MCP servers, skills, agents and instruction files as it
// moves; gnosh gets all of that the one way it already has, by starting
// again in the folder, as /restart does. Before anything moves, the Cd
// rules are checked, the folder's trust is settled (a folder not trusted
// yet is asked about, and a no leaves the session where it is) and its
// configuration is read, so that the new start does not fail on it. Then
// the session's file is put with the new folder's sessions, so that gnosh
// -c there goes on with it, its own folder (the output files of its
// commands, its sub-agents) goes along, and gnosh starts again there.
// mem is a project's, so it is the new folder's from then on.

// MovedEnv is set for the gnosh that /cd starts, to the folder the session
// left: the new gnosh tells the model of the move, and runs the CwdChanged
// hooks. As with RestartedEnv, gnosh takes it out of its environment as it
// starts.
const MovedEnv = "GNOSH_MOVED_FROM"

// slashCd is /cd <folder>.
func (l *live) slashCd(ctx context.Context, c *SlashContext) error {
	cur := l.cfg.Paths.Cwd
	arg := strings.TrimSpace(c.Args)
	if arg == "" {
		c.Say("The session works in %s. /cd <folder> moves it to another, keeping the conversation; from then on that folder's own settings, instructions and skills are in force.", cur)
		return nil
	}
	switch {
	case l.p.o.Relaunch == nil:
		return errors.New("this gnosh cannot start itself again, which moving the session needs. /exit ends the session, and " + l.info().Resume + " --cwd <folder> opens it in another folder")
	case l.st.tree != nil:
		return fmt.Errorf("the session works in a git work tree of its own, %s, which /cd would leave behind. /exit ends the session, and says what becomes of the tree", l.st.tree.path)
	}
	dir := l.cdTarget(arg)
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%s does not exist; /cd takes a folder that is there", dir)
	case err != nil:
		return fmt.Errorf("%s cannot be looked at: %v", dir, err)
	case !info.IsDir():
		return fmt.Errorf("%s is a file; /cd takes a folder", dir)
	}
	if permission.PathKey(dir) == permission.PathKey(cur) {
		c.Say("The session works in %s already.", dir)
		return nil
	}
	names, err := dirNames(dir)
	if err != nil {
		return fmt.Errorf("the session stays in %s: the links on the way to %s cannot be followed (%v)", cur, dir, err)
	}
	if why := l.engine.CdRefusal(names); why != "" {
		return fmt.Errorf("the session stays in %s. %s", cur, why)
	}
	// What a turn or the background is doing would be stopped: that is
	// said before the person is asked anything.
	if _, err := l.goOnArgs(c, "cd"); err != nil {
		return err
	}
	sessionOnly, err := l.cdTrust(ctx, c, dir, cur)
	if err != nil {
		return err
	}
	if err := l.moveSession(dir); err != nil {
		return fmt.Errorf("the session stays in %s: %v", cur, err)
	}
	// (Made once the file has moved: a session opened from its file is
	// opened from where the file is now.)
	args, err := l.goOnArgs(c, "cd", func(r *cli.Relaunch) { r.Cwd, r.TrustFolder = dir, sessionOnly })
	if err != nil {
		return fmt.Errorf("the session is now one of %s's, but gnosh cannot start again there (%v). /exit ends it, and gnosh -c run in %s goes on with it", dir, err, dir)
	}
	p := l.p
	p.restartMu.Lock()
	p.restart, p.moving = args, cur
	p.restartMu.Unlock()
	c.Say("The session moves to %s: gnosh starts again there, with that folder's own settings, instructions and skills.%s", dir, l.loopEndsNote())
	return nil
}

// cdTarget is the folder /cd names: ~ is the home folder, and a path that
// is not absolute is read from the folder the shell is in.
func (l *live) cdTarget(arg string) string {
	dir := arg
	switch {
	case dir == "~":
		dir = l.cfg.Paths.Home
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(l.cfg.Paths.Home, dir[2:])
	case !filepath.IsAbs(dir):
		dir = filepath.Join(l.cwd(), dir)
	}
	return filepath.Clean(dir)
}

// dirNames is every spelling of the folder p that the Cd rules are
// checked against: p as typed; then the path again after each symbolic
// link on the way is followed, one at a time from the root; and last the
// path it resolves to.
func dirNames(p string) ([]string, error) {
	names := []string{p}
	cur := p
	for hops := 0; ; hops++ {
		if hops == 64 {
			return nil, errors.New("there are too many links on the way")
		}
		next, followed, err := followLink(cur)
		if err != nil {
			return nil, err
		}
		if !followed {
			break
		}
		cur = next
		if !slices.Contains(names, cur) {
			names = append(names, cur)
		}
	}
	if real, err := filepath.EvalSymlinks(p); err == nil && real != names[len(names)-1] {
		names = slices.DeleteFunc(names, func(n string) bool { return n == real })
		names = append(names, real)
	}
	return names, nil
}

// followLink follows the first symbolic link in the absolute path p,
// counting from the root: the path with that link in it put in place by
// what the link points to. followed is false when there is no link in p.
func followLink(p string) (next string, followed bool, err error) {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i := range parts {
		at := "/" + filepath.Join(parts[:i+1]...)
		fi, err := os.Lstat(at)
		if err != nil {
			return "", false, err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		to, err := os.Readlink(at)
		if err != nil {
			return "", false, err
		}
		if !filepath.IsAbs(to) {
			to = filepath.Join(filepath.Dir(at), to)
		}
		return filepath.Clean(filepath.Join(append([]string{to}, parts[i+1:]...)...)), true, nil
	}
	return p, false, nil
}

// cdTrust settles the folder dir's trust as a start in it would, before
// the session moves there: a folder recorded as trusted, or with nothing
// waiting for trust, needs no question; any other is asked about, and a
// yes is recorded. sessionOnly says the yes could not be recorded (the
// home folder, or a folder that holds it, is trusted one session at a
// time), so the new start is to be told with --trust-folder. A no, and a
// configuration with mistakes, leave the session where it is (cur).
func (l *live) cdTrust(ctx context.Context, c *SlashContext, dir, cur string) (sessionOnly bool, err error) {
	p := l.p
	inv := l.inv
	inv.Cwd, inv.TrustFolder, inv.Worktree = dir, false, false
	folder, err := trust.Resolve(dir, p.home)
	if err != nil {
		return false, fmt.Errorf("the session stays in %s: %v", cur, err)
	}
	st := &startup{p: p, inv: inv, folder: folder}
	st.gitPrograms = trust.GitPrograms(folder)
	st.load(false)
	if st.cfg == nil {
		return false, cdMistakes(dir, cur, st.problems)
	}
	held := st.heldList()
	record := st.trustStore()
	d := trust.Decide(trust.Asked{Folder: folder, Verdict: record.Check(folder), Interactive: true, Held: len(held)})
	if d.Ask {
		ans, err := c.Agent.Ask(ctx, agent.Question{
			Kind:  "trust",
			Title: "Trust " + dir + "? The session moves there only if it is.",
			Text:  trust.NewQuestion(folder, st.cfg.HeldFiles(), held).Facts(),
			Choices: []agent.Choice{
				{Key: "no", Label: "No: stay in " + cur},
				{Key: "yes", Label: "Yes: use this folder's settings, and move there"},
			},
		})
		if err != nil || ans.Choice != "yes" {
			return false, fmt.Errorf("the session stays in %s: %s was not trusted", cur, dir)
		}
		if err := record.Trust(folder); err != nil {
			// The home folder is trusted for one session at a time; and a
			// record that cannot be written is not a reason to stay.
			sessionOnly = true
			if !errors.Is(err, trust.ErrSessionOnly) {
				c.Say("%s is trusted for this session, but that could not be recorded (%v), so gnosh will ask again next time.", dir, err)
			}
		}
		d.Trusted = true
	}
	if d.Trusted {
		st.load(true)
		if st.cfg == nil {
			return false, cdMistakes(dir, cur, st.problems)
		}
	}
	if len(config.Errors(st.problems)) > 0 {
		return false, cdMistakes(dir, cur, st.problems)
	}
	return sessionOnly, nil
}

// cdMistakes is the refusal of a folder whose configuration a start in it
// would stop on.
func cdMistakes(dir, cur string, problems []config.Problem) error {
	var b strings.Builder
	fmt.Fprintf(&b, "the session stays in %s: a start in %s would stop on its configuration:", cur, dir)
	mistakes := config.Errors(problems)
	if len(mistakes) == 0 {
		b.WriteString(" it could not be read")
	}
	for _, pr := range mistakes {
		b.WriteString("\n  " + pr.String())
	}
	return errors.New(b.String())
}

// moveSession puts the session with the sessions of the folder dir: its
// file (Session.Move), its own folder beside the file, and the log of a
// run in the background. A session opened from a file outside gnosh's
// store is left where it is, and is opened from it again.
func (l *live) moveSession(dir string) error {
	projects := filepath.Dir(l.store.ProjectDir("/"))
	if path := l.ts.Path(); filepath.Dir(filepath.Dir(path)) != projects {
		return nil
	}
	oldFile := l.ts.Path()
	if err := l.ts.Move(dir); err != nil {
		return err
	}
	newFile := l.ts.Path()
	if oldFile == newFile {
		return nil
	}
	// What belongs to the session alone goes with it; should it not, the
	// conversation still goes on, so that is noted and no more.
	id := l.ts.ID()
	from := l.st.dirs(id).Session
	to := filepath.Join(l.store.ProjectDir(dir), id)
	if _, err := os.Lstat(from); err == nil {
		if err := os.Rename(from, to); err != nil {
			l.flog.logf("session", "the session's folder %s could not go along to %s: %v", from, to, err)
		}
	}
	log := strings.TrimSuffix(oldFile, ".jsonl") + ".background.log"
	if _, err := os.Lstat(log); err == nil {
		_ = os.Rename(log, strings.TrimSuffix(newFile, ".jsonl")+".background.log")
	}
	return nil
}

// movingEnv is what the gnosh /cd starts is told, the folder the session
// left, and the one EnterWorktree or ExitWorktree starts, what moved.
func (p *program) movingEnv() []string {
	p.restartMu.Lock()
	defer p.restartMu.Unlock()
	var env []string
	if p.moving != "" {
		env = append(env, MovedEnv+"="+p.moving)
	}
	if p.treeMoving != "" {
		env = append(env, TreeMoveEnv+"="+p.treeMoving)
	}
	return env
}

// movedHere tells the model, in a session /cd moved, that it now works in
// another folder, and runs the CwdChanged hooks: the hooks of the folder
// the session has come to, and the person's own.
func (l *live) movedHere() {
	from := l.p.movedFrom
	if from == "" || l.ts == nil {
		return
	}
	// Once: a session /clear begins later in this gnosh moved nowhere.
	l.p.movedFrom = ""
	to := l.cfg.Paths.Cwd
	_ = l.agent.Tell(agent.Note{Source: "cd",
		Text: prompt.Wrap(fmt.Sprintf("The user moved this session with /cd from %s to %s. The working directory is %s now: commands run there, relative paths start there, and the project's instructions, settings, skills and hooks are that folder's. mem is that folder's project memory: what mem held for this session in %s stays with that folder.", from, to, to, from))})
	// The folder's instruction files, its kinds of sub-agent and skills,
	// and its environment, as a first prompt carries them: the session's
	// first prompt carried the old folder's, and a prompt of a session
	// opened again is never a first one (the box check of 18 November:
	// the model did not know the moved-to folder's GNOSH.md).
	if parts, err := l.firstPromptLines(l.ctx, agent.LineInfo{First: true}); err == nil {
		for _, p := range parts {
			_ = l.agent.Tell(agent.Note{Source: p.Source, Text: p.Text})
		}
	}
	if l.hooks.has(hooks.CwdChanged) {
		l.background(func(ctx context.Context) {
			if out, err := l.hooks.run(ctx, hooks.Input{Event: hooks.CwdChanged, OldCwd: from, NewCwd: to, EnvFile: l.envFile()}); err == nil {
				l.setWatched(out)
			}
		})
	}
}
