package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
)

// While the session waits for its model to start a reply, gnosh asks a
// model server that can say (core.LoadReporter: llama.cpp's /slots) what
// it is doing, every few seconds, and the person is shown it: the prompt
// being read, so many tokens of so many, or the other requests the server
// is busy with. A model server of one's own is often shared, and a long
// wait is then the server's, not the model's (box 1, 16 November: turns
// stopped after 11 to 13 minutes of waiting, with nothing said of why).

// ModeEvent tells the front end the permission mode changed from the
// session's side (a plan approved, EnterPlanMode, a "switch to
// acceptEdits" answer): the screen read it only as a turn ended, and showed
// plan mode while asking about an edit after the plan was approved (seen on
// the devbox's screen).
type ModeEvent struct {
	Mode string
}

// ServerLoadEvent tells the front end what the model server is doing while
// the session waits for a reply to start. Text "" says there is nothing
// more to show.
type ServerLoadEvent struct {
	Text string
}

// loadWatch is the watcher of the request the session waits on.
type loadWatch struct {
	mu   sync.Mutex
	stop chan struct{}
}

// How long a request waits before the server is first asked, and how
// often after.
var (
	loadFirst = 4 * time.Second
	loadEvery = 3 * time.Second
)

// loadMoment starts or stops the watch as the main session's events go
// by: a request to the model starts it, and the first piece of the reply,
// a retry or the turn's end stops it.
func (l *live) loadMoment(ev any) {
	if e, ok := ev.(agent.Event); ok {
		if m := e.EventMeta(); m != nil && m.Agent != "" {
			return // a sub-agent's
		}
	}
	switch e := ev.(type) {
	case *agent.RequestEvent:
		if e.Purpose == "main" {
			l.watchLoad(e)
		}
	case *agent.TextEvent, *agent.ThinkingEvent, *agent.ToolCallEvent, *agent.ReplyEvent, *agent.RetryEvent, *agent.TurnEndEvent:
		l.stopLoad()
	}
}

// watchLoad asks the server of the request what it is doing, until the
// watch is stopped, and tells the front end when that changes.
func (l *live) watchLoad(ev *agent.RequestEvent) {
	l.stopLoad()
	pu := l.providerUsed(ev.Provider)
	if pu == nil {
		return
	}
	lr, ok := pu.prov.(core.LoadReporter)
	if !ok {
		return
	}
	stop := make(chan struct{})
	l.load.mu.Lock()
	l.load.stop = stop
	l.load.mu.Unlock()
	model, estimate := ev.Model, ev.Estimate
	go func() {
		shown := ""
		wait := loadFirst
		for {
			select {
			case <-stop:
				return
			case <-l.ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = loadEvery
			ctx, cancel := context.WithTimeout(l.ctx, 3*time.Second)
			load, err := lr.Load(ctx, model)
			cancel()
			if err != nil {
				return // a server that keeps its slots to itself
			}
			text := loadText(load, estimate)
			select {
			case <-stop:
				return
			default:
			}
			if text != shown {
				shown = text
				l.deliver(&ServerLoadEvent{Text: text})
			}
		}
	}()
}

// stopLoad stops the watch, if one runs.
func (l *live) stopLoad() {
	l.load.mu.Lock()
	defer l.load.mu.Unlock()
	if l.load.stop != nil {
		close(l.load.stop)
		l.load.stop = nil
	}
}

// loadText says what the server is doing, for a request of about estimate
// tokens. The server does not say whose a request is, so this request's
// is taken to be the slot reading a prompt about that long. A server that
// counts a prompt only as far as it has read it gives no such slot until
// the reading is nearly done; there, with a slot standing idle, so that
// this request waits for none, the one slot still reading a prompt not
// yet past this one's length is taken for it. (A Mac Studio, 26
// November: nine minutes of reading this request's own prompt were shown
// as "the server is also answering 1 other request".)
func loadText(load core.ServerLoad, estimate int) string {
	busy, mine, reader, readers := 0, -1, -1, 0
	for i, s := range load.Slots {
		if !s.Busy {
			continue
		}
		busy++
		if mine < 0 && estimate > 0 && s.Prompt > 0 && near(s.Prompt, estimate) {
			mine = i
		}
		if estimate > 0 && !s.Replying && s.Read < s.Prompt && s.Prompt <= estimate+slack(estimate) {
			reader, readers = i, readers+1
		}
	}
	if mine < 0 && readers == 1 && busy < len(load.Slots) {
		mine = reader
	}
	switch {
	case mine >= 0:
		s := load.Slots[mine]
		// The server's count of the prompt, unless it has not counted as
		// far as the session's estimate yet.
		of := kTokens(s.Prompt)
		if estimate > s.Prompt {
			of = "about " + kTokens(estimate)
		}
		text := "reading the prompt: " + kTokens(s.Read) + " of " + of + " tokens"
		if others := busy - 1; others > 0 {
			text += ", beside " + countWords(others, "other request", "other requests")
		}
		return text
	case busy == 0:
		return ""
	case busy == len(load.Slots) && busy == 1:
		return "the server is busy with another request; yours waits"
	case busy == len(load.Slots):
		return fmt.Sprintf("the server's %d slots are all busy with other requests; yours waits", busy)
	}
	return "the server is also answering " + countWords(busy, "other request", "other requests")
}

// near reports whether a server's count of a prompt's tokens is the
// session's estimate of it, as near as the two are made alike.
func near(count, estimate int) bool {
	d := count - estimate
	if d < 0 {
		d = -d
	}
	return d <= slack(estimate)
}

// slack is how far a server's count of a prompt's tokens may stand from
// the session's estimate of it and still be taken for it.
func slack(estimate int) int {
	return max(estimate/4, 500)
}

// kTokens writes a number of tokens shortly: 950, 8.1k, 12k.
func kTokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	s := strconv.FormatFloat(float64(n)/1000, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + "k"
}
