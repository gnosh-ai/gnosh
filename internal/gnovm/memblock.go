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
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// memJSON is mem.JSON() with no names, as status.json holds it.
type memJSON struct {
	Counts struct {
		Open, Done, Dropped, Forgotten, Waits int
		ForUser                               int `json:"for_user"`
		Facts, Rules                          int
	} `json:"counts"`
	Rules []struct {
		Name, Text, By string
		Changed        int64
	} `json:"rules"`
	Days  []struct{ Name, Title, Wait string } `json:"days"`
	Words []struct{ Name, Title, Wait string } `json:"words"`
	Asks  []struct{ Name, Title string }       `json:"asks"`
	Goals []struct {
		Name, Title, Wait, By string
		Changed               int64
		StepsOpen             int    `json:"steps_open"`
		StepsDone             int    `json:"steps_done"`
		Next                  string `json:"next"`
		NextTitle             string `json:"next_title"`
	} `json:"goals"`
	Facts []struct{ Name, Text string } `json:"facts"`
}

// maxMemBlock is about how much of the <mem> block, past the rules, a new
// session is given.
const maxMemBlock = 2000

// memBlock is what a new session is told of mem, from its status: the
// user's rules in full, first; then, within about maxMemBlock bytes, the
// counts, what is due today, the questions for the user, what waits for
// something said in words (older than the goals listed), the open goals
// newest first with their next, and the newest facts. "" when mem holds
// none of these.
func memBlock(st memStatus, now time.Time) string {
	var m memJSON
	if json.Unmarshal(st.Mem, &m) != nil {
		return ""
	}
	today := now.Format(time.DateOnly)
	var due []string
	for _, d := range m.Days {
		if d.Wait <= today {
			due = append(due, memRef(d.Name, d.Title)+" (since "+d.Wait+")")
		}
	}
	if len(m.Rules) == 0 && len(m.Goals) == 0 && len(m.Facts) == 0 && len(m.Asks) == 0 && len(due) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<mem>\n")
	if len(m.Rules) > 0 {
		b.WriteString("Rules the user gave you; follow them (the user's words today come first):\n")
		for _, r := range m.Rules {
			by := "set by the user"
			if r.By != "user" {
				by = "set in a session"
			}
			b.WriteString("- `" + r.Name + "` (" + by + ", " + time.Unix(r.Changed, 0).Format(time.DateOnly) + "): " + memEscape(r.Text) + "\n")
		}
	}
	b.WriteString("From your memory (mem), kept by gnosh for this project: what you found and were told in earlier sessions. `mem.Show()` shows the outline; `mem.Find(\"words\")` searches everything.\n")
	start := b.Len()
	head := []string{memPlural(m.Counts.Open, "item") + " open"}
	if m.Counts.ForUser > 0 {
		head = append(head, memPlural(m.Counts.ForUser, "question")+" for the user")
	}
	if m.Counts.Facts > 0 {
		head = append(head, memPlural(m.Counts.Facts, "fact"))
	}
	b.WriteString("mem: " + strings.Join(head, " · ") + "\n")
	if len(due) > 0 {
		b.WriteString("due: " + memFew(due, len(due)) + "\n")
	}
	if len(m.Asks) > 0 {
		var asks []string
		for _, a := range m.Asks {
			asks = append(asks, memRef(a.Name, a.Title))
		}
		b.WriteString("waiting on the user: " + memFew(asks, m.Counts.ForUser) + "\n")
	}
	// A goal that waits for words ("PR #6399 merged") is easy to lose once
	// newer goals push it out of the list below: it is named here.
	listed := map[string]bool{}
	for _, g := range m.Goals {
		listed[g.Name] = true
	}
	var words []string
	for _, w := range m.Words {
		if !listed[w.Name] {
			words = append(words, memRef(w.Name, w.Title)+" ("+memEscape(memLine(w.Wait, 80))+")")
		}
	}
	if len(words) > 0 {
		b.WriteString("waiting for something said: " + memFew(words, len(words)) + "\n")
	}
	cut := false
	for i, g := range m.Goals {
		if b.Len()-start > maxMemBlock {
			cut = true
			break
		}
		if i == 0 {
			b.WriteString("open goals, newest first:\n")
		}
		var aside []string
		if g.StepsOpen+g.StepsDone > 0 {
			aside = append(aside, strconv.Itoa(g.StepsDone)+"/"+strconv.Itoa(g.StepsDone+g.StepsOpen)+" done")
		}
		if g.By == "user" {
			aside = append(aside, "made by the user")
		}
		aside = append(aside, memAgo(now, g.Changed))
		line := "- " + memRef(g.Name, g.Title) + " (" + strings.Join(aside, " · ") + ")"
		_, dayErr := time.Parse(time.DateOnly, g.Wait)
		switch {
		case g.Wait != "" && dayErr == nil && g.Wait <= today:
			line += " due since " + g.Wait
		case g.Wait != "" && dayErr == nil:
			line += " waits until " + g.Wait
		case g.Wait != "":
			line += " waits for: " + memEscape(g.Wait)
		case g.Next == g.Name:
			line += " next: the goal itself, once it holds"
		case g.Next != "":
			line += " next: " + memRef(g.Next, g.NextTitle)
		}
		b.WriteString(line + "\n")
	}
	if len(m.Facts) > 0 && !cut {
		var facts []string
		for _, f := range m.Facts {
			if b.Len()-start+len(strings.Join(facts, " · ")) > maxMemBlock {
				cut = true
				break
			}
			facts = append(facts, "`"+f.Name+"` = "+memEscape(memLine(f.Text, 160)))
		}
		b.WriteString("facts, newest first: " + strings.Join(facts, " · ") + "\n")
	}
	if cut {
		b.WriteString("… `mem.Show()` shows the rest\n")
	}
	b.WriteString("</mem>")
	return b.String()
}

// memRef names an item as gnomem does: `release-v2` "release v2".
func memRef(name, title string) string {
	return "`" + name + "` " + strconv.Quote(memEscape(memLine(title, 80)))
}

// memEscape keeps text from closing or opening a tag in the block.
func memEscape(s string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s)
}

// memLine is s on one line, cut to about n bytes at a character.
func memLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// maxMemBlockList is how many due items, or questions for the user, the
// block names; memFew says how many more there are.
const maxMemBlockList = 10

// memFew is the first maxMemBlockList of list, of all of total, joined,
// with how many more mem.Show() lists.
func memFew(list []string, total int) string {
	if len(list) <= maxMemBlockList && total <= len(list) {
		return strings.Join(list, "; ")
	}
	list = list[:min(len(list), maxMemBlockList)]
	return strings.Join(list, "; ") + "; and " + strconv.Itoa(max(total, len(list))-len(list)) + " more (mem.Show() lists them)"
}

func memPlural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}

// memAgo is how long before now t was, roughly, as gnomem says it.
func memAgo(now time.Time, t int64) string {
	d := now.Unix() - t
	switch {
	case d < 60:
		return "just now"
	case d < 3600:
		return strconv.FormatInt(d/60, 10) + "m ago"
	case d < 86400:
		return strconv.FormatInt(d/3600, 10) + "h ago"
	}
	return strconv.FormatInt(d/86400, 10) + "d ago"
}

// readMemStatus reads mem's status.json in dir; ok is false when there is
// none, or it cannot be read.
func readMemStatus(dir string) (st memStatus, ok bool) {
	b, err := os.ReadFile(filepath.Join(dir, memStatusFile))
	if err != nil || json.Unmarshal(b, &st) != nil {
		return memStatus{}, false
	}
	// One of another generation is from before an erasure, which may have
	// taken what it shows: it is not read.
	if hdr, _, err := readJournalHeader(filepath.Join(dir, "journal.jsonl")); err != nil || hdr.Generation != st.Generation {
		return memStatus{}, false
	}
	return st, true
}

// MemBlock is what a new session in project is told of its mem: the <mem>
// block, from the status.json that mem last wrote under dir; "" when mem
// holds nothing to tell. It reads a file, and starts nothing.
func MemBlock(dir, project string) string {
	st, ok := readMemStatus(memDir(dir, project))
	if !ok {
		return ""
	}
	return memBlock(st, time.Now())
}

// MemStatusPath is where the status.json of project's mem is under dir,
// for hooks (GNOSH_MEM_STATUS), which read mem without opening it: mem.JSON()
// with when and for what it was computed (memStatus). It names the file,
// which is there once mem first changes, and makes nothing.
func MemStatusPath(dir, project string) string {
	return filepath.Join(memDir(dir, project), memStatusFile)
}

// MemStatusPath is MemStatusPath for this Lazy's directory.
func (l *Lazy) MemStatusPath(project string) string { return MemStatusPath(l.opts.Dir, project) }

// MemNotice is what the user is told at a session's start, by gnosh itself
// rather than the model, which may not raise it: what in mem is due today,
// and what waits on the user. "" when nothing is.
func MemNotice(dir, project string) string {
	st, ok := readMemStatus(memDir(dir, project))
	if !ok {
		return ""
	}
	return memNotice(st, time.Now())
}

// MemNotice is MemNotice for this Lazy's directory.
func (l *Lazy) MemNotice(project string) string { return MemNotice(l.opts.Dir, project) }

func memNotice(st memStatus, now time.Time) string {
	var m memJSON
	if json.Unmarshal(st.Mem, &m) != nil {
		return ""
	}
	today := now.Format(time.DateOnly)
	var parts []string
	var due []string
	for _, d := range m.Days {
		if d.Wait <= today {
			due = append(due, memNoticeRef(d.Name, d.Title)+" (since "+d.Wait+")")
		}
	}
	if len(due) > 0 {
		parts = append(parts, "due: "+strings.Join(due, "; "))
	}
	if len(m.Asks) > 0 {
		var asks []string
		for _, a := range m.Asks {
			asks = append(asks, memNoticeRef(a.Name, a.Title))
		}
		parts = append(parts, "waiting on you: "+strings.Join(asks, "; "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "mem " + strings.Join(parts, " · ")
}

// memNoticeRef names an item for the user: its title, then its name.
func memNoticeRef(name, title string) string {
	return strconv.Quote(memLine(title, 80)) + " (" + name + ")"
}
