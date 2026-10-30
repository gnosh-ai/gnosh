// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

// Package gnomem is an agent's working memory: what it means to get done,
// broken into steps, with what came of each, and facts to remember. Klash
// gives each project a persistent Gno machine named mem, whose variable mem
// is a Board, so that the agent's work outlives its session:
//
//	mem.Add("release v2; done when CI is green", "bump the version", "update CHANGELOG", "tag v2.0.0")
//	mem.Done(2, "now 2.0.0")
//	mem.Note(3, "generating it from git log was too noisy")
//	mem.Facts["deploy cmd"] = "make ship ENV=prod"
//	mem.Show()
//
// An item is a goal, a step of one, or a question: a question's notes are
// its findings, and its result is its answer. A condition is a step, since
// an item cannot be done while a step of it is open. Items are plain data:
// mem.Items[3].Title reads one, and other packages may build on them.
//
// This is version 1, and it only ever grows. A persistent machine is
// rebuilt by running its calls again against whatever version klash ships,
// so a change to what a call does, or a new refusal, belongs in version 2.
package gnomem

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// A Board is an agent's working memory.
type Board struct {
	// Items are the items by id, in the order added. Items[0] is the root,
	// the parent of the top-level items.
	Items []Item
	// Facts are things to remember, by name.
	Facts map[string]string
	// Changed is when an item last changed.
	Changed time.Time
}

// An Item is a goal, a step of one, or a question.
type Item struct {
	Title  string
	Parent int
	// Status is "" while the item is open, then "done" or "dropped".
	Status string
	// Result is what came of it: a summary, an answer, or why it was
	// dropped.
	Result string
	// Notes are findings and attempts, oldest first.
	Notes []string
}

const (
	maxTitle  = 200  // bytes kept of a title
	maxText   = 1000 // bytes kept of a result or a note
	maxShow   = 3000 // about the most Show writes
	maxClosed = 10   // closed top-level items Show lists
	maxFacts  = 20   // facts Show lists
	shownNote = 3    // notes Show gives the next item
)

// New returns an empty board.
func New() *Board {
	return &Board{Items: []Item{{Title: "(root)"}}, Facts: map[string]string{}}
}

// Add adds a top-level item, and its steps if any are given, and returns
// its id.
func (b *Board) Add(title string, steps ...string) int {
	id := b.add(0, title)
	for _, s := range steps {
		b.add(id, s)
	}
	return id
}

// Step adds a step to item parent, which must be open, and returns its id.
func (b *Board) Step(parent int, title string) int {
	b.open(parent)
	return b.add(parent, title)
}

func (b *Board) add(parent int, title string) int {
	title = clip(strings.TrimSpace(title), maxTitle)
	if title == "" {
		panic("an item needs a title")
	}
	if len(b.Items) == 0 {
		b.Items = []Item{{Title: "(root)"}}
	}
	b.Items = append(b.Items, Item{Title: title, Parent: parent})
	b.Changed = time.Now()
	return len(b.Items) - 1
}

// Done closes item id with what came of it, and says what is next. It
// refuses while a step of the item is open.
func (b *Board) Done(id int, result string) string {
	it := b.open(id)
	if open := b.openSteps(id); len(open) > 0 {
		panic("item " + b.ref(id) + " has open steps " + b.refs(open) + ": finish or drop them first")
	}
	it.Status = "done"
	it.Result = clip(strings.TrimSpace(result), maxText)
	b.Changed = time.Now()
	return "done " + b.ref(id) + b.whatNext()
}

// Drop closes item id, and its open steps, as not to be done, with why, and
// says what is next.
func (b *Board) Drop(id int, why string) string {
	it := b.open(id)
	b.dropSteps(id)
	it.Status = "dropped"
	it.Result = clip(strings.TrimSpace(why), maxText)
	b.Changed = time.Now()
	return "dropped " + b.ref(id) + b.whatNext()
}

func (b *Board) dropSteps(id int) {
	for i := 1; i < len(b.Items); i++ {
		if b.Items[i].Parent == id && b.Items[i].Status == "" {
			b.dropSteps(i)
			b.Items[i].Status = "dropped"
		}
	}
}

// Reopen opens a closed item again, with the items it belongs to. What came
// of each when it closed becomes a note.
func (b *Board) Reopen(id int) string {
	b.item(id)
	for i := id; i != 0; i = b.Items[i].Parent {
		it := &b.Items[i]
		if it.Status != "" {
			it.Notes = append(it.Notes, "was "+it.Status+": "+it.Result)
			it.Status, it.Result = "", ""
		}
	}
	b.Changed = time.Now()
	return "reopened " + b.ref(id)
}

// Note records a finding or an attempt on item id.
func (b *Board) Note(id int, text string) {
	it := b.item(id)
	text = clip(strings.TrimSpace(text), maxText)
	if text == "" {
		panic("a note needs text")
	}
	it.Notes = append(it.Notes, text)
	b.Changed = time.Now()
}

func (b *Board) item(id int) *Item {
	if id < 1 || id >= len(b.Items) {
		panic("there is no item " + strconv.Itoa(id) + b.openList())
	}
	return &b.Items[id]
}

func (b *Board) open(id int) *Item {
	it := b.item(id)
	if it.Status != "" {
		panic("item " + b.ref(id) + " is already " + it.Status + "; Reopen(" + strconv.Itoa(id) + ") first")
	}
	return it
}

func (b *Board) openSteps(id int) []int {
	var out []int
	for i := 1; i < len(b.Items); i++ {
		if b.Items[i].Parent == id && b.Items[i].Status == "" {
			out = append(out, i)
		}
	}
	return out
}

// children lists each item's steps, in the order added.
func (b *Board) children() map[int][]int {
	kids := map[int][]int{}
	for i := 1; i < len(b.Items); i++ {
		kids[b.Items[i].Parent] = append(kids[b.Items[i].Parent], i)
	}
	return kids
}

// next is the first open item with no open steps, in the order of the
// outline, or 0.
func (b *Board) next(kids map[int][]int) int {
	var walk func(id int) int
	walk = func(id int) int {
		for _, k := range kids[id] {
			if b.Items[k].Status != "" {
				continue
			}
			if n := walk(k); n != 0 {
				return n
			}
			return k
		}
		return 0
	}
	return walk(0)
}

func (b *Board) whatNext() string {
	if n := b.next(b.children()); n != 0 {
		return "; next: " + b.ref(n)
	}
	return "; nothing is open"
}

func (b *Board) openList() string {
	var open []int
	for i := 1; i < len(b.Items); i++ {
		if b.Items[i].Status == "" {
			open = append(open, i)
		}
	}
	if len(open) == 0 {
		return "; nothing is open"
	}
	if len(open) > 5 {
		return "; open: " + b.refs(open[:5]) + ", …"
	}
	return "; open: " + b.refs(open)
}

// ref names an item by id and title.
func (b *Board) ref(id int) string {
	return strconv.Itoa(id) + " " + strconv.Quote(clip(b.Items[id].Title, 60))
}

func (b *Board) refs(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = b.ref(id)
	}
	return strings.Join(parts, ", ")
}

// String is Show(), so that println(mem) and fmt.Println(mem) show the
// board.
func (b *Board) String() string { return b.Show() }

// Show returns the board as an outline, or, given ids, those items in full.
// The outline gives each open item and its steps; each closed item on one
// line (the newest few at the top level); the next item to work on, marked,
// with its latest notes; and the facts. It stays short: about 3000 bytes at
// most.
func (b *Board) Show(ids ...int) string {
	var sb strings.Builder
	if len(ids) > 0 {
		for _, id := range ids {
			b.showItem(&sb, id)
		}
		return sb.String()
	}
	kids := b.children()
	next := b.next(kids)
	open, done, dropped := 0, 0, 0
	for i := 1; i < len(b.Items); i++ {
		switch b.Items[i].Status {
		case "":
			open++
		case "done":
			done++
		default:
			dropped++
		}
	}
	if len(b.Items) <= 1 && len(b.Facts) == 0 {
		return "the board is empty: Add(title, steps...) starts a checklist, and Facts[name] = value keeps a fact\n"
	}
	sb.WriteString("board: " + strconv.Itoa(open) + " open, " + strconv.Itoa(done) + " done")
	if dropped > 0 {
		sb.WriteString(", " + strconv.Itoa(dropped) + " dropped")
	}
	if next != 0 {
		sb.WriteString(" · next: " + b.ref(next))
	}
	if !b.Changed.IsZero() {
		sb.WriteString(" · changed " + b.Changed.UTC().Format("2006-01-02 15:04") + " UTC")
	}
	sb.WriteString("\n")

	// The newest closed top-level items are listed; older ones counted.
	var closed []int
	for _, k := range kids[0] {
		if b.Items[k].Status != "" {
			closed = append(closed, k)
		}
	}
	hidden := map[int]bool{}
	if len(closed) > maxClosed {
		for _, k := range closed[:len(closed)-maxClosed] {
			hidden[k] = true
		}
		sb.WriteString("… " + strconv.Itoa(len(closed)-maxClosed) + " older closed items (Show(id) shows one)\n")
	}
	for _, k := range kids[0] {
		if !hidden[k] {
			b.outline(&sb, k, 0, next, kids)
		}
	}

	if len(b.Facts) > 0 {
		keys := make([]string, 0, len(b.Facts))
		for k := range b.Facts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteString("facts:\n")
		for i, k := range keys {
			if i == maxFacts {
				sb.WriteString("  … " + strconv.Itoa(len(keys)-maxFacts) + " more\n")
				break
			}
			sb.WriteString("  " + line(k) + " = " + line(b.Facts[k]) + "\n")
		}
	}
	return cut(sb.String())
}

func (b *Board) outline(sb *strings.Builder, id, depth, next int, kids map[int][]int) {
	it := b.Items[id]
	indent := strings.Repeat("  ", depth)
	sb.WriteString(indent + mark(it.Status) + " " + strconv.Itoa(id) + " " + line(it.Title))
	switch it.Status {
	case "done":
		if it.Result != "" {
			sb.WriteString(" → " + line(it.Result))
		}
	case "dropped":
		sb.WriteString(" · dropped")
		if it.Result != "" {
			sb.WriteString(": " + line(it.Result))
		}
	}
	var counts []string
	if n := len(it.Notes); n > 0 && (it.Status != "" || id != next) {
		counts = append(counts, plural(n, "note"))
	}
	if it.Status != "" && len(kids[id]) > 0 {
		counts = append(counts, plural(len(kids[id]), "step"))
	}
	if len(counts) > 0 {
		sb.WriteString(" (" + strings.Join(counts, ", ") + ")")
	}
	if id == next {
		sb.WriteString("  ← next")
	}
	sb.WriteString("\n")
	if id == next {
		notes := it.Notes
		if len(notes) > shownNote {
			sb.WriteString(indent + "    · … " + plural(len(notes)-shownNote, "earlier note") + "\n")
			notes = notes[len(notes)-shownNote:]
		}
		for _, n := range notes {
			sb.WriteString(indent + "    · " + line(n) + "\n")
		}
	}
	if it.Status != "" {
		return // a closed item's steps are in Show(id)
	}
	for _, k := range kids[id] {
		b.outline(sb, k, depth+1, next, kids)
	}
}

func (b *Board) showItem(sb *strings.Builder, id int) {
	if id < 1 || id >= len(b.Items) {
		sb.WriteString("there is no item " + strconv.Itoa(id) + "\n")
		return
	}
	it := b.Items[id]
	sb.WriteString(mark(it.Status) + " " + strconv.Itoa(id) + " " + line(it.Title))
	if it.Parent != 0 {
		sb.WriteString(" (a step of " + b.ref(it.Parent) + ")")
	}
	sb.WriteString("\n")
	if it.Status != "" {
		sb.WriteString("  " + it.Status + ": " + line(it.Result) + "\n")
	}
	for i, n := range it.Notes {
		sb.WriteString("  note " + strconv.Itoa(i+1) + ": " + line(n) + "\n")
	}
	for _, k := range b.children()[id] {
		sb.WriteString("  step " + mark(b.Items[k].Status) + " " + strconv.Itoa(k) + " " + line(b.Items[k].Title) + "\n")
	}
}

func mark(status string) string {
	switch status {
	case "done":
		return "[x]"
	case "dropped":
		return "[-]"
	}
	return "[ ]"
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

// line keeps text on one line of the outline.
func line(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " ⏎ ")
}

// clip cuts s to at most n bytes, on a character boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// cut ends an outline that grew past maxShow at a line, saying so.
func cut(s string) string {
	if len(s) <= maxShow {
		return s
	}
	i := strings.LastIndex(s[:maxShow], "\n")
	return s[:i+1] + "… (cut; Show(id) shows an item in full)\n"
}
