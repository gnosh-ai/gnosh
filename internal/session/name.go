package session

import (
	"strings"
	"unicode"
)

// maxNameLength is the most characters a session's name keeps, as in
// Claude Code.
const maxNameLength = 200

// CleanName is name as a session keeps it, as Claude Code keeps one given
// with /rename: control and invisible characters become spaces, the ends
// are trimmed, and it is cut at 200 characters. "" means nothing is left.
func CleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		if !unicode.IsGraphic(r) {
			return ' '
		}
		return r
	}, name)
	runes := []rune(strings.TrimSpace(name))
	if len(runes) > maxNameLength {
		runes = []rune(strings.TrimSpace(string(runes[:maxNameLength])))
	}
	return string(runes)
}

// ParseRenameCommand reports whether line is Claude Code's /rename
// command, with or without the slash, as typed in the prompt or the
// command palette, and the name it gives ("" when none).
func ParseRenameCommand(line string) (name string, ok bool) {
	line = strings.TrimSpace(line)
	word, rest, _ := strings.Cut(line, " ")
	if !strings.EqualFold(strings.TrimPrefix(word, "/"), "rename") {
		return "", false
	}
	return strings.TrimSpace(rest), true
}
