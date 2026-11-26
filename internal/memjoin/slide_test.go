package memjoin

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

// filler is a file of about size bytes whose every line holds word, so
// that a test can tell whether a request holds the file.
func filler(word string, size int) string {
	var b strings.Builder
	for i := 1; b.Len() < size; i++ {
		fmt.Fprintf(&b, "%s line %04d: the quick brown fox jumps over the lazy dog\n", word, i)
	}
	return b.String()
}

// room is how far above what a session starts with the tests of this file
// put the point at which the loop makes room: enough for the two calls on
// mem that plan and enter a todo and for three files of 6,000 bytes read
// in it, and not for a fourth. (It is what a window of 16,000 tokens gave
// when these tests were written and a session started with 6,676.)
const room = 6124

// startTokens measures what a session of the join's starts with: the size
// of its first request (the system prompt, the tools and the first
// prompt with what goes out beside it), in tokens as the scripted server
// counts them. The tests of the slide and of the room-making point set
// the model's window from it. So they go on testing the same thing when a
// tool is added to every session or a text of the fixed prompt grows:
// what they are about is how much the conversation may grow before room
// is made, and that is the same however much the session starts with.
func startTokens(t *testing.T, words string) int {
	t.Helper()
	w := newWorld(t, says("Nothing to do yet."))
	w.run("-y", "-p", words).wantCode(t, 0)
	reqs := w.script.requests()
	if len(reqs) != 1 {
		t.Fatalf("measuring what a session starts with took %d requests, want one", len(reqs))
	}
	return len(reqs[0].Body) / 4
}

// pointOf is where the loop makes room for a window as small as these
// tests use: at four fifths of it.
func pointOf(window int) int { return (window*4 + 2) / 5 }

// windowWithRoom returns the window whose room-making point stands above
// start by the room these tests give a conversation.
func windowWithRoom(start int) int { return ((start+room)*5 + 3) / 4 }

// The slide, end to end, at a small window. A session that stands in a
// todo and fills its window gets no summary: the oldest rounds of the
// todo leave the model's view instead. The model is told so before its
// next reply, the person is told, the reads of what slid out are
// forgotten, and the next typed prompt's frame line says it again.
func TestSlideInsteadOfASummary(t *testing.T) {
	const (
		task = "review-files"
		todo = "review-files/read-all-four"
	)
	read := func(id, name string) map[string]any { return map[string]any{"file_path": name} }
	w := newWorld(t,
		reply(memCall("c1", `mem.Todo("review the files", "read all four").Push()`)),
		reply(memCall("c2", `mem.Next()`)),
		reply(call("c3", "Read", read("c3", "alpha.txt"))),
		reply(call("c4", "Read", read("c4", "bravo.txt"))),
		reply(call("c5", "Read", read("c5", "charlie.txt"))),
		reply(call("c6", "Read", read("c6", "delta.txt"))),
		// By now the window is full, and the first two reads have slid
		// out: a file read there must be read again, one read after it
		// need not.
		reply(
			call("c7", "Edit", map[string]any{"file_path": "alpha.txt", "old_string": "alpha line 0001", "new_string": "alpha line one"}),
			call("c8", "Edit", map[string]any{"file_path": "delta.txt", "old_string": "delta line 0001", "new_string": "delta line one"}),
		),
		says("I read the four files and changed delta.txt."),
	)
	for _, name := range []string{"alpha", "bravo", "charlie", "delta"} {
		w.Write(name+".txt", filler(name, 6000))
	}
	window := windowWithRoom(startTokens(t, "review the files"))
	point := pointOf(window)
	w.Model.SetWindow(window)
	r := w.run("-y", "-p", "review the files")
	r.wantCode(t, 0)
	w.script.done()
	reqs := w.script.requests()
	if len(reqs) != 8 {
		t.Fatalf("the run made %d requests, want 8: a slide asks no model for anything", len(reqs))
	}
	for i, req := range reqs {
		wellFormed(t, fmt.Sprintf("request %d", i), req)
		if tools, _ := req.JSON["tools"].([]any); len(tools) == 0 {
			t.Errorf("request %d carries no tools: it is not the session's own", i)
		}
	}

	// The model was told the todo had grown large as the second file came
	// in, when more than half of the room left as it was entered had come
	// in, counted with that read. After the slide the oldest reads are
	// gone (two of the four, with the large-step line between them: a
	// slide takes at least half of what the todo holds), and the newest
	// are there.
	before, after := string(reqs[5].Body), string(reqs[6].Body)
	hasNot(t, "the request after the first read", string(reqs[3].Body), "This todo is large")
	has(t, "the request after the second read", string(reqs[4].Body), "This todo is large")
	has(t, "the request before the slide", before, "alpha line 0005", "bravo line 0005", "charlie line 0005", "This todo is large")
	hasNot(t, "the request after the slide", after, "alpha line 0005", "bravo line 0005", "This todo is large")
	has(t, "the request after the slide", after, "charlie line 0005", "delta line 0005")
	t.Logf("a window of %d tokens, with room made at %d; the request before the slide: about %d tokens; the request after it: about %d tokens", window, point, len(before)/4, len(after)/4)
	// The slide was made for want of room, and when the fourth read came
	// in, no sooner: the request before it was under the point, with
	// three files in it; one more file took the conversation over; and
	// what was sent after the slide is under the point again.
	step := (len(reqs[5].Body) - len(reqs[4].Body)) / 4
	if size := len(before) / 4; size >= point || size+step <= point {
		t.Errorf("the request before the slide was about %d tokens and a file read adds about %d, with room made at %d: want it under the point, and over it with one more read", size, step, point)
	}
	if size := len(after) / 4; size >= point {
		t.Errorf("the request after the slide is about %d tokens, and room is made at %d: the slide did not make room", size, point)
	}

	// The answer that entered the todo says what went, and why.
	push := find(reqs[6], "mem.Next()")
	if got := messagesOf(reqs[6])[push+1].text; !strings.HasSuffix(got, "\n[Then came the work in it, 2 tool calls (Read 2), removed from your context to make room.]") {
		t.Errorf("the answer that entered the todo does not end with the slide's note:\n%s", got)
	}
	samePrefix(t, "the requests before and after the slide", reqs[5], reqs[6], push+1)

	// The model is told before its next reply, in gnosh's own words.
	told := prompt.Builtin().SlideLine([]prompt.Slide{{Frame: todo}})
	if got := lastUser(t, reqs[6]); got != told {
		t.Errorf("what the model was told of the slide:\n%s\nwant:\n%s", got, told)
	}
	has(t, "the slide's line", told, "The start of `"+todo+"` left your context to make room; mem.Show(\""+todo+"\") has the plan, the results so far and the thoughts. Your reads there are forgotten: read a file again before you edit it.")

	// The person is told too.
	has(t, "stderr", r.err, "slid `"+todo+"`: about ", " tokens of its start out of the model's context, instead of compacting")
	hasNot(t, "stderr", r.err, "summar")

	// The reads of what slid out are forgotten; a read that is still in
	// front of the model stands.
	edits := results(reqs[7])
	has(t, "the edit of a file whose read slid out", edits[len(edits)-2], "This session has not read "+w.Path("alpha.txt"))
	has(t, "the edit of a file whose read is still there", edits[len(edits)-1], "Edited "+w.Path("delta.txt"))

	// The next prompt the person types carries the frame line, and the
	// frame line says once more that the todo's start is gone.
	w.script.add(says("Yes."))
	r = w.run("-c", "-p", "are you still in the todo?")
	r.wantCode(t, 0)
	reqs = w.script.requests()
	typed := lastUser(t, reqs[len(reqs)-1])
	frameLine := prompt.Builtin().FrameLine([]prompt.Frame{
		{ID: task, Purpose: "review the files"},
		{ID: todo, Purpose: "read all four"},
	}, []prompt.Slide{{Frame: todo}})
	if !strings.HasSuffix(typed, "are you still in the todo?\n\n"+prompt.Builtin().MemLine()+"\n\n"+frameLine) {
		t.Errorf("the prompt typed after the slide does not end with the mem line and a frame line that tells of the slide:\n%s", typed)
	}
	has(t, "the frame line", frameLine, "The start of `"+todo+"` left your context to make room")
}

// A slide whose rounds edited files names them, so that the model does not
// go back over work it has done (box 2, 16 November: a todo over sixteen
// files slid thirteen times while the model read them again).
func TestSlideNamesTheFilesEdited(t *testing.T) {
	const todo = "review-files/read-all-four"
	read := func(name string) map[string]any { return map[string]any{"file_path": name} }
	w := newWorld(t,
		reply(memCall("c1", `mem.Todo("review the files", "read all four").Push()`)),
		reply(memCall("c2", `mem.Next()`)),
		// (The read and the edit in one round, which slides out whole.)
		reply(
			call("c3", "Read", read("alpha.txt")),
			call("c4", "Edit", map[string]any{"file_path": "alpha.txt", "old_string": "alpha line 0001", "new_string": "alpha line one"}),
		),
		reply(call("c5", "Read", read("bravo.txt"))),
		reply(call("c6", "Read", read("charlie.txt"))),
		reply(call("c7", "Read", read("delta.txt"))),
		says("I read the four files and changed alpha.txt."),
	)
	for _, name := range []string{"alpha", "bravo", "charlie", "delta"} {
		w.Write(name+".txt", filler(name, 6000))
	}
	w.Model.SetWindow(windowWithRoom(startTokens(t, "review the files")))
	r := w.run("-y", "-p", "review the files")
	r.wantCode(t, 0)
	w.script.done()
	has(t, "stderr", r.err, "slid `"+todo+"`")
	reqs := w.script.requests()
	var told string
	for _, req := range reqs {
		if got := lastUser(t, req); strings.Contains(got, "left your context to make room") {
			told = got
		}
	}
	has(t, "the slide's line", told, "The start of `"+todo+"` left your context to make room (there you edited alpha.txt, which is done);")
}

// The large-step line: well before the window fills, a todo that has
// taken in more than half of what the window allows since it was entered
// is said to be large, once.
func TestLargeStepLine(t *testing.T) {
	const todo = "review-files/read-all-four"
	w := newWorld(t,
		reply(memCall("c1", `mem.Todo("review the files", "read all four").Push()`)),
		reply(memCall("c2", `mem.Next()`)),
		reply(call("c3", "Read", map[string]any{"file_path": "alpha.txt"})),
		reply(call("c4", "Read", map[string]any{"file_path": "bravo.txt"})),
		reply(call("c5", "Read", map[string]any{"file_path": "charlie.txt"})),
		reply(call("c6", "Read", map[string]any{"file_path": "delta.txt"})),
		reply(call("c6b", "Read", map[string]any{"file_path": "alpha.txt", "limit": 3})),
		says("That is a lot of reading."),
	)
	for _, name := range []string{"alpha", "bravo", "charlie", "delta"} {
		w.Write(name+".txt", filler(name, 17500))
	}
	w.Model.SetWindow(40000)
	r := w.run("-y", "-p", "review the files")
	r.wantCode(t, 0)
	w.script.done()
	reqs := w.script.requests()

	said := -1
	for i, req := range reqs {
		if strings.Contains(string(req.Body), "This todo is large") {
			said = i
			break
		}
	}
	if said < 0 {
		t.Fatalf("the model was never told that the todo is large:\n%s", conversation(reqs[len(reqs)-1]))
	}
	line := lastUser(t, reqs[said])
	if !strings.HasPrefix(line, "<gnosh>This todo is large: about ") ||
		!strings.Contains(line, " tokens came in since you entered `"+todo+"`. If its work is one piece per file or item, put each piece under it as a todo of its own") ||
		!strings.HasSuffix(line, "Or pop this one with what you have.</gnosh>") {
		t.Errorf("the large-step line:\n%s", line)
	}
	if n := strings.Count(string(reqs[len(reqs)-1].Body), "This todo is large"); n != 1 {
		t.Errorf("the large-step line was said %d times in one todo, want once", n)
	}
	hasNot(t, "stderr", r.err, "slid `", "summar")

	// It is said once for a todo, also by a process that takes the
	// session up later: the session's file remembers.
	var marks []string
	for _, e := range w.session(w.Sessions()[0]).Entries() {
		if e.Key == metaSaid {
			marks = append(marks, e.Value)
		}
	}
	if len(marks) != 1 || !strings.HasPrefix(marks[0], "large-step:") {
		t.Errorf("what the session's file remembers of what was said: %q", marks)
	}
	w.script.add(reply(call("c7", "Read", map[string]any{"file_path": "alpha.txt", "limit": 5})), says("Still here."))
	r = w.run("-y", "-c", "-p", "carry on")
	r.wantCode(t, 0)
	// Counted in the session's file, not in the last request: the process
	// that takes the session up may slide the todo's start, the line with
	// it, out of the model's view.
	n := 0
	for _, e := range w.session(w.Sessions()[0]).Entries() {
		for _, b := range e.Message.Blocks {
			n += strings.Count(b.Text, "This todo is large")
		}
	}
	if n != 1 {
		t.Errorf("after a restart the session's file holds the large-step line %d times, want once", n)
	}
}

// A pop on the round that would have crossed the compaction point: the
// popped work is out of the view before the next request is measured, so
// no room has to be made, by a slide or by a summary, and the session
// goes on well under the point.
func TestPopKeepsTheSessionUnderThePoint(t *testing.T) {
	w := newWorld(t,
		reply(memCall("c1", `mem.Todo("review the files", "read the first three", "read the fourth").Push()`)),
		reply(memCall("c2", `mem.Next()`)),
		reply(call("c3", "Read", map[string]any{"file_path": "alpha.txt"})),
		reply(call("c4", "Read", map[string]any{"file_path": "bravo.txt"})),
		reply(call("c5", "Read", map[string]any{"file_path": "charlie.txt"})),
		reply(memCall("c6", "mem.Pop(\"read alpha.txt, bravo.txt and charlie.txt: each holds numbered lines\")\nmem.Next()")),
		reply(call("c7", "Read", map[string]any{"file_path": "delta.txt"})),
		reply(memCall("c8", `mem.Pop("read delta.txt: numbered lines too")`)),
		says("All four hold numbered lines."),
	)
	for _, name := range []string{"alpha", "bravo", "charlie", "delta"} {
		w.Write(name+".txt", filler(name, 6000))
	}
	window := windowWithRoom(startTokens(t, "review the files"))
	point := pointOf(window)
	w.Model.SetWindow(window)
	r := w.run("-y", "-p", "review the files")
	r.wantCode(t, 0)
	w.script.done()
	reqs := w.script.requests()
	if len(reqs) != 9 {
		t.Fatalf("the run made %d requests, want 9", len(reqs))
	}
	// With a fourth file read in the same todo the window would have been
	// full (TestSlideInsteadOfASummary); popped in time, nothing slides.
	hasNot(t, "stderr", r.err, "slid `", "summar", "left your context to make room")
	has(t, "stderr", r.err, "popped `review-files/read-first-three`: about ")
	sizes := make([]int, len(reqs))
	for i, req := range reqs {
		sizes[i] = len(req.Body) / 4
		if strings.Contains(string(req.Body), "left your context to make room") {
			t.Errorf("request %d tells the model of a slide", i)
		}
	}
	t.Logf("a window of %d tokens, with room made at %d; the size of each request, in tokens as the server counts them: %v", window, point, sizes)
	// The pop came on the last round there was room for: the request that
	// made it held three files and was under the point, and one more file
	// would have taken the conversation over it.
	step := sizes[5] - sizes[4]
	if sizes[5] >= point || sizes[5]+step <= point {
		t.Errorf("the request that made the pop was about %d tokens and a file read adds about %d, with room made at %d: want it under the point, and over it with one more read", sizes[5], step, point)
	}
	// After the pop none of the three files is sent: the request is
	// smaller than the one that held the first of them.
	if sizes[6] >= sizes[3] {
		t.Errorf("the request after the pop is about %d tokens, and the one that made it was about %d: the popped reads are still being sent", sizes[6], sizes[5])
	}
	for i, n := range sizes {
		if n >= point {
			t.Errorf("request %d is about %d tokens, at or over the point at which room is made for a window of %d (%d)", i, n, window, point)
		}
	}
}

// A todo entered late, past half the point, is told it has grown large too:
// the line counts the room that was left as it was entered, not half the
// point, which such a todo could never take in before it slid (found by
// the AI-box peer, 16 November).
func TestLargeStepLineForALateTodo(t *testing.T) {
	const todo = "review-files/read-rest"
	w := newWorld(t,
		reply(call("c1", "Read", map[string]any{"file_path": "alpha.txt"})),
		reply(call("c2", "Read", map[string]any{"file_path": "bravo.txt"})),
		reply(memCall("c3", `mem.Todo("review the files", "read the rest").Push()`)),
		reply(memCall("c4", `mem.Next()`)),
		reply(call("c5", "Read", map[string]any{"file_path": "charlie.txt"})),
		reply(call("c6", "Read", map[string]any{"file_path": "delta.txt"})),
		reply(call("c7", "Read", map[string]any{"file_path": "alpha.txt", "limit": 3})),
		says("Read."),
	)
	for _, name := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		w.Write(name+".txt", filler(name, 17500))
	}
	w.Model.SetWindow(40000)
	r := w.run("-y", "-p", "review the files")
	r.wantCode(t, 0)
	w.script.done()
	reqs := w.script.requests()
	if n := strings.Count(string(reqs[len(reqs)-1].Body), "This todo is large"); n != 1 {
		t.Fatalf("the late todo was told it is large %d times, want once", n)
	}
	has(t, "the large-step line", string(reqs[len(reqs)-1].Body), "since you entered `"+todo+"`")
	hasNot(t, "stderr", r.err, "slid `", "summar")
	// (Where it entered: past half the point, the line's old trigger.)
	if push, point := tokensBefore(t, reqs, "read the rest"), pointOf(40000); push*2 <= point {
		t.Errorf("the todo was entered at about %d tokens, with room made at %d: the test wants it past half", push, point)
	}
}

// A todo is not told it is large as a todo under it pops. The line reads
// the size of the conversation as the next request goes out, after the
// pop, and not the last request's count, which still held all the pop
// took out. Told then, the once it is ever told, the todo grew large later
// on its own work with no word until the slide (a Mac Studio session, 21
// to 26 November). It is told when its own work makes it large.
func TestLargeStepLineNotSaidAsASubTodoPops(t *testing.T) {
	const parentLine = "since you entered `review-files`."
	w := newWorld(t,
		reply(memCall("c1", `mem.Todo("review the files", "read the first three").Push()`)),
		reply(memCall("c2", `mem.Next()`)),
		reply(call("c3", "Read", map[string]any{"file_path": "alpha.txt"})),
		reply(call("c4", "Read", map[string]any{"file_path": "bravo.txt"})),
		reply(call("c5", "Read", map[string]any{"file_path": "charlie.txt"})),
		reply(memCall("c6", `mem.Pop("read alpha.txt, bravo.txt and charlie.txt: each holds numbered lines")`)),
		reply(call("c7", "Read", map[string]any{"file_path": "delta.txt"})),
		reply(call("c8", "Read", map[string]any{"file_path": "echo.txt"})),
		says("All five hold numbered lines."),
	)
	for _, name := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		w.Write(name+".txt", filler(name, 6000))
	}
	w.Model.SetWindow(windowWithRoom(startTokens(t, "review the files")))
	r := w.run("-y", "-p", "review the files")
	r.wantCode(t, 0)
	w.script.done()
	reqs := w.script.requests()
	if len(reqs) != 9 {
		t.Fatalf("the run made %d requests, want 9", len(reqs))
	}
	has(t, "stderr", r.err, "popped `review-files/read-first-three`: about ")
	hasNot(t, "stderr", r.err, "slid `", "summar")
	// The sub-todo took in enough to be large itself, and was told so.
	has(t, "the request that made the pop", string(reqs[5].Body), "since you entered `review-files/read-first-three`.")
	// The request after the pop: the parent stands on top again, with
	// little of its own in the conversation.
	hasNot(t, "the request after the pop", string(reqs[6].Body), parentLine)
	// Two files read in the parent itself make it large, and it is told,
	// once.
	if n := strings.Count(string(reqs[8].Body), parentLine); n != 1 {
		t.Errorf("after two files read in it, the parent was told it is large %d times, want once:\n%s", n, conversation(reqs[8]))
	}
}

// tokensBefore is about how many tokens the first request that holds what
// carries.
func tokensBefore(t *testing.T, reqs []apptest.Request, what string) int {
	t.Helper()
	for _, req := range reqs {
		if strings.Contains(string(req.Body), what) {
			return len(req.Body) / 4
		}
	}
	t.Fatalf("no request holds %q", what)
	return 0
}
