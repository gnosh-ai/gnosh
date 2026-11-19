package app

import (
	"fmt"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
)

// backgroundEnded is told by the shell manager that a background command
// has ended, and passes the news to the model through the loop's one way
// in for such things: a note. A running turn gets it before its next
// request; an idle session starts a turn for it, with one quiet line for
// the person saying why. Each end is announced once: the shell manager
// sees to that. Nothing is announced once the session has begun to close.
//
// It is called on a goroutine of the shell manager's and must not block:
// Tell only puts the note in line.
func (l *live) backgroundEnded(e shelltool.Ended) {
	a := l.agent
	if a == nil || l.closing.Load() || l.bgUntold.Load() {
		return
	}
	l.noteBackground()
	end := prompt.BackgroundEnd{
		ID:      e.ID,
		Command: firstOf(e.Description, firstLine(e.Command)),
		Output:  e.OutputFile,
		Monitor: e.Monitor,
	}
	switch {
	case e.Reason == shelltool.ReasonExited && e.ExitCode == 0:
		end.State = prompt.BackgroundFinished
	case e.Reason == shelltool.ReasonExited:
		end.State, end.Code = prompt.BackgroundFailed, e.ExitCode
	default:
		// Its time ran out, it printed too much, or it was stopped.
		end.State = prompt.BackgroundStopped
	}
	if e.Owner != "" {
		// A sub-agent started it. While the sub-agent works, the news is
		// its own, as it was told it would be.
		if sub := l.runningSub(e.Owner); sub != nil && sub.Tell(agent.Note{Text: l.texts.BackgroundEnded(end), Source: "task-notification"}) == nil {
			return
		}
		// It has ended: the session's model is told, and what the
		// sub-agent wrote of the command (its description, its first
		// line) is not given in gnosh's words. The id and the output file
		// say the rest.
		end.Command = "a command a sub-agent of this session started; the sub-agent has ended"
	}
	_ = a.Tell(agent.Note{
		Text:   l.texts.BackgroundEnded(end),
		Source: "task-notification",
		Notice: "A background command ended: " + e.Summary,
		Wake:   true,
	})
}

// runningSub returns the agent of the sub-agent with this session id while
// it runs, or nil.
func (l *live) runningSub(id string) *agent.Agent {
	l.subMu.Lock()
	defer l.subMu.Unlock()
	for s := range l.subs {
		if s.id == id && s.agent != nil {
			return s.agent
		}
	}
	return nil
}

// monitorLines passes on what the command of a Monitor call printed, in a
// note that wakes an idle session, as the end of a background command
// does. While a note the model has not read yet waits in line (the agent
// has something queued), the lines are not taken: the shell manager keeps
// them, with what comes after, and offers them again a moment later. A
// model that is slow to read gets one note with many lines, not a pile of
// notes. The last lines of a command that ended are always taken, so that
// they come before the news of its end.
//
// It is called on a goroutine of the shell manager's and must not block.
func (l *live) monitorLines(w shelltool.Watched) bool {
	a := l.agent
	if a == nil || l.closing.Load() || l.bgUntold.Load() {
		return true
	}
	if !w.Last && a.State().Queued > 0 {
		return false
	}
	what := firstOf(w.Description, "a command")
	lines := countWords(len(w.Lines)+w.Left, "line", "lines")
	_ = a.Tell(agent.Note{
		Text: l.texts.MonitorLines(prompt.MonitorNews{ID: w.ID, Description: w.Description, Lines: w.Lines,
			Left: w.Left, Output: w.OutputFile, Last: w.Last}),
		Source: "monitor",
		Notice: fmt.Sprintf("Monitor %s passed on %s: %s", w.ID, lines, what),
		Wake:   true,
	})
	return true
}
