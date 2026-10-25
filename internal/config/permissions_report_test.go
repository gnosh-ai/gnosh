package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// /permissions shows the mode and what it means, the allow and deny rules
// each with where it comes from (its config key, or the --allowedTools and
// --disallowedTools flags), and the session's directories.
func TestPermissionsReport(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Permissions: &Permissions{
			Allow:        []string{"Bash(git diff *)", "Bash(npm test *)"},
			AllowedTools: []string{"view", "edit"},
			Deny:         []string{"Bash(curl * | sh)"},
		},
		Options: &Options{DisabledTools: []string{"sourcegraph", "fetch"}},
	}
	flags := &RuntimeOverrides{AllowRules: []string{"npm test *"}, AllowedTools: []string{"edit"}, DisabledTools: []string{"fetch"}, AddedDirs: []string{"/extra"}}
	report := PermissionsReport(cfg, flags, "/p", false)
	for _, want := range []string{
		"## Permissions\n\n**Mode:** default: anything the rules below do not allow is asked about.",
		"### Allow\n\n| Rule | From |",
		"| Bash(git diff *) | permissions.allow |",
		"| Bash(npm test *) | --allowedTools |",
		"| view | permissions.allowed_tools |",
		"| edit | --allowedTools |",
		"### Deny\n\n| Rule | From |",
		`| Bash(curl * \| sh) | permissions.deny |`,
		"| sourcegraph | options.disabled_tools |",
		"| fetch | --disallowedTools |",
		"| /p | working directory |",
		"| /extra | --add-dir or /add-dir |",
	} {
		require.Contains(t, report, want)
	}

	require.Contains(t, PermissionsReport(cfg, flags, "/p", true), "**Mode:** bypassPermissions:")
	require.Contains(t, PermissionsReport(cfg, &RuntimeOverrides{AcceptEdits: true}, "/p", false), "**Mode:** acceptEdits:")
	require.Contains(t, PermissionsReport(cfg, &RuntimeOverrides{DontAsk: true}, "/p", false), "**Mode:** dontAsk:")
	require.Contains(t, PermissionsReport(cfg, &RuntimeOverrides{DontAsk: true}, "/p", true), "**Mode:** bypassPermissions:",
		"yolo grants every request whatever the flags")

	empty := PermissionsReport(&Config{}, nil, "/p", false)
	require.Contains(t, empty, "### Allow\n\nNone.\n")
	require.Contains(t, empty, "### Deny\n\nNone.\n")
}
