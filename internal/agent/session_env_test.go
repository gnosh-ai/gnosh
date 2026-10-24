package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// A SessionStart hook is handed the session's env file as CLAUDE_ENV_FILE,
// as in Claude Code: a file of the user's own, outside the project, that
// what the hook adds to stays in.
func TestRun_SessionStartHookGetsTheEnvFile(t *testing.T) {
	t.Parallel()

	seen := filepath.Join(t.TempDir(), "seen")
	r := sessionStartAgent(t, false, "", sessionStartHooks("",
		`echo "$CLAUDE_ENV_FILE" > `+seen+` && echo 'export GREETING=hello' >> "$CLAUDE_ENV_FILE"`)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
	require.NoError(t, err)

	envFile := config.SessionEnvFile(r.sessionID)
	data, err := os.ReadFile(seen)
	require.NoError(t, err)
	require.Equal(t, envFile+"\n", string(data))
	rel, err := filepath.Rel(r.env.workingDir, envFile)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(rel, ".."), "the file is not in the project: %s", envFile)
	data, err = os.ReadFile(envFile)
	require.NoError(t, err)
	require.Equal(t, "export GREETING=hello\n", string(data))
	info, err := os.Stat(envFile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "only the user can read it")
}

// The env file is made once and kept: a session started again (resumed or
// summarized) keeps what its hooks wrote before, whether or not they run
// again. A file the hooks left empty is removed, so sessions whose hooks
// set nothing leave nothing behind.
func TestSessionEnvFileIsKeptAndAnEmptyOneDropped(t *testing.T) {
	t.Parallel()

	id := "kept-" + t.Name()
	path := sessionEnvFile(id)
	require.Equal(t, config.SessionEnvFile(id), path)
	require.NoError(t, os.WriteFile(path, []byte("export A=1\n"), 0o600))
	require.Equal(t, path, sessionEnvFile(id))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "export A=1\n", string(data), "started again, the session keeps its lines")

	dropIfEmpty(path)
	require.FileExists(t, path, "a file with lines stays")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	dropIfEmpty(path)
	require.NoFileExists(t, path, "an empty one goes")
}

// probeOnceModel calls the probe tool once, then answers "done".
type probeOnceModel struct {
	fakeLanguageModel
	calls atomic.Int64
}

func (m *probeOnceModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	if m.calls.Add(1) == 1 {
		return func(yield func(fantasy.StreamPart) bool) {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputStart, ID: "call-1", ToolCallName: "probe"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputDelta, ID: "call-1", Delta: "{}"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputEnd, ID: "call-1"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolCall, ID: "call-1", ToolCallName: "probe", ToolCallInput: "{}"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls})
		}, nil
	}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: "done"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

type envProbeParams struct {
	Note string `json:"note,omitempty"`
}

// A turn's tools are told the session's env file, so its bash commands
// run what the SessionStart hooks wrote there, as Claude Code's do. A
// sub-agent's turn keeps its parent session's: a helper's commands get
// the environment the hooks set up too.
func TestRun_TheTurnsToolsGetTheEnvFile(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		subAgent bool
		ctx      func(context.Context) context.Context
		want     func(sessionID string) string
	}{
		{"a session's own", false, func(ctx context.Context) context.Context { return ctx }, config.SessionEnvFile},
		{"a sub-agent's parent's", true, func(ctx context.Context) context.Context {
			return tools.WithSessionEnvFile(ctx, "/parent/session.sh")
		}, func(string) string { return "/parent/session.sh" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			seen := make(chan string, 1)
			probe := fantasy.NewAgentTool("probe", "Report the env file.",
				func(ctx context.Context, _ envProbeParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
					seen <- tools.SessionEnvFileFromContext(ctx)
					return fantasy.NewTextResponse("ok"), nil
				})
			model := Model{Model: &probeOnceModel{}, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
			a := NewSessionAgent(SessionAgentOptions{
				LargeModel:           model,
				SmallModel:           model,
				SystemPrompt:         "test",
				IsSubAgent:           tc.subAgent,
				IsYolo:               true,
				DisableAutoSummarize: true,
				Sessions:             env.sessions,
				Messages:             env.messages,
				Tools:                []fantasy.AgentTool{probe},
			}).(*sessionAgent)
			sess, err := env.sessions.Create(t.Context(), "env file")
			require.NoError(t, err)
			// A message already there keeps Run from asking the model
			// for a title.
			_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
				Role:  message.User,
				Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
			})
			require.NoError(t, err)

			_, err = a.Run(tc.ctx(t.Context()), SessionAgentCall{SessionID: sess.ID, Prompt: "probe"})
			require.NoError(t, err)
			select {
			case got := <-seen:
				require.Equal(t, tc.want(sess.ID), got)
			default:
				t.Fatal("the probe tool was not called")
			}
		})
	}
}
