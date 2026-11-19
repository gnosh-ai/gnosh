package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Outcome is what the hooks of one event came to, taken together. The
// hooks' answers are combined in the order the hooks are configured, never
// in the order they happened to finish, and the most restrictive answer
// wins: a block over an ask, an ask over an approval, an approval over no
// opinion.
type Outcome struct {
	// Blocked says that a hook objected, or could not evaluate, on an
	// event where that counts:
	//
	//   - PreToolUse: the call must not run. The model is given Reason.
	//     (Two hooks that changed the call's input in different ways
	//     block it too.)
	//   - UserPromptSubmit: the prompt must not be sent. The user is
	//     given Reason, with the prompt back in the editor.
	//   - Stop, SubagentStop: the turn may not end. Reason is the model's
	//     next instruction.
	//   - PostToolUse, PostToolUseFailure: the call has run. Reason goes
	//     to the model as feedback, after the tool's output.
	//   - PreModelSwitch: the model must not be switched. The person is
	//     given Reason.
	//
	// On every other event Blocked is false whatever the hooks said: see
	// Warnings.
	Blocked bool

	// Reason is the hooks' reasons for the answer that won, in the order
	// the hooks are configured, a blank line between them. For a block it
	// is each blocking hook's own words, or, for a hook that could not
	// evaluate, a sentence that names the hook and says what went wrong.
	Reason string

	// CouldNotEvaluate says that at least one hook could not check: it
	// could not be started, ran out of time, was killed, exited with a
	// status that means trouble, or answered in words gnosh cannot read.
	// Such a hook counts as a block wherever a block counts. It must
	// never be shown as a check that passed.
	CouldNotEvaluate bool
	// CutShort says a hook did not finish on its own: it ran out of time,
	// was killed, or printed more than is read. What it would have said
	// is not known, whatever it was for.
	CutShort bool

	// PermissionDecision is the hooks' answer about a tool call.
	//
	// PreToolUse: Deny (Blocked is true), Ask (put the call to the user
	// with Reason, even if it would otherwise run unasked), Allow (run it
	// with no permission question, subject to deny and ask rules) or
	// NoDecision (the normal permission check). The hooks run before any
	// permission check, in every mode.
	//
	// PermissionRequest: Allow, Deny (the model is given Reason and goes
	// on) or NoDecision (show the question).
	//
	// PreModelSwitch: Deny (Blocked is true), Ask (the person is asked
	// whether to switch, with Reason; where nobody can answer, the switch
	// is refused), or Allow and NoDecision (the switch is made).
	PermissionDecision Decision

	// UpdatedInput, when not nil, replaces the tool call's whole input
	// (PreToolUse). The permission check then judges the new input. It is
	// never set on a blocked call.
	UpdatedInput json.RawMessage

	// UpdatedOutput, when not nil, is what the model is to read in place of
	// what the tool returned (PostToolUse's "updatedToolOutput", or
	// "updatedMCPToolOutput"), as the hook wrote it: the caller turns it
	// into the text the model reads. OutputClash says that two hooks gave
	// different ones, so that neither can be trusted to stand in.
	UpdatedOutput json.RawMessage
	OutputClash   bool

	// AdditionalContext is text the hooks want the model to read, each
	// hook's in the order configured, a blank line between them. Where it
	// goes depends on the event: beside the prompt, next to the tool's
	// result, before the session's first prompt. On PreCompact it is
	// extra instructions for the summary. On Stop and SubagentStop Claude
	// Code keeps the turn going with it, as guidance rather than as a
	// block, and so should the agent loop.
	AdditionalContext string

	// EndTurn says that a hook ended the turn ("continue": false, or a
	// PermissionRequest hook's deny with "interrupt": true). It holds
	// whatever the other hooks said, and the turn's Stop hooks are not
	// consulted afterwards. StopReason is what to show the user.
	EndTurn    bool
	StopReason string

	// SystemMessages are warnings the hooks asked gnosh to show the user
	// ("systemMessage"). They block nothing.
	SystemMessages []string

	// ElicitAction is the hooks' answer to an MCP server's request
	// (Elicitation), or what they put in place of the person's answer
	// (ElicitationResult): "accept", with ElicitContent, the values as a
	// JSON object, or nil when the hooks gave none (the person's stand);
	// "decline" (a hook declined or blocked, or could not
	// evaluate, and Blocked is true; or two hooks answered with different
	// values); "cancel"; or "", which leaves the form to the person
	// (Elicitation) or lets their answer go as it is (ElicitationResult).
	// A decline from one hook wins over the others' answers, and a cancel
	// over an accept.
	ElicitAction  string
	ElicitContent json.RawMessage

	// WatchPaths are the files to watch for FileChanged from now on, in
	// place of the ones hooks named before ("watchPaths", from SessionStart,
	// CwdChanged and FileChanged hooks): every hook's, in the order
	// configured. WatchSet says a hook gave the list; an empty one clears
	// it.
	WatchPaths []string
	WatchSet   bool

	// Warnings are sentences of gnosh's own for the user about hooks that
	// had something to say where nothing can be blocked: a hook that
	// objected or could not evaluate on an event that cannot block, and a
	// PermissionRequest hook that did not answer. On the events where
	// Blocked can be true, the same news is in Reason.
	Warnings []string

	// Records has one entry for every hook that was run, in the order
	// configured, so that the interface can show each. It is empty when
	// no hook matched.
	Records []Record
}

// Result says in a word what one hook did.
type Result string

const (
	// ResultNone: the hook ran and had nothing to say.
	ResultNone Result = "none"
	// ResultApproved: the hook approved the tool call, and its approval
	// counted.
	ResultApproved Result = "approved"
	// ResultAsked: the hook put the tool call to the user.
	ResultAsked Result = "asked"
	// ResultBlocked: the hook stopped the call, the prompt or the end of
	// the turn, or refused a permission request.
	ResultBlocked Result = "blocked"
	// ResultFeedback: the hook objected to a tool call that had already
	// run, and its reason went to the model.
	ResultFeedback Result = "feedback"
	// ResultWarning: the hook showed the user a warning, or objected on
	// an event where nothing can be stopped.
	ResultWarning Result = "warning"
	// ResultEndedTurn: the hook ended the turn.
	ResultEndedTurn Result = "ended_turn"
	// ResultCouldNotEvaluate: the hook could not check. This is never
	// "fine".
	ResultCouldNotEvaluate Result = "could_not_evaluate"
	// ResultCancelled: the turn was cancelled while the hook ran, and the
	// hook was stopped.
	ResultCancelled Result = "cancelled"
	// ResultBackground: the hook was started in the background, and
	// nothing waited for it ("async", "asyncRewake").
	ResultBackground Result = "background"
	// ResultAnswered: the hook answered an MCP server's request in the
	// person's place, or put another answer in place of theirs.
	ResultAnswered Result = "answered"
)

// Record is what one hook did on one occurrence of an event.
type Record struct {
	// Event, Matcher, Command, Name and Source are the hook as it is
	// configured.
	Event   Event
	Matcher string
	Command string
	Name    string
	Source  string

	// GuardOnly says that the hook was not written for this tool: it ran
	// because it guards a tool that does the same job, so it could block
	// but not approve.
	GuardOnly bool

	// Duration is how long the hook ran.
	Duration time.Duration
	// ExitCode is the hook's exit status, or -1 when it has none: it
	// never started, was killed, or was stopped by gnosh.
	ExitCode int

	// Result is what the hook did, and Reason says more: the hook's own
	// words for a block, an ask or an approval; what went wrong for a
	// hook that could not evaluate.
	Result Result
	Reason string

	// SystemMessage is the warning the hook asked to have shown.
	SystemMessage string
	// AdditionalContext is the text the hook added for the model.
	AdditionalContext string
	// Notes says, in plain sentences, what else there is to know: what
	// the hook asked for that gnosh does not do, or why an approval it
	// gave did not count.
	Notes []string

	// Stderr is the start of what the hook wrote to standard error.
	Stderr string

	// Background says that the hook ran in the background: it decided
	// nothing.
	Background bool
}

// String returns the record as one line, for a log or a status line.
func (r Record) String() string {
	who := "`" + shorten(r.Command, 60) + "`"
	if r.Name != "" {
		who = quote(r.Name)
	}
	what := map[Result]string{
		ResultNone:             "had nothing to say",
		ResultApproved:         "approved",
		ResultAsked:            "asked for the user's say",
		ResultBlocked:          "blocked",
		ResultFeedback:         "gave feedback",
		ResultWarning:          "warned",
		ResultEndedTurn:        "ended the turn",
		ResultCouldNotEvaluate: "could not evaluate",
		ResultCancelled:        "was cancelled",
		ResultAnswered:         "answered the MCP server",
	}[r.Result]
	if r.Result == ResultBackground {
		return fmt.Sprintf("%s hook %s runs in the background", r.Event, who)
	}
	s := fmt.Sprintf("%s hook %s %s (%s)", r.Event, who, what, r.Duration.Round(time.Millisecond))
	if r.Background {
		s = fmt.Sprintf("%s hook %s, in the background, %s (%s)", r.Event, who, what, r.Duration.Round(time.Millisecond))
	}
	if detail := r.Reason; detail != "" {
		s += ": " + shorten(detail, 200)
	} else if r.SystemMessage != "" {
		s += ": " + shorten(r.SystemMessage, 200)
	}
	return s
}

// stderrStart is how many characters of a hook's stderr a Record keeps.
const stderrStart = 2000

// verdict is what one hook's run comes to, once its exit status and what
// it printed have been read together.
type verdict struct {
	// cancelled: the turn was cancelled while the hook ran. Nothing else
	// is set.
	cancelled bool
	// failure says what went wrong when the hook could not evaluate; it
	// is empty when the hook could.
	failure string
	// cutShort says the hook did not finish on its own: it ran out of
	// time, was killed, or printed more than is read.
	cutShort bool
	// words is what the hook itself said about its answer: the reason it
	// gave, or what it wrote to stderr.
	words string
	// refused, asked and approved are the hook's answer.
	refused  bool
	asked    bool
	approved bool
	// halt says the hook ended the turn; stopReason is its text for the
	// user.
	halt       bool
	stopReason string
	// context, warning and input are the hook's added text for the
	// model, its warning for the user and its changed tool input.
	context string
	warning string
	input   json.RawMessage
	// watch is the hook's watchPaths, and watchSet says it gave them.
	watch    []string
	watchSet bool
	// output is the hook's replacement for the tool's output.
	output json.RawMessage
	// action and content are the hook's answer to an MCP server's request
	// (accept, with the values, or cancel).
	action  string
	content json.RawMessage
	// notes says what else there is to know.
	notes []string
}

// say adds to the hook's own words.
func (v *verdict) say(text string) {
	text = strings.TrimSpace(text)
	switch {
	case text == "" || strings.Contains(v.words, text):
	case v.words == "":
		v.words = text
	default:
		v.words += "\n" + text
	}
}

// judge reads one hook's run: first how it ended, then what it printed.
//
// The exit status means:
//
//	0                    it ran; what it printed is read
//	2                    block, with stderr as the reason
//	3                    it could not evaluate
//	126, 127, above 128  it could not start, or a command in it was not
//	                     found, or was killed: it could not evaluate
//	any other            with text on stderr: it ran into trouble and
//	                     could not evaluate. Silent: a test that came out
//	                     false, which is no opinion.
//
// A hook that never started, ran out of time, was killed, printed past
// the limit or left a process holding its output could not evaluate
// either.
//
// Whatever the status, a JSON object the hook printed is read, and can
// only make the answer stricter: a refusal in it holds even beside a
// field that cannot be read, while an approval counts only from a hook
// that exited 0 and whose whole answer was understood.
func (r *Runner) judge(event Event, info eventInfo, h selected, p proc) verdict {
	var v verdict
	stderr := clip(strings.TrimSpace(p.stderr), r.maxText)
	switch {
	case p.cancelled:
		v.cancelled = true
		return v
	case p.startErr != nil && h.Server != "":
		v.failure = "its MCP tool could not be called (" + p.startErr.Error() + ")"
		return v
	case p.startErr != nil && h.URL != "":
		v.failure = "its server could not be reached (" + p.startErr.Error() + ")"
		return v
	case p.startErr != nil:
		v.failure = "it could not be started (" + startError(p.startErr) + ")"
		return v
	case p.timedOut:
		v.failure, v.cutShort = "it did not finish within "+spell(p.limit)+" and was stopped", true
	case p.signal != "":
		v.failure, v.cutShort = "it was killed by "+p.signal, true
	case p.unknown != "":
		v.failure = "gnosh could not tell what it answered, because " + p.unknown
	case p.exit < 0:
		v.failure = "it ended without an exit status"
	case p.lost > 0:
		v.failure, v.cutShort = "it printed more than the "+size(r.maxOutput)+" gnosh reads from a hook", true
	case p.leftOpen:
		v.failure, v.cutShort = "it ended but left a process running that still holds its output open, so what it printed may not be all it has to say. A hook must redirect the output of anything it leaves running", true
	}
	if v.failure != "" {
		// What it printed is of no use as an answer.
		v.say(stderr)
		return v
	}

	switch c := p.exit; {
	case c == 0:
	case c == 2 && info.class == answers:
		v.notes = append(v.notes, "it exited with status 2, which is not an answer to a permission question; to refuse, a hook answers \"deny\" in JSON")
	case c == 2:
		v.refused = true
		v.say(stderr)
	case c == 3:
		v.failure = "it exited with status 3, which says that it could not evaluate"
		v.say(stderr)
	case c == 126:
		v.failure = "it exited with status 126: its command was found but could not be run (is the file executable?)"
		v.say(stderr)
	case c == 127:
		v.failure = "it exited with status 127: a command it needs was not found"
		v.say(stderr)
	case c > 128:
		v.failure, v.cutShort = fmt.Sprintf("it exited with status %d, which means a command in it was killed (signal %d)", c, c-128), true
		v.say(stderr)
	case stderr != "":
		v.failure = fmt.Sprintf("it exited with status %d and reported trouble", c)
		v.say(stderr)
	}
	if !info.reads {
		return v
	}

	out := strings.TrimSpace(p.stdout)
	if !looksLikeObject(out) {
		switch {
		case strayAnswer(out):
			// To take the text for a log line and drop the answer in it
			// would be to drop a hook's "deny" without a word.
			mixed := "it printed other text along with its JSON answer, so the answer could not be read. A hook's JSON answer must be the only thing it prints"
			if v.failure == "" {
				v.failure = mixed
			} else {
				v.notes = append(v.notes, mixed)
			}
		case p.exit == 0 && info.plain:
			v.context = clip(out, r.maxText)
		}
		return v
	}
	a := parseAnswer(event, info, []byte(out))
	if len(a.errs) > 0 {
		unread := "its answer could not be read: " + strings.Join(a.errs, "; ")
		if v.failure == "" {
			v.failure = unread
		} else {
			v.notes = append(v.notes, unread)
		}
	}
	v.notes = append(v.notes, a.notes...)
	v.warning = clip(a.warning, r.maxText)
	v.context = clip(a.context, r.maxText)
	if len(a.errs) == 0 {
		v.watch, v.watchSet = a.watch, a.watchSet
	}
	if a.output != nil && event == PostToolUse {
		// (Even from a hook that could not be read in full: a hook that
		// meant to hide what a tool returned is not ignored for a slip
		// elsewhere in its answer. The caller decides what to show.)
		v.output = a.output
	}
	reason := clip(a.reason, r.maxText)
	switch {
	case !a.halt:
	case info.halts:
		v.halt = true
		v.stopReason = clip(a.stopReason, r.maxText)
	default:
		v.notes = append(v.notes, fmt.Sprintf("it said \"continue\": false, which a %s hook cannot use to end the turn", event))
	}
	if a.refuse {
		v.refused = true
		v.say(reason)
		if a.interrupt {
			v.halt = true
			v.stopReason = reason
		}
	}
	if a.ask && !v.refused {
		v.asked = true
		v.say(reason)
	}
	if a.input != nil && event == PreToolUse {
		if h.asBash {
			// (As in Claude Code: the hook saw Monitor's command as a Bash
			// call, and what it wrote is a Bash call's input.)
			v.notes = append(v.notes, fmt.Sprintf("its changed input was not used: it is written for %s and ran here only as a guard of the command Monitor starts", quote(h.Matcher)))
		} else {
			v.input = a.input
		}
	}
	if a.action != "" {
		switch {
		case v.refused || v.failure != "":
			// A block stands, and a hook that could not evaluate answers
			// nothing.
		case p.exit != 0:
			v.notes = append(v.notes, fmt.Sprintf("its answer was not used, because it exited with status %d: an answer counts only from a hook that exits 0", p.exit))
		default:
			// (An accept with no "content" stays without: on
			// ElicitationResult it keeps the person's values.)
			v.action, v.content = a.action, a.content
		}
	}
	if a.approve {
		switch {
		case v.refused || v.failure != "":
			// A block stands whatever the JSON says, and a hook that
			// could not evaluate approves nothing.
		case p.exit != 0:
			v.notes = append(v.notes, fmt.Sprintf("its approval was not used, because it exited with status %d: an approval counts only from a hook that exits 0", p.exit))
		case a.changes:
			v.notes = append(v.notes, "it allowed the call with a changed input or changed permissions, which gnosh does not apply; the question is shown instead, for the call as it stands")
		case h.guardOnly:
			v.notes = append(v.notes, fmt.Sprintf("its approval was not used: it is written for %s and ran here only as a guard, so what it checked was another tool's input. To approve this tool, name it in the matcher", quote(h.Matcher)))
		default:
			v.approved = true
			v.say(reason)
		}
	}
	return v
}

// combine puts the hooks' verdicts together, in the order the hooks are
// configured.
func (r *Runner) combine(in *Input, info eventInfo, sel []selected, procs []proc) Outcome {
	event := in.Event
	verdicts := make([]verdict, len(sel))
	for i := range sel {
		verdicts[i] = r.judge(event, info, sel[i], procs[i])
	}

	// A changed tool input is in force only when the hooks that changed
	// it agree. Each hook saw the call as the model wrote it, so two
	// different rewrites cannot both be applied, and to pick one would
	// drop the other's without a word.
	var (
		input   json.RawMessage
		changer string
		clash   string
	)
	if event == PreToolUse {
		first := -1
		for i, v := range verdicts {
			if v.input == nil || v.failure != "" || v.refused || v.cancelled {
				continue
			}
			switch {
			case input == nil:
				input, changer, first = v.input, sel[i].label(), i
			case !bytes.Equal(input, v.input) && clash == "":
				clash = sel[i].label()
				verdicts[i].notes = append(verdicts[i].notes, fmt.Sprintf("its changed input was not used: the hook %s changed the input another way, so the call was blocked", changer))
				verdicts[first].notes = append(verdicts[first].notes, fmt.Sprintf("its changed input was not used: the hook %s changed the input another way, so the call was blocked", clash))
			}
		}
		// An approval is for the input the approving hook saw, or the one
		// it wrote itself. It does not carry over to another hook's
		// rewrite.
		for i := range verdicts {
			if v := &verdicts[i]; v.approved && input != nil && !bytes.Equal(v.input, input) {
				v.approved = false
				v.notes = append(v.notes, fmt.Sprintf("its approval was not used: it approved the call as the model wrote it, and the hook %s changed the call's input", changer))
			}
		}
	}

	var (
		out                    Outcome
		blocks, asks, approves []string
		stops, contexts        []string
		answerer, otherAnswer  string // (two Elicitation hooks' different values)
		accepted               json.RawMessage
		cancelled              bool
		unanswered             bool // (a hook that answers could not check)
	)
	for i, v := range verdicts {
		h, p := sel[i], procs[i]
		rec := Record{
			Event:             h.Event,
			Matcher:           h.Matcher,
			Command:           h.CommandLine(),
			Name:              h.Name,
			Source:            h.Source,
			GuardOnly:         h.guardOnly,
			Duration:          p.took,
			ExitCode:          p.exit,
			Result:            ResultNone,
			SystemMessage:     v.warning,
			AdditionalContext: v.context,
			Notes:             v.notes,
			Stderr:            start(strings.TrimSpace(p.stderr), stderrStart),
		}
		if p.unstopped {
			rec.Notes = append(rec.Notes, "gnosh stopped it but could not see all of its processes go")
		}
		if v.cancelled {
			rec.Result = ResultCancelled
			out.Records = append(out.Records, rec)
			continue
		}

		if v.warning != "" {
			rec.Result = ResultWarning
			out.SystemMessages = append(out.SystemMessages, v.warning)
		}
		if v.output != nil {
			switch {
			case out.UpdatedOutput == nil:
				out.UpdatedOutput = v.output
			case !bytes.Equal(out.UpdatedOutput, v.output):
				out.OutputClash = true
			}
		}
		if v.watchSet && v.failure == "" {
			out.WatchSet = true
			for _, p := range v.watch {
				if !slices.Contains(out.WatchPaths, p) {
					out.WatchPaths = append(out.WatchPaths, p)
				}
			}
		}
		if v.context != "" {
			contexts = append(contexts, v.context)
		}
		switch v.action {
		case "cancel":
			rec.Result = ResultAnswered
			cancelled = true
		case "accept":
			rec.Result = ResultAnswered
			switch {
			case answerer == "":
				accepted, answerer = v.content, h.label()
			case !bytes.Equal(accepted, v.content) && otherAnswer == "":
				otherAnswer = h.label()
			}
		}
		switch {
		case v.approved:
			rec.Result, rec.Reason = ResultApproved, v.words
			approves = append(approves, v.words)
		case v.asked:
			rec.Result, rec.Reason = ResultAsked, v.words
			asks = append(asks, v.words)
		}

		said := ""
		if v.words != "" {
			said = "\nThe hook said: " + v.words
		}
		switch {
		case v.failure != "":
			out.CouldNotEvaluate = true
			out.CutShort = out.CutShort || v.cutShort
			rec.Result = ResultCouldNotEvaluate
			rec.Reason = v.failure + "." + said
			switch info.class {
			case gate, feedback:
				out.Blocked = true
				blocks = append(blocks, fmt.Sprintf("The %s hook %s could not check this: %s. A check that could not run does not pass.%s", event, h.label(), v.failure, said))
			case answers:
				if v.refused {
					blocks = append(blocks, orElse(v.words, fmt.Sprintf("The %s hook %s refused this and gave no reason.", event, h.label())))
					out.Warnings = append(out.Warnings, fmt.Sprintf("The %s hook %s refused the call in an answer gnosh could not read in full: %s. Its refusal holds.", event, h.label(), v.failure))
				} else {
					// A hook that could not check answers nothing, and
					// another hook's approval does not stand for it: the
					// question goes to the person.
					unanswered = true
					out.Warnings = append(out.Warnings, fmt.Sprintf("The %s hook %s did not answer the permission question: %s. The question goes to the user instead.%s", event, h.label(), v.failure, said))
				}
			case warns:
				out.Warnings = append(out.Warnings, fmt.Sprintf("The %s hook %s could not be evaluated: %s. A %s hook cannot stop anything, so gnosh went on without it.%s", event, h.label(), v.failure, event, said))
			}
		case v.refused:
			rec.Reason = v.words
			switch info.class {
			case gate, answers:
				rec.Result = ResultBlocked
				if info.class == gate {
					out.Blocked = true
				}
				blocks = append(blocks, orElse(v.words, fmt.Sprintf("The %s hook %s blocked this and gave no reason.", event, h.label())))
			case feedback:
				rec.Result = ResultFeedback
				out.Blocked = true
				blocks = append(blocks, orElse(v.words, fmt.Sprintf("The %s hook %s objected and gave no reason.", event, h.label())))
			case warns, records:
				rec.Result = ResultWarning
				if info.class == warns || event == SessionEnd {
					out.Warnings = append(out.Warnings, fmt.Sprintf("The %s hook %s objected. A %s hook cannot stop anything, so this is only a warning.%s", event, h.label(), event, said))
				}
			}
		case info.class == answers && !v.approved && len(v.notes) > 0:
			out.Warnings = append(out.Warnings, fmt.Sprintf("The %s hook %s did not answer the permission question: %s. The question goes to the user instead.", event, h.label(), strings.Join(v.notes, "; ")))
		}
		if v.halt {
			out.EndTurn = true
			if rec.Result != ResultCouldNotEvaluate {
				rec.Result = ResultEndedTurn
			}
			if rec.Reason == "" {
				rec.Reason = v.stopReason
			}
			stops = append(stops, orElse(v.stopReason, fmt.Sprintf("The %s hook %s ended the turn and gave no reason.", event, h.label())))
		}
		out.Records = append(out.Records, rec)
	}

	if clash != "" {
		out.Blocked = true
		blocks = append(blocks, fmt.Sprintf("The PreToolUse hooks %s and %s each changed the call's input, in different ways. gnosh cannot apply both, and a call whose hooks do not agree on what to run is not run. Have one hook make the change.", changer, clash))
	}
	if otherAnswer != "" {
		out.Blocked = true
		blocks = append(blocks, fmt.Sprintf("The %s hooks %s and %s answered the MCP server with different values. gnosh cannot send both, so the request was declined. Have one hook answer.", event, answerer, otherAnswer))
	}
	if event == Elicitation || event == ElicitationResult {
		switch {
		case out.Blocked:
			out.ElicitAction = "decline"
		case cancelled:
			out.ElicitAction = "cancel"
		case answerer != "":
			out.ElicitAction, out.ElicitContent = "accept", accepted
		}
	}
	out.AdditionalContext = join(contexts)
	out.StopReason = join(stops)

	refused := len(blocks) > 0
	switch {
	case info.class == answers && refused:
		out.PermissionDecision, out.Reason = Deny, join(blocks)
	case info.class == answers && unanswered:
	case info.class == answers && len(approves) > 0:
		out.PermissionDecision, out.Reason = Allow, join(approves)
	case info.class == answers:
	case out.Blocked:
		out.Reason = join(blocks)
		if event == PreToolUse || event == PreModelSwitch {
			out.PermissionDecision = Deny
		}
	case event != PreToolUse && event != PreModelSwitch:
	case len(asks) > 0:
		out.PermissionDecision, out.Reason = Ask, join(asks)
		out.UpdatedInput = input
	case len(approves) > 0:
		out.PermissionDecision, out.Reason = Allow, join(approves)
		out.UpdatedInput = input
	default:
		out.UpdatedInput = input
	}
	return out
}

// orElse returns s, or fallback when s is empty.
func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
