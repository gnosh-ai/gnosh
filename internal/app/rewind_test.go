package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// /rewind lists the person's prompts and takes the conversation back to
// before one of them: the model is then sent what came before it and
// nothing after, the session is the same one, and a later run that
// continues it reads the conversation as it was left.
func TestRewindInThePlainSession(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "First answer."}, apptest.Reply{Text: "Second answer."}, apptest.Reply{Text: "Third answer."},
		apptest.Reply{Text: "The answer to the changed prompt."}, apptest.Reply{Text: "Continued."})
	s := sit(t, w)
	s.see("Type a prompt and Enter.")
	for i, p := range []string{"the first prompt", "the second prompt", "the third prompt"} {
		s.typeLine(p)
		s.see([]string{"First answer.", "Second answer.", "Third answer."}[i])
		s.see("-- done in ")
	}
	s.typeLine("/rewind")
	s.see("Your prompts in this conversation:")
	s.see("the first prompt")
	s.see("the third prompt")
	s.see("/rewind N takes the conversation back to before prompt N")
	s.see("what a file holds when it is put back is kept first")
	s.typeLine("/rewind 9")
	s.see("is not the number of one of your prompts")
	s.typeLine("/rewind 2")
	s.see("The conversation is back to before this prompt of yours")
	s.see("the second prompt")
	s.see("The files stay as they are now.")

	s.typeLine("the second prompt, changed")
	s.see("The answer to the changed prompt.")
	reqs := w.Model.Requests()
	sent := string(reqs[len(reqs)-1].Body)
	has(t, "what the model was sent after the rewind", sent, "the first prompt", "First answer.", "the second prompt, changed")
	hasNot(t, "what the model was sent after the rewind", sent, "Second answer.", "the third prompt", "Third answer.")
	// The facts that go with a session's first prompt are not given a
	// second time: the first prompt is still in the conversation.
	if n := strings.Count(sent, "Working directory"); n > 1 {
		t.Errorf("the environment was given %d times", n)
	}
	s.typeLine("/rewind")
	s.see("  2. ")
	s.see("the second prompt, changed")
	s.typeLine("/exit")
	if code := s.exit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
	if sessions := w.Sessions(); len(sessions) != 1 {
		t.Fatalf("session files: %v", sessions)
	}
	checkSession(t, w, w.Sessions()[0])

	// Continued in a new process, it is the conversation as it was left.
	run(t, w, "-c", "-p", "go on").wantCode(t, 0)
	reqs = w.Model.Requests()
	sent = string(reqs[len(reqs)-1].Body)
	has(t, "what a later run was sent", sent, "the first prompt", "the second prompt, changed", "The answer to the changed prompt.", "go on")
	hasNot(t, "what a later run was sent", sent, "Second answer.", "Third answer.")
}

// The files: what Write and Edit changed from a prompt on is put back with
// the conversation ("/rewind N all") or alone ("/rewind N files"), as it was
// before the first of those changes; a file that was not there is taken away
// again. Copies are kept beside the session's file, so a later process can
// put the files back too.
func TestRewindPutsFilesBack(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Write", Args: `{"file_path":"notes.txt","content":"first\n"}`}}},
		apptest.Reply{Text: "Written."},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Read", Args: `{"file_path":"keep.txt"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c3", Name: "Edit", Args: `{"file_path":"keep.txt","old_string":"kept","new_string":"changed"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c4", Name: "Write", Args: `{"file_path":"notes.txt","content":"second\n"}`}}},
		apptest.Reply{Text: "Changed both."},
		apptest.Reply{Calls: []apptest.Call{{ID: "c5", Name: "Edit", Args: `{"file_path":"notes.txt","old_string":"second","new_string":"third"}`}}},
		apptest.Reply{Text: "Third."},
		apptest.Reply{Text: "The model knows the files went back."},
	)
	w.Write("keep.txt", "kept\n")
	s := sit(t, w, "--permission-mode", "acceptEdits")
	s.see("Type a prompt and Enter.")
	s.typeLine("write the notes")
	s.see("Written.")
	s.typeLine("change both files")
	s.see("Changed both.")
	s.typeLine("one more change")
	s.see("Third.")
	if got := w.Read("notes.txt") + w.Read("keep.txt"); got != "third\nchanged\n" {
		t.Fatalf("before the rewind the files hold %q", got)
	}

	// The list says which prompts files were changed after.
	s.typeLine("/rewind")
	s.see("1. ")
	s.see("write the notes  (2 files changed since)")
	s.see("change both files  (2 files changed since)")
	s.see("one more change  (1 file changed since)")

	// The files alone, back to before the second prompt: notes.txt as the
	// first prompt left it, keep.txt as it was; the conversation stays.
	s.typeLine("/rewind 2 files")
	s.see("The files are back as they were before your prompt: change both files")
	s.see("Put back 2 files")
	if got := w.Read("notes.txt") + w.Read("keep.txt"); got != "first\nkept\n" {
		t.Errorf("after /rewind 2 files the files hold %q", got)
	}
	s.typeLine("what happened?")
	s.see("The model knows the files went back.")
	reqs := w.Model.Requests()
	has(t, "the request after the files went back", string(reqs[len(reqs)-1].Body), "back as they were before it", "Third.")

	// The session is left, and taken up again in a new process: the copies
	// are still there.
	s.typeLine("/exit")
	s.exit()
	s = sit(t, w, "-c", "--permission-mode", "acceptEdits")
	s.see("Type a prompt and Enter.")
	s.typeLine("/rewind")
	s.see("write the notes  (2 files changed since)")

	// Conversation and files, back to before the first prompt: notes.txt
	// was not there, and goes.
	s.typeLine("/rewind 1 all")
	s.see("The conversation is back to before this prompt of yours")
	s.see("Put back 2 files")
	if w.Exists("notes.txt") {
		t.Errorf("notes.txt was not there before the first prompt, and is there after the rewind")
	}
	if got := w.Read("keep.txt"); got != "kept\n" {
		t.Errorf("keep.txt holds %q after the rewind", got)
	}
	s.typeLine("/exit")
	s.exit()
}

// Back to before the very first prompt: the conversation is empty again,
// and the next prompt is a first prompt, with the facts that go with one.
func TestRewindToTheFirstPrompt(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "First answer."}, apptest.Reply{Text: "Afresh."})
	w.Write("AGENTS.md", "Always answer in one sentence.\n")
	s := sit(t, w, "--trust-folder")
	s.see("Type a prompt and Enter.")
	s.typeLine("the first prompt")
	s.see("First answer.")
	s.see("-- done in ")
	s.typeLine("/rewind 1")
	s.see("The conversation is back to before this prompt of yours")
	s.typeLine("another beginning")
	s.see("Afresh.")
	reqs := w.Model.Requests()
	sent := strings.Join(userTexts(reqs[len(reqs)-1]), "\n")
	has(t, "what the model was sent", sent, "another beginning", "Always answer in one sentence.")
	hasNot(t, "what the model was sent", string(reqs[len(reqs)-1].Body), "the first prompt", "First answer.")
	s.typeLine("/exit")
	if code := s.exit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
	checkSession(t, w, w.Sessions()[0])
}

// Through the controls, as an interface does it: the points, the prompt's
// text back, a session that must be idle, and the features told that the
// session is opened again.
func TestRewindThroughTheControls(t *testing.T) {
	hold := make(chan struct{})
	w := apptest.NewWorld(t, apptest.Reply{Text: "One."}, apptest.Reply{Text: "Two."},
		apptest.Reply{Text: "Held.", Hold: hold}, apptest.Reply{Text: "After the rewind."})
	var mu sync.Mutex
	var opened []string
	feature := Feature{
		Name: "watcher",
		Session: func(ctx context.Context, s *Session) (*Contribution, error) {
			mu.Lock()
			opened = append(opened, s.Source)
			mu.Unlock()
			return &Contribution{}, nil
		},
	}
	var points []RewindPoint
	var text string
	var busyErr, goneErr error
	_, r := attach(t, w, func(o *Options) { o.Features = []Feature{feature} }, func(ctx context.Context, c SessionControls, f *scripted) int {
		for _, p := range []string{"prompt one", "prompt two"} {
			if _, err := c.Submit(agent.Prompt{Text: p}); err != nil {
				return 1
			}
			_ = c.Wait(ctx)
		}
		points = c.RewindPoints()
		// Not while a turn runs.
		third, _ := c.Submit(agent.Prompt{Text: "prompt three"})
		find(f, "the third prompt going out", func(e *agent.PromptEvent) bool { return e.ID == third })
		_, busyErr = c.Rewind(ctx, points[1].ID)
		close(hold)
		_ = c.Wait(ctx)

		_, goneErr = c.Rewind(ctx, "no-such-entry")
		var err error
		if text, err = c.Rewind(ctx, points[1].ID); err != nil {
			t.Errorf("Rewind: %v", err)
			return 1
		}
		if got := c.RewindPoints(); len(got) != 1 || got[0].Text != "prompt one" {
			t.Errorf("the points after the rewind: %+v", got)
		}
		if _, err := c.Submit(agent.Prompt{Text: "prompt two, again"}); err != nil {
			t.Errorf("Submit after the rewind: %v", err)
		}
		_ = c.Wait(ctx)
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
	if len(points) != 2 || points[0].Text != "prompt one" || points[1].Text != "prompt two" || points[0].ID == "" || points[1].Time.IsZero() {
		t.Errorf("the points: %+v", points)
	}
	if text != "prompt two" {
		t.Errorf("the rewind gave back %q", text)
	}
	if busyErr == nil || !strings.Contains(busyErr.Error(), "a turn is running") {
		t.Errorf("a rewind while a turn ran: %v", busyErr)
	}
	if goneErr == nil {
		t.Errorf("a rewind to a prompt that is not there did not fail")
	}
	if got := strings.Join(opened, ","); got != "startup,resume" {
		t.Errorf("the feature was told of sessions opened as %q", got)
	}
	reqs := w.Model.Requests()
	sent := string(reqs[len(reqs)-1].Body)
	has(t, "what the model was sent after the rewind", sent, "prompt one", "One.", "prompt two, again")
	hasNot(t, "what the model was sent after the rewind", sent, "Two.", "prompt three", "Held.")
	checkSession(t, w, w.Sessions()[0])
}

// What a file holds when a rewind puts it back, or takes it away, is kept
// first: here the person had changed by hand a file the model wrote.
func TestRewindKeepsWhatAFileHolds(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Write", Args: `{"file_path":"notes.txt","content":"first\n"}`}}},
		apptest.Reply{Text: "Written."},
	)
	s := sit(t, w, "--permission-mode", "acceptEdits")
	s.see("Type a prompt and Enter.")
	s.typeLine("write the notes")
	s.see("Written.")
	w.Write("notes.txt", "mine, by hand\n")
	s.typeLine("/rewind 1 files")
	s.see("taken away: it was not there; what it held is kept at ")
	s.typeLine("/exit")
	s.exit()
	if w.Exists("notes.txt") {
		t.Error("notes.txt was not there before the prompt, and is there after the rewind")
	}
	kept, _ := filepath.Glob(filepath.Join(w.DataDir(), "projects", "*", "*", "checkpoints", "replaced", "*-notes.txt"))
	if len(kept) != 1 {
		t.Fatalf("what notes.txt held was not kept: %q", kept)
	}
	if b, _ := os.ReadFile(kept[0]); string(b) != "mine, by hand\n" {
		t.Errorf("the kept copy holds %q", b)
	}
}

// A rewind of the conversation alone leaves the files as they are; a later
// rewind of the files to an earlier prompt still finds what they held then,
// under the prompt that was taken out of the conversation.
func TestRewindFilesAfterAConversationRewind(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "Nothing to change."},
		apptest.Reply{Calls: []apptest.Call{{ID: "r1", Name: "Read", Args: `{"file_path":"util.txt"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Edit", Args: `{"file_path":"util.txt","old_string":"v0","new_string":"v1"}`}}},
		apptest.Reply{Text: "Edited."},
		apptest.Reply{Calls: []apptest.Call{{ID: "r2", Name: "Read", Args: `{"file_path":"util.txt"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Edit", Args: `{"file_path":"util.txt","old_string":"v1","new_string":"v2"}`}}},
		apptest.Reply{Text: "Edited again."},
	)
	w.Write("util.txt", "v0\n")
	s := sit(t, w, "--permission-mode", "acceptEdits")
	s.see("Type a prompt and Enter.")
	s.typeLine("look around")
	s.see("Nothing to change.")
	s.typeLine("edit util")
	s.see("Edited.")
	s.typeLine("/rewind 2")
	s.see("The conversation is back to before this prompt of yours")
	s.typeLine("edit util once more")
	s.see("Edited again.")
	if got := w.Read("util.txt"); got != "v2\n" {
		t.Fatalf("util.txt holds %q before the second rewind", got)
	}
	s.typeLine("/rewind 1 files")
	s.see("Put back: ")
	s.typeLine("/exit")
	s.exit()
	if got := w.Read("util.txt"); got != "v0\n" {
		t.Errorf("util.txt holds %q after a rewind to before the first prompt, want what it held then", got)
	}
}

// What a sub-agent changes is its own, as in Claude Code: a rewind of the
// session does not take it back.
func TestRewindLeavesWhatASubAgentChanged(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Agent", Args: `{"description":"write the file","prompt":"Write sub.txt.","subagent_type":"general-purpose"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "s1", Name: "Write", Args: `{"file_path":"sub.txt","content":"by the sub-agent\n"}`}}},
		apptest.Reply{Text: "Wrote it."},
		apptest.Reply{Text: "The sub-agent wrote it."},
	)
	s := sit(t, w, "--permission-mode", "acceptEdits")
	s.see("Type a prompt and Enter.")
	s.typeLine("have it written")
	s.see("The sub-agent wrote it.")
	if !w.Exists("sub.txt") {
		t.Fatal("the sub-agent wrote nothing")
	}
	s.typeLine("/rewind 1 files")
	s.see("No file was changed by Write or Edit since then")
	s.typeLine("/exit")
	s.exit()
	if !w.Exists("sub.txt") {
		t.Error("a rewind took back what a sub-agent wrote")
	}
}

// A branch finds the copies kept under the prompts it took over, in the
// folder of the session it was branched from.
func TestRewindInABranch(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Write", Args: `{"file_path":"notes.txt","content":"first\n"}`}}},
		apptest.Reply{Text: "Written."},
	)
	s := sit(t, w, "--permission-mode", "acceptEdits")
	s.see("Type a prompt and Enter.")
	s.typeLine("write the notes")
	s.see("Written.")
	s.typeLine("/branch")
	s.see("You are in a copy of the conversation")
	s.typeLine("/rewind 1 files")
	s.see("taken away: it was not there")
	s.typeLine("/exit")
	s.exit()
	if w.Exists("notes.txt") {
		t.Error("the branch's rewind did not find the copy kept before the branch")
	}
}

// A prompt typed while a turn ran waited in the queue. A rewind to before
// it goes back to where it went out, after that turn, and the session that
// opens again does not send it by itself.
func TestRewindToAPromptThatWaited(t *testing.T) {
	hold := make(chan struct{})
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "First, slowly.", Hold: hold},
		apptest.Reply{Text: "Second."},
	)
	s := sit(t, w)
	s.see("Type a prompt and Enter.")
	s.typeLine("first")
	for deadline := time.Now().Add(patience); len(w.Model.Requests()) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the first prompt never reached the model")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.typeLine("second")
	close(hold)
	s.see("Second.")
	s.typeLine("/rewind 2")
	s.see("The conversation is back to before this prompt of yours")
	time.Sleep(time.Second)
	if n := len(w.Model.Requests()); n != 2 {
		t.Errorf("the model was asked %d times: the session sent the prompt it went back to before", n)
	}
	s.typeLine("/exit")
	s.exit()
}

// With fileCheckpointingEnabled false, nothing is kept, and a rewind of the
// files says why it cannot be done.
func TestRewindWithoutCheckpoints(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Write", Args: `{"file_path":"notes.txt","content":"first\n"}`}}},
		apptest.Reply{Text: "Written."},
	)
	w.Settings(`{"options": {"fileCheckpointingEnabled": false}}`)
	s := sit(t, w, "--permission-mode", "acceptEdits")
	s.see("Type a prompt and Enter.")
	s.typeLine("write the notes")
	s.see("Written.")
	s.typeLine("/rewind 1 files")
	s.see("fileCheckpointingEnabled is false")
	s.typeLine("/exit")
	s.exit()
	if !w.Exists("notes.txt") {
		t.Error("a rewind with nothing kept took a file away")
	}
	if kept, _ := filepath.Glob(filepath.Join(w.DataDir(), "projects", "*", "*", "checkpoints", "index.jsonl")); len(kept) != 0 {
		t.Errorf("copies were kept: %q", kept)
	}
}
