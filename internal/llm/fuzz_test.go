package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// decodeStream runs a decoder over a raw event stream the way the stream
// loop does, without a connection.
func decodeStream(w wire, raw []byte) ([]core.Event, error) {
	rd := newSSEReader(bytes.NewReader(raw))
	dec := w.newDecoder(func() {})
	var all, out []core.Event
	for {
		ev, err := rd.next()
		done := false
		switch {
		case err == nil:
			out, done, err = dec.event(out[:0], ev)
		case err == io.EOF:
			out, err = dec.end(out[:0])
			done = true
		default:
			return all, err
		}
		if err != nil {
			out = dec.leftover(out)
		}
		all = append(all, out...)
		if err != nil || done {
			return all, err
		}
	}
}

// checkStream fails the test if a decoded stream breaks one of the
// promises a stream makes to its consumer.
func checkStream(t *testing.T, raw []byte, evs []core.Event, err error) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		lines := make([]string, len(evs))
		for i, e := range evs {
			lines[i] = show(e)
		}
		t.Fatalf("%s\nstream: %q\nevents: %q\nerror: %v", fmt.Sprintf(format, args...), raw, lines, err)
	}
	started := make(map[string]bool)
	closed := make(map[string]bool)
	stops := 0
	for _, e := range evs {
		if stops > 0 {
			fail("an event follows the stop event")
		}
		switch e.Type {
		case core.EventTextDelta, core.EventThinkingDelta:
			if e.Text == "" {
				fail("an empty %s", e.Type)
			}
		case core.EventThinkingSignature:
			if e.Signature == "" && e.Opaque == "" {
				fail("a signature event that carries nothing")
			}
		case core.EventToolUseStart:
			if e.ID == "" || started[e.ID] {
				fail("tool call id %q is empty or used twice", e.ID)
			}
			started[e.ID] = true
		case core.EventToolUseDelta:
			if !started[e.ID] || closed[e.ID] || e.Text == "" {
				fail("arguments for tool call %q, which is not open, or empty arguments", e.ID)
			}
		case core.EventToolUseStop:
			if !started[e.ID] || closed[e.ID] {
				fail("tool call %q is closed without being open", e.ID)
			}
			closed[e.ID] = true
		case core.EventUsage:
			u := e.Usage
			if u.InputTokens < 0 || u.OutputTokens < 0 || u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 {
				fail("negative usage %+v", u)
			}
		case core.EventStop:
			stops++
			if e.StopReason == "" {
				fail("a stop event with no reason")
			}
		default:
			fail("an event of unknown type %q", e.Type)
		}
	}
	if err == nil {
		if stops != 1 {
			fail("%d stop events in a stream that ended well", stops)
		}
		if len(closed) != len(started) {
			fail("%d tool calls opened, %d closed", len(started), len(closed))
		}
	} else {
		if stops != 0 {
			fail("a stop event in a stream that failed")
		}
		var pe *core.ProviderError
		if !errors.As(err, &pe) || pe.Kind == "" || pe.Message == "" || pe.Provider != "test" {
			fail("the error is not a full provider error")
		}
	}
	var acc core.Accumulator
	for _, e := range evs {
		acc.Add(e)
	}
	if _, jerr := json.Marshal(acc.Message()); jerr != nil {
		fail("the message does not encode: %v", jerr)
	}
}

func fuzzWire(f *testing.F, typ string) wire {
	n := 0
	p, err := newProvider(Config{Name: "test", Type: typ, BaseURL: "http://203.0.113.7/v1", NewID: func() string {
		n++
		return fmt.Sprintf("made_%d", n)
	}})
	if err != nil {
		f.Fatal(err)
	}
	switch typ {
	case TypeAnthropic:
		return &anthropic{provider: p}
	case TypeLlamaCpp:
		return &llamaCpp{openAI{p}}
	}
	return &openAI{p}
}

func FuzzChatDecoder(f *testing.F) {
	seeds := []string{
		"",
		events(delta(roleChunk), delta(`{"content":"Hel"}`), delta(`{"content":"lo"}`), finish("stop"), usageChunk(`{"prompt_tokens":9,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4}}`), doneLine),
		events(delta(`{"reasoning_content":"hm"}`), delta(`{"reasoning":"hm"}`), delta(`{"content":"<think>a</think>b"}`), finish("length")),
		events(
			delta(`{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"Read","arguments":""}}]}`),
			delta(`{"tool_calls":[{"index":1,"id":"","function":{"name":"Glob","arguments":"{\"p\":"}}]}`),
			delta(`{"tool_calls":[{"index":0,"function":{"arguments":"{}"}},{"index":1,"function":{"arguments":"1}"}}]}`),
			delta(`{"tool_calls":[{"index":0,"id":"call_b","function":{"name":"Read","arguments":{"a":1}}}]}`),
			delta(`{"tool_calls":[{"function":{"arguments":"x"}},{"id":"call_a","name":"N","arguments":null}]}`),
			finish("tool_calls"), doneLine),
		events(delta(`{"function_call":{"name":"f","arguments":"{"}}`), delta(`{"function_call":{"arguments":"}"}}`), finish("function_call"), doneLine),
		events(delta(`{"content":"x"}`), `data: {"error":{"code":502,"message":"gone","metadata":{"error_type":"provider_unavailable"}}}`),
		events(delta(`{"content":"x"}`), finish("error")),
		events(delta(`{"content":[{"type":"text","text":"a"}],"refusal":"no"}`), doneLine),
		events(chunk(`"choices":[{"index":0,"message":{"content":"whole","tool_calls":[{"id":"c","function":{"name":"n","arguments":"{}"}}]},"finish_reason":"stop"}],"timings":`+llamaTimings), doneLine),
		events(`data: {"object":"error","message":"m","code":400}`),
		events("event: error\ndata: {\"message\":\"overloaded\"}"),
		events(`data: {"choices":[{"index":"0","delta":{"content":1,"tool_calls":[{"index":1.5,"id":7}]},"finish_reason":3}],"usage":{"prompt_tokens":"9","completion_tokens":-4}}`, doneLine),
		events(`data: {"choices":"x"}`),
		events(`data: [1,2,3]`, `data: null`, `data: {}`, doneLine),
		": comment\n\ndata: ping\n\n" + events(delta(`{"content":"<thi"}`)),
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	plain := fuzzWire(f, TypeOpenAI)
	split := fuzzWire(f, TypeLlamaCpp)
	f.Fuzz(func(t *testing.T, raw []byte) {
		for _, w := range []wire{plain, split} {
			evs, err := decodeStream(w, raw)
			checkStream(t, raw, evs, err)
		}
	})
}

func FuzzClaudeDecoder(f *testing.F) {
	seeds := []string{
		"",
		events(
			messageStart(`{"input_tokens":25,"cache_read_input_tokens":5,"cache_creation_input_tokens":6,"output_tokens":1}`),
			blockStart(0, `{"type":"thinking","thinking":"","signature":""}`),
			blockDelta(0, `{"type":"thinking_delta","thinking":"hm"}`),
			blockDelta(0, `{"type":"signature_delta","signature":"sig"}`),
			blockStop(0),
			blockStart(1, `{"type":"redacted_thinking","data":"xx"}`), blockStop(1),
			blockStart(2, `{"type":"text","text":""}`), ping,
			blockDelta(2, `{"type":"text_delta","text":"Hello"}`), blockStop(2),
			blockStart(3, `{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}`),
			blockDelta(3, `{"type":"input_json_delta","partial_json":"{\"a\":"}`),
			blockDelta(3, `{"type":"input_json_delta","partial_json":"1}"}`),
			blockStop(3),
			messageDelta("tool_use", `{"output_tokens":15}`), messageStop),
		events(
			blockStart(0, `{"type":"tool_use","id":"","name":"A","input":{"x":1}}`),
			blockStart(0, `{"type":"tool_use","id":"dup","name":"B","input":null}`),
			blockStart(1, `{"type":"tool_use","id":"dup","name":"C","input":{}}`),
			blockDelta(1, `{"type":"input_json_delta","partial_json":"{}"}`),
			blockDelta(7, `{"type":"input_json_delta","partial_json":"{}"}`),
			blockStop(9), messageDelta("end_turn", ""), messageStop),
		events(blockStart(0, `{"type":"server_tool_use","id":"s","name":"web_search","input":{}}`), blockDelta(0, `{"type":"input_json_delta","partial_json":"{}"}`), blockStop(0), messageDelta("pause_turn", ""), messageStop),
		events(blockStart(0, `{"type":"text","text":"x"}`), ev("error", `"error":{"type":"overloaded_error","message":"Overloaded"}`)),
		events(blockStart(0, `{"type":"text","text":"x"}`), messageStop),
		events(blockStart(0, `{"type":"text","text":"x"}`), messageDelta("refusal", "")),
		events(messageStop),
		"event: message_stop\n\n",
		"event: content_block_start\ndata: {\"index\":\"0\",\"content_block\":{\"type\":\"text\",\"text\":\"t\"}}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":7},\"usage\":{\"output_tokens\":-3}}\n\n",
		events(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"orphan"}}`, `data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`),
		events(`data: {"type":`, `data: 12`),
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	w := fuzzWire(f, TypeAnthropic)
	f.Fuzz(func(t *testing.T, raw []byte) {
		evs, err := decodeStream(w, raw)
		checkStream(t, raw, evs, err)
	})
}
