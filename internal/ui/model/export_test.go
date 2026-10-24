package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// exportWorkspace has a working directory and a config for the palette.
type exportWorkspace struct {
	workspace.Workspace
	dir string
}

func (w *exportWorkspace) WorkingDir() string { return w.dir }
func (w *exportWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// pressKeys sends each of presses to the front dialog as the UI does.
func pressKeys(u *UI, presses ...tea.KeyPressMsg) {
	for _, k := range presses {
		u.handleDialogMsg(k)
	}
}

// typeKeys is the key presses that type s.
func typeKeys(s string) []tea.KeyPressMsg {
	var presses []tea.KeyPressMsg
	for _, r := range s {
		presses = append(presses, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return presses
}

var (
	enterPress = tea.KeyPressMsg{Code: tea.KeyEnter}
	downPress  = tea.KeyPressMsg{Code: tea.KeyDown}
)

// exportUI is a UI with a session of a prompt and its answer, in dir.
func exportUI(t *testing.T, dir string) *UI {
	t.Helper()
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.com.Workspace = &exportWorkspace{dir: dir}
	u.session = &session.Session{ID: "s1", Title: "fix-login-bug"}
	u.chat.SetSize(100, 30)
	u.chat.SetMessages(
		chat.NewUserMessageItem(u.com.Styles, &message.Message{
			ID: "u1", Role: message.User,
			Parts: []message.ContentPart{message.TextContent{Text: "Fix the login bug"}},
		}, attachments.NewRenderer(u.com.Styles.Attachments)),
		chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
			ID: "a1", Role: message.Assistant,
			Parts: []message.ContentPart{message.TextContent{Text: "The session cookie was never refreshed."}, message.Finish{Reason: message.FinishReasonEndTurn}},
		}),
	)
	return u
}

// /export with a file name writes the conversation there as plain text,
// as Claude Code's does: a header with the version, the session and the
// working directory, then the messages as the chat shows them, the
// prompt with its bar, without colours and without the selected
// message's mark. ".txt" is added to a name with no extension.
func TestExportConversationWritesTheFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	u := exportUI(t, dir)
	// The prompt is selected in a focused chat, so a draw marks it.
	u.chat.Focus()
	u.chat.SetSelected(0)
	require.Contains(t, u.chat.list.Render(), "▌")

	msg := u.exportConversation("notes")()
	path := filepath.Join(dir, "notes.txt")
	require.Equal(t, util.NewInfoMsg("Conversation exported to: "+path), msg)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(data)
	require.True(t, strings.HasPrefix(text, version.Name+" "+version.Version+"\nSession: fix-login-bug (s1)\nWorking directory: "+dir+"\n\n"), text)
	require.True(t, strings.HasSuffix(text, "\n\n│ Fix the login bug\n\n  The session cookie was never refreshed.\n"), text)
	require.NotContains(t, text, "▌")
	require.NotContains(t, text, "\x1b[")
	for line := range strings.SplitSeq(text, "\n") {
		require.Equal(t, strings.TrimRight(line, " "), line)
	}

	// The prompt and the dialog reach the same export: the dialog's
	// "Save to file" saves under the name it suggests, and closes.
	u.sendMessage("/export talk.md")
	_, err = os.Stat(filepath.Join(dir, "talk.md"))
	require.NoError(t, err)
	require.Nil(t, u.exportConversation(""))
	pressKeys(u, downPress, enterPress, enterPress)
	require.False(t, u.dialog.ContainsDialog(dialog.ExportID))
	saved, err := filepath.Glob(filepath.Join(dir, "*-fix-the-login-bug.txt"))
	require.NoError(t, err)
	require.Len(t, saved, 1)
	data, err = os.ReadFile(saved[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "│ Fix the login bug\n")
}

// /export with no file name, typed or from the palette, opens the dialog
// that asks how, suggesting a name made from the first prompt; with no
// conversation there is nothing to export.
func TestExportConversationAsks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	u := exportUI(t, dir)
	require.Nil(t, u.sendMessage("/export"))
	require.True(t, u.dialog.ContainsDialog(dialog.ExportID))
	u.dialog.CloseDialog(dialog.ExportID)
	require.Equal(t, "Fix the login bug", u.chat.FirstPrompt())

	// The palette's item opens it too, and "export" with a name saves.
	palette := func() {
		c, err := dialog.NewCommands(u.com, "s1", true, false, false, nil, nil)
		require.NoError(t, err)
		u.dialog.OpenDialog(c)
	}
	palette()
	pressKeys(u, typeKeys("export")...)
	pressKeys(u, enterPress)
	require.False(t, u.dialog.ContainsDialog(dialog.CommandsID))
	require.True(t, u.dialog.ContainsDialog(dialog.ExportID))
	u.dialog.CloseDialog(dialog.ExportID)
	palette()
	pressKeys(u, typeKeys("export sub/log")...)
	pressKeys(u, enterPress)
	require.False(t, u.dialog.ContainsDialog(dialog.CommandsID))
	require.False(t, u.dialog.ContainsDialog(dialog.ExportID))
	_, err := os.Stat(filepath.Join(dir, "sub", "log.txt"))
	require.NoError(t, err)

	u.dialog.CloseDialog(dialog.ExportID)
	u.session = nil
	want := util.NewWarnMsg("There is no conversation to export yet: send a prompt first")
	require.Equal(t, want, u.exportConversation("")())
	require.Equal(t, want, u.copyConversation()())
	require.False(t, u.dialog.ContainsDialog(dialog.ExportID))
}

// The suggested name is Claude Code's: the date and time, then the first
// prompt lower-cased, kept to a-z, 0-9 and hyphens, its first 49
// characters when it has more than 50, words joined by hyphens; with
// nothing left, "conversation-" and the date and time.
func TestExportFilename(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 24, 22, 15, 7, 0, time.Local)
	for prompt, want := range map[string]string{
		"Fix the login bug!":            "2026-10-24-221507-fix-the-login-bug.txt",
		"  --Hello   -- world--  ":      "2026-10-24-221507-hello-world.txt",
		"¿Qué tal?\tbien":               "2026-10-24-221507-qu-tal-bien.txt",
		strings.Repeat("abcdefghij", 5): "2026-10-24-221507-" + strings.Repeat("abcdefghij", 5) + ".txt",
		strings.Repeat("abcdefghij", 6): "2026-10-24-221507-" + strings.Repeat("abcdefghij", 4) + "abcdefghi.txt",
		"":                              "conversation-2026-10-24-221507.txt",
		"!!! ???":                       "conversation-2026-10-24-221507.txt",
	} {
		require.Equal(t, want, exportFilename(prompt, now), "%q", prompt)
	}
}

// The file is where Claude Code's /export puts it: ".txt" added to a name
// with no extension, ~ for the home directory, a relative name in the
// working directory; missing directories are made.
func TestExportPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.Equal(t, filepath.Join(dir, "notes.txt"), exportPath("notes", dir))
	require.Equal(t, filepath.Join(dir, "talk.md"), exportPath("talk.md", dir))
	require.Equal(t, filepath.Join(dir, "a", "b.txt"), exportPath("a/b", dir))
	require.Equal(t, filepath.Join(home.Dir(), "talk.txt"), exportPath("~/talk", dir))
	other := filepath.Join(t.TempDir(), "x.log")
	require.Equal(t, other, exportPath(other, dir))

	path := filepath.Join(dir, "new", "dir", "notes.txt")
	require.NoError(t, writeExport(path, "hello\n"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(data))
}
