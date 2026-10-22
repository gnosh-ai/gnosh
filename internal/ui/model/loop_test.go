package model

import (
	"errors"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func running() loopState {
	return loopState{active: true, sessionID: "s1", turns: 3}
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

	stop, cont = loopDecide(running(), notify.RunComplete{SessionID: "s1", Halted: true})
	require.False(t, cont)
	require.Equal(t, loopStopHalted, stop, "a denied permission must not be answered with the next prompt")
}

func TestLoopDecide_HasNoCapByDefault(t *testing.T) {
	t.Parallel()
	st := running()
	st.turns = 1000
	stop, cont := loopDecide(st, notify.RunComplete{SessionID: "s1", Text: "more to do"})
	require.True(t, cont, "a loop must not give up on a count the user never set")
	require.Empty(t, string(stop))
}

func TestLoopDecide_StopsAtTheUsersCap(t *testing.T) {
	t.Parallel()
	st := running()
	st.maxTurns = 3
	stop, cont := loopDecide(st, notify.RunComplete{SessionID: "s1", Text: "more to do"})
	require.False(t, cont)
	require.Equal(t, loopStopExhausted, stop, "loop_max_turns must still bound a loop when set")

	st.turns = 2
	_, cont = loopDecide(st, notify.RunComplete{SessionID: "s1", Text: "more to do"})
	require.True(t, cont, "and only once that many turns have been sent")
}

func TestLoopDecide_StopMarkerEndsTheLoopOnlyAtACheckpoint(t *testing.T) {
	t.Parallel()
	reply := "The tests need a database this box does not have.\n" + loopStopSentinel

	stop, cont := loopDecide(running(), notify.RunComplete{SessionID: "s1", Text: reply})
	require.True(t, cont, "outside a checkpoint the marker is not an answer to anything")
	require.Empty(t, string(stop))

	st := running()
	st.reflecting = true
	stop, cont = loopDecide(st, notify.RunComplete{SessionID: "s1", Text: reply})
	require.False(t, cont, "asked whether to stop, the agent may say yes")
	require.Equal(t, loopStopJudged, stop)

	stop, cont = loopDecide(st, notify.RunComplete{SessionID: "s1", Text: "No reason to stop; next I fix the parser."})
	require.True(t, cont, "and without the marker it keeps going")
	require.Empty(t, string(stop))
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

	m, ws := newPlanUI(t, "s1")
	ws.sessionBusy = true // this session's turn is already running

	probe := loopStartProbe(t, m.startLoop())

	require.True(t, m.loop.active, "the loop is armed")
	require.Equal(t, "s1", m.loop.sessionID)
	require.Zero(t, m.loop.maxTurns, "no cap unless loop_max_turns sets one")
	require.True(t, probe.busy)
	require.Nil(t, m.applyLoopStartProbe(probe), "the running turn's RunComplete drives the first continuation")
	require.Zero(t, m.loop.turns, "the running turn is the user's, not the loop's")
	require.Empty(t, ws.runPrompts)
}

// loopStartProbe runs startLoop's command and returns the answer of its
// probe of the session, which the event loop would feed back into Update.
func loopStartProbe(t *testing.T, cmd tea.Cmd) loopStartProbeMsg {
	t.Helper()
	require.NotNil(t, cmd)
	batch, ok := cmd().(tea.BatchMsg)
	require.True(t, ok, "startLoop says the loop is on and probes the session")
	for _, c := range batch {
		if c == nil {
			continue
		}
		if probe, ok := c().(loopStartProbeMsg); ok {
			return probe
		}
	}
	t.Fatal("startLoop sent no probe of its session")
	return loopStartProbeMsg{}
}

func TestAdvanceLoop_StopsAndClearsWhenTheModelReportsDone(t *testing.T) {
	t.Parallel()

	m := &UI{loop: loopState{active: true, sessionID: "s1", turns: 5}}
	cmd := m.advanceLoop(notify.RunComplete{SessionID: "s1", Text: "finished\nLOOP_DONE"})

	require.NotNil(t, cmd, "the user is told the loop ended")
	require.False(t, m.loop.active, "a finished loop must not keep driving turns")
}

func TestAdvanceLoop_StopsAndClearsAtTheCap(t *testing.T) {
	t.Parallel()

	m := &UI{loop: loopState{active: true, sessionID: "s1", turns: 4, maxTurns: 4}}
	cmd := m.advanceLoop(notify.RunComplete{SessionID: "s1", Text: "still going"})

	require.NotNil(t, cmd)
	require.False(t, m.loop.active, "the cap must actually stop the loop")
}

func TestAdvanceLoop_LeavesAnotherSessionsLoopAlone(t *testing.T) {
	t.Parallel()

	m := &UI{loop: loopState{active: true, sessionID: "s1", turns: 5}}
	cmd := m.advanceLoop(notify.RunComplete{SessionID: "s2", Text: "LOOP_DONE"})

	require.Nil(t, cmd, "another session's turn says nothing")
	require.True(t, m.loop.active, "and must not disarm this loop")
	require.Equal(t, 5, m.loop.turns)
}

func TestStopLoop_IsQuietWhenNoLoopIsRunning(t *testing.T) {
	t.Parallel()

	m := &UI{}
	require.Nil(t, m.stopLoop("cancelled"), "Esc on an idle session says nothing about loops")

	m.loop = loopState{active: true, sessionID: "s1", turns: 3}
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
	u.loop = loopState{active: true, sessionID: "sess-1", turns: 3}

	cmd := u.advanceLoop(notify.RunComplete{SessionID: "sess-1", Text: "wrote a.txt, still more to do"})
	require.NotNil(t, cmd, "an unfinished turn must drive the next one")
	drainCmd(cmd)

	require.Equal(t, []string{loopPrompt}, ws.runPrompts, "exactly one continuation, and it is the loop's")
	require.Equal(t, []bool{true}, ws.runHidden, "continuations stay in model history, not in the chat")
	require.True(t, u.loop.active, "the loop keeps running")
	require.Equal(t, 4, u.loop.turns, "and counts the turn")
}

// TestAdvanceLoop_AsksAtEachCheckpoint covers what replaced the old iteration
// cap: every loopReflectEvery turns the continuation is the question whether
// there is a genuine reason to stop, and the turn after it is ordinary again.
func TestAdvanceLoop_AsksAtEachCheckpoint(t *testing.T) {
	t.Parallel()

	u, ws := newPlanUI(t, "sess-1")
	ws.agentReady = true
	u.loop = loopState{active: true, sessionID: "sess-1", turns: loopReflectEvery}

	drainCmd(u.advanceLoop(notify.RunComplete{SessionID: "sess-1", Text: "still more to do"}))
	require.Equal(t, []string{loopReflectionPrompt(loopReflectEvery)}, ws.runPrompts,
		"the checkpoint asks instead of just continuing")
	require.Contains(t, ws.runPrompts[0], loopStopSentinel, "and says how to answer yes")
	require.Equal(t, []bool{true}, ws.runHidden)
	require.True(t, u.loop.reflecting)

	drainCmd(u.advanceLoop(notify.RunComplete{SessionID: "sess-1", Text: "No reason to stop. Fixed the parser."}))
	require.Equal(t, loopPrompt, ws.runPrompts[1], "a no means the loop goes on as before")
	require.False(t, u.loop.reflecting, "and the next reply is no longer an answer to the question")
	require.True(t, u.loop.active)
}

// TestAdvanceLoop_EndsWhenTheAgentJudgesItShouldStop pairs with the test
// above: at a checkpoint, a reply carrying StopMarker ends the loop and sends
// nothing further.
func TestAdvanceLoop_EndsWhenTheAgentJudgesItShouldStop(t *testing.T) {
	t.Parallel()

	u, ws := newPlanUI(t, "sess-1")
	ws.agentReady = true
	u.loop = loopState{active: true, sessionID: "sess-1", turns: loopReflectEvery + 1, reflecting: true}

	cmd := u.advanceLoop(notify.RunComplete{SessionID: "sess-1", Text: "This needs your API key.\n" + loopStopSentinel})
	require.NotNil(t, cmd, "the user is told why the loop ended")
	drainCmd(cmd)
	require.Empty(t, ws.runPrompts, "no further turn after the agent judged it should stop")
	require.False(t, u.loop.active)
}

// TestAdvanceLoop_SendsNothingOnceTheModelIsDone pairs with the test above:
// the same setup, but a finished task must drive no further turn.
func TestAdvanceLoop_SendsNothingOnceTheModelIsDone(t *testing.T) {
	t.Parallel()

	u, ws := newPlanUI(t, "sess-1")
	ws.agentReady = true
	u.loop = loopState{active: true, sessionID: "sess-1", turns: 3}

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

	drainCmd(u.applyLoopStartProbe(loopStartProbe(t, u.startLoop())))

	require.True(t, u.loop.active, "the loop is armed")
	require.Equal(t, []string{loopPrompt}, ws.runPrompts, "and starts itself straight away")
	require.Equal(t, []bool{true}, ws.runHidden, "hidden, like every other continuation")
	require.Equal(t, 1, u.loop.turns, "and that first turn counts")
}

// The Stop hook asks its own question at its own checkpoints, with its own
// marker. A yes to that is not an answer to the loop's check-in.
func TestLoopDecide_TheStopHooksMarkerIsNotTheLoopsAnswer(t *testing.T) {
	t.Parallel()
	st := running()
	st.reflecting = true
	stop, cont := loopDecide(st, notify.RunComplete{SessionID: "s1", Text: "Stopping despite the hook.\n" + agent.StopMarker})
	require.True(t, cont, "only LOOP_STOP answers the loop's check-in")
	require.Empty(t, string(stop))
}

// A Stop hook can send the agent back to work after it answered the check-in
// with LOOP_STOP, so the turn's final text is not that answer. The answer is
// watched as the replies arrive and still ends the loop.
func TestNoteLoopReply_KeepsAStopAnswerTheTurnWentOnPast(t *testing.T) {
	t.Parallel()

	m := &UI{loop: loopState{active: true, sessionID: "s1", turns: loopReflectEvery + 1, reflecting: true}}
	answer := message.Message{SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: "Blocked on your API key.\n" + loopStopSentinel},
		message.Finish{Reason: message.FinishReasonEndTurn},
	}}
	m.noteLoopReply(answer)
	require.True(t, m.loop.answeredStop)

	stop, cont := loopDecide(m.loop, notify.RunComplete{SessionID: "s1", Text: "Lint fixed."})
	require.False(t, cont)
	require.Equal(t, loopStopJudged, stop)
}

// Outside a checkpoint turn, or in another session, a reply is not an answer.
func TestNoteLoopReply_OnlyDuringACheckpointInTheLoopsSession(t *testing.T) {
	t.Parallel()

	reply := message.Message{SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: loopStopSentinel},
		message.Finish{Reason: message.FinishReasonEndTurn},
	}}
	m := &UI{loop: loopState{active: true, sessionID: "s1", turns: 3}}
	m.noteLoopReply(reply)
	require.False(t, m.loop.answeredStop, "not a checkpoint turn")

	m.loop.reflecting = true
	reply.SessionID = "s2"
	m.noteLoopReply(reply)
	require.False(t, m.loop.answeredStop, "another session's reply")
}

// Another session's running turn must not hold the loop back: its
// RunComplete does not drive this loop, so waiting for it would leave the
// loop armed forever. Only this session's own turn defers the first send.
func TestStartLoop_AnotherSessionsTurnDoesNotHoldItUp(t *testing.T) {
	t.Parallel()

	u, ws := newPlanUI(t, "s1")
	ws.agentReady = true
	u.agentBusyCache.val = true      // some session is busy, but not this one
	u.pending.sessionID = "s1-stale" // nor is a leftover spinner this session's turn

	drainCmd(u.applyLoopStartProbe(loopStartProbe(t, u.startLoop())))
	require.Equal(t, []string{loopPrompt}, ws.runPrompts, "the loop starts itself in this idle session")
}

// A RunComplete can drive the first continuation before the probe answers;
// the late answer must not send a second one, or the queue would grow by one
// every iteration.
func TestApplyLoopStartProbe_SendsTheFirstTurnOnlyOnce(t *testing.T) {
	t.Parallel()

	u, ws := newPlanUI(t, "s1")
	ws.agentReady = true
	u.loop = loopState{active: true, sessionID: "s1", turns: 1}

	require.Nil(t, u.applyLoopStartProbe(loopStartProbeMsg{sessionID: "s1"}))
	require.Empty(t, ws.runPrompts)
}

// When the agent is not ready, the next turn never starts; the loop must
// not stay armed with nothing running.
func TestLoop_StopsWhenTheAgentIsNotReady(t *testing.T) {
	t.Parallel()

	u, ws := newPlanUI(t, "s1")
	ws.agentReady = false
	u.loop = loopState{active: true, sessionID: "s1", turns: 2}

	cmd := u.advanceLoop(notify.RunComplete{SessionID: "s1", Text: "still going"})
	require.NotNil(t, cmd)
	msg, ok := cmd().(agentRunSubmittedMsg)
	require.True(t, ok, "a send that cannot start is reported like a failed send")
	_, _ = u.Update(msg)
	require.False(t, u.loop.active)
}

// A continuation that fails to start leaves nothing running; the loop must
// not stay armed, or it would take over the user's next prompt.
func TestLoop_StopsWhenTheNextTurnCannotBeSent(t *testing.T) {
	t.Parallel()

	m, _ := newPlanUI(t, "s1")
	m.loop = loopState{active: true, sessionID: "s1", turns: 2}
	_, _ = m.Update(agentRunSubmittedMsg{sessionID: "s1", err: errors.New("provider unreachable")})
	require.False(t, m.loop.active)
}

// loop_max_turns, when set, is what startLoop counts down from.
func TestStartLoop_UsesTheConfiguredCap(t *testing.T) {
	t.Parallel()

	m, ws := newPlanUI(t, "s1")
	seven := 7
	ws.cfg.Options = &config.Options{LoopMaxTurns: &seven}
	m.pending.sessionID = "s1" // arm only; the running turn sends the first

	m.startLoop()
	require.Equal(t, 7, m.loop.maxTurns)
}

// Esc stops a loop that is armed with nothing running, as the start message
// promises; before, Esc reached the loop only through cancelling a turn.
func TestEsc_StopsAnArmedLoopWithNothingRunning(t *testing.T) {
	pinTTLs(t)

	ws := &countingWorkspace{ready: true}
	m := newBusyUI(ws)
	warmCaches(m, false) // idle
	m.loop = loopState{active: true, sessionID: "s1", turns: 2}

	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	require.False(t, m.loop.active)
}

// A reply still streaming is not an answer yet: cut mid-line, "LOOP_STOP would
// be premature" reads as a line saying just LOOP_STOP.
func TestNoteLoopReply_IgnoresAReplyStillStreaming(t *testing.T) {
	t.Parallel()

	m := &UI{loop: loopState{active: true, sessionID: "s1", turns: loopReflectEvery + 1, reflecting: true}}
	m.noteLoopReply(message.Message{SessionID: "s1", Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: "No reason to stop.\n" + loopStopSentinel},
	}})
	require.False(t, m.loop.answeredStop)
}
