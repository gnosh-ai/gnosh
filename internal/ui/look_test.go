package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// The prompt mark and the pointer are the blue-green of gnosh.ai, not the
// hot pink, which reads as red on a dark screen.
func TestPromptMarkIsTheSitesBlueGreen(t *testing.T) {
	th := DefaultTheme(text.TrueColor)
	if got := th.Palette.Aqua.String(); got != "#68ffd6" {
		t.Fatalf("the blue-green is %s, and gnosh.ai's is #68ffd6", got)
	}
	const aqua = "38;2;104;255;214"
	for name, styled := range map[string]string{
		"the prompt mark in the scrollback": th.Prompt.Render(">"),
		"the prompt mark in the box":        th.Editor.PromptStyle.Render(">"),
		"the pointer of a list":             th.Menu.PointerStyle.Render(">"),
	} {
		if !strings.Contains(styled, aqua) {
			t.Errorf("%s is drawn as %q, not in the blue-green", name, styled)
		}
	}
}

// working is the line that says what gnosh is doing, as it is written now:
// its cells with their colours.
func (g *rig) working() []shown {
	g.t.Helper()
	g.frame()
	line := g.s.activityLine(g.cols, g.now)
	if line == "" {
		g.t.Fatalf("there is no line that says what gnosh is doing\n%s", g.dump())
	}
	return cellsOf(g.t, line)
}

// lit are the cells of the working line's words that a drop is over: the
// ones in neither of the two dull colours the words stand in.
func (g *rig) lit(line []shown) (at []int) {
	f := &g.s.th.Fall
	for i, c := range line {
		if i >= 2 && c.fg != fg(f.Text) && c.fg != fg(f.Quiet) {
			at = append(at, i)
		}
	}
	return at
}

// While gnosh works the line that says what it is doing moves: the one
// mark before its words turns through its shapes, and colour falls to the
// right through the words, which stay the same words. It stands still
// while a question waits, and with no colour it is the plain mark before
// plain words.
func TestWorkingLineMoves(t *testing.T) {
	g := newRig(t, 80, 24, text.TrueColor)
	g.keys(`"go" <Enter>`).start()
	f := g.s.th.Fall
	const words = "waiting for qwen3.8-27b to start its reply"
	// The cell the fall has got farthest to, frame by frame, for four
	// seconds.
	shapes, places, back, last := map[rune]bool{}, map[int]bool{}, 0, -1
	for range int(4 * time.Second / f.Every) {
		line := g.working()
		if len(line) < 2 || !strings.ContainsRune(strings.Join(f.Marks, ""), line[0].r) || line[1].r != ' ' {
			t.Fatalf("the line does not begin with one mark and a blank: %+v", line[:min(len(line), 3)])
		}
		shapes[line[0].r] = true
		var said strings.Builder
		for _, c := range line[2:] {
			said.WriteRune(c.r)
			if c.r == ' ' && c.fg == "" && !c.dim {
				t.Fatalf("a blank of the words has no colour: the words are not written in their own")
			}
		}
		if !strings.HasPrefix(said.String(), words) {
			t.Fatalf("the words are %q, want them to begin %q", said.String(), words)
		}
		on := g.lit(line)
		if len(on) == 0 {
			t.Fatalf("nothing falls through the words\n%s", g.dump())
		}
		head := on[len(on)-1]
		places[head] = true
		if head < last {
			back++
		}
		last = head
		g.wait(f.Every)
	}
	if len(shapes) < len(f.Marks)-2 {
		t.Errorf("in four seconds the mark showed %d shapes: %v", len(shapes), shapes)
	}
	if len(places) < 15 {
		t.Fatalf("in four seconds the fall stood at %d places: %v\n%s", len(places), places, g.dump())
	}
	// The farthest cell is farther left than the frame before only when
	// the drop before it has left: in four seconds of a calm fall, twice.
	if back > 3 {
		t.Errorf("the fall went back %d times in four seconds", back)
	}
	g.liveLacks("* waiting")

	// The session says when it wants to be woken: soon, while it works.
	if at := g.s.wake(); at.IsZero() || at.Sub(g.now) > f.Every {
		t.Errorf("while it works the session asks to be woken at %v, %v from now", at, at.Sub(g.now))
	}

	// A question that waits for the person does not move.
	g.ev(&agent.QuestionEvent{Question: permission("q1")})
	g.frame()
	g.liveHas("* waiting for your answer").liveLacks(f.Marks...)

	// With no colour the mark is the plain one, and the words plain.
	g = newRig(t, 80, 24, text.NoColor)
	g.keys(`"go" <Enter>`).start()
	g.liveHas("* waiting for").liveLacks(f.Marks...)
	if line := g.s.activityLine(g.cols, g.now); strings.ContainsRune(line, 0x1b) {
		t.Errorf("with no colour the line is written with sequences: %q", line)
	}
}

// A thought that goes on deepens, and soon, and the working line shows
// it: more of the words are coloured, the line says so in words, and the
// session asks to be woken sooner. When the thought is over the fall is
// calm again. (Jae, 18 November 2026: "when 'deep in thought', more things
// drop faster with higher acceleration", and "the transition to
// fasater/more rightward should be faster".)
func TestADeepThoughtShowsInTheLine(t *testing.T) {
	g := newRig(t, 80, 24, text.TrueColor)
	g.keys(`"go" <Enter>`).start()
	g.c.state = agent.State{Busy: true, Phase: agent.PhaseReply}
	g.ev(thought("Let me think.\n"))
	f := g.s.th.Fall
	// How many cells of the words are coloured over two seconds of frames,
	// and how many of the frames have a bold cell.
	watch := func() (cells, bold int) {
		for range 40 {
			line := g.working()
			cells += len(g.lit(line))
			for _, c := range line {
				if c.bold {
					bold++
					break
				}
			}
			g.wait(50 * time.Millisecond)
		}
		return cells, bold
	}
	early, earlyBold := watch()
	g.liveHas("qwen3.8-27b is thinking").liveLacks("deep in thought")
	if earlyBold != 0 {
		t.Errorf("early in a thought %d frames have a bold cell", earlyBold)
	}
	if at := g.s.wake(); at.Sub(g.now) != f.Every {
		t.Errorf("early in a thought the session asks to be woken %v from now, want %v", at.Sub(g.now), f.Every)
	}

	// Fifteen seconds into the thought it is deep.
	g.wait(12 * time.Second)
	g.liveHas("qwen3.8-27b is thinking · 14s").liveLacks("deep in thought")
	g.wait(time.Second)
	g.liveHas("qwen3.8-27b is deep in thought · 15s")
	late, lateBold := watch()
	if late < 3*early {
		t.Errorf("a deep thought colours %d cells in two seconds, and a young one %d", late, early)
	}
	if lateBold != 40 {
		t.Errorf("in a deep thought %d frames of 40 have a bold cell", lateBold)
	}
	if at := g.s.wake(); at.Sub(g.now) != f.EveryDeep {
		t.Errorf("deep in a thought the session asks to be woken %v from now, want %v", at.Sub(g.now), f.EveryDeep)
	}

	// The answer begins: the fall settles, and is calm again.
	g.ev(said("Here it is.\n"))
	g.liveHas("qwen3.8-27b is writing")
	g.wait(time.Second).wait(time.Second)
	if after, afterBold := watch(); after*2 > late || afterBold != 0 {
		t.Errorf("after the thought %d cells are coloured in two seconds (%d frames with a bold one), against %d while it was deep", after, afterBold, late)
	}
	if at := g.s.wake(); at.Sub(g.now) != f.Every {
		t.Errorf("after the thought the session asks to be woken %v from now, want %v", at.Sub(g.now), f.Every)
	}

	// Where the model's name leaves no room for them, the short words.
	g = newRig(t, 60, 24, text.NoColor)
	g.keys(`"go" <Enter>`)
	g.c.state = agent.State{Busy: true, Phase: agent.PhaseReply}
	g.ev(busy(), turnStart(), promptOut("p1", "go"), &agent.RequestEvent{
		Purpose: "main", Round: 1, Attempt: 1, Provider: "local", Model: "a-model-with-a-long-name-of-its-own-v2.5-27b-q8", Messages: 3,
	})
	g.ev(thought("Let me think.\n"))
	g.wait(16 * time.Second)
	g.liveHas("* deep in thought · 16s").liveLacks("is deep in thought")
}

// Folded reasoning shows its last four rows while it arrives, and says
// every time which key writes it out.
func TestThinkingSaysHowToSeeIt(t *testing.T) {
	g := newRig(t, 80, 24, text.NoColor)
	g.keys(`"go" <Enter>`).start()
	g.ev(thought("one\ntwo\nthree\nfour\nfive\nsix\n"))
	g.liveHas("~ thinking · Ctrl+O shows all of it", "three", "four", "five", "six").liveLacks("two")
	g.wait(3*time.Second).ev(said("Done.\n"), replyEnd(core.StopEndTurn))
	g.ev(request(2), thought("again\n"))
	g.wait(2*time.Second).ev(said("Done again.\n"), replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted), idle())
	if n := strings.Count(flat(g.scroll()), "Ctrl+O shows it"); n != 2 {
		t.Fatalf("two folded thoughts name the key %d times, and should name it each time\n%s", n, g.dump())
	}
	g.once("~ thought for 3s · Ctrl+O shows it", "~ thought for 2s · Ctrl+O shows it")
}

// With the verbose option every thought is written out as it comes, and
// nothing is folded.
func TestVerboseWritesThoughtsOut(t *testing.T) {
	g := newRig(t, 80, 40, text.NoColor)
	g.s.detail = true
	g.keys(`"go" <Enter>`).start()
	g.ev(thought("the first thought\n"))
	g.wait(2*time.Second).ev(said("Looking.\n"), replyEnd(core.StopEndTurn))
	g.ev(request(2), thought("the second thought\n"))
	g.wait(3*time.Second).ev(said("Done.\n"), replyEnd(core.StopEndTurn), turnEnd(agent.EndCompleted), idle())
	g.once("the first thought", "the second thought")
	g.committedLacks("Ctrl+O shows it")
}

// The one word exit, or quit, leaves, as /exit does. A sentence that
// begins with it is a prompt.
func TestTheWordExitLeaves(t *testing.T) {
	for _, word := range []string{"exit", "quit", " Exit "} {
		g := newRig(t, 80, 24, text.NoColor)
		g.keys(`"` + word + `" <Enter>`)
		if !g.s.done || g.s.why != "prompt_input_exit" {
			t.Errorf("%q: done %v, reason %q", word, g.s.done, g.s.why)
		}
	}
	g := newRig(t, 80, 24, text.NoColor)
	g.keys(`"exit the loop early" <Enter>`)
	if g.s.done {
		t.Fatal("a prompt that begins with exit left gnosh")
	}
}
