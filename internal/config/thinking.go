package config

import "strings"

// ParseThinkingCommand reads a chat line typed as a thinking switch: "/think"
// asks for as much reasoning as the model offers, "/no_think" (also
// "/nothink", "/no-think") for as little. ok is false for any other line,
// which is then an ordinary prompt.
//
// These are the old global switch, kept because they are in the fingers.
// They now move the current model's own level, which /model and the left and
// right arrows set directly — and which is the only way to reach the levels
// in between.
func ParseThinkingCommand(line string) (enabled, ok bool) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "/think":
		return true, true
	case "/no_think", "/nothink", "/no-think":
		return false, true
	}
	return false, false
}

// ThinkingLevel is the level a typed /think or /no_think asks for on a
// model's axis: either end of it. It returns "" for a model with no axis.
func ThinkingLevel(enabled bool, kind ReasoningKind, levels []string) string {
	if kind == ReasoningNone || len(levels) == 0 {
		return ""
	}
	if enabled {
		return levels[len(levels)-1]
	}
	return levels[0]
}
