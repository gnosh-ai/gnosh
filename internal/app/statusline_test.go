package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// A status line command named in the settings runs when the session opens
// and again after a reply, with the session's facts as JSON on its
// standard input and COLUMNS set to the terminal's width less the padding.
func TestStatusLineCommand(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hello."})
	runs := w.Home + "/sl-runs"
	script := w.Script(w.Home+"/bin/sl.sh", "{ cat; echo; echo \"cols=$COLUMNS\"; } >> "+runs+"\necho \"status of the session\"")
	w.Settings(`{"statusLine": {"type": "command", "command": "` + script + `", "padding": 1}}`)

	// inputs returns the JSON objects the command was given so far.
	inputs := func() []map[string]any {
		data, _ := os.ReadFile(runs)
		var out []map[string]any
		for _, line := range strings.Split(string(data), "\n") {
			var obj map[string]any
			if json.Unmarshal([]byte(line), &obj) == nil {
				out = append(out, obj)
			}
		}
		return out
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); !ok(); {
			if time.Now().After(deadline) {
				data, _ := os.ReadFile(runs)
				t.Fatalf("%s never happened; the command was given:\n%s", what, data)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	s := sit(t, w)
	s.see("Type a prompt and Enter.")
	waitFor("the run at the session's start", func() bool { return len(inputs()) >= 1 })
	first := inputs()[0]
	if first["session_id"] == "" || !strings.HasSuffix(first["transcript_path"].(string), ".jsonl") || first["cwd"] != w.Dir {
		t.Errorf("the first run was given %v", first)
	}
	if m, _ := first["model"].(map[string]any); m["id"] != "local/test-model" {
		t.Errorf("the model it was given: %v", first["model"])
	}
	if ws, _ := first["workspace"].(map[string]any); ws["current_dir"] != w.Dir || ws["project_dir"] != w.Dir {
		t.Errorf("the workspace it was given: %v", first["workspace"])
	}
	// (The script writes its input, then the width: the file is read until
	// both are there, and the width must be a number.)
	waitFor("COLUMNS set to a number", func() bool {
		data, _ := os.ReadFile(runs)
		return regexp.MustCompile(`cols=[0-9]+`).Match(data)
	})

	before := len(inputs())
	s.typeLine("say hello")
	s.see("Hello.")
	waitFor("a run after the reply", func() bool { return len(inputs()) > before })
	last := inputs()[len(inputs())-1]
	cw, _ := last["context_window"].(map[string]any)
	if cw == nil || cw["current_usage"] == nil {
		t.Errorf("after a reply, the context window it was given: %v", last["context_window"])
	}
	s.typeLine("/exit")
	s.exit()
}

// With a refreshInterval the command runs again on its own, with nothing
// else happening.
func TestStatusLineRefresh(t *testing.T) {
	w := apptest.NewWorld(t)
	runs := w.Home + "/sl-runs"
	script := w.Script(w.Home+"/bin/sl.sh", "cat > /dev/null; echo run >> "+runs+"\necho line")
	w.Settings(`{"statusLine": {"type": "command", "command": "` + script + `", "refreshInterval": 1}}`)
	s := sit(t, w)
	s.see("Type a prompt and Enter.")
	count := func() int {
		data, _ := os.ReadFile(runs)
		return strings.Count(string(data), "run")
	}
	for deadline := time.Now().Add(10 * time.Second); count() < 3 && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if n := count(); n < 3 {
		t.Errorf("the command ran %d times in a session that did nothing, want at least 3 (the start and two refreshes)", n)
	}
	s.typeLine("/exit")
	s.exit()
}

// /statusline asks the model, in a turn, to write a status line script
// from the person's words, try it on this session's own JSON, and add the
// setting to their gnosh.json.
func TestStatusLineCommandAsksTheModel(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Written."})
	r := run(t, w, "-p", "/statusline the branch and the model").wantCode(t, 0)
	has(t, "the run", r.out, "Written.")
	has(t, "what the model was asked", requestText(t, w, 0),
		"It is to show: the branch and the model", "statusline.sh", "gnosh.json", `\"session_id\"`, `\"workspace\"`)
	w.Model.Done()
}

// In a --worktree session the status line command is told of the work
// tree, as Claude Code's is: its name, folder and branch, and where the
// session was started.
func TestStatusLineWorktree(t *testing.T) {
	w := repoWorld(t)
	runs := w.Home + "/sl-runs"
	script := w.Script(w.Home+"/bin/sl.sh", "{ cat; echo; } >> "+runs+"\necho status")
	w.Settings(`{"statusLine": {"type": "command", "command": "` + script + `"}}`)
	// The repository's origin, read from its own config through the work
	// tree's .git file.
	f, err := os.OpenFile(w.Dir+"/.git/config", os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(f, "[remote \"origin\"]\n\turl = git@github.com:gnosh-ai/gnosh.git\n")
	f.Close()
	s := sit(t, w, "--trust-folder", "--worktree", "feat")
	s.see("Type a prompt and Enter.")
	var got map[string]any
	for deadline := time.Now().Add(15 * time.Second); got == nil; {
		data, _ := os.ReadFile(runs)
		for _, line := range strings.Split(string(data), "\n") {
			if json.Unmarshal([]byte(line), &got) == nil {
				break
			}
			got = nil
		}
		if got == nil && time.Now().After(deadline) {
			t.Fatalf("the status line command never ran: %s", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	tree := w.Dir + "/.gnosh/worktrees/feat"
	wt, _ := got["worktree"].(map[string]any)
	if wt["name"] != "feat" || wt["path"] != tree || wt["branch"] != "worktree-feat" || wt["original_cwd"] != w.Dir || wt["original_branch"] != "main" {
		t.Errorf("the worktree it was given: %v", got["worktree"])
	}
	if ws, _ := got["workspace"].(map[string]any); ws["git_worktree"] != "feat" || ws["current_dir"] != tree {
		t.Errorf("the workspace it was given: %v", got["workspace"])
	}
	if ws, _ := got["workspace"].(map[string]any); ws != nil {
		if repo, _ := ws["repo"].(map[string]any); repo["host"] != "github.com" || repo["owner"] != "gnosh-ai" || repo["name"] != "gnosh" {
			t.Errorf("the repo it was given: %v", ws["repo"])
		}
	}
	s.typeLine("/exit")
	s.exit()
}

// The front end says the mode of the box's vim keys; the status line
// command runs again and is told it as vim.mode, and with the keys off it
// is told nothing of vim, as Claude Code's is.
func TestStatusLineVimMode(t *testing.T) {
	w := apptest.NewWorld(t)
	runs := w.Home + "/sl-runs"
	script := w.Script(w.Home+"/bin/sl.sh", "{ cat; echo; } >> "+runs+"\necho line")
	w.Settings(`{"statusLine": {"type": "command", "command": "` + script + `"}}`)
	last := func() map[string]any {
		data, _ := os.ReadFile(runs)
		var obj map[string]any
		for _, line := range strings.Split(string(data), "\n") {
			var o map[string]any
			if json.Unmarshal([]byte(line), &o) == nil {
				obj = o
			}
		}
		return obj
	}
	vimOf := func(obj map[string]any) any {
		if v, ok := obj["vim"].(map[string]any); ok {
			return v["mode"]
		}
		return nil
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(15 * time.Second); !ok(); {
			if time.Now().After(deadline) {
				data, _ := os.ReadFile(runs)
				t.Fatalf("%s never happened; the command was given:\n%s", what, data)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	_, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		waitFor("the run at the start", func() bool { return last() != nil })
		if v := vimOf(last()); v != nil {
			t.Errorf("with vim's keys off the command was told vim.mode %v", v)
		}
		teller, ok := c.(interface{ SetVimMode(string) })
		if !ok {
			t.Error("the session's controls take no vim mode")
			c.Quit("prompt_input_exit")
			return 1
		}
		teller.SetVimMode("NORMAL")
		waitFor("a run told NORMAL", func() bool { return vimOf(last()) == "NORMAL" })
		teller.SetVimMode("")
		waitFor("a run told nothing of vim", func() bool { o := last(); return o != nil && vimOf(o) == nil })
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
}

// The status line is told the kind of agent the session runs as, and the
// thinking level of a model that has levels beyond on and off, as Claude
// Code's agent.name and effort.level.
func TestStatusLineAgentAndEffort(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Looked."})
	runs := w.Home + "/sl-runs"
	script := w.Script(w.Home+"/bin/sl.sh", "{ cat; echo; } >> "+runs+"\necho status")
	w.Settings(`{"models": {"local/test-model": {"thinkingLevels": ["low", "high"]}}, "statusLine": {"type": "command", "command": "` + script + `"}}`)
	s := sit(t, w, "--agent", "Explore", "--effort", "high")
	s.see("Type a prompt and Enter.")
	// (The model, with its levels, is settled by the first request.)
	s.typeLine("look around")
	s.see("Looked.")
	var got map[string]any
	for deadline := time.Now().Add(15 * time.Second); got == nil; {
		data, _ := os.ReadFile(runs)
		for _, line := range strings.Split(string(data), "\n") {
			var in map[string]any
			if json.Unmarshal([]byte(line), &in) == nil && in["effort"] != nil {
				got = in
			}
		}
		if got == nil && time.Now().After(deadline) {
			t.Fatalf("the status line command was never told the effort: %s", data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e, _ := got["effort"].(map[string]any); e["level"] != "high" {
		t.Errorf("the effort it was given: %v", got["effort"])
	}
	if a, _ := got["agent"].(map[string]any); a["name"] != "Explore" {
		t.Errorf("the agent it was given: %v", got["agent"])
	}
	if id, _ := got["prompt_id"].(string); id == "" {
		t.Errorf("no prompt_id after a prompt: %v", got)
	}
	s.typeLine("/exit")
	s.exit()
}
