package session

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// /rename, or its alias /name, is read with or without its slash, in any
// case, with the name after it or none.
func TestParseRenameCommand(t *testing.T) {
	t.Parallel()

	for line, want := range map[string]string{
		"/rename fix the login bug": "fix the login bug",
		"rename  spaced  out ":      "spaced  out",
		"/Rename X":                 "X",
		"/rename":                   "",
		" rename ":                  "",
		"/name the alias":           "the alias",
		"NAME":                      "",
	} {
		name, ok := ParseRenameCommand(line)
		require.True(t, ok, line)
		require.Equal(t, want, name, line)
	}
	for _, line := range []string{"/renamed x", "please /rename x", "/re name", "", "/resume", "/names x", "/nam x"} {
		_, ok := ParseRenameCommand(line)
		require.False(t, ok, line)
	}
}

// /resume, or its alias /continue, is read as /rename is, with the
// session it names after it or nothing.
func TestParseResumeCommand(t *testing.T) {
	t.Parallel()

	for line, want := range map[string]string{
		"/resume fix-login-bug":     "fix-login-bug",
		"resume":                    "",
		"/Continue  the auth work ": "the auth work",
	} {
		ref, ok := ParseResumeCommand(line)
		require.True(t, ok, line)
		require.Equal(t, want, ref, line)
	}
	for _, line := range []string{"/resumed x", "/rename x", "please /resume x", "/continues", ""} {
		_, ok := ParseResumeCommand(line)
		require.False(t, ok, line)
	}
}

// /compact is read as /rename is, with the instructions for the summary
// after it or none.
func TestParseCompactCommand(t *testing.T) {
	t.Parallel()

	for line, want := range map[string]string{
		"/compact keep the API decisions": "keep the API decisions",
		"compact":                         "",
		" /Compact  focus on the tests ":  "focus on the tests",
	} {
		instructions, ok := ParseCompactCommand(line)
		require.True(t, ok, line)
		require.Equal(t, want, instructions, line)
	}
	for _, line := range []string{"/compacted", "/rename x", "please /compact", ""} {
		_, ok := ParseCompactCommand(line)
		require.False(t, ok, line)
	}
}

// /context is read as the other commands are; what follows it is not used.
func TestParseContextCommand(t *testing.T) {
	t.Parallel()

	for _, line := range []string{"/context", "context", " /Context all "} {
		require.True(t, ParseContextCommand(line), line)
	}
	for _, line := range []string{"/contexts", "/compact", "what is the context", ""} {
		require.False(t, ParseContextCommand(line), line)
	}
}

// /hooks is read as the other commands are; what follows it is not used.
func TestParseHooksCommand(t *testing.T) {
	t.Parallel()

	for _, line := range []string{"/hooks", "hooks", " /Hooks list "} {
		require.True(t, ParseHooksCommand(line), line)
	}
	for _, line := range []string{"/hook", "/hooksy", "show the hooks", ""} {
		require.False(t, ParseHooksCommand(line), line)
	}
}

// /permissions, or its alias /allowed-tools, is read as the other commands
// are; what follows it is not used.
func TestParsePermissionsCommand(t *testing.T) {
	t.Parallel()

	for _, line := range []string{"/permissions", "permissions", " /Allowed-Tools ", "/permissions list"} {
		require.True(t, ParsePermissionsCommand(line), line)
	}
	for _, line := range []string{"/permission", "/allowed", "my permissions", ""} {
		require.False(t, ParsePermissionsCommand(line), line)
	}
}
