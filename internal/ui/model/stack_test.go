package model

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/gnovm"
	gnovmtools "github.com/charmbracelet/crush/internal/gnovm/tools"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

func push(id, purpose string) gnovm.MemFrame {
	return gnovm.MemFrame{Kind: "push", Id: id, Purpose: purpose, Session: "s1"}
}

func pop(id string) gnovm.MemFrame {
	return gnovm.MemFrame{Kind: "pop", Id: id, Session: "s1"}
}

func popWith(id, result string) gnovm.MemFrame {
	return gnovm.MemFrame{Kind: "pop", Id: id, Result: result, Session: "s1"}
}

// memResult is a gnovm_run result whose metadata carries frames, as the
// tool writes it.
func memResult(t *testing.T, callID string, frames ...gnovm.MemFrame) message.ToolResult {
	t.Helper()
	meta, err := json.Marshal(gnovmtools.RunResponseMetadata{Frames: frames})
	require.NoError(t, err)
	return message.ToolResult{ToolCallID: callID, Name: gnovmtools.RunToolName, Content: "ok", Metadata: string(meta)}
}

func toolRow(id string, at int64, results ...message.ToolResult) message.Message {
	parts := make([]message.ContentPart, 0, len(results))
	for _, r := range results {
		parts = append(parts, r)
	}
	return message.Message{ID: id, SessionID: "s1", Role: message.Tool, CreatedAt: at, Parts: parts}
}

// cutMsg is a reply of size bytes that the pop or slide mark took out of
// the model's context: about size/4 tokens.
func cutMsg(id, mark string, size int) message.Message {
	return message.Message{
		ID: id, SessionID: "s1", Role: message.Assistant, Popped: mark,
		Parts: []message.ContentPart{message.TextContent{Text: strings.Repeat("x", size)}},
	}
}

func openFrames(f sessionFrames) []*frameNode {
	_, stack := f.tree()
	return stack
}

func stackIDs(f sessionFrames) []string {
	var ids []string
	for _, fr := range openFrames(f) {
		ids = append(ids, fr.id)
	}
	return ids
}

// stackSection is the sidebar's Stack section of a session without todos.
func stackSection(t *styles.Styles, f sessionFrames, width int, look stackLook) string {
	view, _ := stackSectionLines(t, f, nil, width, look)
	return view
}

// stackColumn is compact mode's Stack column without its keys.
func stackColumn(t *styles.Styles, f sessionFrames, width, height int, look stackLook) string {
	view, _ := stackColumnLines(t, f, width, height, look)
	return view
}

func TestDeriveFrames_TheStackIsThePushesNotYetPopped(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", []message.Message{
		toolRow("m1", 10, memResult(t, "c1", push("release", "release v2"))),
		toolRow("m2", 20, memResult(t, "c2", push("release/bump", "bump the version"))),
		toolRow("m3", 30, memResult(t, "c3", pop("release/bump"), push("release/tag", "tag the release"))),
	})

	require.Equal(t, []string{"release", "release/tag"}, stackIDs(f))
	top := openFrames(f)[1]
	require.Equal(t, "tag the release", top.purpose)
	require.Equal(t, int64(30), top.since)
}

func TestDeriveFrames_APopTakesItsOwnFrameOff(t *testing.T) {
	t.Parallel()

	// A pop names its frame: it takes that one off even when it is not
	// on top, as the agent's own stack does.
	f := deriveFrames("s1", []message.Message{
		toolRow("m1", 10, memResult(t, "c1", push("a", "a"), push("a/b", "b"))),
		toolRow("m2", 20, memResult(t, "c2", pop("a"))),
	})
	require.Equal(t, []string{"a/b"}, stackIDs(f))
}

func TestDeriveFrames_KeepsOnlyTheSessionsOwnSuccessfulMemCalls(t *testing.T) {
	t.Parallel()

	copied := push("theirs", "a frame of the session this one was branched from")
	copied.Session = "s0"
	failed := memResult(t, "c3", push("failed", "a call that failed"))
	failed.IsError = true
	f := deriveFrames("s1", []message.Message{
		toolRow("m1", 10, memResult(t, "c1", copied)),
		toolRow("m2", 20, message.ToolResult{ToolCallID: "c2", Name: "bash", Metadata: `{"frames":[{"kind":"push","id":"x","session":"s1"}]}`}),
		toolRow("m3", 30, failed),
		toolRow("m4", 40, memResult(t, "c4", push("mine", "mine"))),
	})
	require.Equal(t, []string{"mine"}, stackIDs(f))
}

func TestSessionFrames_ObserveIsIdempotent(t *testing.T) {
	t.Parallel()

	// A row is published again on every update: its push counts once.
	var f sessionFrames
	row := toolRow("m1", 10, memResult(t, "c1", push("a", "a")))
	require.True(t, f.observe("s1", row, time.Time{}))
	require.False(t, f.observe("s1", row, time.Time{}))
	require.Equal(t, []string{"a"}, stackIDs(f))
}

func TestSessionFrames_ForgetDropsADeletedRowsFrames(t *testing.T) {
	t.Parallel()

	// /rewind deletes the rows after the point it goes back to.
	f := deriveFrames("s1", []message.Message{
		toolRow("m1", 10, memResult(t, "c1", push("a", "a"))),
		toolRow("m2", 20, memResult(t, "c2", push("a/b", "b"))),
	})
	require.True(t, f.forget("m2"))
	require.False(t, f.forget("m2"))
	require.Equal(t, []string{"a"}, stackIDs(f))
}

func TestFrameTree_FramesNestAsTheyWereEntered(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", []message.Message{
		toolRow("m1", 10, memResult(t, "c1", push("a", "task a"), push("a/b", "step b"))),
		toolRow("m2", 20, memResult(t, "c2", popWith("a/b", "b is done"), push("a/c", "step c"))),
		toolRow("m3", 30, memResult(t, "c3", push("a/c/d", "step d"))),
	})

	roots, stack := f.tree()
	require.Len(t, roots, 1)
	a := roots[0]
	require.Len(t, a.children, 2)
	b, c := a.children[0], a.children[1]
	require.True(t, b.popped)
	require.Equal(t, "b is done", b.result)
	require.False(t, c.popped)
	require.Len(t, c.children, 1)
	require.Equal(t, "a/c/d", c.children[0].id)
	require.Equal(t, []*frameNode{a, c, c.children[0]}, stack)
}

func TestFrameTree_EnteringAFrameAgainIsANewFrame(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", []message.Message{
		toolRow("m1", 10, memResult(t, "c1", push("a", "a"))),
		toolRow("m2", 20, memResult(t, "c2", popWith("a", "first"), push("a", "a"))),
	})

	roots, stack := f.tree()
	require.Len(t, roots, 2)
	require.True(t, roots[0].popped)
	require.False(t, roots[1].popped)
	require.Equal(t, []*frameNode{roots[1]}, stack)
}

func TestFrameTree_EachFrameKeepsWhatItsPopFreed(t *testing.T) {
	t.Parallel()

	inB := cutMsg("m2", "a/b", 400)
	f := deriveFrames("s1", []message.Message{
		callRow("e1", 10, memCall("c1")),
		toolRow("m1", 10, memResult(t, "c1", push("a", "a"), push("a/b", "b"))),
		inB,
		callRow("e3", 30, memCall("c3")),
		toolRow("m3", 30, memResult(t, "c3", popWith("a/b", "done"))),
		cutMsg("m4", "slide::c1:c3", 40),
	})
	freed := func(n *frameNode) int64 { return f.tokensOf(f.heldRows(n)) }

	roots, _ := f.tree()
	require.Equal(t, int64(103), freed(roots[0].children[0]))
	rows, tokens := f.slid()
	require.Equal(t, 1, rows)
	require.Equal(t, int64(13), tokens)

	// The parent's pop marks the child's rows as its own: the child keeps
	// what its own pop took out, and the parent's figure holds it.
	f.observe("s1", callRow("e5", 50, memCall("c5")), time.Time{})
	f.observe("s1", toolRow("m5", 50, memResult(t, "c5", popWith("a", "all done"))), time.Time{})
	inB.Popped = "a"
	require.True(t, f.observe("s1", inB, time.Time{}))
	roots, _ = f.tree()
	require.Equal(t, int64(103), freed(roots[0].children[0]))
	require.Greater(t, freed(roots[0]), int64(103), "a frame's figure holds its children's")
}

// A session working release-v2: two steps done, the third under way.
func releaseFrames(t *testing.T) sessionFrames {
	t.Helper()
	return deriveFrames("s1", releaseRows(t))
}

// releaseRows is release-v2's rows as they come: each call's assistant
// row, then the tool row answering it.
func releaseRows(t *testing.T) []message.Message {
	t.Helper()
	return []message.Message{
		callRow("e1", 1_700_000_000, memCall("c1")),
		toolRow("m1", 1_700_000_000, memResult(t, "c1", push("release-v2", "release v2"))),
		callRow("e2", 1_700_000_010, memCall("c2")),
		toolRow("m2", 1_700_000_010, memResult(t, "c2", push("release-v2/bump-version", "bump the version"))),
		cutMsg("m3", "release-v2/bump-version", 4000),
		callRow("e4", 1_700_000_030, memCall("c4")),
		toolRow("m4", 1_700_000_030, memResult(t, "c4", popWith("release-v2/bump-version", "VERSION says 2.0.0"), push("release-v2/tag-release", "tag the release"))),
		cutMsg("m5", "release-v2/tag-release", 400),
		callRow("e6", 1_700_000_050, memCall("c6")),
		toolRow("m6", 1_700_000_050, memResult(t, "c6", popWith("release-v2/tag-release", "tagged v2.0.0"), push("release-v2/ci-green", "CI is green"))),
	}
}

func TestStackSection_ShowsFinishedFramesAndTheLastResult(t *testing.T) {
	t.Parallel()

	got := stripANSI(stackSection(minimalFileStyles(), releaseFrames(t), 30, stackLook{}))

	lines := []string{
		"▸ release-v2",
		"├ ✓ bump-version",
		"├ ✓ tag-release",
		"│   tagged v2.0.0",
		"└ ▸ ci-green",
		`"CI is green"`,
	}
	require.NotContains(t, got, "VERSION says 2.0.0", "only the last pop says its result")
	at := 0
	for _, want := range lines {
		i := strings.Index(got[at:], want)
		require.GreaterOrEqual(t, i, 0, "want %q after what came before, in:\n%s", want, got)
		at += i + len(want)
	}
	require.Contains(t, got, "−1K tok", "bump-version's pop freed about 1000 tokens")
	require.Contains(t, got, "−103 tok")
	require.Contains(t, got, time.Unix(1_700_000_050, 0).Format("15:04"), "the frame the agent is in says when it entered it")
	require.NotContains(t, got, "release-v2/bump-version", "a frame under its parent is named by its last part")
	require.NotContains(t, got, `"release v2"`, "only the frame the agent is in shows its purpose")
}

func TestStackSection_AFinishedTaskFoldsItsSteps(t *testing.T) {
	t.Parallel()

	f := releaseFrames(t)
	f.observe("s1", callRow("e7", 1_700_000_070, memCall("c7")), time.Time{})
	f.observe("s1", toolRow("m7", 1_700_000_070, memResult(t, "c7", popWith("release-v2/ci-green", "CI passed"), popWith("release-v2", "released 2.0.0"))), time.Time{})

	got := stripANSI(stackSection(minimalFileStyles(), f, 30, stackLook{}))

	require.Contains(t, got, "✓ release-v2 ●●●", "a dot a step")
	require.Contains(t, got, "released 2.0.0")
	require.Contains(t, got, "−1.1K tok", "the task's figure holds its steps'")
	require.NotContains(t, got, "bump-version")
	require.NotContains(t, got, "▸")
}

func TestStackSection_MarksTheFrameAtWork(t *testing.T) {
	t.Parallel()

	got := strings.Split(stripANSI(stackSection(minimalFileStyles(), releaseFrames(t), 30, stackLook{})), "\n")

	for _, line := range got[2:] {
		if strings.Contains(line, "ci-green") {
			require.True(t, strings.HasPrefix(line, "›└ ▸ ci-green"), line)
		} else {
			require.False(t, strings.HasPrefix(line, "›"), line)
		}
	}
}

func TestStackSection_CountsTheTodosAndSaysWhatComesNext(t *testing.T) {
	t.Parallel()

	todos := []session.Todo{
		{Content: "bump the version", Status: session.TodoStatusCompleted},
		{Content: "tag the release", Status: session.TodoStatusInProgress},
		{Content: "notes", Status: session.TodoStatusPending},
		{Content: "publish", Status: session.TodoStatusPending},
	}

	view, keys := stackSectionLines(minimalFileStyles(), releaseFrames(t), todos, 30, stackLook{})
	got := strings.Split(stripANSI(view), "\n")

	require.Contains(t, got[0], "Stack · 1/4")
	require.Contains(t, got[len(got)-1], "○ notes · ○ publish")
	require.Len(t, keys, len(got), "a key a line, for clicks")
}

func TestStackSection_TheHeadingSaysWhatLeftTheContextInAll(t *testing.T) {
	t.Parallel()

	f := releaseFrames(t)
	heading := func() string {
		got := stripANSI(stackSection(minimalFileStyles(), f, 30, stackLook{}))
		return got[:strings.Index(got, "\n")]
	}

	require.Contains(t, heading(), "1.1K tok freed", "both pops")
	f.observe("s1", cutMsg("m9", "slide::c1:c4", 4000), time.Time{})
	require.Contains(t, heading(), "2.1K tok freed", "and what slid out")

	fresh := deriveFrames("s1", []message.Message{toolRow("m1", 10, memResult(t, "c1", push("a", "a")))})
	got := stripANSI(stackSection(minimalFileStyles(), fresh, 30, stackLook{}))
	require.NotContains(t, got, "freed", "nothing has left the context yet")
}

// attachedPrompt is a prompt with a 40 KB text file attached, which the
// model is sent inside the prompt: about 10K tokens.
func attachedPrompt(id string, at int64) message.Message {
	return message.Message{ID: id, SessionID: "s1", Role: message.User, CreatedAt: at, Parts: []message.ContentPart{
		message.TextContent{Text: "look at this"},
		message.BinaryContent{Path: "notes.txt", MIMEType: "text/plain", Data: []byte(strings.Repeat("x", 40_000))},
	}}
}

func TestSidebar_APopFreesWhatAPromptsAttachmentsHeld(t *testing.T) {
	t.Parallel()

	u, _ := sidebarUI(t, 60)
	prompt := attachedPrompt("u1", 15)
	prompt.Popped = "fix-test"
	observeRows(u,
		callRow("a1", 10, memCall("c1")),
		toolRow("t1", 10, memResult(t, "c1", push("fix-test", "fix the test"))),
		prompt,
		callRow("a2", 20, memCall("c2")),
		toolRow("t2", 20, memResult(t, "c2", popWith("fix-test", "fixed"))),
	)

	require.GreaterOrEqual(t, rowShowing(u, drawSidebarOf(u), "10K tok freed"), 0, "the attachment counts, as the agent counts it")
}

// A frame entered twice is two entries, each with what its own pop took
// out, in the sidebar as in the explorer.
func TestStackSection_EachEntryFreesWhatItsOwnPopTookOut(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	u.sessionFrames = sessionFrames{}
	observeRows(u,
		callRow("a1", 10, memCall("c1")),
		toolRow("t1", 10, memResult(t, "c1", push("fix-test", "fix the test"))),
		cutMsg("w1", "fix-test", 40_000),
		callRow("a2", 20, memCall("c2")),
		toolRow("t2", 20, memResult(t, "c2", popWith("fix-test", "first try"))),
		callRow("a3", 30, memCall("c3")),
		toolRow("t3", 30, memResult(t, "c3", push("fix-test", "fix the test again"))),
		cutMsg("w2", "fix-test", 20_000),
		callRow("a4", 40, memCall("c4")),
		toolRow("t4", 40, memResult(t, "c4", popWith("fix-test", "second try"))),
	)
	for i := range u.sessionFrames.calls {
		u.sessionFrames.calls[i].seen = time.Time{}
	}

	scr := drawSidebarOf(u)
	first, second := rowShowing(u, scr, "−10K tok"), rowShowing(u, scr, "−5K tok")
	require.GreaterOrEqual(t, first, 0, "the first entry's pop took out 10K")
	require.Greater(t, second, first, "and the second's 5K, under it")

	press(u, ctrlX)
	tree := treeOf(u)
	require.Contains(t, lineWith(tree, "−10K tok"), "fix-test", "the explorer says the same")
	require.Contains(t, lineWith(tree, "−5K tok"), "fix-test")
}

func TestStackSection_KeepsTheLastThreeFinishedTasks(t *testing.T) {
	t.Parallel()

	var rows []message.Message
	for i := range 5 {
		id := fmt.Sprintf("task-%d", i+1)
		rows = append(rows, toolRow(fmt.Sprintf("m%d", i), int64(i), memResult(t, fmt.Sprintf("c%d", i), push(id, id), popWith(id, "done "+id))))
	}

	got := stripANSI(stackSection(minimalFileStyles(), deriveFrames("s1", rows), 30, stackLook{}))

	require.Contains(t, got, "…2 earlier")
	require.NotContains(t, got, "task-1")
	require.NotContains(t, got, "task-2")
	for _, id := range []string{"task-3", "task-4", "task-5"} {
		require.Contains(t, got, id)
	}
}

func TestStackSection_ShowsWhatSlidOut(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", []message.Message{
		toolRow("m1", 10, memResult(t, "c1", push("a", "a"))),
		cutMsg("m2", "slide::c1:c9", 8000),
	})

	got := stripANSI(stackSection(minimalFileStyles(), f, 30, stackLook{}))

	require.Contains(t, got, "⟲ slid out of context", "whole in a narrow sidebar")
	require.Contains(t, got, "1 msg")
	require.Contains(t, got, "2K tokens")

	view, _ := stackSectionLines(minimalFileStyles(), f, []session.Todo{{Content: "notes", Status: session.TodoStatusPending}}, 30, stackLook{})
	lines := strings.Split(stripANSI(view), "\n")
	require.Contains(t, lines[len(lines)-1], "○ notes")
	require.Empty(t, strings.TrimSpace(lines[len(lines)-2]), "set apart from what slid out")
}

func TestStackSection_FitsTheSidebar(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a long result that keeps going ", 4)
	f := deriveFrames("s1", []message.Message{
		toolRow("m1", 10, memResult(t, "c1", push("a-very-long-todo-name-that-overflows", strings.Repeat("a long purpose ", 6)))),
		toolRow("m2", 20, memResult(t, "c2", push("a-very-long-todo-name-that-overflows/an-equally-long-step-name", "step"))),
		cutMsg("m3", "a-very-long-todo-name-that-overflows/an-equally-long-step-name", 400_000),
		toolRow("m4", 30, memResult(t, "c4", popWith("a-very-long-todo-name-that-overflows/an-equally-long-step-name", long), push("a-very-long-todo-name-that-overflows/x", "x"))),
		toolRow("m5", 40, memResult(t, "c5", push("a-very-long-todo-name-that-overflows/x/y", "y"), push("a-very-long-todo-name-that-overflows/x/y/z", long))),
	})
	for _, width := range []int{20, 30} {
		for _, line := range strings.Split(stripANSI(stackSection(minimalFileStyles(), f, width, stackLook{})), "\n") {
			require.LessOrEqual(t, lipgloss.Width(line), width, "line exceeds a sidebar %d wide: %q", width, line)
		}
	}
}

func TestStackSection_TheFrameAtWorkShowsHowLongItHasRun(t *testing.T) {
	t.Parallel()

	f := releaseFrames(t)
	now := time.Unix(1_700_000_050+65, 0)

	working := stripANSI(stackSection(minimalFileStyles(), f, 30, stackLook{now: now, busy: true}))
	waiting := stripANSI(stackSection(minimalFileStyles(), f, 30, stackLook{now: now}))

	require.Contains(t, working, "1m05s")
	require.NotContains(t, waiting, "1m05s", "a waiting agent's clock would stand still")
	require.Contains(t, waiting, time.Unix(1_700_000_050, 0).Format("15:04"))
}

func TestStackSection_TheFrameAtWorkShimmers(t *testing.T) {
	t.Parallel()

	f := releaseFrames(t)
	at := time.Unix(1_700_000_100, 0)
	draw := func(busy bool, after time.Duration) string {
		return stackSection(minimalFileStyles(), f, 30, stackLook{now: at.Add(after), busy: busy})
	}

	require.NotEqual(t, draw(true, 0), draw(true, 240*time.Millisecond), "its colors move while the agent works")
	require.Equal(t, stripANSI(draw(true, 0)), stripANSI(draw(true, 240*time.Millisecond)), "and only its colors")
	require.Equal(t, draw(false, 0), draw(false, 240*time.Millisecond), "they stand still while the agent waits")
}

func TestStackSection_APopCountsUpWhatItFreed(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", []message.Message{
		callRow("e1", 10, memCall("c1")),
		toolRow("m1", 10, memResult(t, "c1", push("a", "a"), push("a/b", "b"))),
		cutMsg("m2", "a/b", 4000),
	})
	popped := time.Unix(1_700_000_200, 0)
	f.observe("s1", callRow("e3", 1_700_000_200, memCall("c3")), popped)
	f.observe("s1", toolRow("m3", 1_700_000_200, memResult(t, "c3", popWith("a/b", "done"))), popped)
	draw := func(after time.Duration) string {
		return stripANSI(stackSection(minimalFileStyles(), f, 30, stackLook{now: popped.Add(after)}))
	}

	require.Contains(t, draw(100*time.Millisecond), "✓ b")
	require.NotContains(t, draw(100*time.Millisecond), "−1K", "the count starts low")
	require.Contains(t, draw(2*time.Second), "−1K tok", "and ends at what the pop freed")
	require.True(t, f.animating(popped.Add(100*time.Millisecond), false))
	require.False(t, f.animating(popped.Add(2*time.Second), false))
}

func TestStackSection_StandsStillWhenMotionIsReduced(t *testing.T) {
	t.Parallel()

	f := releaseFrames(t)
	at := time.Unix(1_700_000_100, 0)
	working := func(after time.Duration) string {
		return stackSection(minimalFileStyles(), f, 30, stackLook{now: at.Add(after), busy: true, still: true})
	}
	require.Equal(t, working(0), working(240*time.Millisecond), "the frame at work does not shimmer")

	popped := time.Unix(1_700_000_200, 0)
	f.observe("s1", toolRow("m7", 1_700_000_200, memResult(t, "c7", popWith("release-v2/ci-green", "CI passed"))), popped)
	draw := func(after time.Duration) string {
		return stackSection(minimalFileStyles(), f, 30, stackLook{now: popped.Add(after), still: true})
	}
	require.Equal(t, draw(100*time.Millisecond), draw(2*time.Second), "a pop neither flashes nor counts up")
}

func TestReduceMotionIsATUIOption(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	require.False(t, u.stackLook().still)

	u.com.Workspace = stillWorkspace{u.com.Workspace.(*exportWorkspace)}
	require.True(t, u.stackLook().still)
}

// stillWorkspace is exportWorkspace with reduce_motion set.
type stillWorkspace struct{ *exportWorkspace }

func (stillWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{ReduceMotion: true}}}
}

func TestSessionFrames_AnimatesWhileTheAgentWorksInAFrame(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_100, 0)
	require.True(t, releaseFrames(t).animating(now, true))
	require.False(t, releaseFrames(t).animating(now, false), "a loaded pop does not flash")
	require.False(t, sessionFrames{}.animating(now, true), "nothing to animate outside a frame")
}

func TestStackSection_IsHiddenForASessionWithoutFrames(t *testing.T) {
	t.Parallel()

	require.Empty(t, stackSection(minimalFileStyles(), sessionFrames{}, 30, stackLook{}))
}

// The sidebar follows the agent's frames as their rows arrive, and drops
// the ones /rewind deletes.
func TestSidebarShowsTheStackLive(t *testing.T) {
	t.Parallel()

	// The section's heading, not the word, which the test's temporary
	// directory, shown as the working directory, also holds.
	const heading = "Stack ─"
	u, scr := sidebarUI(t, 60)
	require.Equal(t, -1, rowShowing(u, scr, heading), "no Stack section before a frame")

	send := func(typ pubsub.EventType, row message.Message) {
		t.Helper()
		_, _ = u.Update(pubsub.Event[message.Message]{Type: typ, Payload: row})
	}
	send(pubsub.CreatedEvent, toolRow("m1", 10, memResult(t, "c1", push("fix-flaky-test", "fix the flaky test"))))
	send(pubsub.CreatedEvent, toolRow("m2", 20, memResult(t, "c2", push("fix-flaky-test/find-cause", "find why TestFlaky fails"))))
	scr = drawSidebarOf(u)
	require.GreaterOrEqual(t, rowShowing(u, scr, heading), 0)
	require.GreaterOrEqual(t, rowShowing(u, scr, "find-cause"), 0)
	require.Less(t, rowShowing(u, scr, heading), rowShowing(u, scr, "Artifacts"), "the Stack sits above the Artifacts")

	send(pubsub.DeletedEvent, message.Message{ID: "m2", SessionID: "s1"})
	scr = drawSidebarOf(u)
	require.Equal(t, -1, rowShowing(u, scr, "find-cause"))
	require.GreaterOrEqual(t, rowShowing(u, scr, "fix-flaky-test"), 0)

	// A pop that just came keeps the animation clock running while it
	// flashes, the agent busy or not. The pending artifact would keep it
	// running on its own.
	u.sessionArtifacts = nil
	require.False(t, u.sidebarAnimating(time.Now()))
	send(pubsub.CreatedEvent, toolRow("m3", 30, memResult(t, "c3", popWith("fix-flaky-test", "fixed"))))
	require.True(t, u.sidebarAnimating(time.Now()))
}

// Three frames deep in fix-flaky-test, after a step that freed 2K tokens.
func flakyRows(t *testing.T) []message.Message {
	t.Helper()
	return []message.Message{
		toolRow("f1", 1_700_000_000, memResult(t, "fc1", push("fix-flaky-test", "fix the flaky test"))),
		toolRow("f2", 1_700_000_010, memResult(t, "fc2", push("fix-flaky-test/reproduce-it", "make it fail"))),
		cutMsg("f3", "fix-flaky-test/reproduce-it", 8000),
		toolRow("f4", 1_700_000_030, memResult(t, "fc4", popWith("fix-flaky-test/reproduce-it", "fails 3 times in 20"), push("fix-flaky-test/find-cause", "find why it fails"))),
		toolRow("f5", 1_700_000_040, memResult(t, "fc5", push("fix-flaky-test/find-cause/try-free-port", "listen on a free port"))),
	}
}

func TestStackTrail_ShowsThePathToTheFrameAtWork(t *testing.T) {
	t.Parallel()

	got := stripANSI(stackTrail(minimalFileStyles(), deriveFrames("s1", flakyRows(t)), 80, stackLook{}))

	require.Equal(t, 80, lipgloss.Width(got), "it fills its row")
	require.Contains(t, got, "▸ fix-flaky-test › find-cause › try-free-port")
	require.Contains(t, got, time.Unix(1_700_000_040, 0).Format("15:04"), "when the agent entered the frame it is in")
	require.True(t, strings.HasSuffix(got, "2K tok freed"), "the total ends the row: %q", got)
}

func TestStackTrail_FoldsTheMiddleFramesWhenNarrow(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", flakyRows(t))
	trail := func(width int) string {
		t.Helper()
		got := stripANSI(stackTrail(minimalFileStyles(), f, width, stackLook{}))
		require.LessOrEqual(t, lipgloss.Width(got), width, "the trail overflows %d columns: %q", width, got)
		return got
	}

	require.Contains(t, trail(60), "▸ fix-flaky-test › … › try-free-port")
	require.Contains(t, trail(60), "2K tok freed")
	require.Contains(t, trail(45), "▸ fix-flaky-test › … › try-free-port", "the frames go after the total")
	require.NotContains(t, trail(45), "tok freed")
	for width := 1; width <= 80; width++ {
		trail(width)
	}
}

func TestStackTrail_ShowsTheLastTaskOnceAllAreDone(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", releaseRows(t))
	f.observe("s1", toolRow("m7", 1_700_000_070, memResult(t, "c7", popWith("release-v2/ci-green", "CI passed"), popWith("release-v2", "released 2.0.0"))), time.Time{})

	got := stripANSI(stackTrail(minimalFileStyles(), f, 80, stackLook{}))

	require.Contains(t, got, "✓ release-v2  released 2.0.0")
	require.True(t, strings.HasSuffix(got, "1.1K tok freed"), "the total ends the row: %q", got)
}

func TestStackTrail_TheFrameAtWorkShowsHowLongItHasRun(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_040+65, 0)
	got := stripANSI(stackTrail(minimalFileStyles(), deriveFrames("s1", flakyRows(t)), 80, stackLook{now: now, busy: true}))
	require.Contains(t, got, "try-free-port  1m05s")
}

func TestStackTrail_TheFrameAtWorkShimmers(t *testing.T) {
	t.Parallel()

	f := deriveFrames("s1", flakyRows(t))
	at := time.Unix(1_700_000_100, 0)
	draw := func(busy bool, after time.Duration) string {
		return stackTrail(minimalFileStyles(), f, 80, stackLook{now: at.Add(after), busy: busy})
	}

	require.NotEqual(t, draw(true, 0), draw(true, 240*time.Millisecond), "its colors move while the agent works")
	require.Equal(t, stripANSI(draw(true, 0)), stripANSI(draw(true, 240*time.Millisecond)), "and only its colors")
	require.Equal(t, draw(false, 0), draw(false, 240*time.Millisecond), "they stand still while the agent waits")
}

func TestStackTrail_IsEmptyForASessionWithoutFrames(t *testing.T) {
	t.Parallel()

	require.Empty(t, stackTrail(minimalFileStyles(), sessionFrames{}, 80, stackLook{}))
}

// Three finished tasks, then release-v2 under way, then a slide.
func longSessionFrames(t *testing.T) sessionFrames {
	t.Helper()
	var rows []message.Message
	for i := range 3 {
		id := fmt.Sprintf("task-%d", i+1)
		rows = append(rows, toolRow("t"+id, int64(i), memResult(t, "c"+id, push(id, id), popWith(id, "done "+id))))
	}
	rows = append(rows, releaseRows(t)...)
	rows = append(rows, cutMsg("m9", "slide::c1:c4", 4000))
	return deriveFrames("s1", rows)
}

func TestStackColumn_DropsTheLeastNeededFirstToFitItsHeight(t *testing.T) {
	t.Parallel()

	f := longSessionFrames(t)
	column := func(height int) string {
		t.Helper()
		got := stackColumn(minimalFileStyles(), f, 30, height, stackLook{})
		if got != "" {
			require.LessOrEqual(t, strings.Count(got, "\n")+1, height, "the column overflows %d rows", height)
		}
		return stripANSI(got)
	}
	everything := strings.Count(stackSection(minimalFileStyles(), f, 30, stackLook{}), "\n") + 1
	require.Equal(t, 14, everything, "the tasks, release-v2's tree with the last result, what slid out")

	got := column(everything)
	require.Contains(t, got, "task-1", "all of it while it fits")
	require.Contains(t, got, "tagged v2.0.0", "the last pop keeps its result")
	require.NotContains(t, got, "VERSION says 2.0.0", "the others do not")
	require.NotContains(t, got, "done task-1")

	got = column(12)
	require.Contains(t, got, "…3 earlier", "then the earlier tasks fold")
	require.Contains(t, got, "slid out")

	got = column(9)
	require.NotContains(t, got, "slid out", "then what slid out goes")
	require.Contains(t, got, "tagged v2.0.0")

	got = column(8)
	require.NotContains(t, got, "tagged v2.0.0", "then the last result")
	for _, want := range []string{"release-v2", "ci-green", `"CI is green"`} {
		require.Contains(t, got, want, "the frames the agent is in stay")
	}

	got = column(5)
	require.Contains(t, got, `"CI is green"`, "a column too short for them keeps the frame the agent is in")
	require.Empty(t, column(0))
}

// compactUI is sidebarUI in a terminal too narrow for the sidebar.
func compactUI(t *testing.T) *UI {
	t.Helper()
	u, _ := sidebarUI(t, 40)
	u.header = newHeader(u.com)
	u.keyMap = DefaultKeyMap()
	u.status = NewStatus(u.com, u)
	u.width = 100
	u.updateLayoutAndSize()
	require.True(t, u.isCompact)
	return u
}

func observeRows(u *UI, rows ...message.Message) {
	for _, row := range rows {
		_, _ = u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: row})
	}
}

// screenRows is the text on each row of area, as drawn on scr.
func screenRows(scr uv.ScreenBuffer, area uv.Rectangle) []string {
	var rows []string
	for y := area.Min.Y; y < area.Max.Y; y++ {
		var b strings.Builder
		for x := area.Min.X; x < area.Max.X; x++ {
			if c := scr.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
		rows = append(rows, b.String())
	}
	return rows
}

// Compact mode has no sidebar: the row under its header shows where the
// agent is, and the chat keeps its rows.
func TestCompactModeShowsTheStackUnderTheHeader(t *testing.T) {
	t.Parallel()

	u := compactUI(t)
	draw := func() string {
		t.Helper()
		scr := uv.NewScreenBuffer(u.width, u.height)
		u.Draw(scr, scr.Bounds())
		return screenRows(scr, scr.Bounds())[u.layout.header.Max.Y]
	}
	require.Empty(t, strings.TrimSpace(draw()), "a session without frames keeps the blank row")

	observeRows(u, flakyRows(t)...)
	got := draw()

	require.Contains(t, got, "fix-flaky-test › find-cause › try-free-port")
	require.Contains(t, got, "2K tok freed")
	require.Equal(t, u.layout.header.Max.Y+1, u.layout.main.Min.Y, "the chat starts under it")
}

// Ctrl+d opens the session's details in compact mode: the Stack comes
// first, and keeps the frame the agent is in within the panel's height.
func TestCompactDetailsShowTheStackFirst(t *testing.T) {
	t.Parallel()

	u := compactUI(t)
	details := func() string {
		t.Helper()
		scr := uv.NewScreenBuffer(u.width, u.height)
		u.drawSessionDetails(scr, u.layout.sessionDetails)
		return strings.Join(screenRows(scr, u.layout.sessionDetails), "\n")
	}
	require.NotContains(t, details(), "tok freed", "no Stack without frames")

	var rows []message.Message
	for i := range 5 {
		id := fmt.Sprintf("task-%d", i+1)
		rows = append(rows, toolRow("t"+id, int64(i), memResult(t, "c"+id, push(id, id), popWith(id, "done "+id))))
	}
	observeRows(u, append(rows, flakyRows(t)...)...)
	got := details()

	require.Contains(t, got, "try-free-port")
	require.Contains(t, got, `"listen on a free port"`)
	for line := range strings.SplitSeq(got, "\n") {
		if strings.Contains(line, "tok freed") {
			require.Less(t, strings.Index(line, "Stack"), strings.Index(line, "Modified Files"), "the Stack comes first: %q", line)
			return
		}
	}
	require.Fail(t, "no Stack heading in the details", got)
}

// At 80 columns the sections beside the Stack are narrow enough to wrap
// their titles; the panel still keeps its frame.
func TestCompactDetailsKeepTheirFrameWhenNarrow(t *testing.T) {
	t.Parallel()

	u := compactUI(t)
	u.width = 80
	u.updateLayoutAndSize()
	for i := range 12 {
		path := fmt.Sprintf("internal/pkg%d/file.go", i)
		u.sessionFiles = append(u.sessionFiles, SessionFile{FirstVersion: history.File{Path: path}, LatestVersion: history.File{Path: path}, Additions: 3})
	}
	observeRows(u, flakyRows(t)...)

	scr := uv.NewScreenBuffer(u.width, u.height)
	u.drawSessionDetails(scr, u.layout.sessionDetails)
	got := screenRows(scr, u.layout.sessionDetails)

	require.True(t, slices.ContainsFunc(got, func(row string) bool { return strings.HasPrefix(row, "╰") }), "the panel's bottom edge is cut:\n%s", strings.Join(got, "\n"))
}

func TestCompactDetailsSayHowManyFilesTheyHide(t *testing.T) {
	t.Parallel()

	u := compactUI(t)
	for i := range 12 {
		path := fmt.Sprintf("internal/pkg%d/file.go", i)
		u.sessionFiles = append(u.sessionFiles, SessionFile{FirstVersion: history.File{Path: path}, LatestVersion: history.File{Path: path}, Additions: 3})
	}
	press(u, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})

	require.GreaterOrEqual(t, at(drawn(u), u.layout.sessionDetails, "…and 4 more").Y, 0, "8 files shown, then the 4 hidden counted")
}
