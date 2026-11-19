// Package ui is the interactive session a person sees and types into:
// gnosh's full front end.
//
// It draws in the terminal's own scrollback. There is no alternate screen
// and no mouse capture, so scrolling back, selecting and copying are the
// terminal's own and work as they do for any command's output. The screen
// has two parts, and the whole design is about keeping them apart. (The
// side panel, panel.go, is the one exception, and only when the person
// turns it on: then the session is drawn on the whole screen, beside a
// panel of the files it changed and its deliverables.)
//
// # What is committed, and when
//
// Everything that is finished is committed: written once, above, through
// term.Renderer.Commit, and never touched again. A thing is committed at
// the moment it can no longer change:
//
//	the header             once, when the session starts: the wordmark, the
//	                       version, the model, the folder
//	a prompt of the        when it goes out to the model (PromptEvent), not
//	person's               when Enter is pressed: until then it is "queued"
//	the model's answer     line by line, as the Markdown renderer declares
//	                       each line final; the rest when the reply ends
//	the model's reasoning  one line when it is over ("thought for 12s"); with
//	                       detail on, all of it, line by line
//	a tool call            when it ends, as one block: what it did, as the
//	                       model wrote it, and what came of it
//	a question             when it is answered or taken back: the question
//	                       and the answer, on a line
//	a notice, a warning,   when it is given
//	an error, a hook
//	a wait for a failed    the first of a run of them, and each whose reason
//	model server           is new; and a line when the server answers again
//	a deliverable          when the model claims it, and when it delivers
//	                       it: the label, and the address and the file whole
//	a turn's end           when it ends: how, how long it took, what it cost
//	a slash command        when it has run: the line typed, and what it said
//	                       (but for what follows "/mem redact", which the
//	                       person wants gone: it is shown nowhere and kept
//	                       in no history)
//
// Blocks stand under each other with no blank line between them: each
// begins with a mark of its own (the prompt's >, a thought's ~, a
// command's $). The only empty lines on the screen are the answer's own,
// between its paragraphs.
//
// A tool call's block is headed by what the call does, not by a name and a
// spinner: a command line behind "$" (behind the machine's name, when it
// runs on another), a file's path for a read or an edit, the pattern for a
// search, and for a Gno call its code, whole. Under the heading: an edit's
// diff (internal/diff); the end of a command's output, with a count of the
// lines left out and the exit code; a count for a read or a search; the
// receipt of a Gno call; the reason for a call that was refused or failed.
// The hooks that ran on the call stand under it. Output, code and diffs
// carry nothing of gnosh's own but an indentation, so what is selected in
// the terminal is the text.
//
// A call of the Deliver tool that went well is not shown as a call: its
// block is the deliverable itself (deliver.go), so that the thing the
// person asked for stands apart from the calls around it. The label is in
// one plain colour behind a small mark, green for a file and gold for an
// address; the address and the path are whole, each on a row of its own
// when they do not all fit on one; and the address is a real link where
// the terminal knows links.
//
// Lines are laid out for the width the terminal has when they are
// committed. A terminal draws one line of text per row, and the renderer
// relies on that, so prose is wrapped by gnosh and not by the terminal.
//
// # The live region
//
// Below the committed text are a few rows that are redrawn in place
// (term.Renderer.SetLive). From its first row to its last:
//
//	what is still being written: the lines of the answer that may still
//	  change, the last rows of the reasoning while it is folded, and each
//	  call that is being written or is running, with the last rows of its
//	  output
//	one line that says what gnosh is doing or waiting for, while it is busy
//	the prompts and commands that wait for their turn
//	the question, when one is put; otherwise the prompt box
//	the session's task list, unless Ctrl+T has put it out of sight
//	the status line
//	a short notice that a key called for, for a few seconds, wrapped
//
// The line that says what gnosh is doing is always the truth as the
// session reports it, in plain words with the time that state has lasted:
// waiting for the model to start its reply (and about how many tokens it
// has to read), the model is thinking, is writing, is writing a call;
// running a command, by its own words, or several tools, by their names;
// waiting for the person; waiting so many seconds to try a failed request
// again; writing a summary; stopping. With colour the line moves while
// gnosh works (Jae, 14 November 2026: "i don't like that there's no
// spinner now"), in two parts (fall.go). Before the words stands one mark,
// a spark that swells and dwindles in its one cell. The words themselves
// stand in a grey, and colour falls through them to the right as if
// gravity ran sideways: a drop of it starts at the first letter, slowly,
// gathers speed all the way along the line and leaves at its end, its
// tail stretching as it goes; the letters stay the letters. The fall says
// how long the model has been in one thought. At first, and whenever the
// model is not thinking, one pale drop at a time crosses the words. From
// three seconds of one unbroken thought on it deepens: first the drops
// take colours of their own, then more of them fall under a stronger
// pull, until at fifteen seconds three run through the line at once in
// the blue-green, the purple, the hot pink and the gold, with glints
// among them, and the line's words are "is deep in thought". When the
// thought is over the fall grows calm again within a second and a half.
// (Jae, 18 November 2026: "start with more boring bland even colors first
// ... dropping rightward, as if accelerating to the right, like gravity
// goes sideways, and when 'deep in thought', more things drop faster with
// higher acceleration", and then: "the first part is like claude, i think
// a single letter, and then the second part is the text that starts
// non-white and the whole text is animated. the thing that moves rightward
// is a different color of the text, but the text is always the same".)
// With no colour, and while a question waits for the person, the plain
// mark stands still before plain words.
// It is there only while the session is busy: there is never a "working"
// line with nothing in flight, and should the event that says the session
// went idle ever be missed, the session's own word is asked. When it is
// idle the status line says so. While a question waits the line says that
// gnosh is waiting for the answer, and for how long.
//
// The status line is one row that never wraps: "idle" when nothing runs;
// the permission mode, always and in words ("manual mode", "accept edits",
// "plan mode", "don't ask", and "[BYPASS PERMISSIONS]" in capitals and
// brackets); the model and its thinking level; how full its window is
// (the figure follows the session, so it drops after a pop); "/loop on";
// the folder and its git branch; how far the task list has come ("tasks
// 2/5", and the task in hand when the list is out of sight); and what
// the session tells of beyond that, when it does: the frame, commands in
// the background, and how many deliverables are delivered and how many
// pending ("2 delivered · 1 pending"). In a narrow terminal its parts go
// one by one, the least needed first and the permission mode last.
//
// The task list is the session's own (app.TasksEvent: what mem holds as
// the frame's tasks). It stands under the prompt box, a row a task:
// "[x]" for one that is done, "[>]" for the one in hand, in the words it
// has while it is being done, "[ ]" for one still to do. It takes eight
// rows at most; a longer list shows the tasks around the one in hand and
// counts the rest. It is never written into the scrollback.
//
// The region is bounded: it takes at most one row less than the terminal
// has (and never more than sixty), the question and the prompt box scroll
// inside their share, and what is still being written is cut from its top
// when room runs out, since those are the rows that reach the scrollback
// anyway. A command that floods shows its last three rows.
//
// Something moves from the live region to the scrollback in one step: the
// lines are committed and the next live region no longer holds them, in
// the same frame. Nothing is in both, and nothing is in neither.
//
// # Typing while the model works
//
// The prompt box always has the keys, unless a question is on the screen.
// Enter during a turn hands the prompt to the session, which queues it and
// gives it to the model between two rounds; it is listed above the box
// until it goes out. Up in an empty box takes everything that waits back
// into the box for editing, and Enter sends it again as one prompt. A
// slash command typed during a turn waits for the session to go idle,
// unless it is one of the few that act at once (/status, /model,
// /permissions, /think and the like). A prompt that comes back (a hook
// refused it, the session was stopped while it was being checked) is put
// in the box again, in front of anything typed since, with the reason in
// the scrollback.
//
// When a question appears, what is in the box stays in the box, in sight
// under the question, and has the keys back when the question is over. A
// question takes no key until it has been on the screen for 0.4 s and the
// keyboard has been quiet for 0.6 s: keys typed before it could be read
// were meant for the box, and go there.
//
// # Keys
//
// Where each binding comes from: "cc" is spec/cc/interactive.md with its
// section, "ui" is spec/gnosh/harness-ui.md with its item, "perm" is
// spec/cc/permissions.md, "editor" and "menu" are the tables in those
// packages' doc.go (which name their own sources), and "own" is a choice
// made here.
//
//	In every state
//	Ctrl+L                 draw the live region again; the scrollback is left alone (cc 1, 3)
//	Ctrl+Z                 suspend gnosh; fg brings it back. Where no shell could bring it
//	                       back, nothing is stopped and the key says so (cc 3)
//	Ctrl+O                 the transcript view: the whole conversation on the whole screen, every
//	                       thought, call and result, with each reply's time and model (cc 3; ui
//	                       UI-22). Up, Down, PageUp, PageDown, Home, End and the wheel move it;
//	                       { and } go between prompts; Ctrl+E shows what is cut, or cuts it
//	                       again; [ writes it all into the scrollback, where the terminal's own
//	                       search reaches it; v opens it in your editor; q, Esc, Ctrl+C or Ctrl+O
//	                       go back. A question closes it
//	Ctrl+T                 hide or show the session's task list (cc 3)
//	Alt+T                  thinking on or off from now on, kept for later sessions: on and off of
//	                       a model with that switch, the next level of one with a dial (cc 3)
//	Alt+P                  the model picker, what is typed left in the box (cc 3)
//	Ctrl+B                 move the running command to the background (cc 3). In the prompt box
//	                       Ctrl+X b does the same: Ctrl+B is tmux's own key (ui UI-31)
//
//	The prompt box has the keys (idle, or while a turn runs)
//	Enter                  idle: send. Busy: queue the prompt (cc 2.1, 2.8)
//	Ctrl+Enter             busy: send, and have the model read the queue now: the running commands
//	                       move to the background and the queue is read at the end of that round;
//	                       when something runs that cannot move, or the model is writing, the turn
//	                       is stopped and the queue starts the next one (cc 2.8). Ctrl+X Ctrl+S
//	                       does the same for what is queued, where Ctrl+Enter reads as Enter
//	Esc                    busy: stop the turn, at once (ui UI-31, LOOP-5)
//	Esc Esc                idle, text in the box: clear it; Up brings it back (ui UI-31)
//	Esc Esc                idle, empty box: the list of your prompts, to take the conversation back
//	                       to before one of them; that prompt comes back into the box. Where Write
//	                       or Edit changed files since, they can be put back with it, or alone
//	                       (cc 3; sessions 5). /rewind is the same
//	Ctrl+C                 busy: stop the turn. Idle: clear the box, and again to leave (cc 3)
//	Ctrl+D                 empty box, twice: leave (cc 3)
//	Up                     empty box, prompts queued: take the queue back for editing (ui UI-31)
//	Alt+Up, Ctrl+X Up      take back the one prompt queued last, and leave the rest queued. Up
//	                       takes all; this key is beside it, and Alt+Up is a key terminals and
//	                       tmux leave alone. The chord is for a terminal that does not send Alt+Up
//	Up, Down, Ctrl+R       history, as in the editor (cc 2.4)
//	Shift+Tab              the next permission mode: default, acceptEdits, plan, and
//	                       bypassPermissions when the session was started with it in reach (cc 3; perm 2.1)
//	/ at the start         a command; its name is completed from the session's list (cc 2.2; ui UI-30)
//	@                      a file to mention, completed a directory at a time, leaving out what
//	                       the ignore rules leave out (cc 2.5)
//	! at the start         a shell line; the session's controls cannot run one yet, so the line
//	                       comes back into the box with a line that says so (cc 2.6)
//	?                      in an empty box: write the keys into the scrollback (cc 2.2)
//	Ctrl+G, Ctrl+X Ctrl+E  edit the box's text in the person's own editor (cc 2.3)
//	Ctrl+X Ctrl+K          twice within three seconds: stop every sub-agent that runs in the
//	                       background; the background commands go on (cc 3)
//	every other key        as in the editor: see its table
//
//	A question has the keys
//	Enter, 1 to 9, arrows  choose, as in the menu: see its table
//	Tab                    on "No": open a note for the model (cc 4)
//	Esc, Ctrl+C            on a permission question: no, which stops the turn (cc 4; perm 12).
//	                       On any other question: stop the turn; the session takes the question
//	                       back (ui UI-31; TOOL-40)
//	Shift+Tab              on a question about an edit: yes, and accept edits for the rest of the
//	                       session (cc 3); on any other, up one row, as in the menu
//	PgUp, PgDn             scroll what the question is about (menu)
//
//	A question before the session (trust, which model, which session)
//	Esc                    the trust question: no. The others: leave it unanswered, which ends the
//	                       start (spec/gnosh/harness-trust-and-safety.md SAFE-4)
//	Ctrl+C                 leave gnosh (SAFE-4)
//
// # Questions
//
// A permission question shows what it is about and never hides it: the
// command, or the change drawn as a diff to the width of the terminal,
// scrolling inside the question when it is long, then why gnosh asks, then
// the answers the gate offers: yes this once; yes, and don't ask again,
// with the rule; no, which stops the turn; and no with words, which the
// model reads before it carries on. The model's own questions are a form
// with a tab each and a review at the end; a secret is a masked line whose
// answer is kept nowhere here. question.go says how each kind is told
// apart and how the answers go back.
//
// # The terminal
//
// The terminal is opened for a question before the session and again for
// the session, and is put back as it was found on every way out: a
// return, a signal, a panic in the loop or in one of its helpers. Keys
// send no signals here, so a signal comes from outside: SIGINT while
// something runs stops the turn, as Ctrl+C does, and the session goes on;
// SIGINT with nothing running, SIGTERM and SIGHUP end the session in
// order, with the exit code a signal gives (130, 143, 129). A Ctrl+C
// typed into the person's own editor while it has the terminal is the
// editor's, and does not end the session. On the way out the live region
// is erased and a line says how to resume the session; what the session
// still reports after that (its last hooks, a warning) is written as
// plain lines where errors go.
//
// Bracketed paste is turned on only for a terminal that answers
// questions at all, the kitty keyboard level and synchronized output only
// for one that said it has them; an answer that arrives late still
// counts. The interface begins on a line of its own even when the command
// before it left the cursor in the middle of one. Inside tmux, tmux is
// asked whether the terminal it is shown in takes 24-bit colour, because
// the environment cannot say.
//
// On a resize the renderer finds the live region again and erases it, and
// the session lays it out afresh for the new size; what is committed is
// the terminal's to rearrange and is not drawn again (see
// term.Renderer.Resize for the limits of that).
//
// Drawing is batched. What the session reports is put in line by Event,
// which never waits, with pieces of live output joined while they wait;
// the loop takes it in and draws at most once per frame (10 ms unless
// told otherwise). A key is answered at once.
//
// Everything that reaches the screen from the model, a tool or a file is
// made safe first. The answer goes through the Markdown renderer, which
// lets no control character out. Everything else goes through safe.go,
// which shows a control character as a stand-in ("^[" for Esc). Text from
// a tool can never move the cursor, set a title or repaint the screen.
//
// # The look
//
// Theme holds every mark and every style, with the looks of the prompt
// box, the questions, Markdown and diffs inside it, all made from one
// Palette. The default is gnosh's own: prose and output in the terminal's
// own colour, purple for the marks gnosh draws itself, the blue-green of
// gnosh.ai for the prompt mark, the pointer and the working mark,
// hot pink as the one accent on what is chosen, grey for what is not the
// text itself, and nothing filled in, blurred or dimmed into a background.
// The text colours are middle tones that read on a dark and on a light
// background; the blue-green is light, and only marks are drawn in it. The
// wordmark fades
// through mint, purple, pink and gold with full colour, is "GNO" in green
// and the rest from purple to pink with fewer colours, and is plain
// letters with none. Nothing is said by colour alone: every kind of line
// has a mark or a word of its own, so the screen reads the same with no
// colour at all. The purple and the pink are placeholders until Jae picks
// his own; NewTheme makes the whole look from another Palette.
//
// # What is asked of the session beyond app.Controls
//
// A few things the specs list need more than app.Controls has. Each is
// built here so that it switches itself on when the session's controls
// can do their part, and says so plainly when they cannot, which is how
// what stands in for a session in this package's tests is still a
// Controls. The app's own controls can do all of it (app.SessionControls,
// app.FrontendInfo, app.StatusEvent, app.QuestionData, app.QuestionForm):
//
//   - the prompt the command line gave (`gnosh "fix the test"`): sent as
//     the session's first prompt, from app.FrontendInfo's field Prompt.
//   - shell lines ("!"): a method Shell(command string) error.
//   - Ctrl+B, and the part of Ctrl+Enter that moves commands aside: a
//     method Background(callID, tool string) bool.
//   - the prompts of earlier sessions in the history: a method
//     History() []string (oldest first). Without it, Up and Ctrl+R reach
//     this session's prompts only.
//   - Alt+Up: a method Withdraw(id string) (agent.Prompt, bool).
//   - Esc Esc in an empty box and /rewind: a method RewindPoints()
//     []app.RewindPoint for the list; going back is the command
//     /rewind N, whose result says the session's state is to be forgotten
//     (CommandResult.Reset) and gives the prompt back for the box (Draft).
//   - /config with nothing after it, the picker: a method ConfigChoices()
//     []app.ConfigChoice; changing one is the command /config key=value.
//   - /export with nothing after it asks where to: the command
//     /export --clipboard, or /export.
//   - /mcp with nothing after it, the picker: a method MCPChoices()
//     []app.MCPChoice; what is done to a server is the command /mcp
//     reconnect, disable or enable and its name.
//   - /hooks with nothing after it, a read-only browser: a method
//     HookChoices() ([]app.HookChoice, string), the hooks in force and a
//     note on those that are not.
//   - /memory with nothing after it, the picker: a method MemoryFiles()
//     []app.MemoryFile; the one picked opens in the editor (host.editFile).
//   - /model with nothing after it, the picker: a method
//     ModelChoices(ctx) []app.ModelChoice; switching is the command
//     /model provider/model level.
//   - /skills with nothing after it, the picker: a method SkillChoices()
//     []app.SkillChoice; how far one is seen is the command /skills
//     <name> on, name-only, user-invocable-only or off.
//   - /permissions with nothing after it, the picker: a method
//     PermissionRules() (mode, []app.PermissionRule); adding a rule is the
//     command /permissions allow, ask or deny and the rule, and taking one
//     out /permissions remove and the rule.
//   - Ctrl+O, the transcript view: a method Transcript()
//     []app.TranscriptEntry.
//   - /resume with nothing after it: a method ResumeChoices()
//     []app.SessionChoice for the list; opening is /resume and an id.
//   - /deliverables, which is also /artifacts, and the counts on the
//     status line: a method Deliverables() []app.Deliverable for the list
//     as it stands, and an *app.DeliverablesEvent when it changes. The
//     interface draws the list itself, at once and also while a turn
//     runs, when the session has the command.
//   - the frame and the count of background commands on the status
//     line: an event with a method StatusFacts() map[string]string
//     (events.go names the facts).
//   - the countdown on a question the session takes back by itself: a
//     question whose Data has a method Deadline() time.Time.
//   - the model's own questions: Question.Kind "question" with an
//     app.QuestionForm in Data, shown as a form with a tab for each
//     question; and Kind "secret", a masked line (question.go).
//   - the person's skills in the completion of a command's name:
//     SessionInfo.Skills.
package ui
