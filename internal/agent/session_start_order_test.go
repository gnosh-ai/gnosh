package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/stretchr/testify/require"
)

// An Esc while a SessionStart hook runs ends the turn with the cancelled
// RunComplete a waiting client blocks on (`gnosh run`, the TUI's pending
// turn). The hooks ran after the dispatch before, and the cancel path
// there returned without one.
func TestRun_CancelInASessionStartHookReportsTheCancelledTurn(t *testing.T) {
	t.Parallel()

	r := sessionStartAgent(t, false, "", sessionStartHooks("", `sleep 5; echo "late context"`)...)
	var (
		mu       sync.Mutex
		complete []notify.RunComplete
	)
	done := make(chan error, 1)
	go func() {
		_, err := r.agent.Run(context.Background(), SessionAgentCall{
			SessionID: r.sessionID,
			RunID:     "run-1",
			Prompt:    "do the thing",
			OnComplete: func(c notify.RunComplete) {
				mu.Lock()
				defer mu.Unlock()
				complete = append(complete, c)
			},
		})
		done <- err
	}()
	require.Eventually(t, func() bool { return r.agent.IsSessionBusy(r.sessionID) }, 3*time.Second, 10*time.Millisecond)

	r.agent.Cancel(r.sessionID)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(4 * time.Second):
		t.Fatal("Run did not return after the cancel")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, complete, 1, "the turn ends with exactly one RunComplete")
	require.Equal(t, "run-1", complete[0].RunID)
	require.True(t, complete[0].Cancelled)
	require.Empty(t, r.model.recorded(), "the cancelled prompt is not sent")
}

// With SessionStart and UserPromptSubmit hooks both set, as Jae's Claude
// Code setup has them (memsync and jmem), the first prompt carries what
// both printed, SessionStart's first, and SessionStart's hooks run first.
// What SessionStart printed used to replace what UserPromptSubmit had.
func TestRun_SessionStartAndUserPromptSubmitContextsBothReachTheModel(t *testing.T) {
	t.Parallel()

	order := filepath.Join(t.TempDir(), "order")
	r := sessionStartAgent(t, false, "",
		sessionStartHooks("", `echo session-start >> `+order+`; echo "memory synced"`)...)
	r.cfg.Hooks[hooks.EventUserPromptSubmit] = promptHooks(`echo prompt-submit >> ` + order + `; echo "jmem says hello"`)[hooks.EventUserPromptSubmit]
	require.NoError(t, r.cfg.ValidateHooks())

	_, err := r.agent.Run(t.Context(), SessionAgentCall{SessionID: r.sessionID, Prompt: "fix the build"})
	require.NoError(t, err)

	prompts := r.model.recorded()
	require.Len(t, prompts, 1)
	texts := userTexts(prompts[0])
	sent := texts[len(texts)-1]
	require.Contains(t, sent, "memory synced")
	require.Contains(t, sent, "jmem says hello")
	require.Less(t, strings.Index(sent, "memory synced"), strings.Index(sent, "jmem says hello"))

	ran, err := os.ReadFile(order)
	require.NoError(t, err)
	require.Equal(t, "session-start\nprompt-submit\n", string(ran), "SessionStart's hooks run before UserPromptSubmit's, as in Claude Code")
}
