// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// mem's journal is the record of every input that changed mem: what ran,
// who ran it, and the Ops it made. It is the source: the store and the index
// are built from it, and can be rebuilt from it. Its framing is a VM
// journal's (journal.go): a header line, then one line per entry, appended
// and synced before anything else is written. Its header's kind is "mem",
// and it carries mem's instance id, which a rewrite keeps.

const memJournalKind = "mem"

// memEntry is one input that changed mem.
type memEntry struct {
	Seq int64 `json:"seq"`
	// Time is when the input ran, in Unix seconds: the time its Ops stamp
	// items with. Offset is the user's UTC offset then, in seconds.
	Time   int64 `json:"time"`
	Offset int   `json:"offset"`
	// Req is the request's id, so a caller that lost the reply can ask
	// whether the input was kept.
	Req string `json:"req,omitempty"`
	// Who ran it: "agent" (a session's), "user" or "klash"; and the
	// session it came from, if any.
	Who     string `json:"who"`
	Session string `json:"session,omitempty"`
	// Code is the Gno the input ran, and Ops what it changed, in order.
	Code string  `json:"code"`
	Ops  []memOp `json:"ops"`
}

func (e memEntry) seqNo() int64 { return e.Seq }

// createMemJournal writes a new mem journal, for a new mem: a new
// generation and a new instance id.
func createMemJournal(path string, now time.Time) (*journal, error) {
	return writeJournalHeader(path, journalHeader{Generation: newGeneration(), Created: now.UTC(), Kind: memJournalKind, Instance: newGeneration()})
}

// readMemJournal reads a whole mem journal. A torn last line is reported as
// torn, for the caller to cut under mem's lock.
func readMemJournal(path string) (j *journal, entries []memEntry, torn bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, err
	}
	return parseJournalOf[memEntry](path, data, memJournalKind)
}

// The words erased text becomes in the journal, as in the store.
const (
	memRedacted  = "(redacted)"
	memForgotten = "(forgotten)"
)

// memErasure is what one input erases: the texts it redacts, and the items,
// facts and rules it forgets, as their opTarget and name ("fact cmd/test").
type memErasure struct {
	secrets   []string
	forgotten map[string]bool
}

// erasureOf is what ops erase; ok is false when they erase nothing.
func erasureOf(ops []memOp) (er memErasure, ok bool) {
	for _, op := range ops {
		switch op.Kind {
		case "redact":
			if !slices.Contains(er.secrets, op.Old) {
				er.secrets = append(er.secrets, op.Old)
			}
			ok = true
		case "forget":
			if er.forgotten == nil {
				er.forgotten = map[string]bool{}
			}
			er.forgotten[opTarget(op)+" "+op.Name] = true
			ok = true
		}
	}
	return er, ok
}

// forgets reports whether er forgets the item, fact or rule (target) name.
func (er memErasure) forgets(target, name string) bool {
	return er.forgotten[target+" "+name]
}

// opTarget is what an Op names: an "item", "fact" or "rule"; or
// "statements", for a redact of past inputs' code alone.
func opTarget(op memOp) string {
	switch {
	case op.Kind == "fact" || op.Kind == "rule":
		return op.Kind
	case op.Kind == "redact", op.Kind == "forget" && op.Field != "":
		return op.Field
	}
	return "item"
}

// holds reports whether s holds a secret, as typed or Go-quoted, as it sits
// in statements.
func (er memErasure) holds(s string) bool {
	for _, t := range er.secrets {
		if strings.Contains(s, t) || strings.Contains(s, quotedInner(t)) {
			return true
		}
	}
	return false
}

// nameHolds reports whether a name holds a secret, as typed or in a
// name's form.
func (er memErasure) nameHolds(name string) bool {
	for _, t := range er.secrets {
		if f := memNameForm(t); strings.Contains(name, t) || f != "" && strings.Contains(name, f) {
			return true
		}
	}
	return false
}

// scrub replaces each secret, as typed or Go-quoted, with "(redacted)".
func (er memErasure) scrub(s string) string {
	for _, t := range er.secrets {
		s = strings.ReplaceAll(s, t, memRedacted)
		if q := quotedInner(t); q != t {
			s = strings.ReplaceAll(s, q, memRedacted)
		}
	}
	return s
}

// erase rewrites mem's journal entries so that none holds what er erases:
//   - each secret, typed or Go-quoted, becomes "(redacted)" in every Op's
//     Old and New and in every input's statements; an entry whose Ops held
//     one has its statements replaced whole, since a secret may sit in them
//     escaped or split;
//   - every non-empty Old and New of an Op naming a forgotten item becomes
//     "(forgotten)", except a status set's, and redacts of it are dropped;
//     an entry with such an Op has its statements replaced whole;
//   - a goal that is forgotten, or whose name holds a secret, typed or in a
//     name's form, is renamed forgotten-<n> in every entry; a fact or rule
//     that is forgotten, or whose name holds a secret, loses every Op, its
//     forget among them, and the entries that had one their statements.
//
// Kinds, fields, status words and who made what are left as they are. It
// returns the entries, and what gnosh's own copies must lose too (see
// MemErasure): the goals renamed, old name to new, the texts erased, each
// with what took its place, and the names forgotten.
func erase(entries []memEntry, er memErasure) ([]memEntry, MemErasure) {
	renamed := map[string]string{}
	gone := MemErasure{Texts: map[string]string{}}
	for _, t := range er.secrets {
		gone.Texts[t] = memRedacted
	}
	forget := func(op memOp) {
		for _, t := range []string{op.Old, op.New} {
			if t != "" && gone.Texts[t] == "" {
				gone.Texts[t] = memForgotten
			}
		}
		if !slices.Contains(gone.Names, op.Name) {
			gone.Names = append(gone.Names, op.Name)
		}
	}
	next := 1
	for _, e := range entries {
		for _, op := range e.Ops {
			if g := goalPart(op.Name); opTarget(op) == "item" && strings.HasPrefix(g, "forgotten-") {
				if n, err := strconv.Atoi(strings.TrimPrefix(g, "forgotten-")); err == nil && n >= next {
					next = n + 1
				}
			}
		}
	}
	for _, e := range entries {
		for _, op := range e.Ops {
			g := goalPart(op.Name)
			if opTarget(op) != "item" || renamed[g] != "" || strings.HasPrefix(g, "forgotten-") {
				continue
			}
			if er.forgets("item", g) || er.nameHolds(g) {
				renamed[g] = "forgotten-" + strconv.Itoa(next)
				next++
			}
		}
	}
	out := make([]memEntry, 0, len(entries))
	for _, e := range entries {
		wipe := ""
		var ops []memOp
		for _, op := range e.Ops {
			switch target := opTarget(op); {
			case (target == "fact" || target == "rule") && er.nameHolds(op.Name):
				wipe = memRedacted
				continue
			case (target == "fact" || target == "rule") && er.forgets(target, op.Name):
				if op.Kind == target {
					forget(op)
				}
				if wipe == "" {
					wipe = memForgotten
				}
				continue
			case target == "item" && er.forgets("item", op.Name):
				if op.Kind == "redact" {
					wipe = memForgotten
					continue
				}
				if memTextOp(op) {
					forget(op)
				}
				if op.Kind != "set" || op.Field != "status" {
					if op.Old != "" {
						op.Old = memForgotten
					}
					if op.New != "" {
						op.New = memForgotten
					}
				}
				if wipe == "" {
					wipe = memForgotten
				}
			}
			if er.holds(op.Old) || er.holds(op.New) {
				op.Old, op.New = er.scrub(op.Old), er.scrub(op.New)
				wipe = memRedacted
			}
			if g := goalPart(op.Name); opTarget(op) == "item" && renamed[g] != "" {
				op.Name = renamed[g] + op.Name[len(g):]
			}
			ops = append(ops, op)
		}
		e.Ops = ops
		if wipe != "" {
			e.Code = wipe
		} else {
			e.Code = er.scrub(e.Code)
		}
		out = append(out, e)
	}
	gone.Renamed = renamed
	return out, gone
}

// memTextOp reports whether an Op on an item carries the item's text: its
// title, result, wait or a note, not a status, a session or "user".
func memTextOp(op memOp) bool {
	switch op.Kind {
	case "add", "unadd", "note", "unnote":
		return true
	case "set":
		return op.Field == "result" || op.Field == "wait"
	}
	return false
}

// rewriteMemJournal writes entries as mem's journal at path, under a new
// generation and the same instance, durably and at once: the commit point
// of an erasure. It returns the new journal.
func rewriteMemJournal(path, instance string, entries []memEntry, now time.Time) (*journal, error) {
	hdr := journalHeader{Version: journalVersion, Generation: newGeneration(), Created: now.UTC(), Gno: stdlibVersion(), Kind: memJournalKind, Instance: instance}
	line, err := json.Marshal(hdr)
	if err != nil {
		return nil, err
	}
	data := append(line, '\n')
	last := data
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		last = append(line, '\n')
		data = append(data, last...)
	}
	if err := writeFileSync(path, data); err != nil {
		return nil, err
	}
	j := &journal{path: path, generation: hdr.Generation, gno: hdr.Gno, instance: instance, size: int64(len(data)), lastHash: lineHash(last)}
	if len(entries) > 0 {
		j.seq = entries[len(entries)-1].Seq
	}
	return j, nil
}

// goalPart is the goal an item's name is in: its first segment.
func goalPart(name string) string {
	g, _, _ := strings.Cut(name, "/")
	return g
}

// quotedInner is s as it sits inside a Go-quoted string: strconv.Quote
// without the quotes.
func quotedInner(s string) string {
	q := strconv.Quote(s)
	return q[1 : len(q)-1]
}

// memNameForm is s in a name's form, as gnomem makes names (names.gno):
// lowercase words of letters and digits, accents folded, joined by "-".
func memNameForm(s string) string {
	const from, to = "àáâãäåçèéêëìíîïñòóôõöùúûüýÿ", "aaaaaaceeeeiiiinooooouuuuyy"
	var words []string
	var w strings.Builder
	flush := func() {
		if w.Len() > 0 {
			words = append(words, w.String())
			w.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		if i := strings.IndexRune(from, r); i >= 0 {
			r = []rune(to)[utf8.RuneCountInString(from[:i])]
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			w.WriteRune(r)
		case r == '\'' || r == '’':
		default:
			flush()
		}
	}
	flush()
	return strings.Join(words, "-")
}
