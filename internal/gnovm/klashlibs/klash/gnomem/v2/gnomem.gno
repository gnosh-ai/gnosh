// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

// Package gnomem is an agent's memory: the work it means to get done, as
// goals with steps, and what came of each. Klash keeps one Mem per project,
// in the realm klash.local/r/mem on disk, and binds it to the name mem in
// every input the model runs there:
//
//	mem.Goal("release v2").Steps("bump the version", "tag v2.0.0")
//	mem.Show()
//
// Everything has a name: a lowercase path, printed in backticks. A goal's
// name comes from its title (`release-v2`), and its steps are numbered under
// it (`release-v2/1`, `release-v2/2`). Names are typed exactly as printed.
//
// Every change is an Op, and one function, apply, changes a Mem. The calls
// only decide which Ops to make, and hand each to klash, which journals it.
// Items sit in a B+tree keyed by name, so a call loads only what it touches.
package gnomem

import (
	"strconv"
	"strings"
	"time"

	bptree "klash/bptree/v0"
)

// Mem is an agent's memory. Only its methods change it, and they all have
// pointer receivers: a value receiver would copy the Mem out of its realm.
type Mem struct {
	items bptree.BPTree // key(name) → *Todo
}

// Todo is a piece of work: a goal, or a step of one. Its name is its key.
type Todo struct {
	Title   string
	Status  string // "" while open; "done" or "dropped"
	Result  string // what came of it, or why it was dropped
	Last    int    // the highest step number given under it; never lowered
	Changed int64  // the latest change to it or to an item under it, Unix seconds
	By      string // who made it: a session's id, or "user"
}

// Op is one change to a Mem, as the journal records it.
type Op struct {
	Kind  string // "add"
	Name  string // the item; an add carries the full name
	Field string
	Old   string
	New   string // add: the title
	By    string // add: who made the item
}

// Goal is a goal just made, so its steps can follow in the same statement.
type Goal struct {
	Name string
	m    *Mem
}

// New returns an empty Mem.
func New() *Mem { return &Mem{} }

// Goal adds a goal, named from its title, and returns it so its steps can
// follow.
func (m *Mem) Goal(title string) Goal {
	title = strings.TrimSpace(title)
	if title == "" {
		panic("a goal needs a title")
	}
	name := goalName(title)
	if m.get(name) != nil {
		panic("the name `" + name + "` is taken: give the goal a title that tells it apart")
	}
	m.do(Op{Kind: "add", Name: name, New: title, By: caller()})
	println("added goal `" + name + "` " + strconv.Quote(title))
	return Goal{Name: name, m: m}
}

// Steps adds steps to the goal, numbered after the last it had.
func (g Goal) Steps(titles ...string) Goal {
	g.m.Steps(g.Name, titles...)
	return g
}

// Steps adds steps to open item name, numbered after the last it had.
func (m *Mem) Steps(name string, titles ...string) {
	parent := m.open(name)
	var added []string
	for i, title := range titles {
		title = strings.TrimSpace(title)
		if title == "" {
			panic("step " + strconv.Itoa(i+1) + " needs a title")
		}
		step := name + "/" + strconv.Itoa(parent.Last+1)
		m.do(Op{Kind: "add", Name: step, New: title, By: caller()})
		added = append(added, "`"+step+"` "+strconv.Quote(title))
	}
	if len(added) > 0 {
		println("added " + strings.Join(added, ", "))
	}
}

// Show returns mem as an outline, in the order items were added; given
// names, those items with everything under them.
func (m *Mem) Show(names ...string) string {
	var sb strings.Builder
	line := func(k string, it *Todo) {
		name := unkey(k)
		depth := strings.Count(name, "/")
		sb.WriteString(strings.Repeat("  ", depth) + mark(it.Status) + " `" + name + "` " + strconv.Quote(it.Title) + "\n")
	}
	if len(names) == 0 {
		m.items.Iterate("", "", func(k string, v any) bool {
			line(k, v.(*Todo))
			return false
		})
		if sb.Len() == 0 {
			return "mem is empty\n"
		}
		return sb.String()
	}
	for _, name := range names {
		it := m.get(name)
		if it == nil {
			sb.WriteString("no item `" + name + "`\n")
			continue
		}
		line(key(name), it)
		m.items.Iterate(key(name)+"/", key(name)+"/\xff", func(k string, v any) bool {
			line(k, v.(*Todo))
			return false
		})
	}
	return sb.String()
}

// Apply applies the Ops klash hands it, stamped with their call's time,
// without recording them: klash rebuilds a Mem from its journal with it,
// and undoes with it. It panics unless klash runs the input.
func (m *Mem) Apply() {
	if caller() != "klash" {
		panic("only klash may apply Ops")
	}
	kinds, names, fields, olds, news, bys, at := pending()
	for i := range kinds {
		m.apply(Op{Kind: kinds[i], Name: names[i], Field: fields[i], Old: olds[i], New: news[i], By: bys[i]}, at)
	}
}

// String is Show(), so that println(mem) shows mem.
func (m *Mem) String() string { return m.Show() }

// do applies op and hands it to klash; nothing else changes a Mem.
func (m *Mem) do(op Op) {
	m.apply(op, time.Now().Unix())
	record(op.Kind, op.Name, op.Field, op.Old, op.New, op.By)
}

// apply is the only code that changes a Mem. It panics, so nothing is kept,
// when op does not fit.
func (m *Mem) apply(op Op, at int64) {
	switch op.Kind {
	case "add":
		if m.get(op.Name) != nil {
			panic("apply: `" + op.Name + "` exists")
		}
		if i := strings.LastIndex(op.Name, "/"); i >= 0 {
			parent := m.get(op.Name[:i])
			if parent == nil || parent.Status != "" {
				panic("apply: `" + op.Name[:i] + "` is missing or closed")
			}
			n, err := strconv.Atoi(op.Name[i+1:])
			if err != nil || n < 1 {
				panic("apply: `" + op.Name + "` has no step number")
			}
			if n > parent.Last {
				parent.Last = n
			}
		}
		m.items.Set(key(op.Name), &Todo{Title: op.New, By: op.By})
	default:
		panic("apply: unknown op " + op.Kind)
	}
	m.stamp(op.Name, at)
}

// stamp marks item name, and the items above it, as changed at at.
func (m *Mem) stamp(name string, at int64) {
	for {
		if it := m.get(name); it != nil {
			it.Changed = at
		}
		i := strings.LastIndex(name, "/")
		if i < 0 {
			return
		}
		name = name[:i]
	}
}

func (m *Mem) get(name string) *Todo {
	v := m.items.Get(key(name))
	if v == nil {
		return nil
	}
	return v.(*Todo)
}

func (m *Mem) open(name string) *Todo {
	it := m.get(name)
	if it == nil {
		panic("no item `" + name + "`")
	}
	if it.Status != "" {
		panic("`" + name + "` is " + it.Status)
	}
	return it
}

// key is name with each step number padded to 4 digits, so the B+tree keeps
// an outline's order: release-v2/0002/0001.
func key(name string) string {
	segs := strings.Split(name, "/")
	for i := 1; i < len(segs); i++ {
		for len(segs[i]) < 4 {
			segs[i] = "0" + segs[i]
		}
	}
	return strings.Join(segs, "/")
}

// unkey undoes key.
func unkey(k string) string {
	segs := strings.Split(k, "/")
	for i := 1; i < len(segs); i++ {
		segs[i] = strings.TrimLeft(segs[i], "0")
	}
	return strings.Join(segs, "/")
}

// filler are the words a goal's name leaves out.
var filler = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true,
	"by": true, "for": true, "from": true, "in": true, "is": true, "it": true, "its": true,
	"my": true, "of": true, "on": true, "or": true, "our": true, "that": true, "the": true,
	"this": true, "to": true, "with": true, "your": true,
}

// goalName makes a goal's name from its title: lowercase words of [a-z0-9]
// joined by "-", filler dropped, at most 4 words and 32 bytes.
func goalName(title string) string {
	var words []string
	var w strings.Builder
	flush := func() {
		if w.Len() > 0 {
			words = append(words, w.String())
			w.Reset()
		}
	}
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			w.WriteRune(r)
		case r == '\'' || r == '’':
		default:
			flush()
		}
	}
	flush()
	var kept []string
	for _, word := range words {
		if !filler[word] {
			kept = append(kept, word)
		}
	}
	if len(kept) == 0 {
		kept = words
	}
	name := ""
	for _, word := range kept {
		next := word
		if name != "" {
			next = name + "-" + word
		}
		if strings.Count(next, "-") >= 4 || len(next) > 32 {
			break
		}
		name = next
	}
	if name == "" {
		return "goal"
	}
	if name[0] >= '0' && name[0] <= '9' {
		name = "goal-" + name
	}
	return name
}

func mark(status string) string {
	switch status {
	case "":
		return "[ ]"
	case "done":
		return "[x]"
	}
	return "[-]"
}

// caller is who runs this input: a session's id, "user" or "klash".
func caller() string

// record hands klash an Op of this call.
func record(kind, name, field, old, new, by string)

// pending is the Ops klash hands Apply, field by field, and their call's
// time.
func pending() (kinds, names, fields, olds, news, bys []string, at int64)
