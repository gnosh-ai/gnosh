package agent

import (
	"context"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// fakeTool records the context it was invoked with so tests can assert on
// values stamped onto it by the hookedTool decorator.
type fakeTool struct {
	name   string
	called bool
	gotCtx context.Context
	resp   fantasy.ToolResponse
}

func (f *fakeTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: f.name}
}

func (f *fakeTool) Run(ctx context.Context, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
	f.called = true
	f.gotCtx = ctx
	return f.resp, nil
}

func (f *fakeTool) ProviderOptions() fantasy.ProviderOptions     { return nil }
func (f *fakeTool) SetProviderOptions(_ fantasy.ProviderOptions) {}

// newRunner builds a hooks.Runner from a single HookConfig, running the
// config-loader path that compiles the matcher regex.
func newRunner(t *testing.T, cmd string) *hooks.Runner {
	t.Helper()
	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			hooks.EventPreToolUse: {{Command: cmd}},
		},
	}
	require.NoError(t, cfg.ValidateHooks())
	return hooks.NewRunner(cfg.Hooks[hooks.EventPreToolUse], t.TempDir(), t.TempDir())
}

// fakeMCPTool is a fakeTool that says it is an MCP server's tool.
type fakeMCPTool struct {
	*fakeTool
	server, tool string
}

func (f fakeMCPTool) MCP() string         { return f.server }
func (f fakeMCPTool) MCPToolName() string { return f.tool }

// An MCP tool's hooks see its Claude Code name, mcp__<server>__<tool>,
// so a matcher written for Claude Code blocks it here too.
func TestHookedTool_MatchesAnMCPToolByItsClaudeCodeName(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Hooks: map[string][]config.HookConfig{
		hooks.EventPreToolUse: {{Matcher: "mcp__github__.*", Command: `echo "no github" >&2; exit 2`}},
	}}
	require.NoError(t, cfg.ValidateHooks())
	runner := hooks.NewRunner(cfg.Hooks[hooks.EventPreToolUse], t.TempDir(), t.TempDir())
	inner := fakeMCPTool{fakeTool: &fakeTool{name: "mcp_github_create_issue"}, server: "github", tool: "create_issue"}
	tool := newHookedTool(inner, toolHooks{pre: runner})

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-1", Name: "mcp_github_create_issue", Input: "{}"})
	require.NoError(t, err)
	require.False(t, inner.called, "the hook blocked the call")
	require.Contains(t, resp.Content, "no github")

	// Under another of Klash's wrappers too.
	inner = fakeMCPTool{fakeTool: &fakeTool{name: "mcp_github_create_issue"}, server: "github", tool: "create_issue"}
	tool = newHookedTool(describedTool{AgentTool: inner, description: "the user's"}, toolHooks{pre: runner})
	resp, err = tool.Run(t.Context(), fantasy.ToolCall{ID: "call-2", Name: "mcp_github_create_issue", Input: "{}"})
	require.NoError(t, err)
	require.False(t, inner.called)
	require.Contains(t, resp.Content, "no github")
}

func TestHookedTool_AllowStampsHookApproval(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "view", resp: fantasy.NewTextResponse("ok")}
	runner := newRunner(t, `echo '{"decision":"allow"}'`)
	tool := newHookedTool(inner, toolHooks{pre: runner})

	_, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-1", Name: "view"})
	require.NoError(t, err)
	require.True(t, inner.called, "inner tool should have run")

	// The inner tool's permission service can now treat call-1 as pre-approved.
	svc := permission.NewPermissionService(t.TempDir(), false, nil)
	granted, err := svc.Request(inner.gotCtx, permission.CreatePermissionRequest{
		SessionID:  "s1",
		ToolCallID: "call-1",
		ToolName:   "view",
		Action:     "read",
		Path:       t.TempDir(),
	})
	require.NoError(t, err)
	require.True(t, granted, "hook allow should bypass the permission prompt")
}

func TestHookedTool_SilentDoesNotStampApproval(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "view", resp: fantasy.NewTextResponse("ok")}
	runner := newRunner(t, `exit 0`) // no stdout, no decision
	tool := newHookedTool(inner, toolHooks{pre: runner})

	_, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-2", Name: "view"})
	require.NoError(t, err)
	require.True(t, inner.called)

	// With no hook opinion, a fresh permission request has nothing stamped
	// and must fall through to the normal flow. We verify by checking that
	// the context does not look pre-approved for this call ID: sending a
	// request that no subscriber resolves will block until cancelled.
	svc := permission.NewPermissionService(t.TempDir(), false, nil)
	ctx, cancel := context.WithCancel(inner.gotCtx)
	cancel()
	granted, err := svc.Request(ctx, permission.CreatePermissionRequest{
		SessionID:  "s1",
		ToolCallID: "call-2",
		ToolName:   "view",
		Action:     "read",
		Path:       t.TempDir(),
	})
	require.Error(t, err, "no approval stamped => request should reach the prompt path")
	require.False(t, granted)
}

func TestHookedTool_DenySkipsInnerTool(t *testing.T) {
	t.Parallel()

	inner := &fakeTool{name: "bash"}
	runner := newRunner(t, `echo "blocked" >&2; exit 2`)
	tool := newHookedTool(inner, toolHooks{pre: runner})

	resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-3", Name: "bash"})
	require.NoError(t, err)
	require.False(t, inner.called, "denied call must not reach the inner tool")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "blocked")
}

// A PreToolUse hook that could not check, here because the command it
// runs is missing, blocks the call: Klash fails closed where Claude Code
// would run the tool unchecked. The model reads that the hook could not
// run, not that the call was wrong.
func TestHookedTool_CouldNotEvaluateBlocksTheCall(t *testing.T) {
	t.Parallel()

	for cmd, want := range map[string]string{
		`echo "the secret scanner is not installed" >&2; exit 3`: "could not check this: the secret scanner is not installed",
		`no_such_secret_scanner --scan`:                          "could not run (exit 127 (command not found)",
	} {
		inner := &fakeTool{name: "bash"}
		tool := newHookedTool(inner, toolHooks{pre: newRunner(t, cmd)})

		resp, err := tool.Run(t.Context(), fantasy.ToolCall{ID: "call-4", Name: "bash"})
		require.NoError(t, err)
		require.False(t, inner.called, "%s: a check that could not run must not let the call through", cmd)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, want)
		require.Contains(t, resp.Content, "A check that could not run does not pass.")
	}
}

func TestWrapToolsWithHooks(t *testing.T) {
	t.Parallel()

	runner := newRunner(t, `exit 0`)
	inputs := []fantasy.AgentTool{&fakeTool{name: "a"}, &fakeTool{name: "b"}}

	t.Run("every tool is wrapped", func(t *testing.T) {
		t.Parallel()
		out := wrapToolsWithHooks(inputs, toolHooks{pre: runner})
		require.Len(t, out, len(inputs))
		for i, tool := range out {
			_, ok := tool.(*hookedTool)
			require.Truef(t, ok, "tool %d should be a *hookedTool", i)
		}
	})

	t.Run("no runners skip the wrap", func(t *testing.T) {
		t.Parallel()
		require.Equal(t, inputs, wrapToolsWithHooks(inputs, toolHooks{}))
	})
}

// A PreToolUse hook that answers "ask" puts the call to the user, as in
// Claude Code, even where it would run unasked (yolo here, or a session
// that grants its own requests, as agentic_fetch's sub-agent's does), and
// the tool runs only once they allow it, then needing no other approval.
// Where no one can answer, as in klash run, the call is refused with the
// reason, and so is a call with no ID to ask about.
func TestHookedTool_AskPutsTheCallToTheUser(t *testing.T) {
	t.Parallel()

	const ask = `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"deploys need a second look"}}'`
	call := fantasy.ToolCall{ID: "call-1", Name: "bash", Input: `{"command":"deploy"}`}
	run := func(t *testing.T, perms permission.Service, answer func(permission.PermissionRequest) bool) (*fakeTool, fantasy.ToolResponse, permission.PermissionRequest) {
		t.Helper()
		inner := &fakeTool{name: "bash", resp: fantasy.NewTextResponse("ran")}
		tool := newHookedTool(inner, toolHooks{pre: newRunner(t, ask), permissions: perms, interactive: answer != nil})
		asked := make(chan permission.PermissionRequest, 1)
		if answer != nil {
			events := perms.Subscribe(t.Context())
			go func() {
				event := <-events
				asked <- event.Payload
				answer(event.Payload)
			}()
		}
		resp, err := tool.Run(sessionCtx(t), call)
		require.NoError(t, err)
		var req permission.PermissionRequest
		if answer != nil {
			select {
			case req = <-asked:
			case <-time.After(5 * time.Second):
				t.Fatal("the call was not put to the user")
			}
		}
		return inner, resp, req
	}

	t.Run("allowed", func(t *testing.T) {
		t.Parallel()
		perms := permission.NewPermissionService(t.TempDir(), true, nil)
		inner, resp, req := run(t, perms, perms.Grant)
		require.True(t, inner.called)
		require.Equal(t, "ran", resp.Content)
		require.Equal(t, permission.HookAskAction, req.Action)
		require.Equal(t, "bash", req.ToolName)
		require.Contains(t, req.Description, "deploys need a second look")
		require.Equal(t, call.Input, req.Params)

		// The tool's own request needs no other approval.
		perms.SetSkipRequests(false)
		ctx, cancel := context.WithTimeout(inner.gotCtx, 5*time.Second)
		defer cancel()
		granted, err := perms.Request(ctx, permission.CreatePermissionRequest{SessionID: "s1", ToolCallID: "call-1", ToolName: "bash", Action: "execute", Path: t.TempDir()})
		require.NoError(t, err)
		require.True(t, granted)
	})

	t.Run("denied", func(t *testing.T) {
		t.Parallel()
		perms := permission.NewPermissionService(t.TempDir(), true, nil)
		inner, resp, _ := run(t, perms, perms.Deny)
		require.False(t, inner.called)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "User denied permission")
	})

	t.Run("a session that grants its own requests", func(t *testing.T) {
		t.Parallel()
		perms := permission.NewPermissionService(t.TempDir(), false, nil)
		perms.AutoApproveSession("s1")
		inner, resp, req := run(t, perms, perms.Grant)
		require.True(t, inner.called, "agentic_fetch's sub-agent in the TUI is asked too")
		require.Equal(t, "ran", resp.Content)
		require.Equal(t, permission.HookAskAction, req.Action)
	})

	t.Run("no one to answer", func(t *testing.T) {
		t.Parallel()
		perms := permission.NewPermissionService(t.TempDir(), true, nil)
		perms.AutoApproveSession("s1")
		inner, resp, _ := run(t, perms, nil)
		require.False(t, inner.called)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "no one is here to answer")
		require.Contains(t, resp.Content, "deploys need a second look")
	})

	t.Run("no ID to ask about", func(t *testing.T) {
		t.Parallel()
		perms := permission.NewPermissionService(t.TempDir(), true, nil)
		inner := &fakeTool{name: "bash", resp: fantasy.NewTextResponse("ran")}
		tool := newHookedTool(inner, toolHooks{pre: newRunner(t, ask), permissions: perms, interactive: true})
		resp, err := tool.Run(sessionCtx(t), fantasy.ToolCall{Name: "bash", Input: call.Input})
		require.NoError(t, err)
		require.False(t, inner.called)
		require.Contains(t, resp.Content, "no ID to ask about")
	})
}
