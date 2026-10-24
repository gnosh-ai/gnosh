package session

import (
	"cmp"
	"slices"
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
// command or its alias /name, with or without the slash, as typed in the
// prompt or the command palette, and the name it gives ("" when none).
func ParseRenameCommand(line string) (name string, ok bool) {
	return parseCommand(line, "rename", "name")
}

// ParseResumeCommand reports whether line is Claude Code's /resume
// command or its alias /continue, with or without the slash, and the
// session it names, by ID or name ("" when none).
func ParseResumeCommand(line string) (ref string, ok bool) {
	return parseCommand(line, "resume", "continue")
}

// parseCommand reports whether line is one of the commands names, in any
// case and with or without its slash, and the text after it.
func parseCommand(line string, names ...string) (string, bool) {
	word, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	word = strings.TrimPrefix(word, "/")
	if !slices.ContainsFunc(names, func(name string) bool { return strings.EqualFold(word, name) }) {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// Named is the sessions called name, as Claude Code's /resume and
// --resume find one by name: the title equal to it, ignoring case and
// the space around both, the most recently updated first.
func Named(sessions []Session, name string) []Session {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	var named []Session
	for _, s := range sessions {
		if strings.EqualFold(strings.TrimSpace(s.Title), name) {
			named = append(named, s)
		}
	}
	slices.SortStableFunc(named, func(a, b Session) int { return cmp.Compare(b.UpdatedAt, a.UpdatedAt) })
	return named
}
