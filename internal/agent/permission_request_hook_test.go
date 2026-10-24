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
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// permissionTool asks for permission to run, as Klash's tools do, and runs
// once it has it.
type permissionTool struct {
	perms permission.Service
	ran   bool
}

func (p *permissionTool) Info() fantasy.ToolInfo                       { return fantasy.ToolInfo{Name: "bash"} }
func (p *permissionTool) ProviderOptions() fantasy.ProviderOptions     { return nil }
func (p *permissionTool) SetProviderOptions(_ fantasy.ProviderOptions) {}

func (p *permissionTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	granted, err := p.perms.Request(ctx, permission.CreatePermissionRequest{
		SessionID:   tools.GetSessionFromContext(ctx),
		ToolCallID:  call.ID,
		ToolName:    "bash",
		Action:      "execute",
		Description: "Execute command: deploy",
		Params:      call.Input,
		Path:        os.TempDir(),
	})
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if !granted {
		return tools.NewPermissionDeniedResponse(), nil
	}
	p.ran = true
	return fantasy.NewTextResponse("ran"), nil
}

// permissionRequestRunner runs cmd as a PermissionRequest hook with the
// given matcher, through the config loader.
func permissionRequestRunner(t *testing.T, matcher, cmd string) *hooks.Runner {
	t.Helper()
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{
		hooks.EventPermissionRequest: {{Matcher: matcher, Command: cmd}},
	}}
	require.NoError(t, cfg.ValidateHooks())
	return hooks.NewRunner(cfg.Hooks[hooks.EventPermissionRequest], t.TempDir(), t.TempDir())
}

// permissionRequestCall runs a bash call that asks for permission, with
// cmd as its PermissionRequest hook. answer, when not nil, answers the
// dialog if one shows, and the dialog is returned; when nil, a dialog that
// shows fails the test.
func permissionRequestCall(t *testing.T, perms permission.Service, toolHooks toolHooks, answer func(permission.PermissionRequest) bool) (*permissionTool, fantasy.ToolResponse, *permission.PermissionRequest) {
	t.Helper()
	inner := &permissionTool{perms: perms}
	toolHooks.permissions = perms
	tool := newHookedTool(inner, toolHooks)
	events := perms.Subscribe(t.Context())
	shown := make(chan permission.PermissionRequest, 1)
	go func() {
		select {
		case event := <-events:
			shown <- event.Payload
			if answer != nil {
				answer(event.Payload)
			}
		case <-t.Context().Done():
		}
	}()
	ctx, cancel := context.WithTimeout(sessionCtx(t), 10*time.Second)
	defer cancel()
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: "bash", Input: `{"command":"deploy"}`})
	require.NoError(t, err, "a dialog no one answered would time out here")
	select {
	case req := <-shown:
		require.NotNil(t, answer, "a dialog showed: %s", req.Description)
		return inner, resp, &req
	default:
		return inner, resp, nil
	}
}

func claudeCodeDecision(decision string) string {
	return `echo '{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":` + decision + `}}'`
}

// A PermissionRequest hook answers the dialog in the user's place, as in
// Claude Code: an allow runs the call and a deny refuses it with the
// hook's message, both without a dialog; a deny that interrupts ends the
// turn, where a plain one lets the model go on. Klash's own answer works
// too.
func TestPermissionRequestHookAnswersTheDialog(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		cmd      string
		ran      bool
		content  string
		stopTurn bool
	}{
		"allow":            {cmd: claudeCodeDecision(`{"behavior":"allow"}`), ran: true, content: "ran"},
		"Klash's allow":    {cmd: `echo '{"decision":"allow"}'`, ran: true, content: "ran"},
		"deny":             {cmd: claudeCodeDecision(`{"behavior":"deny","message":"no deploys on Friday"}`), content: "Tool call denied by a PermissionRequest hook. Reason: no deploys on Friday"},
		"deny, interrupt":  {cmd: claudeCodeDecision(`{"behavior":"deny","message":"stop","interrupt":true}`), content: "Reason: stop", stopTurn: true},
		"halt (exit 49)":   {cmd: `echo "out of budget" >&2; exit 49`, content: "Reason: out of budget", stopTurn: true},
		"deny, no message": {cmd: claudeCodeDecision(`{"behavior":"deny"}`), content: "Reason: none given"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			perms := permission.NewPermissionService(t.TempDir(), false, nil)
			inner, resp, dialog := permissionRequestCall(t, perms, toolHooks{permissionRequest: permissionRequestRunner(t, "", tc.cmd), interactive: true}, nil)
			require.Nil(t, dialog)
			require.Equal(t, tc.ran, inner.ran)
			require.Contains(t, resp.Content, tc.content)
			require.Equal(t, !tc.ran, resp.IsError)
			require.Equal(t, tc.stopTurn, resp.StopTurn)
			require.Contains(t, resp.Metadata, `"event":"PermissionRequest"`, "the tool line shows the hook")
		})
	}
}

// With no answer from its hooks the user is asked, as before: a hook that
// is silent, exits 2 (Claude Code's "leave the flow as it was"), or asks.
// A hook that could not check leaves the user to answer too, and the
// dialog says why; so does an allow with changed input, which Klash
// cannot run once the tool has read its input.
func TestPermissionRequestHookWithoutAnAnswerAsksTheUser(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		cmd  string
		note string
	}{
		"silent":          {cmd: `exit 0`},
		"exit 2":          {cmd: `echo "not mine to say" >&2; exit 2`},
		"ask":             {cmd: `echo '{"decision":"ask"}'`},
		"could not check": {cmd: `echo "the pager is down" >&2; exit 3`, note: "could not answer this, so you are asked: hook"},
		"not installed":   {cmd: `no_such_approver --check`, note: "could not answer this"},
		"unreadable":      {cmd: claudeCodeDecision(`{"behavior":"maybe"}`), note: "could not answer this"},
		"changed input":   {cmd: claudeCodeDecision(`{"behavior":"allow","updatedInput":{"command":"deploy --dry-run"}}`), note: "with changed input, which Klash cannot run"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			perms := permission.NewPermissionService(t.TempDir(), false, nil)
			inner, resp, dialog := permissionRequestCall(t, perms, toolHooks{permissionRequest: permissionRequestRunner(t, "", tc.cmd), interactive: true}, perms.Grant)
			require.NotNil(t, dialog, "the user was asked")
			require.True(t, inner.ran, "and allowed it")
			require.Equal(t, "ran", resp.Content)
			require.Contains(t, dialog.Description, "Execute command: deploy")
			if tc.note != "" {
				require.Contains(t, dialog.Description, tc.note)
			} else {
				require.Equal(t, "Execute command: deploy", dialog.Description)
			}
		})
	}

	t.Run("the user denies", func(t *testing.T) {
		t.Parallel()
		perms := permission.NewPermissionService(t.TempDir(), false, nil)
		inner, resp, dialog := permissionRequestCall(t, perms, toolHooks{permissionRequest: permissionRequestRunner(t, "", `exit 0`), interactive: true}, perms.Deny)
		require.NotNil(t, dialog)
		require.False(t, inner.ran)
		require.Equal(t, "User denied permission", resp.Content)
		require.True(t, resp.StopTurn)
	})
}

// The hooks run only when a dialog is due, as in Claude Code: not for a
// call that needs no permission (yolo here), and not for a call another
// tool's matcher names. They get the call as PreToolUse hooks do, and
// match Claude Code's tool names.
func TestPermissionRequestHookRunsWhenADialogIsDue(t *testing.T) {
	t.Parallel()

	t.Run("its input", func(t *testing.T) {
		t.Parallel()
		payload := filepath.Join(t.TempDir(), "payload.json")
		perms := permission.NewPermissionService(t.TempDir(), false, nil)
		inner, _, dialog := permissionRequestCall(t, perms, toolHooks{permissionRequest: permissionRequestRunner(t, "Bash", `cat > `+payload), interactive: true}, perms.Grant)
		require.NotNil(t, dialog)
		require.True(t, inner.ran)
		data, err := os.ReadFile(payload)
		require.NoError(t, err)
		var p map[string]any
		require.NoError(t, json.Unmarshal(data, &p))
		require.Equal(t, "PermissionRequest", p["hook_event_name"])
		require.Equal(t, "s1", p["session_id"])
		require.Equal(t, "bash", p["tool_name"])
		require.Equal(t, map[string]any{"command": "deploy"}, p["tool_input"])
	})

	t.Run("no dialog due", func(t *testing.T) {
		t.Parallel()
		marker := filepath.Join(t.TempDir(), "ran")
		perms := permission.NewPermissionService(t.TempDir(), true, nil)
		inner, resp, dialog := permissionRequestCall(t, perms, toolHooks{permissionRequest: permissionRequestRunner(t, "", `touch `+marker+`; echo '{"decision":"deny"}'`), interactive: true}, nil)
		require.Nil(t, dialog)
		require.True(t, inner.ran)
		require.Equal(t, "ran", resp.Content)
		require.NoFileExists(t, marker, "the hook did not run")
	})

	t.Run("another tool's matcher", func(t *testing.T) {
		t.Parallel()
		perms := permission.NewPermissionService(t.TempDir(), false, nil)
		inner, _, dialog := permissionRequestCall(t, perms, toolHooks{permissionRequest: permissionRequestRunner(t, "Write", claudeCodeDecision(`{"behavior":"deny"}`)), interactive: true}, perms.Grant)
		require.NotNil(t, dialog, "a hook for Write has no say about bash")
		require.True(t, inner.ran)
	})
}

// Where no one can answer a PreToolUse hook's "ask", as in klash run, a
// PermissionRequest hook still can, as in Claude Code, which denies the
// call when none allows it.
func TestPermissionRequestHookAnswersWhereNoOneCan(t *testing.T) {
	t.Parallel()

	const ask = `echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"deploys need a second look"}}'`
	for name, tc := range map[string]struct {
		cmd string
		ran bool
	}{
		"allowed":   {cmd: claudeCodeDecision(`{"behavior":"allow"}`), ran: true},
		"no answer": {cmd: `exit 0`},
		"denied":    {cmd: claudeCodeDecision(`{"behavior":"deny","message":"no"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			perms := permission.NewPermissionService(t.TempDir(), false, nil)
			perms.AutoApproveSession("s1")
			inner, resp, dialog := permissionRequestCall(t, perms, toolHooks{pre: newRunner(t, ask), permissionRequest: permissionRequestRunner(t, "", tc.cmd)}, nil)
			require.Nil(t, dialog)
			require.Equal(t, tc.ran, inner.ran)
			if !tc.ran {
				require.Contains(t, resp.Content, "no one is here to answer")
			}
		})
	}
}

// PermissionRequest hooks alone are enough to wrap the tools, and the
// coordinator builds their runner from the config.
func TestPermissionRequestHooksWrapTheTools(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	coord := newTestCoordinator(t, env, "p", config.ProviderConfig{})
	coord.cfg.Config().Hooks = map[string][]config.HookConfig{hooks.EventPermissionRequest: {{Command: "true"}}}
	require.NoError(t, coord.cfg.Config().ValidateHooks())
	th := coord.buildToolHooks()
	require.NotNil(t, th.permissionRequest)
	out := wrapToolsWithHooks([]fantasy.AgentTool{&fakeTool{name: "bash"}}, th)
	_, ok := out[0].(*hookedTool)
	require.True(t, ok, "the tool is wrapped")
}

// Under dontAsk a call that would be asked about does not run, and the
// model reads why and goes on, where a user's deny would end the turn; a
// PermissionRequest hook's allow still lets it run.
func TestDontAskTellsTheModelWhy(t *testing.T) {
	t.Parallel()

	perms := permission.New(permission.Options{WorkingDir: t.TempDir(), DontAsk: true})
	inner, resp, dialog := permissionRequestCall(t, perms, toolHooks{dontAsk: true, interactive: true}, nil)
	require.Nil(t, dialog)
	require.False(t, inner.ran)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "--permission-mode dontAsk")
	require.False(t, resp.StopTurn, "the turn goes on")

	inner, resp, dialog = permissionRequestCall(t, perms, toolHooks{dontAsk: true, interactive: true, permissionRequest: permissionRequestRunner(t, "", claudeCodeDecision(`{"behavior":"allow"}`))}, nil)
	require.Nil(t, dialog)
	require.True(t, inner.ran)
	require.Equal(t, "ran", resp.Content)

	const ask = `echo '{"decision":"ask"}'`
	inner, resp, _ = permissionRequestCall(t, perms, toolHooks{dontAsk: true, interactive: true, pre: newRunner(t, ask)}, nil)
	require.False(t, inner.ran, "a PreToolUse hook's question is denied too")
	require.Contains(t, resp.Content, "--permission-mode dontAsk")
	require.False(t, resp.StopTurn)

	require.IsType(t, &hookedTool{}, wrapToolsWithHooks([]fantasy.AgentTool{&fakeTool{name: "bash"}}, toolHooks{dontAsk: true})[0], "dontAsk alone wraps the tools")
}
