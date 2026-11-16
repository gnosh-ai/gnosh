package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// twoReplies is a turn of two replies: one that reads a file, and the
// answer.
func twoReplies() []apptest.Reply {
	return []apptest.Reply{
		{Text: "Let me look.", Calls: []apptest.Call{{ID: "call_1", Name: "Read", Args: `{"file_path":"a.txt"}`}}, In: 100, Out: 7},
		{Text: "The answer is 4.", In: 120, Out: 5},
	}
}

// The text format prints the model's words and nothing else: each reply's
// text, a blank line between two, and nothing on standard error.
func TestRunTextFormat(t *testing.T) {
	w := apptest.NewWorld(t, twoReplies()...)
	w.Write("a.txt", "two plus two\n")
	r := run(t, w, "-p", "what is 2+2?").wantCode(t, 0).wantNoErr(t)
	if want := "Let me look.\n\nThe answer is 4.\n"; r.out != want {
		t.Errorf("stdout = %q, want %q", r.out, want)
	}
	w.Model.Done()
}

const wantResult = `{"type":"result","subtype":"success","uuid":"UUID","session_id":"UUID","is_error":false,"result":"The answer is 4.","stop_reason":"end_turn","num_turns":2,"duration_ms":0,"duration_api_ms":0,"total_cost_usd":0,"usage":{"input_tokens":220,"output_tokens":12,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"modelUsage":{"test-model":{"inputTokens":220,"outputTokens":12,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"webSearchRequests":0,"costUSD":0,"contextWindow":100000,"maxOutputTokens":0}},"permission_denials":[],"terminal_reason":"completed"}
`

// The json format prints one object, the result, when the run ends.
func TestRunJSONFormat(t *testing.T) {
	w := apptest.NewWorld(t, twoReplies()...)
	w.Write("a.txt", "two plus two\n")
	r := run(t, w, "-p", "--output-format", "json", "what is 2+2?").wantCode(t, 0).wantNoErr(t)
	if got := masked(w, r.out); got != wantResult {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, wantResult)
	}
}

const wantStream = `{"type":"system","subtype":"init","uuid":"UUID","session_id":"UUID","cwd":"ROOT/proj","model":"test-model","tools":["Read","Write","Edit","Glob","Grep","Bash","TaskStop","Monitor","WebFetch","Agent","SendMessage","AskUserQuestion","EnterPlanMode","ExitPlanMode"],"mcp_servers":[],"permissionMode":"default","slash_commands":["help","exit","background","restart","clear","compact","think","no_think","effort","model","status","config","loop","noloop","permissions","hooks","output-style","skills","reload-skills","autocompact","context","btw","statusline","recap","rename","keybindings","color","resume","fork","branch","rewind","plan","add-dir","panel","tasks","cost","agents","memory","mcp","diff","export","copy","review","security-review","init","debug","doctor"],"output_style":"default","skills":[],"plugins":[],"apiKeySource":"none","gnosh_version":"test-build"}
{"type":"assistant","uuid":"UUID","session_id":"UUID","parent_tool_use_id":null,"message":{"id":"msg_ID_1","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"Let me look."}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
{"type":"assistant","uuid":"UUID","session_id":"UUID","parent_tool_use_id":null,"message":{"id":"msg_ID_1","type":"message","role":"assistant","model":"test-model","content":[{"type":"tool_use","id":"call_ID","name":"Read","input":{"file_path":"a.txt"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":100,"output_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
{"type":"user","uuid":"UUID","session_id":"UUID","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_ID","content":"1\ttwo plus two\n"}]},"tool_use_result":{"file":{"filePath":"ROOT/proj/a.txt","numLines":1,"startLine":1,"totalLines":1},"type":"text"}}
{"type":"assistant","uuid":"UUID","session_id":"UUID","parent_tool_use_id":null,"message":{"id":"msg_ID_2","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"The answer is 4."}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":120,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}
` + wantResult

// The stream-json format prints a line for the session, one for every
// block of every reply, one for every tool result, and the result last.
func TestRunStreamJSONFormat(t *testing.T) {
	w := apptest.NewWorld(t, twoReplies()...)
	w.Write("a.txt", "two plus two\n")
	r := run(t, w, "-p", "--output-format", "stream-json", "what is 2+2?").wantCode(t, 0).wantNoErr(t)
	if got := masked(w, r.out); got != wantStream {
		t.Errorf("stdout:\n%s\nwant:\n%s", got, wantStream)
	}
	// Every line carries the one session id, and the session's file is
	// named by it.
	lines := jsonLines(t, r.out)
	id, _ := lines[0]["session_id"].(string)
	for i, l := range lines {
		if l["session_id"] != id {
			t.Errorf("line %d has session_id %v, the first has %s", i, l["session_id"], id)
		}
	}
	if s := w.Sessions(); len(s) != 1 || !strings.HasSuffix(s[0], id+".jsonl") {
		t.Errorf("session files %v, want one named after %s", s, id)
	}
}

// --include-partial-messages adds each reply as the Messages API's
// streaming events: message_start; each block's start, pieces and stop,
// the last block's stop after the reply's assistant lines; message_delta
// and message_stop; all of the reply's one message id, and the text
// pieces add up to the text.
func TestRunPartialMessages(t *testing.T) {
	w := apptest.NewWorld(t, twoReplies()...)
	w.Write("a.txt", "two plus two\n")
	r := run(t, w, "-p", "--output-format", "stream-json", "--include-partial-messages", "what is 2+2?").wantCode(t, 0).wantNoErr(t)
	var kinds []string
	var text, input strings.Builder
	ids := map[string]int{}
	for _, l := range jsonLines(t, r.out) {
		kind, _ := l["type"].(string)
		switch kind {
		case "stream_event":
			ev, _ := l["event"].(map[string]any)
			kind, _ = ev["type"].(string)
			if d, ok := ev["delta"].(map[string]any); ok {
				switch d["type"] {
				case "text_delta":
					text.WriteString(d["text"].(string))
				case "input_json_delta":
					input.WriteString(d["partial_json"].(string))
				}
			}
			if m, ok := ev["message"].(map[string]any); ok {
				ids[m["id"].(string)]++
			}
			if kind == "message_start" {
				if _, ok := l["ttft_ms"]; !ok {
					t.Errorf("message_start without ttft_ms: %v", l)
				}
			}
		case "assistant":
			ids[l["message"].(map[string]any)["id"].(string)]++
		}
		if n := len(kinds); kind == "content_block_delta" && n > 0 && kinds[n-1] == kind {
			continue // pieces: how many is the provider's business
		}
		kinds = append(kinds, kind)
	}
	want := "system " +
		"message_start content_block_start content_block_delta content_block_stop content_block_start content_block_delta assistant assistant content_block_stop message_delta message_stop " +
		"user " +
		"message_start content_block_start content_block_delta assistant content_block_stop message_delta message_stop " +
		"result"
	if got := strings.Join(kinds, " "); got != want {
		t.Errorf("the lines:\n got %s\nwant %s", got, want)
	}
	if text.String() != "Let me look.The answer is 4." || input.String() != `{"file_path":"a.txt"}` {
		t.Errorf("the pieces: text %q, input %q", text.String(), input.String())
	}
	if len(ids) != 2 {
		t.Errorf("message ids %v: want the two replies' own, each on its message_start and its assistant lines", ids)
	}
	// Without stream-json it is refused before anything starts.
	r = run(t, w, "-p", "--include-partial-messages", "again").wantCode(t, 1)
	has(t, "without stream-json", r.err, "only does something with --output-format stream-json")
}

// --input-format stream-json is a conversation: one user message a line
// on standard input, each answered by a turn that ends with its own
// result line, which carries the message's uuid; a blank message ends at
// once; a line of another type is passed over; with
// --replay-user-messages each message is written back first. The run
// ends when the input does, and the turns are one session's.
func TestRunStreamJSONInput(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "First answer."}, apptest.Reply{Text: "Second answer."})
	o := testOptions(w, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--replay-user-messages")
	o.Stdin = strings.NewReader(strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"first question"},"uuid":"u-1"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"second question"}]},"uuid":"u-2","priority":"later"}`,
		`{"type":"control_request","request_id":"r1","request":{"subtype":"initialize"}}`,
		`{"type":"user","message":{"role":"user","content":"   "},"uuid":"u-3","priority":"later"}`,
		"",
	}, "\n"))
	o.StdinIsTerminal = false
	r := runWith(t, t.Context(), o).wantCode(t, 0)
	results := map[string]string{}
	replays := 0
	for _, l := range jsonLines(t, r.out) {
		switch l["type"] {
		case "result":
			uuid, _ := l["user_message_uuid"].(string)
			text, _ := l["result"].(string)
			if l["subtype"] != "success" {
				t.Errorf("result for %s: %v", uuid, l)
			}
			results[uuid] = text
		case "user":
			if l["isReplay"] == true {
				replays++
			}
		}
	}
	if results["u-1"] != "First answer." || results["u-2"] != "Second answer." || results["u-3"] != "" || len(results) != 3 {
		t.Errorf("results by message: %v", results)
	}
	if replays != 3 {
		t.Errorf("%d messages written back, want 3", replays)
	}
	has(t, "stderr", r.err, `a line of type "control_request" on standard input was passed over`)
	reqs := w.Model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the model was sent %d requests, want 2", len(reqs))
	}
	has(t, "the second request", string(reqs[1].Body), "first question", "First answer.", "second question")
	if n := len(w.Sessions()); n != 1 {
		t.Errorf("%d session files, want one", n)
	}

	// client_composed delivers a message as it is: no @ mention brings a
	// file.
	w.Write("secret-notes.txt", "SECRET-7f3a\n")
	w.Model.Add(apptest.Reply{Text: "Verbatim."})
	o = testOptions(w, "-p", "--input-format", "stream-json", "--output-format", "stream-json")
	o.Stdin = strings.NewReader(`{"type":"user","message":{"role":"user","content":"look at @secret-notes.txt"},"client_composed":true}` + "\n")
	o.StdinIsTerminal = false
	runWith(t, t.Context(), o).wantCode(t, 0)
	reqs = w.Model.Requests()
	hasNot(t, "a client_composed message", string(reqs[len(reqs)-1].Body), "SECRET-7f3a", "The user mentioned")

	// A line that is not JSON stops the run.
	o = testOptions(w, "-p", "--input-format", "stream-json", "--output-format", "stream-json")
	o.Stdin = strings.NewReader("not json\n")
	o.StdinIsTerminal = false
	r = runWith(t, t.Context(), o).wantCode(t, 1)
	has(t, "stderr", r.err, "is not a JSON object")
}

// --json-schema: once the turn ends well, the model is asked beside the
// session's request for the result as JSON that conforms to the schema,
// and again, told what was wrong, until it does: the value goes in the
// result's structured_output, and with the text format it is all that is
// printed. A schema that cannot be used stops the run first.
func TestRunJSONSchema(t *testing.T) {
	schema := `{"type": "object", "properties": {"count": {"type": "integer"}}, "required": ["count"]}`
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "There are three files."},
		apptest.Reply{Text: "The count is three."},
		apptest.Reply{Text: "```json\n{\"count\": 3}\n```"})
	r := run(t, w, "-p", "how many files?", "--output-format", "json", "--json-schema", schema).wantCode(t, 0)
	obj := jsonLines(t, r.out)[0]
	if out, ok := obj["structured_output"].(map[string]any); !ok || fmt.Sprint(out["count"]) != "3" || obj["subtype"] != "success" {
		t.Errorf("result: %v", obj)
	}
	reqs := w.Model.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want the turn and two asks", len(reqs))
	}
	has(t, "the first ask", string(reqs[1].Body), "conforms to the JSON Schema", `\"required\"`)
	has(t, "the second ask", string(reqs[2].Body), "does not conform", "the answer holds no JSON value")

	// The text format prints the JSON alone.
	w.Model.Add(apptest.Reply{Text: "Three."}, apptest.Reply{Text: `{"count": 3}`})
	r = run(t, w, "-p", "again", "--json-schema", schema).wantCode(t, 0)
	if r.out != "{\"count\":3}\n" {
		t.Errorf("text output: %q", r.out)
	}

	// Never conforming ends the run in error.
	w.Model.Add(apptest.Reply{Text: "Three."}, apptest.Reply{Text: `{"count": "three"}`}, apptest.Reply{Text: `{"n": 3}`}, apptest.Reply{Text: `3`})
	r = run(t, w, "-p", "once more", "--output-format", "json", "--json-schema", schema).wantCode(t, 1)
	obj = jsonLines(t, r.out)[0]
	if obj["subtype"] != "error_max_structured_output_retries" || obj["structured_output"] != nil {
		t.Errorf("result after three bad values: %v", obj)
	}

	// A schema that cannot be used.
	r = run(t, w, "-p", "x", "--json-schema", `{"type": "text"}`).wantCode(t, 1)
	has(t, "stderr", r.err, "--json-schema is not a valid JSON Schema", "which is no JSON type")
}

// checkSession opens a session's file as gnosh itself would, and fails
// the test unless it reads without a warning and what a model would be
// sent from it is a conversation a provider accepts: every tool call
// answered, in order.
func checkSession(t *testing.T, w *apptest.World, path string) []core.Message {
	t.Helper()
	store := transcript.New(w.DataDir())
	s, err := store.OpenPath(context.Background(), path, true)
	if err != nil {
		t.Fatalf("the session %s cannot be opened: %v", path, err)
	}
	defer s.Close()
	for _, warn := range s.Warnings() {
		t.Errorf("the session's file needed mending: %s", warn)
	}
	msgs, _ := s.View(transcript.ViewOptions{})
	if err := transcript.Check(msgs); err != nil {
		t.Errorf("the session does not hold a conversation a provider accepts: %v", err)
	}
	return msgs
}

// With -y a turn reads a file, changes it and runs a command: the files
// are really changed, and the session's file holds the whole turn.
func TestRunReadEditBash(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Read", Args: `{"file_path":"a.txt"}`}}},
		apptest.Reply{Text: "Changing it.", Calls: []apptest.Call{
			{ID: "c2", Name: "Edit", Args: `{"file_path":"a.txt","old_string":"two plus two","new_string":"three plus three"}`},
			{ID: "c3", Name: "Bash", Args: `{"command":"echo made by bash > b.txt && echo ran"}`},
		}},
		apptest.Reply{Text: "Both are done."})
	w.Write("a.txt", "two plus two\n")
	r := run(t, w, "-y", "-p", "change the file").wantCode(t, 0)
	if r.out != "Changing it.\n\nBoth are done.\n" {
		t.Errorf("stdout = %q", r.out)
	}
	if got := w.Read("a.txt"); got != "three plus three\n" {
		t.Errorf("a.txt = %q", got)
	}
	if got := w.Read("b.txt"); got != "made by bash\n" {
		t.Errorf("b.txt = %q", got)
	}
	w.Model.Done()

	// The model was told how each call went, in the order it made them.
	names, results := toolResults(w.Model.Requests()[2])
	if strings.Join(names, " ") != "c1 c2 c3" {
		t.Fatalf("tool results in the third request: %q %q", names, results)
	}
	has(t, "the command's result", results[2], "ran")

	// The session's file holds it all, and reads back clean.
	sessions := w.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("session files: %v", sessions)
	}
	msgs := checkSession(t, w, sessions[0])
	uses, answers := 0, 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			switch b.Type {
			case core.BlockToolUse:
				uses++
			case core.BlockToolResult:
				answers++
			}
		}
	}
	if uses != 3 || answers != 3 {
		t.Errorf("the session holds %d tool calls and %d results, want 3 and 3", uses, answers)
	}
}

// Without -y nobody is there to approve a change, so the edit is refused:
// the file stays as it was, the model is told why, and the result lists
// the refusal. The turn itself finished, so the exit code is 0.
func TestRunEditRefusedUnattended(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Read", Args: `{"file_path":"a.txt"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Edit", Args: `{"file_path":"a.txt","old_string":"two plus","new_string":"three plus"}`}}},
		apptest.Reply{Text: "I could not change it."})
	w.Write("a.txt", "two plus two\n")
	r := run(t, w, "-p", "--output-format", "json", "change the file").wantCode(t, 0)
	if got := w.Read("a.txt"); got != "two plus two\n" {
		t.Errorf("a.txt was changed: %q", got)
	}
	var obj struct {
		Subtype string `json:"subtype"`
		Result  string `json:"result"`
		Denials []struct {
			ToolName  string          `json:"tool_name"`
			ToolUseID string          `json:"tool_use_id"`
			ToolInput json.RawMessage `json:"tool_input"`
		} `json:"permission_denials"`
	}
	if err := json.Unmarshal([]byte(r.out), &obj); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, r.out)
	}
	if obj.Subtype != "success" || obj.Result != "I could not change it." {
		t.Errorf("result: %+v", obj)
	}
	if len(obj.Denials) != 1 || obj.Denials[0].ToolName != "Edit" || obj.Denials[0].ToolUseID != callID(t, w.Model.Requests()[2], "c2") || !strings.Contains(string(obj.Denials[0].ToolInput), "old_string") {
		t.Errorf("permission_denials: %+v", obj.Denials)
	}
	// The model was told that it was refused, and why; the person is told
	// how to let a run act.
	third := requestText(t, w, 2)
	has(t, "the request after the refusal", third, "nobody is here to give it")
	has(t, "stderr", r.err, "Edit was not run", "--allowedTools")
	w.Model.Done()
}

// An allow rule lets a call through without a question: one from the
// user's settings file, and one from --allowedTools.
func TestRunAllowRules(t *testing.T) {
	script := func() []apptest.Reply {
		return []apptest.Reply{
			{Calls: []apptest.Call{{ID: "c1", Name: "Read", Args: `{"file_path":"a.txt"}`}}},
			{Calls: []apptest.Call{
				{ID: "c2", Name: "Edit", Args: `{"file_path":"a.txt","old_string":"two plus","new_string":"three plus"}`},
				{ID: "c3", Name: "Bash", Args: `{"command":"touch b.txt"}`},
			}},
			{Text: "Done."},
		}
	}
	t.Run("settings file", func(t *testing.T) {
		w := apptest.NewWorld(t, script()...)
		w.Write("a.txt", "two plus two\n")
		w.Allow("Edit", "Bash(touch *)")
		run(t, w, "-p", "change it").wantCode(t, 0)
		if w.Read("a.txt") != "three plus two\n" || !w.Exists("b.txt") {
			t.Errorf("a.txt = %q, b.txt exists = %v", w.Read("a.txt"), w.Exists("b.txt"))
		}
	})
	t.Run("--allowedTools", func(t *testing.T) {
		w := apptest.NewWorld(t, script()...)
		w.Write("a.txt", "two plus two\n")
		run(t, w, "-p", "--allowedTools", "Edit", "Bash(touch *)", "--", "change it").wantCode(t, 0)
		if w.Read("a.txt") != "three plus two\n" || !w.Exists("b.txt") {
			t.Errorf("a.txt = %q, b.txt exists = %v", w.Read("a.txt"), w.Exists("b.txt"))
		}
	})
	t.Run("a rule that does not cover the call", func(t *testing.T) {
		w := apptest.NewWorld(t, script()...)
		w.Write("a.txt", "two plus two\n")
		run(t, w, "-p", "--allowedTools", "Bash(ls *)", "--", "change it").wantCode(t, 0)
		if w.Read("a.txt") != "two plus two\n" || w.Exists("b.txt") {
			t.Errorf("a.txt = %q, b.txt exists = %v", w.Read("a.txt"), w.Exists("b.txt"))
		}
	})
}

// A deny rule wins over everything, -y included: the call is not run and
// the model is told which rule refused it.
func TestRunDenyRule(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{
			{ID: "c1", Name: "Bash", Args: `{"command":"rm -f a.txt"}`},
			{ID: "c2", Name: "Read", Args: `{"file_path":"secret.txt"}`},
		}},
		apptest.Reply{Text: "Both were refused."})
	w.Write("a.txt", "keep me\n")
	w.Write("secret.txt", "the secret\n")
	w.Deny("Read(./secret.txt)")
	r := run(t, w, "-y", "-p", "--output-format", "json", "--disallowedTools", "Bash(rm *)", "--", "clean up").wantCode(t, 0)
	if !w.Exists("a.txt") {
		t.Errorf("a.txt was removed although a deny rule covers the command")
	}
	second := requestText(t, w, 1)
	has(t, "the request after the refusals", second, "Bash(rm *)", "Read(./secret.txt)")
	hasNot(t, "the request after the refusals", second, "the secret")
	has(t, "the result", r.out, `"tool_name":"Bash"`, `"tool_name":"Read"`)
	w.Model.Done()
}

// Several tool calls in one reply are all run, and the model reads their
// results in the order it made the calls.
func TestRunSeveralCallsInOrder(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{
			{ID: "c1", Name: "Read", Args: `{"file_path":"one.txt"}`},
			{ID: "c2", Name: "Bash", Args: `{"command":"cat two.txt"}`},
			{ID: "c3", Name: "Read", Args: `{"file_path":"three.txt"}`},
			{ID: "c4", Name: "Grep", Args: `{"pattern":"FOUR","output_mode":"content"}`},
		}},
		apptest.Reply{Text: "Read all four."})
	for _, n := range []string{"one", "two", "three", "four"} {
		w.Write(n+".txt", strings.ToUpper(n)+"\n")
	}
	run(t, w, "-y", "-p", "read them").wantCode(t, 0)
	got, texts := toolResults(w.Model.Requests()[1])
	if strings.Join(got, " ") != "c1 c2 c3 c4" {
		t.Fatalf("the results came in the order %v", got)
	}
	for i, want := range []string{"ONE", "TWO", "THREE", "FOUR"} {
		has(t, "the result of "+got[i], texts[i], want)
	}
}
