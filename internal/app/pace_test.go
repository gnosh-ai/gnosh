package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/apptest"
	"github.com/gnosh-ai/gnosh/internal/core"
)

// paceFront is a front end that sends one prompt, waits for its turn, and
// keeps what the pace check found and the notices it was given.
type paceFront struct {
	prompt  string
	mu      sync.Mutex
	paces   []PaceEvent
	notices []string
}

func (f *paceFront) Event(ev any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch ev := ev.(type) {
	case *PaceEvent:
		f.paces = append(f.paces, *ev)
	case *agent.NoticeEvent:
		f.notices = append(f.notices, ev.Text)
	}
}

func (f *paceFront) Run(ctx context.Context, c Controls) int {
	if _, err := c.Submit(agent.Prompt{Text: f.prompt}); err != nil {
		return 1
	}
	if err := c.Wait(ctx); err != nil {
		return 1
	}
	c.Quit("prompt_input_exit")
	return 0
}

func (f *paceFront) found() []PaceEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PaceEvent(nil), f.paces...)
}

func (f *paceFront) told() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.notices, "\n")
}

// runPace runs an interactive session of w, permissions skipped, with one
// prompt, and says how long it took.
func runPace(t *testing.T, w *apptest.World, prompt string) (*paceFront, time.Duration) {
	t.Helper()
	f := &paceFront{prompt: prompt}
	o := testOptions(w, "-y")
	o.StdinIsTerminal, o.StdoutIsTerminal = true, true
	o.Interactive = func(FrontendInfo) Frontend { return f }
	began := time.Now()
	runWith(t, t.Context(), o).wantCode(t, 0)
	return f, time.Since(began)
}

// A Bash command that prints nothing for three seconds is looked at at
// once, by a side request on the side requests' own slot, beside the work;
// a look that finds it stuck shows it, moves the command to the background
// as Ctrl+B does, so that the model has the turn back long before the
// command would have ended, and tells the model with its next request.
func TestPaceCheckMovesAStuckCommand(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 30","description":"wait for the lock"}`}}},
		apptest.Reply{Text: `{"pace": "stuck", "why": "sleep 30 prints nothing and waits on nothing", "next": 10}`},
		apptest.Reply{Text: "It waited on nothing, so I moved on."},
	)
	w.Settings(`{"providers": {"local": {"sideExtraBody": {"id_slot": 1}}}, "options": {"paceCheck": 3600}}`)
	f, took := runPace(t, w, "take the lock and go on")
	if took > 20*time.Second {
		t.Errorf("the session took %v: the stuck command was not moved to the background", took)
	}
	reqs := w.Model.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want the turn's, the look's and the turn's next", len(reqs))
	}
	look := string(reqs[1].Body)
	has(t, "the look", look, "You watch an AI agent while it works", "The task: take the lock and go on", "running Bash: sleep 30", "which has printed nothing for")
	var side, own map[string]any
	if err := json.Unmarshal(reqs[1].Body, &side); err != nil || side["id_slot"] != float64(1) {
		t.Errorf("the look did not go to the side requests' slot: %v (%v)", side["id_slot"], err)
	}
	if err := json.Unmarshal(reqs[0].Body, &own); err != nil || own["id_slot"] != nil {
		t.Errorf("the turn's own request went to the side requests' slot: %v", own["id_slot"])
	}
	has(t, "the turn's next request", string(reqs[2].Body), "A pace check, 0:0", "finds it stuck: sleep 30 prints nothing and waits on nothing.", "now runs in the background")
	found := f.found()
	if len(found) != 1 || found[0].Pace != "stuck" || !strings.Contains(found[0].Moved, "sleep 30") || found[0].Next != 10*time.Second {
		t.Errorf("the interface was shown %+v", found)
	}
}

// The first look comes once the turn has run for the option's time. A look
// that finds the work on pace is shown for a moment and told to nobody.
func TestPaceCheckOnPaceIsOnlyShown(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"for i in 1 2 3 4 5 6 7 8; do echo $i; sleep 0.5; done","description":"count slowly"}`}}},
		apptest.Reply{Text: `{"pace": "ok", "why": "counting as asked", "next": 600}`},
		apptest.Reply{Text: "Counted."},
	)
	w.Settings(`{"providers": {"local": {"sideExtraBody": {"id_slot": 1}}}, "options": {"paceCheck": 1}}`)
	f, _ := runPace(t, w, "count to eight slowly")
	reqs := w.Model.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want the turn's, the look's and the turn's next", len(reqs))
	}
	has(t, "the look", string(reqs[1].Body), "You watch an AI agent while it works", "running Bash: for i in")
	hasNot(t, "the turn's next request", string(reqs[2].Body), "A pace check")
	found := f.found()
	if len(found) != 1 || found[0].Pace != "ok" || found[0].Why != "counting as asked" || found[0].Moved != "" {
		t.Errorf("the interface was shown %+v", found)
	}
}

// Where side requests have no place of their own on a local server, no
// look is taken, since it would push the conversation out of the server's
// cache; a person who set the option is told why it does nothing.
func TestPaceCheckWithoutASidePlace(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 4","description":"wait"}`}}},
		apptest.Reply{Text: "Waited."},
	)
	w.Settings(`{"options": {"paceCheck": 60}}`)
	f, _ := runPace(t, w, "wait four seconds")
	if n := len(w.Model.Requests()); n != 2 {
		t.Errorf("%d requests: a look was taken with no place of its own", n)
	}
	if found := f.found(); len(found) != 0 {
		t.Errorf("the interface was shown %+v", found)
	}
	if got := f.told(); !strings.Contains(got, "The pace check is off here") {
		t.Errorf("the person was not told why the pace check does nothing:\n%s", got)
	}
}

// A look's answer is read past what comes around it; one that cannot be
// read is no finding, and the next look then comes after the option's
// time. The time it asks for is kept within a second and ten minutes.
func TestParsePace(t *testing.T) {
	every := time.Minute
	for _, c := range []struct {
		text, pace, why string
		next            time.Duration
	}{
		{`{"pace": "ok", "why": "fine", "next": 30}`, "ok", "fine", 30 * time.Second},
		{"<think>hm</think>\n```json\n{\"pace\": \"Faster\", \"why\": \"run  one\\n test\", \"next\": 5}\n```", "faster", "run one test", 5 * time.Second},
		{`{"pace": "stuck", "why": "it waits", "next": 0.2}`, "stuck", "it waits", time.Second},
		{`{"pace": "stuck", "why": "it waits", "next": 99999}`, "stuck", "it waits", 10 * time.Minute},
		{`{"pace": "ok", "why": "no time given"}`, "ok", "no time given", every},
		{`{"pace": "maybe", "why": "?", "next": 5}`, "", "", every},
		{`not json`, "", "", every},
	} {
		pace, why, next := parsePace(c.text, every)
		if pace != c.pace || why != c.why || next != c.next {
			t.Errorf("parsePace(%q) = %q, %q, %v; want %q, %q, %v", c.text, pace, why, next, c.pace, c.why, c.next)
		}
	}
}

// A step is told in a few words: the tool and what it was given, and how
// it ended.
func TestPaceSteps(t *testing.T) {
	if got := paceWhat("Bash", json.RawMessage(`{"command":"go test\n  ./...","description":"test"}`)); got != "Bash: go test ./..." {
		t.Errorf("paceWhat = %q", got)
	}
	if got := paceWhat("Read", json.RawMessage(`{"file_path":"/w/a.go"}`)); got != "Read: /w/a.go" {
		t.Errorf("paceWhat = %q", got)
	}
	if got := paceWhat("Odd", json.RawMessage(`{"x":1}`)); got != `Odd: {"x":1}` {
		t.Errorf("paceWhat = %q", got)
	}
	ended := func(name, text string, isErr bool, meta map[string]any) *agent.ToolEndEvent {
		return &agent.ToolEndEvent{Name: name, Result: core.ToolResult{Content: []core.Block{core.Text(text)}, IsError: isErr, Meta: meta}}
	}
	for _, c := range []struct {
		ev   *agent.ToolEndEvent
		want string
	}{
		{ended("Bash", "FAIL TestFlaky\nmore", false, map[string]any{"exitCode": 1}), "exit 1: FAIL TestFlaky"},
		{ended("Read", "\n  package main\n", false, nil), "ok: package main"},
		{ended("Edit", "no such file", true, nil), "failed: no such file"},
		{ended("Bash", "", false, map[string]any{"backgroundTaskId": "bash_3"}), "moved to the background as bash_3"},
		{&agent.ToolEndEvent{Name: "Bash", Denied: true}, "refused"},
		{&agent.ToolEndEvent{Name: "Bash", Interrupted: true}, "stopped"},
	} {
		if got := paceOutcome(c.ev); got != c.want {
			t.Errorf("paceOutcome = %q, want %q", got, c.want)
		}
	}
	line := paceLine(&PaceEvent{Elapsed: 185 * time.Second, Pace: "stuck", Why: "it waits", Moved: "Bash: sleep 30"})
	if line != "pace 3:05 · stuck: it waits (moved to the background: Bash: sleep 30)" {
		t.Errorf("paceLine = %q", line)
	}
	if line := paceLine(&PaceEvent{Elapsed: time.Minute, Pace: "ok", Why: "fine"}); line != "pace 1:00 · on pace: fine" {
		t.Errorf("paceLine = %q", line)
	}
}
