package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// A run in the background ends its log with how it ended, as its last
// line: done when it ended well, failed (and with what) when it ended with
// an error. gnosh agents and the agent view then say done or failed, as
// Claude Code's list does.
func TestRunEndsItsLogWithHowItEnded(t *testing.T) {
	for _, c := range []struct {
		name        string
		reply       apptest.Reply
		code        int
		line, state string
	}{
		{"done", apptest.Reply{Text: "All done."}, 0, " the run is done.", "done"},
		{"failed", apptest.Reply{Status: 400, Body: `{"error":{"message":"no such model"}}`}, 1, " the run failed: it ended with exit status 1.", "failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := apptest.NewWorld(t, c.reply)
			log := filepath.Join(w.Root, "run.background.log")
			head := "gnosh: 2026-11-18T10:00:00Z the session run goes on in the background with: go on\n"
			if err := os.WriteFile(log, []byte(head), 0o600); err != nil {
				t.Fatal(err)
			}
			o := testOptions(w, "-p", "go on")
			o.Environ = append(o.Environ, AsksEnv+"="+runBase(log))
			r := runWith(t, context.Background(), o)
			r.wantCode(t, c.code)
			data, _ := os.ReadFile(log)
			lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
			at, after, _ := strings.Cut(strings.TrimPrefix(lines[len(lines)-1], "gnosh: "), " ")
			if _, err := time.Parse(time.RFC3339, at); err != nil || " "+after != c.line {
				t.Fatalf("the log ends with %q, not the time and %q", lines[len(lines)-1], c.line)
			}
			if got := runState(readRunLog(log, "run", time.Time{})); got != c.state {
				t.Errorf("the run's state is %q, not %q", got, c.state)
			}
		})
	}
	// A gnosh that is not a run in the background writes no such line.
	w := apptest.NewWorld(t, apptest.Reply{Text: "All done."})
	log := filepath.Join(w.Root, "run.background.log")
	if err := os.WriteFile(log, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runWith(t, context.Background(), testOptions(w, "-p", "go on")).wantCode(t, 0)
	if data, _ := os.ReadFile(log); string(data) != "before\n" {
		t.Errorf("a run not in the background wrote to the log: %q", data)
	}
}

// How a run stands, from its log and its process: one whose process is
// gone and that ended its log with nothing failed (it was killed, or its
// machine stopped), unless it has no process id yet (it is starting, or
// an older gnosh started it); one that a signal stopped, or gnosh stop,
// is stopped; a run of a session that goes on again in the background is
// told by its own end, not by the last run's.
func TestRunStates(t *testing.T) {
	dir := t.TempDir()
	const id = "01a1eea5-5555-7000-8000-000000000005"
	head := "gnosh: 2026-11-18T10:00:00Z the session " + id + " goes on in the background with: go\n"
	for _, c := range []struct {
		name, log  string
		pid        bool
		state, end string
	}{
		{"done", head + "All done.\ngnosh: 2026-11-18T10:01:00Z the run is done.\n", true, "done", ""},
		{"failed", head + "gnosh: no model.\ngnosh: 2026-11-18T10:01:00Z the run failed: it ended with exit status 1.\n", true, "failed", "it ended with exit status 1"},
		{"killed", head + "Working on it.\n", true, "failed", "it ended without a word: it was killed, or its machine stopped"},
		{"no process id", head + "Working on it.\n", false, "done", ""},
		{"signal", head + "gnosh: 2026-11-18T10:01:00Z the run was stopped by a signal (exit status 143).\n", true, "stopped", "it was stopped by a signal"},
		{"gnosh stop", head + "gnosh: 2026-11-18T10:01:00Z the run was stopped by a signal (exit status 143).\ngnosh: 2026-11-18T10:01:01Z" + runStopped + "\n", true, "stopped", ""},
		// (What the run printed after an end line makes that one a quote.)
		{"quoted", head + "gnosh: 2026-11-18T10:01:00Z the run is done.\nand then it was killed\n", true, "failed", "it ended without a word: it was killed, or its machine stopped"},
		{"again", head + "gnosh: 2026-11-18T10:01:00Z the run failed: it ended with exit status 1.\n" +
			"gnosh: 2026-11-18T10:02:00Z the session " + id + " goes on in the background with: again\nDone.\ngnosh: 2026-11-18T10:03:00Z the run is done.\n", true, "done", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			sub := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-"))
			if err := os.MkdirAll(sub, 0o700); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(sub, id+".background.log")
			if err := os.WriteFile(log, []byte(c.log), 0o600); err != nil {
				t.Fatal(err)
			}
			if c.pid {
				// (This test's own process, which is not a run of the session.)
				writeRunPID(log, os.Getpid())
			}
			runs := runsIn([]string{sub}, time.Time{})
			if len(runs) != 1 {
				t.Fatalf("%d runs", len(runs))
			}
			if got, end := runState(runs[0]), runEnding(runs[0]); got != c.state || end != c.end {
				t.Errorf("state %q, ending %q; want %q, %q", got, end, c.state, c.end)
			}
			// When it ended is the time on its end line, or on gnosh stop's.
			if want := map[string]string{"done": "10:01:00", "failed": "10:01:00", "signal": "10:01:00", "gnosh stop": "10:01:01", "again": "10:03:00"}[c.name]; want != "" {
				if got := runs[0].endedAt.UTC().Format("15:04:05"); got != want {
					t.Errorf("it ended at %s, not %s", got, want)
				}
			} else if !runs[0].endedAt.IsZero() {
				t.Errorf("one that said nothing ended at %v", runs[0].endedAt)
			}
			if strings.Contains(strings.Join(runs[0].output, "\n"), "the run ") {
				t.Errorf("the end line was taken for what the run printed: %q", runs[0].output)
			}
		})
	}
}

// A run's process is one whose command line names the session; one that
// is gone is told so before anything is run to read a command line.
func TestRunIsFor(t *testing.T) {
	const id = "01a1eea5-6666-7000-8000-000000000006"
	cmd := exec.Command("sh", "-c", `trap "exit 0" TERM; while :; do sleep 0.1; done`, id)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	// (Its command line is there a moment after it starts: the kernel
	// shows it once the new program is laid out.)
	for deadline := time.Now().Add(5 * time.Second); !runIsFor(pid, id) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if !runIsFor(pid, id) || runIsFor(pid, "01a1eea5-7777-7000-8000-000000000007") {
		t.Errorf("the process of the run: for its session %v, for another %v", runIsFor(pid, id), runIsFor(pid, "01a1eea5-7777-7000-8000-000000000007"))
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if runIsFor(pid, id) {
		t.Errorf("a process that is gone is taken for the run")
	}
}
