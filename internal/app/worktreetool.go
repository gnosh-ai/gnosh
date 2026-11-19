package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

// This file is EnterWorktree and ExitWorktree, Claude Code's tools for
// moving a running session into a git work tree of its own and back out
// (its tools reference and worktrees page; spec/DECISIONS.md 80). Asked
// to work in a worktree, the model makes one, or opens one made before by
// its name, as --worktree does (makeTree), and the session moves there:
// gnosh starts again in it with --worktree <name> and the conversation,
// as /cd moves a session (DECISIONS 77), so that everything that hangs on
// the folder (settings, instructions, the gate that keeps edits in the
// tree) is the tree's. The call ends the turn; the gnosh that starts in
// the tree is told what moved (TreeMoveEnv) and goes on with the task by
// itself. ExitWorktree goes back to the folder the session started in,
// keeping the tree or removing it with its branch; a tree with work in it
// is removed only when the call says to throw that work away.
//
// Where gnosh differs from Claude Code: the tools are an interactive
// session's (a run has no person to go on for, and its prompt would be
// gone); a tree is named, not given as a path (a work tree made with git
// worktree add is worked in by starting gnosh there); and the turn ends
// with the move, the next one going on by itself.

const (
	enterTreeName = "EnterWorktree"
	exitTreeName  = "ExitWorktree"
	// TreeMoveEnv is set for the gnosh EnterWorktree or ExitWorktree
	// starts, to what moved (treeMove, as JSON). As with RestartedEnv,
	// gnosh takes it out of its environment as it starts.
	TreeMoveEnv = "GNOSH_TREE_MOVE"
)

// treeMove is what a move into or out of a work tree did.
type treeMove struct {
	// Entered is the tree EnterWorktree made or opened again, and Named
	// says it was named: one left as it was made, not named, goes when
	// the session ends.
	Entered string `json:"entered,omitempty"`
	Named   bool   `json:"named,omitempty"`
	// Left is the tree ExitWorktree left, on the branch Branch, with Name
	// its name; Removed says it went with its branch.
	Left    string `json:"left,omitempty"`
	Name    string `json:"name,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Removed bool   `json:"removed,omitempty"`
	// BranchKept says the removed tree's branch stayed: git would not
	// delete it.
	BranchKept bool `json:"branchKept,omitempty"`
	// Carried says nothing moved: a session in a tree EnterWorktree moved
	// it into starts again there (/restart), and the tree stays one
	// ExitWorktree may take it back from. Nothing is said of it.
	Carried bool `json:"carried,omitempty"`
}

// readTreeMove reads TreeMoveEnv's value; what cannot be read is no move.
func readTreeMove(s string) treeMove {
	var m treeMove
	if json.Unmarshal([]byte(s), &m) != nil || m.Entered != "" && !filepath.IsAbs(m.Entered) || m.Left != "" && !filepath.IsAbs(m.Left) {
		return treeMove{}
	}
	return m
}

// enterTreeTool is EnterWorktree.
type enterTreeTool struct{ l *live }

func (t *enterTreeTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: enterTreeName,
		Description: "Moves this session into a git work tree of its own: a separate checkout of the commit checked out, on a branch of its own, so that its edits leave the user's checkout alone. " +
			"Use it only when the user, or the project's instructions, ask for a worktree by that word; for a branch, use git. " +
			"Changes not committed in the user's checkout are not in the tree. The call ends this turn: gnosh starts again in the tree and goes on with the task there. ExitWorktree takes the session back.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"The work tree's name: letters, digits, dots, dashes and underscores, 64 at most. One of that name made before is opened again; without a name, one is made up."}},"additionalProperties":false}`),
	}
}

func (t *enterTreeTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	return core.Action{Kind: core.ActionOther, Summary: "Move the session into a git work tree"}, nil
}

func (t *enterTreeTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	l := t.l
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return core.ErrorResult("The input could not be read: " + err.Error()), nil
	}
	switch {
	case l.st.tree != nil:
		return core.ErrorResult(fmt.Sprintf("The session works in the git work tree %s already: ExitWorktree takes it back first.", l.st.tree.path)), nil
	case !l.st.folder.Repo:
		return core.ErrorResult(fmt.Sprintf("A work tree is one of a git repository's, and %s is in none.", l.cwd())), nil
	case l.st.folder.Worktree:
		return core.ErrorResult(fmt.Sprintf("%s is in a work tree of the repository at %s already; gnosh makes work trees from the repository's own checkout.", l.st.folder.Start, l.st.folder.Key)), nil
	case !l.st.trusted:
		return core.ErrorResult("A work tree is made with git, and this folder is not trusted: a repository's own settings can name programs git runs."), nil
	}
	if why := l.cannotMove(); why != "" {
		return core.ErrorResult(why), nil
	}
	name, named := strings.TrimSpace(in.Name), strings.TrimSpace(in.Name) != ""
	if !named {
		name = madeUpTreeName()
	}
	if !treeNameRe.MatchString(name) {
		return core.ErrorResult(fmt.Sprintf("%q cannot name a work tree: a name is letters, digits, dots, dashes and underscores, 64 at most, and begins with a letter or a digit.", name)), nil
	}
	tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tree, start, err := makeTree(tctx, l.p.o.Environ, l.st.folder, l.cfg.Paths.DataDir, name, named)
	if err != nil {
		return core.ErrorResult("No work tree could be had: " + err.Error() + "."), nil
	}
	undo := func() {
		// A tree made by this call, and nothing done in it yet, goes again.
		if !tree.reused {
			uctx, done := context.WithTimeout(context.Background(), time.Minute)
			defer done()
			if _, err := runGit(uctx, l.p.o.Environ, tree.repo, "worktree", "remove", "--force", tree.path); err == nil {
				_, _ = runGit(uctx, l.p.o.Environ, tree.repo, "branch", "-D", tree.branch)
			}
		}
	}
	move, _ := json.Marshal(treeMove{Entered: tree.path, Named: named})
	if err := l.moveFor(start, string(move), func(r *cli.Relaunch) { r.Tree = name }); err != nil {
		undo()
		return core.ErrorResult("The session could not move into the work tree, so it stays where it is: " + err.Error() + "."), nil
	}
	how := "made, on the branch " + tree.branch + " at " + shortCommit(tree.base)
	if tree.reused {
		how = "opened again, on the branch " + tree.branch
	}
	if len(tree.included) > 0 {
		how += ", with copies of " + fewOf(tree.included, 5) + " (" + includeFile + ")"
	}
	text := fmt.Sprintf("The git work tree %s was %s. The session moves into it now: this turn ends, gnosh starts again there, and the next turn goes on with the task in the tree.", tree.path, how)
	if tree.includeErr != nil {
		text += " " + tree.includeErr.Error() + "."
	}
	l.restartWhy.Store(ptrTo("The session moves into the git work tree " + tree.path + ": gnosh starts again there." + l.loopEndsNote()))
	res := core.TextResult(text)
	res.Meta = map[string]any{agent.MetaHalt: "the session moves into the work tree " + tree.path}
	return res, nil
}

// exitTreeTool is ExitWorktree.
type exitTreeTool struct{ l *live }

func (t *exitTreeTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: exitTreeName,
		Description: "Takes this session out of the git work tree EnterWorktree moved it into, back to the folder it started in. Use it only when the user asks to leave the worktree. " +
			"action keep leaves the tree and its branch on disk, to come back to; remove deletes both. A tree with changes not committed, or commits not on the branch it came from, is removed only with discard_changes true: ask the user first. The call ends this turn: gnosh starts again in the folder, and the next turn goes on.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["keep","remove"]},"discard_changes":{"type":"boolean","description":"With remove: remove the tree even though it holds work, which is lost."}},"required":["action"],"additionalProperties":false}`),
	}
}

func (t *exitTreeTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	var in struct {
		Action  string `json:"action"`
		Discard bool   `json:"discard_changes"`
	}
	_ = json.Unmarshal(call.Input, &in)
	if in.Action == "remove" && in.Discard {
		// What is in the tree and not committed, or committed and not on
		// the branch it came from, is lost: the person is asked first,
		// whatever the mode, as Claude Code asks.
		return core.Action{Kind: core.ActionOther, Confirm: true,
			Summary: "Remove the session's git work tree and its branch, with the work in them that is kept nowhere else: that work is lost"}, nil
	}
	return core.Action{Kind: core.ActionOther, Summary: "Take the session out of its git work tree"}, nil
}

func (t *exitTreeTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	l := t.l
	var in struct {
		Action  string `json:"action"`
		Discard bool   `json:"discard_changes"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return core.ErrorResult("The input could not be read: " + err.Error()), nil
	}
	tree := l.st.tree
	switch {
	case tree == nil || !tree.entered:
		return core.TextResult("The session is in no work tree that EnterWorktree moved it into, so nothing was done."), nil
	case in.Action != "keep" && in.Action != "remove":
		return core.ErrorResult(`action is "keep" or "remove".`), nil
	}
	if why := l.cannotMove(); why != "" {
		return core.ErrorResult(why), nil
	}
	tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	removed, keptBranch := false, ""
	if in.Action == "remove" {
		what, err := treeHolds(tctx, l.p.o.Environ, &tree.worktree)
		switch {
		case err != nil:
			return core.ErrorResult("Whether the work tree holds work could not be told (" + err.Error() + "), so it was not removed. Call again with action keep, or try again."), nil
		case len(what) > 0 && !in.Discard:
			return core.ErrorResult(fmt.Sprintf("The work tree %s holds %s, which removing it would lose, so it was not removed. Ask the user: then call again with discard_changes true to remove it anyway, or with action keep.", tree.path, strings.Join(what, " and "))), nil
		}
		if _, err := runGit(tctx, l.p.o.Environ, tree.repo, "worktree", "remove", "--force", tree.path); err != nil {
			return core.ErrorResult("The work tree could not be removed (" + err.Error() + "). Call again with action keep to leave it."), nil
		}
		// The branch goes only as far as git agrees that nothing on it is
		// lost (-d), unless the user agreed to lose it.
		del := "-d"
		if in.Discard {
			del = "-D"
		}
		if _, err := runGit(tctx, l.p.o.Environ, tree.repo, "branch", del, tree.branch); err != nil {
			keptBranch = " Its branch " + tree.branch + " is kept: git would not delete it (" + oneLine(err.Error(), 200) + ")."
		}
		removed = true
	}
	move, _ := json.Marshal(treeMove{Left: tree.path, Name: tree.name, Branch: tree.branch, Removed: removed, BranchKept: keptBranch != ""})
	if err := l.moveFor(tree.from, string(move), func(r *cli.Relaunch) { r.Tree, r.LeaveTree = "", true }); err != nil {
		if removed {
			return core.ErrorResult("The work tree was removed, and the session could not move back to " + tree.from + " (" + err.Error() + "). /exit ends the session; gnosh -c in " + tree.from + " goes on with it."), nil
		}
		return core.ErrorResult("The session could not move back, so it stays in the work tree: " + err.Error() + "."), nil
	}
	text := fmt.Sprintf("The session leaves the git work tree %s, which is kept with its branch %s. This turn ends; gnosh starts again in %s, and the next turn goes on there.", tree.path, tree.branch, tree.from)
	if removed {
		text = fmt.Sprintf("The git work tree %s was removed with its branch %s. This turn ends; gnosh starts again in %s, and the next turn goes on there.", tree.path, tree.branch, tree.from)
		if keptBranch != "" {
			text = fmt.Sprintf("The git work tree %s was removed.%s This turn ends; gnosh starts again in %s, and the next turn goes on there.", tree.path, keptBranch, tree.from)
		}
	}
	l.restartWhy.Store(ptrTo("The session goes back to " + tree.from + ": gnosh starts again there." + l.loopEndsNote()))
	res := core.TextResult(text)
	res.Meta = map[string]any{agent.MetaHalt: "the session leaves the work tree " + tree.path}
	return res, nil
}

// ptrTo returns a pointer to a copy of v.
func ptrTo[T any](v T) *T { return &v }

// cannotMove says why the session cannot start again elsewhere now, from a
// tool call: "" when it can.
func (l *live) cannotMove() string {
	if l.p.o.Relaunch == nil {
		return "This gnosh cannot start itself again, which moving the session needs."
	}
	if work := l.runningWork(); len(work) > 0 {
		return "This runs in the background and would be stopped by the move: " + strings.Join(work, ", ") + ". Wait for it to end, or stop it, first."
	}
	if n := l.agent.State().Queued; n > 0 {
		return "The user has sent more that waits to be read, which the move would lose: read it first, and move after."
	}
	return ""
}

// moveFor moves the session to dir, the folder gnosh is to start again in,
// for a tool call: its file goes with the sessions of dir, and the command
// line that opens it there, made by adjust from this session's own, is
// what the program starts once the session is closed. move is what the
// new start is told (TreeMoveEnv). The front end is told to close the
// session when the turn has ended (RestartEvent).
func (l *live) moveFor(dir, move string, adjust func(*cli.Relaunch)) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		if err == nil {
			err = &fs.PathError{Op: "stat", Path: dir, Err: errors.New("not a folder")}
		}
		return err
	}
	if err := l.moveSession(dir); err != nil {
		return err
	}
	session := l.ts.ID()
	if l.inv.ResumeIsFile && !l.inv.ForkSession {
		session = l.ts.Path()
	}
	args, err := l.relaunchFor(session, false, adjust)
	if err != nil {
		return err
	}
	p := l.p
	p.restartMu.Lock()
	p.restart, p.treeMoving = args, move
	p.restartMu.Unlock()
	return nil
}

// treeMoved tells the model, in a gnosh EnterWorktree or ExitWorktree
// started, where the session now works, and starts the turn that goes on
// with the task.
func (l *live) treeMoved() {
	m := l.p.treeMove
	if m.Entered == "" && m.Left == "" || l.ts == nil || l.agent == nil {
		return
	}
	// Once: a session /clear begins later in this gnosh moved nowhere.
	l.p.treeMove = treeMove{}
	if m.Carried {
		return
	}
	var text string
	switch {
	case m.Entered != "" && l.st.tree != nil:
		t := l.st.tree
		text = fmt.Sprintf("The session works in the git work tree %s now, on the branch %s, a checkout of %s at %s: commands run and files are read and written there, and the user's checkout at %s is out of reach for edits. Changes that were not committed there are not in the tree. The project's instructions and settings are the tree's. ExitWorktree takes the session back when the user asks.",
			l.cwd(), t.branch, t.repo, shortCommit(t.base), t.from)
	case m.Left != "" && m.Removed && m.BranchKept:
		text = fmt.Sprintf("The session works in %s again: the git work tree %s was removed, and its branch %s is kept.", l.cwd(), m.Left, m.Branch)
	case m.Left != "" && m.Removed:
		text = fmt.Sprintf("The session works in %s again: the git work tree %s was removed with its branch %s.", l.cwd(), m.Left, m.Branch)
	case m.Left != "":
		text = fmt.Sprintf("The session works in %s again: the git work tree %s is kept, with its branch %s (gnosh --worktree %s works in it again).", l.cwd(), m.Left, m.Branch, m.Name)
	default:
		// (The tree could not be entered: the start has said why.)
		return
	}
	_ = l.agent.Tell(agent.Note{Source: "worktree", Text: prompt.Wrap(text)})
	if _, err := l.agent.Submit(agent.Prompt{
		Text:   "Go on with the task where you left off.",
		Hidden: true,
		Own:    true,
		Source: "worktree",
		Notice: "The session moved; the model goes on.",
	}); err != nil {
		l.flog.logf("session", "the turn after the move could not start: %v", err)
	}
}
