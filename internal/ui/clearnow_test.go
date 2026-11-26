package ui

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// sideLetGo says whether a /btw of withClear was let go.
var sideLetGo atomic.Bool

// withClear gives the rig's session a /clear, by its name and its
// aliases, that opens a new session, and a /btw that waits for its answer
// until it is let go (sideLetGo), or answers after five seconds.
func withClear(c *controls) {
	sideLetGo.Store(false)
	c.info.Commands = append(c.info.Commands, "clear", "btw")
	c.info.CommandList = append(c.info.CommandList, app.CommandInfo{Name: "clear", Aliases: []string{"new", "reset"}})
	c.command = func(ctx context.Context, line string) (app.CommandResult, error) {
		if strings.HasPrefix(line, "/btw") {
			select {
			case <-ctx.Done():
				sideLetGo.Store(true)
				return app.CommandResult{Handled: true, Name: "btw"}, ctx.Err()
			case <-time.After(5 * time.Second):
				return app.CommandResult{Handled: true, Name: "btw", Text: "The side answer."}, nil
			}
		}
		return app.CommandResult{Handled: true, Name: "clear", Text: "A new session is open."}, nil
	}
}

// /clear typed while a turn runs stops the turn instead of waiting for it,
// and runs as soon as the turn's end is in. What the person queued comes
// back into the box, so that none of it starts another turn in the
// session being left (Jae, 26 November 2026: "/clear doesn't work when
// it's thinking on a previous prompt... i have to wait. i don't want
// to").
func TestClearDoesNotWaitForTheTurn(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor, withClear)
	g.keys(`"first" <Enter>`).start().ev(said("Thinking about it. "))
	g.keys(`"second thing" <Enter>`)
	g.c.queue = queueAs("p2", "second thing")
	g.ev(&agent.QueueEvent{Queued: g.c.queue})
	g.keys(`"/new" <Enter>`)
	if g.c.stops != 1 {
		t.Fatalf("/new during a turn: %d stops, want 1", g.c.stops)
	}
	if len(g.c.commands) != 0 {
		t.Fatalf("/new ran before the turn had stopped: %q", g.c.commands)
	}
	if g.box() != "second thing" {
		t.Fatalf("the box holds %q, want the queued prompt back in it", g.box())
	}
	if len(g.c.queue) != 0 {
		t.Fatalf("the queued prompt is still on the session's queue: %v", g.c.queue)
	}
	g.liveHas("queued: /new")
	g.ev(turnEnd(agent.EndStopped), idle())
	if want := []string{"/new"}; !slices.Equal(g.c.commands, want) {
		t.Fatalf("commands run: %q, want %q", g.c.commands, want)
	}
	g.once("A new session is open.")
	g.liveLacks("queued:")
	if got := g.c.sent(); len(got) != 2 {
		t.Fatalf("prompts submitted: %q; the one taken back was sent again", got)
	}
	if g.box() != "second thing" {
		t.Fatalf("after the new session opened the box holds %q", g.box())
	}
}

// A turn that starts after the one /clear stopped, before the session has
// gone idle (a prompt of gnosh's own), is stopped too. Once /clear has
// run, a turn is a turn again.
func TestClearStopsTheTurnsThatFollow(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor, withClear)
	g.keys(`"first" <Enter>`).start()
	g.keys(`"/clear" <Enter>`)
	g.ev(turnEnd(agent.EndStopped), turnStart())
	if g.c.stops != 2 {
		t.Fatalf("%d stops, want 2: the turn after the one /clear stopped went on", g.c.stops)
	}
	if len(g.c.commands) != 0 {
		t.Fatalf("/clear ran before the session was idle: %q", g.c.commands)
	}
	g.ev(turnEnd(agent.EndStopped), idle())
	if want := []string{"/clear"}; !slices.Equal(g.c.commands, want) {
		t.Fatalf("commands run: %q, want %q", g.c.commands, want)
	}
	g.keys(`"next" <Enter>`).start()
	if g.c.stops != 2 {
		t.Fatalf("a turn after /clear had run was stopped (%d stops)", g.c.stops)
	}
}

// A /clear taken back out of the queue (Up) stops nothing more.
func TestClearTakenBackStopsNothingMore(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor, withClear)
	g.keys(`"first" <Enter>`).start()
	g.keys(`"/clear" <Enter>`)
	g.keys(`<Up>`)
	if g.box() != "/clear" {
		t.Fatalf("Up brought back %q, want /clear", g.box())
	}
	g.ev(turnEnd(agent.EndStopped), turnStart())
	if g.c.stops != 1 {
		t.Fatalf("%d stops after /clear was taken back, want 1", g.c.stops)
	}
	g.ev(turnEnd(agent.EndCompleted), idle())
	if len(g.c.commands) != 0 {
		t.Fatalf("/clear ran after it was taken back: %q", g.c.commands)
	}
}

// /clear does not wait behind a side question that waits for the model
// either: that is let go, and /clear is next, as /exit is.
func TestClearDoesNotWaitBehindASideQuestion(t *testing.T) {
	g := newRig(t, 100, 24, text.NoColor, withClear)
	// (The commands are held until released, as they run away from the
	// loop.)
	g.hold = true
	g.keys(`"/btw is it done?" <Enter>`)
	g.keys(`"/clear" <Enter>`)
	g.release()
	g.release()
	if !sideLetGo.Load() {
		t.Fatal("/clear waited for the side question's answer instead of letting it go")
	}
	if want := []string{"/btw is it done?", "/clear"}; !slices.Equal(g.c.commands, want) {
		t.Fatalf("commands run: %q, want %q", g.c.commands, want)
	}
	if g.c.stops != 0 {
		t.Fatalf("/clear with no turn running stopped a turn (%d stops)", g.c.stops)
	}
	g.once("A new session is open.")
}
