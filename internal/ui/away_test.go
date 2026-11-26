package ui

import (
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// With the awayAfter option on, the session is told that the person is
// away once its time has gone by with the session idle and no key pressed:
// once for each time away, never while a turn runs, and a key or a turn's
// end starts the count again (Jae, 26 November 2026: "i don't want the
// studio to be idle for long").
func TestAwayIsToldOncePerTimeAway(t *testing.T) {
	g := newRig(t, 80, 24, text.NoColor, func(c *controls) { c.info.AwayAfter = time.Minute })
	aways := func() int {
		g.c.mu.Lock()
		defer g.c.mu.Unlock()
		return g.c.aways
	}
	settled := func(want int, when string) {
		t.Helper()
		g.s.tick()
		if at := g.s.wake(); !at.IsZero() && !at.After(g.now) {
			t.Fatalf("%s, the session asks to be woken at %v, which is not after now (%v): the loop would spin", when, at, g.now)
		}
		if got := aways(); got != want {
			t.Fatalf("%s, the session was told %d times that the person is away, want %d", when, got, want)
		}
	}

	// From the start, with nothing typed.
	g.wait(59 * time.Second)
	settled(0, "59 s after the start")
	g.wait(2 * time.Second)
	settled(1, "61 s after the start")
	g.wait(5 * time.Minute)
	settled(1, "6 minutes after the start, with no key")

	// A key: the person is back, and the count starts over.
	g.keys(`"x"`)
	g.wait(59 * time.Second)
	settled(1, "59 s after a key")
	g.wait(2 * time.Second)
	settled(2, "61 s after a key")

	// While a turn runs, never; a minute after it ends, again.
	g.keys(`<Enter>`).start()
	g.wait(3 * time.Minute)
	settled(2, "3 minutes into a turn")
	g.ev(turnEnd(agent.EndCompleted), idle())
	g.wait(59 * time.Second)
	settled(2, "59 s after the turn ended")
	g.wait(2 * time.Second)
	settled(3, "61 s after the turn ended")
}

// With the option off, the session is never told.
func TestAwayIsNotToldWhenOff(t *testing.T) {
	g := newRig(t, 80, 24, text.NoColor)
	g.wait(time.Hour)
	if at := g.s.wake(); !at.IsZero() && at.Sub(g.now) < time.Minute {
		t.Errorf("with awayAfter off the session asks to be woken in %v", at.Sub(g.now))
	}
	g.c.mu.Lock()
	defer g.c.mu.Unlock()
	if g.c.aways != 0 {
		t.Errorf("with awayAfter off the session was told %d times that the person is away", g.c.aways)
	}
}

// A turn the session started on its own while the person was away says so
// on its working line, and a turn of the person's does not.
func TestAwayTurnSaysSoOnItsWorkingLine(t *testing.T) {
	const words = "on its own while you are away"
	g := newRig(t, 100, 24, text.NoColor, func(c *controls) { c.info.AwayAfter = time.Minute })
	g.ev(busy(), turnStart(), &agent.PromptEvent{ID: "a1", Prompt: agent.Prompt{ID: "a1", Text: "carry on", Hidden: true, Own: true, Source: "away"}})
	g.liveHas(words)
	g.ev(turnEnd(agent.EndStopped), idle())
	g.liveLacks(words)
	g.keys(`"what did you find?" <Enter>`).start()
	g.liveLacks(words)
}
