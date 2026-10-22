package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/question"
	"github.com/stretchr/testify/require"
)

// fakeGnokey puts a gnokey on PATH that records its arguments and its
// standard input, echoes both, and exits with GNOKEY_FAKE_EXIT. It returns
// where the two records land.
func fakeGnokey(t *testing.T) (argsFile, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	stdinFile = filepath.Join(dir, "stdin")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"$GNOKEY_FAKE_ARGS\"\n" +
		"cat > \"$GNOKEY_FAKE_STDIN\"\n" +
		"echo \"args: $*\"\n" +
		"echo \"stdin: $(cat \"$GNOKEY_FAKE_STDIN\")\"\n" +
		"exit \"${GNOKEY_FAKE_EXIT:-0}\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gnokey"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GNOKEY_FAKE_ARGS", argsFile)
	t.Setenv("GNOKEY_FAKE_STDIN", stdinFile)
	t.Setenv("GNOKEY_FAKE_EXIT", "0")
	return argsFile, stdinFile
}

func gnokeyContext() context.Context {
	return context.WithValue(context.Background(), SessionIDContextKey, "session-1")
}

func runGnokeyTool(t *testing.T, tool fantasy.AgentTool, params GnokeyParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(gnokeyContext(), fantasy.ToolCall{ID: "call-1", Name: GnokeyToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func allowingPermissions() permission.Service {
	return &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
}

const deployCommand = "gnokey maketx addpkg -pkgpath gno.land/r/dev/hello -pkgdir . -gas-fee 1000000ugnot -gas-wanted 20000000 -chainid dev -remote 127.0.0.1:26657 devtest"

func TestParseGnokeyCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		command string
		action  string
		signs   bool
		key     string
		argv    []string
		wantErr string
	}{
		{
			name:    "addpkg signs, flag goes after the subcommand",
			command: deployCommand,
			action:  "maketx addpkg",
			signs:   true,
			key:     "devtest",
			argv:    []string{"maketx", "addpkg", passwordStdinFlag, "-pkgpath", "gno.land/r/dev/hello", "-pkgdir", ".", "-gas-fee", "1000000ugnot", "-gas-wanted", "20000000", "-chainid", "dev", "-remote", "127.0.0.1:26657", "devtest"},
		},
		{
			name:    "the leading gnokey is optional",
			command: "sign -tx-path tx.json -chainid dev -account-number 1 -account-sequence 2 alice",
			action:  "sign",
			signs:   true,
			key:     "alice",
			argv:    []string{"sign", passwordStdinFlag, "-tx-path", "tx.json", "-chainid", "dev", "-account-number", "1", "-account-sequence", "2", "alice"},
		},
		{
			name:    "an unsigned maketx needs no password",
			command: "maketx call -pkgpath gno.land/r/dev/hello -func Hello -broadcast=false alice",
			action:  "maketx call",
			argv:    []string{"maketx", "call", "-pkgpath", "gno.land/r/dev/hello", "-func", "Hello", "-broadcast=false", "alice"},
		},
		{
			name:    "queries need no password",
			command: "query vm/qrender -data 'gno.land/r/dev/hello:' -remote 127.0.0.1:26657",
			action:  "query",
			argv:    []string{"query", "vm/qrender", "-data", "gno.land/r/dev/hello:", "-remote", "127.0.0.1:26657"},
		},
		{
			name:    "a quoted argument survives and is shown quoted again",
			command: `maketx call -pkgpath gno.land/r/dev/hello -func Post -args "hello world" alice`,
			action:  "maketx call",
			signs:   true,
			key:     "alice",
			argv:    []string{"maketx", "call", passwordStdinFlag, "-pkgpath", "gno.land/r/dev/hello", "-func", "Post", "-args", "hello world", "alice"},
		},
		{name: "empty", command: "gnokey", wantErr: "empty"},
		{name: "maketx without a kind", command: "maketx -pkgpath x", wantErr: "maketx needs a subcommand"},
		{name: "key management is refused", command: "add alice", wantErr: "gnokey add manages keys"},
		{name: "unknown subcommand", command: "frobnicate", wantErr: "unknown gnokey subcommand"},
		{name: "the password flag is the tool's", command: "sign -insecure-password-stdin -tx-path tx.json alice", wantErr: "do not pass -insecure-password-stdin"},
		{name: "nor in its other spelling", command: "sign --insecure-password-stdin=true -tx-path tx.json alice", wantErr: "do not pass -insecure-password-stdin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd, err := parseGnokeyCommand(tt.command)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.action, cmd.action)
			require.Equal(t, tt.signs, cmd.signs)
			require.Equal(t, tt.key, cmd.key)
			require.Equal(t, tt.argv, cmd.argv())
			require.NotContains(t, cmd.display(), passwordStdinFlag)
		})
	}
}

func TestGnokeyCommandDisplayQuotes(t *testing.T) {
	t.Parallel()
	cmd, err := parseGnokeyCommand(`maketx call -pkgpath gno.land/r/dev/hello -func Post -args "it's here" alice`)
	require.NoError(t, err)
	require.Equal(t, `gnokey maketx call -pkgpath gno.land/r/dev/hello -func Post -args 'it'\''s here' alice`, cmd.display())
}

func TestGnokeyCommandMainnet(t *testing.T) {
	t.Parallel()
	for command, want := range map[string]bool{
		deployCommand: false,
		"maketx call -pkgpath gno.land/r/x/y -func F -chainid gnoland-1 -remote https://rpc.gno.land:443 k":       true,
		"maketx call -pkgpath gno.land/r/x/y -func F -chainid staging -remote https://rpc.staging.gno.land:443 k": false,
	} {
		cmd, err := parseGnokeyCommand(command)
		require.NoError(t, err)
		require.Equal(t, want, cmd.mainnet(), command)
	}
}

// TestGnokeyTool_SignsWithThePasswordTypedByTheUser is the whole point:
// the password the user types reaches gnokey's standard input and nothing
// else, and the flag that makes gnokey read it there is the tool's own.
func TestGnokeyTool_SignsWithThePasswordTypedByTheUser(t *testing.T) {
	argsFile, stdinFile := fakeGnokey(t)
	questions := question.NewService()
	tool := NewGnokeyTool(allowingPermissions(), questions, t.TempDir())

	done := make(chan fantasy.ToolResponse, 1)
	go func() {
		done <- runGnokeyTool(t, tool, GnokeyParams{Command: deployCommand, Description: "Deploy hello"})
	}()

	var pending question.Request
	require.Eventually(t, func() bool {
		var ok bool
		pending, ok = questions.Pending()
		return ok
	}, 5*time.Second, 10*time.Millisecond, "the tool must ask for the password")
	require.Equal(t, "session-1", pending.SessionID)
	require.Equal(t, "call-1", pending.ToolCallID)
	require.Len(t, pending.Questions, 1)
	q := pending.Questions[0]
	require.True(t, q.Secret, "the prompt must be masked")
	require.Equal(t, question.TypeFreeText, q.Type)
	require.Equal(t, "Password for key devtest", q.Text)
	require.Contains(t, q.Description, "gnokey maketx addpkg -pkgpath gno.land/r/dev/hello")
	require.NotContains(t, q.Description, "Mainnet")
	require.True(t, questions.Answer([]question.Answer{{QuestionID: q.ID, FillInText: "hunter2 pass"}}))

	resp := <-done
	require.False(t, resp.IsError, resp.Content)

	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	require.Equal(t, []string{"maketx", "addpkg", passwordStdinFlag}, lines[:3])
	require.Equal(t, "devtest", lines[len(lines)-1])

	stdin, err := os.ReadFile(stdinFile)
	require.NoError(t, err)
	require.Equal(t, "hunter2 pass\n", string(stdin))

	// The fake echoes its standard input; the tool scrubs it anyway.
	require.NotContains(t, resp.Content, "hunter2")
	require.NotContains(t, resp.Metadata, "hunter2")
	require.Contains(t, resp.Content, "stdin: [password]")
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, 0, meta.ExitCode)
	require.Equal(t, "Deploy hello", meta.Description)
}

func TestGnokeyTool_MainnetIsNamedInThePrompt(t *testing.T) {
	fakeGnokey(t)
	questions := question.NewService()
	tool := NewGnokeyTool(allowingPermissions(), questions, t.TempDir())
	go runGnokeyTool(t, tool, GnokeyParams{Command: "maketx call -pkgpath gno.land/r/x/y -func F -gas-fee 1ugnot -gas-wanted 1 -chainid gnoland-1 -remote https://rpc.gno.land:443 main"})
	var pending question.Request
	require.Eventually(t, func() bool {
		var ok bool
		pending, ok = questions.Pending()
		return ok
	}, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, pending.Questions[0].Description, "Mainnet (gnoland-1): this spends real GNOT")
	require.True(t, questions.Cancel())
}

func TestGnokeyTool_QueriesAskForNoPassword(t *testing.T) {
	argsFile, stdinFile := fakeGnokey(t)
	questions := question.NewService()
	tool := NewGnokeyTool(allowingPermissions(), questions, t.TempDir())

	resp := runGnokeyTool(t, tool, GnokeyParams{Command: "query bank/balances/g1abc -remote 127.0.0.1:26657", Description: "Balance"})
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "args: query bank/balances/g1abc -remote 127.0.0.1:26657")

	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.NotContains(t, string(args), passwordStdinFlag)
	stdin, err := os.ReadFile(stdinFile)
	require.NoError(t, err)
	require.Empty(t, stdin)
	_, pending := questions.Pending()
	require.False(t, pending)
}

func TestGnokeyTool_RefusesToSignWithNoOneToAsk(t *testing.T) {
	argsFile, _ := fakeGnokey(t)
	tool := NewGnokeyTool(allowingPermissions(), nil, t.TempDir())

	resp := runGnokeyTool(t, tool, GnokeyParams{Command: deployCommand})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "no one to ask")
	require.Contains(t, resp.Content, "gnokey maketx addpkg -pkgpath gno.land/r/dev/hello")
	require.NoFileExists(t, argsFile, "gnokey must not run")
}

func TestGnokeyTool_CancelledPromptStopsTheTurn(t *testing.T) {
	argsFile, _ := fakeGnokey(t)
	questions := question.NewService()
	tool := NewGnokeyTool(allowingPermissions(), questions, t.TempDir())

	done := make(chan fantasy.ToolResponse, 1)
	go func() {
		done <- runGnokeyTool(t, tool, GnokeyParams{Command: deployCommand})
	}()
	require.Eventually(t, func() bool {
		_, ok := questions.Pending()
		return ok
	}, 5*time.Second, 10*time.Millisecond)
	require.True(t, questions.Cancel())

	resp := <-done
	require.True(t, resp.IsError)
	require.True(t, resp.StopTurn, "a cancelled signing ends the turn so the user is asked")
	require.Contains(t, resp.Content, "cancelled")
	require.NoFileExists(t, argsFile, "gnokey must not run")
}

func TestGnokeyTool_PermissionDenied(t *testing.T) {
	argsFile, _ := fakeGnokey(t)
	perms := &denyingPermissionService{mockBashPermissionService: &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}}
	tool := NewGnokeyTool(perms, question.NewService(), t.TempDir())

	resp := runGnokeyTool(t, tool, GnokeyParams{Command: deployCommand, Description: "Deploy hello"})
	require.Equal(t, NewPermissionDeniedResponse(), resp)
	require.NoFileExists(t, argsFile, "gnokey must not run")
	req := perms.request
	require.Equal(t, GnokeyToolName, req.ToolName)
	require.Equal(t, "maketx addpkg", req.Action)
	require.Equal(t, "Deploy hello", req.Description)
	params, ok := req.Params.(BashPermissionsParams)
	require.True(t, ok, "the dialog shows gnokey like bash")
	require.Equal(t, "gnokey maketx addpkg -pkgpath gno.land/r/dev/hello -pkgdir . -gas-fee 1000000ugnot -gas-wanted 20000000 -chainid dev -remote 127.0.0.1:26657 devtest", params.Command)
}

// denyingPermissionService refuses every request and keeps the last one.
type denyingPermissionService struct {
	*mockBashPermissionService
	request permission.CreatePermissionRequest
}

func (d *denyingPermissionService) Request(_ context.Context, req permission.CreatePermissionRequest) (bool, error) {
	d.request = req
	return false, nil
}

func TestGnokeyTool_ReportsAFailure(t *testing.T) {
	fakeGnokey(t)
	t.Setenv("GNOKEY_FAKE_EXIT", "2")
	tool := NewGnokeyTool(allowingPermissions(), question.NewService(), t.TempDir())

	resp := runGnokeyTool(t, tool, GnokeyParams{Command: "query bank/balances/g1abc"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "gnokey exited with code 2")
	require.Contains(t, resp.Content, "args: query bank/balances/g1abc")
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, 2, meta.ExitCode)
}

func TestGnokeyTool_RunsInTheWorkingDir(t *testing.T) {
	fakeGnokey(t)
	dir := t.TempDir()
	tool := NewGnokeyTool(allowingPermissions(), question.NewService(), dir)

	resp := runGnokeyTool(t, tool, GnokeyParams{Command: "list"})
	require.False(t, resp.IsError, resp.Content)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, dir, meta.WorkingDirectory)
}

func TestGnokeyTool_NotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tool := NewGnokeyTool(allowingPermissions(), question.NewService(), t.TempDir())
	resp := runGnokeyTool(t, tool, GnokeyParams{Command: "list"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "not installed")
}

func TestGnokeyTool_RejectsABadCommandBeforeAnything(t *testing.T) {
	t.Parallel()
	tool := NewGnokeyTool(allowingPermissions(), question.NewService(), t.TempDir())
	resp := runGnokeyTool(t, tool, GnokeyParams{Command: "add alice"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "run it themselves")
}

// TestGnokeySessionCommandsKeepTheirSubcommandOrder pins where the password
// flag lands in a `maketx session <kind>` command. gnokey parses flags per
// subcommand, so the flag has to sit after "session" and before the kind;
// putting it after the kind, or at the end, is a flag gnokey's parser never
// sees. The order below was run against a local chain and created a session.
func TestGnokeySessionCommandsKeepTheirSubcommandOrder(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"create", "revoke", "revokeall"} {
		cmd, err := parseGnokeyCommand("maketx session " + kind + " -pubkey gpub1x -gas-fee 1000000ugnot -chainid dev owner")
		require.NoError(t, err)
		require.True(t, cmd.signs, "a session command signs, so it needs the password")
		require.Equal(t, "owner", cmd.key)
		argv := cmd.argv()
		require.Equal(t, []string{"maketx", "session", passwordStdinFlag, kind}, argv[:4],
			"the flag belongs after the subcommand gnokey parses it for")
		require.NotContains(t, cmd.display(), passwordStdinFlag)
	}
}
