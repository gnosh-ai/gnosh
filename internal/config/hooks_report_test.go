package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// /hooks lists the hooks by event, in the order the events fire, each
// with its matcher ("*" for every tool), its command, pipes escaped so the
// table holds, and the timeout it runs under, the default included.
func TestHooksReport(t *testing.T) {
	t.Parallel()

	report := HooksReport(map[string][]HookConfig{
		EventStop:             {{Command: "~/.config/crush/hooks/stop-check.sh", Timeout: 900}},
		EventPreToolUse:       {{Matcher: "Bash", Command: "jq -r .tool_input.command | grep -q rm && exit 2"}},
		EventUserPromptSubmit: {{Name: "context", Command: "date\necho hi"}},
	})
	require.True(t, strings.HasPrefix(report, "## Hooks\n\n| Event | Matcher | Command | Timeout |\n"), report)
	rows := strings.Split(strings.TrimSpace(report), "\n")[4:]
	require.Equal(t, []string{
		`| PreToolUse | Bash | jq -r .tool_input.command \| grep -q rm && exit 2 | 600 s |`,
		`| UserPromptSubmit | * | context: date echo hi | 30 s |`,
		`| Stop | * | ~/.config/crush/hooks/stop-check.sh | 900 s |`,
	}, rows)

	require.Contains(t, HooksReport(nil), "No hooks are configured.")
}
