// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnomem

import (
	"strconv"
	"strings"
	"time"
)

const (
	maxShow       = 3000 // about the most Show or Find writes
	maxShowClosed = 5    // closed goals Show lists
	maxShowFacts  = 10   // facts Show lists
	maxLine       = 160  // bytes of a result, wait or fact a line shows
)

// Show returns mem as an outline: the rules, the questions for the user,
// what is due, the open goals newest first with their steps and progress,
// the newest closed goals, then the newest facts. Given names, it shows
// those items, facts or rules in full; a name that only has names under it
// ("cmd") lists them. About 3000 bytes at most.
func (m *Mem) Show(names ...string) string {
	var sb strings.Builder
	if len(names) > 0 {
		for _, n := range names {
			m.showName(&sb, n)
		}
		return cut(sb.String())
	}
	m.showHeader(&sb)
	if m.rules.Size() > 0 {
		sb.WriteString("rules:\n")
		m.rules.Iterate("", "", func(n string, v any) bool {
			sb.WriteString("  `" + n + "` = " + line(v.(*Rule).Text, maxText) + "\n")
			return false
		})
	}
	if m.asks.Size() > 0 {
		sb.WriteString("questions for the user:\n")
		m.asks.Iterate("", "", func(n string, _ any) bool {
			sb.WriteString("  [?] " + ref(n, m.get(n).Title) + "\n")
			return false
		})
	}
	var due []string
	m.days.Iterate("", today()+"|\xff", func(_ string, v any) bool {
		due = append(due, v.(string))
		return false
	})
	if len(due) > 0 {
		sb.WriteString("due:\n")
		for _, n := range due {
			sb.WriteString("  [ ] " + ref(n, m.get(n).Title) + " (since " + m.get(n).Wait + ")\n")
		}
	}
	if m.open.Size() > 0 {
		sb.WriteString("goals:\n")
		// Newest first.
		m.open.ReverseIterate("", "", func(_ string, v any) bool {
			m.outline(&sb, v.(string))
			return sb.Len() > maxShow
		})
	}
	if m.closed.Size() > 0 {
		sb.WriteString("closed lately:\n")
		i := 0
		m.closed.ReverseIterate("", "", func(_ string, v any) bool {
			n := v.(string)
			it := m.get(n)
			sb.WriteString("  " + mark(it) + " " + ref(n, it.Title) + arrow(it.Result) + " (" + day(it.Changed) + ")\n")
			i++
			return i == maxShowClosed
		})
	}
	if m.facts.Size() > 0 {
		sb.WriteString("facts, newest first:\n")
		i := 0
		m.recent.ReverseIterate("", "", func(_ string, v any) bool {
			n := v.(string)
			sb.WriteString("  `" + n + "` = " + line(m.getFact(n).Text, maxLine) + "\n")
			i++
			return i == maxShowFacts
		})
		if more := m.facts.Size() - i; more > 0 {
			sb.WriteString("  … " + plural(more, "more fact") + ": mem.Show(\"name\") or mem.Find(\"words\")\n")
		}
	}
	return cut(sb.String())
}

// String is Show(), so that println(mem) shows mem.
func (m *Mem) String() string { return m.Show() }

func (m *Mem) showHeader(sb *strings.Builder) {
	c := m.counts
	if c.Open+c.Done+c.Dropped == 0 && m.facts.Size() == 0 && m.rules.Size() == 0 {
		sb.WriteString("mem is empty: mem.Goal(\"what to get done\").Steps(\"…\", \"…\") plans a task, and mem.Fact(\"cmd/test\", \"…\") keeps a fact\n")
		return
	}
	parts := []string{plural(m.open.Size(), "goal") + " open"}
	if c.ForUser > 0 {
		parts[0] += ", " + plural(c.ForUser, "question") + " for the user"
	}
	if closed := m.closed.Size(); closed > 0 {
		parts = append(parts, strconv.Itoa(closed)+" closed")
	}
	if n := m.rules.Size(); n > 0 {
		parts = append(parts, plural(n, "rule"))
	}
	if n := m.facts.Size(); n > 0 {
		parts = append(parts, plural(n, "fact"))
	}
	sb.WriteString("mem: " + strings.Join(parts, " · ") + "\n")
}

// outline writes goal name, and its steps if it is open, with its next
// step marked.
func (m *Mem) outline(sb *strings.Builder, name string) {
	it := m.get(name)
	sb.WriteString("  " + mark(it) + " " + ref(name, it.Title) + arrow(it.Result) + m.aside(name, it, true) + "\n")
	next := m.nextFor(name, it, caller())
	m.eachStep(name, it, func(step string, s *Todo) {
		m.outlineStep(sb, step, s, 2, next)
	})
}

func (m *Mem) outlineStep(sb *strings.Builder, name string, it *Todo, depth int, next string) {
	if it.Status == "forgotten" {
		return
	}
	sb.WriteString(strings.Repeat("  ", depth) + mark(it) + " " + ref(name, it.Title) + arrow(it.Result) + m.aside(name, it, false))
	if name == next {
		sb.WriteString("  ← next")
	}
	sb.WriteString("\n")
	if it.Status != "" {
		return
	}
	m.eachStep(name, it, func(step string, s *Todo) {
		m.outlineStep(sb, step, s, depth+1, next)
	})
}

// aside is what a line says in parentheses: progress, who and when for a
// goal; a wait, a claim or whose move it is for any item.
func (m *Mem) aside(name string, it *Todo, top bool) string {
	var parts []string
	if top {
		if it.Last > 0 {
			parts = append(parts, m.progress(name, it))
		}
		parts = append(parts, who(it.By), ago(it.Changed))
	}
	if it.Status == "" {
		switch {
		case it.Wait != "" && !m.waiting(it):
			parts = append(parts, "due since "+it.Wait)
		case it.Wait != "":
			parts = append(parts, strings.TrimSpace(waitText(it.Wait)))
		case len(m.openForUser(name, it)) > 0:
			parts = append(parts, "waits on the user")
		}
		if it.Doing != "" {
			parts = append(parts, "started by "+who(it.Doing))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " · ") + ")"
}

func (m *Mem) openForUser(name string, it *Todo) []string {
	var out []string
	m.eachStep(name, it, func(step string, s *Todo) {
		if s.Status == "" && s.For != "" {
			out = append(out, step)
		}
	})
	return out
}

// progress is "1/3 done": the steps done of those not dropped.
func (m *Mem) progress(name string, it *Todo) string {
	done, all := 0, 0
	m.eachStep(name, it, func(_ string, s *Todo) {
		switch s.Status {
		case "done":
			done++
			all++
		case "":
			all++
		}
	})
	return strconv.Itoa(done) + "/" + strconv.Itoa(all) + " done"
}

func (m *Mem) showName(sb *strings.Builder, name string) {
	shown := false
	if it := m.get(name); it != nil && it.Status != "forgotten" {
		m.showItem(sb, name, it)
		shown = true
	}
	if f := m.getFact(name); f != nil {
		sb.WriteString("fact `" + name + "` = " + f.Text + " (" + day(f.Changed) + ")\n")
		shown = true
	}
	if r := m.getRule(name); r != nil {
		sb.WriteString("rule `" + name + "` = " + r.Text + " (" + who(r.By) + ", " + day(r.Changed) + ")\n")
		shown = true
	}
	if shown {
		return
	}
	var under []string
	m.facts.Iterate(name+"/", name+"/\xff", func(n string, _ any) bool {
		under = append(under, "fact `"+n+"`")
		return len(under) == 50
	})
	m.rules.Iterate(name+"/", name+"/\xff", func(n string, _ any) bool {
		under = append(under, "rule `"+n+"`")
		return len(under) == 100
	})
	if len(under) > 0 {
		sb.WriteString("under `" + name + "/`: " + strings.Join(under, ", ") + "\n")
		return
	}
	sb.WriteString(m.miss(name) + "\n")
}

// showItem writes item name in full: its status, result, wait, claim, notes
// and every item under it.
func (m *Mem) showItem(sb *strings.Builder, name string, it *Todo) {
	sb.WriteString(mark(it) + " " + ref(name, it.Title))
	if p := parentOf(name); p != "" {
		sb.WriteString(" (a step of " + ref(p, m.get(p).Title) + ")")
	}
	sb.WriteString("\n")
	if it.Status != "" {
		sb.WriteString("  " + it.Status + arrow(it.Result) + "\n")
	}
	if it.Wait != "" {
		sb.WriteString("  " + strings.TrimSpace(waitText(it.Wait)) + "\n")
	}
	if it.For != "" {
		sb.WriteString("  for the user\n")
	}
	if it.Doing != "" {
		sb.WriteString("  started by " + who(it.Doing) + "\n")
	}
	sb.WriteString("  made by " + who(it.By) + ", changed " + ago(it.Changed) + "\n")
	for i, n := range it.Notes {
		sb.WriteString("  note " + strconv.Itoa(i+1) + ": " + n + "\n")
	}
	next := m.nextFor(goalOf(name), m.get(goalOf(name)), caller())
	m.eachStep(name, it, func(step string, s *Todo) {
		m.outlineStep(sb, step, s, 1, next)
	})
}

// Find lists items, closed ones too, facts and rules that match words, by
// what they say or what they mean, best first. About 3000 bytes at most.
func (m *Mem) Find(words string) string {
	words = text("the words", words)
	if words == "" {
		panic("Find needs words: mem.Find(\"release key\")")
	}
	names, kinds := find(words, 20)
	if len(names) == 0 {
		return "nothing in mem matches " + strconv.Quote(words) + "\n"
	}
	var sb strings.Builder
	for i, n := range names {
		switch kinds[i] {
		case "fact":
			if f := m.getFact(n); f != nil {
				sb.WriteString("fact `" + n + "` = " + line(f.Text, maxLine) + "\n")
			}
		case "rule":
			if r := m.getRule(n); r != nil {
				sb.WriteString("rule `" + n + "` = " + line(r.Text, maxLine) + "\n")
			}
		default:
			if it := m.get(n); it != nil && it.Status != "forgotten" {
				sb.WriteString(mark(it) + " " + ref(n, it.Title) + arrow(it.Result) + " (" + day(it.Changed) + ")\n")
			}
		}
	}
	return cut(sb.String())
}

// ref names an item by its name and title: `release-v2` "release v2".
func ref(name, title string) string {
	return "`" + name + "` " + strconv.Quote(line(title, 80))
}

// refs names items by name and title.
func (m *Mem) refs(names []string) string {
	var parts []string
	for _, n := range names {
		parts = append(parts, ref(n, m.get(n).Title))
	}
	return joinList(parts)
}

// names lists names in backticks.
func names(ns []string) string {
	var parts []string
	for _, n := range ns {
		parts = append(parts, "`"+n+"`")
	}
	return joinList(parts)
}

func joinList(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// arrow is " → result", or "" for none.
func arrow(result string) string {
	if result == "" {
		return ""
	}
	return " → " + line(result, maxLine)
}

// who says who a session id is to the session that asks.
func who(by string) string {
	switch by {
	case "":
		return "nobody"
	case "user":
		return "the user"
	case "klash":
		return "klash"
	case caller():
		return "this session"
	}
	return "session " + by
}

// ago is how long before now t was, roughly: "5m ago", "3h ago", "2d ago".
func ago(t int64) string {
	d := time.Now().Unix() - t
	switch {
	case d < 60:
		return "just now"
	case d < 3600:
		return strconv.FormatInt(d/60, 10) + "m ago"
	case d < 86400:
		return strconv.FormatInt(d/3600, 10) + "h ago"
	}
	return strconv.FormatInt(d/86400, 10) + "d ago"
}

// day is t as a day, 2026-10-29.
func day(t int64) string {
	return time.Unix(t, 0).UTC().Format("2006-01-02")
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

func mark(it *Todo) string {
	switch {
	case it.Status == "" && it.For != "":
		return "[?]"
	case it.Status == "":
		return "[ ]"
	case it.Status == "done":
		return "[x]"
	}
	return "[-]"
}

// line keeps text on one line, cut to n bytes.
func line(s string, n int) string {
	return clip(strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " ⏎ "), n)
}

// cut ends a long outline at a line, saying so.
func cut(s string) string {
	if len(s) <= maxShow {
		return s
	}
	i := strings.LastIndex(s[:maxShow], "\n")
	return s[:i+1] + "… (cut: mem.Show(\"name\") shows one goal, mem.Find(\"words\") searches)\n"
}

// factsBeside says which facts share name's first segment, for a receipt:
// "; `cmd/` also has `cmd/test`, `cmd/build`".
func (m *Mem) factsBeside(name string, new bool) string {
	prefix := goalOf(name)
	if prefix == name {
		return ""
	}
	var beside []string
	m.facts.Iterate(prefix+"/", prefix+"/\xff", func(n string, _ any) bool {
		if n != name {
			beside = append(beside, "`"+n+"`")
		}
		return len(beside) == 5
	})
	if len(beside) == 0 {
		return ""
	}
	if new {
		return "; `" + prefix + "/` also has " + joinList(beside)
	}
	return "; `" + prefix + "/` has " + joinList(beside)
}
