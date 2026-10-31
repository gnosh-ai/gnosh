// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnomem

import "strings"

// eachStep calls fn on each step of item name, in the order they were
// added, until fn returns true.
func (m *Mem) eachStep(name string, fn func(step string, s *Todo) bool) {
	it := m.get(name)
	if it == nil {
		return
	}
	for _, s := range it.Steps {
		step := name + "/" + s
		if fn(step, m.get(step)) {
			return
		}
	}
}

// eachBelow calls fn on every item under name: in outline order, or, with
// bottomUp, each item after everything under it. fn may apply Ops to the
// items, which changes no step's place.
func (m *Mem) eachBelow(name string, bottomUp bool, fn func(n string, it *Todo)) {
	var walk func(n string)
	walk = func(n string) {
		it := m.get(n)
		if it == nil {
			return
		}
		steps := make([]string, len(it.Steps))
		copy(steps, it.Steps)
		for _, s := range steps {
			step := n + "/" + s
			if !bottomUp {
				fn(step, m.get(step))
			}
			walk(step)
			if bottomUp {
				fn(step, m.get(step))
			}
		}
	}
	walk(name)
}

// openSteps lists item name's open steps.
func (m *Mem) openSteps(name string) []string {
	var open []string
	m.eachStep(name, func(step string, s *Todo) bool {
		if s.Status == "" {
			open = append(open, step)
		}
		return false
	})
	return open
}

// goalOf is the goal name is in: its first segment (a thought's address
// aside).
func goalOf(name string) string {
	name, _ = splitAddr(name)
	if i := strings.Index(name, "/"); i >= 0 {
		return name[:i]
	}
	return name
}

// stepName is the name of step id: its last segment.
func stepName(id string) string {
	return id[strings.LastIndex(id, "/")+1:]
}
