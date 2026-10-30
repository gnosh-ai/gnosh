package model

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/chat"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/stretchr/testify/require"
)

// copyUI is a UI whose session has two prompts, each answered, the first
// answer after a step of tool calls with no text.
func copyUI(t *testing.T) *UI {
	t.Helper()
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.com.Workspace = &exportWorkspace{dir: t.TempDir()}
	u.session = &session.Session{ID: "s1", Title: "fix-login-bug"}
	user := func(id, text string) chat.MessageItem {
		return chat.NewUserMessageItem(u.com.Styles, &message.Message{
			ID: id, Role: message.User,
			Parts: []message.ContentPart{message.TextContent{Text: text}},
		}, attachments.NewRenderer(u.com.Styles.Attachments))
	}
	assistant := func(id, text string) chat.MessageItem {
		return chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
			ID: id, Role: message.Assistant,
			Parts: []message.ContentPart{message.TextContent{Text: text}, message.Finish{Reason: message.FinishReasonEndTurn}},
		})
	}
	u.chat.SetMessages(user("u1", "q1"), assistant("a0", ""), assistant("a1", "first"), user("u2", "q2"), assistant("a2", "second\nline"))
	return u
}

// copyFile is where /copy writes what it copies under the temp directory
// tmp.
func copyFile(tmp string) string {
	return filepath.Join(tmp, "gnosh-"+strconv.Itoa(os.Getuid()), "response.md")
}

// The latest responses come newest first, as many as asked for, leaving
// out steps with no text.
func TestRecentResponses(t *testing.T) {
	t.Parallel()

	u := copyUI(t)
	require.Equal(t, []string{"second\nline", "first"}, u.chat.RecentResponses(20))
	require.Equal(t, []string{"second\nline"}, u.chat.RecentResponses(1))
}

// /copy copies the latest response, or with N the Nth-latest, as Claude
// Code's does, and writes it to response.md in a directory of gnosh's
// only the user can open; typed, from the palette's item, or as "copy N"
// in the palette. N must be a count of responses there are.
func TestCopyResponse(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	u := copyUI(t)
	read := func() string {
		t.Helper()
		data, err := os.ReadFile(copyFile(tmp))
		require.NoError(t, err)
		return string(data)
	}

	require.NotNil(t, u.copyResponse(""))
	require.Equal(t, "second\nline", read())
	dir, err := os.Stat(filepath.Dir(copyFile(tmp)))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dir.Mode().Perm())
	file, err := os.Stat(copyFile(tmp))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), file.Mode().Perm())

	require.NotNil(t, u.sendMessage("/copy 2"))
	require.Equal(t, "first", read())

	c, err := dialog.NewCommands(u.com, "s1", true, false, false, nil, nil)
	require.NoError(t, err)
	u.dialog.OpenDialog(c)
	for _, r := range "copy" {
		u.handleDialogMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.False(t, u.dialog.ContainsDialog(dialog.CommandsID))
	require.Equal(t, "second\nline", read())

	for n, want := range map[string]string{
		"3": "Only 2 assistant messages available to copy",
		"0": "Usage: /copy [N] where N is 1 (latest), 2, 3, … Got: 0",
		"x": "Usage: /copy [N] where N is 1 (latest), 2, 3, … Got: x",
	} {
		require.Equal(t, util.NewWarnMsg(want), u.copyResponse(n)(), n)
	}
	require.Equal(t, "second\nline", read(), "nothing is written when nothing is copied")

	u.chat.SetMessages()
	require.Equal(t, util.NewWarnMsg("No assistant message to copy"), u.copyResponse("")())
}

// One response left is "message", not "messages".
func TestCopyResponseOfOne(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	u := copyUI(t)
	u.chat.SetMessages(u.chat.list.ItemAt(4).(chat.MessageItem))
	require.Equal(t, util.NewWarnMsg("Only 1 assistant message available to copy"), u.copyResponse("2")())
}

// The temp directory is shared, so the copy is not written where gnosh's
// directory is a link, or is open to others; the clipboard still gets it.
func TestWriteCopyNeedsAPrivateDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dir := filepath.Dir(copyFile(tmp))

	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, dir))
	_, err := writeCopy("secret", "response.md")
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(elsewhere, "response.md"))
	require.True(t, os.IsNotExist(err), "nothing written through the link")

	require.NoError(t, os.Remove(dir))
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))
	_, err = writeCopy("secret", "response.md")
	require.Error(t, err)

	require.NoError(t, os.Chmod(dir, 0o700))
	path, err := writeCopy("secret", "response.md")
	require.NoError(t, err)
	require.Equal(t, copyFile(tmp), path)
}

// What /copy says: how much it copied, and where else it is.
func TestCopiedMessage(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Copied to clipboard (11 characters, 2 lines); also written to /t/response.md", copiedMessage("hé\nworld!!!", "/t/response.md", nil))
	require.Equal(t, "Copied to clipboard (1 characters, 1 lines)", copiedMessage("x", "", os.ErrPermission))
}

// The code blocks of a response are its fenced blocks, as CommonMark reads
// them: backticks or tildes, at least three; closed by at least as many of
// the same, or by the end; an info string's first word is the language; a
// fence indented up to three spaces takes as many off its lines.
func TestCodeBlocks(t *testing.T) {
	t.Parallel()

	for text, want := range map[string][]codeBlock{
		"Run:\n```bash\nnpm test\n```\nThen:\n~~~go title\nfunc main() {}\n~~~\n": {{"bash", "npm test"}, {"go", "func main() {}"}},
		"````md\n```go\nx\n```\n````":                     {{"md", "```go\nx\n```"}},
		"```\na\nb":                                       {{"", "a\nb"}},
		"  ```py\n  x = 1\n   y = 2\n  ```":               {{"py", "x = 1\n y = 2"}},
		"````\na\n```\nb\n````":                           {{"", "a\n```\nb"}},
		"```\nx\n```  ":                                   {{"", "x"}},
		"```\na\n```b\nc\n```":                            {{"", "a\n```b\nc"}},
		"```\na\n    ```\nb\n```":                         {{"", "a\n    ```\nb"}},
		"no code ```inline``` here\n    ```not\n```x`y\n": nil,
	} {
		require.Equal(t, want, codeBlocks(text), "%q", text)
	}
}

// The picker offers the full response, then each block by its first line
// (cut at 120 characters), with its language and size, written to Claude
// Code's file names.
func TestCopyChoices(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 130)
	text := "Do this:\n```bash\n\nnpm test\nnpm run lint\n```\nand\n```c++\n" + long + "\n```\n```\n```"
	choices := copyChoices(text)
	require.Equal(t, dialog.CopyChoice{Label: "Full response", Meta: fmt.Sprintf("%d chars, 12 lines", len(text)), Text: text, FileName: "response.md"}, choices[0])
	require.Equal(t, dialog.CopyChoice{Label: "npm test", Meta: "bash, 3 lines", Text: "\nnpm test\nnpm run lint", FileName: "copy.bash"}, choices[1])
	require.Equal(t, dialog.CopyChoice{Label: strings.Repeat("x", 120) + "…", Meta: "c++", Text: long, FileName: "copy.c"}, choices[2])
	require.Equal(t, dialog.CopyChoice{Label: "(code block)", Meta: "code", Text: "", FileName: "copy.txt"}, choices[3])
	require.Len(t, copyChoices("no blocks"), 1)

	require.Equal(t, ".txt", fileExtension("plaintext"))
	require.Equal(t, ".ts", fileExtension("ts"))
	require.Equal(t, ".txt", fileExtension("ü"))
}

// /copy of a response with code blocks asks which part to copy; the one
// chosen is copied and written to its file.
func TestCopyResponseAsksWhichPart(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	u := copyUI(t)
	u.chat.SetMessages(chat.NewAssistantMessageItem(u.com.Styles, &message.Message{
		ID: "a3", Role: message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "Run:\n```bash\nnpm test\n```"}, message.Finish{Reason: message.FinishReasonEndTurn}},
	}))

	require.Nil(t, u.copyResponse(""))
	require.True(t, u.dialog.ContainsDialog(dialog.CopyPickerID))
	u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	require.NotNil(t, u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
	require.False(t, u.dialog.ContainsDialog(dialog.CopyPickerID))
	data, err := os.ReadFile(filepath.Join(filepath.Dir(copyFile(tmp)), "copy.bash"))
	require.NoError(t, err)
	require.Equal(t, "npm test", string(data))
}
