package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Texts are the loop's own words: what it tells the model when it speaks
// for itself. Every field has a default, so the zero value works; the
// package that owns gnosh's wording may replace any of them.
//
// Every field is a format for fmt.Sprintf, also the ones that are given
// nothing: a per cent sign in the words is written twice. A field that is
// given values says which, in the order given.
//
// The loop puts the marker (Options.Mark) around the texts that stand on
// the user's side of the conversation: the nudges, the checkpoint, the
// check that could not run, the request for a summary, the lines before
// and after a summary, the note where a summary request was cut, the
// hook that could not run, and /loop's lines. The texts that answer a
// tool call are results, and are not marked.
type Texts struct {
	// EmptyNudge is sent when the model answered with nothing.
	EmptyNudge string
	// TextCallNudge is sent when the model's reply is a tool call written
	// out as text, and TextCallNotice tells the person at every third in a
	// row.
	TextCallNudge  string
	TextCallNotice string
	// EmptyNotice tells the person about it at every third empty reply in
	// a row. It is given the count.
	EmptyNotice string

	// RepeatNudge tells the model that it keeps making the same call. It
	// is given how many of the last rounds were the same, how many rounds
	// were looked at, and the call with its result.
	RepeatNudge string
	// RepeatPair is the same for two calls taking turns. It is given the
	// number of rounds looked at and the two calls.
	RepeatPair string

	// Checkpoint asks the model to judge whether there is a real reason to
	// stop. It is given the marker and how many times in a row the turn
	// was held open. What holds the turn open follows it, in the driver's
	// own words.
	Checkpoint string
	// DriverFailed sends the model back when a turn-end check could not
	// run. It is given the check's name and the error.
	DriverFailed string

	// StoppedBefore answers a call that never started because the person
	// stopped the turn, and StoppedDuring one that was running then.
	StoppedBefore string
	StoppedDuring string
	// HaltedBefore answers a call that never started because the turn was
	// ended, and is given the reason. HaltedDuring answers one that was
	// running then.
	HaltedBefore string
	HaltedDuring string
	// ReplyCut answers a call whose reply was cut off before it finished.
	ReplyCut string
	// Killed answers a call that an earlier process left without a result.
	Killed string
	// DidNotStop answers a call that was still running when the loop gave
	// up waiting for it to stop. It is given how long the loop waited.
	DidNotStop string
	// AsideRunning answers, in a side question about the conversation
	// (SideRequest.Session), a call of the last reply that is still
	// running.
	AsideRunning string

	// UnknownTool answers a call to a tool that does not exist. It is
	// given the name and the list of tools.
	UnknownTool string
	// BadInput answers a call whose arguments are not a JSON object. It is
	// given the start of what arrived.
	BadInput string
	// DuplicateKey answers a call whose arguments name a key twice. It is
	// given the key.
	DuplicateKey string
	// BadDescribe answers a call that its tool found unable to run as
	// asked. It is given the tool's error.
	BadDescribe string
	// ToolFailed answers a call the harness could not run, and ToolCrashed
	// one whose tool crashed. Each is given the error.
	ToolFailed  string
	ToolCrashed string
	// GateFailed answers a call whose permission check could not be made.
	// It is given the error.
	GateFailed string
	// Denied answers a refused call for which the gate gave no reason, and
	// Unattended one that was refused because nobody could be asked.
	Denied     string
	Unattended string
	// HookFailed tells the model that a check that runs after tool calls
	// could not run. It is given the error.
	HookFailed string

	// SummaryRequest asks the model for a summary of the conversation.
	// SummaryExtra follows it on a line of its own when the person gave
	// instructions for the summary; it is given them.
	SummaryRequest string
	SummaryExtra   string
	// SummaryIntro stands just before a stored summary, which follows it
	// outside the marker, being the model's own text. SummaryAfter comes
	// after the summary and tells the model to carry on.
	SummaryIntro string
	SummaryAfter string
	// TrimNote stands where part of a conversation was left out of a
	// summary request because it would not fit: after its first message,
	// or at its start.
	TrimNote string

	// LoopContinue is /loop's "keep going" prompt. LoopQuestion questions
	// a first claim of being done. LoopCheckin is the check-in sent every
	// LoopEvery turns; it is given the stop word and the number of turns.
	LoopContinue string
	LoopQuestion string
	LoopCheckin  string
}

// The words /loop reads in a reply. Each counts only alone on a line.
const (
	// LoopDone is how a model in a /loop says the work is finished.
	LoopDone = "LOOP_DONE"
	// LoopStop is how a model answers a /loop check-in to stop the loop.
	LoopStop = "LOOP_STOP"
)

// defaultTexts is what the loop says when nobody gave it other words.
var defaultTexts = Texts{
	EmptyNudge: "Your last reply was empty: it held no text and no tool call. " +
		"If there is more to do, go on and do it now. If you are done, say what you did or found.",
	TextCallNudge: "Your last reply held a tool call written out as text, which runs nothing. " +
		"To use a tool, call it; otherwise answer in words.",
	TextCallNotice: "The model has written a tool call out as text, which runs nothing, %d times in a row. gnosh keeps telling it so; Esc stops the turn.",
	EmptyNotice: "The model has answered with nothing %d times in a row. gnosh keeps asking. " +
		"A lower thinking level usually fixes this (/no_think, or pick a level in /model); Esc stops the turn.",

	RepeatNudge: "You have made the same call in %d of your last %d rounds, with the same result each time:\n\n%s\n\n" +
		"Making it again will tell you nothing new. Answer with what you have, or take a different approach.",
	RepeatPair: "Your last %d rounds only went back and forth between the same calls, with the same results each time:\n\n%s\n\n" +
		"Making them again will tell you nothing new. Answer with what you have, or take a different approach.",

	Checkpoint: "Decide this first, honestly, before anything else: is there a genuine reason to stop here? " +
		"A genuine reason is one of these: the task cannot be done as asked; you are blocked on something only the user can give or decide; " +
		"the check cannot pass in this environment; you are repeating yourself without getting anywhere.\n\n" +
		"If there is one, say what it is in a few sentences and end your reply with this marker on a line of its own:\n\n%s\n\n" +
		"If there is none, do not write the marker: carry on with the work.\n\n" +
		"This turn has now been held open %d times in a row. What holds it open follows.",
	DriverFailed: "The check %q could not run: %v. A check that could not run has not passed, so the turn goes on. " +
		"Carry on with the work, and if this check cannot run here, say so.",

	StoppedBefore: "This call did not run: the user stopped the turn first.",
	StoppedDuring: "The user stopped the turn while this call was running. It may have partly run; check before relying on it.",
	HaltedBefore:  "This call did not run: the turn was ended first (%s).",
	HaltedDuring:  "The turn was ended while this call was running. It may have partly run; check before relying on it.",
	ReplyCut:      "This call did not run: the reply that asked for it was cut off before it was complete.",
	Killed:        "gnosh was stopped before this call finished. It may have partly run; check before relying on it.",
	AsideRunning:  "Still running: nobody interrupted this call. No result yet.",
	DidNotStop: "This call was told to stop when the turn ended, and %s later it still had not. " +
		"gnosh went on without it; it may still be running, and it may have partly run.",

	UnknownTool: "There is no tool named %q, so nothing ran. The tools you have are: %s.",
	BadInput: "This call did not run: its arguments are not a complete JSON object. They may have been cut off. " +
		"What arrived begins:\n\n%s\n\nSend the call again with complete arguments.",
	DuplicateKey: "This call did not run: its arguments name the key %q twice, so it is not clear which value is meant. " +
		"Send the call again with each key once.",
	BadDescribe: "This call did not run: %v.",
	ToolFailed:  "gnosh could not run this call: %v.",
	ToolCrashed: "The tool crashed while it ran this call (%v). It may have partly run; check before relying on it.",
	GateFailed: "This call did not run: the permission check for it could not be made (%v), " +
		"and a check that could not be made has not passed.",
	Denied: "This call did not run: permission for it was refused.",
	Unattended: "This call did not run: it needs a person's approval, and nobody is here to give it. " +
		"Do the work another way if there is one, or say what you could not do.",
	HookFailed: "A check that runs after tool calls could not run (%v), so this call's result was not checked.",

	SummaryRequest: "The conversation above has grown too long to go on with as it is. Write a summary of it that will stand in its place: " +
		"you will carry on the work from the summary alone, so leave out nothing you will need.\n\n" +
		"Write these sections, in this order:\n\n" +
		"1. What the user asked for: every message of the user's, in their own words where they are short, and what they meant by each.\n" +
		"2. What was done: the steps taken and what came of each, with the names of the files read and changed and the commands run.\n" +
		"3. What was found: facts about the project, decisions made and why, errors met and how each was fixed.\n" +
		"4. Where things stand: what is finished, what is half done, what was found or planned and not yet done, and anything running in the background.\n" +
		"5. The next step: what you were about to do, quoting the user's own words for it.\n\n" +
		"Call no tools. Answer with the summary and nothing else.",
	SummaryExtra: "The user asks this of the summary: %s",
	SummaryIntro: "The earlier part of this conversation ran out of room in your context and was replaced by the summary of it that follows.",
	SummaryAfter: "Carry on with the task from where the summary leaves it. Keep every stop the user set: where they " +
		"asked to be told or asked before a step, stop there and do that first. Do not ask the user to repeat anything, " +
		"and otherwise do not ask whether to go on.",
	TrimNote: "Part of the conversation is left out here, because the whole of it no longer fits.",

	LoopContinue: "Keep going with the task. When all of it is finished and checked, reply with " + LoopDone + " alone on a line.",
	LoopQuestion: "You said the task is done. Before that is believed: name what you produced, and show the listing or the output that proves each part of it. " +
		"If it is all there, reply with " + LoopDone + " alone on a line again. If something is missing, go back and do it.",
	LoopCheckin: "Decide this first, honestly, before anything else: is there a genuine reason to stop working on this task? " +
		"A genuine reason is one of these: it cannot be done as asked; you are blocked on something only the user can give or decide; " +
		"you are repeating yourself without getting anywhere.\n\n" +
		"If there is one, say what it is in a few sentences and end your reply with %s alone on a line. " +
		"If there is none, carry on with the task. (This is turn %d of the loop.)",
}

// fill puts the default in every field of t that is empty.
func (t Texts) fill() Texts {
	d := defaultTexts
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&d.EmptyNudge, t.EmptyNudge)
	set(&d.TextCallNudge, t.TextCallNudge)
	set(&d.EmptyNotice, t.EmptyNotice)
	set(&d.RepeatNudge, t.RepeatNudge)
	set(&d.RepeatPair, t.RepeatPair)
	set(&d.Checkpoint, t.Checkpoint)
	set(&d.DriverFailed, t.DriverFailed)
	set(&d.StoppedBefore, t.StoppedBefore)
	set(&d.StoppedDuring, t.StoppedDuring)
	set(&d.HaltedBefore, t.HaltedBefore)
	set(&d.HaltedDuring, t.HaltedDuring)
	set(&d.ReplyCut, t.ReplyCut)
	set(&d.Killed, t.Killed)
	set(&d.AsideRunning, t.AsideRunning)
	set(&d.DidNotStop, t.DidNotStop)
	set(&d.UnknownTool, t.UnknownTool)
	set(&d.BadInput, t.BadInput)
	set(&d.DuplicateKey, t.DuplicateKey)
	set(&d.BadDescribe, t.BadDescribe)
	set(&d.ToolFailed, t.ToolFailed)
	set(&d.ToolCrashed, t.ToolCrashed)
	set(&d.GateFailed, t.GateFailed)
	set(&d.Denied, t.Denied)
	set(&d.Unattended, t.Unattended)
	set(&d.HookFailed, t.HookFailed)
	set(&d.SummaryRequest, t.SummaryRequest)
	set(&d.SummaryExtra, t.SummaryExtra)
	set(&d.SummaryIntro, t.SummaryIntro)
	set(&d.SummaryAfter, t.SummaryAfter)
	set(&d.TrimNote, t.TrimNote)
	set(&d.LoopContinue, t.LoopContinue)
	set(&d.LoopQuestion, t.LoopQuestion)
	set(&d.LoopCheckin, t.LoopCheckin)

	// The texts that are given nothing are read as the formats they are,
	// once, here; the loop then uses them as they stand.
	for _, p := range []*string{
		&d.EmptyNudge, &d.TextCallNudge, &d.StoppedBefore, &d.StoppedDuring, &d.HaltedDuring, &d.ReplyCut, &d.Killed, &d.AsideRunning,
		&d.Denied, &d.Unattended, &d.SummaryRequest, &d.SummaryIntro, &d.SummaryAfter,
		&d.TrimNote, &d.LoopContinue, &d.LoopQuestion,
	} {
		*p = plain(*p)
	}
	return d
}

// The marker gnosh puts around its own words on the user's side of a
// conversation, so that a model can tell the harness speaking from the
// person typing.
const (
	markOpen  = "<gnosh>"
	markClose = "</gnosh>"
)

// markBreak is the one character that makes a marker inside quoted text
// harmless: a zero-width space, put right after the marker's "<". It takes
// no room on a screen and a model reads past it.
const markBreak = "\u200b"

// defaultMark wraps s in gnosh's marker. It is what the loop uses when the
// wiring gave it no Mark of its own. What s quotes (a call's arguments, a
// tool's output) can neither end the marker early nor start a second one:
// a marker inside s is broken up, in whatever letter case it is written
// and with or without blanks inside its brackets.
func defaultMark(s string) string {
	return markOpen + "\n" + breakMarkers(s) + "\n" + markClose
}

// breakMarkers puts a markBreak after every "<" of s that opens gnosh's
// marker or closes it. Text that holds no marker comes back as it was.
func breakMarkers(s string) string {
	var b strings.Builder
	kept := 0 // s[:kept] has been written to b
	for i := 0; ; {
		at := strings.IndexByte(s[i:], '<')
		if at < 0 {
			break
		}
		i += at + 1
		if !opensMarker(s[i:]) {
			continue
		}
		b.WriteString(s[kept:i])
		b.WriteString(markBreak)
		kept = i
	}
	if kept == 0 {
		return s
	}
	b.WriteString(s[kept:])
	return b.String()
}

// opensMarker reports whether rest, the text that follows a "<", goes on
// as gnosh's marker: blanks, an optional "/", blanks, the marker's name in
// any letter case, blanks, and the closing ">".
func opensMarker(rest string) bool {
	name := markOpen[1 : len(markOpen)-1]
	i := skipBlanks(rest, 0)
	if i < len(rest) && rest[i] == '/' {
		i = skipBlanks(rest, i+1)
	}
	if len(rest)-i <= len(name) || !strings.EqualFold(rest[i:i+len(name)], name) {
		return false
	}
	i = skipBlanks(rest, i+len(name))
	return i < len(rest) && rest[i] == '>'
}

// skipBlanks returns the index of the first byte of s at or after i that
// is not a space, a tab or a line end.
func skipBlanks(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// plain reads a text that is given nothing as the format it is: a per
// cent sign written twice is one.
func plain(format string) string {
	return strings.ReplaceAll(format, "%%", "%")
}

// clip cuts s to at most n bytes on a rune boundary and says so when it
// did.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf(" … (%d more bytes)", len(s)-cut)
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// oneLine puts s on one line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// span writes a duration the way a person says it: 800ms, 5s, 1m30s, 5m,
// 1h.
func span(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	d = d.Round(time.Second)
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	s := (d % time.Minute) / time.Second
	var b strings.Builder
	if h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if s > 0 || b.Len() == 0 {
		fmt.Fprintf(&b, "%ds", s)
	}
	return b.String()
}

// tokens writes a token count the way a person reads it: 850, 44K, 1.2M.
func tokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1000000:
		return fmt.Sprintf("%dK", (n+500)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1e6)
}

// exact writes a number in full, with commas: 32,768.
func exact(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// sortedNames returns names sorted, joined by commas.
func sortedNames(names []string) string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return strings.Join(out, ", ")
}
