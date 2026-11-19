package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// widths are the terminal widths every case is tried at.
var widths = []int{40, 80, 120}

// each runs a case at every width.
func each(t *testing.T, f func(t *testing.T, cols int)) {
	t.Helper()
	for _, cols := range widths {
		t.Run(fmt.Sprint(cols), func(t *testing.T) { f(t, cols) })
	}
}

// show prints the terminal when GNOSH_UI_SHOW is set, for looking at a
// state by eye.
func (g *rig) show(what string) *rig {
	if os.Getenv("GNOSH_UI_SHOW") != "" {
		g.t.Logf("%s:\n%s", what, g.scr.Dump())
	}
	return g
}

// A session at rest: the header once, then a prompt box and a status line
// and nothing else, with no empty row between them.
func TestIdleScreen(t *testing.T) {
	each(t, func(t *testing.T, cols int) {
		g := newRig(t, cols, 24, text.NoColor)
		g.committedHas("GNOSH test-build", "qwen3.8-27b", "~/src/proj", "https://gnosh.ai · built on gno.land (https://gno.land) · /license")
		g.liveHas("> ", "idle", "manual mode")
		if cols >= 80 {
			g.liveHas("qwen3.8-27b", "thinking on", "context 11%", "~/src/proj (main)")
		}
		if n := len(g.live); n != 2 {
			t.Errorf("the live region of an idle session has %d rows, want 2: the box and the status line\n%s", n, g.dump())
		}
		g.show("idle")
	})
}

// A prompt is typed and sent; the answer streams in pieces as Markdown and
// every line of it reaches the scrollback exactly once, in order.
func TestPlainAnswer(t *testing.T) {
	answer := "# Plan\n\nFirst **look** at the `test`, then:\n\n- read the file\n- fix the *bug*\n\n" +
		"```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```\n\n" +
		"| name | value |\n|---|---|\n| a | 1 |\n| b | 2 |\n\n" +
		"> quoted words\n\n1. one\n2. two\n   - nested under two\n\n- [x] ticked\n- [ ] open\n\n" +
		"Some *leaning* and ~~struck~~ words.<br>After a break.\n\n---\n\n<details>raw html</details>\n\n" +
		"See [the docs](https://example.com/docs) and ![a picture](x.png).\n\nDone at last.\n"
	each(t, func(t *testing.T, cols int) {
		for _, piece := range []int{1, 7, 64, len(answer)} {
			g := newRig(t, cols, 30, text.NoColor)
			g.keys(`"fix the flaky test" <Enter>`)
			if got := g.c.sent(); len(got) != 1 || got[0] != "fix the flaky test" {
				t.Fatalf("submitted %q", got)
			}
			g.liveHas("queued: fix the flaky test")
			g.start()
			g.once("> fix the flaky test")
			g.liveLacks("queued:")
			if cols >= 80 {
				g.liveHas("* waiting for qwen3.8-27b to start its reply")
			} else {
				g.liveHas("* waiting for the model")
			}
			for i := 0; i < len(answer); i += piece {
				g.ev(said(answer[i:min(i+piece, len(answer))]))
			}
			g.liveHas("qwen3.8-27b is writing")
			g.ev(replyEnd(core.StopEndTurn, core.Text(answer)), turnEnd(agent.EndCompleted), idle())
			g.once("# Plan", "First look at the `test`, then:", "println(\"hi\")", "quoted words", "Done at last.",
				"done in 42s", "2 replies", "1.1k written")
			g.committedHas("read the file", "fix the bug", "example.com/docs", "one", "two", "nested under two",
				"ticked", "open", "leaning", "struck", "After a break.", "raw html", "a picture")
			g.liveHas("idle").liveLacks("is writing", "Done at last")
			// The order of the answer's parts is the order they were written in.
			at := -1
			for _, part := range []string{"> fix the flaky test", "# Plan", "read the file", "println", "quoted words", "Done at last.", "done in 42s"} {
				i := strings.Index(g.scroll(), part)
				if i <= at {
					t.Fatalf("%q is out of order in the scrollback\n%s", part, g.dump())
				}
				at = i
			}
			if piece == 7 {
				g.show("after a plain answer")
			}
		}
	})
}

// However the answer is cut into pieces, the scrollback ends up the same.
func TestStreamingIsStable(t *testing.T) {
	answer := "Here is what I found.\n\nThe *first* thing is a paragraph that is long enough to be wrapped onto several rows of a narrow terminal, so that its lines become final one after the other.\n\n```\ncode line one\ncode line two\n```\n\nAnd a closing line.\n"
	var want string
	for i, piece := range []int{len(answer), 1, 3, 11, 50} {
		g := newRig(t, 60, 24, text.NoColor)
		g.keys(`"go" <Enter>`).start()
		for j := 0; j < len(answer); j += piece {
			g.ev(said(answer[j:min(j+piece, len(answer))]))
		}
		g.ev(replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted), idle())
		if i == 0 {
			want = g.scroll()
			continue
		}
		if got := g.scroll(); got != want {
			t.Errorf("in pieces of %d the scrollback is\n%s\nand in one piece it is\n%s", piece, got, want)
		}
	}
}

// Typing goes on while a long answer streams: the box holds what was
// typed, the cursor stays in it, and nothing of the answer is lost.
func TestTypingWhileStreaming(t *testing.T) {
	each(t, func(t *testing.T, cols int) {
		g := newRig(t, cols, 24, text.NoColor)
		g.keys(`"first" <Enter>`).start()
		typed := "and then also check the other package"
		for i := 0; i < 400; i++ {
			g.ev(said(fmt.Sprintf("Line %d of a long answer that keeps coming. ", i)))
			if i%8 == 0 && i/8 < len(typed) {
				g.keys(fmt.Sprintf("%q", typed[i/8:i/8+1]))
			}
			if i%50 == 49 {
				g.ev(said("\n\n"))
			}
		}
		if g.box() != typed {
			t.Fatalf("the box holds %q, want %q", g.box(), typed)
		}
		g.liveHas(typed[:min(len(typed), cols-4)])
		g.keys(`<Enter>`)
		if got := g.c.sent(); len(got) != 2 || got[1] != typed {
			t.Fatalf("submitted %q", got)
		}
		g.ev(queued(typed))
		g.liveHas("queued: " + typed[:min(len(typed), cols-12)])
		g.ev(replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted))
		g.once("Line 0 of a long answer", "Line 399 of a long answer")
		if g.most > liveBound(24) {
			t.Errorf("the live region grew to %d rows", g.most)
		}
	})
}

// The line above the box says what gnosh is doing at every moment of a
// turn, and there is never more than one of it.
func TestActivityLine(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor)
	one := func(words string) {
		t.Helper()
		g.liveHas(words)
		if n := strings.Count(g.region(), "* "); n != 1 {
			t.Fatalf("the live region has %d lines that say what gnosh is doing, want one\n%s", n, g.dump())
		}
	}
	g.liveLacks("* ")
	g.keys(`"go" <Enter>`)
	g.c.state = agent.State{Busy: true, Phase: agent.PhaseStarting, Waiting: "taking the session's turn"}
	g.ev(busy())
	one("* taking the session's turn")
	g.ev(turnStart(), promptOut("p1", "go"), request(1))
	one("* waiting for qwen3.8-27b to start its reply")
	g.liveHas("about 14k tokens to read", "Esc stops")
	g.wait(75 * time.Second)
	one("waiting for qwen3.8-27b to start its reply · 1m 15s")
	// What the model server says it is doing stands in the estimate's
	// place (app.ServerLoadEvent).
	g.ev(&app.ServerLoadEvent{Text: "reading the prompt: 8.1k of 14k tokens"})
	g.liveHas("reading the prompt: 8.1k of 14k tokens")
	g.liveLacks("about 14k tokens to read")
	g.ev(thought("Let me think about this."))
	one("* qwen3.8-27b is thinking")
	g.liveHas("~ thinking", "Let me think about this.")
	g.wait(12 * time.Second)
	one("qwen3.8-27b is thinking · 12s")
	// A thought that has gone on for fifteen seconds is a deep one.
	g.wait(2 * time.Second)
	one("qwen3.8-27b is thinking · 14s")
	g.wait(time.Second)
	one("* qwen3.8-27b is deep in thought · 15s")
	g.ev(said("Here it is."))
	one("* qwen3.8-27b is writing")
	g.once("~ thought for 15s")
	g.ev(toolCall("c1", "Bash"), toolInput("c1", `{"command":"go test`))
	one("* qwen3.8-27b is writing a Bash call")
	g.liveHas("$ go test")
	g.ev(toolInput("c1", ` ./..."}`), replyEnd(core.StopToolUse), toolStart("c1", "Bash", map[string]any{"command": "go test ./..."}))
	one("* running go test ./...")
	g.wait(3 * time.Second)
	one("running go test ./... · 3s")
	g.ev(toolEnd("c1", "Bash", map[string]any{"command": "go test ./..."},
		core.ToolResult{Content: []core.Block{core.Text("ok\n")}, Meta: map[string]any{"exitCode": 0}}, 3*time.Second))
	g.ev(request(2), said("All good."), replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted), idle())
	g.liveLacks("* ").liveHas("idle")
}

// While hooks with a status message run, the line above the box says what
// they say they do, and goes back to its own words after.
func TestActivityLineHookStatus(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor)
	g.keys(`"go" <Enter>`)
	g.c.state = agent.State{Busy: true, Phase: agent.PhaseStarting, Waiting: "taking the session's turn"}
	g.ev(busy())
	g.liveHas("* taking the session's turn")
	g.ev(&app.HookStatusEvent{Event: hooks.UserPromptSubmit, Status: "Checking the prompt"})
	g.liveHas("* Checking the prompt")
	g.liveLacks("taking the session's turn")
	g.ev(&app.HookStatusEvent{Event: hooks.UserPromptSubmit})
	g.liveHas("* taking the session's turn")
}
