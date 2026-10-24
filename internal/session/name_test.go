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

// /rename is read with or without its slash, in any case, with the name
// after it or none.
func TestParseRenameCommand(t *testing.T) {
	t.Parallel()

	for line, want := range map[string]string{
		"/rename fix the login bug": "fix the login bug",
		"rename  spaced  out ":      "spaced  out",
		"/Rename X":                 "X",
		"/rename":                   "",
		" rename ":                  "",
	} {
		name, ok := ParseRenameCommand(line)
		require.True(t, ok, line)
		require.Equal(t, want, name, line)
	}
	for _, line := range []string{"/renamed x", "please /rename x", "/re name", "", "/resume"} {
		_, ok := ParseRenameCommand(line)
		require.False(t, ok, line)
	}
}
