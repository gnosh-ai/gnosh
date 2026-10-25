package dialog

import (
	"fmt"
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The keys the rewind dialogs are driven with, besides enterKey.
var (
	downKey = tea.KeyPressMsg{Code: tea.KeyDown}
	upKey   = tea.KeyPressMsg{Code: tea.KeyUp}
	escKey  = tea.KeyPressMsg{Code: tea.KeyEscape}
)

// The rewind list shows each prompt as its first line, or what was
// attached to it, and starts on the latest: Enter asks about the one
// chosen, ↑ and ↓ choose, and Esc closes it. A long list scrolls to keep
// the chosen prompt in view.
func TestRewindListsThePrompts(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{Styles: &sty}
	prompts := []RewindPrompt{
		{MessageID: "m1", Text: "make a.txt"},
		{MessageID: "m2", Text: "change a.txt\nand b.txt"},
		{MessageID: "m3", Attachments: []message.Attachment{{FileName: "shot.png"}}},
	}
	d := NewRewind(com, "s1", prompts)
	scr := uv.NewScreenBuffer(100, 30)
	d.Draw(scr, image.Rect(0, 0, 100, 30))
	view := ansi.Strip(scr.Render())
	for _, want := range []string{"Rewind", "Restore the code and/or conversation to the point before…", "make a.txt", "change a.txt …", "(1 attached)"} {
		require.Contains(t, view, want)
	}

	require.Equal(t, ActionRewindPick{SessionID: "s1", Prompt: prompts[2]}, d.HandleMsg(enterKey))
	for range 3 {
		d.HandleMsg(upKey)
	}
	require.Equal(t, ActionRewindPick{SessionID: "s1", Prompt: prompts[0]}, d.HandleMsg(enterKey), "stays on the first")
	d.HandleMsg(downKey)
	require.Equal(t, ActionRewindPick{SessionID: "s1", Prompt: prompts[1]}, d.HandleMsg(enterKey))
	require.Equal(t, ActionClose{}, d.HandleMsg(escKey))

	many := make([]RewindPrompt, 30)
	for i := range many {
		many[i] = RewindPrompt{MessageID: fmt.Sprint(i), Text: fmt.Sprintf("prompt %02d", i)}
	}
	d = NewRewind(com, "s1", many)
	shown := func() string {
		scr := uv.NewScreenBuffer(100, 40)
		d.Draw(scr, image.Rect(0, 0, 100, 40))
		return ansi.Strip(scr.Render())
	}
	require.Contains(t, shown(), "prompt 29")
	require.NotContains(t, shown(), "prompt 17")
	for range 20 {
		d.HandleMsg(upKey)
	}
	require.Contains(t, shown(), "prompt 09")
	require.NotContains(t, shown(), "prompt 29")
}

// With files to restore, the dialog offers the code and the conversation
// (first), either, or nothing, and says what each does as Claude Code
// says it; without, the conversation or nothing. Esc goes back.
func TestRewindConfirmOffersWhatToRestore(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{Styles: &sty}
	prompt := RewindPrompt{MessageID: "m2", Text: "change a.txt"}
	files := []string{"/w/a.go", "/w/b.go", "/w/c.go"}
	choose := func(files []string, downs int) Action {
		d := NewRewindConfirm(com, "s1", prompt, files, 3, 1)
		for range downs {
			d.HandleMsg(downKey)
		}
		return d.HandleMsg(enterKey)
	}
	require.Equal(t, ActionRewindTo{SessionID: "s1", Prompt: prompt, Conversation: true, Code: true}, choose(files, 0))
	require.Equal(t, ActionRewindTo{SessionID: "s1", Prompt: prompt, Conversation: true}, choose(files, 1))
	require.Equal(t, ActionRewindTo{SessionID: "s1", Prompt: prompt, Code: true}, choose(files, 2))
	require.Equal(t, ActionClose{}, choose(files, 3), "never mind")
	require.Equal(t, ActionRewindTo{SessionID: "s1", Prompt: prompt, Conversation: true}, choose(nil, 0))
	require.Equal(t, ActionClose{}, choose(nil, 1), "never mind")
	require.Equal(t, ActionClose{}, NewRewindConfirm(com, "s1", prompt, files, 3, 1).HandleMsg(escKey))

	d := NewRewindConfirm(com, "s1", prompt, files, 3, 1)
	require.Equal(t, []string{"The conversation will be forked.", "The code will be restored +3 -1 in a.go and 2 other files."}, d.said(rewindBoth))
	require.Equal(t, []string{"The conversation will be forked.", "The code will be unchanged."}, d.said(rewindConversation))
	require.Equal(t, []string{"The conversation will be unchanged.", "The code will be restored +3 -1 in a.go and 2 other files."}, d.said(rewindCode))
	require.Equal(t, []string{"The conversation will be unchanged.", "The code will be unchanged."}, d.said(rewindNevermind))
	require.Equal(t, "a.go", NewRewindConfirm(com, "s1", prompt, files[:1], 0, 0).where())
	require.Equal(t, "a.go and b.go", NewRewindConfirm(com, "s1", prompt, files[:2], 0, 0).where())

	scr := uv.NewScreenBuffer(120, 30)
	d.Draw(scr, image.Rect(0, 0, 120, 30))
	view := ansi.Strip(scr.Render())
	for _, want := range []string{
		"Confirm you want to restore to the point before you sent this message:",
		"│ change a.txt",
		"Restore code and conversation", "Restore conversation", "Restore code", "Never mind",
		"The code will be restored +3 -1 in a.go and 2 other files.",
		"Rewinding does not affect files edited manually, via bash, or downloaded.",
	} {
		require.Contains(t, view, want)
	}
	scr = uv.NewScreenBuffer(120, 30)
	NewRewindConfirm(com, "s1", prompt, nil, 0, 0).Draw(scr, image.Rect(0, 0, 120, 30))
	view = ansi.Strip(scr.Render())
	require.Contains(t, view, "Confirm you want to restore the conversation to the point before")
	require.NotContains(t, view, "Restore code")
	require.NotContains(t, view, "Rewinding does not affect")
}
