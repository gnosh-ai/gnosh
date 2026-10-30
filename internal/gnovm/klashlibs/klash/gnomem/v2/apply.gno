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

// maxSteps caps the steps under one item: keys pad step numbers to 4
// digits, so that keys sort as the outline does.
const maxSteps = 9999

// apply is the only code that changes a Mem. It reads only the Mem, op and
// at, and panics, so nothing is kept, when op does not fit:
//   - set, fact, rule, unnote: Old is not the current value (unnote: the
//     last note);
//   - add: the name exists, its parent is missing or closed, or its step
//     number is not 1 to maxSteps; unadd: the item is closed, or has steps,
//     notes, a result or a wait;
//   - a set would open an item under a closed one, or close one with an
//     open step;
//   - a forget names an open item, or a missing fact or rule.
//
// A redact naming a missing item, fact or rule changes and stamps nothing.
func (m *Mem) apply(op Op, at int64) {
	switch op.Kind {
	case "add":
		m.applyAdd(op)
	case "unadd":
		m.applyUnadd(op)
	case "set":
		m.applySet(op, at)
	case "note":
		it := m.must(op)
		notes := make([]string, len(it.Notes), len(it.Notes)+1)
		copy(notes, it.Notes)
		it.Notes = append(notes, op.New)
	case "unnote":
		it := m.must(op)
		if len(it.Notes) == 0 || it.Notes[len(it.Notes)-1] != op.Old {
			panic("apply: `" + op.Name + "`'s last note is not " + strconv.Quote(op.Old))
		}
		notes := make([]string, len(it.Notes)-1)
		copy(notes, it.Notes)
		it.Notes = notes
	case "fact":
		m.applyFact(op, at)
		return
	case "rule":
		m.applyRule(op, at)
		return
	case "forget":
		switch op.Field {
		case "fact":
			f := m.getFact(op.Name)
			if f == nil {
				panic("apply: no fact `" + op.Name + "`")
			}
			m.recent.Remove(stampKey(f.Changed, op.Name))
			m.facts.Remove(op.Name)
			m.group(op.Name, -1)
			return
		case "rule":
			if m.getRule(op.Name) == nil {
				panic("apply: no rule `" + op.Name + "`")
			}
			m.rules.Remove(op.Name)
			return
		}
		it := m.must(op)
		if it.Status == "" {
			panic("apply: cannot forget open `" + op.Name + "`")
		}
		it.Title, it.Result, it.Notes = "", "", nil
		if it.Wait != "" {
			m.setWait(op.Name, it, "")
		}
		m.setStatus(op.Name, it, "forgotten", at)
	case "redact":
		if !m.applyRedact(op) {
			return
		}
		if op.Field != "item" {
			m.stampFact(op.Field, op.Name, at)
			return
		}
	default:
		panic("apply: unknown op " + strconv.Quote(op.Kind))
	}
	m.stamp(op.Name, at)
}

func (m *Mem) applyAdd(op Op) {
	if m.get(op.Name) != nil {
		panic("apply: `" + op.Name + "` exists")
	}
	if p := parentOf(op.Name); p != "" {
		parent := m.get(p)
		if parent == nil || parent.Status != "" {
			panic("apply: `" + p + "` is missing or closed")
		}
		n := stepNumber(op.Name)
		if n < 1 || n > maxSteps {
			panic("apply: `" + op.Name + "` has no step number from 1 to " + strconv.Itoa(maxSteps))
		}
		if n > parent.Last {
			parent.Last = n
		}
		parent.OpenSteps++
	} else {
		m.taken.Set(op.Name, true)
		m.open.Set(stampKey(0, op.Name), op.Name)
		m.titles.Set(sameTitle(op.New)+"|"+op.Name, op.Name)
	}
	m.items.Set(key(op.Name), &Todo{Title: op.New, By: op.By})
	m.counts.Open++
}

func (m *Mem) applyUnadd(op Op) {
	it := m.must(op)
	if it.Status != "" || it.Result != "" || it.Wait != "" || it.For != "" || it.Doing != "" || len(it.Notes) > 0 || m.hasSteps(op.Name) {
		panic("apply: `" + op.Name + "` has changed since it was added")
	}
	m.items.Remove(key(op.Name))
	if p := parentOf(op.Name); p == "" {
		m.open.Remove(stampKey(it.Changed, op.Name))
		m.titles.Remove(sameTitle(it.Title) + "|" + op.Name)
	} else {
		m.get(p).OpenSteps--
	}
	m.counts.Open--
}

func (m *Mem) applySet(op Op, at int64) {
	it := m.must(op)
	var cur string
	switch op.Field {
	case "status":
		cur = it.Status
	case "result":
		cur = it.Result
	case "wait":
		cur = it.Wait
	case "for":
		cur = it.For
	case "doing":
		cur = it.Doing
	default:
		panic("apply: unknown field " + strconv.Quote(op.Field))
	}
	if cur != op.Old {
		panic("apply: `" + op.Name + "`'s " + op.Field + " is " + strconv.Quote(cur) + ", not " + strconv.Quote(op.Old))
	}
	switch op.Field {
	case "status":
		m.setStatus(op.Name, it, op.New, at)
	case "result":
		it.Result = op.New
	case "wait":
		m.setWait(op.Name, it, op.New)
	case "for":
		if it.Status == "" {
			if it.For != "" {
				m.asks.Remove(op.Name)
				m.counts.ForUser--
			}
			if op.New != "" {
				m.asks.Set(op.Name, true)
				m.counts.ForUser++
			}
		}
		it.For = op.New
	case "doing":
		it.Doing = op.New
	}
}

// setStatus moves item name to status, keeping the counts and indexes.
func (m *Mem) setStatus(name string, it *Todo, status string, at int64) {
	if status == it.Status {
		return
	}
	var parent *Todo
	if p := parentOf(name); p != "" {
		parent = m.get(p)
	}
	if status == "" {
		if parent != nil && parent.Status != "" {
			panic("apply: cannot open `" + name + "` under closed `" + parentOf(name) + "`")
		}
	} else if it.Status == "" && it.OpenSteps > 0 {
		panic("apply: cannot close `" + name + "` while a step of it is open")
	}
	if parent != nil {
		parent.countStep(it.Status, -1)
		parent.countStep(status, 1)
	}
	top := parent == nil
	if it.Status == "" {
		// Leaving open.
		m.counts.Open--
		if top {
			m.open.Remove(stampKey(it.Changed, name))
			m.titles.Remove(sameTitle(it.Title) + "|" + name)
		}
		if it.For != "" {
			m.asks.Remove(name)
			m.counts.ForUser--
		}
		if it.Wait != "" {
			m.counts.Waits--
			m.unwait(name, it.Wait)
		}
	} else {
		m.counts.countStatus(it.Status, -1)
		if top {
			m.closed.Remove(stampKey(it.Changed, name))
		}
	}
	it.Status = status
	if status == "" {
		m.counts.Open++
		if top {
			m.open.Set(stampKey(it.Changed, name), name)
			m.titles.Set(sameTitle(it.Title)+"|"+name, name)
		}
		if it.For != "" {
			m.asks.Set(name, true)
			m.counts.ForUser++
		}
		if it.Wait != "" {
			m.counts.Waits++
			m.rewait(name, it.Wait)
		}
	} else {
		m.counts.countStatus(status, 1)
		if top && status != "forgotten" {
			// stamp re-keys it to at, after this Op.
			m.closed.Set(stampKey(it.Changed, name), name)
		}
	}
}

// countStep counts a step of it in or out of its open and done steps.
func (it *Todo) countStep(status string, d int) {
	switch status {
	case "":
		it.OpenSteps += d
	case "done":
		it.DoneSteps += d
	}
}

// countStatus counts an item in or out of the closed status it has.
func (c *Counts) countStatus(status string, d int) {
	switch status {
	case "done":
		c.Done += d
	case "dropped":
		c.Dropped += d
	case "forgotten":
		c.Forgotten += d
	}
}

// setWait changes open-or-closed item name's wait, keeping the indexes.
func (m *Mem) setWait(name string, it *Todo, wait string) {
	if it.Status == "" {
		if it.Wait != "" {
			m.counts.Waits--
			m.unwait(name, it.Wait)
		}
		if wait != "" {
			m.counts.Waits++
			m.rewait(name, wait)
		}
	}
	it.Wait = wait
}

// rewait puts open item name, waiting for wait, in the days or the words
// index; unwait takes it out.
func (m *Mem) rewait(name, wait string) {
	if isDay(wait) {
		m.days.Set(wait+"|"+name, name)
	} else {
		m.words.Set(name, true)
	}
}

func (m *Mem) unwait(name, wait string) {
	if isDay(wait) {
		m.days.Remove(wait + "|" + name)
	} else {
		m.words.Remove(name)
	}
}

func (m *Mem) applyFact(op Op, at int64) {
	f := m.getFact(op.Name)
	cur := ""
	if f != nil {
		cur = f.Text
	}
	if cur != op.Old {
		panic("apply: fact `" + op.Name + "` is " + strconv.Quote(cur) + ", not " + strconv.Quote(op.Old))
	}
	if f != nil {
		m.recent.Remove(stampKey(f.Changed, op.Name))
	}
	if op.New == "" {
		m.facts.Remove(op.Name)
		m.group(op.Name, -1)
		return
	}
	if f == nil {
		f = &Fact{}
		m.facts.Set(op.Name, f)
		m.group(op.Name, 1)
	}
	f.Text, f.Changed = op.New, at
	m.recent.Set(stampKey(at, op.Name), op.Name)
}

func (m *Mem) applyRule(op Op, at int64) {
	r := m.getRule(op.Name)
	cur := ""
	if r != nil {
		cur = r.Text
	}
	if cur != op.Old {
		panic("apply: rule `" + op.Name + "` is " + strconv.Quote(cur) + ", not " + strconv.Quote(op.Old))
	}
	if op.New == "" {
		m.rules.Remove(op.Name)
		return
	}
	if r == nil {
		r = &Rule{}
		m.rules.Set(op.Name, r)
	}
	r.Text, r.Changed, r.By = op.New, at, op.By
}

// applyRedact replaces op.Old with "(redacted)" in what op names, and
// reports whether there was anything to change.
func (m *Mem) applyRedact(op Op) bool {
	re := func(s string) string { return strings.ReplaceAll(s, op.Old, redacted) }
	switch op.Field {
	case "item":
		it := m.get(op.Name)
		if it == nil {
			return false
		}
		if parentOf(op.Name) == "" && it.Status == "" {
			m.titles.Remove(sameTitle(it.Title) + "|" + op.Name)
			m.titles.Set(sameTitle(re(it.Title))+"|"+op.Name, op.Name)
		}
		it.Title, it.Result = re(it.Title), re(it.Result)
		if it.Wait != "" {
			m.setWait(op.Name, it, re(it.Wait))
		}
		notes := make([]string, len(it.Notes))
		for i, n := range it.Notes {
			notes[i] = re(n)
		}
		it.Notes = notes
	case "fact":
		f := m.getFact(op.Name)
		if f == nil {
			return false
		}
		f.Text = re(f.Text)
	case "rule":
		r := m.getRule(op.Name)
		if r == nil {
			return false
		}
		r.Text = re(r.Text)
	default:
		return false
	}
	return true
}

const redacted = "(redacted)"

// stamp marks item name, and the items above it, as changed at at,
// re-keying its goal in the open or closed index, which order by time.
func (m *Mem) stamp(name string, at int64) {
	for n := name; n != ""; n = parentOf(n) {
		it := m.get(n)
		if it == nil {
			continue
		}
		if parentOf(n) == "" {
			switch it.Status {
			case "":
				m.open.Remove(stampKey(it.Changed, n))
				m.open.Set(stampKey(at, n), n)
			case "done", "dropped":
				m.closed.Remove(stampKey(it.Changed, n))
				m.closed.Set(stampKey(at, n), n)
			}
		}
		it.Changed = at
	}
}

// stampFact marks fact or rule name as changed at at.
func (m *Mem) stampFact(kind, name string, at int64) {
	switch kind {
	case "fact":
		if f := m.getFact(name); f != nil {
			m.recent.Remove(stampKey(f.Changed, name))
			f.Changed = at
			m.recent.Set(stampKey(at, name), name)
		}
	case "rule":
		if r := m.getRule(name); r != nil {
			r.Changed = at
		}
	}
}

// sameTitle is a title as titles are matched: ASCII case folded, runs of
// white space as one space, trailing . , ; : ! ? dropped. The titles
// index is keyed by it, so it lives here, under the layout's pin.
func sameTitle(t string) string {
	t = strings.Join(strings.Fields(strings.ToLower(t)), " ")
	return strings.TrimRight(t, ".,;:!?")
}

// factGroup is the group a fact's name is in, as Show lists hidden facts:
// its first part with the "/", as `cmd/`, or the whole name when it has
// none.
func factGroup(name string) string {
	if i := strings.Index(name, "/"); i >= 0 {
		return name[:i+1]
	}
	return name
}

// group counts fact name in or out of its group.
func (m *Mem) group(name string, d int) {
	g := factGroup(name)
	n := d
	if v := m.groups.Get(g); v != nil {
		n += v.(int)
	}
	if n <= 0 {
		m.groups.Remove(g)
	} else {
		m.groups.Set(g, n)
	}
}

// stampKey orders by time, then name: 12 digits of Unix seconds.
func stampKey(at int64, name string) string {
	s := strconv.FormatInt(at, 10)
	for len(s) < 12 {
		s = "0" + s
	}
	return s + "|" + name
}

func (m *Mem) must(op Op) *Todo {
	it := m.get(op.Name)
	if it == nil {
		panic("apply: no item `" + op.Name + "`")
	}
	return it
}

func (m *Mem) get(name string) *Todo {
	v := m.items.Get(key(name))
	if v == nil {
		return nil
	}
	return v.(*Todo)
}

func (m *Mem) getFact(name string) *Fact {
	v := m.facts.Get(name)
	if v == nil {
		return nil
	}
	return v.(*Fact)
}

func (m *Mem) getRule(name string) *Rule {
	v := m.rules.Get(name)
	if v == nil {
		return nil
	}
	return v.(*Rule)
}

func (m *Mem) hasSteps(name string) bool {
	found := false
	m.items.Iterate(key(name)+"/", key(name)+"/\xff", func(string, any) bool {
		found = true
		return true
	})
	return found
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

// isDay reports whether a wait is a day, 2026-12-01, which the days index
// holds, rather than words.
func isDay(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, r := range s {
		if i != 4 && i != 7 && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// parentOf is the name of the item name is a step of; "" for a goal.
func parentOf(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[:i]
	}
	return ""
}

// stepNumber is name's last segment as a step number, or 0.
func stepNumber(name string) int {
	n, err := strconv.Atoi(name[strings.LastIndex(name, "/")+1:])
	if err != nil {
		return 0
	}
	return n
}
