package model

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// copyLimit is how far back /copy reaches, as in Claude Code: the 20
// latest responses.
const copyLimit = 20

// copyResponse copies a response to the clipboard, as Claude Code's /copy
// does: the latest, or with n the Nth-latest. A response with code blocks
// asks first, as Claude Code's does, whether to copy it whole or one of
// its blocks. Like Claude Code it also writes the copy to a file in
// klash's temp directory, for when the clipboard does not reach (over
// ssh, say).
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
	if choices := copyChoices(text); len(choices) > 1 {
		m.dialog.OpenDialog(dialog.NewCopyPicker(m.com, choices))
		return nil
	}
	return copyText(text, "response.md")
}

// copyText copies text to the clipboard and writes it to fileName in
// klash's temp directory.
func copyText(text, fileName string) tea.Cmd {
	path, err := writeCopy(text, fileName)
	return common.CopyToClipboard(text, copiedMessage(text, path, err))
}

// copyLabelWidth is how much of a block's first line the copy picker
// shows, as in Claude Code.
const copyLabelWidth = 120

// copyChoices is what /copy offers from text, as Claude Code's picker
// lists it: the full response, then each of its code blocks by its first
// line, with its language and size, each written to Claude Code's file
// name (response.md; copy and the language's extension).
func copyChoices(text string) []dialog.CopyChoice {
	choices := []dialog.CopyChoice{{
		Label:    "Full response",
		Meta:     fmt.Sprintf("%d chars, %d lines", utf8.RuneCountInString(text), strings.Count(text, "\n")+1),
		Text:     text,
		FileName: "response.md",
	}}
	for _, b := range codeBlocks(text) {
		label := "(code block)"
		for line := range strings.SplitSeq(b.text, "\n") {
			if strings.TrimSpace(line) != "" {
				label = line
				if r := []rune(line); len(r) > copyLabelWidth {
					label = string(r[:copyLabelWidth]) + "…"
				}
				break
			}
		}
		meta := cmp.Or(b.lang, "code")
		if n := strings.Count(b.text, "\n") + 1; n > 1 {
			meta += fmt.Sprintf(", %d lines", n)
		}
		choices = append(choices, dialog.CopyChoice{Label: label, Meta: meta, Text: b.text, FileName: "copy" + fileExtension(b.lang)})
	}
	return choices
}

// fileExtension is the extension Claude Code's /copy gives a block in
// lang: the language's letters and digits, or .txt.
func fileExtension(lang string) string {
	ext := strings.Map(func(r rune) rune {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return r
		}
		return -1
	}, lang)
	if ext == "" || ext == "plaintext" {
		return ".txt"
	}
	return "." + ext
}

// codeBlock is a fenced code block in a response: its language (the first
// word of the fence's info string) and its text.
type codeBlock struct {
	lang, text string
}

// fence is a CommonMark code fence: up to three spaces, three or more
// backticks or tildes, and the info string.
var fence = regexp.MustCompile("^( {0,3})(`{3,}|~{3,})(.*)$")

// codeBlocks are the fenced code blocks in Markdown text, in order. A
// block runs to the fence that closes it (the same character, at least as
// many, nothing after) or, unclosed, to the end; its lines lose as many
// leading spaces as its fence had, as CommonMark has it.
func codeBlocks(text string) []codeBlock {
	var blocks []codeBlock
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		m := fence.FindStringSubmatch(lines[i])
		if m == nil || m[2][0] == '`' && strings.Contains(m[3], "`") {
			continue
		}
		indent, open := len(m[1]), m[2]
		var body []string
		for i++; i < len(lines) && !closesFence(lines[i], open); i++ {
			line := lines[i]
			for range indent {
				line = strings.TrimPrefix(line, " ")
			}
			body = append(body, line)
		}
		lang := ""
		if fields := strings.Fields(m[3]); len(fields) > 0 {
			lang = fields[0]
		}
		blocks = append(blocks, codeBlock{lang: lang, text: strings.Join(body, "\n")})
	}
	return blocks
}

// closesFence reports whether line closes a block opened by open: up to
// three spaces, at least as many of open's character, then only spaces.
func closesFence(line, open string) bool {
	rest := strings.TrimLeft(line, " ")
	if len(line)-len(rest) > 3 {
		return false
	}
	run := len(rest) - len(strings.TrimLeft(rest, open[:1]))
	return run >= len(open) && strings.TrimSpace(rest[run:]) == ""
}

// copiedMessage says what /copy copied, and where else it is.
func copiedMessage(text, path string, err error) string {
	msg := fmt.Sprintf("Copied to clipboard (%d characters, %d lines)", utf8.RuneCountInString(text), strings.Count(text, "\n")+1)
	if err != nil {
		return msg
	}
	return msg + "; also written to " + path
}

// writeCopy writes text to fileName in klash's own directory under the
// temp directory, one per user and private to them, as Claude Code keeps
// its copy, and returns the file's path.
func writeCopy(text, fileName string) (string, error) {
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
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
