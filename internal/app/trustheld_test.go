package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// The trust question shows what a Yes would turn on beside the folder's
// settings files: the servers of its .mcp.json, and a skill that lets tools
// run without asking. It showed only the settings files', so a Yes to one
// harmless option started a server it never named; and a folder with
// nothing but such files was never asked (the config review of
// 2026-11-19). A No starts none of it.
func TestTrustQuestionShowsWhatAYesTurnsOn(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hello."})
	w.Write("gnosh.json", `{"options": {"language": "english"}}`+"\n")
	w.Write(".mcp.json", `{"mcpServers": {"helper": {"command": "sh", "args": ["-c", "touch ran.txt"]}}}`+"\n")
	w.Write(".claude/skills/deploy/SKILL.md", "---\nname: deploy\ndescription: Deploys the site.\nallowed-tools: Bash\n---\nDeploy it.\n")
	s := sit(t, w)
	s.see("? Trust this folder?")
	s.seeAnywhere("the MCP server helper, run on this machine as sh -c touch ran.txt")
	s.seeAnywhere("the skill deploy lets these run without asking while it is used: Bash")
	s.see("  1. No")
	s.typeLine("1")
	s.see("Type a prompt and Enter.")
	s.typeLine("hi")
	s.see("Hello.")
	s.typeLine("/exit")
	s.exit()
	if w.Exists("ran.txt") {
		t.Error("the folder's MCP server was started after a No")
	}

	// A folder whose only own content is such a file is asked about too.
	w = apptest.NewWorld(t)
	w.Write(".mcp.json", `{"mcpServers": {"online": {"type": "http", "url": "https://mcp.example.com/x"}}}`+"\n")
	s = sit(t, w)
	s.see("? Trust this folder?")
	s.seeAnywhere("the MCP server online at https://mcp.example.com/x")
	s.typeLine("1")
	s.see("Type a prompt and Enter.")
	s.typeLine("/exit")
	s.exit()
}

// In a folder that is not trusted, whose repository's own git settings name
// a program, the person's status line command is not run: it may call git,
// and git would run that program (gnosh's own git status is held there for
// that). A notice says why (the config review of 2026-11-19).
func TestStatusLineHeldWhereGitRunsTheRepositorysPrograms(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hello."})
	gitIn(t, w.Dir, "init", "-q")
	gitIn(t, w.Dir, "config", "core.fsmonitor", "touch fsmonitor-ran")
	ran := w.Home + "/sl-ran"
	script := w.Script(w.Home+"/bin/sl.sh", "touch "+ran+"\necho status")
	w.Settings(`{"statusLine": {"type": "command", "command": "` + script + `"}}`)
	s := sit(t, w)
	s.see("? Trust this folder?")
	s.typeLine("1")
	s.seeAnywhere("Your status line command is not run in this folder: the repository's own git settings name programs")
	s.typeLine("hi")
	s.see("Hello.")
	s.typeLine("/exit")
	s.exit()
	if _, err := os.Stat(ran); err == nil {
		t.Error("the status line command ran")
	}
}

// A variable the person's env setting sets to nothing is taken away from
// every command gnosh starts, not only from Bash and hooks: the status
// line and a skill's own commands were handed the one gnosh inherited (the
// config review of 2026-11-19).
func TestEnvTakenAwayFromEveryCommand(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Done."})
	seen := w.Home + "/sl-saw"
	script := w.Script(w.Home+"/bin/sl.sh", "printf '%s\\n' \"${SECRET-unset}\" >> "+seen+"\necho status")
	w.Settings(`{"env": {"SECRET": ""}, "statusLine": {"type": "command", "command": "` + script + `"}}`)
	w.Write(w.ConfigDir()+"/skills/peek/SKILL.md", "---\ndescription: Peeks.\n---\nIt holds: !`printf '%s' \"${SECRET-unset}\"`\n")
	s := sitWith(t, w, func(o *Options) { o.Environ = append(o.Environ, "SECRET=shh") }, "-y")
	s.see("Type a prompt and Enter.")
	s.typeLine("/peek")
	s.see("Done.")
	// (The status line runs a moment after the reply, debounced.)
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if data, _ := os.ReadFile(seen); len(data) > 0 {
			break
		}
	}
	s.typeLine("/exit")
	s.exit()
	data, _ := os.ReadFile(seen)
	if !strings.Contains(string(data), "unset") || strings.Contains(string(data), "shh") {
		t.Errorf("the status line saw %q", data)
	}
	sent := string(w.Model.Requests()[0].Body)
	if !strings.Contains(sent, "It holds: unset") || strings.Contains(sent, "shh") {
		t.Errorf("the skill's command saw the secret, or nothing: %s", sent)
	}
}
