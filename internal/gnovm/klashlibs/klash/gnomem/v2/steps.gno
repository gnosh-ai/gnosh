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

// eachStep calls fn on each step of item name, in order, until fn returns
// true. It walks the items' leaves rather than looking each step up.
func (m *Mem) eachStep(name string, fn func(step string, s *Todo) bool) {
	prefix := key(name) + "/"
	m.items.Iterate(prefix, prefix+"\xff", func(k string, v any) bool {
		num := k[len(prefix):]
		if strings.IndexByte(num, '/') >= 0 {
			return false // under a step
		}
		return fn(name+"/"+strings.TrimLeft(num, "0"), v.(*Todo))
	})
}

// eachBelow calls fn on every item under name: in outline order, or, with
// bottomUp, each item after everything under it. fn may apply Ops to the
// items, which changes no key.
func (m *Mem) eachBelow(name string, bottomUp bool, fn func(n string, it *Todo)) {
	var ks []string
	var its []*Todo
	m.items.Iterate(key(name)+"/", key(name)+"/\xff", func(k string, v any) bool {
		ks = append(ks, k)
		its = append(its, v.(*Todo))
		return false
	})
	for i := range ks {
		j := i
		if bottomUp {
			j = len(ks) - 1 - i
		}
		fn(unkey(ks[j]), its[j])
	}
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

// goalOf is the goal name is in: its first segment.
func goalOf(name string) string {
	if i := strings.Index(name, "/"); i >= 0 {
		return name[:i]
	}
	return name
}
