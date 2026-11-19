package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/term/vtest"
	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/ui/editor/keyscript"
)

// fakeRuns is an app.AgentSource whose runs a test sets, and which keeps
// what was asked of it.
type fakeRuns struct {
	mu          sync.Mutex
	runs        []app.AgentRun
	convs       map[string][]app.TranscriptEntry
	dispatchErr error
	dispatched  []string
	stopped     []string
	forgotten   []string
	opened      []string
	answered    []string
	resumed     []string
	told        []string
	renamed     []string
	unread      map[string][]string
	now         func() time.Time
}

func (f *fakeRuns) Tell(id, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.told = append(f.told, id+" "+text)
	return nil
}

func (f *fakeRuns) Unread(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	words := f.unread[id]
	delete(f.unread, id)
	return words
}

func (f *fakeRuns) Resume(_ context.Context, id, prompt string, flags []string) error {
	f.mu.Lock()
	f.resumed = append(f.resumed, words(id, strings.Join(flags, " "), prompt))
	f.mu.Unlock()
	f.set(id, func(r *app.AgentRun) { r.State, r.Started, r.Ended, r.Prompt = "working", f.now(), time.Time{}, prompt })
	return nil
}

func (f *fakeRuns) Answer(id, question, choice, text string) error {
	f.mu.Lock()
	f.answered = append(f.answered, id+" "+question+" "+choice+" "+text)
	f.mu.Unlock()
	f.set(id, func(r *app.AgentRun) { r.State, r.Question = "working", nil })
	return nil
}

func (f *fakeRuns) Runs() []app.AgentRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.runs)
}

func (f *fakeRuns) Conversation(id string) []app.TranscriptEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.convs[id])
}

func (f *fakeRuns) set(id string, change func(*app.AgentRun)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.runs {
		if f.runs[i].ID == id {
			change(&f.runs[i])
		}
	}
}

func (f *fakeRuns) Stop(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	f.stopped = append(f.stopped, id)
	f.mu.Unlock()
	f.set(id, func(r *app.AgentRun) { r.State, r.Ended = "stopped", f.now() })
	return "the session " + id + " was stopped. What it did so far is kept: gnosh -r " + id + " opens it.", nil
}

func (f *fakeRuns) Rename(id, name string) error {
	f.mu.Lock()
	f.renamed = append(f.renamed, words(id, name))
	f.mu.Unlock()
	f.set(id, func(r *app.AgentRun) { r.Name = name })
	return nil
}

func (f *fakeRuns) Forget(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, id)
	f.runs = slices.DeleteFunc(f.runs, func(r app.AgentRun) bool { return r.ID == id })
	return nil
}

func (f *fakeRuns) Dispatch(_ context.Context, dir, prompt string, flags []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dispatchErr != nil {
		return "", f.dispatchErr
	}
	if dir != "" {
		dir = "in " + dir
	}
	f.dispatched = append(f.dispatched, words(dir, strings.Join(flags, " "), prompt))
	id := fmt.Sprintf("01a1eea5-9999-7000-8000-%012d", len(f.dispatched))
	f.runs = append([]app.AgentRun{{ID: id, Short: id[:13], State: "working", Prompt: prompt, Started: f.now()}}, f.runs...)
	return id, nil
}

func (f *fakeRuns) Open(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, id)
	return nil
}

// words joins the parts that are not empty with spaces.
func words(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(p string) bool { return p == "" }), " ")
}

// The ids of the runs of agentRig.
const (
	runTests = "01a1eea5-1111-7000-8000-000000000001"
	runDocs  = "01a1eea5-2222-7000-8000-000000000002"
	runFlaky = "01a1eea5-3333-7000-8000-000000000003"
	runShip  = "01a1eea5-4444-7000-8000-000000000004"
)

// agentRig is an agent view of four runs: two working (one running a
// command, one waiting on the model), one done and one stopped.
type agentRig struct {
	t   *testing.T
	v   *agentView
	src *fakeRuns
	clk *clock
}

func newAgentRig(t *testing.T) *agentRig {
	clk := &clock{at: time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)}
	now := clk.Now()
	src := &fakeRuns{now: clk.Now, runs: []app.AgentRun{
		{ID: runTests, Short: runTests[:13], State: "working", Cwd: "/home/jae/src/app", Folder: "~/src/app",
			Prompt: "run the tests and fix what fails", Started: now.Add(-2 * time.Minute)},
		{ID: runDocs, Short: runDocs[:13], Name: "docs", State: "working", Cwd: "/home/jae/src/app", Folder: "~/src/app",
			Prompt: "write the docs", Started: now.Add(-30 * time.Second)},
		{ID: runFlaky, Short: runFlaky[:13], State: "done", Cwd: "/home/jae/src/lib", Folder: "~/src/lib", Prompt: "fix the flaky test",
			Output: []string{"The test raced on its clock.", "All 42 tests pass."}, Started: now.Add(-time.Hour), Ended: now.Add(-time.Hour + 45*time.Second)},
		{ID: runShip, Short: runShip[:13], State: "stopped", Cwd: "/home/jae/src/app", Folder: "~/src/app", Prompt: "deploy it",
			Output: []string{"Building."}, Started: now.Add(-3 * time.Hour), Ended: now.Add(-3*time.Hour + 5*time.Minute)},
	}, convs: map[string][]app.TranscriptEntry{
		runTests: {
			{Kind: "prompt", Message: core.UserText("run the tests and fix what fails")},
			{Kind: "reply", Message: core.Message{Role: core.RoleAssistant, Blocks: []core.Block{
				{Type: core.BlockText, Text: "Running the tests."},
				core.ToolUse("c1", "Bash", json.RawMessage(`{"command":"go test ./..."}`)),
			}}},
		},
		runDocs: {{Kind: "prompt", Message: core.UserText("write the docs")}},
	}}
	v := newAgentView(DefaultTheme(text.NoColor), text.Metrics{}, clk.Now, app.AgentViewInfo{Source: src, Cwd: "/home/jae/src/app", Home: "/home/jae"})
	v.refresh()
	return &agentRig{t: t, v: v, src: src, clk: clk}
}

// screen is the view at 100 by 20, as plain lines: the rows from the top,
// then the rows at the bottom.
func (g *agentRig) screen() (body, live []string) {
	b, l, _, _ := g.v.render(100, 20)
	for _, s := range b {
		body = append(body, strings.TrimRight(text.Strip(s), " "))
	}
	for _, s := range l {
		live = append(live, strings.TrimRight(text.Strip(s), " "))
	}
	return body, live
}

// keys types a key script into the view.
func (g *agentRig) keys(script string) *agentRig {
	g.t.Helper()
	evs, err := keyscript.Parse(script)
	if err != nil {
		g.t.Fatal(err)
	}
	for _, ev := range evs {
		g.v.input(ev)
	}
	return g
}

// row is the row of the list that names a run, or "".
func row(body []string, name string) string {
	for _, l := range body {
		if strings.Contains(l, " "+name) {
			return l
		}
	}
	return ""
}

// The list: a title that counts the runs, the working ones under Working
// and the rest under Completed, no line left empty between; each run with
// its mark, its name, what it is doing (the command it runs, "thinking"
// while the model has the turn) or how it ended, and its age, frozen at
// the end for one that is over. The first run is chosen, and the last row
// names the keys.
func TestAgentViewLists(t *testing.T) {
	g := newAgentRig(t)
	body, live := g.screen()
	if body[0] != "gnosh agents · 2 working · 2 completed" || body[1] != "Working" || body[4] != "Completed" {
		t.Fatalf("the list:\n%s", strings.Join(body, "\n"))
	}
	for _, c := range []struct{ name, row string }{
		{"run the tests", "> ✽ run the tests and fix w…  $ go test ./..."},
		{"docs", "  ✽ docs                      thinking"},
		{"fix the flaky test", "  ∙ fix the flaky test        All 42 tests pass."},
		{"deploy it", "  ∙ deploy it                 stopped · Building."},
	} {
		if r := row(body, c.name); !strings.HasPrefix(r, c.row) {
			t.Errorf("the row of %s is %q, not %q...", c.name, r, c.row)
		}
	}
	for name, age := range map[string]string{"run the tests": " 2m", "docs": " 30s", "fix the flaky test": " 45s", "deploy it": " 5m"} {
		if r := row(body, name); !strings.HasSuffix(r, age) || len([]rune(r)) != 100 {
			t.Errorf("the row of %s does not end at the right edge with its age %q: %q", name, age, r)
		}
	}
	if live[len(live)-1] != "↑↓ choose · Space peeks · Enter watches · Ctrl+X stops · ? keys · Esc leaves" {
		t.Errorf("the keys line: %q", live[len(live)-1])
	}
	if len(body)+len(live) != 20 {
		t.Errorf("the view is %d rows, not 20", len(body)+len(live))
	}
	// A minute on, the working runs are older and the others are not.
	g.clk.add(time.Minute)
	body, _ = g.screen()
	if !strings.HasSuffix(row(body, "docs"), " 1m") || !strings.HasSuffix(row(body, "fix the flaky test"), " 45s") {
		t.Errorf("a minute on:\n%s", strings.Join(body, "\n"))
	}
}

// Down chooses the next run; Space peeks at it (its folder, its prompt,
// what it did last, and for one that is over what it printed) and closes
// again; ? shows the keys; Esc closes them, and with nothing open leaves.
func TestAgentViewPeekAndKeys(t *testing.T) {
	g := newAgentRig(t)
	g.keys("<Space>")
	body, _ := g.screen()
	peek := strings.Join(body, "\n")
	for _, want := range []string{"─ 01a1eea5-1111 · working 2m · ~/src/app ─", "> run the tests and fix what fails", "  Running the tests.", "  ▸ $ go test ./..."} {
		if !strings.Contains(peek, want) {
			t.Errorf("peeking does not show %q:\n%s", want, peek)
		}
	}
	if strings.Count(peek, "run the tests and fix what fails") != 1 {
		t.Errorf("peeking shows the prompt twice:\n%s", peek)
	}
	g.keys("<Down> <Down>")
	body, _ = g.screen()
	peek = strings.Join(body, "\n")
	if !strings.HasPrefix(row(body, "fix the flaky test"), "> ") || !strings.Contains(peek, "─ 01a1eea5-3333 · done 45s · ~/src/lib ─") ||
		!strings.Contains(peek, "  The test raced on its clock.\n  All 42 tests pass.") {
		t.Errorf("peeking at the run that is done:\n%s", peek)
	}
	g.keys("<Space>")
	if body, _ = g.screen(); strings.Contains(strings.Join(body, "\n"), "─ 01a1eea5-3333") {
		t.Errorf("Space did not close the peek")
	}
	g.keys(`"?"`)
	if body, _ = g.screen(); !strings.Contains(strings.Join(body, "\n"), "─ keys ─") {
		t.Errorf("? did not show the keys")
	}
	g.keys("<Esc>")
	if body, _ = g.screen(); strings.Contains(strings.Join(body, "\n"), "─ keys ─") || g.v.quit {
		t.Errorf("Esc did not just close the keys")
	}
	g.keys("<End>")
	if body, _ = g.screen(); !strings.HasPrefix(row(body, "deploy it"), "> ") {
		t.Errorf("End did not choose the last run")
	}
	g.keys("<Esc>")
	if !g.v.quit {
		t.Errorf("Esc with nothing open did not leave")
	}
}

// A prompt and Enter starts a run in the view's folder and chooses it; a
// prompt too short is taken for a slip. Ctrl+C empties the box, and twice
// on an empty box leaves. A run that could not be started says why.
func TestAgentViewDispatch(t *testing.T) {
	g := newAgentRig(t)
	g.keys(`"hi" <Enter>`)
	if _, live := g.screen(); live[len(live)-1] != "Too short: a prompt of 4 characters or more starts a run." || len(g.src.dispatched) != 0 {
		t.Errorf("a short prompt: %q, %q", live, g.src.dispatched)
	}
	g.keys("<Ctrl+C>")
	if g.v.box.Text() != "" || g.v.quit {
		t.Errorf("Ctrl+C did not empty the box: %q", g.v.box.Text())
	}
	g.keys(`"tidy the README"`)
	if _, live := g.screen(); live[0] != "> tidy the README" || live[1] != "Enter starts a run in ~/src/app · Esc empties the box" {
		t.Errorf("the box with a prompt: %q", live)
	}
	g.keys("<Enter>")
	body, live := g.screen()
	if !slices.Equal(g.src.dispatched, []string{"tidy the README"}) || g.v.box.Text() != "" {
		t.Fatalf("the run was not started: %q", g.src.dispatched)
	}
	if body[0] != "gnosh agents · 3 working · 2 completed" || !strings.HasPrefix(row(body, "tidy the README"), "> ✽ tidy the README") ||
		live[len(live)-1] != "Started 01a1eea5-9999 in ~/src/app: Enter watches it." {
		t.Errorf("after the start:\n%s\n%s", strings.Join(body, "\n"), strings.Join(live, "\n"))
	}
	g.v.flags = []string{"--permission-mode=acceptEdits"}
	g.keys(`"tidy"`)
	if _, live := g.screen(); live[1] != "Enter starts a run in ~/src/app with --permission-mode=acceptEdits · Esc empties the box" {
		t.Errorf("the box with a prompt and the view's flags: %q", live)
	}
	g.v.box.Reset()
	g.src.dispatchErr = errors.New("the folder is not trusted")
	g.keys(`"tidy it again" <Enter>`)
	if _, live := g.screen(); live[len(live)-1] != "The run was not started: the folder is not trusted." {
		t.Errorf("a start that failed: %q", live)
	}
	g.keys("<Ctrl+C>")
	if _, live := g.screen(); live[len(live)-1] != "Ctrl+C again leaves the view; the runs go on." || g.v.quit {
		t.Errorf("the first Ctrl+C on an empty box: %q", live)
	}
	g.keys("<Ctrl+C>")
	if !g.v.quit {
		t.Errorf("the second Ctrl+C did not leave")
	}
}

// What the box holds is a filter when its words begin with n:, s: or o:
// (the name or prompt, the state, what it printed): the list shows only
// what passes it, and Enter opens the run chosen among them.
func TestAgentViewFilters(t *testing.T) {
	g := newAgentRig(t)
	for _, c := range []struct {
		filter string
		names  []string
	}{
		{"s:work", []string{"run the tests", "docs"}},
		{"s:stop", []string{"deploy it"}},
		{"s:completed", []string{"fix the flaky test", "deploy it"}},
		{"n:DOCS", []string{"docs"}},
		{"n:deploy s:stopped", []string{"deploy it"}},
		{"o:42", []string{"fix the flaky test"}},
		{"n:nothing", nil},
	} {
		g.v.box.Reset()
		g.keys(fmt.Sprintf("%q", c.filter))
		body, live := g.screen()
		var names []string
		for _, n := range []string{"run the tests", "docs", "fix the flaky test", "deploy it"} {
			if row(body, n) != "" {
				names = append(names, n)
			}
		}
		if !slices.Equal(names, c.names) {
			t.Errorf("%s lists %q, not %q", c.filter, names, c.names)
		}
		if c.names == nil && !strings.Contains(strings.Join(body, "\n"), "No run is like that.") {
			t.Errorf("%s with no match:\n%s", c.filter, strings.Join(body, "\n"))
		}
		if live[len(live)-1] != "Enter opens the run chosen · Esc empties the box" {
			t.Errorf("%s: the keys line is %q", c.filter, live[len(live)-1])
		}
	}
	g.v.box.Reset()
	g.keys(`"s:stopped" <Enter>`)
	if g.v.open != runShip || len(g.src.dispatched) != 0 {
		t.Errorf("Enter on a filter opened %q and started %q", g.v.open, g.src.dispatched)
	}
}

// Ctrl+X stops a working run; on one that is over it asks first, and the
// second press within two seconds takes it off the list. Later than that
// it asks again.
func TestAgentViewStopAndForget(t *testing.T) {
	g := newAgentRig(t)
	g.keys("<Ctrl+X>")
	body, live := g.screen()
	if !slices.Equal(g.src.stopped, []string{runTests}) || !strings.HasPrefix(row(body, "run the tests"), "> ∙ run the tests and fix w…  stopped") {
		t.Fatalf("Ctrl+X on a working run: stopped %q\n%s", g.src.stopped, strings.Join(body, "\n"))
	}
	if live[len(live)-1] != "01a1eea5-1111 was stopped. What it did so far is kept: gnosh -r 01a1eea5-1111 opens it" {
		t.Errorf("what was said of the stop: %q", live[len(live)-1])
	}
	g.v.sel = runFlaky
	g.keys("<Ctrl+X>")
	if _, live = g.screen(); live[len(live)-1] != "Ctrl+X again takes 01a1eea5-3333 off the list; its session is kept." || len(g.src.forgotten) != 0 {
		t.Errorf("the first Ctrl+X on a run that is over: %q, forgot %q", live, g.src.forgotten)
	}
	g.keys("<Ctrl+X>")
	body, live = g.screen()
	if !slices.Equal(g.src.forgotten, []string{runFlaky}) || row(body, "fix the flaky test") != "" ||
		live[len(live)-1] != "01a1eea5-3333 is off the list. Its session is kept: gnosh -r 01a1eea5-3333 opens it." {
		t.Errorf("the second Ctrl+X: forgot %q\n%s\n%s", g.src.forgotten, strings.Join(body, "\n"), live[len(live)-1])
	}
	g.v.sel = runShip
	g.keys("<Ctrl+X>")
	g.clk.add(3 * time.Second)
	g.v.tick()
	g.keys("<Ctrl+X>")
	if len(g.src.forgotten) != 1 {
		t.Errorf("a second Ctrl+X 3 s later took the run off the list")
	}
}

// Enter on a working run watches its conversation as the transcript view
// lays it out, and what is written to it since shows; the last row says
// whose it is and how it stands. Esc goes back. Enter on a run that is
// over opens it; so does Enter on one watched once it is over.
func TestAgentViewWatch(t *testing.T) {
	g := newAgentRig(t)
	g.keys("<Enter>")
	if g.v.watch == nil {
		t.Fatal("Enter did not watch the working run")
	}
	body, live := g.screen()
	all := strings.Join(body, "\n")
	for _, want := range []string{"> run the tests and fix what fails", "Running the tests.", "▸ Bash", `"command": "go test ./..."`} {
		if !strings.Contains(all, want) {
			t.Errorf("the run watched does not show %q:\n%s", want, all)
		}
	}
	if len(live) != 2 || live[0] != "> words for this run: it reads them between its steps" || live[1] != "watching 01a1eea5-1111 · working 2m · Ctrl+X stops it · Esc goes back" {
		t.Errorf("the last rows: %q", live)
	}
	// Words typed under it reach it between its steps, as Claude Code's
	// attached session takes a prompt; q is a letter here, and Esc first
	// empties the box.
	g.keys(`"quick: the flaky one too" <Enter>`)
	if !slices.Equal(g.src.told, []string{runTests + " quick: the flaky one too"}) || g.v.watch == nil || g.v.box.Text() != "" {
		t.Errorf("words while watching: told %q, watching %v, box %q", g.src.told, g.v.watch != nil, g.v.box.Text())
	}
	g.keys(`"never mind" <Esc>`)
	if g.v.watch == nil || g.v.box.Text() != "" {
		t.Errorf("Esc with words in the box: watching %v, box %q", g.v.watch != nil, g.v.box.Text())
	}
	g.src.mu.Lock()
	g.src.convs[runTests] = append(g.src.convs[runTests], app.TranscriptEntry{Kind: "results", Message: core.Message{Role: core.RoleUser,
		Blocks: []core.Block{core.ToolResultBlock("c1", core.TextResult("ok  example.com/app  0.4s"))}}})
	g.src.mu.Unlock()
	g.clk.add(watchLook)
	g.v.tick()
	if body, _ = g.screen(); !strings.Contains(strings.Join(body, "\n"), "ok  example.com/app  0.4s") {
		t.Errorf("what was written since does not show:\n%s", strings.Join(body, "\n"))
	}
	g.src.set(runTests, func(r *app.AgentRun) { r.State, r.Ended = "done", g.clk.Now() })
	g.clk.add(agentLook)
	g.v.tick()
	if _, live = g.screen(); live[len(live)-1] != "watching 01a1eea5-1111 · done 2m · Enter opens it · Esc goes back" {
		t.Errorf("the last row once it is over: %q", live)
	}
	g.keys("<Esc>")
	if g.v.watch != nil || g.v.quit {
		t.Fatalf("Esc did not go back to the list")
	}
	g.keys("<Enter>")
	if g.v.watch != nil || g.v.open != runTests {
		t.Errorf("Enter on a run that is over: watch %v, open %q", g.v.watch, g.v.open)
	}
}

// A run watched that waits on a question shows it above the box, as
// Claude Code's attached session shows its dialog: its number answers it,
// and words with Enter answer no, with them.
func TestAgentViewWatchAnswers(t *testing.T) {
	g := newAgentRig(t)
	ask := func() {
		g.src.set(runDocs, func(r *app.AgentRun) {
			r.State, r.Question = "blocked", &app.AgentQuestion{ID: "q1", Title: "Allow Bash?", Text: "  $ rm -rf build",
				Choices: []app.QuestionChoice{{Key: "yes", Label: "Yes, this once"}, {Key: "no", Label: "No"}}}
		})
		g.v.refresh()
	}
	ask()
	g.v.sel = runDocs
	g.keys("<Enter> <Enter>")
	if g.v.watch == nil || g.v.watch.id != runDocs {
		t.Fatalf("Enter twice on the run that asks did not watch it")
	}
	_, live := g.screen()
	all := strings.Join(live, "\n")
	for _, want := range []string{"? Allow Bash?", "    $ rm -rf build", "  1. Yes, this once", "  2. No", "1-2 answers · words and Enter: no, with them"} {
		if !strings.Contains(all, want) {
			t.Errorf("watching the run that asks does not show %q:\n%s", want, all)
		}
	}
	// (Read first: a key in the moment the question came is typing.)
	g.clk.add(time.Second)
	g.keys(`"1"`)
	if !slices.Equal(g.src.answered, []string{runDocs + " q1 yes "}) || g.v.watch == nil {
		t.Fatalf("1 while watching: answered %q, watching %v", g.src.answered, g.v.watch != nil)
	}
	ask()
	g.clk.add(time.Second)
	g.keys(`"not now, read the README first" <Enter>`)
	if len(g.src.answered) != 2 || g.src.answered[1] != runDocs+" q1 no not now, read the README first" || len(g.src.told) != 0 {
		t.Errorf("words while watching the run that asks: answered %q, told %q", g.src.answered, g.src.told)
	}
}

// A question takes no answer in the moment it came, as the session's own
// questions do: a digit typed then is typing, in the box, and words sent
// then go to the run, which reads them once it goes on; once it has been
// on the screen a while and the person has stopped typing, the digit
// answers (a review: "2 tests still fail…" answered a question that had
// just come 2).
func TestAgentViewQuestionGuard(t *testing.T) {
	g := newAgentRig(t)
	g.v.sel = runDocs
	g.keys("<Enter>")
	if g.v.watch == nil {
		t.Fatal("Enter did not watch the working run")
	}
	g.src.set(runDocs, func(r *app.AgentRun) {
		r.State, r.Question = "blocked", &app.AgentQuestion{ID: "q1", Title: "Allow Bash?",
			Choices: []app.QuestionChoice{{Key: "yes", Label: "Yes"}, {Key: "dont", Label: "Yes, and don't ask again"}, {Key: "no", Label: "No"}}}
	})
	g.v.refresh()
	g.keys(`"2 tests still fail" <Enter>`)
	if len(g.src.answered) != 0 || !slices.Equal(g.src.told, []string{runDocs + " 2 tests still fail"}) {
		t.Fatalf("typed as the question came: answered %q, told %q", g.src.answered, g.src.told)
	}
	g.clk.add(time.Second)
	g.keys(`"1"`)
	if !slices.Equal(g.src.answered, []string{runDocs + " q1 yes "}) {
		t.Errorf("1 a second later: answered %q", g.src.answered)
	}
}

// The view on a terminal: it takes the whole screen; Enter on a run that
// is over gives the terminal to gnosh -r and takes it back after, the list
// drawn again; Esc leaves, and the terminal is put back.
func TestAgentViewOnATerminal(t *testing.T) {
	g := newAgentRig(t)
	ft := newFakeTerm(100, 20, vtest.AllAnswers)
	open := NewAgentView(Options{Open: func(TerminalHooks) (Terminal, error) { return ft, nil }, Now: g.clk.Now})
	done := make(chan int, 1)
	go func() { done <- open(app.AgentViewInfo{Source: g.src, Cwd: "/home/jae/src/app", Home: "/home/jae"}) }()
	l := &loop{t: t, ft: ft}
	l.has("gnosh agents · 2 working · 2 completed", "$ go test ./...")
	l.keys("<Down> <Down> <Enter>")
	l.eventually("gnosh -r ran", func(string) bool {
		g.src.mu.Lock()
		defer g.src.mu.Unlock()
		return slices.Equal(g.src.opened, []string{runFlaky})
	})
	l.has("All 42 tests pass.")
	ft.mu.Lock()
	released, resumed := ft.released, ft.resumed
	ft.mu.Unlock()
	if released != 1 || resumed != 1 {
		t.Errorf("the terminal was released %d times and taken back %d", released, resumed)
	}
	l.keys("<Esc>")
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("the view ended with %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Esc did not leave the view")
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if ft.closed != 1 {
		t.Errorf("the terminal was closed %d times", ft.closed)
	}
}

// A run that waits on a permission question comes first, under Needs
// input, with the question on its row; Enter shows it whole with its
// answers numbered, a number answers it, and words with Enter answer no
// with them, which the model reads.
func TestAgentViewNeedsInput(t *testing.T) {
	g := newAgentRig(t)
	const runAsk = "01a1eea5-5555-7000-8000-000000000005"
	question := func() *app.AgentQuestion {
		return &app.AgentQuestion{ID: "q7", Title: "Allow Bash?", Text: "  $ rm -rf build\nWhy gnosh asks: it removes files.",
			Since: g.clk.Now().Add(-3 * time.Minute), Choices: []app.QuestionChoice{
				{Key: "yes", Label: "Yes, this once"},
				{Key: "always:0", Label: "Yes, and don't ask again this session for Bash(rm:*)"},
				{Key: "no", Label: "No (alone it stops the turn; with words after it, the model reads them and carries on)"},
			}}
	}
	g.src.runs = append(g.src.runs, app.AgentRun{ID: runAsk, Short: runAsk[:13], State: "blocked", Cwd: "/home/jae/src/app", Folder: "~/src/app",
		Prompt: "clean the build", Started: g.clk.Now().Add(-5 * time.Minute), Question: question()})
	g.v.sel = ""
	g.v.refresh()
	body, live := g.screen()
	if body[0] != "gnosh agents · 1 needs input · 2 working · 2 completed" || body[1] != "Needs input" ||
		!strings.HasPrefix(body[2], "> ? clean the build           Allow Bash? $ rm -rf build") || body[3] != "Working" {
		t.Fatalf("the list:\n%s", strings.Join(body, "\n"))
	}
	if live[len(live)-1] != "↑↓ choose · Space peeks · Enter answers · Ctrl+X stops · ? keys · Esc leaves" {
		t.Errorf("the keys line: %q", live[len(live)-1])
	}
	g.keys("<Enter>")
	body, live = g.screen()
	all := strings.Join(body, "\n")
	for _, want := range []string{"─ 01a1eea5-5555 · needs input · waiting 3m · ~/src/app ─", "> clean the build", "? Allow Bash?",
		"    $ rm -rf build", "  Why gnosh asks: it removes files.", "  1. Yes, this once", "  3. No (alone it stops the turn"} {
		if !strings.Contains(all, want) {
			t.Errorf("the question does not show %q:\n%s", want, all)
		}
	}
	if live[len(live)-1] != "1-3 answers · words and Enter: no, with them · Enter watches · Esc closes" || g.v.watch != nil {
		t.Errorf("the keys line while answering: %q", live[len(live)-1])
	}
	g.clk.add(time.Second)
	g.keys(`"1"`)
	if !slices.Equal(g.src.answered, []string{runAsk + " q7 yes "}) {
		t.Fatalf("1 answered %q", g.src.answered)
	}
	if body, live = g.screen(); live[len(live)-1] != "Answered 01a1eea5-5555: Yes, this once." || !strings.HasPrefix(body[0], "gnosh agents · 3 working") {
		t.Errorf("after the answer:\n%s\n%s", strings.Join(body, "\n"), live[len(live)-1])
	}
	// Words and Enter: no, with them.
	g.src.set(runAsk, func(r *app.AgentRun) { r.State, r.Question = "blocked", question() })
	g.v.refresh()
	g.v.sel = runAsk
	g.clk.add(time.Second)
	g.keys(`<Enter> "use git clean instead" <Enter>`)
	if len(g.src.answered) != 2 || g.src.answered[1] != runAsk+" q7 no use git clean instead" || len(g.src.dispatched) != 0 {
		t.Errorf("words and Enter answered %q and started %q", g.src.answered, g.src.dispatched)
	}
}

// Words typed while peeking at a run that is over, and Enter, send it back
// to work on them (gnosh --bg -r), where without the peek they would start
// a new run; a working run is told them, and if it ends before it reads
// them it is sent back to work on them.
func TestAgentViewReply(t *testing.T) {
	g := newAgentRig(t)
	g.v.sel = runFlaky
	g.keys(`<Space> "and the README too"`)
	if _, live := g.screen(); live[len(live)-1] != "Enter sends 01a1eea5-3333 back to work on it · Esc empties the box" {
		t.Errorf("the keys line while replying: %q", live[len(live)-1])
	}
	g.keys("<Enter>")
	if !slices.Equal(g.src.resumed, []string{runFlaky + " and the README too"}) || len(g.src.dispatched) != 0 {
		t.Fatalf("the reply resumed %q and started %q", g.src.resumed, g.src.dispatched)
	}
	body, live := g.screen()
	if !strings.HasPrefix(row(body, "and the README too"), "> ✽ and the README too") || live[len(live)-1] != "01a1eea5-3333 goes on in the background: Enter watches it." {
		t.Errorf("after the reply:\n%s\n%s", strings.Join(body, "\n"), live[len(live)-1])
	}
	g.v.sel = runTests
	g.keys(`"stop and test again"`)
	if _, live = g.screen(); live[len(live)-1] != "Enter sends your words to 01a1eea5-1111 · Esc empties the box" {
		t.Errorf("the keys line while telling a working run: %q", live[len(live)-1])
	}
	g.keys("<Enter>")
	if _, live = g.screen(); live[len(live)-1] != "Sent to 01a1eea5-1111: it reads your words between its steps." ||
		!slices.Equal(g.src.told, []string{runTests + " stop and test again"}) || len(g.src.resumed) != 1 {
		t.Fatalf("a reply to a working run: %q, told %q, resumed %q", live[len(live)-1], g.src.told, g.src.resumed)
	}
	// It ends before it reads them: it goes on with them.
	g.src.unread = map[string][]string{runTests: {"stop and test again"}}
	g.src.set(runTests, func(r *app.AgentRun) { r.State, r.Ended = "done", g.clk.Now() })
	g.v.refresh()
	if _, live = g.screen(); live[len(live)-1] != "01a1eea5-1111 ended before it read your words, and goes on with them in the background." ||
		!slices.Equal(g.src.resumed[1:], []string{runTests + " stop and test again"}) {
		t.Errorf("words a run did not read: %q, resumed %q", live[len(live)-1], g.src.resumed)
	}
}

// The view tells the person of what changed since its last look, as
// Claude Code's notifies: a run that needs input, and one that is done;
// not of what was there as it opened. The title counts the runs that
// wait on the person. On a terminal the notes go by the notification
// channel (here the bell).
func TestAgentViewTells(t *testing.T) {
	g := newAgentRig(t)
	if len(g.v.notes) != 0 || g.v.title() != "gnosh agents" {
		t.Fatalf("as the view opened: notes %q, title %q", g.v.notes, g.v.title())
	}
	g.src.set(runDocs, func(r *app.AgentRun) {
		r.State, r.Question = "blocked", &app.AgentQuestion{ID: "q1", Title: "Allow Bash?", Choices: []app.QuestionChoice{{Key: "yes", Label: "Yes"}}}
	})
	g.src.set(runTests, func(r *app.AgentRun) { r.State, r.Ended, r.Output = "done", g.clk.Now(), []string{"All tests pass."} })
	g.v.refresh()
	if want := []string{"docs needs input: Allow Bash?", "01a1eea5-1111 is done: All tests pass."}; !slices.Equal(g.v.notes, want) {
		t.Errorf("the notes: %q, want %q", g.v.notes, want)
	}
	if g.v.title() != "1 awaiting input · gnosh agents" {
		t.Errorf("the title: %q", g.v.title())
	}
	g.v.notes = nil
	g.v.refresh()
	if len(g.v.notes) != 0 {
		t.Errorf("nothing changed, and the notes are %q", g.v.notes)
	}

	h := newAgentRig(t)
	ft := newFakeTerm(100, 20, vtest.AllAnswers)
	open := NewAgentView(Options{Open: func(TerminalHooks) (Terminal, error) { return ft, nil }, Now: h.clk.Now})
	done := make(chan int, 1)
	go func() {
		done <- open(app.AgentViewInfo{Source: h.src, Cwd: "/home/jae/src/app", Home: "/home/jae", Notify: "terminal_bell"})
	}()
	l := &loop{t: t, ft: ft}
	l.has("gnosh agents · 2 working")
	h.src.set(runDocs, func(r *app.AgentRun) {
		r.State, r.Question = "blocked", &app.AgentQuestion{ID: "q1", Title: "Allow Bash?", Choices: []app.QuestionChoice{{Key: "yes", Label: "Yes"}}}
	})
	h.clk.add(agentLook)
	l.has("1 needs input")
	l.eventually("the bell rang and the title says so", func(string) bool {
		ok := false
		ft.look(func(scr *vtest.Screen) { ok = scr.Bells() == 1 && scr.Title() == "1 awaiting input · gnosh agents" })
		return ok
	})
	l.keys("<Esc>")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Esc did not leave the view")
	}
}

// A run that failed is marked so (✗), as Claude Code's list marks one in
// red: its row says failed and what it printed last, its peek says how it
// ended, the view tells of it as it does of one that is done, and s:failed
// lists it. One that a signal stopped says so in its peek.
func TestAgentViewFailedRuns(t *testing.T) {
	g := newAgentRig(t)
	g.src.set(runTests, func(r *app.AgentRun) {
		r.State, r.Ending, r.Ended = "failed", "it ended with exit status 1", g.clk.Now()
		r.Output = []string{"Running the tests.", "gnosh: the model server did not answer."}
	})
	g.src.set(runShip, func(r *app.AgentRun) { r.Ending = "it was stopped by a signal" })
	g.v.refresh()
	if want := []string{"01a1eea5-1111 failed: gnosh: the model server did not answer."}; !slices.Equal(g.v.notes, want) {
		t.Errorf("the notes: %q, want %q", g.v.notes, want)
	}
	body, _ := g.screen()
	if r := row(body, "run the tests"); !strings.HasPrefix(r, "> ✗ run the tests and fix w…  failed · gnosh: the model server did not answer.") {
		t.Errorf("the failed run's row: %q\n%s", r, strings.Join(body, "\n"))
	}
	g.keys("<Space>")
	body, _ = g.screen()
	peek := strings.Join(body, "\n")
	for _, want := range []string{"─ 01a1eea5-1111 · failed 2m · ~/src/app ─", "> run the tests and fix what fails\n✗ it ended with exit status 1\n  Running the tests.\n  gnosh: the model server did not answer."} {
		if !strings.Contains(peek, want) {
			t.Errorf("peeking at the failed run does not show %q:\n%s", want, peek)
		}
	}
	g.keys("<Space> <End> <Space>")
	if body, _ = g.screen(); !strings.Contains(strings.Join(body, "\n"), "> deploy it\n∙ it was stopped by a signal\n  Building.") {
		t.Errorf("peeking at the run a signal stopped:\n%s", strings.Join(body, "\n"))
	}
	g.keys(`<Space> <Paste "s:failed">`)
	body, _ = g.screen()
	if !strings.Contains(strings.Join(body, "\n"), "run the tests") || strings.Contains(strings.Join(body, "\n"), "deploy it") {
		t.Errorf("s:failed does not list only the failed run:\n%s", strings.Join(body, "\n"))
	}
}

// /stop in the box stops the run chosen, as Ctrl+X does (Claude Code's
// reply of exactly /stop), and says so of one that is not running; s:needs
// lists the runs that wait on the person. In colour, gnosh's own words in
// a peek keep their colour: what a run printed is made safe to show, and
// the line that says it printed nothing is not (which showed its colour's
// codes as text on a box).
func TestAgentViewStopCommandAndPlaceholder(t *testing.T) {
	g := newAgentRig(t)
	g.keys(`<Paste "/stop"> <Enter>`)
	if !slices.Equal(g.src.stopped, []string{runTests}) {
		t.Fatalf("/stop stopped %q", g.src.stopped)
	}
	g.v.said = ""
	g.keys(`<End> <Paste "/stop"> <Enter>`)
	if len(g.src.stopped) != 1 || !strings.Contains(g.v.said, "is not running") {
		t.Errorf("/stop on a run that is over: stopped %q, said %q", g.src.stopped, g.v.said)
	}
	g.src.set(runDocs, func(r *app.AgentRun) {
		r.State, r.Question = "blocked", &app.AgentQuestion{ID: "q1", Title: "Allow Bash?", Choices: []app.QuestionChoice{{Key: "yes", Label: "Yes"}}}
	})
	g.v.refresh()
	g.keys(`<Paste "s:needs">`)
	if body, _ := g.screen(); row(body, "docs") == "" || row(body, "deploy it") != "" {
		t.Errorf("s:needs:\n%s", strings.Join(body, "\n"))
	}

	c := newAgentRig(t)
	c.v = newAgentView(DefaultTheme(text.ANSI256), text.Metrics{}, c.clk.Now, app.AgentViewInfo{Source: c.src, Cwd: "/home/jae/src/app", Home: "/home/jae"})
	c.src.set(runShip, func(r *app.AgentRun) { r.Output = nil })
	c.v.refresh()
	c.keys(`<End> <Space>`)
	body, _, _, _ := c.v.render(100, 20)
	all := strings.Join(body, "\n")
	if !strings.Contains(text.Strip(all), "  (it printed nothing)") || strings.Contains(all, "^[") {
		t.Errorf("the peek of a run that printed nothing:\n%q", all)
	}
}

// "@name prompt" starts the run in the folder named, as Claude Code's
// @<repo>: a git repository one level below the view's folder, or the one
// folder listed runs work in with that name. A kind of agent of that name
// wins, and a name that is neither is taken for a kind, as before.
func TestAgentViewDispatchesToAFolder(t *testing.T) {
	g := newAgentRig(t)
	g.v.info.Kinds = []string{"reviewer", "lib"}
	g.v.info.Repos = []string{"/home/jae/src/app/api"}
	g.keys(`<Paste "@api add the endpoint"> <Enter>`)
	g.keys(`<Paste "@src look around"> <Enter>`)
	g.keys(`<Paste "@lib fix the flaky one"> <Enter>`)
	g.v.info.Kinds = nil
	g.keys(`<Paste "@lib fix the flaky one"> <Enter>`)
	want := []string{"in /home/jae/src/app/api add the endpoint", "--agent=src look around", "--agent=lib fix the flaky one", "in /home/jae/src/lib fix the flaky one"}
	if !slices.Equal(g.src.dispatched, want) {
		t.Errorf("dispatched %q, want %q", g.src.dispatched, want)
	}
	if !strings.Contains(g.v.said, "~/src/lib") {
		t.Errorf("it says %q", g.v.said)
	}
}

// Alt+↓ chooses the first run of the next group and Alt+↑ the first of
// the group chosen, or of the one before when it is that one already, as
// Claude Code's go between group headers; Ctrl+F starts its n: filter.
func TestAgentViewGroupJumpsAndFind(t *testing.T) {
	g := newAgentRig(t)
	chosen := func(want string) {
		t.Helper()
		if r, _ := g.v.chosen(); r.ID != want {
			t.Fatalf("the run chosen is %s, not %s", r.ID, want)
		}
	}
	chosen(runTests)
	g.keys(`<Alt+Down>`)
	chosen(runFlaky)
	g.keys(`<Alt+Down>`)
	chosen(runFlaky)
	g.keys(`<Alt+Up>`)
	chosen(runTests)
	g.keys(`<Down> <Alt+Up>`)
	chosen(runTests)
	g.keys(`<End> <Alt+Up>`)
	chosen(runFlaky)
	g.keys(`<Ctrl+F>`)
	if g.v.box.Text() != "n:" {
		t.Fatalf("Ctrl+F put %q in the box", g.v.box.Text())
	}
	g.keys(`"docs"`)
	if body, _ := g.screen(); row(body, "docs") == "" || row(body, "deploy it") != "" {
		t.Errorf("n:docs after Ctrl+F:\n%s", strings.Join(body, "\n"))
	}
}

// /model and /effort typed in the box set what the runs started from the
// view are given, for the view only, and default puts back what it was
// opened with; "@name prompt" starts a run as that kind of agent; /exit
// leaves.
func TestAgentViewBoxCommands(t *testing.T) {
	g := newAgentRig(t)
	g.v.info.Flags = []string{"--effort=low"}
	g.v.flags = []string{"--effort=low"}
	g.keys(`"/model qwen/other" <Enter>`)
	if _, live := g.screen(); live[len(live)-1] != "New runs from here start with --model=qwen/other (for this view only)." {
		t.Errorf("after /model: %q", live[len(live)-1])
	}
	g.keys(`"/effort high" <Enter> "@reviewer look over the diff" <Enter>`)
	if want := []string{"--model=qwen/other --effort=high --agent=reviewer look over the diff"}; !slices.Equal(g.src.dispatched, want) {
		t.Errorf("the run was started with %q, want %q", g.src.dispatched, want)
	}
	g.keys(`"/effort default" <Enter> "/model" <Enter>`)
	if _, live := g.screen(); live[len(live)-1] != "New runs from here start with --model=qwen/other; /model default puts back what gnosh would take." ||
		!slices.Equal(g.v.flags, []string{"--model=qwen/other", "--effort=low"}) {
		t.Errorf("after /effort default and /model: %q, flags %q", live[len(live)-1], g.v.flags)
	}
	// While peeking, a command of the view's is still one, not words for
	// the run.
	g.v.sel = runTests
	g.keys(`<Space> "/effort max" <Enter>`)
	if len(g.src.told) != 0 || !slices.Contains(g.v.flags, "--effort=max") {
		t.Errorf("/effort while peeking: told %q, flags %q", g.src.told, g.v.flags)
	}
	g.keys(`"/exit" <Enter>`)
	if !g.v.quit || len(g.src.dispatched) != 1 {
		t.Errorf("/exit: quit %v, dispatched %q", g.v.quit, g.src.dispatched)
	}
}

// Ctrl+S groups the runs by folder: each row then says how it stands, and
// a prompt starts a run in the folder of the run chosen, as Claude Code's
// view does. Ctrl+S again groups them by how they stand.
func TestAgentViewByFolder(t *testing.T) {
	g := newAgentRig(t)
	g.keys("<Ctrl+S>")
	body, _ := g.screen()
	all := strings.Join(body, "\n")
	if body[1] != "~/src/app" || !strings.Contains(all, "\n~/src/lib\n") || strings.Contains(all, "\nWorking\n") {
		t.Fatalf("grouped by folder:\n%s", all)
	}
	for name, want := range map[string]string{"run the tests": "working · $ go test ./...", "fix the flaky test": "done · All 42 tests pass.", "deploy it": "stopped · Building."} {
		if !strings.Contains(row(body, name), want) {
			t.Errorf("the row of %s does not say %q: %q", name, want, row(body, name))
		}
	}
	g.v.sel = runFlaky
	g.keys(`"tidy the lib"`)
	if _, live := g.screen(); live[len(live)-1] != "Enter starts a run in ~/src/lib · Esc empties the box" {
		t.Errorf("the keys line: %q", live[len(live)-1])
	}
	g.keys("<Enter>")
	if !slices.Equal(g.src.dispatched, []string{"in /home/jae/src/lib tidy the lib"}) {
		t.Errorf("the run was started %q", g.src.dispatched)
	}
	g.keys("<Ctrl+S>")
	if body, _ = g.screen(); body[1] != "Working" {
		t.Errorf("grouped by how they stand again:\n%s", strings.Join(body, "\n"))
	}
}

// The list calls a run by the name it was given, or else by its prompt's
// first line; Ctrl+R puts /rename and its name in the box, and Enter names
// it so (an empty name takes the name away).
func TestAgentViewRename(t *testing.T) {
	g := newAgentRig(t)
	g.keys("<Ctrl+R>")
	if g.v.box.Text() != "/rename " {
		t.Errorf("Ctrl+R put %q in the box", g.v.box.Text())
	}
	g.keys(`"nightly tests" <Enter>`)
	body, live := g.screen()
	if !slices.Equal(g.src.renamed, []string{runTests + " nightly tests"}) || !strings.HasPrefix(row(body, "nightly tests"), "> ✽ nightly tests") ||
		live[len(live)-1] != "01a1eea5-1111 is named nightly tests." {
		t.Errorf("after the rename: renamed %q\n%s\n%s", g.src.renamed, strings.Join(body, "\n"), live[len(live)-1])
	}
	g.keys(`<Ctrl+R> <Ctrl+W> <Ctrl+W> <Enter>`)
	if body, _ = g.screen(); row(body, "run the tests") == "" {
		t.Errorf("the name was not taken away:\n%s", strings.Join(body, "\n"))
	}
}

// gnosh attach opens the view on one run: a working one is watched, one
// that waits on a question shows it, one that is over is opened (gnosh
// -r), and one that is not there is said so.
func TestAgentViewAttach(t *testing.T) {
	g := newAgentRig(t)
	g.v.focusOn(runTests)
	if g.v.watch == nil || g.v.watch.id != runTests || g.v.sel != runTests {
		t.Errorf("attached to a working run: watch %v, chosen %q", g.v.watch, g.v.sel)
	}
	g = newAgentRig(t)
	g.v.focusOn(runFlaky)
	if g.v.open != runFlaky || g.v.watch != nil {
		t.Errorf("attached to a run that is over: open %q", g.v.open)
	}
	g = newAgentRig(t)
	g.src.set(runDocs, func(r *app.AgentRun) {
		r.State, r.Question = "blocked", &app.AgentQuestion{ID: "q", Title: "Allow Bash?", Choices: []app.QuestionChoice{{Key: "yes", Label: "Yes"}}}
	})
	g.v.refresh()
	g.v.focusOn(runDocs)
	if !g.v.peek || g.v.sel != runDocs || !g.v.answering() {
		t.Errorf("attached to a run that waits: peek %v, chosen %q", g.v.peek, g.v.sel)
	}
	g.v.focusOn("01a1eea5-0000-7000-8000-000000000000")
	if _, live := g.screen(); live[len(live)-1] != "The run 01a1eea5-0000 is not among the runs in the background." {
		t.Errorf("attached to no run: %q", live[len(live)-1])
	}
}

// While a question is shown, a command or a filter typed in the box keeps
// its meaning and does not answer it; other words answer no, with them,
// as the keys line says. A question too long for the screen keeps its
// head and its answers in sight, and Enter again watches the whole.
func TestAgentViewAnsweringKeepsCommands(t *testing.T) {
	g := newAgentRig(t)
	long := strings.Repeat("a line of the plan\n", 30)
	g.src.set(runDocs, func(r *app.AgentRun) {
		r.State, r.Question = "blocked", &app.AgentQuestion{ID: "q9", Title: "Carry out this plan?", Text: long,
			Choices: []app.QuestionChoice{{Key: "yes", Label: "Yes, carry it out"}, {Key: "no", Label: "No, keep planning"}}}
	})
	g.v.refresh()
	g.v.sel = runDocs
	g.keys("<Enter>")
	body, live := g.screen()
	all := strings.Join(body, "\n")
	if !strings.Contains(all, "  1. Yes, carry it out") || !strings.Contains(all, "  2. No, keep planning") || !strings.Contains(all, "more (Enter watches the conversation, where it is whole)") {
		t.Errorf("a long question at 20 rows:\n%s", all)
	}
	if len(body)+len(live) != 20 {
		t.Errorf("the frame is %d rows", len(body)+len(live))
	}
	g.keys(`"/rename planner" <Enter>`)
	if len(g.src.answered) != 0 || !slices.Equal(g.src.renamed, []string{runDocs + " planner"}) {
		t.Errorf("/rename while a question is shown: answered %q, renamed %q", g.src.answered, g.src.renamed)
	}
	g.v.peek = true
	g.clk.add(time.Second)
	g.keys(`"use smaller steps"`)
	if _, live = g.screen(); live[len(live)-1] != "Enter answers no, with your words · Esc empties the box" {
		t.Errorf("the keys line with words for the question: %q", live[len(live)-1])
	}
	g.keys("<Enter>")
	if !slices.Equal(g.src.answered, []string{runDocs + " q9 no use smaller steps"}) {
		t.Errorf("words and Enter answered %q", g.src.answered)
	}
	// Enter again, on a question shown, watches the conversation.
	g.src.set(runDocs, func(r *app.AgentRun) {
		r.State, r.Question = "blocked", &app.AgentQuestion{ID: "q10", Title: "Allow Bash?", Choices: []app.QuestionChoice{{Key: "yes", Label: "Yes"}}}
	})
	g.v.refresh()
	g.v.sel, g.v.peek = runDocs, true
	g.keys("<Enter>")
	if g.v.watch == nil || g.v.watch.id != runDocs {
		t.Errorf("Enter on a question shown did not watch the run")
	}
}

// A run the person stopped is not told of as done, and words it was sent
// and did not read are not sent again: they are said.
func TestAgentViewStoppedBeforeItRead(t *testing.T) {
	g := newAgentRig(t)
	g.v.sel, g.v.peek = runTests, true
	g.keys(`"and the docs too" <Enter> <Ctrl+X>`)
	if !slices.Equal(g.src.stopped, []string{runTests}) {
		t.Fatalf("stopped %q", g.src.stopped)
	}
	g.src.unread = map[string][]string{runTests: {"and the docs too"}}
	g.v.notes = nil
	g.v.refresh()
	if len(g.src.resumed) != 0 {
		t.Errorf("a stopped run was sent back to work: %q", g.src.resumed)
	}
	for _, n := range g.v.notes {
		if strings.Contains(n, "is done") {
			t.Errorf("a stopped run was told of as done: %q", n)
		}
	}
	// (Stopping made it "stopped" in the fake; one whose log says done
	// while it was being stopped is not told of either.)
	g.v.stopping[runDocs] = true
	g.src.set(runDocs, func(r *app.AgentRun) { r.State, r.Ended = "done", g.clk.Now() })
	g.v.refresh()
	for _, n := range g.v.notes {
		if strings.Contains(n, "docs is done") {
			t.Errorf("a run being stopped was told of as done: %q", n)
		}
	}
}
