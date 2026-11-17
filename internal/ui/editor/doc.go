// Package editor is the box a person types prompts into.
//
// It is a plain value with no terminal behind it. The application that
// owns the terminal gives it each event the terminal reports and draws
// what it is told to:
//
//	ed := editor.New(editor.Options{Theme: editor.DefaultTheme(profile), Complete: complete})
//	for ev := range events {
//		res := ed.Handle(ev)
//		switch res.Action {
//		case editor.ActionSubmit:
//			send(res.Text, res.Pastes, res.Mode)
//			ed.AddHistory(editor.Draft{Text: res.Text, Pastes: res.Pastes})
//		case editor.ActionInterrupt:
//			stopTheTurn()
//		}
//		v := ed.Render(cols)
//		renderer.SetLive(v.Lines, v.CursorRow, v.CursorCol)
//	}
//
// Handle never sends, stops or exits anything. It says what a key did and
// what the application is asked to do, and a key that means nothing to
// the box comes back with Handled false, so the application can give it a
// meaning of its own (Shift+Tab, Ctrl+L, Ctrl+O, Ctrl+B, Ctrl+T, Ctrl+Z).
// Render gives rows that are already styled and cut to the width, and the
// cursor's row and column, which is what the renderer's live region takes.
// Nothing here reads a clock (one is passed in for telling a second press
// from a first), starts a goroutine, or touches the terminal, so every
// behaviour can be tested with a list of events; the package keyscript
// writes such lists the way a person would say them.
//
// # The text
//
// The text is lines of UTF-8. The cursor moves, and the delete keys
// delete, by what a reader takes for one character: a letter with its
// accents, an emoji however many code points it is made of, a Chinese
// character. A tab is kept as a tab and drawn to the next tab stop.
// Control characters and escape sequences that arrive in a paste are kept
// in the text and drawn as stand-ins ("^[" for Esc), so nothing typed or
// pasted can reach the terminal as a control sequence; a character with
// no width of its own (a zero-width space, a mark with nothing to sit on)
// is drawn as its code point ("<U+200B>"), so that nothing in the box is
// invisible.
//
// A large paste (more than ten lines, or more than 800 characters) is
// shown collapsed, as "[pasted 120 lines]". It is one step for the cursor
// and one delete, the word and line keys take it whole, and it is sent in
// full. Tab with the cursor touching it turns it back into its text. Text
// and Result.Text always hold the full text; Draft and Result.Pastes also
// say which parts of it are collapsed pastes, so the application can tell
// the model what was pasted, and can put a prompt back in the box (from
// the queue, from the history, after a hook refused it) the way it was.
//
// # Modes
//
// The first character decides what kind of line the box holds: "/" makes
// it a command, "!" a shell line, anything else a prompt. That character
// is drawn as the prefix of the box, in place of the prompt mark, and the
// cursor does not go in front of it; deleting backwards at the start of
// the line takes it away and leaves the mode. Text and the submitted
// Result.Text keep it as their first character, and Result.Mode says
// which it was.
//
// # Completion
//
// The application passes one function, Options.Complete, which is given
// the text and the cursor and returns candidates: commands, file paths,
// earlier shell lines, whatever fits there. The list opens by itself
// while a word that begins with "/" or "@" is typed and after the name of
// a command, for what the command takes; anywhere else it opens on Tab.
// It narrows as letters are typed. An exact name always wins: when what
// was typed is a candidate in full, that candidate is the one chosen, and
// Enter sends the line instead of completing it. And what was typed is
// never swapped unasked for something that only resembles it: Enter takes
// a candidate that begins with what was typed, or one chosen with the
// arrows, and otherwise sends the line as it stands. A candidate marked
// Run (a command that needs nothing more) is sent as soon as Enter takes
// it.
//
// # Keys
//
// Where each binding comes from: "cc" is spec/cc/interactive.md with its
// section, "ui" is spec/gnosh/harness-ui.md with its item, "brief" is the
// build brief for this package, "emacs" is the convention of emacs and of
// readline where the specs are silent, and "own" is a choice made here.
//
//	Sending and new lines
//	Enter                          send (cc 2.1); nothing, when the box holds nothing worth sending (own)
//	Ctrl+M                         as Enter, for terminals that tell them apart (own)
//	Ctrl+Enter                     send, with Result.Now set (cc 2.8, there P2)
//	Shift+Enter, Alt+Enter, Ctrl+J new line (cc 2.1)
//	\ then Enter                   new line; a shell line keeps the backslash (cc 2.1; the shell part own)
//
//	Moving
//	Left, Right, Ctrl+F            one character (emacs; Ctrl+B is left to the application: cc 3, ui UI-31)
//	Alt+B, Alt+F                   one word: a run of letters and digits (cc 2.3)
//	Alt+Left/Right, Ctrl+Left/Right the same (own; what terminals send for these keys)
//	Up, Down, Ctrl+P, Ctrl+N       one row, keeping to the column; from the first row or the last, the history (cc 2.4)
//	PgUp, PgDn                     as many rows as the box shows (own)
//	Home, End, Ctrl+A, Ctrl+E      start and end of the line, wrapped or not (cc 2.3; Home and End brief)
//	Ctrl+Home, Ctrl+End, Alt+<, Alt+> start and end of the text (emacs)
//
//	Deleting
//	Backspace, Ctrl+H              the character before the cursor (emacs)
//	Delete                         the character after it (emacs)
//	Ctrl+D                         with text: as Delete (cc 2.3); empty: see below
//	Alt+Backspace, Ctrl+Backspace  back to the start of the word, kept (emacs)
//	Ctrl+W                         back to the previous space, kept: a whole path at once (cc 2.3)
//	Alt+D, Alt+Delete, Ctrl+Delete to the end of the word, kept (cc 2.3; the Delete forms own)
//	Ctrl+K                         to the end of the line, kept; at the end, the line break (cc 2.3; the line break emacs)
//	Ctrl+U                         to the start of the line, kept; at the start, the line above, so repeating clears upwards (cc 2.3)
//	Ctrl+Y                         put back what was deleted last (cc 2.3)
//	Alt+Y                          straight after Ctrl+Y: swap it for the deletion before (cc 2.3)
//	Ctrl+_                         undo; also Ctrl+/ and Ctrl+Shift+-, which send the same (cc 2.3)
//	Alt+/                          redo (own; fish's key)
//
//	History
//	Up on the first row            the entry before; the draft being typed is kept (cc 2.4)
//	Down on the last row           the entry after, and after the newest, the draft (cc 2.4)
//	Up on an empty box             the queued prompts, when SetQueued(true): ActionQueue (ui UI-31)
//	Ctrl+R                         search the history as letters are typed (cc 2.4)
//	  Ctrl+R, Up                   the next older match (cc 2.4; Up own)
//	  Ctrl+S, Down                 the next newer match (own)
//	  Tab, Esc                     keep the match, for editing (cc 2.4)
//	  Enter                        keep the match and send it; nothing, when nothing matched (cc 2.4; the second half own)
//	  Ctrl+C, Ctrl+G               give up; the box is as it was (cc 2.4; Ctrl+G emacs)
//	  Backspace                    one character off the search; on an empty search, give up (cc 2.4)
//	  Ctrl+U                       empty the search (own)
//	  a key that moves or edits    keep the match and act on it (emacs)
//	Ctrl+S                         put the text aside and empty the box; on an empty box, bring it back (cc 2.3)
//
//	Completion
//	Tab                            ask for candidates; a lone one is taken at once (cc 2.6, brief)
//	Tab on a collapsed paste       turn it back into its text (own)
//	  Up, Down, Ctrl+P, Ctrl+N     choose, going round (brief)
//	  PgUp, PgDn                   choose a screenful away (own)
//	  Tab                          take the candidate chosen (cc 3); if it is in the box already, letter for letter, go on to the next (brief)
//	  Enter                        take the candidate chosen (cc 2.5), and send it if it runs; send instead, if it is typed in full already
//	                               or does not begin with what was typed and was not chosen with the arrows (ui UI-30)
//	  Esc                          close the list; it stays closed for that word (brief)
//
//	Asking the application
//	Esc                            while busy: ActionInterrupt (ui UI-31)
//	Esc                            on a line that holds only its "!" or "/": leave the mode (cc 2.6)
//	Esc, Esc                       with text: the first warns, the second clears, and Up brings the text back (ui UI-31)
//	Esc, Esc                       empty: ActionRewind (ui UI-31)
//	Ctrl+C                         while busy: ActionInterrupt (cc 3)
//	Ctrl+C, Ctrl+C                 the first clears and warns, the second asks for ActionExit (cc 3)
//	Ctrl+D, Ctrl+D                 empty: the first warns, the second asks for ActionExit (cc 3)
//	?                              on an empty box: ActionHelp (cc 2.2)
//	Ctrl+G                         ActionEditor (cc 2.3)
//
// The second press of Esc, Ctrl+C or Ctrl+D counts when it comes within
// DoublePress (0.8 s) of the first and no other key came between.
//
// # Vim's keys
//
// SetVim (Claude Code's editorMode "vim") gives the box vim's modes, and
// the status line is told the mode with Vim. INSERT mode has the keys
// above, but Esc and Ctrl+[ go to NORMAL mode, and a pair set with
// SetVimRemaps (vimInsertModeRemaps: "jj") typed within a second does the
// same and leaves neither character. NORMAL mode, with counts throughout
// (3w, d2w, 2dd):
//
//	h j k l, Space, Backspace      move; j and k from the first row or the last walk the history
//	w W b B e E                    words (letters, digits and _, or other characters) and WORDs (all but blanks)
//	0 ^ $ gg G                     the line's start, first character, end; the first and last lines (a count: that line)
//	f F t T, then a character      to it on the line, or to just before it; ; again, , the other way
//	%                              the bracket that matches the next one on the line
//	d c y > <, then a motion       delete, change, yank, indent, dedent; doubled (dd, >>) for whole lines
//	iw aw iW aW i" a" i' a' i( a( i[ a[ i{ a{ i< a<  text objects after d c y, and in VISUAL mode
//	x X r D C s S Y p P J ~ u .    as vim's: . makes the last change again
//	i I a A o O                    INSERT mode, before, at the first character, after, at the end, on a new line
//	v V                            VISUAL mode by characters, by lines
//	/                              the history search (Ctrl+R)
//	Esc                            drops a command half typed; else as Esc above: while busy, ActionInterrupt
//
// In VISUAL mode a motion moves the selection's end, o swaps its ends,
// and d x y c s p P r ~ u U > < J act on it. Enter sends from any mode,
// and a box that was sent or cleared starts again in INSERT mode. Each
// change is one step of undo, with the INSERT session it began; "."
// types again the text of an INSERT session, from where the last move in
// it left off, as vim's does.
//
// # What the specs left open, and what was chosen
//
//   - A line is the text between two line breaks, however many rows it is
//     wrapped onto, for Home, End, Ctrl+A, Ctrl+E, Ctrl+K and Ctrl+U:
//     the readline meaning. Up and Down go by rows.
//   - spec/cc collapses a paste of more than three lines; harness-ui.md
//     says a long paste is one of more than ten. Ten lines it is, with
//     spec/cc's 800 characters for a paste that is long without being
//     many lines. Both are options.
//   - Ctrl+C's second press has the same time limit as Ctrl+D's, which
//     spec/cc gives only for Ctrl+D: nothing leaves gnosh on a press made
//     long after the first.
//   - Tab does two things the brief names (it moves through the list and
//     it accepts) and one that spec/cc names (it accepts). It accepts,
//     and moves on only when there is nothing to accept.
//   - Shift+Tab is never the box's: it changes the permission mode
//     whatever is open under the box.
//   - Ctrl+X is left to the application, with the chords that begin with
//     it (Ctrl+X Ctrl+E for the editor, Ctrl+X Ctrl+S, Ctrl+X Ctrl+K).
//   - The editor does not record what it sends in its history. The
//     application adds it with AddHistory, because it may hold the
//     prompt in its queue first, and the history must not offer a second
//     copy of a prompt that is still waiting there (harness-agent-loop.md
//     LOOP-7).
//   - Walking the history, clearing the box, putting it aside and loading
//     text are steps of the undo history like any edit, so that no key
//     can put typed text out of reach. Sending is what begins a new undo
//     history.
//   - Removing invisible characters from a prompt on Enter (spec/cc 2.7)
//     is not done here but by the application (ui/invisible.go), which
//     has what it needs to clean a prompt and put it back (Result.Text,
//     SetDraft). Until then the box shows such characters.
package editor
