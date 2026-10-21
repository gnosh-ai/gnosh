package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/session"
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

	m := &UI{}
	cmd := m.startLoop()
	require.NotNil(t, cmd, "the user is told why nothing happened")
	require.False(t, m.loop.active, "no session means no loop to run")
}

func TestStartLoop_WhileATurnIsRunningOnlyArms(t *testing.T) {
	t.Parallel()

	m := &UI{}
	m.session = &session.Session{ID: "s1"}
	m.agentBusyCache.val = true // a turn is already running

	cmd := m.startLoop()

	require.NotNil(t, cmd)
	require.True(t, m.loop.active, "the loop is armed")
	require.Equal(t, "s1", m.loop.sessionID)
	require.Equal(t, loopMaxIterations, m.loop.remaining)
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
