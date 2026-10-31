package agent

import (
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// Messages of a step's list, for cutPopped.
func sys(text string) fantasy.Message  { return fantasy.NewSystemMessage(text) }
func user(text string) fantasy.Message { return fantasy.NewUserMessage(text) }
func assistant(calls ...string) fantasy.Message {
	var parts []fantasy.MessagePart
	for _, id := range calls {
		parts = append(parts, fantasy.ToolCallPart{ToolCallID: id, ToolName: "gnovm_run", Input: "{}"})
	}
	if len(parts) == 0 {
		parts = append(parts, fantasy.TextPart{Text: "working"})
	}
	return fantasy.Message{Role: fantasy.MessageRoleAssistant, Content: parts}
}

func toolMsg(calls ...string) fantasy.Message {
	var parts []fantasy.MessagePart
	for _, id := range calls {
		parts = append(parts, fantasy.ToolResultPart{ToolCallID: id, Output: fantasy.ToolResultOutputContentText{Text: "ok"}})
	}
	return fantasy.Message{Role: fantasy.MessageRoleTool, Content: parts}
}

// texts is a list as a test reads it: each message's role and its first
// text or call id.
func texts(msgs []fantasy.Message) []string {
	var out []string
	for _, m := range msgs {
		label := string(m.Role)
		for _, part := range m.Content {
			switch p := part.(type) {
			case fantasy.TextPart:
				label += ":" + strings.SplitN(p.Text, "\n", 2)[0]
			case fantasy.ToolCallPart:
				label += ":" + p.ToolCallID
			case fantasy.ToolResultPart:
				label += ":" + p.ToolCallID
			}
			break
		}
		out = append(out, label)
	}
	return out
}

// validSequence checks what every provider needs of a list: an assistant
// message with tool calls is followed by the tool message answering them,
// and nothing else sits between.
func validSequence(t *testing.T, msgs []fantasy.Message) {
	t.Helper()
	for i, m := range msgs {
		if m.Role != fantasy.MessageRoleAssistant {
			continue
		}
		calls := false
		for _, part := range m.Content {
			if _, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part); ok {
				calls = true
			}
		}
		if calls {
			require.Less(t, i+1, len(msgs), "a call without its result at the end")
			require.Equal(t, fantasy.MessageRoleTool, msgs[i+1].Role, "a call must be followed by its results")
		}
	}
}

// A pop removes everything between the push's round and the pop's: the
// work, the folded prompt, nested pushes and pops; what is left is valid
// for every provider.
func TestCutPopped(t *testing.T) {
	t.Parallel()

	list := []fantasy.Message{
		sys("system"), user("fix it"),
		assistant("c1"), toolMsg("c1"), // push g
		assistant("c2"), toolMsg("c2"), // push g/s1
		assistant("v1"), toolMsg("v1"), // a view inside
		user("8080"),                   // a prompt folded in
		assistant("c3"), toolMsg("c3"), // pop g/s1, push g/s2
		assistant("v2"), toolMsg("v2"), // work in g/s2
		assistant("c4"), toolMsg("c4"), // pop g/s2
		assistant("c5"), toolMsg("c5"), // pop g
		assistant(), // carries on
	}
	validSequence(t, list)

	t.Run("one pop", func(t *testing.T) {
		t.Parallel()
		got := cutPopped(list, []poppedRange{{frame: "g/s1", pushCallID: "c2", popCallID: "c3"}})
		require.Equal(t, []string{
			"system:system", "user:fix it", "assistant:c1", "tool:c1", "assistant:c2", "tool:c2", "assistant:c3", "tool:c3",
			"assistant:v2", "tool:v2", "assistant:c4", "tool:c4", "assistant:c5", "tool:c5", "assistant:working",
		}, texts(got))
		validSequence(t, got)
	})
	t.Run("nested pops remove the union, the inner push and pop rounds too", func(t *testing.T) {
		t.Parallel()
		got := cutPopped(list, []poppedRange{
			{frame: "g/s1", pushCallID: "c2", popCallID: "c3"},
			{frame: "g/s2", pushCallID: "c3", popCallID: "c4"},
			{frame: "g", pushCallID: "c1", popCallID: "c5"},
		})
		require.Equal(t, []string{"system:system", "user:fix it", "assistant:c1", "tool:c1", "assistant:c5", "tool:c5", "assistant:working"}, texts(got))
		validSequence(t, got)
	})
	t.Run("nothing between means nothing removed", func(t *testing.T) {
		t.Parallel()
		got := cutPopped(list, []poppedRange{{frame: "g/s2", pushCallID: "c3", popCallID: "c4"}})
		require.Len(t, got, len(list)-2)
		require.NotContains(t, texts(got), "assistant:v2")
		adjacent := cutPopped(list, []poppedRange{{frame: "x", pushCallID: "c4", popCallID: "c5"}})
		require.Equal(t, texts(list), texts(adjacent))
	})
	t.Run("a push older than the summary removes everything from the first assistant message", func(t *testing.T) {
		t.Parallel()
		after := []fantasy.Message{
			sys("system"), user("summary"), user("given"), user("continue"),
			assistant("v3"), toolMsg("v3"),
			user("a prompt"),
			assistant("c9"), toolMsg("c9"), // the pop
			assistant(),
		}
		got := cutPopped(after, []poppedRange{{frame: "g", pushCallID: "c-older", popCallID: "c9"}})
		require.Equal(t, []string{"system:system", "user:summary", "user:given", "user:continue", "assistant:c9", "tool:c9", "assistant:working"}, texts(got))
		validSequence(t, got)
	})
	t.Run("a pop not in the list, or in the push's own round, cuts nothing", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, texts(list), texts(cutPopped(list, []poppedRange{{frame: "g", pushCallID: "c1", popCallID: "c-none"}})))
		require.Equal(t, texts(list), texts(cutPopped(list, []poppedRange{{frame: "g", pushCallID: "c3", popCallID: "c3"}})))
		require.Equal(t, texts(list), texts(cutPopped(list, nil)))
	})
}

// Rows of a session, for poppedRows.
func row(role message.MessageRole, id string, parts ...message.ContentPart) message.Message {
	return message.Message{ID: id, Role: role, Parts: parts}
}

func callPart(id string) message.ToolCall {
	return message.ToolCall{ID: id, Name: "gnovm_run", Input: "{}", Finished: true}
}

func resultPart(id string) message.ToolResult {
	return message.ToolResult{ToolCallID: id, Name: "gnovm_run", Content: "ok"}
}

func rowIDs(rows []message.Message) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// The rows a pop marks are those after the push's whole round, its
// sibling results included, and before the pop's assistant row; a summary
// in between stays with the prompts that continue it.
func TestPoppedRows(t *testing.T) {
	t.Parallel()

	rows := []message.Message{
		row(message.User, "u0", message.TextContent{Text: "fix it"}),
		row(message.Assistant, "a1", callPart("c1"), callPart("s1")), // push with a sibling call
		row(message.Tool, "t1", resultPart("c1")),
		row(message.Tool, "t1s", resultPart("s1")), // the sibling's result lands after the push's
		row(message.User, "u1", message.TextContent{Text: "8080"}),
		row(message.Assistant, "a2", callPart("v1")),
		row(message.Tool, "t2", resultPart("v1")),
		row(message.Assistant, "a3", callPart("c2")), // the pop
		row(message.Tool, "t3", resultPart("c2")),
	}
	require.Equal(t, []string{"u1", "a2", "t2"}, rowIDs(poppedRows(rows, "c1", "c2")))
	require.Empty(t, poppedRows(rows, "c1", "c-none"), "no pop row, nothing")
	require.Empty(t, poppedRows(rows, "c2", "c2"), "push and pop in one round")

	withSummary := []message.Message{
		row(message.Assistant, "a1", callPart("c1")),
		row(message.Tool, "t1", resultPart("c1")),
		row(message.Assistant, "a2", callPart("v1")),
		row(message.Tool, "t2", resultPart("v1")),
		{ID: "sum", Role: message.Assistant, IsSummaryMessage: true, Parts: []message.ContentPart{message.TextContent{Text: "summary"}}},
		row(message.User, "given", message.TextContent{Text: "files", Hidden: true}),
		row(message.User, "cont", message.TextContent{Text: "continue", Hidden: true}),
		row(message.Assistant, "a3", callPart("v2")),
		row(message.Tool, "t3", resultPart("v2")),
		row(message.Assistant, "a4", callPart("c2")),
	}
	require.Equal(t, []string{"a2", "t2", "a3", "t3"}, rowIDs(poppedRows(withSummary, "c1", "c2")), "the summary and its continuation stay")
	require.Equal(t, []string{"a3", "t3"}, rowIDs(poppedRows(withSummary, "", "c2")), "a push not found: from the first assistant row after the summary")
}

func TestPopClauses(t *testing.T) {
	t.Parallel()

	ev := gnovm.MemFrame{Kind: "pop", Id: "fix-flaky/find-cause", Result: "races", Session: "s"}
	require.Equal(t, "Everything since you entered `fix-flaky/find-cause` was removed from your context: this result stays; the user still sees all of it.",
		popClauses(ev, nil, nil, "/w", false))

	ev.Thoughts = 1
	require.Contains(t, popClauses(ev, nil, nil, "/w", true), "this result stays, and its 1 thought is in mem (mem.Show(\"fix-flaky/find-cause\") lists it when you need it); the user still sees all of it. Your reads inside it are forgotten: view a file before you edit it.")

	ev.Thoughts = 2
	rows := []message.Message{
		row(message.Assistant, "a1",
			message.ToolCall{ID: "e1", Name: tools.EditToolName, Input: `{"file_path":"/w/internal/net/listen_test.go","old_string":"a","new_string":"b"}`, Finished: true},
			message.ToolCall{ID: "m1", Name: "gnovm_run", Input: `{"name":"mem","code":"mem.Fact(\"port\", \"8080\")\nmem.Think(\"port\", \"8080\")\nmem.Show()\nmem.Todo(\"a\")\nmem.Note(\"fix-flaky\", \"n\")\nmem.Rule(\"r\", \"x\")\nmem.Forget(\"old\")\nmem.Pop(\"x\")"}`, Finished: true},
		),
		row(message.Tool, "t1",
			message.ToolResult{ToolCallID: "e1", Name: tools.EditToolName, Content: "edited", Metadata: `{"old_content":"a","new_content":"b"}`},
			message.ToolResult{ToolCallID: "m1", Name: "gnovm_run", Content: "ok"},
		),
	}
	calls := map[string]message.ToolCall{}
	for _, call := range rows[0].ToolCalls() {
		calls[call.ID] = call
	}
	got := popClauses(ev, rows, calls, "/w", false)
	require.Contains(t, got, "its 2 thoughts are in mem (mem.Show(\"fix-flaky/find-cause\") lists them when you need them)")
	require.Contains(t, got, " In it you edited internal/net/listen_test.go.")
	require.Contains(t, got, " In it you also changed mem: Fact `port`, Todo `a`, Note `fix-flaky`, Rule `r` and 1 more.")
	require.NotContains(t, got, "Think")
	require.NotContains(t, got, "reads inside it")
}

func TestFrameLine(t *testing.T) {
	t.Parallel()

	one := frameLine([]stackFrame{{id: "fix-flaky", purpose: "fix the flaky test"}})
	require.True(t, strings.HasPrefix(one, "You are in `fix-flaky` \"fix the flaky test\": pop it with its result when it is done; everything until then is removed from your context when you pop it, what the user says included, so keep what matters as a thought. Other work the user asks for meanwhile goes in a frame of its own: mem.Todo(\"rename the port flag\").Push(), popped when done."), one)
	two := frameLine([]stackFrame{{id: "fix-flaky", purpose: "fix the flaky test"}, {id: "fix-flaky/find-cause", purpose: "find why TestFlaky fails"}})
	require.Contains(t, two, "You are in `fix-flaky/find-cause` \"find why TestFlaky fails\" (your stack: `fix-flaky`, then `fix-flaky/find-cause`): pop it")
	require.Equal(t, "You are in `fix-flaky/find-cause` \"find why TestFlaky fails\": pop it with its result when it is done; everything until then is removed from your context when you pop it, what the user says included, so keep what matters as a thought.",
		frameReminder(stackFrame{id: "fix-flaky/find-cause", purpose: "find why TestFlaky fails"}))
	require.Empty(t, frameLine(nil))
}

func TestRoughCount(t *testing.T) {
	t.Parallel()
	require.Equal(t, "850", roughCount(850))
	require.Equal(t, "44K", roughCount(44_400))
	require.Equal(t, "45K", roughCount(44_600))
	require.Equal(t, "1.2M", roughCount(1_240_000))
}

// A frame's step is called large once, when the session has grown past
// half the compaction point since the push.
func TestLargeLine(t *testing.T) {
	t.Parallel()
	f := &frameStacks{records: map[string]*sessionFrames{"s": {stack: []*stackFrame{{id: "g/s1", purpose: "p", tokens: 1_000}}}}}
	require.Empty(t, f.largeLine(t.Context(), "s", 50_000, 100_000), "not yet")
	line := f.largeLine(t.Context(), "s", 51_001, 100_000)
	require.Contains(t, line, largeStep)
	require.Contains(t, line, "you are in `g/s1`")
	require.Empty(t, f.largeLine(t.Context(), "s", 90_000, 100_000), "given once")
	require.Empty(t, (*frameStacks)(nil).largeLine(t.Context(), "s", 90_000, 100_000))
}
