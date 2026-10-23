package agent

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/stretchr/testify/require"
)

// Two prompts sent at once while a slow SessionStart hook runs make it
// run once, not twice: the second prompt waits on the submit lock, and
// when it gets it the session is already marked started, so it does not
// run the hooks again. Before the fix, Run decided whether to run
// SessionStart before taking the lock, and did not check again once it
// had it, so the second prompt ran the hooks a second time, as "clear"
// or "compact", and its output reached the model twice.
func TestRun_SessionStartRunsOnceForConcurrentPrompts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	source := filepath.Join(dir, "source")
	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `sleep 2; echo x >> `+count+`; echo "$KLASH_SESSION_START_SOURCE" > `+source)...)

	var wg sync.WaitGroup
	wg.Add(2)
	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() {
		defer wg.Done()
		_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
		done1 <- err
	}()
	go func() {
		defer wg.Done()
		// Wait for the first prompt to be in its hooks.
		require.Eventually(t, func() bool { return r.agent.IsSessionBusy(r.sessionID) }, 3*time.Second, 10*time.Millisecond)
		_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "second"})
		done2 <- err
	}()
	wg.Wait()

	select {
	case err := <-done1:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("first prompt did not finish")
	}
	select {
	case err := <-done2:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("second prompt did not finish")
	}

	data, err := os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data), "the hooks ran once for both prompts")
	data, err = os.ReadFile(source)
	require.NoError(t, err)
	require.Equal(t, "startup\n", string(data), "the hooks ran with source startup, not clear or compact")
}

// A prompt to the plan agent after one to the coder does not fire
// SessionStart again: the session is started for both agents, so the
// second prompt does not run the hooks. Before the fix, sessionStarted
// was per agent, so switching to the plan agent and sending a prompt
// fired the hooks again, as "resume".
func TestRun_SessionStartSharedAcrossAgents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	hookCfgs := sessionStartHooks("", `echo x >> `+count)

	// Build one agent in the shared environment, as a coordinator does:
	// the coder and the plan agent share the session store and the
	// SessionStart state.
	env := testEnv(t)
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{hooks.EventSessionStart: hookCfgs}}
	require.NoError(t, cfg.ValidateHooks())
	llm := &promptRecordingModel{}
	model := Model{
		Model:      llm,
		CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000},
		ModelCfg:   config.SelectedModel{Model: "test-model", Provider: "test"},
	}
	store := config.NewTestStore(cfg, env.workingDir)
	state := newSessionStartState()
	coderAgent := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  store,
		Notify:               &recordingNotifier{},
	}).(*sessionAgent)
	planAgent := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "plan",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  store,
		Notify:               &recordingNotifier{},
	}).(*sessionAgent)
	coderAgent.SetSessionStartState(state)
	planAgent.SetSessionStartState(state)

	sess, err := env.sessions.Create(t.Context(), "shared session")
	require.NoError(t, err)

	// The coder sends the first prompt to the session, which runs the
	// hooks and marks the session started in the shared state.
	_, err = coderAgent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "first"})
	require.NoError(t, err)
	data, err := os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data), "the hooks ran for the coder's first prompt")

	// The plan agent sends a prompt to the same session. The session is
	// started, so the hooks do not run again.
	_, err = planAgent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "second"})
	require.NoError(t, err)
	data, err = os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data), "the plan agent's prompt to the same session does not run the hooks again")
}

// A hidden prompt that runs the SessionStart hooks carries their output
// to the model. Before the fix, promptHookBlock dropped the context of
// hidden prompts, so what the hooks printed never reached the model.
func TestRun_SessionStartHiddenPromptCarriesContext(t *testing.T) {
	t.Parallel()

	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo "The build on main is red."`)...)
	_, err := r.agent.Run(t.Context(), SessionAgentCall{
		SessionID:         r.sessionID,
		Prompt:            "fix the build",
		HiddenUserMessage: true,
	})
	require.NoError(t, err)

	prompts := r.model.recorded()
	require.Len(t, prompts, 1)
	texts := userTexts(prompts[0])
	require.Contains(t, texts[len(texts)-1], "The build on main is red.", "the hidden prompt carries what the hooks printed")
}

// When a UserPromptSubmit hook blocks the first prompt, the SessionStart
// hooks' output still reaches the model with the next prompt that is
// sent, by keeping their output for it. Before the fix, the session was
// marked started before the UserPromptSubmit hooks ran, so when one
// blocked the first prompt, what SessionStart printed was gone, and the
// hooks did not run again in that process.
func TestRun_SessionStartBlockedFirstPromptNextCarriesContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo x >> `+count+`; echo "The build on main is red."`)...)
	// The UserPromptSubmit hook blocks the first prompt only: it exits 2
	// on the first call, and 0 on the second.
	r.cfg.Hooks[hooks.EventUserPromptSubmit] = promptHooks(`[ -f ` + dir + `/blocked ] || { touch ` + dir + `/blocked; echo "no" >&2; exit 2; }`)[hooks.EventUserPromptSubmit]
	require.NoError(t, r.cfg.ValidateHooks())

	// The first prompt is blocked by the UserPromptSubmit hook.
	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "first"})
	require.ErrorIs(t, err, ErrPromptBlocked, "the first prompt is blocked")
	data, err := os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data), "the SessionStart hooks ran for the first prompt")

	// The second prompt is not blocked, and carries what the SessionStart
	// hooks printed for the first.
	_, err = r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "second"})
	require.NoError(t, err, "the second prompt is not blocked")
	data, err = os.ReadFile(count)
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data), "the SessionStart hooks did not run again for the second prompt")

	prompts := r.model.recorded()
	require.Len(t, prompts, 1, "only the second prompt reached the model")
	texts := userTexts(prompts[0])
	require.Contains(t, texts[len(texts)-1], "The build on main is red.", "the second prompt carries what the hooks printed")
}
