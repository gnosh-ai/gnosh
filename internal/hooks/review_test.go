package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

// The same command configured twice runs once, but two hooks that run it
// differently are two guards: a copy in the background listed first does
// not turn the blocking one into a hook that decides nothing.
func TestSameCommandRunDifferentlyIsTwoHooks(t *testing.T) {
	r := newRunner(t, options(t),
		Hook{Event: PreToolUse, Matcher: "Bash", Command: "echo no >&2; exit 2", Async: true},
		Hook{Event: PreToolUse, Matcher: "Bash", Command: "echo no >&2; exit 2"})
	if out := run(t, r, input(PreToolUse)); !out.Blocked {
		t.Errorf("the blocking copy did not decide: %+v", out)
	}
}

// A PermissionRequest hook that could not check answers nothing, and
// another hook's approval does not stand in for it: the question goes to
// the person.
func TestPermissionRequestApprovalNeedsEveryHook(t *testing.T) {
	approve := prints(t, `{"hookSpecificOutput": {"hookEventName": "PermissionRequest", "decision": {"behavior": "allow"}}}`)
	r := newRunner(t, options(t),
		Hook{Event: PermissionRequest, Command: approve},
		Hook{Event: PermissionRequest, Command: "exit 3"})
	out := run(t, r, input(PermissionRequest))
	if out.PermissionDecision == Allow {
		t.Errorf("one hook's approval stood though another could not check: %+v", out)
	}
	if len(out.Warnings) == 0 {
		t.Error("nothing said that a hook could not check")
	}
}

// In a folder that is not trusted, a hook of the person's own that runs a
// program in the project is not run: a repository just cloned could have
// put anything there. A guard of that kind blocks what it would check.
func TestOwnHookOfAProjectProgramWaitsForTrust(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	hook := Hook{Event: PreToolUse, Matcher: "Bash", Global: true,
		Command: `touch ` + shq(marker) + `; "$CLAUDE_PROJECT_DIR"/.claude/hooks/check.sh`}
	opts := options(t)
	opts.Untrusted = true
	out := run(t, newRunner(t, opts, hook), input(PreToolUse))
	if !out.Blocked {
		t.Errorf("a guard that could not run did not block: %+v", out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the hook ran in a folder that is not trusted")
	}
	contains(t, "what was said", out.Reason, "not trusted")

	// Trusted, it runs; and in a folder that is not trusted, a hook of the
	// project's own never gets this far (it waits for trust in config).
	opts.Untrusted = false
	run(t, newRunner(t, opts, hook), input(PreToolUse))
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("in a trusted folder the hook did not run: %v", err)
	}
}

// A tool matcher that names an MCP server alone fits none of its tools,
// and the check says so with the matcher that would.
func TestBareServerMatcherIsWarned(t *testing.T) {
	problems := Check(Config{Hooks: []Hook{{Event: PreToolUse, Matcher: "mcp__memory", Command: "true"}}}, options(t))
	if len(problems) != 1 || !problems[0].Warning {
		t.Fatalf("problems: %+v", problems)
	}
	contains(t, "the warning", problems[0].Message, `"mcp__memory__.*"`)
	if p := Check(Config{Hooks: []Hook{{Event: PreToolUse, Matcher: "mcp__memory__.*", Command: "true"}}}, options(t)); len(p) != 0 {
		t.Errorf("a matcher of the server's tools was warned about: %+v", p)
	}
}

// The exec form looks its program up in the absolute folders of PATH
// alone: an empty or relative entry would be the folder the hook works
// in, which a repository fills.
func TestExecFormSkipsRelativePathEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planted"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p, err := program("planted", []string{"PATH=:.:bin"}, dir); err == nil {
		t.Errorf("the program was found in the hook's folder: %s", p)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if p, err := program("tool", []string{"PATH=.:" + bin}, dir); err != nil || p != filepath.Join(bin, "tool") {
		t.Errorf("program on an absolute PATH entry: %q, %v", p, err)
	}
}
