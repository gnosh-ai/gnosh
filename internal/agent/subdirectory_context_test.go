package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// A session viewing a file in a subdirectory with context files of its
// own is given them once, and again after a summary, which leaves them
// behind; another session is given them too.
func TestSubdirectoryContextOncePerSessionAndSummary(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sub := filepath.Join(env.workingDir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("the sub rules"), 0o644))
	c := &coordinator{
		cfg:               config.NewTestStoreIn(&config.Config{Options: &config.Options{ContextPaths: []string{"AGENTS.md"}}}, env.workingDir),
		sessions:          env.sessions,
		subdirectoryGiven: map[string]bool{},
	}
	one, err := env.sessions.Create(t.Context(), "one")
	require.NoError(t, err)
	two, err := env.sessions.Create(t.Context(), "two")
	require.NoError(t, err)
	file := filepath.Join(sub, "main.go")

	require.Contains(t, c.subdirectoryContext(t.Context(), one.ID, file), "the sub rules")
	require.Empty(t, c.subdirectoryContext(t.Context(), one.ID, filepath.Join(sub, "other.go")), "given once")
	require.Contains(t, c.subdirectoryContext(t.Context(), two.ID, file), "the sub rules", "another session")

	one.SummaryMessageID = "summary-1"
	_, err = env.sessions.Save(t.Context(), one)
	require.NoError(t, err)
	require.Contains(t, c.subdirectoryContext(t.Context(), one.ID, file), "the sub rules", "a summary left it behind")
}

// The write, edit, multiedit and view tools all give a session the
// subdirectory's context files once it writes or edits a file there, and
// the record is shared: a file given by one is not given by another, a
// failed call gives nothing, and a summary gives them again.
func TestSubdirectoryContextOnWriteAndEdit(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sub := filepath.Join(env.workingDir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("the sub rules"), 0o644))
	c := &coordinator{
		cfg:               config.NewTestStoreIn(&config.Config{Options: &config.Options{ContextPaths: []string{"AGENTS.md"}}}, env.workingDir),
		sessions:          env.sessions,
		subdirectoryGiven: map[string]bool{},
	}
	one, err := env.sessions.Create(t.Context(), "one")
	require.NoError(t, err)

	sess := env.permissions
	tracker := env.filetracker
	hist := env.history

	writeTool := tools.NewWriteTool(nil, sess, hist, tracker, c.subdirectoryContext, env.workingDir)
	viewTool := tools.NewViewTool(nil, sess, tracker, nil, c.subdirectoryContext, env.workingDir)
	editTool := tools.NewEditTool(nil, sess, hist, tracker, c.subdirectoryContext, env.workingDir)

	ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, one.ID)

	resp, err := writeTool.Run(ctx, fantasy.ToolCall{ID: "w1", Name: tools.WriteToolName, Input: `{"file_path":"sub/new.go","content":"package sub\n"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "the sub rules", "a write in a subdirectory gives its AGENTS.md")

	// The view and the edit did not get them from the write.
	resp, err = viewTool.Run(ctx, fantasy.ToolCall{ID: "v1", Name: tools.ViewToolName, Input: `{"file_path":"sub/new.go"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.NotContains(t, resp.Content, "the sub rules", "a view after the write does not give it again")

	resp, err = editTool.Run(ctx, fantasy.ToolCall{ID: "e1", Name: tools.EditToolName, Input: `{"file_path":"sub/new.go","old_string":"package sub","new_string":"package sub2"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.NotContains(t, resp.Content, "the sub rules", "an edit after the write does not give it again")

	// A failed edit gives nothing.
	resp, err = editTool.Run(ctx, fantasy.ToolCall{ID: "e2", Name: tools.EditToolName, Input: `{"file_path":"sub/new.go","old_string":"no such text","new_string":"x"}`})
	require.NoError(t, err)
	require.True(t, resp.IsError, resp.Content)
	require.NotContains(t, resp.Content, "the sub rules", "a failed edit gives nothing")

	// A summary gives them again, to whichever tool asks.
	one.SummaryMessageID = "summary-1"
	_, err = env.sessions.Save(t.Context(), one)
	require.NoError(t, err)
	resp, err = viewTool.Run(ctx, fantasy.ToolCall{ID: "v2", Name: tools.ViewToolName, Input: `{"file_path":"sub/new.go"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "the sub rules", "a summary gives them again")
}

// subdirectoryTest is a coordinator whose working directory has a
// subdirectory, sub, with an AGENTS.md of its own.
func subdirectoryTest(t *testing.T) (*coordinator, fakeEnv, string) {
	t.Helper()
	env := testEnv(t)
	sub := filepath.Join(env.workingDir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("the sub rules"), 0o644))
	c := &coordinator{
		cfg:               config.NewTestStoreIn(&config.Config{Options: &config.Options{ContextPaths: []string{"AGENTS.md"}}}, env.workingDir),
		sessions:          env.sessions,
		subdirectoryGiven: map[string]bool{},
	}
	return c, env, sub
}

// A call with no session gives nothing and records nothing, so the
// session's own first call still gets the files; a relative path is
// taken from the working directory.
func TestSubdirectoryContextNeedsASessionAndTakesARelativePath(t *testing.T) {
	t.Parallel()

	c, env, _ := subdirectoryTest(t)
	one, err := env.sessions.Create(t.Context(), "one")
	require.NoError(t, err)

	require.Empty(t, c.subdirectoryContext(t.Context(), "", "sub/main.go"))
	require.Empty(t, c.subdirectoryGiven, "nothing recorded")
	require.Contains(t, c.subdirectoryContext(t.Context(), one.ID, "sub/main.go"), "the sub rules")
}

// Each of the tools that read, write or edit a file gives a session the
// subdirectory's context files the first time it touches a file there,
// with the path as the model writes it, relative to the working
// directory.
func TestSubdirectoryContextFromEachTool(t *testing.T) {
	t.Parallel()

	for _, name := range []string{tools.ViewToolName, tools.WriteToolName, tools.EditToolName, tools.MultiEditToolName} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, env, sub := subdirectoryTest(t)
			sess, err := env.sessions.Create(t.Context(), name)
			require.NoError(t, err)
			ctx := context.WithValue(t.Context(), tools.SessionIDContextKey, sess.ID)
			tracker := env.filetracker
			file := filepath.Join(sub, "a.go")
			if name != tools.WriteToolName {
				require.NoError(t, os.WriteFile(file, []byte("package sub\n"), 0o644))
				tracker.RecordRead(ctx, sess.ID, file)
			}

			var tool fantasy.AgentTool
			input := `{"file_path":"sub/a.go","old_string":"package sub","new_string":"package sub2"}`
			switch name {
			case tools.ViewToolName:
				tool = tools.NewViewTool(nil, env.permissions, tracker, nil, c.subdirectoryContext, env.workingDir)
				input = `{"file_path":"sub/a.go"}`
			case tools.WriteToolName:
				tool = tools.NewWriteTool(nil, env.permissions, env.history, tracker, c.subdirectoryContext, env.workingDir)
				input = `{"file_path":"sub/a.go","content":"package sub\n"}`
			case tools.EditToolName:
				tool = tools.NewEditTool(nil, env.permissions, env.history, tracker, c.subdirectoryContext, env.workingDir)
			case tools.MultiEditToolName:
				tool = tools.NewMultiEditTool(nil, env.permissions, env.history, tracker, c.subdirectoryContext, env.workingDir)
				input = `{"file_path":"sub/a.go","edits":[{"old_string":"package sub","new_string":"package sub2"}]}`
			}
			resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "c1", Name: name, Input: input})
			require.NoError(t, err)
			require.False(t, resp.IsError, resp.Content)
			require.Contains(t, resp.Content, "the sub rules")
		})
	}
}

// A subdirectory context file over 40,000 characters is given cut at the
// limit, with a note naming the file and pointing at the view tool; one
// under it is given whole. Context files loaded at the start of a session
// are not cut.
func TestSubdirectoryContextCutsAFileOverTheLimit(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sub := filepath.Join(env.workingDir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	long := strings.Repeat("x", 40001) + "\nEND\n"
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte(long), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "AGENTS.md"), []byte(long), 0o644))
	c := &coordinator{
		cfg:               config.NewTestStoreIn(&config.Config{Options: &config.Options{ContextPaths: []string{"AGENTS.md"}}}, env.workingDir),
		sessions:          env.sessions,
		subdirectoryGiven: map[string]bool{},
	}
	one, err := env.sessions.Create(t.Context(), "one")
	require.NoError(t, err)
	file := filepath.Join(sub, "main.go")

	got := c.subdirectoryContext(t.Context(), one.ID, file)
	require.Contains(t, got, "over 40000 characters", "cut with a note")
	require.Contains(t, got, filepath.Join(sub, "AGENTS.md"), "the note names the file")
	require.Contains(t, got, "the view tool", "the note says to read the rest with the view tool")
	require.NotContains(t, got, "END", "the end of the file is not given")
	// The part given is the first 40,000 characters of the file.
	prefix, _, _ := strings.Cut(got, "over 40000 characters")
	require.Contains(t, prefix, strings.Repeat("x", 40000))

	// Under the limit, the file is given whole.
	short := strings.Repeat("y", 39995) + "\nEND\n"
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte(short), 0o644))
	two, err := env.sessions.Create(t.Context(), "two")
	require.NoError(t, err)
	got = c.subdirectoryContext(t.Context(), two.ID, file)
	require.NotContains(t, got, "over 40000 characters", "under the limit is not cut")
	require.Contains(t, got, "END")

	// Multi-byte UTF-8: 40,000 two-byte characters are 80,000 bytes but
	// only 40,000 characters, so they are not cut. A byte-based limit
	// would cut them at 20,000 characters.
	multibyte := strings.Repeat("é", 39995) + "\nEND\n"
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte(multibyte), 0o644))
	three, err := env.sessions.Create(t.Context(), "three")
	require.NoError(t, err)
	got = c.subdirectoryContext(t.Context(), three.ID, file)
	require.NotContains(t, got, "over 40000 characters", "multi-byte characters count as characters, not bytes")
	require.Contains(t, got, "END", "the whole file is given")

	// Direct check: onDemandContextFile counts runes, not bytes.
	cf := prompt.ContextFile{Path: "test.md", Content: strings.Repeat("é", 39995) + "\nEND\n"}
	require.NotContains(t, onDemandContextFile(cf), "over 40000 characters", "40,000 runes is not over the limit")
	cf = prompt.ContextFile{Path: "test.md", Content: strings.Repeat("é", 40001) + "\nEND\n"}
	require.Contains(t, onDemandContextFile(cf), "over 40000 characters", "40,006 runes is over the limit")

	// Over the limit in characters: 40,001 two-byte characters plus 5
	// more = 40,006 runes.
	multibyteLong := strings.Repeat("é", 40001) + "\nEND\n"
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte(multibyteLong), 0o644))
	four, err := env.sessions.Create(t.Context(), "four")
	require.NoError(t, err)
	got = c.subdirectoryContext(t.Context(), four.ID, file)
	require.Contains(t, got, "over 40000 characters", "cut at the character limit")
	require.NotContains(t, got, "END", "the end is not given")
	// The part given is valid UTF-8 and has exactly 40,000 characters.
	cutPrefix, _, _ := strings.Cut(got, "over 40000 characters")
	require.True(t, utf8.ValidString(cutPrefix), "the cut is on a character boundary")
}
