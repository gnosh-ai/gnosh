package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/trust"
)

// This file is --worktree: a session in a git work tree of the
// repository's own, as Claude Code's --worktree (spec/cc/cli.md 3.8; its
// worktrees page). The work tree is <data folder>/worktrees/<name>, on the
// branch worktree-<name>, as Claude Code names them; one of that name
// already there is opened again. The session works there, and the gate
// keeps its edits out of the repository's own checkout, as for a
// sub-agent with a work tree of its own.
//
// Where gnosh differs from Claude Code, and why (spec/DECISIONS.md 47):
// the work tree starts at the commit checked out, as a sub-agent's does,
// not at the remote's default branch, which would need the network; the
// folder must be trusted in print mode too, since making one runs git;
// and at the end nobody is asked: a work tree left as it was made goes
// when it was not named, and any other is kept, with a line that says
// how to come back to it and how to take it away.

// sessionTree is the work tree a session started with --worktree works in.
type sessionTree struct {
	worktree
	name string
	// named says the person gave the name, and reused that the work tree
	// was there already: either keeps it at the end.
	named, reused bool
	// from is the folder the session was started in, and fromBranch the
	// branch checked out there ("" when none could be read).
	from, fromBranch string
}

// treeNameRe is what a work tree's name may be: a folder's name and a
// branch's, with no way out of the folder of work trees.
var treeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// The words a made-up name is made of, as "bright-river-3f2a".
var (
	treeAdjectives = []string{"bright", "calm", "quick", "quiet", "bold", "keen", "brave", "clear", "fresh", "steady", "swift", "warm"}
	treeNouns      = []string{"river", "cedar", "harbor", "meadow", "falcon", "lantern", "summit", "willow", "canyon", "comet", "orchard", "beacon"}
)

// madeUpTreeName is a name for a work tree nobody named.
func madeUpTreeName() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s-%s-%02x%02x", treeAdjectives[int(b[0])%len(treeAdjectives)], treeNouns[int(b[1])%len(treeNouns)], b[2], b[3])
}

// enterTree makes, or opens again, the work tree --worktree asks for, and moves
// the startup into it: its folder, and its configuration, read again
// there. trusted says the folder's settings are in force. It returns an
// exit code other than ExitOK when no work tree could be had; the reason
// has been printed.
func (st *startup) enterTree(ctx context.Context, trusted bool) int {
	p, inv := st.p, st.inv
	fail := func(format string, args ...any) int {
		p.errf("--worktree: "+format+", so nothing was started.", args...)
		return cli.ExitFailure
	}
	switch {
	case !st.folder.Repo:
		return fail("a work tree is one of a git repository's, and %s is not in one", st.folder.Start)
	case st.folder.Worktree:
		return fail("%s is in a work tree of the repository at %s already: start gnosh --worktree from that checkout", st.folder.Start, st.folder.Key)
	case !trusted:
		return fail("a work tree is made with git, and this folder is not trusted: a repository's own settings can name programs git runs. gnosh trust trusts it")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fail("a work tree is made with git, and there is none on this machine's PATH")
	}
	name, named := inv.WorktreeName, inv.WorktreeName != ""
	if !named {
		name = madeUpTreeName()
	}
	if !treeNameRe.MatchString(name) {
		return fail("%q cannot name a work tree: a name is letters, digits, dots, dashes and underscores, 64 at most, and begins with a letter or a digit (a prompt right after --worktree is taken for the name: for a made-up one, put the prompt first or another flag between them)", name)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env := p.o.Environ
	// The repository's top folder as git has it, which the work tree
	// keeps out of: the folder that settles trust may be a lower one (a
	// .git/config that includes another file is read only that far).
	top := st.folder.Root
	if t, err := runGit(ctx, env, st.folder.Start, "rev-parse", "--show-toplevel"); err == nil && t != "" {
		top = t
	}
	root, err := treesFolder(st.cfg.Paths.DataDir)
	if err != nil {
		return fail("%v", err)
	}
	t := &sessionTree{worktree: worktree{repo: top, path: filepath.Join(root, name), branch: "worktree-" + name}, name: name, named: named, from: st.folder.Start}
	if b, err := runGit(ctx, env, top, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && b != "HEAD" {
		t.fromBranch = b
	}
	_, branchErr := runGit(ctx, env, top, "rev-parse", "--verify", "--quiet", "refs/heads/"+t.branch)
	switch {
	case fileExists(t.path):
		// One of that name is there: it is opened again if it is a work
		// tree of this repository, and nothing else is.
		list, err := runGit(ctx, env, top, "worktree", "list", "--porcelain")
		if err != nil || !listsTree(list, t.path) {
			return fail("%s is there and is no work tree of this repository: name another", t.path)
		}
		head, err := runGit(ctx, env, t.path, "rev-parse", "HEAD")
		if err != nil {
			return fail("the work tree %s is there, and its commit could not be read (%v)", t.path, err)
		}
		t.base, t.reused = head, true
	default:
		base, err := runGit(ctx, env, top, "rev-parse", "--verify", "--quiet", "HEAD")
		if err != nil || base == "" {
			return fail("the repository has no commit yet, so there is nothing to make a work tree of")
		}
		t.base = base
		// A branch of that name left from before is checked out again.
		if err := addTree(ctx, env, &t.worktree, branchErr != nil); err != nil {
			return fail("%v", err)
		}
		if branchErr == nil {
			if head, err := runGit(ctx, env, t.path, "rev-parse", "HEAD"); err == nil {
				t.base = head
			}
			t.reused = true
		}
	}
	// The session starts in the folder of the work tree that stands where
	// the one it was started in stands in the repository.
	start := t.path
	if rel, err := filepath.Rel(resolved(top), resolved(st.folder.Start)); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
		if info, err := os.Stat(filepath.Join(t.path, rel)); err == nil && info.IsDir() {
			start = filepath.Join(t.path, rel)
		}
	}
	folder, err := trust.Resolve(start, p.home)
	if err != nil {
		return fail("%v", err)
	}
	st.folder, st.tree = folder, t
	st.gitPrograms = trust.GitPrograms(folder)
	st.load(true)
	if st.cfg == nil {
		// The work tree's own configuration has a mistake (its commit's,
		// which the checkout may have fixed since): it is said, and the
		// work tree goes or stays as at a session's end.
		st.printProblems()
		st.leaveTree()
		return cli.ExitFailure
	}
	how := "made, on the branch " + t.branch + " at " + shortCommit(t.base)
	if t.reused {
		how = "opened again, on the branch " + t.branch
	}
	fmt.Fprintf(p.stderr, "gnosh works in the git work tree %s, %s.\n", t.path, how)
	return cli.ExitOK
}

// listsTree reports whether git worktree list --porcelain lists the work
// tree at path.
func listsTree(list, path string) bool {
	want := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		want = real
	}
	for _, line := range strings.Split(list, "\n") {
		got, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if real, err := filepath.EvalSymlinks(got); err == nil {
			got = real
		}
		if got == want {
			return true
		}
	}
	return false
}

// treeEnv is the work tree the session's tools are given, for the gate to
// keep its edits in it; nil without --worktree.
func (st *startup) treeEnv() *core.WorkTree {
	if st.tree == nil {
		return nil
	}
	return &core.WorkTree{Path: st.tree.path, Repo: st.tree.repo}
}

// leaveTree ends --worktree's work tree as the session ends. One left as it was
// made, that nobody named and that was not there before, goes with its
// branch; any other is kept, and a line says how to come back to it and
// how to take it away. Under /restart it is kept as it is.
func (st *startup) leaveTree() {
	t := st.tree
	if t == nil || st.p.restartArgs() != nil {
		// (/restart: the session goes on in it, under the name it has.)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env, out := st.p.o.Environ, st.p.stderr
	what, err := treeHolds(ctx, env, &t.worktree)
	why := ""
	switch {
	case err != nil:
		why = err.Error()
	case len(what) > 0:
		why = "it holds " + strings.Join(what, " and ")
	case t.reused:
		why = "it was there before"
	case t.named:
		why = "it was named"
	}
	if why == "" {
		_, err := runGit(ctx, env, t.repo, "worktree", "remove", t.path)
		if err == nil {
			if _, err := runGit(ctx, env, t.repo, "branch", "-d", t.branch); err != nil {
				fmt.Fprintf(out, "gnosh: the work tree %s was left as it was made, and is removed; its branch %s is left (%v).\n", t.path, t.branch, err)
				return
			}
			fmt.Fprintf(out, "gnosh: the work tree %s was left as it was made, and is removed with its branch.\n", t.path)
			return
		}
		why = fmt.Sprintf("it could not be removed: %v", err)
	}
	fmt.Fprintf(out, "gnosh: the work tree %s, on the branch %s, is kept (%s). gnosh --worktree %s works in it again; git -C %s worktree remove %s takes it away.\n",
		t.path, t.branch, why, t.name, t.repo, t.path)
}
