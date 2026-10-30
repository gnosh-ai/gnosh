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
)

// Check walks the whole Mem and says what disagrees with what apply keeps:
// each item's step counts, the indexes and the counts; "" when nothing
// does. It stops at ten problems. For klash, after a rebuild, and for tests:
// it reads every item.
func (m *Mem) Check() string {
	var problems []string
	bad := func(s string) { problems = append(problems, s) }
	var c Counts
	open, closed, asks, days := 0, 0, 0, 0
	m.items.Iterate("", "", func(k string, v any) bool {
		name, it := unkey(k), v.(*Todo)
		stepsOpen, stepsDone := 0, 0
		if it.Last > 0 {
			m.eachStep(name, func(_ string, s *Todo) bool {
				switch s.Status {
				case "":
					stepsOpen++
				case "done":
					stepsDone++
				}
				return false
			})
		}
		if stepsOpen != it.OpenSteps || stepsDone != it.DoneSteps {
			bad("`" + name + "` counts " + strconv.Itoa(it.OpenSteps) + " open and " + strconv.Itoa(it.DoneSteps) + " done steps, but has " + strconv.Itoa(stepsOpen) + " and " + strconv.Itoa(stepsDone))
		}
		top := parentOf(name) == ""
		switch it.Status {
		case "":
			c.Open++
			if top {
				open++
				if m.open.Get(stampKey(it.Changed, name)) == nil {
					bad("open `" + name + "` is not in the open goals")
				}
			}
			if it.For != "" {
				c.ForUser++
				asks++
				if m.asks.Get(name) == nil {
					bad("`" + name + "` is for the user but not in the questions")
				}
			}
			if it.Wait != "" {
				c.Waits++
				if isDay(it.Wait) {
					days++
					if m.days.Get(it.Wait+"|"+name) == nil {
						bad("`" + name + "` waits until " + it.Wait + " but is not in the days")
					}
				}
			}
		case "done", "dropped":
			c.countStatus(it.Status, 1)
			if top {
				closed++
				if m.closed.Get(stampKey(it.Changed, name)) == nil {
					bad("closed `" + name + "` is not in the closed goals")
				}
			}
		case "forgotten":
			c.Forgotten++
		default:
			bad("`" + name + "` has status " + strconv.Quote(it.Status))
		}
		if top && m.taken.Get(name) == nil {
			bad("goal `" + name + "` is not among the names taken")
		}
		return len(problems) >= 10
	})
	if c != m.counts {
		bad("the counts are " + countsText(m.counts) + ", but the items make " + countsText(c))
	}
	for _, ix := range []struct {
		what       string
		size, want int
	}{
		{"open goals", m.open.Size(), open},
		{"closed goals", m.closed.Size(), closed},
		{"questions", m.asks.Size(), asks},
		{"days", m.days.Size(), days},
		{"facts by time", m.recent.Size(), m.facts.Size()},
	} {
		if ix.size != ix.want {
			bad("the " + ix.what + " index has " + strconv.Itoa(ix.size) + " entries, not " + strconv.Itoa(ix.want))
		}
	}
	return strings.Join(problems, "\n")
}

func countsText(c Counts) string {
	return "open " + strconv.Itoa(c.Open) + ", done " + strconv.Itoa(c.Done) + ", dropped " + strconv.Itoa(c.Dropped) +
		", forgotten " + strconv.Itoa(c.Forgotten) + ", waits " + strconv.Itoa(c.Waits) + ", for the user " + strconv.Itoa(c.ForUser)
}
