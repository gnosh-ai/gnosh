package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// A session start that says stop (a SessionStart hook's "continue": false)
// refuses the prompt it stands before and every one after it, with its
// reason, and nothing goes to the model.
func TestSessionStartHalt(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Moments.SessionStart = func(context.Context, StartInfo) ([]Part, error) {
			return nil, &StopWork{Reason: "the license has run out"}
		}
	})
	for _, p := range []string{"first", "second"} {
		res := r.run(p)
		if res.Reason != EndRefused || res.Detail != "the license has run out" {
			t.Errorf("%s prompt: %+v", p, res)
		}
	}
	if n := r.model.count(); n != 0 {
		t.Errorf("the model was asked %d times", n)
	}
}

// The end of a summary made in a turn that says stop (a PostCompact
// hook's "continue": false) ends that turn, once what it gave back is kept.
func TestAfterSummaryHalt(t *testing.T) {
	j := &job{want: 14}
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.System = []string{"You are gnosh."}
		o.Tools = []core.Tool{fileTool(600)}
		o.Moments.AfterSummary = func(context.Context, SummaryInfo) ([]Part, error) {
			return []Part{{Source: "files", Text: "kept after the summary"}}, &StopWork{Reason: "review the summary first"}
		}
	})
	r.model.honest = true
	r.model.window = 8000
	r.model.answer(j.respond)
	res := r.run("read fourteen files")
	if res.Reason != EndHalted || !strings.Contains(res.Detail, "review the summary first") {
		t.Fatalf("result = %+v", res)
	}
	if j.summaryCount() == 0 {
		t.Fatal("no summary was made")
	}
	r.checkLog()
}

// What the after-summary moment gave back is kept even when the turn was
// stopped while it ran, as the summary is: it finished, and the files it
// read again count as read (only a moment that does not finish in time is
// gone on without).
func TestAfterSummaryPartsAreKeptWhenStopped(t *testing.T) {
	j := &job{want: 14}
	var r *rig
	r = newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.System = []string{"You are gnosh."}
		o.Tools = []core.Tool{fileTool(600)}
		o.Moments.AfterSummary = func(ctx context.Context, _ SummaryInfo) ([]Part, error) {
			r.a.Interrupt()
			<-ctx.Done()
			return []Part{{Source: "files", Text: "given back after the summary"}}, nil
		}
	})
	r.model.honest = true
	r.model.window = 8000
	r.model.answer(j.respond)
	if res := r.run("read fourteen files"); res.Reason != EndStopped {
		t.Fatalf("result = %+v", res)
	}
	if j.summaryCount() == 0 {
		t.Fatal("no summary was made")
	}
	kept := false
	for _, e := range r.log.of(EntryNote) {
		if e.Source == "files" && strings.Contains(core.TextOf(e.Blocks), "given back after the summary") {
			kept = true
		}
	}
	if !kept {
		t.Error("what the after-summary moment gave back was dropped when the turn was stopped")
	}
	r.checkLog()
}

// A reply to the summary request that begins a tool call ("Let me check
// the test first." and a Read) is no summary, which would stand for the
// whole conversation: the model is asked again without tools, and that
// answer is the summary (the compaction review of 2026-11-19).
func TestSummaryThatBeginsACallIsRefused(t *testing.T) {
	j := &job{want: 14}
	j.summary = func(_ int, req *core.Request) step {
		if len(req.Tools) > 0 {
			s := use(core.ToolCall{ID: "s1", Name: "Read", Input: []byte(`{}`)})
			s.evs = append([]core.Event{{Type: core.EventTextDelta, Text: "Let me check the failing test first."}}, s.evs...)
			return s
		}
		return say("SUMMARY from the bare try: fourteen files are being read.")
	}
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.System = []string{"You are gnosh."}
		o.Tools = []core.Tool{fileTool(600)}
	})
	r.model.honest = true
	r.model.window = 8000
	r.model.answer(j.respond)
	if res := r.run("read fourteen files"); res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	sums := r.log.of(EntrySummary)
	if len(sums) == 0 {
		t.Fatal("no summary was made")
	}
	for _, e := range sums {
		text := core.TextOf(e.Blocks)
		if strings.Contains(text, "Let me check the failing test first") || !strings.Contains(text, "SUMMARY from the bare try") {
			t.Errorf("the summary stored is %q", text)
		}
	}
}

// One tool result takes at most a quarter of the model's window: the rest
// is cut, with a line that says how much there was and how to have it.
func TestResultIsCutToAQuarterOfTheWindow(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.Tools = []core.Tool{fileTool(4000)}
	})
	r.model.push(use(core.ToolCall{ID: "c1", Name: "Read", Input: []byte(`{"file_path":"/big"}`)}), say("read"))
	if res := r.run("read the big file"); res.Reason != EndCompleted {
		t.Fatalf("result = %+v", res)
	}
	results := r.log.of(EntryResult)
	if len(results) != 1 {
		t.Fatalf("%d results", len(results))
	}
	text := core.TextOf(results[0].Result.Content)
	if !strings.Contains(text, "a quarter of the model's window") || len(text) > 9000 {
		t.Errorf("the result kept is %d bytes and ends %q", len(text), text[max(0, len(text)-200):])
	}
}

// What goes out with a prompt (an @-mentioned file) is counted when room
// is made for the prompt: counted after, it made the first round's summary
// take the new prompt, unanswered, with the rest (the compaction review of
// 2026-11-19). The summary comes before the prompt now. (On an 8,000-token
// window the point is 6,400: a first answer of about 4,000 tokens is under
// it alone, and over it with 3,500 of the prompt's lines, more than a
// summary of both could fit.)
func TestPromptLinesAreCountedBeforeThePromptGoesOut(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
		o.Moments.PromptLines = []func(context.Context, LineInfo) ([]Part, error){
			func(_ context.Context, info LineInfo) ([]Part, error) {
				if info.Prompt.Text != "fix the nil deref" {
					return nil, nil
				}
				return []Part{{Source: "mention", Text: strings.Repeat("the mentioned file\n", 737)}}, nil
			},
		}
	})
	r.model.honest = true
	r.model.window = 8000
	r.model.answer(func(req *core.Request, _ int) step {
		if isSummaryAsk(req) {
			return say("SUMMARY: a long answer was given.")
		}
		if strings.Contains(lastUserText(req), "tell me a long story") {
			return say(strings.Repeat("a long answer goes on\n", 727))
		}
		return say("fixed")
	})
	if res := r.run("tell me a long story"); res.Reason != EndCompleted {
		t.Fatalf("first turn: %+v", res)
	}
	if res := r.run("fix the nil deref"); res.Reason != EndCompleted || res.Text != "fixed" {
		t.Fatalf("second turn: %+v", res)
	}
	summary, prompt := -1, -1
	for i, e := range r.log.all() {
		switch {
		case e.Kind == EntrySummary:
			summary = i
		case e.Kind == EntryPrompt && core.TextOf(e.Blocks) == "fix the nil deref":
			prompt = i
		}
	}
	if summary < 0 || prompt < 0 || summary > prompt {
		t.Errorf("the summary is entry %d and the prompt entry %d: the summary must come before the prompt", summary, prompt)
	}
}

// A summary is judged by what it left: a long pasted prompt about to go
// out, which no summary makes shorter, does not turn automatic summaries
// off for the session (the compaction review of 2026-11-19).
func TestLongPasteDoesNotTurnSummariesOff(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Model.Info.ContextWindow = 8000
	})
	r.model.honest = true
	r.model.window = 8000
	r.model.answer(func(req *core.Request, _ int) step {
		switch {
		case isSummaryAsk(req):
			return say("SUMMARY: a long answer was given.")
		case strings.Contains(lastUserText(req), "tell me a long story"):
			return say(strings.Repeat("a long answer goes on\n", 727))
		}
		return say("read it")
	})
	if res := r.run("tell me a long story"); res.Reason != EndCompleted {
		t.Fatalf("first turn: %+v", res)
	}
	if res := r.run(strings.Repeat("a long pasted log line\n", 830)); res.Reason != EndCompleted {
		t.Fatalf("the paste: %+v", res)
	}
	r.idle()
	if r.a.summariesOff {
		t.Error("a long paste turned automatic summaries off")
	}
}
