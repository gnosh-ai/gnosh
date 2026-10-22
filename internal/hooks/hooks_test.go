package hooks

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

func TestAggregation(t *testing.T) {
	t.Parallel()

	t.Run("empty results", func(t *testing.T) {
		t.Parallel()
		agg := aggregate(nil, "{}")
		require.Equal(t, DecisionNone, agg.Decision)
		require.Empty(t, agg.Reason)
		require.Empty(t, agg.Context)
		require.False(t, agg.Halt)
	})

	t.Run("single allow", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow},
		}, "{}")
		require.Equal(t, DecisionAllow, agg.Decision)
	})

	t.Run("deny wins over allow", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, Context: "ctx1"},
			{Decision: DecisionDeny, Reason: "blocked"},
		}, "{}")
		require.Equal(t, DecisionDeny, agg.Decision)
		require.Equal(t, "blocked", agg.Reason)
		require.Equal(t, "ctx1", agg.Context)
	})

	t.Run("multiple deny reasons concatenated", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionDeny, Reason: "reason1"},
			{Decision: DecisionDeny, Reason: "reason2"},
		}, "{}")
		require.Equal(t, DecisionDeny, agg.Decision)
		require.Equal(t, "reason1\nreason2", agg.Reason)
	})

	t.Run("context concatenated from all hooks", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, Context: "ctx-a"},
			{Decision: DecisionNone, Context: "ctx-b"},
		}, "{}")
		require.Equal(t, DecisionAllow, agg.Decision)
		require.Equal(t, "ctx-a\nctx-b", agg.Context)
	})

	t.Run("allow wins over none", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionNone},
			{Decision: DecisionAllow},
		}, "{}")
		require.Equal(t, DecisionAllow, agg.Decision)
	})

	t.Run("halt is sticky across results", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow},
			{Halt: true, Reason: "stop now"},
		}, "{}")
		require.True(t, agg.Halt)
		require.Contains(t, agg.Reason, "stop now")
	})

	t.Run("halt with deny only records reason once", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionDeny, Halt: true, Reason: "stop"},
		}, "{}")
		require.True(t, agg.Halt)
		require.Equal(t, DecisionDeny, agg.Decision)
		require.Equal(t, "stop", agg.Reason)
	})
}

func TestParseStdout(t *testing.T) {
	t.Parallel()

	t.Run("empty stdout", func(t *testing.T) {
		t.Parallel()
		r := parseStdout("")
		require.Equal(t, DecisionNone, r.Decision)
	})

	t.Run("valid allow", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","context":"some context"}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, "some context", r.Context)
	})

	t.Run("valid deny", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"deny","reason":"not allowed"}`)
		require.Equal(t, DecisionDeny, r.Decision)
		require.Equal(t, "not allowed", r.Reason)
	})

	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{bad json}`)
		require.Equal(t, DecisionNone, r.Decision)
	})

	t.Run("unknown decision", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"maybe"}`)
		require.Equal(t, DecisionNone, r.Decision)
	})

	t.Run("version 1 accepted", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"version":1,"decision":"allow"}`)
		require.Equal(t, DecisionAllow, r.Decision)
	})

	t.Run("unknown higher version still parses", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"version":99,"decision":"deny","reason":"future"}`)
		require.Equal(t, DecisionDeny, r.Decision)
		require.Equal(t, "future", r.Reason)
	})

	t.Run("halt true without decision", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"halt":true,"reason":"turn over"}`)
		require.True(t, r.Halt)
		require.Equal(t, "turn over", r.Reason)
		require.Equal(t, DecisionNone, r.Decision)
	})

	t.Run("context string form", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","context":"one note"}`)
		require.Equal(t, "one note", r.Context)
	})

	t.Run("context array form", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","context":["first","second"]}`)
		require.Equal(t, "first\nsecond", r.Context)
	})

	t.Run("context array drops empty entries", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","context":["","keep",""]}`)
		require.Equal(t, "keep", r.Context)
	})

	t.Run("context null becomes empty", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","context":null}`)
		require.Empty(t, r.Context)
	})
}

func TestBuildEnv(t *testing.T) {
	t.Parallel()

	env := BuildEnv(EventPreToolUse, "bash", "sess-1", "/work", "/project", `{"command":"ls","file_path":"/tmp/f.txt"}`)

	envMap := make(map[string]string)
	for _, e := range env {
		parts := splitFirst(e, "=")
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// Each variable is set under its CRASH_ name and under the CRUSH_ name
	// that hooks written before the rename read.
	for _, prefix := range []string{"CRASH_", "CRUSH_"} {
		require.Equal(t, EventPreToolUse, envMap[prefix+"EVENT"])
		require.Equal(t, "bash", envMap[prefix+"TOOL_NAME"])
		require.Equal(t, "sess-1", envMap[prefix+"SESSION_ID"])
		require.Equal(t, "/work", envMap[prefix+"CWD"])
		require.Equal(t, "/project", envMap[prefix+"PROJECT_DIR"])
		require.Equal(t, "ls", envMap[prefix+"TOOL_INPUT_COMMAND"])
		require.Equal(t, "/tmp/f.txt", envMap[prefix+"TOOL_INPUT_FILE_PATH"])
	}

	// Shared Crush markers must be present so hook-authored scripts can
	// detect they're running under Crush the same way bash-tool-invoked
	// scripts can.
	require.Equal(t, "1", envMap["CRASH"])
	require.Equal(t, "1", envMap["CRUSH"])
	require.Equal(t, "crush", envMap["AGENT"])
	require.Equal(t, "crush", envMap["AI_AGENT"])
}

func splitFirst(s, sep string) []string {
	before, after, found := strings.Cut(s, sep)
	if !found {
		return []string{s}
	}
	return []string{before, after}
}

func TestBuildPayload(t *testing.T) {
	t.Parallel()
	payload := BuildPayload(EventPreToolUse, "sess-1", "/work", "bash", `{"command":"ls"}`)
	s := string(payload)
	require.Contains(t, s, `"event":"`+EventPreToolUse+`"`)
	require.Contains(t, s, `"tool_name":"bash"`)
	// tool_input should be an object, not a string.
	require.Contains(t, s, `"tool_input":{"command":"ls"}`)
}

// Every payload names its event under Claude Code's key as well, which
// Claude Code hooks read to tell events apart.
func TestPayloadsCarryHookEventName(t *testing.T) {
	t.Parallel()
	for event, payload := range map[string][]byte{
		EventPreToolUse:  BuildPayload(EventPreToolUse, "s", "/w", "bash", `{}`),
		EventPostToolUse: BuildPostPayload("s", "/w", "bash", `{}`, "out", ""),
		EventStop:        BuildStopPayload("s", "/w", "done", false),
	} {
		var got struct {
			Event         string `json:"event"`
			HookEventName string `json:"hook_event_name"`
		}
		require.NoError(t, json.Unmarshal(payload, &got))
		require.Equal(t, event, got.Event)
		require.Equal(t, event, got.HookEventName)
	}
}

func TestRunnerExitCode0Allow(t *testing.T) {
	t.Parallel()
	hookCfg := config.HookConfig{
		Command: `echo '{"decision":"allow","context":"ok"}'`,
	}
	r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, result.Decision)
	require.Equal(t, "ok", result.Context)
}

func TestRunnerExitCode2Deny(t *testing.T) {
	t.Parallel()
	hookCfg := config.HookConfig{
		Command: `echo "forbidden" >&2; exit 2`,
	}
	r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.False(t, result.Halt)
	require.Equal(t, "forbidden", result.Reason)
}

func TestRunnerExitCode49Halt(t *testing.T) {
	t.Parallel()
	hookCfg := config.HookConfig{
		Command: `echo "stop the turn" >&2; exit 49`,
	}
	r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.True(t, result.Halt)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "stop the turn", result.Reason)
}

func TestRunnerHaltViaJSON(t *testing.T) {
	t.Parallel()
	hookCfg := config.HookConfig{
		Command: `echo '{"halt":true,"reason":"via json"}'`,
	}
	r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.True(t, result.Halt)
	require.Equal(t, "via json", result.Reason)
}

func TestRunnerExitCodeOtherNonBlocking(t *testing.T) {
	t.Parallel()
	hookCfg := config.HookConfig{
		Command: `exit 1`,
	}
	r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
}

func TestRunnerTimeout(t *testing.T) {
	t.Parallel()
	hookCfg := config.HookConfig{
		Command: `sleep 10`,
		Timeout: 1,
	}
	r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())
	start := time.Now()
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.Less(t, elapsed, 5*time.Second)
}

func TestRunnerDeduplication(t *testing.T) {
	t.Parallel()
	// Two hooks with the same command should only run once.
	hookCfg := config.HookConfig{
		Command: `echo '{"decision":"allow"}'`,
	}
	r := NewRunner([]config.HookConfig{hookCfg, hookCfg}, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, result.Decision)
}

func TestRunnerNoMatchingHooks(t *testing.T) {
	t.Parallel()
	// Hooks are empty.
	r := NewRunner(nil, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
}

// validatedHooks builds hook configs and runs ValidateHooks to compile
// matcher regexes, mirroring the real config-load path.
func validatedHooks(t *testing.T, hooks []config.HookConfig) []config.HookConfig {
	t.Helper()
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			EventPreToolUse: hooks,
		},
	}
	require.NoError(t, cfg.ValidateHooks())
	return cfg.Hooks[EventPreToolUse]
}

func TestRunnerMatcherFiltering(t *testing.T) {
	t.Parallel()

	t.Run("compiled regex matches", func(t *testing.T) {
		t.Parallel()
		hooks := validatedHooks(t, []config.HookConfig{
			{Command: `echo '{"decision":"deny","reason":"blocked"}'`, Matcher: "^bash$"},
		})
		r := NewRunner(hooks, t.TempDir(), t.TempDir())
		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionDeny, result.Decision)
	})

	t.Run("compiled regex does not match", func(t *testing.T) {
		t.Parallel()
		hooks := validatedHooks(t, []config.HookConfig{
			{Command: `echo '{"decision":"deny","reason":"blocked"}'`, Matcher: "^edit$"},
		})
		r := NewRunner(hooks, t.TempDir(), t.TempDir())
		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionNone, result.Decision)
	})

	t.Run("no matcher matches everything", func(t *testing.T) {
		t.Parallel()
		hooks := validatedHooks(t, []config.HookConfig{
			{Command: `echo '{"decision":"allow"}'`},
		})
		r := NewRunner(hooks, t.TempDir(), t.TempDir())
		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionAllow, result.Decision)
	})

	t.Run("partial regex match", func(t *testing.T) {
		t.Parallel()
		hooks := validatedHooks(t, []config.HookConfig{
			{Command: `echo '{"decision":"deny","reason":"mcp blocked"}'`, Matcher: "^mcp_"},
		})
		r := NewRunner(hooks, t.TempDir(), t.TempDir())

		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "mcp_github_get_me", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionDeny, result.Decision)

		result, err = r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionNone, result.Decision)
	})

	// Runner must compile matchers itself; it cannot rely on
	// ValidateHooks having run first. This is the guarantee that prevents
	// the reload-drops-matcher class of bug.
	t.Run("runner compiles matcher without ValidateHooks", func(t *testing.T) {
		t.Parallel()
		raw := []config.HookConfig{
			{Command: `echo '{"decision":"deny","reason":"blocked"}'`, Matcher: "^bash$"},
		}
		r := NewRunner(raw, t.TempDir(), t.TempDir())

		deny, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionDeny, deny.Decision)

		noop, err := r.Run(context.Background(), EventPreToolUse, "sess", "view", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionNone, noop.Decision)
	})

	// A matcher that fails to compile at Runner construction must not
	// degrade to match-everything; the hook is dropped instead.
	t.Run("runner skips hooks with invalid matcher", func(t *testing.T) {
		t.Parallel()
		raw := []config.HookConfig{
			{Command: `echo '{"decision":"deny","reason":"should not fire"}'`, Matcher: "[invalid"},
		}
		r := NewRunner(raw, t.TempDir(), t.TempDir())

		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionNone, result.Decision)
		require.Empty(t, r.Hooks())
	})
}

func TestValidateHooksInvalidRegex(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			EventPreToolUse: {
				{Command: "true", Matcher: "[invalid"},
			},
		},
	}
	err := cfg.ValidateHooks()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid matcher regex")
}

func TestValidateHooksEmptyCommand(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			EventPreToolUse: {
				{Command: ""},
			},
		},
	}
	err := cfg.ValidateHooks()
	require.Error(t, err)
	require.Contains(t, err.Error(), "command is required")
}

// Event names are matched exactly. The old loader folded case and
// stripped underscores so "pre_tool_use" still worked; that forgiveness
// is gone, because it rescued small typos while leaving every other
// misspelling as a key nothing reads — a hook that looks configured and
// silently never runs.
func TestValidateHooksRejectsUnknownEventNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{"lowercase", "pretooluse"},
		{"snake_case", "pre_tool_use"},
		{"upper_snake", "PRE_TOOL_USE"},
		{"mixed_case", "preToolUse"},
		{"kebab_case", "pre-tool-use"},
		{"near_miss", "PreToolUsage"},
		{"lowercase_stop", "stop"},
		{"not_an_event", "UserPromptSubmit"},
		{"nonsense", "banana"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Hooks: map[string][]config.HookConfig{
					tt.input: {{Command: "true"}},
				},
			}
			err := cfg.ValidateHooks()
			require.Error(t, err, "an event name Crush does not fire must be a load error")
			require.Contains(t, err.Error(), "unknown event name")
			require.Contains(t, err.Error(), EventPreToolUse,
				"the error must list the names that do work")
			require.Contains(t, err.Error(), EventPostToolUse)
			require.Contains(t, err.Error(), EventStop)
		})
	}
}

func TestValidateHooksAcceptsKnownEventNames(t *testing.T) {
	t.Parallel()

	for _, event := range config.KnownHookEvents() {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Hooks: map[string][]config.HookConfig{
					event: {{Command: "true"}},
				},
			}
			require.NoError(t, cfg.ValidateHooks())
			require.Len(t, cfg.Hooks[event], 1,
				"a valid event name must be left exactly as written")
		})
	}
}

// A Stop hook fires when the turn ends, with no tool in play, so a
// matcher could never match anything. Rejecting it keeps it from
// becoming another silently dead setting.
func TestValidateHooksRejectsMatcherOnStop(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			EventStop: {{Command: "true", Matcher: "^bash$"}},
		},
	}
	err := cfg.ValidateHooks()
	require.Error(t, err)
	require.Contains(t, err.Error(), "matcher is not supported")
	require.Contains(t, err.Error(), EventStop)
}

// A PostToolUse hook has nothing left to approve or rewrite once the call
// has run: an allow counts as no opinion and an input rewrite is dropped,
// while a deny stays, as feedback. Its matcher is tested against the tool
// name, as a PreToolUse hook's is.
func TestRunPostToolUse(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			EventPostToolUse: {{
				Command: `echo '{"decision":"allow","updated_input":{"command":"rm -rf /"}}'`,
				Matcher: "^bash$",
			}},
		},
	}
	require.NoError(t, cfg.ValidateHooks(), "a PostToolUse hook may carry a matcher")
	r := NewRunner(cfg.Hooks[EventPostToolUse], t.TempDir(), t.TempDir())

	result, err := r.RunPostToolUse(context.Background(), "sess", "bash", `{"command":"ls"}`, "out", "", 0)
	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	require.Empty(t, result.UpdatedInput)
	require.Len(t, result.Hooks, 1)
	require.False(t, result.Hooks[0].InputRewrite)
	require.Equal(t, EventPostToolUse, result.Hooks[0].Event)

	result, err = r.RunPostToolUse(context.Background(), "sess", "edit", `{}`, "out", "", 0)
	require.NoError(t, err)
	require.Zero(t, result.HookCount, "the matcher keeps it to bash")

	deny := NewRunner([]config.HookConfig{{Command: `echo "fix the lint" >&2; exit 2`}}, t.TempDir(), t.TempDir())
	result, err = deny.RunPostToolUse(context.Background(), "sess", "edit", `{}`, "out", "", 0)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "fix the lint", result.Reason)
}

// tool_response carries the tool's metadata only when it is a JSON object.
func TestBuildPostPayloadMetadata(t *testing.T) {
	t.Parallel()

	for metadata, want := range map[string]string{
		`{"exit_code":2}`: `{"output":"out","metadata":{"exit_code":2}}`,
		"":                `{"output":"out"}`,
		"[1]":             `{"output":"out"}`,
		"not json":        `{"output":"out"}`,
	} {
		var payload struct {
			Event        string          `json:"event"`
			ToolResponse json.RawMessage `json:"tool_response"`
		}
		require.NoError(t, json.Unmarshal(BuildPostPayload("sess", "/cwd", "bash", `{}`, "out", metadata), &payload))
		require.Equal(t, EventPostToolUse, payload.Event)
		require.JSONEq(t, want, string(payload.ToolResponse), metadata)
	}
}

func TestValidateHooksReportsTheOffendingEventFirst(t *testing.T) {
	t.Parallel()

	// Two bad events in one config: iteration is sorted so the message
	// is the same on every run rather than whichever key Go hands back.
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			"Zebra": {{Command: "true"}},
			"Alpha": {{Command: "true"}},
		},
	}
	for range 10 {
		err := cfg.ValidateHooks()
		require.Error(t, err)
		require.Contains(t, err.Error(), `hook "Alpha"`,
			"map iteration order must not leak into the error message")
	}
}

func TestRunnerHookNameUsesDisplayName(t *testing.T) {
	t.Parallel()

	t.Run("name field is used when set", func(t *testing.T) {
		t.Parallel()
		hookCfg := config.HookConfig{
			Name:    "my-hook",
			Command: `echo '{"decision":"allow"}'`,
		}
		r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())
		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionAllow, result.Decision)
		require.Len(t, result.Hooks, 1)
		require.Equal(t, "my-hook", result.Hooks[0].Name)
	})

	t.Run("command is used when name is empty", func(t *testing.T) {
		t.Parallel()
		hookCfg := config.HookConfig{
			Command: `echo '{"decision":"allow"}'`,
		}
		r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())
		result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
		require.NoError(t, err)
		require.Equal(t, DecisionAllow, result.Decision)
		require.Len(t, result.Hooks, 1)
		require.Equal(t, `echo '{"decision":"allow"}'`, result.Hooks[0].Name)
	})
}

func TestRunnerParallelExecution(t *testing.T) {
	t.Parallel()
	// Two hooks: one allows, one denies. Deny should win.
	hooks := []config.HookConfig{
		{Command: `echo '{"decision":"allow","context":"hook1"}'`},
		{Command: `echo '{"decision":"deny","reason":"nope"}' ; exit 0`},
	}
	r := NewRunner(hooks, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "nope", result.Reason)
}

func TestRunnerEnvVarsPropagated(t *testing.T) {
	t.Parallel()
	hookCfg := config.HookConfig{
		Command: `printf '{"decision":"allow","context":"%s"}' "$CRUSH_TOOL_NAME"`,
	}
	r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, result.Decision)
	require.Equal(t, "bash", result.Context)
}

func TestParseStdoutUpdatedInput(t *testing.T) {
	t.Parallel()

	t.Run("nested object", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","updated_input":{"command":"rtk cat foo.go"}}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, `{"command":"rtk cat foo.go"}`, r.UpdatedInput)
	})

	t.Run("stringified backward compat", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","updated_input":"{\"command\":\"rtk cat foo.go\"}"}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, `{"command":"rtk cat foo.go"}`, r.UpdatedInput)
	})

	t.Run("no updated_input", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow"}`)
		require.Empty(t, r.UpdatedInput)
	})
}

func TestAggregationUpdatedInput(t *testing.T) {
	t.Parallel()

	t.Run("patches merge in config order with later overriding", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `{"command":"first","keep":"me"}`},
			{Decision: DecisionAllow, UpdatedInput: `{"command":"second"}`},
		}, `{"command":"orig","timeout":60}`)
		require.Equal(t, DecisionAllow, agg.Decision)
		// command overridden by second patch; keep preserved from first
		// patch; timeout preserved from original input.
		require.JSONEq(
			t,
			`{"command":"second","keep":"me","timeout":60}`,
			agg.UpdatedInput,
		)
	})

	t.Run("shallow: nested objects are replaced wholesale", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `{"env":{"FOO":"bar"}}`},
		}, `{"env":{"BAZ":"qux"},"command":"ls"}`)
		// "env" is replaced entirely; "command" preserved.
		require.JSONEq(
			t,
			`{"env":{"FOO":"bar"},"command":"ls"}`,
			agg.UpdatedInput,
		)
	})

	t.Run("deny still reports merged input (caller ignores it)", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `{"command":"rewritten"}`},
			{Decision: DecisionDeny, Reason: "blocked"},
		}, `{"command":"orig"}`)
		require.Equal(t, DecisionDeny, agg.Decision)
	})

	t.Run("no patches leaves updated_input empty", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow},
			{Decision: DecisionNone},
		}, `{"command":"orig"}`)
		require.Empty(t, agg.UpdatedInput)
	})

	t.Run("invalid patch is ignored", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `"not-an-object"`},
			{Decision: DecisionAllow, UpdatedInput: `{"command":"good"}`},
		}, `{"command":"orig"}`)
		require.JSONEq(t, `{"command":"good"}`, agg.UpdatedInput)
	})

	t.Run("malformed patch JSON is ignored and merge continues", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `{broken json`},
			{Decision: DecisionAllow, UpdatedInput: `{"command":"good"}`},
		}, `{"command":"orig"}`)
		require.JSONEq(t, `{"command":"good"}`, agg.UpdatedInput)
	})

	t.Run("non-object tool_input rejects all patches", func(t *testing.T) {
		t.Parallel()
		agg := aggregate([]HookResult{
			{Decision: DecisionAllow, UpdatedInput: `{"command":"rewrite"}`},
		}, `"just-a-string"`)
		require.Empty(t, agg.UpdatedInput)
	})

	t.Run("null updated_input is a no-op", func(t *testing.T) {
		t.Parallel()
		// parseStdout converts null updated_input to "", so aggregate
		// never sees a patch — the merged input is empty and the
		// original tool_input is used unchanged.
		r := parseStdout(`{"decision":"allow","updated_input":null}`)
		require.Empty(t, r.UpdatedInput)
		agg := aggregate([]HookResult{r}, `{"command":"orig"}`)
		require.Empty(t, agg.UpdatedInput)
	})
}

// TestRunnerAbandonRaceSafety verifies that if a hook's shell execution
// does not yield to ctx cancellation within abandonGrace, runOne returns
// promptly and never touches the shared stdout/stderr buffers again —
// even while the abandoned goroutine continues to write to them.
//
// The substitute shell executor ignores ctx entirely, writes to Stdout
// both before and after the abandon deadline, and only then returns.
// Under -race this catches any code path in runOne that reads those
// buffers after returning the DecisionNone abandon result.
//
// The abandoned goroutine outlives r.Run, so the test also gates its
// shutdown on the stub having been entered (started): wg.Add runs inside
// the stub, and under a loaded scheduler the goroutine may not be
// scheduled until after the test body returned. Waiting on started
// orders wg.Add (and the goroutine's read of the runShell variable)
// before the cleanup's wg.Wait and the cleanup restoring runShell;
// without it both pairs are an Add-after-Wait misuse that -race flags.
func TestRunnerAbandonRaceSafety(t *testing.T) {
	origRunShell := runShell
	t.Cleanup(func() { runShell = origRunShell })

	// Synchronize shutdown with the abandoned goroutine so the test
	// exits cleanly even under -race.
	var wg sync.WaitGroup
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		wg.Wait()
	})

	runShell = func(_ context.Context, opts shell.RunOptions) error {
		wg.Add(1)
		defer wg.Done()
		close(started)
		// Write before the caller observes ctx.Done(); the caller will
		// not read the buffer while we still own it.
		_, _ = io.WriteString(opts.Stdout, "before\n")
		// Hold past ctx deadline + abandonGrace so the caller takes
		// the abandon branch, then continue writing. If the caller
		// reads these buffers after abandoning, -race will flag it.
		select {
		case <-time.After(5 * time.Second):
		case <-release:
		}
		_, _ = io.WriteString(opts.Stdout, "after\n")
		return nil
	}

	hookCfg := config.HookConfig{
		Command: "# irrelevant; runShell is stubbed",
		Timeout: 1,
	}
	r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())

	start := time.Now()
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{}`)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Equal(t, DecisionNone, result.Decision)
	// Abandon must happen at ~timeout + abandonGrace. Allow generous
	// slack so CI noise doesn't flake the test.
	require.Less(t, elapsed, 3500*time.Millisecond,
		"runOne should return within timeout+abandonGrace+slack")

	// Block until the abandoned goroutine has entered the stub. In the
	// common case it was entered long before the abandon deadline and
	// this returns immediately; in the pathological case the goroutine
	// was starved for the whole abandon window, and this waits for it to
	// run so the cleanups below stay ordered after its wg.Add and its
	// read of the runShell variable.
	<-started
}

func TestRunnerUpdatedInput(t *testing.T) {
	t.Parallel()
	hookCfg := config.HookConfig{
		Command: `echo '{"decision":"allow","updated_input":{"command":"echo rewritten"}}'`,
	}
	r := NewRunner([]config.HookConfig{hookCfg}, t.TempDir(), t.TempDir())
	result, err := r.Run(context.Background(), EventPreToolUse, "sess", "bash", `{"command":"echo original","timeout":60}`)
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, result.Decision)
	require.JSONEq(
		t,
		`{"command":"echo rewritten","timeout":60}`,
		result.UpdatedInput,
	)
}

func TestParseStdoutClaudeCodeFormat(t *testing.T) {
	t.Parallel()

	t.Run("allow with reason", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"allow","permissionDecisionReason":"RTK auto-rewrite"}}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, "RTK auto-rewrite", r.Reason)
	})

	t.Run("allow with updatedInput", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"allow","updatedInput":{"command":"rtk cat foo.go"}}}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, `{"command":"rtk cat foo.go"}`, r.UpdatedInput)
	})

	t.Run("deny", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"not allowed"}}`)
		require.Equal(t, DecisionDeny, r.Decision)
		require.Equal(t, "not allowed", r.Reason)
	})

	t.Run("no permissionDecision", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"hookSpecificOutput":{}}`)
		require.Equal(t, DecisionNone, r.Decision)
	})

	t.Run("crush format still works", func(t *testing.T) {
		t.Parallel()
		r := parseStdout(`{"decision":"allow","context":"hello"}`)
		require.Equal(t, DecisionAllow, r.Decision)
		require.Equal(t, "hello", r.Context)
	})
}

// A matcher written for a tool's name from before the rename to Crash
// still matches the tool under its new name.
func TestMatcherMatchesALegacyToolName(t *testing.T) {
	t.Parallel()

	r := NewRunner([]config.HookConfig{{Command: "true", Matcher: "^crush_logs$"}}, t.TempDir(), t.TempDir())
	require.Len(t, r.matchingHooks("crash_logs"), 1)
	require.Empty(t, r.matchingHooks("bash"))
}

// A PostToolUseFailure hook gets the failed call and its error, as in
// Claude Code, and a block is feedback, as for PostToolUse.
func TestRunPostToolUseFailure(t *testing.T) {
	t.Parallel()

	payload := filepath.Join(t.TempDir(), "payload.json")
	r := NewRunner([]config.HookConfig{{Command: "cat > " + payload + `; echo "check the path" >&2; exit 2`}}, t.TempDir(), t.TempDir())
	result, err := r.RunPostToolUseFailure(context.Background(), "sess", "view", `{"file_path":"x.go"}`, "file not found: x.go", false, 1500*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, result.Decision)
	require.Equal(t, "check the path", result.Reason)
	require.Equal(t, EventPostToolUseFailure, result.Hooks[0].Event)

	data, err := os.ReadFile(payload)
	require.NoError(t, err)
	var got struct {
		Event      string `json:"hook_event_name"`
		ToolName   string `json:"tool_name"`
		Error      string `json:"error"`
		DurationMS int64  `json:"duration_ms"`
	}
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, EventPostToolUseFailure, got.Event)
	require.Equal(t, "view", got.ToolName)
	require.Equal(t, "file not found: x.go", got.Error)
	require.Equal(t, int64(1500), got.DurationMS)
}
