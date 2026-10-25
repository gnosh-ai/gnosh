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

// ParseContextCommand reports whether line is Claude Code's /context
// command, with or without the slash. What follows it is not used.
func ParseContextCommand(line string) bool {
	_, ok := parseCommand(line, "context")
	return ok
}

// ParseHooksCommand reports whether line is Claude Code's /hooks command,
// with or without the slash. What follows it is not used.
func ParseHooksCommand(line string) bool {
	_, ok := parseCommand(line, "hooks")
	return ok
}

// ParsePermissionsCommand reports whether line is Claude Code's
// /permissions command or its alias /allowed-tools, with or without the
// slash. What follows it is not used.
func ParsePermissionsCommand(line string) bool {
	_, ok := parseCommand(line, "permissions", "allowed-tools")
	return ok
}

// ParseStatusCommand reports whether line is Claude Code's /status
// command, with or without the slash. What follows it is not used.
func ParseStatusCommand(line string) bool {
	_, ok := parseCommand(line, "status")
	return ok
}

// ParseBranchCommand reports whether line is Claude Code's /branch
// command, with or without the slash, and the name it gives the branch
// ("" when none).
func ParseBranchCommand(line string) (name string, ok bool) {
	return parseCommand(line, "branch")
}

// ParseRewindCommand reports whether line is Claude Code's /rewind
// command or its aliases /checkpoint and /undo, with or without the
// slash. What follows it is not used.
func ParseRewindCommand(line string) bool {
	_, ok := parseCommand(line, "rewind", "checkpoint", "undo")
	return ok
}

// ParseCopyCommand reports whether line is Claude Code's /copy command,
// with or without the slash, and the N after it that picks the
// Nth-latest response ("" for the latest).
func ParseCopyCommand(line string) (n string, ok bool) {
	return parseCommand(line, "copy")
}

// ParseTasksCommand reports whether line is Claude Code's /tasks command
// or its alias /bashes, with or without the slash. What follows it is not
// used.
func ParseTasksCommand(line string) bool {
	_, ok := parseCommand(line, "tasks", "bashes")
	return ok
}

// ParseDiffCommand reports whether line is Claude Code's /diff command,
// with or without the slash. What follows it is not used.
func ParseDiffCommand(line string) bool {
	_, ok := parseCommand(line, "diff")
	return ok
}

// ParseDoctorCommand reports whether line is Claude Code's /doctor
// command or its alias /checkup, with or without the slash. What follows
// it is not used.
func ParseDoctorCommand(line string) bool {
	_, ok := parseCommand(line, "doctor", "checkup")
	return ok
}

// ParseExportCommand reports whether line is Claude Code's /export
// command, with or without the slash, and the file it names ("" when
// none).
func ParseExportCommand(line string) (filename string, ok bool) {
	return parseCommand(line, "export")
}

// ParseCompactCommand reports whether line is Claude Code's /compact
// command, with or without the slash, and the instructions after it for
// the summary ("" when none).
func ParseCompactCommand(line string) (instructions string, ok bool) {
	return parseCommand(line, "compact")
}
