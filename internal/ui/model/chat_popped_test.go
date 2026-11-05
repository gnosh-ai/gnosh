package model

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/chat"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// reply is a round of the agent ending in tool calls, as the rounds inside
// a frame do.
func reply(id, text, popped string) *message.Message {
	return &message.Message{
		ID: id, SessionID: "s1", Role: message.Assistant, Popped: popped,
		Parts: []message.ContentPart{message.TextContent{Text: text}, message.Finish{Reason: message.FinishReasonToolUse}},
	}
}

// chatRows draws u's chat and returns each screen row with whether its
// text is drawn faint.
func chatRows(t *testing.T, u *UI) (rows []string, faint []bool) {
	t.Helper()
	u.chat.SetSize(80, 30)
	out := u.chat.list.Render()
	buf := uv.NewScreenBuffer(80, strings.Count(out, "\n")+1)
	uv.NewStyledString(out).Draw(&buf, buf.Bounds())
	for y := range buf.Height() {
		var b strings.Builder
		dim := false
		for x := range buf.Width() {
			c := buf.CellAt(x, y)
			if c == nil {
				continue
			}
			b.WriteString(c.Content)
			if strings.TrimSpace(c.Content) != "" && c.Style.Attrs&uv.AttrFaint != 0 {
				dim = true
			}
		}
		rows = append(rows, b.String())
		faint = append(faint, dim)
	}
	return rows, faint
}

// rowWith is the index of the first row holding text, or -1.
func rowWith(rows []string, text string) int {
	return slices.IndexFunc(rows, func(r string) bool { return strings.Contains(r, text) })
}

func TestChatDimsTheRowsAPopTookOutOfTheModelsContext(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	before := reply("a1", "entering bump-version", "")
	inside := reply("a2", "reading the version file", "release-v2/bump-version")
	after := reply("a3", "popped it, next", "")
	u.chat.SetMessages(
		chat.NewAssistantMessageItem(u.com.Styles, before),
		chat.NewAssistantMessageItem(u.com.Styles, inside),
		chat.NewAssistantMessageItem(u.com.Styles, after),
	)
	for _, msg := range []*message.Message{before, inside, after} {
		u.chat.MarkPopped(*msg)
	}

	rows, faint := chatRows(t, u)
	in := rowWith(rows, "reading the version file")
	require.GreaterOrEqual(t, in, 1)
	require.True(t, faint[in], "the popped reply is drawn faint")
	require.False(t, faint[rowWith(rows, "entering bump-version")])
	require.False(t, faint[rowWith(rows, "popped it, next")])
	label := rowWith(rows, "popped by `release-v2/bump-version`")
	require.Equal(t, in-1, label, "the gap above the popped rows says which pop took them out")
	require.Equal(t, 1, strings.Count(strings.Join(rows, "\n"), "popped by"), "one label per run")

	// The mark goes, and with it the dimming and the label.
	inside.Popped = ""
	u.chat.MarkPopped(*inside)
	rows, faint = chatRows(t, u)
	require.False(t, faint[rowWith(rows, "reading the version file")])
	require.Equal(t, -1, rowWith(rows, "popped by"))
}

func TestChatLabelsADeepPopByItsLastName(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	before := reply("a1", "trying a free port", "")
	inside := reply("a2", "reading the notes", "fix-flaky-test/find-cause/try-free-port")
	u.chat.SetMessages(chat.NewAssistantMessageItem(u.com.Styles, before), chat.NewAssistantMessageItem(u.com.Styles, inside))
	u.chat.MarkPopped(*inside)

	rows, _ := chatRows(t, u)
	require.GreaterOrEqual(t, rowWith(rows, "popped by `…/try-free-port`"), 0)
}

func TestChatLabelsWhatASlideTookOut(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	slid := reply("a1", "an early round", "slide::c1:c4")
	u.chat.SetMessages(
		chat.NewAssistantMessageItem(u.com.Styles, reply("a0", "the prompt's answer", "")),
		chat.NewAssistantMessageItem(u.com.Styles, slid),
	)
	u.chat.MarkPopped(*slid)

	rows, faint := chatRows(t, u)
	require.True(t, faint[rowWith(rows, "an early round")])
	require.GreaterOrEqual(t, rowWith(rows, "slid out of the model's context"), 0)
}

func TestPoppedItemIDs(t *testing.T) {
	t.Parallel()

	row := message.Message{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: "x"},
		message.ToolCall{ID: "call-1", Name: "view"},
		message.ToolCall{ID: "call-2", Name: "ls"},
	}}
	results := message.Message{ID: "t1", Role: message.Tool, Parts: []message.ContentPart{
		message.ToolResult{ToolCallID: "call-1"},
	}}

	require.ElementsMatch(t, []string{"a1", chat.AssistantInfoID("a1"), "call-1", "call-2"}, poppedItemIDs(row))
	require.ElementsMatch(t, []string{"t1", "call-1"}, poppedItemIDs(results))
}

// poppedItemIDs names every item the chat makes for a row, whatever its
// kind.
func TestPoppedItemIDsNameEveryItemTheChatMakes(t *testing.T) {
	t.Parallel()

	sty := newTestUI().com.Styles
	rows := map[string]message.Message{
		"prompt": {ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "fix it"}}},
		"notice": {ID: "u2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go on", Hidden: true, Notice: true}}},
		"shell commands": {ID: "u3", Role: message.User, Parts: []message.ContentPart{
			message.ShellCommand{Command: "go test ./...", Output: "ok"},
			message.ShellCommand{Command: "git status", Output: "clean"},
		}},
		"reply and calls": {ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: "x"}, message.ToolCall{ID: "call-1", Name: "view"},
		}},
		"calls alone": {ID: "a2", Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "call-2", Name: "ls"}}},
	}
	for name, row := range rows {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			items := chat.ExtractMessageItems(sty, &row, nil, "")
			require.NotEmpty(t, items)
			for _, item := range items {
				require.Contains(t, poppedItemIDs(row), item.ID())
			}
		})
	}
}

// A bang command run inside a frame is dimmed with the frame's rows.
func TestChatDimsAPoppedShellCommand(t *testing.T) {
	t.Parallel()

	u, _ := sidebarUI(t, 60)
	_, _ = u.Update(loadSessionMsg{session: &session.Session{ID: "s1", Title: "release"}, messages: []message.Message{
		*reply("a0", "entering fix-test", ""),
		*reply("a1", "running the tests first", "fix-test"),
		{ID: "u1", SessionID: "s1", Role: message.User, Popped: "fix-test", Parts: []message.ContentPart{
			message.ShellCommand{Command: "go test ./...", Output: "ok  release 0.4s"},
		}},
		*reply("a2", "reading the log after", "fix-test"),
	}})

	rows, faint := chatRows(t, u)
	require.True(t, faint[rowWith(rows, "go test ./...")], "the command is drawn faint")
	require.True(t, faint[rowWith(rows, "ok  release 0.4s")], "and its output")
	require.Equal(t, 1, strings.Count(strings.Join(rows, "\n"), "popped by"), "under the run's one label")
}

// A new session's chat starts with nothing dimmed.
func TestChatSetMessagesForgetsTheMarks(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	inside := reply("a2", "reading the version file", "release-v2/bump-version")
	u.chat.SetMessages(chat.NewAssistantMessageItem(u.com.Styles, inside))
	u.chat.MarkPopped(*inside)
	u.chat.ShowFrame([]string{"a2"})

	u.chat.SetMessages(chat.NewAssistantMessageItem(u.com.Styles, reply("a2", "reading the version file", "")))
	rows, faint := chatRows(t, u)
	require.NotContains(t, faint, true)
	require.NotContains(t, strings.Join(rows, "\n"), "▌", "nor the frame the explorer showed")
}

func TestChatShowFrameMarksItsItemsWithAGutter(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.chat.SetMessages(
		chat.NewAssistantMessageItem(u.com.Styles, reply("a1", "entering bump-version", "")),
		chat.NewAssistantMessageItem(u.com.Styles, reply("a2", "reading the version file", "")),
	)

	u.chat.ShowFrame([]string{"a2"})
	rows, _ := chatRows(t, u)
	require.True(t, strings.HasPrefix(rows[rowWith(rows, "reading the version file")], "▌"))
	require.False(t, strings.HasPrefix(rows[rowWith(rows, "entering bump-version")], "▌"))

	u.chat.ShowFrame(nil)
	rows, _ = chatRows(t, u)
	require.NotContains(t, strings.Join(rows, "\n"), "▌")
}

// A session loaded from its rows dims the ones its pops marked.
func TestChatDimsTheMarkedRowsOfALoadedSession(t *testing.T) {
	t.Parallel()

	u, _ := sidebarUI(t, 60)
	_, _ = u.Update(loadSessionMsg{session: &session.Session{ID: "s1", Title: "release"}, messages: []message.Message{
		*reply("a1", "entering bump-version", ""),
		*reply("a2", "reading the version file", "release-v2/bump-version"),
	}})

	rows, faint := chatRows(t, u)
	require.True(t, faint[rowWith(rows, "reading the version file")])
	require.False(t, faint[rowWith(rows, "entering bump-version")])
}

// The marks arrive as the rows are updated, the way a pop marks them.
func TestChatDimsRowsAsTheirPopArrives(t *testing.T) {
	t.Parallel()

	u, _ := sidebarUI(t, 60)
	send := func(typ pubsub.EventType, msg *message.Message) {
		t.Helper()
		_, _ = u.Update(pubsub.Event[message.Message]{Type: typ, Payload: *msg})
	}
	inside := reply("a2", "reading the version file", "")
	send(pubsub.CreatedEvent, reply("a1", "entering bump-version", ""))
	send(pubsub.CreatedEvent, inside)
	rows, faint := chatRows(t, u)
	require.False(t, faint[rowWith(rows, "reading the version file")])

	inside.Popped = "release-v2/bump-version"
	send(pubsub.UpdatedEvent, inside)
	rows, faint = chatRows(t, u)
	require.True(t, faint[rowWith(rows, "reading the version file")])
	require.GreaterOrEqual(t, rowWith(rows, "popped by `release-v2/bump-version`"), 0)
}
