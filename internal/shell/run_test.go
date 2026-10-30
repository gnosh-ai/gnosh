package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRun_Echo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: "echo hi",
		Cwd:     t.TempDir(),
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v (stderr=%q)", err, stderr.String())
	}
	if got := stdout.String(); got != "hi\n" {
		t.Fatalf("stdout = %q, want %q", got, "hi\n")
	}
}

func TestRun_ExitCode(t *testing.T) {
	err := Run(t.Context(), RunOptions{
		Command: "exit 7",
		Cwd:     t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error for exit 7, got nil")
	}
	if code := ExitCode(err); code != 7 {
		t.Fatalf("ExitCode = %d, want 7", code)
	}
}

func TestRun_Stdin(t *testing.T) {
	// Use the `read` shell builtin so the test doesn't depend on any
	// external binary being on PATH (we pass an empty Env here).
	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: "read line; echo got:$line",
		Cwd:     t.TempDir(),
		Stdin:   strings.NewReader("hello\n"),
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := stdout.String(); got != "got:hello\n" {
		t.Fatalf("stdout = %q, want %q", got, "got:hello\n")
	}
}

func TestRun_Env(t *testing.T) {
	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: `echo "$FOO"`,
		Cwd:     t.TempDir(),
		Env:     []string{"FOO=bar"},
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := stdout.String(); got != "bar\n" {
		t.Fatalf("stdout = %q, want %q", got, "bar\n")
	}
}

func TestRun_Cwd(t *testing.T) {
	dir := t.TempDir()
	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: "pwd",
		Cwd:     dir,
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// mvdan's pwd builtin resolves symlinks (e.g. /var -> /private/var on
	// macOS). Compare against a suffix so we don't get bitten by that.
	got := strings.TrimRight(stdout.String(), "\n")
	if !strings.HasSuffix(got, dir) && !strings.HasSuffix(dir, got) {
		t.Fatalf("pwd = %q, want it to match %q", got, dir)
	}
}

func TestRun_JqBuiltin(t *testing.T) {
	var stdout bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: `echo '{"a":1}' | jq .a`,
		Cwd:     t.TempDir(),
		Stdout:  &stdout,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := stdout.String(); got != "1\n" {
		t.Fatalf("stdout = %q, want %q", got, "1\n")
	}
}

func TestRun_ParallelIsolation(t *testing.T) {
	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	outs := make([]string, n)
	dirs := make([]string, n)
	for i := range n {
		dirs[i] = t.TempDir()
		go func(i int) {
			defer wg.Done()
			var stdout bytes.Buffer
			errs[i] = Run(t.Context(), RunOptions{
				Command: `echo "$MARKER"`,
				Cwd:     dirs[i],
				Env:     []string{fmt.Sprintf("MARKER=id-%d", i)},
				Stdout:  &stdout,
			})
			outs[i] = stdout.String()
		}(i)
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Errorf("goroutine %d: err = %v", i, errs[i])
			continue
		}
		want := fmt.Sprintf("id-%d\n", i)
		if outs[i] != want {
			t.Errorf("goroutine %d: stdout = %q, want %q", i, outs[i], want)
		}
	}
}

// TestRun_CtxCancel_BusyLoop verifies that a pure-shell loop respects ctx
// cancellation. mvdan's interpreter checks ctx between statements, so this
// should return quickly even without any external command. The test bounds
// its own wait via a select so a regression can't hang CI.
func TestRun_CtxCancel_BusyLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, RunOptions{
			Command: "while true; do :; done",
			Cwd:     t.TempDir(),
		})
	}()

	select {
	case err := <-done:
		if !IsInterrupt(err) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected interrupt/deadline error, got: %v", err)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("Run did not return within 1.5s after ctx cancel")
	}
}

// TestRun_CtxCancel_ExternalSleep verifies ctx cancellation reaches an
// external process via mvdan's default exec. Uses sleep, which lives in
// coreutils on Windows and /bin on Unix.
func TestRun_CtxCancel_ExternalSleep(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	t.Cleanup(cancel)

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- Run(ctx, RunOptions{
			Command: "sleep 30",
			Cwd:     t.TempDir(),
		})
	}()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if elapsed > time.Second {
			t.Fatalf("sleep took too long to cancel: %v", elapsed)
		}
		if err == nil {
			t.Fatal("expected non-nil error from cancelled sleep")
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s after ctx cancel")
	}
}

func TestRun_ParseError(t *testing.T) {
	err := Run(t.Context(), RunOptions{
		Command: "echo 'unterminated",
		Cwd:     t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Fatalf("error should mention parse: %v", err)
	}
}

func TestRun_BlockFuncs(t *testing.T) {
	block := CommandsBlocker([]string{"forbidden"})
	var stderr bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command:    "forbidden",
		Cwd:        t.TempDir(),
		Stderr:     &stderr,
		BlockFuncs: []BlockFunc{block},
	})
	if err == nil {
		t.Fatal("expected error when running blocked command")
	}
	if !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected 'not allowed' error, got: %v", err)
	}
}

// An agent on the AI box ran "job_kill 077" in bash, meaning the job_kill
// tool, and the shell's not-found error told it nothing more. The shell
// now says it is a tool, by its current name; another missing command,
// and a command on the PATH named like a tool, are left as they were.
func TestRun_ToolNameAsCommand(t *testing.T) {
	run := func(command string, env []string) (stdout, stderr string, code int) {
		var out, errOut bytes.Buffer
		err := Run(t.Context(), RunOptions{
			Command: command,
			Cwd:     t.TempDir(),
			Env:     env,
			Stdout:  &out,
			Stderr:  &errOut,
		})
		return out.String(), errOut.String(), ExitCode(err)
	}

	_, stderr, code := run("job_kill 077", nil)
	if code != 127 {
		t.Fatalf("exit code = %d, want 127", code)
	}
	if !strings.Contains(stderr, "executable file not found") {
		t.Fatalf("the shell's own error is gone: %q", stderr)
	}
	if !strings.Contains(stderr, "job_kill is a Gnosh tool, not a shell command: call the job_kill tool instead.") {
		t.Fatalf("stderr does not say job_kill is a tool: %q", stderr)
	}

	_, stderr, _ = run("crush_info", nil)
	if !strings.Contains(stderr, "call the gnosh_info tool instead") {
		t.Fatalf("a tool's old name does not lead to the tool: %q", stderr)
	}

	_, stderr, code = run("no_such_command_here", nil)
	if code != 127 || strings.Contains(stderr, "Gnosh tool") {
		t.Fatalf("a missing command that names no tool: exit %d, stderr %q", code, stderr)
	}

	// Go's coreutils, the default on Windows, run ls in process, off the
	// PATH: an ls that worked is no cue to call the ls tool.
	if coreUtilsExecHandler != nil {
		saved := useGoCoreUtils
		useGoCoreUtils = true
		t.Cleanup(func() { useGoCoreUtils = saved })
		_, stderr, code = run("ls", nil)
		if code != 0 || strings.Contains(stderr, "Gnosh tool") {
			t.Fatalf("ls through Go's coreutils: exit %d, stderr %q", code, stderr)
		}
		useGoCoreUtils = saved
	}

	if runtime.GOOS == "windows" {
		return
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "view"), []byte("#!/bin/sh\necho ran\nexit 127\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := run("view", []string{"PATH=" + bin})
	if stdout != "ran\n" || code != 127 || strings.Contains(stderr, "Gnosh tool") {
		t.Fatalf("a view command on the PATH: stdout %q, exit %d, stderr %q", stdout, code, stderr)
	}
}

func TestRun_RequiresCwd(t *testing.T) {
	err := Run(t.Context(), RunOptions{
		Command: "echo hi",
	})
	if err == nil {
		t.Fatal("expected error when Cwd is empty, got nil")
	}
	if !strings.Contains(err.Error(), "Cwd is required") {
		t.Fatalf("error should mention Cwd requirement: %v", err)
	}
}

func TestWithNonInteractiveEnv_Empty(t *testing.T) {
	t.Parallel()
	result := withNonInteractiveEnv(nil)
	// All defaults must be present.
	for _, want := range nonInteractiveEnvVars {
		if !slices.Contains(result, want) {
			t.Errorf("missing default %q in result", want)
		}
	}
}

func TestWithNonInteractiveEnv_OverridesExisting(t *testing.T) {
	t.Parallel()
	env := []string{"EDITOR=nvim", "PAGER=less", "FOO=bar"}
	result := withNonInteractiveEnv(env)

	// EDITOR and PAGER must be overridden, not preserved.
	for _, e := range result {
		if e == "EDITOR=nvim" {
			t.Error("EDITOR=nvim should have been overridden")
		}
		if e == "PAGER=less" {
			t.Error("PAGER=less should have been overridden")
		}
	}
	// FOO must survive.
	if !slices.Contains(result, "FOO=bar") {
		t.Error("FOO=bar should be preserved")
	}
}

func TestWithNonInteractiveEnv_NoPrefixCollision(t *testing.T) {
	t.Parallel()
	// EDITORIAL should NOT match EDITOR.
	env := []string{"EDITORIAL=yes", "GITHUB_TOKEN=secret"}
	result := withNonInteractiveEnv(env)

	foundEditorial := false
	foundGithub := false
	for _, e := range result {
		if e == "EDITORIAL=yes" {
			foundEditorial = true
		}
		if e == "GITHUB_TOKEN=secret" {
			foundGithub = true
		}
	}
	if !foundEditorial {
		t.Error("EDITORIAL=yes should not be removed by EDITOR override")
	}
	if !foundGithub {
		t.Error("GITHUB_TOKEN=secret should not be removed")
	}
}

func TestWithNonInteractiveEnv_SliceIndependence(t *testing.T) {
	t.Parallel()
	env := []string{"FOO=bar"}
	result := withNonInteractiveEnv(env)
	// Mutating the input must not affect the result.
	env[0] = "FOO=baz"
	for _, e := range result {
		if e == "FOO=baz" {
			t.Error("result shares backing array with input")
		}
	}
}

func TestWithoutHerdrEnv_StripsAllVars(t *testing.T) {
	t.Parallel()
	env := []string{
		"HERDR_ENV=1",
		"HERDR_SOCKET_PATH=/tmp/herdr.sock",
		"HERDR_PANE_ID=wA:p1",
		"PATH=/usr/bin",
		"HOME=/home/user",
	}
	result := withoutHerdrEnv(env)
	for _, e := range result {
		if strings.HasPrefix(e, "HERDR_") {
			t.Errorf("herdr var not stripped: %s", e)
		}
	}
	if !slices.Contains(result, "PATH=/usr/bin") {
		t.Error("non-herdr var PATH was incorrectly removed")
	}
	if !slices.Contains(result, "HOME=/home/user") {
		t.Error("non-herdr var HOME was incorrectly removed")
	}
}

func TestWithoutHerdrEnv_EmptyInput(t *testing.T) {
	t.Parallel()
	result := withoutHerdrEnv(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil input, got %v", result)
	}
}

func TestWithoutHerdrEnv_SliceIndependence(t *testing.T) {
	t.Parallel()
	env := []string{"HERDR_ENV=1", "FOO=bar"}
	result := withoutHerdrEnv(env)
	env[1] = "FOO=baz"
	for _, e := range result {
		if e == "FOO=baz" {
			t.Error("result shares backing array with input")
		}
	}
}

func TestRun_DiscardsNilWriters(t *testing.T) {
	// No panic when Stdout/Stderr are nil.
	err := Run(t.Context(), RunOptions{
		Command: "echo hi; echo err >&2",
		Cwd:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}
