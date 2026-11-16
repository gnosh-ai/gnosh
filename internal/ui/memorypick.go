package ui

import (
	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
	"github.com/gnosh-ai/gnosh/internal/ui/menu"
)

// /memory with nothing after it is a picker of the context files, as
// Claude Code's /memory lists its memory files and opens one: those the
// model is given, then the person's own and the project's GNOSH.md; the
// one picked opens in the person's editor (VISUAL, EDITOR), and a change
// shows in the next session, which reads them as it starts.

// memoryLister is a session whose controls list the context files
// (app.SessionControls).
type memoryLister interface {
	MemoryFiles() []app.MemoryFile
}

// memoryWords are the lines that open the picker.
var memoryWords = map[string]bool{"/memory": true}

// chooseMemory opens the picker, where the session lists the files.
func (s *session) chooseMemory(line string) bool {
	ml, ok := s.c.(memoryLister)
	if !ok {
		return false
	}
	files := ml.MemoryFiles()
	if len(files) == 0 {
		return false
	}
	s.ed.AddHistory(editor.Draft{Text: line})
	var opts []menu.Option
	for _, f := range files {
		opts = append(opts, menu.Option{Label: showPath(s.cwd, s.home, f.Path), Description: oneLine(f.About)})
	}
	opts = append(opts, menu.Option{Label: "Never mind"})
	pick := &question{q: agent.Question{Kind: "memory", Title: "Open which context file in your editor? A change shows in the next session."}}
	pick.ui = menu.NewSelect(menu.SelectOptions{Title: pick.q.Title, Options: opts, CancelHint: "never mind", Theme: s.th.Menu, Metrics: s.m})
	pick.local = func(res menu.Result) {
		if res.Cancelled || res.Index < 0 || res.Index >= len(files) {
			return
		}
		s.openInEditor(files[res.Index].Path, "the next session is given it")
	}
	s.openLocal(pick)
	return true
}

// openInEditor opens a file in the person's editor, and then says it is
// as they left it and what becomes of it (after). The side panel gives the
// screen back first, and comes back after.
func (s *session) openInEditor(path, after string) bool {
	panel := s.panel != nil
	s.closePanel()
	s.frame()
	err := s.host.editFile(path)
	if panel {
		s.openPanel()
	}
	s.dirty = true
	if err != nil {
		s.flash(err.Error())
		return false
	}
	s.flash(showPath(s.cwd, s.home, path) + " is as you left it; " + after + ".")
	return true
}
