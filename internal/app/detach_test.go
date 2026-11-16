package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
	has(t, "opening it again", r.out+r.err, "is open in another gnosh", "If it is a run in the background, it lets go of the session when it is done", ".background.log")
	s.typeLine("/exit")
	s.exit()
	hasNot(t, "the list once it is closed", run(t, w, "session", "list").wantCode(t, 0).out, "(open now)")
}
