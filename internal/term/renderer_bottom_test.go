package term

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/term/vtest"
)

// told tells r which row the cursor is on, as a terminal answers the
// question: asked, then answered, with nothing drawn in between.
func told(r *Renderer, scr *vtest.Screen) {
	row, _ := scr.Cursor()
	r.AskedCursor()
	r.CursorAnswer(row + 1)
}

// sameRow fails unless the renderer's count of the cursor's row is the
// terminal's.
func sameRow(t *testing.T, r *Renderer, scr *vtest.Screen, after string) {
	t.Helper()
	row, _ := scr.Cursor()
	if !r.known {
		t.Fatalf("after %s: the renderer lost the cursor's row", after)
	}
	if r.at != row {
		t.Fatalf("after %s: the renderer counts the cursor on row %d, the terminal has it on row %d\n%s", after, r.at, row, scr.Dump())
	}
}

// lastRows is the screen's last n rows.
func lastRows(scr *vtest.Screen, n int) []string {
	lines := scr.Lines()
	return lines[len(lines)-n:]
}

// A block that moves from the live region to the scrollback is drawn
// once, in one write, and the screen does not scroll to show it twice.
// Commit and then SetLive (the old way) pushed the region up off the
// bottom of the screen and left the rows below it empty.
func TestCommitLive(t *testing.T) {
	g := newRig(t, 30, 10)
	for i := 0; i < 20; i++ {
		g.commit(fmt.Sprint("old ", i))
	}
	g.setLive(6, 2, "", "s1", "s2", "s3", "", "act", "> prompt", "status")
	top := g.scr.Line(0)
	writes := g.out.writes
	if err := g.r.CommitLive([]string{"s1", "s2", "s3"}, []string{"", "act", "> prompt", "status"}, 2, 2); err != nil {
		t.Fatal(err)
	}
	g.committed = append(g.committed, "s1", "s2", "s3")
	g.live = []string{"", "act", "> prompt", "status"}
	g.curRow, g.curCol = 2, 2
	g.check("CommitLive")
	if g.out.writes != writes+1 {
		t.Errorf("CommitLive took %d writes, want 1", g.out.writes-writes)
	}
	if got := g.scr.Line(0); got != top {
		t.Errorf("the screen scrolled: its top row is %q, was %q\n%s", got, top, g.scr.Dump())
	}
	// With nothing to commit it is SetLive.
	if err := g.r.CommitLive(nil, []string{"> prompt", "status"}, 0, 2); err != nil {
		t.Fatal(err)
	}
	g.live = []string{"> prompt", "status"}
	g.curRow = 0
	g.check("CommitLive with nothing to commit")
}

// The live region stands on the bottom rows of the screen from the first
// frame on: when it is shorter than the rows below its top, blank rows
// above it make up the difference, and the next lines committed fill them
// (Jae, 2026-11-22: "the text field is not fixed on bottom of screen").
func TestLiveRegionKeptAtTheBottom(t *testing.T) {
	scr := vtest.New(30, 10)
	scr.SetScrollOnClear(true)
	r := NewRenderer(scr, 30, 10, vtest.Width)
	told(r, scr)

	// On an empty screen too: the box is on the last rows at once, and a
	// list that closes leaves it there.
	set := func(row int, lines ...string) {
		t.Helper()
		if err := r.SetLive(lines, row, 2); err != nil {
			t.Fatal(err)
		}
		sameRow(t, r, scr, fmt.Sprintf("SetLive(%q)", lines))
	}
	commitLive := func(commit []string, row int, lines ...string) {
		t.Helper()
		if err := r.CommitLive(commit, lines, row, 2); err != nil {
			t.Fatal(err)
		}
		sameRow(t, r, scr, fmt.Sprintf("CommitLive(%q, %q)", commit, lines))
	}
	set(0, "> /", "/model", "/mem", "status")
	if got := lastRows(scr, 4); !reflect.DeepEqual(got, []string{"> /", "/model", "/mem", "status"}) {
		t.Fatalf("on an empty screen the region is not on the last rows: %q\n%s", got, scr.Dump())
	}
	set(0, "> ", "status")
	want := []string{"", "", "", "", "", "", "", "", ">", "status"}
	if got := scr.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the list closed the screen is\n  %q\nwant\n  %q", got, want)
	}

	// The conversation fills the screen from the top, and the box stays.
	commitLive([]string{"line 0"}, 0, "> ", "status")
	want = []string{"line 0", "", "", "", "", "", "", "", ">", "status"}
	if got := scr.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after one line the screen is\n  %q\nwant\n  %q", got, want)
	}
	for i := 1; i < 12; i++ {
		commitLive([]string{fmt.Sprint("line ", i)}, 0, "> ", "status")
	}
	if got := lastRows(scr, 2); !reflect.DeepEqual(got, []string{">", "status"}) {
		t.Fatalf("the box is not on the last rows: %q\n%s", got, scr.Dump())
	}

	// A reply streams, with a thought folded above it, and is then
	// committed; the thought's rows go. The box stays on the last rows.
	set(5, "", "~ thinking", "  a", "  b", "", "act", "> ", "status")
	commitLive([]string{"answer 1", "answer 2"}, 1, "act", "> ", "status")
	want = []string{"line 10", "line 11", "answer 1", "answer 2", "", "", "", "act", ">", "status"}
	if got := scr.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the reply was committed the screen is\n  %q\nwant\n  %q", got, want)
	}
	// The turn ends: the working line goes, and a blank row more stands
	// above the box instead of below it.
	set(0, "> ", "status")
	if got := lastRows(scr, 4); !reflect.DeepEqual(got, []string{"", "", ">", "status"}) {
		t.Fatalf("after the working line went: %q\n%s", got, scr.Dump())
	}
	// What is committed next fills the blank rows first: nothing scrolls.
	top := scr.Line(0)
	commitLive([]string{"done"}, 0, "> ", "status")
	if got := scr.Line(0); got != top {
		t.Errorf("the screen scrolled: its top row is %q, was %q", got, top)
	}
	want = []string{"line 10", "line 11", "answer 1", "answer 2", "done", "", "", "", ">", "status"}
	if got := scr.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after one more line the screen is\n  %q\nwant\n  %q", got, want)
	}
	// Commit, the old way, keeps it there too.
	if err := r.Commit("more"); err != nil {
		t.Fatal(err)
	}
	sameRow(t, r, scr, "Commit")
	if got := lastRows(scr, 2); !reflect.DeepEqual(got, []string{">", "status"}) {
		t.Fatalf("after Commit the box is not on the last rows: %q", got)
	}
	// A hidden cursor waits at the top of the region, blank rows and all.
	if err := r.SetLive([]string{"working"}, -1, 0); err != nil {
		t.Fatal(err)
	}
	sameRow(t, r, scr, "SetLive with the cursor hidden")
	if got := lastRows(scr, 1); !reflect.DeepEqual(got, []string{"working"}) {
		t.Fatalf("the hidden-cursor region is not on the last row: %q", got)
	}
	if row, _ := scr.Cursor(); row != 6 {
		t.Errorf("the hidden cursor is on row %d, want 6, the region's top", row)
	}

	// Clear takes the region away, blank rows and all, and leaves the
	// cursor right under the conversation, for the shell.
	if err := r.Clear(); err != nil {
		t.Fatal(err)
	}
	if row, col := scr.Cursor(); row != 6 || col != 0 {
		t.Errorf("after Clear the cursor is at %d,%d, want 6,0 under %q", row, col, scr.Line(5))
	}
	if got := scr.Line(5); got != "more" {
		t.Errorf("the row above the cursor is %q, want the last committed line", got)
	}
	for i := 6; i < 10; i++ {
		if scr.Line(i) != "" {
			t.Errorf("row %d holds %q after Clear", i, scr.Line(i))
		}
	}
	// Ctrl+L draws the region again where it was: at the bottom.
	set(0, "> ", "status")
	if got := lastRows(scr, 2); !reflect.DeepEqual(got, []string{">", "status"}) {
		t.Fatalf("after Clear and SetLive the box is not on the last rows: %q\n%s", got, scr.Dump())
	}
}

// A region that begins on the screen's last row, as gnosh does when it
// starts under a full screen, is at the bottom from the start.
func TestLiveRegionStartsAtTheBottom(t *testing.T) {
	scr := vtest.New(30, 6)
	for i := 0; i < 8; i++ {
		_, _ = scr.WriteString(fmt.Sprintf("shell %d\r\n", i))
	}
	r := NewRenderer(scr, 30, 6, vtest.Width)
	told(r, scr)
	if err := r.SetLive([]string{"> /", "/a", "/b", "status"}, 0, 2); err != nil {
		t.Fatal(err)
	}
	if err := r.SetLive([]string{"> ", "status"}, 0, 2); err != nil {
		t.Fatal(err)
	}
	sameRow(t, r, scr, "SetLive")
	if got := lastRows(scr, 2); !reflect.DeepEqual(got, []string{">", "status"}) {
		t.Fatalf("the box is not on the last rows: %q\n%s", got, scr.Dump())
	}
}

// An answer that comes after the renderer drew more is brought up to date
// with what it drew since the question; only the last question's answer
// counts; a resize or a Reset voids the answers on their way, and the
// renderer wants to be told again.
func TestCursorAnswers(t *testing.T) {
	scr := vtest.New(30, 8)
	r := NewRenderer(scr, 30, 8, vtest.Width)
	if !r.WantsCursor() {
		t.Fatal("a new renderer should want the cursor's row")
	}
	r.AskedCursor()
	if r.WantsCursor() {
		t.Fatal("with a question on its way the renderer should not want another")
	}
	asked, _ := scr.Cursor()
	for i := 0; i < 5; i++ {
		_ = r.Commit(fmt.Sprint("line ", i))
		_ = r.SetLive([]string{"> ", "status", "list"}, 0, 2)
	}
	r.CursorAnswer(asked + 1)
	sameRow(t, r, scr, "a late answer")

	// Two questions: the first answer does not count, the second does.
	r.Reset()
	if !r.WantsCursor() {
		t.Fatal("after Reset the renderer should want the cursor's row")
	}
	r.AskedCursor()
	first, _ := scr.Cursor()
	_ = r.SetLive([]string{"> ", "status"}, 0, 2)
	r.AskedCursor()
	second, _ := scr.Cursor()
	_ = r.Commit("a", "b", "c", "d", "e", "f", "g", "h")
	_ = r.SetLive([]string{"> x", "status"}, 0, 3)
	r.CursorAnswer(first + 1)
	if r.known {
		t.Fatal("the answer to the first of two questions counted")
	}
	r.CursorAnswer(second + 1)
	sameRow(t, r, scr, "the second answer")

	// A resize voids the answer on its way.
	r.AskedCursor()
	before, _ := scr.Cursor()
	scr.Resize(30, 6)
	_ = r.Resize(30, 6)
	if r.known {
		t.Fatal("the cursor's row is still taken as known after a resize")
	}
	if !r.WantsCursor() {
		t.Fatal("after a resize the renderer should want the row again at once: the answer on its way no longer counts")
	}
	r.CursorAnswer(before + 1)
	if r.known {
		t.Fatal("an answer to a question from before the resize counted")
	}
	if !r.WantsCursor() {
		t.Fatal("once the void answer came, the renderer should want the row again")
	}
	told(r, scr)
	sameRow(t, r, scr, "a fresh answer after the resize")

	// An answer nobody asked for is ignored, and so is one off the screen.
	r.CursorAnswer(1)
	sameRow(t, r, scr, "an answer to no question")
	r.Reset()
	r.AskedCursor()
	r.CursorAnswer(99)
	if r.known {
		t.Fatal("an answer below the screen counted")
	}
}

// After a resize the region is drawn from its top again, and once the
// terminal has said where the cursor is, a region that was at the bottom
// goes back to the bottom.
func TestLiveRegionBackAtTheBottomAfterResize(t *testing.T) {
	scr := vtest.New(30, 8)
	r := NewRenderer(scr, 30, 8, vtest.Width)
	told(r, scr)
	for i := 0; i < 10; i++ {
		_ = r.CommitLive([]string{fmt.Sprint("line ", i)}, []string{"act", "> ", "status"}, 1, 2)
	}
	_ = r.SetLive([]string{"> ", "status"}, 0, 2)
	if got := lastRows(scr, 2); !reflect.DeepEqual(got, []string{">", "status"}) {
		t.Fatalf("the box is not on the last rows: %q", got)
	}
	scr.Resize(30, 12)
	_ = r.Resize(30, 12)
	told(r, scr)
	_ = r.SetLive([]string{"> ", "status"}, 0, 2)
	sameRow(t, r, scr, "SetLive after the resize")
	if got := lastRows(scr, 2); !reflect.DeepEqual(got, []string{">", "status"}) {
		t.Fatalf("after the resize the box is not back on the last rows: %q\n%s", got, scr.Dump())
	}
}

// Whatever is drawn, in any order, with the cursor's row known: the
// renderer's count of the row is the terminal's; every committed line is
// on the terminal once and in order, then only blank rows, then the live
// region, whose last row is the screen's last row.
func TestLiveRegionAtTheBottomRandom(t *testing.T) {
	for seed := int64(1); seed <= 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		rows := 4 + rng.Intn(10)
		scr := vtest.New(24, rows)
		for i := rng.Intn(rows + 3); i > 0; i-- {
			_, _ = scr.WriteString("before\r\n")
		}
		base := len(scr.AllLines())
		r := NewRenderer(scr, 24, rows, vtest.Width)
		told(r, scr)
		var committed, live []string
		n := 0
		line := func() string { n++; return fmt.Sprint("c", n) }
		for step := 0; step < 60; step++ {
			live = live[:0]
			for i := rng.Intn(rows); i >= 0; i-- {
				live = append(live, fmt.Sprintf("l%d.%d", step, i))
			}
			var commit []string
			for i := rng.Intn(4); i > 0; i-- {
				commit = append(commit, line())
			}
			row := rng.Intn(len(live)+1) - 1
			var err error
			switch rng.Intn(3) {
			case 0:
				err = r.CommitLive(commit, live, row, 1)
			case 1:
				if len(commit) > 0 {
					err = r.Commit(commit...)
				}
				if err == nil {
					err = r.SetLive(live, row, 1)
				}
			default:
				commit = nil
				err = r.SetLive(live, row, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			committed = append(committed, commit...)
			what := fmt.Sprintf("seed %d step %d", seed, step)
			sameRow(t, r, scr, what)

			all := scr.AllLines()[base:]
			vis := live
			if len(vis) > rows {
				vis = vis[len(vis)-rows:]
			}
			// The live region's own rows end the terminal's lines.
			if len(all) < len(vis) || !reflect.DeepEqual(all[len(all)-len(vis):], vis) {
				t.Fatalf("%s: the terminal does not end with the live region %q:\n%s", what, vis, scr.Dump())
			}
			rest := all[:len(all)-len(vis)]
			for len(rest) > 0 && rest[len(rest)-1] == "" {
				rest = rest[:len(rest)-1]
			}
			if !reflect.DeepEqual(rest, committed) && !(len(rest) == 0 && len(committed) == 0) {
				t.Fatalf("%s: above the live region the terminal holds\n  %q\nwant the committed lines\n  %q\n%s", what, rest, committed, scr.Dump())
			}
			if bottomRow := len(scr.AllLines()) - len(scr.Scrollback()) - 1; bottomRow != rows-1 {
				t.Fatalf("%s: the region ends on row %d of %d, not on the last:\n%s", what, bottomRow, rows, scr.Dump())
			}
		}
	}
}

// The renderer's notion of a blank row is a row the terminal shows blank.
func TestPaddingRowsAreBlank(t *testing.T) {
	scr := vtest.New(20, 5)
	r := NewRenderer(scr, 20, 5, vtest.Width)
	told(r, scr)
	_ = r.SetLive([]string{"a", "b", "c", "d", "e"}, -1, 0)
	_ = r.SetLive([]string{"\x1b[1mbold\x1b[m"}, 0, 1)
	if got := strings.Join(scr.Lines(), "|"); got != "||||bold" {
		t.Fatalf("screen = %q", got)
	}
}

// The first rows of the live region that go on with the committed lines,
// such as the open row of a paragraph whose first rows were committed,
// stand right under them, and the blank rows that keep the region on the
// bottom of the screen go below them (Jae, 2026-11-26: "green text has
// newlines where it shouldn't", of "...flatter a bad", blank rows, and
// "lemma, though, ..."). When the open row is committed in its turn,
// nothing on the screen moves.
func TestJoinedRowsStandUnderTheCommittedLines(t *testing.T) {
	scr := vtest.New(30, 12)
	scr.SetScrollOnClear(true)
	r := NewRenderer(scr, 30, 12, vtest.Width)
	told(r, scr)
	draw := func(commit []string, joined, row int, lines ...string) {
		t.Helper()
		r.SetJoined(joined)
		if err := r.CommitLive(commit, lines, row, 2); err != nil {
			t.Fatal(err)
		}
		sameRow(t, r, scr, fmt.Sprintf("CommitLive(%q, %q) with %d joined", commit, lines, joined))
	}
	screen := func(when string, want ...string) {
		t.Helper()
		if got := scr.Lines(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s the screen is\n  %q\nwant\n  %q", when, got, want)
		}
	}
	cursor := func(when string, wantRow, wantCol int) {
		t.Helper()
		if row, col := scr.Cursor(); row != wantRow || col != wantCol {
			t.Fatalf("%s the cursor is on row %d, cell %d; want row %d, cell %d\n%s", when, row, col, wantRow, wantCol, scr.Dump())
		}
	}

	// Nothing of the answer is final yet: it is all in the region, on the
	// bottom rows, as before.
	draw([]string{"> go"}, 0, 4, "", "flatter a", "", "act", "> ", "status")
	screen("before a row of the answer is final,",
		"> go", "", "", "", "", "", "", "flatter a", "", "act", ">", "status")

	// Its first row is final and committed; the open row goes on with it.
	draw([]string{"", "flatter a bad"}, 1, 3, "lemma,", "", "act", "> ", "status")
	screen("with the first row committed,",
		"> go", "", "flatter a bad", "lemma,", "", "", "", "", "", "act", ">", "status")
	cursor("with the first row committed,", 10, 2)

	// A hidden cursor waits at the region's top: the open row.
	r.SetJoined(1)
	if err := r.SetLive([]string{"lemma,", "", "act", "> ", "status"}, -1, 0); err != nil {
		t.Fatal(err)
	}
	sameRow(t, r, scr, "SetLive with the cursor hidden")
	cursor("with the cursor hidden,", 3, 0)

	// The model writes a call after the paragraph, whose last row stays
	// open meanwhile, under the rest of it.
	draw(nil, 1, 5, "lemma, though.", "", "$ cat big.go", "", "act", "> ", "status")
	want := []string{"> go", "", "flatter a bad", "lemma, though.", "", "", "", "$ cat big.go", "", "act", ">", "status"}
	screen("while the call is written,", want...)
	cursor("while the call is written,", 10, 2)

	// The reply ends: the open row is committed where it stood.
	draw([]string{"lemma, though."}, 0, 4, "", "$ cat big.go", "", "act", "> ", "status")
	screen("after the reply,", want...)
	cursor("after the reply,", 10, 2)
}
