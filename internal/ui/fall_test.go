package ui

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/text"
)

// The words the fall is tried over: what the line says, and what follows.
const (
	fallWords = "qwen3.8-27b is thinking"
	fallRest  = " · 12s · Esc stops"
)

// runFall lets a fall run from its beginning for d at one depth, over
// words cells wide, a frame every every, and shows each frame to see.
func runFall(depth float64, cells int, d, every time.Duration, see func(fl *fall)) {
	var fl fall
	for at := time.Duration(0); at <= d; at += every {
		fl.step(stamp.Add(at), depth, cells)
		see(&fl)
	}
}

// fg is the words of the sequence that sets a 24-bit foreground colour,
// or "" for the terminal's own colour.
func fg(c text.Color) string {
	if c == text.Default {
		return ""
	}
	var r, g, b int
	fmt.Sscanf(c.String(), "#%02x%02x%02x", &r, &g, &b)
	return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
}

// shown is one cell of a styled line as a terminal shows it: its
// character, the words that set its foreground colour ("" for the
// terminal's own), and whether it is bold or faint.
type shown struct {
	r         rune
	fg        string
	bold, dim bool
}

// rgb is the cell's colour as numbers, for a 24-bit colour.
func (c shown) rgb() (rgb [3]int, ok bool) {
	n, _ := fmt.Sscanf(c.fg, "38;2;%d;%d;%d", &rgb[0], &rgb[1], &rgb[2])
	return rgb, n == 3
}

// rgbOf is a 24-bit colour as numbers.
func rgbOf(c text.Color) [3]int {
	rgb, _ := shown{fg: fg(c)}.rgb()
	return rgb
}

// apart is how far two colours are from each other.
func apart(a, b [3]int) int { return abs(a[0]-b[0]) + abs(a[1]-b[1]) + abs(a[2]-b[2]) }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// cellsOf reads a styled line the way a terminal does, one cell for each
// character (the lines of these tests have no wide ones).
func cellsOf(t *testing.T, line string) []shown {
	t.Helper()
	var out []shown
	var now shown
	for line != "" {
		if rest, ok := strings.CutPrefix(line, "\x1b["); ok {
			end := strings.IndexByte(rest, 'm')
			if end < 0 {
				t.Fatalf("a sequence that is no colour or style in %q", line)
			}
			words := strings.Split(rest[:end], ";")
			for i := 0; i < len(words); i++ {
				n, _ := strconv.Atoi(words[i])
				switch {
				case n == 0:
					now = shown{}
				case n == 1:
					now.bold = true
				case n == 2:
					now.dim = true
				case n == 38 && i+4 < len(words) && words[i+1] == "2":
					now.fg = strings.Join(words[i:i+5], ";")
					i += 4
				case n == 38 && i+2 < len(words) && words[i+1] == "5":
					now.fg = strings.Join(words[i:i+3], ";")
					i += 2
				case n >= 30 && n <= 37, n >= 90 && n <= 97:
					now.fg = words[i]
				default:
					t.Fatalf("the sequence %q sets something the fall never does", rest[:end])
				}
			}
			line = rest[end+1:]
			continue
		}
		for _, r := range line {
			c := now
			c.r = r
			out = append(out, c)
			line = line[len(string(r)):]
			break
		}
	}
	return out
}

// ranks draws what passes over the cells: a point where nothing does, and
// where a drop does, which cell of it counted from its head.
func ranks(over []spot) string {
	var b strings.Builder
	for _, at := range over {
		if at.on {
			b.WriteString(strconv.Itoa(at.rank))
		} else {
			b.WriteByte('.')
		}
	}
	return b.String()
}

// A thought deepens from three seconds on, and is deep at fifteen.
func TestDepthOfAThought(t *testing.T) {
	for _, tt := range []struct {
		d    time.Duration
		want float64
	}{
		{0, 0}, {2 * time.Second, 0}, {3 * time.Second, 0},
		{6 * time.Second, 0.25}, {9 * time.Second, 0.5}, {12 * time.Second, 0.75},
		{15 * time.Second, 1}, {10 * time.Minute, 1}, {-time.Second, 0},
	} {
		if got := depthOf(tt.d); got != tt.want {
			t.Errorf("a thought of %v is %v deep, want %v", tt.d, got, tt.want)
		}
	}
}

// A drop starts at the first letter and gathers speed all the way along
// the words, as a thing does that falls: each cell takes it no longer than
// the one before, and the last far less long than the first.
func TestADropGathersSpeed(t *testing.T) {
	const cells = 40
	for _, depth := range []float64{0, 0.5, 1} {
		// When the head of the fall's first drop first stood in each cell.
		reached := map[int]time.Duration{}
		var fl fall
		for at := time.Duration(0); at <= 6*time.Second; at += time.Millisecond {
			fl.step(stamp.Add(at), depth, cells)
			for _, d := range fl.drops {
				if c := int(d.x); d.n == 0 && c <= cells {
					if _, ok := reached[c]; !ok {
						reached[c] = at
					}
				}
			}
		}
		if at, ok := reached[0]; !ok || at != 0 {
			t.Fatalf("depth %v: the first drop is at the first letter at %v (%v)", depth, at, ok)
		}
		last := time.Hour
		var first time.Duration
		for c := 1; c <= cells; c++ {
			at, ok := reached[c]
			if !ok {
				t.Fatalf("depth %v: the first drop never reached cell %d: %v", depth, c, reached)
			}
			took := at - reached[c-1]
			if c == 1 {
				first = took
			}
			// (The clock of this test steps a millisecond at a time.)
			if took > last+2*time.Millisecond {
				t.Errorf("depth %v: cell %d took the drop %v, and the cell before it %v: it slowed down", depth, c-1, took, last)
			}
			last = took
		}
		if last*3 > first {
			t.Errorf("depth %v: the last cell took the drop %v and the first %v: it hardly gathered speed", depth, last, first)
		}
	}

	// However long the line, no drop goes faster than the fastest, and
	// it still gets to the end.
	fastest, far := 0.0, 0.0
	runFall(1, 300, 8*time.Second, 10*time.Millisecond, func(fl *fall) {
		for _, d := range fl.drops {
			fastest, far = max(fastest, d.v), max(far, d.x)
		}
	})
	if fastest != fallFastest || far < 300 {
		t.Errorf("along 300 cells the fastest drop went %.1f cells a second and the farthest got to %.0f", fastest, far)
	}
	if got, want := crossing(fallPullDeep, fallPushDeep, 300), 1.4+(300-44.8)/60; got < want-0.01 || got > want+0.01 {
		t.Errorf("300 cells take a drop %.3f s in a deep thought, want %.3f", got, want)
	}
}

// In a deep thought more drops fall, they cross sooner, and the pull on
// them is several times stronger.
func TestADeepThoughtFallsHarder(t *testing.T) {
	const cells = 45
	type figures struct {
		mean  float64       // drops with their head on the words, on average
		born  uint32        // drops started
		cross time.Duration // how long the first drop's head took along them
		pull  float64       // the speed a drop gains in a second
	}
	measure := func(depth float64) figures {
		var fig figures
		frames, heads := 0, 0
		var v0 float64
		runFall(depth, cells, 30*time.Second, 20*time.Millisecond, func(fl *fall) {
			frames++
			for _, d := range fl.drops {
				if int(d.x) < cells {
					heads++
				}
				if d.n != 0 {
					continue
				}
				if int(d.x) < cells {
					fig.cross = fl.at.Sub(stamp)
				}
				switch frames {
				case 1:
					v0 = d.v
				case 6:
					fig.pull = (d.v - v0) / 0.1
				}
			}
			fig.born = fl.born
		})
		fig.mean = float64(heads) / float64(frames)
		return fig
	}
	calm, deep := measure(0), measure(1)
	if calm.mean > 1.6 || deep.mean < 2.5 {
		t.Errorf("on average %.2f drops fall while calm and %.2f in a deep thought", calm.mean, deep.mean)
	}
	if deep.born < 4*calm.born {
		t.Errorf("in thirty seconds %d drops started while calm and %d in a deep thought", calm.born, deep.born)
	}
	if deep.cross*2 > calm.cross {
		t.Errorf("a drop crosses in %v while calm and in %v in a deep thought", calm.cross, deep.cross)
	}
	if deep.pull < 4*calm.pull || calm.pull <= 0 {
		t.Errorf("a drop gains %.1f cells a second each second while calm and %.1f in a deep thought", calm.pull, deep.pull)
	}
	// Between the two it is between the two.
	if half := measure(0.5); half.mean <= calm.mean || half.mean >= deep.mean || half.cross >= calm.cross || half.cross <= deep.cross {
		t.Errorf("half way: %+v, between %+v and %+v", half, calm, deep)
	}
}

// Something falls through the words in every frame, at every depth and
// however wide they are, and the words stay exactly the words.
func TestFallNeverLeavesTheWordsBare(t *testing.T) {
	var m text.Metrics
	for _, p := range []text.Profile{text.TrueColor, text.ANSI256, text.ANSI16} {
		f := DefaultTheme(p).Fall
		if !f.moves() {
			t.Fatalf("with %v colours there is no fall", p)
		}
		for _, words := range []string{"working", fallWords, "waiting for qwen3.8-27b to start its reply, the long way round"} {
			cells := m.Width(words) + m.Width(fallRest)
			for _, depth := range []float64{0, 0.25, 0.5, 0.75, 1} {
				runFall(depth, cells, 15*time.Second, f.every(depth), func(fl *fall) {
					on := 0
					for _, at := range fl.spots(cells) {
						if at.on {
							on++
						}
					}
					if on == 0 {
						t.Fatalf("%v, depth %v, %d cells: nothing falls at %v", p, depth, cells, fl.at.Sub(stamp))
					}
					if got := text.Strip(fl.wash(&f, m, words, fallRest)); got != words+fallRest {
						t.Fatalf("%v, depth %v: the words came out as %q", p, depth, got)
					}
				})
			}
		}
	}
	if f := DefaultTheme(text.NoColor).Fall; f.moves() {
		t.Error("with no colour there is a fall")
	}
}

// A wide letter is one letter to the fall: it is never cut, and takes one
// colour.
func TestFallOverWideLetters(t *testing.T) {
	var m text.Metrics
	f := DefaultTheme(text.TrueColor).Fall
	const words, rest = "模型 is 思考中", " · 12s"
	cells := m.Width(words) + m.Width(rest)
	runFall(1, cells, 5*time.Second, 30*time.Millisecond, func(fl *fall) {
		line := fl.wash(&f, m, words, rest)
		if got := text.Strip(line); got != words+rest {
			t.Fatalf("the words came out as %q", got)
		}
		if got := m.VisibleWidth(line); got != cells {
			t.Fatalf("the line is %d cells wide, want %d", got, cells)
		}
	})
}

// A drop's head colours the cell it has reached and its tail the cells to
// its left; what is off the words colours nothing; and where two drops
// meet, the cell nearer its own head shows.
func TestFallLaysDropsOut(t *testing.T) {
	for _, tt := range []struct {
		name  string
		drops []drop
		want  string
	}{
		{"just started", []drop{{x: 0.2, v: 2}}, "0..........."},
		{"one cell on", []drop{{x: 1.9, v: 4}}, "10.........."},
		{"faster, a longer tail", []drop{{x: 5.9, v: 16}}, "..3210......"},
		{"at the last cell", []drop{{x: 11.99, v: 40}}, ".....6543210"},
		{"its head gone, its tail still leaving", []drop{{x: 13.5, v: 30}}, "........5432"},
		{"as fast as a drop goes", []drop{{x: 9.5, v: fallFastest}}, ".876543210.."},
		{"two apart", []drop{{x: 9.5, v: 9}, {x: 1.2, v: 2}}, "10.....210.."},
		{"a head over another's tail", []drop{{x: 8.2, v: 20}, {x: 7.1, v: 4}}, ".....3100..."},
		{"two heads in one cell", []drop{{x: 0.9, v: 3}, {x: 0.1, v: 2}}, "0..........."},
	} {
		fl := fall{drops: tt.drops}
		if got := ranks(fl.spots(12)); got != tt.want {
			t.Errorf("%s: over twelve cells %q, want %q", tt.name, got, tt.want)
		}
	}
	// Of two heads in one cell the drop that started first shows.
	fl := fall{drops: []drop{{x: 0.9, v: 3, n: 4}, {x: 0.1, v: 2, n: 5}}}
	if at := fl.spots(3)[0]; at.n != 4 {
		t.Errorf("of two heads in one cell drop %d shows, want the first", at.n)
	}
	// The faster the drop, the longer its tail, up to the longest.
	last := 0
	for v := 0.0; v <= 2*fallFastest; v += 0.5 {
		tail := (&drop{v: v}).tail()
		if tail < last || tail < 1 || tail > fallTailMost {
			t.Fatalf("a drop at %.1f cells a second has a tail of %d, after %d", v, tail, last)
		}
		last = tail
	}
	if last != fallTailMost {
		t.Errorf("the fastest drop's tail is %d cells, want %d", last, fallTailMost)
	}
}

// The same moment draws the same frame, however often it is asked for.
func TestFallStandsStillInOneMoment(t *testing.T) {
	var m text.Metrics
	f := DefaultTheme(text.TrueColor).Fall
	cells := m.Width(fallWords) + m.Width(fallRest)
	frame := func(fl *fall) string { return fl.mark(&f) + " " + fl.wash(&f, m, fallWords, fallRest) }
	var fl fall
	for at := time.Duration(0); at <= 3*time.Second; at += 70 * time.Millisecond {
		now := stamp.Add(at)
		fl.step(now, 0.8, cells)
		first := frame(&fl)
		fl.step(now, 0.8, cells)
		fl.step(now, 0.8, cells)
		if again := frame(&fl); again != first {
			t.Fatalf("at %v the frame changed when it was drawn again:\n%q\n%q", at, first, again)
		}
	}
	// Two falls with the same clock are the same fall.
	var a, b strings.Builder
	runFall(1, cells, 5*time.Second, 60*time.Millisecond, func(fl *fall) { a.WriteString(frame(fl)) })
	runFall(1, cells, 5*time.Second, 60*time.Millisecond, func(fl *fall) { b.WriteString(frame(fl)) })
	if a.String() != b.String() {
		t.Error("two falls with the same clock fell differently")
	}
}

// A fall that was not drawn for a while begins again, and a frame that
// comes late moves the drops as the frames on time would have.
func TestFallAfterAGap(t *testing.T) {
	const cells = 45
	var fl fall
	for at := time.Duration(0); at <= 2*time.Second; at += 50 * time.Millisecond {
		fl.step(stamp.Add(at), 1, cells)
	}
	if len(fl.drops) < 2 {
		t.Fatalf("after two seconds of a deep thought %d drops fall", len(fl.drops))
	}
	fl.step(stamp.Add(10*time.Second), 0, cells)
	if len(fl.drops) != 1 || fl.drops[0].x != 0 || fl.drops[0].n != 0 || fl.depth != 0 || fl.turned != 0 {
		t.Errorf("after a gap the fall is %+v at depth %v, and should have begun again", fl.drops, fl.depth)
	}
	// The clock going back is a gap too.
	fl.step(stamp, 0.5, cells)
	if len(fl.drops) != 1 || fl.drops[0].x != 0 {
		t.Errorf("after the clock went back the fall is %+v", fl.drops)
	}

	var steady, late fall
	for at := time.Duration(0); at <= 900*time.Millisecond; at += 20 * time.Millisecond {
		steady.step(stamp.Add(at), 0.6, cells)
	}
	late.step(stamp, 0.6, cells)
	late.step(stamp.Add(900*time.Millisecond), 0.6, cells)
	if len(steady.drops) != len(late.drops) {
		t.Fatalf("one late frame left %d drops, and frames on time %d", len(late.drops), len(steady.drops))
	}
	for i := range steady.drops {
		if d := steady.drops[i].x - late.drops[i].x; d > 1.5 || d < -1.5 {
			t.Errorf("drop %d stands at %.2f after frames on time and at %.2f after one late frame", i, steady.drops[i].x, late.drops[i].x)
		}
	}
	if d := steady.turned - late.turned; d > 0.01 || d < -0.01 {
		t.Errorf("the mark turned %.2f shapes in frames on time and %.2f in one late frame", steady.turned, late.turned)
	}
}

// When a deep thought is over the fall grows calm over a second and a
// half, not at once; a deeper thought is followed at once.
func TestFallSettles(t *testing.T) {
	f := DefaultTheme(text.TrueColor).Fall
	var fl fall
	at := time.Duration(0)
	run := func(d time.Duration, depth float64) {
		for end := at + d; at < end; {
			at += 50 * time.Millisecond
			fl.step(stamp.Add(at), depth, 45)
		}
	}
	fl.step(stamp, 0, 45)
	run(time.Second, 1)
	if fl.depth != 1 {
		t.Fatalf("the fall follows a deep thought to %v", fl.depth)
	}
	run(750*time.Millisecond, 0)
	if fl.depth < 0.4 || fl.depth > 0.6 {
		t.Errorf("half way through settling the fall is %v deep", fl.depth)
	}
	run(time.Second, 0)
	if fl.depth != 0 {
		t.Errorf("after settling the fall is %v deep", fl.depth)
	}
	if got := f.every(0); got != f.Every {
		t.Errorf("a calm fall steps every %v", got)
	}
	if got := f.every(1); got != f.EveryDeep || got >= f.Every {
		t.Errorf("the deepest fall steps every %v", got)
	}
}

// The words stand in their own dull colours, and are never white: the
// words in a grey, what follows them in the quiet grey. What falls
// through them while calm is pale and even, and never bold: a head nearly
// the colour of the person's own words, its tail fading into the letters'
// own colour. In a deep thought the heads are the palette's own four
// colours, bold, and some of them glint.
func TestFallTakesColourAsItDeepens(t *testing.T) {
	var m text.Metrics
	th := DefaultTheme(text.TrueColor)
	f, pal := th.Fall, th.Palette
	hues := []text.Color{pal.Aqua, pal.Purple, pal.Pink, pal.Sun}
	nWords := m.Width(fallWords)
	cells := nWords + m.Width(fallRest)
	if f.Text == pal.Prompt || f.Text == text.Default || f.Text == f.Quiet || f.Quiet != pal.Grey {
		t.Fatalf("the words stand in %v and what follows in %v", f.Text, f.Quiet)
	}
	is := func(c shown, colours ...text.Color) bool {
		for _, want := range colours {
			if c.fg == fg(want) {
				return true
			}
		}
		return false
	}
	type tally struct{ heads, glints, bold, overRest int }
	watch := func(depth float64, heads []text.Color, glints []text.Color) (n tally) {
		runFall(depth, cells, 20*time.Second, f.every(depth), func(fl *fall) {
			over := fl.spots(cells)
			line := cellsOf(t, fl.wash(&f, m, fallWords, fallRest))
			if len(line) != cells {
				t.Fatalf("depth %v: the line has %d cells, want %d", depth, len(line), cells)
			}
			for i, c := range line {
				base := f.Text
				if i >= nWords {
					base = f.Quiet
				}
				if c.dim {
					t.Fatalf("depth %v: cell %d is faint", depth, i)
				}
				if c.bold {
					n.bold++
				}
				switch at := over[i]; {
				case !at.on:
					if !is(c, base) || c.bold {
						t.Fatalf("depth %v: cell %d, which no drop is over, is %+v, want the dull %v", depth, i, c, base)
					}
				case at.rank == 0:
					n.heads++
					if i >= nWords {
						n.overRest++
					}
					switch {
					case is(c, glints...):
						n.glints++
						if !c.bold {
							t.Fatalf("depth %v: a glint that is not bold: %+v", depth, c)
						}
					case !is(c, heads...):
						t.Fatalf("depth %v: the head at cell %d is %+v, want one of %v", depth, i, c, heads)
					}
				case c.bold:
					t.Fatalf("depth %v: a tail cell is bold: %+v", depth, c)
				case at.rank > 1 && at.rank%fallTailBand != 0 && i+1 < len(line) && (i+1 < nWords || i >= nWords) &&
					over[i+1].on && over[i+1].n == at.n && over[i+1].rank == at.rank-1:
					// Within one colour of a tail, and over letters of one
					// colour, a cell is nearer the letters' own colour than
					// the cell before it, toward the head.
					here, _ := c.rgb()
					there, _ := line[i+1].rgb()
					if own := rgbOf(base); apart(here, own) > apart(there, own)+6 {
						t.Fatalf("depth %v: the tail at cell %d (%v) is farther from the letters' %v than the cell before it (%v)", depth, i, here, own, there)
					}
				}
			}
		})
		return n
	}

	// Calm: pale heads, a tenth of the way to their colours.
	var pale []text.Color
	for _, hue := range hues {
		c := text.Blend(pal.Prompt, hue, fallDim)
		pale = append(pale, c)
		// Pale is far nearer the colour of the person's words than its own.
		if apart(rgbOf(c), rgbOf(pal.Prompt))*3 > apart(rgbOf(c), rgbOf(hue)) {
			t.Errorf("the pale form %v of %v is not near %v", c, hue, pal.Prompt)
		}
	}
	calm := watch(0, pale, nil)
	if calm.heads == 0 || calm.bold != 0 || calm.glints != 0 {
		t.Errorf("a calm fall: %+v", calm)
	}
	if calm.overRest == 0 {
		t.Error("a calm fall never reaches what follows the words: the whole line should be fallen through")
	}

	// Deep: the colours themselves, bold, and glints toward the pale.
	var flashes []text.Color
	for _, hue := range hues {
		flashes = append(flashes, text.Blend(hue, pal.Prompt, fallGlintTo))
	}
	deep := watch(1, hues, flashes)
	if deep.heads < 3*calm.heads {
		t.Errorf("a deep fall shows %d heads in twenty seconds, and a calm one %d", deep.heads, calm.heads)
	}
	if deep.glints == 0 || deep.glints*2 > deep.heads {
		t.Errorf("of %d heads in a deep fall %d glint", deep.heads, deep.glints)
	}
	if deep.bold != deep.heads {
		t.Errorf("in a deep fall %d cells are bold and there are %d heads", deep.bold, deep.heads)
	}

	// A thought that has only begun to deepen does not glint yet.
	var early []text.Color
	for _, hue := range hues {
		early = append(early, text.Blend(pal.Prompt, hue, colour(fallGlintFrom)))
	}
	if n := watch(fallGlintFrom, early, nil); n.glints != 0 {
		t.Errorf("a fall at depth %v glints", fallGlintFrom)
	}
}

// The mark before the words is one cell that turns through its shapes,
// faster in a deep thought, in the colour of the drop that started last:
// pale while calm, the palette's colours and bold when deep.
func TestTheMarkTurns(t *testing.T) {
	th := DefaultTheme(text.TrueColor)
	f, pal := th.Fall, th.Palette
	for _, shape := range f.Marks {
		if text.Width(shape) != 1 {
			t.Errorf("the shape %q is %d cells wide", shape, text.Width(shape))
		}
	}
	type sight struct {
		shapes, colours map[string]bool
		turns, bold     int
	}
	watch := func(depth float64) sight {
		s := sight{shapes: map[string]bool{}, colours: map[string]bool{}}
		last := ""
		runFall(depth, 45, 6*time.Second, 20*time.Millisecond, func(fl *fall) {
			cells := cellsOf(t, fl.mark(&f))
			if len(cells) != 1 {
				t.Fatalf("depth %v: the mark is %d cells: %+v", depth, len(cells), cells)
			}
			shape := string(cells[0].r)
			if shape != last {
				s.turns++
				last = shape
			}
			s.shapes[shape] = true
			s.colours[cells[0].fg] = true
			if cells[0].bold {
				s.bold++
			}
		})
		return s
	}
	calm, deep := watch(0), watch(1)
	for _, shape := range f.Marks {
		if !calm.shapes[shape] || !deep.shapes[shape] {
			t.Errorf("the mark never showed %q (calm %v, deep %v)", shape, calm.shapes, deep.shapes)
		}
	}
	if calm.turns < 30 || deep.turns*2 < calm.turns*3 {
		t.Errorf("in six seconds the mark turned %d times while calm and %d times in a deep thought", calm.turns, deep.turns)
	}
	if calm.bold != 0 || deep.bold == 0 {
		t.Errorf("the mark was bold in %d calm frames and %d deep ones", calm.bold, deep.bold)
	}
	for _, hue := range []text.Color{pal.Aqua, pal.Purple, pal.Pink, pal.Sun} {
		if calm.colours[fg(hue)] {
			t.Errorf("a calm mark shows %v outright", hue)
		}
		if !deep.colours[fg(hue)] {
			t.Errorf("a deep mark never shows %v: %v", hue, deep.colours)
		}
	}
	for c := range calm.colours {
		got, _ := shown{fg: c}.rgb()
		if apart(got, rgbOf(pal.Prompt)) > 120 {
			t.Errorf("a calm mark is %v, far from %v", c, pal.Prompt)
		}
	}
}

// With sixteen colours the words are faint, in the terminal's own colour,
// and what falls is one of the terminal's whites while calm and its
// colours in a deep thought. On a light background the pale colour is
// the terminal's own, not faint, over words in the grey.
func TestFallWithFewColours(t *testing.T) {
	var m text.Metrics
	cells := m.Width(fallWords) + m.Width(fallRest)
	f := DefaultTheme(text.ANSI16).Fall
	if f.Text != text.Default || f.Quiet != text.Default {
		t.Fatalf("with sixteen colours the words stand in %v and %v", f.Text, f.Quiet)
	}
	runFall(0, cells, 10*time.Second, f.Every, func(fl *fall) {
		over := fl.spots(cells)
		for i, c := range cellsOf(t, fl.wash(&f, m, fallWords, fallRest)) {
			switch {
			case !over[i].on && (!c.dim || c.fg != "" || c.bold):
				t.Fatalf("a cell no drop is over is %+v, want faint", c)
			case over[i].on && (c.dim || c.bold):
				t.Fatalf("a cell a calm drop is over is %+v", c)
			case over[i].on && over[i].rank == 0 && c.fg != "97" && c.fg != "37":
				t.Fatalf("a calm head is %+v, want one of the terminal's whites", c)
			}
		}
	})
	var deep strings.Builder
	runFall(1, cells, 10*time.Second, f.EveryDeep, func(fl *fall) { deep.WriteString(fl.wash(&f, m, fallWords, fallRest)) })
	for _, want := range []string{"\x1b[1;96m", "\x1b[1;35m", "\x1b[1;95m", "\x1b[1;33m"} {
		if !strings.Contains(deep.String(), want) {
			t.Errorf("a deep fall with sixteen colours lacks %q", want)
		}
	}

	th := themeFor("light", text.TrueColor, true)
	light := th.Fall
	if light.Bland != text.Default || light.Glint != text.Default || light.Text != th.Palette.Grey {
		t.Fatalf("on a light background the pale colour is %v, the glint %v and the words %v", light.Bland, light.Glint, light.Text)
	}
	heads := 0
	runFall(0, cells, 10*time.Second, light.Every, func(fl *fall) {
		over := fl.spots(cells)
		for i, c := range cellsOf(t, fl.wash(&light, m, fallWords, fallRest)) {
			switch {
			case c.dim || c.bold:
				t.Fatalf("on a light background cell %d is %+v", i, c)
			case !over[i].on && c.fg != fg(th.Palette.Grey):
				t.Fatalf("on a light background a cell no drop is over is %+v", c)
			case over[i].on && over[i].rank == 0:
				heads++
				if c.fg != "" {
					t.Fatalf("on a light background a calm head is %+v, want the terminal's own colour", c)
				}
			}
		}
	})
	if heads == 0 {
		t.Error("on a light background nothing fell")
	}
}
