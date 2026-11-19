package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// gnosh --bg starts a new session in the background on the prompt, as a
// run of its own (the flags written out, a new session's id, -p, and the
// prompt after --), keeps its process id beside its log, and says how to
// follow it. gnosh logs <session> prints what it printed, and gnosh stop
// <session> says when it is not running. -p, -c and a missing prompt are
// refused before anything starts.
func TestBackgroundFromTheShell(t *testing.T) {
	w := apptest.NewWorld(t)
	var got []string
	var log string
	o := testOptions(w, "--bg", "-y", "--name", "nightly", "fix the flaky test")
	o.Detach = func(args, env []string, l string) (int, error) {
		got, log = args, l
		return 4242, nil
	}
	r := runWith(t, context.Background(), o).wantCode(t, 0)
	if log == "" {
		t.Fatalf("nothing was started: %s", r.err)
	}
	id := strings.TrimSuffix(filepath.Base(log), ".background.log")
	want := []string{"--name=nightly", "--dangerously-skip-permissions", "--session-id=" + id, "--print", "--", "fix the flaky test"}
	slices.Sort(want[:3])
	head := slices.Clone(got[:3])
	slices.Sort(head)
	if len(got) != 6 || !slices.Equal(head, want[:3]) || !slices.Equal(got[3:], want[3:]) {
		t.Fatalf("the run was started with %q", got)
	}
	has(t, "what is said", r.out, "gnosh: backgrounded · "+id+" · process 4242", "gnosh agents ", "gnosh logs "+id[:13], "gnosh stop "+id[:13], "gnosh -r "+id[:13]+"  ")
	if pid, _ := os.ReadFile(strings.TrimSuffix(log, ".log") + ".pid"); strings.TrimSpace(string(pid)) != "4242" {
		t.Errorf("the process id kept: %q", pid)
	}

	// The list names it by the name it was given.
	has(t, "gnosh agents", run(t, w, "agents").wantCode(t, 0).out, id[:13]+"  ", "  nightly")
	has(t, "gnosh agents --json --all", run(t, w, "agents", "--json", "--all").wantCode(t, 0).out, `"name": "nightly"`, `"prompt": "fix the flaky test"`)

	// What the run printed, by the start of its id.
	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("The flaky test is fixed.\n")
	f.Close()
	r = run(t, w, "logs", id[:13]).wantCode(t, 0)
	has(t, "gnosh logs", r.out, "goes on in the background with: fix the flaky test", "The flaky test is fixed.")
	r = run(t, w, "stop", id[:13]).wantCode(t, 0)
	has(t, "gnosh stop", r.out, "the session "+id+" is not running: gnosh -r "+id+" opens it")
	r = run(t, w, "stop", "nosuch").wantCode(t, 1)
	has(t, "gnosh stop", r.err, "no session ran in the background under the ID nosuch", "gnosh agents lists them")

	for _, bad := range [][]string{{"--bg", "-p", "x"}, {"--bg", "--fork-session", "-c", "x"}, {"--bg"}} {
		o := testOptions(w, bad...)
		started := false
		o.Detach = func([]string, []string, string) (int, error) { started = true; return 1, nil }
		r := runWith(t, context.Background(), o).wantCode(t, 1)
		if started {
			t.Errorf("%q started a run", bad)
		}
		has(t, strings.Join(bad, " "), r.err, "nothing was started")
	}
}

// gnosh agents lists the sessions in the background of every folder, the
// working ones first, each by the start of its ID with when it started,
// how it stands, its folder and its prompt; --cwd keeps that folder's
// own. --json prints the working ones, and with --all the rest too. A run
// works while its process does: a session held open by some other gnosh
// is not working. gnosh logs finds a run of another folder, and gnosh stop
// stops one and marks it stopped.
func TestAgentsListsTheRunsOfEveryFolder(t *testing.T) {
	w := apptest.NewWorld(t)
	other := filepath.Join(w.Root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	start := func(cwd, prompt string) (id, log string) {
		t.Helper()
		o := testOptions(w, "--bg", prompt)
		o.Cwd = cwd
		o.Detach = func(_, _ []string, l string) (int, error) {
			log = l
			return 4242, nil
		}
		runWith(t, context.Background(), o).wantCode(t, 0)
		return strings.TrimSuffix(filepath.Base(log), ".background.log"), log
	}
	addTo := func(log, text string) {
		t.Helper()
		f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(text)
		f.Close()
	}
	done, doneLog := start(w.Dir, "fix the flaky test")
	// The run quotes gnosh's own lines: they are its output, not gnosh's.
	addTo(doneLog, "  gnosh: its folder is /elsewhere\ngnosh: its session file is /elsewhere/x.jsonl\nAll 42 tests pass.\ngnosh: 2026-11-18T10:00:00Z"+runDone+"\n")
	working, workingLog := start(other, "write the docs")
	addTo(workingLog, "Reading the README.\n")
	// The working run's process: one whose command line names its
	// session, as a run's does, and which ends when asked to.
	proc := exec.Command("sh", "-c", `trap "exit 0" TERM; while :; do sleep 0.1; done`, working)
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	go func() { proc.Wait(); close(ended) }()
	t.Cleanup(func() { proc.Process.Kill(); <-ended })
	if err := os.WriteFile(strings.TrimSuffix(workingLog, ".log")+".pid", []byte(strconv.Itoa(proc.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The finished run's session is open in a gnosh of its own.
	file := filepath.Join(filepath.Dir(doneLog), done+".jsonl")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(run(t, w, "agents").wantCode(t, 0).out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], working[:13]+"  ") || !strings.HasPrefix(lines[1], done[:13]+"  ") {
		t.Fatalf("gnosh agents printed:\n%s", strings.Join(lines, "\n"))
	}
	has(t, "the working run", lines[0], "  working ", "  "+other+"  ", "write the docs")
	has(t, "the run that is done", lines[1], "  done  ", "  "+w.Dir+"  ", "fix the flaky test")
	has(t, "how to go on", lines[2], "gnosh logs <name>", "gnosh stop <name>", "gnosh -r <name>")
	out := run(t, w, "agents", "--cwd", other).wantCode(t, 0).out
	has(t, "gnosh agents --cwd", out, working[:13])
	hasNot(t, "gnosh agents --cwd", out, done[:13])

	var rows []map[string]any
	if err := json.Unmarshal([]byte(run(t, w, "agents", "--json").wantCode(t, 0).out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["id"] != working || rows[0]["kind"] != "background" || rows[0]["state"] != "working" ||
		rows[0]["cwd"] != other || rows[0]["prompt"] != "write the docs" || rows[0]["status"] != "Reading the README." ||
		rows[0]["pid"] != float64(proc.Process.Pid) {
		t.Errorf("gnosh agents --json: %v", rows)
	}
	rows = nil
	if err := json.Unmarshal([]byte(run(t, w, "agents", "--json", "--all").wantCode(t, 0).out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1]["id"] != done || rows[1]["state"] != "done" || rows[1]["status"] != "All 42 tests pass." || rows[1]["cwd"] != w.Dir {
		t.Errorf("gnosh agents --json --all: %v", rows)
	}

	// From this folder, the run of the other: its log, then a stop.
	has(t, "gnosh logs", run(t, w, "logs", working[:13]).wantCode(t, 0).out, "goes on in the background with: write the docs", "Reading the README.")
	r := run(t, w, "stop", working[:13]).wantCode(t, 0)
	has(t, "gnosh stop", r.out, "gnosh: the session "+working+" was stopped. What it did so far is kept")
	has(t, "gnosh stop", r.err, "was asked to stop; it ends what it was doing first")
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the run's process did not end")
	}
	lines = strings.Split(run(t, w, "agents", "--cwd", other).wantCode(t, 0).out, "\n")
	has(t, "the stopped run", lines[0], working[:13], "  stopped  ")
	if err := json.Unmarshal([]byte(run(t, w, "agents", "--json", "--all", "--cwd", other).wantCode(t, 0).out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["state"] != "stopped" || rows[0]["status"] != "Reading the README." {
		t.Errorf("the stopped run as JSON: %v", rows)
	}
	has(t, "gnosh agents once nothing runs", run(t, w, "agents", "--json").wantCode(t, 0).out, "[]")
	empty := filepath.Join(w.Root, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	has(t, "gnosh agents --cwd of a folder with none", run(t, w, "agents", "--cwd", empty).wantCode(t, 0).out,
		"No session runs in the background from "+empty+" or a folder in it, and none ran in the last week: gnosh --bg <prompt> starts one.")
}

// gnosh --bg takes what is piped in with the prompt, as gnosh -p does,
// and starts the run in this folder when --worktree is given, leaving the
// work tree to the run.
func TestBackgroundTakesPipedInput(t *testing.T) {
	w := apptest.NewWorld(t)
	var got []string
	o := testOptions(w, "--bg", "review this diff")
	o.Stdin = strings.NewReader("--- a/x.go\n+++ b/x.go\n")
	o.StdinIsTerminal = false
	o.Detach = func(args, _ []string, _ string) (int, error) {
		got = args
		return 4242, nil
	}
	runWith(t, context.Background(), o).wantCode(t, 0)
	if len(got) == 0 || got[len(got)-1] != "--- a/x.go\n+++ b/x.go\n\nreview this diff" {
		t.Errorf("the run was given %q", got)
	}
}

// gnosh --bg --worktree leaves the work tree to the run: the process that
// starts it makes none, and the run is given --worktree.
func TestBackgroundLeavesTheWorkTreeToTheRun(t *testing.T) {
	w := apptest.NewWorld(t)
	var got []string
	o := testOptions(w, "--bg", "fix it", "--worktree")
	o.Detach = func(args, _ []string, _ string) (int, error) {
		got = args
		return 4242, nil
	}
	r := runWith(t, context.Background(), o).wantCode(t, 0)
	if !slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, "--worktree") }) {
		t.Errorf("the run was given %q", got)
	}
	hasNot(t, "what is said", r.out+r.err, "work tree")
}

// A session sent to the background from two folders has a log in each:
// the list tells it once, by the run that works, and gnosh stop and gnosh
// logs find that run's log by any start of the session's id.
func TestRunLoggedFromTwoFolders(t *testing.T) {
	root := t.TempDir()
	const id = "01a1ee90-0000-7000-8000-000000000001"
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The first run, from a, is done; the second, from b, works.
	write(filepath.Join(a, id+".background.log"), "gnosh: 2026-11-18T10:00:00Z the session "+id+" goes on in the background with: first\ngnosh: its folder is /a\ndone\n")
	write(filepath.Join(a, id+".background.pid"), "4242\n")
	write(filepath.Join(b, id+".background.log"), "gnosh: 2026-11-18T09:00:00Z the session "+id+" goes on in the background with: second\ngnosh: its folder is /b\nworking\n")
	// A stand-in for gnosh on its session: a process whose command line
	// names the session the whole time. (With one command, dash puts sleep
	// in its own place, and the session's id is gone from the command line:
	// a CI run on a busy machine looked after that.) It is in a process
	// group of its own, so that a kill takes its sleep too.
	proc := exec.Command("sh", "-c", "sleep 30; true", id)
	proc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	stop := func() { _ = syscall.Kill(-proc.Process.Pid, syscall.SIGKILL); _ = proc.Wait() }
	t.Cleanup(stop)
	write(filepath.Join(b, id+".background.pid"), strconv.Itoa(proc.Process.Pid)+"\n")
	// Start returns once the exec is under way; the kernel sets out the
	// new program's arguments a moment later, and until then its command
	// line reads empty, naming no session. On a busy machine that moment
	// was long enough for the run to read as one that had died (a CI run
	// of 19 November).
	cmdline := "/proc/" + strconv.Itoa(proc.Process.Pid) + "/cmdline"
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		data, err := os.ReadFile(cmdline)
		if err != nil || strings.Contains(string(data), id) {
			break
		}
	}

	runs := runsIn([]string{a, b}, time.Time{})
	if len(runs) != 1 || runs[0].prompt != "second" || !runs[0].running || runs[0].cwd != "/b" {
		t.Fatalf("runs: %+v", runs)
	}
	for _, word := range []string{id, id[:13], id[:4]} {
		if got, log, err := findRun([]string{a, b}, word); err != nil || got != id || filepath.Dir(log) != b {
			t.Errorf("findRun(%q) = %q, %q, %v", word, got, log, err)
		}
	}
	// Once neither works, the newer run is told.
	stop()
	runs = runsIn([]string{a, b}, time.Time{})
	if len(runs) != 1 || runs[0].prompt != "first" || runs[0].running {
		t.Fatalf("runs once neither works: %+v", runs)
	}
}

// Whatever a run's log holds, reading it ends, and gnosh's own lines are
// taken only from where gnosh writes them.
func FuzzReadRunLog(f *testing.F) {
	const id = "01a1ee90-0000-7000-8000-000000000001"
	for _, s := range []string{
		"gnosh: 2026-11-18T10:00:00Z the session " + id + " goes on in the background with: x\ngnosh: its folder is /a\ngnosh: its session file is /a/s.jsonl\nout\n",
		"gnosh: 2026-11-18T10:00:00Z the run was stopped; what it did so far is kept.\n",
		"  gnosh: its folder is /x\n", "gnosh: \n", "\n\n\x00",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		path := filepath.Join(t.TempDir(), id+".background.log")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		r := readRunLog(path, id, time.Time{})
		if r.cwd != "" && !strings.Contains(text, " the session "+id+" goes on in the background with: ") {
			t.Fatalf("a folder %q was read from a log with no first line of gnosh's", r.cwd)
		}
	})
}

// gnosh --bg -r <session> and --bg -c send a session that is there back
// to work: the run resumes it (--resume, not --session-id), and its log is
// beside the session's file, the new run's head after the old one's. A
// session open in another gnosh is not sent; a name that matches none
// says so.
func TestBackgroundGoesOnWithASession(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "First."})
	run(t, w, "-p", "the first prompt").wantCode(t, 0)
	w.Model.Done()
	files, _ := filepath.Glob(filepath.Join(w.Root, "*", "gnosh", "projects", "*", "*.jsonl"))
	if len(files) != 1 {
		files, _ = filepath.Glob(filepath.Join(w.Root, "**", "*.jsonl"))
	}
	var session string
	_ = filepath.WalkDir(w.Root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") && !strings.Contains(p, "checkpoints") {
			session = p
		}
		return nil
	})
	if session == "" {
		t.Fatal("the first run left no session file")
	}
	id := strings.TrimSuffix(filepath.Base(session), ".jsonl")
	send := func(args ...string) ([]string, string, result) {
		t.Helper()
		var got []string
		var log string
		o := testOptions(w, args...)
		o.Detach = func(a, _ []string, l string) (int, error) {
			got, log = a, l
			return 4242, nil
		}
		return got, log, runWith(t, context.Background(), o)
	}
	for _, args := range [][]string{{"--bg", "-r", id[:13], "go on"}, {"--bg", "-c", "go on again"}} {
		got, log, r := send(args...)
		r.wantCode(t, 0)
		if !slices.Contains(got, "--resume="+id) || slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, "--session-id") }) {
			t.Errorf("%q started the run with %q", args, got)
		}
		if log != strings.TrimSuffix(session, ".jsonl")+".background.log" {
			t.Errorf("%q: the log is %s, not beside %s", args, log, session)
		}
		has(t, "what is said", r.out, "gnosh: backgrounded · "+id+" · process 4242")
	}
	// A name given (the agent view's Ctrl+R) is the run's, and a run sent
	// back to work keeps it.
	log := strings.TrimSuffix(session, ".jsonl") + ".background.log"
	if err := renameRun(log, id, "nightly", time.Now(), true); err != nil {
		t.Fatal(err)
	}
	if r := readRunLog(log, id, time.Time{}); r.name != "nightly" {
		t.Errorf("after the rename the run is called %q", r.name)
	}
	if _, _, r := send("--bg", "-r", id, "and once more"); r.code != 0 {
		t.Fatalf("a third send: %d %s", r.code, r.err)
	}
	if r := readRunLog(log, id, time.Time{}); r.name != "nightly" || r.prompt != "and once more" {
		t.Errorf("the run sent back to work is called %q, on %q", r.name, r.prompt)
	}
	data, _ := os.ReadFile(log)
	if n := strings.Count(string(data), "goes on in the background with: "); n != 3 {
		t.Errorf("the log holds %d heads:\n%s", n, data)
	}
	_, _, r := send("--bg", "-r", "no such name", "go on")
	r.wantCode(t, 1)
	has(t, "an unknown session", r.err, `no session has the ID or the name "no such name", so nothing was started`)
	// Held open by another gnosh: not sent.
	f, err := os.Open(session)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	got, _, r := send("--bg", "-r", id, "go on")
	r.wantCode(t, 1)
	if got != nil {
		t.Errorf("a held session was sent: %q", got)
	}
	has(t, "a held session", r.err, "is open in another gnosh, and one session has one writer, so nothing was started")
}
