package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/gnovm"
	gnovmtools "github.com/charmbracelet/crush/internal/gnovm/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
)

// frameStacks is what gnosh keeps of the sessions' frames in mem
// (FRAMES.md): nothing of its own. A session's stack is derived from its
// rows, the gnovm_run results whose metadata carries the frames a call to
// mem pushed and popped, each stamped with the session that made it, so a
// copied row's push is told from the session's own. The stack is the
// pushes without a matching pop, in order; it is derived once, kept, and
// updated as calls happen. On a pop, the rows between the push's round
// and the pop's are marked as removed from the model's context, the reads
// and views made in them forgotten, and the model told what that changed.
type frameStacks struct {
	sessions session.Service
	messages message.Service
	// files forgets the reads made inside a popped frame; nil forgets none.
	files filetracker.Service
	// forgetGiven forgets the views and the subdirectory context files
	// given in removed rows (coordinator.forgetGiven); nil forgets none.
	forgetGiven func(sessionID string, removed []string)
	// notify tells the user of a pop, for a non-interactive run; nil tells
	// no one.
	notify     pubsub.Publisher[notify.Notification]
	workingDir string

	mu      sync.Mutex
	records map[string]*sessionFrames
}

// sessionFrames is one session's frames: its stack, every pop seen, for
// the cut, and what pops removed that the session's counts do not show yet.
type sessionFrames struct {
	stack []*stackFrame
	pops  []poppedRange
	// estimate is about how many tokens the pops removed from the model's
	// context since the last usage report that counted the cut list.
	estimate int64
}

// stackFrame is a frame on a session's stack.
type stackFrame struct {
	id, purpose string
	// callID is the gnovm_run call that pushed it: its round is where the
	// context is cut.
	callID string
	// tokens is the session's running total at the push, for the line that
	// says the step is large.
	tokens int64
	// warned is set once that line was given for this frame.
	warned bool
}

// poppedRange is a pop: everything between the round of pushCallID and
// the round of popCallID is removed from the model's context. pushCallID
// is "" when the push was not found among the rows.
type poppedRange struct {
	frame      string
	pushCallID string
	popCallID  string
}

func newFrameStacks(sessions session.Service, messages message.Service, files filetracker.Service, workingDir string) *frameStacks {
	return &frameStacks{sessions: sessions, messages: messages, files: files, workingDir: workingDir, records: map[string]*sessionFrames{}}
}

// record is the session's frames, derived from its rows when not kept
// yet. The caller holds f.mu.
func (f *frameStacks) record(ctx context.Context, sessionID string) *sessionFrames {
	if rec, ok := f.records[sessionID]; ok {
		return rec
	}
	rec := &sessionFrames{}
	msgs, err := f.messages.List(ctx, sessionID)
	if err != nil {
		slog.Warn("Could not read a session's rows to derive its frames", "session_id", sessionID, "error", err)
		return rec
	}
	var tokens int64
	if sess, err := f.sessions.Get(ctx, sessionID); err == nil {
		tokens = sess.PromptTokens + sess.CompletionTokens
	}
	for _, msg := range msgs {
		if msg.Role != message.Tool {
			continue
		}
		for _, result := range msg.ToolResults() {
			for _, ev := range frameEvents(result) {
				if ev.Session == sessionID {
					rec.apply(ev, result.ToolCallID, tokens)
				}
			}
		}
	}
	f.records[sessionID] = rec
	return rec
}

// frameEvents is the frames a gnovm_run result's metadata carries.
func frameEvents(result message.ToolResult) []gnovm.MemFrame {
	if result.Name != gnovmtools.RunToolName || result.IsError || result.Metadata == "" {
		return nil
	}
	var meta gnovmtools.RunResponseMetadata
	if json.Unmarshal([]byte(result.Metadata), &meta) != nil {
		return nil
	}
	return meta.Frames
}

// apply puts a push on the stack, or takes a pop's frame off it and
// records the pop's range; it returns the frame a pop took off, or nil.
func (rec *sessionFrames) apply(ev gnovm.MemFrame, callID string, tokens int64) *stackFrame {
	switch ev.Kind {
	case "push":
		rec.stack = append(rec.stack, &stackFrame{id: ev.Id, purpose: ev.Purpose, callID: callID, tokens: tokens})
	case "pop":
		for i := len(rec.stack) - 1; i >= 0; i-- {
			if rec.stack[i].id != ev.Id {
				continue
			}
			popped := rec.stack[i]
			rec.stack = slices.Delete(rec.stack, i, i+1)
			rec.pops = append(rec.pops, poppedRange{frame: ev.Id, pushCallID: popped.callID, popCallID: callID})
			return popped
		}
	}
	return nil
}

// forget drops what is kept of the session: its stack is derived again
// from its rows when next asked, as after a rewind.
func (f *frameStacks) forget(sessionID string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, sessionID)
}

// stack is the session's stack of frames, bottom to top: what gnovm_run
// tells mem before each call (gnovmtools.MemStack).
func (f *frameStacks) stack(ctx context.Context, sessionID string) []string {
	if f == nil || sessionID == "" {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.record(ctx, sessionID)
	if len(rec.stack) == 0 {
		return nil
	}
	ids := make([]string, len(rec.stack))
	for i, fr := range rec.stack {
		ids[i] = fr.id
	}
	return ids
}

// top is a copy of the frame on top of the session's stack, and false
// with an empty stack.
func (f *frameStacks) top(ctx context.Context, sessionID string) (stackFrame, bool) {
	if f == nil || sessionID == "" {
		return stackFrame{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.record(ctx, sessionID)
	if len(rec.stack) == 0 {
		return stackFrame{}, false
	}
	return *rec.stack[len(rec.stack)-1], true
}

// ranges is every pop of the session, for the cut (cutPopped).
func (f *frameStacks) ranges(ctx context.Context, sessionID string) []poppedRange {
	if f == nil || sessionID == "" {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.record(ctx, sessionID).pops)
}

// estimate is about how many tokens pops removed from the session's
// context that its counts, from the last usage report, still hold.
func (f *frameStacks) estimate(sessionID string) int64 {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if rec, ok := f.records[sessionID]; ok {
		return rec.estimate
	}
	return 0
}

// counted records that a usage report counted a list cut by spent tokens:
// that much of the estimate is spent; a pop made since stays held.
func (f *frameStacks) counted(sessionID string, spent int64) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if rec, ok := f.records[sessionID]; ok {
		rec.estimate = max(rec.estimate-spent, 0)
	}
}

// events is what gnovm_run tells of the frames a call to mem pushed and
// popped (gnovmtools.OnMemFrames): each is applied to the session's stack,
// and for each pop the rows between the push's round and the pop's are
// removed from the model's context (pop). It answers what the call's
// result tells the model of that, one line per pop, "" for none.
func (f *frameStacks) events(ctx context.Context, sessionID, callID string, frames []gnovm.MemFrame) string {
	if f == nil || sessionID == "" {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.record(ctx, sessionID)
	var tokens int64
	if sess, err := f.sessions.Get(ctx, sessionID); err == nil {
		tokens = sess.PromptTokens + sess.CompletionTokens
	}
	var lines []string
	for _, ev := range frames {
		if ev.Session != sessionID {
			continue
		}
		popped := rec.apply(ev, callID, tokens)
		if ev.Kind != "pop" {
			continue
		}
		pushCallID := ""
		if popped != nil {
			pushCallID = popped.callID
		}
		lines = append(lines, f.pop(ctx, sessionID, rec, ev, pushCallID, callID))
	}
	return strings.Join(lines, "\n")
}

// pop removes from the model's context everything between the round of
// the push (pushCallID) and the round of the pop (popCallID) of frame ev:
// the rows are marked (message.Popped), the reads of the files they
// touched and the views they were given are forgotten, and what they
// held is estimated against the session's counts. It answers the clauses
// the model reads with mem's "done" line: what was removed, where the
// frame's thoughts are, the files edited, the other changes to mem, and
// that the reads are forgotten; or that nothing came between.
func (f *frameStacks) pop(ctx context.Context, sessionID string, rec *sessionFrames, ev gnovm.MemFrame, pushCallID, popCallID string) string {
	if err := f.messages.FlushAll(ctx); err != nil {
		slog.Warn("Could not flush the session's rows before a pop", "session_id", sessionID, "error", err)
	}
	msgs, err := f.messages.List(ctx, sessionID)
	if err != nil {
		slog.Warn("Could not read the session's rows for a pop", "session_id", sessionID, "error", err)
		return ""
	}
	rows := poppedRows(msgs, pushCallID, popCallID)
	if len(rows) == 0 {
		return "nothing came between, so nothing was removed from your context."
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	if err := f.messages.SetPopped(ctx, ids, ev.Id); err != nil {
		slog.Warn("Could not mark the rows a pop removed", "session_id", sessionID, "frame", ev.Id, "error", err)
	}
	calls := map[string]message.ToolCall{}
	for _, row := range rows {
		for _, call := range row.ToolCalls() {
			calls[call.ID] = call
		}
	}
	// The reads made inside the frame are forgotten, by path: the model no
	// longer has what it read, so an edit of the file waits for a view.
	reads := readPaths(rows, calls, f.workingDir)
	if f.files != nil && len(reads) > 0 {
		if err := f.files.ForgetReads(ctx, sessionID, reads); err != nil {
			slog.Warn("Could not forget the reads made inside a popped frame", "session_id", sessionID, "error", err)
		}
	}
	if f.forgetGiven != nil {
		f.forgetGiven(sessionID, ids)
	}
	// Rows an inner pop removed already are out of the context: they are
	// marked again, not counted again.
	var removed int64
	for _, row := range rows {
		if row.Popped == "" {
			removed += estimateMessageTokens(row.ToAIMessage())
		}
	}
	rec.estimate += removed
	if f.notify != nil {
		f.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID: sessionID,
			Type:      notify.TypeFramePopped,
			Message:   fmt.Sprintf("popped `%s`: about %s tokens out of the model's context", ev.Id, roughCount(removed)),
		})
	}
	return popClauses(ev, rows, calls, f.workingDir, len(reads) > 0)
}

// poppedRows is the rows a pop removes from the model's context: those
// after the round of the push (the assistant row holding call pushCallID
// and the tool rows answering its calls) and before the assistant row
// holding call popCallID. Never the summary row, nor the prompts that
// continue it: a push older than the summary removes everything from the
// first of the model's own rows after it. None when nothing came between,
// or the pop's row is not there.
func poppedRows(msgs []message.Message, pushCallID, popCallID string) []message.Message {
	end := slices.IndexFunc(msgs, func(m message.Message) bool { return m.Role == message.Assistant && hasToolCall(m, popCallID) })
	if end < 0 {
		return nil
	}
	start := -1
	if pushCallID != "" {
		if push := slices.IndexFunc(msgs, func(m message.Message) bool { return m.Role == message.Assistant && hasToolCall(m, pushCallID) }); push >= 0 {
			if push == end {
				return nil
			}
			start = push
			answers := map[string]bool{}
			for _, call := range msgs[push].ToolCalls() {
				answers[call.ID] = true
			}
			for i := push + 1; i < end; i++ {
				if msgs[i].Role == message.Tool && slices.ContainsFunc(msgs[i].ToolResults(), func(r message.ToolResult) bool { return answers[r.ToolCallID] }) {
					start = i
				}
			}
		}
	}
	// With no push found, the cut starts after the summary, or at the
	// first of the model's own rows.
	afterSummary := start < 0
	if start < 0 {
		for i := end - 1; i >= 0; i-- {
			if msgs[i].IsSummaryMessage {
				start = i
				break
			}
		}
	}
	var rows []message.Message
	for i := start + 1; i < end; i++ {
		row := msgs[i]
		if row.IsSummaryMessage {
			afterSummary = true
			continue
		}
		if afterSummary {
			// The prompts that continue the summary stay: the cut starts
			// at the model's first own row after it.
			if row.Role != message.Assistant {
				continue
			}
			afterSummary = false
		}
		rows = append(rows, row)
	}
	return rows
}

func hasToolCall(m message.Message, callID string) bool {
	return slices.ContainsFunc(m.ToolCalls(), func(c message.ToolCall) bool { return c.ID == callID })
}

// readPaths is the files the removed rows read: the file tools' file_path
// arguments, and the file a bash command showed (fileViewedByBash), as the
// file tracker keeps them.
func readPaths(rows []message.Message, calls map[string]message.ToolCall, workingDir string) []string {
	var paths []string
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" {
			return
		}
		path = normalizePath(workingDir, path)
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	for _, row := range rows {
		for _, call := range row.ToolCalls() {
			switch call.Name {
			case tools.ViewToolName, tools.EditToolName, tools.MultiEditToolName, tools.WriteToolName:
				var params struct {
					FilePath string `json:"file_path"`
				}
				if json.Unmarshal([]byte(call.Input), &params) == nil {
					add(params.FilePath)
				}
			}
		}
		for _, result := range row.ToolResults() {
			if result.Name == tools.BashToolName {
				call := calls[result.ToolCallID]
				add(fileViewedByBash(fantasy.ToolCall{ID: call.ID, Name: call.Name, Input: call.Input}, fantasy.ToolResponse{Metadata: result.Metadata}))
			}
		}
	}
	return paths
}

// popClauses is what the model reads of a pop besides mem's own "done"
// line: that everything since the push was removed, where the frame's
// thoughts are, the files its rounds edited, the other calls to mem in
// them, and that their reads are forgotten.
func popClauses(ev gnovm.MemFrame, rows []message.Message, calls map[string]message.ToolCall, workingDir string, reads bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Everything since you entered `%s` was removed from your context: this result stays", ev.Id)
	switch ev.Thoughts {
	case 0:
	case 1:
		fmt.Fprintf(&b, ", and its 1 thought is in mem (mem.Show(%q) lists it when you need it)", ev.Id)
	default:
		fmt.Fprintf(&b, ", and its %d thoughts are in mem (mem.Show(%q) lists them when you need them)", ev.Thoughts, ev.Id)
	}
	b.WriteString("; the user still sees all of it.")
	if edited := editedFiles(rows, calls, workingDir); len(edited) > 0 {
		b.WriteString(" In it you edited " + strings.Join(edited, ", ") + ".")
	}
	if changes := memChanges(rows); len(changes) > 0 {
		b.WriteString(" In it you also changed mem: " + fewOf(changes, 4) + ".")
	}
	if reads {
		b.WriteString(" Your reads inside it are forgotten: view a file before you edit it.")
	}
	return b.String()
}

// editedFiles is the files the file tools changed in rows, as /rewind
// finds them (tools.ChangedFiles), each once, relative to the working
// directory where under it.
func editedFiles(rows []message.Message, calls map[string]message.ToolCall, workingDir string) []string {
	var files []string
	seen := map[string]bool{}
	for _, row := range rows {
		for _, result := range row.ToolResults() {
			if result.IsError {
				continue
			}
			for _, f := range tools.ChangedFiles(result.Name, calls[result.ToolCallID].Input, result.Metadata) {
				if f.Path == "" {
					continue
				}
				path := normalizePath(workingDir, f.Path)
				if seen[path] {
					continue
				}
				seen[path] = true
				if rel, err := filepath.Rel(workingDir, path); err == nil && !strings.HasPrefix(rel, "..") {
					path = rel
				}
				files = append(files, path)
			}
		}
	}
	return files
}

// memStatement is a call to mem in an input, by its first word and the
// name it was given: mem.Fact("port", …).
var memStatement = regexp.MustCompile(`(?m)^\s*mem\.([A-Z]\w*)\(\s*(?:"((?:[^"\\]|\\.)*)")?`)

// memChanges is the calls to mem in rows' inputs that changed it, other
// than Think and the frame calls themselves, by first word and name:
// "Fact `port`". A read (Show, Find, …) changed nothing and is left out.
func memChanges(rows []message.Message) []string {
	var changes []string
	for _, row := range rows {
		for _, call := range row.ToolCalls() {
			if !isMemCall(call) {
				continue
			}
			var input struct {
				Code string `json:"code"`
			}
			if json.Unmarshal([]byte(call.Input), &input) != nil {
				continue
			}
			for _, m := range memStatement.FindAllStringSubmatch(input.Code, -1) {
				switch m[1] {
				case "Think", "Push", "Pop", "Frame", "Thought", "Recall",
					"Show", "Find", "Next", "JSON", "Check", "String", "Bind", "Apply":
					continue
				}
				change := m[1]
				if m[2] != "" {
					change += " `" + m[2] + "`"
				}
				changes = append(changes, change)
			}
		}
	}
	return changes
}

// fewOf is the first n of list, joined, with how many more there are.
func fewOf(list []string, n int) string {
	if len(list) <= n {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:n], ", ") + " and " + strconv.Itoa(len(list)-n) + " more"
}

// roughCount is n as the user reads a token count: 850, 44K, 1.2M.
func roughCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%dK", (n+500)/1000)
	}
	return strconv.FormatInt(n, 10)
}

// cutPopped is msgs, the messages a step sends, without those a pop
// removed from the model's context: for each pop, everything after the
// round of its push (the assistant message holding the push call and the
// tool message answering it) and before the round of its pop. A pop whose
// push is not among msgs, older than the summary, removes everything from
// the first assistant message on. Nested pops remove the union. The list
// that remains keeps a tool message followed by an assistant message, so
// it is valid for every provider.
func cutPopped(msgs []fantasy.Message, ranges []poppedRange) []fantasy.Message {
	if len(ranges) == 0 {
		return msgs
	}
	skip := make([]bool, len(msgs))
	cut := false
	for _, r := range ranges {
		end := indexOfCall(msgs, r.popCallID)
		if end < 0 {
			continue
		}
		from := -1
		if r.pushCallID != "" {
			if push := indexOfCall(msgs, r.pushCallID); push >= 0 {
				if push == end {
					continue
				}
				from = push + 1
				if from < len(msgs) && msgs[from].Role == fantasy.MessageRoleTool {
					from++
				}
			}
		}
		if from < 0 {
			from = slices.IndexFunc(msgs, func(m fantasy.Message) bool { return m.Role == fantasy.MessageRoleAssistant })
			if from < 0 {
				continue
			}
		}
		for i := from; i < end; i++ {
			skip[i] = true
			cut = true
		}
	}
	if !cut {
		return msgs
	}
	kept := make([]fantasy.Message, 0, len(msgs))
	for i, m := range msgs {
		if !skip[i] {
			kept = append(kept, m)
		}
	}
	return kept
}

// indexOfCall is the index in msgs of the assistant message holding the
// tool call callID, or -1.
func indexOfCall(msgs []fantasy.Message, callID string) int {
	if callID == "" {
		return -1
	}
	return slices.IndexFunc(msgs, func(m fantasy.Message) bool {
		if m.Role != fantasy.MessageRoleAssistant {
			return false
		}
		for _, part := range m.Content {
			if call, ok := fantasy.AsMessagePart[fantasy.ToolCallPart](part); ok && call.ToolCallID == callID {
				return true
			}
		}
		return false
	})
}

// frameLine is what a prompt tells the model of its stack while that is
// not empty (FRAMES.md section 7): the frame it is in, the stack when it
// is more than one frame, and what a pop does.
func frameLine(stack []stackFrame) string {
	if len(stack) == 0 {
		return ""
	}
	top := stack[len(stack)-1]
	var b strings.Builder
	fmt.Fprintf(&b, "You are in `%s` %s", top.id, strconv.Quote(top.purpose))
	if len(stack) > 1 {
		ids := make([]string, len(stack))
		for i, fr := range stack {
			ids[i] = "`" + fr.id + "`"
		}
		b.WriteString(" (your stack: " + strings.Join(ids, ", then ") + ")")
	}
	b.WriteString(frameLineRest)
	return b.String()
}

// frameLineRest ends the first sentence of frameLine and adds its second.
const frameLineRest = ": pop it with its result when it is done; everything until then is removed from your context when you pop it, what the user says included, so keep what matters as a thought. Other work the user asks for meanwhile goes in a frame of its own: mem.Todo(\"rename the port flag\").Push(), popped when done."

// frameReminder is the first sentence of frameLine, for the mem reminder.
func frameReminder(top stackFrame) string {
	line := frameLine([]stackFrame{top})
	return line[:strings.Index(line, ". Other work")+1]
}

// stackLine is frameLine for the session, "" with an empty stack.
func (f *frameStacks) stackLine(ctx context.Context, sessionID string) string {
	if f == nil || sessionID == "" {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.record(ctx, sessionID)
	stack := make([]stackFrame, len(rec.stack))
	for i, fr := range rec.stack {
		stack[i] = *fr
	}
	return frameLine(stack)
}

// largeStep is the line the model is given once, inside a frame whose
// work has grown past half the point at which the conversation is
// compacted: well before that point, and safe, where deferring the
// compaction check was not.
const largeStep = "this step is large: pop it with what you have, or split it into sub-steps"

// largeLine is largeStep for the frame on top of the session's stack when
// the session's running total, tokens, has grown past half of point since
// the push, given once per frame; "" otherwise.
func (f *frameStacks) largeLine(ctx context.Context, sessionID string, tokens, point int64) string {
	if f == nil || sessionID == "" || point <= 0 {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.record(ctx, sessionID)
	if len(rec.stack) == 0 {
		return ""
	}
	top := rec.stack[len(rec.stack)-1]
	if top.warned || tokens-top.tokens <= point/2 {
		return ""
	}
	top.warned = true
	return fmt.Sprintf("<system_reminder>%s (you are in `%s`, and about %s tokens came in since you entered it).</system_reminder>", largeStep, top.id, roughCount(tokens-top.tokens))
}

// decorateTodos shows the frame the session is in where its todos show
// mem's open work: the todo in progress reads "in `id` "purpose"".
func (f *frameStacks) decorateTodos(ctx context.Context, sessionID string, todos []session.Todo) []session.Todo {
	top, ok := f.top(ctx, sessionID)
	if !ok {
		return todos
	}
	for i := range todos {
		if todos[i].Status == session.TodoStatusInProgress {
			todos[i].ActiveForm = fmt.Sprintf("in `%s` %s", top.id, strconv.Quote(top.purpose))
			return todos
		}
	}
	return todos
}
