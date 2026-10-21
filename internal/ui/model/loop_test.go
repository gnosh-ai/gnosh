package model

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent/notify"
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
