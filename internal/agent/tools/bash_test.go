package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

type mockBashPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
}

func (m *mockBashPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	return true, nil
}

func (m *mockBashPermissionService) Grant(req permission.PermissionRequest) bool { return true }

func (m *mockBashPermissionService) Deny(req permission.PermissionRequest) bool { return true }

func (m *mockBashPermissionService) GrantPersistent(req permission.PermissionRequest) bool {
	return true
}

func (m *mockBashPermissionService) AutoApproveSession(sessionID string) {}

func (m *mockBashPermissionService) IsSessionAutoApproved(string) bool { return false }

func (m *mockBashPermissionService) ActiveRequest() (permission.PermissionRequest, bool) {
	return permission.PermissionRequest{}, false
}

func (m *mockBashPermissionService) SetSkipRequests(skip bool) {}

func (m *mockBashPermissionService) SkipRequests() bool {
	return false
}

func (m *mockBashPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func TestBashTool_DefaultAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "default threshold",
		Command:     "echo done",
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Background)
	require.Empty(t, meta.ShellID)
	require.Contains(t, meta.Output, "done")
}

func TestBashTool_CustomAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description:         "custom threshold",
		Command:             "sleep 1.5 && echo done",
		AutoBackgroundAfter: 1,
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Background)
	require.NotEmpty(t, meta.ShellID)
	require.Contains(t, resp.Content, "moved to background")
	require.Contains(t, resp.Content, "Call the job_output tool with this ID to read its output, or the job_kill tool to stop it.")

	bgManager := shell.GetBackgroundShellManager()
	require.NoError(t, bgManager.Kill(meta.ShellID))
}

// run_in_background starts the command as a background job at once, and
// the result names the tools that read and stop it as tools.
func TestBashTool_RunInBackground(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description:     "serve",
		Command:         "sleep 30",
		RunInBackground: true,
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Background)
	require.Equal(t, "Background shell started with ID: "+meta.ShellID+"\n\nCall the job_output tool with this ID to read its output, or the job_kill tool to stop it.", resp.Content)
	require.NoError(t, shell.GetBackgroundShellManager().Kill(meta.ShellID))
}

// An agent on the AI box ran "job_kill 077" in bash to stop a background
// job, meaning the job_kill tool, and left the job running; the result
// now says to call the tool.
func TestBashTool_ToolNameAsCommand(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "stop the server",
		Command:     "job_kill 077",
	})

	require.Contains(t, resp.Content, "job_kill is a Klash tool, not a shell command: call the job_kill tool instead.")
}

type recordingPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
	requestCount int
	allow        bool
}

func (m *recordingPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	m.requestCount++
	return m.allow, nil
}

func (m *recordingPermissionService) Grant(req permission.PermissionRequest) bool { return true }

func (m *recordingPermissionService) Deny(req permission.PermissionRequest) bool { return true }

func (m *recordingPermissionService) GrantPersistent(req permission.PermissionRequest) bool {
	return true
}

func (m *recordingPermissionService) AutoApproveSession(sessionID string) {}

func (m *recordingPermissionService) IsSessionAutoApproved(string) bool { return false }

func (m *recordingPermissionService) ActiveRequest() (permission.PermissionRequest, bool) {
	return permission.PermissionRequest{}, false
}

func (m *recordingPermissionService) SetSkipRequests(skip bool) {}

func (m *recordingPermissionService) SkipRequests() bool {
	return false
}

func (m *recordingPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func newBashToolForTest(workingDir string) fantasy.AgentTool {
	permissions := &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	return NewBashTool(permissions, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs)
}

func newBashToolWithRecordingPerms(workingDir string, allow bool) (fantasy.AgentTool, *recordingPermissionService) {
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  allow,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	return NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs), perms
}

func TestBashTool_ChainedCommandsRequirePermission(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, true)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	// ls && echo should trigger permission check.
	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "chained ls",
		Command:     "ls && echo done",
	})

	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount, "chained command should trigger permission request")

	// Plain ls should NOT trigger permission check.
	perms.requestCount = 0
	resp = runBashTool(t, tool, ctx, BashParams{
		Description: "plain ls",
		Command:     "ls -la",
	})

	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount, "plain ls should not trigger permission request")
}

func TestBashTool_ChainedCommandsDenied(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, false)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "chained ls denied",
		Command:     "ls && rm -rf /",
	})

	require.Equal(t, 1, perms.requestCount)
	require.Contains(t, resp.Content, "User denied permission")
}

func runBashTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params BashParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
		Input: string(input),
	}

	resp, err := tool.Run(ctx, call)
	require.NoError(t, err)
	return resp
}

func TestTruncateOutputValidUTF8(t *testing.T) {
	t.Parallel()
	// CJK characters are 2 cells wide; this string is far wider than
	// MaxOutputLength so TruncateOutput must truncate it.
	content := strings.Repeat("你好世界", MaxOutputLength)

	out := TruncateOutput(content, t.TempDir())
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

func TestTruncateOutputShortContent(t *testing.T) {
	t.Parallel()
	content := "short output"
	require.Equal(t, content, TruncateOutput(content, t.TempDir()))
}

func TestTruncateOutputEmoji(t *testing.T) {
	t.Parallel()
	// Emoji with ZWJ sequences should not be split.
	content := strings.Repeat("👨‍👩‍👧‍👦", MaxOutputLength)

	out := TruncateOutput(content, t.TempDir())
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

// A command's exit status is in the metadata, not only in the output text,
// so the --verbose trace can mark a command that failed.
func TestBashTool_RecordsTheExitCode(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	for command, want := range map[string]int{"exit 3": 3, "echo fine": 0} {
		resp := runBashTool(t, tool, ctx, BashParams{Description: "exit status", Command: command})
		require.False(t, resp.IsError, "a command that exits non-zero is a result, not a tool error")
		var meta BashResponseMetadata
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
		require.Equal(t, want, meta.ExitCode, command)
	}
}

// TestBashTool_RefusesGnokeyPasswordOnStdin keeps a gno.land key's password
// off the shell. gnokey reads a password from standard input only with
// -insecure-password-stdin; that flag belongs to the gnokey tool's masked
// prompt, so bash refuses it in either spelling while the rest of gnokey
// stays available.
func TestBashTool_RefusesGnokeyPasswordOnStdin(t *testing.T) {
	t.Parallel()

	blockedParts := func(parts []string) bool {
		for _, block := range blockFuncs() {
			if block(parts) {
				return true
			}
		}
		return false
	}

	for _, parts := range [][]string{
		{"gnokey", "sign", "-insecure-password-stdin", "-tx-path", "tx.json", "alice"},
		{"gnokey", "maketx", "call", "--insecure-password-stdin=true", "-pkgpath", "gno.land/r/x", "-func", "F", "alice"},
	} {
		require.True(t, blockedParts(parts), "must refuse: %v", parts)
	}

	for _, parts := range [][]string{
		{"gnokey", "query", "vm/qrender", "-data", "gno.land/r/x:"},
		{"gnokey", "maketx", "call", "-pkgpath", "gno.land/r/x", "-func", "F", "-broadcast=false", "alice"},
		{"gnokey", "list"},
	} {
		require.False(t, blockedParts(parts), "must stay allowed: %v", parts)
	}
}

// TestBashTool_GnokeyPasswordFlagIsRefusedByTheRealShell proves the block
// holds where it matters: the embedded shell the bash tool runs, including
// behind a pipe that would otherwise feed the password in.
func TestBashTool_GnokeyPasswordFlagIsRefusedByTheRealShell(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	for _, command := range []string{
		"gnokey sign -insecure-password-stdin -tx-path tx.json alice",
		"echo secret | gnokey sign -insecure-password-stdin -tx-path tx.json alice",
	} {
		resp := runBashTool(t, tool, ctx, BashParams{Description: "sign", Command: command})
		require.Contains(t, resp.Content, "not allowed for security reasons", "command: %s", command)
	}
}

// Claude Code's timeout, in milliseconds, is how long a command runs in
// the foreground before it moves to the background, rounded up to whole
// seconds and at most ten minutes.
func TestBashTool_TimeoutIsClaudeCodes(t *testing.T) {
	require.Equal(t, 0, timeoutSeconds(0))
	require.Equal(t, 0, timeoutSeconds(-5))
	require.Equal(t, 1, timeoutSeconds(1))
	require.Equal(t, 1, timeoutSeconds(1000))
	require.Equal(t, 2, timeoutSeconds(1001))
	require.Equal(t, 600, timeoutSeconds(600000))
	require.Equal(t, 600, timeoutSeconds(900000))

	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	background := func(params BashParams) bool {
		t.Helper()
		resp := runBashTool(t, tool, ctx, params)
		require.False(t, resp.IsError, resp.Content)
		var meta BashResponseMetadata
		require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
		if meta.Background {
			require.NoError(t, shell.GetBackgroundShellManager().Kill(meta.ShellID))
		}
		return meta.Background
	}

	require.True(t, background(BashParams{Description: "short timeout", Command: "sleep 3 && echo done", Timeout: 1000}),
		"a one second timeout moves a three second command to the background")
	require.True(t, background(BashParams{Description: "both", Command: "sleep 1.5 && echo done", AutoBackgroundAfter: 1, Timeout: 600000}),
		"auto_background_after wins")
}
