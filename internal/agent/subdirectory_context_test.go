package agent

import (
	"os"
	"path/filepath"
	"testing"

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
