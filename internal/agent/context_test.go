package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// job is a model that reads files one round at a time and then says it is
// done. Asked for a summary, it writes one.
type job struct {
	mu        sync.Mutex
	reads     int // how many files it has asked for
	want      int // how many it reads before it is done
	summaries []*core.Request
	summary   func(n int, req *core.Request) step
	next      int
}

func isSummaryAsk(req *core.Request) bool {
	return strings.Contains(lastUserText(req), "Write a summary of it")
}

func (j *job) respond(req *core.Request, _ int) step {
	j.mu.Lock()
	defer j.mu.Unlock()
	if isSummaryAsk(req) {
		j.summaries = append(j.summaries, req)
		if j.summary != nil {
			return j.summary(len(j.summaries), req)
		}
		return say(fmt.Sprintf("SUMMARY %d: the user asked to read files; %d are read.", len(j.summaries), j.reads))
	}
	if j.reads >= j.want {
		return say("done")
	}
	j.reads++
	j.next++
	return use(tc(fmt.Sprintf("r%d", j.next), "Read", fmt.Sprintf(`{"file_path":"/src/file%d.go"}`, j.next)))
}

func (j *job) summaryCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.summaries)
}

// fileTool returns a file of about n tokens by the loop's own estimate.
func fileTool(n int) *lookTool {
	return look("Read", func(_ context.Context, call core.ToolCall, _ *core.ToolEnv) (core.ToolResult, error) {
		return core.TextResult(string(call.Input) + "\n" + strings.Repeat("abcdefghi\n", n*3/10)), nil
	})
}

func TestLimits(t *testing.T) {
	cases := []struct {
		window, point, room int
	}{
		{262144, 242144, 10000},
		{131072, 104858, 13107},
		{32768, 26214, 3276},
		{24576, 19661, 2457},
		{4000, 3200, 400},
	}
	for _, c := range cases {
		if got := defaultPoint(c.window); got != c.point {
			t.Errorf("the point for a window of %d = %d, want %d", c.window, got, c.point)
		}
		if got := summaryRoom(c.window); got != c.room {
			t.Errorf("the summary's room for a window of %d = %d, want %d", c.window, got, c.room)
		}
	}
	a := &Agent{}
	m := Model{Provider: &fakeModel{name: "fake"}, Info: core.ModelInfo{ID: "m"}}
	l := a.limits(setup{model: m})
	if l.window != 32768 || l.known || l.point != 26214 || l.hard <= l.point || l.hard >= l.window {
		t.Errorf("limits for an unknown window = %+v", l)
	}
	m.Info.ContextWindow = 262144
	if l := a.limits(setup{model: m, compactAt: 150000}); l.point != 150000 || !l.known {
		t.Errorf("an earlier point was not taken: %+v", l)
	}
	if l := a.limits(setup{model: m, compactAt: 260000}); l.point != 242144 {
		t.Errorf("a later point was taken: %+v", l)
	}
}

// A model whose window nobody could learn is treated as a small one, and
// the person is told. The conversation is kept under it.
func TestUnknownWindowIsSmall(t *testing.T) {
	log := &slideLog{memLog: newMemLog()}
	j := &job{want: 40}
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 0
		o.Log = log
		o.Tools = []core.Tool{fileTool(1500)}
	})
	r.model.honest = true
	r.model.window = UnknownWindow
	r.model.answer(j.respond)

	res := r.run("read forty files")
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	c := r.a.Context()
	if c.Window != 32768 || c.Known || c.Point != 26214 {
		t.Errorf("context = %+v", c)
	}
	warn := notices(r.rec, LevelWarning)
	if len(warn) != 1 || !strings.Contains(warn[0], "could not learn the context window") || !strings.Contains(warn[0], "32,768") {
		t.Errorf("warnings = %v", warn)
	}
	if r.model.refused != 0 {
		t.Errorf("%d requests were longer than the window", r.model.refused)
	}
	if r.model.longest > 26214+1600 {
		t.Errorf("the longest request was %d tokens", r.model.longest)
	}
	if len(log.asked) == 0 || j.summaryCount() != 0 {
		t.Errorf("room was asked for %d times and %d summaries were made", len(log.asked), j.summaryCount())
	}
	r.checkLog()
}

// When the provider refuses a request as too long, the same request is
// never sent again: the log is asked for room, and what goes out next is
// shorter.
func TestContextTooLongAsksForRoom(t *testing.T) {
	log := &slideLog{memLog: newMemLog()}
	j := &job{want: 12}
	r := newRig(t, func(o *Options) {
		o.Log = log // the loop believes in a window of 100,000
		o.Tools = []core.Tool{fileTool(600)}
	})
	r.model.honest = true
	r.model.window = 3000 // the server's slot is far smaller
	r.model.answer(j.respond)

	res := r.run("read twelve files")
	if res.Reason != EndCompleted || res.Text != "done" {
		t.Fatalf("result = %+v", res)
	}
	if r.model.refused != 1 {
		t.Errorf("%d requests were refused, want exactly the first surprise", r.model.refused)
	}
	reqs := r.model.requests()
	for i, req := range reqs {
		if n := r.model.realTokens(req); n > 3000 {
			if i+1 >= len(reqs) {
				t.Fatal("the refused request was the last")
			}
			if next := r.model.realTokens(reqs[i+1]); next >= n {
				t.Errorf("after a refusal of %d tokens the next request was %d tokens", n, next)
			}
			if !strings.Contains(lastUserText(reqs[i+1]), "<slid>") {
				t.Errorf("the model was not told that the start of the conversation left its context")
			}
		}
	}
	if c := r.a.Context(); c.Window >= 3200 || c.Window < 2000 || !c.Known {
		t.Errorf("the window the loop now believes in = %+v", c)
	}
	warn := notices(r.rec, LevelWarning)
	if len(warn) != 1 || !strings.Contains(warn[0], "refused a request of about") || !strings.Contains(warn[0], "too long for its context") {
		t.Errorf("warnings = %v", warn)
	}
	if len(log.asked) == 0 {
		t.Fatal("the log was never asked for room")
	}
	prompt := r.log.of(EntryPrompt)[0]
	for _, ask := range log.asked {
		if ask.Moment != RoomMidTurn || ask.Protect != prompt.ID || ask.Need <= 0 || ask.Estimate == nil {
			t.Errorf("the log was asked %+v", ask)
		}
	}
	if len(eventsOf[*RoomEvent](r.rec)) == 0 || len(eventsOf[*ContextEvent](r.rec)) == 0 {
		t.Error("the consumer of events was not told")
	}
	if j.summaryCount() != 0 {
		t.Errorf("%d summaries were made though the log had room to give", j.summaryCount())
	}
	r.checkLog()
}

// With no count of the provider's to go by, a refusal means the loop's
// estimate was too low, not that the window is smaller than it was told.
func TestRefusalWithoutACountBlamesTheEstimate(t *testing.T) {
	log := &slideLog{memLog: newMemLog()}
	// A resumed session: a long conversation is already in the log.
	entries := []Entry{{Kind: EntryPrompt, ID: "p0", Blocks: []core.Block{core.Text("read the files")}}}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("old%d", i)
		entries = append(entries,
			Entry{Kind: EntryAssistant, ID: "a" + id, StopReason: core.StopToolUse,
				Message: core.Message{Role: core.RoleAssistant, Blocks: []core.Block{core.ToolUse(id, "Read", json.RawMessage(`{}`))}}},
			Entry{Kind: EntryResult, ID: "r" + id, ToolUseID: id, ToolName: "Read", Result: core.TextResult(strings.Repeat("0123456789", 300))},
		)
	}
	entries = append(entries,
		Entry{Kind: EntryAssistant, ID: "alast", StopReason: core.StopEndTurn, Message: core.AssistantText("all read")},
		Entry{Kind: EntryTurn, ID: "t0", Reason: EndCompleted},
	)
	log.Append(context.Background(), entries...)

	r := newRig(t, func(o *Options) {
		o.Log = log
		o.Model.Info.ContextWindow = 30000
	})
	r.model.honest = true
	r.model.dense = 1.6 // the server counts 1.6 tokens where the loop guesses one
	r.model.window = 30000
	r.model.push(say("here is what they hold"))

	res := r.run("what do they hold?")
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	if r.model.refused != 1 {
		t.Errorf("%d refusals", r.model.refused)
	}
	if c := r.a.Context(); c.Window != 30000 {
		t.Errorf("the loop gave up the window it was told: %+v", c)
	}
	warn := notices(r.rec, LevelWarning)
	if len(warn) != 1 || !strings.Contains(warn[0], "estimates were too low") {
		t.Errorf("warnings = %v", warn)
	}
	if len(log.asked) == 0 {
		t.Error("no room was asked for")
	}
	r.checkLog()
}

// At small windows, with a fixed prompt as large as the real one and a
// server that counts more tokens than the loop guesses, a long task never
// produces a request over the window.
func TestSmallWindowsHold(t *testing.T) {
	for _, window := range []int{24576, 32768} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			log := &slideLog{memLog: newMemLog()}
			j := &job{want: 80}
			r := newRig(t, func(o *Options) {
				o.Log = log
				o.Model.Info.ContextWindow = window
				// About 10,700 tokens by the loop's guess, 14,000 by the
				// server's count.
				o.System = []string{strings.Repeat("You are gnosh. Follow the rules. ", 970)}
				o.Tools = []core.Tool{fileTool(900)}
			})
			r.model.honest = true
			r.model.dense = 1.3
			r.model.window = window
			r.model.answer(j.respond)

			res := r.run("read eighty files and say done")
			if res.Reason != EndCompleted || res.Text != "done" {
				t.Fatalf("result = %+v", res)
			}
			if r.model.refused != 0 {
				t.Errorf("%d requests were over the window of %d", r.model.refused, window)
			}
			if r.model.longest > window {
				t.Errorf("the longest request was %d tokens", r.model.longest)
			}
			if j.summaryCount() != 0 {
				t.Errorf("%d summaries were made; the slide should have done it", j.summaryCount())
			}
			if len(eventsOf[*RoomEvent](r.rec)) == 0 {
				t.Error("nothing ever slid")
			}
			t.Logf("window %d: %d requests, the longest %d tokens, %d slides", window, r.model.count(), r.model.longest, len(eventsOf[*RoomEvent](r.rec)))
			r.checkLog()
		})
	}
}

// With no room to be had from the log, a summary is the backstop. Its
// request begins exactly as the session's own requests do.
func TestSummaryIsTheBackstop(t *testing.T) {
	j := &job{want: 14}
	var before, after []SummaryInfo
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.System = []string{"You are gnosh."}
		o.Tools = []core.Tool{fileTool(600)}
		o.Moments.BeforeSummary = func(_ context.Context, info SummaryInfo) (string, error) {
			before = append(before, info)
			return "Keep every file name.", nil
		}
		o.Moments.AfterSummary = func(_ context.Context, info SummaryInfo) ([]Part, error) {
			after = append(after, info)
			return []Part{{Source: "files", Text: "<file path=\"/src/file1.go\">fresh from disk</file>"}}, nil
		}
	})
	r.model.honest = true
	r.model.window = 8000
	r.model.answer(j.respond)

	res := r.run("read fourteen files")
	if res.Reason != EndCompleted || res.Text != "done" {
		t.Fatalf("result = %+v", res)
	}
	if r.model.refused != 0 {
		t.Errorf("%d requests were over the window", r.model.refused)
	}
	n := j.summaryCount()
	if n == 0 {
		t.Fatal("no summary was made")
	}

	reqs := r.model.requests()
	first := -1
	for i, req := range reqs {
		if isSummaryAsk(req) {
			first = i
			break
		}
	}
	// The request for a summary is the request before it, with what came
	// since and the asking at its end.
	ask, prev := reqs[first], reqs[first-1]
	if strings.Join(ask.System, "|") != "You are gnosh." || len(ask.Tools) != 1 {
		t.Errorf("the summary request does not start as the session's own: system %q, %d tools", ask.System, len(ask.Tools))
	}
	for i, m := range prev.Messages {
		a, _ := json.Marshal(m)
		b, _ := json.Marshal(ask.Messages[i])
		if i < len(prev.Messages)-1 && string(a) != string(b) {
			t.Errorf("the summary request changed message %d", i)
		}
	}
	if text := lastUserText(ask); !strings.Contains(text, "Keep every file name.") {
		t.Errorf("what BeforeSummary returned is not in the asking: %q", clip(text, 200))
	}
	// What follows a summary starts from it, and is short.
	next := reqs[first+1]
	blocks := userBlocks(next)
	// The line before the summary is gnosh's own, and marked; the summary
	// is the model's text, and stands outside the marker.
	if len(next.Messages) != 1 || len(blocks) != 4 || !strings.HasPrefix(blocks[0], "<gnosh>") || !strings.Contains(blocks[0], "ran out of room") {
		t.Fatalf("the request after the summary = %q", blocks)
	}
	if !strings.HasPrefix(blocks[1], "SUMMARY 1") || strings.Contains(blocks[1], "<gnosh>") {
		t.Errorf("the summary = %q", blocks[1])
	}
	if !strings.Contains(blocks[2], "Carry on with the task") || !strings.HasPrefix(blocks[2], "<gnosh>") || !strings.Contains(blocks[3], "fresh from disk") {
		t.Errorf("what the session was given back = %q", blocks[2:])
	}
	if text := lastUserText(ask); !strings.HasSuffix(strings.TrimSpace(text), "</gnosh>") {
		t.Errorf("the request for a summary is not marked as gnosh's own: %q", clip(text, 120))
	}
	if r.model.realTokens(next) > 1500 {
		t.Errorf("the request after the summary is %d tokens", r.model.realTokens(next))
	}

	events := eventsOf[*SummaryEvent](r.rec)
	if len(events) != 2*n || events[0].Phase != "start" || events[1].Phase != "end" || events[1].Trigger != "auto" || events[1].Err != nil {
		t.Fatalf("summary events = %+v", events)
	}
	if events[1].PreTokens <= 6400 || events[1].PostTokens >= 1500 {
		t.Errorf("the summary went from %d to %d tokens", events[1].PreTokens, events[1].PostTokens)
	}
	if len(before) != n || len(after) != n || before[0].Trigger != "auto" || !strings.Contains(after[0].Summary, "SUMMARY 1") {
		t.Errorf("the moments were told %+v and %+v", before, after)
	}
	if got := len(r.log.of(EntrySummary)); got != n {
		t.Errorf("%d summary entries for %d summaries", got, n)
	}
	for _, e := range r.log.all() {
		if strings.Contains(core.TextOf(e.Blocks), "Write a summary of it") {
			t.Error("the request for a summary was stored in the log")
		}
	}
	r.checkLog()
}

// A summary the model was cut off in is not accepted. It is asked for
// again with less: first without the system prompt and the tools, then
// with the earliest part of the conversation left out.
func TestSummaryCutShortIsAskedAgainWithLess(t *testing.T) {
	j := &job{want: 12}
	j.summary = func(n int, req *core.Request) step {
		if n < 3 {
			return step{evs: []core.Event{{Type: core.EventTextDelta, Text: "SUMMARY cut off in the mid"}, usageEv(0, 0), stopEv(core.StopMaxTokens)}}
		}
		return say("SUMMARY whole")
	}
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.System = []string{"You are gnosh."}
		o.Tools = []core.Tool{fileTool(600)}
	})
	r.model.honest = true
	r.model.answer(j.respond)

	res := r.run("read twelve files")
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	j.mu.Lock()
	asks := j.summaries
	j.mu.Unlock()
	if len(asks) < 3 {
		t.Fatalf("%d summary requests", len(asks))
	}
	if len(asks[0].System) != 1 || len(asks[0].Tools) != 1 {
		t.Errorf("the first asking is not the session's own request")
	}
	if len(asks[1].System) != 0 || len(asks[1].Tools) != 0 || len(asks[1].Messages) != len(asks[0].Messages) {
		t.Errorf("the second asking is not bare: %d system parts, %d tools", len(asks[1].System), len(asks[1].Tools))
	}
	third := asks[2]
	if len(third.System) != 0 || len(third.Messages) >= len(asks[1].Messages) {
		t.Errorf("the third asking does not leave out the earliest part")
	}
	if got := third.Messages[0].Text(); !strings.Contains(got, "conversation is left out here") {
		t.Errorf("the third asking does not say what was left out: %q", got)
	}
	if got := third.Messages[0].Text(); !strings.HasPrefix(got, "read twelve files") {
		t.Errorf("the third asking does not keep the first message, the task as it was set: %q", got)
	}
	if third.Messages[1].Role != core.RoleAssistant {
		t.Errorf("the cut is not at one of the model's own messages")
	}
	if err := checkView(third.Messages); err != nil {
		t.Errorf("the third asking is not a valid conversation: %v", err)
	}
	if r.model.realTokens(third)*4 > r.model.realTokens(asks[1])*3 {
		t.Errorf("the third asking (%d tokens) is not a good deal shorter than the second (%d)", r.model.realTokens(third), r.model.realTokens(asks[1]))
	}
	sums := r.log.of(EntrySummary)
	if len(sums) == 0 || !strings.Contains(core.TextOf(sums[0].Blocks), "SUMMARY whole") {
		t.Fatalf("summary entries = %+v", sums)
	}
	for _, s := range sums {
		if strings.Contains(core.TextOf(s.Blocks), "cut off in the mid") {
			t.Error("a summary that was cut off was stored")
		}
	}
	r.checkLog()
}

// A first message too big to keep in a shorter asking is left out with the
// earliest part: a summary is still made, of what follows it.
func TestSummaryLeavesOutAFirstMessageTooBigToKeep(t *testing.T) {
	j := &job{want: 6}
	j.summary = func(n int, req *core.Request) step {
		if n < 3 {
			return step{evs: []core.Event{{Type: core.EventTextDelta, Text: "SUMMARY cut off in the mid"}, usageEv(0, 0), stopEv(core.StopMaxTokens)}}
		}
		return say("SUMMARY whole")
	}
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.Tools = []core.Tool{fileTool(600)}
	})
	r.model.honest = true
	r.model.answer(j.respond)

	paste := "read the files this log names\n" + strings.Repeat("a long pasted log line\n", 690)
	if res := r.run(paste); res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	j.mu.Lock()
	asks := j.summaries
	j.mu.Unlock()
	if len(asks) < 3 {
		t.Fatalf("%d summary requests", len(asks))
	}
	for i, ask := range asks {
		t.Logf("asking %d: %d tokens, %d messages", i+1, r.model.realTokens(ask), len(ask.Messages))
	}
	third := asks[2]
	got := third.Messages[0].Text()
	if strings.Contains(got, "a long pasted log line") {
		t.Errorf("the third asking keeps a first message too big to keep")
	}
	if !strings.Contains(got, "conversation is left out here") {
		t.Errorf("the third asking does not say what was left out: %q", got)
	}
	if err := checkView(third.Messages); err != nil {
		t.Errorf("the third asking is not a valid conversation: %v", err)
	}
	sums := r.log.of(EntrySummary)
	if len(sums) == 0 || !strings.Contains(core.TextOf(sums[0].Blocks), "SUMMARY whole") {
		t.Fatalf("summary entries = %+v", sums)
	}
	r.checkLog()
}

// When no summary fits, the conversation is kept as it was and the person
// reads why. A request that cannot be sent ends the turn with a plain
// explanation; it is never sent.
func TestSummaryThatDoesNotFit(t *testing.T) {
	j := &job{want: 30}
	j.summary = func(int, *core.Request) step {
		return step{evs: []core.Event{{Type: core.EventTextDelta, Text: "cut"}, usageEv(0, 0), stopEv(core.StopMaxTokens)}}
	}
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.Tools = []core.Tool{fileTool(600)}
	})
	r.model.honest = true
	r.model.window = 8000
	r.model.answer(j.respond)

	res := r.run("read thirty files")
	if res.Reason != EndFailed || res.ErrorKind != "context_too_long" {
		t.Fatalf("result = %+v", res)
	}
	msg := res.Err.Error()
	for _, part := range []string{"The conversation has grown to about", "can read 8K at once", "did not fit in the model's context window", "/compact"} {
		if !strings.Contains(msg, part) {
			t.Errorf("the failure %q does not say %q", msg, part)
		}
	}
	if r.model.refused != 0 {
		t.Errorf("%d requests over the window were sent", r.model.refused)
	}
	if len(r.log.of(EntrySummary)) != 0 {
		t.Error("a summary was stored")
	}
	warn := notices(r.rec, LevelWarning)
	found := false
	for _, w := range warn {
		found = found || strings.Contains(w, "No summary could be made (the summary did not fit in the model's context window)")
	}
	if !found {
		t.Errorf("the person was not told why: %v", warn)
	}
	// The conversation is kept, and a new prompt can still be answered
	// once there is room: here, by a model with a larger window.
	big := Model{Provider: r.model, Info: core.ModelInfo{ID: "bigger", ContextWindow: 100000}}
	r.model.window = 100000
	if err := r.a.SetModel(big); err != nil {
		t.Fatal(err)
	}
	j.mu.Lock()
	j.want = 0
	j.mu.Unlock()
	if res := r.run("never mind, just say done"); res.Reason != EndCompleted {
		t.Fatalf("after the switch = %+v", res)
	}
	r.checkLog()
}

// A summary that leaves the conversation over the point is not followed
// by another: automatic summaries stop for the session, with a warning
// each time one would have run.
func TestSummaryThatDoesNotHelpIsNotRepeated(t *testing.T) {
	j := &job{want: 4}
	j.summary = func(int, *core.Request) step {
		// A "summary" as long as what it replaces.
		return say("SUMMARY " + strings.Repeat("and then another file was read. ", 620))
	}
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.Tools = []core.Tool{fileTool(100)}
	})
	r.model.honest = true
	r.model.window = 8000
	r.model.answer(j.respond)
	// The conversation starts long: one long answer.
	r.model.push(say(strings.Repeat("a long answer. ", 1300)))
	if res := r.run("tell me everything"); res.Reason != EndCompleted {
		t.Fatalf("first turn = %+v", res)
	}

	res := r.run("read four small files")
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	if n := j.summaryCount(); n != 1 {
		t.Errorf("%d summaries were made, want 1", n)
	}
	if !r.a.Context().AutoOff {
		t.Errorf("context = %+v", r.a.Context())
	}
	var off, skipped int
	for _, w := range notices(r.rec, LevelWarning) {
		if strings.Contains(w, "Another would not help, so automatic summaries are off") {
			off++
		}
		if strings.Contains(w, "automatic summaries are off for this session because the last one did not help") {
			skipped++
		}
	}
	if off != 1 || skipped < 2 {
		t.Errorf("%d notices that summaries went off and %d that one was skipped", off, skipped)
	}
	if r.model.refused != 0 {
		t.Errorf("%d requests over the window", r.model.refused)
	}

	// A summary the person asks for by hand lets them run again.
	j.mu.Lock()
	j.summary = nil
	j.mu.Unlock()
	if err := r.a.Compact(""); err != nil {
		t.Fatal(err)
	}
	r.idle()
	if c := r.a.Context(); c.AutoOff || j.summaryCount() != 2 {
		t.Errorf("a summary by hand did not reset it: %d summaries, context %+v", j.summaryCount(), c)
	}
	r.checkLog()
}

// A point the person set earlier than the default is given up when a
// summary cannot bring the conversation under it.
func TestEarlierPointIsDroppedWhenItCannotBeMet(t *testing.T) {
	j := &job{want: 3}
	j.summary = func(int, *core.Request) step {
		return say("SUMMARY " + strings.Repeat("and then another file was read. ", 200))
	}
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 20000
		o.CompactAt = 1500
		o.Tools = []core.Tool{fileTool(700)}
	})
	r.model.honest = true
	r.model.answer(j.respond)
	res := r.run("read three files")
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	if n := j.summaryCount(); n != 1 {
		t.Errorf("%d summaries, want 1", n)
	}
	c := r.a.Context()
	if c.Point != 16000 || c.AutoOff {
		t.Errorf("context = %+v", c)
	}
	found := false
	for _, w := range notices(r.rec, LevelWarning) {
		found = found || strings.Contains(w, "still over the point you set")
	}
	if !found {
		t.Errorf("warnings = %v", notices(r.rec, LevelWarning))
	}
}

// Switching to a model with a smaller window brings the conversation
// under that window before the next prompt goes out.
func TestSwitchToASmallerWindow(t *testing.T) {
	log := &slideLog{memLog: newMemLog()}
	j := &job{want: 12}
	r := newRig(t, func(o *Options) {
		o.Log = log
		o.Tools = []core.Tool{fileTool(800)}
	})
	r.model.honest = true
	r.model.answer(j.respond)
	if res := r.run("read twelve files"); res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	if len(log.asked) != 0 {
		t.Fatalf("room was asked for under a window of 100,000")
	}

	small := &fakeModel{name: "fake", honest: true, window: 8000}
	small.push(say("still with you"))
	if err := r.a.SetModel(Model{Provider: small, Info: core.ModelInfo{ID: "small", ContextWindow: 8000}}); err != nil {
		t.Fatal(err)
	}
	res := r.run("are you still there?")
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	if small.refused != 0 || small.longest > 8000 {
		t.Errorf("the small model was sent %d tokens and refused %d requests", small.longest, small.refused)
	}
	if len(log.asked) == 0 || log.asked[0].Moment != RoomBeforePrompt || log.asked[0].Protect != "" {
		t.Fatalf("room was asked for as %+v", log.asked)
	}
	// Before a typed prompt the prompt's own lines can tell the model, so
	// nothing was put into the conversation for it.
	for _, e := range r.log.of(EntryNote) {
		if e.Source == "room" {
			t.Errorf("a room note was stored before a typed prompt: %+v", e)
		}
	}
	r.checkLog()
}

// A window smaller than what every request begins with can hold nothing,
// and the person is told so plainly instead of being shown nonsense.
func TestWindowSmallerThanTheFixedPrompt(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 4096
		o.System = []string{strings.Repeat("an instruction. ", 1200)}
	})
	res := r.run("hello")
	if res.Reason != EndFailed || res.ErrorKind != "context_too_long" {
		t.Fatalf("result = %+v", res)
	}
	for _, part := range []string{"can read 4K tokens at once", "instructions and tool descriptions", "no request can fit", "larger context window"} {
		if !strings.Contains(res.Err.Error(), part) {
			t.Errorf("the failure %q does not say %q", res.Err.Error(), part)
		}
	}
	if r.model.count() != 0 {
		t.Error("a request was sent")
	}
	if p := r.log.of(EntryPrompt); len(p) != 1 {
		t.Errorf("the prompt was not kept: %v", r.log.kinds())
	}
	r.checkLog()
}

// A local server whose slot is smaller than the window it was said to
// have cuts the reply off when the slot is full. The loop then learns the
// real window, makes room and asks again.
func TestReplyCutByASmallerWindow(t *testing.T) {
	for _, local := range []bool{true, false} {
		t.Run(fmt.Sprintf("local=%v", local), func(t *testing.T) {
			log := &slideLog{memLog: newMemLog()}
			cuts := 0
			var mu sync.Mutex
			j := &job{want: 6}
			r := newRig(t, func(o *Options) {
				o.Log = log
				o.Model.Local = local
				o.Tools = []core.Tool{fileTool(500)}
			})
			r.model.honest = true
			r.model.answer(func(req *core.Request, n int) step {
				mu.Lock()
				defer mu.Unlock()
				if cuts == 0 && r.model.realTokens(req) > 3000 {
					cuts++
					return step{evs: []core.Event{{Type: core.EventTextDelta, Text: "I have rea"}, usageEv(0, 0), stopEv(core.StopMaxTokens)}}
				}
				return j.respond(req, n)
			})
			res := r.run("read six files")
			if !local {
				// Without the flag a cut reply is the reply.
				if res.Reason != EndCompleted || res.Text != "I have rea" || res.StopReason != core.StopMaxTokens {
					t.Fatalf("result = %+v", res)
				}
				if w := notices(r.rec, LevelWarning); len(w) != 1 || !strings.Contains(w[0], "cut off at the length limit") {
					t.Errorf("warnings = %v", w)
				}
				return
			}
			if res.Reason != EndCompleted || res.Text != "done" {
				t.Fatalf("result = %+v", res)
			}
			c := r.a.Context()
			if c.Window < 3000 || c.Window > 3700 {
				t.Errorf("the window learned = %+v", c)
			}
			found := false
			for _, w := range notices(r.rec, LevelWarning) {
				found = found || strings.Contains(w, "cut its reply off after")
			}
			if !found {
				t.Errorf("warnings = %v", notices(r.rec, LevelWarning))
			}
			for _, e := range r.log.of(EntryAssistant) {
				if e.Message.Text() == "I have rea" {
					t.Error("the reply that was cut off by the window is in the log")
				}
			}
			if len(log.asked) == 0 {
				t.Error("no room was made after the window was learned")
			}
			r.checkLog()
		})
	}
}

// With making room turned off, nothing is slid and nothing is summarized;
// a request that would not fit at all ends the turn and says why.
func TestNoAutoCompact(t *testing.T) {
	log := &slideLog{memLog: newMemLog()}
	j := &job{want: 40}
	r := newRig(t, func(o *Options) {
		o.Log = log
		o.NoAutoCompact = true
		o.Model.Info.ContextWindow = 8000
		o.Tools = []core.Tool{fileTool(600)}
	})
	r.model.honest = true
	r.model.window = 8000
	r.model.answer(j.respond)
	res := r.run("read forty files")
	if res.Reason != EndFailed || !strings.Contains(res.Err.Error(), "Making room automatically is turned off") {
		t.Fatalf("result = %+v", res)
	}
	if len(log.asked) != 0 || j.summaryCount() != 0 || r.model.refused != 0 {
		t.Errorf("%d slides asked, %d summaries, %d refusals", len(log.asked), j.summaryCount(), r.model.refused)
	}
	if r.model.longest <= 6400 {
		t.Errorf("the turn ended at %d tokens, before the window was full", r.model.longest)
	}
	r.checkLog()
}

// /compact: a summary the person asks for, with their instructions.
func TestManualCompact(t *testing.T) {
	j := &job{want: 2}
	r := newRig(t, func(o *Options) { o.Tools = []core.Tool{fileTool(300)} })
	r.model.honest = true
	r.model.answer(j.respond)

	// With nothing to summarize it says so.
	if err := r.a.Compact(""); err != nil {
		t.Fatal(err)
	}
	r.idle()
	if e := notices(r.rec, LevelError); len(e) != 1 || !strings.Contains(e[0], "there is no conversation to summarize yet") {
		t.Errorf("errors = %v", e)
	}

	r.run("read two files")
	if err := r.a.Compact("focus on the auth bug"); err != nil {
		t.Fatal(err)
	}
	r.idle()

	j.mu.Lock()
	asks := j.summaries
	j.mu.Unlock()
	if len(asks) != 1 || !strings.Contains(lastUserText(asks[0]), "The user asks this of the summary: focus on the auth bug") {
		t.Fatalf("summary requests = %d", len(asks))
	}
	events := eventsOf[*SummaryEvent](r.rec)
	if len(events) != 2 || events[0].Trigger != "manual" || events[1].Err != nil {
		t.Fatalf("summary events = %+v", events)
	}
	sums := r.log.of(EntrySummary)
	if len(sums) != 1 || sums[0].Trigger != "manual" || sums[0].PreTokens == 0 {
		t.Fatalf("summary entries = %+v", sums)
	}
	j.mu.Lock()
	j.want = j.reads
	j.mu.Unlock()
	res := r.run("what was that about?")
	if res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	reqs := r.model.requests()
	last := reqs[len(reqs)-1]
	if len(last.Messages) != 1 || !strings.Contains(userBlocks(last)[1], "SUMMARY 1") || !strings.Contains(lastUserText(last), "what was that about?") {
		t.Errorf("the turn after the summary was sent %q", userBlocks(last))
	}
	r.checkLog()

	// A summary by hand can be stopped, and leaves the conversation as it
	// was.
	reached := make(chan struct{})
	j.mu.Lock()
	j.summary = func(int, *core.Request) step { return step{at: once(reached), stall: true} }
	j.mu.Unlock()
	r.a.Compact("")
	<-reached
	if st := r.a.State(); st.Phase != PhaseRequest && st.Phase != PhaseSummary {
		t.Errorf("state during a summary = %+v", st)
	}
	r.a.Interrupt()
	r.idle()
	if len(r.log.of(EntrySummary)) != 1 {
		t.Error("a stopped summary was stored")
	}
	if got := slices.ContainsFunc(notices(r.rec, LevelInfo), func(s string) bool { return strings.Contains(s, "The summary was stopped") }); !got {
		t.Errorf("the person was not told: %v", notices(r.rec, ""))
	}
	r.checkLog()
}
