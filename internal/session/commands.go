package session

import (
	"slices"
	"strings"
)

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

// ParseCompactCommand reports whether line is Claude Code's /compact
// command, with or without the slash, and the instructions after it for
// the summary ("" when none).
func ParseCompactCommand(line string) (instructions string, ok bool) {
	return parseCommand(line, "compact")
}
