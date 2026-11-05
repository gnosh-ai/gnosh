package model

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/agent"
	gnovmtools "github.com/charmbracelet/crush/internal/gnovm/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// callRow is an assistant row making calls, at Unix second at.
func callRow(id string, at int64, calls ...message.ToolCall) message.Message {
	parts := make([]message.ContentPart, 0, len(calls))
	for _, c := range calls {
		parts = append(parts, c)
	}
	return message.Message{ID: id, SessionID: "s1", Role: message.Assistant, CreatedAt: at, Parts: parts}
}

func toolCall(id, name, input string) message.ToolCall {
	return message.ToolCall{ID: id, Name: name, Input: input, Finished: true}
}

func memCall(id string) message.ToolCall {
	return toolCall(id, gnovmtools.RunToolName, `{"code":"mem"}`)
}

// textRow is a row of size bytes of text: about size/4 tokens.
func textRow(id string, role message.MessageRole, at int64, size int) message.Message {
	return message.Message{ID: id, SessionID: "s1", Role: role, CreatedAt: at, Parts: []message.ContentPart{message.TextContent{Text: strings.Repeat("x", size)}}}
}

func viewResult(callID string) message.ToolResult {
	return message.ToolResult{ToolCallID: callID, Name: "view", Content: strings.Repeat("x", 4000)}
}

func editResult(callID string) message.ToolResult {
	return message.ToolResult{ToolCallID: callID, Name: "edit", Content: "edited", Metadata: `{"old_content":"a","new_content":"b"}`}
}

// flakySession is a session entering fix-flaky-test, reading two files and
// editing one in it, then leaving it, as its rows come: each call's round
// is the assistant row making it and the tool row answering it.
func flakySession(t *testing.T) []message.Message {
	t.Helper()
	return []message.Message{
		textRow("u0", message.User, 1, 400),
		callRow("a1", 2, memCall("c1")),
		toolRow("t1", 3, memResult(t, "c1", push("fix-flaky-test", "fix the flaky test"))),
		callRow("a2", 4, toolCall("v1", "view", `{"file_path":"/w/listen_test.go"}`), toolCall("v2", "view", `{"file_path":"/w/serve_test.go"}`)),
		toolRow("t2", 5, viewResult("v1"), viewResult("v2")),
		callRow("a3", 6, toolCall("e1", "edit", `{"file_path":"/w/listen_test.go"}`)),
		toolRow("t3", 7, editResult("e1")),
		callRow("a4", 8, memCall("c2")),
		toolRow("t4", 9, memResult(t, "c2", popWith("fix-flaky-test", "fixed"))),
	}
}

func rowIDsOf(rows []message.Message) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func TestSessionFrames_KeepsTheRowsInOrder(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", flakySession(t)[:3])
	f.observe("s1", textRow("a9", message.Assistant, 10, 4), time.Time{})
	grown := textRow("u0", message.User, 1, 800)
	f.observe("s1", grown, time.Time{})

	require.Equal(t, []string{"u0", "a1", "t1", "a9"}, rowIDsOf(f.rows))
	require.Equal(t, grown, f.rows[0], "an update replaces the row where it stands")

	f.forget("a1")
	f.observe("s1", textRow("a10", message.Assistant, 11, 4), time.Time{})
	f.observe("s1", textRow("a9", message.Assistant, 10, 8), time.Time{})
	require.Equal(t, []string{"u0", "t1", "a9", "a10"}, rowIDsOf(f.rows), "a deleted row leaves its place, the rest keep theirs")
}

func TestFrameTree_EntriesKnowTheirCallsAndTimes(t *testing.T) {
	t.Parallel()

	roots, _ := deriveFrames("s1", flakySession(t)).tree()

	n := roots[0]
	require.Equal(t, "c1", n.pushCall)
	require.Equal(t, "c2", n.popCall)
	require.Equal(t, int64(3), n.since)
	require.Equal(t, int64(9), n.until)

	again, _ := deriveFrames("s1", []message.Message{
		toolRow("m1", 1, memResult(t, "c1", push("a", "a"), pop("a"), push("a", "a again"))),
	}).tree()
	require.Len(t, again, 2)
	require.NotEqual(t, again[0].key, again[1].key, "a frame entered twice is two entries")
}

func TestFrameRows_AreWhatItsPopTookOrWouldTake(t *testing.T) {
	t.Parallel()

	rows := flakySession(t)
	done := deriveFrames("s1", rows)
	roots, _ := done.tree()
	require.Equal(t, []string{"a2", "t2", "a3", "t3"}, rowIDsOf(done.frameRows(roots[0])))

	open := deriveFrames("s1", rows[:7])
	roots, _ = open.tree()
	require.Equal(t, []string{"a2", "t2", "a3", "t3"}, rowIDsOf(open.frameRows(roots[0])), "open, up to the last row")
}

// A parent's pop marks its children's rows as its own; each frame still
// holds what its own pop took out.
func TestHeldRows_AreWhatItsOwnPopTookOrAPopNowWould(t *testing.T) {
	t.Parallel()

	rows := []message.Message{
		callRow("a1", 1, memCall("c1")),
		toolRow("t1", 2, memResult(t, "c1", push("p", "p"))),
		callRow("a2", 3, memCall("c2")),
		toolRow("t2", 4, memResult(t, "c2", push("p/c", "c"))),
		textRow("x1", message.Assistant, 5, 4000),
		callRow("a3", 6, memCall("c3")),
		toolRow("t3", 7, memResult(t, "c3", pop("p/c"))),
		textRow("x2", message.Assistant, 8, 400),
		callRow("a4", 9, memCall("c4")),
		toolRow("t4", 10, memResult(t, "c4", pop("p"))),
	}
	for i := 2; i < 8; i++ {
		rows[i].Popped = "p"
	}
	rows[7].Popped = "slide:p:c1:c4"

	done := deriveFrames("s1", rows)
	roots, _ := done.tree()
	require.Equal(t, []string{"x1"}, rowIDsOf(done.heldRows(roots[0].children[0])), "the child's own pop took x1, whatever mark it bears now")
	require.Equal(t, []string{"a2", "t2", "x1", "a3", "t3"}, rowIDsOf(done.heldRows(roots[0])), "a slide's rows are not the pop's")

	open := rows[:8]
	for i := range open {
		open[i].Popped = ""
	}
	open[4].Popped = "p/c"
	running := deriveFrames("s1", open)
	roots, _ = running.tree()
	require.Equal(t, []string{"a2", "t2", "a3", "t3", "x2"}, rowIDsOf(running.heldRows(roots[0])), "open: what is still sent, which a pop now would take")
}

func TestFrameWork_CountsItsToolCallsMostUsedFirst(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", flakySession(t))
	roots, _ := f.tree()

	require.Equal(t, []toolUse{{name: "view", n: 2}, {name: "edit", n: 1}}, frameWork(f.frameRows(roots[0])))
}

func TestFrameEdits_AreTheFilesItsCallsChanged(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", flakySession(t))
	roots, _ := f.tree()

	require.Equal(t, []string{"listen_test.go"}, agent.EditedFiles(f.frameRows(roots[0]), "/w"))
}

func TestContextSeries_RisesWithEachRowAndDropsAtAPop(t *testing.T) {
	t.Parallel()

	rows := flakySession(t)
	f := deriveFrames("s1", rows)

	series := f.contextSeries()

	require.Len(t, series, len(rows), "a point per row")
	var held int64
	for _, r := range rows[3:7] {
		held += f.rowTokens(r)
	}
	before, after := series[len(series)-2], series[len(series)-1]
	require.Equal(t, before.tokens+f.rowTokens(rows[8])-held, after.tokens, "the pop takes out what the frame held")
	require.Equal(t, int64(9), after.at)
	for i := 1; i < len(series)-1; i++ {
		require.Greater(t, series[i].tokens, series[i-1].tokens, "every row before the pop adds")
	}
}

func TestContextSeries_ASlideTakesItsRowsOutAfterTheLastOfThem(t *testing.T) {
	t.Parallel()

	slid := func(id string, at int64) message.Message {
		r := textRow(id, message.Assistant, at, 4000)
		r.Popped = "slide:x:c1:c2"
		return r
	}
	f := deriveFrames("s1", []message.Message{
		textRow("u0", message.User, 1, 400),
		slid("a1", 2),
		slid("a2", 3),
		textRow("a3", message.Assistant, 4, 40),
	})

	series := f.contextSeries()

	require.Equal(t, int64(2107), series[2].tokens, "both slid rows were sent until the slide")
	require.Equal(t, int64(114), series[3].tokens, "then only the prompt and what came after")
}

func TestContextSeries_StartsAgainAtASummary(t *testing.T) {
	t.Parallel()

	summary := textRow("sum", message.Assistant, 3, 800)
	summary.IsSummaryMessage = true
	f := deriveFrames("s1", []message.Message{
		textRow("u0", message.User, 1, 4000),
		textRow("a1", message.Assistant, 2, 4000),
		summary,
		textRow("u1", message.User, 4, 40),
	})

	series := f.contextSeries()

	require.Equal(t, int64(203), series[2].tokens, "the summary alone")
	require.Equal(t, int64(214), series[3].tokens)
}
