package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// fakeTool answers every call with its input, reports the frames its
// input names, and asks for a person's approval when the input says so.
type fakeTool struct {
	name  string
	calls []core.ToolCall
	stack func(string) []string
	seen  [][]string
}

func (f *fakeTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: f.name, Description: "a tool for tests", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (f *fakeTool) Run(_ context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	f.calls = append(f.calls, call)
	f.seen = append(f.seen, f.stack(env.SessionID))
	var in struct {
		Frames []Frame `json:"frames"`
		Fail   bool    `json:"fail"`
	}
	_ = json.Unmarshal(call.Input, &in)
	return core.ToolResult{Content: []core.Block{core.Text("ran " + string(call.Input))}, IsError: in.Fail,
		Meta: map[string]any{"frames": in.Frames}}, nil
}

func (f *fakeTool) Describe(call core.ToolCall, _ *core.ToolEnv) (core.Action, error) {
	return core.Action{Kind: core.ActionOther, Summary: "drop it", Confirm: strings.Contains(string(call.Input), `"delete"`)}, nil
}

func newTest(t *testing.T) (*Server, *fakeTool) {
	t.Helper()
	ft := &fakeTool{name: "t"}
	s := New(Options{
		Project: "/p", Build: "b1", Version: "gnosh b1",
		Tools: func(stack func(string) []string) []core.Tool { ft.stack = stack; return []core.Tool{ft} },
		Frames: func(res core.ToolResult) []Frame {
			fr, _ := res.Meta["frames"].([]Frame)
			return fr
		},
		Confirms: map[string]bool{"t": true},
	})
	return s, ft
}

func serve(t *testing.T, s *Server, lines ...string) ([]Reply, error) {
	t.Helper()
	var out bytes.Buffer
	err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out)
	var reps []Reply
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r Reply
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("reply %q: %v", l, err)
		}
		reps = append(reps, r)
	}
	return reps, err
}

const hello = `{"op":"hello","id":"h","protocol":1}`

// bridge.md §4: hello first, one version, and the reply's fields.
func TestSpec4Hello(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		code  string
		ends  bool
	}{
		{"not hello first", []string{`{"op":"tools"}`, `{"op":"tools"}`}, CodeProtocol, true},
		{"not JSON first", []string{`nope`, hello}, CodeProtocol, true},
		{"another version", []string{`{"op":"hello","protocol":2}`, hello}, CodeProtocol, true},
		{"no version", []string{`{"op":"hello"}`}, CodeProtocol, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := newTest(t)
			reps, err := serve(t, s, c.lines...)
			if len(reps) != 1 || reps[0].OK || reps[0].Error.Code != c.code {
				t.Fatalf("replies %+v", reps)
			}
			if (err != nil) != c.ends {
				t.Errorf("ended: %v", err)
			}
		})
	}
	s, _ := newTest(t)
	reps, err := serve(t, s, hello)
	if err != nil || len(reps) != 1 {
		t.Fatalf("%v %+v", err, reps)
	}
	r := reps[0]
	if !r.OK || string(r.ID) != `"h"` || r.Protocol != 1 || r.Build != "b1" || r.Version != "gnosh b1" || r.Project != "/p" || strings.Join(r.Ops, ",") != "hello,tools,call,view" {
		t.Errorf("hello reply %+v", r)
	}
}

// bridge.md §3 and §8: ids come back, errors leave the bridge running.
func TestSpec3And8Errors(t *testing.T) {
	s, _ := newTest(t)
	reps, err := serve(t, s, hello,
		`{"op":"what","id":7}`,
		`{"id":8}`,
		`[1,2]`,
		`{"op":"call","id":9,"session":"S","call_id":"c","name":"nope"}`,
		`{"op":"call","session":"S","name":"t"}`,
		`{"op":"view"}`,
		`{"op":"view","session":"S","entries":[{"type":"user"}]}`,
		`{"op":"tools","id":10}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"", CodeUnknownOp, CodeBadRequest, CodeBadRequest, CodeUnknownTool, CodeBadRequest, CodeBadRequest, CodeBadRequest, ""}
	for i, r := range reps {
		got := ""
		if r.Error != nil {
			got = r.Error.Code
		}
		if got != want[i] || r.OK != (want[i] == "") {
			t.Errorf("reply %d: %+v, want code %q", i, r, want[i])
		}
	}
	if string(reps[1].ID) != "7" || string(reps[4].ID) != "9" || string(reps[8].ID) != "10" {
		t.Errorf("ids not echoed: %s %s %s", reps[1].ID, reps[4].ID, reps[8].ID)
	}
}

// bridge.md §5 and §6: the tools as they are, calls, frames and approval.
func TestSpec5And6Calls(t *testing.T) {
	s, ft := newTest(t)
	reps, err := serve(t, s, hello, `{"op":"tools"}`,
		`{"op":"call","session":"S","call_id":"c1","name":"t","input":{"frames":[{"kind":"push","id":"a","session":"S"},{"kind":"push","id":"x","session":"other"}]}}`,
		`{"op":"call","session":"S","call_id":"c2","name":"t","input":{"fail":true}}`,
		`{"op":"call","session":"S","call_id":"c3","name":"t","input":{"frames":[{"kind":"pop","id":"a","session":"S"}]}}`,
		`{"op":"call","session":"S","call_id":"c4","name":"t","input":{"action":"delete"}}`,
		`{"op":"call","session":"S","call_id":"c5","name":"t","input":{"action":"delete"},"confirmed":true}`,
		`{"op":"call","session":"S","call_id":"c6","name":"t"}`)
	if err != nil {
		t.Fatal(err)
	}
	if ti := reps[1].Tools; len(ti) != 1 || ti[0].Name != "t" || string(ti[0].InputSchema) != `{"type":"object"}` || !ti[0].Confirm {
		t.Errorf("tools %+v", reps[1].Tools)
	}
	if r := reps[2]; !r.OK || len(r.Frames) != 1 || r.Frames[0].ID != "a" {
		t.Errorf("a call's frames are its session's own: %+v", r)
	}
	if r := reps[3]; !r.OK || !r.IsError {
		t.Errorf("a failure the model sees is not an error: %+v", r)
	}
	if r := reps[5]; r.OK || r.Error.Code != CodeConfirm || r.Error.Summary != "drop it" {
		t.Errorf("unconfirmed: %+v", r)
	}
	if !reps[6].OK || len(ft.calls) != 5 {
		t.Errorf("confirmed: %+v; %d calls ran", reps[6], len(ft.calls))
	}
	if string(ft.calls[4].Input) != "{}" {
		t.Errorf("a call with no input is given {}: %s", ft.calls[4].Input)
	}
	// §6.3: each call is made from the stack the calls before it left.
	want := []string{"", "a", "a", "", "", ""}
	for i, st := range ft.seen {
		if strings.Join(st, "/") != want[i] {
			t.Errorf("call %d made from stack %v, want %q", i+1, st, want[i])
		}
	}
}

// bridge.md §6.3 and §7: a view sets the stack, and says where each
// message came from.
func TestSpec7View(t *testing.T) {
	s, ft := newTest(t)
	push := transcript.PushEntry("f", "a frame", "c1")
	push.SessionID = "S"
	entries := []transcript.Entry{
		transcript.PromptEntry("go"),
		transcript.AssistantEntry(core.Message{Blocks: []core.Block{core.ToolUse("c1", "t", nil)}}, core.StopToolUse),
		transcript.UserEntry(core.Message{Blocks: []core.Block{core.ToolResultBlock("c1", core.ToolResult{Content: []core.Block{core.Text("pushed")}})}}, transcript.SourceToolResults),
		push,
	}
	for i := range entries {
		entries[i].UUID = string(rune('a' + i))
	}
	view, _ := json.Marshal(map[string]any{"op": "view", "session": "S", "entries": entries})
	reps, err := serve(t, s, hello, string(view), `{"op":"call","session":"S","call_id":"c2","name":"t"}`)
	if err != nil {
		t.Fatal(err)
	}
	v := reps[1]
	if !v.OK || len(v.Messages) != 3 || mustJSON(t, v.Sources) != `[["a"],["b"],["c"]]` {
		t.Fatalf("view %+v", v)
	}
	if len(v.Stack) != 1 || v.Stack[0] != (StackFrame{ID: "f", Label: "a frame", CallID: "c1"}) {
		t.Errorf("stack %+v", v.Stack)
	}
	if strings.Join(ft.seen[0], "/") != "f" {
		t.Errorf("a call after a view is made from the view's stack: %v", ft.seen[0])
	}
	// Another session's view does not move this one's stack.
	if st := s.Stack("other"); len(st) != 0 {
		t.Errorf("other session's stack %v", st)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
