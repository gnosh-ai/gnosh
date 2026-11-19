package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// largeWindow is where a window stops being "small": above it room is made
// a fixed distance from the end, below it at four fifths.
const largeWindow = 200000

// limits are the sizes a request is measured against, in tokens.
type limits struct {
	window int  // what the model can read at once, as the loop believes it
	known  bool // false when the window was assumed, not learned
	point  int  // above this, room is made before the request goes out
	hard   int  // above this, the request is never sent
}

// defaultPoint is where room is made for a window when the person has not
// asked for it earlier: 20,000 tokens before the end of a large window,
// and four fifths of a smaller one.
func defaultPoint(window int) int {
	if window >= largeWindow {
		return window - 20000
	}
	return (window*4 + 2) / 5
}

// summaryRoom is how much of the window is kept free for a summary's own
// text: half of the gap between the default point and the window.
func summaryRoom(window int) int {
	if window >= largeWindow {
		return 10000
	}
	return window / 10
}

// limits works out the sizes for the model in su.
func (a *Agent) limits(su setup) limits {
	w := su.model.window()
	known := su.model.Info.ContextWindow > 0
	if a.learned > 0 && a.learnedFor == modelKey(su.model) && a.learned < w {
		w, known = a.learned, true
	}
	l := limits{window: w, known: known, point: defaultPoint(w)}
	if su.compactAt > 0 && su.compactAt < l.point {
		l.point = su.compactAt
	}
	reserve := min(max(w/16, 256), 8192)
	l.hard = max(w-reserve, l.point)
	return l
}

// modelKey names a model for the things the loop remembers about one.
func modelKey(m Model) string {
	name := ""
	if m.Provider != nil {
		name = m.Provider.Name()
	}
	return name + "\x00" + m.Info.ID
}

// learnWindow makes the loop believe that m's window is at most n tokens,
// because the server showed it, and says so.
func (a *Agent) learnWindow(m Model, n int, why string) {
	if n < 1 {
		n = 1
	}
	if a.learnedFor == modelKey(m) && a.learned > 0 && a.learned <= n {
		return
	}
	a.learned, a.learnedFor = n, modelKey(m)
	a.notice(LevelWarning, "context", fmt.Sprintf(
		"%s. gnosh now treats its window as %s tokens for this session; if that is wrong, set the model's context window in the config.", why, exact(n)))
}

// anchor ties the loop's estimates to the provider's own count: the
// estimate of a request that was sent, and what the provider said it came
// to. The next request is then the provider's count plus the scaled
// difference between the two estimates, so only what is new is guessed.
type anchor struct {
	ok    bool
	est   int
	real  int
	model string
}

// predict says about how many tokens a request with estimate est is for
// model m.
func (a *Agent) predict(est int, m Model) int {
	if a.anchor.ok && a.anchor.model == modelKey(m) {
		return max(1, a.anchor.real+scale(est-a.anchor.est, a.calib))
	}
	return max(1, scale(est, a.calib))
}

// scale applies how far the estimates have run off the provider's counts.
func scale(n int, by float64) int {
	return int(float64(n)*by + 0.5)
}

// counted takes a provider's count for a request the loop had estimated.
// A report without a prompt count is no report.
func (a *Agent) counted(est int, u core.Usage, m Model) {
	real := u.PromptTokens()
	if real <= 0 || est <= 0 {
		return
	}
	a.calib = min(max(float64(real)/float64(est), 0.5), 3)
	a.anchor = anchor{ok: true, est: est, real: real, model: modelKey(m)}
	if a.heldFor != modelKey(m) {
		a.held, a.heldFor = 0, modelKey(m)
	}
	a.held = max(a.held, real)
}

// estBlocks guesses the tokens of blocks. thinking says whether reasoning
// counts, which it does only when the provider sends it back.
func (a *Agent) estBlocks(blocks []core.Block, thinking bool) int {
	n := 0
	for _, b := range blocks {
		switch b.Type {
		case core.BlockText:
			n += a.est(b.Text)
		case core.BlockThinking:
			if thinking {
				n += a.est(b.Text) + a.est(b.Signature) + a.est(b.Opaque)
			}
		case core.BlockToolUse:
			n += 8 + a.est(b.Name) + a.est(string(b.Input))
		case core.BlockToolResult:
			n += 8 + a.estBlocks(b.Content, thinking)
		case core.BlockImage:
			n += 1500
		}
	}
	return n
}

// estMessages guesses the tokens of messages, with the wiring's own guess
// for a message when it gave one.
func (a *Agent) estMessages(msgs []core.Message, thinking bool) int {
	n := 0
	for _, m := range msgs {
		if f := a.opts.EstimateMessage; f != nil {
			n += f(m)
			continue
		}
		n += 4 + a.estBlocks(m.Blocks, thinking)
	}
	return n
}

// estFixed guesses the tokens of the part every request begins with: the
// system prompt and the tools.
func (a *Agent) estFixed(req *core.Request) int {
	n := 0
	for _, s := range req.System {
		n += a.est(s)
	}
	for _, t := range req.Tools {
		n += 8 + a.est(t.Name) + a.est(t.Description) + a.est(string(t.InputSchema))
	}
	return n
}

// estimate guesses the tokens of a whole request.
func (a *Agent) estimate(req *core.Request, m Model) int {
	return a.estFixed(req) + a.estMessages(req.Messages, m.ResendsThinking)
}

// assemble makes the request for msgs as the session stands.
func (a *Agent) assemble(su setup, msgs []core.Message) *core.Request {
	m := su.model
	return &core.Request{
		Model:       m.Info.ID,
		System:      slices.Clone(su.system),
		Messages:    msgs,
		Tools:       slices.Clone(su.specs),
		MaxTokens:   m.MaxTokens,
		Temperature: m.Temperature,
		TopP:        m.TopP,
		Thinking:    m.mainThinking(),
		Extra:       maps.Clone(m.Extra),
	}
}

// mainThinking is the thinking a main request asks for: the model's level,
// and its budget when it thinks at all.
func (m Model) mainThinking() core.Thinking {
	t := thinkingFor(m.Level)
	if t.Mode != "off" && m.ThinkingBudget > 0 {
		t.BudgetTokens, t.BudgetNote = m.ThinkingBudget, m.BudgetNote
	}
	return t
}

// checkView reports what is wrong with msgs as a conversation to send, or
// nil. The rules are core.Request's: it starts with a user message, the
// roles take turns, no message and no text block is empty, and every tool
// call is answered by exactly one result in the user message that follows,
// where the results come before anything else.
func checkView(msgs []core.Message) error {
	if len(msgs) == 0 {
		return errors.New("it is empty")
	}
	if msgs[0].Role != core.RoleUser {
		return errors.New("it does not start with a user message")
	}
	var calls map[string]string // the calls of the message before, by id
	for i, m := range msgs {
		if i > 0 && m.Role == msgs[i-1].Role {
			return fmt.Errorf("messages %d and %d are both from the %s", i-1, i, m.Role)
		}
		if len(m.Blocks) == 0 {
			return fmt.Errorf("message %d is empty", i)
		}
		for _, b := range m.Blocks {
			if b.Type == core.BlockText && b.Text == "" {
				return fmt.Errorf("message %d holds an empty text block", i)
			}
		}
		if m.Role == core.RoleAssistant {
			calls = map[string]string{}
			for _, u := range m.ToolUses() {
				calls[u.ID] = u.Name
			}
			if len(calls) > 0 && i+1 >= len(msgs) {
				return fmt.Errorf("the tool calls of message %d have no results", i)
			}
			continue
		}
		answered := map[string]bool{}
		other := false
		for _, b := range m.Blocks {
			if b.Type != core.BlockToolResult {
				other = true
				continue
			}
			switch _, asked := calls[b.ToolUseID]; {
			case other:
				return fmt.Errorf("message %d holds a tool result after other blocks", i)
			case !asked:
				return fmt.Errorf("message %d holds a result for %q, which the message before did not call", i, b.ToolUseID)
			case answered[b.ToolUseID]:
				return fmt.Errorf("message %d holds two results for the tool call %q", i, b.ToolUseID)
			}
			answered[b.ToolUseID] = true
		}
		for id, name := range calls {
			if !answered[id] {
				return fmt.Errorf("the tool call %q (%s) of message %d has no result", id, name, i-1)
			}
		}
		calls = nil
	}
	return nil
}

// fitted is a request that may be sent, with what was worked out about it.
type fitted struct {
	req       *core.Request
	est       int // the loop's estimate of the request
	predicted int // about how many tokens the provider will count
	su        setup
	lim       limits
}

// fitError says the conversation could not be made to fit the model.
type fitError struct{ msg string }

func (e *fitError) Error() string { return e.msg }

// fit builds the next request and makes sure the model can hold it. Above
// the compaction point it first asks the log to take the oldest part out
// of the view, and only when nothing can go does it write a summary. A
// request above the hard limit is never returned: fit ends with a
// *fitError instead.
//
// extra is the estimate of what is about to be added to the conversation
// and is not in the log yet: the prompt, when fit runs before a prompt
// goes out. The request returned then lacks it and must not be sent.
func (a *Agent) fit(ctx context.Context, t *turn, moment RoomMoment, extra int) (fitted, error) {
	var (
		slid       bool // room was made by a slide at this point
		slideSpent bool // the log has nothing more to give
		summarized bool // a summary was made at this point
		before     = -1 // the size before the last slide
	)
	for {
		if ctx.Err() != nil {
			return fitted{}, errStopped
		}
		su := a.setup()
		v, err := a.view()
		if err != nil {
			return fitted{}, err
		}
		req := a.assemble(su, v.Messages)
		est := a.estimate(req, su.model)
		lim := a.limits(su)
		predicted := a.predict(est+extra, su.model)
		fr := fitted{req: req, est: est, predicted: predicted, su: su, lim: lim}
		a.noteContext(su, lim, predicted)

		if extra == 0 {
			if err := checkView(v.Messages); err != nil {
				return fitted{}, fmt.Errorf("the session's log does not read as a conversation a model can be sent: %w", err)
			}
		}

		// A window smaller than what every request begins with can hold
		// no conversation at all.
		if fixed := scale(a.estFixed(req), a.calib); fixed > lim.hard {
			return fitted{}, &fitError{fmt.Sprintf(
				"%s can read %s tokens at once, and gnosh sends about %s tokens of instructions and tool descriptions before the first word of a conversation, so no request can fit. "+
					"Give the model a larger context window (for a local server, its context size setting), or switch to a model that has one.",
				su.model.label(), tokens(lim.window), tokens(fixed))}
		}

		if predicted <= lim.point {
			return fr, nil
		}
		cannot := func(why string) error {
			return &fitError{fmt.Sprintf(
				"The conversation has grown to about %s tokens, and %s can read %s at once. %s "+
					"Start a new session, switch to a model with a larger window (/model), or run /compact.",
				tokens(predicted), su.model.label(), tokens(lim.window), why)}
		}
		if su.noAuto {
			if predicted <= lim.hard {
				return fr, nil
			}
			return fitted{}, cannot("Making room automatically is turned off.")
		}

		// A slide that did not make the request smaller will not do so the
		// next time either.
		if slid && before >= 0 && predicted >= before {
			slideSpent = true
		}

		// With nothing in the conversation yet there is nothing to take
		// out of it: what is too long is the prompt itself.
		if len(v.Messages) == 0 {
			if predicted <= lim.hard {
				return fr, nil
			}
			return fitted{}, cannot("The prompt alone is longer than that.")
		}

		// First the slide: the oldest part of the conversation leaves the
		// view, and no model is asked for anything.
		if !slideSpent {
			a.setPhase(PhaseRoom, "making room in the model's context")
			before = predicted
			freed := a.slide(ctx, t, moment, predicted-lim.point+lim.window/10)
			if ctx.Err() != nil {
				return fitted{}, errStopped
			}
			if freed > 0 {
				slid = true
				continue
			}
			slideSpent = true
		}

		// Where a slide was made no summary is made, unless the request
		// still cannot be sent at all.
		if slid && predicted <= lim.hard {
			return fr, nil
		}

		// Last the summary. One that left the conversation over the point
		// is not followed by another.
		if summarized {
			// It did not help only if what it left is over the point: what
			// is over may be the prompt about to go out (a long paste),
			// which no summary makes shorter, and for which summaries must
			// not be turned off.
			if a.predict(est, su.model) > lim.point {
				a.summaryDidNotHelp(su, lim, predicted)
			}
			if predicted <= lim.hard {
				return fr, nil
			}
			return fitted{}, cannot("A summary was made and it is still too long.")
		}
		if a.summariesOff {
			a.notice(LevelWarning, "context", fmt.Sprintf(
				"The conversation is about %s tokens, past the point (%s) where a summary would be made, but automatic summaries are off for this session because the last one did not help. /compact makes one by hand.",
				tokens(predicted), tokens(lim.point)))
			if predicted <= lim.hard {
				return fr, nil
			}
			return fitted{}, cannot("Automatic summaries are off for this session because the last one did not help.")
		}
		err = a.summarize(ctx, t, "auto", "")
		switch {
		case err == nil:
			summarized = true
			continue
		case ctx.Err() != nil:
			return fitted{}, errStopped
		case errors.Is(err, errFellBack):
			// The turn goes on with a fallback model, which room is made
			// for afresh.
			slid, slideSpent, before = false, false, -1
			continue
		case errors.Is(err, errNothingToSummarize):
			// The model has said nothing yet: what is long is what the
			// person sent, and a summary of that alone would only put the
			// model's words in place of theirs.
			if predicted <= lim.hard {
				return fr, nil
			}
			return fitted{}, cannot("There is nothing to summarize: what was sent is itself too long.")
		}
		// A summary that fails sends the waiting prompt as it is.
		a.notice(LevelWarning, "summary", fmt.Sprintf("No summary could be made (%v). The conversation goes on as it is.", err))
		if predicted <= lim.hard {
			return fr, nil
		}
		return fitted{}, cannot(fmt.Sprintf("No summary could be made (%v).", err))
	}
}

// summaryDidNotHelp is called when the conversation is still over the
// point right after an automatic summary. Summarizing again would not fix
// it, so the person's earlier-than-default point is dropped, and if the
// conversation is over the default point too, automatic summaries stop
// for the session. The slide is not touched by any of this.
func (a *Agent) summaryDidNotHelp(su setup, lim limits, predicted int) {
	def := defaultPoint(lim.window)
	if su.compactAt > 0 && su.compactAt < def {
		a.mu.Lock()
		a.compactAt = 0
		a.mu.Unlock()
		a.notice(LevelWarning, "context", fmt.Sprintf(
			"A summary left the conversation at about %s tokens, still over the point you set (%s). gnosh goes back to its own point (%s) for this session.",
			tokens(predicted), tokens(su.compactAt), tokens(def)))
		if predicted <= def {
			return
		}
	}
	if !a.summariesOff {
		a.summariesOff = true
		a.notice(LevelWarning, "context", fmt.Sprintf(
			"A summary left the conversation at about %s tokens, still over the point (%s) where one is made. Another would not help, so automatic summaries are off for this session. /compact makes one by hand.",
			tokens(predicted), tokens(def)))
	}
}

// noteContext records the size of the conversation for whoever asks, and
// tells the consumer of events when it has changed. It also says, once
// per model, that a window had to be assumed.
func (a *Agent) noteContext(su setup, lim limits, used int) {
	c := ContextStatus{Window: lim.window, Known: lim.known, Point: lim.point, Limit: lim.hard, Used: used, AutoOff: a.summariesOff}
	a.mu.Lock()
	changed := a.context != c
	a.context = c
	a.mu.Unlock()
	if changed {
		a.emit(&ContextEvent{Context: c})
	}
	if !lim.known && a.warnedWindow != modelKey(su.model) {
		a.warnedWindow = modelKey(su.model)
		a.notice(LevelWarning, "context", fmt.Sprintf(
			"gnosh could not learn the context window of %s, so it treats it as %s tokens and makes room early. Set the model's context window in the config to use all of it.",
			su.model.label(), exact(UnknownWindow)))
	}
}

// slide asks the log's owner to take the oldest part of the conversation
// out of the view, and returns about how many tokens went.
func (a *Agent) slide(ctx context.Context, t *turn, moment RoomMoment, need int) int {
	r, ok := a.opts.Log.(Roomer)
	if !ok {
		return 0
	}
	thinking := a.setup().model.ResendsThinking
	req := RoomRequest{
		Need: need, Moment: moment, Estimate: a.est,
		EstimateMessage: func(m core.Message) int { return a.estMessages([]core.Message{m}, thinking) },
	}
	if t != nil && moment == RoomMidTurn {
		// The prompt the person sent, and all after it: after a nudge or
		// a driver's objection the newest prompt is gnosh's own, and a
		// slide that kept only that one took the person's words with the
		// rest (the compaction review of 2026-11-19).
		req.Protect = firstNonEmpty(t.sent, t.newest)
	}
	room, err, done := await(ctx, a, "making room in the log", func(ctx context.Context) (Room, error) {
		a.logMu.Lock()
		defer a.unlockLog()
		return r.MakeRoom(ctx, req)
	})
	if !done || ctx.Err() != nil {
		return 0
	}
	if err != nil {
		a.notice(LevelWarning, "context", fmt.Sprintf("The oldest part of the conversation could not be taken out of the model's context (%v).", err))
		return 0
	}
	if room.Freed <= 0 {
		return 0
	}
	a.emit(&RoomEvent{Freed: room.Freed, Notice: room.Notice})
	if room.Notice != "" {
		a.notice(LevelInfo, "context", room.Notice)
	}
	if strings.TrimSpace(room.Reminder) != "" {
		turn := ""
		if t != nil {
			turn = t.id
		}
		if err := a.appendLog(Entry{Kind: EntryNote, Turn: turn, Source: "room", Blocks: []core.Block{core.Text(room.Reminder)}}); err != nil {
			a.notice(LevelWarning, "context", err.Error())
		}
	}
	return room.Freed
}
