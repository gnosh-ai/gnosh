package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// awayFront is a front end that runs steps, one after another, on the
// session it is given, and then quits.
type awayFront struct {
	steps func(ctx context.Context, c Controls) error
}

func (f *awayFront) Event(any) {}

func (f *awayFront) Run(ctx context.Context, c Controls) int {
	if err := f.steps(ctx, c); err != nil {
		return 1
	}
	c.Quit("prompt_input_exit")
	return 0
}

// runAway runs an interactive session of w with the front end's steps.
func runAway(t *testing.T, w *apptest.World, steps func(ctx context.Context, c Controls) error) {
	t.Helper()
	o := testOptions(w)
	o.StdinIsTerminal, o.StdoutIsTerminal = true, true
	o.Interactive = func(FrontendInfo) Frontend { return &awayFront{steps: steps} }
	runWith(t, t.Context(), o).wantCode(t, 0)
}

// With the awayAfter option on, the interface's word that the person is
// away has the session send itself the next piece of work, hidden, in the
// conversation itself, so that the model has all of it and its server the
// conversation's cache. A reply that says nothing is worth doing rests the
// work: the next word that the person is away sends nothing. With the
// option off, the word does nothing.
func TestAwayWorksOnItsOwnAndRests(t *testing.T) {
	for _, on := range []bool{true, false} {
		w := apptest.NewWorld(t,
			apptest.Reply{Text: "The flaky test races on the cache."},
			apptest.Reply{Text: "I wrote a test that shows the race.\n\nAWAY_IDLE"},
		)
		if on {
			w.Settings(`{"options": {"awayAfter": 60}}`)
		}
		runAway(t, w, func(ctx context.Context, c Controls) error {
			if c.Info().AwayAfter != map[bool]time.Duration{true: time.Minute, false: 0}[on] {
				t.Errorf("on %v: the interface is told to wait %v", on, c.Info().AwayAfter)
			}
			if _, err := c.Submit(agent.Prompt{Text: "look into the flaky test"}); err != nil {
				return err
			}
			if err := c.Wait(ctx); err != nil {
				return err
			}
			c.Away()
			if err := c.Wait(ctx); err != nil {
				return err
			}
			// The work rests now: this sends nothing.
			c.Away()
			time.Sleep(200 * time.Millisecond)
			return c.Wait(ctx)
		})
		reqs := w.Model.Requests()
		if !on {
			if len(reqs) != 1 {
				t.Errorf("with the option off, %d requests, want the person's turn alone", len(reqs))
			}
			continue
		}
		if len(reqs) != 2 {
			t.Fatalf("%d requests, want the person's turn and one turn on its own", len(reqs))
		}
		// The same conversation: the person's prompt and the answer are in
		// it, and the hidden prompt after them.
		has(t, "the request of the turn on its own", string(reqs[1].Body), "look into the flaky test", "The flaky test races on the cache.", "The user is away from the keyboard", "AWAY_IDLE")
		data, _ := os.ReadFile(w.Sessions()[0])
		has(t, "the session's file", string(data), `"hidden":true,"src":"away"`)
	}
}

// The person's next prompt stops a turn the session started on its own at
// once, and goes out next: it does not wait for that turn to end (Jae, 26
// November 2026: "such that it doesn't affect my UX in terms of
// responsiveness").
func TestAwayTurnGivesWayToThePerson(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	arrived := make(chan struct{}, 1)
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "Looked."},
		apptest.Reply{Text: "Working on the next step", Hold: hold, Arrived: func() { arrived <- struct{}{} }},
		apptest.Reply{Text: "I found the race."},
	)
	w.Settings(`{"options": {"awayAfter": 60}}`)
	var took time.Duration
	runAway(t, w, func(ctx context.Context, c Controls) error {
		if _, err := c.Submit(agent.Prompt{Text: "look into the flaky test"}); err != nil {
			return err
		}
		if err := c.Wait(ctx); err != nil {
			return err
		}
		c.Away()
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("the turn on its own never asked the model")
		}
		start := time.Now()
		if _, err := c.Submit(agent.Prompt{Text: "what did you find?"}); err != nil {
			return err
		}
		err := c.Wait(ctx)
		took = time.Since(start)
		return err
	})
	if took > 5*time.Second {
		t.Errorf("the person's prompt was answered %v after it was sent: it waited for the turn on its own", took)
	}
	reqs := w.Model.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want the person's turn, the turn on its own, and the person's next", len(reqs))
	}
	has(t, "the person's next request", string(reqs[2].Body), "what did you find?")
	if !strings.Contains(string(reqs[2].Body), "The user is away from the keyboard") {
		t.Errorf("the person's next request does not hold the stopped turn's prompt: the conversation lost it")
	}
}
