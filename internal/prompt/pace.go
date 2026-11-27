package prompt

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PaceInput is what a pace check is shown (Set.PaceCheck): a look beside
// the work while a turn runs, at whether it is going about its task the
// time-efficient way.
type PaceInput struct {
	// Ask is the prompt the turn works on, and Todo the mem todo in hand,
	// when there is one.
	Ask, Todo string
	// Elapsed is how long the turn has run.
	Elapsed time.Duration
	// Steps are the turn's finished steps, oldest first, and LeftOut how
	// many earlier ones are not among them.
	Steps   []PaceStep
	LeftOut int
	// Now says what the agent does at this moment.
	Now string
	// Before is what earlier looks of the turn said, oldest first.
	Before []string
}

// PaceStep is one finished step of a turn.
type PaceStep struct {
	// At is when it began, counted from the turn's start, and Took how
	// long it took.
	At, Took time.Duration
	// What is the step (a tool, and what it was given), and Outcome how
	// it ended.
	What, Outcome string
}

// PaceCheck returns the request of a pace check. Its answer is one line of
// JSON: {"pace": "ok" | "faster" | "stuck", "why": …, "next": seconds}.
func (s *Set) PaceCheck(in PaceInput) SideRequest {
	var steps []string
	for _, st := range in.Steps {
		line := PaceClock(st.At) + " " + oneLine(st.What, maxQuote) + " (" + PaceClock(st.Took) + ")"
		if out := oneLine(st.Outcome, maxQuote); out != "" {
			line += ": " + out
		}
		steps = append(steps, line)
	}
	ask := clipChars(strings.TrimSpace(in.Ask), 2000)
	if ask == "" {
		ask = "(not known)"
	}
	now := oneLine(in.Now, maxQuote)
	if now == "" {
		now = "(between steps)"
	}
	vals := values{
		"ask":     ask,
		"todo":    oneLine(in.Todo, maxTitle),
		"elapsed": PaceClock(in.Elapsed),
		"steps":   strings.Join(steps, "\n"),
		"now":     now,
		"before":  strings.Join(in.Before, "\n"),
	}
	vals.flag("no_steps", len(steps) == 0)
	if in.LeftOut > 0 {
		vals["left_out"] = strconv.Itoa(in.LeftOut)
	}
	return SideRequest{
		System: s.fill(textPaceCheck, nil),
		Prompt: s.fill(textPaceRequest, vals),
	}
}

// PaceNoteInput is what the model is told when a pace check found the work
// stuck or saw a faster way (Set.PaceNote).
type PaceNoteInput struct {
	// Elapsed is how long the turn had run, Stuck says the check found it
	// stuck (otherwise it saw a faster way), and Why is what it saw.
	Elapsed time.Duration
	Stuck   bool
	Why     string
	// Command is a command the check found stuck, which now runs in the
	// background.
	Command string
}

// PaceNote returns the note the model reads with its next request after a
// pace check found the work stuck or saw a faster way: gnosh's own look,
// to weigh for a moment and no more.
func (s *Set) PaceNote(in PaceNoteInput) string {
	vals := values{
		"elapsed": PaceClock(in.Elapsed),
		"why":     strings.TrimRight(strings.TrimSpace(oneLine(in.Why, maxQuote)), ". "),
	}
	vals.flag("stuck", in.Stuck)
	vals.flag("faster", !in.Stuck)
	if in.Command != "" {
		vals["command"] = oneLine(in.Command, maxQuote)
	}
	return s.fill(textPaceNote, vals)
}

// PaceClock writes a span of time the way a clock on the wall would:
// "0:45", "3:05", "1:02:30".
func PaceClock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Round(time.Second) / time.Second)
	if sec >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}
