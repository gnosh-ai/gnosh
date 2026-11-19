package transcript

import (
	"encoding/json"
	"slices"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// ViewOptions says how a view is computed.
type ViewOptions struct {
	// Budget is the most tokens the view should hold, by Estimate's count.
	// When the view is over it and the session stands in a frame, the view
	// slides: the oldest rounds leave it (see doc.go). Zero means no limit.
	Budget int
	// Room is how much a slide frees beyond what is over the budget, so
	// that the rounds that follow have room. Zero means a tenth of Budget.
	Room int
	// Estimate counts a message's tokens. Nil means EstimateTokens.
	Estimate func(core.Message) int
	// KeepRounds is how many of the most recent rounds of the frame the
	// session stands in a slide always leaves. Zero means one.
	KeepRounds int
	// Prompt is the UUID of the prompt the model is answering, if any. A
	// slide never cuts it.
	Prompt string
	// Session is the id of the session whose frames make the stack. Empty
	// means the session of the last entry. Frames entered under another
	// session id (the session a branch came from) are not on the stack.
	Session string
	// Trace asks for ViewInfo.Sources and for what each Cut took: its
	// Removed, SparedEntries and Replies lists.
	Trace bool
	// SummaryLead is put in front of a summary's text when it becomes the
	// first message of the view, in place of SummaryPrefix: the caller's
	// own words, such as a line in its marks that says whose the summary
	// is. Empty means SummaryPrefix.
	SummaryLead string
}

// ViewInfo says what a view left out, and why.
type ViewInfo struct {
	// Messages and Tokens are the size of the view: its message count and
	// its tokens by the estimator.
	Messages int
	Tokens   int
	// OverBudget is true when the view is still over ViewOptions.Budget
	// after everything that could slide has.
	OverBudget bool

	// Rewound counts the user and assistant entries that rewinds cut off.
	Rewound int
	// IgnoredRewinds lists the UUIDs of rewind entries that name an entry
	// the session does not hold. They changed nothing.
	IgnoredRewinds []string

	// Summary is the UUID of the summary entry the view starts with, or
	// empty. Summarized counts the user and assistant entries behind it.
	Summary    string
	Summarized int

	// Stack is the frames the session stands in, bottom to top.
	Stack []Frame
	// Cuts lists every pop and slide that could take something out of the
	// view, in the order they were made.
	Cuts []Cut
	// UnmatchedPops lists the UUIDs of pop entries that name a frame which
	// was not on the stack. They changed nothing.
	UnmatchedPops []string
	// NewSlides holds the slide entries this call planned because the view
	// was over budget. The view already has them applied. Append them to
	// the session so that every later view cuts the same.
	NewSlides []Entry

	// Repairs lists what had to be changed to make the result a
	// conversation any provider accepts. Merged counts the messages that
	// were joined to the one before because both had the same role.
	Repairs []Repair
	Merged  int

	// Sources, when ViewOptions.Trace is set, holds for each message of the
	// view the UUIDs of the entries it came from, in order. A message the
	// view made up itself has none.
	Sources [][]string
}

// Frame is a frame the session stands in.
type Frame struct {
	// ID and Label are the frame's id and title.
	ID    string
	Label string
	// UUID is the push entry and ToolUseID the tool call that made it: the
	// frame's push call id.
	UUID      string
	ToolUseID string
	// Slid is true when the start of the frame has left the view to make
	// room since it was entered.
	Slid bool
	// TotalAtPush is the session's token total when the frame was entered:
	// the prompt and completion tokens of the last usage entry written
	// before the push. It is zero when no usage entry came before it.
	TotalAtPush int
}

// Cut is one pop or slide and what it took out of the view.
type Cut struct {
	// Kind is OpPop or OpSlide.
	Kind FrameOp
	// Frame, Label and Result are the frame's id and title and the pop's
	// result. A slide of the rounds before the first frame has no Frame.
	Frame  string
	Label  string
	Result string
	// UUID is the frame entry that made the cut. It is empty for a slide
	// planned by this call (Planned is true) and for an entry that Preview
	// was shown before it was appended. PushUUID is the entry that entered
	// the frame, for a pop.
	UUID     string
	PushUUID string
	Planned  bool
	// PushCall and PopCall are the two tool calls that bound the cut. For a
	// pop they are the call that entered the frame and the call that left
	// it. For a slide they are the first call of the reply whose round
	// stays in front of the cut (the frame's own push call when the slide
	// cut a frame's start), and the first call of the reply the cut ends
	// before (the push call of the frame entered there).
	PushCall string
	PopCall  string
	// PushReply and PopReply are the UUIDs of the replies that hold those
	// calls: the cut lies after the round of the first and before the
	// second. PopReply is empty when the reply that made the pop is not in
	// the conversation, and then nothing is cut. PushReply is empty when
	// the reply that made the push is not: the cut then begins at the first
	// reply after the push entry and leaves no note. When the two are the
	// same reply nothing is cut.
	PushReply string
	PopReply  string
	// Above lists the frames that stood above a popped frame on the stack.
	// They stay on it: a pop takes off one frame. mem pops only the frame
	// the session stands in, so this is empty unless the two disagreed.
	Above []string
	// Before is, for a slide of the rounds before the first frame, the
	// frame that was at the bottom of the stack then: the frame the
	// slide's line names.
	Before string
	// Between counts the messages that lie between the two rounds, apart
	// from the spared prompts and what continues a summary, whether or not
	// an earlier cut had taken them. Zero means nothing came between.
	Between int
	// Messages and Tokens count what this cut took out of the view that no
	// earlier cut had taken and no summary stood in for. Tokens is by
	// ViewOptions.Estimate, a row at a time.
	Messages int
	Tokens   int
	// Spared counts the prompts of the pop's own turn that stayed in the
	// view because of it.
	Spared int
	// Note is the line added to the answer in front of the cut, or empty.
	Note string
	// Live is true for a slide whose frame (Frame, or Before) is still on
	// the stack as the same frame: it was not popped since.
	Live bool
	// Reason says why the cut took nothing, when it took nothing.
	Reason string

	// The rest is set when ViewOptions.Trace is.

	// Removed lists the UUIDs of the entries inside the cut, in order,
	// whether or not an earlier cut had taken them.
	Removed []string
	// SparedEntries lists the UUIDs of the entries whose prompts Spared
	// counts.
	SparedEntries []string
	// Replies lists the replies of the model inside the cut, in order,
	// with their tool calls and what each call returned.
	Replies []Reply
}

// EditedPaths returns the file paths that the calls of the cut's replies
// to the named tools (the file-editing ones) wrote to and that succeeded,
// each once, in order, as the calls gave them. It needs a cut made with
// ViewOptions.Trace.
func (c Cut) EditedPaths(tools ...string) []string {
	var out []string
	for _, r := range c.Replies {
		for _, call := range r.Calls {
			if call.Path == "" || !call.Answered || call.IsError || !slices.Contains(tools, call.Name) || slices.Contains(out, call.Path) {
				continue
			}
			out = append(out, call.Path)
		}
	}
	return out
}

// Mark returns the marker the frame rule gives the entries this cut
// removed (spec/gnosh/frames.md sections 11 and 8.6): the frame's id for a
// pop, and "slide:<frame>:<push call id>:<pop call id>" for a slide. Where
// two cuts hold an entry, the one made last gives it its marker.
func (c Cut) Mark() string {
	if c.Kind == OpSlide {
		return "slide:" + c.Frame + ":" + c.PushCall + ":" + c.PopCall
	}
	return c.Frame
}

// Reply is one reply of the model that a cut took out of the view.
type Reply struct {
	// UUID is the assistant entry.
	UUID string
	// Said is true when the reply holds text. A reply that says something
	// and calls no tool is an answer to the person.
	Said bool
	// Calls are the reply's tool calls, in order.
	Calls []Call
	// Gone is true when the reply was out of the view already: an earlier
	// cut had taken it, or it is behind the summary.
	Gone bool
}

// Call is one tool call of a Reply, with its result.
type Call struct {
	// ID and Name are the call's id and the tool it asked for.
	ID   string
	Name string
	// Input is the call's arguments. It is shared with the entry and must
	// not be changed.
	Input json.RawMessage
	// Path is the call's "file_path" argument, or empty when it has none.
	Path string
	// Answered is true when the conversation holds a result for the call.
	// IsError is that result's failure mark and Content what it says; a
	// call with no result never ran to its end. Content is shared with
	// the entry and must not be changed.
	Answered bool
	IsError  bool
	Content  []core.Block
	// ResultUUID is the user entry that holds the result, or empty.
	ResultUUID string
}

// SummaryPrefix is the fixed sentence put in front of a summary's text
// when it becomes the first message of the view.
const SummaryPrefix = "The earlier part of this conversation is no longer shown in full. What follows is a summary of it.\n\n"

// View computes the messages the model is sent from a session's entries.
// It applies, in order: rewinds; the latest summary; the cuts of popped
// frames and recorded slides; a new slide when the result is over budget;
// and repairs that make the result a valid conversation. It is a pure
// function: it reads nothing but its arguments, changes none of them, and
// gives the same answer every time. doc.go has the rules in full.
func View(entries []Entry, opts ViewOptions) ([]core.Message, ViewInfo) {
	var info ViewInfo
	c := newConv(entries, opts, &info)
	st := c.derive()
	for i := range st.cuts {
		c.apply(&st.cuts[i])
	}
	if opts.Budget > 0 {
		if over := c.total() - opts.Budget; over > 0 {
			room := opts.Room
			if room <= 0 {
				room = opts.Budget / 10
			}
			for _, ct := range c.planSlides(st, over+room, opts) {
				st.cuts = append(st.cuts, ct)
				info.NewSlides = append(info.NewSlides, c.slideEntry(&ct))
			}
		}
	}
	c.report(st, &info)

	msgs, srcs := repair(c.pieces(), &info)
	info.Messages = len(msgs)
	for _, m := range msgs {
		info.Tokens += c.est(m)
	}
	info.OverBudget = opts.Budget > 0 && info.Tokens > opts.Budget
	if opts.Trace {
		info.Sources = make([][]string, len(srcs))
		for i, src := range srcs {
			info.Sources[i] = c.uuids(src)
		}
	}
	return msgs, info
}

// Stack returns the frames the session stands in, bottom to top, without
// computing a view. session is as ViewOptions.Session. It is what mem is
// told before each of its calls.
func Stack(entries []Entry, session string) []Frame {
	var info ViewInfo
	c := newConv(entries, ViewOptions{Session: session}, &info)
	return c.frames(c.derive())
}

// Preview says what the frame entries among next would do if next were
// appended to entries now, without appending anything. It returns one Cut
// for each pop and each slide among them, in order, and the stack as it
// would then stand. Every Cut is as View would report it with
// ViewOptions.Trace once the entries are stored: where next comes to stand
// after the reply that made the call does not change what is cut. A pop
// that names a frame the session does not stand in gets a Cut that says so
// in Reason and takes nothing.
//
// This is how a pop's result is written before the pop is stored: the
// reply that made the call must be among entries; the call's own result
// need not be. It also gives the rows a pop would take, for judging whether
// any work was done in a frame, by previewing a pop that is never
// appended.
//
// An entry of next with no session id is taken to be the session's own:
// that of opts.Session, or of the last of entries. Of opts, Estimate and
// Session are used.
func Preview(entries, next []Entry, opts ViewOptions) ([]Cut, []Frame) {
	session := opts.Session
	if session == "" && len(entries) > 0 {
		session = entries[len(entries)-1].SessionID
	}
	all := make([]Entry, len(entries), len(entries)+len(next))
	copy(all, entries)
	for _, e := range next {
		if e.SessionID == "" {
			e.SessionID = session
		}
		all = append(all, e)
	}
	var info ViewInfo
	c := newConv(all, ViewOptions{Estimate: opts.Estimate, Session: session, Trace: true}, &info)
	st := c.derive()
	for i := range st.cuts {
		c.apply(&st.cuts[i])
	}
	made := map[int]int{} // by entry, the cut it made
	for n, ev := range c.events {
		if ev.ent >= len(entries) {
			made[ev.ent] = st.made[n]
		}
	}
	var cuts []Cut
	for i := range next {
		e := &all[len(entries)+i]
		if e.Type != TypeFrame || e.Op != OpPop && e.Op != OpSlide {
			continue
		}
		if k, ok := made[len(entries)+i]; ok && k >= 0 {
			cuts = append(cuts, c.cutInfo(&st.cuts[k]))
			continue
		}
		why := "the session does not stand in this frame"
		if _, ok := made[len(entries)+i]; !ok {
			why = "the entry would not be part of the conversation"
		}
		cuts = append(cuts, Cut{Kind: e.Op, Frame: e.Frame, Label: e.Label, Result: e.Result, PopCall: e.ToolUseID, Reason: why})
	}
	return cuts, c.frames(st)
}

// Slide plans a slide that frees about need tokens. It returns the slide
// entries to append to the session and, beside each, what it takes out of
// the view; or nothing when nothing can slide (the session stands in no
// frame, or what is left is too little to cut). It is what View does when
// it is over budget, for a caller that decides from the provider's own
// token counts instead. Of opts it uses Estimate, KeepRounds, Prompt,
// Session and Trace.
func Slide(entries []Entry, need int, opts ViewOptions) (slides []Entry, cuts []Cut) {
	var info ViewInfo
	c := newConv(entries, opts, &info)
	st := c.derive()
	for i := range st.cuts {
		c.apply(&st.cuts[i])
	}
	for _, ct := range c.planSlides(st, need, opts) {
		slides = append(slides, c.slideEntry(&ct))
		cuts = append(cuts, c.cutInfo(&ct))
	}
	return slides, cuts
}

// Conversation returns the entries that are still part of the conversation
// after every rewind, in order: what the session's file holds, less what
// rewinds cut off and the rewind entries themselves.
func Conversation(entries []Entry) []Entry {
	var info ViewInfo
	alive := survivors(entries, &info)
	out := make([]Entry, len(alive))
	for i, idx := range alive {
		out[i] = entries[idx]
	}
	return out
}

// survivors applies the rewinds: it returns the indices of the entries
// that are still part of the conversation, in order.
//
// Every entry is written after the tip of the conversation as it then
// stood. A rewind entry moves the tip back to where it was right after the
// entry it names, so what came between is off the conversation, though
// still in the file. Rewinding to an entry that an earlier rewind cut off
// brings its line of the conversation back.
func survivors(entries []Entry, info *ViewInfo) []int {
	targets := map[string]int{}
	for i := range entries {
		if entries[i].Type == TypeRewind && entries[i].To != "" {
			targets[entries[i].To] = -2
		}
	}
	if len(targets) == 0 {
		// No rewind names anything: every entry survives but the rewind
		// entries themselves, which are reported as ignored.
		alive := make([]int, 0, len(entries))
		for i := range entries {
			if entries[i].Type == TypeRewind {
				info.IgnoredRewinds = append(info.IgnoredRewinds, entries[i].UUID)
				continue
			}
			alive = append(alive, i)
		}
		return alive
	}
	// prev[i] is the entry before i on i's line of the conversation.
	prev := make([]int, len(entries))
	tip := -1
	for i := range entries {
		e := &entries[i]
		if e.Type == TypeRewind {
			if to, ok := targets[e.To]; ok && to != -2 {
				tip = to
			} else {
				info.IgnoredRewinds = append(info.IgnoredRewinds, e.UUID)
			}
		} else {
			prev[i] = tip
			tip = i
		}
		// The tip right after this entry is where a rewind to it goes.
		if _, wanted := targets[e.UUID]; wanted && e.UUID != "" {
			targets[e.UUID] = tip
		}
	}
	var alive []int
	for i := tip; i >= 0; i = prev[i] {
		alive = append(alive, i)
	}
	for l, r := 0, len(alive)-1; l < r; l, r = l+1, r-1 {
		alive[l], alive[r] = alive[r], alive[l]
	}
	messages := 0
	for i := range entries {
		if entries[i].IsMessage() {
			messages++
		}
	}
	for _, i := range alive {
		if entries[i].IsMessage() {
			messages--
		}
	}
	info.Rewound = messages
	return alive
}

// rowKind sorts the rows of a conversation.
type rowKind uint8

const (
	// rowPrompt is user-side text: a prompt the person typed, or something
	// the harness or a hook said (hidden).
	rowPrompt rowKind = iota
	// rowAssistant is one reply of the model.
	rowAssistant
	// rowResult is the results that answer a reply's tool calls.
	rowResult
)

// row is one row of the conversation, the unit the frame rule speaks of.
// An assistant entry is one row. A user entry is a result row, a prompt
// row, or one of each when it holds both tool results and other blocks.
type row struct {
	kind   rowKind
	ent    int          // the entry it came from: an index into entries
	blocks []core.Block // the row's blocks; never modified
	hidden bool         // a prompt row the person did not type
	calls  int          // an assistant row's tool calls
	ended  bool         // an assistant row whose reply was cancelled or failed
}

// event is a frame entry and where it falls among the rows.
type event struct {
	at    int // how many rows come before it
	ent   int // the entry: an index into entries
	total int // the session's token total by the last usage entry before it
}

// conv is a conversation laid out as rows, with what has been cut from it.
type conv struct {
	entries []Entry
	rows    []row
	events  []event
	// start is the first row the model can still have: the rows before it
	// are behind the summary. cont is the first assistant row at or after
	// start: the rows between continue the summary and are never cut.
	start, cont int
	// summary is the summary entry the view starts with, or -1.
	summary int
	// session is the id of the session whose frames make the stack.
	session string
	// trace says the caller wants to know what each cut took.
	trace bool
	// lead is what stands before a summary's text (ViewOptions.SummaryLead).
	lead string
	// halts are the places, each as the number of rows before it, of
	// replies that hold nothing and were cancelled or failed. Such a reply
	// is not a row, since the model is not sent it, but it ended its turn.
	// endedBehind says the same of the last reply behind the summary.
	halts       []int
	endedBehind bool

	est     func(core.Message) int
	tokens  []int          // per row, by est; -1 until asked for
	removed []bool         // per row: a cut took it out of the view
	notes   map[int][]note // per result row: the lines added to its answers

	// The walk over the frame entries keeps two tables of the assistant
	// rows it has passed: the latest row that holds each tool call id, and
	// (when the conversation has slides) the row of each entry.
	walked int
	calls  map[string]int
	byUUID map[string]int
	// turns lists the runs of prompts that start a turn. It is made the
	// first time a pop asks where its turn began.
	turns      []turn
	turnsBuilt bool
}

// newConv lays the entries out as rows.
func newConv(entries []Entry, opts ViewOptions, info *ViewInfo) *conv {
	c := &conv{entries: entries, summary: -1, est: opts.Estimate, session: opts.Session, trace: opts.Trace, lead: opts.SummaryLead}
	if c.est == nil {
		c.est = EstimateTokens
	}
	alive := survivors(entries, info)
	if c.session == "" && len(entries) > 0 {
		c.session = entries[len(entries)-1].SessionID
	}

	// The view starts at the latest summary that survived. covered is the
	// position in alive of the last entry it stands in for.
	covered := -1
	for p := len(alive) - 1; p >= 0; p-- {
		e := &entries[alive[p]]
		if e.Type != TypeSummary {
			continue
		}
		c.summary = alive[p]
		covered = p - 1
		if e.Covers != "" {
			for q := p - 1; q >= 0; q-- {
				if entries[alive[q]].UUID == e.Covers {
					covered = q
					break
				}
			}
		}
		info.Summary = e.UUID
		break
	}

	total := 0 // the session's token total by the last usage entry so far
	for p, i := range alive {
		e := &entries[i]
		switch e.Type {
		case TypeAssistant:
			ended := e.Stop == core.StopCancelled || e.Stop == core.StopError
			if len(e.Message.Blocks) > 0 {
				c.rows = append(c.rows, row{
					kind:   rowAssistant,
					ent:    i,
					blocks: e.Message.Blocks,
					calls:  countToolUses(e.Message.Blocks),
					ended:  ended,
				})
			} else if ended && p > covered {
				c.halts = append(c.halts, len(c.rows))
			}
			if p <= covered && (ended || len(e.Message.Blocks) > 0) {
				c.endedBehind = ended
			}
		case TypeUser:
			results, rest := splitResults(e.Message.Blocks)
			if len(results) > 0 {
				c.rows = append(c.rows, row{kind: rowResult, ent: i, blocks: results})
			}
			if len(rest) > 0 {
				c.rows = append(c.rows, row{kind: rowPrompt, ent: i, blocks: rest, hidden: e.Source != SourcePrompt})
			}
		case TypeFrame:
			c.events = append(c.events, event{at: len(c.rows), ent: i, total: total})
			if e.Op == OpSlide && c.byUUID == nil {
				c.byUUID = map[string]int{}
			}
		case TypeUsage:
			total = e.Usage.PromptTokens() + e.Usage.OutputTokens
		}
		if p <= covered {
			c.start = len(c.rows)
			if e.IsMessage() {
				info.Summarized++
			}
		}
	}
	c.cont = c.start
	for c.cont < len(c.rows) && c.rows[c.cont].kind != rowAssistant {
		c.cont++
	}
	c.tokens = make([]int, len(c.rows))
	for i := range c.tokens {
		c.tokens[i] = -1
	}
	c.removed = make([]bool, len(c.rows))
	c.calls = map[string]int{}
	return c
}

// countToolUses counts the tool calls among blocks.
func countToolUses(blocks []core.Block) int {
	n := 0
	for i := range blocks {
		if blocks[i].Type == core.BlockToolUse {
			n++
		}
	}
	return n
}

// splitResults separates a user message's tool results from its other
// blocks. It copies only when the message holds both.
func splitResults(blocks []core.Block) (results, rest []core.Block) {
	n := 0
	for i := range blocks {
		if blocks[i].Type == core.BlockToolResult {
			n++
		}
	}
	switch n {
	case 0:
		return nil, blocks
	case len(blocks):
		return blocks, nil
	}
	results = make([]core.Block, 0, n)
	rest = make([]core.Block, 0, len(blocks)-n)
	for i := range blocks {
		if blocks[i].Type == core.BlockToolResult {
			results = append(results, blocks[i])
		} else {
			rest = append(rest, blocks[i])
		}
	}
	return results, rest
}

// role returns the role of the message a row belongs to.
func (r *row) role() core.Role {
	if r.kind == rowAssistant {
		return core.RoleAssistant
	}
	return core.RoleUser
}

// present reports whether the model still has row r: it is not behind the
// summary and no cut has taken it.
func (c *conv) present(r int) bool {
	return r >= c.start && !c.removed[r]
}

// rowTokens returns the estimated tokens of row r.
func (c *conv) rowTokens(r int) int {
	if c.tokens[r] < 0 {
		c.tokens[r] = c.est(core.Message{Role: c.rows[r].role(), Blocks: c.rows[r].blocks})
	}
	return c.tokens[r]
}

// span returns the estimated tokens of the rows in [from, to) that the
// model still has.
func (c *conv) span(from, to int) int {
	n := 0
	for r := max(from, c.start); r < to; r++ {
		if !c.removed[r] {
			n += c.rowTokens(r)
		}
	}
	return n
}

// total returns the estimated tokens of the view as it stands.
func (c *conv) total() int {
	n := c.span(c.start, len(c.rows))
	if c.summary >= 0 {
		n += c.est(c.summaryMessage())
	}
	return n
}

// summaryMessage is the message the summary becomes.
func (c *conv) summaryMessage() core.Message {
	lead := c.lead
	if lead == "" {
		lead = SummaryPrefix
	}
	return core.UserText(lead + c.entries[c.summary].Summary)
}

// piece is one message of the view before repair, with the entries it
// came from.
type piece struct {
	role   core.Role
	blocks []core.Block
	src    []int
}

// pieces returns the view before repair: the summary, then every row the
// model still has, each as a message of its own.
func (c *conv) pieces() []piece {
	var out []piece
	if c.summary >= 0 {
		m := c.summaryMessage()
		out = append(out, piece{role: m.Role, blocks: m.Blocks, src: []int{c.summary}})
	}
	for r := c.start; r < len(c.rows); r++ {
		if c.removed[r] {
			continue
		}
		row := &c.rows[r]
		out = append(out, piece{role: row.role(), blocks: c.withNotes(r), src: []int{row.ent}})
	}
	return out
}

// uuids turns entry indices into the entries' UUIDs, leaving out repeats
// that follow one another.
func (c *conv) uuids(src []int) []string {
	out := make([]string, 0, len(src))
	last := -1
	for _, i := range src {
		if i != last {
			out = append(out, c.entries[i].UUID)
		}
		last = i
	}
	return out
}

// EstimateTokens is the default estimator: a rough count of a message's
// tokens from the size of what it holds, at four bytes to a token, with a
// flat 1,500 for an image.
func EstimateTokens(m core.Message) int {
	return 4 + estimateBlocks(m.Blocks)
}

func estimateBlocks(blocks []core.Block) int {
	n := 0
	for i := range blocks {
		b := &blocks[i]
		size := len(b.Text) + len(b.Signature) + len(b.Opaque) + len(b.ID) + len(b.Name) + len(b.Input) + len(b.ToolUseID)
		n += 3 + (size+3)/4
		if b.Type == core.BlockImage {
			n += 1500
		}
		n += estimateBlocks(b.Content)
	}
	return n
}
