package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// /config lists gnosh's options with what set each, shows one, and sets
// them in the user's state file, never in gnosh.json; a project's files
// still win, and /config says so.
func TestConfigCommand(t *testing.T) {
	w := apptest.NewWorld(t)
	w.Settings(`{"options": {"memoryText": "full"}}`)
	state := filepath.Join(w.DataDir(), "state.json")
	settings, _ := os.ReadFile(w.SettingsFile())

	has(t, "/config", run(t, w, "-p", "/config").wantCode(t, 0).out,
		"gnosh's options in this session (/config key says more, /config key=value sets one):",
		"memoryText", "full", "~/.config/gnosh/gnosh.json:", "maxBackgroundTasks", "50", "the default",
		"What /config sets is kept in ~/.local/share/gnosh/state.json: above your gnosh.json, below a project's files.")
	has(t, "/config memoryText", run(t, w, "-p", "/config memoryText").wantCode(t, 0).out,
		"memoryText: which of gnosh's two texts about mem the model is given",
		"It takes one of lean, full; when nothing sets it, it is lean.", "Now: full (~/.config/gnosh/gnosh.json:")

	// Two at once, kept in the state file: debug is in force at once, as
	// a running session can change it; the other from the next start.
	has(t, "/config debug=true maxBackgroundTasks=7", run(t, w, "-p", "/config debug=true maxBackgroundTasks='7'").wantCode(t, 0).out,
		"debug is true from now on, in this session and later ones, in every project whose own files do not set it.",
		"maxBackgroundTasks is 7 when gnosh next starts",
		"(Kept in ~/.local/share/gnosh/state.json, never in your gnosh.json.)")
	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	has(t, "the state file", string(data), `"debug": true`, `"maxBackgroundTasks": 7`)
	if now, _ := os.ReadFile(w.SettingsFile()); !reflect.DeepEqual(now, settings) {
		t.Errorf("/config wrote gnosh.json:\n%s", now)
	}
	// The next start has them, from /config.
	out := run(t, w, "-p", "/config").wantCode(t, 0).out
	has(t, "/config after the change", out, "debug", "true", "/config")
	hasNot(t, "/config after the change", out, "when gnosh next starts")

	// A mistake saves nothing, not even the pairs before it.
	r := run(t, w, "-p", "/config memoryText=lean debug=yes").wantCode(t, 1)
	has(t, "/config debug=yes", r.out+r.err, `debug is true or false, and "yes" is neither`)
	if again, _ := os.ReadFile(state); string(again) != string(data) {
		t.Errorf("a refused /config changed the state file:\n%s", again)
	}
	has(t, "/config hiddenSkills=x", run(t, w, "-p", "/config hiddenSkills=x").wantCode(t, 1).err, "hiddenSkills holds a list, and a list is set in gnosh.json")
	has(t, "/config fancy", run(t, w, "-p", "/config fancy").wantCode(t, 1).err, `"fancy" is not an option gnosh knows`)

	// A project's own file outranks what /config set.
	w.Write("gnosh.json", `{"options": {"debug": false}}`)
	out = run(t, w, "--trust-folder", "-p", "/config debug").wantCode(t, 0).out
	has(t, "/config debug under a project's file", out, "Now: false (", "proj/gnosh.json", "(set with /config to true; what sets it here outranks that)")
	has(t, "/config debug=true under a project's file", run(t, w, "--trust-folder", "-p", "/config debug=true").wantCode(t, 0).out,
		"Here ", "proj/gnosh.json", " sets it to false, and that wins.")
	if err := os.Remove(w.Path("gnosh.json")); err != nil {
		t.Fatal(err)
	}

	// key= takes the choice away; a second time there is nothing to take.
	has(t, "/config debug=", run(t, w, "-p", "/config debug=").wantCode(t, 0).out,
		"debug is no longer set with /config: when gnosh next starts the config files decide (in this session it is true, from /config).")
	has(t, "/config debug= again", run(t, w, "-p", "/config debug=").wantCode(t, 0).out, "debug was not set with /config, so nothing changed.")
	has(t, "/config after taking debug away", run(t, w, "-p", "/config debug").wantCode(t, 0).out, "Now: false (the default).")
}

// What a running session can change is in force at once: the listing
// says so, with no word of waiting for the next start.
func TestConfigLiveOptions(t *testing.T) {
	w := apptest.NewWorld(t)
	s := sit(t, w)
	s.see("Type a prompt and Enter.")
	s.typeLine("/config autoCompactEnabled=false")
	s.see("autoCompactEnabled is false from now on, in this session and later ones")
	s.typeLine("/config autoCompactEnabled")
	s.see("Now: false (/config).")
	s.typeLine("/config memoryText=full")
	s.see("memoryText is full when gnosh next starts")
	s.typeLine("/config autoCompactEnabled=")
	s.see("autoCompactEnabled is no longer set with /config")
	// /autocompact is the autoCompactWindow option, as Claude Code's.
	s.typeLine("/autocompact 200,000")
	s.see("autoCompactWindow is 200000 from now on")
	s.typeLine("/autocompact")
	s.see("Now: 200000 (/config).")
	s.typeLine("/autocompact auto")
	s.see("autoCompactWindow is no longer set with /config")
	s.typeLine("/autocompact lots")
	s.see("/autocompact takes auto, or a number of tokens")
	s.typeLine("/exit")
	if code := s.exit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// showTurnDuration and respectGitignore reach the interface by the
// session's info, and /config changes them in a running session: the
// interface reads the info again after each command.
func TestInterfaceOptionsAreLive(t *testing.T) {
	w := apptest.NewWorld(t)
	w.Settings(`{"options": {"showTurnDuration": false, "respectGitignore": false}}`)
	_, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		if info := c.Info(); !info.HideTurnDuration || !info.ListIgnored {
			t.Errorf("showTurnDuration and respectGitignore false do not reach the interface: %+v", info)
		}
		if _, err := c.Command(ctx, "/config showTurnDuration=true respectGitignore=true"); err != nil {
			t.Errorf("/config showTurnDuration=true respectGitignore=true: %v", err)
		}
		if info := c.Info(); info.HideTurnDuration || info.ListIgnored {
			t.Errorf("/config set them true, and the interface still has them off: %+v", info)
		}
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
}

// Claude Code's language and includeGitInstructions: the language the
// person set is asked for in the system prompt, and with
// includeGitInstructions false the block ahead of a session's first
// prompt says nothing of git.
func TestLanguageAndGitInstructions(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Konnichiwa."}, apptest.Reply{Text: "Hai."})
	w.Settings(`{"options": {"language": "japanese"}}`)
	run(t, w, "-p", "hello").wantCode(t, 0)
	first := requestText(t, w, 0)
	has(t, "the first session's request", first, "Reply to the user in japanese, whatever language their messages are in", "- Git: ")
	w.Settings(`{"options": {"includeGitInstructions": false}}`)
	run(t, w, "-p", "hello again").wantCode(t, 0)
	second := requestText(t, w, 1)
	has(t, "the second session's request", second, "Reply to the user in japanese", "Working directory: ")
	hasNot(t, "the second session's request", second, "- Git: ")
}

// bashOutputMaxChars, Claude Code's settings key, bounds the part of a
// command's output the model reads; BASH_MAX_OUTPUT_LENGTH, when set,
// wins, as the environment outranks the files.
func TestBashOutputMaxChars(t *testing.T) {
	count := apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"seq 1 3000"}`}}}
	w := apptest.NewWorld(t, count, apptest.Reply{Text: "Counted."}, count, apptest.Reply{Text: "Counted again."})
	w.Settings(`{"options": {"bashOutputMaxChars": 2000}}`)
	run(t, w, "-y", "-p", "count").wantCode(t, 0)
	// seq 1 3000 prints 13,893 characters: the start and the end of them
	// are shown, 2,000 in all.
	cut := toolResult(t, w, 1, "c1")
	has(t, "the cut output", cut, "1\n2\n3\n", "3000")
	if len(cut) > 4000 {
		t.Errorf("the model read %d characters of the output, with bashOutputMaxChars 2000", len(cut))
	}
	o := testOptions(w, "-y", "-p", "count again")
	o.Environ = append(o.Environ, "BASH_MAX_OUTPUT_LENGTH=20000")
	runWith(t, t.Context(), o).wantCode(t, 0)
	if whole := toolResult(t, w, 3, "c1"); !strings.Contains(whole, "\n1500\n") {
		t.Errorf("with BASH_MAX_OUTPUT_LENGTH 20000 the middle of the output was still cut")
	}
}

// attribution, Claude Code's: the lines a settings file sets are asked
// for in the system prompt; with none set, gnosh says nothing of signing,
// as it signs with nothing of its own.
func TestAttributionInTheSystemPrompt(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "ok"}, apptest.Reply{Text: "ok"})
	run(t, w, "-p", "hi").wantCode(t, 0)
	hasNot(t, "a request with no attribution set", requestText(t, w, 0), "end its message with these lines", "end its description with")
	w.Settings(`{"attribution": {"commit": "Co-Authored-By: gnosh <bot@example.com>"}}`)
	run(t, w, "-p", "hi").wantCode(t, 0)
	second := requestText(t, w, 1)
	has(t, "a request with the commit's attribution set", second, "When you make a git commit, end its message with these lines, as they are:", "Co-Authored-By: gnosh ", "theirs come first")
	hasNot(t, "a request with the commit's attribution set", second, "When you open a pull request")
}
