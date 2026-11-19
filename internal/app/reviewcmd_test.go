package app

import (
	"os"
	"os/exec"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// /review and /security-review: in a trusted repository, the model is
// asked to review the changes since the branch left the default branch,
// committed or not, with the diff command that shows them; with nothing to
// review, or no repository, or a folder that is not trusted, nothing is
// sent.
func TestReviewCommands(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("there is no git on this machine")
	}
	w := apptest.NewWorld(t)
	w.Model.Respond(func(n int, r apptest.Request) *apptest.Reply { return &apptest.Reply{Text: "Nothing real."} })
	gitIn := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", w.Dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+w.Home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	asked := func() string {
		t.Helper()
		reqs := w.Model.Requests()
		if len(reqs) == 0 {
			return ""
		}
		return string(reqs[len(reqs)-1].Body)
	}

	r := run(t, w, "-p", "/review")
	has(t, "a folder that is not trusted", r.out+r.err, "/review runs git, and this folder is not trusted")
	r = run(t, w, "--trust-folder", "-p", "/security-review")
	has(t, "no repository", r.out+r.err, "not in a git repository")
	if len(w.Model.Requests()) != 0 {
		t.Fatal("the model was asked with nothing to review")
	}

	w.Write("a.go", "package a\n")
	gitIn("init", "-q", "-b", "main")
	gitIn("add", "a.go")
	gitIn("commit", "-q", "-m", "one")
	r = run(t, w, "--trust-folder", "-p", "/review")
	has(t, "nothing changed", r.out+r.err, "there are no changes since this branch left main")

	gitIn("checkout", "-q", "-b", "feature")
	w.Write("a.go", "package a\n\nfunc F() {}\n")
	gitIn("commit", "-q", "-am", "two")
	w.Write("b.go", "package a\n")
	gitIn("add", "b.go")
	base := gitIn("rev-parse", "--short=12", "main")[:12]
	r = run(t, w, "--trust-folder", "-p", "/security-review the login path").wantCode(t, 0)
	has(t, "what the person reads", r.out+r.err, "Nothing real.")
	has(t, "what the model is asked", asked(), "The user typed /security-review", "from where this branch left main ("+base+")", "git diff "+base, "Change no file", "look at this in particular: the login path")
	run(t, w, "--trust-folder", "-p", "/review").wantCode(t, 0)
	has(t, "/review", asked(), "The user typed /review", "for bugs", "git diff "+base, "Change no file")
	// As Claude Code 2.1.328's: the instruction files too, each finding
	// marked, a fix suggested, and a clean review said in its words.
	has(t, "/review", asked(), "instruction files", "Important (", "Pre-existing (", "Nit (", "the fix you suggest", "Code review found no issues.")

	// Claude Code's name for it, a level, --fix, and what to look at.
	r = run(t, w, "--trust-folder", "-p", "/code-review low --fix the parser").wantCode(t, 0)
	has(t, "what the person reads", r.out+r.err, "/review: the model is reviewing the changes and fixing the bugs it is sure of.")
	has(t, "/code-review low --fix", asked(), "The user typed /review", "Then fix each bug you are sure of", "at most three findings", "look at this in particular: the parser")
	hasNot(t, "/code-review low --fix", asked(), "Change no file", "in particular: low", "in particular: --fix")

	// /simplify: the same range, and the model may change files.
	r = run(t, w, "--trust-folder", "-p", "/simplify the new function").wantCode(t, 0)
	has(t, "what the person reads", r.out+r.err, "/simplify: the model is making the changes simpler.")
	has(t, "/simplify", asked(), "The user typed /simplify", "git diff "+base, "reuse:", "efficiency:", "not a search for bugs", "look at this in particular: the new function")
	hasNot(t, "/simplify", asked(), "Change no file")
	// The level words and --fix are /review's own: elsewhere they are words.
	run(t, w, "--trust-folder", "-p", "/security-review high --fix").wantCode(t, 0)
	has(t, "/security-review high --fix", asked(), "Change no file", "look at this in particular: high --fix")
}
