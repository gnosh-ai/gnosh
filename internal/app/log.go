package app

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// sessionLog joins the loop's log (agent.Log, and agent.Roomer) to a
// session file (transcript.Session). The loop appends its entries and asks
// for the view; the file keeps every field of every entry so that it comes
// back, and computes the view by its own rule.
//
// How the loop's entries are written:
//
//	prompt     a user entry: source "prompt" for one a person typed,
//	           "notice" for one gnosh sent for itself
//	note       a user entry: source "hook" for what a hook added,
//	           "notice" for the rest
//	assistant  an assistant entry with the reply's stop reason. A reply
//	           with no text and no tool call is written with no blocks,
//	           so that it is in no view; a reply that was cancelled or
//	           failed is written even when it holds nothing, because it
//	           ends its turn. After it comes a usage entry with the
//	           request's token counts: one for each of the session's own
//	           requests, and for no other request
//	result     a user entry of source "tool_results" holding the one
//	           tool_result block
//	summary    a summary entry holding the model's own text. The line the
//	           loop puts before it is not written into the summary: the
//	           view puts its own sentence there, and the text would
//	           otherwise say it twice
//	notice, turn, queued, unqueued
//	           entries of those types, which the view passes over
//
// What the file's format has no field for (the loop's id of an entry, its
// turn, who wrote a note, a result's display and metadata, and so on) is
// kept under one extra key, "loop", and read back from it.
type sessionLog struct {
	ts    *transcript.Session
	texts *prompt.Set

	// stored are the features' hands on what is written: each is shown an
	// entry of the loop's and may add entries of its own right after it.
	stored []func(agent.Entry) []transcript.Entry
	// afterStored are told of every entry once it is in the file, with
	// what was written for it.
	afterStored []func(ctx context.Context, e agent.Entry, written []transcript.Entry)
	// slid are told what a slide took out of the view.
	slid []func(ctx context.Context, cuts []transcript.Cut)
	// onCut is called when part of the conversation has left the view (a
	// pop a feature recorded, or a slide), so that what was given in it
	// can be given again.
	onCut func()
	// cwd is the session's working directory now, against which the files
	// a slide's rounds edited are named.
	cwd func() string

	// startedWith names the gnosh that begins the file, for its first
	// entry; begun says the file has been looked at for one.
	startedWith string
	begun       bool
	// runAs names the agent the session runs as, when the file does not
	// say so yet: it is recorded with the first thing stored.
	runAs string

	mu sync.Mutex
	// prompts maps the loop's id of a prompt to the entry it was written
	// as, so that a slide can be told which prompt to keep.
	prompts map[string]string
	// last is the entry of the newest prompt.
	last string
}

// shownFile is a file a call named, as the model is told of it: relative
// to the working directory where it lies under it.
func (l *sessionLog) shownFile(path string) string {
	cwd := ""
	if l.cwd != nil {
		cwd = l.cwd()
	}
	if cwd == "" || !filepath.IsAbs(path) {
		return filepath.ToSlash(path)
	}
	if rel, err := filepath.Rel(cwd, filepath.Clean(path)); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return filepath.Clean(path)
}

// lastPrompt is the session-file entry of the newest prompt, or "" before
// the first.
func (l *sessionLog) lastPrompt() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

// promptEntries lists the session-file entries of every prompt that is
// still part of the conversation, the person's and gnosh's own, in order.
func promptEntries(entries []transcript.Entry) []string {
	var out []string
	conv := transcript.Conversation(entries)
	for i := range conv {
		for _, e := range decode(&conv[i]) {
			if e.Kind == agent.EntryPrompt {
				out = append(out, conv[i].UUID)
				break
			}
		}
	}
	return out
}

// loopExtra is the one key of an entry's Extra under which the loop's own
// fields are kept.
const loopExtra = "loop"

// The types of the entries that only the loop reads. The view passes over
// every type it does not know.
const (
	typeNotice   transcript.EntryType = "notice"
	typeTurn     transcript.EntryType = "turn"
	typeQueued   transcript.EntryType = "queued"
	typeUnqueued transcript.EntryType = "unqueued"
)

// loopFields is what an entry of the loop carries that the session file's
// format has no field for.
type loopFields struct {
	Kind        agent.EntryKind   `json:"kind"`
	ID          string            `json:"id,omitempty"`
	Turn        string            `json:"turn,omitempty"`
	Hidden      bool              `json:"hidden,omitempty"`
	Source      string            `json:"src,omitempty"`
	Prompt      string            `json:"prompt,omitempty"`
	Blocks      []core.Block      `json:"blocks,omitempty"`
	Model       string            `json:"model,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Usage       *core.Usage       `json:"usage,omitempty"`
	Silent      bool              `json:"silent,omitempty"`
	BadInputs   map[string]string `json:"badInputs,omitempty"`
	ToolName    string            `json:"tool,omitempty"`
	Display     *core.Display     `json:"display,omitempty"`
	ResultCwd   string            `json:"resultCwd,omitempty"`
	Meta        map[string]any    `json:"meta,omitempty"`
	Input       json.RawMessage   `json:"input,omitempty"`
	Denied      bool              `json:"denied,omitempty"`
	Interrupted bool              `json:"interrupted,omitempty"`
	DurationNS  int64             `json:"durNs,omitempty"`
	Level       agent.Level       `json:"level,omitempty"`
	Text        string            `json:"text,omitempty"`
	Reason      agent.EndReason   `json:"reason,omitempty"`
	Trigger     string            `json:"trigger,omitempty"`
	PreTokens   int               `json:"preTokens,omitempty"`
}

// newSessionLog makes the log of a session file, and learns from what the
// file already holds which entry each prompt was written as.
func newSessionLog(ts *transcript.Session, texts *prompt.Set) *sessionLog {
	l := &sessionLog{ts: ts, texts: texts, prompts: map[string]string{}}
	for _, te := range ts.Entries() {
		if f, ok := loopOf(&te); ok && f.Kind == agent.EntryPrompt {
			l.prompts[f.ID] = te.UUID
			l.last = te.UUID
		}
	}
	return l
}

// loopOf reads the loop's own fields out of an entry.
func loopOf(te *transcript.Entry) (loopFields, bool) {
	raw, ok := te.Extra[loopExtra]
	if !ok {
		return loopFields{}, false
	}
	var f loopFields
	if json.Unmarshal(raw, &f) != nil || f.Kind == "" {
		return loopFields{}, false
	}
	return f, true
}

// withLoop puts the loop's own fields on an entry. Metadata that cannot be
// written as JSON is left out rather than losing the entry.
func withLoop(te transcript.Entry, f loopFields) transcript.Entry {
	raw, err := json.Marshal(f)
	if err != nil {
		f.Meta = nil
		raw, err = json.Marshal(f)
	}
	if err != nil {
		raw, _ = json.Marshal(loopFields{Kind: f.Kind, ID: f.ID, Turn: f.Turn})
	}
	te.Extra = map[string]json.RawMessage{loopExtra: raw}
	return te
}

// encode turns one entry of the loop into the entries the file is written.
// The first of them is the entry itself.
func (l *sessionLog) encode(e agent.Entry) []transcript.Entry {
	f := loopFields{Kind: e.Kind, ID: e.ID, Turn: e.Turn}
	var te transcript.Entry
	switch e.Kind {
	case agent.EntryPrompt:
		src := transcript.SourcePrompt
		if e.Hidden {
			src = transcript.SourceNotice
		}
		te = transcript.UserEntry(core.Message{Blocks: e.Blocks}, src)
		f.Hidden, f.Source = e.Hidden, e.Source
	case agent.EntryNote:
		src := transcript.SourceNotice
		if strings.HasPrefix(e.Source, "hook") {
			src = transcript.SourceHook
		}
		te = transcript.UserEntry(core.Message{Blocks: e.Blocks}, src)
		f.Source, f.Prompt = e.Source, e.Prompt
	case agent.EntryAssistant:
		msg := e.Message
		if e.Silent {
			// Kept for the record, and out of every view.
			f.Blocks = msg.Blocks
			msg = core.Message{}
		}
		te = transcript.AssistantEntry(msg, e.StopReason)
		usage := e.Usage
		f.Model, f.Provider, f.Usage = e.Model, e.Provider, &usage
		f.Silent, f.BadInputs, f.DurationNS = e.Silent, e.BadInputs, int64(e.Duration)
	case agent.EntryResult:
		te = transcript.UserEntry(core.Message{Blocks: []core.Block{core.ToolResultBlock(e.ToolUseID, e.Result)}}, transcript.SourceToolResults)
		f.ToolName, f.Display, f.ResultCwd, f.Meta = e.ToolName, e.Result.Display, e.Result.Cwd, e.Result.Meta
		f.Input, f.Denied, f.Interrupted, f.DurationNS = e.Input, e.Denied, e.Interrupted, int64(e.Duration)
	case agent.EntrySummary:
		// The summary is the model's writing about a conversation that
		// held outside text (files, pages, tool output): it is kept with
		// gnosh's own marks and chat-template tokens in it broken, as a
		// tool's output is, since it goes back to the model on the user's
		// side, after gnosh's line that says whose it is.
		te = transcript.SummaryEntry(prompt.Untrusted(summaryText(e.Blocks)), "")
		f.Blocks, f.Trigger, f.PreTokens = e.Blocks, e.Trigger, e.PreTokens
	case agent.EntryNotice:
		te = transcript.Entry{Type: typeNotice}
		f.Level, f.Source, f.Text = e.Level, e.Source, e.Text
	case agent.EntryTurn:
		te = transcript.Entry{Type: typeTurn}
		f.Reason, f.Text = e.Reason, e.Text
	case agent.EntryQueued:
		te = transcript.Entry{Type: typeQueued}
		f.Blocks, f.Hidden, f.Source = e.Blocks, e.Hidden, e.Source
	case agent.EntryUnqueued:
		te = transcript.Entry{Type: typeUnqueued}
		f.Prompt, f.Text = e.Prompt, e.Text
	default:
		te = transcript.Entry{Type: transcript.EntryType(e.Kind)}
	}
	te.Timestamp = e.Time
	out := []transcript.Entry{withLoop(te, f)}
	if e.Kind == agent.EntryAssistant && e.Usage != (core.Usage{}) {
		// The session's own request: its count is what a frame's size is
		// later measured against.
		u := transcript.UsageEntry(e.Usage, e.Model, e.Duration)
		u.Timestamp = e.Time
		out = append(out, u)
	}
	return out
}

// summaryLead is what stands before a summary when it becomes the first
// message the model reads: gnosh's own line, in its marks, that the summary
// is the model's, and not the person's words.
func (l *sessionLog) summaryLead() string {
	if l.texts == nil {
		return ""
	}
	return prompt.Wrap(l.texts.SummaryIntro()) + "\n\n"
}

// summaryText is the text a summary entry holds: the model's own summary.
// The loop gives a summary as its introducing line, in gnosh's marker, and
// then the summary; the view writes an introducing sentence of its own, so
// the loop's is kept beside the entry and not in it.
func summaryText(blocks []core.Block) string {
	if len(blocks) > 1 && blocks[0].Type == core.BlockText && strings.HasPrefix(blocks[0].Text, prompt.Open) {
		blocks = blocks[1:]
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == core.BlockText && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// decode turns an entry of the file back into the loop's entries. An entry
// the loop did not write (a frame's boundary, a usage count, a title) gives
// none; a message somebody else wrote is read as well as it can be.
func decode(te *transcript.Entry) []agent.Entry {
	f, ok := loopOf(te)
	if !ok {
		return foreign(te)
	}
	e := agent.Entry{Kind: f.Kind, ID: f.ID, Turn: f.Turn, Time: te.Timestamp}
	switch f.Kind {
	case agent.EntryPrompt:
		e.Blocks, e.Hidden, e.Source = te.Message.Blocks, f.Hidden, f.Source
	case agent.EntryNote:
		e.Blocks, e.Source, e.Prompt = te.Message.Blocks, f.Source, f.Prompt
	case agent.EntryAssistant:
		e.Message = core.Message{Role: core.RoleAssistant, Blocks: te.Message.Blocks}
		if f.Silent {
			e.Message.Blocks = f.Blocks
		}
		e.Model, e.Provider, e.StopReason = f.Model, f.Provider, te.Stop
		if f.Usage != nil {
			e.Usage = *f.Usage
		}
		e.Silent, e.BadInputs, e.Duration = f.Silent, f.BadInputs, time.Duration(f.DurationNS)
	case agent.EntryResult:
		for _, b := range te.Message.Blocks {
			if b.Type == core.BlockToolResult {
				e.ToolUseID = b.ToolUseID
				e.Result = core.ToolResult{Content: b.Content, IsError: b.IsError}
				break
			}
		}
		e.ToolName = f.ToolName
		e.Result.Display, e.Result.Cwd, e.Result.Meta = f.Display, f.ResultCwd, f.Meta
		e.Input, e.Denied, e.Interrupted, e.Duration = f.Input, f.Denied, f.Interrupted, time.Duration(f.DurationNS)
	case agent.EntrySummary:
		e.Blocks, e.Trigger, e.PreTokens = f.Blocks, f.Trigger, f.PreTokens
		if len(e.Blocks) == 0 {
			e.Blocks = []core.Block{core.Text(te.Summary)}
		}
	case agent.EntryNotice:
		e.Level, e.Source, e.Text = f.Level, f.Source, f.Text
	case agent.EntryTurn:
		e.Reason, e.Text = f.Reason, f.Text
	case agent.EntryQueued:
		e.Blocks, e.Hidden, e.Source = f.Blocks, f.Hidden, f.Source
	case agent.EntryUnqueued:
		e.Prompt, e.Text = f.Prompt, f.Text
	}
	return []agent.Entry{e}
}

// foreign reads an entry that carries none of the loop's fields.
func foreign(te *transcript.Entry) []agent.Entry {
	switch te.Type {
	case transcript.TypeAssistant:
		return []agent.Entry{{
			Kind: agent.EntryAssistant, ID: te.UUID, Time: te.Timestamp,
			Message:    core.Message{Role: core.RoleAssistant, Blocks: te.Message.Blocks},
			StopReason: te.Stop, Silent: len(te.Message.Blocks) == 0,
		}}
	case transcript.TypeUser:
		var out []agent.Entry
		var rest []core.Block
		for _, b := range te.Message.Blocks {
			if b.Type != core.BlockToolResult {
				rest = append(rest, b)
				continue
			}
			out = append(out, agent.Entry{
				Kind: agent.EntryResult, ID: te.UUID, Time: te.Timestamp, ToolUseID: b.ToolUseID,
				Result: core.ToolResult{Content: b.Content, IsError: b.IsError},
			})
		}
		if len(rest) > 0 {
			out = append(out, agent.Entry{
				Kind: agent.EntryPrompt, ID: te.UUID, Time: te.Timestamp, Blocks: rest,
				Hidden: te.Source != transcript.SourcePrompt, Source: string(te.Source),
			})
		}
		return out
	case transcript.TypeSummary:
		return []agent.Entry{{Kind: agent.EntrySummary, ID: te.UUID, Time: te.Timestamp, Blocks: []core.Block{core.Text(te.Summary)}}}
	}
	return nil
}

// Append stores the loop's entries after everything stored before, in one
// write: either all of them are in the file, with what the features added
// beside them, or none is.
func (l *sessionLog) Append(ctx context.Context, entries ...agent.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	// A file that holds nothing yet is begun with an entry that says
	// which gnosh began it. It is written with the first thing the loop
	// stores, not before, so that a session nothing was ever said in
	// leaves no file; and it is what a rewind to before the first prompt
	// goes back to.
	if l.startedWith != "" && !l.begun {
		l.begun = true
		if len(l.ts.Entries()) == 0 {
			if _, err := l.ts.Append(transcript.MetaEntry("startedWith", l.startedWith)); err != nil {
				return err
			}
		}
		if l.runAs != "" {
			if _, err := l.ts.Append(transcript.MetaEntry(metaAgent, l.runAs)); err != nil {
				return err
			}
		}
	}
	var out []transcript.Entry
	at := make([]int, len(entries))
	cut := false
	for i, e := range entries {
		at[i] = len(out)
		out = append(out, l.encode(e)...)
		for _, f := range l.stored {
			for _, extra := range companions(f, e) {
				if extra.Type == transcript.TypeFrame && extra.Op == transcript.OpPop {
					cut = true
				}
				out = append(out, extra)
			}
		}
	}
	written, err := l.ts.AppendAll(out...)
	if err != nil {
		return err
	}
	l.mu.Lock()
	for i, e := range entries {
		if e.Kind == agent.EntryPrompt && at[i] < len(written) {
			l.prompts[e.ID] = written[at[i]].UUID
			l.last = written[at[i]].UUID
		}
	}
	l.mu.Unlock()
	if cut && l.onCut != nil {
		l.onCut()
	}
	// A command that left the session's shell in another directory: what
	// is written from here on says so.
	for _, e := range entries {
		if e.Kind == agent.EntryResult && e.Result.Cwd != "" {
			l.ts.SetCwd(e.Result.Cwd)
		}
	}
	// Everything is in the file. The features are told, entry by entry in
	// the order stored, and the loop goes on when they have returned.
	if len(l.afterStored) > 0 && len(written) == len(out) {
		for i, e := range entries {
			end := len(written)
			if i+1 < len(at) {
				end = at[i+1]
			}
			for _, f := range l.afterStored {
				func() {
					defer func() { _ = recover() }()
					f(ctx, e, slices.Clone(written[at[i]:end]))
				}()
			}
		}
	}
	return nil
}

// companions asks a feature what it writes beside an entry. A feature that
// panics adds nothing.
func companions(f func(agent.Entry) []transcript.Entry, e agent.Entry) (out []transcript.Entry) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	return f(e)
}

// View returns the conversation as the model is to read it: what the
// session file's own rule makes of its entries. The prompt the model is
// answering is named to it, so that nothing the view plans can take it.
func (l *sessionLog) View(ctx context.Context) (agent.View, error) {
	l.mu.Lock()
	last := l.last
	l.mu.Unlock()
	msgs, _ := l.ts.View(transcript.ViewOptions{Prompt: last, SummaryLead: l.summaryLead()})
	return agent.View{Messages: msgs}, nil
}

// Recent yields the loop's entries from the newest to the oldest, as they
// were appended. What a rewind took off the conversation is left out, and
// so is everything the loop did not write itself.
func (l *sessionLog) Recent(ctx context.Context) iter.Seq2[agent.Entry, error] {
	return func(yield func(agent.Entry, error) bool) {
		entries := transcript.Conversation(l.ts.Entries())
		for i := len(entries) - 1; i >= 0; i-- {
			if err := ctx.Err(); err != nil {
				yield(agent.Entry{}, err)
				return
			}
			for _, e := range slices.Backward(decode(&entries[i])) {
				if !yield(e, nil) {
					return
				}
			}
		}
	}
}

// MakeRoom takes the oldest part of the conversation out of the view
// without a summary, when the session stands in frames: the slide. The
// slide is recorded in the file before MakeRoom returns, so that every
// later view cuts the same place.
func (l *sessionLog) MakeRoom(ctx context.Context, req agent.RoomRequest) (agent.Room, error) {
	l.mu.Lock()
	keep := l.prompts[req.Protect]
	l.mu.Unlock()
	slides, cuts := transcript.Slide(l.ts.Entries(), req.Need, transcript.ViewOptions{
		Session:     l.ts.ID(),
		Prompt:      keep,
		Estimate:    req.EstimateMessage,
		Trace:       true,
		SummaryLead: l.summaryLead(),
	})
	if len(slides) == 0 {
		return agent.Room{}, nil
	}
	if _, err := l.ts.AppendAll(slides...); err != nil {
		return agent.Room{}, fmt.Errorf("the slide could not be recorded: %w", err)
	}
	var room agent.Room
	var told []prompt.Slide
	var notices []string
	names := fstools.Defaults()
	for _, c := range cuts {
		room.Freed += c.Tokens
		// The files the rounds that went had edited, as the frame line
		// names them (memjoin liveSlides).
		var files []string
		for _, p := range c.EditedPaths(names.EditName, names.WriteName) {
			files = append(files, l.shownFile(p))
		}
		switch {
		case c.Frame != "":
			told = append(told, prompt.Slide{Frame: c.Frame, Files: files})
			notices = append(notices, fmt.Sprintf("slid `%s`: about %s tokens of its start out of the model's context, instead of compacting", c.Frame, tokenCount(c.Tokens)))
		case c.Before != "":
			told = append(told, prompt.Slide{Frame: c.Before, Before: true, Files: files})
			notices = append(notices, fmt.Sprintf("slid what came before `%s`: about %s tokens out of the model's context, instead of compacting", c.Before, tokenCount(c.Tokens)))
		}
	}
	room.Notice = strings.Join(notices, "; ")
	// A prompt a person typed is about to carry the frame line, which
	// tells the model of the slide. At every other moment the model is
	// told here.
	if req.Moment != agent.RoomBeforePrompt && l.texts != nil {
		room.Reminder = l.texts.SlideLine(told)
	}
	for _, f := range l.slid {
		func() {
			defer func() { _ = recover() }()
			f(ctx, cuts)
		}()
	}
	if l.onCut != nil {
		l.onCut()
	}
	return room, nil
}

// tokenCount writes a number of tokens the way gnosh shows them: 850, 44K,
// 1.2M.
func tokenCount(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", max(n, 0))
	case n < 1000000:
		return fmt.Sprintf("%dK", (n+500)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1e6)
}
