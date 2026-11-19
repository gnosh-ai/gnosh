package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// ev writes one event of a Messages stream the way the API does: the
// event's name on one line and its JSON, which repeats the name as "type",
// on the next. The streams in these tests are written by hand from the
// API's public documentation.
func ev(name, fields string) string {
	data := `{"type":"` + name + `"`
	if fields != "" {
		data += "," + fields
	}
	return "event: " + name + "\ndata: " + data + "}"
}

func messageStart(usage string) string {
	return ev("message_start", `"message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-test","stop_reason":null,"stop_sequence":null,"usage":`+usage+`}`)
}

func blockStart(index int, block string) string {
	return ev("content_block_start", `"index":`+itoa(index)+`,"content_block":`+block)
}

func blockDelta(index int, delta string) string {
	return ev("content_block_delta", `"index":`+itoa(index)+`,"delta":`+delta)
}

func blockStop(index int) string {
	return ev("content_block_stop", `"index":`+itoa(index))
}

func messageDelta(reason string, usage string) string {
	fields := `"delta":{"stop_reason":"` + reason + `","stop_sequence":null}`
	if usage != "" {
		fields += `,"usage":` + usage
	}
	return ev("message_delta", fields)
}

func itoa(i int) string { return string(rune('0' + i)) }

var (
	ping        = ev("ping", "")
	messageStop = ev("message_stop", "")
)

func TestAnthropicStreams(t *testing.T) {
	runStreamCases(t, TypeAnthropic, []streamCase{
		{
			name: "plain text",
			stream: events(
				messageStart(`{"input_tokens":25,"output_tokens":1}`),
				blockStart(0, `{"type":"text","text":""}`),
				ping,
				blockDelta(0, `{"type":"text_delta","text":"Hello"}`),
				blockDelta(0, `{"type":"text_delta","text":"!"}`),
				blockStop(0),
				messageDelta("end_turn", `{"output_tokens":15}`),
				messageStop,
			),
			events: []string{
				`usage in=25 out=1 read=0 write=0`,
				`text "Hello"`, `text "!"`,
				`usage in=25 out=15 read=0 write=0`,
				`stop end_turn`,
			},
			message: []string{`text "Hello!"`},
		},
		{
			name: "thinking, its signature, then text",
			stream: events(
				messageStart(`{"input_tokens":30,"output_tokens":2}`),
				blockStart(0, `{"type":"thinking","thinking":"","signature":""}`),
				blockDelta(0, `{"type":"thinking_delta","thinking":"I need to find the GCD."}`),
				blockDelta(0, `{"type":"thinking_delta","thinking":"\n147 = 7 × 21 + 0"}`),
				blockDelta(0, `{"type":"signature_delta","signature":"EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds"}`),
				blockStop(0),
				blockStart(1, `{"type":"text","text":""}`),
				blockDelta(1, `{"type":"text_delta","text":"It is **21**."}`),
				blockStop(1),
				ev("message_delta", `"delta":{"stop_reason":"end_turn","stop_sequence":null}`),
				messageStop,
			),
			events: []string{
				`usage in=30 out=2 read=0 write=0`,
				`thinking "I need to find the GCD."`,
				`thinking "\n147 = 7 × 21 + 0"`,
				`signature "EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds"`,
				`text "It is **21**."`,
				`stop end_turn`,
			},
			message: []string{
				`thinking "I need to find the GCD.\n147 = 7 × 21 + 0" signature "EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds"`,
				`text "It is **21**."`,
			},
		},
		{
			name: "thinking that is not shown: an empty block with a signature",
			stream: events(
				messageStart(`{"input_tokens":30,"output_tokens":2}`),
				blockStart(0, `{"type":"thinking","thinking":"","signature":""}`),
				blockDelta(0, `{"type":"thinking_delta","thinking":""}`),
				blockDelta(0, `{"type":"signature_delta","signature":"EosnCkYICxIMMb3LzNrMu"}`),
				blockStop(0),
				blockStart(1, `{"type":"text","text":""}`),
				blockDelta(1, `{"type":"text_delta","text":"12,231."}`),
				blockStop(1),
				messageDelta("end_turn", `{"output_tokens":900}`),
				messageStop,
			),
			events: []string{
				`usage in=30 out=2 read=0 write=0`,
				`signature "EosnCkYICxIMMb3LzNrMu"`,
				`text "12,231."`,
				`usage in=30 out=900 read=0 write=0`,
				`stop end_turn`,
			},
			message: []string{`thinking "" signature "EosnCkYICxIMMb3LzNrMu"`, `text "12,231."`},
		},
		{
			name: "a signature sent in pieces, and two thinking blocks in a row",
			stream: events(
				messageStart(`{"input_tokens":1,"output_tokens":1}`),
				blockStart(0, `{"type":"thinking","thinking":"","signature":""}`),
				blockDelta(0, `{"type":"thinking_delta","thinking":"one"}`),
				blockDelta(0, `{"type":"signature_delta","signature":"sig-"}`),
				blockDelta(0, `{"type":"signature_delta","signature":"A"}`),
				blockStop(0),
				blockStart(1, `{"type":"thinking","thinking":"","signature":""}`),
				blockDelta(1, `{"type":"thinking_delta","thinking":"two"}`),
				blockDelta(1, `{"type":"signature_delta","signature":"sig-B"}`),
				blockStop(1),
				messageDelta("end_turn", ""),
				messageStop,
			),
			events: []string{
				`usage in=1 out=1 read=0 write=0`,
				`thinking "one"`, `signature "sig-A"`,
				`thinking "two"`, `signature "sig-B"`,
				`stop end_turn`,
			},
			message: []string{`thinking "one" signature "sig-A"`, `thinking "two" signature "sig-B"`},
		},
		{
			name: "redacted thinking",
			stream: events(
				messageStart(`{"input_tokens":1,"output_tokens":1}`),
				blockStart(0, `{"type":"thinking","thinking":"","signature":""}`),
				blockDelta(0, `{"type":"thinking_delta","thinking":"seen"}`),
				blockDelta(0, `{"type":"signature_delta","signature":"sig"}`),
				blockStop(0),
				blockStart(1, `{"type":"redacted_thinking","data":"EmwKAhgBEgy3va3pzix"}`),
				blockStop(1),
				blockStart(2, `{"type":"text","text":""}`),
				blockDelta(2, `{"type":"text_delta","text":"ok"}`),
				blockStop(2),
				messageDelta("end_turn", ""),
				messageStop,
			),
			events: []string{
				`usage in=1 out=1 read=0 write=0`,
				`thinking "seen"`, `signature "sig"`,
				`signature opaque "EmwKAhgBEgy3va3pzix"`,
				`text "ok"`,
				`stop end_turn`,
			},
			message: []string{`thinking "seen" signature "sig"`, `thinking "" opaque "EmwKAhgBEgy3va3pzix"`, `text "ok"`},
		},
		{
			name: "text, then one tool call",
			stream: events(
				messageStart(`{"input_tokens":472,"output_tokens":2}`),
				blockStart(0, `{"type":"text","text":""}`),
				ping,
				blockDelta(0, `{"type":"text_delta","text":"Okay"}`),
				blockDelta(0, `{"type":"text_delta","text":", let's check."}`),
				blockStop(0),
				blockStart(1, `{"type":"tool_use","id":"toolu_01T1x1fJ34qAmk2tNTrN7Up6","name":"get_weather","input":{}}`),
				blockDelta(1, `{"type":"input_json_delta","partial_json":""}`),
				blockDelta(1, `{"type":"input_json_delta","partial_json":"{\"location\":"}`),
				blockDelta(1, `{"type":"input_json_delta","partial_json":" \"San"}`),
				blockDelta(1, `{"type":"input_json_delta","partial_json":" Francisco, CA\"}"}`),
				blockStop(1),
				messageDelta("tool_use", `{"output_tokens":89}`),
				messageStop,
			),
			events: []string{
				`usage in=472 out=2 read=0 write=0`,
				`text "Okay"`, `text ", let's check."`,
				`tool toolu_01T1x1fJ34qAmk2tNTrN7Up6 get_weather`,
				`args toolu_01T1x1fJ34qAmk2tNTrN7Up6 "{\"location\":"`,
				`args toolu_01T1x1fJ34qAmk2tNTrN7Up6 " \"San"`,
				`args toolu_01T1x1fJ34qAmk2tNTrN7Up6 " Francisco, CA\"}"`,
				`done toolu_01T1x1fJ34qAmk2tNTrN7Up6`,
				`usage in=472 out=89 read=0 write=0`,
				`stop tool_use`,
			},
			message: []string{
				`text "Okay, let's check."`,
				`tool_use toolu_01T1x1fJ34qAmk2tNTrN7Up6 get_weather {"location":"San Francisco, CA"}`,
			},
		},
		{
			name: "parallel tool calls, with thinking before them",
			stream: events(
				messageStart(`{"input_tokens":10,"output_tokens":1}`),
				blockStart(0, `{"type":"thinking","thinking":"","signature":""}`),
				blockDelta(0, `{"type":"thinking_delta","thinking":"Two files."}`),
				blockDelta(0, `{"type":"signature_delta","signature":"sig"}`),
				blockStop(0),
				blockStart(1, `{"type":"tool_use","id":"toolu_a","name":"Read","input":{}}`),
				blockDelta(1, `{"type":"input_json_delta","partial_json":"{\"file_path\":\"/a\"}"}`),
				blockStop(1),
				blockStart(2, `{"type":"tool_use","id":"toolu_b","name":"Read","input":{}}`),
				blockDelta(2, `{"type":"input_json_delta","partial_json":"{\"file_path\":"}`),
				blockDelta(2, `{"type":"input_json_delta","partial_json":"\"/b\"}"}`),
				blockStop(2),
				blockStart(3, `{"type":"tool_use","id":"toolu_c","name":"Glob","input":{}}`),
				blockStop(3),
				messageDelta("tool_use", `{"output_tokens":60}`),
				messageStop,
			),
			events: []string{
				`usage in=10 out=1 read=0 write=0`,
				`thinking "Two files."`, `signature "sig"`,
				`tool toolu_a Read`, `args toolu_a "{\"file_path\":\"/a\"}"`, `done toolu_a`,
				`tool toolu_b Read`, `args toolu_b "{\"file_path\":"`, `args toolu_b "\"/b\"}"`, `done toolu_b`,
				`tool toolu_c Glob`, `done toolu_c`,
				`usage in=10 out=60 read=0 write=0`,
				`stop tool_use`,
			},
			message: []string{
				`thinking "Two files." signature "sig"`,
				`tool_use toolu_a Read {"file_path":"/a"}`,
				`tool_use toolu_b Read {"file_path":"/b"}`,
				`tool_use toolu_c Glob {}`,
			},
		},
		{
			name: "tool input sent whole with the start; empty and repeated ids",
			stream: events(
				messageStart(`{"input_tokens":1,"output_tokens":1}`),
				blockStart(0, `{"type":"tool_use","id":"","name":"Read","input":{"file_path":"/a"}}`),
				blockStop(0),
				blockStart(1, `{"type":"tool_use","id":"dup","name":"Read","input":{}}`),
				blockDelta(1, `{"type":"input_json_delta","partial_json":"{}"}`),
				blockStop(1),
				blockStart(2, `{"type":"tool_use","id":"dup","name":"Glob","input":null}`),
				blockStop(2),
				messageDelta("tool_use", ""),
				messageStop,
			),
			events: []string{
				`usage in=1 out=1 read=0 write=0`,
				`tool made_1 Read`, `args made_1 "{\"file_path\":\"/a\"}"`, `done made_1`,
				`tool dup Read`, `args dup "{}"`, `done dup`,
				`tool made_2 Glob`, `done made_2`,
				`stop tool_use`,
			},
			message: []string{`tool_use made_1 Read {"file_path":"/a"}`, `tool_use dup Read {}`, `tool_use made_2 Glob {}`},
		},
		{
			name: "usage: cache counts at the start, running totals at the end",
			stream: events(
				messageStart(`{"input_tokens":12,"cache_creation_input_tokens":2051,"cache_read_input_tokens":90000,"output_tokens":3}`),
				blockStart(0, `{"type":"text","text":""}`),
				blockDelta(0, `{"type":"text_delta","text":"ok"}`),
				blockStop(0),
				ev("message_delta", `"delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":14,"cache_creation_input_tokens":null,"output_tokens":40}`),
				messageStop,
			),
			events: []string{
				`usage in=12 out=3 read=90000 write=2051`,
				`text "ok"`,
				`usage in=14 out=40 read=90000 write=2051`,
				`stop end_turn`,
			},
			message: []string{`text "ok"`},
		},
		{
			name: "the API's own tools and blocks of unknown kinds are passed over",
			stream: events(
				messageStart(`{"input_tokens":2679,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}`),
				blockStart(0, `{"type":"text","text":""}`),
				blockDelta(0, `{"type":"text_delta","text":"I'll check."}`),
				blockStop(0),
				blockStart(1, `{"type":"server_tool_use","id":"srvtoolu_014hJH82Qum7Td6UV8gDXThB","name":"web_search","input":{}}`),
				blockDelta(1, `{"type":"input_json_delta","partial_json":"{\"query\":\"weather NYC today\"}"}`),
				blockStop(1),
				blockStart(2, `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_014hJH82Qum7Td6UV8gDXThB","content":[{"type":"web_search_result","title":"Weather","url":"https://example.com","encrypted_content":"Ev0DCioIAxgC","page_age":null}]}`),
				blockStop(2),
				blockStart(3, `{"type":"fallback","from":{"model":"a"},"to":{"model":"b"}}`),
				blockStop(3),
				blockStart(4, `{"type":"text","text":""}`),
				blockDelta(4, `{"type":"text_delta","text":" Sunny."}`),
				blockDelta(4, `{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.com"}}`),
				blockStop(4),
				ev("some_new_event", `"x":1`),
				messageDelta("end_turn", `{"output_tokens":50,"server_tool_use":{"web_search_requests":1}}`),
				messageStop,
			),
			events: []string{
				`usage in=2679 out=3 read=0 write=0`,
				`text "I'll check."`, `text " Sunny."`,
				`usage in=2679 out=50 read=0 write=0`,
				`stop end_turn`,
			},
			message: []string{`text "I'll check. Sunny."`},
		},
		{
			name: "an empty answer is an answer",
			stream: events(
				messageStart(`{"input_tokens":5,"output_tokens":1}`),
				messageDelta("end_turn", `{"output_tokens":1}`),
				messageStop,
			),
			events: []string{`usage in=5 out=1 read=0 write=0`, `usage in=5 out=1 read=0 write=0`, `stop end_turn`},
		},
		{
			name: "the stream closes after the stop reason, with no message_stop",
			stream: events(
				messageStart(`{"input_tokens":5,"output_tokens":1}`),
				blockStart(0, `{"type":"text","text":""}`),
				blockDelta(0, `{"type":"text_delta","text":"ok"}`),
				messageDelta("end_turn", ""),
			),
			events:  []string{`usage in=5 out=1 read=0 write=0`, `text "ok"`, `stop end_turn`},
			message: []string{`text "ok"`},
		},
		{
			name: "message_stop with no stop reason: the reason is read off the answer",
			stream: events(
				blockStart(0, `{"type":"tool_use","id":"toolu_a","name":"Read","input":{}}`),
				blockDelta(0, `{"type":"input_json_delta","partial_json":"{}"}`),
				ev("message_delta", `"delta":{"stop_reason":null}`),
				messageStop,
			),
			events:  []string{`tool toolu_a Read`, `args toolu_a "{}"`, `done toolu_a`, `stop tool_use`},
			message: []string{`tool_use toolu_a Read {}`},
		},
		{
			name: "events without the data's own type, bare names, CRLF and comments",
			stream: "event: message_start\r\ndata: {\"message\":{\"usage\":{\"input_tokens\":3}}}\r\n\r\n" +
				": keep-alive\r\n\r\n" +
				"event: content_block_start\r\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Hi\"}}\r\n\r\n" +
				"event: content_block_delta\r\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" there\"}}\r\n\r\n" +
				"event: ping\r\n\r\n" +
				"data: not json at all\r\n\r\n" +
				"event: message_delta\r\ndata: {\"delta\":{\"stop_reason\":\"end_turn\"}}\r\n\r\n" +
				"event: message_stop\r\n\r\n",
			events:  []string{`usage in=3 out=0 read=0 write=0`, `text "Hi"`, `text " there"`, `stop end_turn`},
			message: []string{`text "Hi there"`},
		},
	})
}

func TestAnthropicStopReasons(t *testing.T) {
	text := events(blockStart(0, `{"type":"text","text":""}`), blockDelta(0, `{"type":"text_delta","text":"x"}`), blockStop(0))
	call := events(blockStart(0, `{"type":"tool_use","id":"toolu_a","name":"Read","input":{}}`), blockStop(0))
	tests := []struct {
		reason string
		body   string
		want   core.StopReason
	}{
		{"end_turn", text, core.StopEndTurn},
		{"max_tokens", text, core.StopMaxTokens},
		{"stop_sequence", text, core.StopSequence},
		{"tool_use", call, core.StopToolUse},
		{"refusal", text, core.StopRefusal},
		{"pause_turn", text, core.StopEndTurn},
		{"model_context_window_exceeded", text, core.StopMaxTokens},
		{"max_tokens", call, core.StopMaxTokens},
		{"end_turn", call, core.StopToolUse}, // tool calls that are there must be answered
		{"tool_use", text, core.StopEndTurn}, // and a caller told to run tools must have some
		{"something_new", text, core.StopEndTurn},
	}
	for _, tt := range tests {
		stream := events(messageStart(`{"input_tokens":1,"output_tokens":1}`)) + tt.body + events(messageDelta(tt.reason, ""), messageStop)
		f := newFake(t, playing(stream))
		res := ask(f.provider(TypeAnthropic, nil))
		wantOK(t, res)
		if res.stop != tt.want {
			t.Errorf("stop_reason %q: stop = %s, want %s", tt.reason, res.stop, tt.want)
		}
	}
}

func TestAnthropicErrors(t *testing.T) {
	before := []struct {
		name   string
		status int
		header map[string]string
		body   string
		kind   core.ErrorKind
		retry  time.Duration
	}{
		{"bad request", 400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: at least one message is required"},"request_id":"req_1"}`, core.ErrInvalidRequest, 0},
		{"prompt too long", 400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213742 tokens > 200000 maximum"},"request_id":"req_1"}`, core.ErrContextTooLong, 0},
		{"bad key", 401, nil, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"},"request_id":"req_1"}`, core.ErrAuth, 0},
		{"billing", 402, nil, `{"type":"error","error":{"type":"billing_error","message":"Your credit balance is too low"}}`, core.ErrAuth, 0},
		{"permission", 403, nil, `{"type":"error","error":{"type":"permission_error","message":"not allowed"}}`, core.ErrAuth, 0},
		{"not found", 404, nil, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-nope"}}`, core.ErrInvalidRequest, 0},
		{"request too large", 413, nil, `{"type":"error","error":{"type":"request_too_large","message":"Request exceeds the maximum allowed number of bytes."}}`, core.ErrContextTooLong, 0},
		{"rate limit", 429, map[string]string{"Retry-After": "42"}, `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`, core.ErrRateLimit, 42 * time.Second},
		{"spend cap", 429, nil, `{"type":"error","error":{"type":"rate_limit_error","message":"You have reached your API usage limits.","details":{"error_code":"enforced_spend_limit_reached"}}}`, core.ErrAuth, 0},
		{"api error", 500, nil, `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`, core.ErrServer, 0},
		{"overloaded", 529, nil, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, core.ErrOverloaded, 0},
	}
	for _, tt := range before {
		t.Run("before the stream/"+tt.name, func(t *testing.T) {
			f := newFake(t, answering(tt.status, tt.header, tt.body))
			res := ask(f.provider(TypeAnthropic, nil))
			e := wantFailure(t, res.err, tt.kind)
			if e.Status != tt.status || e.RetryAfter != tt.retry || e.Body != tt.body || e.Message == "" {
				t.Errorf("status %d, retry after %s, message %q, body %q", e.Status, e.RetryAfter, e.Message, e.Body)
			}
			if len(res.events) != 0 || len(f.requests()) != 1 {
				t.Errorf("events %q, %d requests", res.events, len(f.requests()))
			}
			f.waitClosed()
		})
	}

	start := events(
		messageStart(`{"input_tokens":25,"output_tokens":1}`),
		blockStart(0, `{"type":"text","text":""}`),
		blockDelta(0, `{"type":"text_delta","text":"Half an ans"}`),
	)
	during := []struct {
		name  string
		event string
		kind  core.ErrorKind
	}{
		{"overloaded", `event: error` + "\n" + `data: {"type": "error", "error": {"type": "overloaded_error", "message": "Overloaded"}}`, core.ErrOverloaded},
		{"api error", ev("error", `"error":{"type":"api_error","message":"Internal server error"}`), core.ErrServer},
		{"rate limit", ev("error", `"error":{"type":"rate_limit_error","message":"slow"}`), core.ErrRateLimit},
		{"by its name only", "event: error\ndata: {\"error\":{\"message\":\"something broke\"}}", core.ErrServer},
		{"by its type only", `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, core.ErrOverloaded},
	}
	for _, tt := range during {
		t.Run("in the stream/"+tt.name, func(t *testing.T) {
			f := newFake(t, playing(start+events(tt.event), hang{}))
			res := ask(f.provider(TypeAnthropic, nil))
			e := wantFailure(t, res.err, tt.kind)
			if e.Status != 0 || e.Message == "" || e.Body == "" {
				t.Errorf("status %d, message %q, body %q", e.Status, e.Message, e.Body)
			}
			wantLines(t, "events", res.events, []string{`usage in=25 out=1 read=0 write=0`, `text "Half an ans"`})
			wantLines(t, "message", res.message, []string{`text "Half an ans"`})
			if res.usage.InputTokens != 25 {
				t.Errorf("the usage that came before the error is lost: %+v", res.usage)
			}
			f.waitClosed()
		})
	}
}

func TestAnthropicStreamCutShort(t *testing.T) {
	head := events(messageStart(`{"input_tokens":5,"output_tokens":1}`), blockStart(0, `{"type":"text","text":""}`), blockDelta(0, `{"type":"text_delta","text":"half"}`))
	tool := events(blockStart(0, `{"type":"tool_use","id":"toolu_a","name":"Write","input":{}}`), blockDelta(0, `{"type":"input_json_delta","partial_json":"{\"file_path\":\"/a\",\"content\":\"half a fi"}`))
	tests := []struct {
		name    string
		steps   []any
		kind    core.ErrorKind
		events  []string
		message []string
	}{
		{"the stream ends in the middle of the text", []any{head}, core.ErrBadResponse,
			[]string{`usage in=5 out=1 read=0 write=0`, `text "half"`}, []string{`text "half"`}},
		{"the stream ends in the middle of a tool call", []any{tool}, core.ErrBadResponse,
			[]string{`tool toolu_a Write`, `args toolu_a "{\"file_path\":\"/a\",\"content\":\"half a fi"`}, []string{`tool_use toolu_a Write {}`}},
		{"message_stop and nothing else", []any{events(messageStop)}, core.ErrBadResponse, nil, nil},
		{"only a ping", []any{events(ping)}, core.ErrBadResponse, nil, nil},
		{"an event that is not JSON", []any{head + events("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_del")}, core.ErrBadResponse,
			[]string{`usage in=5 out=1 read=0 write=0`, `text "half"`}, []string{`text "half"`}},
		{"the connection breaks", []any{head, cut{}}, core.ErrNetwork,
			[]string{`usage in=5 out=1 read=0 write=0`, `text "half"`}, []string{`text "half"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t, playing(tt.steps...))
			res := ask(f.provider(TypeAnthropic, nil))
			wantFailure(t, res.err, tt.kind)
			wantLines(t, "events", res.events, tt.events)
			wantLines(t, "message", res.message, tt.message)
			if res.stops != 0 {
				t.Errorf("a stream that failed sent a stop event")
			}
			f.waitClosed()
		})
	}
}

func TestAnthropicRequest(t *testing.T) {
	f := newFake(t, playing(events(messageDelta("end_turn", ""), messageStop)))
	p := f.provider(TypeAnthropic, func(c *Config) { c.APIKey = "sk-ant-secret" })
	wantOK(t, drain(p.Stream(context.Background(), fullRequest())))

	got := f.last()
	if got.Method != "POST" || got.Path != "/v1/messages" {
		t.Errorf("sent %s %s", got.Method, got.Path)
	}
	wantJSON(t, "request body", got.Body, `{
		"model": "gpt-test",
		"max_tokens": 1000,
		"stream": true,
		"temperature": 0.5,
		"top_p": 0.9,
		"stop_sequences": ["END"],
		"thinking": {"type": "adaptive"},
		"output_config": {"effort": "high"},
		"system": [
			{"type": "text", "text": "You are gnosh.", "cache_control": {"type": "ephemeral"}},
			{"type": "text", "text": "The project is a compiler.", "cache_control": {"type": "ephemeral"}}
		],
		"tools": [
			{"name": "Read", "description": "Reads a file.", "input_schema": {"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}},
			{"name": "Glob", "input_schema": {"type":"object","properties":{}}, "cache_control": {"type": "ephemeral"}}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "Read main.go"},
				{"type": "text", "text": "and <b>fix</b> it."}
			]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "I should read it.", "signature": "sig-1"},
				{"type": "text", "text": "I'll read it."},
				{"type": "tool_use", "id": "call_1", "name": "Read", "input": {"file_path": "main.go"}},
				{"type": "tool_use", "id": "call_2", "name": "Glob", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_1", "content": [{"type": "text", "text": "package main"}]},
				{"type": "tool_result", "tool_use_id": "call_2", "content": [{"type": "text", "text": "no matches"}], "is_error": true},
				{"type": "text", "text": "Go on."}
			]},
			{"role": "assistant", "content": [{"type": "text", "text": "Done."}]},
			{"role": "user", "content": [{"type": "text", "text": "Thanks.", "cache_control": {"type": "ephemeral"}}]}
		]
	}`)

	wantHeader := map[string]string{
		"X-Api-Key":         "sk-ant-secret",
		"Anthropic-Version": "2023-06-01",
		"User-Agent":        "gnosh",
		"Content-Type":      "application/json",
		"Accept":            "text/event-stream",
	}
	for k, v := range wantHeader {
		if got.Header.Get(k) != v {
			t.Errorf("header %s = %q, want %q", k, got.Header.Get(k), v)
		}
	}
	if got.Header.Get("Authorization") != "" {
		t.Errorf("Authorization = %q, want none", got.Header.Get("Authorization"))
	}
	f.waitClosed()
}

func TestAnthropicRequestBlocks(t *testing.T) {
	f := newFake(t, playing(events(messageDelta("end_turn", ""), messageStop)))
	p := f.provider(TypeAnthropic, func(c *Config) {
		c.BaseURL = f.srv.URL + "/v1/" // A base URL may already hold the version.
		c.PromptCache = "off"
		c.Headers = map[string]string{"anthropic-version": "2026-10-21", "anthropic-beta": "thinking-binding-controls-2026-08-01"}
	})
	req := &core.Request{
		Model: "claude-test",
		Messages: []core.Message{
			{Role: core.RoleUser, Blocks: []core.Block{
				core.Text("Look:"),
				{Type: core.BlockImage, MediaType: "image/jpeg", Data: []byte("jpegdata")},
				core.Text("  \n"), // nothing but white space: the API refuses it
				core.Text(""),
			}},
			{Role: core.RoleAssistant, Blocks: []core.Block{
				{Type: core.BlockThinking, Text: "from another provider"},          // no signature: left out
				{Type: core.BlockThinking, Text: "", Signature: "sig-empty"},       // not shown, still signed: kept
				{Type: core.BlockThinking, Opaque: "redacted-data"},                // kept as redacted_thinking
				{Type: core.BlockThinking, Text: "mine", Signature: "sig-mine"},    // kept as it came
				{Type: core.BlockToolUse, ID: "toolu_1", Name: "Shot", Input: nil}, // no input: {}
				{Type: core.BlockToolUse, ID: "toolu_2", Name: "Read", Input: json.RawMessage(`"not an object"`)},
			}},
			{Role: core.RoleUser, Blocks: []core.Block{
				{Type: core.BlockToolResult, ToolUseID: "toolu_1", Content: []core.Block{
					core.Text("A screenshot."),
					{Type: core.BlockImage, Data: []byte("\x89PNG\r\n\x1a\nfake")},
				}},
				{Type: core.BlockToolResult, ToolUseID: "toolu_2", IsError: true},
			}},
			{Role: core.RoleAssistant, Blocks: []core.Block{{Type: core.BlockThinking, Text: "unsigned only"}}}, // nothing left: the message is left out
			{Role: core.RoleUser, Blocks: []core.Block{core.Text("")}},                                          // likewise
			{Role: core.RoleUser, Blocks: []core.Block{core.Text("And now?")}},
		},
		Thinking: core.Thinking{Mode: "on"},
		Extra:    map[string]any{"thinking": map[string]any{"display": "summarized"}},
	}
	wantOK(t, drain(p.Stream(context.Background(), req)))
	got := f.last()
	if got.Path != "/v1/messages" {
		t.Errorf("sent to %s", got.Path)
	}
	if v := got.Header.Get("Anthropic-Version"); v != "2026-10-21" {
		t.Errorf("anthropic-version = %q; a configured header wins", v)
	}
	if v := got.Header.Get("Anthropic-Beta"); v != "thinking-binding-controls-2026-08-01" {
		t.Errorf("anthropic-beta = %q", v)
	}
	if got.Header.Get("X-Api-Key") != "" {
		t.Errorf("x-api-key sent with no key configured")
	}
	wantJSON(t, "request body", got.Body, `{
		"model": "claude-test",
		"max_tokens": 64000,
		"stream": true,
		"thinking": {"type": "adaptive", "display": "summarized"},
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "Look:"},
				{"type": "image", "source": {"type": "base64", "media_type": "image/jpeg", "data": "anBlZ2RhdGE="}}
			]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "", "signature": "sig-empty"},
				{"type": "redacted_thinking", "data": "redacted-data"},
				{"type": "thinking", "thinking": "mine", "signature": "sig-mine"},
				{"type": "tool_use", "id": "toolu_1", "name": "Shot", "input": {}},
				{"type": "tool_use", "id": "toolu_2", "name": "Read", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": [
					{"type": "text", "text": "A screenshot."},
					{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgpmYWtl"}}
				]},
				{"type": "tool_result", "tool_use_id": "toolu_2", "is_error": true}
			]},
			{"role": "user", "content": [{"type": "text", "text": "And now?"}]}
		]
	}`)

	// With thinking dropped from history, no thinking block goes back.
	p = f.provider(TypeAnthropic, func(c *Config) { c.HistoryThinking = "drop"; c.PromptCache = "off" })
	wantOK(t, drain(p.Stream(context.Background(), &core.Request{
		Model: "claude-test",
		Messages: []core.Message{
			core.UserText("hi"),
			{Role: core.RoleAssistant, Blocks: []core.Block{
				{Type: core.BlockThinking, Text: "mine", Signature: "sig-mine"},
				{Type: core.BlockThinking, Opaque: "redacted-data"},
				core.Text("Hello."),
			}},
			core.UserText("more"),
		},
	})))
	wantJSON(t, "request body, thinking dropped", f.last().Body, `{
		"model": "claude-test", "max_tokens": 64000, "stream": true,
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hi"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "Hello."}]},
			{"role": "user", "content": [{"type": "text", "text": "more"}]}
		]
	}`)
}

func TestAnthropicThinkingAndMaxTokens(t *testing.T) {
	tests := []struct {
		name     string
		thinking core.Thinking
		max      int
		def      int
		want     string // the fields that depend on the above
	}{
		{"left to the model", core.Thinking{}, 0, 0, `"max_tokens": 64000`},
		{"off", core.Thinking{Mode: "off"}, 0, 0, `"max_tokens": 64000, "thinking": {"type": "disabled"}`},
		{"off wins over a budget", core.Thinking{Mode: "off", BudgetTokens: 5000}, 0, 0, `"max_tokens": 64000, "thinking": {"type": "disabled"}`},
		{"on", core.Thinking{Mode: "on"}, 0, 0, `"max_tokens": 64000, "thinking": {"type": "adaptive"}`},
		{"on with a budget", core.Thinking{Mode: "on", BudgetTokens: 5000}, 0, 0, `"max_tokens": 64000, "thinking": {"type": "enabled", "budget_tokens": 5000}`},
		{"a budget alone asks for thinking", core.Thinking{BudgetTokens: 5000}, 0, 0, `"max_tokens": 64000, "thinking": {"type": "enabled", "budget_tokens": 5000}`},
		{"effort alone", core.Thinking{Effort: "low"}, 0, 0, `"max_tokens": 64000, "output_config": {"effort": "low"}`},
		{"on with effort", core.Thinking{Mode: "on", Effort: "xhigh"}, 0, 0, `"max_tokens": 64000, "thinking": {"type": "adaptive"}, "output_config": {"effort": "xhigh"}`},
		{"the request's cap", core.Thinking{}, 1234, 0, `"max_tokens": 1234`},
		{"the configured default", core.Thinking{}, 0, 8000, `"max_tokens": 8000`},
		{"the request's cap wins over the default", core.Thinking{}, 1234, 8000, `"max_tokens": 1234`},
		{"a default cap makes room for the budget", core.Thinking{Mode: "on", BudgetTokens: 10000}, 0, 8000, `"max_tokens": 18000, "thinking": {"type": "enabled", "budget_tokens": 10000}`},
		{"the request's cap is sent as it is, the budget under it", core.Thinking{Mode: "on", BudgetTokens: 10000}, 2000, 0, `"max_tokens": 2000, "thinking": {"type": "enabled", "budget_tokens": 1999}`},
		{"a budget under the least is the least", core.Thinking{Mode: "on", BudgetTokens: 500}, 0, 0, `"max_tokens": 64000, "thinking": {"type": "enabled", "budget_tokens": 1024}`},
		{"a cap with no room for the least budget", core.Thinking{Mode: "on", BudgetTokens: 4096}, 1000, 0, `"max_tokens": 1000, "thinking": {"type": "adaptive"}`},
	}
	for _, tt := range tests {
		f := newFake(t, playing(events(messageDelta("end_turn", ""), messageStop)))
		p := f.provider(TypeAnthropic, func(c *Config) { c.DefaultMaxTokens = tt.def; c.PromptCache = "off" })
		req := &core.Request{Model: "m", Messages: []core.Message{core.UserText("hi")}, Thinking: tt.thinking, MaxTokens: tt.max}
		wantOK(t, drain(p.Stream(context.Background(), req)))
		wantJSON(t, tt.name, f.last().Body, `{"model": "m", "stream": true, "messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}], `+tt.want+`}`)
	}
}

// A request with no cap of its own asks for no more than the model writes
// in one answer, as the Models API listed it: the default of 64000 was
// above an older Claude's 32000, and every such request was refused (the
// model client review of 2026-11-19). A thinking budget goes under that.
func TestAnthropicDefaultCapIsTheModelsOwn(t *testing.T) {
	stream := playing(events(messageDelta("end_turn", ""), messageStop))
	f := newFake(t, func(f *fake, w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"type":"model","id":"claude-old","max_input_tokens":200000,"max_tokens":32000},{"type":"model","id":"claude-new","max_input_tokens":1000000,"max_tokens":128000}],"has_more":false,"last_id":"claude-new"}`)
			return
		}
		stream(f, w, r)
	})
	p := f.provider(TypeAnthropic, func(c *Config) { c.PromptCache = "off" })
	if _, err := p.(core.ModelLister).Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		model    string
		thinking core.Thinking
		max      int
		want     string
	}{
		{"claude-old", core.Thinking{}, 0, `"max_tokens": 32000`},
		{"claude-old", core.Thinking{Mode: "on", BudgetTokens: 40000}, 0, `"max_tokens": 32000, "thinking": {"type": "enabled", "budget_tokens": 31999}`},
		{"claude-old", core.Thinking{}, 50000, `"max_tokens": 50000`},
		{"claude-new", core.Thinking{}, 0, `"max_tokens": 64000`},
		{"unlisted", core.Thinking{}, 0, `"max_tokens": 64000`},
	} {
		req := &core.Request{Model: tt.model, Messages: []core.Message{core.UserText("hi")}, Thinking: tt.thinking, MaxTokens: tt.max}
		wantOK(t, drain(p.Stream(context.Background(), req)))
		wantJSON(t, tt.model+" "+tt.want, f.last().Body, `{"model": "`+tt.model+`", "stream": true, "messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}], `+tt.want+`}`)
	}
}

// No thinking is asked for the way each model takes it, as the Models API
// says: thinking {type: "disabled"} where it is taken (or not said),
// {type: "between_tools"} on a model that takes only that, and the least
// effort on one whose thinking cannot be turned off. The models that
// refuse "disabled" refused every request that asked for no thinking (the
// model client review of 2026-11-19).
func TestAnthropicThinkingOffAsEachModelTakesIt(t *testing.T) {
	stream := playing(events(messageDelta("end_turn", ""), messageStop))
	f := newFake(t, func(f *fake, w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[
				{"id":"always-thinks","max_tokens":128000,"capabilities":{"thinking":{"supported":true,"types":{"disabled":{"supported":false},"between_tools":{"supported":false}}}}},
				{"id":"between-tools","max_tokens":128000,"capabilities":{"thinking":{"supported":true,"types":{"disabled":{"supported":false},"between_tools":{"supported":true}}}}},
				{"id":"can-stop","max_tokens":128000,"capabilities":{"thinking":{"supported":true,"types":{"disabled":{"supported":true},"between_tools":{"supported":false}}}}},
				{"id":"says-nothing","max_tokens":128000}
			],"has_more":false,"last_id":"says-nothing"}`)
			return
		}
		stream(f, w, r)
	})
	p := f.provider(TypeAnthropic, func(c *Config) { c.PromptCache = "off" })
	if _, err := p.(core.ModelLister).Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]string{
		"always-thinks": `"output_config": {"effort": "low"}`,
		"between-tools": `"thinking": {"type": "between_tools"}`,
		"can-stop":      `"thinking": {"type": "disabled"}`,
		"says-nothing":  `"thinking": {"type": "disabled"}`,
		"unlisted":      `"thinking": {"type": "disabled"}`,
	} {
		req := &core.Request{Model: model, Messages: []core.Message{core.UserText("hi")}, Thinking: core.Thinking{Mode: "off"}}
		wantOK(t, drain(p.Stream(context.Background(), req)))
		wantJSON(t, model, f.last().Body, `{"model": "`+model+`", "stream": true, "max_tokens": 64000, "messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}], `+want+`}`)
	}
}

// cacheMarks lists where a request body carries cache marks, as
// "system[0]", "tools[1]", "messages[2][0]".
func cacheMarks(t *testing.T, body []byte) []string {
	t.Helper()
	var req struct {
		System []map[string]any `json:"system"`
		Tools  []map[string]any `json:"tools"`
		Msgs   []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	var out []string
	for i, b := range req.Tools {
		if b["cache_control"] != nil {
			out = append(out, "tools["+itoa(i)+"]")
		}
	}
	for i, b := range req.System {
		if b["cache_control"] != nil {
			out = append(out, "system["+itoa(i)+"]")
		}
	}
	for i, m := range req.Msgs {
		for j, b := range m.Content {
			if b["cache_control"] != nil {
				out = append(out, "messages["+itoa(i)+"]["+itoa(j)+"]")
			}
		}
	}
	return out
}

func TestAnthropicCacheMarks(t *testing.T) {
	tools := []core.ToolSpec{{Name: "A"}, {Name: "B"}, {Name: "C"}}
	convo := []core.Message{
		core.UserText("one"),
		{Role: core.RoleAssistant, Blocks: []core.Block{core.Text("two"), core.ToolUse("t1", "A", nil)}},
		{Role: core.RoleUser, Blocks: []core.Block{core.ToolResultBlock("t1", core.TextResult("r")), core.Text("three")}},
	}
	tests := []struct {
		name   string
		system []string
		tools  []core.ToolSpec
		msgs   []core.Message
		change func(*Config)
		want   []string
		ttl    string
	}{
		{"everything fits", []string{"a", "b"}, tools, convo, nil, []string{"tools[2]", "system[0]", "system[1]", "messages[2][1]"}, ""},
		{"more system parts than marks: the first ones get them", []string{"a", "b", "c", "d"}, tools, convo, nil, []string{"tools[2]", "system[0]", "system[1]", "messages[2][1]"}, ""},
		{"no tools leaves a mark for the system prompt", []string{"a", "b", "c", "d"}, nil, convo, nil, []string{"system[0]", "system[1]", "system[2]", "messages[2][1]"}, ""},
		{"empty system parts do not count", []string{"a", "", " ", "b"}, tools, convo, nil, []string{"tools[2]", "system[0]", "system[1]", "messages[2][1]"}, ""},
		{"no system prompt", nil, tools, convo, nil, []string{"tools[2]", "messages[2][1]"}, ""},
		{"only a message", nil, nil, []core.Message{core.UserText("hi")}, nil, []string{"messages[0][0]"}, ""},
		{"the last block is a tool call", []string{"a"}, tools, convo[:2:2], nil, []string{"tools[2]", "system[0]", "messages[1][1]"}, ""},
		{"a thinking block cannot carry a mark", []string{"a"}, nil,
			[]core.Message{core.UserText("hi"), {Role: core.RoleAssistant, Blocks: []core.Block{core.Text("so"), {Type: core.BlockThinking, Text: "t", Signature: "s"}}}},
			nil, []string{"system[0]", "messages[1][0]"}, ""},
		{"nothing in the last message can carry a mark", []string{"a"}, nil,
			[]core.Message{core.UserText("hi"), {Role: core.RoleAssistant, Blocks: []core.Block{{Type: core.BlockThinking, Text: "t", Signature: "s"}}}},
			nil, []string{"system[0]", "messages[0][0]"}, ""},
		{"switched off", []string{"a", "b"}, tools, convo, func(c *Config) { c.PromptCache = "off" }, nil, ""},
		{"switched on by name", []string{"a"}, nil, convo, func(c *Config) { c.PromptCache = "on" }, []string{"system[0]", "messages[2][1]"}, ""},
		{"kept for an hour", []string{"a"}, tools, convo, func(c *Config) { c.CacheTTL = "1h" }, []string{"tools[2]", "system[0]", "messages[2][1]"}, "1h"},
		{"five minutes is the API's default and is not spelled out", []string{"a"}, nil, convo, func(c *Config) { c.CacheTTL = "5m" }, []string{"system[0]", "messages[2][1]"}, ""},
	}
	for _, tt := range tests {
		f := newFake(t, playing(events(messageDelta("end_turn", ""), messageStop)))
		p := f.provider(TypeAnthropic, tt.change)
		req := &core.Request{Model: "m", System: tt.system, Tools: tt.tools, Messages: tt.msgs}
		wantOK(t, drain(p.Stream(context.Background(), req)))
		body := f.last().Body
		got := cacheMarks(t, body)
		wantLines(t, tt.name, got, tt.want)
		if len(got) > maxCacheMarks {
			t.Errorf("%s: %d marks; the API takes %d", tt.name, len(got), maxCacheMarks)
		}
		wantTTL := `"cache_control":{"type":"ephemeral"}`
		if tt.ttl != "" {
			wantTTL = `"cache_control":{"ttl":"` + tt.ttl + `","type":"ephemeral"}`
		}
		if n := strings.Count(string(body), wantTTL); n != len(tt.want) {
			t.Errorf("%s: %d marks of the form %s, want %d\n%s", tt.name, n, wantTTL, len(tt.want), body)
		}
	}
}

func TestAnthropicModels(t *testing.T) {
	f := newFake(t, func(f *fake, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after_id") {
		case "":
			io.WriteString(w, `{"data":[
				{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5","created_at":"2026-09-21T00:00:00Z","max_input_tokens":1000000,"max_tokens":128000,"capabilities":{"thinking":{"supported":true}}},
				{"type":"model","id":"claude-haiku-4-5","display_name":"Claude Haiku 4.5","created_at":"2025-10-01T00:00:00Z","max_input_tokens":200000,"max_tokens":64000}
			],"first_id":"claude-opus-5-5","has_more":true,"last_id":"claude-haiku-4-5"}`)
		case "claude-haiku-4-5":
			io.WriteString(w, `{"data":[{"type":"model","id":"claude-old","max_input_tokens":null,"max_tokens":null}],"first_id":"claude-old","has_more":false,"last_id":"claude-old"}`)
		default:
			t.Errorf("unexpected page %q", r.URL.RawQuery)
		}
	})
	p := f.provider(TypeAnthropic, func(c *Config) { c.APIKey = "sk-ant-1" })
	got, err := p.(core.ModelLister).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []core.ModelInfo{
		{ID: "claude-opus-5-5", ContextWindow: 1000000, MaxOutput: 128000},
		{ID: "claude-haiku-4-5", ContextWindow: 200000, MaxOutput: 64000},
		{ID: "claude-old"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	reqs := f.requests()
	if len(reqs) != 2 || reqs[0].Method != "GET" || reqs[0].Path != "/v1/models" || reqs[0].Query != "limit=1000" || reqs[1].Query != "after_id=claude-haiku-4-5&limit=1000" {
		t.Errorf("requests: %+v", reqs)
	}
	if h := reqs[0].Header; h.Get("X-Api-Key") != "sk-ant-1" || h.Get("Anthropic-Version") != "2023-06-01" {
		t.Errorf("headers: %v", h)
	}
	f.waitClosed()

	// A list that says it goes on and does not say where is not followed
	// round and round.
	f = newFake(t, answering(200, nil, `{"data":[{"id":"a"}],"has_more":true,"last_id":""}`))
	_, err = f.provider(TypeAnthropic, nil).(core.ModelLister).Models(context.Background())
	wantFailure(t, err, core.ErrBadResponse)
	f = newFake(t, answering(200, nil, `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`))
	_, err = f.provider(TypeAnthropic, nil).(core.ModelLister).Models(context.Background())
	wantFailure(t, err, core.ErrBadResponse)
	if n := len(f.requests()); n != 2 {
		t.Errorf("%d requests for a list that repeats its cursor, want 2", n)
	}
	var turn atomic.Int32
	f = newFake(t, func(f *fake, w http.ResponseWriter, r *http.Request) {
		// A list that goes round in a circle of three.
		next := []string{"b", "c", "a"}[(turn.Add(1)-1)%3]
		io.WriteString(w, `{"data":[{"id":"m"}],"has_more":true,"last_id":"`+next+`"}`)
	})
	_, err = f.provider(TypeAnthropic, nil).(core.ModelLister).Models(context.Background())
	wantFailure(t, err, core.ErrBadResponse)
	if n := len(f.requests()); n != 4 {
		t.Errorf("%d requests for a list that goes in a circle of three, want 4", n)
	}
	f = newFake(t, answering(401, nil, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	_, err = f.provider(TypeAnthropic, nil).(core.ModelLister).Models(context.Background())
	wantFailure(t, err, core.ErrAuth)
}

func TestAnthropicCountTokens(t *testing.T) {
	f := newFake(t, answering(200, nil, `{"input_tokens":2188}`))
	p := f.provider(TypeAnthropic, func(c *Config) {
		c.Extra = map[string]any{"metadata": map[string]any{"user_id": "u"}, "tool_choice": map[string]any{"type": "auto"}}
	})
	req := fullRequest()
	req.Extra = map[string]any{"thinking": map[string]any{"display": "omitted"}, "context_management": map[string]any{"edits": []any{}}}
	n, err := p.(core.TokenCounter).CountTokens(context.Background(), req)
	if err != nil || n != 2188 {
		t.Fatalf("got %d, %v", n, err)
	}
	got := f.last()
	if got.Method != "POST" || got.Path != "/v1/messages/count_tokens" || got.Header.Get("Accept") != "application/json" {
		t.Errorf("sent %s %s, Accept %q", got.Method, got.Path, got.Header.Get("Accept"))
	}
	// What a count takes of a message request: no cap, no stream, no
	// sampling, no cache marks, and of the extra fields only the ones the
	// counting endpoint knows.
	wantJSON(t, "count body", got.Body, `{
		"model": "gpt-test",
		"thinking": {"type": "enabled", "budget_tokens": 2000, "display": "omitted"},
		"output_config": {"effort": "high"},
		"tool_choice": {"type": "auto"},
		"system": [
			{"type": "text", "text": "You are gnosh."},
			{"type": "text", "text": "The project is a compiler."}
		],
		"tools": [
			{"name": "Read", "description": "Reads a file.", "input_schema": {"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"]}},
			{"name": "Glob", "input_schema": {"type":"object","properties":{}}}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "text", "text": "Read main.go"},
				{"type": "text", "text": "and <b>fix</b> it."}
			]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "I should read it.", "signature": "sig-1"},
				{"type": "text", "text": "I'll read it."},
				{"type": "tool_use", "id": "call_1", "name": "Read", "input": {"file_path": "main.go"}},
				{"type": "tool_use", "id": "call_2", "name": "Glob", "input": {}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "call_1", "content": [{"type": "text", "text": "package main"}]},
				{"type": "tool_result", "tool_use_id": "call_2", "content": [{"type": "text", "text": "no matches"}], "is_error": true},
				{"type": "text", "text": "Go on."}
			]},
			{"role": "assistant", "content": [{"type": "text", "text": "Done."}]},
			{"role": "user", "content": [{"type": "text", "text": "Thanks."}]}
		]
	}`)
	f.waitClosed()

	f = newFake(t, answering(400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213742 tokens > 200000 maximum"}}`))
	_, err = f.provider(TypeAnthropic, nil).(core.TokenCounter).CountTokens(context.Background(), req)
	wantFailure(t, err, core.ErrContextTooLong)
	f = newFake(t, answering(200, nil, `{"tokens":5}`))
	_, err = f.provider(TypeAnthropic, nil).(core.TokenCounter).CountTokens(context.Background(), req)
	wantFailure(t, err, core.ErrBadResponse)
	_, err = f.provider(TypeAnthropic, nil).(core.TokenCounter).CountTokens(context.Background(), nil)
	wantFailure(t, err, core.ErrInvalidRequest)
}
