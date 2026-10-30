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
	"unicode/utf8"
)

// Names. One grammar covers items, facts and rules: segments joined by "/";
// a segment is words of [a-z0-9] joined by single "-"; the first segment
// has a letter. A name is typed exactly as printed; anything else is
// refused with the name it should have been.
//
// An item's name is its goal's name, then step numbers: release-v2/2/1. A
// goal's name is one segment of at most 4 words and 32 bytes, made from
// its title or given, never reserved and never given twice. A fact's or a
// rule's name is any name of at most 64 bytes.
const (
	maxGoalName = 32
	maxName     = 64
)

// reserved are names no goal may take.
var reserved = map[string]bool{"all": true, "next": true, "none": true}

// filler are the words a goal's name leaves out.
var filler = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true, "be": true,
	"by": true, "for": true, "from": true, "in": true, "is": true, "it": true, "its": true,
	"my": true, "of": true, "on": true, "or": true, "our": true, "that": true, "the": true,
	"this": true, "to": true, "with": true, "your": true,
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

// validName reports whether s follows the name grammar.
func validName(s string) bool {
	if s == "" {
		return false
	}
	for i, seg := range strings.Split(s, "/") {
		if seg == "" || seg[0] == '-' || seg[len(seg)-1] == '-' || strings.Contains(seg, "--") {
			return false
		}
		letter := false
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z':
				letter = true
			case r >= '0' && r <= '9', r == '-':
			default:
				return false
			}
		}
		if i == 0 && !letter {
			return false
		}
	}
	return true
}

// nameForm is s as a name: lowercase words of [a-z0-9] joined by "-", with
// ' and ’ deleted and Latin accents folded. Redact matches names with it.
func nameForm(s string) string {
	return strings.Join(words(s), "-")
}

func words(s string) []string {
	var out []string
	var w strings.Builder
	flush := func() {
		if w.Len() > 0 {
			out = append(out, w.String())
			w.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		r = fold(r)
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			w.WriteRune(r)
		case r == '\'' || r == '’':
		default:
			flush()
		}
	}
	flush()
	return out
}

// fold turns a Latin letter with an accent into its plain letter.
func fold(r rune) rune {
	const from, to = "àáâãäåçèéêëìíîïñòóôõöùúûüýÿ", "aaaaaaceeeeiiiinooooouuuuyy"
	if i := strings.IndexRune(from, r); i >= 0 {
		return []rune(to)[utf8.RuneCountInString(from[:i])]
	}
	return r
}

// goalName makes a goal's name from its title: filler words dropped, words
// that look like keys or hashes skipped (so a token in a title rarely
// reaches a name), at most 4 words and 32 bytes.
func goalName(title string) string {
	all := words(title)
	var kept []string
	for _, w := range all {
		if !filler[w] && !keyLike(w) {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		for _, w := range all {
			if !keyLike(w) {
				kept = append(kept, w)
			}
		}
	}
	name := ""
	for i, w := range kept {
		next := w
		if name != "" {
			next = name + "-" + w
		}
		if i == 4 || len(next) > maxGoalName {
			break
		}
		name = next
	}
	switch {
	case name == "":
		return "goal"
	case !validName(name):
		return "goal-" + name
	}
	return name
}

// keyLike reports whether w looks like a key or a hash: over 20 bytes, or
// over 8 bytes mixing letters and digits.
func keyLike(w string) bool {
	if len(w) > 20 {
		return true
	}
	if len(w) <= 8 {
		return false
	}
	letters, digits := false, false
	for _, r := range w {
		if r >= '0' && r <= '9' {
			digits = true
		} else {
			letters = true
		}
	}
	return letters && digits
}

// newGoalName is the name a new goal gets: the one given, which must be
// free, or one made from its title, with -2, -3, … if taken.
func (m *Mem) newGoalName(title string, given ...string) string {
	free := func(n string) bool { return !reserved[n] && !forgottenName(n) && m.taken.Get(n) == nil }
	if len(given) > 0 && given[0] != "" {
		n := given[0]
		switch {
		case len(given) > 1:
			panic("a goal takes one name: mem.Goal(\"title\", \"name\")")
		case strings.Contains(n, "/"):
			panic("a goal's name has no \"/\" (`" + n + "` would be a step of `" + parentOf(n) + "`): try `" + nameForm(n) + "`")
		case !validName(n):
			panic("`" + n + "` is not a name: a goal's name is lowercase words joined by \"-\", like `" + goalName(n) + "`")
		case len(n) > maxGoalName || strings.Count(n, "-") >= 4:
			panic("a goal's name is at most 4 words and " + strconv.Itoa(maxGoalName) + " bytes; the title comes first: mem.Goal(\"" + title + "\", \"" + goalName(n) + "\")")
		case reserved[n] || forgottenName(n):
			panic("`" + n + "` is reserved; leave the name out to get `" + m.newGoalName(title) + "`")
		case !free(n):
			panic("the name `" + n + "` is taken, and names are never given twice: give another, like `" + m.freeName(n, free) + "`, or leave it out")
		}
		return n
	}
	return m.freeName(goalName(title), free)
}

func (m *Mem) freeName(base string, free func(string) bool) string {
	if free(base) {
		return base
	}
	for i := 2; ; i++ {
		if n := base + "-" + strconv.Itoa(i); free(n) {
			return n
		}
	}
}

func forgottenName(n string) bool {
	return strings.HasPrefix(n, "forgotten-")
}

// sameTitle is a title as titles are matched: ASCII case folded, runs of
// white space as one space, trailing . , ; : ! ? dropped.
func sameTitle(t string) string {
	t = strings.Join(strings.Fields(strings.ToLower(t)), " ")
	return strings.TrimRight(t, ".,;:!?")
}

// refuseOpenTitle refuses a new top-level item whose title an open one has.
func (m *Mem) refuseOpenTitle(title string) {
	want := sameTitle(title)
	m.open.Iterate("", "", func(_ string, v any) bool {
		n := v.(string)
		it := m.get(n)
		if sameTitle(it.Title) != want {
			return false
		}
		panic(ref(n, it.Title) + " (" + who(it.By) + ", " + ago(it.Changed) + ", " + m.progress(n, it) + ") is already open: add to it with mem.Steps(\"" + n + "\", \"…\"), or give other work a title that tells them apart")
	})
}

// checkName refuses a fact's or a rule's name outside the grammar.
func (m *Mem) checkName(name, kind string) {
	switch {
	case name == "":
		panic("a " + kind + " needs a name, like `cmd/test`")
	case strings.ToLower(name) != name && validName(strings.ToLower(name)):
		panic("names are lowercase: `" + strings.ToLower(name) + "`")
	case !validName(name):
		panic("`" + name + "` is not a name: a " + kind + "'s name is lowercase words joined by \"-\" and \"/\", like `" + nameForm(name) + "`")
	case len(name) > maxName:
		panic("a " + kind + "'s name is at most " + strconv.Itoa(maxName) + " bytes")
	}
}

// item is item name, refusing a name that is not one, with what it should
// have been, and a forgotten item.
func (m *Mem) item(name string) *Todo {
	if it := m.get(name); it != nil && validName(name) {
		if it.Status == "forgotten" {
			panic("`" + name + "` is forgotten")
		}
		return it
	}
	panic(m.miss(name))
}

// openItem is open item name.
func (m *Mem) openItem(name string) *Todo {
	it := m.item(name)
	if it.Status != "" {
		panic(ref(name, it.Title) + " is " + it.Status + arrow(it.Result) + "; mem.Reopen(\"" + name + "\") to work on it again")
	}
	return it
}

// miss says why name names no item, and what it might have been.
func (m *Mem) miss(name string) string {
	low := strings.ToLower(strings.TrimSpace(name))
	switch {
	case name == "":
		return "no item named \"\": mem.Show() lists them"
	case low != name && m.get(low) != nil:
		return "names are lowercase, typed as printed: `" + low + "`"
	case strings.Contains(name, "#") || isNumber(strings.TrimLeft(name, "#")):
		return "`" + name + "` is not a name; steps are named under their goal, like `goal/2`: mem.Show() lists them"
	case m.getFact(name) != nil:
		return "`" + name + "` is a fact, not an item; to change it: mem.Fact(\"" + name + "\", \"…\")"
	case m.getRule(name) != nil:
		return "`" + name + "` is a rule, not an item; to change it: mem.Rule(\"" + name + "\", \"…\")"
	}
	if p := parentOf(name); p != "" {
		if it := m.get(p); it != nil {
			if it.Last == 0 {
				return "no item `" + name + "`; `" + p + "` has no steps"
			}
			return "no item `" + name + "`; `" + p + "` has `" + p + "/1` to `" + p + "/" + strconv.Itoa(it.Last) + "`"
		}
	}
	return "no item `" + name + "`; mem.Show() lists them"
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// text refuses text that is not valid UTF-8, and trims it.
func text(what, s string) string {
	if !utf8.ValidString(s) {
		panic(what + " is not valid UTF-8")
	}
	return strings.TrimSpace(s)
}

// clip cuts s to at most n bytes, at a character, with "…".
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// overlapsMarker reports whether redacting s could leave s behind: s holds
// "(redacted)", lies inside it, or begins or ends partway into it.
func overlapsMarker(s string) bool {
	if strings.Contains(s, redacted) || strings.Contains(redacted, s) {
		return true
	}
	for i := 1; i < len(redacted); i++ {
		if strings.HasSuffix(s, redacted[:i]) || strings.HasPrefix(s, redacted[i:]) {
			return true
		}
	}
	return false
}
