package ui

import (
	"math"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/text"
)

// While gnosh works, the line that says what it is doing has two parts
// that move: one mark before the words, a spark that swells and dwindles
// in its one cell, and the words themselves, through which colour falls
// to the right as if gravity ran sideways. The words never change for it:
// they stand in a dull colour of their own, and what falls is another
// colour passing over their letters. A drop of it starts at the first
// letter, slowly, gathers speed all the way along the line and leaves at
// its end, its tail stretching as it goes faster.
//
// How it falls says how deep the model is in a thought. While nothing is
// thought for long, one pale drop at a time crosses the words. A thought
// that goes on deepens, and soon: first the drops take colours of their
// own, then more of them fall, under a stronger pull, until in a deep
// thought three run through the line at once in the palette's four
// colours, with glints among them. The words say the same ("is deep in
// thought"), so nothing is said by colour alone.
//
// Jae asked for it. 18 November 2026: "do something like the yellow ->
// deep in thought thing except keep it multi colored sparkly ... start with
// more boring bland even colors first ... movement like dropping rightward,
// as if accelerating to the right, like gravity goes sideways, and when
// 'deep in thought', more things drop faster with higher acceleration."
// And of the strip of marks that was built from that, the same day: "make
// it two part (it already is) where the first part is like claude, i think
// a single letter, and then the second part is the text that starts
// non-white and the whole text is animated. the thing that moves rightward
// is a different color of the text, but the text is always the same... and
// the transition to fasater/more rightward should be faster".

// Fall is the look of the working line while gnosh works.
type Fall struct {
	// Marks are the shapes the one mark before the words turns through,
	// over and over: a spark that swells and dwindles. Each is one cell
	// wide.
	Marks []string
	// Every is how long one step of the fall lasts while it is calm, and
	// EveryDeep in the deepest thought, when the drops are fastest.
	Every, EveryDeep time.Duration
	// Plain is what the mark and the words are written in apart from
	// their colour.
	Plain text.Style
	// Text is the colour the words stand in while nothing passes over
	// them, and Quiet that of what follows them on the line (how long,
	// which key stops). Where one is the terminal's own colour, it is
	// written faint.
	Text, Quiet text.Color
	// Bland is the colour of what falls while the fall is calm, and Hues
	// are the colours the drops take as a thought deepens: a drop's head
	// one of them, and its tail the ones after it, fading into the words.
	// Glint is what a head flashes toward in a deep thought.
	Bland text.Color
	Hues  []text.Color
	Glint text.Color
}

// moves reports whether there is a fall to draw: with no colour there is
// none, and the plain mark stands still before plain words.
func (f *Fall) moves() bool {
	return len(f.Marks) > 0 && len(f.Hues) > 0 && f.Every > 0
}

// every is how long one step lasts at a depth.
func (f *Fall) every(depth float64) time.Duration {
	if f.EveryDeep <= 0 || f.EveryDeep >= f.Every {
		return f.Every
	}
	return f.Every - time.Duration(depth*float64(f.Every-f.EveryDeep))
}

// A thought begins to deepen once it has gone on for deepenAfter, and is
// deep at deepAfter.
const (
	deepenAfter = 3 * time.Second
	deepAfter   = 15 * time.Second
)

// depthOf says how deep a thought is that has lasted d: 0 while it is
// young, 1 once it is deep, and evenly between.
func depthOf(d time.Duration) float64 {
	return min(max(float64(d-deepenAfter)/float64(deepAfter-deepenAfter), 0), 1)
}

// How the drops fall, in cells and seconds: each pair is the calm fall's
// figure and the deepest thought's, and a depth between has the figure that
// far between them.
const (
	// The pull to the right, in cells a second every second.
	fallPull, fallPullDeep = 8.0, 40.0
	// The speed a drop starts with, in cells a second: slowly.
	fallPush, fallPushDeep = 2.0, 4.0
	// How many drops are on their way along the line at once, on average.
	// More than three and so much of the line is coloured that nothing in
	// it is seen to move.
	fallMany, fallManyDeep = 1.0, 3.0
	// No drop goes faster than this, however long the line: faster, and
	// it would be seen in a different place each frame and not to move.
	fallFastest = 60.0

	// A drop's tail is one cell, and as many more as the drop crosses in
	// fallTail seconds, up to fallTailMost. Each colour of a tail is
	// fallTailBand cells long.
	fallTail     = 0.14
	fallTailMost = 8
	fallTailBand = 4
	// fallDim is how far from the bland colour toward its own a drop
	// stands while the fall is calm; in the deepest thought it is all the
	// way there.
	fallDim = 0.1
	// The heads, and the mark, are bold from this much colour on.
	fallBold = 0.5
	// Glints begin at the depth fallGlintFrom; in the deepest thought a
	// head has the chance fallGlintMost of flashing, drawn again every
	// fallGlintEvery seconds; a flash goes fallGlintTo of the way to the
	// glint colour.
	fallGlintFrom  = 0.35
	fallGlintMost  = 0.3
	fallGlintEvery = 0.09
	fallGlintTo    = 0.6
	// The mark turns through this many of its shapes in a second.
	fallTurn, fallTurnDeep = 6.0, 12.0

	// fallSettle is how long the deepest fall takes to grow calm again
	// once its thought is over.
	fallSettle = 1500 * time.Millisecond
	// The drops are moved fallStride at a time at most, so that a late
	// frame moves them as several on time would have.
	fallStride = 40 * time.Millisecond
	// A fall that was not drawn for fallGap begins again.
	fallGap = time.Second
)

// between is the figure a fraction t of the way from a to b.
func between(a, b, t float64) float64 { return a + (b-a)*t }

// crossing is how many seconds a drop takes along cells cells, starting
// at the speed push under the pull pull, and never faster than
// fallFastest.
func crossing(pull, push, cells float64) float64 {
	// How long, and how far, until it is as fast as a drop goes.
	t := max(fallFastest-push, 0) / pull
	if far := push*t + pull*t*t/2; cells > far {
		return t + (cells-far)/fallFastest
	}
	return (math.Sqrt(push*push+2*pull*cells) - push) / pull
}

// colour is how far from the bland colour toward its own a drop stands at
// a depth: fallDim while calm, all the way in the deepest thought, and
// most of the way well before that, so that colour is the first thing a
// thought that goes on brings.
func colour(depth float64) float64 {
	return between(fallDim, 1, 1-(1-depth)*(1-depth))
}

// drop is one falling stretch of colour: its head and its tail.
type drop struct {
	// x is where its head is, in cells from the first letter of the
	// words, and v how fast it falls, in cells a second.
	x, v float64
	// n is which drop of the fall it is: it chooses its colours and its
	// glints.
	n uint32
}

// tail is how many cells of tail the drop has at its speed.
func (d *drop) tail() int {
	return min(1+int(d.v*fallTail), fallTailMost)
}

// fall is the drops as they stand at one moment. Its zero value is a fall
// that has not begun.
type fall struct {
	// at is the moment the drops stand at.
	at time.Time
	// depth is how deep the thought is as the fall shows it: it follows
	// the thought up at once and comes down over fallSettle.
	depth float64
	// ran is how long the fall has run, in seconds, and turned how far
	// the mark has turned, in shapes.
	ran, turned float64
	drops       []drop
	// owed is how much of the next drop has come due: at need it starts.
	owed, need float64
	// born is how many drops have started.
	born uint32
	// seed is the state of the dice that set the drops a little apart.
	seed uint32
}

// step moves the fall on to the moment now, with a thought as deep as
// depth, along words that are cells cells wide. The same moment twice
// moves nothing, so that drawing a frame again draws the same frame.
func (fl *fall) step(now time.Time, depth float64, cells int) {
	depth = min(max(depth, 0), 1)
	width := float64(max(cells, 1))
	dt := now.Sub(fl.at)
	if fl.at.IsZero() || dt > fallGap || dt < 0 {
		// The first frame, or the first after a while (the session was
		// idle, or a question stood in the fall's place): it begins
		// again, with its first drop on its way.
		*fl = fall{at: now, depth: depth, drops: fl.drops[:0], seed: 0x9E3779B9}
		fl.start(depth)
		return
	}
	fl.at = now
	for dt > 0 {
		h := min(dt, fallStride)
		dt -= h
		s := h.Seconds()
		if depth >= fl.depth {
			fl.depth = depth
		} else {
			fl.depth = max(depth, fl.depth-s/fallSettle.Seconds())
		}
		fl.ran += s
		fl.turned += s * between(fallTurn, fallTurnDeep, fl.depth)
		pull := between(fallPull, fallPullDeep, fl.depth)
		push := between(fallPush, fallPushDeep, fl.depth)
		keep := fl.drops[:0]
		for _, d := range fl.drops {
			v := min(d.v+pull*s, fallFastest)
			d.x += (d.v + v) / 2 * s
			d.v = v
			if d.x-float64(d.tail()) < width {
				// Its head, or some of its tail, is still on the words.
				keep = append(keep, d)
			}
		}
		fl.drops = keep
		// As many drops start as keep the right number on their way.
		fl.owed += s * between(fallMany, fallManyDeep, fl.depth) / crossing(pull, push, width)
		if fl.owed >= fl.need || len(fl.drops) == 0 {
			// The words are never without a drop for longer than a
			// stride.
			fl.start(fl.depth)
		}
	}
}

// start lets the next drop go, and draws how long the one after it waits:
// between six and fourteen tenths of the usual time, so that the drops do
// not fall in step.
func (fl *fall) start(depth float64) {
	fl.drops = append(fl.drops, drop{v: between(fallPush, fallPushDeep, depth), n: fl.born})
	fl.born++
	fl.owed = 0
	fl.need = 0.6 + 0.8*fl.roll()
}

// roll is the next throw of the fall's dice, from 0 up to 1. The dice are
// its own and start the same way every time, so that a fall is the same
// fall whenever the clock is: a test can say what it shows.
func (fl *fall) roll() float64 {
	x := fl.seed
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	fl.seed = x
	return float64(x>>8) / (1 << 24)
}

// spot is what passes over one cell of the words: which drop, which cell
// of it counted from its head, and how long its tail is.
type spot struct {
	on   bool
	n    uint32
	rank int
	tail int
}

// spots lays the drops out over cells cells of words. Where two drops
// meet, the cell nearer its own head shows, and of two as near the one
// that started first.
func (fl *fall) spots(cells int) []spot {
	over := make([]spot, max(cells, 0))
	for i := range fl.drops {
		d := &fl.drops[i]
		head, tail := int(d.x), d.tail()
		for rank := 0; rank <= tail; rank++ {
			c := head - rank
			if c < 0 || c >= len(over) {
				continue
			}
			if at := &over[c]; !at.on || rank < at.rank {
				*at = spot{on: true, n: d.n, rank: rank, tail: tail}
			}
		}
	}
	return over
}

// mark writes the mark before the words as it stands: the shape it has
// turned to, in the colour of the drop that started last.
func (fl *fall) mark(f *Fall) string {
	much := colour(fl.depth)
	c := text.Blend(f.Bland, f.Hues[int((fl.born+uint32(len(f.Hues))-1)%uint32(len(f.Hues)))], much)
	st := f.lit(c)
	if much >= fallBold {
		st = st.Bold()
	}
	return st.Render(f.Marks[int(fl.turned)%len(f.Marks)])
}

// wash writes the words of the line, and after them the rest of it, with
// the fall passing over both: every letter as it is, in its own dull
// colour where no drop is and in the drop's where one is. Letters that
// follow each other in one colour are written as one piece.
func (fl *fall) wash(f *Fall, m text.Metrics, words, rest string) string {
	over := fl.spots(m.Width(words) + m.Width(rest))
	var b strings.Builder
	var run strings.Builder
	var style text.Style
	flush := func() {
		if run.Len() > 0 {
			b.WriteString(style.Render(run.String()))
			run.Reset()
		}
	}
	cell := 0
	for _, part := range [2]struct {
		s    string
		base text.Color
	}{{words, f.Text}, {rest, f.Quiet}} {
		for g := range text.Graphemes(part.s) {
			st := f.dull(part.base)
			if cell < len(over) && over[cell].on {
				st = f.style(over[cell], part.base, fl.depth, fl.glints(over[cell]))
			}
			if st != style {
				flush()
				style = st
			}
			run.WriteString(g)
			cell += m.Width(g)
		}
	}
	flush()
	return b.String()
}

// dull is the style of a letter no drop is over: its own colour, or
// faint where that is the terminal's own.
func (f *Fall) dull(c text.Color) text.Style {
	if c == text.Default {
		return f.Plain.Dim()
	}
	return f.Plain.Fg(c)
}

// lit is the style of what a drop colours, or of the mark: the colour, or
// the terminal's own, never faint.
func (f *Fall) lit(c text.Color) text.Style {
	return f.Plain.Fg(c)
}

// style is the style of a letter a drop is over, at a depth. The drop's
// head has one of the fall's colours, as far from the bland one as the
// depth has brought it; each stretch of its tail has the next colour,
// fading into base, the letter's own; a head is bold once it has colour
// enough; and a head that glints goes most of the way to the glint
// colour.
func (f *Fall) style(at spot, base text.Color, depth float64, glint bool) text.Style {
	much := colour(depth)
	hue := f.Hues[(int(at.n%uint32(len(f.Hues)))+at.rank/fallTailBand)%len(f.Hues)]
	c := text.Blend(f.Bland, hue, much)
	switch {
	case at.rank != 0:
		return f.lit(text.Blend(c, base, float64(at.rank)/float64(at.tail+1)))
	case glint:
		return f.lit(text.Blend(c, f.Glint, fallGlintTo)).Bold()
	case much >= fallBold:
		return f.lit(c).Bold()
	}
	return f.lit(c)
}

// glints reports whether the head at a spot flashes now. The chance grows
// with the depth from fallGlintFrom on, and is thrown again for every drop
// every fallGlintEvery seconds.
func (fl *fall) glints(at spot) bool {
	if at.rank != 0 || fl.depth <= fallGlintFrom {
		return false
	}
	chance := fallGlintMost * (fl.depth - fallGlintFrom) / (1 - fallGlintFrom)
	// A throw that depends on nothing but which drop it is and when.
	x := at.n*0x9E3779B1 ^ uint32(fl.ran/fallGlintEvery)*0x85EBCA6B
	x ^= x >> 15
	x *= 0x2C1B3C6D
	x ^= x >> 12
	x *= 0x297A2D39
	x ^= x >> 15
	return float64(x>>8)/(1<<24) < chance
}
