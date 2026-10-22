package model

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/util"
)

const (
	// loopDoneSentinel is what the model is asked to reply when the task is
	// finished. Seeing it in a turn's text ends the loop.
	loopDoneSentinel = "LOOP_DONE"

	// loopStopSentinel is the answer to a check-in that there is a genuine
	// reason to stop. It is the loop's own, not the Stop hook's marker: a
	// Stop hook asks its own question at its own checkpoints, and a yes to
	// that must not be read as a yes to the loop's.
	loopStopSentinel = "LOOP_STOP"

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
// turns. It asks the agent to decide, before anything else, whether there is a
// genuine reason to stop, and to answer in one of three set ways; absent such
// a reason it is told plainly to keep going.
//
// The wording is measured, not guessed. Replaying a real checkpoint request
// against the box's Qwen, with thinking off as crash sends it, an earlier
// "step back and judge" version was mostly ignored: the model answered with
// the next step of the task (1 stop in 5 where the user had said to stop).
// Saying that the question overrides the system prompt's keep-working rules,
// and asking for the decision first, stopped it 4 or 5 times in 5 (with this
// sentinel, and with the Stop hook's marker), and on a task that was still
// making progress it stopped 0 times in 5 and carried on.
func loopReflectionPrompt(turns int) string {
	return fmt.Sprintf(
		"Checkpoint: this loop has run %d turns and the task is not reported finished. "+
			"The user set up this loop and asked to be told at these checkpoints when "+
			"going on is pointless; this question takes precedence over any general "+
			"instruction to keep working, and stopping here for a genuine reason is "+
			"not a failure. Before doing anything else, decide whether there is a "+
			"genuine reason to stop. Genuine reasons include: the user's request says "+
			"to stop at this point; the task cannot be done as asked; it is blocked on "+
			"something only the user can provide or decide; a check it needs cannot "+
			"pass in this environment; or you are repeating the same attempts without "+
			"making progress.\n\n"+
			"Then reply in exactly one of these three ways:\n"+
			"1. There is a genuine reason to stop: explain it for the user, and make "+
			"the last line of your reply exactly:\n%s\n"+
			"2. The whole task is finished: reply with exactly %s on its own line.\n"+
			"3. Neither: start your reply with \"No reason to stop.\" and carry on "+
			"with the task. Taking many turns is fine.",
		turns, loopStopSentinel, loopDoneSentinel,
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
	// question: only the reply to it may end the loop with loopStopSentinel.
	reflecting bool
	// answeredStop records that a reply during the checkpoint turn said
	// loopStopSentinel. The turn's final text may not be that reply: a
	// Stop hook can send the agent back to work after it, and a prompt
	// typed meanwhile is answered after it, so the replies are watched as
	// they arrive (noteLoopReply).
	answeredStop bool
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
	case st.reflecting && (st.answeredStop || loopReportedStop(rc.Text)):
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
	return hasSentinelLine(text, loopDoneSentinel)
}

// loopReportedStop reports whether a reply answers a check-in with
// loopStopSentinel, on its own line like the done sentinel.
func loopReportedStop(text string) bool {
	return hasSentinelLine(text, loopStopSentinel)
}

// hasSentinelLine reports whether sentinel stands alone on a line of text. A
// line wrapped in inline-code backticks still counts, since some models add
// them.
func hasSentinelLine(text, sentinel string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "`")) == sentinel {
			return true
		}
	}
	return false
}

// noteLoopReply watches the replies of a checkpoint turn for the stop
// answer, so it counts even when the turn goes on past it (see
// loopState.answeredStop).
func (m *UI) noteLoopReply(msg message.Message) {
	if !m.loop.active || !m.loop.reflecting || m.loop.answeredStop {
		return
	}
	if msg.SessionID == m.loop.sessionID && msg.Role == message.Assistant && loopReportedStop(msg.Content().Text) {
		m.loop.answeredStop = true
	}
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
	// loop's own, so the queue would grow with every iteration. Only this
	// session's turn counts: another session's RunComplete does not drive
	// this loop, so waiting on it would leave the loop armed forever.
	if m.turnInFlight(m.session.ID) {
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
		m.loop.answeredStop = false
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
