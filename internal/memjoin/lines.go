package memjoin

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	gnovmtools "github.com/gnosh-ai/gnosh/internal/gnovm/tools"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// Who wrote a hidden part, as the session's log names it.
const (
	srcDigest    = "mem:digest"
	srcRecall    = "mem:recall"
	srcMemLine   = "mem:line"
	srcFrameLine = "mem:frame-line"
	srcReminder  = "mem:reminder"
	srcLargeStep = "mem:large-step"
	srcGoal      = "mem:goal"
)

// The keys of the meta entries the join writes into a session's log.
const (
	// metaSaid marks something the model was told once, so that a session
	// that is opened again does not tell it twice: "reminder", or
	// "large-step:" and the push entry of the frame it was said of.
	metaSaid = "mem.said"
	// metaGoal holds the session's /goal binding as JSON, or nothing once
	// it is cleared. The newest one written under the session's own id is
	// the binding in force.
	metaGoal = "mem.goal"
	// metaTasks holds the session's task list as JSON: mem's todos for
	// the last task a call of the session changed.
	metaTasks = "mem.tasks"
)

// remindAfter is how many replies of the model may go by with no call on
// mem before it is reminded of mem.
const remindAfter = 10

// reminderState is what the join remembers so as to remind at the right
// moments and no more often. It is guarded by the join's lock.
type reminderState struct {
	// replies counts the model's replies since its last call on mem, the
	// last reminder or the last summary, whichever came last.
	replies int
	// large holds the push entries of the frames the large-step line was
	// said of, and saying is the one it is being said of right now.
	large  map[string]bool
	saying string
}

// promptLines is the join's PromptLines provider. It is asked as a prompt
// goes out, whether the prompt starts a turn or is folded into one that
// is running, and what it returns is stored after the prompt's text and
// after what the hooks added, in this order:
//
//   - the directive of a goal that `gnosh -p --goal` bound as the run
//     began, with the run's own prompt;
//   - the digest of mem, with the first prompt a session ever sends (a
//     hidden one too: a /goal directive), and again with a typed prompt
//     when mem was changed elsewhere since the session last spoke;
//   - what mem recalls for the prompt, when an embedding server is set;
//   - the mem line, last on every prompt a person types in a session that
//     has gnovm_run;
//   - the frame line after it, while the session stands in a todo.
//
// In plan mode the last two are left out: the model may look and not
// change, and is given the digest and what is recalled alone. The mode is
// asked as each prompt goes out, since the person may switch it between
// two prompts.
//
// Nothing of this is in the system prompt, which is the same bytes in
// every session.
func (j *join) promptLines(ctx context.Context, info agent.LineInfo) ([]agent.Part, error) {
	var parts []agent.Part
	typed := !info.Prompt.Hidden
	if typed {
		if text := j.takeDirective(); text != "" {
			parts = append(parts, agent.Part{Source: srcGoal, Text: prompt.Wrap(text)})
		}
	}

	block := ""
	switch {
	case info.First:
		block = j.vms.MemBlock(j.project, j.id)
	case typed:
		if since, ok := j.newestMessage(); ok {
			block = j.vms.MemChanged(j.project, j.id, since)
		}
	}
	if block != "" {
		parts = append(parts, agent.Part{Source: srcDigest, Text: block})
	}
	if !typed {
		return parts, nil
	}

	// A session left open overnight tells the person what is due when
	// they are back.
	if day := j.now().Format(time.DateOnly); j.newDay(day) {
		j.tell(agent.LevelWarning, "mem", j.vms.MemNotice(j.project))
	}

	if recall := j.vms.MemRecall(ctx, j.project, j.id, info.Prompt.Text); recall != "" {
		parts = append(parts, agent.Part{Source: srcRecall, Text: recall})
	}
	if !j.run || j.planMode() {
		// Plan mode, and a session without the tool: the digest and what
		// is recalled still go with the prompt, the lines do not.
		return parts, nil
	}
	parts = append(parts, agent.Part{Source: srcMemLine, Text: j.texts.MemLine()})
	if line := j.frameLine(); line != "" {
		parts = append(parts, agent.Part{Source: srcFrameLine, Text: line})
	}
	return parts, nil
}

// newDay notes the day the person is being told on, and reports whether
// it is another day than the last time.
func (j *join) newDay(day string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.noticeDay == day {
		return false
	}
	j.noticeDay = day
	return true
}

// memNotice tells the person, as a session starts, what in mem is due and
// what waits on them.
func (j *join) memNotice() {
	j.newDay(j.now().Format(time.DateOnly))
	j.tell(agent.LevelWarning, "mem", j.vms.MemNotice(j.project))
}

// newestMessage is the time of the session's newest message: the moment
// the session last spoke or was spoken to. mem changed after it was
// changed elsewhere.
func (j *join) newestMessage() (time.Time, bool) {
	entries := j.ts.Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		if e := &entries[i]; e.IsMessage() {
			return e.Timestamp, true
		}
	}
	return time.Time{}, false
}

// frames is the session's stack as the prompt texts take it.
func (j *join) frames() []prompt.Frame {
	stack := j.ts.Stack()
	frames := make([]prompt.Frame, len(stack))
	for i, f := range stack {
		frames[i] = prompt.Frame{ID: f.ID, Purpose: f.Label}
	}
	return frames
}

// frameLine is the line that follows the mem line while the session
// stands in a todo: where it stands, and what a pop does. It is written
// from the stack as it stands at this moment, and it carries the line of
// every slide that concerns a todo still on the stack.
func (j *join) frameLine() string {
	frames := j.frames()
	if len(frames) == 0 {
		return ""
	}
	return j.texts.FrameLine(frames, j.liveSlides())
}

// liveSlides lists the slides the model is still to be told of: those
// whose todo is on the stack as the same todo, each once.
func (j *join) liveSlides() []prompt.Slide {
	_, info := j.ts.View(transcript.ViewOptions{Trace: true})
	type key struct {
		frame  string
		before bool
	}
	var out []prompt.Slide
	at := map[key]int{}
	names := fstools.Defaults()
	cwd := j.env().Cwd
	for _, c := range info.Cuts {
		if c.Kind != transcript.OpSlide || !c.Live {
			continue
		}
		k := key{frame: c.Frame}
		if c.Frame == "" {
			k = key{frame: c.Before, before: true}
		}
		if k.frame == "" {
			continue
		}
		i, seen := at[k]
		if !seen {
			i = len(out)
			at[k] = i
			out = append(out, prompt.Slide{Frame: k.frame, Before: k.before})
		}
		// What the rounds that went had edited, so that the model does
		// not go back over work it has done (box 2, 16 November: a todo
		// over sixteen files slid thirteen times while the model read
		// them again).
		for _, p := range c.EditedPaths(names.EditName, names.WriteName) {
			if file := j.shown(p, cwd); file != "" && !slices.Contains(out[i].Files, file) {
				out[i].Files = append(out[i].Files, file)
			}
		}
	}
	return out
}

// beforeRequest is the join's BeforeRequest provider: it is asked before
// every request of a turn for reminders to put in front of the model. In
// plan mode there is none: both reminders ask for something mem would
// have to be changed for.
func (j *join) beforeRequest(_ context.Context, info agent.RequestInfo) ([]agent.Part, error) {
	if !j.run || j.planMode() {
		return nil, nil
	}
	var parts []agent.Part
	if text := j.largeStep(info); text != "" {
		parts = append(parts, agent.Part{Source: srcLargeStep, Text: text})
	}
	if text := j.memReminder(); text != "" {
		parts = append(parts, agent.Part{Source: srcReminder, Text: text})
	}
	return parts, nil
}

// largeStep returns the line that tells the model, once per todo, that
// the todo it stands in has grown large: when more than half of the room
// that was left below the compaction point as the todo was entered has
// come in since. It comes well before the point itself, so that the model
// can pop or split the todo while there is room. (It was half the point
// itself, which a todo entered past half the point could never reach
// before the slide: every late todo of a long session, the AI-box peer
// found.) info.Used is measured as the request goes out, so a pop just
// made counts: the todo it returns to has not taken in what it removed.
func (j *join) largeStep(info agent.RequestInfo) string {
	if info.Point <= 0 || (j.s.Config != nil && !j.s.Config.Options.AutoCompactEnabled.V) {
		return ""
	}
	stack := j.ts.Stack()
	if len(stack) == 0 {
		return ""
	}
	top := stack[len(stack)-1]
	grown := info.Used - top.TotalAtPush
	room := info.Point - top.TotalAtPush
	// (And at least a tenth of the point: a todo entered with little room
	// left is not large after a few hundred tokens.)
	if top.UUID == "" || top.TotalAtPush <= 0 || room <= 0 || grown <= room/2 || grown <= info.Point/10 {
		return ""
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.remind.large[top.UUID] {
		return ""
	}
	if j.remind.large == nil {
		j.remind.large = map[string]bool{}
	}
	j.remind.large[top.UUID] = true
	j.remind.saying = top.UUID
	return j.texts.LargeStep(top.ID, grown)
}

// memReminder returns the reminder of mem when the model has written ten
// replies with no call on mem and no reminder among them, counted since
// the last summary. With a stack the frame line's first sentence follows,
// and with a task list, where its todos stand.
func (j *join) memReminder() string {
	j.mu.Lock()
	due := j.remind.replies >= remindAfter
	list := j.tasks.list
	j.mu.Unlock()
	if !due {
		return ""
	}
	var todos []prompt.Todo
	for _, l := range list {
		status := "pending"
		switch l.State {
		case gnovmtools.LineDone:
			status = "completed"
		case gnovmtools.LineWorking:
			status = "in progress"
		}
		todos = append(todos, prompt.Todo{Title: l.Title, Status: status})
	}
	return j.texts.MemReminder(j.frames(), todos)
}

// afterSummary is the join's AfterSummary: a summary stands in place of
// the conversation. mem is told, so that what it showed the session
// before may be recalled to it again, and the digest is given back with
// what the session is handed after the summary.
func (j *join) afterSummary(context.Context, agent.SummaryInfo) ([]agent.Part, error) {
	j.vms.MemSummarized(j.id)
	if block := j.vms.MemBlock(j.project, j.id); block != "" {
		return []agent.Part{{Source: srcDigest, Text: block}}, nil
	}
	return nil, nil
}

// usesMem reports whether a reply of the model calls mem.
func usesMem(m core.Message) bool {
	for _, b := range m.Blocks {
		if b.Type == core.BlockToolUse && b.Name == gnovmtools.RunToolName {
			if _, ok := memCode(b.Input); ok {
				return true
			}
		}
	}
	return false
}

// count is the join's bookkeeping on what the loop stores: the calls on
// a machine whose result is still to come, the count of replies for the
// reminder, and how the closes of the turn went. It is called while the
// log is being written, so it only reads the entry and writes the join's
// own memory.
func (j *join) count(e agent.Entry) {
	j.mu.Lock()
	defer j.mu.Unlock()
	switch e.Kind {
	case agent.EntryAssistant:
		for _, b := range e.Message.Blocks {
			if b.Type == core.BlockToolUse && b.Name == gnovmtools.RunToolName {
				if j.inflight == nil {
					j.inflight = map[string]bool{}
				}
				j.inflight[b.ID] = true
			}
		}
		switch {
		case e.Silent:
		case usesMem(e.Message):
			j.remind.replies = 0
		default:
			j.remind.replies++
		}
	case agent.EntryNote:
		if e.Source == srcReminder {
			j.remind.replies = 0
		}
	case agent.EntrySummary:
		j.remind.replies = 0
	case agent.EntryPrompt:
		j.turn.prompt(e)
	case agent.EntryResult:
		if e.ToolName == gnovmtools.RunToolName {
			delete(j.inflight, e.ToolUseID)
			j.turn.result(e)
		}
	}
}

// marks returns the meta entry that goes beside a hidden part of the
// join's own, so that a session opened again knows what was said: the
// reminder, which starts the count of replies over, and the large-step
// line, which is said once for a todo.
func (j *join) marks(e agent.Entry) []transcript.Entry {
	if e.Kind != agent.EntryNote {
		return nil
	}
	switch e.Source {
	case srcReminder:
		return []transcript.Entry{transcript.MetaEntry(metaSaid, "reminder")}
	case srcLargeStep:
		j.mu.Lock()
		uuid := j.remind.saying
		j.remind.saying = ""
		j.mu.Unlock()
		if uuid != "" {
			return []transcript.Entry{transcript.MetaEntry(metaSaid, "large-step:"+uuid)}
		}
	}
	return nil
}

// load reads from the session's log what the join keeps in memory while
// the session runs: the count of replies since mem was last used, the
// todos the large-step line was said of, the goal the session is bound
// to, and its task list. A new session has none of it.
func (j *join) load() {
	if j.ts == nil {
		return
	}
	entries := transcript.Conversation(j.ts.Entries())
	j.mu.Lock()
	defer j.mu.Unlock()
	counting := true
	for i := len(entries) - 1; i >= 0; i-- {
		e := &entries[i]
		switch e.Type {
		case transcript.TypeMeta:
			switch {
			case e.Key == metaSaid && e.Value == "reminder":
				counting = false
			case e.Key == metaSaid && strings.HasPrefix(e.Value, "large-step:"):
				if j.remind.large == nil {
					j.remind.large = map[string]bool{}
				}
				j.remind.large[strings.TrimPrefix(e.Value, "large-step:")] = true
			}
		case transcript.TypeSummary:
			counting = false
		case transcript.TypeAssistant:
			switch {
			case !counting || len(e.Message.Blocks) == 0:
			case usesMem(e.Message):
				counting = false
			default:
				j.remind.replies++
			}
		}
	}
	j.goal.load(j.id, entries)
	j.tasks.load(entries)
}
