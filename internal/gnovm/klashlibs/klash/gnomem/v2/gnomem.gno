// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

// Package gnomem is an agent's memory: the work it means to get done, as
// goals with steps, and what came of each; what it waits for; what the user
// must answer or do; facts to keep; and the user's standing rules. Klash
// keeps one Mem per project, in the realm klash.local/r/mem on disk, and
// binds it to the name mem in every input the model runs there:
//
//	mem.Goal("release v2").Steps("bump the version", "tag v2.0.0", "CI is green")
//	mem.Done("release-v2/1", "now 2.0.0")
//	mem.Ask("release-v2/2", "which key signs the tag?")
//	mem.Note("release-v2/2", "the tag needs the release key")
//	mem.Goal("renew the gno.shop TLS cert", "renew-cert").Wait("2026-12-01")
//	mem.Fact("cmd/test", "go test -race ./...")
//	mem.Rule("push-after-landing", "push to GitHub only after landed:")
//	mem.Find("release key")
//	mem.Show()
//
// Everything has a name: a lowercase path, printed in backticks. An item's
// name is its goal's name, then the step numbers: `release-v2`,
// `release-v2/2`, `release-v2/2/1`. A fact's or a rule's name is any path:
// `cmd/test`. Names are typed exactly as printed, never reused, and never
// renamed (an erasure aside).
//
// An item is open until it is done, dropped or forgotten, and no open item
// is ever under a closed one. A condition is a step: an item cannot be done
// while a step of it is open. An item may wait for a day or for what its
// words say; a wait holds back only next. An item for the user (a question
// or a request) is never the agent's next. A day is the user's local date.
//
// Every change is an Op, and one function, apply, changes a Mem. The calls
// only decide which Ops to make, and hand each to klash, which journals it.
// What an Op means never changes, so klash can rebuild a Mem from its Ops
// whatever this package's code or layout becomes. Items, facts and rules
// sit in B+trees keyed by name, so a call loads only what it touches, and a
// Mem has no size limit. Every call refuses text that is not valid UTF-8.
package gnomem

import (
	"strconv"
	"strings"
	"time"
)

// Goal is a goal just made, so its steps and wait can follow in the same
// statement.
type Goal struct {
	Name string
	m    *Mem
}

// Caps on what a Mem keeps; longer text is cut at a character, with "…".
// (maxSteps, in apply.gno, caps the steps under one item.)
const (
	maxTitle = 200  // bytes of a title
	maxText  = 1000 // bytes of a result, note, wait, fact or rule
)

// Goal adds a goal, and returns it so its steps and wait can follow. Its
// name comes from the title (`make-pot-tea` from "make a pot of tea"), or
// name gives it; a given name that is not free is refused, with one that
// is. Goal refuses a title an open goal already has.
func (m *Mem) Goal(title string, name ...string) Goal {
	title = clip(text("the title", title), maxTitle)
	if title == "" {
		panic("a goal needs a title: mem.Goal(\"what to get done\")")
	}
	m.refuseOpenTitle(title)
	n := m.newGoalName(title, name...)
	m.do(Op{Kind: "add", Name: n, New: title, By: caller()})
	println("added goal " + ref(n, title))
	return Goal{Name: n, m: m}
}

// Steps adds steps to the goal, numbered after the last it had.
func (g Goal) Steps(titles ...string) Goal {
	g.m.Steps(g.Name, titles...)
	return g
}

// Wait holds the goal back until a day, or until what the words say (see
// Mem.Wait).
func (g Goal) Wait(until string) Goal {
	g.m.Wait(g.Name, until)
	return g
}

// String is the goal's name and title.
func (g Goal) String() string {
	if it := g.m.get(g.Name); it != nil {
		return ref(g.Name, it.Title)
	}
	return "`" + g.Name + "`"
}

// Steps adds steps to open item name, numbered after the last it had
// (name/1, name/2, …). It refuses a title an open step of name already has,
// two steps with one title, and a 10,000th step.
func (m *Mem) Steps(name string, titles ...string) {
	parent := m.openItem(name)
	if parent.For != "" {
		panic(ref(name, parent.Title) + " is for the user: it has no steps")
	}
	if len(titles) == 0 {
		panic("no steps given: mem.Steps(\"" + name + "\", \"a step\", \"another\")")
	}
	seen := map[string]bool{}
	m.eachStep(name, func(_ string, it *Todo) bool {
		if it.Status == "" {
			seen[sameTitle(it.Title)] = true
		}
		return false
	})
	var refs []string
	for i, title := range titles {
		title = clip(text("step "+strconv.Itoa(i+1), title), maxTitle)
		if title == "" {
			panic("step " + strconv.Itoa(i+1) + " needs a title")
		}
		if seen[sameTitle(title)] {
			panic(ref(name, parent.Title) + " already has an open step " + strconv.Quote(title))
		}
		seen[sameTitle(title)] = true
		if parent.Last >= maxSteps {
			panic(ref(name, parent.Title) + " has " + strconv.Itoa(maxSteps) + " steps, the most an item may have")
		}
		step := name + "/" + strconv.Itoa(parent.Last+1)
		m.do(Op{Kind: "add", Name: step, New: title, By: caller()})
		refs = append(refs, ref(step, title))
	}
	println("added " + strings.Join(refs, ", ") + m.whatNext(name))
}

// Ask adds a question or a request for the user as a step of item name
// (with name "", as a top-level item named from the question). It is never
// the agent's next, the user is shown it, and the item it is under cannot
// be done until it is answered: Done(it, "the answer").
func (m *Mem) Ask(name, question string) {
	question = clip(text("the question", question), maxTitle)
	if question == "" {
		panic("a question needs words: mem.Ask(\"" + name + "\", \"what to ask the user\")")
	}
	var asked string
	if name == "" {
		m.refuseOpenTitle(question)
		asked = m.newGoalName("q " + question)
	} else {
		parent := m.openItem(name)
		if parent.For != "" {
			panic(ref(name, parent.Title) + " is for the user already")
		}
		if parent.Last >= maxSteps {
			panic(ref(name, parent.Title) + " has " + strconv.Itoa(maxSteps) + " steps, the most an item may have")
		}
		asked = name + "/" + strconv.Itoa(parent.Last+1)
	}
	m.do(Op{Kind: "add", Name: asked, New: question, By: caller()})
	m.do(Op{Kind: "set", Name: asked, Field: "for", New: "user"})
	msg := "asked " + ref(asked, question) + " (for the user)"
	if name != "" {
		msg += "; " + "`" + name + "` waits on it" + m.whatNext(goalOf(name))
	}
	println(msg)
}

// Start marks open item name as being worked on by this session, so that
// other sessions' next skips it; Show marks it "(started by …)". Done and
// Drop clear the mark. Starting an item another session started takes it
// over, and says from whom: that is how a step left by a session that has
// ended is picked up.
func (m *Mem) Start(name string) {
	it := m.openItem(name)
	if it.For != "" {
		panic(ref(name, it.Title) + " is for the user: only they can do it")
	}
	me := caller()
	if it.Doing == me {
		println(ref(name, it.Title) + " is started already, by this session")
		return
	}
	from := it.Doing
	m.do(Op{Kind: "set", Name: name, Field: "doing", Old: from, New: me})
	msg := "started " + ref(name, it.Title)
	if from != "" {
		msg += " (taken over from " + who(from) + ")"
	}
	println(msg)
}

// Done closes open item name with what came of it, if anything is given,
// and clears its wait. It refuses while a step of it is open, naming those
// steps and saying to Done or Drop them first. Done of a done item says
// "already done → <result>" and changes nothing.
func (m *Mem) Done(name string, result ...string) {
	it := m.item(name)
	switch it.Status {
	case "done":
		println(ref(name, it.Title) + " is already done" + arrow(it.Result))
		return
	case "dropped":
		panic(ref(name, it.Title) + " was dropped: " + it.Result + "; mem.Reopen(\"" + name + "\") to take it up again")
	}
	if it.OpenSteps > 0 {
		panic(ref(name, it.Title) + " has open steps " + m.refs(m.openSteps(name)) + ": Done or Drop them first")
	}
	r := clip(text("the result", strings.Join(result, " ")), maxText)
	if it.Wait != "" {
		m.do(Op{Kind: "set", Name: name, Field: "wait", Old: it.Wait})
	}
	if it.Doing != "" {
		m.do(Op{Kind: "set", Name: name, Field: "doing", Old: it.Doing})
	}
	m.do(Op{Kind: "set", Name: name, Field: "status", New: "done"})
	if r != "" {
		m.do(Op{Kind: "set", Name: name, Field: "result", New: r})
	}
	verb := "done "
	if it.For != "" {
		verb = "answered "
	}
	println(verb + ref(name, it.Title) + arrow(r) + m.whatNext(goalOf(name)))
}

// Drop closes open item name as not to be done, with why. Its open steps
// are dropped with it (the lowest first), keeping no reason; they, and its
// wait, come back when it is reopened.
func (m *Mem) Drop(name, why string) {
	it := m.openItem(name)
	why = clip(text("why", why), maxText)
	if why == "" {
		panic("say why: mem.Drop(\"" + name + "\", \"why\")")
	}
	var dropped []string
	m.eachBelow(name, true, func(step string, s *Todo) {
		if s.Status != "" {
			return
		}
		if s.Doing != "" {
			m.do(Op{Kind: "set", Name: step, Field: "doing", Old: s.Doing})
		}
		m.do(Op{Kind: "set", Name: step, Field: "status", New: "dropped"})
		dropped = append(dropped, step)
	})
	if it.Doing != "" {
		m.do(Op{Kind: "set", Name: name, Field: "doing", Old: it.Doing})
	}
	m.do(Op{Kind: "set", Name: name, Field: "status", New: "dropped"})
	m.do(Op{Kind: "set", Name: name, Field: "result", New: why})
	msg := "dropped " + ref(name, it.Title)
	if len(dropped) > 0 {
		for i, j := 0, len(dropped)-1; i < j; i, j = i+1, j-1 {
			dropped[i], dropped[j] = dropped[j], dropped[i]
		}
		msg += ", with its open steps " + names(dropped)
	}
	println(msg + m.whatNext(goalOf(name)))
}

// Reopen opens item name again if it is done or dropped, with the items it
// belongs to and the steps dropped with any of them (the highest first);
// what came of each becomes a note. An open item is left as it is.
func (m *Mem) Reopen(name string) {
	it := m.item(name)
	if it.Status == "" {
		println(ref(name, it.Title) + " is open")
		return
	}
	// The closed items above it, from the top, then it.
	var chain []string
	for p := name; p != ""; p = parentOf(p) {
		if m.get(p).Status != "" {
			chain = append([]string{p}, chain...)
		}
	}
	var others []string
	for _, n := range chain {
		for _, r := range m.reopenOne(n) {
			if r != name {
				others = append(others, r)
			}
		}
	}
	msg := "reopened " + ref(name, it.Title)
	if len(others) > 0 {
		msg += ", with " + names(others)
	}
	println(msg + m.whatNext(goalOf(name)))
}

// reopenOne reopens item n, then the steps dropped with it, the highest
// first, and returns their names.
func (m *Mem) reopenOne(n string) []string {
	it := m.get(n)
	if it.Status == "" {
		return nil
	}
	dropped := it.Status == "dropped"
	if it.Result != "" {
		m.do(Op{Kind: "note", Name: n, New: "was " + it.Status + ": " + it.Result})
		m.do(Op{Kind: "set", Name: n, Field: "result", Old: it.Result})
	}
	m.do(Op{Kind: "set", Name: n, Field: "status", Old: it.Status})
	out := []string{n}
	if !dropped {
		return out
	}
	m.eachBelow(n, false, func(step string, s *Todo) {
		// Steps dropped with their parent keep no reason; one dropped on
		// its own has one, and stays dropped.
		if s.Status == "dropped" && s.Result == "" && m.get(parentOf(step)).Status == "" {
			m.do(Op{Kind: "set", Name: step, Field: "status", Old: "dropped"})
			out = append(out, step)
		}
	})
	return out
}

// Note records a finding or an attempt on item name, open or closed.
func (m *Mem) Note(name, note string) {
	it := m.item(name)
	note = clip(text("the note", note), maxText)
	if note == "" {
		panic("a note needs words: mem.Note(\"" + name + "\", \"what you found\")")
	}
	m.do(Op{Kind: "note", Name: name, New: note})
	println("noted on " + ref(name, it.Title))
}

// Wait sets what open item name waits for: a day, "2026-12-01", until that
// day; or words, "PR #6399 merged", until Wait(name, "") or Done clears it.
// A wait holds back only next: every call still works on a waiting item.
// It refuses a day written any other way, alone or after until, after, on
// or by (Nov 3, 11/03, next Monday, tomorrow, in 2 days); a 2026-11-03 among
// other words or not on the calendar; a day before yesterday; and an item's
// name, since a step is how one item waits for another. A refusal names
// today ("today is Thu 2026-10-29").
func (m *Mem) Wait(name, until string) {
	it := m.openItem(name)
	until = m.parseWait(clip(text("the wait", until), maxText))
	if until == it.Wait {
		println(ref(name, it.Title) + waitText(it.Wait) + " already")
		return
	}
	m.do(Op{Kind: "set", Name: name, Field: "wait", Old: it.Wait, New: until})
	msg := ref(name, it.Title) + waitText(until)
	if goalOf(name) != name {
		msg += m.whatNext(goalOf(name))
	}
	println(msg)
}

// Fact keeps text under name, any path, replacing what was there; "" removes
// it. Its receipt says whether it is new or changed (and from what), and
// lists the other facts beside it.
func (m *Mem) Fact(name, fact string) {
	m.checkName(name, "fact")
	fact = clip(text("the fact", fact), maxText)
	old := ""
	if f := m.getFact(name); f != nil {
		old = f.Text
	}
	switch {
	case fact == old && fact == "":
		panic("no fact `" + name + "`" + m.factsBeside(name, false))
	case fact == old:
		println("fact `" + name + "` is unchanged")
		return
	}
	m.do(Op{Kind: "fact", Name: name, Old: old, New: fact})
	switch {
	case fact == "":
		println("removed fact `" + name + "`")
	case old == "":
		println("fact `" + name + "` is new" + m.factsBeside(name, true))
	default:
		println("fact `" + name + "` changed (was " + strconv.Quote(old) + ")")
	}
}

// Rule keeps a standing instruction from the user under name; "" removes
// it. Every session is given every rule in full, and klash tells the user
// of each change. Only the user's words become rules.
func (m *Mem) Rule(name, rule string) {
	m.checkName(name, "rule")
	rule = clip(text("the rule", rule), maxText)
	old := ""
	if r := m.getRule(name); r != nil {
		old = r.Text
	}
	switch {
	case rule == old && rule == "":
		panic("no rule `" + name + "`")
	case rule == old:
		println("rule `" + name + "` is unchanged")
		return
	}
	m.do(Op{Kind: "rule", Name: name, Old: old, New: rule, By: caller()})
	switch {
	case rule == "":
		println("removed rule `" + name + "`")
	case old == "":
		println("rule `" + name + "` is new; every session will be given it")
	default:
		println("rule `" + name + "` changed (was " + strconv.Quote(old) + ")")
	}
}

// Forget empties closed item name and every item under it (title, result,
// wait, notes) and closes them as forgotten. Klash erases their text from
// the journal and the index. It refuses an open item (Drop it first);
// every other call refuses a forgotten item. Its receipt names no text.
func (m *Mem) Forget(name string) {
	it := m.get(name)
	if it == nil {
		m.item(name) // refuses, saying why
	}
	switch it.Status {
	case "":
		panic("`" + name + "` is open: mem.Drop(\"" + name + "\", \"why\") first, then forget it")
	case "forgotten":
		println("`" + name + "` is forgotten already")
		return
	}
	var gone []string
	m.eachBelow(name, true, func(step string, s *Todo) {
		if s.Status != "forgotten" {
			m.do(Op{Kind: "forget", Name: step})
			gone = append(gone, step)
		}
	})
	m.do(Op{Kind: "forget", Name: name})
	msg := "forgot `" + name + "`"
	if len(gone) > 0 {
		msg += " and its " + plural(len(gone), "step")
	}
	println(msg + "; klash erases their text from its files")
}

// Redact replaces a secret's exact text with "(redacted)" wherever mem holds
// or ever held it: titles, results, waits, notes, facts, rules, and the
// statements of past inputs. It is for a secret's exact text: it refuses
// text under 6 bytes, text held nowhere, text that overlaps "(redacted)",
// and text held in more than 3 places (/mem redact has no such limit). Its
// receipt says how many places, never which or what.
func (m *Mem) Redact(secret string) {
	text("the text", secret)
	if len(secret) < 6 {
		panic("Redact takes a secret's exact text, 6 bytes or more")
	}
	if overlapsMarker(secret) {
		panic("that text overlaps \"(redacted)\": give the secret alone")
	}
	names, kinds, statements := holding(secret)
	places := len(names)
	if statements && places == 0 {
		places = 1
	}
	switch {
	case places == 0:
		panic("mem holds no such text; Redact takes a secret's exact text")
	case places > 3 && caller() != "user":
		panic("that text is in " + strconv.Itoa(places) + " places: Redact is for a secret's exact text, and the user can erase wider with /mem redact")
	}
	for i, n := range names {
		m.do(Op{Kind: "redact", Name: n, Field: kinds[i], Old: secret})
	}
	if len(names) == 0 {
		m.do(Op{Kind: "redact", Field: "statements", Old: secret})
	}
	println("redacted it in " + plural(places, "place") + "; klash rewrites mem's files")
}

// do applies op and hands it to klash; nothing else changes a Mem.
// time.Now() is the call's time, which klash fixes once per call, so all
// its Ops share it.
func (m *Mem) do(op Op) {
	m.apply(op, time.Now().Unix())
	record(op.Kind, op.Name, op.Field, op.Old, op.New, op.By)
}

// Apply applies the Ops klash hands it, stamped with their call's time,
// without recording them: klash rebuilds a Mem from its journal with it,
// and undoes with it. It panics unless klash runs the input.
func (m *Mem) Apply() {
	if who := caller(); who != "klash" && who != "user" {
		panic("only klash may apply Ops")
	}
	kinds, names, fields, olds, news, bys, at := pending()
	for i := range kinds {
		m.apply(Op{Kind: kinds[i], Name: names[i], Field: fields[i], Old: olds[i], New: news[i], By: bys[i]}, at)
	}
}
