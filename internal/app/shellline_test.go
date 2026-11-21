package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// The person's command lines behind "!" are in the prompt history, with
// the "!", in the session that ran them and in the folder's later ones; a
// prompt sent with a blank before its "!" stays a prompt there. By default
// (respondToBashCommands off), the model does not answer a command when it
// ends: the command and what it printed are put in the conversation at
// once, before the next prompt, which the model then answers.
func TestShellLinesInTheHistoryAndUnanswered(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "It printed a greeting."}, apptest.Reply{Text: "Words, not a command."})
	var during []string
	front, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		if err := c.Shell("echo hello from the person"); err != nil {
			t.Errorf("Shell: %v", err)
			return 1
		}
		find(f, "the command and its output in the conversation", func(e *agent.NoteEvent) bool { return e.Note.Source == "shell" })
		during = c.History()
		if _, err := c.Submit(agent.Prompt{Text: "what did it say?"}); err != nil {
			return 1
		}
		_ = c.Wait(ctx)
		if _, err := c.Submit(agent.Prompt{Text: " !not a command"}); err != nil {
			return 1
		}
		_ = c.Wait(ctx)
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
	if len(w.Model.Requests()) == 0 {
		t.Fatal("the model was never asked")
	}
	// The first request is the prompt's: the command had none of its own,
	// and what it printed comes before the prompt.
	first := strings.Join(userTexts(w.Model.Requests()[0]), "\n")
	has(t, "what the model was handed with the prompt", first, "$ echo hello from the person", "hello from the person", "what did it say?")
	if out, asked := strings.Index(first, "$ echo hello from the person"), strings.Index(first, "what did it say?"); out < 0 || asked < out {
		t.Errorf("the command's output is not before the prompt:\n%s", first)
	}
	for _, ev := range front.events {
		if e, ok := ev.(*agent.PromptEvent); ok && e.Prompt.Source == "shell" {
			t.Errorf("the command went to the model as a prompt of its own: %+v", e.Prompt)
		}
	}
	if got := strings.Join(during, " | "); got != "!echo hello from the person" {
		t.Errorf("the history after the command: %q", got)
	}

	// A later session of the folder.
	var later []string
	_, r = attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		later = c.History()
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
	if got := strings.Join(later, " | "); got != "!echo hello from the person | what did it say? |  !not a command" {
		t.Errorf("the history of a later session: %q", got)
	}
}

// A command's output that waits for the next prompt is in the session's
// file at once: a session opened again before that prompt still has it.
func TestShellOutputKeptAcrossARestart(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "It said hello."})
	w.Write("AGENTS.md", "The project's rule: the codeword is marmalade.\n")
	_, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		if err := c.Shell("echo kept for later"); err != nil {
			t.Errorf("Shell: %v", err)
			return 1
		}
		find(f, "the command and its output in the conversation", func(e *agent.NoteEvent) bool { return e.Note.Source == "shell" })
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
	run(t, w, "-c", "-p", "what did my command print?").wantCode(t, 0)
	if reqs := w.Model.Requests(); len(reqs) != 1 {
		t.Fatalf("the model was asked %d times, want once", len(reqs))
	}
	// The session's first prompt still carries what a first prompt does:
	// the command's output alone in the file did not count as one.
	has(t, "what the resumed session sent", strings.Join(userTexts(w.Model.Requests()[0]), "\n"), "$ echo kept for later", "what did my command print?", "the codeword is marmalade")
}

// A command of the person's that gives gnokey a password on its standard
// input is refused before anything keeps it: not the session's file, not
// the history Up brings back, not the model. (The shell tool review of
// 2026-11-19: the refusal came after all three had the password.)
func TestPersonsGnokeyPasswordIsNotKept(t *testing.T) {
	w := apptest.NewWorld(t)
	var shellErr error
	var history []string
	_, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		shellErr = c.Shell("echo hunter2 | gnokey maketx call -insecure-password-stdin key")
		history = c.History()
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
	if shellErr == nil || !strings.Contains(shellErr.Error(), "it was not run, and not kept") {
		t.Errorf("Shell: %v", shellErr)
	}
	for _, h := range history {
		if strings.Contains(h, "hunter2") {
			t.Errorf("the history holds the password: %q", h)
		}
	}
	for _, s := range w.Sessions() {
		if data, _ := os.ReadFile(s); strings.Contains(string(data), "hunter2") {
			t.Errorf("the session's file holds the password: %s", s)
		}
	}
	if n := len(w.Model.Requests()); n != 0 {
		t.Errorf("the model was sent %d requests", n)
	}
}

// Running multiple "!" commands in a row with default settings executes only
// the requested commands and never triggers an unrequested model turn (#4).
func TestRepeatedBangCommandsTriggerNoModelTurnByDefault(t *testing.T) {
	w := apptest.NewWorld(t)
	front, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		for _, cmd := range []string{"echo first-run", "echo second-run"} {
			if err := c.Shell(cmd); err != nil {
				t.Errorf("Shell(%q): %v", cmd, err)
				return 1
			}
			find(f, "recorded shell note for "+cmd, func(e *agent.NoteEvent) bool {
				return e.Note.Source == "shell" && strings.Contains(e.Note.Text, cmd)
			})
		}
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
	if n := len(w.Model.Requests()); n != 0 {
		t.Fatalf("the model was asked %d times after ! commands, want 0", n)
	}
	for _, ev := range front.events {
		if e, ok := ev.(*agent.NoteEvent); ok && e.Note.Source == "shell" {
			if strings.Contains(e.Note.Text, "go on with it") {
				t.Errorf("recorded shell note should not instruct the model to continue work: %q", e.Note.Text)
			}
		}
	}
}
