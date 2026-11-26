package ui

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/ui/editor"
	"github.com/gnosh-ai/gnosh/internal/ui/menu"
)

// What the keys do. The table is in doc.go; this file is where it is
// carried out.
//
// The prompt box has the keys whenever no question is on the screen, also
// while a turn runs: the editor takes each key and says what it did and
// what it asks for (send, stop, leave, help), and the session acts on
// that. A question takes the keys while it is on the screen, once the
// person has stopped typing (see the guard in question.go). A few keys
// are the session's own and work in every state.

// sheller is what a session's controls may also be: able to run a line the
// person typed behind "!" as a command of their own. The app's own
// controls are (app.SessionControls), and so are a backgrounder and a
// historian; what stands in for them in a test need not be.
type sheller interface {
	Shell(command string) error
}

// backgrounder is what a session's controls may also be: able to move the
// running command of a tool call to the background, as Ctrl+B asks. tool
// is the call's tool, which the screen knows from the moment the call
// starts: a command call may not have reached its command yet.
type backgrounder interface {
	Background(callID, tool string) bool
}

// withdrawer is what a session's controls may also be: able to take one
// queued prompt of the person's back off the queue.
type withdrawer interface {
	Withdraw(id string) (agent.Prompt, bool)
}

// rewinder is what a session's controls may also be: able to list the
// person's prompts of this conversation, the oldest first, for the menu
// that takes the conversation back to before one of them. The going back
// itself is the command /rewind N.
type rewinder interface {
	RewindPoints() []app.RewindPoint
}

// resumer is what a session's controls may also be: able to list the
// folder's other sessions, the one written to last first, for the menu
// that opens one in place of the session that is open. The opening
// itself is the command /resume with the session's id.
type resumer interface {
	ResumeChoices() []app.SessionChoice
}

// resumeWords are the lines that open the list of sessions.
var resumeWords = map[string]bool{"/resume": true, "/continue": true}

// rewindWords are the lines that open the list of prompts to go back to.
var rewindWords = map[string]bool{"/rewind": true, "/checkpoint": true, "/undo": true}

// historian is what a session's controls may also be: able to give the
// prompts sent from this folder in earlier sessions, oldest first, for Up
// and Ctrl+R to reach.
type historian interface {
	History() []string
}

// resizeTeller is what a session's controls may also be: told that the
// terminal's size changed, for the status line command, which is told the
// size.
type resizeTeller interface {
	Resized()
}

// vimTeller is what a session's controls may also be: told the mode of
// the box's vim keys, for the status line command (Claude Code's vim.mode).
type vimTeller interface {
	SetVimMode(mode string)
}

// reportVim tells the session the mode of the box's vim keys, when it has
// changed since it was last told.
func (s *session) reportVim() {
	m := s.ed.Vim().String()
	if m == s.vimSaid {
		return
	}
	s.vimSaid = m
	if v, ok := s.c.(vimTeller); ok {
		v.SetVimMode(m)
	}
}

// atOnce are the commands that run while a turn runs. Every other command
// typed meanwhile waits for the session to go idle (spec/cc
// interactive.md, 2.8), except /clear, which stops the turn and runs as
// soon as it has stopped (clearNow).
var atOnce = map[string]bool{
	"status": true, "tasks": true, "model": true, "permissions": true, "config": true,
	"deliverables": true, "artifacts": true,
	"help": true, "hooks": true, "context": true, "doctor": true, "mem": true,
	"cost": true, "usage": true, "stats": true, "agents": true, "memory": true, "bashes": true, "diff": true, "mcp": true, "copy": true,
	"think": true, "no_think": true, "nothink": true, "no-think": true, "effort": true, "btw": true, "recap": true, "reload-skills": true, "skills": true,
	"loop": true, "noloop": true, "no_loop": true, "no-loop": true, "focus": true,
	"exit": true, "quit": true,
}

// commandWord matches a word that reads as a slash command: a slash and a
// plain name. A path, such as /usr/local, does not.
var commandWord = regexp.MustCompile(`^/[a-z][a-z0-9_-]*$`)

// input takes in one thing the terminal reported.
func (s *session) input(ev term.Event) {
	switch ev.(type) {
	case term.Key, term.Mouse, term.Paste, term.Resume:
		// The person is here, in the transcript view, at the side panel's
		// wheel or back from Ctrl+Z as well as at the box: the time without
		// them (a question's time to close by itself, the note for one who
		// is away) starts again.
		s.lastInput = s.now()
	}
	switch ev := ev.(type) {
	case term.Resize:
		s.resize(ev.Cols, ev.Rows)
	case term.Resume:
		// The terminal is gnosh's again after the process was stopped:
		// what was on the screen is no longer what the renderer left.
		s.r.Reset()
		if s.panel != nil {
			// The terminal took the whole screen back for the panel as it
			// was taken back itself (term.Terminal.noteScreen): the panel
			// is drawn anew.
			s.keep(s.panel.Clear())
		}
		if s.view != nil {
			s.keep(s.view.p.Clear())
		}
		if o := s.agents; o != nil {
			o.scr.drawn = nil
			s.keep(o.scr.p.Clear())
		}
		s.dirty = true
	case term.Key, term.Mouse, term.Paste:
		if s.listing {
			// A picker's list is being read (loadThen): what is typed
			// meanwhile is for the list, as had it come at once. Ctrl+C
			// lets a list that is slow to come go.
			if k, ok := ev.(term.Key); ok && k.Is('c', term.ModCtrl) {
				s.dropList("The list was not opened: " + s.listAgain + " opens it again.")
				return
			}
			s.heldInput = append(s.heldInput, ev)
			return
		}
		if k, ok := ev.(term.Key); ok && s.rebound(k) {
			return
		}
		if _, key := ev.(term.Key); !key {
			s.dropHeldKey()
		}
		if s.agents != nil {
			// The agent view takes every key, and pastes, while it is
			// open.
			s.agentsInput(ev)
			return
		}
		if s.view != nil {
			// The transcript view takes every key while it is open. A
			// paste goes into the prompt box, where it is when the view
			// is closed: it was lost (the TUI review of 2026-11-19).
			k, key := ev.(term.Key)
			switch {
			case !key:
				if _, paste := ev.(term.Paste); paste {
					s.pasteAside(ev)
					s.flash("The paste went into the prompt box.")
					return
				}
				s.viewInput(ev)
			case k.Is('l', term.ModCtrl):
				// Draw the view again.
				s.keep(s.view.p.Clear())
			case k.Is('z', term.ModCtrl), k.Is('t', term.ModCtrl), k.Is('t', term.ModAlt), k.Is('b', term.ModCtrl):
				// The keys of every state (doc.go) work here too: they did
				// nothing (the TUI review of 2026-11-19). A stop gives the
				// view's screen back, and the terminal takes it again.
				s.global(k)
			default:
				s.viewInput(ev)
			}
			return
		}
		s.typedOrMouse(ev)
	}
}

// pasteAside puts a paste into the prompt box while something else has the
// keys: the box takes it as it takes any paste, and is locked again after.
func (s *session) pasteAside(ev term.Event) {
	s.ed.SetLocked(false)
	s.box(ev, nil)
	s.lock()
}

// typedOrMouse takes in a key, a paste or the mouse.
func (s *session) typedOrMouse(ev term.Event) {
	switch ev := ev.(type) {
	case term.Key:
		s.typed(ev, &ev)
	case term.Paste:
		s.typed(ev, nil)
	case term.Mouse:
		// The terminal reports the mouse only while the side panel is on:
		// the wheel scrolls back through what was said.
		switch ev.Action {
		case term.MouseWheelUp:
			s.scrollPanel(3)
		case term.MouseWheelDown:
			s.scrollPanel(-3)
		}
	}
}

// typed takes in a key or a paste. k is the key, or nil for a paste.
func (s *session) typed(ev term.Event, k *term.Key) {
	now := s.now()
	// (Taken when the key is done with: Ctrl+Z comes back here only once
	// the shell has given the terminal back.)
	defer func() { s.lastInput = s.now() }()
	s.dirty = true
	if k != nil && s.global(*k) {
		return
	}
	q := s.q
	if q == nil {
		s.box(ev, k)
		return
	}
	if q.guarded && now.Before(q.quiet) {
		// Typed before the question could have been read: the key was
		// meant for the prompt box, and goes there. The question waits
		// for a pause.
		q.quiet = now.Add(guardPause)
		s.box(ev, k)
		return
	}
	q.guarded = false
	s.lock()
	if k != nil && k.IsKey(term.KeyTab, term.ModShift) && s.allowEdits(q) {
		return
	}
	res := q.ui.Handle(ev)
	if k != nil && !res.Handled && k.Mod&term.ModAlt != 0 && (k.Code == term.KeyRune || k.Code == term.KeyEscape) {
		// Esc with a key typed straight after it reaches gnosh, from a
		// terminal that sends Alt as Esc and the key, as that key with
		// Alt: a key the question has no use for, it is the Esc it began
		// with, which was lost (Claude Code 2.1.328 declines with such a
		// key too).
		res = q.ui.Handle(term.Key{Code: term.KeyEscape})
	}
	if k == nil && !res.Handled {
		// A paste the question has no field open for goes into the
		// prompt box, in sight under the question, instead of nowhere
		// (the TUI review of 2026-11-19).
		s.pasteAside(ev)
		s.flash("The paste went into the prompt box: the question takes keys, and words only in a field it opens.")
		return
	}
	if !res.Done {
		return
	}
	if q.local != nil {
		// A menu of the interface's own: the session is not answered.
		s.closeQuestion()
		q.local(res)
		return
	}
	ans, record, how := q.answer(res)
	if how == endLeft {
		// Esc on a question that has no "no": the turn is stopped, in
		// plain words, and the session takes its question back.
		s.record(q, record)
		q.answered = true
		s.interrupt()
		s.closeQuestion()
		return
	}
	if err := s.c.Answer(q.q.ID, ans); err != nil {
		// Answered a moment before from elsewhere (a channel that relays
		// the questions): this answer did not count, and the question's
		// end, on its way, says what did.
		return
	}
	s.record(q, record)
	q.answered = true
	s.closeQuestion()
}

// suspendNow stops the process, as Ctrl+Z does to any program: the shell
// has the screen while gnosh is stopped. When it is continued the live
// region is drawn afresh, at the cursor. The side panel gives the screen
// back first, and comes back after.
func (s *session) suspendNow() {
	panel := s.panel != nil
	s.closePanel()
	s.frame()
	s.keep(s.r.Clear())
	if err := s.host.suspend(); err != nil {
		// Nothing was stopped, and nothing else has written to the
		// terminal: the region is drawn again where it was.
		s.flash("Ctrl+Z: " + err.Error() + ".")
	} else {
		s.r.Reset()
	}
	if panel {
		s.openPanel()
	}
}

// global carries out the keys that are the session's own in every state,
// and reports whether k was one.
func (s *session) global(k term.Key) bool {
	switch {
	case k.Is('l', term.ModCtrl):
		// Draw the live region again, in case something else wrote over
		// it. The scrollback is the terminal's and is left alone.
		if cols, rows, ok := s.host.size(); ok {
			s.resize(cols, rows)
		}
		s.keep(s.r.Clear())
	case k.Is('z', term.ModCtrl):
		s.suspendNow()
	case s.panel != nil && k.IsKey(term.KeyPageUp, 0):
		s.scrollPanel(s.panel.Page())
	case s.panel != nil && k.IsKey(term.KeyPageDown, 0):
		s.scrollPanel(-s.panel.Page())
	case k.Is('o', term.ModCtrl):
		s.toggleView()
	case k.Is('t', term.ModAlt):
		s.toggleThinking()
	case k.Is('p', term.ModAlt) && s.q == nil:
		// The model picker, with what is typed left in the box.
		s.chooseModel("")
	case k.Is('t', term.ModCtrl):
		switch {
		case len(s.tasks) == 0:
			s.flash("There is no task list to show: the session has none.")
		case s.hideTasks:
			s.hideTasks = false
		default:
			s.hideTasks = true
			if k := s.keyHint("app:toggleTodos", "Ctrl+T"); k != "" {
				s.flash("The task list is out of sight. " + k + " shows it again.")
			} else {
				s.flash("The task list is out of sight.")
			}
		}
	case k.Is('b', term.ModCtrl):
		s.background()
	default:
		return false
	}
	s.dirty = true
	return true
}

// agentStopper is a session whose controls stop its background sub-agents
// (app.SessionControls).
type agentStopper interface {
	StopBackgroundAgents() int
}

// stopAgentsWait is how soon Ctrl+X Ctrl+K must come again to stop the
// background sub-agents.
const stopAgentsWait = 3 * time.Second

// stopAgentsKey is Ctrl+X Ctrl+K, as Claude Code's: pressed twice within
// three seconds, it stops every sub-agent that runs in the background.
// The first press says what the second does.
func (s *session) stopAgentsKey() {
	st, ok := s.c.(agentStopper)
	if !ok {
		s.flash("Stopping the background sub-agents (Ctrl+X Ctrl+K) is not joined to this build.")
		return
	}
	now := s.now()
	if s.stopArmed.IsZero() || now.Sub(s.stopArmed) > stopAgentsWait {
		s.stopArmed = now
		s.flash("Ctrl+X Ctrl+K again within 3 seconds stops every sub-agent that runs in the background.")
		return
	}
	s.stopArmed = time.Time{}
	s.host.spawn(func() func() {
		n := st.StopBackgroundAgents()
		return func() {
			if n == 0 {
				s.flash("No sub-agent runs in the background.")
				return
			}
			s.say(count(n, "sub-agent that ran in the background is", "sub-agents that ran in the background are") + " stopped.")
		}
	})
}

// box gives a key or a paste to the prompt box and acts on what the box
// asks for.
func (s *session) box(ev term.Event, k *term.Key) {
	if s.suggestion != "" {
		// Tab or the right arrow in the empty box takes the suggested
		// prompt, as in Claude Code; anything else drops it.
		take := k != nil && s.ed.IsEmpty() && (k.IsKey(term.KeyTab, 0) || k.IsKey(term.KeyRight, 0))
		text := s.suggestion
		s.suggestion = ""
		if take {
			s.ed.SetText(text)
			return
		}
	}
	if k != nil && !k.IsKey(term.KeyLeft, 0) {
		s.leftAt = time.Time{}
	}
	if k != nil {
		switch {
		case !s.chord && k.IsKey(term.KeyLeft, 0) && s.ed.IsEmpty() && s.agentsOffered():
			// ← in the empty box, twice, opens the agent view, as it
			// does in Claude Code (agentsover.go).
			s.leftKey()
			return
		case s.chord:
			// The key after Ctrl+X.
			s.chord = false
			switch {
			case k.Is('e', term.ModCtrl):
				s.external()
			case k.Is('b', 0), k.Is('b', term.ModCtrl):
				// Ctrl+B is tmux's own key, where it has to be pressed
				// twice; this is the same without it.
				s.background()
			case k.Is('s', term.ModCtrl), k.Is('s', 0):
				// Ctrl+Enter, for a terminal that cannot tell it from
				// Enter.
				s.sendNow()
			case k.IsKey(term.KeyUp, 0):
				// Alt+Up, for a terminal that does not send it.
				s.withdrawLast()
			case k.Is('k', term.ModCtrl), k.Is('k', 0):
				s.stopAgentsKey()
			default:
				s.flash("Ctrl+X " + k.String() + " does nothing here. Ctrl+X Ctrl+E opens your editor.")
			}
			return
		case k.Is('x', term.ModCtrl):
			s.chord = true
			return
		case k.IsKey(term.KeyUp, term.ModAlt):
			s.withdrawLast()
			return
		case k.IsKey(term.KeyTab, term.ModShift):
			s.cycleMode()
			return
		case k.Is('v', term.ModCtrl) && s.ed.Mode() == editor.ModePrompt && !s.ed.Searching():
			// (Not into the history search, which would take @image-N
			// for what to look for.)
			s.pastePicture()
			return
		}
	}
	if p, ok := ev.(term.Paste); ok && s.ed.Mode() == editor.ModePrompt && !s.ed.Searching() {
		// Files dragged in: their paths become @ mentions (picture.go).
		if text, ok := s.picturePaths(p.Text); ok {
			p.Text = s.spaced(text)
			ev = p
			s.flash("The picture goes with the prompt.")
		}
	}
	s.ed.SetBusy(s.busy || s.cmdRun != "")
	res := s.ed.Handle(ev)
	s.reportVim()
	if res.Truncated {
		s.flash("The terminal cut that paste short. What arrived is in the box.")
	}
	if res.Hint == editor.HintStashed {
		// The warnings of a first press show for as long as they are in
		// force (see hintLines); this one is said once.
		s.flash(res.Hint.String())
	}
	switch res.Action {
	case editor.ActionSubmit:
		d := editor.Draft{Text: res.Text, Pastes: res.Pastes}
		if !s.cleanToSend(&d, res.Mode) {
			break
		}
		s.failHint = ""
		switch res.Mode {
		case editor.ModeShell:
			s.shell(d)
		case editor.ModeCommand:
			s.command(d)
		default:
			if leaveWord(d.Text) && len(d.Pastes) == 0 {
				// The one word leaves, as /exit does and as it does in
				// Claude Code. To send the word to the model, say more.
				s.command(editor.Draft{Text: "/exit"})
				break
			}
			s.send(d)
			if res.Now {
				// Ctrl+Enter: not behind what is running, but now.
				s.sendNow()
			}
		}
	case editor.ActionInterrupt:
		s.interrupt()
	case editor.ActionExit:
		s.leave(0, "prompt_input_exit")
	case editor.ActionRewind:
		s.rewind()
	case editor.ActionHelp:
		s.help()
	case editor.ActionEditor:
		s.external()
	case editor.ActionQueue:
		s.takeBack()
	}
}

// send hands a prompt to the session. While a turn runs the session
// queues it. Either way it shows as queued until it is seen going out,
// so it is never out of sight.
func (s *session) send(d editor.Draft) {
	p := agent.Prompt{Text: d.Text, Attachments: s.attach, Meta: pastedMeta(d.Pastes)}
	id, err := s.c.Submit(p)
	if err != nil {
		s.restore(d)
		s.notice(agent.LevelWarning, "The prompt was not sent: "+err.Error()+"\nIt is back in the prompt box.")
		return
	}
	s.attach = nil
	s.sending = append(s.sending, sent{id: id, draft: d})
	s.queued()
}

// shell runs a line typed behind "!" as a command of the person's own,
// when the session can do that.
func (s *session) shell(d editor.Draft) {
	sh, ok := s.c.(sheller)
	if !ok {
		s.restore(d)
		s.notice(agent.LevelWarning, "Shell lines (a line that begins with !) are not joined to this build yet, so the command was not run.\nIt is back in the prompt box.")
		return
	}
	if err := sh.Shell(strings.TrimPrefix(d.Text, "!")); err != nil {
		s.restore(d)
		s.notice(agent.LevelWarning, "The command was not run: "+err.Error()+"\nIt is back in the prompt box.")
		return
	}
	s.ed.AddHistory(d)
}

// ownCommands are the commands the interface carries out itself when
// nothing follows them, at once, also while a turn runs: the pickers (the
// session's command with words after it is what each runs), the side
// panel and the list of deliverables. take says whether it took the
// line: where the session's controls cannot do their part, or a question
// is on the screen, the line goes to the session's command as it is.
var ownCommands = []struct {
	words map[string]bool
	take  func(s *session, line string) bool
}{
	{rewindWords, func(s *session, _ string) bool {
		_, ok := s.c.(rewinder)
		if ok {
			s.rewind()
		}
		return ok
	}},
	{resumeWords, func(s *session, _ string) bool {
		_, ok := s.c.(resumer)
		if ok {
			s.resume()
		}
		return ok
	}},
	{modelWords, func(s *session, line string) bool {
		_, ok := s.c.(modelChooser)
		if ok {
			s.chooseModel(line)
		}
		return ok
	}},
	{themeWords, func(s *session, line string) bool { return s.q == nil && s.chooseTheme(line) }},
	{configWords, func(s *session, line string) bool {
		// (Asked before the picker opens: the picker is a question too.)
		if _, ok := s.c.(configChooser); !ok || s.q != nil {
			return false
		}
		s.chooseConfig(line)
		return true
	}},
	{exportWords, func(s *session, line string) bool { return s.q == nil && s.chooseExport(line) }},
	{memoryWords, func(s *session, line string) bool { return s.q == nil && s.chooseMemory(line) }},
	{keybindingsWords, func(s *session, line string) bool { return s.q == nil && s.openKeybindings(line) }},
	{mcpWords, func(s *session, line string) bool { return s.q == nil && s.chooseMCP(line) }},
	{hooksWords, func(s *session, line string) bool { return s.q == nil && s.chooseHooks(line) }},
	{agentsWords, func(s *session, line string) bool { return s.q == nil && s.chooseAgents(line) }},
	{permissionsWords, func(s *session, line string) bool { return s.q == nil && s.choosePermissions(line) }},
	{skillsWords, func(s *session, line string) bool { return s.q == nil && s.chooseSkills(line) }},
	{effortWords, func(s *session, line string) bool { return s.q == nil && s.chooseEffort(line) }},
	{focusWords, func(s *session, line string) bool {
		// Turned over by what the screen shows.
		arg := "on"
		if s.focus {
			arg = "off"
		}
		s.run(line + " " + arg)
		return true
	}},
	{panelWords, func(s *session, line string) bool {
		s.ed.AddHistory(editor.Draft{Text: line})
		s.togglePanel()
		return true
	}},
	{deliverWords, func(s *session, line string) bool {
		ok := s.listsDeliverables()
		if ok {
			s.listDeliverables(line)
		}
		return ok
	}},
}

// command takes a line that begins with a slash. While a turn runs it
// waits for the session to go idle, unless it is one of the few commands
// that act at once, or /clear, which stops the turn instead of waiting
// for it (clearNow).
func (s *session) command(d editor.Draft) {
	line := strings.TrimSpace(d.Text)
	word, _, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	for _, own := range ownCommands {
		if own.words[strings.ToLower(line)] && own.take(s, line) {
			return
		}
	}
	if s.busy && !atOnce[strings.ToLower(word)] {
		if s.clearCommand(line) {
			s.clearNow(line)
			return
		}
		s.cmdq = append(s.cmdq, line)
		s.queued()
		return
	}
	s.run(line)
}

// shownCommand is a slash command's line as the interface shows it and
// remembers it, and whether something was left out of it. What follows
// "/mem redact" is a text the person wants gone from everywhere: the
// session keeps it out of its log, and the interface keeps it out of the
// scrollback, the live region and the history. (Nothing in the session's
// controls says which commands carry such a text, so this one is named
// here.)
func shownCommand(line string) (shown string, held bool) {
	words := strings.Fields(line)
	if len(words) > 2 && strings.EqualFold(words[0], "/mem") && strings.EqualFold(words[1], "redact") {
		return words[0] + " " + words[1] + " (what was typed after it is not shown)", true
	}
	return line, false
}

// run runs a slash command. The session code runs it and may take its
// time, so it runs away from the loop: the box stays usable meanwhile,
// and Esc stops it.
func (s *session) run(line string) {
	if s.cmdRun != "" {
		if sideCommand(s.cmdRun) && (exitCommand(line) || s.clearCommand(line)) {
			// /exit and /clear do not wait behind a side question, which
			// may take minutes: that is let go, and the command is next.
			if s.cmdStop != nil {
				s.cmdStop()
			}
			s.cmdq = append([]string{line}, s.cmdq...)
			s.queued()
			return
		}
		s.cmdq = append(s.cmdq, line)
		s.queued()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cmdRun, s.cmdStop = line, cancel
	// A command of the session's is shown as typed when it starts, as a
	// prompt is: what it says meanwhile (the notes of a summary it waits
	// for, minutes on a local model) comes after it. A line that may be no
	// command is shown once it has run (ran).
	if s.cmdShown = s.isCommand(line); s.cmdShown {
		s.showTyped(line)
	}
	c := s.c
	s.host.spawn(func() func() {
		res, err := c.Command(ctx, line)
		return func() {
			cancel()
			s.ran(line, res, err)
		}
	})
}

// sideCommand reports whether line is a side question's command: /btw or
// /recap, which only wait for the model's answer.
func sideCommand(line string) bool {
	word, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "/"), " ")
	switch strings.ToLower(word) {
	case "btw", "recap":
		return true
	}
	return false
}

// exitCommand reports whether line ends the session: /exit or /quit.
func exitCommand(line string) bool {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "/exit", "/quit":
		return true
	}
	return false
}

// clearCommand reports whether line opens a fresh session: the session's
// /clear, by its name or an alias (/new, /reset), with or without a name
// after it.
func (s *session) clearCommand(line string) bool {
	word, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "/"), " ")
	word = strings.ToLower(word)
	for _, c := range s.info.CommandList {
		if c.Name == "clear" && (word == c.Name || slices.Contains(c.Aliases, word)) {
			return true
		}
	}
	return false
}

// clearNow is /clear (or /new, /reset) typed while a turn runs. It does
// not wait for the turn to end, which on a local model reading a long
// conversation may take minutes (Jae, 26 November 2026: "/clear doesn't
// work when it's thinking on a previous prompt... i have to wait. i don't
// want to"). What the person queued comes back into the box first, so
// that none of it starts another turn in the session being left, or is
// lost. Then the turn is stopped, with a side question that waits, and
// the command goes first in the queue, to run as soon as the turn's end
// is in (goIdle). The session is not cleared under a turn that is still
// stopping: that turn's last events would land on the new session's
// screen.
func (s *session) clearNow(line string) {
	waits := len(s.cmdq) > 0
	for _, q := range s.c.Queued() {
		waits = waits || !q.Prompt.Hidden
	}
	if waits {
		s.takeBack()
	}
	// (What waited is in the box now.)
	s.cmdq = []string{line}
	if s.cmdStop != nil {
		s.cmdStop()
	}
	s.stopping = true
	s.c.Interrupt()
	s.queued()
	s.flash("The turn is being stopped so that " + oneLine(strings.Fields(line)[0]) + " runs now.")
}

// clearWaits reports whether /clear waits, first in the queue, for the
// turn to end (clearNow).
func (s *session) clearWaits() bool {
	return len(s.cmdq) > 0 && s.clearCommand(s.cmdq[0])
}

// isCommand reports whether line names one of the session's commands, by
// its name or one of its aliases.
func (s *session) isCommand(line string) bool {
	word, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "/"), " ")
	word = strings.ToLower(word)
	for _, c := range s.info.CommandList {
		if c.Name == word || slices.Contains(c.Aliases, word) {
			return true
		}
	}
	return false
}

// showTyped puts a command line into the scrollback as the person typed
// it, with what is not to be kept of it held back (shownCommand).
func (s *session) showTyped(line string) {
	th := &s.th
	shown, _ := shownCommand(line)
	s.begin(blkPrompt)
	s.put(wrap(s.m, th.PromptText.Render(oneLine(shown)), s.cols, th.Prompt.Render(th.PromptMark), strings.Repeat(" ", s.m.Width(th.PromptMark)))...)
}

// verboseSet reads a /config line for the verbose option set true or
// false, the last time it is named.
func verboseSet(line string) (on, ok bool) {
	switch v, _ := configSet(line, "verbose"); v {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// configSet is the value a /config line gives the option key, without its
// quotes, and whether it gives one.
func configSet(line, key string) (string, bool) {
	words := strings.Fields(line)
	if len(words) == 0 || !strings.EqualFold(words[0], "/config") {
		return "", false
	}
	val, ok := "", false
	for _, w := range words[1:] {
		if v, found := strings.CutPrefix(w, key+"="); found {
			val, ok = strings.Trim(v, `"'`), true
		}
	}
	return val, ok
}

// ran takes in what a slash command came to.
func (s *session) ran(line string, res app.CommandResult, err error) {
	shown := s.cmdShown
	s.cmdRun, s.cmdStop, s.cmdShown = "", nil, false
	s.dirty = true
	word, _, _ := strings.Cut(line, " ")
	switch {
	case !res.Handled && err == nil && commandWord.MatchString(strings.ToLower(word)):
		// No such command. The line goes back into the box.
		s.restore(editor.Draft{Text: line})
		s.notice(agent.LevelWarning, "There is no command "+oneLine(word)+". /help lists the commands. To send a prompt that begins with a slash, put a space in front of it.\nWhat you typed is back in the prompt box.")
	case !res.Handled && err == nil:
		// A prompt that happens to begin with a slash, such as a path.
		s.send(editor.Draft{Text: line})
	default:
		_, held := shownCommand(line)
		switch {
		case !shown:
			s.showTyped(line)
		case s.last != blkPrompt && strings.TrimSpace(res.Text) != "":
			// Something came between: what the command says is a block
			// of its own.
			s.begin(blkText)
		}
		for _, l := range safeLines(res.Text) {
			s.put(wrap(s.m, l, s.cols, "", "")...)
		}
		if !held {
			s.ed.AddHistory(editor.Draft{Text: line})
		}
		if res.Copy != "" {
			if cerr := s.host.copy(res.Copy); cerr != nil {
				s.notice(agent.LevelWarning, "The text could not be put on the clipboard: "+cerr.Error())
			}
		}
		if err != nil {
			s.notice(agent.LevelError, err.Error())
		}
		if on, ok := verboseSet(line); ok && err == nil && res.Name == "config" {
			// The verbose option is the interface's own: what /config set
			// is in force here at once.
			if on {
				s.setView("verbose")
			} else {
				s.detail = false
			}
		}
		if mode, ok := configSet(line, "viewMode"); ok && err == nil && res.Name == "config" {
			// So is the viewMode option, and /focus, which sets it; set to
			// nothing, verbose decides.
			s.setView(firstNonEmpty(mode, s.unfocused()))
		}
		if on, ok := focusSet(line); ok && err == nil && res.Name == "focus" {
			switch {
			case on:
				s.setView("focus")
			case s.focus:
				s.setView(s.unfocused())
			}
		}
		if mode, ok := configSet(line, "editorMode"); ok && mode != "" && err == nil && res.Name == "config" {
			// So is the editorMode option.
			s.ed.SetVim(mode == "vim")
			s.reportVim()
		}
		if name, ok := configSet(line, "theme"); ok && err == nil && res.Name == "config" {
			// And the theme option: the colours change at once.
			s.setTheme(name)
		}
		if name, ok := themeSet(line); ok && err == nil && res.Name == "theme" {
			s.setTheme(name)
		}
		if res.Quit {
			// (/restart and /background: the session goes on in another
			// gnosh at once, which says itself how to come back to it.)
			s.restarting = res.Name == "restart" || res.Name == "background"
			s.leave(0, "prompt_input_exit")
			return
		}
		if res.Name == "clear" || res.Reset {
			// A new session stands behind the same controls, or the same
			// one at another point of its conversation: nothing of what
			// was under way is any more.
			s.busy, s.stopping, s.ph = false, false, phIdle
			s.reply, s.calls, s.queue, s.sending = reply{}, nil, nil, nil
			s.fold = fold{}
			s.q, s.qwait = nil, nil
			s.loop = false
			s.closeView()
			s.facts = map[string]string{}
			s.changed = nil
			s.lock()
		}
		s.refresh()
		// (/resume: the session opened shows where it left off.)
		s.recap()
		// A command may have put another session behind the controls,
		// with a task list of its own or none, and deliverables of its
		// own or none.
		s.tasks = s.c.Tasks()
		s.readDeliverables()
		if res.Draft != "" {
			// The prompt a rewind went back to is in the box again, to be
			// sent as it was or changed.
			s.restore(editor.Draft{Text: res.Draft})
		}
	}
	s.queued()
	s.nextCommand()
}

// nextCommand runs the next slash command that waited for the session to
// go idle.
func (s *session) nextCommand() {
	if s.busy || s.cmdRun != "" || len(s.cmdq) == 0 || s.done {
		return
	}
	line := s.cmdq[0]
	s.cmdq = s.cmdq[1:]
	s.queued()
	s.run(line)
}

// interrupt stops the turn that is running, all of it and at once, and a
// slash command that is running. The line above the box says so until
// the turn's end arrives.
func (s *session) interrupt() {
	if s.cmdStop != nil && sideCommand(s.cmdRun) {
		// A side question (/btw, /recap) waits: Esc lets it go, and the
		// turn goes on; Esc again stops the turn (the side-request review
		// of 2026-11-19).
		s.cmdStop()
		s.dirty = true
		return
	}
	if s.cmdStop != nil {
		s.cmdStop()
	}
	if s.busy {
		s.stopping = true
	}
	s.c.Interrupt()
	s.dirty = true
}

// takeBack brings the prompts that wait for their turn back into the box
// for editing, and off the queue: what Up does in an empty box. Slash
// commands that waited come back after them. Enter then sends all of it
// as one prompt.
func (s *session) takeBack() {
	var d editor.Draft
	add := func(text string, pastes []editor.Span) {
		if strings.TrimSpace(text) == "" {
			return
		}
		if d.Text != "" {
			d.Text += "\n\n"
		}
		at := len(d.Text)
		d.Text += text
		for _, sp := range pastes {
			d.Pastes = append(d.Pastes, editor.Span{Start: sp.Start + at, End: sp.End + at})
		}
	}
	for _, p := range s.c.TakeQueued() {
		add(p.Text, pastesOf(p.Meta))
		s.attach = append(s.attach, p.Attachments...)
		for i, x := range s.sending {
			if x.draft.Text == p.Text {
				s.sending = append(s.sending[:i], s.sending[i+1:]...)
				break
			}
		}
	}
	for _, line := range s.cmdq {
		add(line, nil)
	}
	s.cmdq = nil
	// The session's own list of what waits follows as an event; until
	// then, what was just taken back is not shown as waiting.
	kept := s.queue[:0:0]
	for _, q := range s.queue {
		if q.Prompt.Hidden {
			kept = append(kept, q)
		}
	}
	s.queue = kept
	if d.Text == "" {
		s.flash("Nothing waits in the queue any more.")
	}
	s.restore(d)
	s.queued()
}

// external opens the box's text in the person's own editor. The side
// panel gives the screen back first, and comes back after.
func (s *session) external() {
	panel := s.panel != nil
	s.closePanel()
	s.frame()
	edited, err := s.host.edit(s.ed.Text())
	if panel {
		s.openPanel()
	}
	s.dirty = true
	if err != nil {
		s.flash(err.Error())
		return
	}
	s.ed.SetText(strings.TrimRight(edited, "\n"))
}

// modeOrder is the order Shift+Tab goes through the permission modes in.
// Skipping permissions is part of it only in a session that was started
// with it within reach: the session refuses it otherwise, and it is
// passed over.
var modeOrder = []string{"default", "acceptEdits", "plan", "bypassPermissions"}

// cycleMode moves to the next permission mode.
func (s *session) cycleMode() {
	cur := s.c.Mode()
	at := -1
	for i, m := range modeOrder {
		if m == cur {
			at = i
		}
	}
	for step := 1; step <= len(modeOrder); step++ {
		next := modeOrder[(at+step+len(modeOrder))%len(modeOrder)]
		if next == cur {
			break
		}
		if err := s.c.SetMode(next); err != nil {
			continue
		}
		s.mode = next
		if next == "bypassPermissions" {
			s.notice(agent.LevelWarning, "The permission mode is now BYPASS PERMISSIONS: nothing asks before it runs, except what a deny rule or an ask rule names and the removals gnosh always asks about. Shift+Tab leaves it.")
		} else {
			if k := s.keyHint("chat:cycleMode", "Shift+Tab"); k != "" {
				s.flash("Permission mode: " + modeNames[next] + ". " + k + " goes on to the next.")
			} else {
				s.flash("Permission mode: " + modeNames[next] + ".")
			}
		}
		s.dirty = true
		return
	}
	s.flash("The permission mode stays " + firstNonEmpty(modeNames[cur], cur) + ": there is no other to go to.")
}

// allowEdits is Shift+Tab on a question about an edit: it answers yes and
// accepts edits for the rest of the session, when the question offers
// that. It reports whether it did.
func (s *session) allowEdits(q *question) bool {
	if q.q.Kind != "permission" || q.q.Action == nil || q.q.Action.Kind != core.ActionEdit {
		return false
	}
	for i, key := range q.keys {
		// The gate offers the change of mode as one of its "yes, and
		// don't ask again" answers, and names the mode in it.
		if strings.HasPrefix(key, "always:") && strings.Contains(q.labels[i], "acceptEdits") {
			s.record(q, q.labels[i])
			q.answered = true
			if err := s.c.Answer(q.q.ID, agent.Answer{Choice: key}); err != nil {
				s.say("That question is no longer waiting for an answer.")
			}
			s.closeQuestion()
			return true
		}
	}
	return false
}

// background moves the commands that are running to the background, when
// the session can do that.
func (s *session) background() {
	b, ok := s.c.(backgrounder)
	if !ok {
		s.flash("Moving a running command to the background (Ctrl+B) is not joined to this build yet.")
		return
	}
	moved := 0
	for _, c := range s.calls {
		if c.started && b.Background(c.id, c.name) {
			moved++
		}
	}
	if moved == 0 {
		s.flash("No command is running that could move to the background.")
	}
}

// ask puts a question on the screen, or behind the one that is there.
// sub is the id of the sub-agent that asks, if one does.
func (s *session) ask(q agent.Question, sub string) {
	qs := newQuestion(&s.th, s.m, s.cols, q, -1)
	qs.sub = sub
	if s.q != nil {
		s.qwait = append(s.qwait, qs)
		return
	}
	s.open(qs)
}

// open makes qs the question on the screen. It takes no key until the
// person has had time to see it and has stopped typing.
func (s *session) open(qs *question) {
	// A question is for the person: the transcript view gives the screen
	// back, so that it is seen.
	s.closeView()
	s.dropHeldKey()
	now := s.now()
	qs.opened, qs.guarded = now, true
	qs.quiet = now.Add(guardShown)
	if t := s.lastInput.Add(guardPause); t.After(qs.quiet) {
		qs.quiet = t
	}
	if qs.secret {
		// A key's password is typed at once, often before its question
		// is drawn: no key typed for it may go to the prompt box, where
		// Enter would send it to the model. The question takes keys at
		// once, and what was typed into the box since the question
		// before it closed (the approval of the command, a moment ago)
		// is taken out again.
		qs.guarded = false
		if !s.closedAt.IsZero() && now.Sub(s.closedAt) < secretGap && s.ed.Text() != s.closedText {
			s.ed.SetText(s.closedText)
		}
	}
	if qs.form != nil && !qs.escCancels && qs.local == nil && qs.q.Tool == "AskUserQuestion" {
		qs.idle = s.info.AskTimeout
	}
	if cur := s.q; cur != nil && cur != qs && cur.local == nil && !cur.answered {
		// A menu of the interface's own takes the screen from the
		// session's question (a picker's second screen, opened just as
		// the first one closed and put up the question that waited
		// behind it): the question goes back to the head of the line,
		// not out of it. It was lost, and its turn waited for ever (the
		// TUI review of 2026-11-19).
		s.qwait = append([]*question{cur}, s.qwait...)
	}
	s.q = qs
	s.dirty = true
}

// questionAway closes the model's questions when they went without a key
// for their idle time (the askUserQuestionTimeout option): what was picked
// by then is the answer, with the Choice app.AskedAway, and the model goes
// on without the person.
func (s *session) questionAway(q *question) {
	f, ok := q.ui.(*menu.Form)
	if !ok {
		q.idle = 0
		return
	}
	ans, record, _ := q.answer(menu.Result{Done: true, Answers: f.Answers()})
	ans.Choice = app.AskedAway
	if err := s.c.Answer(q.q.ID, ans); err != nil {
		// Answered a moment before from elsewhere.
		return
	}
	words := "no answer in " + span(q.idle) + ": the model goes on without you"
	for _, line := range strings.Split(record, "\n") {
		if !strings.HasSuffix(line, "(no answer)") {
			words += "; picked by then: " + strings.ReplaceAll(record, "\n", "; ")
			break
		}
	}
	s.record(q, words)
	q.answered = true
	s.closeQuestion()
}

// closeQuestion takes the question off the screen. The prompt box has the
// keys again, with whatever was in it, unless another question waits.
func (s *session) closeQuestion() {
	s.q = nil
	s.closedText, s.closedAt = s.ed.Text(), s.now()
	if len(s.qwait) > 0 {
		next := s.qwait[0]
		s.qwait = s.qwait[1:]
		s.open(next)
	}
	s.lock()
	s.dirty = true
}

// answered takes in the session's word that a question is over.
func (s *session) answered(ev *agent.AnswerEvent) {
	if q := s.q; q != nil && q.q.ID == ev.ID {
		if !q.answered {
			words := "answered elsewhere"
			if ev.Answer.By != "" {
				words = "answered by " + ev.Answer.By
			}
			for _, c := range q.q.Choices {
				if c.Key == ev.Answer.Choice && ev.Answer.Choice != "" {
					words += ": " + c.Label
				}
			}
			switch {
			case ev.Withdrawn && !q.deadline.IsZero() && !s.now().Before(q.deadline):
				words = "taken back unanswered: nobody answered in time"
			case ev.Withdrawn:
				words = "taken back unanswered: the turn was stopped"
			}
			s.record(q, words)
		}
		s.closeQuestion()
		return
	}
	for i, q := range s.qwait {
		if q.q.ID == ev.ID {
			s.qwait = append(s.qwait[:i], s.qwait[i+1:]...)
			return
		}
	}
}

// record writes a question and what became of it into the scrollback, so
// that what was decided can be read back.
func (s *session) record(q *question, words string) {
	th := &s.th
	title := oneLine(firstNonEmpty(q.q.Title, q.q.Kind, "question"))
	if q.sub != "" {
		title = "(for a sub-agent) " + title
	}
	lines := safeLines(words)
	if len(lines) == 0 {
		lines = []string{""}
	}
	s.begin(blkAsk)
	s.put(answerRows(th, s.m, s.cols, title, lines)...)
}

// answerRoom is the fewest cells a question's title may leave for its
// answer beside it in the record.
const answerRoom = 8

// answerRows are the rows that record a question's answer: "? title" and
// the answer beside it. Several answers (a form's), or a title that
// leaves no room beside it, put the title on its own row and each answer
// under it: beside a title that filled the row, an answer lost its first
// letters (the TUI review of 2026-11-19: "es: use this folder's
// settings").
func answerRows(th *Theme, m text.Metrics, cols int, title string, answers []string) []string {
	lead := th.Ask.Render(th.AskMark) + " "
	if len(answers) == 1 && m.VisibleWidth(lead+title+" ")+answerRoom <= cols {
		return wrap(m, th.Answer.Render(answers[0]), cols, lead+title+" ", "  ")
	}
	rows := wrap(m, title, cols, lead, "  ")
	for _, l := range answers {
		rows = append(rows, wrap(m, th.Answer.Render(l), cols, "  ", "  ")...)
	}
	return rows
}

// help writes the keys into the scrollback, where they can be read and
// scrolled like anything else. What this build cannot do yet is said
// under them.
func (s *session) help() {
	th := &s.th
	rows := [][2]string{
		{"Enter", "send; while a turn runs, queue the prompt behind it"},
		{"\\ then Enter, Ctrl+J", "new line (Shift+Enter too, where the terminal reports it)"},
		{"Esc", "stop the turn; idle, twice: clear the box (Up brings it back)"},
		{"Esc Esc", "in an empty box: go back to before one of your prompts (/rewind)"},
		{"Ctrl+C", "stop the turn; idle: clear the box, and again to leave"},
		{"Ctrl+D", "on an empty box, twice: leave"},
		{"Up, Down", "history; Up in an empty box takes the queue back for editing"},
		{"Alt+Up, Ctrl+X Up", "take back the prompt you queued last, and leave the others queued"},
		{"Ctrl+Enter, Ctrl+X Ctrl+S", "while a turn runs: send, and have it read now, not after the turn"},
		{"Ctrl+R", "search the history"},
		{"/ at the start", "a command, completed as you type (/help lists them)"},
		{"@", "a file to mention, completed as you type"},
		{"Tab", "take a completion; on a folded paste, unfold it"},
		{"Shift+Tab", "the next permission mode; it is always on the status line"},
		{"Ctrl+O", "the whole transcript: every thought, call and result"},
		{"Alt+T", "thinking on or off (or the next level)"},
		{"Alt+P", "switch the model, keeping what is typed"},
		{"Ctrl+B, Ctrl+X b", "move the running command to the background"},
		{"Ctrl+X Ctrl+K", "twice within 3 seconds: stop the background sub-agents"},
		{"Ctrl+T", "hide or show the task list"},
		{"Ctrl+G", "edit the prompt in your editor (also Ctrl+X Ctrl+E)"},
		{"Ctrl+S", "put the prompt aside, and bring it back"},
		{"Ctrl+V", "paste the picture on the clipboard (or drag a picture's file in)"},
		{"Ctrl+L", "draw the bottom of the screen again"},
		{"Ctrl+Z", "suspend gnosh; fg brings it back"},
		{"/config editorMode=vim", "vim's keys in the box: Esc for NORMAL mode, i to type, v to select"},
		{"?", "in an empty box: this list"},
	}
	wide := 0
	for _, r := range rows {
		wide = max(wide, s.m.Width(r[0]))
	}
	own := s.ownKeys()
	for _, r := range own {
		wide = max(wide, s.m.Width(r[0]))
	}
	s.begin(blkText)
	s.put(th.ToolName.Render("Keys"))
	list := func(rows [][2]string) {
		for _, r := range rows {
			key := r[0] + strings.Repeat(" ", wide-s.m.Width(r[0]))
			rest := strings.Repeat(" ", wide+4)
			if s.cols < wide+24 {
				rest = "    "
			}
			s.put(wrap(s.m, th.Quiet.Render(r[1]), s.cols, "  "+key+"  ", rest)...)
		}
	}
	list(rows)
	if len(own) > 0 {
		// The person's own keys, as Claude Code's hints follow its
		// keybindings.json (2.1.329): what each does here, in the words
		// of the list above.
		s.put(wrap(s.m, th.ToolName.Render("Yours, from "+showPath(s.cwd, s.home, s.keys.path)), s.cols, "", "  ")...)
		list(own)
	}
	var missing []string
	if _, ok := s.c.(sheller); !ok {
		missing = append(missing, "shell lines that begin with !")
	}
	if _, ok := s.c.(backgrounder); !ok {
		missing = append(missing, "moving a running command to the background")
	}
	if _, ok := s.c.(historian); !ok {
		missing = append(missing, "the prompts of earlier sessions in the history")
	}
	if _, ok := s.c.(rewinder); !ok {
		missing = append(missing, "going back to an earlier prompt (Esc Esc in an empty box)")
	}
	if len(missing) > 0 {
		s.put(wrap(s.m, th.Quiet.Render(fmt.Sprintf("Not joined to this build yet: %s.", strings.Join(missing, "; "))), s.cols, "", "  ")...)
	}
}

// loadThen reads a picker's list away from the loop, so that a long
// session's file, or a folder of many sessions, does not hold the screen
// still, and then shows it. What the person types meanwhile is held for
// the list, as had it come at once: Down and Enter pick in it, Esc closes
// it. A question or a turn that comes first lets the list go (listCheck),
// and so does Ctrl+C. again says how to ask for the list again.
func loadThen[T any](s *session, again string, read func() T, show func(T)) {
	if s.listing {
		return
	}
	s.listing, s.listAgain = true, again
	gen := s.listGen
	s.host.spawn(func() func() {
		got := read()
		return func() {
			if !s.listing || gen != s.listGen {
				// Let go meanwhile.
				return
			}
			s.listing = false
			held := s.heldInput
			s.heldInput = nil
			show(got)
			if s.q == nil {
				// No list came up (there was nothing in it): what was
				// typed for it is not for the box, where Enter sends.
				return
			}
			list := s.q
			for _, ev := range held {
				if s.q != list {
					// The list was answered, and what that opened (the
					// rewind's choice of what to put back) was not seen
					// when the rest was typed: Esc Esc Enter Enter must
					// not put the files back unseen (the rewind review of
					// 2026-11-19). The rest is let go.
					break
				}
				s.refeed(ev)
			}
		}
	})
}

// listCheck lets a list that is being read go when a question or a turn
// came first: what the person types next is for what is on the screen.
func (s *session) listCheck() {
	switch {
	case !s.listing:
	case s.q != nil:
		s.dropList("A question came first: " + s.listAgain + " opens the list again.")
	case s.busy:
		s.dropList("A turn began first: " + s.listAgain + " opens the list again.")
	}
}

// dropList lets the list that is being read go, with what was typed for
// it, and says why.
func (s *session) dropList(why string) {
	s.listing, s.heldInput = false, nil
	s.listGen++
	s.flash(why)
}

// rewindNote is what the menu says of the files a rewind can and cannot
// put back.
const rewindNote = "Each goes back as it was before that prompt, and what it holds now is kept first. Files only shell commands or sub-agents changed are not followed."

// rewind opens the list of the person's prompts, the newest first, to
// take the conversation back to before one of them. It is what Esc Esc
// in an empty box and /rewind do.
func (s *session) rewind() {
	rw, ok := s.c.(rewinder)
	switch {
	case !ok:
		s.flash("Going back to an earlier prompt (Esc Esc) is not joined to this build yet.")
		return
	case s.busy:
		s.flash("A turn is running. Stop it first (Esc), then go back to an earlier prompt.")
		return
	case s.q != nil:
		return
	}
	loadThen(s, "Esc Esc", rw.RewindPoints, s.rewindMenu)
}

// rewindMenu opens the list of the person's prompts that rewind read.
func (s *session) rewindMenu(points []app.RewindPoint) {
	if len(points) == 0 {
		s.flash("There is no prompt of yours in this conversation to go back to.")
		return
	}
	var opts []menu.Option
	for i := len(points) - 1; i >= 0; i-- {
		opts = append(opts, menu.Option{Label: oneLine(points[i].Text), Description: points[i].Time.Local().Format("15:04")})
	}
	opts = append(opts, menu.Option{Label: "Never mind"})
	pick := &question{q: agent.Question{Kind: "rewind", Title: "Go back to before which prompt?"}}
	pick.ui = menu.NewSelect(menu.SelectOptions{Title: pick.q.Title, Options: opts, CancelHint: "never mind", Theme: s.th.Menu, Metrics: s.m})
	pick.local = func(res menu.Result) {
		if res.Cancelled || res.Index < 0 || res.Index >= len(points) {
			return
		}
		// The list is the newest first; the command counts from the
		// oldest.
		number := len(points) - res.Index
		point := points[number-1]
		// The choices, as Claude Code offers them: the conversation and the
		// files, the conversation alone, or the files alone (the ones about
		// files only where one of the files followed changed since); and a
		// summary of what followed the prompt, gone back from, or of what
		// came before it (not for the first prompt).
		// (A long prompt, pasted, would take the rows the files and the
		// choices need.)
		sure := &question{q: agent.Question{Kind: "rewind", Title: "Go back to before: " + fit(s.m, oneLine(point.Text), rewindTitleMost)}}
		var opts []menu.Option
		var commands []string
		body := []string{"No file this session's Write and Edit changed has changed since then: files only shell commands or sub-agents changed are not followed."}
		if point.FilesWhy != "" {
			body = []string{"No file can be put back: " + oneLine(point.FilesWhy) + "."}
		}
		if n := len(point.Files); n > 0 {
			shown := point.Files
			if len(shown) > 5 {
				shown = shown[:5]
			}
			names := make([]string, len(shown))
			for i, f := range shown {
				// (A name the model gave a file may hold anything: the
				// menu keeps styles and links.)
				names[i] = pathShown(f)
			}
			more := ""
			if n > len(shown) {
				more = fmt.Sprintf(" and %d more", n-len(shown))
			}
			body = []string{fmt.Sprintf("%s changed since then: %s%s. %s", count(n, "file", "files"), strings.Join(names, ", "), more, rewindNote)}
			opts = append(opts, menu.Option{Label: "Go back, and put the files back", Description: "the conversation and the files as they were"})
			commands = append(commands, fmt.Sprintf("/rewind %d all", number))
		}
		opts = append(opts, menu.Option{Label: "Go back, the conversation only", Description: "the prompt is put in the box; the files stay as they are"})
		commands = append(commands, fmt.Sprintf("/rewind %d", number))
		if len(point.Files) > 0 {
			opts = append(opts, menu.Option{Label: "Put the files back only", Description: "the conversation stays as it is"})
			commands = append(commands, fmt.Sprintf("/rewind %d files", number))
		}
		if point.InView {
			// (Of a prompt the model still reads: one a summary or a slide
			// took out has nothing before or after it to summarize.)
			// (Each takes, as Claude Code's do, what the summary is to
			// keep, typed on its row after Tab.)
			opts = append(opts, menu.Option{Label: "Summarize from here", Description: "go back to before it, keeping a summary of what followed; the files stay",
				Text: menu.TextOptional, Placeholder: "what the summary is to keep, if anything in particular"})
			commands = append(commands, fmt.Sprintf("/rewind %d summarize", number))
			if number > 1 {
				opts = append(opts, menu.Option{Label: "Summarize up to here", Description: "what came before it becomes a summary; it and what followed stay",
					Text: menu.TextOptional, Placeholder: "what the summary is to keep, if anything in particular"})
				commands = append(commands, fmt.Sprintf("/rewind %d summarize-before", number))
			}
		}
		opts = append(opts, menu.Option{Label: "Never mind"})
		sure.ui = menu.NewSelect(menu.SelectOptions{
			Title: sure.q.Title, Body: body, Options: opts,
			CancelHint: "never mind", Theme: s.th.Menu, Metrics: s.m,
		})
		sure.local = func(res menu.Result) {
			if res.Cancelled || res.Index < 0 || res.Index >= len(commands) {
				return
			}
			line := commands[res.Index]
			if guide := strings.Join(strings.Fields(res.Text), " "); guide != "" && strings.Contains(line, " summarize") {
				line += " " + guide
			}
			s.run(line)
		}
		s.openLocal(sure)
	}
	s.openLocal(pick)
}

// rewindTitleMost is how many cells of the prompt a rewind's confirmation
// shows.
const rewindTitleMost = 100

// pathShown is a path for a menu: on one line, with the marks that turn
// the order text is drawn in written out, so that a name cannot pass for
// another.
func pathShown(p string) string {
	var b strings.Builder
	for _, r := range oneLine(p) {
		if bidiMark(r) {
			fmt.Fprintf(&b, "\\u%04X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// openLocal puts a menu of the interface's own on the screen. The person
// asked for it with a key, so it takes keys at once.
func (s *session) openLocal(qs *question) {
	s.open(qs)
	qs.guarded = false
}

// withdrawLast takes the prompt the person queued last back into the
// box, and leaves the ones before it in the queue. It is Alt+Up (and
// Ctrl+X Up): Up alone takes all of them back, this takes one, and
// pressed again the one before it.
func (s *session) withdrawLast() {
	w, ok := s.c.(withdrawer)
	if !ok {
		s.flash("Taking one queued prompt back (Alt+Up) is not joined to this build yet. Up takes all of them back.")
		return
	}
	queued := s.c.Queued()
	for i := len(queued) - 1; i >= 0; i-- {
		if queued[i].Prompt.Hidden {
			continue
		}
		p, took := w.Withdraw(queued[i].ID)
		if !took {
			// It went out to the model in the meantime.
			continue
		}
		d := editor.Draft{Text: p.Text, Pastes: pastesOf(p.Meta)}
		s.attach = append(s.attach, p.Attachments...)
		for j, x := range s.sending {
			if x.id == queued[i].ID {
				s.sending = append(s.sending[:j], s.sending[j+1:]...)
				break
			}
		}
		kept := s.queue[:0:0]
		for _, q := range s.queue {
			if q.ID != queued[i].ID {
				kept = append(kept, q)
			}
		}
		s.queue = kept
		s.restore(d)
		s.dirty = true
		return
	}
	s.flash("No prompt of yours waits in the queue.")
}

// sendNow has the model read what the person queued without waiting for
// the round that is running to end by itself (Ctrl+Enter, and Ctrl+X
// Ctrl+S). The commands that are running are moved to the background,
// where they go on, and the model reads the queue at the end of that
// round, in the same turn. When something is in the way that cannot be
// moved (the model is writing, or another tool runs) the turn is stopped,
// and what was queued starts the next one at once.
func (s *session) sendNow() {
	if !s.busy {
		return
	}
	waits := false
	for _, q := range s.c.Queued() {
		waits = waits || !q.Prompt.Hidden
	}
	if !waits {
		s.flash("Nothing of yours waits to be sent.")
		return
	}
	moved, blocked := 0, 0
	b, movable := s.c.(backgrounder)
	for _, c := range s.calls {
		switch {
		case c.started && movable && b.Background(c.id, c.name):
			moved++
		default:
			blocked++
		}
	}
	if moved > 0 && blocked == 0 {
		s.flash(fmt.Sprintf("Moved to the background: %d running command(s), which go on there. The model reads what you sent next.", moved))
		return
	}
	s.c.Interrupt()
	s.flash("The turn was stopped so that what you sent goes out now.")
}

// resume opens the list of the folder's other sessions, to open one in
// place of the session that is open. It is what /resume does with
// nothing after it.
func (s *session) resume() {
	rs, ok := s.c.(resumer)
	switch {
	case !ok:
		return
	case s.busy:
		s.flash("A turn is running. Stop it first (Esc), then open another session.")
		return
	case s.q != nil:
		return
	}
	loadThen(s, "/resume", rs.ResumeChoices, s.resumeMenu)
}

// resumeMenu opens the list of the folder's other sessions that resume
// read.
func (s *session) resumeMenu(choices []app.SessionChoice) {
	if len(choices) == 0 {
		s.flash("There is no other session in this folder to open.")
		return
	}
	var opts []menu.Option
	for _, ch := range choices {
		opts = append(opts, menu.Option{Label: oneLine(ch.Title), Description: ch.Modified.Local().Format("2006-01-02 15:04") + ", " + ch.ID[:min(8, len(ch.ID))]})
	}
	opts = append(opts, menu.Option{Label: "Never mind"})
	pick := &question{q: agent.Question{Kind: "resume", Title: "Open which session? This one is kept."}}
	pick.ui = menu.NewSelect(menu.SelectOptions{Title: pick.q.Title, Options: opts, CancelHint: "never mind", Theme: s.th.Menu, Metrics: s.m})
	pick.local = func(res menu.Result) {
		if res.Cancelled || res.Index < 0 || res.Index >= len(choices) {
			return
		}
		s.run("/resume " + choices[res.Index].ID)
	}
	s.openLocal(pick)
}
