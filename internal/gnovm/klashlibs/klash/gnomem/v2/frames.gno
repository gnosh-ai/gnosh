// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnomem

import (
	"strconv"
	"strings"
)

// Frames (FRAMES.md): the task tree seen through a stack. A Frame is a
// piece of work in mem, a goal or a step; entering one (PushFrame) marks
// where gnosh will cut the model's context, and leaving it with a result
// (PopFrame) cuts everything since. A thought is a labelled line kept on
// the frame the model stands in: it costs nothing, cuts nothing, and is
// how anything worth keeping outlives a pop. The stack itself is gnosh's,
// derived from the session's own pushes and pops; the realm is told it
// through frames(), and tells gnosh each push and pop through pushed() and
// popped().

// Frame is a piece of work in mem, seen through the stack. It is a
// snapshot, read-only; mem's calls change mem.
type Frame struct {
	Id       string    // its path of names: `fix-flaky`, `fix-flaky/find-cause`; "" for the root
	Purpose  string    // what it is for; "" for a step made from a thought with no reason given
	Result   string    // what came of it, once popped
	State    string    // "open", "doing", "waiting", "asked", "done" or "dropped": what NextTodo reads
	Doing    string    // the session working it, when State is "doing"
	Todos    []*Frame  // its steps, in order, one level
	Thoughts []Thought // its thoughts, oldest first
	m        *Mem
}

// Thought is a labelled line kept on a frame, and the step it was promoted
// into, if any.
type Thought struct {
	Name  string // its label, unique among the frame's thoughts
	Text  string
	Frame *Frame // the step it was promoted into; nil until then
	frame string // the id of the frame it is on
	m     *Mem
}

// Fact is a fact as Recall returns it.
type Fact struct {
	Name string
	Body string
}

// Instantiation is what a Frame, a Thought and a Fact have in common: a
// concrete piece of memory, which Recall returns. *Frame, Thought and Fact
// implement it; a type switch tells them apart.
type Instantiation interface {
	Kind() string   // "frame", "thought" or "fact"
	Ref() string    // how a call names it: a frame's Id, a thought's frame and name, a fact's name
	Says() string   // a frame's purpose, a thought's text, a fact's body
	String() string // how it is shown
}

// maxRecall is how many Instantiations Recall returns at most.
const maxRecall = 5

func (f *Frame) Kind() string { return "frame" }
func (f *Frame) Ref() string  { return f.Id }
func (f *Frame) Says() string { return f.Purpose }

// String shows the frame as mem.Show would: the root as the whole outline.
func (f *Frame) String() string {
	switch {
	case f == nil:
		return "no frame\n"
	case f.m == nil:
		return "frame `" + f.Id + "` " + quote(f.Purpose) + "\n"
	case f.Id == "":
		return f.m.Show()
	}
	return f.m.Show(f.Id)
}

// NextTodo is f's first step, in the order of its Todos, whose State is
// "open", or "doing" by this session; nil when f has none, and at the
// unbound root, where a goal is entered by id or made and pushed.
func (f *Frame) NextTodo() *Frame {
	if f == nil || f.m == nil {
		return nil
	}
	return f.m.nextTodo(f.Id)
}

func (t Thought) Kind() string { return "thought" }
func (t Thought) Ref() string  { return t.frame + "#" + t.Name }
func (t Thought) Says() string { return t.Text }

func (t Thought) String() string {
	s := "thought `" + t.Ref() + "`: " + t.Text
	if t.Frame != nil {
		s += " (now the step `" + t.Frame.Id + "`)"
	}
	return s + "\n"
}

func (f Fact) Kind() string   { return "fact" }
func (f Fact) Ref() string    { return f.Name }
func (f Fact) Says() string   { return f.Body }
func (f Fact) String() string { return "fact `" + f.Name + "` = " + f.Body + "\n" }

// Frame is the present frame: the top of this session's stack, or, with
// the stack empty, the root: the goal the user's /goal bound the session
// to, or everything open. Given an id, it is that frame:
// mem.PushFrame(mem.Frame("fix-flaky")) enters a goal by id.
func (m *Mem) Frame(id ...string) *Frame {
	switch {
	case len(id) > 1:
		panic("Frame takes one id, or none for the present frame")
	case len(id) == 0:
		return m.frame(m.present())
	}
	n := id[0]
	if n == "" {
		return m.frame(bound())
	}
	if it := m.get(n); it == nil || it.Status == "forgotten" {
		panic(m.noFrame(n))
	}
	return m.frame(n)
}

// present is the frame this session is in: the top of its stack, or the
// root: the goal /goal bound it to, or "" for the unbound root.
func (m *Mem) present() string {
	if s := frames(); len(s) > 0 {
		return s[len(s)-1]
	}
	return bound()
}

// frame is frame id, with its steps one level deep and its thoughts; the
// unbound root's steps are the open goals, newest first.
func (m *Mem) frame(id string) *Frame {
	f := m.shallow(id)
	if id == "" {
		m.open.ReverseIterate("", "", func(_ string, v any) bool {
			f.Todos = append(f.Todos, m.shallow(v.(string)))
			return false
		})
		return f
	}
	it := m.get(id)
	for _, s := range it.Steps {
		step := id + "/" + s
		if m.get(step).Status != "forgotten" {
			f.Todos = append(f.Todos, m.shallow(step))
		}
	}
	for _, n := range it.Notes {
		f.Thoughts = append(f.Thoughts, m.thoughtOf(id, n))
	}
	return f
}

// shallow is frame id without its steps and thoughts.
func (m *Mem) shallow(id string) *Frame {
	if id == "" {
		return &Frame{Purpose: "everything open", State: "open", m: m}
	}
	it := m.get(id)
	return &Frame{Id: id, Purpose: it.Title, Result: it.Result, State: m.state(id, it), Doing: it.Doing, m: m}
}

// thoughtOf is thought n of frame id, as a Thought.
func (m *Mem) thoughtOf(id string, n note) Thought {
	t := Thought{Name: n.Name, Text: n.Text, frame: id, m: m}
	if n.Step != "" {
		if s := m.get(n.Step); s != nil && s.Status != "forgotten" {
			t.Frame = m.shallow(n.Step)
		}
	}
	return t
}

// state is item it's State: its status once closed; else "asked" for the
// user, "waiting" for a day or words (its own or an item's above it),
// "doing" for a session's, or "open".
func (m *Mem) state(id string, it *Todo) string {
	switch {
	case it.Status != "":
		return it.Status
	case it.For != "":
		return "asked"
	case m.waitsAbove(id):
		return "waiting"
	case it.Doing != "":
		return "doing"
	}
	return "open"
}

// waitsAbove reports whether item id waits, itself or through an item
// above it.
func (m *Mem) waitsAbove(id string) bool {
	for n := id; n != ""; n = parentOf(n) {
		if m.waiting(m.get(n)) {
			return true
		}
	}
	return false
}

// nextTodo is Frame.NextTodo for frame id.
func (m *Mem) nextTodo(id string) *Frame {
	if id == "" {
		return nil
	}
	it := m.get(id)
	if it == nil {
		return nil
	}
	me := caller()
	for _, s := range it.Steps {
		step := id + "/" + s
		st := m.get(step)
		switch m.state(step, st) {
		case "open":
			return m.shallow(step)
		case "doing":
			if st.Doing == me {
				return m.shallow(step)
			}
		}
	}
	return nil
}

// noFrame says that id names no frame, and what it might have been.
func (m *Mem) noFrame(id string) string {
	msg := "there is no frame " + named(id)
	low := strings.ToLower(strings.TrimSpace(id))
	switch {
	case strings.IndexByte(id, '#') >= 0:
		item, name := splitAddr(id)
		return msg + ": that is a thought's address; mem.Thought(" + quote(name) + ") reads it, and mem.Frame(" + quote(item) + ") is its frame"
	case low != id && m.get(low) != nil:
		return msg + ": names are lowercase, typed as printed: `" + low + "`"
	case m.formed(id) != "":
		return msg + ": did you mean `" + m.formed(id) + "`?"
	case m.titled(id) != "":
		return msg + ": that is a title; the goal is `" + m.titled(id) + "`"
	}
	return msg + "; mem.Show() lists them"
}

// NewFrame adds a step under the present frame, at the end of its Todos,
// and returns it; at the unbound root it adds a goal. Frames are named,
// not numbered: the name is a mem name, unique among the parent's steps,
// and the Id is the parent's Id and the name. The purpose defaults to the
// name.
func (m *Mem) NewFrame(name string, purpose ...string) *Frame {
	if len(purpose) > 1 {
		panic("NewFrame takes a name and, after it, one purpose: mem.NewFrame(\"find-cause\", \"find why TestFlaky fails\")")
	}
	p := name
	if len(purpose) == 1 {
		p = clip(text("the purpose", purpose[0]), maxTitle)
	}
	if p == "" {
		p = name
	}
	parent := m.present()
	id := m.frameName(parent, name, p)
	if parent == "" {
		m.refuseOpenTitle(p)
		m.do(Op{Kind: "add", Name: id, New: p, By: caller()})
		println("added goal " + ref(id, p))
		return m.frame(id)
	}
	m.addStep(parent, id, p)
	println("added " + ref(id, p))
	return m.frame(id)
}

// frameName checks a new frame's name under parent ("" for the root), and
// returns its id.
func (m *Mem) frameName(parent, name, purpose string) string {
	where, example := "the root", "fix-flaky"
	if parent != "" {
		where, example = "`"+parent+"`", "find-cause"
	}
	switch {
	case name == "":
		panic("a frame needs a name: mem.NewFrame(\"" + example + "\", \"what it is for\")")
	case strings.ToLower(name) != name && validName(strings.ToLower(name)):
		panic("names are lowercase: `" + strings.ToLower(name) + "`")
	case !validName(name) && validName(purpose) && !strings.Contains(purpose, "/"):
		panic("the name comes first, then the purpose: mem.NewFrame(" + quote(purpose) + ", " + quote(name) + ")")
	case strings.Contains(name, "/"):
		panic("a frame's name is one segment, under the frame you are in (" + where + "): try `" + nameForm(stepName(name)) + "`")
	case !validName(name):
		panic(named(name) + " is not a name: a frame's name is lowercase words joined by \"-\"" + like(nameForm(name), example))
	case len(name) > maxGoalName:
		panic("a frame's name is at most " + strconv.Itoa(maxGoalName) + " bytes: try `" + nameOf(name, "frame") + "`")
	}
	if parent == "" {
		free := func(n string) bool { return !reserved[n] && !forgottenName(n) && m.taken.Get(n) == nil }
		switch {
		case reserved[name] || forgottenName(name):
			panic("`" + name + "` is reserved: give another name")
		case !free(name):
			panic("the name `" + name + "` is taken, and names are never given twice: give another, like `" + m.freeName(name, free) + "`")
		}
		return name
	}
	if m.get(parent+"/"+name) != nil {
		panic("`" + parent + "` already has a step named `" + name + "`")
	}
	return parent + "/" + name
}

// addStep adds step id, titled title, under open item parent.
func (m *Mem) addStep(parent, id, title string) {
	p := m.openItem(parent)
	if p.For != "" {
		panic(ref(parent, p.Title) + " is for the user: it has no steps")
	}
	if len(p.Steps) >= maxSteps {
		panic(ref(parent, p.Title) + " has " + strconv.Itoa(maxSteps) + " steps, the most an item may have")
	}
	m.do(Op{Kind: "add", Name: id, New: title, By: caller()})
}

// PushFrame enters f: it becomes this session's, the top of its stack, and
// from here the context will be cut when it is popped. With nil, which
// NextTodo gives when nothing is left to enter, it is a kept no-op that
// says what to do instead.
func (m *Mem) PushFrame(f *Frame) {
	if f == nil {
		println(m.nothingToPush())
		return
	}
	m.push(f.Id, "")
}

// push enters frame id. thought is the text of the thought the frame was
// made from, when the push follows its making.
func (m *Mem) push(id, thought string) {
	stack := frames()
	switch {
	case id == "":
		panic("the root is not a frame to enter: push a goal, or make one")
	case id == bound() && len(stack) == 0:
		panic("you are in `" + id + "` already: it is this session's goal; push its steps")
	}
	it := m.openItem(id)
	if it.For != "" {
		panic(ref(id, it.Title) + " is asked of the user: end your turn so they can answer, or mem.Drop(\"" + id + "\", \"why\")")
	}
	m.refuseWait(id)
	me := caller()
	switch {
	case it.Doing != "" && it.Doing != me:
		panic(m.heldText(id, it))
	case onStack(stack, id):
		panic("`" + id + "` is already on your stack")
	}
	if s := deepestUnder(stack, id); s != "" {
		panic("`" + id + "` cannot go above its own step `" + s + "`, which is on your stack: pop or drop that first")
	}
	if it.Doing != me {
		m.do(Op{Kind: "set", Name: id, Field: "doing", Old: it.Doing, New: me})
	}
	pushed(id, it.Title)
	println(pushText(id, it.Title, thought))
}

// pushText is what a push says: where the model is, and what a pop will do.
func pushText(id, title, thought string) string {
	head := "in " + ref(id, title)
	if title == "" && thought != "" {
		head = "in `" + id + "` (thought: " + quote(line(thought, 80)) + ")"
	}
	return head + ": when it is done, pop it with what came of it and what the next step needs. " +
		"Everything from here until the pop is removed from your context then, what you were told included; " +
		"what matters after, keep as a thought first. Your result stays, and nothing you did is undone."
}

// nothingToPush is what PushFrame(nil) says: what there is to do instead,
// where the model stands.
func (m *Mem) nothingToPush() string {
	p := m.present()
	if p == "" {
		newest := ""
		m.open.ReverseIterate("", "", func(_ string, v any) bool {
			newest = v.(string)
			return true
		})
		if newest != "" {
			return "nothing to push at the root: " + ref(newest, m.get(newest).Title) + " is open: mem.PushFrame(mem.Frame(" + quote(newest) + ")) enters it; or make a goal: mem.NewFrame(\"…\", \"…\")."
		}
		return "nothing to push at the root: no goal is open: make one, mem.NewFrame(\"…\", \"…\")."
	}
	it := m.get(p)
	if it.OpenSteps > 0 {
		return "nothing to push: nothing in `" + p + "` can be done now: " + m.holdsOf(p) + "."
	}
	if len(frames()) == 0 {
		return "nothing to push: `" + p + "` is this session's goal and has no open step: mem.Steps to plan more, or mem.Done when it holds."
	}
	return "nothing to push: `" + p + "` has no open step: pop it with its result."
}

// holdsOf says what holds back each open step of frame id that NextTodo
// passed over: asked of the user, waiting, or held by another session.
func (m *Mem) holdsOf(id string) string {
	var parts []string
	m.eachStep(id, func(step string, s *Todo) bool {
		switch m.state(step, s) {
		case "asked":
			parts = append(parts, "`"+step+"` is asked of the user: end your turn so they can answer, or mem.Drop(\""+step+"\", \"why\")")
		case "waiting":
			parts = append(parts, "`"+step+"`"+waitText(m.waitOn(step)))
		case "doing":
			parts = append(parts, "`"+step+"` is held by "+who(s.Doing))
		}
		return false
	})
	return joinList(parts)
}

// heldText refuses a call on a frame another session holds.
func (m *Mem) heldText(id string, it *Todo) string {
	return "`" + id + "` is held by " + who(it.Doing) + ": only it can pop, close or drop it (resume that session if it has ended)"
}

// refuseHeld refuses to close, drop, forget or start item id while another
// session holds it, or holds an open step under it.
func (m *Mem) refuseHeld(id string, it *Todo) {
	me := caller()
	if it.Doing != "" && it.Doing != me {
		panic(m.heldText(id, it))
	}
	m.eachBelow(id, false, func(step string, s *Todo) {
		if s.Status == "" && s.Doing != "" && s.Doing != me {
			panic(m.heldText(step, s))
		}
	})
}

// refuseInside refuses to close or drop item name while this session is
// in a frame under it, or has it on its stack below the top: the frame
// above must be popped or dropped first.
func (m *Mem) refuseInside(stack []string, name string) {
	if len(stack) == 0 || name == stack[len(stack)-1] {
		return
	}
	in := deepestUnder(stack, name)
	if in == "" && onStack(stack, name) {
		in = stack[len(stack)-1]
	}
	if in != "" {
		panic("you are still in `" + in + "`: pop or drop it before `" + name + "`")
	}
}

// refuseWait refuses to enter or start item id while it, or an item above
// it, waits.
func (m *Mem) refuseWait(id string) {
	for p := id; p != ""; p = parentOf(p) {
		if w := m.get(p); m.waiting(w) {
			until := "until " + w.Wait
			if !isDay(w.Wait) {
				until = "for " + strconv.Quote(w.Wait)
			}
			panic(ref(p, w.Title) + " waits " + until + ": start it then, or clear the wait with mem.Wait(\"" + p + "\", \"\")")
		}
	}
}

// onStack reports whether id is on stack.
func onStack(stack []string, id string) bool {
	for _, s := range stack {
		if s == id {
			return true
		}
	}
	return false
}

// deepestUnder is the last frame of stack under id, a step of it or of a
// step of it; "" for none.
func deepestUnder(stack []string, id string) string {
	found := ""
	for _, s := range stack {
		if strings.HasPrefix(s, id+"/") {
			found = s
		}
	}
	return found
}

// top is the top of stack, or "".
func top(stack []string) string {
	if len(stack) == 0 {
		return ""
	}
	return stack[len(stack)-1]
}

// PopFrame leaves the present frame done with its result, and pops it:
// gnosh then removes everything since the push from the model's context.
// result is the model's own words, one string, kept whole up to mem's
// limit; it is what stays.
func (m *Mem) PopFrame(result string) {
	stack := frames()
	if len(stack) == 0 {
		panic("nothing to pop: you are at the root")
	}
	id := top(stack)
	m.pop(id, m.item(id), result, false)
}

// pop leaves frame id, the top of the stack: done with result, or, with
// dropped, dropped with result as why, its open steps dropped first.
func (m *Mem) pop(id string, it *Todo, result string, dropped bool) {
	r := clip(text("the result", result), maxText)
	if r == "" {
		if dropped {
			panic("say why: mem.Drop(\"" + id + "\", \"why\")")
		}
		panic("say what came of `" + id + "`, even \"nothing found\"")
	}
	if dropped {
		steps := m.drop(id, it, r)
		popped(id, it.Title, "dropped: "+r, len(it.Notes))
		msg := "dropped " + ref(id, it.Title)
		if len(steps) > 0 {
			msg += ", with its open steps " + names(outlineOrder(steps))
		}
		println(msg)
		return
	}
	if it.OpenSteps > 0 {
		panic(ref(id, it.Title) + " has open steps " + m.refs(m.openSteps(id)) + ": push and pop them, or drop them, first")
	}
	if it.Wait != "" {
		m.do(Op{Kind: "set", Name: id, Field: "wait", Old: it.Wait})
	}
	if it.Doing != "" {
		m.do(Op{Kind: "set", Name: id, Field: "doing", Old: it.Doing})
	}
	m.do(Op{Kind: "set", Name: id, Field: "status", New: "done"})
	m.do(Op{Kind: "set", Name: id, Field: "result", New: r})
	popped(id, it.Title, r, len(it.Notes))
	println("done " + ref(id, it.Title) + " → " + line(r, maxText))
}

// NextHint is what to do after frame id was popped, for gnosh to end the
// receipt with when no push follows in the same input; "" for nothing.
// Gnosh calls it with the stack as it stands after the pop.
func (m *Mem) NextHint(id string) string {
	parent := parentOf(id)
	if parent == "" {
		return ""
	}
	p := m.get(parent)
	if p == nil || p.Status != "" {
		return ""
	}
	present := m.present()
	if nt := m.nextTodo(parent); nt != nil {
		if parent == present {
			return "next: " + ref(nt.Id, nt.Purpose) + ": mem.PushFrame(mem.Frame().NextTodo())"
		}
		return "next: " + ref(nt.Id, nt.Purpose) + ": mem.PushFrame(mem.Frame(" + quote(nt.Id) + "))"
	}
	switch {
	case p.OpenSteps > 0:
		return "next: nothing in `" + parent + "` can be done now: " + m.holdsOf(parent)
	case parent == present && onStack(frames(), parent):
		return "next: pop `" + parent + "` with the evidence that it holds"
	}
	return "next: mem.Done(" + quote(parent) + ", \"…\") once it holds"
}

// Think keeps a labelled thought on the present frame and returns it: one
// journal line, no cut, nothing on the server. Thoughts are what a frame
// keeps besides its result, and the one way anything a frame learned or
// was told outlives its pop: they stay in mem under the frame, popped or
// not. At the unbound root there is no frame to think on.
func (m *Mem) Think(name, thought string) Thought {
	p := m.present()
	if p == "" {
		panic("you are at the root, in no frame: mem.Fact(\"…\", \"…\") keeps something to remember")
	}
	it := m.item(p)
	name = m.thoughtName(p, it, name)
	thought = clip(text("the thought", thought), maxText)
	if thought == "" {
		panic("a thought needs words: mem.Think(" + quote(name) + ", \"what you found\")")
	}
	m.do(Op{Kind: "note", Name: p + "#" + name, New: thought})
	println("thought `" + name + "` kept on `" + p + "`")
	return Thought{Name: name, Text: thought, frame: p, m: m}
}

// thoughtName checks a new thought's name on frame id.
func (m *Mem) thoughtName(id string, it *Todo, name string) string {
	switch {
	case name == "":
		panic("a thought needs a name to find it by: mem.Think(\"port-env\", \"the port is read from an env var\")")
	case strings.ToLower(name) != name && validName(strings.ToLower(name)):
		panic("names are lowercase: `" + strings.ToLower(name) + "`")
	case strings.Contains(name, "/") || !validName(name):
		panic(named(name) + " is not a name: a thought's name is lowercase words joined by \"-\"" + like(nameForm(name), "port-env"))
	case len(name) > maxGoalName:
		panic("a thought's name is at most " + strconv.Itoa(maxGoalName) + " bytes: try `" + nameOf(name, "thought") + "`")
	case noteIndex(it, name) >= 0:
		panic("`" + id + "` already has a thought named `" + name + "`")
	}
	return name
}

// Thought is a thought by name: the present frame's, else the first found
// walking down the stack, each frame's own and then its steps', so a
// thought of a popped step is still reached by name while its goal is on
// the stack.
func (m *Mem) Thought(name string) Thought {
	for _, f := range m.framesDown() {
		it := m.get(f)
		if it == nil {
			continue
		}
		if n := noteNamed(it, name); n != nil {
			return m.thoughtOf(f, *n)
		}
		for _, s := range it.Steps {
			step := f + "/" + s
			if n := noteNamed(m.get(step), name); n != nil {
				return m.thoughtOf(step, *n)
			}
		}
	}
	panic("no frame on your stack has a thought named `" + name + "`")
}

// framesDown is the present frame, then the stack from the top down.
func (m *Mem) framesDown() []string {
	var out []string
	if p := m.present(); p != "" {
		out = append(out, p)
	}
	stack := frames()
	for i := len(stack) - 1; i >= 0; i-- {
		if len(out) == 0 || stack[i] != out[0] {
			out = append(out, stack[i])
		}
	}
	return out
}

// Recall is what in mem is about words, by meaning where gnosh has an
// embedding server and by the words otherwise: frames by their purpose,
// thoughts by their text and facts by their body, a handful at most, each
// an Instantiation. A promoted thought is one entry, the thought.
func (m *Mem) Recall(words string) []Instantiation {
	words = text("the words", words)
	if words == "" {
		panic("Recall needs words: mem.Recall(\"shared port\")")
	}
	names, kinds, _ := find(words, maxRecall*2)
	promoted := map[string]bool{}
	for i, k := range kinds {
		if k == "thought" {
			if item, name := splitAddr(names[i]); m.get(item) != nil {
				if n := noteNamed(m.get(item), name); n != nil && n.Step != "" {
					promoted[n.Step] = true
				}
			}
		}
	}
	var out []Instantiation
	for i, n := range names {
		if len(out) == maxRecall {
			break
		}
		switch kinds[i] {
		case "item":
			if it := m.get(n); it != nil && it.Status != "forgotten" && !promoted[n] {
				out = append(out, m.frame(n))
			}
		case "thought":
			item, name := splitAddr(n)
			if it := m.get(item); it != nil && it.Status != "forgotten" {
				if t := noteNamed(it, name); t != nil {
					out = append(out, m.thoughtOf(item, *t))
				}
			}
		case "fact":
			if f := m.getFact(n); f != nil {
				out = append(out, Fact{Name: n, Body: f.Text})
			}
		}
	}
	return out
}

// PushThought dives into a thought: it makes a step named as the thought,
// if it has none, under the thought's frame when that is the present frame
// or on the stack, else under the present frame; the step's purpose is why,
// if given, else empty, since the thought's text is there; then it enters
// the step as PushFrame would. A thought already dived into enters its step
// again.
func (m *Mem) PushThought(t Thought, why ...string) {
	if len(why) > 1 {
		panic("PushThought takes a thought and, after it, one reason for the dive")
	}
	frame := t.frame
	if frame == "" {
		frame = m.present()
	}
	if frame == "" {
		panic("you are at the root, in no frame: a thought lives on a frame; mem.Frame(id).Thoughts lists one's")
	}
	it := m.get(frame)
	if it == nil || it.Status == "forgotten" {
		panic(m.noFrame(frame))
	}
	n := noteNamed(it, t.Name)
	if n == nil {
		panic("`" + frame + "` has no thought named `" + t.Name + "`")
	}
	if n.Step != "" {
		if s := m.get(n.Step); s != nil && s.Status != "forgotten" {
			m.push(n.Step, n.Text)
			return
		}
	}
	present := m.present()
	parent := frame
	if it.Status != "" || !(frame == present || onStack(frames(), frame)) {
		parent = present
	}
	if parent == "" {
		panic("`" + frame + "` is " + it.Status + ", and you are at the root, in no frame: enter a frame to dive into `" + t.Name + "` from")
	}
	purpose := ""
	if len(why) == 1 {
		purpose = clip(text("why", why[0]), maxTitle)
	}
	step := m.frameName(parent, t.Name, purpose)
	m.addStep(parent, step, purpose)
	m.do(Op{Kind: "set", Name: frame + "#" + t.Name, Field: "frame", Old: n.Step, New: step})
	println("thought `" + t.Name + "` is now the step `" + step + "`")
	m.push(step, n.Text)
}
