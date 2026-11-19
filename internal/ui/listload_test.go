package ui

import (
	"testing"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// rewindControls are a test's controls that list prompts to go back to,
// and count how often the list was read.
type rewindControls struct {
	*controls
	points []app.RewindPoint
	reads  *int
}

func (c rewindControls) RewindPoints() []app.RewindPoint {
	*c.reads++
	return c.points
}

// The list Esc Esc opens is read away from the loop, so that a long
// session's file does not hold the screen still. What is typed while it
// is read is for the list, as had it come at once: Down and Enter pick in
// it, and do not take a prompt back into the box and send it. Ctrl+C lets
// a list that is slow to come go, and so does a question that comes
// first: what is typed next is the question's.
func TestRewindListIsReadAwayFromTheLoop(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor)
	reads := 0
	g.s.c = rewindControls{g.c, []app.RewindPoint{
		{ID: "u1", Text: "the first prompt", Time: stamp},
		{ID: "u2", Text: "the second prompt", Time: stamp},
	}, &reads}
	g.keys(`"an earlier prompt" <Enter>`)
	g.hold = true

	g.keys(`<Esc> <Esc>`)
	g.liveLacks("Go back to before which prompt?")
	g.keys(`<Down> <Enter>`)
	if g.box() != "" || len(g.c.sent()) != 1 {
		t.Fatalf("keys typed while the list was read went to the box: it holds %q, %d prompts sent", g.box(), len(g.c.sent()))
	}
	g.release()
	g.liveHas("Go back to before: the first prompt", "Go back, the conversation only")
	g.keys(`<Esc>`)
	g.liveLacks("Go back to before")

	g.keys(`<Esc> <Esc> <Ctrl+C>`)
	g.release()
	g.liveLacks("Go back to before which prompt?")
	if g.s.hint != "The list was not opened: Esc Esc opens it again." {
		t.Fatalf("after Ctrl+C the hint is %q", g.s.hint)
	}

	g.keys(`<Esc> <Esc>`)
	g.ev(&agent.QuestionEvent{Question: permission("q1")})
	g.release()
	g.liveHas("Allow Bash?").liveLacks("Go back to before which prompt?")
	if g.s.hint != "A question came first: Esc Esc opens the list again." {
		t.Fatalf("after a question came the hint is %q", g.s.hint)
	}
	if reads != 3 {
		t.Fatalf("the list was read %d times, want 3", reads)
	}
}
