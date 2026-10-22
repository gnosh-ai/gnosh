package model

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/session"
)

// upKey returns the Up arrow keystroke bound to editor history recall.
func upKey() tea.Msg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeyUp})
}

// TestUpWithEmptyEditorAndQueuePopsIntoTextarea pins the Claude Code-style
// "press up to edit queued messages": with the agent working and prompts
// queued, Up in an empty editor moves them into the editor, oldest first,
// separated by blank lines, and out of the queue so the pill stops showing
// them. The pop goes off-thread (AgentPopQueuedPrompts is a synchronous
// HTTP round-trip in client/server mode) and lands as a promptQueuePopMsg.
func TestUpWithEmptyEditorAndQueuePopsIntoTextarea(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"queued one", "queued two"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	// The queue pill is populated from the last off-thread refresh.
	m.promptQueue = 2
	m.promptQueueItems = []string{"queued one", "queued two"}
	m.promptQueueCheckedAt = time.Now()
	ws.resetCounters()

	_, cmd := m.Update(upKey())
	require.Zero(t, ws.syncProbes(), "Update itself must not probe the workspace")
	require.NotNil(t, cmd, "Up in an empty editor with a queue must dispatch a pop")
	require.Equal(t, 2, m.promptQueue,
		"the pill still shows the queue until the pop lands")

	runCmds(m, cmd)
	require.Equal(t, 1, ws.popQueueCalls, "the pop must hit the workspace exactly once")
	require.Equal(t, "queued one\n\nqueued two", m.textarea.Value(),
		"the popped prompts must land in the editor oldest first, separated by blank lines")
	require.Equal(t, 0, m.promptQueue, "the pill must stop showing the popped prompts")
	require.Empty(t, m.promptQueueItems)
	require.Equal(t, -1, m.promptHistory.index,
		"the editor holds the popped text, not a history entry")
}

// TestUpWithQueueWhileRefreshInFlightDefers pins that Up defers the pop
// while a queue refresh is in flight: their results would race (the refresh
// would clobber the local queue the pop relies on). The pop is dispatched
// once the refresh lands.
func TestUpWithQueueWhileRefreshInFlightDefers(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"queued one"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueue = 1
	m.promptQueueItems = []string{"queued one"}
	m.promptQueueInFlight = true
	ws.resetCounters()

	_, cmd := m.Update(upKey())
	require.Nil(t, cmd, "the pop must defer while a refresh is in flight")
	require.Zero(t, ws.popQueueCalls)

	// Let the in-flight refresh land; its generation matches, so it is
	// applied and clears promptQueueInFlight.
	_, _ = m.Update(promptQueueMsg{forSession: "s1", gen: m.promptQueueGen, prompts: []string{"queued one"}})
	require.False(t, m.promptQueueInFlight)
	require.Equal(t, 1, m.promptQueue, "the refresh must repopulate the queue")

	_, cmd = m.Update(upKey())
	require.NotNil(t, cmd, "after the refresh lands, Up must dispatch a pop")
	runCmds(m, cmd)
	require.Equal(t, 1, ws.popQueueCalls)
	require.Equal(t, "queued one", m.textarea.Value())
}

// TestUpWithQueueAndTextGoesToHistory pins that Up pops only from an empty
// editor: with text in the editor, Up steps through history as before
// (which now also includes the queued prompts, newest first) and does not
// pop.
func TestUpWithQueueAndTextGoesToHistory(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"queued one", "queued two"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueue = 2
	m.promptQueueItems = []string{"queued one", "queued two"}
	m.promptHistory.messages = []string{"older prompt"}
	m.promptHistory.index = -1
	m.textarea.SetValue("draft text")
	m.textarea.CursorStart()
	ws.resetCounters()

	m.handleHistoryUp(nil)
	require.Zero(t, ws.popQueueCalls, "Up with text in the editor must not pop")
	require.Equal(t, 0, m.promptHistory.index)
	require.Equal(t, "queued two", m.textarea.Value(),
		"history recall still leads with the most recently queued prompt")
	require.Equal(t, 2, m.promptQueue, "the queue must stay queued")
}

// TestUpWithoutQueueStillStepsThroughHistory pins the fallback: with
// nothing queued, Up in an empty editor steps through the session's earlier
// prompts, exactly as before the queue-editing behavior landed.
func TestUpWithoutQueueStillStepsThroughHistory(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, true)
	// The database loads history newest first (ORDER BY created_at DESC).
	m.promptHistory.messages = []string{"newer", "older"}
	m.promptHistory.index = -1
	ws.resetCounters()

	m.handleHistoryUp(nil)
	require.Zero(t, ws.popQueueCalls)
	require.Equal(t, 0, m.promptHistory.index)
	require.Equal(t, "newer", m.textarea.Value())

	m.handleHistoryUp(nil)
	require.Equal(t, 1, m.promptHistory.index)
	require.Equal(t, "older", m.textarea.Value())
}

// The pop takes the prompts out of the agent's queue before its result
// lands, so the result must never be thrown away. A queue transition in the
// meantime (the turn ending, another refresh) only makes the pill's own
// fetch stale; the popped prompts still belong in the editor.
func TestUpPopLandsEvenWhenThePillWentStale(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"queued one"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueue = 1
	m.promptQueueItems = []string{"queued one"}

	_, cmd := m.Update(upKey())
	require.NotNil(t, cmd)
	m.invalidatePromptQueue() // a queue transition while the pop is in flight

	runCmds(m, cmd)
	require.Equal(t, "queued one", m.textarea.Value(), "a popped prompt must not be lost")
}

// Text typed while the pop is in flight stays in the editor, after the
// popped prompts, instead of being wiped by them.
func TestUpPopKeepsTextTypedMeanwhile(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"queued one"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueue = 1
	m.promptQueueItems = []string{"queued one"}

	_, cmd := m.Update(upKey())
	require.NotNil(t, cmd)
	m.textarea.SetValue("typed meanwhile")

	runCmds(m, cmd)
	require.Equal(t, "queued one\n\ntyped meanwhile", m.textarea.Value())
}

// When the session changes while the pop is in flight, the editor belongs to
// another session, so the popped prompts go back to the session they came
// from, in order, rather than being lost or sent to the wrong session.
func TestUpPopAfterASessionSwitchSendsThePromptsBack(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"queued one", "queued two"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueue = 2
	m.promptQueueItems = []string{"queued one", "queued two"}

	_, cmd := m.Update(upKey())
	require.NotNil(t, cmd)
	m.session = &session.Session{ID: "s2"}

	runCmds(m, cmd)
	require.Empty(t, m.textarea.Value(), "the other session's editor stays as it was")
	require.Equal(t, []string{"s1: queued one", "s1: queued two"}, ws.runs)
}
