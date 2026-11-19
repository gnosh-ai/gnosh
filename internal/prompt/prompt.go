package prompt

import (
	"embed"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

// builtinFiles holds gnosh's own texts, one file each, as they were when
// the program was built.
//
//go:embed texts/*.md
var builtinFiles embed.FS

// Kind says where a text goes, which decides how it is marked.
type Kind string

const (
	// KindSystem is a part of the system prompt. It is sent as it is.
	KindSystem Kind = "system"
	// KindLine is text on the user's side of the conversation. It is sent
	// between Open and Close.
	KindLine Kind = "line"
	// KindResult is text that stands inside a tool result: a note in
	// square brackets, or the words of a refusal. It carries no marker,
	// because nothing inside a tool result is an instruction.
	KindResult Kind = "result"
	// KindSide is part of a small request gnosh makes for itself, to a
	// model that sees nothing else: a judge, a namer.
	KindSide Kind = "side"
)

// The names of the texts. Each is the name of a file under texts, without
// its ".md".
const (
	textCore             = "core"
	textCoreAnyRole      = "core-any-role"
	textStyleProactive   = "style-proactive"
	textStyleConcise     = "style-concise"
	textStyleExplain     = "style-explanatory"
	textStyleLearning    = "style-learning"
	textSecurity         = "security"
	textNoTools          = "no-tools"
	textLanguage         = "language"
	textAttribution      = "attribution"
	textMemory           = "memory"
	textMemoryFull       = "memory-full"
	textGno              = "gno"
	textDeliver          = "deliver"
	textEnvironment      = "environment"
	textMemLine          = "mem-line"
	textFrameLine        = "frame-line"
	textSlide            = "slide"
	textLargeStep        = "large-step"
	textMemReminder      = "mem-reminder"
	textRepeat           = "repeat"
	textRepeatClose      = "repeat-refused-close"
	textEmptyReply       = "empty-reply"
	textTextCall         = "text-call"
	textStopHook         = "stop-hook"
	textContinuation     = "continuation"
	textCheckFailed      = "check-could-not-run"
	textCheckpoint       = "checkpoint"
	textLoop             = "loop"
	textLoopProve        = "loop-prove"
	textLoopCheckIn      = "loop-check-in"
	textCloseSendBack    = "close-sendback"
	textGoalNotMet       = "goal-not-met"
	textInterrupted      = "interrupted"
	textBackgroundEnded  = "background-ended"
	textAgentEnded       = "agent-ended"
	textSubtaskEnded     = "subtask-ended"
	textMonitorLines     = "monitor-lines"
	textBtw              = "btw"
	textRecap            = "recap"
	textNextPrompt       = "next-prompt"
	textThinkingBudget   = "thinking-budget"
	textPrintWait        = "print-wait"
	textPrintWrapUp      = "print-wrap-up"
	textPrintLeftovers   = "print-leftovers"
	textStatusLine       = "statusline"
	textMention          = "mention"
	textAgentMention     = "agent-mention"
	textStructured       = "structured-output"
	textAfterSummary     = "after-summary"
	textEarlierLeftOut   = "earlier-left-out"
	textFilesBack        = "files-back"
	textSummary          = "summary"
	textSummaryExtra     = "summary-extra"
	textSummaryIntro     = "summary-intro"
	textPopNote          = "pop-note"
	textSlideNote        = "slide-note"
	textPopReceipt       = "pop-receipt"
	textPlanGnovm        = "plan-gnovm"
	textMissingResult    = "missing-result"
	textReportCaution    = "report-caution"
	textAgentWorkTree    = "agent-work-tree"
	textCloseEnteredHere = "close-entered-here"
	textCloseUnbacked    = "close-unbacked"
	textCloseNotMade     = "close-not-made"
	textCloseRefused     = "close-review-refused"
	textCloseCancelled   = "close-cancelled"
	textCloseReview      = "close-review"
	textCloseRequest     = "close-review-request"
	textCloseCommand     = "close-review-command"
	textGoalJudge        = "goal-judge"
	textGoalRequest      = "goal-judge-request"
	textNamer            = "namer"
	textTitle            = "title"
)

// A spec is what gnosh knows about one of its texts before reading it.
type spec struct {
	name string
	kind Kind
	// need lists the names a text must print: a replacement that drops
	// one would leave out something the model has to be told.
	need []string
	// may lists the other names the text may use.
	may []string
	// refusal marks the refusal of a close. The first thing such a text
	// puts in backquotes must be the todo, because that is how the rest
	// of gnosh knows which todo a refusal is about (RefusedClose).
	refusal bool
	// when says, in a line, when the text is sent.
	when string
}

// specs lists every text, in the order they are shown to a person.
func specs() []spec {
	return []spec{
		{name: textCore, kind: KindSystem,
			when: "The first part of every system prompt: the same bytes in every session, project and machine."},
		{name: textSecurity, kind: KindSystem,
			when: "The last lines of that first part: security work is helped, not refused. A file of its own, so that it can be replaced alone."},
		{name: textCoreAnyRole, kind: KindSystem,
			when: "In place of the core's first line, the coding agent's role, under a custom output style that does not keep the coding instructions (keep-coding-instructions)."},
		{name: textStyleProactive, kind: KindSystem,
			when: "After the core, under the built-in output style Proactive: start at once, assume the routine, check in before harm."},
		{name: textStyleConcise, kind: KindSystem,
			when: "After the core, under the built-in output style Concise: the result first, nothing around it."},
		{name: textStyleExplain, kind: KindSystem,
			when: "After the core, under the built-in output style Explanatory: Insight blocks on the choices made."},
		{name: textStyleLearning, kind: KindSystem,
			when: "After the core, under the built-in output style Learning: Insights, and a piece of the code left to the user (TODO(human))."},
		{name: textLanguage, kind: KindSystem, need: []string{"language"},
			when: "After the core and the output style, when the person set the language replies are in (Claude Code's language)."},
		{name: textAttribution, kind: KindSystem, may: []string{"commit", "pr"},
			when: "After those, when a settings file says what the model signs its commits and pull requests with (Claude Code's attribution); gnosh adds none of its own."},
		{name: textNoTools, kind: KindSystem,
			when: "A part of the system prompt only when the session has no tools at all."},
		{name: textMemory, kind: KindSystem,
			when: "A part of the system prompt only when the session has the gnovm_run tool: how to use mem and the Gno machines."},
		{name: textMemoryFull, kind: KindSystem,
			when: "Sent in place of memory when the system prompt is built with MemoryFull: the same, with every item the mem spec lists, where memory leaves some to mem's own receipts."},
		{name: textGno, kind: KindSystem, need: []string{"interrealm", "security"},
			when: "A part of the system prompt only in a project that holds Gno code: new Gno code gets an audit todo, against the Gno project's interrealm and security guides, which gnosh writes to a folder of its own (gnodocs)."},
		{name: textDeliver, kind: KindSystem,
			when: "A part of the system prompt only when the session has the Deliver tool: name a deliverable before making it."},
		{name: textAgentWorkTree, kind: KindSystem, need: []string{"tree", "repo"},
			when: "Last in the system prompt of a sub-agent that works in a git work tree of its own (isolation worktree): where the work tree is, and that its changes stay there."},

		{name: textEnvironment, kind: KindLine, need: []string{"date", "cwd"},
			may:  []string{"weekday", "user", "machine", "user_only", "machine_only", "platform", "branch", "repo", "nogit", "status", "unread", "why", "dirs", "hosts"},
			when: "With a session's first prompt, ahead of its text: the date, the directory, the machine, the state of git."},
		{name: textMemLine, kind: KindLine,
			when: "Last on every prompt a person types, in a session that has gnovm_run."},
		{name: textFrameLine, kind: KindLine, need: []string{"id"}, may: []string{"purpose", "stack", "more", "slides"},
			when: "After the mem line, while the session stands in a todo. Its first sentence also ends the mem reminder."},
		{name: textSlide, kind: KindLine, need: []string{"show"}, may: []string{"frame", "before", "files"},
			when: "After the oldest rounds were taken out of the view to make room: inside the next frame line, or as a note of its own before the next reply."},
		{name: textLargeStep, kind: KindLine, need: []string{"id", "tokens"},
			when: "Once per todo, before the next reply, when much has come in since the todo was entered."},
		{name: textMemReminder, kind: KindLine, may: []string{"frame", "todos"},
			when: "Before the next reply, after ten replies with no mem call."},

		{name: textRepeat, kind: KindLine, may: []string{"calls"},
			when: "When the last rounds made the same one or two calls over and over. The turn goes on."},
		{name: textRepeatClose, kind: KindLine, need: []string{"refusal"},
			when: "When the call being repeated is a close that mem or the check refuses."},
		{name: textEmptyReply, kind: KindLine,
			when: "When a reply ended with no text and no tool call."},
		{name: textTextCall, kind: KindLine,
			when: "When a reply calls no tool and its text is a tool call written out in a chat template's markup, which the model's server did not take as a call."},
		{name: textStopHook, kind: KindLine, need: []string{"reason"},
			when: "When a Stop hook refuses to let the turn end, carrying the hook's reason."},
		{name: textContinuation, kind: KindLine, need: []string{"reason"},
			when: "When anything else sends the model back to work as a turn would end, carrying the reason."},
		{name: textCheckFailed, kind: KindLine, need: []string{"name", "error"},
			when: "When a check that runs as a turn would end could not run: a check that could not run has not passed."},
		{name: textCheckpoint, kind: KindLine, need: []string{"marker"}, may: []string{"count", "objection"},
			when: "In place of the usual send-back, every so many in a row: is there a real reason to stop?"},
		{name: textLoop, kind: KindLine, need: []string{"marker"},
			when: "As each turn ends while /loop is on: keep going, and how to say it is done."},
		{name: textLoopProve, kind: KindLine, need: []string{"marker"},
			when: "When a reply under /loop first says it is done: show it."},
		{name: textLoopCheckIn, kind: KindLine, need: []string{"marker"}, may: []string{"turns"},
			when: "Every 25 turns under /loop, in place of the usual line: is there a real reason to stop?"},
		{name: textCloseSendBack, kind: KindLine, need: []string{"todo"}, may: []string{"undone", "refusal"},
			when: "Once per turn, when the turn would end in a todo whose last close was refused."},
		{name: textGoalNotMet, kind: KindLine, need: []string{"name", "condition", "reason"},
			when: "When the goal judge finds a closed goal or task not met."},

		{name: textInterrupted, kind: KindLine, may: []string{"user", "exit"},
			when: "Where a turn was stopped before it finished, by the person or because gnosh itself was stopped."},
		{name: textBackgroundEnded, kind: KindLine, need: []string{"command"},
			may:  []string{"id", "monitor", "finished", "failed", "stopped", "not_run", "why", "output"},
			when: "When a background command ends, between rounds or as a turn of its own."},
		{name: textMonitorLines, kind: KindLine, need: []string{"id"}, may: []string{"description", "last", "left", "output", "lines"},
			when: "When a command that Monitor watches has printed lines, between rounds or as a turn of its own."},
		{name: textAgentEnded, kind: KindLine, need: []string{"description", "id"},
			may:  []string{"finished", "failed", "stopped", "why", "caution", "report", "output"},
			when: "When a sub-agent the model started in the background ends, between rounds or as a turn of its own."},
		{name: textSubtaskEnded, kind: KindLine, need: []string{"task", "id"},
			may:  []string{"finished", "failed", "stopped", "why", "report"},
			when: "When a copy of the conversation the person set to work in the background with /subtask ends, between rounds or as a turn of its own."},
		{name: textBtw, kind: KindLine,
			when: "Before a side question the person asks with /btw, in a request beside the session's that is not kept."},
		{name: textRecap, kind: KindLine,
			when: "As the question of /recap, in a request beside the session's that is not kept."},
		{name: textThinkingBudget, kind: KindLine,
			when: "At the end of the model's thinking when its thinking budget runs out, where the server can put it (llama.cpp's reasoning_budget_message)."},
		{name: textNextPrompt, kind: KindLine,
			when: "After a turn, with promptSuggestionEnabled on, in a request beside the session's that is not kept: the person's likely next prompt, which the box shows."},
		{name: textStatusLine, kind: KindLine, need: []string{"wish", "script", "settings", "sample"},
			when: "As the prompt of /statusline: write a status line script from the user's words, try it on the session's own JSON, and add it to their gnosh.json."},
		{name: textPrintWait, kind: KindLine, need: []string{"waited", "work"},
			when: "In a run (-p) that has waited long for background work it started, as a turn of its own: stop that work, or wait on."},
		{name: textPrintWrapUp, kind: KindLine, need: []string{"used", "budget"},
			when: "Once, in a run (-p) with GNOSH_PRINT_MAX_DURATION_MS, when most of that time is used: finish, check quickly, and end the turn."},
		{name: textPrintLeftovers, kind: KindLine, need: []string{"left"},
			when: "Once, as a run (-p) is about to end with processes its commands left running, which the end stops, as a turn of its own: keep one past the run with setsid, or end the turn."},
		{name: textStructured, kind: KindLine, need: []string{"schema"}, may: []string{"problem"},
			when: "After a run's turn with --json-schema, beside the session's request: the result as JSON that conforms to the schema, again with what was wrong."},
		{name: textAgentMention, kind: KindLine, need: []string{"name"},
			when: "After a prompt a person typed, for each kind of sub-agent it names with @agent-: use that kind."},
		{name: textMention, kind: KindLine, need: []string{"path"}, may: []string{"file", "dir", "denied", "failed", "why", "resource"},
			when: "After a prompt a person typed, for each file, directory or MCP resource it mentions with @: what Read returns follows it, a listing, or what the resource holds."},
		{name: textSummary, kind: KindLine, need: []string{"words"}, may: []string{"mem"},
			when: "At the end of the session's own request, when the conversation must be replaced by a summary."},
		{name: textSummaryExtra, kind: KindLine, need: []string{"instructions"},
			when: "After the request for a summary, when the person gave instructions for it with /compact."},
		{name: textEarlierLeftOut, kind: KindLine,
			when: "At the start of a summary request whose earliest turns had to be left out to fit."},
		{name: textSummaryIntro, kind: KindLine,
			when: "Just before a summary, where it stands in place of the conversation it replaced."},
		{name: textAfterSummary, kind: KindLine, may: []string{"path"},
			when: "Just after that summary: carry on, keeping every stop the user set."},
		{name: textFilesBack, kind: KindLine, need: []string{"path"}, may: []string{"shown", "named"},
			when: "After a summary, for each file the model read or changed last that is given back: what Read returns for it follows; and once, the ones that are not given back."},

		{name: textPopNote, kind: KindResult, may: []string{"messages", "one_message", "calls", "one_call", "tools"},
			when: "Appended to the answer of the call that entered a todo, once the todo is popped: what came between was removed."},
		{name: textSlideNote, kind: KindResult, may: []string{"messages", "one_message", "calls", "one_call", "tools"},
			when: "Appended to the answer the view keeps, where rounds after it slid out to make room."},
		{name: textPopReceipt, kind: KindResult, need: []string{"id"},
			may:  []string{"nothing", "spared", "one_thought", "thoughts", "files", "changes", "reads"},
			when: "In the result of the call that pops a todo, after mem's own lines: what the pop removed from the context, and what that changed."},
		{name: textPlanGnovm, kind: KindResult, need: []string{"tool"},
			when: "As the answer to a gnovm tool that runs or changes something, called while the session is in plan mode."},
		{name: textReportCaution, kind: KindResult, may: []string{"marked", "bypass"},
			when: "In front of a sub-agent's report that holds text written like a harness's notes or a conversation's turns, or names a way to turn permission checks off."},
		{name: textMissingResult, kind: KindResult,
			may:  []string{"not_started", "stopped", "ended", "why", "cut_off", "killed", "still", "waited", "running"},
			when: "As the result of a tool call that never ran or never returned, because the turn was stopped or gnosh was."},
		{name: textCloseEnteredHere, kind: KindResult, refusal: true, need: []string{"todo"},
			when: "The refusal when one call entered a todo and closed it with nothing done."},
		{name: textCloseUnbacked, kind: KindResult, refusal: true, need: []string{"todo", "file"}, may: []string{"popped"},
			when: "The refusal when a result says a file changed and nothing in the todo changed or showed it."},
		{name: textCloseNotMade, kind: KindResult, refusal: true, need: []string{"todo", "file"},
			when: "The refusal when a result says a file was made and there is no such file."},
		{name: textCloseRefused, kind: KindResult, refusal: true, need: []string{"todo", "reason"}, may: []string{"popped", "closed"},
			when: "The refusal when the review of a close finds something missing."},
		{name: textCloseCancelled, kind: KindResult, refusal: true, need: []string{"todo"},
			when: "The refusal when the turn was stopped while a close was being reviewed."},

		{name: textCloseReview, kind: KindSide,
			when: "The instructions of the request that reviews a close, to the small model."},
		{name: textCloseRequest, kind: KindSide,
			need: []string{"todo", "thoughts", "ask", "files", "commands", "result", "code", "transcript"},
			may:  []string{"no_thoughts", "no_files", "no_commands", "no_result", "no_transcript", "more_files", "left_out"},
			when: "What that request shows the reviewer, ending in the question."},
		{name: textCloseCommand, kind: KindSide, need: []string{"line"}, may: []string{"exit", "not_run"},
			when: "One command of the reviewer's list, with how it ended."},
		{name: textGoalJudge, kind: KindSide,
			when: "The instructions of the request that judges a goal or a closed task."},
		{name: textGoalRequest, kind: KindSide, need: []string{"condition", "mem", "conversation"},
			may:  []string{"no_mem", "no_conversation"},
			when: "What that request shows the judge."},
		{name: textNamer, kind: KindSide,
			when: "The instructions of the request that names new todos, thoughts and facts."},
		{name: textTitle, kind: KindSide,
			when: "The instructions of the request that gives a session its title, after its first turn."},
	}
}

// Set is the texts in force: gnosh's own, with a person's replacements
// where there are any. A Set does not change once made, so one Set can be
// used from many goroutines.
type Set struct {
	order []string
	texts map[string]*text
}

// A text is one of the Set's texts, ready to be filled in.
type text struct {
	spec     spec
	body     string
	tmpl     template
	replaced bool
	path     string
	stale    bool
}

// Builtin returns gnosh's own texts, as they were built into the program.
func Builtin() *Set {
	s, err := builtinSet()
	if err != nil {
		// The texts are part of the program. A broken one is a broken
		// build, which the package's first test catches.
		panic("prompt: " + err.Error())
	}
	return s
}

// builtinSet reads and checks every built-in text.
func builtinSet() (*Set, error) {
	list := specs()
	s := &Set{texts: make(map[string]*text, len(list))}
	for _, sp := range list {
		body, err := builtinBody(sp.name)
		if err != nil {
			return nil, err
		}
		tmpl, err := sp.check(body)
		if err != nil {
			return nil, fmt.Errorf("gnosh's own text %q: %w", sp.name, err)
		}
		s.order = append(s.order, sp.name)
		s.texts[sp.name] = &text{spec: sp, body: body, tmpl: tmpl}
	}
	files, err := builtinFiles.ReadDir("texts")
	if err != nil {
		return nil, fmt.Errorf("listing gnosh's own texts: %w", err)
	}
	for _, f := range files {
		if name := strings.TrimSuffix(f.Name(), ".md"); s.texts[name] == nil {
			return nil, fmt.Errorf("the file texts/%s is not one of the texts gnosh sends", f.Name())
		}
	}
	return s, nil
}

// builtinBody returns gnosh's own words for the text called name.
func builtinBody(name string) (string, error) {
	raw, err := builtinFiles.ReadFile("texts/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("reading gnosh's own text %q: %w", name, err)
	}
	return tidy(string(raw)), nil
}

// tidy puts a text read from a file into the one form gnosh uses: line
// ends are plain newlines, and blank space around the whole is gone.
func tidy(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

// check reads body as the text sp describes and returns it ready to fill
// in. It refuses a body the text could not be sent as: an empty one, one
// that drops a name the text must print or uses one it does not have, one
// that holds a control sequence, for a line one that writes one of gnosh's
// own tags itself, and for the refusal of a close one that does not name
// the todo first.
func (sp spec) check(body string) (template, error) {
	if !utf8.ValidString(body) {
		return template{}, fmt.Errorf("it is not valid UTF-8 text")
	}
	if strings.TrimSpace(body) == "" {
		return template{}, fmt.Errorf("it is empty")
	}
	if Defuse(body) != body {
		return template{}, fmt.Errorf("it holds a control sequence of a chat template (such as \"<|\" or \"<think>\"), which would cut the message it is sent in: write it another way")
	}
	if sp.kind == KindLine && Untrusted(body) != body {
		return template{}, fmt.Errorf("it writes one of gnosh's own tags itself (%s or <%s>): leave the tags out, gnosh puts its marker around this text", Open, memTag)
	}
	tmpl, err := parseTemplate(body)
	if err != nil {
		return template{}, err
	}
	printed, asked := tmpl.names()
	known := sp.names()
	for _, name := range slices.Concat(printed, asked) {
		if !slices.Contains(known, name) {
			return template{}, fmt.Errorf("it uses {{%s}}, which this text is not given: %s", name, sp.takes())
		}
	}
	var dropped []string
	for _, name := range sp.need {
		if !slices.Contains(printed, name) {
			dropped = append(dropped, "{{"+name+"}}")
		}
	}
	if len(dropped) > 0 {
		return template{}, fmt.Errorf("it leaves out %s, which the model must be told: %s", strings.Join(dropped, " and "), sp.takes())
	}
	if sp.refusal {
		const todo = "`{{todo}}`"
		if at := strings.IndexByte(body, '`'); at < 0 || !strings.HasPrefix(body[at:], todo) {
			return template{}, fmt.Errorf("the first thing it puts in backquotes is not %s: gnosh knows which todo a refusal is about by the first name in backquotes", todo)
		}
	}
	return tmpl, nil
}

// names lists every name the text may use, the needed ones first.
func (sp spec) names() []string {
	return slices.Concat(sp.need, sp.may)
}

// takes says in words which names the text is given.
func (sp spec) takes() string {
	all := sp.names()
	if len(all) == 0 {
		return "it is given no names to fill in"
	}
	quoted := make([]string, len(all))
	for i, name := range all {
		quoted[i] = "{{" + name + "}}"
	}
	return "it is given " + strings.Join(quoted, ", ")
}

// fill returns the text called name with vals filled in. A line comes back
// between Open and Close.
func (s *Set) fill(name string, vals values) string {
	out := s.bare(name, vals)
	if s.texts[name].spec.kind == KindLine {
		return Wrap(out)
	}
	return out
}

// bare returns the text called name with vals filled in and no marker
// around it, for a line that is put inside another.
func (s *Set) bare(name string, vals values) string {
	return strings.TrimSpace(s.texts[name].tmpl.fill(vals))
}

// Format returns the text called name as a format for fmt.Sprintf, for
// code that fills its texts in that way. holes gives, for each name of
// the text, what is written in its place: a verb such as "%[1]s", or
// plain words. A name that holes leaves out counts as having no value, so
// an optional part that needs it is left out, and a name the text only
// asks about is switched on by giving it any words. Every "%" in the
// text's own words is doubled.
//
// What comes back has no marker around it, and what is later put in the
// verbs is not made harmless here: a caller that sends the result on the
// user's side passes it through Wrap, which does both.
//
// Format refuses a name the text does not have, and a call that leaves
// out a name the text must print.
func (s *Set) Format(name string, holes map[string]string) (string, error) {
	t, ok := s.texts[name]
	if !ok {
		return "", fmt.Errorf("gnosh has no text called %q. The texts are: %s", name, strings.Join(Names(), ", "))
	}
	known := t.spec.names()
	for _, hole := range slices.Sorted(maps.Keys(holes)) {
		if !slices.Contains(known, hole) {
			return "", fmt.Errorf("the text %q has no {{%s}}: %s", name, hole, t.spec.takes())
		}
	}
	for _, need := range t.spec.need {
		if strings.TrimSpace(holes[need]) == "" {
			return "", fmt.Errorf("the text %q must print {{%s}}, and nothing was given for it: %s", name, need, t.spec.takes())
		}
	}
	return strings.TrimSpace(t.tmpl.format(values(holes))), nil
}

// Info describes one text of a Set, for a command that shows a person
// what the model is sent and where it came from.
type Info struct {
	// Name is the text's name. Its file is Name with ".md".
	Name string
	// Kind says where the text goes.
	Kind Kind
	// When says, in a line, when the text is sent.
	When string
	// Required lists the names, written {{name}} in the text, that a
	// replacement must keep. Optional lists the others it may use.
	Required []string
	Optional []string
	// Body is the text in force.
	Body string
	// Bytes and Tokens are Body's size, and about how many tokens that is.
	Bytes  int
	Tokens int
	// Replaced says Body is a person's own text, read from Path. Stale
	// says gnosh's own text has changed since that file was exported.
	Replaced bool
	Path     string
	Stale    bool
}

// Names lists the names of gnosh's texts, in the order they are shown.
func Names() []string {
	list := specs()
	names := make([]string, len(list))
	for i, sp := range list {
		names[i] = sp.name
	}
	return names
}

// Texts describes every text of the Set, in the order of Names.
func (s *Set) Texts() []Info {
	out := make([]Info, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, s.info(s.texts[name]))
	}
	return out
}

// Text describes the text called name. The second result is false when
// gnosh has no text of that name.
func (s *Set) Text(name string) (Info, bool) {
	t, ok := s.texts[name]
	if !ok {
		return Info{}, false
	}
	return s.info(t), true
}

func (s *Set) info(t *text) Info {
	return Info{
		Name:     t.spec.name,
		Kind:     t.spec.kind,
		When:     t.spec.when,
		Required: slices.Clone(t.spec.need),
		Optional: slices.Clone(t.spec.may),
		Body:     t.body,
		Bytes:    len(t.body),
		Tokens:   EstimateTokens(t.body),
		Replaced: t.replaced,
		Path:     t.path,
		Stale:    t.stale,
	}
}
