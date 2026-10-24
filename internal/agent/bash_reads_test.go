package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/stretchr/testify/require"
)

// The bash commands Claude Code counts as reading a file are cat, nl,
// bat, batcat, head, tail, sed -n 'X,Yp', grep, egrep, fgrep and rg, each
// run on one file with no pipe or redirect. Anything that could show
// more than that one file, or that the shell would expand, is no read.
func TestFileViewedBy(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"a.go", "b.go", filepath.Join("sub", "c.go")} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		writeFile(t, filepath.Join(dir, name), "package a\n")
	}
	a := filepath.Join(dir, "a.go")
	for command, want := range map[string]string{
		"cat a.go":                 a,
		"cat " + a:                 a,
		"cat 'a.go'":               a,
		`cat "a.go"`:               a,
		"nl a.go":                  a,
		"head -n 20 a.go":          a,
		"tail -5 a.go":             a,
		"bat --style=plain a.go":   a,
		"batcat a.go":              a,
		"sed -n '10,20p' a.go":     a,
		"sed -n 5p a.go":           a,
		"sed -n '3,$p' a.go":       a,
		`grep -n "func main" a.go`: a,
		"rg -n main a.go":          a,
		"egrep 'a|b' a.go":         a,
		"fgrep main a.go":          a,
		"cat sub/c.go":             filepath.Join(dir, "sub", "c.go"),

		"cat a.go b.go":         "",
		"cat a.go | head":       "",
		"cat a.go > out":        "",
		"cd sub && cat c.go":    "",
		"cat a.go; cat b.go":    "",
		"cat a.go &":            "",
		"! cat a.go":            "",
		"LC_ALL=C cat a.go":     "",
		"cat $FILE":             "",
		"cat $(echo a.go)":      "",
		"cat *.go":              "",
		"cat a.go {b,c}.go":     "",
		"cat ~/a.go":            "",
		"grep -r main .":        "",
		"grep -rn main sub":     "",
		"sed -i 's/a/b/' a.go":  "",
		"sed -n '/main/p' a.go": "",
		"wc -l a.go":            "",
		"cat missing.go":        "",
		"cat sub":               "",
		"cat":                   "",
	} {
		require.Equalf(t, want, fileViewedBy(command, dir), "%s", command)
	}
}

// A bash command that shows the agent one file records a read of it, so
// the edit tool takes the file without a view first. A search that
// found nothing, or a command still running in the background, does not.
func TestBashViewRecordsARead(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := filepath.Join(c.workingDir, "a.go")
	writeFile(t, file, "package a\n")

	bash := func(command string, meta tools.BashResponseMetadata) bool {
		meta.WorkingDirectory = c.workingDir
		tool := c.wrap(&funcTool{name: tools.BashToolName, run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(meta.Output), meta), nil
		}})
		input, err := json.Marshal(tools.BashParams{Command: command})
		require.NoError(t, err)
		c.runWith(t, tool, tools.BashToolName, string(input))
		return !c.files.LastReadTime(t.Context(), c.sessionID, file).IsZero()
	}

	require.False(t, bash("grep -n nothing a.go", tools.BashResponseMetadata{Output: "\nExit code 1", ExitCode: 1}), "found nothing")
	require.False(t, bash("grep -q package a.go", tools.BashResponseMetadata{}), "printed nothing")
	require.False(t, bash("cat a.go", tools.BashResponseMetadata{Output: "package a\n", Background: true}), "still running")
	require.False(t, bash("wc -l a.go", tools.BashResponseMetadata{Output: "1 a.go\n"}), "not a view")
	require.True(t, bash("sed -n '1,5p' a.go", tools.BashResponseMetadata{Output: "package a\n"}))
}

// A file changed on disk since the agent read it, then shown by cat, is
// not named as changed: the agent has just seen what it holds now.
func TestBashViewOfAChangedFileIsNotNamed(t *testing.T) {
	t.Parallel()
	c := newChangedFilesTest(t)
	file := c.writeAndRead(t, "a.go", "package a\n")
	writeFile(t, file, "package a\n// changed\n")

	tool := c.wrap(&funcTool{name: tools.BashToolName, run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		meta := tools.BashResponseMetadata{Output: "package a\n// changed\n", WorkingDirectory: c.workingDir}
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse(meta.Output), meta), nil
	}})
	require.Equal(t, "package a\n// changed\n", c.runWith(t, tool, tools.BashToolName, `{"command":"cat a.go"}`))
}
