package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// old_string is found as Claude Code's Edit finds it where the file does
// not have it as written: with curly quotes for straight ones, after text
// beyond ASCII as well; as characters for \uXXXX escapes; and as escapes,
// in either case of hex digit, a surrogate pair for a character beyond
// the Basic Multilingual Plane, for characters. An escape that a backslash
// escapes is no match, nor is escaped text the same text comes earlier
// than, nor is old_string ending in an escaping backslash.
func TestActualOldString(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ content, old, want string }{
		{"say \u201cHello\u201d now", `say "Hello" now`, "say \u201cHello\u201d now"},
		{"don\u2019t stop", "don't stop", "don\u2019t stop"},
		{"\u05e9\u05dc\u05d5\u05dd \u201c\u05e2\u05d5\u05dc\u05dd\u201d!", "\u05e2\u05d5\u05dc\u05dd\"!", "\u05e2\u05d5\u05dc\u05dd\u201d!"},
		{`{"greeting": "\u05e9\u05dc\u05d5\u05dd"}`, "\u05e9\u05dc\u05d5\u05dd", `\u05e9\u05dc\u05d5\u05dd`},
		{`{"g": "\u05E9\u05DC"}`, "\u05e9\u05dc", `\u05E9\u05DC`},
		{`{"e": "\ud83d\ude00"}`, "\U0001F600", `\ud83d\ude00`},
		{"title: \u05e9\u05dc\u05d5\u05dd", `\u05e9\u05dc\u05d5\u05dd`, "\u05e9\u05dc\u05d5\u05dd"},
		{`path \\u05e9`, "\u05e9", ""},
		{`a \\u05e9 b \u05e9`, "\u05e9", ""},
		{`{"g": "\u05e9\\"}`, "\u05e9\\", ""},
		{"nothing here", "missing", ""},
	} {
		require.Equal(t, c.want, actualOldString(c.content, c.old), "%q in %q", c.old, c.content)
	}
}

// new_string goes in in the style of the text it replaces: its quotes
// curled as there, an apostrophe between letters, an opening quote at the
// start or after a space, bracket or dash and a closing one elsewhere;
// its characters escaped as there, with the hex digits the file gives a
// character it has and, for one it does not, the case most of its digits
// are in; or its escapes unescaped.
func TestStyledNewString(t *testing.T) {
	t.Parallel()

	for _, c := range []struct{ old, actual, new, want string }{
		{`"Hi"`, "\u201cHi\u201d", `"Bye" ("now") and 'it's'`, "\u201cBye\u201d (\u201cnow\u201d) and 'it's'"},
		{"don't", "don\u2019t", "won't 'go' \u2014'yes'", "won\u2019t \u2018go\u2019 \u2014\u2018yes\u2019"},
		{"\u05e9\u05dc\u05d5\u05dd", `\u05e9\u05dc\u05d5\u05dd`, "\u05e9\u05dc\u05d5\u05dd \u05d5", `\u05e9\u05dc\u05d5\u05dd \u05d5`},
		{"\u05e9", `\u05E9`, "\u05e9\u05dc", `\u05E9\u05DC`},
		{"\u05e9", `\u05e9`, "\u05dc", `\u05dc`},
		{"\u05e9\u05dc", `\u05E9\u05dc`, "\u05dc\u05e9", `\u05dc\u05E9`},
		{"\U0001F600", `\ud83d\ude00`, "\U0001F600!", `\ud83d\ude00!`},
		{`\u05e9`, "\u05e9", `\u05dc\u05d5`, "\u05dc\u05d5"},
		{"same", "same", `"x"`, `"x"`},
	} {
		require.Equal(t, c.want, styledNewString(c.old, c.actual, c.new), "%q for %q", c.new, c.actual)
	}
}

// An edit whose old_string has straight quotes changes a file written
// with curly ones, and its new text goes in curled; one with Hebrew
// changes a JSON file that holds it as \u escapes, and its new text goes
// in escaped.
func TestEditMatchesTheFilesQuotesAndEscapes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	edit := editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		permissions: &mockPermissionService{},
		files:       &mockHistoryService{},
		filetracker: &mockEditFileTracker{lastRead: time.Now().Add(time.Second)},
		workingDir:  dir,
	}
	for _, c := range []struct{ file, content, old, new, want string }{
		{
			"ad.txt",
			"Headline: \u201cBuy now\u201d \u2014 it\u2019s here\n",
			"\"Buy now\" \u2014 it's here",
			"\"Buy today\" \u2014 it's here",
			"Headline: \u201cBuy today\u201d \u2014 it\u2019s here\n",
		},
		{
			"copy.json",
			`{"title": "\u05e9\u05dc\u05d5\u05dd"}` + "\n",
			"\u05e9\u05dc\u05d5\u05dd",
			"\u05e9\u05dc\u05d5\u05dd \u05e2\u05d5\u05dc\u05dd",
			`{"title": "\u05e9\u05dc\u05d5\u05dd \u05e2\u05d5\u05dc\u05dd"}` + "\n",
		},
	} {
		path := filepath.Join(dir, c.file)
		require.NoError(t, os.WriteFile(path, []byte(c.content), 0o644))
		resp, err := replaceContent(edit, path, c.old, c.new, false, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		require.False(t, resp.IsError, resp.Content)
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, c.want, string(got), c.file)
	}
}
