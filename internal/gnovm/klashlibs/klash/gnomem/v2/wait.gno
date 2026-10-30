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

// A wait is a day or words. A day is written 2026-12-01 and nothing else, so
// that "until Nov 3" never becomes words that wait for ever; anything that
// looks like a day written another way is refused, naming today.

// parseWait checks until as a wait, and returns it as kept.
func (m *Mem) parseWait(until string) string {
	if until == "" {
		return ""
	}
	if isDay(until) {
		if !onCalendar(until) {
			panic(strconv.Quote(until) + " is not on the calendar (" + todayIs() + ")")
		}
		if until < yesterday() {
			panic(until + " is past (" + todayIs() + "): give a day still to come, or Done the item")
		}
		return until
	}
	if looksLikeDay(until) {
		panic("write a day as " + suggestDay(until) + " (" + todayIs() + ")")
	}
	if validName(until) && m.get(until) != nil {
		panic("a wait is a day or words, not an item: to wait for `" + until + "`, make this work a step of it, or say it in words")
	}
	return until
}

// isDay reports whether s is shaped as a day: 2026-12-01.
func isDay(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, r := range s {
		if i != 4 && i != 7 && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// onCalendar reports whether day s exists.
func onCalendar(s string) bool {
	y, _ := strconv.Atoi(s[:4])
	mo, _ := strconv.Atoi(s[5:7])
	d, _ := strconv.Atoi(s[8:])
	if mo < 1 || mo > 12 || d < 1 {
		return false
	}
	t := time.Date(y, time.Month(mo), d, 12, 0, 0, 0, time.UTC)
	return t.Day() == d
}

var (
	months   = []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}
	weekdays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday", "mon", "tue", "tues", "wed", "thu", "thur", "thurs", "fri", "sat", "sun"}
	relative = []string{"today", "tonight", "tomorrow", "yesterday", "next", "this", "week", "weekend", "month", "year", "day", "days", "weeks", "months", "years", "hours", "minutes", "noon", "midnight", "morning", "evening", "eod", "eow"}
)

// looksLikeDay reports whether s, after one leading until, after, on or by,
// is a day written some other way: a month or weekday name with a number or
// alone, a numeric d/m or m/d, an ISO day among other words, a relative day
// ("tomorrow", "next week", "in 2 days", "+3d").
func looksLikeDay(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"until ", "after ", "on ", "by "} {
		if strings.HasPrefix(low, p) {
			low = strings.TrimSpace(low[len(p):])
			break
		}
	}
	if isDay(low) {
		return true
	}
	ws := strings.Fields(strings.NewReplacer(",", " ", ".", " ").Replace(low))
	if len(ws) == 0 {
		return false
	}
	day := 0
	for i, w := range ws {
		switch {
		case isDay(w):
			return true
		case strings.Count(w, "/") >= 1 && digitsAnd(w, "/"),
			strings.Count(w, "-") >= 1 && digitsAnd(w, "-"):
			return true
		case w[0] == '+' && len(w) > 1 && digitsAnd(strings.TrimRight(w[1:], "dhwm"), ""):
			return true
		case w == "in" && i+2 < len(ws) && isNumber(ws[i+1]) && isRelative(ws[i+2]):
			return true
		case isMonth(w) || isWeekday(w) || isRelative(w):
			day++
		}
	}
	// A short phrase made of day words ("next monday", "nov 3", "tomorrow").
	return day > 0 && len(ws) <= 3 && day+countNumbers(ws) == len(ws)
}

func isMonth(w string) bool {
	for _, mo := range months {
		if strings.HasPrefix(w, mo) && len(w) <= 9 {
			return true
		}
	}
	return false
}

func isWeekday(w string) bool {
	for _, d := range weekdays {
		if w == d {
			return true
		}
	}
	return false
}

func isRelative(w string) bool {
	for _, r := range relative {
		if w == r {
			return true
		}
	}
	return false
}

func countNumbers(ws []string) int {
	n := 0
	for _, w := range ws {
		if isNumber(strings.TrimRight(w, "stndrh")) {
			n++
		}
	}
	return n
}

// digitsAnd reports whether s is digits and the characters in sep only.
func digitsAnd(s, sep string) bool {
	if s == "" {
		return false
	}
	digit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case strings.ContainsRune(sep, r):
		default:
			return false
		}
	}
	return digit
}

// todayIs is "today is Thu 2026-10-29".
func todayIs() string {
	d := today()
	y, _ := strconv.Atoi(d[:4])
	mo, _ := strconv.Atoi(d[5:7])
	day, _ := strconv.Atoi(d[8:])
	wd := time.Date(y, time.Month(mo), day, 12, 0, 0, 0, time.UTC).Weekday().String()
	return "today is " + wd[:3] + " " + d
}

// yesterday is the day before today.
func yesterday() string {
	return shiftDay(today(), -1)
}

// suggestDay is the day s most likely means, written as a day, for a
// refusal to offer: "Dec 1" is the next Dec 1, "tomorrow" is tomorrow, "in
// 3 days" is three days on, "next Monday" is the next Monday, "11/03" is
// Nov 3. Anything else gets a week from today, as an example.
func suggestDay(s string) string {
	t := today()
	low := strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"until ", "after ", "on ", "by "} {
		if strings.HasPrefix(low, p) {
			low = strings.TrimSpace(low[len(p):])
			break
		}
	}
	if isDay(low) {
		return low
	}
	ws := strings.Fields(strings.NewReplacer(",", " ", ".", " ").Replace(low))
	month, dayNum := 0, 0
	for i, w := range ws {
		for mi, mo := range months {
			if strings.HasPrefix(w, mo) {
				month = mi + 1
			}
		}
		if n, err := strconv.Atoi(strings.TrimRight(w, "stndrh")); err == nil && n >= 1 && n <= 31 {
			dayNum = n
		}
		switch {
		case w == "today":
			return t
		case w == "tomorrow":
			return shiftDay(t, 1)
		case w == "in" && i+2 < len(ws):
			if n, err := strconv.Atoi(ws[i+1]); err == nil {
				switch ws[i+2] {
				case "day", "days":
					return shiftDay(t, n)
				case "week", "weeks":
					return shiftDay(t, 7*n)
				}
			}
		case w[0] == '+' && len(w) > 2:
			if n, err := strconv.Atoi(w[1 : len(w)-1]); err == nil {
				switch w[len(w)-1] {
				case 'd':
					return shiftDay(t, n)
				case 'w':
					return shiftDay(t, 7*n)
				}
			}
		case strings.Count(w, "/") == 1 && digitsAnd(w, "/"):
			parts := strings.Split(w, "/")
			mo, _ := strconv.Atoi(parts[0])
			d, _ := strconv.Atoi(parts[1])
			if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
				return nextDate(t, mo, d)
			}
		}
		for wi, wd := range weekdays[:7] {
			if w == wd || (len(w) >= 3 && strings.HasPrefix(wd, w)) {
				return nextWeekday(t, wi)
			}
		}
	}
	if month > 0 && dayNum > 0 {
		return nextDate(t, month, dayNum)
	}
	return shiftDay(t, 7)
}

// nextDate is the next month/day on or after day t.
func nextDate(t string, month, d int) string {
	y, _ := strconv.Atoi(t[:4])
	for _, yy := range []int{y, y + 1} {
		s := time.Date(yy, time.Month(month), d, 12, 0, 0, 0, time.UTC).Format("2006-01-02")
		if s >= t {
			return s
		}
	}
	return t
}

// nextWeekday is the next weekday wi (0 is Monday) after day t.
func nextWeekday(t string, wi int) string {
	for i := 1; i <= 7; i++ {
		d := shiftDay(t, i)
		y, _ := strconv.Atoi(d[:4])
		mo, _ := strconv.Atoi(d[5:7])
		dd, _ := strconv.Atoi(d[8:])
		// Go's Weekday has Sunday as 0.
		if (int(time.Date(y, time.Month(mo), dd, 12, 0, 0, 0, time.UTC).Weekday())+6)%7 == wi {
			return d
		}
	}
	return t
}

func shiftDay(d string, days int) string {
	y, _ := strconv.Atoi(d[:4])
	mo, _ := strconv.Atoi(d[5:7])
	day, _ := strconv.Atoi(d[8:])
	return time.Date(y, time.Month(mo), day+days, 12, 0, 0, 0, time.UTC).Format("2006-01-02")
}

// waitText is how a receipt says what an item waits for.
func waitText(w string) string {
	switch {
	case w == "":
		return " no longer waits"
	case isDay(w):
		return " waits until " + w
	}
	return " waits for: " + w
}
