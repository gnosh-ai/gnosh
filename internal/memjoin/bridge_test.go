package memjoin

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/bridge"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// host is a test's side of `gnosh bridge`: the program running with its
// standard input and output joined to the test, one line each way.
type host struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Scanner
	done chan int
}

func (w *world) bridge() *host {
	w.t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	o := w.options("bridge")
	o.Stdin, o.Stdout = inR, outW
	h := &host{t: w.t, in: inW, out: bufio.NewScanner(outR), done: make(chan int, 1)}
	h.out.Buffer(nil, bridge.MaxLine)
	go func() {
		code := app.Main(context.Background(), o)
		outW.Close()
		h.done <- code
	}()
	return h
}

// ask sends one request and returns the reply.
func (h *host) ask(req map[string]any) bridge.Reply {
	h.t.Helper()
	line, err := json.Marshal(req)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.in.Write(append(line, '\n')); err != nil {
		h.t.Fatalf("writing %s: %v", line, err)
	}
	if !h.out.Scan() {
		h.t.Fatalf("no reply to %s: %v", line, h.out.Err())
	}
	var rep bridge.Reply
	if err := json.Unmarshal(h.out.Bytes(), &rep); err != nil {
		h.t.Fatalf("reply %s: %v", h.out.Bytes(), err)
	}
	return rep
}

// close ends standard input and waits for the program's exit code.
func (h *host) close() int {
	h.t.Helper()
	h.in.Close()
	select {
	case code := <-h.done:
		return code
	case <-time.After(60 * time.Second):
		h.t.Fatal("gnosh bridge did not end when its input did")
	}
	return -1
}

// conv builds a host's conversation as the entries bridge.md §7.2 maps it
// to.
type conv struct {
	session string
	entries []transcript.Entry
}

func (c *conv) add(e transcript.Entry) {
	e.UUID = "e" + strings.Repeat("0", 3-len(itoa(len(c.entries)))) + itoa(len(c.entries))
	c.entries = append(c.entries, e)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (c *conv) prompt(text string) { c.add(transcript.PromptEntry(text)) }

func (c *conv) call(id, code, vm string) {
	in := map[string]string{"code": code}
	if vm != "" {
		in["name"] = vm
	}
	raw, _ := json.Marshal(in)
	c.add(transcript.AssistantEntry(core.Message{Blocks: []core.Block{core.ToolUse(id, "gnovm_run", raw)}}, core.StopToolUse))
}

func (c *conv) result(id string, rep bridge.Reply) {
	c.add(transcript.UserEntry(core.Message{Blocks: []core.Block{
		core.ToolResultBlock(id, core.ToolResult{Content: rep.Content, IsError: rep.IsError}),
	}}, transcript.SourceToolResults))
	for _, f := range rep.Frames {
		var e transcript.Entry
		if f.Kind == "push" {
			e = transcript.PushEntry(f.ID, f.Purpose, id)
		} else {
			e = transcript.PopEntry(f.ID, f.Purpose, f.Result, id)
		}
		e.SessionID = c.session
		c.add(e)
	}
}

func runCall(id, code, vm string) map[string]any {
	in := map[string]string{"code": code}
	if vm != "" {
		in["name"] = vm
	}
	return map[string]any{"op": "call", "session": "S1", "call_id": id, "name": "gnovm_run", "input": in}
}

func textOf(m core.Message) string {
	var b strings.Builder
	for _, bl := range m.Blocks {
		b.WriteString(bl.Text)
		for _, c := range bl.Content {
			b.WriteString(c.Text)
		}
		b.WriteString(string(bl.Input))
	}
	return b.String()
}

// A host pushes a frame, works in it and pops it, all through the bridge:
// after the pop the work is gone from the view, the push's result carries
// the note, and the prefix through the push is byte for byte what it was
// (bridge.md §6, §7; frames.md §12.2).
func TestBridgePushWorkPop(t *testing.T) {
	w := newWorld(t)
	h := w.bridge()

	if rep := h.ask(map[string]any{"op": "hello", "id": 1, "protocol": 1, "client": "test"}); !rep.OK || rep.Protocol != 1 || rep.Build == "" || string(rep.ID) != "1" {
		t.Fatalf("hello: %+v", rep)
	}
	tools := h.ask(map[string]any{"op": "tools"})
	var names []string
	for _, ti := range tools.Tools {
		names = append(names, ti.Name)
		if len(ti.InputSchema) == 0 || ti.Description == "" {
			t.Errorf("tool %s has no schema or description", ti.Name)
		}
		if ti.Confirm != (ti.Name == "gnovm_drop") {
			t.Errorf("tool %s: confirm %v", ti.Name, ti.Confirm)
		}
	}
	if strings.Join(names, " ") != "gnovm_run gnovm_new gnovm_list gnovm_drop" {
		t.Fatalf("tools: %v", names)
	}

	c := &conv{session: "S1"}
	c.prompt("fix the port")
	c.call("c1", `mem.Todo("fix the port").Push()`, "mem")
	push := h.ask(runCall("c1", `mem.Todo("fix the port").Push()`, "mem"))
	if !push.OK || push.IsError || len(push.Frames) != 1 || push.Frames[0].Kind != "push" {
		t.Fatalf("push: %+v", push)
	}
	c.result("c1", push)
	c.call("c2", `"lots of work"`, "")
	work := h.ask(runCall("c2", `"lots of work"`, ""))
	if !work.OK || work.IsError {
		t.Fatalf("work: %+v", work)
	}
	c.result("c2", work)

	before := h.ask(map[string]any{"op": "view", "session": "S1", "entries": c.entries})
	if !before.OK || len(before.Stack) != 1 || before.Stack[0].ID != push.Frames[0].ID || before.Stack[0].CallID != "c1" {
		t.Fatalf("view before the pop: %+v", before)
	}
	if !strings.Contains(textOf(before.Messages[len(before.Messages)-1]), "lots of work") {
		t.Fatalf("the work is not in the view before the pop")
	}

	c.call("c3", `mem.Pop("the port is picked free in setup")`, "mem")
	pop := h.ask(runCall("c3", `mem.Pop("the port is picked free in setup")`, "mem"))
	if !pop.OK || pop.IsError || len(pop.Frames) != 1 || pop.Frames[0].Kind != "pop" {
		t.Fatalf("pop (made from the stack the bridge kept): %+v", pop)
	}
	c.result("c3", pop)
	c.add(transcript.AssistantEntry(core.AssistantText("done"), core.StopEndTurn))

	after := h.ask(map[string]any{"op": "view", "session": "S1", "entries": c.entries})
	if !after.OK || len(after.Stack) != 0 {
		t.Fatalf("view after the pop: %+v", after)
	}
	for i, m := range after.Messages {
		if strings.Contains(textOf(m), "lots of work") || strings.Contains(textOf(m), `\"lots of work\"`) {
			t.Errorf("message %d still holds the popped work: %s", i, textOf(m))
		}
	}
	wantSources := [][]string{{"e000"}, {"e001"}, {"e002"}, {"e006"}, {"e007"}, {"e009"}}
	if got, _ := json.Marshal(after.Sources); string(got) != mustJSON(t, wantSources) {
		t.Errorf("sources after the pop: %s, want %s", got, mustJSON(t, wantSources))
	}
	// The prefix through the push's call is unchanged, and the push's
	// result is what it was with the note after it.
	for i := 0; i < 2; i++ {
		if mustJSON(t, after.Messages[i]) != mustJSON(t, before.Messages[i]) {
			t.Errorf("message %d changed across the pop:\n%s\n%s", i, mustJSON(t, before.Messages[i]), mustJSON(t, after.Messages[i]))
		}
	}
	was, now := textOf(before.Messages[2]), textOf(after.Messages[2])
	if !strings.HasPrefix(now, was) || !strings.Contains(now[len(was):], "removed from your context when you popped it.]") {
		t.Errorf("the push's result after the pop:\n%q\nwas\n%q", now, was)
	}

	// A later request extends the one after the pop.
	c.prompt("thanks")
	later := h.ask(map[string]any{"op": "view", "session": "S1", "entries": c.entries})
	if len(later.Messages) != len(after.Messages)+1 {
		t.Fatalf("view after a new prompt: %d messages, want %d", len(later.Messages), len(after.Messages)+1)
	}
	if mustJSON(t, later.Messages[:len(after.Messages)]) != mustJSON(t, after.Messages) {
		t.Errorf("the request after the pop is not a prefix of the next")
	}

	// Deleting a machine needs a person's word first.
	del := map[string]any{"op": "call", "session": "S1", "call_id": "c4", "name": "gnovm_drop", "input": map[string]string{"name": "mem", "action": "delete"}}
	if rep := h.ask(del); rep.OK || rep.Error == nil || rep.Error.Code != bridge.CodeConfirm || rep.Error.Summary == "" {
		t.Errorf("an unconfirmed delete: %+v", rep)
	}

	if code := h.close(); code != 0 {
		t.Errorf("gnosh bridge ended with %d", code)
	}
	// What the bridge kept is the project's mem, as gnosh mem reads it.
	r := w.run("mem", "log")
	r.wantCode(t, 0)
	has(t, "gnosh mem log", r.out, "fix the port")
}
