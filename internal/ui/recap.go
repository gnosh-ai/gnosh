package ui

import (
	"strings"

	"github.com/gnosh-ai/gnosh/internal/markdown"
)

// recapLines is how many lines of the model's last answer the recap of a
// session opened again shows; Ctrl+O shows all of it.
const recapLines = 30

// recap shows where a session that was opened again left off, as Claude
// Code shows a resumed session's conversation: the last prompt the person
// sent, and the model's last answer to it, drawn as they were. It is shown
// once for each session that is opened, and only when the app says its
// conversation is not on the screen already (app.SessionInfo.ShowPast).
func (s *session) recap() {
	if !s.info.ShowPast || s.recapped == s.info.ID {
		return
	}
	s.recapped = s.info.ID
	tr, ok := s.c.(transcriber)
	if !ok {
		return
	}
	entries := tr.Transcript()
	last := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == "prompt" && !entries[i].Hidden {
			last = i
			break
		}
	}
	if last < 0 {
		return
	}
	answer := ""
	for _, e := range entries[last+1:] {
		if e.Kind == "reply" {
			if t := strings.TrimSpace(e.Message.Text()); t != "" {
				answer = t
			}
		}
	}
	th := &s.th
	s.begin(blkTurn)
	s.put(wrap(s.m, th.Quiet.Render("Where this session left off (Ctrl+O shows all of it):"), s.cols, "", "")...)
	s.begin(blkPrompt)
	mark := th.Prompt.Render(th.PromptMark)
	indent := strings.Repeat(" ", s.m.Width(th.PromptMark))
	for i, l := range safeLines(entries[last].Message.Text()) {
		first := indent
		if i == 0 {
			first = mark
		}
		s.put(wrap(s.m, th.PromptText.Render(l), s.cols, first, indent)...)
	}
	if answer == "" {
		return
	}
	md := markdown.New(markdown.Options{Width: s.cols, Profile: s.th.Profile, Theme: s.th.Markdown, Metrics: s.m})
	lines := append(md.Write(answer), md.Close()...)
	if len(lines) > recapLines {
		lines = append(lines[:recapLines], th.Quiet.Render("… (Ctrl+O shows the rest)"))
	}
	s.begin(blkAnswer)
	s.put(lines...)
}
