package session

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A session's name is kept as Claude Code keeps one: control and
// invisible characters become spaces, the ends are trimmed, and it is cut
// at 200 characters.
func TestCleanName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "fix the login bug", CleanName("  fix the login bug \n"))
	require.Equal(t, "a b", CleanName("a\tb"))
	require.Equal(t, "zero width", CleanName("zero​width"))
	require.Equal(t, "", CleanName("​‍\t "), "nothing left")
	require.Equal(t, "日本語のセッション", CleanName("日本語のセッション"))
	long := CleanName(strings.Repeat("é", 250))
	require.Equal(t, 200, len([]rune(long)))
	require.Equal(t, strings.Repeat("a", 199), CleanName(strings.Repeat("a", 199)+"  tail"), "no space left at the cut")
}

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

// A name finds the sessions whose title it is, ignoring case and the
// space around both, the most recently updated first; part of a title is
// no match.
func TestNamed(t *testing.T) {
	t.Parallel()

	sessions := []Session{
		{ID: "a", Title: "fix-login-bug", UpdatedAt: 10},
		{ID: "b", Title: "add-auth", UpdatedAt: 30},
		{ID: "c", Title: " Fix-Login-Bug ", UpdatedAt: 20},
		{ID: "d", Title: "fix-login-bug-again", UpdatedAt: 40},
	}
	ids := func(found []Session) []string {
		var out []string
		for _, s := range found {
			out = append(out, s.ID)
		}
		return out
	}
	require.Equal(t, []string{"c", "a"}, ids(Named(sessions, "FIX-login-bug ")))
	require.Equal(t, []string{"b"}, ids(Named(sessions, "add-auth")))
	require.Empty(t, Named(sessions, "fix-login"))
	require.Empty(t, Named(sessions, "  "))
}
