package hooks

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// matcher is a hook's matcher, read the way Claude Code reads one:
//
//   - empty, or "*": everything;
//   - only letters, digits, "_", "-", spaces, "," and "|": a list of exact
//     names separated by "|" or ",", so that "Bash" does not match
//     "BashOutput";
//   - anything else: a regular expression that may match anywhere in the
//     name.
type matcher struct {
	all   bool
	names []string
	re    *regexp.Regexp
}

// compileMatcher reads pattern. With narrow set, a list is made of letters,
// digits, "_" and "|" only, and "|" alone separates its names; that is the
// rule for StopFailure, whose values are error kinds, and for FileChanged,
// whose are file names, as in Claude Code.
func compileMatcher(pattern string, narrow bool) (matcher, error) {
	if pattern == "" || pattern == "*" {
		return matcher{all: true}, nil
	}
	if isList(pattern, narrow) {
		seps := "|,"
		if narrow {
			seps = "|"
		}
		var names []string
		for _, name := range strings.FieldsFunc(pattern, func(r rune) bool { return strings.ContainsRune(seps, r) }) {
			if name = strings.TrimSpace(name); name != "" && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
		if len(names) == 0 {
			return matcher{}, fmt.Errorf("the matcher %s names nothing. Leave the matcher out, or write \"*\", to match everything", quote(pattern))
		}
		return matcher{names: names}, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return matcher{}, fmt.Errorf("the matcher %s cannot be read as a regular expression (%v). A matcher is a list of exact names, such as Edit|Write, or a regular expression without look-ahead, look-behind or backreferences", quote(pattern), err)
	}
	return matcher{re: re}, nil
}

// isList reports whether pattern is made only of the characters of a list
// of exact names.
func isList(pattern string, narrow bool) bool {
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '_', c == '|':
		case !narrow && (c == '-' || c == ' ' || c == ','):
		default:
			return false
		}
	}
	return true
}

// matches reports whether the matcher fits name.
func (m matcher) matches(name string) bool {
	switch {
	case m.all:
		return true
	case m.re != nil:
		return m.re.MatchString(name)
	}
	return slices.Contains(m.names, name)
}

// matchesAny reports whether the matcher fits one of names.
func (m matcher) matchesAny(names []string) bool {
	return slices.ContainsFunc(names, m.matches)
}

// toolNames knows which names a tool answers to in matchers.
//
// A tool answers to its own name and to every older name it had: a matcher
// that says Task matches Agent, as in Claude Code. A hook matched that way
// is matched in full.
//
// A tool is also guarded by the hooks written for the Claude Code tools
// that do the same job. gnosh has tools Claude Code has nothing like, and a
// guard written for Claude Code must not be walked around through them: a
// hook on WebFetch also runs for a tool that saves a URL to a file. A hook
// matched only that way is a guard: it can block, ask, add text and change
// the input, but its approval does not count, because what it checked
// before approving was another tool's input.
type toolNames struct {
	// older maps a tool's name to the older names it answers to.
	older map[string][]string
	// guards maps a tool's name to the names whose hooks also guard it.
	guards map[string][]string
}

// newToolNames puts Claude Code's own renames and the caller's tables
// together. renamed maps an older name to the name the tool has now.
// guards maps a tool's name to the names whose hooks also guard it.
func newToolNames(renamed map[string]string, guards map[string][]string) toolNames {
	t := toolNames{older: map[string][]string{}, guards: map[string][]string{}}
	rename := func(old, now string) {
		if old != "" && now != "" && old != now && !slices.Contains(t.older[now], old) {
			t.older[now] = append(t.older[now], old)
		}
	}
	// Claude Code's renames, from its tools reference.
	rename("Task", "Agent")
	rename("KillShell", "TaskStop")
	rename("KillBash", "TaskStop")
	rename("BashOutput", "TaskOutput")
	for _, old := range slices.Sorted(maps.Keys(renamed)) {
		rename(old, renamed[old])
	}
	guard := func(tool, by string) {
		if tool != "" && by != "" && tool != by && !slices.Contains(t.guards[tool], by) {
			t.guards[tool] = append(t.guards[tool], by)
		}
	}
	// A hook written for the tool that made several edits at once checked
	// that tool's input, not Edit's. It guards Edit and approves nothing.
	guard("Edit", "MultiEdit")
	// A Bash hook guards the commands Monitor starts, as in Claude Code:
	// before they run (PreToolUse and PermissionRequest), seeing each as a
	// Bash call; its approval and changed input count for nothing there
	// (selected.asBash).
	guard(monitorTool, "Bash")
	for _, tool := range slices.Sorted(maps.Keys(guards)) {
		for _, by := range guards[tool] {
			guard(tool, by)
			for _, old := range t.older[by] {
				guard(tool, old)
			}
		}
	}
	return t
}

// fit says how a matcher fits a call of tool: in full, as a guard only, or
// not at all.
func (t toolNames) fit(m matcher, tool string) (matched, guardOnly bool) {
	if m.matches(tool) || m.matchesAny(t.older[tool]) {
		return true, false
	}
	if m.matchesAny(t.guards[tool]) {
		return true, true
	}
	return false, false
}
