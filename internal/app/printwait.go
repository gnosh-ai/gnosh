package app

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/term"
)

// This file is a run's wait for the background work it started
// (spec/cc/cli.md 4.5). Once the run's turn has ended, the run stays open
// while a background command or a sub-agent in the background still
// runs. When one ends, its notice wakes the model, and the turn that
// starts is the run's too: text output prints each turn's reply, and the
// JSON formats report the last turn. Claude Code stops what is left after
// ten minutes of idle waiting; gnosh does not give up on a clock, so every
// ten minutes of idle waiting it asks the model instead, which may stop
// the work with TaskStop or wait on. When the model cannot be asked (the
// run's --max-turns are spent), what is left is stopped then, as Claude
// Code's wait stops it. CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS
// still sets a ceiling for whoever wants one (a number of milliseconds;
// 0 waits with no checkpoint).
//
// When nothing is left to wait for, a run whose commands left processes
// running (a server started with nohup ... &), which its end stops, tells
// the model so once and gives it a turn to keep one with setsid: the
// commands' results said so too, and a model that took no notice of it
// lost a FrontierHarness task, its server stopped before it was graded,
// that Pi passed with the same command.

// backgrounder is what a session's controls may also be: able to say what
// still runs in the background, to put a checkpoint to the model (or say
// why it cannot), and to stop the lot; and to say what its commands left
// running, and tell the model of it.
type backgrounder interface {
	BackgroundWork() (running []string, ever bool)
	CheckBackground(waited time.Duration, work []string) error
	StopBackground()
	LeftRunning() []string
	TellLeftRunning(left []string) bool
}

// The pace of the wait: how often it looks, how long all must be quiet
// before the run ends, when it says on a terminal that it waits, and how
// often it asks the model.
const (
	waitLook   = 200 * time.Millisecond
	waitSettle = time.Second
	waitSay    = 5 * time.Second
	// wakeLost is how long a note that was to wake the model may go
	// without a turn starting before the run stops waiting for one.
	wakeLost = 10 * time.Second
)

// waitCheckpoint is how long the run waits idle before it asks the model
// (a variable for the tests).
var waitCheckpoint = 10 * time.Minute

// awaitBackground keeps the run open for the background work its turns
// started, and returns the last turn's result. In stream-json output each
// turn's result line is written as the turn ends; written says the line
// of res, the turn the wait begins after, already was.
func (f *printer) awaitBackground(ctx context.Context, c Controls, res agent.TurnResult, written bool) (agent.TurnResult, os.Signal, agent.State) {
	b, ok := c.(backgrounder)
	if !ok {
		return res, nil, agent.State{}
	}
	// told says the model was told what the run's end stops, and woken
	// that the turn it was woken for has not ended yet (since wokenAt).
	told, woken, wokenAt := false, false, time.Time{}
	ended := func(r agent.TurnResult) bool {
		return r.Reason == agent.EndCompleted || r.Reason == agent.EndModelStopped
	}
	if _, ever := b.BackgroundWork(); !ever && f.inboxed.Load() == 0 {
		// (A prompt that came while the turn ran, from the agent view's
		// inbox, is waited for as background work is.)
		left := b.LeftRunning()
		if len(left) == 0 || !ended(res) || !b.TellLeftRunning(left) {
			return res, nil, agent.State{}
		}
		told, woken, wokenAt = true, true, time.Now()
	}
	if f.format == cli.FormatStreamJSON {
		f.mu.Lock()
		f.resultsOut = true
		f.mu.Unlock()
		if !written {
			f.emitResult(c, res)
		}
	}
	ceiling, checkpoints := f.waitCeiling()
	signals := c.Signals()
	tick := time.NewTicker(waitLook)
	defer tick.Stop()
	idleSince, quietSince, said := time.Now(), time.Time{}, false
	for {
		select {
		case r := <-f.results:
			// A turn that background work started has ended: its reply
			// stands apart from the next one's, and its result line is
			// written.
			f.closeReply()
			if f.format == cli.FormatStreamJSON {
				f.emitResult(c, r)
			}
			if !woken {
				// (The turn the model was told what the end stops in is
				// gnosh's own: the run's result stays the answer to the
				// prompt, which that turn's reply ("Restarted the server
				// with setsid") had taken the place of, and its end is no
				// reason for the run to fail.)
				res = r
			}
			idleSince, quietSince, woken = time.Now(), time.Time{}, false
			continue
		case s := <-signals:
			st := c.State()
			c.Interrupt()
			return res, s, st
		case <-ctx.Done():
			return res, nil, agent.State{}
		case <-tick.C:
		}
		st := c.State()
		if woken && !st.Busy && st.Queued == 0 && time.Since(wokenAt) >= wakeLost {
			// The note never started a turn (the session is closing).
			woken = false
		}
		if st.Busy || st.Queued > 0 || woken {
			idleSince, quietSince = time.Now(), time.Time{}
			continue
		}
		work, _ := b.BackgroundWork()
		if len(work) == 0 {
			// Nothing runs and nothing is coming, unless the end of the
			// last piece of work is still on its way to the model: all
			// must stay quiet for a moment.
			if quietSince.IsZero() {
				quietSince = time.Now()
				continue
			}
			if time.Since(quietSince) < waitSettle {
				continue
			}
			if left := b.LeftRunning(); !told && len(left) > 0 && ended(res) && b.TellLeftRunning(left) {
				told, woken, wokenAt, quietSince = true, true, time.Now(), time.Time{}
				continue
			}
			return res, nil, st
		}
		quietSince = time.Time{}
		waited := time.Since(idleSince)
		if !said && waited >= waitSay {
			said = true
			if f.p.stderrIsTerminal() {
				f.stderr("Waiting for background work to finish: %s", strings.Join(work, ", "))
			}
		}
		switch {
		case ceiling > 0 && waited >= ceiling:
			f.stderr("the run waited %s for background work and stops it now (CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS): %s", roundTime(waited), strings.Join(work, ", "))
			b.StopBackground()
			return res, nil, st
		case checkpoints && waited >= f.checkpointEvery():
			if err := b.CheckBackground(waited, work); err != nil {
				// A turn started now would be stopped before its first
				// request, and the next checkpoint would ask again: the
				// run never ended (the print mode review of 2026-11-19).
				f.stderr("the run waited %s for background work and cannot ask the model whether to wait on, as %v; it stops the work now: %s", roundTime(waited), err, strings.Join(work, ", "))
				b.StopBackground()
				return res, nil, st
			}
			idleSince = time.Now()
		}
	}
}

// emitResult writes the result line of a turn that has ended (stream-json
// output).
func (f *printer) emitResult(c Controls, res agent.TurnResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.encode(f.result(c, res))
	f.rounds = 0
}

// waitCeiling reads CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS: a ceiling on
// the idle wait, with no checkpoints; 0 for no ceiling and no checkpoint.
// Unset (or not a number), there is no ceiling, and the model is asked
// every waitCheckpoint.
func (f *printer) waitCeiling() (ceiling time.Duration, checkpoints bool) {
	v, set := f.p.env["CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS"]
	ms, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if !set || err != nil || ms < 0 {
		return 0, true
	}
	return time.Duration(ms) * time.Millisecond, false
}

// checkpointEvery reads GNOSH_PRINT_BG_CHECKPOINT_MS: how long the idle
// wait for background work lasts before the model is asked whether to
// wait on. Unset (or not a positive number), it is waitCheckpoint, ten
// minutes, as Claude Code waits. A run with a short budget (a benchmark's
// 15 minutes) wants it shorter: a server left running in the background
// otherwise holds the run idle for most of its time.
func (f *printer) checkpointEvery() time.Duration {
	ms, err := strconv.ParseInt(strings.TrimSpace(f.p.env["GNOSH_PRINT_BG_CHECKPOINT_MS"]), 10, 64)
	if err != nil || ms <= 0 {
		return waitCheckpoint
	}
	return time.Duration(ms) * time.Millisecond
}

// wrapUpShare is the part of a run's time budget after which the model is
// told to wrap up.
const wrapUpShare = 0.8

// maxDuration reads GNOSH_PRINT_MAX_DURATION_MS: the run's time budget,
// set by whoever stops it from outside when it runs out. Unset (or not a
// positive number), the run has none.
func (f *printer) maxDuration() time.Duration {
	ms, err := strconv.ParseInt(strings.TrimSpace(f.p.env["GNOSH_PRINT_MAX_DURATION_MS"]), 10, 64)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// wrapUpper is what a session's controls may also be: able to tell the
// model, once, that most of the run's time budget is used.
type wrapUpper interface {
	WrapUp(used, budget time.Duration)
}

// watchBudget tells the model to wrap up once wrapUpShare of the run's
// time budget (maxDuration) is used, counted from began. It returns what
// stops the watch.
func (f *printer) watchBudget(c Controls, began time.Time) (stop func()) {
	budget := f.maxDuration()
	w, ok := c.(wrapUpper)
	if budget <= 0 || !ok {
		return func() {}
	}
	at := time.Duration(float64(budget) * wrapUpShare)
	t := time.AfterFunc(at-time.Since(began), func() {
		w.WrapUp(time.Since(began), budget)
	})
	return func() { t.Stop() }
}

// closeReply ends the reply of a turn in text output, so that the next
// turn's reply starts on a line of its own.
func (f *printer) closeReply() {
	if f.format != cli.FormatText || f.inv.JSONSchema != "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endReply()
	if f.wroteAny && !f.endsLine {
		fmt.Fprintln(f.p.stdout)
		f.endsLine = true
	}
}

// stderrIsTerminal reports whether the program writes its errors to a
// terminal.
func (p *program) stderrIsTerminal() bool {
	file, ok := p.o.Stderr.(*os.File)
	return ok && term.IsTerminal(file)
}
