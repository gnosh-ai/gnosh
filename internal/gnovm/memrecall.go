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
	"context"
	"regexp"
	"strings"
	"time"
)

// Recall (MEMPLAN §6): with each prompt the user types in a main session,
// gnosh gives the model what mem holds close in meaning to it, which the
// model may not think to look for. It reads mem's index and vectors in
// gnosh's own process, under mem's lock, and waits no longer than
// memEmbedWait: with no embedding server set, or with it down, a prompt
// goes without.

const (
	// memRecalls is how many items or facts recall gives at most, and
	// memRecallMax about how many bytes.
	memRecalls   = 5
	memRecallMax = 1000
)

// MemBlock is what session is told of project's mem as it starts, and
// again after each summary: the <mem> block (MemBlock). What it names is
// not recalled to the session until its next summary.
func (l *Lazy) MemBlock(project, session string) string {
	block := MemBlock(l.opts.Dir, project)
	l.show(session, memNamesIn(block))
	fillMemVectors(memDir(l.opts.Dir, project), newMemEmbedder(l.opts.Embeddings))
	return block
}

// MemChanged is the <mem> block for session, resumed, when mem changed
// after since, the time of the session's last turn: by another session,
// or the user's /mem. A session's own changes come in its turns, before
// since. Both are whole seconds, so a change in the very second the turn
// ended is not seen. "" when mem has not changed since, or holds nothing
// to tell.
func (l *Lazy) MemChanged(project, session string, since time.Time) string {
	st, ok := readMemStatus(memDir(l.opts.Dir, project))
	if !ok || st.At <= since.Unix() {
		return ""
	}
	return l.MemBlock(project, session)
}

// MemSummarized is told that session was summarized: what it was shown
// of mem may be recalled to it again.
func (l *Lazy) MemSummarized(session string) {
	l.shownMu.Lock()
	defer l.shownMu.Unlock()
	delete(l.shown, session)
}

// MemRecall is what project's mem holds close in meaning to prompt, which
// the user typed in session: a <mem> block of at most memRecalls items or
// facts, in about memRecallMax bytes, that the session was not shown since
// its last summary. It is "" when there are none, when no embedding server
// is set, or when the server or mem's lock takes longer than memEmbedWait.
func (l *Lazy) MemRecall(ctx context.Context, project, session, prompt string) string {
	e := newMemEmbedder(l.opts.Embeddings)
	if e == nil || strings.TrimSpace(prompt) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, memEmbedWait)
	defer cancel()
	dir := memDir(l.opts.Dir, project)
	s, ok, err := keptMemSnapshot(ctx, dir)
	if err != nil || !ok {
		return ""
	}
	if s.lacksVectors() {
		fillMemVectors(dir, e)
	}
	if len(s.vecs) == 0 {
		return ""
	}
	qs, model := e.query(ctx, prompt)
	if qs == nil {
		return ""
	}
	// A session this process has told nothing yet is given the <mem> block
	// with its first prompt, after this, or was given it as it started:
	// what the block names is not recalled.
	if !l.hasShown(session) {
		l.show(session, memNamesIn(MemBlock(l.opts.Dir, project)))
	}
	var b strings.Builder
	var names []string
	for _, r := range s.nearest(qs, model, memRecallScore, "item", "fact") {
		if len(names) == memRecalls {
			break
		}
		if l.wasShown(session, r.name) {
			continue
		}
		line := memRecallLine(r.memRow)
		if b.Len()+len(line) > memRecallMax {
			break
		}
		b.WriteString(line)
		names = append(names, r.name)
	}
	if len(names) == 0 {
		return ""
	}
	l.show(session, names)
	return "<mem>\nFrom your memory (mem), what may bear on what the user just wrote, found by meaning; `mem.Show(\"name\")` shows one in full:\n" + b.String() + "</mem>"
}

// memRecallLine is how recall shows a row: a fact with its text; an item
// marked as mem marks it, with its title and, once closed, its result.
func memRecallLine(r memRow) string {
	if r.kind == "fact" {
		return "- fact `" + r.name + "` = " + memEscape(memLine(r.title, 200)) + "\n"
	}
	mark := "[-]"
	switch r.status {
	case "":
		mark = "[ ]"
	case "done":
		mark = "[x]"
	}
	line := "- " + mark + " " + memRef(r.name, r.title)
	if r.result != "" {
		line += " → " + memEscape(memLine(r.result, 160))
	}
	return line + "\n"
}

// memNameRe is a name as mem's text shows one, in backticks.
var memNameRe = regexp.MustCompile("`([^`\\s]+)`")

// memNamesIn is the names text shows in backticks.
func memNamesIn(text string) []string {
	var names []string
	for _, m := range memNameRe.FindAllStringSubmatch(text, -1) {
		names = append(names, m[1])
	}
	return names
}

// show notes that session was shown names.
func (l *Lazy) show(session string, names []string) {
	if session == "" || len(names) == 0 {
		return
	}
	l.shownMu.Lock()
	defer l.shownMu.Unlock()
	if l.shown == nil {
		l.shown = map[string]map[string]bool{}
	}
	if l.shown[session] == nil {
		l.shown[session] = map[string]bool{}
	}
	for _, n := range names {
		l.shown[session][n] = true
	}
}

func (l *Lazy) wasShown(session, name string) bool {
	l.shownMu.Lock()
	defer l.shownMu.Unlock()
	return l.shown[session][name]
}

func (l *Lazy) hasShown(session string) bool {
	l.shownMu.Lock()
	defer l.shownMu.Unlock()
	return l.shown[session] != nil
}
