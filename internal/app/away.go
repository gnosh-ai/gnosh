package app

import (
	"slices"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

// This file is the session's work while the person is away (the awayAfter
// option; Jae, 26 November 2026: "i want ssh studio to always be doing
// something in the background after an idle like a minute... such that it
// doesn't affect my UX in terms of responsiveness... i don't want the
// studio to be idle for long"). When the interface has seen the session
// idle, with no key pressed, for that long, it calls Away, and the session
// sends itself a hidden prompt: take the next step of the recent work, or
// the most pressing open todo in mem. The turn runs in the conversation
// itself, so the model has all of it, and a model server of one's own
// keeps the conversation where it was: work in a conversation beside it
// would take that place among the server's slots, and the person's next
// prompt would wait while the server read the whole conversation again.
// The person's next prompt stops such a turn at once and goes out on what
// the server kept. A reply that says nothing is worth doing, a turn that
// failed, and Esc rest the work for a while.

// awayRest is how many of the option's waits the session lets pass before
// it works on its own again after a turn that rested it: ten minutes, at a
// minute.
const awayRest = 10

// awayNotice is the line the person sees as such a turn starts.
const awayNotice = "Working on its own while you are away: your next prompt stops it, and what it did is kept."

// awayState is the work done while the person is away. It is guarded by
// its lock.
type awayState struct {
	mu sync.Mutex
	// id is the hidden prompt sent last, until its turn has ended, and
	// yielded says the person's prompt stopped that turn.
	id      string
	yielded bool
	// until is when the session may work on its own again after a turn
	// that rested it.
	until time.Time
}

// awayAfter is how long the session sits idle with no key pressed before
// it works on its own (the awayAfter option), in a session a person
// attends; zero for never.
func (l *live) awayAfter() time.Duration {
	if !l.interactive || l.cfg == nil {
		return 0
	}
	return time.Duration(max(l.cfg.Options.AwayAfter.V, 0)) * time.Second
}

// Away is the interface's word that the person seems to be away (see
// awayStart).
func (c *controls) Away() { c.cur().awayStart() }

// awayStart sends the session its next piece of work while the person is
// away, unless the option is off, the work rests, plan mode is on (the
// model may look and not change), /loop already keeps the session working,
// or anything else is in hand or waiting.
func (l *live) awayStart() {
	if l.awayAfter() <= 0 || l.engine.Mode() == permission.ModePlan || l.currentLoop() != nil {
		return
	}
	a := l.agentReady.Load()
	if a == nil {
		return
	}
	if st := a.State(); st.Busy || st.Queued > 0 || st.Loop {
		return
	}
	l.away.mu.Lock()
	rest := l.p.now().Before(l.away.until)
	l.away.mu.Unlock()
	if rest {
		return
	}
	id, err := a.Submit(agent.Prompt{Text: l.texts.Away(), Hidden: true, Own: true, Source: "away", Notice: awayNotice})
	if err != nil {
		return
	}
	l.away.mu.Lock()
	l.away.id, l.away.yielded = id, false
	l.away.mu.Unlock()
}

// awayYield is told, before a prompt the person sent goes in, that they
// are back: the work rests no more, and it reports whether a turn the
// session started on its own is under way, which is to stop so that the
// prompt goes out at once. (With the session idle, a prompt recorded as
// sent is one whose end was never told, as one withdrawn before it ran;
// it is forgotten, so that the person's own turn is never the one
// stopped.)
func (l *live) awayYield(a *agent.Agent) bool {
	l.away.mu.Lock()
	defer l.away.mu.Unlock()
	l.away.until = time.Time{}
	if l.away.id == "" {
		return false
	}
	if a == nil || !a.State().Busy {
		l.away.id = ""
		return false
	}
	l.away.yielded = true
	return true
}

// awayStopped is told that the person stopped a turn (Esc): one the
// session started on its own rests the work, since they want it quiet
// for now.
func (l *live) awayStopped() {
	l.away.mu.Lock()
	defer l.away.mu.Unlock()
	if l.away.id != "" && !l.away.yielded {
		l.away.until = l.p.now().Add(awayRest * l.awayAfter())
	}
}

// awayTurnEnded follows a turn the session started on its own as it ends:
// one that found nothing worth doing, or failed, rests the work.
func (l *live) awayTurnEnded(res agent.TurnResult) {
	l.away.mu.Lock()
	defer l.away.mu.Unlock()
	if l.away.id == "" || !slices.Contains(res.Prompts, l.away.id) {
		return
	}
	l.away.id, l.away.yielded = "", false
	if res.Reason == agent.EndFailed || res.Reason == agent.EndCompleted && prompt.HasMarkerLine(res.Text, prompt.AwayIdle) {
		l.away.until = l.p.now().Add(awayRest * l.awayAfter())
	}
}
