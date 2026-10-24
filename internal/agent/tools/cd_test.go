package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A cd into the project, or a directory in it, written in plain text,
// counts as read-only, as in Claude Code, so "cd sub && ls" runs without
// asking; a cd anywhere else, or one Klash cannot be sure of, asks. A line
// that cds into another directory and runs git asks too: git in another
// repository can run what that repository's config names.
func TestReadOnlyCd(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(project, "sub"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(project, "out")))
	// A directory named ~ does not make "cd ~" a cd into the project: the
	// shell takes ~ for the home directory.
	require.NoError(t, os.Mkdir(filepath.Join(project, "~"), 0o755))
	dirs := workDirs{start: project, project: project}

	for _, command := range []string{
		"cd sub && ls",
		"cd sub; cat a.txt",
		"cd . && git status",
		"cd " + project + " && git status",
		"cd " + filepath.Join(project, "sub") + " && ls",
		"cd 'sub' && pwd",
	} {
		require.True(t, readOnlyCommand(command, dirs), command)
	}
	for _, command := range []string{
		"cd .. && ls",
		"cd " + outside + " && ls",
		"cd out && ls",
		"cd ~ && ls",
		"cd ~/sub && ls",
		"cd $HOME && ls",
		"cd && ls",
		"cd - && ls",
		"cd -P sub && ls",
		"cd sub other && ls",
		"cd missing && ls",
		"cd sub && git status",
		"git status && cd sub",
		"cd sub && touch a",
		"D=sub cd sub && ls",
	} {
		require.False(t, readOnlyCommand(command, dirs), command)
	}
	require.False(t, readOnlyCommand("cd sub && ls", workDirs{}), "with no project, a cd asks")

	// Started in a directory of the project, a cd is read from there.
	inSub := workDirs{start: filepath.Join(project, "sub"), project: project}
	require.True(t, readOnlyCommand("cd .. && ls", inSub))
	require.False(t, readOnlyCommand("cd ../.. && ls", inSub))

	// Allow rules take it too, git apart.
	rules := commandRules([]string{"npm test *", "git log *"})
	require.True(t, allowedCommand("cd sub && npm test", dirs, rules))
	require.False(t, allowedCommand("cd sub && git log", dirs, rules), "cd and git ask")
	require.False(t, allowedCommand("cd sub && timeout 5 git log", dirs, rules), "past a wrapper too")
	require.True(t, allowedCommand("git log", dirs, rules))
}
