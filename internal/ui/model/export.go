package model

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/version"
)

// exportConversation exports the conversation as Claude Code's /export
// does: to the file filename names, or, with none, as the export dialog
// asks, suggesting a name made from the first prompt.
func (m *UI) exportConversation(filename string) tea.Cmd {
	if !m.hasSession() || m.chat.Len() == 0 {
		return util.ReportWarn("There is no conversation to export yet: send a prompt first")
	}
	if filename == "" {
		m.dialog.OpenDialog(dialog.NewExport(m.com, exportFilename(m.chat.FirstPrompt(), time.Now())))
		return nil
	}
	path := exportPath(filename, m.com.Workspace.WorkingDir())
	if err := writeExport(path, m.conversationText()); err != nil {
		return util.ReportError(fmt.Errorf("failed to export conversation: %w", err))
	}
	return util.ReportInfo("Conversation exported to: " + path)
}

// copyConversation copies the conversation to the clipboard, as Claude
// Code's /export does when that is chosen.
func (m *UI) copyConversation() tea.Cmd {
	if !m.hasSession() || m.chat.Len() == 0 {
		return util.ReportWarn("There is no conversation to export yet: send a prompt first")
	}
	return common.CopyToClipboard(m.conversationText(), "Conversation copied to clipboard")
}

// conversationText is the conversation as Claude Code's /export writes
// it, as plain text: a header with gnosh's version, the session and the
// working directory, as Claude Code's welcome header has its own, then
// each message as the chat shows it.
func (m *UI) conversationText() string {
	return fmt.Sprintf("%s %s\nSession: %s (%s)\nWorking directory: %s\n\n%s\n",
		version.Name, version.Version, cmp.Or(m.session.Title, "untitled"), m.session.ID,
		m.com.Workspace.WorkingDir(), m.chat.Transcript())
}

// exportFilename is the name Claude Code's /export suggests for a
// conversation saved at now: the date and time, then the first prompt
// (its first 49 characters when longer than 50) lower-cased, kept to a-z,
// 0-9 and hyphens, its words joined by hyphens; "conversation-" and the
// date and time when that leaves nothing.
func exportFilename(prompt string, now time.Time) string {
	stamp := now.Format("2006-01-02-150405")
	prompt = strings.TrimSpace(prompt)
	if r := []rune(prompt); len(r) > 50 {
		prompt = string(r[:49])
	}
	var kept strings.Builder
	for _, r := range strings.ToLower(prompt) {
		switch {
		case unicode.IsSpace(r):
			kept.WriteRune(' ')
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			kept.WriteRune(r)
		}
	}
	words := strings.FieldsFunc(strings.Join(strings.Fields(kept.String()), "-"), func(r rune) bool { return r == '-' })
	if len(words) == 0 {
		return "conversation-" + stamp + ".txt"
	}
	return stamp + "-" + strings.Join(words, "-") + ".txt"
}

// exportPath is where Claude Code's /export writes the file filename
// names: with .txt added when the name has no extension, ~ for the home
// directory, and a relative name in dir.
func exportPath(filename, dir string) string {
	if filepath.Ext(filename) == "" {
		filename += ".txt"
	}
	path := home.Long(filename)
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return path
}

// writeExport writes text to the file at path, making its directory when
// it is missing.
func writeExport(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}
