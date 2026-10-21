package model

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

func running() loopState {
	return loopState{active: true, sessionID: "s1", remaining: 5}
}

func TestLoopDecide_ContinuesOnAPlainTurn(t *testing.T) {
	t.Parallel()
	stop, cont := loopDecide(running(), notify.RunComplete{SessionID: "s1", Text: "still working on it"})
	require.True(t, cont, "an ordinary finished turn must continue the loop")
	require.Empty(t, string(stop))
}

func TestLoopDecide_StopsOnSentinel(t *testing.T) {
	t.Parallel()
	stop, cont := loopDecide(running(), notify.RunComplete{SessionID: "s1", Text: "all finished\nLOOP_DONE"})
	require.False(t, cont)
	require.Equal(t, loopStopDone, stop)
}

func TestLoopDecide_StopsOnCancelAndError(t *testing.T) {
	t.Parallel()
	stop, cont := loopDecide(running(), notify.RunComplete{SessionID: "s1", Cancelled: true})
	require.False(t, cont)
	require.Equal(t, loopStopCancelled, stop, "Esc must end the loop, not restart it")

	stop, cont = loopDecide(running(), notify.RunComplete{SessionID: "s1", Error: "boom"})
	require.False(t, cont)
	require.Equal(t, loopStopError, stop, "a failed turn must not be retried forever")
}

func TestLoopDecide_StopsWhenCapReached(t *testing.T) {
	t.Parallel()
	st := running()
	st.remaining = 0
	stop, cont := loopDecide(st, notify.RunComplete{SessionID: "s1", Text: "more to do"})
	require.False(t, cont)
	require.Equal(t, loopStopExhausted, stop, "the cap must bound a model that never reports done")
}

func TestLoopDecide_IgnoresOtherSessionsAndIdleLoop(t *testing.T) {
	t.Parallel()
	stop, cont := loopDecide(running(), notify.RunComplete{SessionID: "other", Text: "hi"})
	require.False(t, cont)
	require.Empty(t, string(stop), "another session's turn must neither continue nor stop this loop")

	stop, cont = loopDecide(loopState{}, notify.RunComplete{SessionID: "s1", Text: "hi"})
	require.False(t, cont)
	require.Empty(t, string(stop), "no loop running means nothing to report")
}

func TestLoopDecide_DoesNotStopWhenTheModelOnlyMentionsTheSentinel(t *testing.T) {
	t.Parallel()

	stop, cont := loopDecide(running(), notify.RunComplete{
		SessionID: "s1",
		Text:      "Created a.txt. I will reply LOOP_DONE once b.txt and c.txt are written.",
	})
	require.True(t, cont, "a turn that merely mentions the sentinel must keep looping")
	require.Empty(t, string(stop))
}

func TestLoopReportedDone_RequiresTheSentinelOnItsOwnLine(t *testing.T) {
	t.Parallel()

	// The real end of a task.
	require.True(t, loopReportedDone("LOOP_DONE"))
	require.True(t, loopReportedDone("all three files exist\nLOOP_DONE"))
	require.True(t, loopReportedDone("done\n  LOOP_DONE  \n"))

	// Talking about the sentinel is not reporting done. Matching it anywhere
	// in the text would cut the loop off on its very first turn.
	require.False(t, loopReportedDone("I will reply LOOP_DONE when the files exist"))
	require.False(t, loopReportedDone("Next I will write b.txt, then say LOOP_DONE."))
	require.False(t, loopReportedDone("LOOP_DONE_NOT_YET"))
	require.False(t, loopReportedDone("still working"))
}

// The tests below drive the real methods on a bare UI. They only exercise
// paths that must not reach the workspace: if the busy guard in startLoop or
// a stop branch in advanceLoop regressed into sending a message, the nil
// workspace would panic, which is exactly the regression being guarded.

func TestStartLoop_NeedsASession(t *testing.T) {
	t.Parallel()

	m, _ := newPlanUI(t, "")
	cmd := m.startLoop()
	require.NotNil(t, cmd, "the user is told why nothing happened")
	require.False(t, m.loop.active, "no session means no loop to run")
}

func TestStartLoop_WhileATurnIsRunningOnlyArms(t *testing.T) {
	t.Parallel()

	m, _ := newPlanUI(t, "s1")
	m.agentBusyCache.val = true // a turn is already running

	cmd := m.startLoop()

	require.NotNil(t, cmd)
	require.True(t, m.loop.active, "the loop is armed")
	require.Equal(t, "s1", m.loop.sessionID)
	require.Equal(t, config.DefaultLoopMaxTurns, m.loop.remaining)
	// Reaching sendMessageInternal here would panic on the nil workspace:
	// the running turn's RunComplete is what drives the first continuation.
}

func TestAdvanceLoop_StopsAndClearsWhenTheModelReportsDone(t *testing.T) {
	t.Parallel()

	m := &UI{loop: loopState{active: true, sessionID: "s1", remaining: 5}}
	cmd := m.advanceLoop(notify.RunComplete{SessionID: "s1", Text: "finished\nLOOP_DONE"})

	require.NotNil(t, cmd, "the user is told the loop ended")
	require.False(t, m.loop.active, "a finished loop must not keep driving turns")
}

func TestAdvanceLoop_StopsAndClearsAtTheCap(t *testing.T) {
	t.Parallel()

	m := &UI{loop: loopState{active: true, sessionID: "s1", remaining: 0}}
	cmd := m.advanceLoop(notify.RunComplete{SessionID: "s1", Text: "still going"})

	require.NotNil(t, cmd)
	require.False(t, m.loop.active, "the cap must actually stop the loop")
}

func TestAdvanceLoop_LeavesAnotherSessionsLoopAlone(t *testing.T) {
	t.Parallel()

	m := &UI{loop: loopState{active: true, sessionID: "s1", remaining: 5}}
	cmd := m.advanceLoop(notify.RunComplete{SessionID: "s2", Text: "LOOP_DONE"})

	require.Nil(t, cmd, "another session's turn says nothing")
	require.True(t, m.loop.active, "and must not disarm this loop")
	require.Equal(t, 5, m.loop.remaining)
}

func TestStopLoop_IsQuietWhenNoLoopIsRunning(t *testing.T) {
	t.Parallel()

	m := &UI{}
	require.Nil(t, m.stopLoop("cancelled"), "Esc on an idle session says nothing about loops")

	m.loop = loopState{active: true, sessionID: "s1", remaining: 3}
	require.NotNil(t, m.stopLoop("cancelled"))
	require.False(t, m.loop.active)
}

// TestAdvanceLoop_SendsTheNextContinuationHidden covers the branch that makes
// /loop a loop at all: a finished turn driving the next one. It is the only
// branch that reaches the workspace, so it needs a real UI rather than a bare
// struct, and it is where a regression would be least visible — the loop would
// simply stop continuing, looking like the model had finished.
func TestAdvanceLoop_SendsTheNextContinuationHidden(t *testing.T) {
	t.Parallel()

	u, ws := newPlanUI(t, "sess-1")
	ws.agentReady = true
	u.loop = loopState{active: true, sessionID: "sess-1", remaining: 3}

	cmd := u.advanceLoop(notify.RunComplete{SessionID: "sess-1", Text: "wrote a.txt, still more to do"})
	require.NotNil(t, cmd, "an unfinished turn must drive the next one")
	drainCmd(cmd)

	require.Equal(t, []string{loopPrompt}, ws.runPrompts, "exactly one continuation, and it is the loop's")
	require.Equal(t, []bool{true}, ws.runHidden, "continuations stay in model history, not in the chat")
	require.True(t, u.loop.active, "the loop keeps running")
	require.Equal(t, 2, u.loop.remaining, "and spends one turn of its budget")
}

// TestAdvanceLoop_SendsNothingOnceTheModelIsDone pairs with the test above:
// the same setup, but a finished task must drive no further turn.
func TestAdvanceLoop_SendsNothingOnceTheModelIsDone(t *testing.T) {
	t.Parallel()

	u, ws := newPlanUI(t, "sess-1")
	ws.agentReady = true
	u.loop = loopState{active: true, sessionID: "sess-1", remaining: 3}

	drainCmd(u.advanceLoop(notify.RunComplete{SessionID: "sess-1", Text: "all three files exist\nLOOP_DONE"}))

	require.Empty(t, ws.runPrompts, "a finished task must not be given another turn")
	require.False(t, u.loop.active)
}

// drainCmd runs a command and anything it batches or sequences, so the work
// it describes actually reaches the workspace. tea.Sequence yields a message
// of an unexported slice-of-Cmd type, which no type switch here can name, so
// any slice of commands is walked reflectively.
func drainCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	switch m := msg.(type) {
	case tea.BatchMsg:
		for _, c := range m {
			drainCmd(c)
		}
		return
	case tea.Cmd:
		drainCmd(m)
		return
	}
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice {
		return
	}
	for i := range v.Len() {
		if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
			drainCmd(c)
		}
	}
}

// TestStartLoop_OnAnIdleSessionSendsTheFirstContinuation is the other half of
// TestStartLoop_WhileATurnIsRunningOnlyArms: with nothing running there is no
// RunComplete coming to start the loop off, so startLoop has to send the first
// continuation itself or the loop would sit armed and never move.
func TestStartLoop_OnAnIdleSessionSendsTheFirstContinuation(t *testing.T) {
	t.Parallel()

	u, ws := newPlanUI(t, "s1")
	ws.agentReady = true
	u.agentBusyCache.val = false // idle

	drainCmd(u.startLoop())

	require.True(t, u.loop.active, "the loop is armed")
	require.Equal(t, []string{loopPrompt}, ws.runPrompts, "and starts itself straight away")
	require.Equal(t, []bool{true}, ws.runHidden, "hidden, like every other continuation")
}
