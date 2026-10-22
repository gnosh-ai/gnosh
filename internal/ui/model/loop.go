package model

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/ui/util"
)

const (
	// loopDoneSentinel is what the model is asked to reply when the task is
	// finished. Seeing it in a turn's text ends the loop.
	loopDoneSentinel = "LOOP_DONE"

	// loopPrompt is sent after each turn while the loop runs. It goes out as a
	// hidden continuation: the model sees it in history, but the chat is not
	// filled with one identical prompt per iteration.
	loopPrompt = "Continue working on the task until it is complete. " +
		"When, and only when, the whole task is finished, reply with exactly " + loopDoneSentinel + " on its own line."

	// loopReflectEvery is how many turns a loop runs between checkpoints.
	//
	// There is deliberately no cap by default. A task can genuinely need any
	// number of turns, and ending the loop on a counter would throw away
	// exactly the persistence /loop exists to add. Instead, at each
	// checkpoint the agent itself judges whether there is a real reason to
	// stop; if there is not, it keeps working. Esc always stops it, and
	// loop_max_turns sets a hard cap for a user who wants one.
	loopReflectEvery = 25
)

// loopReflectionPrompt is sent in place of loopPrompt every loopReflectEvery
// turns. It asks the agent to judge whether there is a genuine reason to stop,
// and tells it how to say so; absent such a reason it is told plainly to keep
// going. The marker is the Stop hook's, which the chat already hides.
func loopReflectionPrompt(turns int) string {
	return fmt.Sprintf(
		"This loop has now run %d turns without the task being reported finished. "+
			"Before continuing, step back and judge honestly whether there is a "+
			"genuine reason to stop — for example: the task cannot be done as "+
			"asked; it is blocked on something only the user can provide or "+
			"decide; a check it needs cannot pass in this environment; or you "+
			"are repeating the same attempts without making progress.\n\n"+
			"If there is such a reason, explain it clearly for the user, then end "+
			"your reply with this line on its own:\n%s\n\n"+
			"If the whole task is in fact finished, reply with exactly %s on its own line.\n\n"+
			"Otherwise, do not stop: keep working on the task. Taking many turns is fine.",
		turns, agent.StopMarker, loopDoneSentinel,
	)
}

// loopState is the /loop driver. A turn ending publishes RunComplete, and
// while the loop is active that event sends the next continue prompt, so the
// session keeps working without the user typing "continue" each time.
//
// Only sessionAgent.Run publishes RunComplete, so the loop is driven by real
// turns alone. Auto-summarization in particular does not publish one, which
// is what keeps a compaction in the middle of a long loop from reading as a
// finished turn and spending an iteration on a continuation nobody asked for.
type loopState struct {
	active    bool
	sessionID string
	// turns counts the continue prompts sent so far.
	turns int
	// maxTurns is the user's own cap from loop_max_turns; 0 means none.
	maxTurns int
	// reflecting marks that the latest continue prompt was the checkpoint
	// question: only the reply to it may end the loop with StopMarker.
	reflecting bool
}

// loopStop explains why a running loop ended; the empty string means it is
// still running.
type loopStop string

const (
	loopStopDone      loopStop = "the model reported the task finished"
	loopStopCancelled loopStop = "the turn was cancelled"
	loopStopError     loopStop = "the turn failed"
	loopStopExhausted loopStop = "the loop_max_turns cap was reached"
	loopStopJudged    loopStop = "the agent judged there is a reason to stop; its reply says why"
)

// loopDecide reports whether the loop should send another continue prompt
// after the turn described by rc, and when it should not, why it stopped. A
// loop that is not running, or an event for another session, is not a stop:
// both return ("", false).
func loopDecide(st loopState, rc notify.RunComplete) (loopStop, bool) {
	if !st.active || st.sessionID != rc.SessionID {
		return "", false
	}
	switch {
	case rc.Cancelled:
		return loopStopCancelled, false
	case rc.Error != "":
		return loopStopError, false
	case loopReportedDone(rc.Text):
		return loopStopDone, false
	case st.reflecting && agent.HasStopMarker(rc.Text):
		return loopStopJudged, false
	case st.maxTurns > 0 && st.turns >= st.maxTurns:
		return loopStopExhausted, false
	}
	return "", true
}

// loopReportedDone reports whether a turn's text claims the task is finished.
// The sentinel must stand alone on its own line, as the loop prompt asks:
// matching it anywhere in the text would end the loop on a turn that merely
// talks about it ("I will reply LOOP_DONE when the files exist"), which is
// the opposite of what the user asked for.
func loopReportedDone(text string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(line) == loopDoneSentinel {
			return true
		}
	}
	return false
}

// startLoop begins looping the current session. The first continuation goes
// out now when the session is idle; when a turn is already running, that
// turn's RunComplete sends it instead.
func (m *UI) startLoop() tea.Cmd {
	if !m.hasSession() {
		return util.ReportWarn("Start a session before looping")
	}
	maxTurns := m.com.Config().Options.GetLoopMaxTurns()
	m.loop = loopState{active: true, sessionID: m.session.ID, maxTurns: maxTurns}
	limit := fmt.Sprintf("checking in every %d turns", loopReflectEvery)
	if maxTurns > 0 {
		limit = fmt.Sprintf("max %d turns", maxTurns)
	}
	started := util.ReportInfo(fmt.Sprintf("Loop on: continuing until %s (%s). Esc stops it.", loopDoneSentinel, limit))

	// A turn already running will publish RunComplete, and that drives the
	// next continuation. Sending one here as well would put two in flight
	// for the same turn, and each of those would again be followed by the
	// loop's own, so the queue would grow with every iteration.
	if m.isAgentBusy() {
		return started
	}
	m.loop.turns++
	return tea.Sequence(started, m.sendMessageInternal(loopPrompt, true))
}

// stopLoop clears the loop and says why. It is quiet when no loop was
// running, so Esc on an idle session says nothing about loops.
func (m *UI) stopLoop(reason string) tea.Cmd {
	if !m.loop.active {
		return nil
	}
	m.loop = loopState{}
	return util.ReportInfo("Loop off: " + reason)
}

// advanceLoop runs on every finished turn: it sends the next continue prompt
// while the loop should keep going, and otherwise ends the loop and says why.
// Every loopReflectEvery turns the prompt is the checkpoint question instead.
func (m *UI) advanceLoop(rc notify.RunComplete) tea.Cmd {
	stop, cont := loopDecide(m.loop, rc)
	if cont {
		prompt := loopPrompt
		m.loop.reflecting = m.loop.turns > 0 && m.loop.turns%loopReflectEvery == 0
		if m.loop.reflecting {
			prompt = loopReflectionPrompt(m.loop.turns)
		}
		m.loop.turns++
		return m.sendMessageInternal(prompt, true)
	}
	if stop == "" {
		return nil
	}
	return m.stopLoop(string(stop))
}
