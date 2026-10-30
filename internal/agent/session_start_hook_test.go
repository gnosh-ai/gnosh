package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// sessionStartRun is the pieces a SessionStart test drives: the agent,
// its environment, the model it answers with, and the session to send a
// prompt to.
type sessionStartRun struct {
	agent     *sessionAgent
	env       fakeEnv
	model     *promptRecordingModel
	sessionID string
	cfg       *config.Config
	notifier  *recordingNotifier
}

// sessionStartAgent builds a session agent with the given SessionStart
// hooks configured, through ValidateHooks. If preseed is non-empty a user
// message with that text is created in the session before it is
// returned, so the agent's first prompt sees messages already there
// (source "resume").
func sessionStartAgent(t *testing.T, subAgent bool, preseed string, hookCfgs ...config.HookConfig) sessionStartRun {
	t.Helper()
	env := testEnv(t)
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{hooks.EventSessionStart: hookCfgs}}
	require.NoError(t, cfg.ValidateHooks())
	llm := &promptRecordingModel{}
	model := Model{
		Model:      llm,
		CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000},
		ModelCfg:   config.SelectedModel{Model: "test-model", Provider: "test"},
	}
	notifier := &recordingNotifier{}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsSubAgent:           subAgent,
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  config.NewTestStore(cfg, env.workingDir),
		Notify:               notifier,
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "session start")
	require.NoError(t, err)
	if preseed != "" {
		_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: preseed}},
		})
		require.NoError(t, err)
	}
	return sessionStartRun{agent: a, env: env, model: llm, sessionID: sess.ID, cfg: cfg, notifier: notifier}
}

func sessionStartHooks(matcher string, commands ...string) []config.HookConfig {
	cfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		cfgs[i] = config.HookConfig{Command: cmd, Matcher: matcher}
	}
	return cfgs
}

// A new session's first prompt fires the hooks with source "startup",
// and the second prompt does not fire them again.
func TestRun_SessionStartFirstPromptStartup(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "source")
	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo "$KLASH_SESSION_START_SOURCE" > `+out)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
	require.NoError(t, err)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "startup\n", string(data), "a new session's first prompt is a startup")

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "second"})
	require.NoError(t, err)
	data, err = os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "startup\n", string(data), "the second prompt does not run the hooks again")
}

// A session that already has messages fires the hooks with source
// "resume".
func TestRun_SessionStartResume(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "source")
	r := sessionStartAgent(t, false, "earlier prompt",
		sessionStartHooks("", `echo "$KLASH_SESSION_START_SOURCE" > `+out)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "next"})
	require.NoError(t, err)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "resume\n", string(data), "a session with messages is a resume")
}

// A second new session in the same process fires the hooks with source
// "clear".
func TestRun_SessionStartSecondSessionClear(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out1 := filepath.Join(dir, "source1")
	out2 := filepath.Join(dir, "source2")
	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo "$KLASH_SESSION_START_SOURCE" > `+out1)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
	require.NoError(t, err)
	data, err := os.ReadFile(out1)
	require.NoError(t, err)
	require.Equal(t, "startup\n", string(data))

	sess2, err := r.env.sessions.Create(t.Context(), "second session")
	require.NoError(t, err)
	r.cfg.Hooks[hooks.EventSessionStart] = sessionStartHooks("", `echo "$KLASH_SESSION_START_SOURCE" > `+out2)
	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: sess2.ID, Prompt: "hello"})
	require.NoError(t, err)
	data, err = os.ReadFile(out2)
	require.NoError(t, err)
	require.Equal(t, "clear\n", string(data), "a fresh session in a running process is a clear")
}

// A matcher is tested against the source, as in Claude Code: a hook
// matched on "resume" does not fire on a new session.
func TestRun_SessionStartMatcherOnTheSource(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		matcher string
		source  string
		preseed string
		want    int
	}{
		{"^resume$", "resume", "earlier", 1},
		{"^resume$", "startup", "", 0},
		{"^startup$", "startup", "", 1},
		{"^startup$", "resume", "earlier", 0},
		{"", "startup", "", 1},
		{"", "resume", "earlier", 1},
		{"^fork$", "fork", "earlier", 1},
		{"^resume$", "fork", "earlier", 0},
	} {
		t.Run(tc.matcher+"_"+tc.source, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Hooks: map[string][]config.HookConfig{
				hooks.EventSessionStart: {{Command: "true", Matcher: tc.matcher}},
			}}
			require.NoError(t, cfg.ValidateHooks())
			runner := hooks.NewRunner(cfg.Hooks[hooks.EventSessionStart], t.TempDir(), t.TempDir())
			agg, err := runner.RunSessionStart(t.Context(), "s", "m", tc.source, "")
			require.NoError(t, err)
			require.Equal(t, tc.want, agg.HookCount,
				"the hook fires (or not) based on whether the source matches")
		})
	}
}

// The first prompt after a summary fires the hooks again, with source
// "compact".
func TestRun_SessionStartAfterSummaryCompact(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "source")
	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo "$KLASH_SESSION_START_SOURCE" > `+out)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
	require.NoError(t, err)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "startup\n", string(data))

	subModel := &subSummarizeModel{}
	sumModel := Model{Model: subModel, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}, ModelCfg: config.SelectedModel{Model: "test-model", Provider: "test"}}
	r.agent.SetModels(sumModel, sumModel)
	require.NoError(t, r.agent.Summarize(t.Context(), r.sessionID, "", nil, nil, preCompactTriggerManual))

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "after summary"})
	require.NoError(t, err)
	data, err = os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "compact\n", string(data), "the prompt after a summary is a compact")
}

// A session made as a copy of another to go on in, by /branch or
// --fork-session, fires the hooks with source "fork" for its first
// prompt, as in Claude Code, and not again for the next; after a summary
// its next prompt is a compact's, and a session not copied that has
// messages is a resume's.
func TestRun_SessionStartFork(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "sources")
	r := sessionStartAgent(t, false, "the conversation copied",
		sessionStartHooks("", `echo "$KLASH_SESSION_START_SOURCE" >> `+out)...)
	c := &coordinator{sessionStarts: r.agent.sessionStarts}
	c.Branched(r.sessionID)
	sources := func() string {
		t.Helper()
		data, err := os.ReadFile(out)
		require.NoError(t, err)
		return string(data)
	}

	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first in the copy"})
	require.NoError(t, err)
	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "second"})
	require.NoError(t, err)
	require.Equal(t, "fork\n", sources(), "a copy's first prompt is a fork's, its second runs no hooks")

	sumModel := Model{Model: &subSummarizeModel{}, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}, ModelCfg: config.SelectedModel{Model: "test-model", Provider: "test"}}
	r.agent.SetModels(sumModel, sumModel)
	require.NoError(t, r.agent.Summarize(t.Context(), r.sessionID, "", nil, nil, preCompactTriggerManual))
	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "after summary"})
	require.NoError(t, err)
	require.Equal(t, "fork\ncompact\n", sources(), "the prompt after a summary is a compact's")

	other, err := r.env.sessions.Create(t.Context(), "not a copy")
	require.NoError(t, err)
	_, err = r.env.messages.Create(t.Context(), other.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier"}},
	})
	require.NoError(t, err)
	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: other.ID, Prompt: "back to it"})
	require.NoError(t, err)
	require.Equal(t, "fork\ncompact\nresume\n", sources(), "only the copy is a fork")
}

// What a SessionStart hook prints reaches the model with the prompt, in
// Claude Code's UserPromptSubmit tag, and is still in the history on the
// next turn.
func TestRun_SessionStartHookAddsContext(t *testing.T) {
	t.Parallel()

	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo "The build on main is red."`)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "fix the build"})
	require.NoError(t, err)

	want := "fix the build\n\n<user-prompt-submit-hook>\nThe build on main is red.\n</user-prompt-submit-hook>"
	prompts := r.model.recorded()
	require.Len(t, prompts, 1)
	first := userTexts(prompts[0])
	require.Equal(t, want, first[len(first)-1])

	msgs, err := r.env.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)
	var stored *message.Message
	for i := range msgs {
		if msgs[i].Role == message.User && msgs[i].Content().Text == "fix the build" {
			stored = &msgs[i]
		}
	}
	require.NotNil(t, stored, "the chat keeps the prompt as typed")
	require.Equal(t, []string{message.PromptHookContext("The build on main is red.")}, stored.PromptHookBlocks())

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "and the tests"})
	require.NoError(t, err)
	prompts = r.model.recorded()
	require.Len(t, prompts, 2)
	require.Contains(t, userTexts(prompts[1]), want, "the rebuilt history sends the first prompt exactly as it went out")
}

// A hook that exits 2 does not stop the prompt, as in Claude Code: the
// prompt goes ahead and the user is told the hook objected.
func TestRun_SessionStartExit2DoesNotStopThePrompt(t *testing.T) {
	t.Parallel()

	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo "not yet" >&2; exit 2`)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "go"})
	require.NoError(t, err, "the prompt goes ahead over the hook's objection")
	require.Len(t, r.model.recorded(), 1, "the model was asked")
	warnings := r.notifier.warnings()
	require.Len(t, warnings, 1, "the user is told the hook objected")
	require.Contains(t, warnings[0].Message, "not yet", "the hook's reason")
	require.Contains(t, warnings[0].Message, "SessionStart", "the event is named")
}

// A hook that fails, or here could not run, is shown as one hook warning,
// not also as an objection, and the prompt goes ahead without it: no
// hook can stop it.
func TestRun_SessionStartFailedHookWarns(t *testing.T) {
	t.Parallel()

	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", "./hooks/does-not-exist.sh")...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "go"})
	require.NoError(t, err)
	require.Len(t, r.model.recorded(), 1)
	warnings := r.notifier.warnings()
	require.Len(t, warnings, 1, "the user is told the hook did not run")
	require.Contains(t, warnings[0].Message, "SessionStart", "the event is named")
}

// A sub-agent's session never fires SessionStart hooks, as it fires no
// other hook: the prompt goes out with no hook output and no warning.
func TestRun_SessionStartSubAgentFiresNoHooks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	r := sessionStartAgent(t, true, "",
		sessionStartHooks("", `echo x >> `+count)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "go"})
	require.NoError(t, err)
	require.Len(t, r.model.recorded(), 1, "the prompt still goes out")
	_, err = os.Stat(count)
	require.True(t, os.IsNotExist(err), "the sub-agent's hooks do not run")
	require.Empty(t, r.notifier.warnings())
}

// The payload carries hook_event_name "SessionStart", the session ID,
// the working directory, the model ID, and the source.
func TestRun_SessionStartPayload(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	r := sessionStartAgent(t, false, "earlier prompt",
		sessionStartHooks("", `cat > `+payloadPath)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "next"})
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(readFileForTest(t, payloadPath)), &payload))
	require.Equal(t, "SessionStart", payload["hook_event_name"])
	require.Equal(t, "SessionStart", payload["event"])
	require.Equal(t, r.sessionID, payload["session_id"])
	require.Equal(t, "resume", payload["source"])
	require.NotEmpty(t, payload["model"], "the ID of the model answering")
	require.NotEmpty(t, payload["cwd"], "the working directory")
}

// An Esc while the SessionStart hooks run cancels the prompt, as it does
// while UserPromptSubmit hooks run: the hooks stop, the prompt is not
// sent, and the session is not busy.
func TestRun_CancelWhileASessionStartHookRuns(t *testing.T) {
	t.Parallel()

	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `sleep 5; echo "late context"`)...)
	done := make(chan error, 1)
	go func() {
		_, err := r.agent.Run(context.Background(), SessionAgentCall{SessionID: r.sessionID, Prompt: "do the thing"})
		done <- err
	}()
	require.Eventually(t, func() bool { return r.agent.IsSessionBusy(r.sessionID) }, 3*time.Second, 10*time.Millisecond,
		"a prompt in its SessionStart hooks makes the session busy")

	r.agent.Cancel(r.sessionID)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(4 * time.Second):
		t.Fatal("Run did not return after the cancel")
	}
	require.Empty(t, r.model.recorded(), "the cancelled prompt is not sent")
	require.False(t, r.agent.IsSessionBusy(r.sessionID))
}

// A Claude Code hook that answers with hookSpecificOutput.additionalContext
// adds context for the SessionStart event too.
func TestRun_SessionStartHookClaudeCodeJSON(t *testing.T) {
	t.Parallel()

	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"Today is Thursday."}}'`)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "deploy"})
	require.NoError(t, err)
	texts := userTexts(r.model.recorded()[0])
	require.Equal(t, "deploy\n\n<user-prompt-submit-hook>\nToday is Thursday.\n</user-prompt-submit-hook>", texts[len(texts)-1])
}

// A hook that exits 2 still marks the session as started: the second
// prompt does not run the hooks again, and is not treated as a fresh
// startup. The prompt goes ahead over the block either way.
func TestRun_SessionStartBlockStillMarksSessionStarted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo x >> `+count+`; echo "no" >&2; exit 2`)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
	require.NoError(t, err, "the prompt goes ahead over the block")
	data, err := os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data), "the hook ran for the first prompt")

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "second"})
	require.NoError(t, err)
	data, err = os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data), "the second prompt does not run the hooks again")
}

// A new session starts with the project's Gno board in view, when an item
// on it is open: what is next goes with the first prompt, as SessionStart
// hook output does, and stays in the history as it went out.
func TestRun_BoardSummaryGoesWithTheFirstPrompt(t *testing.T) {
	t.Parallel()

	r := sessionStartAgent(t, false, "")
	board := "<gno-board>\nYour board: 2 open, 1 done · next: 3 \"update CHANGELOG\"\n</gno-board>"
	r.agent.boardSummary = func() string { return board }
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "carry on"})
	require.NoError(t, err)

	want := "carry on\n\n" + message.PromptHookContext(board)
	prompts := r.model.recorded()
	require.Len(t, prompts, 1)
	first := userTexts(prompts[0])
	require.Equal(t, want, first[len(first)-1])

	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "and the tag"})
	require.NoError(t, err)
	prompts = r.model.recorded()
	require.Len(t, prompts, 2)
	texts := userTexts(prompts[1])
	require.Contains(t, texts, want, "the first prompt goes out again as it did")
	require.Equal(t, "and the tag", texts[len(texts)-1], "later prompts carry no board")
}
