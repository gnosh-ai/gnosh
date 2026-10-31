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

// Check walks the whole Mem and says what disagrees with what apply keeps:
// each item's step counts, the indexes and the counts; "" when nothing
// does. It stops at ten problems. For gnosh, after a rebuild, and for tests:
// it reads every item.
func (m *Mem) Check() string {
	var problems []string
	bad := func(s string) { problems = append(problems, s) }
	var c Counts
	open, closed, asks, days, words := 0, 0, 0, 0, 0
	m.items.Iterate("", "", func(name string, v any) bool {
		it := v.(*Todo)
		stepsOpen, stepsDone := 0, 0
		m.eachStep(name, func(step string, s *Todo) bool {
			if s == nil {
				bad("`" + name + "` lists the step `" + step + "`, which is not there")
				return true
			}
			switch s.Status {
			case "":
				stepsOpen++
			case "done":
				stepsDone++
			}
			return false
		})
		if p := parentOf(name); p != "" {
			parent := m.get(p)
			listed := false
			if parent != nil {
				for _, s := range parent.Steps {
					listed = listed || s == stepName(name)
				}
			}
			if !listed {
				bad("`" + name + "` is not among `" + p + "`'s steps")
			}
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
					bad("open `" + name + "` is not in the open todos")
				}
				if m.titles.Get(sameTitle(it.Title)+"|"+name) == nil {
					bad("open `" + name + "` is not in the titles")
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
				} else {
					words++
					if m.words.Get(name) == nil {
						bad("`" + name + "` waits for words but is not in the words")
					}
				}
			}
		case "done", "dropped":
			c.countStatus(it.Status, 1)
			if top {
				closed++
				if m.closed.Get(stampKey(it.Changed, name)) == nil {
					bad("closed `" + name + "` is not in the closed todos")
				}
			}
		case "forgotten":
			c.Forgotten++
		default:
			bad("`" + name + "` has status " + strconv.Quote(it.Status))
		}
		if top && m.taken.Get(name) == nil {
			bad("todo `" + name + "` is not among the names taken")
		}
		return len(problems) >= 10
	})
	if c != m.counts {
		bad("the counts are " + countsText(m.counts) + ", but the items make " + countsText(c))
	}
	groups := map[string]int{}
	m.facts.Iterate("", "", func(n string, _ any) bool {
		groups[factGroup(n)]++
		return false
	})
	m.groups.Iterate("", "", func(g string, v any) bool {
		if v.(int) != groups[g] {
			bad("the group `" + g + "` counts " + strconv.Itoa(v.(int)) + " facts, but has " + strconv.Itoa(groups[g]))
		}
		return len(problems) >= 10
	})
	for _, ix := range []struct {
		what       string
		size, want int
	}{
		{"open todos", m.open.Size(), open},
		{"closed todos", m.closed.Size(), closed},
		{"questions", m.asks.Size(), asks},
		{"days", m.days.Size(), days},
		{"words", m.words.Size(), words},
		{"titles", m.titles.Size(), open},
		{"fact groups", m.groups.Size(), len(groups)},
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
