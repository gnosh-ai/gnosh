package app

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// scripted is a front end as another interface would be attached
// (Options.Interactive): it keeps the events it is told, and what it does
// with the session's controls is the test's to say.
type scripted struct {
	t    *testing.T
	info FrontendInfo
	run  func(ctx context.Context, c SessionControls, f *scripted) int

	mu     sync.Mutex
	events []any
}

func (f *scripted) Event(ev any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
}

// Run hands the test the session's controls, all of them: what a front
// end is given is always more than the Controls it is declared to take.
func (f *scripted) Run(ctx context.Context, c Controls) int {
	more, ok := c.(SessionControls)
	if !ok {
		f.t.Errorf("the front end was handed controls of type %T, which are not SessionControls", c)
		return 1
	}
	return f.run(ctx, more, f)
}

// find waits for an event that ok accepts, and returns it.
func find[E any](f *scripted, what string, ok func(E) bool) E {
	f.t.Helper()
	deadline := time.Now().Add(patience)
	for {
		f.mu.Lock()
		for _, ev := range f.events {
			if e, is := ev.(E); is && (ok == nil || ok(e)) {
				f.mu.Unlock()
				return e
			}
		}
		f.mu.Unlock()
		if time.Now().After(deadline) {
			f.t.Fatalf("the front end was never told of %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// attach runs an interactive session with a scripted front end attached.
func attach(t *testing.T, w *apptest.World, change func(*Options), run func(ctx context.Context, c SessionControls, f *scripted) int, args ...string) (*scripted, result) {
	t.Helper()
	front := &scripted{t: t, run: run}
	o := testOptions(w, args...)
	o.StdinIsTerminal, o.StdoutIsTerminal = true, true
	o.Signals = make(chan os.Signal, 4)
	o.Interactive = func(info FrontendInfo) Frontend {
		front.info = info
		return front
	}
	if change != nil {
		change(&o)
	}
	return front, runWith(t, t.Context(), o)
}

// The maker of an interface is handed the command line's prompt, and may
// decline: the session is then the plain one.
func TestInterfaceMakerIsHandedThePromptAndMayDecline(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hello to you."})
	front, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		if _, err := c.Submit(agent.Prompt{Text: f.info.Prompt}); err != nil {
			return 1
		}
		_ = c.Wait(ctx)
		c.Quit("prompt_input_exit")
		return 0
	}, "hello there")
	r.wantCode(t, 0)
	if front.info.Prompt != "hello there" || front.info.Getenv == nil || front.info.Stdout == nil {
		t.Errorf("the maker was handed %+v", front.info)
	}
	if users := userTexts(w.Model.Requests()[0]); len(users) != 1 || !strings.HasSuffix(users[0], "hello there") {
		t.Errorf("the session's first prompt: %q", users)
	}

	// A maker that declines.
	w = apptest.NewWorld(t, apptest.Reply{Text: "Plainly."})
	o := testOptions(w)
	o.Stdin = strings.NewReader("say it plainly\n")
	o.StdinIsTerminal, o.StdoutIsTerminal = true, true
	asked := false
	o.Interactive = func(FrontendInfo) Frontend { asked = true; return nil }
	r = runWith(t, t.Context(), o).wantCode(t, 0)
	if !asked {
		t.Error("the maker was not asked")
	}
	has(t, "the plain session", r.out, "Type a prompt and Enter.", "Plainly.")
}

// A line of the person's own behind "!": it runs without a question, it
// is shown as a tool call is, and the model is handed the command and
// what it printed and answers. A long one is moved to the background, and
// the status line's facts count it.
func TestShellLineOfThePersonsOwn(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "I see the file was made and the command said so."}, apptest.Reply{Text: "It runs in the background now."})
	w.Settings(`{"options": {"respondToBashCommands": true}}`)
	front, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		// In the default mode the model would be asked about this one.
		if err := c.Shell("touch mine.txt && echo made it myself"); err != nil {
			t.Errorf("Shell: %v", err)
			return 1
		}
		if err := c.Shell("echo a second at once"); err == nil {
			t.Error("a second command of the person's was started while the first ran")
		}
		find(f, "the turn that answers the command", func(e *agent.TurnEndEvent) bool { return true })
		_ = c.Wait(ctx)

		// A long one, moved to the background.
		if err := c.Shell("sleep 30; echo late"); err != nil {
			t.Errorf("Shell: %v", err)
			return 1
		}
		start := find(f, "the long command's start", func(e *agent.ToolStartEvent) bool { return strings.Contains(string(e.Input), "sleep 30") })
		if !c.Background(start.CallID, start.Name) {
			t.Error("the running command could not be moved to the background")
		}
		end := find(f, "the long command's call to return", func(e *agent.ToolEndEvent) bool { return e.CallID == start.CallID })
		if end.Interrupted {
			t.Errorf("a command that was moved to the background is reported as stopped")
		}
		deadline := time.Now().Add(patience)
		for c.Status()["background"] != "1" {
			if time.Now().After(deadline) {
				t.Errorf("the facts for the status line are %v, want one command in the background", c.Status())
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if c.Background("no-such-call", "Read") {
			t.Error("Background found a command for a call that is not there")
		}
		_ = c.Wait(ctx)
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
	if !w.Exists("mine.txt") {
		t.Errorf("the person's command did not run")
	}
	for _, ev := range front.events {
		if q, ok := ev.(*agent.QuestionEvent); ok {
			t.Errorf("the person was asked about their own command: %+v", q.Question.Title)
		}
	}
	end := find(front, "the command's end", func(e *agent.ToolEndEvent) bool { return strings.Contains(string(e.Input), "touch mine.txt") })
	if end.Name != "Bash" || !end.Ran || end.Denied || !strings.Contains(textOf(end), "made it myself") {
		t.Errorf("the command was shown as %+v", end)
	}
	first := userTexts(w.Model.Requests()[0])
	last := first[len(first)-1]
	has(t, "what the model was handed", last, "The user ran this shell command themselves", "$ touch mine.txt && echo made it myself", "made it myself")
	if got := find(front, "a status event", func(e *StatusEvent) bool { return e.Facts["background"] == "1" }); got.StatusFacts()["background"] != "1" {
		t.Errorf("the status event: %+v", got)
	}
	checkSession(t, w, w.Sessions()[0])
}

func textOf(e *agent.ToolEndEvent) string {
	var b strings.Builder
	for _, c := range e.Result.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// What else an interface asks of the session's controls: the prompts of
// earlier sessions of the folder, one queued prompt taken back, what the
// commands are, the facts a feature and the session have for a status
// line, and what a permission question says of itself.
func TestControlsForAnInterface(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "one"}, apptest.Reply{Text: "two"})
	run(t, w, "-p", "an earlier prompt").wantCode(t, 0)
	run(t, w, "-p", "another one").wantCode(t, 0)

	hold := make(chan struct{})
	w.Model.Add(
		apptest.Reply{Text: "This reply is held.", Hold: hold},
		apptest.Reply{Text: "The second queued prompt is answered."},
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"touch made.txt"}`}}},
		apptest.Reply{Text: "Made."})
	feature := Feature{
		Name: "frames",
		Session: func(ctx context.Context, s *Session) (*Contribution, error) {
			return &Contribution{
				Ready: func(*agent.Agent) { s.Status("frame", "fix-flaky") },
				Slash: []SlashCommand{{Name: "vault", Args: "show|redact <text>", Summary: "Looks into the vault.", Sensitive: []string{"redact"},
					Run: func(context.Context, *SlashContext) error { return nil }}},
			}, nil
		},
	}
	var history, later []string
	var taken agent.Prompt
	var took, again bool
	var queued []agent.QueuedPrompt
	var info SessionInfo
	var facts map[string]string
	front, r := attach(t, w, func(o *Options) { o.Features = []Feature{feature} }, func(ctx context.Context, c SessionControls, f *scripted) int {
		history = c.History()
		info = c.Info()
		facts = c.Status()
		if _, err := c.Submit(agent.Prompt{Text: "now this"}); err != nil {
			return 1
		}
		find(f, "the first piece of the held reply", func(e *agent.TextEvent) bool { return true })
		idOne, _ := c.Submit(agent.Prompt{Text: "queued one"})
		c.Submit(agent.Prompt{Text: "queued two"})
		find(f, "both prompts in the queue", func(e *agent.QueueEvent) bool { return len(e.Queued) == 2 })
		taken, took = c.Withdraw(idOne)
		_, again = c.Withdraw(idOne)
		queued = c.Queued()
		close(hold)
		_ = c.Wait(ctx)
		later = c.History()

		// A question, answered yes with a word for the model.
		c.Submit(agent.Prompt{Text: "make the file"})
		q := find(f, "the question", func(e *agent.QuestionEvent) bool { return true })
		if err := c.Answer(q.Question.ID, agent.Answer{Choice: "yes", Text: "and tell me when it is there"}); err != nil {
			t.Errorf("Answer: %v", err)
		}
		_ = c.Wait(ctx)
		c.Quit("prompt_input_exit")
		return 0
	}, "-n", "Nightly")
	r.wantCode(t, 0)

	if got := strings.Join(history, " | "); got != "an earlier prompt | another one" {
		t.Errorf("the history at the start: %q", got)
	}
	if got := strings.Join(later, " | "); got != "an earlier prompt | another one | now this | queued two" {
		t.Errorf("the history later: %q", got)
	}
	if !took || taken.Text != "queued one" || again {
		t.Errorf("Withdraw gave %+v, %v, and again %v", taken, took, again)
	}
	if len(queued) != 1 || queued[0].Prompt.Text != "queued two" {
		t.Errorf("left in the queue: %+v", queued)
	}
	for _, req := range w.Model.Requests() {
		for _, u := range userTexts(req) {
			if strings.Contains(u, "queued one") {
				t.Errorf("a prompt that was taken back reached the model")
			}
		}
	}

	var compact, vault *CommandInfo
	for i := range info.CommandList {
		switch info.CommandList[i].Name {
		case "compact":
			compact = &info.CommandList[i]
		case "vault":
			vault = &info.CommandList[i]
		}
	}
	if compact == nil || compact.Args != "[instructions]" || compact.Summary == "" || !compact.InRun || len(compact.Sensitive) != 0 {
		t.Errorf("what is said of /compact: %+v", compact)
	}
	if vault == nil || !reflect.DeepEqual(vault.Sensitive, []string{"redact"}) || vault.Args != "show|redact <text>" {
		t.Errorf("what is said of the feature's command: %+v", vault)
	}
	if len(info.CommandList) != len(info.Commands) {
		t.Errorf("%d commands are named and %d described", len(info.Commands), len(info.CommandList))
	}

	if facts["frame"] != "fix-flaky" || facts["session"] != "Nightly" {
		t.Errorf("the facts for the status line at the start: %v", facts)
	}
	find(front, "the frame as a status event", func(e *StatusEvent) bool { return e.StatusFacts()["frame"] == "fix-flaky" })

	q := find(front, "the question", func(e *agent.QuestionEvent) bool { return true }).Question
	data, ok := q.Data.(QuestionData)
	if !ok || !data.CommentOnYes || !data.Deadline().IsZero() {
		t.Fatalf("what the question says of itself: %#v", q.Data)
	}
	for _, c := range q.Choices {
		switches := strings.Contains(c.Label, "switch to acceptEdits")
		if got := data.Modes[c.Key] == "acceptEdits"; got != switches {
			t.Errorf("the choice %q (%s) is marked as switching to accept-edits: %v", c.Label, c.Key, got)
		}
	}
	if !w.Exists("made.txt") {
		t.Errorf("the command did not run after the yes")
	}
	reqs := w.Model.Requests()
	has(t, "what the model read after the yes", string(reqs[len(reqs)-1].Body), "The user approved your Bash call, and said with it: and tell me when it is there")
}

// A removal that cannot be ruled out is asked about even when permissions
// are skipped, and that question says by when it must be answered: an
// interface counts down to it. (The command here is one the shell itself
// would refuse to read, and it names a folder that is not there: nothing
// could come of it if it ran.)
func TestCriticalQuestionHasADeadline(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"rmdir 'no-such-folder-for-this-test"}`}}},
		apptest.Reply{Text: "Then I leave it."})
	began := time.Now()
	var q agent.Question
	_, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		if _, err := c.Submit(agent.Prompt{Text: "tidy up"}); err != nil {
			return 1
		}
		q = find(f, "the question", func(e *agent.QuestionEvent) bool { return true }).Question
		if err := c.Answer(q.ID, agent.Answer{Choice: "no", Text: "leave it"}); err != nil {
			t.Errorf("Answer: %v", err)
		}
		_ = c.Wait(ctx)
		c.Quit("prompt_input_exit")
		return 0
	}, "-y")
	r.wantCode(t, 0)
	data, ok := q.Data.(QuestionData)
	if !ok {
		t.Fatalf("what the question says of itself: %#v", q.Data)
	}
	until := data.Deadline()
	if until.Before(began.Add(criticalWait)) || until.After(time.Now().Add(criticalWait)) {
		t.Errorf("the question must be answered by %s, want %s after it was put (the test began at %s)", until, criticalWait, began)
	}
	has(t, "the question", q.Text, "A dangerous removal cannot be ruled out")
	has(t, "what the model read", toolResult(t, w, 1, "c1"), "the user refused it, and said: leave it")
}

// A session opened again shows where it left off (SessionInfo.ShowPast),
// except in a gnosh /restart started, whose session is on the screen
// already; GNOSH_RESTARTED says so, and nothing the session runs sees it.
func TestShowPastOnResume(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "Hi."}, apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"echo restarted=${GNOSH_RESTARTED:-unset}"}`}}}, apptest.Reply{Text: "Done."})
	run(t, w, "-p", "say hi").wantCode(t, 0)
	shows := func(env []string, args ...string) bool {
		t.Helper()
		var got bool
		attach(t, w, func(o *Options) { o.Environ = append(o.Environ, env...) }, func(ctx context.Context, c SessionControls, f *scripted) int {
			got = c.Info().ShowPast
			c.Quit("prompt_input_exit")
			return 0
		}, args...)
		return got
	}
	if !shows(nil, "-c") {
		t.Error("a session opened again with -c shows nothing of where it left off")
	}
	if shows([]string{"GNOSH_RESTARTED=1"}, "-c") {
		t.Error("a session /restart opened again shows where it left off, which is on the screen already")
	}
	if shows(nil) {
		t.Error("a new session shows a past")
	}
	// The word is /restart's alone: it stops nothing, and no command sees it.
	r := run(t, w, "-c", "-p", "-y", "run it")
	r.wantCode(t, 0)
	has(t, "a command run in a session opened with -c", toolResult(t, w, len(w.Model.Requests())-1, "c1"), "restarted=unset")
}

// A command of several lines is asked about as it is written: the prompt
// mark on each line where a command starts, the text of a here-document
// set under its first line, tabs as tabs, and every other control
// character escaped.
func TestPermissionQuestionShowsAManyLineCommand(t *testing.T) {
	cmd := "cat > a.go <<'EOF'\npackage a\n\nfunc F() {\n\treturn\n}\nEOF\necho \x1b[2Jdone"
	args, _ := json.Marshal(map[string]string{"command": cmd})
	w := apptest.NewWorld(t, apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: string(args)}}}, apptest.Reply{Text: "Left it."})
	var q agent.Question
	_, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		if _, err := c.Submit(agent.Prompt{Text: "write it"}); err != nil {
			return 1
		}
		q = find(f, "the question", func(e *agent.QuestionEvent) bool { return true }).Question
		if err := c.Answer(q.ID, agent.Answer{Choice: "no", Text: "not now"}); err != nil {
			t.Errorf("Answer: %v", err)
		}
		_ = c.Wait(ctx)
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
	has(t, "the question", q.Text, "  $ cat > a.go <<'EOF'\n    package a\n    \n    func F() {\n    \treturn\n    }\n    EOF\n  $ echo \\x1b[2Jdone\n")
	hasNot(t, "the question", q.Text, "$ package", `\x09`, "\x1b")
}
