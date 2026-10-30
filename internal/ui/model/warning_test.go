package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/stretchr/testify/require"
)

// A warning, here a failed hook's, is shown to the user in the status
// bar.
func TestWarningShowsInStatusBar(t *testing.T) {
	pinTTLs(t)

	m := newBusyUI(&countingWorkspace{ready: true})
	cmd := m.handleAgentNotification(notify.Notification{
		Type:      notify.TypeWarning,
		SessionID: "s1",
		Message:   `Stop hook "tests-must-pass" failed: exit 1; the turn ended without its check`,
	})
	require.NotNil(t, cmd)

	msg, ok := cmd().(util.InfoMsg)
	require.True(t, ok, "the warning must reach the status bar")
	require.Equal(t, util.InfoTypeWarn, msg.Type)
	require.Contains(t, msg.Msg, "tests-must-pass")
}

// The property that matters most. TypeAgentFinished and TypeAgentError
// are the busy→idle edge and trigger a busy and queue refresh (see
// TestAgentTerminalNotificationsRefreshBusy). A warning is published
// while the turn is still ending, so if it were read the same way the UI
// would re-probe early and could flip to idle while the agent is busy —
// the family of status-line bugs this TUI has already had.
func TestWarningIsNotATurnEnding(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, true)
	ws.resetCounters()
	busyGen := m.busyFetchGen
	queueGen := m.promptQueueGen

	m.Update(pubsub.Event[notify.Notification]{
		Type:    pubsub.CreatedEvent,
		Payload: notify.Notification{Type: notify.TypeWarning, SessionID: "s1", Message: "x"},
	})

	require.False(t, m.busyFetchInFlight, "a warning must not schedule a busy refresh")
	require.False(t, m.promptQueueInFlight, "a warning must not schedule a queue refresh")
	require.Equal(t, busyGen, m.busyFetchGen, "a warning must not invalidate the busy cache")
	require.Equal(t, queueGen, m.promptQueueGen, "a warning must not invalidate the prompt queue")
	require.True(t, m.isAgentBusy(), "the agent is still mid-turn; nothing here should change that")
}
