package ui

import (
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// What still changes stands right under the conversation, a blank row
// between, and the blank rows that keep the region on the bottom rows go
// under it, above the row that says what gnosh is doing. A folded thought
// whose rows leave the region as it ends leaves no hole between the answer
// and the call after it (Jae, 26 November 2026, of five blank rows there on
// the Mac Studio while the call ran: "too many blank spaces").
func TestNoHoleBetweenAnAnswerAndItsCall(t *testing.T) {
	const (
		answer = "The incremental primorial build was the bottleneck (a million multiplications against a growing 25-million-bit accumulator). Fix: build it with a product tree instead — divide and conquer, seconds instead of minutes."
		cmd    = "cat > /tmp/bucket/main.go <<'EOF'\npackage main\nEOF"
	)
	for _, pinned := range []bool{false, true} {
		name := "region under the conversation"
		if pinned {
			name = "region on the bottom rows"
		}
		t.Run(name, func(t *testing.T) {
			g := newRig(t, 118, 33, text.NoColor)
			if pinned {
				g.free = true
				r := g.s.r.(*term.Renderer)
				row, _ := g.scr.Cursor()
				r.AskedCursor()
				r.CursorAnswer(row + 1)
				g.frame()
			}
			g.keys(`"continue" <Enter>`).start()
			for i := range 12 {
				g.ev(thought("The incremental build multiplies a growing accumulator by each prime in turn, step " + strings.Repeat("and again ", i%3) + "so the cost is quadratic in the bit length; a product tree pairs them up instead. "))
			}
			for _, w := range strings.SplitAfter(answer, " ") {
				g.ev(said(w))
			}
			g.ev(toolCall("c1", "Bash"), toolInput("c1", `{"command":"cat > /tmp/bucket/main.go <<'EOF'\npackage main\nEOF"}`))
			g.ev(replyEnd(core.StopToolUse, core.Text(answer)))
			g.ev(toolStart("c1", "Bash", map[string]any{"command": cmd}))
			blanks := blankRowsBetween(t, g, "seconds instead of minutes.", "cat > /tmp/bucket/main.go")
			if blanks > 1 {
				t.Errorf("while the call runs, %d blank rows stand between the answer and the call, want at most one\n%s", blanks, g.scr.Dump())
			}
		})
	}
}

// blankRowsBetween counts the blank rows on the screen between the row
// that ends with upper and the next row that holds lower.
func blankRowsBetween(t *testing.T, g *rig, upper, lower string) int {
	t.Helper()
	grid := g.scr.Lines()
	for i, l := range grid {
		if !strings.HasSuffix(strings.TrimRight(seen(l), " "), upper) {
			continue
		}
		n := 0
		for _, r := range grid[i+1:] {
			s := strings.TrimSpace(seen(r))
			if strings.Contains(s, lower) {
				return n
			}
			if s != "" {
				t.Fatalf("a row other than the call's comes between the answer and the call: %q\n%s", s, g.scr.Dump())
			}
			n++
		}
		t.Fatalf("the call is not on the screen under the answer\n%s", g.scr.Dump())
	}
	t.Fatalf("the answer's last row is not on the screen\n%s", g.scr.Dump())
	return 0
}
