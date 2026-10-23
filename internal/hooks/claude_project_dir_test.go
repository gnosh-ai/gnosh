package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// A Claude Code hook command names its script by the project directory,
// "${CLAUDE_PROJECT_DIR}/.claude/hooks/check.sh", and runs the project's
// script. Without CLAUDE_PROJECT_DIR it ran /.claude/hooks/check.sh,
// which failed to start, and a hook that fails lets the call go ahead.
func TestClaudeCodeHookCommandFindsItsScript(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	script := filepath.Join(project, ".claude", "hooks", "check.sh")
	require.NoError(t, os.MkdirAll(filepath.Dir(script), 0o755))
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho 'not in this project' >&2\nexit 2\n"), 0o755))
	r := NewRunner([]config.HookConfig{{Command: `"${CLAUDE_PROJECT_DIR}/.claude/hooks/check.sh"`}}, project, project)

	result, err := r.Run(t.Context(), EventPreToolUse, "s1", "bash", `{"command": "ls"}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "not in this project", result.Reason)
}
