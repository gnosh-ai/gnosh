package memjoin

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
	"github.com/gnosh-ai/gnosh/internal/gnovm"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

const goalName = "greeting-says-hi"

// /goal, end to end, in a session a person types into: the goal is bound
// and the agent is sent its directive; the agent closes the goal without
// doing the work, a judge finds it not met, the todo is opened again and
// the agent is sent back; it does the work, closes the goal again, the
// judge finds it met, and the session is bound to nothing.
func TestGoalBoundJudgedSentBackAndMet(t *testing.T) {
	w := newWorld(t,
		// The directive's turn: the agent claims the goal holds.
		reply(memCall("c1", `mem.Done("`+goalName+`", "greet.txt says hi")`)),
		says("The greeting says hi."),
		// Sent back by the judge: it does the work and closes again.
		reply(call("c2", "Read", map[string]any{"file_path": "greet.txt"})),
		reply(call("c3", "Edit", map[string]any{"file_path": "greet.txt", "old_string": "hello", "new_string": "hi"})),
		reply(memCall("c4", `mem.Done("`+goalName+`", "greet.txt now says hi")`)),
		says("Now it does."),
	)
	w.Write("greet.txt", "hello, world\n")
	judged := 0
	w.script.onSide = func(kind string, r apptest.Request) *apptest.Reply {
		if kind != sideJudge {
			return nil
		}
		judged++
		if judged == 1 {
			return &apptest.Reply{Text: `{"ok": false, "reason": "greet.txt was never read or changed, so nothing shows that it says hi."}`}
		}
		return &apptest.Reply{Text: `{"ok": true, "basis": "conversation", "reason": "the edit of greet.txt put hi in place of hello"}`}
	}

	s := w.sit("-y")
	s.see("Type a prompt and Enter.")
	s.typeLine("/goal")
	s.see("No goal is set.")
	s.typeLine("/goal greeting says hi")
	s.see("Goal set: greeting says hi (`" + goalName + "` in mem)")
	s.see("Goal not met yet: greet.txt was never read or changed, so nothing shows that it says hi. The agent goes back to it.")
	s.see("Now it does.")
	s.see("Goal met: greeting says hi (the edit of greet.txt put hi in place of hello)")
	s.see("-- done in ")
	s.typeLine("/goal")
	s.see("No goal is set.")
	s.typeLine("/exit")
	if code := s.exit(); code != 0 {
		t.Fatalf("exit code %d\n%s", code, s.out.String())
	}
	w.script.done()
	if got := w.Read("greet.txt"); got != "hi, world\n" {
		t.Errorf("greet.txt = %q", got)
	}

	reqs := w.script.requests()
	// The directive went out as a prompt of gnosh's own, with the digest
	// of mem (the goal is in it by then) and no mem line: nobody typed
	// it.
	directive := lastUser(t, reqs[0])
	bound := gnovm.GoalBinding{Condition: "greeting says hi", Name: goalName, Title: "greeting says hi"}
	has(t, "the directive's prompt", directive,
		prompt.Wrap(gnovm.GoalDirective(bound)),
		"<mem>", "`"+goalName+"` \"greeting says hi\"")
	hasNot(t, "the directive's prompt", directive, prompt.Builtin().MemLine())

	// The bound name went with every call on mem.
	for i, req := range w.memRequests() {
		if req.Goal != goalName {
			t.Errorf("mem call %d was not told the goal the session is bound to: %q", i, req.Goal)
		}
	}

	// The judge found the first close not met: the agent was sent back
	// with what the check found, the goal restated, and what to do.
	back := lastUser(t, reqs[2])
	if want := prompt.Builtin().GoalNotMet(goalName, "greeting says hi", "greet.txt was never read or changed, so nothing shows that it says hi."); back != want {
		t.Errorf("the send-back after a goal was judged not met:\n%s\nwant:\n%s", back, want)
	}

	// What the judge was shown: the condition, what mem shows of the
	// todo, and the conversation in full.
	judges := w.script.sides(sideJudge)
	if len(judges) != 2 {
		t.Fatalf("the judge was asked %d times, want twice", len(judges))
	}
	shown := lastUser(t, judges[1])
	has(t, "what the judge was shown", shown,
		"The goal:\ngreeting says hi",
		"`"+goalName+"`",
		"the agent called Edit: {\"file_path\":\"greet.txt\"",
		"the tool returned: Edited ",
		"Does the conversation show that the goal holds?")
	if got := judges[0].JSON["max_tokens"]; got != float64(judgeTokens) {
		t.Errorf("the judge was given room for %v tokens, want %d", got, judgeTokens)
	}

	// The session's file holds the binding, and then that it was cleared.
	var bindings []string
	for _, e := range w.session(w.Sessions()[0]).Entries() {
		if e.Key == metaGoal {
			bindings = append(bindings, e.Value)
		}
	}
	if len(bindings) < 2 || !strings.Contains(bindings[0], `"name":"`+goalName+`"`) || bindings[len(bindings)-1] != "" {
		t.Errorf("the bindings written to the session's file: %q", bindings)
	}
}

// gnosh -p --goal: the goal is bound before the run's prompt goes out.
// A run whose goal is met ends well.
func TestRunGoalMet(t *testing.T) {
	w := newWorld(t,
		reply(call("c1", "Read", map[string]any{"file_path": "greet.txt"})),
		reply(call("c2", "Edit", map[string]any{"file_path": "greet.txt", "old_string": "hello", "new_string": "hi"})),
		reply(memCall("c3", `mem.Done("`+goalName+`", "greet.txt now says hi")`)),
		says("The greeting says hi now."),
	)
	w.Write("greet.txt", "hello, world\n")
	r := w.run("-y", "-p", "--goal", "greeting says hi", "the file is greet.txt")
	r.wantCode(t, 0)
	w.script.done()
	has(t, "stderr", r.err,
		"Goal set: greeting says hi (`"+goalName+"` in mem)",
		"Goal met: greeting says hi (the conversation shows it)")
	hasNot(t, "stderr", r.err, "still set")
	// The goal was bound before the run's prompt was composed: the
	// prompt went out as one message, the person's words first, then the
	// goal's directive, the digest of mem, which holds the goal by then,
	// and the mem line last.
	reqs := w.script.requests()
	first := messagesOf(reqs[0])
	if len(first) != 2 {
		t.Fatalf("the run's first request holds %d messages, want the system prompt and one prompt:\n%s", len(first), conversation(reqs[0]))
	}
	text := first[1].text
	bound := gnovm.GoalBinding{Condition: "greeting says hi", Name: goalName, Title: "greeting says hi"}
	directive := prompt.Wrap(gnovm.GoalDirective(bound))
	has(t, "the run's prompt", text, "Goal `"+goalName+"` \"greeting says hi\" is set: greeting says hi")
	words, dir, digest, line := strings.Index(text, "the file is greet.txt"), strings.Index(text, directive), strings.Index(text, "<mem>"), strings.Index(text, prompt.Builtin().MemLine())
	if !(words >= 0 && words < dir && dir < digest && digest < line) || !strings.HasSuffix(text, prompt.Builtin().MemLine()) {
		t.Errorf("the run's prompt is not its words, the directive, the digest and the mem line, in that order:\n%s", text)
	}
	if n := strings.Count(string(reqs[len(reqs)-1].Body), "is set: greeting says hi"); n != 1 {
		t.Errorf("the directive stands %d times in the conversation, want once", n)
	}
}

// gnosh -p --goal with nothing else: the goal's directive is the run's
// prompt, sent as /goal sends it in a session.
func TestRunGoalAlone(t *testing.T) {
	w := newWorld(t,
		reply(call("c1", "Read", map[string]any{"file_path": "greet.txt"})),
		reply(call("c2", "Edit", map[string]any{"file_path": "greet.txt", "old_string": "hello", "new_string": "hi"})),
		reply(memCall("c3", `mem.Done("`+goalName+`", "greet.txt now says hi")`)),
		says("The greeting says hi now."),
	)
	w.Write("greet.txt", "hello, world\n")
	r := w.run("-p", "-y", "--goal", "greeting says hi")
	r.wantCode(t, 0)
	w.script.done()
	if r.out != "The greeting says hi now.\n" {
		t.Errorf("stdout = %q", r.out)
	}
	has(t, "stderr", r.err,
		"Goal set: greeting says hi (`"+goalName+"` in mem)",
		"Goal met: greeting says hi (the conversation shows it)")
	hasNot(t, "stderr", r.err, "the prompt is empty", "still set")
	if got := w.Read("greet.txt"); got != "hi, world\n" {
		t.Errorf("greet.txt = %q", got)
	}

	// The directive is the prompt: gnosh's own words, with the digest of
	// mem after them and no mem line, since nobody typed anything.
	reqs := w.script.requests()
	first := messagesOf(reqs[0])
	if len(first) != 2 {
		t.Fatalf("the run's first request holds %d messages, want the system prompt and one prompt:\n%s", len(first), conversation(reqs[0]))
	}
	bound := gnovm.GoalBinding{Condition: "greeting says hi", Name: goalName, Title: "greeting says hi"}
	text := first[1].text
	dir, digest := strings.Index(text, prompt.Wrap(gnovm.GoalDirective(bound))), strings.Index(text, "<mem>")
	if !(dir >= 0 && dir < digest) {
		t.Errorf("the run's prompt is not the goal's directive and then the digest:\n%s", text)
	}
	hasNot(t, "the run's prompt", text, prompt.Builtin().MemLine())
	for i, req := range w.memRequests() {
		if req.Goal != goalName {
			t.Errorf("mem call %d was not told the goal the run is bound to: %q", i, req.Goal)
		}
	}

	// With no goal and no prompt a run is refused, as before.
	r = w.run("-p")
	r.wantCode(t, 1)
	has(t, "stderr of a run with nothing to do", r.err, "the prompt is empty")

	// A goal that could not be bound is no prompt: the run says why no
	// goal was set, and that it was given nothing to do.
	r = w.run("-p", "--tools", "Read", "--goal", "greeting says hi")
	r.wantCode(t, 1)
	has(t, "stderr of a run whose goal could not be set", r.err, "--goal needs the gnovm_run tool, which this run does not have, so no goal was set.", "the prompt is empty")
}

// A run whose goal is not met keeps being sent back: no count ends the
// work. Every tenth time in a row the agent is asked whether there is a
// real reason to stop, and when it says so the run ends, with the goal
// still set and a failing exit code.
func TestRunGoalStillSet(t *testing.T) {
	replies := []apptest.Reply{reply(call("c1", "Read", map[string]any{"file_path": "greet.txt"}))}
	for i := 0; i < 10; i++ {
		replies = append(replies, says("The file is read-only, so I cannot change it."))
	}
	replies = append(replies, says("The goal cannot be reached as set: greet.txt is read-only here.\n"+prompt.StopMarker))
	w := newWorld(t, replies...)
	w.Write("greet.txt", "hello, world\n")
	r := w.run("-y", "-p", "--goal", "greeting says hi", "the file is greet.txt")
	r.wantCode(t, 1)
	w.script.done()
	has(t, "stderr", r.err,
		"Goal set: greeting says hi (`"+goalName+"` in mem)",
		"The goal is still set: greeting says hi (`"+goalName+"` in mem).",
		"The model chose to stop. Its reason: The goal cannot be reached as set: greet.txt is read-only here.")

	reqs := w.script.requests()
	bound := gnovm.GoalBinding{Condition: "greeting says hi", Name: goalName, Title: "greeting says hi"}
	// A plain continuation names the goal and how to close it.
	has(t, "a continuation", lastUser(t, reqs[2]),
		"<gnosh>The goal `"+goalName+"` \"greeting says hi\" is not closed, and none of its todos is open. Once it holds, close it with `mem.Done(\""+goalName+"\", \"<command> → <result>\")`")
	// The tenth in a row is the question.
	if got, want := lastUser(t, reqs[11]), prompt.Wrap(gnovm.GoalCheckpoint(bound, 10, prompt.StopMarker)); got != want {
		t.Errorf("the tenth continuation in a row:\n%s\nwant:\n%s", got, want)
	}
	if n := len(reqs); n != 12 {
		t.Errorf("the run made %d requests, want 12", n)
	}
	if regexp.MustCompile(`GNOSH_STOP`).MatchString(r.out) {
		t.Errorf("the stop marker was shown to the person:\n%s", r.out)
	}
}

// A task closed as done whose turn-end check is cut off at its length
// limit: the judge is asked once more with more room; when that is cut off
// too, the agent is sent back once to show the task holds, and the next
// close of it stands unchecked, so a judge that cannot run never traps the
// turn.
func TestJudgeCutOffRetriedAndSentBackOnce(t *testing.T) {
	const task = "read-notes"
	w := newWorld(t,
		reply(memCall("a1", `mem.Todo("read the notes", "read notes.txt").Push()`)),
		reply(memCall("a2", `mem.Next()`)),
		reply(call("a3", "Read", map[string]any{"file_path": "notes.txt"})),
		reply(memCall("a4", `mem.Pop("notes.txt says hello")`)),
		reply(memCall("a5", `mem.Pop("its todo is done: notes.txt says hello")`)),
		says("Done."),
		// Sent back because the check could not run.
		reply(call("a6", "Read", map[string]any{"file_path": "notes.txt"})),
		reply(memCall("a7", `mem.Done("`+task+`", "Read notes.txt → hello")`)),
		says("Shown."),
	)
	w.Write("notes.txt", "hello\n")
	w.script.onSide = func(kind string, r apptest.Request) *apptest.Reply {
		if kind != sideJudge {
			return nil
		}
		return &apptest.Reply{Text: `{"ok": tr`, Finish: "length"}
	}
	r := w.run("-y", "-p", "read the notes")
	r.wantCode(t, 0)
	w.script.done()

	judges := w.script.sides(sideJudge)
	if len(judges) != 4 {
		t.Fatalf("the judge was asked %d times, want 4: twice for each close", len(judges))
	}
	for i, want := range []int{judgeTokens, judgeRetryTokens, judgeTokens, judgeRetryTokens} {
		if got := judges[i].JSON["max_tokens"]; got != float64(want) {
			t.Errorf("judge request %d was given room for %v tokens, want %d", i, got, want)
		}
	}
	has(t, "stderr", r.err,
		"`"+task+"` \"read the notes\" was closed as done, and the check of it could not run (",
		"It is open again, and the agent goes back to show it.",
		"so it stands unchecked.")
	reqs := w.script.requests()
	has(t, "the send-back", lastUser(t, reqs[6]), "The check of `"+task+"` found it not met: its check could not run (")
}
