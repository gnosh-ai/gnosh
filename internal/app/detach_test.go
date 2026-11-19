package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// /background ends the session here and hands it to a run of its own, on
// the prompt given: the same flags, the session resumed, -p, and the prompt
// after --, with its output in a file beside the session's.
func TestBackgroundCommand(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hi."})
	var got [][]string
	var logs []string
	s := sitWith(t, w, func(o *Options) {
		o.Detach = func(args, env []string, log string) (int, error) {
			got, logs = append(got, args), append(logs, log)
			return 4242, nil
		}
	}, "-y", "say hi")
	s.see("Hi.")
	s.see("-- done in ")
	s.typeLine("/bg")
	s.see("say what the session is to do on its own: /background <prompt>")
	s.typeLine("/background fix the flaky test, then run them all")
	s.see("The session goes on in the background, on its own, with: fix the flaky test, then run them all")
	if code := s.exit(); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	id := strings.TrimSuffix(filepath.Base(w.Sessions()[0]), ".jsonl")
	want := []string{"--dangerously-skip-permissions", "--resume=" + id, "--print", "--", "fix the flaky test, then run them all"}
	if len(got) != 1 || !slices.Equal(got[0], want) {
		t.Fatalf("the run was started with %q, want %q", got, want)
	}
	if logs[0] != filepath.Join(filepath.Dir(w.Sessions()[0]), id+".background.log") {
		t.Errorf("its output goes to %s", logs[0])
	}
	head, _ := os.ReadFile(logs[0])
	has(t, "the log's first line", string(head), "the session "+id+" goes on in the background with: fix the flaky test, then run them all")
	has(t, "what is said on the way out", s.errs.String(), "gnosh: the session goes on in the background (process 4242)", id+".background.log", "gnosh -r "+id+" opens it again once it is done")
	if _, err := os.Stat(w.Sessions()[0]); err != nil {
		t.Errorf("the session's file: %v", err)
	}

	// A gnosh that cannot do it says so, and the session stays.
	w = apptest.NewWorld(t)
	s = sit(t, w)
	s.see("Type a prompt and Enter.")
	s.typeLine("/background go on")
	s.see("this gnosh cannot go on without its terminal")
	s.typeLine("/exit")
	s.exit()
}

// /fork copies the conversation into a new session and starts it in the
// background on the prompt, while this session goes on here.
func TestForkCommand(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hi."}, apptest.Reply{Text: "Still here."})
	var got [][]string
	s := sitWith(t, w, func(o *Options) {
		o.Detach = func(args, env []string, log string) (int, error) {
			got = append(got, args)
			return 4343, nil
		}
	}, "--permission-mode", "acceptEdits")
	s.see("Type a prompt and Enter.")
	s.typeLine("/fork look into it")
	s.see("there is nothing to copy yet")
	s.typeLine("say hi")
	s.see("Hi.")
	s.see("-- done in ")
	s.typeLine("/fork look into the flaky test")
	s.see("goes on in the background (process 4343) with: look into the flaky test")
	s.see("It works in this same folder, beside this session.")
	// This session goes on.
	s.typeLine("are you there?")
	s.see("Still here.")
	s.typeLine("/exit")
	s.exit()
	if len(w.Sessions()) != 2 {
		t.Fatalf("%d session files, want the session and its copy", len(w.Sessions()))
	}
	if len(got) != 1 {
		t.Fatalf("started %d runs: %q", len(got), got)
	}
	args := got[0]
	if !slices.Equal(args[len(args)-3:], []string{"--print", "--", "look into the flaky test"}) || !slices.Contains(args, "--permission-mode=acceptEdits") {
		t.Errorf("the copy's run: %q", args)
	}
	resume := ""
	for _, a := range args {
		if strings.HasPrefix(a, "--resume=") {
			resume = strings.TrimPrefix(a, "--resume=")
		}
	}
	var ids []string
	for _, f := range w.Sessions() {
		ids = append(ids, strings.TrimSuffix(filepath.Base(f), ".jsonl"))
	}
	if resume == "" || !slices.Contains(ids, resume) {
		t.Errorf("the run resumes %q, not one of the sessions %q", resume, ids)
	}
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(w.Sessions()[0]), resume+".jsonl"))
	has(t, "the copy", string(data), "say hi")
}

// A session open in a gnosh is marked in gnosh session list, and opening it
// again is refused with a word about the run's log when it has one.
func TestOpenSessionsMarked(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hi."})
	s := sit(t, w, "say hi")
	s.see("Hi.")
	s.see("-- done in ")
	out := run(t, w, "session", "list").wantCode(t, 0).out
	has(t, "the list while the session is open", out, "(open now)")
	has(t, "the list as JSON", run(t, w, "session", "list", "--json").wantCode(t, 0).out, `"open": true`)
	path := w.Sessions()[0]
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if err := os.WriteFile(strings.TrimSuffix(path, ".jsonl")+".background.log", []byte("working\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Another session's /resume list marks it too.
	other := sit(t, w)
	other.see("Type a prompt and Enter.")
	other.typeLine("/resume")
	other.see("(open now)")
	other.typeLine("/exit")
	other.exit()
	r := run(t, w, "-r", id, "-p", "hello")
	has(t, "opening it again", r.out+r.err, "is open in another gnosh", "If it is a run in the background, it lets go of the session when it is done: gnosh logs -f "+id[:13]+" follows it, and gnosh stop "+id[:13]+" stops it")
	s.typeLine("/exit")
	s.exit()
	hasNot(t, "the list once it is closed", run(t, w, "session", "list").wantCode(t, 0).out, "(open now)")
}

// /tasks lists the sessions /background and /fork sent off from this
// folder: the latest run of each, running (it holds its session) or done
// with the last line it printed, and how to open it.
func TestTasksListsRunsInTheBackground(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hi."})
	s := sitWith(t, w, func(*Options) {}, "-y", "say hi")
	s.see("Hi.")
	s.see("-- done in ")
	dir := filepath.Dir(w.Sessions()[0])
	const done, running = "01a1e4c6-59d1-7a20-a39f-c815401a8978", "01a1e4c7-0000-7000-8000-000000000001"
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(done+".background.log", "gnosh: 2026-11-16T10:00:00Z the session "+done+" goes on in the background with: an older run\nold output\n"+
		"gnosh: 2026-11-16T11:00:00Z the session "+done+" goes on in the background with: fix the flaky test\nworking\nAll 42 tests pass.\n\n")
	write(running+".background.log", "gnosh: 2026-11-16T12:00:00Z the session "+running+" goes on in the background with: write the docs\n")
	write(running+".jsonl", "")
	f, err := os.Open(filepath.Join(dir, running+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	s.typeLine("/tasks")
	s.see("a session in the background, running for ")
	s.see("write the docs")
	s.see("/tasks stop " + running[:13] + " stops it")
	s.see("gnosh -r " + running + " opens it once it is done")
	s.see("a session in the background, done, started ")
	s.see("fix the flaky test")
	s.see("it ended with: All 42 tests pass.")
	s.see("gnosh -r " + done + " opens it; all it printed is in ")
	if strings.Contains(s.out.String(), "an older run") {
		t.Error("an earlier run in the same log was listed")
	}
	// /tasks stop reaches a run of this folder too, as gnosh stop does.
	s.typeLine("/tasks stop " + done[:13])
	s.see("The session " + done + " is not running: gnosh -r " + done + " opens it.")
	s.typeLine("/tasks stop " + running[:13])
	s.see("gnosh has no process ID for a run of it")
	s.typeLine("/exit")
	s.exit()
}

// -c goes on with the newest session that no other gnosh holds open, as
// Claude Code's passes over a background session that still runs: a run
// /background or /fork started is the newest while it works.
func TestContinuePassesOverASessionOpenElsewhere(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "One."}, apptest.Reply{Text: "Two."})
	run(t, w, "-p", "first").wantCode(t, 0)
	run(t, w, "-p", "second").wantCode(t, 0)
	sessions := w.Sessions()
	if len(sessions) != 2 {
		t.Fatalf("%d sessions, want 2", len(sessions))
	}
	older := strings.TrimSuffix(filepath.Base(sessions[0]), ".jsonl")
	newer := strings.TrimSuffix(filepath.Base(sessions[1]), ".jsonl")
	f, err := os.Open(sessions[1])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	r := run(t, w, "-c", "-p", "/status").wantCode(t, 0)
	has(t, "what -c says", r.out+r.err, "The newest session of this folder, "+shortID(newer)+", is open in another gnosh", "-c goes on with "+shortID(older))
}

// While this session is open, it says when a run /fork started is done:
// the run holds its session file while it works and lets go at its end.
// /subtask is /fork whose answer comes back: when the run ends, what it
// printed goes to the model, which tells the person.
func TestSubtaskAnswerComesBack(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hi."}, apptest.Reply{Text: "The subtask says all 42 tests pass."})
	var named string
	s := sitWith(t, w, func(o *Options) {
		o.Detach = func(args, env []string, log string) (int, error) {
			id := ""
			for _, a := range args {
				if v, ok := strings.CutPrefix(a, "--resume="); ok {
					id = v
				}
			}
			if head, err := os.ReadFile(log); err == nil {
				named = string(head)
			}
			path := filepath.Join(filepath.Dir(log), id+".jsonl")
			held := make(chan error, 1)
			go func() {
				f, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					held <- err
					return
				}
				defer f.Close()
				if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
					held <- err
					return
				}
				held <- nil
				time.Sleep(3 * time.Second)
				lf, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND, 0)
				if err == nil {
					fmt.Fprintln(lf, "Ran the suite.")
					fmt.Fprintln(lf, "All 42 tests pass.")
					fmt.Fprintf(lf, "gnosh: %s%s\n", time.Now().UTC().Format(time.RFC3339), runDone)
					lf.Close()
				}
			}()
			return 4545, <-held
		}
	})
	s.see("Type a prompt and Enter.")
	s.typeLine("/subtask")
	s.see("say what the copy is to do: /subtask <task>")
	s.typeLine("say hi")
	s.see("Hi.")
	s.see("-- done in ")
	s.typeLine("/subtask run the whole test suite and report the count")
	s.see("works on it in the background (process 4545)")
	s.see("is done (gnosh -r ")
	s.see("The subtask says all 42 tests pass.")
	s.typeLine("/exit")
	s.exit()
	if !strings.Contains(named, runNameLine+"run the whole test suite and\n") {
		t.Errorf("the run is not named after the task's first words: %q", named)
	}
	reqs := w.Model.Requests()
	last := reqs[len(reqs)-1]
	var told string
	for _, m := range last.Messages() {
		told += apptest.Text(m) + "\n"
	}
	has(t, "what the model read", told, "set to work in the background with /subtask has finished: run the whole test suite and report the count",
		"Its answer, in its own words", "Ran the suite.\nAll 42 tests pass.")
}

// A run that begins and ends between two looks of the watcher (it never
// seems to hold its session) is read from its log: it is done, and its
// answer comes back, not "ended without taking its session".
func TestSubtaskThatEndsBetweenLooks(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hi."}, apptest.Reply{Text: "It says: fast answer."})
	s := sitWith(t, w, func(o *Options) {
		o.Detach = func(args, env []string, log string) (int, error) {
			lf, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				return 0, err
			}
			fmt.Fprintln(lf, "Fast answer.")
			fmt.Fprintf(lf, "gnosh: %s%s\n", time.Now().UTC().Format(time.RFC3339), runDone)
			lf.Close()
			return 4646, nil
		}
	})
	s.see("Type a prompt and Enter.")
	s.typeLine("say hi")
	s.see("-- done in ")
	s.typeLine("/subtask answer fast")
	s.see("is done (gnosh -r ")
	s.see("It says: fast answer.")
	s.typeLine("/exit")
	s.exit()
	if strings.Contains(s.out.String(), "ended without taking its session") {
		t.Error("a run that ended between two looks was said not to have started")
	}
}

func TestForkSaysWhenItIsDone(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hi."})
	s := sitWith(t, w, func(o *Options) {
		o.Detach = func(args, env []string, log string) (int, error) {
			id := ""
			for _, a := range args {
				if v, ok := strings.CutPrefix(a, "--resume="); ok {
					id = v
				}
			}
			path := filepath.Join(filepath.Dir(log), id+".jsonl")
			held := make(chan error, 1)
			go func() {
				// As a run does: take the session, work, write the answer
				// to the log, let go.
				f, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					held <- err
					return
				}
				defer f.Close()
				if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
					held <- err
					return
				}
				held <- nil
				time.Sleep(5 * time.Second)
				lf, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND, 0)
				if err == nil {
					fmt.Fprintln(lf, "All 42 tests pass.")
					fmt.Fprintf(lf, "gnosh: %s%s\n", time.Now().UTC().Format(time.RFC3339), runDone)
					lf.Close()
				}
			}()
			return 4444, <-held
		}
	})
	s.see("Type a prompt and Enter.")
	s.typeLine("say hi")
	s.see("Hi.")
	s.see("-- done in ")
	s.typeLine("/fork run the tests")
	s.see("goes on in the background (process 4444)")
	s.see("is done (gnosh -r ")
	s.see("It ended with: All 42 tests pass.")
	s.typeLine("/exit")
	s.exit()
}
