package logo

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestWordUnstretchedAndMarkUnder(t *testing.T) {
	purple := color.RGBA{R: 0x6b, G: 0x50, B: 0xff, A: 0xff}
	o := Opts{FieldColor: purple, TitleColorA: purple, TitleColorB: purple, CharmColor: purple, VersionColor: purple, Width: 120}
	// The word's first row exactly as letterforms.go draws it, no letter widened.
	const firstRow = "\u2584\u2580\u2580\u2580\u2580 \u2588\u2584  \u2588 \u2584\u2580\u2580\u2580\u2584 \u2584\u2580\u2580\u2580\u2580 \u2588   \u2588"
	for _, compact := range []bool{true, false} {
		for range 3 {
			lines := strings.Split(ansi.Strip(Render(lipgloss.NewStyle(), "v9.9", compact, o)), "\n")
			word, mark := -1, -1
			for i, l := range lines {
				if word < 0 && strings.Contains(l, firstRow) {
					word = i
				}
				if strings.Contains(l, Mark) {
					mark = i
				}
			}
			if word < 0 {
				t.Fatalf("compact=%v: the word is missing or stretched:\n%s", compact, strings.Join(lines, "\n"))
			}
			if mark != word+3 {
				t.Fatalf("compact=%v: %s is on line %d, want %d, under the word's three rows", compact, Mark, mark, word+3)
			}
		}
	}
}
