package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

// This file is /review (also /code-review), /security-review and
// /simplify, as Claude Code has them: the model reviews the changes on this
// branch, from where it left the default branch, committed or not, for
// bugs or for ways in, and changes no file, or with /review --fix fixes
// the bugs it is sure of; /simplify makes the changes simpler, the code
// doing what it did. gnosh works out the range; the model runs the diff and
// reads what is around it.

// reviewRange says what a review covers: the git diff command that shows
// the changes, and in words, what they are.
type reviewRange struct {
	diff, what string
}

// reviewBase works out the changes a review covers in dir: from the point
// where HEAD left the default branch (origin's, else main or master) to the
// working tree, or with no such branch, what is not committed yet.
func (l *live) reviewBase(ctx context.Context, dir string) (reviewRange, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if inside, err := l.git(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil || inside != "true" {
		return reviewRange{}, errors.New("this folder is not in a git repository, so there are no changes to review")
	}
	var base string
	if ref, err := l.git(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		base = ref
	} else {
		for _, b := range []string{"origin/main", "origin/master", "main", "master"} {
			if _, err := l.git(ctx, dir, "rev-parse", "--verify", "--quiet", b+"^{commit}"); err == nil {
				base = b
				break
			}
		}
	}
	if base != "" {
		if mb, err := l.git(ctx, dir, "merge-base", "HEAD", base); err == nil && mb != "" {
			short := mb
			if len(short) > 12 {
				short = short[:12]
			}
			r := reviewRange{diff: "git diff " + short, what: fmt.Sprintf("from where this branch left %s (%s) to the working tree, committed or not", base, short)}
			if stat, _ := l.git(ctx, dir, "diff", "--stat", mb); stat == "" {
				return reviewRange{}, fmt.Errorf("there are no changes since this branch left %s, so there is nothing to review", base)
			}
			return r, nil
		}
	}
	if stat, _ := l.git(ctx, dir, "diff", "--stat", "HEAD"); stat == "" {
		return reviewRange{}, errors.New("there is no default branch to compare with (origin's, main or master), and nothing that is not committed, so there is nothing to review")
	}
	return reviewRange{diff: "git diff HEAD", what: "not committed yet (there is no default branch to compare with)"}, nil
}

// slashReview is /review: the changes on this branch, reviewed for bugs;
// with --fix, the bugs it is sure of fixed.
func (l *live) slashReview(ctx context.Context, c *SlashContext) error {
	return l.review(ctx, c, "review", reviewTask)
}

// slashSecurityReview is /security-review: the changes on this branch,
// reviewed for ways in.
func (l *live) slashSecurityReview(ctx context.Context, c *SlashContext) error {
	return l.review(ctx, c, "security-review", securityReviewTask)
}

// slashSimplify is /simplify: the changes on this branch made simpler, the
// code doing what it did.
func (l *live) slashSimplify(ctx context.Context, c *SlashContext) error {
	return l.review(ctx, c, "simplify", simplifyTask)
}

// reviewLevels are the words for how thorough a review is that Claude
// Code's /code-review takes first, and what each asks of the model; medium
// asks nothing more.
var reviewLevels = map[string]string{
	"low":    "Report only what you are most sure of: at most three findings.",
	"medium": "",
	"high":   "Be thorough: read every changed function and what calls it, and report what you are less sure of too, saying how sure you are.",
	"xhigh":  "Be thorough: read every changed function and what calls it, and report what you are less sure of too, saying how sure you are.",
	"max":    "Be as thorough as you can: read every changed function, what calls it and what it calls, and report what you are less sure of too, saying how sure you are.",
}

// review sends the model a review of the branch's changes to make. task
// takes the range in words and its diff command. What the person typed
// after the command is, for /review, an optional level word and --fix,
// and for any of them, what to look at in particular.
func (l *live) review(ctx context.Context, c *SlashContext, name, task string) error {
	if !l.st.trusted {
		return fmt.Errorf("/%s runs git, and this folder is not trusted: a repository's own settings can name programs git runs. gnosh trust trusts it", name)
	}
	var level string
	fix := false
	var focus []string
	for i, w := range strings.Fields(c.Args) {
		switch _, isLevel := reviewLevels[w]; {
		case name == "review" && w == "--fix":
			fix = true
		case name == "review" && i == 0 && isLevel:
			level = w
		default:
			focus = append(focus, w)
		}
	}
	r, err := l.reviewBase(ctx, l.cwd())
	if err != nil {
		return err
	}
	text := fmt.Sprintf(task, r.what, "`"+r.diff+"`")
	if fix {
		text = strings.TrimSuffix(text, reviewNoChange) + reviewFix
	}
	if more := reviewLevels[level]; more != "" {
		text += "\n\n" + more
	}
	if name == "security-review" && hasGnoCode(l.cfg.Paths.ProjectRoot, l.cfg.Paths.Cwd) {
		if g := l.gnoGuides(); len(g) == 2 {
			text += fmt.Sprintf("\n\nThis project holds Gno code: read the Gno guides first and hold the Gno changes against them rule by rule: %s (interrealm) and %s (security).", g[0], g[1])
		}
	}
	if len(focus) > 0 {
		text += "\n\nThe user asks you to look at this in particular: " + strings.Join(focus, " ")
	}
	doing := "reviewing the changes"
	switch {
	case name == "simplify":
		doing = "making the changes simpler"
		c.Say("The model looks for what to make simpler in the changes %s, and makes it so.", r.what)
	case fix:
		doing = "reviewing the changes and fixing the bugs it is sure of"
		c.Say("The model reviews the changes %s, and fixes the bugs it is sure of.", r.what)
	default:
		c.Say("The model reviews the changes %s.", r.what)
	}
	return c.Send(agent.Prompt{Hidden: true, Source: name, Text: prompt.Wrap(text),
		Notice: "/" + name + ": the model is " + doing + "."})
}

// reviewNoChange is how the review tasks end: no file is to change.
// reviewFix takes its place for /review --fix.
const (
	reviewNoChange = " Change no file."
	reviewFix      = " Then fix each bug you are sure of, one at a time, with the smallest change that does it, and run the project's tests after. Leave the ones you are not sure of as they are, and say at the end which you fixed and which you left."
)

// reviewTask is what /review asks of the model: the range in words, then
// the command that shows it.
const reviewTask = `The user typed /review. Review the changes %s for bugs, and for what goes against the project's instruction files (GNOSH.md, CLAUDE.md, AGENTS.md). %s shows them. Read the changed files around each change, and the code that calls into them, before you judge a change.

Look for what will go wrong when this code runs: a wrong result; a case that is not handled (empty, nil, zero, the last one, an error); a crash, a leak or a race; a promise to a caller that is broken; a test that does not test what it says. For each, give the file and line, how sure you are, how it fails (the input, and what comes out instead of what should), and the fix you suggest. Mark each one Important (these changes bring it), Pre-existing (it was there before, in code the changes touch or lean on) or Nit (small, but it will bite someone). Leave out style, naming and taste unless they hide a bug. If you find nothing, say "Code review found no issues." Change no file.`

// simplifyTask is what /simplify asks of the model: the range in words,
// then the command that shows it. It does not hunt for bugs; /review does.
const simplifyTask = `The user typed /simplify. Make the changes %s simpler, the code doing what it did. %s shows them. Read the changed files around each change, and the code near it, before you change anything.

Look for four things:
- reuse: code the changes add that does what a function, type or helper already in the repository does: use the one that is there;
- simplicity: more code, branches, layers, options or special cases than the job needs; state kept that could be worked out; code repeated or no longer used;
- efficiency: work done twice, in a loop or for nothing: a file read, a command run, a request sent, a list searched or sorted again; memory held longer than it is needed;
- level: a change made in the wrong place: a fix in a caller that belongs in what it calls, a special case in general code, a detail leaking out of the part that owns it.

This is not a search for bugs (/review is): if you see one, say so, and leave it. Leave names and style alone unless they make the code harder to follow. Make the changes one at a time, each small enough to check, and run the project's tests (or build it) after them. At the end, say in a few lines what you changed and why, and what you saw and left as it is.`

// securityReviewTask is what /security-review asks of the model: the range
// in words, then the command that shows it.
const securityReviewTask = `The user typed /security-review. Review the changes %s for security vulnerabilities. %s shows them. Read the changed files around each change, and what calls into them, so that you know where the data comes from and who can send it.

Look for what an attacker could actually do with this code:
- input that reaches a shell, a query, a template, a path or a deserializer unchecked (injection, path traversal);
- checks of who may do what that are missing, can be skipped, or test the wrong value (authentication, authorization, ownership);
- secrets in the code, in logs or in errors, and data sent where it should not go;
- cryptography used wrongly: home-made schemes, fixed or reused nonces and keys, weak randomness, comparisons that leak time;
- requests to an address the user controls (SSRF), redirects to one, and script injected into what is served (XSS);
- a check and its use that a second request can come between;
- in Gno code, what the Gno guides warn of: who the caller is across realms, coins sent and received, storage a caller can make grow.

Report only what you are confident is a real way in, each with: how bad it is (high, medium or low), the file and line, what an attacker does, step by step, and the fix. Leave out style, hardening that would merely be nice, and what matters only if another bug exists. If you find nothing real, say so plainly: that is a good result. Change no file.`
