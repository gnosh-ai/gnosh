package ui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// screenText is what the terminal's screen shows now.
func (g *rig) screenText() string { return strings.Join(g.scr.Lines(), "\n") }

// viewHas fails the test unless the screen shows every piece.
func (g *rig) viewHas(pieces ...string) {
	g.t.Helper()
	screen := g.screenText()
	for _, p := range pieces {
		if !strings.Contains(screen, p) {
			g.t.Fatalf("the screen does not show %q:\n%s", p, screen)
		}
	}
}

// Ctrl+O, as Claude Code's: the whole conversation on the whole screen.
// Every thought, the ones the session folded too, each reply with its
// time and model, each call with its input and its result, long ones cut
// until Ctrl+E; Home, End, { and } move through it; q gives the screen
// back as it was, with what the session said meanwhile in the scrollback.
func TestTranscriptView(t *testing.T) {
	g := newRig(t, 80, 20, text.NoColor)
	g.free = true
	at := time.Date(2026, 11, 15, 14, 2, 11, 0, time.Local)
	var long strings.Builder
	for i := range 40 {
		long.WriteString("output line " + string(rune('A'+i%26)) + "\n")
	}
	g.c.mu.Lock()
	g.c.trans = []app.TranscriptEntry{
		{Kind: "notice", Time: at, Message: core.UserText("<gnosh>Where this session runs: here.</gnosh>")},
		{Kind: "prompt", Time: at, Message: core.UserText("fix the test")},
		{Kind: "reply", Time: at.Add(time.Second), Model: "qwen/qwen3.8-27b", Message: core.Message{Role: core.RoleAssistant, Blocks: []core.Block{
			{Type: core.BlockThinking, Text: "the first thought of the session\n\nand its second line"},
			core.Text("Running the tests."),
			core.ToolUse("c1", "Bash", json.RawMessage(`{"command":"go test ./..."}`)),
		}}},
		{Kind: "results", Time: at.Add(2 * time.Second), Message: core.Message{Role: core.RoleUser, Blocks: []core.Block{
			core.ToolResultBlock("c1", core.ToolResult{Content: []core.Block{core.Text(long.String())}}),
		}}},
		{Kind: "frame", Op: "push", Frame: "fix", Label: "make the test pass"},
		{Kind: "notice", Level: agent.LevelWarning, Text: "a background command ended"},
		{Kind: "prompt", Time: at.Add(time.Minute), Message: core.UserText("and the other one")},
	}
	g.c.mu.Unlock()
	g.keys(`"go" <Enter>`).start()
	g.keys(`<Ctrl+O>`)
	if g.s.view == nil || !g.scr.AltScreenEver() {
		t.Fatal("Ctrl+O did not open the transcript view")
	}
	g.viewHas("> and the other one", "⇥ step fix: make the test pass", "warning: a background command ended", "transcript · the session is working · q goes back")

	g.keys(`<Home>`)
	// What gnosh added is shown without its marker: the label says whose.
	g.viewHas("gnosh: Where this session runs: here.")
	if strings.Contains(g.screenText(), "<gnosh>") {
		t.Errorf("the view shows gnosh's marker:\n%s", g.screenText())
	}
	g.viewHas("> fix the test", "─ 14:02:12 · qwen/qwen3.8-27b ─", "~ the first thought of the session", "  and its second line",
		"Running the tests.", "▸ Bash", `"command": "go test ./..."`, "⎿ output line A")
	if strings.Contains(g.screenText(), "\n\n") {
		t.Errorf("the view has an empty row:\n%s", g.screenText())
	}
	g.keys(`<End>`)
	g.viewHas("… 16 more lines: Ctrl+E shows all")
	g.keys(`<Ctrl+E> <End>`)
	g.viewHas("output line N", "Ctrl+E cuts long parts")
	if strings.Contains(g.screenText(), "more lines") {
		t.Errorf("Ctrl+E left something cut:\n%s", g.screenText())
	}
	// { goes to the prompt before the top, } to the next one.
	g.keys(`"{"`)
	if top := g.s.view.p.Top(); top != g.s.view.prompts[0] {
		t.Errorf("{ from the end went to row %d, want the first prompt's", top)
	}
	g.keys(`"}"`)
	g.viewHas("> and the other one")

	// The session goes on meanwhile: what it says is kept for the
	// scrollback, and the thought that is arriving shows at the end.
	g.ev(thought("a thought still arriving\n"))
	g.wait(time.Second)
	g.viewHas("─ now ─", "~ a thought still arriving")
	g.ev(said("Both pass.\n"), replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted), idle())
	if strings.Contains(strings.Join(g.scr.Scrollback(), "\n"), "Both pass.") {
		t.Errorf("the answer was written while the view had the screen")
	}

	// q: the screen as it was, and the answer where it would have been.
	g.keys(`"q"`)
	if g.s.view != nil || g.scr.Mode(1000) {
		t.Fatal("q did not close the view")
	}
	all := strings.Join(g.all(), "\n")
	if !strings.Contains(all, "> go") || !strings.Contains(all, "Both pass.") || strings.Contains(all, "output line A") {
		t.Errorf("after the view the terminal shows:\n%s", all)
	}

	// [ writes the whole of it, nothing cut, into the scrollback, where
	// the terminal's own search reaches it.
	g.keys(`<Ctrl+O> "["`)
	if g.s.view != nil {
		t.Fatal("[ did not close the view")
	}
	all = strings.Join(g.all(), "\n")
	if !strings.Contains(all, "output line A") || !strings.Contains(all, "output line N") || !strings.Contains(all, "~ the first thought of the session") {
		t.Errorf("after [ the scrollback lacks the transcript:\n%s", all)
	}
	// v hands it to the person's editor, as plain text.
	var edited string
	g.editor = func(s string) (string, error) { edited = s; return s, nil }
	g.keys(`<Ctrl+O> "v"`)
	if g.s.view != nil || !strings.Contains(edited, "> fix the test") || !strings.Contains(edited, "output line N") || strings.Contains(edited, "\x1b") {
		t.Errorf("v gave the editor %q", edited)
	}

	// A question closes it, so that the person sees the question.
	g.keys(`<Ctrl+O>`)
	if g.s.view == nil {
		t.Fatal("Ctrl+O did not open the view again")
	}
	g.ev(&agent.QuestionEvent{Question: permission("p1")})
	if g.s.view != nil || g.s.q == nil {
		t.Fatalf("a question did not close the view")
	}
}

// Where the session hands out no transcript, Ctrl+O says so.
func TestTranscriptViewNeeds(t *testing.T) {
	g := newRig(t, 80, 24, text.NoColor)
	g.s.c = struct{ app.Controls }{g.c}
	g.keys(`<Ctrl+O>`)
	if g.s.view != nil {
		t.Fatal("the view opened with nothing to read")
	}
	g.liveHas("not joined to this build")
}

// v in the transcript view with the side panel on gives the editor the
// screen: the panel steps aside while the editor runs, and comes back
// after, as it does for Ctrl+G. The editor ran under the panel (the TUI
// review of 2026-11-19).
func TestTranscriptInEditorWithThePanel(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor)
	g.free = true
	g.c.mu.Lock()
	g.c.trans = []app.TranscriptEntry{{Kind: "prompt", Message: core.UserText("fix the test")}}
	g.c.mu.Unlock()
	g.keys(`"/panel" <Enter>`)
	if g.s.panel == nil {
		t.Fatal("/panel did not open the panel")
	}
	panelDuringEdit := true
	g.editor = func(s string) (string, error) { panelDuringEdit = g.s.panel != nil; return s, nil }
	g.keys(`<Ctrl+O> "v"`)
	if panelDuringEdit {
		t.Error("the editor ran with the side panel on the screen")
	}
	if g.s.panel == nil {
		t.Error("the side panel did not come back after the editor")
	}
}

// The keys of every state work in the transcript view too: Ctrl+Z stops
// gnosh and the view is there after, Ctrl+L draws it again. They did
// nothing there (the TUI review of 2026-11-19).
func TestEveryStateKeysInTheTranscriptView(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor)
	g.free = true
	g.c.mu.Lock()
	g.c.trans = []app.TranscriptEntry{{Kind: "prompt", Message: core.UserText("fix the test")}}
	g.c.mu.Unlock()
	g.keys(`<Ctrl+O>`)
	if g.s.view == nil {
		t.Fatal("Ctrl+O did not open the view")
	}
	g.keys(`<Ctrl+Z>`)
	if g.suspends != 1 {
		t.Errorf("Ctrl+Z in the view stopped gnosh %d times", g.suspends)
	}
	if g.s.view == nil {
		t.Fatal("the view was gone after Ctrl+Z")
	}
	g.keys(`<Ctrl+L>`)
	if g.s.view == nil || !strings.Contains(strings.Join(g.scr.Lines(), "\n"), "fix the test") {
		t.Errorf("after Ctrl+L the view shows:\n%s", strings.Join(g.scr.Lines(), "\n"))
	}
}

// A "!" command's entry in the conversation carries gnosh's internal
// instruction for the model and the shell tool's host stamp around the
// command and its output. The transcript view (Ctrl+O / Ctrl+E) shows the
// command and its output without exposing the internal instruction or tags.
func TestTranscriptViewPersonsShellOutput(t *testing.T) {
	g := newRig(t, 80, 20, text.NoColor)
	g.free = true
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.Local)
	var out strings.Builder
	for i := range 30 {
		out.WriteString("help line " + string(rune('A'+i%26)) + "\n")
	}
	shellText := "<gnosh>\nThe user ran this shell command themselves, in this session's working directory, and this is what it printed. It was their own command, not a request to run it again: answer what it shows, in a few words, and if you were in the middle of something for them, go on with it.\n</gnosh>\n\n$ gnosh help\n<host>local (user@box)</host><cwd>/work</cwd>\n" + out.String() + "\n<gnosh>\nThe user stopped it before it finished.\n</gnosh>"
	g.c.mu.Lock()
	g.c.trans = []app.TranscriptEntry{
		{Kind: "notice", Time: at, Message: core.UserText(shellText)},
	}
	g.c.mu.Unlock()
	g.keys(`<Ctrl+O> <Home>`)
	if g.s.view == nil {
		t.Fatal("Ctrl+O did not open the transcript view")
	}
	g.viewHas("$ gnosh help", "help line A")
	g.keys(`<Ctrl+E> <Home>`)
	screen := g.screenText()
	for _, forbidden := range []string{"<gnosh>", "</gnosh>", "The user ran this shell command themselves", "The user stopped it before it finished", "<host>"} {
		if strings.Contains(screen, forbidden) {
			t.Errorf("expanded transcript view exposed %q:\n%s", forbidden, screen)
		}
	}
	g.keys(`<End>`)
	g.viewHas("help line D")
}
