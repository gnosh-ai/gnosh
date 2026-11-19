package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// errSummaryDidNotFit means no request for a summary could be made small
// enough to leave the model room to write one.
var errSummaryDidNotFit = errors.New("the summary did not fit in the model's context window")

// errNothingToSummarize means the model has not said anything yet.
var errNothingToSummarize = errors.New("there is no conversation to summarize yet")

// summarize replaces the conversation by a summary of it. It is the
// backstop: it is asked for only when nothing can be taken out of the view
// without one, or when the person asks.
//
// The request for a summary begins exactly as the session's own requests
// do (the same system prompt, tools and messages), so that a local model
// server reads only the request at its end. It must leave the model room
// to write. If it does not, it is sent bare, without the system prompt and
// the tools; if that does not fit either, with the earliest part of the
// conversation left out. A summary the model was cut off in is not
// accepted: it is asked for again with less. If nothing fits, the
// conversation stays as it was.
//
// t is the turn the summary is made in, or nil for one the person asked
// for between turns.
func (a *Agent) summarize(ctx context.Context, t *turn, trigger, instructions string) (err error) {
	su := a.setup()
	v, err := a.view()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(v.Messages, func(m core.Message) bool { return m.Role == core.RoleAssistant }) {
		return errNothingToSummarize
	}
	lim := a.limits(su)
	full := a.assemble(su, v.Messages)
	pre := a.predict(a.estimate(full, su.model), su.model)
	info := SummaryInfo{Trigger: trigger, Instructions: instructions, PreTokens: pre, Window: lim.window}

	// The request is gnosh's own line: the asking, then on lines of their
	// own what the person and their hooks want of the summary, and the
	// marker around all of it.
	ask := a.texts.SummaryRequest
	if strings.TrimSpace(instructions) != "" {
		ask += "\n" + fmt.Sprintf(a.texts.SummaryExtra, strings.TrimSpace(instructions))
	}
	if f := a.opts.Moments.BeforeSummary; f != nil {
		extra, ferr, ok := await(ctx, a, "the before-summary moment", func(ctx context.Context) (string, error) { return f(ctx, info) })
		switch {
		case ctx.Err() != nil || !ok:
			return errStopped
		case ferr != nil:
			a.notice(LevelWarning, "summary", fmt.Sprintf("The before-summary moment could not run (%v). The summary goes ahead without it.", ferr))
		case strings.TrimSpace(extra) != "":
			ask += "\n" + strings.TrimSpace(extra)
		}
	}
	ask = a.mark(ask)

	a.emit(&SummaryEvent{Phase: "start", Trigger: trigger, PreTokens: pre})
	a.setPhase(PhaseSummary, "writing a summary of the conversation")
	post := 0
	defer func() {
		if errors.Is(err, errFellBack) {
			// Not a summary that failed: the fallback model is asked for
			// it, and says so with an end of its own.
			return
		}
		ev := &SummaryEvent{Phase: "end", Trigger: trigger, PreTokens: pre, PostTokens: post}
		if err != nil && !errors.Is(err, errStopped) {
			ev.Err = err
		}
		a.emit(ev)
	}()

	room := summaryRoom(lim.window)
	fits := func(req *core.Request) (int, bool) {
		est := a.estimate(req, su.model)
		return est, a.predict(est, su.model)+room <= lim.window
	}
	text := ""
	sent := 0 // the estimate of the last request that was really sent
	try := func(req *core.Request) (bool, error) {
		est, ok := fits(req)
		if !ok {
			return false, nil
		}
		// A request that failed is followed by one that is a good deal
		// smaller, not by one a message shorter: each try costs a local
		// model minutes.
		if sent > 0 && est > sent*3/4 {
			return false, nil
		}
		sent = est
		rep, xerr := a.exchange(ctx, t, "summary", req, est, su.model, false)
		if rep == nil {
			return false, errStopped // stopped between two tries
		}
		why := rep.stop
		switch {
		case errors.Is(xerr, errStopped):
			why = core.StopCancelled
		case xerr != nil:
			why = core.StopError
		}
		a.emit(&ReplyEvent{
			Purpose: "summary", Message: rep.msg, StopReason: why,
			Provider: su.model.Provider.Name(), Model: su.model.Info.ID,
			Usage: rep.usage, Duration: rep.dur, FirstPiece: rep.first,
		})
		var long *tooLongError
		switch {
		case errors.Is(xerr, errStopped):
			return false, errStopped
		case errors.As(xerr, &long):
			return false, nil // asked again with less
		case xerr != nil:
			return false, xerr
		case rep.stop == core.StopMaxTokens:
			return false, nil // cut short: not accepted
		case len(rep.msg.ToolUses()) > 0 || rep.stop == core.StopToolUse || rep.stop == core.StopRefusal:
			// The model began a call ("Let me check the test first.")
			// or refused: not a summary, which would stand for the whole
			// conversation. The bare try has no tools to call.
			return false, nil
		}
		text = strings.TrimSpace(rep.msg.Text())
		return text != "", nil
	}

	// As the session's own requests; then bare; then with the earliest
	// part left out, cut at one of the model's own messages.
	done, err := try(withAsk(full, v.Messages, ask))
	if err != nil {
		return err
	}
	if !done {
		bare := a.assemble(su, nil)
		bare.System, bare.Tools = nil, nil
		sent = 0 // the bare request is tried whatever its size
		done, err = try(withAsk(bare, v.Messages, ask))
		if err != nil {
			return err
		}
		// The earliest part is left out, cut at one of the model's own
		// messages: first keeping the first message (the task as it was
		// set, or the summary made before this one, on which all after it
		// builds), with the note of what was left out after it; and only
		// when nothing fits so, without it as well.
		first := v.Messages[0]
		for _, keepFirst := range []bool{true, false} {
			for i := 1; !done && i < len(v.Messages); i++ {
				if v.Messages[i].Role != core.RoleAssistant {
					continue
				}
				lead := core.UserText(a.mark(a.texts.TrimNote))
				if keepFirst {
					lead = core.Message{Role: core.RoleUser, Blocks: append(slices.Clone(first.Blocks), core.Text(a.mark(a.texts.TrimNote)))}
				}
				rest := append([]core.Message{lead}, v.Messages[i:]...)
				done, err = try(withAsk(bare, rest, ask))
				if err != nil {
					return err
				}
			}
		}
	}
	if !done {
		return errSummaryDidNotFit
	}

	// The summary stands for everything before it. The estimates are no
	// longer tied to the last count, which was of the long conversation.
	a.anchor.ok = false
	turn := ""
	if t != nil {
		turn = t.id
	}
	err = a.appendLog(
		Entry{Kind: EntrySummary, Turn: turn, Trigger: trigger, PreTokens: pre,
			Blocks: []core.Block{core.Text(a.mark(a.texts.SummaryIntro)), core.Text(text)}},
		Entry{Kind: EntryNote, Turn: turn, Source: "summary",
			Blocks: []core.Block{core.Text(a.mark(a.texts.SummaryAfter))}},
	)
	if err != nil {
		return err
	}
	if t != nil {
		t.guard.reset()
	}

	// What the session is given back: the files it had read, the work
	// open in mem, what hooks print after a summary.
	if f := a.opts.Moments.AfterSummary; f != nil {
		info.Summary = text
		parts, ferr, ok := await(ctx, a, "the after-summary moment", func(ctx context.Context) ([]Part, error) { return f(ctx, info) })
		var h *StopWork
		halted := errors.As(ferr, &h)
		if halted && t != nil {
			// It said stop: the turn the summary was made in ends here,
			// once what it gave back is kept. (After /compact no turn
			// runs, and there is nothing to end.)
			t.halt = firstNonEmpty(h.Reason, "the end of the summary stopped the turn")
		}
		if ferr != nil && !halted && ok && ctx.Err() == nil {
			// What could not be made is said; what was made is still
			// given back below (the instructions the session started with,
			// its skills: lost, they stayed lost for the session).
			a.notice(LevelWarning, "summary", fmt.Sprintf("Not all of what the session is given back after a summary could be put together (%v).", ferr))
		}
		// What the moment made is kept even when the turn is being stopped,
		// as the summary is, with which it goes; only a moment that did not
		// finish gives nothing back.
		if ok {
			var notes []Entry
			for _, p := range parts {
				if strings.TrimSpace(p.Text) != "" {
					notes = append(notes, Entry{Kind: EntryNote, Turn: turn, Source: p.Source, Blocks: []core.Block{core.Text(p.Text)}})
				}
			}
			if err := a.appendLog(notes...); err != nil {
				return err
			}
		}
	}

	if nv, verr := a.view(); verr == nil {
		now := a.setup()
		after := a.assemble(now, nv.Messages)
		post = a.predict(a.estimate(after, now.model), now.model)
		a.noteContext(now, a.limits(now), post)
	}
	return nil
}

// withAsk returns a copy of base that holds msgs and ends with the request
// for a summary. The request joins the last message when that is the
// user's, so that the roles still take turns.
func withAsk(base *core.Request, msgs []core.Message, ask string) *core.Request {
	req := *base
	out := slices.Clone(msgs)
	if n := len(out); n > 0 && out[n-1].Role == core.RoleUser {
		last := out[n-1]
		last.Blocks = append(slices.Clone(last.Blocks), core.Text(ask))
		out[n-1] = last
	} else {
		out = append(out, core.UserText(ask))
	}
	req.Messages = out
	return &req
}
