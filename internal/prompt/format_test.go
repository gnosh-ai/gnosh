package prompt

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestFormat builds formats the way the agent loop fills its own words
// (fmt.Sprintf, then the marker around the result) and checks that each
// comes out as the function of this package that says the same thing. The
// table is also the map from the loop's texts to the texts here.
func TestFormat(t *testing.T) {
	s := Builtin()
	yes := "yes"
	cases := []struct {
		name  string
		text  string
		holes map[string]string
		args  []any
		want  string
	}{
		{"an empty reply", textEmptyReply, nil, nil, s.EmptyReply()},
		{"a call written out as text", textTextCall, nil, nil, s.TextCall()},
		{"a repeated call", textRepeat, map[string]string{"calls": "%[3]s"}, []any{6, 10, "- Bash {\"command\":\"ls\"}"}, s.Repeat([]string{"Bash {\"command\":\"ls\"}"})},
		{"two calls taking turns", textRepeat, map[string]string{"calls": "%[2]s"}, []any{10, "- Read a\n- Grep b"}, s.Repeat([]string{"Read a", "Grep b"})},
		{"a checkpoint", textCheckpoint, map[string]string{"marker": "%[1]s", "count": "%[2]d"}, []any{StopMarker, 10}, s.Checkpoint(10, "", "")},
		{"a check that could not run", textCheckFailed, map[string]string{"name": "%[1]s", "error": "%[2]v"}, []any{"stop-hook", errors.New("no such file")}, s.CheckCouldNotRun("stop-hook", "no such file")},
		{"/loop", textLoop, map[string]string{"marker": LoopDone}, nil, s.Loop()},
		{"/loop, show it", textLoopProve, map[string]string{"marker": LoopDone}, nil, s.LoopProve()},
		{"/loop, the check-in", textLoopCheckIn, map[string]string{"marker": "%[1]s", "turns": "%[2]d"}, []any{LoopStop, 25}, s.LoopCheckIn(25)},
		{"the summary request", textSummary, map[string]string{"words": "500"}, nil, s.Summary(SummaryInput{})},
		{"the summary request, with mem", textSummary, map[string]string{"words": "500", "mem": yes}, nil, s.Summary(SummaryInput{Mem: true})},
		{"before a summary", textSummaryIntro, nil, nil, s.SummaryIntro()},
		{"after a summary", textAfterSummary, nil, nil, s.AfterSummary("")},
		{"the earliest part left out", textEarlierLeftOut, nil, nil, s.EarlierLeftOut()},
		{"a call that never started", textMissingResult, map[string]string{"not_started": yes}, nil, s.MissingResult(NotStarted, "")},
		{"a call that was stopped", textMissingResult, map[string]string{"stopped": yes}, nil, s.MissingResult(StoppedRunning, "")},
		{"a call the turn's end came before", textMissingResult, map[string]string{"ended": yes, "why": "%[1]s"}, []any{"the user refused a permission"}, s.MissingResult(TurnEnded, "the user refused a permission")},
		{"a call whose reply was cut off", textMissingResult, map[string]string{"cut_off": yes}, nil, s.MissingResult(ReplyCutOff, "")},
		{"a call gnosh's own stop cut off", textMissingResult, map[string]string{"killed": yes}, nil, s.MissingResult(ProcessStopped, "")},
		{"a call that would not stop", textMissingResult, map[string]string{"still": yes, "waited": "%[1]s"}, []any{"10s"}, s.MissingResult(StillRunning, "10s")},
		{"a Stop hook", textStopHook, map[string]string{"reason": "%s"}, []any{"no such file"}, s.StopHook("no such file")},
	}
	for _, c := range cases {
		format, err := s.Format(c.text, c.holes)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		got := fmt.Sprintf(format, c.args...)
		if info, _ := s.Text(c.text); info.Kind == KindLine {
			got = Wrap(got)
		}
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		if strings.Contains(got, "%!") {
			t.Errorf("%s: fmt complains: %q", c.name, got)
		}
	}

	// The person's wishes for a summary are a format of their own, put
	// after the request.
	extra, err := s.Format(textSummaryExtra, map[string]string{"instructions": "%s"})
	if err != nil {
		t.Fatal(err)
	}
	ask, _ := s.Format(textSummary, map[string]string{"words": "500"})
	if got, want := Wrap(ask+"\n"+fmt.Sprintf(extra, "keep the API decisions")), s.Summary(SummaryInput{Instructions: "keep the API decisions"}); got != want {
		t.Errorf("the summary request with wishes:\n got %q\nwant %q", got, want)
	}

	// What is refused.
	for name, c := range map[string]struct {
		text   string
		holes  map[string]string
		errHas string
	}{
		"a text gnosh does not have":      {"no-such-text", nil, "no text called"},
		"a name the text does not have":   {textStopHook, map[string]string{"reason": "%s", "hook": "%s"}, "has no {{hook}}"},
		"a name the text must print":      {textStopHook, nil, "must print {{reason}}"},
		"a needed name given only blanks": {textCheckpoint, map[string]string{"marker": " "}, "must print {{marker}}"},
	} {
		if _, err := s.Format(c.text, c.holes); err == nil || !strings.Contains(err.Error(), c.errHas) {
			t.Errorf("%s: got %v, want an error that says %q", name, err, c.errHas)
		}
	}
}

// TestFormatDoublesPercent checks that a per cent sign in a text's own
// words survives fmt.
func TestFormatDoublesPercent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "stop-hook.md", "Not yet, you are 90% there. The hook says: {{reason}}")
	set, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	format, err := set.Format(textStopHook, map[string]string{"reason": "%s"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Not yet, you are 90%% there. The hook says: %s"; format != want {
		t.Errorf("the format is %q, want %q", format, want)
	}
	if got, want := Wrap(fmt.Sprintf(format, "100% sure")), set.StopHook("100% sure"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
