package model

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// rewindCall is a rewind a workspace was asked for.
type rewindCall struct {
	sessionID, messageID string
	opts                 app.RewindOptions
}

// rewindWorkspace lists a session's messages and rewinds it, recording
// what it was asked, and the sessions asked for to load, loading none.
type rewindWorkspace struct {
	workspace.Workspace
	msgs   []message.Message
	asked  []rewindCall
	result app.RewindResult
	err    error
	loaded []string
}

func (w *rewindWorkspace) ListMessages(context.Context, string) ([]message.Message, error) {
	return w.msgs, nil
}

func (w *rewindWorkspace) Rewind(_ context.Context, sessionID, messageID string, opts app.RewindOptions) (app.RewindResult, error) {
	w.asked = append(w.asked, rewindCall{sessionID, messageID, opts})
	return w.result, w.err
}

func (w *rewindWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	w.loaded = append(w.loaded, id)
	return session.Session{}, errors.New("not loaded in this test")
}

func (w *rewindWorkspace) SetCurrentSession(context.Context, string) error { return nil }

func (w *rewindWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// reported runs cmd's tree and returns the messages it reports.
func reported(cmd tea.Cmd) []util.InfoMsg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		var out []util.InfoMsg
		for _, c := range msg {
			out = append(out, reported(c)...)
		}
		return out
	case util.InfoMsg:
		return []util.InfoMsg{msg}
	}
	return nil
}

// The prompts /rewind lists are the user's, oldest first, from after the
// summary of a compacted session, with what was attached; hidden ones,
// empty ones and the rest of the conversation are not.
func TestRewindPrompts(t *testing.T) {
	t.Parallel()

	user := func(id, text string, hidden bool, parts ...message.ContentPart) message.Message {
		return message.Message{ID: id, Role: message.User, Parts: append([]message.ContentPart{message.TextContent{Text: text, Hidden: hidden}}, parts...)}
	}
	msgs := []message.Message{
		user("u0", "before the summary", false),
		{ID: "sum", Role: message.Assistant, IsSummaryMessage: true, Parts: []message.ContentPart{message.TextContent{Text: "Summary."}}},
		user("u1", "make a.txt", false),
		{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Made it."}}},
		user("u2", "keep going", true),
		user("u3", "", false, message.BinaryContent{Path: "/tmp/shot.png", MIMEType: "image/png", Data: []byte("png")}),
		user("u4", "", false),
	}
	require.Equal(t, []dialog.RewindPrompt{
		{MessageID: "u1", Text: "make a.txt"},
		{MessageID: "u3", Attachments: []message.Attachment{{FilePath: "/tmp/shot.png", FileName: "shot.png", MimeType: "image/png", Content: []byte("png")}}},
	}, rewindPrompts(msgs, "sum"))
	require.Len(t, rewindPrompts(msgs, ""), 3, "with no summary, from the start")
}

// /rewind, or /checkpoint or /undo, typed or from the palette, lists the
// session's prompts; with no session, no prompt to go back to, or a busy
// agent, it says why not.
func TestOpenRewind(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &rewindWorkspace{}
	u.com.Workspace = ws

	require.Equal(t, util.NewWarnMsg("Nothing to rewind to yet."), u.sendMessage("/rewind")())
	u.session = &session.Session{ID: "s1"}
	require.Equal(t, util.NewWarnMsg("Nothing to rewind to yet."), u.sendMessage("/undo")(), "no prompts")
	ws.msgs = []message.Message{{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "make a.txt"}}}}
	want := rewindPromptsMsg{sessionID: "s1", prompts: []dialog.RewindPrompt{{MessageID: "u1", Text: "make a.txt"}}}
	require.Equal(t, want, u.sendMessage("/checkpoint")())
	require.Equal(t, want, u.handleDialogAction(dialog.ActionRewind{SessionID: "s1"})())

	u.agentBusyCache.set(true)
	require.Equal(t, util.NewWarnMsg("Agent is busy, please wait before rewinding..."), u.sendMessage("/rewind")())
	require.Equal(t, util.NewWarnMsg("Agent is busy, please wait before rewinding..."), u.rewindTo(dialog.ActionRewindTo{SessionID: "s1", Conversation: true})())
}

// Choosing a prompt asks what restoring the code to the point before it
// changes, and that opens the dialog asking what to restore, over the list
// it was chosen from and once. Choosing there rewinds and closes both.
func TestRewindChoosing(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &rewindWorkspace{result: app.RewindResult{Files: []string{"/w/a.go"}, Additions: 2, Removals: 1}}
	u.com.Workspace = ws
	u.session = &session.Session{ID: "s1"}
	prompt := dialog.RewindPrompt{MessageID: "u1", Text: "make a.txt"}

	preview := rewindPreviewMsg{sessionID: "s1", prompt: prompt, result: ws.result}
	require.Equal(t, preview, u.handleDialogAction(dialog.ActionRewindPick{SessionID: "s1", Prompt: prompt})())
	require.Equal(t, []rewindCall{{"s1", "u1", app.RewindOptions{DryRun: true}}}, ws.asked)

	u.Update(preview)
	require.False(t, u.dialog.ContainsDialog(dialog.RewindConfirmID), "the list is gone")
	u.Update(rewindPromptsMsg{sessionID: "s1", prompts: []dialog.RewindPrompt{prompt}})
	require.True(t, u.dialog.ContainsDialog(dialog.RewindID))
	u.Update(preview)
	u.Update(preview)
	require.True(t, u.dialog.ContainsDialog(dialog.RewindConfirmID))
	u.dialog.CloseFrontDialog()
	require.False(t, u.dialog.ContainsDialog(dialog.RewindConfirmID), "opened once")
	require.True(t, u.dialog.ContainsDialog(dialog.RewindID), "back at the list")
	u.Update(preview)

	to := dialog.ActionRewindTo{SessionID: "s1", Prompt: prompt, Conversation: true, Code: true}
	require.Equal(t, rewoundMsg{sessionID: "s1", prompt: prompt, conversation: true, code: true, result: ws.result}, u.handleDialogAction(to)())
	require.Equal(t, rewindCall{"s1", "u1", app.RewindOptions{Conversation: true, Code: true}}, ws.asked[len(ws.asked)-1])
	require.False(t, u.dialog.ContainsDialog(dialog.RewindConfirmID))
	require.False(t, u.dialog.ContainsDialog(dialog.RewindID))

	ws.err = errors.New("disk full")
	for _, cmd := range []tea.Cmd{u.previewRewind("s1", prompt), u.rewindTo(to)} {
		info, ok := cmd().(util.InfoMsg)
		require.True(t, ok)
		require.Equal(t, util.InfoTypeError, info.Type)
		require.Equal(t, "failed to rewind: disk full", info.Msg)
	}
}

// After the conversation is rewound, the session is loaded again and the
// prompt goes back in the editor with what was attached to it, in place
// of what was there, as Claude Code puts it back; restoring only the code
// leaves the editor as it is. Each says what it did, and warns of the
// files it left as they are.
func TestRewound(t *testing.T) {
	pinTTLs(t)
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.attachments = attachments.New(nil, attachments.Keymap{})
	ws := &rewindWorkspace{}
	u.com.Workspace = ws
	warmCaches(u, false)
	u.session = &session.Session{ID: "s1"}
	rewound := func(msg rewoundMsg) []util.InfoMsg {
		_, cmd := u.Update(msg)
		return reported(cmd)
	}
	shot := message.Attachment{FilePath: "/tmp/shot.png", FileName: "shot.png", MimeType: "image/png", Content: []byte("png")}
	prompt := dialog.RewindPrompt{MessageID: "u2", Text: "change a.txt", Attachments: []message.Attachment{shot}}

	code := rewoundMsg{sessionID: "s1", prompt: prompt, code: true, result: app.RewindResult{Files: []string{"/w/a.go"}, Additions: 2, Removals: 1}}
	u.textarea.SetValue("half a thought")
	old := message.Attachment{FileName: "old.png"}
	u.attachments.Update(old)
	require.Equal(t, []util.InfoMsg{util.NewInfoMsg("Restored the code in 1 file (+2 -1).")}, rewound(code))
	require.Equal(t, "half a thought", u.textarea.Value())
	require.Equal(t, []message.Attachment{old}, u.attachments.List())
	require.Empty(t, ws.loaded)
	require.Equal(t, "The code had not changed.", rewoundMsg{code: true}.said())

	both := rewoundMsg{sessionID: "s1", prompt: prompt, conversation: true, code: true, result: app.RewindResult{
		Files: []string{"/w/a.go", "/w/b.go"}, Additions: 3, Removals: 1,
		Skipped: []string{"/w/c.go: a symbolic link", "/w/d.go: not a file now"},
		Kept:    session.Session{ID: "k1", Title: "fix (before rewind)"},
	}}
	said := `Rewound the conversation to before your message; the whole of it is kept as "fix (before rewind)" (/resume k1). ` +
		`Restored the code in 2 files (+3 -1). Skipped 2 files, left as they are: /w/c.go: a symbolic link, and 1 more.`
	require.Equal(t, []util.InfoMsg{util.NewWarnMsg(said)}, rewound(both))
	require.Equal(t, "change a.txt", u.textarea.Value())
	require.Equal(t, []message.Attachment{shot}, u.attachments.List())
	require.Equal(t, []string{"s1"}, ws.loaded)
}

// Esc twice on an empty prompt opens /rewind, as in Claude Code: the
// second press within rewindEscWindow of the first, or both at once as
// alt+esc. One press, a second one too late, a prompt with text in it and
// no session do nothing; Esc there goes to the editor as before. The help
// lists it once there is a session.
func TestEscEscOpensRewind(t *testing.T) {
	pinTTLs(t)
	u := newTestUI()
	u.keyMap = DefaultKeyMap()
	u.dialog = dialog.NewOverlay()
	u.attachments = attachments.New(nil, attachments.Keymap{})
	u.com.Workspace = &rewindWorkspace{msgs: []message.Message{{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "make a.txt"}}}}}
	warmCaches(u, false)
	u.promptHistory.index = -1
	opened := rewindPromptsMsg{sessionID: "s1", prompts: []dialog.RewindPrompt{{MessageID: "u1", Text: "make a.txt"}}}
	esc := func(mod tea.KeyMod) tea.Msg {
		if cmd := u.handleKeyPressMsg(tea.KeyPressMsg{Code: tea.KeyEscape, Mod: mod}); cmd != nil {
			return cmd()
		}
		return nil
	}
	helps := func() []string {
		var keys []string
		for _, row := range u.FullHelp() {
			for _, b := range row {
				keys = append(keys, b.Help().Key)
			}
		}
		return keys
	}

	require.Nil(t, esc(0))
	require.Nil(t, esc(0), "no session")
	require.Nil(t, esc(tea.ModAlt), "no session")
	require.NotContains(t, helps(), "esc esc")

	u.session = &session.Session{ID: "s1"}
	require.Contains(t, helps(), "esc esc")
	require.Nil(t, esc(0))
	require.Equal(t, opened, esc(0))
	require.Nil(t, esc(0), "a third press starts over")
	require.Equal(t, opened, esc(0))

	require.Nil(t, esc(0))
	u.rewindEscAt = u.rewindEscAt.Add(-rewindEscWindow - time.Millisecond)
	require.Nil(t, esc(0), "too late")
	require.Equal(t, opened, esc(0))

	require.Equal(t, opened, esc(tea.ModAlt), "two at once")

	require.Nil(t, esc(0))
	u.textarea.SetValue("half a thought")
	require.Equal(t, util.NewInfoMsg("Esc again to clear"), esc(0), "a prompt with text")
	require.Equal(t, "half a thought", u.textarea.Value())
	u.textarea.SetValue("")
	require.Nil(t, esc(0), "the text started it over")
	require.Equal(t, opened, esc(0))
}

// Esc twice on a prompt with text clears it, as in Claude Code: the first
// says "Esc again to clear", the second, within rewindEscWindow, empties
// the editor, and Up brings the text back, a shell command with its "!".
// A second Esc too late says it again; alt+esc clears at once; Esc while
// browsing history goes back to the draft, as before.
func TestEscEscClearsAPromptForUp(t *testing.T) {
	pinTTLs(t)
	u := newTestUI()
	u.keyMap = DefaultKeyMap()
	u.dialog = dialog.NewOverlay()
	u.attachments = attachments.New(nil, attachments.Keymap{})
	u.com.Workspace = &rewindWorkspace{}
	warmCaches(u, false)
	u.session = &session.Session{ID: "s1"}
	u.promptHistory.index = -1
	u.promptHistory.messages = []string{"sent before"}
	u.promptHistory.times = []int64{time.Now().Unix() - 60}
	press := func(code rune, mod tea.KeyMod) tea.Cmd {
		return u.handleKeyPressMsg(tea.KeyPressMsg{Code: code, Mod: mod})
	}
	again := util.NewInfoMsg("Esc again to clear")

	u.textarea.SetValue("half a thought")
	require.Equal(t, again, press(tea.KeyEscape, 0)())
	require.Equal(t, "half a thought", u.textarea.Value())
	press(tea.KeyEscape, 0)
	require.Empty(t, u.textarea.Value())
	require.Equal(t, []string{"half a thought", "sent before"}, u.historyEntries())
	press(tea.KeyUp, 0)
	require.Equal(t, "half a thought", u.textarea.Value(), "Up brings it back")

	press(tea.KeyEscape, 0)
	require.Empty(t, u.textarea.Value(), "browsing history: back to the draft")
	require.Equal(t, -1, u.promptHistory.index)
	require.Len(t, u.promptHistory.cleared, 1, "nothing cleared")

	u.textarea.SetValue("second thought")
	require.Equal(t, again, press(tea.KeyEscape, 0)())
	u.clearEscAt = u.clearEscAt.Add(-rewindEscWindow - time.Millisecond)
	require.Equal(t, again, press(tea.KeyEscape, 0)(), "too late")
	require.Equal(t, "second thought", u.textarea.Value())
	press(tea.KeyEscape, tea.ModAlt)
	require.Empty(t, u.textarea.Value(), "two at once")

	u.bangMode = true
	u.textarea.SetValue("ls -la")
	press(tea.KeyEscape, 0)
	press(tea.KeyEscape, 0)
	require.Empty(t, u.textarea.Value())
	require.Equal(t, []string{"!ls -la", "second thought", "half a thought", "sent before"}, u.historyEntries())

	// History reloads after a prompt is sent; a prompt sent after the
	// clearing comes before what was cleared.
	u.Update(promptHistoryLoadedMsg{messages: []string{"sent after", "sent before"}, times: []int64{time.Now().Unix() + 10, time.Now().Unix() - 60}})
	require.Equal(t, []string{"sent after", "!ls -la", "second thought", "half a thought", "sent before"}, u.historyEntries())
}
