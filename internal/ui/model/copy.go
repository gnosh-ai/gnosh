package model

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// copyLimit is how far back /copy reaches, as in Claude Code: the 20
// latest responses.
const copyLimit = 20

// copyResponse copies a response to the clipboard, as Claude Code's /copy
// does: the latest, or with n the Nth-latest. Like Claude Code it also
// writes it to response.md in klash's temp directory, for when the
// clipboard does not reach (over ssh, say).
func (m *UI) copyResponse(n string) tea.Cmd {
	responses := m.chat.RecentResponses(copyLimit)
	if len(responses) == 0 {
		return util.ReportWarn("No assistant message to copy")
	}
	nth := 1
	if n = strings.TrimSpace(n); n != "" {
		v, err := strconv.Atoi(n)
		if err != nil || v < 1 {
			return util.ReportWarn("Usage: /copy [N] where N is 1 (latest), 2, 3, … Got: " + n)
		}
		if v > len(responses) {
			noun := "messages"
			if len(responses) == 1 {
				noun = "message"
			}
			return util.ReportWarn(fmt.Sprintf("Only %d assistant %s available to copy", len(responses), noun))
		}
		nth = v
	}
	text := responses[nth-1]
	path, err := writeCopy(text)
	return common.CopyToClipboard(text, copiedMessage(text, path, err))
}

// copiedMessage says what /copy copied, and where else it is.
func copiedMessage(text, path string, err error) string {
	msg := fmt.Sprintf("Copied to clipboard (%d characters, %d lines)", utf8.RuneCountInString(text), strings.Count(text, "\n")+1)
	if err != nil {
		return msg
	}
	return msg + "; also written to " + path
}

// writeCopy writes text to response.md in klash's own directory under the
// temp directory, one per user and private to them, as Claude Code keeps
// its copy, and returns the file's path.
func writeCopy(text string) (string, error) {
	uid := os.Getuid()
	name := "klash"
	if uid >= 0 {
		name += "-" + strconv.Itoa(uid)
	}
	dir := filepath.Join(os.TempDir(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// The temp directory is shared, so this one must be the user's own: not
	// a link left in its place, nor open to anyone else.
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	owner, err := fsext.Owner(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || owner != uid || (uid >= 0 && info.Mode().Perm()&0o077 != 0) {
		return "", fmt.Errorf("%s is not a directory only this user can use", dir)
	}
	path := filepath.Join(dir, "response.md")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
