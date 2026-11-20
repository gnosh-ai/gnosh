package ui

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/markdown"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// The transcript view (Ctrl+O, as Claude Code's): the session's whole
// conversation on the whole screen, as its file holds it. Every prompt
// with what came with it, every reply with its time, its model and its
// reasoning, and every call with its whole input and its result, the work
// of the steps that were popped too. It is what was asked for on 14
// November 2026 ("what about ctrl-o or whatever to expand at least? or
// whatever claude does"), and the way to read a thought again once it
// has gone by (UI-22). Long parts are cut at viewPart lines until Ctrl+E
// shows everything. q, Esc, Ctrl+C or Ctrl+O go back; the session goes on
// meanwhile, the view taking in what is written to the file, and what the
// session said goes into the scrollback when the view closes, as the side
// panel's does. A question for the person closes it.

// transcriber is a session that hands out its conversation for the view
// (app.SessionControls).
type transcriber interface {
	Transcript() []app.TranscriptEntry
}

// viewPart is how many lines of one long part (a call's input, a result,
// what came with a prompt) the view shows until Ctrl+E shows everything.
const viewPart = 24

// viewEvery is how often an open view looks for what was written to the
// session's file since it last looked.
const viewEvery = 300 * time.Millisecond

// transcriptView is the view while it is open.
type transcriptView struct {
	p *term.Panel
	// all says everything is shown whole (Ctrl+E).
	all bool
	// prompts are the rows the person's prompts start at, for { and }.
	prompts []int
	// shown is how many entries the view shows, and looked when it last
	// asked for them. base is their lines, and tail those of the
	// reasoning that is arriving, which the file has only once the reply
	// is over.
	shown      int
	looked     time.Time
	base, tail []string
	// held is what the session drew meanwhile.
	held heldScreen
	// panel says the side panel was on, and comes back after.
	panel bool
}

// heldScreen is what the session draws on while the view has the screen:
// what it commits is kept for the scrollback, and its live region waits.
type heldScreen struct{ lines []string }

func (h *heldScreen) Commit(lines ...string) error {
	h.lines = append(h.lines, lines...)
	return nil
}
func (h *heldScreen) SetLive([]string, int, int) error { return nil }
func (h *heldScreen) Resize(int, int) error            { return nil }
func (h *heldScreen) Clear() error                     { return nil }
func (h *heldScreen) Reset()                           {}

// toggleView opens the transcript view, or closes it when it is open.
func (s *session) toggleView() {
	if s.view != nil {
		s.closeView()
		return
	}
	s.openView()
}

// openView takes the screen for the transcript view.
func (s *session) openView() {
	if _, ok := s.c.(transcriber); !ok {
		s.flash("The transcript view (Ctrl+O) is not joined to this build.")
		return
	}
	cols, rows, ok := s.host.size()
	if !ok {
		cols, rows = s.termCols, s.rows
	}
	// (What waits to be written goes into the scrollback first.)
	s.frame()
	panel := s.panel != nil
	s.closePanel()
	p, err := s.host.openView(cols, rows)
	if err != nil {
		s.notice(agent.LevelWarning, "The transcript view could not be opened: "+err.Error())
		if panel {
			s.openPanel()
		}
		return
	}
	s.view = &transcriptView{p: p, panel: panel}
	s.r = &s.view.held
	s.termCols, s.rows = cols, rows
	s.refreshView(true)
	s.dirty = true
}

// closeView gives the screen back, and writes what the session said
// meanwhile into the scrollback, where it would have been. The agent view,
// when it is open over the session, closes too.
func (s *session) closeView() {
	s.closeAgents()
	v := s.view
	if v == nil {
		return
	}
	_, err := v.p.Close()
	s.keep(err)
	s.view, s.r = nil, s.inline
	if cols, rows, ok := s.host.size(); ok {
		s.termCols, s.rows = cols, rows
	}
	s.keep(s.inline.Resize(s.termCols, s.rows))
	if len(v.held.lines) > 0 {
		s.keep(s.inline.Commit(v.held.lines...))
	}
	s.relayout(s.termCols)
	s.dirty = true
	if v.panel {
		s.openPanel()
	}
}

// viewInput takes in what the terminal reported while the view is open.
func (s *session) viewInput(ev term.Event) {
	v := s.view
	s.dirty = true
	switch ev := ev.(type) {
	case term.Mouse:
		switch ev.Action {
		case term.MouseWheelUp:
			v.p.Scroll(3)
		case term.MouseWheelDown:
			v.p.Scroll(-3)
		}
	case term.Key:
		k := ev
		switch {
		case typedRune(k, 'q'), k.IsKey(term.KeyEscape, 0), k.Is('c', term.ModCtrl), k.Is('o', term.ModCtrl):
			s.closeView()
		case k.IsKey(term.KeyUp, 0), typedRune(k, 'k'):
			v.p.Scroll(1)
		case k.IsKey(term.KeyDown, 0), typedRune(k, 'j'), k.IsKey(term.KeyEnter, 0):
			v.p.Scroll(-1)
		case k.IsKey(term.KeyPageUp, 0), typedRune(k, 'b'):
			v.p.Scroll(v.p.Page())
		case k.IsKey(term.KeyPageDown, 0), typedRune(k, ' '):
			v.p.Scroll(-v.p.Page())
		case k.IsKey(term.KeyHome, 0), typedRune(k, 'g'):
			v.p.ScrollTo(0)
		case k.IsKey(term.KeyEnd, 0), typedRune(k, 'G'):
			v.p.Scroll(-1 << 30)
		case k.Is('e', term.ModCtrl):
			v.all = !v.all
			s.refreshView(true)
		case typedRune(k, '['):
			s.viewToScrollback()
		case typedRune(k, 'v'):
			s.viewInEditor()
		case typedRune(k, '{'):
			top := v.p.Top()
			for i := len(v.prompts) - 1; i >= 0; i-- {
				if v.prompts[i] < top {
					v.p.ScrollTo(v.prompts[i])
					break
				}
			}
		case typedRune(k, '}'):
			top := v.p.Top()
			for _, row := range v.prompts {
				if row > top {
					v.p.ScrollTo(row)
					break
				}
			}
		}
	}
}

// viewToScrollback closes the view and writes the whole conversation,
// nothing cut, into the terminal's own scrollback, where the terminal's
// own search (Cmd+F, tmux's copy mode) reaches it: Claude Code's [.
func (s *session) viewToScrollback() {
	tr, ok := s.c.(transcriber)
	if !ok {
		return
	}
	entries := tr.Transcript()
	s.closeView()
	lines, _ := s.transcriptLines(entries, s.cols, true)
	s.begin(blkText)
	s.put(lines...)
	s.flash("The whole transcript is in the scrollback now, where the terminal can search it.")
}

// viewInEditor closes the view and opens the whole conversation, as plain
// text, in the person's own editor (VISUAL, EDITOR): Claude Code's v.
// What is saved there is let go.
func (s *session) viewInEditor() {
	tr, ok := s.c.(transcriber)
	if !ok {
		return
	}
	lines, _ := s.transcriptLines(tr.Transcript(), 100, true)
	for i, l := range lines {
		lines[i] = strings.TrimRight(text.Strip(l), " ")
	}
	s.closeView()
	// The side panel, which closing the view put back, gives the screen up
	// while the editor has it, as it does for every other editor: the
	// editor ran under it, took mouse reports as keys, and left the panel
	// drawn over the scrollback (the TUI review of 2026-11-19).
	panel := s.panel != nil
	s.closePanel()
	s.frame()
	if _, err := s.host.edit(strings.Join(lines, "\n") + "\n"); err != nil {
		s.flash("The transcript could not be opened in an editor: " + err.Error())
	}
	if panel {
		s.openPanel()
	}
	s.dirty = true
}

// typedRune reports whether k is the character r, as a terminal sends it
// alone or with Shift reported beside it.
func typedRune(k term.Key, r rune) bool {
	return k.Is(r, 0) || k.Is(r, term.ModShift)
}

// viewFrame draws the view: what was written to the session's file since
// it last looked, and its last row, which says where the view is and
// which keys it takes.
func (s *session) viewFrame() {
	s.refreshView(false)
	v := s.view
	th := &s.th
	cols, _ := v.p.Size()
	parts := []string{"transcript"}
	if s.busy {
		parts = append(parts, "the session is working")
	}
	// (The keys the person bound, where the keybindings file says.)
	if k := s.keyHint("transcript:exit", "q"); k != "" {
		parts = append(parts, k+" goes back")
	}
	if k := s.keyHint("transcript:toggleShowAll", "Ctrl+E"); k != "" {
		if v.all {
			parts = append(parts, k+" cuts long parts")
		} else {
			parts = append(parts, k+" shows all")
		}
	}
	parts = append(parts, "{ } between prompts", "[ into the scrollback", "v in your editor")
	s.keep(v.p.SetLive([]string{fit(s.m, th.Quiet.Render(strings.Join(parts, th.Sep)), cols)}, -1, 0))
}

// refreshView lays the session's conversation out in the view again: at
// once with force, and otherwise when viewEvery has gone by and the
// conversation has more in it.
func (s *session) refreshView(force bool) {
	v := s.view
	tr, ok := s.c.(transcriber)
	if v == nil || !ok {
		return
	}
	now := s.now()
	if !force && now.Sub(v.looked) < viewEvery {
		return
	}
	v.looked = now
	cols, _ := v.p.Size()
	changed := force
	if entries := tr.Transcript(); force || len(entries) != v.shown {
		v.shown = len(entries)
		v.base, v.prompts = s.transcriptLines(entries, cols, v.all)
		changed = true
	}
	if tail := s.viewTail(cols); changed || !slices.Equal(tail, v.tail) {
		v.tail = tail
		s.keep(v.p.SetLines(append(v.base[:len(v.base):len(v.base)], tail...)...))
	}
}

// viewTail lays out the reasoning of the reply that is arriving.
func (s *session) viewTail(width int) []string {
	if s.reply.think.Len() == 0 {
		return nil
	}
	return append([]string{s.th.Quiet.Render(fit(s.m, "─ now ─", width))}, s.viewThought(s.reply.think.String(), width)...)
}

// viewThought lays out reasoning, its blank lines left out.
func (s *session) viewThought(words string, width int) []string {
	return thoughtLines(&s.th, s.m, words, width)
}

// thoughtLines is viewThought in the look th.
func thoughtLines(th *Theme, m text.Metrics, words string, width int) []string {
	var out []string
	lead := th.Quiet.Render(th.ThinkMark) + " "
	for _, l := range safeLines(words) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, wrap(m, th.Thinking.Render(l), width, lead, "  ")...)
		lead = "  "
	}
	return out
}

// transcriptLines lays entries out at width: their lines, and the rows
// the person's prompts start at. With all, nothing is cut.
func (s *session) transcriptLines(entries []app.TranscriptEntry, width int, all bool) (lines []string, prompts []int) {
	return layTranscript(&s.th, s.m, entries, width, all, s.keyHint("transcript:toggleShowAll", "Ctrl+E"))
}

// layTranscript is transcriptLines in the look th. showAll is the key
// that shows a long part whole, named under one that is cut, or "".
func layTranscript(th *Theme, m text.Metrics, entries []app.TranscriptEntry, width int, all bool, showAll string) (lines []string, prompts []int) {
	width = max(width, 10)
	// Each call is shown with its result, which comes in the entry after
	// the reply that made the call.
	results := map[string]core.Block{}
	for _, e := range entries {
		if e.Kind != "results" {
			continue
		}
		for _, b := range e.Message.Blocks {
			if b.Type == core.BlockToolResult {
				results[b.ToolUseID] = b
			}
		}
	}
	shown := map[string]bool{}
	add := func(rows ...string) { lines = append(lines, rows...) }
	// text lays out outside text, safe to draw, a row per line of it,
	// wrapped: first leads its first row and rest the others. With prose,
	// blank lines are left out.
	rowsOf := func(words string, style func(string) string, first, rest string, prose bool) []string {
		var out []string
		lead := first
		for _, l := range safeLines(words) {
			if prose && strings.TrimSpace(l) == "" {
				continue
			}
			out = append(out, wrap(m, style(l), width, lead, rest)...)
			lead = rest
		}
		return out
	}
	screenRowsOf := func(words string, style func(string) string, first, rest string) []string {
		var out []string
		lead := first
		for _, l := range screenLines(words) {
			out = append(out, wrap(m, style(l), width, lead, rest)...)
			lead = rest
		}
		return out
	}
	quiet := func(l string) string { return th.Quiet.Render(l) }
	plain := func(l string) string { return l }
	// part is a long part, cut at viewPart rows until everything is shown.
	part := func(rows []string) []string {
		if all || len(rows) <= viewPart {
			return rows
		}
		more := "    … " + count(len(rows)-viewPart, "more line", "more lines")
		if showAll != "" {
			more += ": " + showAll + " shows all"
		}
		return append(rows[:viewPart:viewPart], quiet(more))
	}
	call := func(b core.Block) {
		add(wrap(m, th.Tool.Render(th.ToolMark)+" "+th.ToolName.Render(oneLine(b.Name)), width, "", "  ")...)
		add(part(rowsOf(viewJSON(b.Input), quiet, "    ", "    ", false))...)
		r, ok := results[b.ID]
		if !ok {
			return
		}
		shown[b.ID] = true
		style, first := plain, "  ⎿ "
		if r.IsError {
			style, first = func(l string) string { return th.Fail.Render(l) }, "  "+th.Fail.Render(th.FailMark)+" "
		}
		out := screenRowsOf(resultText(r), style, first, "    ")
		if len(out) == 0 {
			out = []string{"  ⎿ " + quiet("(nothing)")}
		}
		add(part(out)...)
	}
	for _, e := range entries {
		when := e.Time.Local().Format("15:04:05")
		switch e.Kind {
		case "prompt":
			prompts = append(prompts, len(lines))
			texts := blockTexts(e.Message)
			mark := th.Prompt.Render(th.PromptMark)
			if e.Hidden {
				mark = quiet(th.PromptMark)
			}
			if len(texts) == 0 {
				add(mark + quiet(when))
				continue
			}
			add(rowsOf(texts[0], plain, mark, strings.Repeat(" ", m.Width(th.PromptMark)), false)...)
			// The rest came with the prompt: what gnosh and the hooks added.
			for _, t := range texts[1:] {
				add(part(rowsOf(unwrapped(t), quiet, "  + ", "    ", false))...)
			}
		case "reply":
			head := when
			if e.Model != "" {
				head += th.Sep + e.Model
			}
			add(quiet(fit(m, "─ "+head+" ─", width)))
			for _, b := range e.Message.Blocks {
				switch b.Type {
				case core.BlockThinking:
					add(thoughtLines(th, m, b.Text, width)...)
				case core.BlockText:
					md := markdown.New(markdown.Options{Width: width, Profile: th.Profile, Theme: th.Markdown, Metrics: m})
					add(append(md.Write(b.Text), md.Close()...)...)
				case core.BlockToolUse:
					call(b)
				}
			}
		case "results":
			// A result whose call the view did not show (its reply was cut
			// off), and what came with the results.
			for _, b := range e.Message.Blocks {
				switch {
				case b.Type == core.BlockToolResult && !shown[b.ToolUseID]:
					add(part(screenRowsOf(resultText(b), plain, quiet("  ⎿ "+oneLine(b.ToolUseID)+": "), "    "))...)
				case b.Type == core.BlockText:
					add(part(rowsOf(unwrapped(b.Text), quiet, "  + ", "    ", false))...)
				}
			}
		case "hook":
			for _, t := range blockTexts(e.Message) {
				add(part(rowsOf(unwrapped(t), quiet, "  hook: ", "    ", false))...)
			}
		case "notice":
			if e.Text != "" {
				label := th.NoteLabel.Render("note:")
				switch e.Level {
				case agent.LevelWarning:
					label = th.WarnLabel.Render("warning:")
				case agent.LevelError:
					label = th.ErrorLabel.Render("error:")
				}
				add(rowsOf(e.Text, plain, label+" ", "  ", true)...)
				continue
			}
			for _, t := range blockTexts(e.Message) {
				add(part(screenRowsOf(unwrapped(t), quiet, "  gnosh: ", "    "))...)
			}
		case "summary":
			add(part(rowsOf(e.Text, quiet, "※ what came before, in short: ", "  ", true))...)
		case "frame":
			switch e.Op {
			case "push":
				add(rowsOf(e.Frame+": "+e.Label, quiet, "⇥ step ", "  ", true)...)
			case "pop":
				add(rowsOf(e.Frame+" done: "+e.Text, quiet, "⇤ step ", "  ", true)...)
			default:
				add(quiet(fit(m, "⋯ older work slid out of what the model is sent", width)))
			}
		}
	}
	return lines, prompts
}

// unwrapped is a text gnosh added, without the marker around it (the
// <gnosh> of prompt.Open and Close, or mem's own <mem>): the label beside
// it in the view already says whose it is. A text that is more than one
// marked piece is left as it is.
func unwrapped(s string) string {
	t := strings.TrimSpace(s)
	for _, tag := range []string{"gnosh", "mem"} {
		open, end := "<"+tag+">", "</"+tag+">"
		if strings.HasPrefix(t, open) && strings.HasSuffix(t, end) {
			if inner := t[len(open) : len(t)-len(end)]; !strings.Contains(inner, end) {
				return strings.TrimSpace(inner)
			}
		}
	}
	return s
}

// blockTexts are the texts of a message's text blocks.
func blockTexts(m core.Message) []string {
	var out []string
	for _, b := range m.Blocks {
		if b.Type == core.BlockText && strings.TrimSpace(b.Text) != "" {
			out = append(out, b.Text)
		}
	}
	return out
}

// resultText is what a tool result says, its pictures named.
func resultText(b core.Block) string {
	var parts []string
	for _, c := range b.Content {
		switch c.Type {
		case core.BlockText:
			parts = append(parts, c.Text)
		case core.BlockImage:
			parts = append(parts, "[a picture, "+c.MediaType+"]")
		}
	}
	return strings.Join(parts, "\n")
}

// viewJSON is a call's input laid out to be read, a field to a line.
func viewJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") != nil {
		return string(raw)
	}
	return b.String()
}
