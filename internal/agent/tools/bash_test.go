package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	return NewBashTool(permissions, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, nil, nil)
}

func newBashToolWithRecordingPerms(workingDir string, allow bool) (fantasy.AgentTool, *recordingPermissionService) {
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  allow,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	return NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, nil, nil), perms
}

func TestBashTool_ChainedCommandsRequirePermission(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, true)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	// A chain with a part that writes asks.
	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "chained ls",
		Command:     "ls && touch made.txt",
	})

	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount, "chained command should trigger permission request")

	// A chain whose every part only reads does not, as in Claude Code.
	perms.requestCount = 0
	resp = runBashTool(t, tool, ctx, BashParams{
		Description: "chained reads",
		Command:     "ls && echo done",
	})
	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount, "a chain of reading commands should not trigger a permission request")

	// One that only looks like it reads asks: a second line runs too.
	perms.requestCount = 0
	resp = runBashTool(t, tool, ctx, BashParams{
		Description: "two lines",
		Command:     "echo hi\ntouch second.txt",
	})
	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount, "a command on a second line should trigger a permission request")

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
		for _, block := range blockFuncs(nil) {
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

// A bash command runs its session's env file first, where the
// SessionStart hooks write export lines (Claude Code's CLAUDE_ENV_FILE),
// in the foreground and in the background, whatever its path holds; a
// command with no env file, or an empty one, runs as it is. A background
// job shows its command alone.
func TestBashTool_RunsTheSessionEnvFile(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	envFile := filepath.Join(t.TempDir(), "it's; a $file.sh")
	require.NoError(t, os.WriteFile(envFile, []byte("export KLASH_TEST_GREETING=hello\n"), 0o600))
	const command = `echo "greeting=[$KLASH_TEST_GREETING]"`
	session := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	withEnv := WithSessionEnvFile(session, envFile)

	run := func(ctx context.Context) string {
		t.Helper()
		resp := runBashTool(t, tool, ctx, BashParams{Description: "greet", Command: command})
		require.False(t, resp.IsError, resp.Content)
		return resp.Content
	}
	require.Contains(t, run(withEnv), "greeting=[hello]")
	require.Contains(t, run(session), "greeting=[]")

	// Long enough to still be running when bash stops waiting for it.
	const slow = "sleep 1.5; " + command
	resp := runBashTool(t, tool, withEnv, BashParams{Description: "greet", Command: slow, RunInBackground: true})
	require.False(t, resp.IsError, resp.Content)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	job, ok := shell.GetBackgroundShellManager().Get(meta.ShellID)
	require.True(t, ok)
	job.Wait()
	stdout, _, _, _ := job.GetOutput()
	require.Contains(t, stdout, "greeting=[hello]")
	require.Equal(t, slow, job.Command, "the job is the command alone")

	require.NoError(t, os.WriteFile(envFile, nil, 0o600))
	require.Contains(t, run(withEnv), "greeting=[]", "an empty file is not run")
}

// A context that names an env file keeps it: a helper agent's turn,
// started from its parent's, runs its parent session's file.
func TestWithSessionEnvFileKeepsTheParents(t *testing.T) {
	t.Parallel()

	parent := WithSessionEnvFile(context.Background(), "/data/session-env/parent.sh")
	require.Equal(t, "/data/session-env/parent.sh", SessionEnvFileFromContext(WithSessionEnvFile(parent, "/data/session-env/child.sh")))
	require.Empty(t, SessionEnvFileFromContext(context.Background()))
}

// A Bash deny rule given to the bash tool stops the commands it matches,
// even after another command on the line, and the tool's description
// names the rule for the model; other commands run.
func TestBashTool_DenyRules(t *testing.T) {
	workingDir := t.TempDir()
	perms := &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, []string{"touch *"}, nil)
	require.Contains(t, tool.Info().Description, "deny, wherever they appear in a command line: touch *")
	require.NotContains(t, newBashToolForTest(workingDir).Info().Description, "permission rules deny")

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	resp := runBashTool(t, tool, ctx, BashParams{Description: "touch", Command: "echo start && touch denied.txt"})
	require.Contains(t, resp.Content, `not allowed for security reasons: "touch denied.txt"`)
	require.NoFileExists(t, filepath.Join(workingDir, "denied.txt"))

	resp = runBashTool(t, tool, ctx, BashParams{Description: "echo", Command: "echo fine"})
	require.Contains(t, resp.Content, "fine")
}

// A Bash allow rule given to the bash tool lets the commands it matches
// run without asking, as Claude Code's permissions.allow does, but only
// when every command on the line is allowed or only reads.
func TestBashTool_AllowRules(t *testing.T) {
	workingDir := t.TempDir()
	perms := &recordingPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest](), allow: true}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", config.DefaultMaxBackgroundJobs, nil, []string{"touch *"})
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	require.NoError(t, os.Mkdir(filepath.Join(workingDir, "sub"), 0o755))
	require.NoError(t, exec.Command("git", "init", "-q", workingDir).Run(), "git status needs a repository")

	for command, asks := range map[string]int{
		"touch a.txt":                 0,
		"ls && touch b.txt":           0,
		"touch c.txt && rm c.txt":     1,
		"mkdir d":                     1,
		"touch e.txt > f.txt":         1,
		"timeout 5 touch g.txt":       0,
		"echo $(touch h.txt)":         1,
		"touch i.txt &":               1,
		"X=1 touch j.txt":             1,
		"touch k.txt; echo done | wc": 0,
		"cd sub && touch l.txt":       0,
		"cd sub && git status":        1,
	} {
		perms.requestCount = 0
		resp := runBashTool(t, tool, ctx, BashParams{Description: "touch", Command: command})
		require.False(t, resp.IsError, command)
		require.Equal(t, asks, perms.requestCount, command)
	}
	require.FileExists(t, filepath.Join(workingDir, "a.txt"))

	// A cd is read from where the command starts.
	perms.requestCount = 0
	resp := runBashTool(t, tool, ctx, BashParams{Description: "touch", Command: "cd .. && touch m.txt", WorkingDir: filepath.Join(workingDir, "sub")})
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, 0, perms.requestCount, "from sub, cd .. is the project")
	require.FileExists(t, filepath.Join(workingDir, "m.txt"))
}
