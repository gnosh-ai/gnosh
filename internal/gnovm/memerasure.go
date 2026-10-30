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
	"sort"
	"strings"
)

// MemErasure is what a Redact or a Forget took out of mem, for gnosh to
// take out of its own copies of mem's text too (MEMPLAN §5): the <mem>
// block saved with a session's first prompt, a session's /goal binding, its
// task list, its summaries. It holds the very text erased, so it is never
// logged and never written anywhere.
type MemErasure struct {
	// Texts are the texts erased, each with what took its place: a secret,
	// "(redacted)"; a forgotten item's, fact's or rule's text,
	// "(forgotten)".
	Texts map[string]string `json:"texts,omitempty"`
	// Names are the items, facts and rules forgotten, by the names they
	// had; Renamed are the goals renamed, old name to new: a goal forgotten,
	// or whose name held a secret, is forgotten-<n> now.
	Names   []string          `json:"names,omitempty"`
	Renamed map[string]string `json:"renamed,omitempty"`
}

// memMinErased is the shortest text Scrub replaces wherever it is: a
// shorter one, a step titled "a", would mangle text that never held it.
// (Redact refuses a secret under 6 bytes, for the same reason.)
const memMinErased = 6

// Scrub takes what e erased out of s, a copy of mem's text: a line that
// names something forgotten, in backticks as mem prints names, goes; a goal
// renamed for a secret in its name is named anew; and each erased text, as
// typed or Go-quoted, gives way to what took its place.
func (e MemErasure) Scrub(s string) string {
	if len(e.Names) > 0 && strings.Contains(s, "`") {
		lines := strings.Split(s, "\n")
		kept := lines[:0]
		for _, line := range lines {
			if !e.namesForgotten(line) {
				kept = append(kept, line)
			}
		}
		s = strings.Join(kept, "\n")
	}
	for old, new := range e.Renamed {
		s = strings.ReplaceAll(s, "`"+old+"`", "`"+new+"`")
		s = strings.ReplaceAll(s, "`"+old+"/", "`"+new+"/")
	}
	texts := make([]string, 0, len(e.Texts))
	for t := range e.Texts {
		if len(t) >= memMinErased {
			texts = append(texts, t)
		}
	}
	// The longest first, so a text that holds another goes whole.
	sort.Slice(texts, func(i, j int) bool { return len(texts[i]) > len(texts[j]) })
	for _, t := range texts {
		s = strings.ReplaceAll(s, t, e.Texts[t])
		if q := quotedInner(t); q != t {
			s = strings.ReplaceAll(s, q, e.Texts[t])
		}
	}
	return s
}

// namesForgotten reports whether line names, in backticks, something e
// forgot, or a step under it.
func (e MemErasure) namesForgotten(line string) bool {
	for _, n := range e.Names {
		if strings.Contains(line, "`"+n+"`") || strings.Contains(line, "`"+n+"/") {
			return true
		}
	}
	return false
}

// Secrets are the texts e redacted, which a session's visible messages may
// still hold: Scrub leaves those alone.
func (e MemErasure) Secrets() []string {
	var out []string
	for t, with := range e.Texts {
		if with == memRedacted && len(t) >= memMinErased {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}
