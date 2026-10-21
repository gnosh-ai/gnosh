package model

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

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
)

// loopState is the /loop driver. A turn ending publishes RunComplete, and
// while the loop is active that event sends the next continue prompt, so the
// session keeps working without the user typing "continue" each time.
type loopState struct {
	active    bool
	sessionID string
	remaining int
}

// loopStop explains why a running loop ended; the empty string means it is
// still running.
type loopStop string

const (
	loopStopDone      loopStop = "the model reported the task finished"
	loopStopCancelled loopStop = "the turn was cancelled"
	loopStopError     loopStop = "the turn failed"
	loopStopExhausted loopStop = "the iteration cap was reached"
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
	case st.remaining <= 0:
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
	m.loop = loopState{active: true, sessionID: m.session.ID, remaining: maxTurns}
	started := util.ReportInfo(fmt.Sprintf("Loop on: continuing until %s (max %d turns). Esc stops it.", loopDoneSentinel, maxTurns))

	// A turn already running will publish RunComplete, and that drives the
	// next continuation. Sending one here as well would put two in flight
	// for the same turn, and each of those would again be followed by the
	// loop's own, so the queue would grow with every iteration.
	if m.isAgentBusy() {
		return started
	}
	return tea.Sequence(started, m.sendMessageInternal(loopPrompt, true))
}

// stopLoop clears the loop. It is quiet when no loop was running, so Esc on
// an idle session says nothing.
func (m *UI) stopLoop(reason string) tea.Cmd {
	if !m.loop.active {
		return nil
	}
	m.loop = loopState{}
	if reason == "" {
		return nil
	}
	return util.ReportInfo("Loop off: " + reason)
}

// advanceLoop runs on every finished turn: it sends the next continue prompt
// while the loop should keep going, and otherwise ends the loop and says why.
func (m *UI) advanceLoop(rc notify.RunComplete) tea.Cmd {
	stop, cont := loopDecide(m.loop, rc)
	if cont {
		m.loop.remaining--
		return m.sendMessageInternal(loopPrompt, true)
	}
	if stop == "" {
		return nil
	}
	return m.stopLoop(string(stop))
}
