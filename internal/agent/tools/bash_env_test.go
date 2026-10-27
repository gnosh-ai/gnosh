package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

// Claude Code's BASH_DEFAULT_TIMEOUT_MS, BASH_MAX_TIMEOUT_MS and
// BASH_MAX_OUTPUT_LENGTH set how long a command runs before it moves to
// the background, the most a call's timeout can ask for, and how much of
// its output the model reads; unset or not a positive number, Klash's own
// limits hold.
func TestBashEnvLimits(t *testing.T) {
	limits := func(def, most, output string) (int, int, int, int) {
		t.Helper()
		t.Setenv("BASH_DEFAULT_TIMEOUT_MS", def)
		t.Setenv("BASH_MAX_TIMEOUT_MS", most)
		t.Setenv("BASH_MAX_OUTPUT_LENGTH", output)
		return defaultWaitSeconds(), maxWaitSeconds(), maxOutputLength(), timeoutSeconds(3_600_000)
	}

	def, most, output, hour := limits("", "", "")
	require.Equal(t, []int{120, 600, 30000, 600}, []int{def, most, output, hour}, "Klash's own limits")

	def, most, output, hour = limits("600000", "1800000", "60000")
	require.Equal(t, []int{600, 1800, 60000, 1800}, []int{def, most, output, hour})

	def, most, _, _ = limits("900000", "", "")
	require.Equal(t, 900, def)
	require.Equal(t, 900, most, "the most a call can ask for is at least the default")

	_, _, output, _ = limits("", "", "500000")
	require.Equal(t, 150000, output, "as in Claude Code, at most 150000")

	def, most, output, _ = limits("0", "-1", "lots")
	require.Equal(t, []int{120, 600, 30000}, []int{def, most, output}, "a value that is no positive number is ignored")

	require.Equal(t, 2, func() int { t.Setenv("BASH_DEFAULT_TIMEOUT_MS", "1001"); return defaultWaitSeconds() }(), "rounded up to whole seconds")
}

// The limits reach the tool: its description, the cut of a long output,
// and when a call with no timeout moves to the background.
func TestBashToolHonorsTheEnvLimits(t *testing.T) {
	t.Setenv("BASH_DEFAULT_TIMEOUT_MS", "1000")
	t.Setenv("BASH_MAX_TIMEOUT_MS", "1200000")
	t.Setenv("BASH_MAX_OUTPUT_LENGTH", "100")

	description := bashDescription(&config.Attribution{}, "m", nil, false, nil)
	require.Contains(t, description, "Commands exceeding 1 seconds")
	require.Contains(t, description, "up to 1200000")
	require.Contains(t, description, "Truncate if exceeds 100 characters")

	cut := TruncateOutput(strings.Repeat("x", 1000), t.TempDir())
	require.Less(t, len(cut), 300, "cut near 100 characters")
	require.Contains(t, cut, "lines truncated")

	tool := newBashToolForTest(t.TempDir())
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp := runBashTool(t, tool, ctx, BashParams{Description: "slow", Command: "sleep 3 && echo done"})
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Background, "a three second command moved to the background after one")
	require.NoError(t, shell.GetBackgroundShellManager().Kill(meta.ShellID))
}
