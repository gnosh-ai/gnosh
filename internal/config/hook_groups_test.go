package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A Claude Code settings.json hooks block, pasted into klash.json as it
// is, loads as one Klash hook per command, in order, each under its
// group's matcher. This is the shape of Jae's own: SessionStart and
// UserPromptSubmit groups with no matcher. It was a config error before
// ("command is required"), so a Claude Code hooks block kept Klash from
// starting.
func TestClaudeCodeHookGroupsLoad(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "klash.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"hooks": {
		"SessionStart": [{"hooks": [
			{"type": "command", "command": "~/.claude/hooks-renice.sh"},
			{"type": "command", "command": "jmem-session.sh"},
			{"type": "command", "command": "python3 memsync.py --quiet", "timeout": 30}
		]}],
		"UserPromptSubmit": [{"matcher": "", "hooks": [{"type": "command", "command": "jmem-hook.sh"}]}],
		"PreToolUse": [
			{"matcher": "^bash$", "command": "./klash-style.sh"},
			{"matcher": "Edit|Write", "hooks": [
				{"type": "command", "command": "./a.sh", "statusMessage": "checking", "once": true, "shell": "bash", "async": false, "asyncRewake": false, "onFailure": "continue"},
				{"type": "command", "command": "./b.sh"}
			]}
		]
	}}`), 0o644))

	cfg, _, err := loadFromConfigPaths(t.Context(), []string{path})
	require.NoError(t, err)
	require.NoError(t, cfg.ValidateHooks())
	require.Equal(t, []HookConfig{
		{Command: "~/.claude/hooks-renice.sh"},
		{Command: "jmem-session.sh"},
		{Command: "python3 memsync.py --quiet", Timeout: 30},
	}, cfg.Hooks[EventSessionStart])
	require.Equal(t, []HookConfig{{Command: "jmem-hook.sh"}}, cfg.Hooks[EventUserPromptSubmit])
	require.Equal(t, []HookConfig{
		{Matcher: "^bash$", Command: "./klash-style.sh"},
		{Matcher: "Edit|Write", Command: "./a.sh"},
		{Matcher: "Edit|Write", Command: "./b.sh"},
	}, cfg.Hooks[EventPreToolUse])
}

// What Klash cannot do as Claude Code would is an error that names it,
// with where it is, not a hook run some other way than written.
func TestClaudeCodeHookGroupsRefuseWhatKlashCannotDo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		hook, want string
	}{
		{`{"type": "prompt", "prompt": "is it done?"}`, `hook type "prompt" is not supported`},
		{`{"type": "http", "url": "http://x"}`, `hook type "http" is not supported`},
		{`{"type": "command", "command": "x", "if": "Bash(rm *)"}`, `hooks.PreToolUse[0].hooks[0].if: not supported`},
		{`{"type": "command", "command": "x", "async": true}`, `background hooks are not supported`},
		{`{"type": "command", "command": "x", "asyncRewake": true}`, `background hooks are not supported`},
		{`{"type": "command", "command": "x", "shell": "powershell"}`, `"powershell" is not supported`},
		{`{"type": "command", "command": "x", "onFailure": "block"}`, `"block" is not supported`},
		{`{"type": "command", "command": "x", "args": ["-v"]}`, `"args" is not supported`},
		{`{"type": "command", "command": "x", "timeout": 1.5}`, `whole number of seconds`},
		{`{"command": "x"}`, `type: required`},
		{`{"type": "command"}`, `command: required`},
	} {
		data := []byte(`{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [` + tc.hook + `]}]}}`)
		_, err := flattenHookGroups(data)
		require.ErrorContains(t, err, tc.want, tc.hook)
	}

	_, err := flattenHookGroups([]byte(`{"hooks": {"Stop": [{"hooks": [], "description": "x"}]}}`))
	require.ErrorContains(t, err, `unknown field "description" in a hook group`)

	path := filepath.Join(t.TempDir(), "klash.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"hooks": {"Stop": [{"hooks": [{"type": "agent", "prompt": "check"}]}]}}`), 0o644))
	_, _, err = loadFromConfigPaths(t.Context(), []string{path})
	require.ErrorContains(t, err, path, "the error names the file")
}

// A file with no groups is left byte for byte as it is.
func TestFlattenHookGroupsLeavesKlashEntriesAlone(t *testing.T) {
	t.Parallel()

	data := []byte(`{"hooks": {"PreToolUse": [{"matcher": "^bash$", "command": "x"}]}, "options": {"debug": true}}`)
	out, err := flattenHookGroups(data)
	require.NoError(t, err)
	require.Equal(t, string(data), string(out))
}
