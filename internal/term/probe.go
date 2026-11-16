package term

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// DefaultProbeWait is how long Probe waits for a terminal's answers when
// it is not told otherwise.
const DefaultProbeWait = 300 * time.Millisecond

// Capabilities is what a terminal said it can do. The zero value, nothing,
// is what gnosh assumes of a terminal that did not answer: every terminal
// can do without these.
type Capabilities struct {
	// KittyKeyboard: the terminal knows the kitty keyboard protocol, so
	// Modes.KittyKeyboard may be set and Shift+Enter can be told from
	// Enter.
	KittyKeyboard bool
	// SynchronizedOutput: the terminal knows mode 2026, so
	// Renderer.SetSynchronized may be turned on.
	SynchronizedOutput bool
	// BackgroundKnown says the terminal told its background colour, and
	// LightBackground that it is a light one.
	BackgroundKnown, LightBackground bool
	// Answered is true when the terminal answered the one question
	// every terminal answers. When it is false the other answers may
	// still be on their way (a slow connection); hand later Reply events
	// to Note and they will count.
	Answered bool
}

// probeQuery is the questions Probe asks, in one write. The device
// attributes question comes last: terminals answer in order, so its answer
// means the others have been answered or never will be.
const probeQuery = QueryKittyKeyboard + QuerySynchronizedOutput + QueryBackground + QueryDeviceAttributes

// Note takes in one event. It reports true when the event was one of the
// answers Probe waits for, whether or not it changed anything.
func (c *Capabilities) Note(ev Event) bool {
	r, ok := ev.(Reply)
	if !ok {
		return false
	}
	switch {
	case r.Kind == ReplyKittyKeyboard:
		c.KittyKeyboard = true
	case r.Kind == ReplyMode && r.Private && r.Mode == 2026:
		// "Set" and "reset" both mean the mode is known. "Permanently
		// reset" means it can never be turned on.
		c.SynchronizedOutput = r.State == ModeSet || r.State == ModeReset
	case r.Kind == ReplyString && r.Intro == ']' && strings.HasPrefix(r.Text, "11;"):
		if light, ok := lightColour(strings.TrimPrefix(r.Text, "11;")); ok {
			c.BackgroundKnown, c.LightBackground = true, light
		}
	case r.Kind == ReplyDeviceAttributes:
		c.Answered = true
	default:
		return false
	}
	return true
}

// lightColour reads a colour as terminals give one ("rgb:RRRR/GGGG/BBBB",
// each one to four hex digits) and says whether it is a light one: its
// luminance over a half.
func lightColour(spec string) (light, ok bool) {
	body, found := strings.CutPrefix(spec, "rgb:")
	if !found {
		return false, false
	}
	parts := strings.Split(body, "/")
	if len(parts) != 3 {
		return false, false
	}
	var v [3]float64
	for i, p := range parts {
		if len(p) < 1 || len(p) > 4 {
			return false, false
		}
		n, err := strconv.ParseUint(p, 16, 16)
		if err != nil {
			return false, false
		}
		v[i] = float64(n) / float64(uint64(1)<<(4*len(p))-1)
	}
	return 0.2126*v[0]+0.7152*v[1]+0.0722*v[2] > 0.5, true
}

// Probe asks the terminal what it supports. It writes three questions to
// out (the kitty keyboard flags, whether synchronized output is known, and
// the device attributes) and reads events until the device attributes
// answer arrives, wait runs out, ctx is done or events is closed. A wait
// of zero or less means DefaultProbeWait.
//
// Terminals that know neither feature (tmux before 3.4, Apple's Terminal)
// ignore the first two questions and answer the third at once, so the wait
// is spent in full only on a terminal that answers nothing.
//
// Events that are not answers, such as keys typed before gnosh was ready,
// are returned in the order they came, for the caller to act on. The
// error is ctx's, or the one from writing the questions.
func Probe(ctx context.Context, out io.Writer, events <-chan Event, wait time.Duration) (Capabilities, []Event, error) {
	var caps Capabilities
	if _, err := io.WriteString(out, probeQuery); err != nil {
		return caps, nil, fmt.Errorf("ask the terminal what it supports: %w", err)
	}
	if wait <= 0 {
		wait = DefaultProbeWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var others []Event
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return caps, others, nil
			}
			if !caps.Note(ev) {
				others = append(others, ev)
			}
			if caps.Answered {
				return caps, others, nil
			}
		case <-timer.C:
			return caps, others, nil
		case <-ctx.Done():
			return caps, others, ctx.Err()
		}
	}
}
