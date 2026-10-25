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

// The titles a session is created with until its first prompt titles it:
// the TUI's and a channel's (NewTitle), and klash run's through a server
// (RunTitle) and in process (UntitledTitle).
const (
	NewTitle      = "New Session"
	RunTitle      = "non-interactive"
	UntitledTitle = "Untitled Session"
)

// Untitled reports whether title is one a session has until its first
// prompt titles it: none, or a title a session is created with. Any other
// was given to it, by klash -n or a client of the server, and the first
// prompt keeps it, as Claude Code keeps a --name.
func Untitled(title string) bool {
	switch title {
	case "", NewTitle, RunTitle, UntitledTitle:
		return true
	}
	return false
}

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
