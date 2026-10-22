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

// TestUpWithQueueWhileRefreshInFlightStillPops pins that a pill refresh in
// flight does not swallow the keypress: the pop goes out at once, and when
// it lands it bumps the queue generation, so the refresh's older result is
// discarded instead of putting the popped prompts back in the pill.
func TestUpWithQueueWhileRefreshInFlightStillPops(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"queued one"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueue = 1
	m.promptQueueItems = []string{"queued one"}
	m.promptQueueInFlight = true
	staleGen := m.promptQueueGen
	ws.resetCounters()

	_, cmd := m.Update(upKey())
	require.NotNil(t, cmd, "Up must pop even while a refresh is in flight")
	runCmds(m, cmd)
	require.Equal(t, 1, ws.popQueueCalls)
	require.Equal(t, "queued one", m.textarea.Value())

	// The refresh that was in flight lands late, listing the prompt the
	// pop already took.
	_, _ = m.Update(promptQueueMsg{forSession: "s1", gen: staleGen, prompts: []string{"queued one"}})
	require.Zero(t, m.promptQueue, "a refresh older than the pop must not bring the prompt back")
}

// TestUpWithQueueAndTextGoesToHistory pins that Up pops only from an empty
// editor: with text in the editor, Up steps through the prompts already
// sent, as before, and neither pops nor offers a copy of a queued prompt.
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
	require.Equal(t, "older prompt", m.textarea.Value(),
		"history offers what was sent, not a copy of what is still queued")
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
	require.Equal(t, []string{"s1: queued one\n\nqueued two"}, ws.runs,
		"sent back as the one prompt Enter would have sent, so none can be lost or reordered")
}

// When everything queued is what Up leaves in place (a channel message, a
// prompt with attachments, one a caller waits on), the pop takes nothing;
// Up must then do what it does with nothing queued instead of going dead.
func TestUpWithOnlyKeptPromptsQueuedStepsIntoHistory(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"<channel>hi</channel>"}, keepOnPop: true}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueue = 1
	m.promptQueueItems = []string{"<channel>hi</channel>"}
	m.promptHistory.messages = []string{"earlier prompt"}
	m.promptHistory.index = -1

	_, cmd := m.Update(upKey())
	require.NotNil(t, cmd)
	runCmds(m, cmd)
	require.Equal(t, 1, ws.popQueueCalls)
	require.NotEmpty(t, m.textarea.Value(), "Up must not be a dead key")
	require.Equal(t, 0, m.promptHistory.index, "it steps into history, as with nothing queued")
}

// After the pop the cursor sits at the end of the text, as after typing, so a
// second Up moves within the text instead of swapping it out of the editor
// for an old prompt from history.
func TestSecondUpAfterPopStaysOnThePoppedText(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true, queued: []string{"queued one"}}
	m := newBusyUI(ws)
	warmCaches(m, true)
	m.promptQueue = 1
	m.promptQueueItems = []string{"queued one"}
	m.promptHistory.messages = []string{"older prompt"}
	m.promptHistory.index = -1

	_, cmd := m.Update(upKey())
	runCmds(m, cmd)
	require.Equal(t, "queued one", m.textarea.Value())

	m.handleHistoryUp(nil)
	require.Equal(t, "queued one", m.textarea.Value(), "the popped text must stay in the editor")
	require.Equal(t, -1, m.promptHistory.index)
}
