package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func streamLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var v map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &v), line)
		lines = append(lines, v)
	}
	return lines
}

// fixedInit is an init line's content that does not change.
func fixedInit(session, cwd, model string) func() streamInit {
	return func() streamInit {
		return streamInit{SessionID: session, CWD: cwd, Model: model, PermissionMode: config.PermissionModeBypass}
	}
}

func assistantMsg(id, session string, parts ...message.ContentPart) message.Message {
	return message.Message{ID: id, Role: message.Assistant, SessionID: session, Model: "qwen3.8-27b", Parts: parts}
}

// A turn that calls a tool streams as Claude Code's does: the assistant
// message with its tool_use before the tool's result, though crash marks
// the message finished only after the tool has run, then the answer; each
// once, however many updates come.
func TestStreamJSONWritesATurnInOrder(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	o, err := newRunOutput("stream-json", &out)
	require.NoError(t, err)
	o.started("s1", fixedInit("s1", "/work", "qwen3.8-27b"))

	call := message.ToolCall{ID: "c1", Name: "bash", Input: `{"command":"ls"}`, Finished: true}
	o.message(message.Message{ID: "u1", Role: message.User, SessionID: "s1", Parts: []message.ContentPart{message.TextContent{Text: "list files"}}})
	o.message(assistantMsg("a1", "s1", message.TextContent{Text: "Let me"}))
	o.message(assistantMsg("a1", "s1", message.TextContent{Text: "Let me look."}, call))
	o.message(message.Message{ID: "t1", Role: message.Tool, SessionID: "s1", Parts: []message.ContentPart{
		message.ToolResult{ToolCallID: "c1", Name: "bash", Content: "go.mod\nmain.go"},
	}})
	o.message(assistantMsg("a1", "s1", message.TextContent{Text: "Let me look."}, call, message.Finish{Reason: message.FinishReasonToolUse}))
	// A sub-agent's messages and a summary are not the run's own.
	o.message(assistantMsg("x1", "sub", message.TextContent{Text: "sub-agent work"}, message.Finish{Reason: message.FinishReasonEndTurn}))
	o.message(message.Message{ID: "sum", Role: message.Assistant, SessionID: "s1", IsSummaryMessage: true, Parts: []message.ContentPart{message.TextContent{Text: "summary"}, message.Finish{Reason: message.FinishReasonEndTurn}}})
	o.message(assistantMsg("a2", "s1", message.ReasoningContent{Thinking: "Two files."}, message.TextContent{Text: "go.mod and main.go"}, message.Finish{Reason: message.FinishReasonEndTurn}))
	// What the run printed holds every step's text; the result is the answer.
	_, _ = o.w.Write([]byte("Let me look.\n\ngo.mod and main.go"))
	require.NoError(t, o.finish(nil))

	lines := streamLines(t, out.String())
	var types []string
	for _, l := range lines {
		types = append(types, l["type"].(string))
	}
	require.Equal(t, []string{"system", "assistant", "user", "assistant", "result"}, types)

	require.Equal(t, "init", lines[0]["subtype"])
	require.Equal(t, "s1", lines[0]["session_id"])
	require.Equal(t, "/work", lines[0]["cwd"])

	first := lines[1]["message"].(map[string]any)
	require.Equal(t, "tool_use", first["stop_reason"])
	blocks := first["content"].([]any)
	require.Equal(t, map[string]any{"type": "text", "text": "Let me look."}, blocks[0])
	require.Equal(t, map[string]any{"type": "tool_use", "id": "c1", "name": "bash", "input": map[string]any{"command": "ls"}}, blocks[1])

	result := lines[2]["message"].(map[string]any)["content"].([]any)[0].(map[string]any)
	require.Equal(t, map[string]any{"type": "tool_result", "tool_use_id": "c1", "content": "go.mod\nmain.go", "is_error": false}, result)

	last := lines[3]["message"].(map[string]any)
	require.Equal(t, "end_turn", last["stop_reason"])
	require.Equal(t, []any{
		map[string]any{"type": "thinking", "thinking": "Two files."},
		map[string]any{"type": "text", "text": "go.mod and main.go"},
	}, last["content"])

	require.Equal(t, "success", lines[4]["subtype"])
	require.Equal(t, "go.mod and main.go", lines[4]["result"])
	require.Equal(t, float64(2), lines[4]["num_turns"])
}

// A turn cut short leaves its last message unfinished; its text is still
// written, before the result.
func TestStreamJSONWritesAnUnfinishedMessageAtTheEnd(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	o, err := newRunOutput("stream-json", &out)
	require.NoError(t, err)
	o.started("s1", fixedInit("s1", "/work", "m"))
	o.message(assistantMsg("a1", "s1", message.TextContent{Text: "half an ans"}))
	require.Error(t, o.finish(errors.New("model server went away")))

	lines := streamLines(t, out.String())
	require.Len(t, lines, 3)
	require.Equal(t, "assistant", lines[1]["type"])
	msg := lines[1]["message"].(map[string]any)
	require.Nil(t, msg["stop_reason"])
	require.Equal(t, []any{map[string]any{"type": "text", "text": "half an ans"}}, msg["content"])
	require.Equal(t, "error_during_execution", lines[2]["subtype"])
	require.Equal(t, true, lines[2]["is_error"])
}

// The init line names the model the run uses. It is written at the run's
// first message, after a resumed session's model was restored; written
// when the session became known, it named the default model instead.
// Its lists are lists even when empty, as Claude Code's are.
func TestStreamJSONInitNamesTheModelInUse(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	o, err := newRunOutput("stream-json", &out)
	require.NoError(t, err)
	model := "default-model"
	o.started("s1", func() streamInit {
		return streamInit{SessionID: "s1", Model: model, PermissionMode: config.PermissionModePlan, Tools: []string{"glob", "view"}}
	})
	model = "session-model" // restored from the session
	o.message(assistantMsg("a1", "s1", message.TextContent{Text: "done"}, message.Finish{Reason: message.FinishReasonEndTurn}))
	require.NoError(t, o.finish(nil))

	lines := streamLines(t, out.String())
	require.Equal(t, "session-model", lines[0]["model"])
	require.Equal(t, "plan", lines[0]["permissionMode"])
	require.Equal(t, []any{"glob", "view"}, lines[0]["tools"])
	require.Equal(t, []any{}, lines[0]["mcp_servers"])
}

// A run that fails after its session is known but before any message
// still begins with the init line, then the result.
func TestStreamJSONInitBeforeAnEarlyFailure(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	o, err := newRunOutput("stream-json", &out)
	require.NoError(t, err)
	o.started("s1", fixedInit("s1", "/work", "m"))
	require.Error(t, o.finish(errors.New("reasoning effort not supported")))

	lines := streamLines(t, out.String())
	require.Len(t, lines, 2)
	require.Equal(t, "system", lines[0]["type"])
	require.Equal(t, "result", lines[1]["type"])
	require.Equal(t, "s1", lines[1]["session_id"])
}

func TestToolInput(t *testing.T) {
	t.Parallel()

	require.JSONEq(t, `{}`, string(toolInput("")))
	require.JSONEq(t, `{"path":"a.go"}`, string(toolInput(`{"path":"a.go"}`)))
	require.JSONEq(t, `"{not json"`, string(toolInput("{not json")))
}

// json's result is the final answer, as in Claude Code, not every step's
// text run together, and a late update of an earlier message does not
// take it back.
func TestJSONResultIsTheFinalAnswer(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	o, err := newRunOutput("json", &out)
	require.NoError(t, err)
	o.started("s1", fixedInit("s1", "/work", "m"))
	o.message(assistantMsg("a1", "s1", message.TextContent{Text: "Let me look."}))
	o.message(assistantMsg("a2", "s1", message.TextContent{Text: "Two files."}))
	o.message(assistantMsg("a1", "s1", message.TextContent{Text: "Let me look."}, message.Finish{Reason: message.FinishReasonToolUse}))
	_, _ = o.w.Write([]byte("Let me look.\n\nTwo files."))
	require.NoError(t, o.finish(nil))

	var res map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &res))
	require.Equal(t, "Two files.", res["result"])
}
