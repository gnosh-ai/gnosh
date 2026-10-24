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
