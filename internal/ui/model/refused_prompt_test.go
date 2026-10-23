package model

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/agent/notify"
)

// A prompt sent behind a running turn and refused (a UserPromptSubmit hook
// blocked it) is reported, and the running turn keeps its spinner; before,
// the refusal ended the running turn's pending state.
func TestRefusedQueuedPromptLeavesTheRunningTurn(t *testing.T) {
	m := newPendingUI(t)
	m.sendMessage("hello")
	require.True(t, hasPending(m))

	_, cmd := m.Update(agentRunSubmittedMsg{sessionID: "s1", err: errors.New("prompt blocked by a UserPromptSubmit hook: no"), queued: true})
	require.True(t, hasPending(m), "the running turn is still running")
	require.Contains(t, infoMsgs(cmd), "prompt blocked by a UserPromptSubmit hook: no")
}

// Nor does it switch off a /loop driving the running turn.
func TestLoop_KeepsGoingWhenAQueuedPromptIsRefused(t *testing.T) {
	t.Parallel()

	m, _ := newPlanUI(t, "s1")
	m.loop = loopState{active: true, sessionID: "s1", turns: 2}
	_, _ = m.Update(agentRunSubmittedMsg{sessionID: "s1", err: errors.New("prompt blocked by a UserPromptSubmit hook: no"), queued: true})
	require.True(t, m.loop.active)
}

// In client/server mode a failed run, a blocked prompt among them, is only
// an agent-error notification; its message is now shown, not dropped.
func TestAgentErrorNotificationIsShown(t *testing.T) {
	m := newPendingUI(t)
	cmd := m.handleAgentNotification(notify.Notification{
		SessionID: "s1",
		Type:      notify.TypeAgentError,
		Message:   "prompt blocked by a UserPromptSubmit hook: no keys in prompts",
	})
	require.Contains(t, infoMsgs(cmd), "prompt blocked by a UserPromptSubmit hook: no keys in prompts")
}
