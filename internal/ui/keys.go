package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/term"
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
// interactive.md, 2.8).
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
			// The transcript view takes every key while it is open.
			if _, paste := ev.(term.Paste); !paste {
				s.viewInput(ev)
			}
			return
		}
		s.typedOrMouse(ev)
	}
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
			s.flash("The task list is out of sight. Ctrl+T shows it again.")
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
	p := agent.Prompt{Text: d.Text, Attachments: s.attach}
	if len(d.Pastes) > 0 {
		p.Meta = map[string]any{metaPastes: d.Pastes}
	}
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
// that act at once.
func (s *session) command(d editor.Draft) {
	line := strings.TrimSpace(d.Text)
	word, _, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	for _, own := range ownCommands {
		if own.words[strings.ToLower(line)] && own.take(s, line) {
			return
		}
	}
	if s.busy && !atOnce[strings.ToLower(word)] {
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
		s.cmdq = append(s.cmdq, line)
		s.queued()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cmdRun, s.cmdStop = line, cancel
	c := s.c
	s.host.spawn(func() func() {
		res, err := c.Command(ctx, line)
		return func() {
			cancel()
			s.ran(line, res, err)
		}
	})
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
	s.cmdRun, s.cmdStop = "", nil
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
		th := &s.th
		shown, held := shownCommand(line)
		s.begin(blkPrompt)
		s.put(wrap(s.m, th.PromptText.Render(oneLine(shown)), s.cols, th.Prompt.Render(th.PromptMark), strings.Repeat(" ", s.m.Width(th.PromptMark)))...)
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
		spans, _ := p.Meta[metaPastes].([]editor.Span)
		add(p.Text, spans)
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
			s.flash("Permission mode: " + modeNames[next] + ". Shift+Tab goes on to the next.")
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
	if len(lines) > 1 {
		// Several answers (a form's): the title stands on its own line,
		// and each answer under it.
		s.put(wrap(s.m, title, s.cols, th.Ask.Render(th.AskMark)+" ", "  ")...)
		for _, l := range lines {
			s.put(wrap(s.m, th.Answer.Render(l), s.cols, "  ", "  ")...)
		}
		return
	}
	for i, l := range lines {
		first := "  "
		if i == 0 {
			first = th.Ask.Render(th.AskMark) + " " + title + " "
		}
		s.put(wrap(s.m, th.Answer.Render(l), s.cols, first, "  ")...)
	}
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
		{"Ctrl+L", "draw the bottom of the screen again"},
		{"Ctrl+Z", "suspend gnosh; fg brings it back"},
		{"/config editorMode=vim", "vim's keys in the box: Esc for NORMAL mode, i to type, v to select"},
		{"?", "in an empty box: this list"},
	}
	wide := 0
	for _, r := range rows {
		wide = max(wide, s.m.Width(r[0]))
	}
	s.begin(blkText)
	s.put(th.ToolName.Render("Keys"))
	for _, r := range rows {
		key := r[0] + strings.Repeat(" ", wide-s.m.Width(r[0]))
		rest := strings.Repeat(" ", wide+4)
		if s.cols < wide+24 {
			rest = "    "
		}
		s.put(wrap(s.m, th.Quiet.Render(r[1]), s.cols, "  "+key+"  ", rest)...)
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
			for _, ev := range held {
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
const rewindNote = "What a file holds when it is put back is kept first; what shell commands changed in other files is not put back."

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
		// files, the conversation alone, or the files alone; the ones about
		// files only where Write or Edit changed one since.
		sure := &question{q: agent.Question{Kind: "rewind", Title: "Go back to before: " + oneLine(point.Text)}}
		var opts []menu.Option
		var commands []string
		body := []string{"Write and Edit changed no file since then. " + rewindNote}
		if n := len(point.Files); n > 0 {
			shown := point.Files
			if len(shown) > 5 {
				shown = shown[:5]
			}
			names := make([]string, len(shown))
			for i, f := range shown {
				names[i] = filepath.Base(f)
			}
			more := ""
			if n > len(shown) {
				more = fmt.Sprintf(" and %d more", n-len(shown))
			}
			body = []string{fmt.Sprintf("Write and Edit changed %s since then: %s%s. %s", count(n, "file", "files"), strings.Join(names, ", "), more, rewindNote)}
			opts = append(opts, menu.Option{Label: "Go back, and put the files back", Description: "the conversation and the files as they were"})
			commands = append(commands, fmt.Sprintf("/rewind %d all", number))
		}
		opts = append(opts, menu.Option{Label: "Go back, the conversation only", Description: "the prompt is put in the box; the files stay as they are"})
		commands = append(commands, fmt.Sprintf("/rewind %d", number))
		if len(point.Files) > 0 {
			opts = append(opts, menu.Option{Label: "Put the files back only", Description: "the conversation stays as it is"})
			commands = append(commands, fmt.Sprintf("/rewind %d files", number))
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
			s.run(commands[res.Index])
		}
		s.openLocal(sure)
	}
	s.openLocal(pick)
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
		d := editor.Draft{Text: p.Text}
		if spans, ok := p.Meta[metaPastes].([]editor.Span); ok {
			d.Pastes = spans
		}
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
