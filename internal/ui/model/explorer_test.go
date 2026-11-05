package model

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// explorerRows is a session in fix-flaky-test › find-cause ›
// try-free-port, after reproduce-it's pop took out 2K tokens, as its rows
// come: each call's assistant row, then the tool row answering it.
func explorerRows(t *testing.T) []message.Message {
	t.Helper()
	return []message.Message{
		callRow("e1", 1_700_000_000, memCall("fc1")),
		toolRow("f1", 1_700_000_000, memResult(t, "fc1", push("fix-flaky-test", "fix the flaky test"))),
		callRow("e2", 1_700_000_010, memCall("fc2")),
		toolRow("f2", 1_700_000_010, memResult(t, "fc2", push("fix-flaky-test/reproduce-it", "make it fail"))),
		cutMsg("f3", "fix-flaky-test/reproduce-it", 8000),
		callRow("e4", 1_700_000_030, memCall("fc4")),
		toolRow("f4", 1_700_000_030, memResult(t, "fc4", popWith("fix-flaky-test/reproduce-it", "fails 3 times in 20"), push("fix-flaky-test/find-cause", "find why it fails"))),
		callRow("e5", 1_700_000_040, memCall("fc5")),
		toolRow("f5", 1_700_000_040, memResult(t, "fc5", push("fix-flaky-test/find-cause/try-free-port", "listen on a free port"))),
	}
}

// explorerUI is a UI of width × height with the frames of explorerRows,
// their pops long seen.
func explorerUI(t *testing.T, width, height int) *UI {
	t.Helper()
	return explorerUIOf(t, width, height, explorerRows(t)...)
}

// explorerUIOf is explorerUI with the frames of rows.
func explorerUIOf(t *testing.T, width, height int, rows ...message.Message) *UI {
	t.Helper()
	u, _ := sidebarUI(t, height)
	u.header = newHeader(u.com)
	u.keyMap = DefaultKeyMap()
	u.status = NewStatus(u.com, u)
	u.width = width
	u.updateLayoutAndSize()
	observeRows(u, rows...)
	for i := range u.sessionFrames.calls {
		u.sessionFrames.calls[i].seen = time.Time{}
	}
	return u
}

func press(u *UI, keys ...tea.KeyPressMsg) {
	for _, k := range keys {
		_, _ = u.Update(k)
	}
}

var (
	ctrlX = tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	tab   = tea.KeyPressMsg{Code: tea.KeyTab}
)

func typed(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func click(u *UI, x, y int) {
	_, _ = u.Update(tea.MouseClickMsg(tea.Mouse{X: x, Y: y, Button: uv.MouseLeft}))
}

// drawn is the screen as the UI draws it, row by row.
func drawn(u *UI) []string {
	scr := uv.NewScreenBuffer(u.width, u.height)
	u.Draw(scr, scr.Bounds())
	return screenRows(scr, scr.Bounds())
}

// at is the first point inside area of the screen showing text, or
// (-1, -1).
func at(rows []string, area image.Rectangle, text string) image.Point {
	for y := max(area.Min.Y, 0); y < min(area.Max.Y, len(rows)); y++ {
		cells := []rune(rows[y])
		seg := string(cells[min(area.Min.X, len(cells)):min(area.Max.X, len(cells))])
		if i := strings.Index(seg, text); i >= 0 {
			return image.Pt(area.Min.X+len([]rune(seg[:i])), y)
		}
	}
	return image.Pt(-1, -1)
}

func TestExplorer_CtrlXOpensItAndEscClosesIt(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	require.Equal(t, uiFocusEditor, u.focus)

	press(u, ctrlX)
	require.True(t, u.explorer.open)
	require.Equal(t, uiFocusExplorer, u.focus, "it takes the keys")
	require.True(t, u.explorer.following, "opened from a key, it follows the agent")
	require.GreaterOrEqual(t, at(drawn(u), u.layout.explorer, "Frames").Y, 0)

	press(u, esc)
	require.False(t, u.explorer.open)
	require.Equal(t, uiFocusEditor, u.focus, "focus goes back where it was")
	require.Equal(t, -1, at(drawn(u), u.layout.area, "Frames").Y)
}

func TestExplorer_CtrlXClosesItOrTakesFocusBack(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, tab)
	require.True(t, u.explorer.open)
	require.Equal(t, uiFocusEditor, u.focus, "tab hands the keys to the editor, the half view stays")

	press(u, ctrlX)
	require.True(t, u.explorer.open)
	require.Equal(t, uiFocusExplorer, u.focus, "ctrl+x takes them back")

	press(u, ctrlX)
	require.False(t, u.explorer.open, "and closes it once it has them")
}

// Esc cancels a working agent; with the explorer open, esc closes the
// explorer and leaves the agent alone.
func TestExplorer_EscClosesItWithoutCancellingTheAgent(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	u.agentReady = true
	u.agentBusyCache.set(true)
	press(u, ctrlX, esc)

	require.False(t, u.explorer.open)
	require.False(t, u.isCanceling, "the agent's cancel is not armed")

	press(u, esc)
	require.True(t, u.isCanceling, "esc with the explorer closed arms it, as before")
}

func TestExplorer_HalfWhereItFitsFullElsewhere(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	edge := u.layout.sidebar.Max.X
	press(u, ctrlX)
	require.Equal(t, explorerHalf, u.explorerDrawn(), "opens half at 200 columns")
	require.Equal(t, edge, u.layout.explorer.Max.X, "it ends where the sidebar did")
	require.InDelta(t, u.layout.main.Dx(), u.layout.explorer.Dx(), 2, "and is as wide as the chat")
	require.Less(t, u.layout.main.Max.X, u.layout.explorer.Min.X, "the chat keeps the left half")

	u.width = 150
	u.updateLayoutAndSize()
	require.Equal(t, explorerFull, u.explorerDrawn(), "too narrow for half")
	require.True(t, u.layout.main.In(u.layout.explorer), "it covers the chat")
	require.True(t, u.layout.editor.In(u.layout.explorer), "and the editor")
	require.LessOrEqual(t, u.layout.explorer.Max.Y, u.layout.status.Min.Y, "not the status bar")

	u.width = 200
	u.updateLayoutAndSize()
	require.Equal(t, explorerHalf, u.explorerDrawn(), "half again once there is room")

	press(u, typed('+'))
	require.Equal(t, explorerFull, u.explorerDrawn())
	press(u, typed('-'))
	require.Equal(t, explorerHalf, u.explorerDrawn())

	u.width = 100
	u.updateLayoutAndSize()
	require.True(t, u.isCompact)
	require.Equal(t, explorerFull, u.explorerDrawn(), "compact mode draws it full")
}

func TestExplorer_SizeAndMovesLeaveTheTabAlone(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('k'), typed('k'))

	require.Equal(t, explorerTree, u.explorer.tab)
	require.Equal(t, explorerFull, u.explorerDrawn())
}

func TestExplorer_TabsSwitchOnBracketsAndDigits(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX)
	require.Equal(t, explorerTree, u.explorer.tab)

	press(u, typed(']'))
	require.Equal(t, explorerPulse, u.explorer.tab)
	press(u, typed(']'), typed(']'))
	require.Equal(t, explorerTree, u.explorer.tab, "round again")
	press(u, typed('['))
	require.Equal(t, explorerContext, u.explorer.tab)
	press(u, typed('1'))
	require.Equal(t, explorerTree, u.explorer.tab)
	press(u, typed('2'))
	require.Equal(t, explorerPulse, u.explorer.tab)
}

func TestExplorer_FollowsTheAgentUntilMoved(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX)
	require.Equal(t, "fix-flaky-test/find-cause/try-free-port", u.selectedFrame().id)

	press(u, typed('k'))
	require.False(t, u.explorer.following)
	require.Equal(t, "fix-flaky-test/find-cause", u.selectedFrame().id)

	observeRows(u, toolRow("f6", 1_700_000_050, memResult(t, "fc6", push("fix-flaky-test/find-cause/try-free-port/rerun", "rerun the suite"))))
	require.Equal(t, "fix-flaky-test/find-cause", u.selectedFrame().id, "paused, the selection stays")
	require.GreaterOrEqual(t, at(drawn(u), u.layout.explorer, "1 new below").Y, 0)

	press(u, typed('G'))
	require.True(t, u.explorer.following)
	require.Equal(t, "fix-flaky-test/find-cause/try-free-port/rerun", u.selectedFrame().id)
}

func TestExplorer_TreeFoldsAndShowsTheSelectedFrame(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('k'), typed('k'))
	require.Equal(t, "fix-flaky-test/reproduce-it", u.selectedFrame().id)

	rows := drawn(u)
	for _, want := range []string{"reproduce-it", "make it fail", "fails 3 times in 20", "−2K tok"} {
		require.GreaterOrEqual(t, at(rows, u.layout.explorer, want).Y, 0, "the detail shows %q", want)
	}

	press(u, typed('k'), typed('h'))
	require.Equal(t, "fix-flaky-test", u.selectedFrame().id)
	require.Equal(t, -1, at(drawn(u), u.layout.explorer, "try-free-port").Y, "folded, its frames are hidden")
	press(u, typed('l'))
	require.GreaterOrEqual(t, at(drawn(u), u.layout.explorer, "try-free-port").Y, 0)
}

func TestExplorer_OpensFromAClickOnTheSidebarStack(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	rows := drawn(u)
	heading := at(rows, u.layout.sidebar, "Stack ─")
	require.GreaterOrEqual(t, heading.Y, 0)

	click(u, heading.X+1, heading.Y)
	require.True(t, u.explorer.open)
	require.True(t, u.explorer.following, "the heading opens it following")
	press(u, typed('3'), esc)

	row := at(rows, u.layout.sidebar, "reproduce-it")
	click(u, row.X+1, row.Y)
	require.True(t, u.explorer.open)
	require.False(t, u.explorer.following, "a frame's row opens it on that frame")
	require.Equal(t, "fix-flaky-test/reproduce-it", u.selectedFrame().id)
	require.Equal(t, explorerTree, u.explorer.tab, "in the tree, whichever tab was last open")
}

func TestExplorer_OpensFollowingFromAClickOnTheFrameAtWork(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('3'), esc)

	row := at(drawn(u), u.layout.sidebar, "try-free-port")
	require.GreaterOrEqual(t, row.Y, 0)
	click(u, row.X+1, row.Y)
	require.True(t, u.explorer.open)
	require.True(t, u.explorer.following, "the frame the agent is in is what following shows")
	require.Equal(t, "fix-flaky-test/find-cause/try-free-port", u.selectedFrame().id)
	require.Equal(t, explorerTree, u.explorer.tab)
}

func TestExplorer_OpensFromACompactTrailClick(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 100, 40)
	require.True(t, u.isCompact)

	click(u, u.layout.stackTrail.Min.X+4, u.layout.stackTrail.Min.Y)

	require.True(t, u.explorer.open)
	require.Equal(t, explorerFull, u.explorerDrawn())
}

func TestExplorer_OpensFromAClickInTheDetailsColumn(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 100, 40)
	press(u, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	require.True(t, u.detailsOpen)
	row := at(drawn(u), u.layout.sessionDetails, "reproduce-it")
	require.GreaterOrEqual(t, row.Y, 0)

	click(u, row.X+1, row.Y)

	require.True(t, u.explorer.open)
	require.Equal(t, "fix-flaky-test/reproduce-it", u.selectedFrame().id)
	require.False(t, u.detailsOpen, "the panel it came from closes")
}

func TestExplorer_HeaderButtonsSizeAndCloseIt(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX)

	full := at(drawn(u), u.layout.explorer, "■ full")
	click(u, full.X+1, full.Y)
	require.Equal(t, explorerFull, u.explorerDrawn())

	half := at(drawn(u), u.layout.explorer, "◧ half")
	click(u, half.X+1, half.Y)
	require.Equal(t, explorerHalf, u.explorerDrawn())

	pulse := at(drawn(u), u.layout.explorer, "Pulse")
	click(u, pulse.X+1, pulse.Y)
	require.Equal(t, explorerPulse, u.explorer.tab, "a tab's name opens it")

	closing := at(drawn(u), u.layout.explorer, "× close")
	click(u, closing.X+1, closing.Y)
	require.False(t, u.explorer.open)
}

// A header too narrow for anything but the buttons still ends with them,
// where a click finds them.
func TestExplorer_HeaderButtonsWorkWhenNarrow(t *testing.T) {
	t.Parallel()

	for _, width := range []int{32, 24} {
		u := explorerUI(t, width, 40)
		press(u, ctrlX)

		closing := at(drawn(u), u.layout.explorer, "× close")
		require.GreaterOrEqual(t, closing.Y, 0, "at %d columns", width)
		click(u, closing.X+1, closing.Y)
		require.False(t, u.explorer.open, "at %d columns", width)
	}
}

func TestExplorer_AClickOnAFrameSelectsIt(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX)
	frame := at(drawn(u), u.layout.explorer, "└ ▸ find-cause")
	require.GreaterOrEqual(t, frame.Y, 0)

	click(u, frame.X+5, frame.Y)

	require.False(t, u.explorer.following)
	require.Equal(t, "fix-flaky-test/find-cause", u.selectedFrame().id)
}

func TestExplorer_KeysShowInTheStatusBar(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX)

	var help []string
	for _, b := range u.ShortHelp() {
		help = append(help, b.Help().Key+" "+b.Help().Desc)
	}
	require.Contains(t, help, "esc close")
	require.Contains(t, help, "[/] tabs")
	require.Contains(t, help, "+/- full/half")
	require.Contains(t, help, "enter show in chat")
	require.Contains(t, help, "/ search")
}

func TestExplorer_IsNotOfferedWithoutFrames(t *testing.T) {
	t.Parallel()

	u, _ := sidebarUI(t, 50)
	u.keyMap = DefaultKeyMap()
	u.width = 200
	u.updateLayoutAndSize()

	press(u, ctrlX)

	require.False(t, u.explorer.open)
}

// treeOf is the Tree tab's lines above its detail, each cut to the
// explorer.
func treeOf(u *UI) []string {
	rows := drawn(u)
	area := u.layout.explorer
	var tree []string
	for y := area.Min.Y + explorerBodyRow; y < area.Max.Y; y++ {
		cells := []rune(rows[y])
		line := string(cells[min(area.Min.X, len(cells)):min(area.Max.X, len(cells))])
		if strings.HasPrefix(strings.TrimSpace(line), "────") {
			break
		}
		tree = append(tree, line)
	}
	return tree
}

// lineWith is the first of lines holding text, or "".
func lineWith(lines []string, text string) string {
	for _, l := range lines {
		if strings.Contains(l, text) {
			return l
		}
	}
	return ""
}

// column is the column of text in line, in cells, or -1.
func column(line, text string) int {
	i := strings.Index(line, text)
	if i < 0 {
		return -1
	}
	return len([]rune(line[:i]))
}

func TestExplorer_TreeDrawsEachFrameOnATimeAxis(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 34)
	press(u, ctrlX, typed('+'))
	tree := treeOf(u)

	axis := lineWith(tree, "freed")
	require.Contains(t, axis, "0s", "the axis starts at the first push")
	require.Contains(t, axis, "10s")
	require.Contains(t, lineWith(tree, "▴"), "pop", "a row marks the pops on the axis")

	top, reproduce := lineWith(tree, "fix-flaky-test"), lineWith(tree, "reproduce-it")
	require.Greater(t, column(reproduce, "█"), column(top, "█"), "reproduce-it was pushed after its parent")
	require.Contains(t, reproduce, "−2K tok", "what its pop freed ends its line")
	cause := lineWith(tree, "find-cause")
	require.Contains(t, cause, "▶", "an open frame's bar runs to the end")
	require.Less(t, column(cause, "▶"), column(cause, time.Unix(1_700_000_030, 0).Format("15:04")), "then when it was entered")
}

func TestExplorer_TreeDropsTheBarsWhenNarrow(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 48, 30)
	press(u, ctrlX)
	tree := strings.Join(treeOf(u), "\n")

	require.NotContains(t, tree, "█")
	require.NotContains(t, tree, "freed", "no axis either")
	require.Contains(t, tree, "reproduce-it")
	require.Contains(t, tree, "−2K tok")
}

// slidRows is explorerRows with try-free-port's first round slid out of the
// model's context, and a round after it.
func slidRows(t *testing.T) []message.Message {
	t.Helper()
	mark := "slide:fix-flaky-test/find-cause/try-free-port:fc5:fc7"
	first, answer := callRow("e6", 1_700_000_050, toolCall("v1", "view", `{"file_path":"a.go"}`)), toolRow("f6", 1_700_000_050, viewResult("v1"))
	first.Popped, answer.Popped = mark, mark
	return append(explorerRows(t), first, answer,
		callRow("e7", 1_700_000_060, toolCall("v2", "view", `{"file_path":"b.go"}`)),
		toolRow("f7", 1_700_000_060, viewResult("v2")),
	)
}

func TestExplorer_TreeShowsASlideUnderTheFrameItSlid(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 34)
	observeRows(u, slidRows(t)[len(explorerRows(t)):]...)
	press(u, ctrlX, typed('+'))
	tree := treeOf(u)

	slide := slices.IndexFunc(tree, func(l string) bool { return strings.Contains(l, "⟲ slid · 2 rows") })
	require.Greater(t, slide, slices.IndexFunc(tree, func(l string) bool { return strings.Contains(l, "try-free-port") }))
	require.Contains(t, tree[slide], "−1K tok")
	require.Contains(t, lineWith(tree, "▴"), "⟲", "the axis marks it too")

	press(u, typed('j'))
	require.Equal(t, "fix-flaky-test/find-cause/try-free-port", u.selectedFrame().id, "the selection skips it")
}

func TestExplorer_TreeEndsTheGoalAtWorkWithItsStepsNotStarted(t *testing.T) {
	t.Parallel()

	for name, size := range map[string][2]int{"with bars": {200, 50}, "without": {100, 40}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			u := explorerUI(t, size[0], size[1])
			u.session.Todos = []session.Todo{
				{Content: "reproduce it", Status: session.TodoStatusCompleted},
				{Content: "find the cause", Status: session.TodoStatusInProgress},
				{Content: "rerun the suite 20 times", Status: session.TodoStatusPending},
			}
			press(u, ctrlX, typed('+'))
			tree := treeOf(u)

			next := slices.IndexFunc(tree, func(l string) bool { return strings.Contains(l, "○ rerun the suite 20 times") })
			require.Greater(t, next, slices.IndexFunc(tree, func(l string) bool { return strings.Contains(l, "try-free-port") }))
			require.Contains(t, tree[next], "└ ○", "the goal's last step")
			require.Contains(t, tree[next], "next")
			require.Empty(t, lineWith(tree, "○ find the cause"), "only the steps not started")

			press(u, typed('j'))
			require.Equal(t, "fix-flaky-test/find-cause/try-free-port", u.selectedFrame().id, "the selection skips it")
		})
	}
}

func TestExplorer_TreeShowsTheContextOverTimeWhenTall(t *testing.T) {
	t.Parallel()

	tall := explorerUI(t, 200, 50)
	press(tall, ctrlX, typed('+'))
	require.NotEmpty(t, lineWith(treeOf(tall), "model context"))

	short := explorerUI(t, 200, 34)
	press(short, ctrlX, typed('+'))
	require.Empty(t, lineWith(treeOf(short), "model context"), "a short terminal drops the chart")
}

// whole is row with the finish part a reply gets once it is whole.
func whole(row message.Message) message.Message {
	row.Parts = append(slices.Clone(row.Parts), message.Finish{Reason: message.FinishReasonToolUse})
	return row
}

func TestContextOverTime_AddsWhatTheProviderCountedBeyondTheRows(t *testing.T) {
	u := contextUI(t, 200, 50)
	observeRows(u, whole(callRow("e5", 1_700_000_040, memCall("fc5"))))
	rows := u.sessionFrames.contextSeries()
	reply := slices.IndexFunc(u.sessionFrames.rows, func(r message.Message) bool { return r.ID == "e5" })
	always := 6_000 - rows[reply].tokens
	require.Positive(t, always, "beyond the rows as they stood at the last reply, the one counted")

	series, got := u.contextOverTime()
	require.Equal(t, always, got)
	for i := range rows {
		require.Equal(t, rows[i].tokens+always, series[i].tokens, "every point is its rows and what is always sent, not a multiple of its rows")
	}
}

func TestContextOverTime_APopAfterTheCountLeavesWhatIsAlwaysSent(t *testing.T) {
	u := contextUI(t, 200, 50)
	observeRows(u, whole(callRow("e6", 1_700_000_050, memCall("fc6"))))
	_, before := u.contextOverTime()

	observeRows(u,
		toolRow("f6", 1_700_000_051, memResult(t, "fc6",
			popWith("fix-flaky-test/find-cause/try-free-port", "port 0"), popWith("fix-flaky-test/find-cause", "a shared port"))),
		callRow("e7", 1_700_000_052), // the next reply, still streaming
	)
	_, after := u.contextOverTime()
	require.Equal(t, before, after, "the count is of the request before the pop")
	require.Equal(t, before, u.contextNow().always, "and the now bar says the same")
}

func TestExplorer_TreeChartShowsWhatIsAlwaysSentAtItsFoot(t *testing.T) {
	u := contextUI(t, 200, 50)
	press(u, ctrlX, typed('+'))
	tree := treeOf(u)

	series, always := u.contextOverTime()
	var top int64
	for _, p := range series {
		top = max(top, p.tokens)
	}
	require.Contains(t, lineWith(tree, "model context"), shortTokens(top))
	require.Contains(t, lineWith(tree, "always sent"), shortTokens(always))
	band := slices.IndexFunc(tree, func(l string) bool { return strings.Contains(l, "always sent") })
	require.Contains(t, tree[band+1], "▴ pop", "what is always sent takes one row, at the foot, whatever its size")
}

func TestContextChartDrawsFromItsFloor(t *testing.T) {
	t.Parallel()

	series := []contextPoint{{at: 10, tokens: 9000}, {at: 20, tokens: 10_000}, {at: 30, tokens: 9500}}

	require.Equal(t, []string{" █ ", " ██"}, contextChart(series, timeAxis{from: 10, to: 30, width: 3}, 2, 9000, 10_000), "the rows from the floor to the top")
}

func TestContextChartScalesToItsHeight(t *testing.T) {
	t.Parallel()

	series := []contextPoint{{at: 10, tokens: 1000}, {at: 20, tokens: 2000}, {at: 30, tokens: 500}}

	require.Equal(t, []string{" █ ", "██▄"}, contextChart(series, timeAxis{from: 10, to: 30, width: 3}, 2, 0, 2000))
	require.Equal(t, []string{"   ██ ", " ████▄"}, contextChart(series, timeAxis{from: 5, to: 30, width: 6}, 2, 0, 2000), "a column shows the last point at or before its time")
}

var enter = tea.KeyPressMsg{Code: tea.KeyEnter}

func TestExplorer_SearchSelectsTheFirstFrameMatching(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('/'))
	for _, r := range "WHY" {
		press(u, typed(r))
	}
	require.Equal(t, "fix-flaky-test/find-cause", u.selectedFrame().id, "its purpose matches, in any case")
	require.GreaterOrEqual(t, at(drawn(u), u.layout.explorer, "/WHY").Y, 0, "the query shows while it is typed")

	press(u, enter)
	require.True(t, u.explorer.open)
	require.Equal(t, "fix-flaky-test/find-cause", u.selectedFrame().id)
	require.False(t, u.explorer.following)
	require.Equal(t, -1, at(drawn(u), u.layout.explorer, "/WHY").Y, "enter ends the search")

	press(u, typed('/'), typed('m'), typed('a'), typed('k'))
	require.Equal(t, "fix-flaky-test/reproduce-it", u.selectedFrame().id)
	press(u, esc)
	require.True(t, u.explorer.open, "esc ends the search, not the explorer")
	require.Equal(t, "fix-flaky-test/find-cause", u.selectedFrame().id, "and puts the selection back")
}

func TestExplorer_SearchOpensTheFramesAboveItsMatch(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('k'), typed('k'), typed('k'), typed('h'))
	require.Equal(t, "fix-flaky-test", u.selectedFrame().id)
	require.Empty(t, lineWith(treeOf(u), "try-free-port"))

	press(u, typed('/'), typed('p'), typed('o'), typed('r'), typed('t'))

	require.Equal(t, "fix-flaky-test/find-cause/try-free-port", u.selectedFrame().id)
	require.NotEmpty(t, lineWith(treeOf(u), "try-free-port"))
}

func TestExplorer_EnterShowsTheFrameInTheChat(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('k'), typed('k'))
	require.Equal(t, "fix-flaky-test/reproduce-it", u.selectedFrame().id)

	press(u, enter)

	require.True(t, u.explorer.open, "beside the chat, it stays")
	require.True(t, u.chat.framed["f3"], "the frame's rows are marked")
	require.True(t, u.chat.framed["fc4"], "and the call that popped it")
	require.True(t, u.chat.framed["e4"], "with the text its row opens with")
	require.False(t, u.chat.framed["fc2"], "not the one that pushed it")
	top, _ := u.chat.ScrollPosition()
	require.Equal(t, u.chat.idInxMap["fc2"], top, "the chat starts at its push")
	require.GreaterOrEqual(t, at(drawn(u), u.layout.main, "▌").Y, 0, "the marked rows have a gutter")

	press(u, typed('+'), enter)
	require.True(t, u.explorer.open)
	require.Equal(t, explorerHalf, u.explorerDrawn(), "full drops to half, where half fits")

	narrow := explorerUI(t, 150, 50)
	press(narrow, ctrlX, typed('k'), enter)
	require.False(t, narrow.explorer.open, "where it does not, it closes")
	require.True(t, narrow.chat.framed["e5"])

	press(narrow, ctrlX, esc)
	require.Empty(t, narrow.chat.framed, "closing the explorer clears the marks")
}

func TestExplorer_EnterOnAFrameNearTheChatsEndLeavesNoRowBlank(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX)
	require.Equal(t, "fix-flaky-test/find-cause/try-free-port", u.selectedFrame().id, "its push is the chat's last item")

	press(u, enter)

	main := u.layout.main
	last := []rune(drawn(u)[main.Max.Y-1])
	require.NotEmpty(t, strings.TrimSpace(string(last[main.Min.X:main.Max.X-1])), "the chat stops at its bottom")
}

func TestExplorer_TheChatShowsWhichMarkedItemIsSelected(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('k'), typed('k'), enter)
	reply := at(drawn(u), u.layout.main, "xxxx")
	click(u, reply.X, reply.Y)
	require.Equal(t, u.chat.idInxMap["f3"], u.chat.list.Selected(), "the click selects the marked reply")

	// bar is the color of the first cell of the reply's first row.
	bar := func() color.Color {
		scr := uv.NewScreenBuffer(u.width, u.height)
		u.Draw(scr, scr.Bounds())
		y := at(screenRows(scr, scr.Bounds()), u.layout.main, "xxxx").Y
		return scr.CellAt(u.layout.main.Min.X, y).Style.Fg
	}
	focused := u.com.Styles.Messages.AssistantFocused.GetBorderLeftForeground()
	require.True(t, sameColor(bar(), focused), "the selected item keeps its focus bar")

	press(u, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	require.True(t, sameColor(bar(), u.com.Styles.Header.LogoGradToColor), "once another is selected, the gutter")
}

func TestExplorer_DetailListsWhatTheModelNoLongerHas(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('k'), typed('k'))
	require.Equal(t, "fix-flaky-test/reproduce-it", u.selectedFrame().id)

	rows := drawn(u)
	head := at(rows, u.layout.explorer, "what the model no longer has · 1 row")
	require.Equal(t, at(rows, u.layout.explorer, "fix-flaky-test/reproduce-it").Y, head.Y, "beside the detail")
	require.Equal(t, head.Y+1, at(rows, u.layout.explorer, "xxxxxxxx").Y, "the row it took, as the chat shows it")
	require.GreaterOrEqual(t, at(rows, u.layout.explorer, "enter: show these rows in the chat").Y, 0)

	press(u, typed('G'))
	require.GreaterOrEqual(t, at(drawn(u), u.layout.explorer, "what the model holds").Y, 0, "an open frame lists what it holds")

	half := explorerUI(t, 200, 50)
	press(half, ctrlX)
	require.Equal(t, -1, at(drawn(half), half.layout.explorer, "what the model").Y, "a half explorer has no room for it")
}

// A prompt folded into the turn of a frame's pop came between its push
// and its pop, and stays in the model's context: the pop freed nothing.
func TestExplorer_DetailSaysAPopThatKeptItsTurnsPromptFreedNothing(t *testing.T) {
	t.Parallel()

	u := explorerUIOf(t, 200, 50,
		callRow("e1", 1_700_000_000, memCall("c1")),
		toolRow("f1", 1_700_000_000, memResult(t, "c1", push("a", "do a"))),
		textRow("u1", message.User, 1_700_000_005, 40),
		callRow("e2", 1_700_000_010, memCall("c2")),
		toolRow("f2", 1_700_000_010, memResult(t, "c2", popWith("a", "done"))),
	)
	press(u, ctrlX, typed('+'))
	require.Equal(t, "a", u.selectedFrame().id)

	body := bodyOf(u)
	require.NotEmpty(t, lineWith(body, "nothing: its pop took nothing out of the model's context"), strings.Join(body, "\n"))
	require.Empty(t, lineWith(body, "nothing came between"))
}

// modelWorkspace is exportWorkspace with a config that has providers, as
// the sidebar needs for a model.
type modelWorkspace struct{ *exportWorkspace }

func (modelWorkspace) Config() *config.Config {
	return &config.Config{Providers: csync.NewMap[string, config.ProviderConfig](), Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// contextUI is explorerUI on a model with a 32K window, which slides at
// 25.6K, the provider having counted 6K tokens for the last request.
func contextUI(t *testing.T, width, height int) *UI {
	t.Helper()
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	u := explorerUI(t, width, height)
	u.com.Workspace = modelWorkspace{u.com.Workspace.(*exportWorkspace)}
	u.agentReady = true
	u.agentModel = workspace.AgentModel{CatwalkCfg: catwalk.Model{ContextWindow: 32_000}}
	u.session.PromptTokens = 6_000
	return u
}

// bodyOf is the explorer's lines under its tabs, each cut to it.
func bodyOf(u *UI) []string {
	rows := drawn(u)
	area := u.layout.explorer
	var body []string
	for y := area.Min.Y + explorerBodyRow; y < area.Max.Y; y++ {
		cells := []rune(rows[y])
		body = append(body, string(cells[min(area.Min.X, len(cells)):min(area.Max.X, len(cells))]))
	}
	return body
}

func TestExplorer_ContextShowsWhatTheModelIsSentNow(t *testing.T) {
	u := contextUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('3'))
	body := bodyOf(u)

	now := lineWith(body, "the model is sent now")
	require.Contains(t, now, "6K of 32K")
	require.Contains(t, now, "│", "the slide point is marked on the bar")
	legend := strings.Join(body[slices.Index(body, now)+1:slices.Index(body, now)+3], "\n")
	for _, part := range []string{"always sent", "outside every frame", "fix-flaky-test", "find-cause", "slides at 25.6K"} {
		require.Contains(t, legend, part)
	}
	require.NotEmpty(t, lineWith(body, "before the next slide"))
}

// How far the slide is, the tab reckons as the agent does: from what the
// provider counted, the rows that came after the reply it counted left
// out, since the agent sends them before it slides.
func TestExplorer_ContextSaysHowFarTheSlideIsAsTheAgentDoes(t *testing.T) {
	u := contextUI(t, 200, 50)
	observeRows(u,
		whole(callRow("g1", 1_700_000_050, toolCall("v1", "view", `{"file_path":"big.go"}`))),
		toolRow("g2", 1_700_000_050, message.ToolResult{ToolCallID: "v1", Name: "view", Content: strings.Repeat("x", 100_000)}),
	)
	u.session.PromptTokens = 6_000 // counted for g1's request, before its 25K result came
	press(u, ctrlX, typed('+'), typed('3'))

	body := bodyOf(u)
	require.Empty(t, lineWith(body, "Past the slide point"), "the agent goes on from 6K")
	require.NotEmpty(t, lineWith(body, "19.6K before the next slide"))

	u.session.PromptTokens = 27_000
	require.NotEmpty(t, lineWith(bodyOf(u), "Past the slide point"), "from 27K it slides")
}

func TestExplorer_ContextSaysWhatAPopNowWouldFree(t *testing.T) {
	u := contextUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('3'))
	body := bodyOf(u)

	require.NotEmpty(t, lineWith(body, "a pop now would free"))
	require.Contains(t, lineWith(body, "› ▸ try-free-port"), "0", "the frame at work first, holding nothing yet")
	require.Regexp(t, `find-cause\s+with try-free-port\s+2\s`, lineWith(body, "▸ find-cause"))
	require.Regexp(t, `fix-flaky-test\s+with both\s+6\s`, lineWith(body, "▸ fix-flaky-test"))
	require.Regexp(t, `outside every frame\s+2\s.*no pop takes these`, lineWith(body, "no pop takes these"))
	require.Contains(t, lineWith(body, "since its push"), "of 12.8K", "against half the slide point")
}

func TestExplorer_ContextListsTheBiggestRowsAndShowsOne(t *testing.T) {
	u := contextUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('3'))
	body := bodyOf(u)
	head := slices.IndexFunc(body, func(l string) bool { return strings.Contains(l, "the biggest rows the model still has") })
	require.Greater(t, head, 0)
	require.Contains(t, body[head+1], "≈")
	require.Contains(t, body[head+1], "›", "the first is selected")

	press(u, typed('j'))
	require.Contains(t, bodyOf(u)[head+2], "›")

	press(u, enter)
	require.NotEmpty(t, u.chat.framed, "enter shows the selected row in the chat")
	require.Equal(t, explorerHalf, u.explorerDrawn(), "and makes room for the chat")
	require.Equal(t, explorerContext, u.explorer.tab)
}

func TestExplorer_ContextNamesWhatEachBigRowIs(t *testing.T) {
	u := contextUI(t, 200, 50)
	text := func(id string, role message.MessageRole, at int64, s string) message.Message {
		return message.Message{ID: id, SessionID: "s1", Role: role, CreatedAt: at, Parts: []message.ContentPart{message.TextContent{Text: s}}}
	}
	observeRows(u,
		text("u1", message.User, 1_700_000_045, "release v2 "+strings.Repeat("and the notes ", 300)),
		text("a9", message.Assistant, 1_700_000_046, "Both tests bind :8080.\n"+strings.Repeat("x", 3000)),
	)
	press(u, ctrlX, typed('+'), typed('3'))
	body := bodyOf(u)

	prompt := lineWith(body, "prompt")
	require.Contains(t, prompt, "release v2")
	require.NotContains(t, prompt, "│", "not the chat's border")
	require.Contains(t, lineWith(body, "reply"), `"Both tests bind :8080."`)
}

func TestExplorer_ContextCountsAPromptsAttachments(t *testing.T) {
	u := contextUI(t, 200, 50)
	observeRows(u, attachedPrompt("u1", 1_700_000_045))
	press(u, ctrlX, typed('+'), typed('3'))

	require.Contains(t, lineWith(bodyOf(u), "look at this"), "≈10K", "the model is sent the file inside the prompt")
}

func TestExplorer_ContextSetsItsListsSideBySideWhenWide(t *testing.T) {
	wide := contextUI(t, 226, 50)
	press(wide, ctrlX, typed('+'), typed('3'))
	require.Contains(t, lineWith(bodyOf(wide), "a pop now would free"), "the biggest rows the model still has")

	half := contextUI(t, 226, 50)
	press(half, ctrlX, typed('3'))
	require.NotContains(t, lineWith(bodyOf(half), "a pop now would free"), "the biggest rows", "one under the other when not")
}

func TestExplorer_ContextKeepsItsFootWhenShort(t *testing.T) {
	u := contextUI(t, 100, 28)
	press(u, ctrlX, typed('3'))
	require.NotEmpty(t, lineWith(bodyOf(u), "provider's count"))
}

func TestExplorer_ContextListSaysWhatItHidesAbove(t *testing.T) {
	u := contextUI(t, 200, 50)
	for i := range 8 {
		observeRows(u, callRow(fmt.Sprintf("x%d", i), 1_700_000_050+int64(i), toolCall(fmt.Sprintf("v%d", i), "view", `{"file_path":"a.go"}`)))
	}
	press(u, ctrlX, typed('+'), typed('3'))
	for range len(u.contextNow().sent) {
		press(u, typed('j'))
	}
	body := bodyOf(u)
	head := slices.IndexFunc(body, func(l string) bool { return strings.Contains(l, "the biggest rows the model still has") })
	require.Contains(t, body[head+1], "above")
	require.False(t, slices.ContainsFunc(body[head:], func(l string) bool { return strings.Contains(l, " more") }), "nothing is hidden under the last")
}

func TestExplorer_ContextChartsTheSessionWhenTall(t *testing.T) {
	tall := contextUI(t, 200, 50)
	press(tall, ctrlX, typed('+'), typed('3'))
	body := bodyOf(tall)
	require.NotEmpty(t, lineWith(body, "the model's context over the session"))
	require.NotEmpty(t, lineWith(body, "32K window"))
	require.True(t, slices.ContainsFunc(body, func(l string) bool { return strings.Contains(l, "slides at 25.6K") && strings.Contains(l, "┄") }),
		"the slide point is a dashed line")

	_, always := tall.contextOverTime()
	band := slices.IndexFunc(body, func(l string) bool {
		return strings.Contains(l, "always sent "+shortTokens(always)) && strings.Contains(l, "█")
	})
	require.Positive(t, band, "what is always sent is the chart's foot")
	require.Contains(t, body[band+1], "▴ pop", "one row, whatever its size")

	over := contextUI(t, 200, 50)
	over.session.PromptTokens = 40_000
	press(over, ctrlX, typed('+'), typed('3'))
	require.Empty(t, lineWith(bodyOf(over), "32K window"), "a session past its window tops the chart with its own peak")

	short := contextUI(t, 170, 30)
	press(short, ctrlX, typed('+'), typed('3'))
	require.Empty(t, lineWith(bodyOf(short), "the model's context over the session"))
}

// A rewind that took every push leaves the rows a slide took; the chart
// spans their minute, not the time since 1970.
func TestExplorer_ContextChartSpansTheRowsOnceEveryPushIsGone(t *testing.T) {
	u := contextUI(t, 200, 50)
	observeRows(u, slidRows(t)[len(explorerRows(t)):]...)
	press(u, ctrlX, typed('+'), typed('3'))
	for _, id := range []string{"f1", "f2", "f4", "f5"} {
		_, _ = u.Update(pubsub.Event[message.Message]{Type: pubsub.DeletedEvent, Payload: message.Message{ID: id, SessionID: "s1"}})
	}
	require.Zero(t, u.frameEntries())

	body := bodyOf(u)
	require.NotEmpty(t, lineWith(body, "the model's context over the session"))
	require.NotEmpty(t, lineWith(body, "30s"), "the axis runs from the first row")
}

func TestExplorer_ContextWithoutAWindow(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('3'))
	body := strings.Join(bodyOf(u), "\n")

	require.Contains(t, body, "the model is sent now")
	require.NotContains(t, body, "slides at")
}

// pulseUI is explorerUI with mem holding a question for the user, release
// v2 whose next step is the notes, and the shapes plan, whose circle a
// peer session started; and the session's todos.
func pulseUI(t *testing.T, width, height int) *UI {
	t.Helper()
	u := explorerUI(t, width, height)
	u.memPulse = gnovm.MemPulse{
		At:   time.Now().Unix() - 30,
		Asks: []gnovm.MemPulseItem{{Name: "release-v2/ci-green/which-runner", Title: "which CI runner is the release one?"}},
		Goals: []gnovm.MemPulseGoal{
			{Name: "release-v2", Title: "release v2", Doing: "s1", StepsOpen: 2, Next: "release-v2/notes", NextTitle: "write the notes"},
			{Name: "shapes", Title: "the shapes plan", StepsOpen: 3, Held: []gnovm.MemPulseHeld{
				{Name: "shapes/circle", Title: "draw the circle", Why: "session", Doing: "3a91c0de-77c0-4e1b-9a7e-0d1c2b3a4f5e"},
			}},
		},
	}
	u.session.Todos = []session.Todo{
		{Content: "reproduce the flake", Status: session.TodoStatusCompleted},
		{Content: "rerun the suite 20 times", Status: session.TodoStatusPending},
	}
	return u
}

func TestExplorer_PulseShowsWhatWaitsOnTheUser(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('2'))
	body := bodyOf(u)

	require.Contains(t, body[0], "▲ WAITS ON YOU (1)")
	require.Contains(t, lineWith(body, "which CI runner is the release one?"), "release-v2/ci-green/which-runner")

	u.memPulse.Asks = nil
	require.NotContains(t, strings.Join(bodyOf(u), "\n"), "WAITS ON YOU", "the strip shows only when something waits")
}

func TestExplorer_PulseLanes(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('2'))
	body := strings.Join(bodyOf(u), "\n")

	for _, want := range []string{
		"NOW (4)", "fix-flaky-test/find-cause/try-free-port", "this session", "shapes/circle", "session 3a91c0de",
		"JUST FINISHED (1)", "fix-flaky-test/reproduce-it", "−2K tok", "fails 3 times in 20",
		"UP NEXT (2)", "rerun the suite 20 times", "release-v2/notes",
	} {
		require.Contains(t, body, want)
	}
	require.Regexp(t, `1 working · 1 waits on you · updated 3[01]s ago`, body)
	require.NotContains(t, body, "reproduce the flake", "a todo done is not up next")
	nowAt := column(lineWith(bodyOf(u), "NOW (4)"), "NOW")
	require.Less(t, nowAt, column(lineWith(bodyOf(u), "JUST FINISHED"), "JUST FINISHED"), "the lanes stand side by side when wide")

	half := pulseUI(t, 200, 50)
	press(half, ctrlX, typed('2'))
	lines := bodyOf(half)
	require.Less(t, slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "NOW (4)") }),
		slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "JUST FINISHED") }), "and one under the other when not")
}

func TestExplorer_PulseListsEachStepUpNextOnceOnALine(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	u.memPulse.Goals = append(u.memPulse.Goals, gnovm.MemPulseGoal{
		Name: "fix-flaky-test", Title: "fix the flaky test",
		Next: "fix-flaky-test/rerun-suite-20-times", NextTitle: "rerun the suite 20 times",
	})
	press(u, ctrlX, typed('+'), typed('2'))
	body := strings.Join(bodyOf(u), "\n")

	require.Contains(t, body, "UP NEXT (2)")
	require.Equal(t, 1, strings.Count(body, "rerun the suite 20 times"), "the goal at work's next step is the session's todo")
	require.Contains(t, lineWith(bodyOf(u), "release-v2/notes"), "write the notes")
}

// Under /goal mem does not push the goal itself: the session's bottom
// frame is a step of it.
func TestExplorer_PulseListsTheNextStepOnceUnderAGoal(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	u.sessionFrames = deriveFrames("s1", []message.Message{
		callRow("g1", 1_700_000_000, memCall("gc1")),
		toolRow("g2", 1_700_000_000, memResult(t, "gc1", push("release-v2/bump-version", "bump the version"))),
	})
	u.session.Todos = []session.Todo{
		{Content: "bump the version", Status: session.TodoStatusInProgress},
		{Content: "tag v2.0.0", Status: session.TodoStatusPending},
		{Content: "write the notes", Status: session.TodoStatusPending},
	}
	u.memPulse = gnovm.MemPulse{At: time.Now().Unix(), Goals: []gnovm.MemPulseGoal{
		{Name: "release-v2", Title: "release v2", Doing: "s1", StepsOpen: 3, Next: "release-v2/tag-v2-0-0", NextTitle: "tag v2.0.0"},
	}}
	press(u, ctrlX, typed('+'), typed('2'))
	body := strings.Join(bodyOf(u), "\n")

	require.Contains(t, body, "UP NEXT (2)")
	require.Equal(t, 1, strings.Count(body, "tag v2.0.0"), "the goal's next step is the session's todo")
}

func TestExplorer_PulseCountsTheSessionsAtWork(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('2'))
	body := strings.Join(bodyOf(u), "\n")
	require.Contains(t, body, "1 working", "an idle session is not at work, its peer is")
	require.Contains(t, body, "this session · paused")

	u.agentBusyCache.val = true
	body = strings.Join(bodyOf(u), "\n")
	require.Contains(t, body, "2 working")
	require.NotContains(t, body, "paused")
}

func TestExplorer_PulseShimmersTheFrameAtWork(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	at := time.Unix(1_700_000_100, 0)
	head := func(after time.Duration) string {
		r := u.explorerRenderer()
		r.look = stackLook{now: at.Add(after), busy: true}
		return u.pulseLanes(r)[laneNow][0].head
	}

	require.Contains(t, stripANSI(head(0)), "fix-flaky-test/find-cause/try-free-port")
	require.NotEqual(t, head(0), head(240*time.Millisecond), "its colors move while the agent works")
	require.Equal(t, stripANSI(head(0)), stripANSI(head(240*time.Millisecond)), "and only its colors")
}

func TestExplorer_PulseLaneSaysWhatItHidesAboveAndBelow(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	items := make([]pulseItem, 8)
	for i := range items {
		items[i] = pulseItem{head: fmt.Sprintf("item %d", i), sub: "sub"}
	}
	u.explorer.lane, u.explorer.pulse = laneDone, 7
	lines := u.laneLines(laneDone, items, 60, 7)
	require.LessOrEqual(t, len(lines), 7)
	require.Contains(t, lines[1], "… 6 above")
	require.Contains(t, strings.Join(lines, "\n"), "item 7")
	require.NotContains(t, strings.Join(lines, "\n"), "more", "nothing is hidden under the last")

	u.explorer.pulse = 3
	lines = u.laneLines(laneDone, items, 60, 7)
	require.LessOrEqual(t, len(lines), 7)
	text := strings.Join(lines, "\n")
	require.Contains(t, text, "item 3")
	require.Contains(t, text, "above")
	require.Contains(t, text, "more")
}

func TestExplorer_PulseOffersEnterOnAFrameBeforeTheTabsKeys(t *testing.T) {
	t.Parallel()

	help := func(u *UI) []string {
		var descs []string
		for _, b := range u.ShortHelp() {
			descs = append(descs, b.Help().Desc)
		}
		return descs
	}
	u := pulseUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('2'))
	descs := help(u)
	require.Contains(t, descs, "open in tree")
	require.Less(t, slices.Index(descs, "open in tree"), slices.Index(descs, "tabs"), "the tab's own keys go first, where a narrow bar keeps them")

	press(u, typed('l'), typed('l'))
	require.NotContains(t, help(u), "open in tree", "a step up next is no frame to open")
}

func TestExplorer_PulseGivesAPopWithoutResultOneRow(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	observeRows(u, toolRow("f6", 1_700_000_060, memResult(t, "fc6", popWith("fix-flaky-test/find-cause/try-free-port", ""))))
	done := u.pulseLanes(u.explorerRenderer())[laneDone]
	require.Equal(t, 1, itemRows(slices.DeleteFunc(done, func(it pulseItem) bool { return it.frame.id != "fix-flaky-test/find-cause/try-free-port" })))
}

func TestExplorer_PulseSummarySitsAtTheFoot(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('2'))
	body := bodyOf(u)
	last := len(body) - 1
	for last > 0 && strings.TrimSpace(body[last]) == "" {
		last--
	}
	require.Contains(t, body[last], "working")
	require.Greater(t, last, len(body)-3)
}

func TestExplorer_PulseLanesShareTheRows(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 80, 26)
	u.memPulse.Asks = nil
	press(u, ctrlX, typed('2'))
	body := strings.Join(bodyOf(u), "\n")

	require.Contains(t, body, "shapes/circle", "a lane takes the rows the others leave")
	require.NotContains(t, body, "more")
}

// Lanes too tall for a short tab, or many questions waiting, are cut
// above the summary, which stays at the foot.
func TestExplorer_PulseKeepsItsSummaryWhenShort(t *testing.T) {
	t.Parallel()

	for _, height := range []int{10, 12, 15, 16} {
		u := pulseUI(t, 80, height)
		u.memPulse.Asks = nil
		press(u, ctrlX, typed('2'))
		body := bodyOf(u)
		require.Contains(t, body[len(body)-1], "0 waits on you", "%d rows", height)
	}

	u := pulseUI(t, 80, 24)
	for i := range 8 {
		u.memPulse.Asks = append(u.memPulse.Asks, gnovm.MemPulseItem{Name: fmt.Sprintf("q%d", i), Title: "which one?"})
	}
	press(u, ctrlX, typed('2'))
	body := bodyOf(u)
	require.Contains(t, body[len(body)-1], "9 waits on you")
}

func TestExplorer_PulseOpensAFrameInTheTree(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	press(u, ctrlX, typed('+'), typed('2'))
	require.Contains(t, lineWith(bodyOf(u), "› ◌ fix-flaky-test/find-cause/try-free-port"), "try-free-port", "the frame at work is selected")

	press(u, typed('l'))
	require.NotEmpty(t, lineWith(bodyOf(u), "› ✓"), "→ goes to the next lane")
	press(u, enter)

	require.Equal(t, explorerTree, u.explorer.tab)
	require.Equal(t, "fix-flaky-test/reproduce-it", u.selectedFrame().id)
}

func TestExplorer_MemPulseIsReadThroughTheWorkspace(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	pulse := gnovm.MemPulse{At: 7, Asks: []gnovm.MemPulseItem{{Name: "a", Title: "b"}}}
	u.com.Workspace = pulseWorkspace{u.com.Workspace.(*exportWorkspace), pulse}

	_, _ = u.Update(u.loadMemPulse()())
	require.Equal(t, pulse, u.memPulse)
	require.True(t, callsMem(toolRow("r", 1, memResult(t, "c", push("x", "y")))))
	require.False(t, callsMem(toolRow("r", 1, viewResult("c"))))
}

// pulseReads is exportWorkspace counting the reads of mem's pulse.
type pulseReads struct {
	*exportWorkspace
	n int
}

func (w *pulseReads) MemPulse(context.Context) (gnovm.MemPulse, error) {
	w.n++
	return gnovm.MemPulse{}, nil
}

// A pop marks every row it takes out, the gnovm_run rows among them, and
// each comes again with its mark: none is a new call to mem.
func TestExplorer_APopsMarksReadNoPulse(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	ws := &pulseReads{exportWorkspace: u.com.Workspace.(*exportWorkspace)}
	u.com.Workspace = ws

	marked := toolRow("f2", 1_700_000_010, memResult(t, "fc2", push("fix-flaky-test/reproduce-it", "make it fail")))
	marked.Popped = "fix-flaky-test/reproduce-it"
	_, cmd := u.Update(pubsub.Event[message.Message]{Type: pubsub.UpdatedEvent, Payload: marked})
	messagesOf(cmd)
	require.Zero(t, ws.n)

	_, cmd = u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: toolRow("f6", 1_700_000_060, memResult(t, "fc6"))})
	messagesOf(cmd)
	require.Equal(t, 1, ws.n, "a call's result is read after")
}

// pulseWorkspace is exportWorkspace whose mem says pulse.
type pulseWorkspace struct {
	*exportWorkspace
	pulse gnovm.MemPulse
}

func (w pulseWorkspace) MemPulse(context.Context) (gnovm.MemPulse, error) { return w.pulse, nil }

func TestExplorer_AFoldedTaskShowsADotAStep(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	observeRows(u, toolRow("f6", 1_700_000_060, memResult(t, "fc6",
		popWith("fix-flaky-test/find-cause/try-free-port", "port 0"), popWith("fix-flaky-test/find-cause", "a shared port"), popWith("fix-flaky-test", "fixed"))))
	press(u, ctrlX, typed('+'))

	require.Contains(t, lineWith(treeOf(u), "fix-flaky-test"), "✓ fix-flaky-test ●●", "two steps, folded")
}

func TestExplorer_AFoldedTaskSaysWhatItEndedWith(t *testing.T) {
	t.Parallel()

	for name, size := range map[string][2]int{"with bars": {200, 50}, "without": {100, 40}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			u := explorerUI(t, size[0], size[1])
			observeRows(u, toolRow("f6", 1_700_000_060, memResult(t, "fc6",
				popWith("fix-flaky-test/find-cause/try-free-port", "port 0"), popWith("fix-flaky-test/find-cause", "a shared port"), popWith("fix-flaky-test", "fixed: each test binds port 0"))))
			press(u, ctrlX, typed('+'))
			tree := treeOf(u)

			task := slices.IndexFunc(tree, func(l string) bool { return strings.Contains(l, "✓ fix-flaky-test") })
			require.Contains(t, tree[task+1], "fixed: each test binds port 0", "its result under it")

			press(u, typed('l'))
			require.Empty(t, lineWith(treeOf(u), "fixed: each test binds port 0"), "open, its frames show instead")
		})
	}

	u := explorerUI(t, 200, 50)
	observeRows(u,
		toolRow("f6", 1_700_000_060, memResult(t, "fc6",
			popWith("fix-flaky-test/find-cause/try-free-port", "port 0"), popWith("fix-flaky-test/find-cause", "a shared port"), popWith("fix-flaky-test", "fixed"))),
		toolRow("f7", 1_700_000_070, memResult(t, "fc7", push("update-readme", "update the readme"))),
		toolRow("f8", 1_700_000_080, memResult(t, "fc8", popWith("update-readme", "it already says 2.0.0"))),
	)
	observeRows(u, cutMsg("m9", "slide::fc7:fc8", 4000))
	press(u, ctrlX, typed('+'))
	tree := treeOf(u)
	require.NotEmpty(t, lineWith(tree, "⟲ slid"), "a slide of no frame stands at the top")
	task := slices.IndexFunc(tree, func(l string) bool { return strings.Contains(l, "✓ update-readme") })
	require.Less(t, task+1, len(tree), "a task that entered no frames has a line under it")
	require.Contains(t, tree[task+1], "it already says 2.0.0")
}

// askedHere adds to u's session the mem call fc9 that asked pulseUI's
// question.
func askedHere(t *testing.T, u *UI) {
	t.Helper()
	asked := toolRow("f9", 1_700_000_090, memResult(t, "fc9"))
	asked.Parts[0] = message.ToolResult{
		ToolCallID: "fc9", Name: "gnovm_run",
		Content: "asked `release-v2/ci-green/which-runner` \"which CI runner is the release one?\" (for the user)",
	}
	observeRows(u, callRow("e9", 1_700_000_090, memCall("fc9")), asked)
}

// The call that asked is found by what mem's Ask prints, read from mem's
// own transcript: a change to its wording fails here.
func TestAskCallFindsTheCallByWhatMemPrints(t *testing.T) {
	t.Parallel()

	story, err := os.ReadFile(filepath.Join("..", "..", "gnovm", "testdata", "gnomem", "v2", "story.golden"))
	require.NoError(t, err)
	_, after, ok := strings.Cut(string(story), "> s1: mem.Ask(\"release-v2/tag\", \"which key signs the tag?\")\n")
	require.True(t, ok, "the story asks which key signs the tag")
	printed, _, _ := strings.Cut(after, "\n>")

	u := explorerUI(t, 200, 50)
	res := memResult(t, "fc9")
	res.Content = printed
	observeRows(u, callRow("e9", 1_700_000_090, memCall("fc9")), toolRow("f9", 1_700_000_090, res))

	require.Equal(t, "fc9", u.askCall("release-v2/tag/which-key-signs-tag"), "mem printed: %s", printed)
}

func TestExplorer_PulseAClickOnWhatWaitsShowsTheCallThatAskedIt(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	askedHere(t, u)
	press(u, ctrlX, typed('+'), typed('2'))

	question := at(drawn(u), u.layout.explorer, "which CI runner")
	require.GreaterOrEqual(t, question.Y, 0)
	click(u, question.X+1, question.Y)
	require.True(t, u.chat.framed["fc9"], "the call that asked is marked in the chat")
	require.Equal(t, uiFocusEditor, u.focus, "and the keys go to the editor, for the answer")
	require.True(t, u.explorerBeside(), "a full explorer turns half, for the chat to show")
}

func TestExplorer_PulseSelectsWhatWaitsAboveTheLanes(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	askedHere(t, u)
	press(u, ctrlX, typed('2'), typed('k'))
	require.Contains(t, lineWith(bodyOf(u), "release-v2/ci-green/which-runner"), "›", "↑ from a lane's top selects what waits")
	require.Empty(t, lineWith(bodyOf(u), "› ◌"), "and no lane's entry")

	var descs []string
	for _, b := range u.ShortHelp() {
		descs = append(descs, b.Help().Desc)
	}
	require.Contains(t, descs, "answer")
	require.NotContains(t, descs, "open in tree")

	press(u, typed('j'))
	require.NotEmpty(t, lineWith(bodyOf(u), "› ◌ fix-flaky-test/find-cause/try-free-port"), "↓ goes back to the lane")

	press(u, typed('k'), enter)
	require.True(t, u.chat.framed["fc9"], "enter shows the call that asked it")
	require.Equal(t, uiFocusEditor, u.focus)
}

func TestExplorer_PulseMovesFromALaneOnceWhatWaitsIsAnswered(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	press(u, ctrlX, typed('2'), typed('k'))
	u.memPulse.Asks = nil
	require.NotEmpty(t, lineWith(bodyOf(u), "› ◌ fix-flaky-test/find-cause/try-free-port"), "answered, the lane's entry shows selected")

	press(u, typed('j'))
	require.Regexp(t, `› ◌ fix-flaky-test/find-cause\s`, strings.Join(bodyOf(u), "\n"), "↓ moves on from it")
}

func TestExplorer_PulseOffersAnAnswerOnlyForWhatThisSessionAsked(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	press(u, ctrlX, typed('2'), typed('k'))
	for _, b := range u.ShortHelp() {
		require.NotEqual(t, "answer", b.Help().Desc, "no call of this session asked it")
	}
}

// The answer is offered once the call that asked comes, and no longer
// once a rewind takes it.
func TestExplorer_PulseOffersAnAnswerOnceThisSessionAsks(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	press(u, ctrlX, typed('2'), typed('k'))
	answers := func() bool {
		return slices.ContainsFunc(u.ShortHelp(), func(b key.Binding) bool { return b.Help().Desc == "answer" })
	}
	require.False(t, answers())

	askedHere(t, u)
	require.True(t, answers(), "the call that asked came")

	_, _ = u.Update(pubsub.Event[message.Message]{Type: pubsub.DeletedEvent, Payload: message.Message{ID: "f9", SessionID: "s1"}})
	require.False(t, answers(), "a rewind took it")
}

func TestExplorer_PulseOpensTheFrameAtWorkFollowing(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	press(u, ctrlX, typed('2'), enter)
	require.Equal(t, explorerTree, u.explorer.tab)
	require.True(t, u.explorer.following, "the frame the agent is in is what following shows")
	require.Equal(t, "fix-flaky-test/find-cause/try-free-port", u.selectedFrame().id)
}

func TestExplorer_ShowingInTheChatLeavesAClosedExplorersSize(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	u.explorer.want = explorerFull
	u.showInChat([]string{"f5"}, "f5")
	require.Equal(t, explorerFull, u.explorer.want)
	require.False(t, u.explorer.open)
}

func TestSidebar_AClickOnWhatWaitsShowsTheCallThatAskedIt(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	askedHere(t, u)

	question := at(drawn(u), u.layout.sidebar, "which CI runner")
	click(u, question.X+1, question.Y)
	require.True(t, u.chat.framed["fc9"], "the call that asked is marked in the chat")
	require.Equal(t, uiFocusEditor, u.focus, "and the keys go to the editor, for the answer")
	require.False(t, u.explorer.open)
}

func TestSidebar_AClickOnWhatWaitsFromElsewhereOpensPulse(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	waits := at(drawn(u), u.layout.sidebar, "▲ 1 waits on you")
	click(u, waits.X+1, waits.Y)
	require.True(t, u.explorer.open, "no call of this session asked it")
	require.Equal(t, explorerPulse, u.explorer.tab)
}

func TestSidebarSaysWhatWaitsOnTheUser(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	rows := drawn(u)

	waits := at(rows, u.layout.sidebar, "▲ 1 waits on you")
	require.GreaterOrEqual(t, waits.Y, 0)
	require.Equal(t, waits.Y+1, at(rows, u.layout.sidebar, "which CI runner").Y, "the question under it")
	require.Less(t, waits.Y, at(rows, u.layout.sidebar, "Stack").Y, "above the Stack")

	u.memPulse.Asks = nil
	require.Equal(t, -1, at(drawn(u), u.layout.sidebar, "waits on you").Y, "nothing waits")
}

// forgettingWorkspace is pulseWorkspace whose /mem forgets what waits,
// writing mem's status again, as /mem forget, redact and undo can.
type forgettingWorkspace struct{ pulseWorkspace }

func (w *forgettingWorkspace) MemCommand(context.Context, string, string) (string, error) {
	w.pulse = gnovm.MemPulse{At: time.Now().Unix()}
	return "mem: forgot `release-v2/ci-green/which-runner`", nil
}

func TestSidebar_WhatWaitsIsReadAgainAfterMem(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	u.com.Workspace = &forgettingWorkspace{pulseWorkspace{u.com.Workspace.(*exportWorkspace), u.memPulse}}

	for _, msg := range messagesOf(u.sendMessage("/mem forget release-v2/ci-green/which-runner")) {
		_, _ = u.Update(msg)
	}
	press(u, esc)
	require.False(t, u.dialog.HasDialogs(), "the report is closed")
	require.Equal(t, -1, at(drawn(u), u.layout.sidebar, "waits on you").Y)
}

func TestSidebarShowsHowMuchTheModelSees(t *testing.T) {
	u := contextUI(t, 200, 50)
	line := at(drawn(u), u.layout.sidebar, "model sees")
	require.GreaterOrEqual(t, line.Y, 0)
	require.Contains(t, drawn(u)[line.Y], "▕██")
	require.Contains(t, drawn(u)[line.Y], "6K")

	unknown := explorerUI(t, 200, 50)
	require.Equal(t, -1, at(drawn(unknown), unknown.layout.sidebar, "model sees").Y, "no window known, no gauge")
}

// gaugeFill is the color the sidebar fills its gauge with, nil without a
// gauge.
func gaugeFill(u *UI) color.Color {
	scr := uv.NewScreenBuffer(u.width, u.height)
	u.Draw(scr, scr.Bounds())
	y := at(screenRows(scr, scr.Bounds()), u.layout.sidebar, "model sees").Y
	for x := u.layout.sidebar.Min.X; x < u.layout.sidebar.Max.X; x++ {
		if c := scr.CellAt(x, y); c != nil && c.Content == "█" {
			return c.Style.Fg
		}
	}
	return nil
}

func sameColor(a, b color.Color) bool {
	r1, g1, b1, a1 := a.RGBA()
	r2, g2, b2, a2 := b.RGBA()
	return r1 == r2 && g1 == g2 && b1 == b2 && a1 == a2
}

// What a pop took out after the count is not sent again: the gauge shows
// the count less it, and is past the slide point as the agent reckons.
func TestSidebar_ModelSeesTheCountLessWhatPopsTookOut(t *testing.T) {
	u := contextUI(t, 200, 50)
	observeRows(u,
		whole(callRow("g1", 1_700_000_050, memCall("gc1"))),
		toolRow("g2", 1_700_000_050, memResult(t, "gc1", push("g", "a big step"))),
		cutMsg("g3", "g", 60_000),
		whole(callRow("g4", 1_700_000_060, memCall("gc4"))),
		toolRow("g5", 1_700_000_060, memResult(t, "gc4", popWith("g", "done"))),
	)
	u.session.PromptTokens = 27_000 // counted for g4's request, past the slide point at 25.6K
	warning := u.com.Styles.LSP.WarningDiagnostic.GetForeground()

	rows := drawn(u)
	require.Contains(t, rows[at(rows, u.layout.sidebar, "model sees").Y], "12K", "the pop took 15K out")
	require.False(t, sameColor(gaugeFill(u), warning), "under the slide point once the pop is taken out")

	u.session.PromptTokens = 45_000
	require.True(t, sameColor(gaugeFill(u), warning), "past it even so")
}

func TestExplorer_DrawsInATerminalTooShortForIt(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX)
	for _, height := range []int{2, 1, 0} {
		u.height = height
		u.updateLayoutAndSize()
		require.NotPanics(t, func() { drawn(u) }, "%d rows", height)
	}
}

func TestExplorer_OutlivesItsFrames(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	observeRows(u, slidRows(t)[len(explorerRows(t)):]...)
	press(u, ctrlX)
	for _, id := range []string{"f1", "f2", "f4", "f5"} {
		_, _ = u.Update(pubsub.Event[message.Message]{Type: pubsub.DeletedEvent, Payload: message.Message{ID: id, SessionID: "s1"}})
	}
	require.Zero(t, u.frameEntries(), "a rewind took every push, not the rows a slide took")

	require.NotPanics(t, func() { drawn(u) })
	require.NotPanics(t, func() { press(u, typed('j'), typed('h'), enter) })
}

func TestExplorer_TakesTheKeysWhenItCoversTheEditor(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, tab)
	require.Equal(t, uiFocusEditor, u.focus)

	u.width = 120
	u.updateLayoutAndSize()

	require.Equal(t, explorerFull, u.explorerDrawn())
	require.Equal(t, uiFocusExplorer, u.focus, "keys typed to an editor out of sight would be lost")
}

func TestBarOfAPopRecordedBeforeItsPush(t *testing.T) {
	t.Parallel()

	r := stackRenderer{t: minimalFileStyles()}
	n := &frameNode{since: 100, until: 50, popped: true}

	bar := stripANSI(r.bar(n, timeAxis{from: 0, to: 200, width: 20}))

	require.Equal(t, 20, lipgloss.Width(bar), "a clock stepped back draws the pop at its push")
}

func TestExplorer_AnOlderPulseDoesNotReplaceANewerOne(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	_, _ = u.Update(memPulseMsg{gnovm.MemPulse{At: 10, Asks: []gnovm.MemPulseItem{{Name: "new"}}}})
	_, _ = u.Update(memPulseMsg{gnovm.MemPulse{At: 5}})

	require.EqualValues(t, 10, u.memPulse.At, "reads land in any order")
}

// mem without a status, as after an erasure that could not write it
// again, reads as an empty pulse: nothing is known to wait any more.
func TestSidebar_AnEmptyPulseClearsWhatWaits(t *testing.T) {
	t.Parallel()

	u := pulseUI(t, 200, 50)
	_, _ = u.Update(memPulseMsg{gnovm.MemPulse{}})

	require.Equal(t, -1, at(drawn(u), u.layout.sidebar, "waits on you").Y)
}

func TestExplorer_SearchFoldsBackWhatItOpened(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('k'), typed('k'), typed('k'), typed('h'))
	require.Empty(t, lineWith(treeOf(u), "try-free-port"))

	press(u, typed('/'), typed('p'), typed('o'), typed('r'), typed('t'))
	require.NotEmpty(t, lineWith(treeOf(u), "try-free-port"))
	press(u, typed('x'))
	require.Empty(t, lineWith(treeOf(u), "try-free-port"), "a query that moved on folds back what it opened")

	press(u, tea.KeyPressMsg{Code: tea.KeyBackspace})
	require.NotEmpty(t, lineWith(treeOf(u), "try-free-port"))
	press(u, esc)
	require.Empty(t, lineWith(treeOf(u), "try-free-port"), "and so does esc")
}

func TestExplorer_TreeKeepsALineARow(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	u.session.Todos = []session.Todo{{Content: "rerun the suite\n20 times", Status: session.TodoStatusPending}}
	observeRows(u,
		toolRow("f6", 1_700_000_060, memResult(t, "fc6",
			popWith("fix-flaky-test/find-cause/try-free-port", "port 0"), popWith("fix-flaky-test/find-cause", "a shared port"), popWith("fix-flaky-test", "fixed\nline two\nline three"))),
		toolRow("f7", 1_700_000_070, memResult(t, "fc7", push("update-readme", "update the readme"))),
	)
	press(u, ctrlX, typed('+'))
	tree := treeOf(u)

	require.NotEmpty(t, lineWith(tree, "fixed"))
	require.Empty(t, lineWith(tree, "line two"), "a result shows its first line")
	require.NotEmpty(t, lineWith(tree, "○ rerun the suite"))
	require.Empty(t, lineWith(tree, "20 times"), "and so does a step")
}

func TestListWindowFitsItsRoom(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ n, sel, room, per int }{
		{3, 1, 3, 2}, {3, 1, 2, 1}, {8, 7, 6, 2}, {8, 3, 6, 2}, {8, 0, 6, 2}, {20, 10, 7, 1}, {2, 0, 4, 2},
	} {
		w := listWindow(tc.n, tc.sel, tc.room, tc.per)
		used := w.shown * tc.per
		if w.above != "" {
			used++
		}
		if w.below != "" {
			used++
		}
		require.LessOrEqual(t, used, max(tc.room, tc.per+1), "%+v", tc)
		require.True(t, tc.sel < 0 || tc.sel >= w.first && tc.sel < w.first+w.shown, "the selection shows: %+v", tc)
		hidden := tc.n - w.shown
		require.Equal(t, hidden > 0, w.above != "" || w.below != "", "what it hides is said: %+v", tc)
	}
	require.Equal(t, "… 1 above · 1 more", listWindow(3, 1, 3, 2).below, "with no room for two, both on one row")
}

func TestExplorer_TreeColumnsHoldWhenAFrameFolds(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	observeRows(u,
		callRow("e6", 1_700_000_050, memCall("fc6")),
		toolRow("f6", 1_700_000_050, memResult(t, "fc6", push("fix-flaky-test/find-cause/try-free-port/bind-each-listener-to-port-zero", "bind each to port 0"))),
	)
	press(u, ctrlX, typed('+'))
	before := column(lineWith(treeOf(u), "frame"), "0s")

	press(u, typed('k'), typed('k'), typed('k'), typed('k'), typed('h'))
	require.Empty(t, lineWith(treeOf(u), "try-free-port"), "fix-flaky-test is folded")
	require.Equal(t, before, column(lineWith(treeOf(u), "frame"), "0s"), "the axis stays where it was")
}

func TestExplorer_HeaderNamesTheSession(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	u.session.Title = "release v2, then the flaky test"
	press(u, ctrlX, typed('+'))
	require.GreaterOrEqual(t, at(drawn(u), u.layout.explorer, `session "release v2, then the flaky test"`).Y, 0)
}

func TestExplorer_DetailTimesToTheSecondAndSaysWhatTheChatShows(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('k'), typed('k'))
	require.Equal(t, "fix-flaky-test/reproduce-it", u.selectedFrame().id)
	body := strings.Join(bodyOf(u), "\n")
	require.Regexp(t, `ran\s+\d\d:\d\d:\d\d → \d\d:\d\d:\d\d · 20s`, body)
	require.Contains(t, body, "thoughts  none")
	require.NotContains(t, body, "the chat shows its rows")

	press(u, enter)
	require.Contains(t, strings.Join(bodyOf(u), "\n"), "the chat shows its rows, marked ▌")
}

func TestExplorer_DetailSaysWhatTheChatShowsUnderAFullTree(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	for i := range 40 {
		id := fmt.Sprintf("fix-flaky-test/find-cause/try-free-port/step-%d", i)
		at := 1_700_000_050 + int64(i)
		call := fmt.Sprintf("s%d", i)
		observeRows(u, callRow("c"+call, at, memCall(call)), toolRow("t"+call, at, memResult(t, call, push(id, "a step"), popWith(id, "done"))))
	}
	press(u, ctrlX, typed('k'), typed('k'), enter)
	require.Equal(t, "fix-flaky-test/reproduce-it", u.selectedFrame().id)

	tree := treeOf(u)
	require.NotEmpty(t, strings.TrimSpace(tree[len(tree)-1]), "the tree has no row to spare")
	require.Contains(t, strings.Join(bodyOf(u), "\n"), "the chat shows its rows, marked ▌")
}

func TestExplorer_DetailFollowsNewWorkInTheFrame(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX)
	require.GreaterOrEqual(t, at(drawn(u), u.layout.explorer, "work      nothing yet").Y, 0)

	observeRows(u,
		callRow("e8", 1_700_000_050, toolCall("ed1", "edit", `{"file_path":"/x/a.go"}`)),
		toolRow("f8", 1_700_000_050, editResult("ed1")),
	)

	rows := drawn(u)
	require.GreaterOrEqual(t, at(rows, u.layout.explorer, "edit 1").Y, 0)
	require.GreaterOrEqual(t, at(rows, u.layout.explorer, "/x/a.go").Y, 0)
}

func TestExplorer_DetailComesUnderAShortTree(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX, typed('+'))

	tree := treeOf(u)
	require.NotEmpty(t, strings.TrimSpace(tree[len(tree)-1]), "no blank rows between the tree and its detail")
}

// askYesNo has the agent ask text through the UI, and is what the user
// answered: empty while the question waits.
func askYesNo(t *testing.T, u *UI, text string) *[]question.Answer {
	t.Helper()
	_, _ = u.Update(pubsub.Event[question.Request]{Type: pubsub.CreatedEvent, Payload: question.Request{
		ID:        "q1",
		SessionID: u.session.ID,
		Questions: []question.Question{{ID: "ok", Type: question.TypeYesNo, Text: text}},
	}})
	form, ok := u.activeInline.(*dialog.QuestionForm)
	require.True(t, ok, "the question waits in the editor")
	var answered []question.Answer
	form.OnAnswer = func(a []question.Answer) { answered = a }
	return &answered
}

func wheelAt(u *UI, x, y int, deltaY float64) {
	_, _ = u.Update(common.CoalescedWheelMsg{Mouse: tea.Mouse{X: x, Y: y}, DeltaY: deltaY})
}

// The agent asks while the explorer covers the editor.
func TestExplorer_AClickOnItFullLeavesTheEditorUnderIt(t *testing.T) {
	t.Parallel()

	bare := explorerUI(t, 150, 50)
	askYesNo(t, bare, "Ship it?")
	yes := at(drawn(bare), bare.layout.editor, "Yes")
	require.GreaterOrEqual(t, yes.Y, 0)

	u := explorerUI(t, 150, 50)
	press(u, ctrlX)
	answered := askYesNo(t, u, "Ship it?")
	require.Equal(t, explorerFull, u.explorerDrawn())
	require.Equal(t, -1, at(drawn(u), u.layout.area, "Yes").Y, "the explorer covers the question")
	require.Equal(t, bare.layout.editor, u.layout.editor, "where the Yes is under it")
	click(u, yes.X+1, yes.Y)

	require.Empty(t, *answered, "the Yes under the explorer is not pressed")
	require.NotNil(t, u.activeInline, "the question still waits")
}

func TestExplorer_TheWheelOnItFullLeavesTheEditorUnderIt(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 150, 50)
	press(u, ctrlX)
	askYesNo(t, u, "Ship it?")
	drawn(u)
	under := u.layout.editor.Min.Add(image.Pt(1, 1))
	require.True(t, under.In(u.layout.explorer))

	wheelAt(u, under.X, under.Y, -1)

	require.Equal(t, "fix-flaky-test/find-cause", u.selectedFrame().id, "the wheel moves the explorer's selection")
}

// Some terminals, Ghostty among them, send a notch of the wheel as 3.
func TestExplorer_TheWheelMovesOneEntryANotch(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX)
	on := u.layout.explorer.Min.Add(image.Pt(1, explorerBodyRow))

	wheelAt(u, on.X, on.Y, -3)
	require.Equal(t, "fix-flaky-test/find-cause", u.selectedFrame().id)
	wheelAt(u, on.X, on.Y, 3)
	require.Equal(t, "fix-flaky-test/find-cause/try-free-port", u.selectedFrame().id)
}

func TestExplorer_BesideTheChatItLeavesTheSidebarNoFocus(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 30)
	drawn(u)
	require.True(t, u.sidebarScrollable, "the sidebar is long enough to take the keys")

	press(u, ctrlX, tab)
	drawn(u)
	press(u, tab)
	drawn(u)
	require.True(t, u.explorerBeside())
	require.Equal(t, uiFocusMain, u.focus)

	press(u, typed('l'))
	require.Equal(t, uiFocusMain, u.focus, "l finds no sidebar to go to")
	press(u, tab)
	require.Equal(t, uiFocusEditor, u.focus, "tab goes round to the editor")
}

func TestExplorer_BesideTheChatAQuestionFitsTheEditor(t *testing.T) {
	t.Parallel()

	u := explorerUI(t, 200, 50)
	press(u, ctrlX)
	askYesNo(t, u, strings.Repeat("Should the release wait for the flaky test? ", 9))
	require.True(t, u.explorerBeside())

	require.GreaterOrEqual(t, at(drawn(u), u.layout.editor, "Yes").Y, 0, "the question's buttons are drawn")
}

func TestExplorer_FullHelpListsItsKeys(t *testing.T) {
	t.Parallel()

	ctrlG := tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}
	for name, asking := range map[string]bool{"alone": false, "over a question": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			u := explorerUI(t, 150, 50)
			press(u, ctrlX)
			if asking {
				askYesNo(t, u, "Ship it?")
			}
			require.Equal(t, uiFocusExplorer, u.focus)
			press(u, ctrlG)

			rows := drawn(u)
			for _, want := range []string{"close", "tabs", "full/half", "less"} {
				require.GreaterOrEqual(t, at(rows, u.layout.status, want).Y, 0, "the help lists %q", want)
			}
			require.Equal(t, -1, at(rows, u.layout.status, "commands").Y, "and none of the chat's keys")
		})
	}
}
