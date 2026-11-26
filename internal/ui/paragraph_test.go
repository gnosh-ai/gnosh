package ui

import (
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// jaesParagraph is an answer Jae copied from the Mac Studio's screen on 26
// November 2026 while the model wrote a call after it. It came in two
// pieces, a blank row between "flatter a bad" and "lemma": "green text has
// newlines where it shouldn't".
const jaesParagraph = `No Lemma 2 failures among the 78 composites — the 58 "suspicious" rows are all primes (where the defect is identically zero, as AKS says it should be). Small composites all leak a proper factor via gcd. Small numbers can flatter a bad lemma, though, so let me push to larger semiprimes before calling it.`

// A paragraph is on the screen in one piece at every moment: while it is
// written, while the model writes a call after it, and after the reply.
// Its open row stands right under its committed rows, with no blank row
// between them, also when the region is kept on the bottom rows of a
// screen that is not full yet (DECISIONS 391).
func TestAParagraphIsNeverSeenInTwoPieces(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		name := "region under the conversation"
		if pinned {
			name = "region on the bottom rows"
		}
		t.Run(name, func(t *testing.T) {
			g := newRig(t, 118, 33, text.NoColor)
			if pinned {
				// The terminal says which row the cursor is on, so
				// blank rows keep the region on the bottom rows. The
				// rig's own checks expect the region right under the
				// conversation; the screen is read here instead.
				g.free = true
				r := g.s.r.(*term.Renderer)
				row, _ := g.scr.Cursor()
				r.AskedCursor()
				r.CursorAnswer(row + 1)
				g.frame()
			}
			g.keys(`"go" <Enter>`).start()
			var written strings.Builder
			for _, w := range strings.SplitAfter(jaesParagraph, " ") {
				g.ev(said(w))
				written.WriteString(w)
				inOnePiece(t, g, written.String(), "while it is written")
			}
			g.ev(toolCall("c1", "Bash"), toolInput("c1", `{"command":"cat > /tmp/akstest/big.go <<'EOF'\npackage main\n`))
			inOnePiece(t, g, jaesParagraph, "while a call is written after it")
			g.ev(replyEnd(core.StopToolUse))
			inOnePiece(t, g, jaesParagraph, "after the reply")
		})
	}
}

// inOnePiece fails unless the screen shows the paragraph, as far as it is
// written, in one piece: its rows one right under another.
func inOnePiece(t *testing.T, g *rig, written, when string) {
	t.Helper()
	want := strings.TrimSpace(written)
	start := want[:min(len(want), len("No Lemma 2"))]
	grid := g.scr.Lines()
	for i, l := range grid {
		if !strings.HasPrefix(seen(l), start) {
			continue
		}
		var rows []string
		for _, r := range grid[i:] {
			r = strings.TrimRight(seen(r), " ")
			if r == "" {
				break
			}
			rows = append(rows, r)
		}
		if got := strings.Join(rows, " "); !strings.HasPrefix(got, want) {
			t.Fatalf("%s, the paragraph is on the screen as\n  %s\nand should be in one piece:\n  %s\n%s", when, strings.Join(rows, "\n  "), want, g.scr.Dump())
		}
		return
	}
	t.Fatalf("%s, the paragraph is not on the screen\n%s", when, g.scr.Dump())
}
