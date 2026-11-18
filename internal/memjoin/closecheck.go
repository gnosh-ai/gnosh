package memjoin

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/gnovm"
	gnovmtools "github.com/gnosh-ai/gnosh/internal/gnovm/tools"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// This file is the close check (spec/gnosh/close-check.md): before mem
// keeps a call that closes a todo as done, the close is held against what
// happened in the todo. The gate inside mem hands the closes out and runs
// the call again once they are approved; the three checks, cheapest first,
// are here.

// reviewState is what the review remembers. It is guarded by the join's
// lock.
type reviewState struct {
	// verdicts holds what the review said, by what it was asked, so that
	// a close sent again unchanged is not reviewed again; order is the
	// keys from the oldest, for dropping the oldest when there are many.
	verdicts map[string]verdict
	order    []string
	// asked holds, by tool call id, the closes of top-level todos that the
	// review approved in that call, until mem says whether it kept the
	// call.
	asked map[string][]approval
	// passed holds, by top-level todo, the result the review approved in
	// a call mem kept: such a todo is not judged again as the turn ends.
	passed map[string]string
	// judged holds, by top-level todo, the journal entry of the close
	// that the turn-end judge was last asked about, so that one close is
	// judged once. A todo that is opened again and closed again has a
	// new entry, and is judged again.
	judged map[string]int64
	// unchecked holds the top-level todos whose close the turn-end judge
	// could not judge and that the agent was sent back for: it is sent
	// back once a todo.
	unchecked map[string]bool
}

// verdict is what a review came to.
type verdict struct {
	ok     bool
	reason string
}

// approval is a close the review approved.
type approval struct{ todo, result string }

// maxVerdicts is how many verdicts are remembered.
const maxVerdicts = 300

// window is what counts as "in the todo" for a close: the events of the
// session between a start point and the closing call, with the calls made
// before the closing call in its own reply.
type window struct {
	events []event
	// start is when the window begins: a file changed at or after it was
	// changed in the todo.
	start time.Time
	// named maps a base name to a file the session's calls named.
	named map[string]string
	// asked is what the person asked in the turn, when the window holds
	// none of their words.
	asked string
	// code is the closing call's own code.
	code string
	// last names the last thing that happened in the window, for telling
	// a close that is sent again from one made after more work.
	last string
}

// checkCloses is the tools' CheckCloses slot. mem has kept nothing of the
// call yet, and hands over the closes that are not approved. For each, in
// order: was the todo entered and closed in one go with nothing done; does
// the result claim a file change that nothing in the todo made or showed;
// and, unless the option is off, does a small model reading what happened
// find that the result holds. The first refusal is the call's error; an
// empty answer approves them all.
func (j *join) checkCloses(ctx context.Context, _ string, callID string, closes []gnovm.MemClose) string {
	events := layout(j.ts.Entries(), j.id)
	stack := j.ts.Stack()
	for _, cl := range closes {
		if refusal := j.checkClose(ctx, events, stack, callID, cl); refusal != "" {
			return refusal
		}
	}
	return ""
}

// checkClose runs the three checks on one close.
func (j *join) checkClose(ctx context.Context, events []event, stack []transcript.Frame, callID string, cl gnovm.MemClose) string {
	onStack := slices.ContainsFunc(stack, func(f transcript.Frame) bool { return f.ID == cl.Id })
	// A todo that is neither on the stack nor entered by this call is
	// being closed by its name: the words of a refusal say "closed".
	byName := !onStack && !cl.Entered
	w, ok := j.window(events, stack, callID, cl, onStack)
	if !ok {
		// The window cannot be read, so the close is approved unchecked.
		return ""
	}
	if cl.Entered && !cl.Worked && !worked(w.events, cl.Id) {
		return j.texts.CloseEnteredHere(cl.Id)
	}
	if refusal := j.claimCheck(w, cl, byName); refusal != "" {
		return refusal
	}
	if j.s.Config != nil && !j.s.Config.Options.MemReviewCloses.V {
		return ""
	}
	return j.reviewClose(ctx, w, callID, cl, byName)
}

// window works out what counts as "in the todo" for a close made by the
// call callID (close-check.md section 3.2). ok is false when it cannot be
// read: the closing call, or the push of a todo on the stack, is not in
// the conversation.
func (j *join) window(events []event, stack []transcript.Frame, callID string, cl gnovm.MemClose, onStack bool) (window, bool) {
	at := -1
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].kind == evCall && events[i].id == callID {
			at = i
			break
		}
	}
	if at < 0 {
		return window{}, false
	}
	// roundStart is the first event of the reply that the event at i
	// belongs to, and roundOf the first event of the reply that made a
	// call.
	roundStart := func(i int) int {
		for i > 0 && events[i-1].reply == events[i].reply && (events[i-1].kind == evSaid || events[i-1].kind == evCall) {
			i--
		}
		return i
	}
	callAt := func(id string, before int) int {
		for i := before - 1; i >= 0; i-- {
			if events[i].kind == evCall && events[i].id == id {
				return i
			}
		}
		return -1
	}
	pushOf := func(frame string) int {
		for i := at - 1; i >= 0; i-- {
			if events[i].kind == evPush && events[i].frame == frame {
				return i
			}
		}
		return -1
	}
	lastFrame := func(before int) int {
		for i := before - 1; i >= 0; i-- {
			if events[i].kind == evPush || events[i].kind == evPop {
				return i
			}
		}
		return -1
	}
	turn := turnStart(events, at)

	start := 0
	switch {
	case cl.Entered:
		// The input itself entered the todo: the closing call's own reply
		// only.
		start = roundStart(at)
	case onStack:
		push := pushOf(cl.Id)
		if push < 0 {
			return window{}, false
		}
		entering := callAt(events[push].id, push)
		popped := false
		if entering >= 0 {
			for i := range events[:at] {
				if e := &events[i]; e.kind == evPop {
					if c := callAt(e.id, i); c >= 0 && events[c].reply == events[entering].reply {
						popped = true
						break
					}
				}
			}
		}
		if popped {
			// The reply that entered it also left a todo: nothing comes
			// between the todo before and this one.
			start = push + 1
		} else {
			// Work done on the way counts: from the session's last push
			// or pop before the todo was entered, or the turn's start,
			// whichever is later; but no later than its own entering.
			start = max(lastFrame(push)+1, turn)
			if entering >= 0 {
				start = min(start, roundStart(entering))
			} else {
				start = min(start, push)
			}
		}
	default:
		// Closed by its name: a sibling's work is not this todo's.
		start = lastFrame(at) + 1
		if len(stack) > 0 {
			if push := pushOf(stack[len(stack)-1].ID); push >= 0 {
				if entering := callAt(events[push].id, push); entering >= 0 {
					start = max(start, roundStart(entering))
				}
			}
		}
		start = max(start, turn)
	}
	// A todo of a plan counts from where the plan was made: a whole plan
	// worked from its root, and each todo then entered and closed, has
	// its work before the todo was entered (fix A).
	if len(stack) > 0 && stack[0].ID != cl.Id {
		if push := pushOf(stack[0].ID); push >= 0 && push+1 < start {
			start = push + 1
		}
	}
	start = min(start, at)

	w := window{events: events[start:at], start: events[at].time, named: map[string]string{}}
	if len(w.events) > 0 {
		w.start = w.events[0].time
	}
	w.code, _ = memCode(events[at].input)
	typed := false
	for i := range w.events {
		typed = typed || w.events[i].kind == evPrompt
	}
	// A close that is sent again after a refusal has the refused call in
	// its window, and nothing else new: only work makes the window
	// another one.
	if i := lastWork(w.events, cl.Id); i >= 0 {
		w.last = w.events[i].uuid + "/" + w.events[i].id
	}
	if !typed {
		var words []string
		for i := turn; i < at; i++ {
			if events[i].kind == evPrompt {
				words = append(words, events[i].text)
			}
		}
		w.asked = strings.Join(words, "\n")
	}
	for i := range events[:at] {
		if e := &events[i]; e.kind == evCall && e.path != "" {
			path := j.expand(e.path)
			w.named[filepath.Base(path)] = path
		}
	}
	return w, true
}

// claim is a file a result says was changed. made says the result says it
// was made.
type claim struct {
	file string
	made bool
}

var (
	// clauseRe splits a result into clauses: at a semicolon, a line end, a
	// full stop followed by a blank, or a dash with blanks around it.
	clauseRe = regexp.MustCompile(`;|\n|\.\s| [-–—] `)
	// settledRe finds the words of a clause that says what is so already,
	// what is to come, or what was done elsewhere. Such a clause claims
	// nothing.
	settledRe = regexp.MustCompile(`(?i)\b(already|still|unchanged|untouched|no change|not changed|no edits?|nothing to|needs? no|needed no|no need|not needed|did not|didn['’]t|does not|doesn['’]t|will|must|should|needs? to|next|later|before|earlier|per|todo|pending|left for)\b`)
	// fileRe is a path-like word whose extension begins with a letter,
	// with the line or the lines it may name after a colon.
	fileRe = regexp.MustCompile(`^([A-Za-z0-9_.~/\\-]*[A-Za-z0-9_-]\.[A-Za-z][A-Za-z0-9]*)(?::\d+(?:-\d+)?)?$`)
)

// The verbs that follow "now" in a claim of what a file says, the words
// for a change, and the words for a file that was made.
var (
	nowVerbs    = wordSet("says reads returns documents has calls uses prints shows takes lists includes contains handles checks gives refuses accepts is does")
	changeWords = wordSet("added created wrote written edited changed updated fixed renamed removed deleted replaced moved implemented inserted appended bumped refactored rewrote rewritten converted documented")
	madeWords   = wordSet("added created wrote written")
	// knownExts are the kinds of file a result is taken to mean when it
	// says one was made and there is no such file.
	knownExts = wordSet("go gno mod sum md txt json yaml yml toml py js ts tsx jsx rs c h cc cpp java rb sh html css sql proto csv xml ini cfg")
)

func wordSet(list string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(list) {
		m[w] = true
	}
	return m
}

// fileWord returns the file a word of a result names, or "".
func fileWord(word string) string {
	for {
		next := strings.TrimRight(strings.Trim(word, "`'\"“”‘’()[]{}<>,;!?*"), ".:")
		if next == word {
			break
		}
		word = next
	}
	if word == "" || strings.Contains(word, "://") || strings.HasPrefix(word, "www.") {
		return ""
	}
	m := fileRe.FindStringSubmatch(word)
	if m == nil {
		return ""
	}
	return m[1]
}

// claims lists the files a result says were changed (close-check.md
// section 3.3). Only two shapes count: a file, then "now", then a verb of
// what it says; and a word for a change, then the files after it, or the
// file right before it.
func claims(result string) []claim {
	var out []claim
	add := func(file string, made bool) {
		for i := range out {
			if out[i].file == file {
				out[i].made = out[i].made && made
				return
			}
		}
		out = append(out, claim{file, made})
	}
	for _, clause := range clauseRe.Split(result, -1) {
		if settledRe.MatchString(clause) {
			continue
		}
		toks := strings.Fields(clause)
		plain := make([]string, len(toks))
		files := make([]string, len(toks))
		for i, t := range toks {
			plain[i] = strings.ToLower(strings.Trim(t, "`'\"“”‘’()[]{}<>,;:.!?*"))
			files[i] = fileWord(t)
		}
		for i := range toks {
			if files[i] != "" && i+2 < len(toks) && plain[i+1] == "now" && nowVerbs[plain[i+2]] {
				add(files[i], false)
			}
			if !changeWords[plain[i]] {
				continue
			}
			made := madeWords[plain[i]]
			if i > 0 && files[i-1] != "" {
				add(files[i-1], made)
			}
			for k := i + 1; k < len(toks); k++ {
				if files[k] != "" {
					add(files[k], made)
				}
			}
		}
	}
	return out
}

// claimCheck refuses a result that says a file changed when nothing in
// the todo changed or showed that file. It is always on and asks no
// model. A claim that names no file is left to the review.
func (j *join) claimCheck(w window, cl gnovm.MemClose, byName bool) string {
	cwd := j.env().Cwd
	for _, c := range claims(cl.Result) {
		path, exists := j.locate(c.file, cwd, w.named)
		known := knownExts[strings.TrimPrefix(strings.ToLower(filepath.Ext(c.file)), ".")]
		if !exists && !known {
			// What the result names is not a file: "client.Get".
			continue
		}
		if j.backed(w, c.file, path, exists) {
			continue
		}
		if c.made && !exists {
			return j.texts.CloseNotMade(cl.Id, c.file, byName)
		}
		return j.texts.CloseUnbacked(cl.Id, c.file, byName)
	}
	return ""
}

// The folders a search for a file passes over: version control, what a
// package manager fetched, and gnosh's own data.
var skipDirs = wordSet(".git .hg .svn node_modules vendor .gnosh .venv venv __pycache__ target dist build")

// maxSearched is how many directory entries a search for a file looks at.
const maxSearched = 20000

// locate finds the file a result names: under the working directory as
// written; else as an absolute path; else a file of that base name that
// the session's calls named; else a path under the working directory that
// ends with it.
func (j *join) locate(name, cwd string, named map[string]string) (string, bool) {
	isFile := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && !info.IsDir()
	}
	name = j.expand(name)
	if filepath.IsAbs(name) {
		return name, isFile(name)
	}
	if cwd != "" {
		if path := filepath.Join(cwd, name); isFile(path) {
			return path, true
		}
	}
	if path, ok := named[filepath.Base(name)]; ok {
		if !filepath.IsAbs(path) && cwd != "" {
			path = filepath.Join(cwd, path)
		}
		if endsWith(path, name) && isFile(path) {
			return path, true
		}
	}
	if cwd == "" {
		return name, false
	}
	found, seen := "", 0
	_ = filepath.WalkDir(cwd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		seen++
		if seen > maxSearched {
			return fs.SkipAll
		}
		if d.IsDir() {
			if path != cwd && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if endsWith(path, name) {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if found != "" {
		return found, true
	}
	return filepath.Join(cwd, name), false
}

// The programs whose output shows a file.
var showers = wordSet("cat head tail less more nl bat sed")

// shows reports whether a command line shows the file called name: it
// runs a program that prints files, and names the file.
func shows(line, name string) bool {
	toks := strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '|' || r == '&' || r == '(' || r == ')' || r == '<' || r == '>'
	})
	shower, named := false, false
	for _, t := range toks {
		t = strings.Trim(t, "'\"")
		shower = shower || showers[filepath.Base(t)]
		named = named || endsWith(t, name)
	}
	return shower && named
}

// namesFile reports whether a command line names the file.
func namesFile(line, name string) bool {
	for _, t := range strings.FieldsFunc(line, func(r rune) bool { return strings.ContainsRune(" \t\n;|&()<>'\"", r) }) {
		if endsWith(t, name) {
			return true
		}
	}
	return false
}

// backed reports whether something in the window changed or showed the
// file called name: an edit or a write of it that did not fail; a read of
// it, by the Read tool or by a command that printed it; or, for a file
// that is there, a modification or change time at or after the window's
// start, which is how a command that changed it shows. A file that is no
// longer there is backed by a command that named it.
func (j *join) backed(w window, name, path string, exists bool) bool {
	for i := range w.events {
		e := &w.events[i]
		if e.kind != evCall || !e.answered || e.failed {
			continue
		}
		switch {
		case editTool(e.tool), readTool(e.tool):
			if endsWith(j.expand(e.path), name) {
				return true
			}
		case shellTool(e.tool):
			line := command(e.input)
			if shows(line, name) || (!exists && namesFile(line, name)) {
				return true
			}
		}
	}
	if !exists {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	since := func(t time.Time) bool {
		if t.IsZero() {
			return false
		}
		start := w.start
		if t.Nanosecond() == 0 {
			// A file system that keeps times to the second: a change
			// made in the window's first second must not be missed.
			start = start.Truncate(time.Second)
		}
		return !t.Before(start)
	}
	return since(info.ModTime()) || since(changeTime(info))
}

// reviewClose asks the small model whether the result of a close is what
// came of its todo, and whether what happened in the todo shows it
// (close-check.md section 3.4). A verdict is remembered, so that a close
// sent again unchanged is not reviewed again. When the review cannot run
// the close is kept unchecked and the person is warned; a turn that was
// stopped while it ran is not a pass.
func (j *join) reviewClose(ctx context.Context, w window, callID string, cl gnovm.MemClose, byName bool) string {
	key := strings.Join([]string{j.id, cl.Id, cl.Result, w.last}, "\x00")
	if cl.Entered {
		key += "\x00" + w.code
	}
	done := func(v verdict) string {
		if !v.ok {
			return j.texts.CloseReviewRefused(cl.Id, v.reason, byName)
		}
		if !strings.Contains(cl.Id, "/") {
			j.mu.Lock()
			if j.review.asked == nil {
				j.review.asked = map[string][]approval{}
			}
			j.review.asked[callID] = append(j.review.asked[callID], approval{cl.Id, cl.Result})
			j.mu.Unlock()
		}
		return ""
	}
	j.mu.Lock()
	v, cached := j.review.verdicts[key]
	j.mu.Unlock()
	if cached {
		return done(v)
	}

	unchecked := func(why string) string {
		j.tell(agent.LevelWarning, "mem", fmt.Sprintf("The close of `%s` could not be checked, so it was kept unchecked: %s", cl.Id, why))
		return ""
	}
	if ctx.Err() != nil {
		// The turn was stopped: a check that did not run has not passed.
		return j.texts.CloseCancelled(cl.Id, byName)
	}
	a := agent.FromContext(ctx)
	if a == nil {
		a = j.theAgent()
	}
	if a == nil {
		return unchecked("there is no model to ask")
	}
	in := prompt.CloseReviewInput{
		Todo:       cl.Title,
		Thoughts:   cl.Thoughts,
		Ask:        w.asked,
		Result:     cl.Result,
		Transcript: shownTo(w.events),
	}
	if in.Todo == "" {
		in.Todo = cl.Id
	}
	if cl.Entered {
		in.Code = w.code
	}
	cwd := j.env().Cwd
	for i := range w.events {
		e := &w.events[i]
		if e.kind != evCall || !e.answered {
			continue
		}
		switch {
		case editTool(e.tool):
			if file := j.shown(e.path, cwd); !e.failed && file != "" && !slices.Contains(in.Files, file) {
				in.Files = append(in.Files, file)
			}
		case shellTool(e.tool):
			cmd := prompt.Command{Line: command(e.input), Exit: e.exit}
			if !e.hasExit && e.failed {
				cmd.Exit = 1
			}
			in.Commands = append(in.Commands, cmd)
		}
	}
	req := j.texts.CloseReview(in)
	zero := 0.0
	res, err := a.Side(ctx, agent.SideRequest{
		Purpose:     "close-review",
		System:      req.System,
		Prompt:      req.Prompt,
		MaxTokens:   1500,
		Temperature: &zero,
		Timeout:     3 * time.Minute,
	})
	if ctx.Err() != nil {
		return j.texts.CloseCancelled(cl.Id, byName)
	}
	if err != nil {
		return unchecked(err.Error())
	}
	v, ok := readVerdict(res.Text)
	if !ok {
		return unchecked("the review's answer could not be read: " + clipText(res.Text, 200))
	}
	j.mu.Lock()
	if j.review.verdicts == nil {
		j.review.verdicts = map[string]verdict{}
	}
	if _, had := j.review.verdicts[key]; !had {
		j.review.order = append(j.review.order, key)
		if len(j.review.order) > maxVerdicts {
			delete(j.review.verdicts, j.review.order[0])
			j.review.order = j.review.order[1:]
		}
	}
	j.review.verdicts[key] = v
	j.mu.Unlock()
	return done(v)
}

// readVerdict reads a judge's answer: one JSON object with "ok" and
// "reason", wherever in the answer it stands.
func readVerdict(text string) (verdict, bool) {
	obj, ok := agent.JSONObject(text)
	if !ok {
		return verdict{}, false
	}
	var v struct {
		OK     *bool  `json:"ok"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal([]byte(obj), &v) != nil || v.OK == nil {
		return verdict{}, false
	}
	return verdict{ok: *v.OK, reason: strings.TrimSpace(v.Reason)}, true
}

// closesKept is the tools' ClosesKept slot: after a call whose closes
// were checked, mem says whether it kept the call. A top-level todo whose
// close the review approved in a kept call is not judged again as the
// turn ends; an approval in a call that was then refused or failed is
// forgotten.
func (j *join) closesKept(_ context.Context, _ string, callID string, kept bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	approved := j.review.asked[callID]
	delete(j.review.asked, callID)
	if !kept {
		return
	}
	for _, a := range approved {
		if j.review.passed == nil {
			j.review.passed = map[string]string{}
		}
		j.review.passed[a.todo] = a.result
	}
}

// reviewed reports whether the review approved the close of the top-level
// todo called name, in a call mem kept, with a result that mem still
// shows for it.
func (j *join) reviewed(name, show string) bool {
	j.mu.Lock()
	result, ok := j.review.passed[name]
	j.mu.Unlock()
	return ok && (result == "" || strings.Contains(show, result))
}

// closeDriver is the turn-end check that sends the model back, once a
// turn, when the turn would end while the session stands in a todo whose
// last close in this turn was refused and no later one was kept. Without
// it a model that was refused told the person "Fixed": mem stayed true,
// and the person was not.
func (j *join) closeDriver() agent.Driver {
	return agent.Driver{Name: "mem-close", Decide: j.closeSendBack}
}

func (j *join) closeSendBack(context.Context, agent.TurnEnd) (agent.Verdict, error) {
	stack := j.ts.Stack()
	if len(stack) == 0 || j.planMode() {
		// At the root there is nothing to go back to; and in plan mode the
		// model cannot close anything, so it is not sent back to try.
		return agent.Verdict{Action: agent.End}, nil
	}
	top := stack[len(stack)-1].ID
	j.mu.Lock()
	refusal, refused := j.turn.refused[top]
	sent := j.turn.sentBack
	if refused && !sent {
		j.turn.sentBack = true
	}
	j.mu.Unlock()
	if !refused || sent {
		return agent.Verdict{Action: agent.End}, nil
	}

	// Refused as never done, and nothing done since: the plain form.
	never := strings.Contains(refusal, "nothing came between your entering") || strings.Contains(refusal, "closed it with nothing done in it")
	if never {
		events := layout(j.ts.Entries(), j.id)
		for i := len(events) - 1; i >= 0; i-- {
			e := &events[i]
			if e.kind == evCall && e.failed {
				if _, todo, ok := refusalOf(e.text); ok && todo == top {
					// The refused call itself is looked at too: an
					// answer the model gave after it is the claim, and
					// no work.
					never = !worked(events[i:], top)
					break
				}
			}
		}
	}
	if never {
		return agent.Verdict{
			Action:    agent.Continue,
			Prompt:    j.texts.CloseSendBack(top, ""),
			Objection: fmt.Sprintf("mem refused to close `%s`: nothing was done in it", top),
			Notice:    fmt.Sprintf("mem refused to close `%s`, since nothing was done in it; the agent goes back to it", top),
		}, nil
	}
	return agent.Verdict{
		Action:    agent.Continue,
		Prompt:    j.texts.CloseSendBack(top, refusal),
		Objection: fmt.Sprintf("the close of `%s` was refused: %s", top, clipText(refusal, 300)),
		Notice:    fmt.Sprintf("The check refused to close `%s`, and the turn ended with it open; the agent goes back to it", top),
	}, nil
}

// numberRe is a number in a mem call's answer: a duration, an entry's
// number, a count.
var numberRe = regexp.MustCompile(`[0-9]+(\.[0-9]+)?`)

// repeatKey is Contribution.RepeatKey: how a gnovm_run call and its
// answer are compared when the loop looks for a model repeating itself.
// Every answer ends in a receipt that says how long the call ran, so a
// repeated call never looked repeated: on box 2 a model ran
// println("hello") on a scratch machine 46 times and was never told. The
// numbers of mem's answers are blanked first (they also number entries);
// on another machine, only those of the receipt, since what the program
// printed is its own.
func (j *join) repeatKey(call core.ToolCall, res core.ToolResult) (string, bool) {
	if call.Name != gnovmtools.RunToolName {
		return "", false
	}
	text := core.TextOf(res.Content)
	if _, ok := memCode(call.Input); ok {
		text = numberRe.ReplaceAllString(text, "#")
	} else {
		lines := strings.Split(text, "\n")
		for i, l := range lines {
			if strings.Contains(l, " gas (≈") {
				lines[i] = numberRe.ReplaceAllString(l, "#")
			}
		}
		text = strings.Join(lines, "\n")
	}
	failed := "ok"
	if res.IsError {
		failed = "error"
	}
	return call.Name + "\x00" + string(call.Input) + "\x00" + failed + "\x00" + text, true
}

// repeatNudge is Contribution.RepeatNudge: a loop whose repeated step is
// a refused close is told that the todo's work is still to do, with the
// refusal quoted. Told only "do not repeat", a model once sent one
// refused close three hundred times.
func (j *join) repeatNudge(rep agent.Repetition) string {
	for _, calls := range [][]agent.RepeatedCall{rep.Calls, rep.Other} {
		for _, c := range calls {
			if c.Call.Name != gnovmtools.RunToolName || !c.Result.IsError {
				continue
			}
			if line, _, ok := refusalOf(core.TextOf(c.Result.Content)); ok {
				return j.texts.RepeatRefusedClose(line)
			}
		}
	}
	return ""
}
