package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// In a session a person attends, the end of a command that runs in the
// background is told to the model without anybody asking: the session
// wakes, the model reads how the command ended, and answers.
func TestBackgroundCommandEndWakesTheSession(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 0.4; echo finished > bg.txt; echo the job is done","run_in_background":true}`}}},
		apptest.Reply{Text: "It runs in the background now."},
		apptest.Reply{Text: "The background job has finished."})
	s := sit(t, w, "-y")
	s.see("Type a prompt and Enter.")
	s.typeLine("start the job")
	s.see("It runs in the background now.")
	s.see("-- done in ")
	// Nobody types anything: the end of the command starts the next turn.
	s.see("A background command ended")
	s.see("The background job has finished.")
	if !w.Exists("bg.txt") {
		t.Errorf("the background command did not run to its end")
	}
	third := requestText(t, w, 2)
	has(t, "what the model read about the command's end", third, "bg1", "exit code 0")
	s.typeLine("/exit")
	s.exit()
	w.Model.Done()
	checkSession(t, w, w.Sessions()[0])
}

// /tasks lists the session's background commands, running and ended, and
// stops one by its id.
func TestTasksCommand(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 60","run_in_background":true}`}}},
		apptest.Reply{Text: "It sleeps in the background."},
		apptest.Reply{Text: "Noted that it was stopped."})
	s := sit(t, w, "-y")
	s.see("Type a prompt and Enter.")
	s.typeLine("start a long job")
	s.see("It sleeps in the background.")
	s.see("-- done in ")
	s.typeLine("/tasks")
	s.see("bg1  running for ")
	s.see(": sleep 60")
	s.see("its output: ")
	s.see("/tasks stop <id> stops one.")
	s.typeLine("/tasks stop bg1")
	s.see("bg1 is stopped: sleep 60")
	s.typeLine("/tasks")
	s.see("bg1  killed by ")
	s.typeLine("/exit")
	s.exit()
}

// A run waits for the background work its turn started (spec/cc/cli.md
// 4.5): the command's end wakes the model, the turn that starts is the
// run's too, and the run ends when nothing is left running. Text output
// prints each turn's reply; JSON output reports the last turn.
func TestRunWaitsForItsBackgroundWork(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 1; touch late.txt; echo the job is done","run_in_background":true}`}}},
		apptest.Reply{Text: "Started."},
		apptest.Reply{Text: "The job is done."})
	r := run(t, w, "-y", "-p", "start it").wantCode(t, 0)
	if !w.Exists("late.txt") {
		t.Errorf("the run did not wait for its background command")
	}
	if r.out != "Started.\n\nThe job is done.\n" {
		t.Errorf("the run printed %q", r.out)
	}
	has(t, "what the model read", requestText(t, w, 2), "A background command has ended", "bg1", "exit code 0")
	w.Model.Done()

	w.Model.Add(
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Bash", Args: `{"command":"sleep 1; echo again","run_in_background":true}`}}},
		apptest.Reply{Text: "Started again."},
		apptest.Reply{Text: "Done again."})
	r = run(t, w, "-y", "-p", "--output-format", "json", "start it again").wantCode(t, 0)
	has(t, "the JSON result", r.out, `"result":"Done again."`)
	hasNot(t, "the JSON result", r.out, "Started again.")
}

// A run whose command left a process running (a server started with
// nohup ... &, its output sent to a file) tells the model, once, as it is
// about to end, that its end stops it, and gives it a turn: one restarted
// with setsid is left running after the run; what is not is stopped. A
// run that left nothing running is not told. (The FrontierHarness run of
// 18 November lost a task this way: the server was stopped before it was
// graded.)
func TestRunTellsWhatItsEndStops(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"nohup sleep 300 > server.log 2>&1 & echo $! > server.pid"}`}}},
		apptest.Reply{Text: "Done."},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Bash", Args: `{"command":"if kill -0 $(cat server.pid) 2>/dev/null; then echo running > before.txt; else echo stopped > before.txt; fi; setsid nohup sleep 301 > kept.log 2>&1 < /dev/null & echo $! > kept.pid"}`}}},
		apptest.Reply{Text: "Kept the server."})
	r := run(t, w, "-y", "-p", "start the server", "--output-format", "json").wantCode(t, 0)
	// The run's result is the answer to the prompt: the reply to gnosh's
	// own note took its place (the print mode review of 2026-11-19).
	if obj := jsonLines(t, r.out)[0]; obj["result"] != "Done." || obj["subtype"] != "success" {
		t.Errorf("the run's result: %v", obj)
	}
	has(t, "what the model read", requestText(t, w, 2), "This run (gnosh -p) is about to end, and what your commands left running was stopped: process group", "(sleep)", "setsid nohup <command>")
	// (Stopped before the model's turn: a server started again finds its
	// port free.)
	if data, _ := os.ReadFile(filepath.Join(w.Dir, "before.txt")); strings.TrimSpace(string(data)) != "stopped" {
		t.Errorf("as the model was told, the process it left was %q", strings.TrimSpace(string(data)))
	}
	pidOf := func(name string) int {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(w.Dir, name))
		pid, perr := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || perr != nil {
			t.Fatalf("%s: %q, %v", name, data, err)
		}
		return pid
	}
	server, kept := pidOf("server.pid"), pidOf("kept.pid")
	t.Cleanup(func() { syscall.Kill(kept, syscall.SIGKILL); syscall.Kill(server, syscall.SIGKILL) })
	if syscall.Kill(server, 0) == nil {
		t.Errorf("the process the model did not keep is still running after the run")
	}
	if syscall.Kill(kept, 0) != nil {
		t.Errorf("the process started with setsid was stopped with the run")
	}
	w.Model.Done()

	w.Model.Add(apptest.Reply{Text: "Nothing to start."})
	r = run(t, w, "-y", "-p", "say so").wantCode(t, 0)
	if r.out != "Nothing to start.\n" {
		t.Errorf("a run that left nothing running printed %q", r.out)
	}
	w.Model.Done()
}

// In stream-json output each turn's result line is written as the turn
// ends, the one the end of background work started too, and a run fed
// stream-json waits for that work as well once its input is over.
func TestRunWaitsInStreamJSON(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 1; echo one","run_in_background":true}`}}},
		apptest.Reply{Text: "Started."},
		apptest.Reply{Text: "It said one."})
	r := run(t, w, "-y", "-p", "--output-format", "stream-json", "--verbose", "start it").wantCode(t, 0)
	results := 0
	for _, line := range strings.Split(r.out, "\n") {
		if strings.Contains(line, `"type":"result"`) {
			results++
		}
	}
	if results != 2 || !strings.Contains(r.out, `"result":"Started."`) || !strings.Contains(r.out, `"result":"It said one."`) {
		t.Errorf("%d result lines:\n%s", results, r.out)
	}
	w.Model.Done()

	w.Model.Add(
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Bash", Args: `{"command":"sleep 1; echo two","run_in_background":true}`}}},
		apptest.Reply{Text: "Started two."},
		apptest.Reply{Text: "It said two."})
	o := testOptions(w, "-y", "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
	o.Stdin = strings.NewReader(`{"type":"user","message":{"role":"user","content":"start two"}}` + "\n")
	r = runWith(t, context.Background(), o).wantCode(t, 0)
	if !strings.Contains(r.out, `"result":"Started two."`) || !strings.Contains(r.out, `"result":"It said two."`) {
		t.Errorf("a run fed stream-json:\n%s", r.out)
	}
	w.Model.Done()
}

// A run whose turns of the model are spent (--max-turns) cannot ask the
// model at a checkpoint whether to wait on: what is left is stopped and
// the run ends, as Claude Code's wait ends. It asked, the turn was stopped
// before its first request, and the next checkpoint asked again, so the
// run never ended (the print mode review of 2026-11-19).
func TestRunWaitCheckpointWithItsTurnsSpent(t *testing.T) {
	defer func(was time.Duration) { waitCheckpoint = was }(waitCheckpoint)
	waitCheckpoint = 300 * time.Millisecond
	start := apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 60; touch late.txt","run_in_background":true}`}}}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	// The answer took the last of the run's turns.
	w := apptest.NewWorld(t, start, apptest.Reply{Text: "Started."})
	r := runWith(t, ctx, testOptions(w, "-y", "-p", "--max-turns", "2", "--output-format", "json", "start it")).wantCode(t, 0)
	has(t, "the JSON result", r.out, `"result":"Started."`, `"subtype":"success"`)
	has(t, "stderr", r.err, "cannot ask the model whether to wait on, as its 2 turns of the model are spent (--max-turns); it stops the work now: bg1")
	w.Model.Done()
	// The cap stopped the turn.
	w.Model.Add(start)
	r = runWith(t, ctx, testOptions(w, "-y", "-p", "--max-turns", "1", "--output-format", "json", "start it again")).wantCode(t, 1)
	has(t, "the JSON result", r.out, `"subtype":"error_max_turns"`)
	has(t, "stderr", r.err, "it stops the work now: bg1")
	w.Model.Done()
	time.Sleep(time.Second)
	if w.Exists("late.txt") {
		t.Errorf("the background command outlived the run")
	}
}

// A ceiling set with CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS stops what is
// left once the run has waited that long, and the run ends.
func TestRunWaitCeiling(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 60; touch late.txt","run_in_background":true}`}}},
		apptest.Reply{Text: "Started."})
	o := testOptions(w, "-y", "-p", "start it")
	o.Environ = append(o.Environ, "CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=500")
	began := time.Now()
	r := runWith(t, context.Background(), o).wantCode(t, 0)
	if took := time.Since(began); took > 30*time.Second {
		t.Errorf("the run took %v", took)
	}
	has(t, "stderr", r.err, "waited", "for background work and stops it now (CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS): bg1")
	w.Model.Done()
	time.Sleep(time.Second)
	if w.Exists("late.txt") {
		t.Errorf("the background command outlived the run")
	}
}

// GNOSH_PRINT_BG_CHECKPOINT_MS makes the run ask the model sooner than
// every ten minutes whether to wait on for its background work.
func TestRunWaitCheckpointEvery(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 60","run_in_background":true}`}}},
		apptest.Reply{Text: "Started."},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "TaskStop", Args: `{"task_id":"bg1"}`}}},
		apptest.Reply{Text: "Stopped it."})
	o := testOptions(w, "-y", "-p", "start it")
	o.Environ = append(o.Environ, "GNOSH_PRINT_BG_CHECKPOINT_MS=500")
	began := time.Now()
	runWith(t, context.Background(), o).wantCode(t, 0)
	if took := time.Since(began); took > 30*time.Second {
		t.Errorf("the run took %v", took)
	}
	has(t, "what the model read", requestText(t, w, 2), "This run (gnosh -p) has waited", "for background work that still runs")
	w.Model.Done()
}

// GNOSH_PRINT_MAX_DURATION_MS gives the run a time budget: once most of it
// is used, the model is told, with its next request, to wrap up.
func TestRunTimeBudgetWrapUp(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 2"}`}}},
		apptest.Reply{Text: "Done."})
	o := testOptions(w, "-y", "-p", "work")
	o.Environ = append(o.Environ, "GNOSH_PRINT_MAX_DURATION_MS=1000")
	runWith(t, context.Background(), o).wantCode(t, 0)
	has(t, "what the model read", requestText(t, w, 1), "This run (gnosh -p) has used", "time budget", "Wrap up now")
	hasNot(t, "the first request", requestText(t, w, 0), "Wrap up now")
	w.Model.Done()

	// Without the budget, nothing is said.
	w.Model.Add(
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Bash", Args: `{"command":"true"}`}}},
		apptest.Reply{Text: "Done."})
	run(t, w, "-y", "-p", "work").wantCode(t, 0)
	hasNot(t, "a run with no budget", requestText(t, w, 3), "Wrap up now")
	w.Model.Done()
}
