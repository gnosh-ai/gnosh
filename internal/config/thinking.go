package config

import "strings"

// ThinkingKey is the config field the /think switch is stored under.
const ThinkingKey = "options.thinking"

// ParseThinkingCommand reads a chat line typed as a thinking switch: "/think"
// turns a model's thinking on, "/no_think" (also "/nothink", "/no-think")
// turns it off. ok is false for any other line, which is then an ordinary
// prompt. The switch is global and holds for every later request, in the
// chat and in `crush run`, until it is typed again.
func ParseThinkingCommand(line string) (enabled, ok bool) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "/think":
		return true, true
	case "/no_think", "/nothink", "/no-think":
		return false, true
	}
	return false, false
}

// Thinking returns the stored switch. set is false when it was never
// toggled; the server's own default then stands.
func (c *Config) Thinking() (enabled, set bool) {
	if c == nil || c.Options == nil || c.Options.Thinking == nil {
		return false, false
	}
	return *c.Options.Thinking, true
}
