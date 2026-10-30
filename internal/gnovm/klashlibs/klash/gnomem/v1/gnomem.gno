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
//	mem.Add("release v2", "bump the version", "tag v2.0.0", "CI is green")
//	mem.Done(2, "now 2.0.0")
//	mem.Note(3, "the tag needs the release key")
//	mem.Facts["deploy cmd"] = "make ship ENV=prod"
//	mem.Show()
//
// An item is a goal, a step of one, or a question: a question's notes are
// its findings, and its result is its answer. A condition is a step: an item
// cannot be done while a step of it is open. Each change prints a line
// saying what it did, with the ids it made and what is next.
//
// A Board is plain data. Items[0] is the root, so ids are indexes and loops
// run over Items[1:]. An item's Status is "" while it is open; anything else
// closes it ("done" and "dropped" are this package's). Ids are never reused
// or reordered. Other packages may build on these types, or replace them.
//
// This is version 1. A persistent machine is rebuilt by running its calls
// again against the version klash ships, so a call here never changes what it
// stores or the ids it returns, and never refuses what it once accepted; it
// may store new fields, refuse less, and print and show things differently.
// Anything else is version 2.
package gnomem

import (
	"sort"
	"strconv"
	"strings"
)

// A Board is an agent's working memory.
type Board struct {
	// Items are the items by id, in the order added. Items[0] is the root,
	// the parent of the top-level items.
	Items []Item
	// Facts are things to remember, by name.
	Facts map[string]string
}

// An Item is a goal, a step of one, or a question.
type Item struct {
	Title  string
	Parent int
	// Status is "" while the item is open; "done", "dropped" or another
	// word closes it.
	Status string
	// Result is what came of it: a summary, an answer, or why it was
	// dropped. Steps dropped with their parent keep none.
	Result string
	// Notes are findings and attempts, oldest first.
	Notes []string
}

const (
	maxTitle  = 200  // bytes kept of a title
	maxText   = 1000 // bytes kept of a result or a note
	maxShow   = 3000 // about the most Show writes
	maxClosed = 10   // closed top-level items Show lists
	maxClosedSteps = 5 // closed steps Show lists under an open item
	maxFacts  = 20   // facts Show lists
	shownNote = 3    // notes Show gives the next item
)

// New returns an empty board.
func New() *Board {
	return &Board{Items: []Item{{Title: "(root)"}}, Facts: map[string]string{}}
}

// Add adds a top-level item, and its steps if any are given, and returns
// its id; the steps take the ids after it.
func (b *Board) Add(title string, steps ...string) int {
	id := b.add(0, title)
	for _, s := range steps {
		b.add(id, s)
	}
	receipt := "added " + b.ref(id)
	if len(steps) > 0 {
		var ids []int
		for i := range steps {
			ids = append(ids, id+1+i)
		}
		receipt += ": steps " + b.refs(ids)
	}
	println(receipt)
	return id
}

// Step adds a step to item parent, which must be open, and returns its id.
func (b *Board) Step(parent int, title string) int {
	b.open(parent)
	id := b.add(parent, title)
	println("added " + b.ref(id) + ", a step of " + b.ref(parent))
	return id
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
	return len(b.Items) - 1
}

// Done closes item id with what came of it. It refuses while a step of the
// item is open.
func (b *Board) Done(id int, result string) {
	it := b.open(id)
	if open := b.openSteps(id); len(open) > 0 {
		panic("item " + b.ref(id) + " has open steps " + b.refs(open) + ": finish or drop them first")
	}
	it.Status = "done"
	it.Result = clip(strings.TrimSpace(result), maxText)
	println("done " + b.ref(id) + b.whatNext())
}

// Drop closes item id as not to be done, with why. Its open steps are
// dropped with it, keeping no reason of their own, and come back if it is
// reopened.
func (b *Board) Drop(id int, why string) {
	it := b.open(id)
	why = clip(strings.TrimSpace(why), maxText)
	if why == "" {
		panic("say why: Drop(" + strconv.Itoa(id) + ", \"why\")")
	}
	b.dropSteps(id)
	it.Status = "dropped"
	it.Result = why
	println("dropped " + b.ref(id) + b.whatNext())
}

func (b *Board) dropSteps(id int) {
	for i := 1; i < len(b.Items); i++ {
		if b.Items[i].Parent == id && b.Items[i].Status == "" {
			b.dropSteps(i)
			b.Items[i].Status = "dropped"
		}
	}
}

// Reopen opens a closed item again, with the items it belongs to, and the
// steps dropped along with any of them. What came of each when it closed
// becomes a note.
func (b *Board) Reopen(id int) {
	b.item(id)
	for i := id; i != 0; i = b.Items[i].Parent {
		b.reopen(i)
	}
	println("reopened " + b.ref(id) + b.whatNext())
}

func (b *Board) reopen(i int) {
	it := &b.Items[i]
	if it.Status == "" {
		return
	}
	dropped := it.Status == "dropped"
	if it.Result != "" {
		it.Notes = append(it.Notes, "was "+it.Status+": "+it.Result)
	}
	it.Status, it.Result = "", ""
	if !dropped {
		return
	}
	for k := 1; k < len(b.Items); k++ {
		if s := b.Items[k]; s.Parent == i && s.Status == "dropped" && s.Result == "" {
			b.reopen(k)
		}
	}
}

// Note records a finding or an attempt on item id, open or closed.
func (b *Board) Note(id int, text string) {
	it := b.item(id)
	text = clip(strings.TrimSpace(text), maxText)
	if text == "" {
		panic("a note needs text")
	}
	it.Notes = append(it.Notes, text)
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
// The outline starts with a line of counts and the next item to work on,
// then the facts, then each open item with its steps and each closed item
// on one line (the newest few at the top level); the next item is marked,
// with its latest notes. It stays short: about 3000 bytes at most.
func (b *Board) Show(ids ...int) string {
	var sb strings.Builder
	if len(ids) > 0 {
		for _, id := range ids {
			b.showItem(&sb, id)
		}
		return cut(sb.String())
	}
	if len(b.Items) <= 1 && len(b.Facts) == 0 {
		return "the board is empty: Add(title, steps...) starts a checklist, and Facts[name] = value keeps a fact\n"
	}
	kids := b.children()
	next := b.next(kids)
	counts := map[string]int{}
	var words []string
	for i := 1; i < len(b.Items); i++ {
		w := b.Items[i].Status
		if w == "" {
			w = "open"
		}
		if counts[w] == 0 && w != "open" && w != "done" && w != "dropped" {
			words = append(words, w)
		}
		counts[w]++
	}
	sb.WriteString("board: " + strconv.Itoa(counts["open"]) + " open, " + strconv.Itoa(counts["done"]) + " done")
	for _, w := range append([]string{"dropped"}, words...) {
		if counts[w] > 0 {
			sb.WriteString(", " + strconv.Itoa(counts[w]) + " " + w)
		}
	}
	if next != 0 {
		sb.WriteString(" · next: " + b.ref(next))
	}
	sb.WriteString("\n")

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
	return cut(sb.String())
}

func (b *Board) outline(sb *strings.Builder, id, depth, next int, kids map[int][]int) {
	it := b.Items[id]
	indent := strings.Repeat("  ", depth)
	sb.WriteString(indent + mark(it.Status) + " " + strconv.Itoa(id) + " " + line(it.Title))
	switch it.Status {
	case "":
	case "done":
		if it.Result != "" {
			sb.WriteString(" → " + line(it.Result))
		}
	default:
		sb.WriteString(" · " + it.Status)
		if it.Result != "" {
			sb.WriteString(": " + line(it.Result))
		}
	}
	var counts []string
	if n := len(it.Notes); n > 0 && id != next {
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
	var closed []int
	for _, k := range kids[id] {
		if b.Items[k].Status != "" {
			closed = append(closed, k)
		}
	}
	hidden := map[int]bool{}
	if len(closed) > maxClosedSteps {
		for _, k := range closed[:len(closed)-maxClosedSteps] {
			hidden[k] = true
		}
		sb.WriteString(indent + "  … " + plural(len(closed)-maxClosedSteps, "earlier closed step") + "\n")
	}
	for _, k := range kids[id] {
		if !hidden[k] {
			b.outline(sb, k, depth+1, next, kids)
		}
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
	switch {
	case it.Status != "" && it.Result != "":
		sb.WriteString("  " + it.Status + ": " + line(it.Result) + "\n")
	case it.Status != "":
		sb.WriteString("  " + it.Status + "\n")
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
	case "":
		return "[ ]"
	case "done":
		return "[x]"
	}
	return "[-]"
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
