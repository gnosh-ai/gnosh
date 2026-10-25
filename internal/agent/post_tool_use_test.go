package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/stretchr/testify/require"
)

// funcTool is a tool whose Run is the given function.
type funcTool struct {
	name string
	run  func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error)
}

func (f *funcTool) Info() fantasy.ToolInfo { return fantasy.ToolInfo{Name: f.name} }
func (f *funcTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return f.run(ctx, call)
}
func (f *funcTool) ProviderOptions() fantasy.ProviderOptions     { return nil }
func (f *funcTool) SetProviderOptions(_ fantasy.ProviderOptions) {}

// readRecorder is a filetracker.Service that records the reads it is told
// about.
type readRecorder struct{ reads []string }

func (r *readRecorder) RecordRead(_ context.Context, _, path string) { r.reads = append(r.reads, path) }

func (r *readRecorder) LastReadTime(context.Context, string, string) time.Time {
	return time.Time{}
}

func (r *readRecorder) UnchangedSinceRead(context.Context, string, string) bool { return false }

func (r *readRecorder) ListReadFiles(context.Context, string) ([]string, error) {
	return r.reads, nil
}

func (r *readRecorder) CopyReads(context.Context, string, string) error { return nil }

func (r *readRecorder) ForgetReadsSince(context.Context, string, int64) error { return nil }

// newPostRunner builds a hooks.Runner for one PostToolUse hook, through
// the config validation a real one passes.
func newPostRunner(t *testing.T, cmd string) *hooks.Runner {
	t.Helper()
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			hooks.EventPostToolUse: {{Command: cmd}},
		},
	}
	require.NoError(t, cfg.ValidateHooks())
	return hooks.NewRunner(cfg.Hooks[hooks.EventPostToolUse], t.TempDir(), t.TempDir())
}

func sessionCtx(t *testing.T) context.Context {
	return context.WithValue(t.Context(), tools.SessionIDContextKey, "s1")
}

// A PostToolUse block is feedback on a call that has already run: the
// agent reads the hook's reason after the tool's output, and the call
// still counts as a success.
func TestPostToolUseFeedbackReachesTheAgent(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "edit", resp: fantasy.NewTextResponse("edited")}
	tool := newHookedTool(inner, toolHooks{post: newPostRunner(t, `echo "lint: unused import" >&2; exit 2`)})

	resp, err := tool.Run(sessionCtx(t), fantasy.ToolCall{ID: "c1", Name: "edit", Input: `{}`})
	require.NoError(t, err)
	require.True(t, inner.called)
	require.False(t, resp.IsError)
	require.False(t, resp.StopTurn)
	require.Equal(t, "edited\nPostToolUse hook feedback: lint: unused import", resp.Content)

	var meta struct {
		Hook hooks.HookMetadata `json:"hook"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Len(t, meta.Hook.Hooks, 1)
	require.Equal(t, hooks.EventPostToolUse, meta.Hook.Hooks[0].Event)
	require.Equal(t, "deny", meta.Hook.Hooks[0].Decision)
	require.Equal(t, "none", meta.Hook.Decision, "the call itself was not denied")
}

// The hook gets what the tool returned as tool_response, as in Claude
// Code, along with the call.
func TestPostToolUseSeesTheToolResponse(t *testing.T) {
	t.Parallel()

	payload := filepath.Join(t.TempDir(), "payload.json")
	inner := &fakeTool{name: "bash", resp: fantasy.ToolResponse{Content: "hello", Metadata: `{"exit_code":0}`}}
	tool := newHookedTool(inner, toolHooks{post: newPostRunner(t, `cat > `+payload)})

	_, err := tool.Run(sessionCtx(t), fantasy.ToolCall{ID: "c1", Name: "bash", Input: `{"command":"echo hello"}`})
	require.NoError(t, err)

	data, err := os.ReadFile(payload)
	require.NoError(t, err)
	var got struct {
		Event        string          `json:"event"`
		SessionID    string          `json:"session_id"`
		ToolName     string          `json:"tool_name"`
		ToolUseID    string          `json:"tool_use_id"`
		ToolInput    json.RawMessage `json:"tool_input"`
		ToolResponse struct {
			Output   string          `json:"output"`
			Metadata json.RawMessage `json:"metadata"`
		} `json:"tool_response"`
	}
	require.NoError(t, json.Unmarshal(data, &got), string(data))
	require.Equal(t, hooks.EventPostToolUse, got.Event)
	require.Equal(t, "s1", got.SessionID)
	require.Equal(t, "bash", got.ToolName)
	require.Equal(t, "c1", got.ToolUseID)
	require.JSONEq(t, `{"command":"echo hello"}`, string(got.ToolInput))
	require.Equal(t, "hello", got.ToolResponse.Output)
	require.JSONEq(t, `{"exit_code":0}`, string(got.ToolResponse.Metadata))
}

// A call that failed does not fire PostToolUse hooks, as in Claude Code.
func TestPostToolUseSkipsAFailedCall(t *testing.T) {
	t.Parallel()

	marker := filepath.Join(t.TempDir(), "ran")
	inner := &fakeTool{name: "bash", resp: fantasy.NewTextErrorResponse("boom")}
	tool := newHookedTool(inner, toolHooks{post: newPostRunner(t, `touch `+marker)})

	resp, err := tool.Run(sessionCtx(t), fantasy.ToolCall{ID: "c1", Name: "bash", Input: `{}`})
	require.NoError(t, err)
	require.Equal(t, "boom", resp.Content)
	require.NoFileExists(t, marker)
}

// A halting PostToolUse hook ends the turn; context reaches the agent.
func TestPostToolUseHaltAndContext(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "bash", resp: fantasy.NewTextResponse("ok")}
	tool := newHookedTool(inner, toolHooks{post: newPostRunner(t, `echo "out of budget" >&2; exit 49`)})
	resp, err := tool.Run(sessionCtx(t), fantasy.ToolCall{ID: "c1", Name: "bash", Input: `{}`})
	require.NoError(t, err)
	require.True(t, resp.StopTurn)
	require.Contains(t, resp.Content, "Turn halted by a PostToolUse hook. Reason: out of budget")

	inner = &fakeTool{name: "bash", resp: fantasy.NewTextResponse("ok")}
	tool = newHookedTool(inner, toolHooks{post: newPostRunner(t,
		`echo '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"the build is red"}}'`)})
	resp, err = tool.Run(sessionCtx(t), fantasy.ToolCall{ID: "c2", Name: "bash", Input: `{}`})
	require.NoError(t, err)
	require.False(t, resp.StopTurn)
	require.Equal(t, "ok\nthe build is red", resp.Content)
}

// A formatter run from a PostToolUse hook changes the file the agent just
// edited. Without a fresh read recorded, its next edit of the file would
// be refused as modified since it was read.
func TestPostToolUseResyncsAFileAHookRewrote(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	edit := &funcTool{name: tools.EditToolName, run: func(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
		if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
			return fantasy.ToolResponse{}, err
		}
		// Back-date the write, so that a hook touching the file moves its
		// time however coarse the filesystem's clock is.
		old := time.Now().Add(-time.Hour)
		return fantasy.NewTextResponse("edited"), os.Chtimes(file, old, old)
	}}
	call := fantasy.ToolCall{ID: "c1", Name: tools.EditToolName, Input: `{"file_path":"main.go"}`}

	t.Run("rewritten", func(t *testing.T) {
		files := &readRecorder{}
		tool := newHookedTool(edit, toolHooks{
			post:       newPostRunner(t, `printf 'package main\n\n' > `+file),
			files:      files,
			workingDir: dir,
		})
		resp, err := tool.Run(sessionCtx(t), call)
		require.NoError(t, err)
		require.Equal(t, []string{file}, files.reads)
		require.Contains(t, resp.Content, "A PostToolUse hook changed "+file+" after this call")
	})

	// lsp_replace_symbol writes the file it names, and records a read of
	// it, as edit does; a formatter's Edit hook now runs after it too.
	t.Run("rewritten after lsp_replace_symbol", func(t *testing.T) {
		files := &readRecorder{}
		replace := &funcTool{name: tools.ReplaceSymbolToolName, run: edit.run}
		tool := newHookedTool(replace, toolHooks{
			post:       newPostRunner(t, `printf 'package main\n\n' > `+file),
			files:      files,
			workingDir: dir,
		})
		resp, err := tool.Run(sessionCtx(t), fantasy.ToolCall{ID: "c2", Name: tools.ReplaceSymbolToolName, Input: `{"symbol":"main","file_path":"main.go"}`})
		require.NoError(t, err)
		require.Equal(t, []string{file}, files.reads)
		require.Contains(t, resp.Content, "A PostToolUse hook changed "+file+" after this call")
	})

	t.Run("touched, same content", func(t *testing.T) {
		files := &readRecorder{}
		tool := newHookedTool(edit, toolHooks{post: newPostRunner(t, `touch `+file), files: files, workingDir: dir})
		resp, err := tool.Run(sessionCtx(t), call)
		require.NoError(t, err)
		require.Equal(t, []string{file}, files.reads, "a fresh read keeps the next edit from being refused")
		require.Equal(t, "edited", resp.Content, "nothing changed that the agent needs to hear about")
	})

	t.Run("left alone", func(t *testing.T) {
		files := &readRecorder{}
		tool := newHookedTool(edit, toolHooks{post: newPostRunner(t, `true`), files: files, workingDir: dir})
		resp, err := tool.Run(sessionCtx(t), call)
		require.NoError(t, err)
		require.Empty(t, files.reads)
		require.Equal(t, "edited", resp.Content)
	})
}

// PreToolUse and PostToolUse hooks both show on the tool line.
func TestPreAndPostToolUseHooksShareTheMetadata(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "bash", resp: fantasy.NewTextResponse("ok")}
	tool := newHookedTool(inner, toolHooks{
		pre:  newRunner(t, `echo '{"decision":"allow"}'`),
		post: newPostRunner(t, `exit 0`),
	})
	resp, err := tool.Run(sessionCtx(t), fantasy.ToolCall{ID: "c1", Name: "bash", Input: `{}`})
	require.NoError(t, err)

	var meta struct {
		Hook hooks.HookMetadata `json:"hook"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, 2, meta.Hook.HookCount)
	require.Equal(t, "allow", meta.Hook.Decision)
	require.Equal(t, hooks.EventPreToolUse, meta.Hook.Hooks[0].Event)
	require.Equal(t, hooks.EventPostToolUse, meta.Hook.Hooks[1].Event)
}

// A tool call's PreToolUse and PostToolUse hooks get the same
// tool_use_id, in the payload and in KLASH_TOOL_USE_ID, as in Claude Code,
// so they can tell they saw the same call.
func TestToolHooksShareTheToolUseID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	record := func(name string) string {
		return `printf '%s ' "$KLASH_TOOL_USE_ID" > ` + filepath.Join(dir, name+".env") + `; cat > ` + filepath.Join(dir, name+".json")
	}
	inner := &fakeTool{name: "bash", resp: fantasy.NewTextResponse("ok")}
	tool := newHookedTool(inner, toolHooks{pre: newRunner(t, record("pre")), post: newPostRunner(t, record("post"))})
	_, err := tool.Run(sessionCtx(t), fantasy.ToolCall{ID: "call-42", Name: "bash", Input: `{}`})
	require.NoError(t, err)

	for _, name := range []string{"pre", "post"} {
		env, err := os.ReadFile(filepath.Join(dir, name+".env"))
		require.NoError(t, err)
		require.Equal(t, "call-42 ", string(env), name)
		data, err := os.ReadFile(filepath.Join(dir, name+".json"))
		require.NoError(t, err)
		var payload struct {
			ToolUseID string `json:"tool_use_id"`
		}
		require.NoError(t, json.Unmarshal(data, &payload), name)
		require.Equal(t, "call-42", payload.ToolUseID, name)
	}
}

// A failed call fires the PostToolUseFailure hooks, not the PostToolUse
// ones, and what they say is added to the error the agent reads.
func TestPostToolUseFailureFeedback(t *testing.T) {
	t.Parallel()

	payload := filepath.Join(t.TempDir(), "payload.json")
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{
		hooks.EventPostToolUseFailure: {{Command: "cat > " + payload + `; echo "view it before you edit it" >&2; exit 2`, Matcher: "^edit$"}},
	}}
	require.NoError(t, cfg.ValidateHooks())
	failure := hooks.NewRunner(cfg.Hooks[hooks.EventPostToolUseFailure], t.TempDir(), t.TempDir())

	inner := &fakeTool{name: "edit", resp: fantasy.NewTextErrorResponse("old_string not found in file")}
	tool := newHookedTool(inner, toolHooks{post: newPostRunner(t, "touch "+payload+".post"), postFailure: failure})
	resp, err := tool.Run(sessionCtx(t), fantasy.ToolCall{ID: "c1", Name: "edit", Input: `{"file_path":"a.go"}`})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Equal(t, "old_string not found in file\nPostToolUseFailure hook feedback: view it before you edit it", resp.Content)
	require.NoFileExists(t, payload+".post", "PostToolUse does not fire for a failed call")

	data, err := os.ReadFile(payload)
	require.NoError(t, err)
	var got struct {
		Event string `json:"hook_event_name"`
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, hooks.EventPostToolUseFailure, got.Event)
	require.Equal(t, "old_string not found in file", got.Error)
}
