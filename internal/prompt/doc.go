// Package prompt holds every word gnosh itself says to a model: the system
// prompt, the lines it adds to prompts, its reminders and nudges, the notes
// it leaves inside tool results, and the instructions of the small
// requests it makes on its own account. How a model behaves in gnosh is
// decided here more than anywhere else.
//
// The words are in files, one text to a file, under texts. The Go code
// around them has no words of its own: it chooses which texts apply, fills
// in their names and joins lists with commas and line ends. So a change to
// what gnosh says shows as a change to a text file, and a person can read
// every text and replace any one of them with a file of their own (Load,
// Export). Texts lists them all, with when each is sent and how big it is.
//
// The package reads no clock, no environment and no disk, except for Load,
// which reads the one directory it is given. Every function returns the
// same text for the same arguments, so a conversation rebuilt from what
// was stored is byte for byte what was sent.
//
// # Two facts that shape the texts
//
// Most of gnosh's use is a model on the person's own machine, with a
// window of 32,000 to 64,000 tokens. Every token of fixed prompt rides on
// every request, and a model server reuses what it has already read only
// as far as the first byte that differs from the request before. The
// earlier gnosh's instructions were about 7,800 tokens, a quarter of them
// about mem, and the size of its fixed prompt was one of the measured
// reasons it ran 1.3 to 4 times slower than it had to. So:
//
//   - The system prompt is small. The fixed core is about 750 tokens and
//     the part about mem just under 1,500; with every part on, what gnosh
//     writes comes to about 2,600 (about 2,900 with the fuller of the two
//     texts about mem). The tests hold these as budgets.
//   - The system prompt is fixed. Nothing in it is a date, a directory, a
//     model's name or a session's id. What differs from session to session
//     goes with the first prompt instead (Environment).
//   - A rule the model needs at the moment of a mistake is not here. It
//     belongs in the refusal or the tool result that the mistake produces,
//     which is read right before the next call and was measured to work
//     where a line in the system prompt did not.
//
// # The marker
//
// Whatever gnosh says on the user's side of a conversation stands between
// <gnosh> and </gnosh> (Open, Close). The tag is short, it is gnosh's own
// name, and it is the same every time; the first part of the system prompt
// tells the model, in one sentence, that such text is the harness and not
// the person. Every function here that returns such a line returns it
// already wrapped. Wrap does the same for text of gnosh's that is written
// elsewhere, such as the goal directive the mem code makes.
//
// The marker is used only on the user's side. Inside a tool result gnosh
// writes plain words with no marker: a note in square brackets after a
// tool's own answer, or a result that is wholly gnosh's, such as a
// refusal. The model is told that nothing a tool returns is the user
// speaking, whatever tags it carries, so a tag there would teach it the
// opposite. What gnosh says inside a result is news about the
// conversation, never an instruction from the user.
//
// Nothing put into a line can break it. A value goes through Untrusted on
// its way in, which breaks gnosh's own tags and every control sequence with
// one invisible character, and a value is never read as template text. So
// a hook's reason, a file name or a quoted command can neither end the
// marker early nor start a second one.
//
// # The system prompt
//
// System returns it as parts, the part that changes least first, the way
// core.Request.System wants them. In order:
//
//   - core, with security as its last lines. The same bytes in every
//     session. It says who the model is working as and for; that text in
//     the marker is the harness, and that what a tool returns is data and
//     never the user's instruction or approval; and the handful of things
//     models kept getting wrong until they were told: act instead of
//     describing, send calls that do not depend on each other in one reply
//     (each reply is a round trip of seconds to minutes), use the file
//     tools on files and why, look for a thing where it belongs and never
//     across the whole disk, read a refusal and do what it says, do not
//     poll for a background command whose end will be announced, never
//     delete gnosh's data folder, never ask for a password in the
//     conversation, check before claiming, and say plainly what was done
//     and what was not. It is short rules, one to a line, written for a
//     model of 27 billion parameters to follow; where the reason for a
//     rule is what makes it stick (the file tools keep track of what was
//     read), the rule gives it.
//
//   - security is a file of its own so that it can be replaced alone. The
//     rule is Jae's: security work is offered and done, "someone could
//     misuse this" is not by itself a reason to decline a coding request,
//     and when unsure the model asks and then goes ahead. The sentence is
//     written fresh from that requirement.
//
//   - style-proactive, style-concise, style-explanatory or style-learning,
//     right after the core, when the person chose one of Claude Code's
//     four output styles (outputStyle, /output-style), or the text of a
//     style of their own. The texts say what each style does in gnosh's
//     words, from what Claude Code's documentation says the styles do. A
//     style of the person's own that does not keep the coding
//     instructions puts core-any-role in place of the core's first line,
//     the coding agent's role; the rest of the core, how to work in gnosh,
//     stays. These are the one choice of the person's that changes the
//     system prompt: a choice made on purpose, for the whole session.
//
//   - language, after the style, when the person set the language replies
//     are in (Claude Code's language): it is asked for in the system
//     prompt, as Claude Code does, since it holds for the whole session;
//     code, commands and quoted text stay as they are.
//
//   - attribution, after it, when a settings file says what the model
//     signs its commits and pull requests with (Claude Code's
//     attribution). gnosh signs with nothing of its own: Jae's rule for
//     his repositories is no agent trailers.
//
//   - no-tools, only in a session with no tools at all: a model with no
//     tools otherwise writes its tool calls out as text.
//
//   - memory, only when the session has the gnovm_run tool. It is the
//     earlier gnosh's own text on mem, which was tuned against the real
//     model over some forty changes, cut from about 2,000 tokens to under
//     1,500. What stayed is what only the system prompt can teach before
//     the first call: print every term of a sum and never bend a figure,
//     plan a task as todos in one call, the worked example of that call
//     (told to make the task and then add each step, a model nested the
//     plan under the user's first step; with the one call, every plan came
//     out right), enter the todo the user names, write a result after the
//     work, thoughts, questions, facts, rules, secrets. What went is what
//     mem's own receipts and refusals say at the moment it matters.
//     memory-full is sent in its place when the system prompt is built
//     with MemoryFull: the same text with what went put back. The section
//     after this one says how the two differ and how to choose.
//
//   - gno, only in a project with Gno code: a plan that writes Gno code
//     has an audit todo right after it, against the two guides. Without
//     the rule in the instructions, a tested model shipped every planted
//     defect; with it, the audit was planned three times in three.
//
//   - the project's and the person's instruction files, handed in already
//     laid out. They are files, so control sequences and gnosh's tag in
//     them are made harmless.
//
//   - deliver, only when the session has the Deliver tool: name what is
//     being made before making it. It stands after the instructions so
//     that a session with the tool and one without share everything before
//     it in a model server's cache.
//
// agent-work-tree (AgentWorkTree) is not one of these parts: the session
// adds it to SystemInput.Append, last, for a sub-agent that works in a git
// work tree of its own. It says where the work tree is, that a path into
// the person's checkout means the same file in the work tree, and that
// the sub-agent's changes stay there.
//
// # The two texts about mem
//
// SystemInput.Memory chooses between two texts about mem: memory, the
// lean one, which is the default, and memory-full. Which of them a model
// works better with is not known, and it is not to be argued: it is to be
// settled by measurement on the AI box, the same tasks run on the real
// model with each, which is how the earlier gnosh settled its wording.
// The 1,500 tokens the lean text was cut to fit were a guess and not a
// finding, and every item the full text adds is one the mem spec lists as
// required in the system prompt (mem.md §14.1). Until the runs are made
// the lean text is sent.
//
// The full text, about 1,750 tokens, says every item of that list but the
// last, the Gno rule, which is the part gno. It leaves to mem's receipts
// only what the spec itself gives them to say: what to do on entering a
// todo (do it now, with your tools; or, for a todo with todos under it,
// that they are its work), a todo's thoughts, handed back word for word
// when it is entered and when it is popped, the call to send next after a
// pop, that a pop closed the whole plan, who else is working the plan and
// how long a wait for them was, and the right name where a wrong one was
// typed.
//
// The lean text leaves all of that to the receipts too, and besides it
// what the full text adds, about 250 tokens. For each of those, this is
// what tells the model instead:
//
//   - the receipt for entering a todo, at every push: that the result
//     is to be what the tools returned (the lean text says so too, the
//     full one adds "the edit made, the command run and what it
//     printed"), and that nothing done in a todo is undone by its pop.
//     That receipt and the frame line, on every typed prompt: that a pop
//     also removes what the user said;
//   - the frame line: that work the plan has no todo for goes in a todo
//     of its own;
//   - the goal's directive, and the receipt when a goal has no todo left:
//     that the todo /goal bound the session to is closed with mem.Done;
//   - mem's refusals: that a todo another session holds is theirs, and
//     that the Parallel mark goes on the todo the others are under;
//   - the receipts while other sessions work the plan: to keep to the
//     todo's files, and that a bare mem.Next() waits for the others;
//   - the digest a new session is given: what a session starts with;
//   - the fixed core: that the data folder is never deleted or moved;
//   - nothing, at the moment it matters: that leaving a todo is neither
//     popping nor dropping it (the lean text says only that Leave leaves
//     it open), that todos stay in order unless marked, that what is
//     fixed outside a todo the others fix again, and the smaller calls:
//     a thought named by the model itself, a question under a new todo,
//     a wait taken off, a fact removed, what /mem can do. A model that
//     needs one of these has mem.Help, or the refusal it gets for doing
//     without. Here nothing stands in for the system prompt, so this is
//     where the two texts differ most.
//
// The two texts are kept word for word alike but for those items, and a
// test holds them to it, so that a run with one and a run with the other
// differ in nothing else.
//
// # Lines on the user's side
//
// With prompts:
//
//   - environment (Environment) goes ahead of a session's first prompt:
//     the day, written 2026-10-30 because a model copies the form it is
//     shown into calls that take only that form; the directory; who gnosh
//     runs as and on which machine; how git stands; the other machines
//     declared, with the rule that a path belongs to the machine it was
//     seen on. It says it is not kept up to date, so that a git status
//     from the start is not taken for the present one.
//   - mem-line (MemLine) goes last on every prompt a person types, where a
//     model that answers without thinking still reads it right before its
//     reply. The words are Jae's. With it a tested question was worked out
//     in Gno six times in six.
//   - frame-line (FrameLine) follows it while the session stands in a
//     todo: where it stands, that a pop removes everything since, and that
//     other work goes in a todo of its own. Its first sentence also ends
//     the mem reminder.
//   - slide (inside FrameLine, or SlideLine by itself) says that the
//     oldest rounds left the context to make room, where the plan still
//     is, and that the reads made there are forgotten.
//   - large-step (LargeStep) says, once per todo, that the todo has grown
//     large: pop it or split it.
//   - mem-reminder (MemReminder) comes after ten replies with no mem call.
//     It calls itself a reminder to ignore if it does not apply, since not
//     every stretch of work is a task to plan.
//
// When a turn would end or goes round in circles. No count ever ends work
// in gnosh, so each of these keeps the turn going and says why:
//
//   - repeat (Repeat) quotes the call the model keeps making and says that
//     making it again will tell it nothing new. repeat-refused-close
//     (RepeatRefusedClose) is the same for a close that keeps being
//     refused; it quotes the refusal and says the work is still to do. A
//     model once sent one refused close three hundred times through thirty
//     nudges that said only "do not repeat".
//   - text-call (TextCall) tells a model whose reply is a tool call written
//     out as text (a chat template's <tool_call> markup that its server did
//     not take as a call) that nothing ran: Qwen on llama.cpp does this
//     now and then, and the turn ended on it.
//   - empty-reply (EmptyReply) asks again after a reply with no text and
//     no tool call.
//   - stop-hook (StopHook) carries a Stop hook's reason, and says the hook
//     is the user's, since the user's own hooks speak for the user.
//     continuation (Continuation) carries any other reason, and
//     check-could-not-run (CheckCouldNotRun) says that a check which could
//     not run has not passed.
//   - checkpoint (Checkpoint) is sent in place of a send-back every so
//     many in a row. It asks for the decision first (is there a real
//     reason to stop?), because a model asked last carries on working and
//     never answers; it gives the marker on a line of its own; and what
//     holds the turn open comes after.
//   - loop, loop-prove and loop-check-in (Loop, LoopProve, LoopCheckIn)
//     are /loop's: keep going; show that it is done, since a model once
//     wrote the word that ends the loop to get out of a hard step; and the
//     check-in every 25 turns.
//   - close-sendback (CloseSendBack) sends the model back to a todo whose
//     close was refused, once per turn, and ends "Do not tell the user it
//     is done": a model that was refused once told the user "Fixed".
//   - goal-not-met (GoalNotMet) gives the judge's finding and says to ask
//     the user when what is missing is theirs to do. Told only to do what
//     is missing, an agent edited the user's files unasked.
//
// Notices:
//
//   - interrupted (Interrupted) is put where a turn was stopped, by the
//     person or because gnosh itself was stopped, so that a reply cut
//     short is not read as one the model chose to end.
//   - background-ended (BackgroundEnded) says a background command ended,
//     how, and where its output is, and that it is news and not a request.
//   - monitor-lines (MonitorLines) passes on what a command that Monitor
//     watches printed, a few lines at a time, as the command's output and
//     not a request; how many lines did not fit, and the file that holds
//     them all; the last lines say so. background-ended then says that a
//     command a Monitor watched has ended.
//   - agent-ended (AgentEnded) says the same of a sub-agent the model
//     started in the background, with its report, marked as its own words.
//   - subtask-ended (SubtaskEnded) says the same of a copy of the
//     conversation the person set to work in the background with
//     /subtask, with the answer it printed, marked as its own words: the
//     model is to tell the person what came of it.
//   - btw (Btw) stands before a side question the person asks with /btw,
//     in a request beside the session's own that is not kept: answer in
//     words from the conversation, call no tool, start on nothing.
//   - recap (Recap) is the question of /recap, asked the same way: the
//     session in one line, for the person.
//   - thinking-budget (ThinkingBudget) is put at the end of the model's
//     thinking when its budget runs out (thinkingBudget, MAX_THINKING_TOKENS),
//     by a server that can: the model goes on to its answer.
//   - next-prompt (NextPrompt) is asked the same way after a turn, when
//     promptSuggestionEnabled is on: the prompt the person would most
//     likely send next, a few words on one line, which their box shows.
//   - statusline (StatusLine) is the prompt of /statusline: write a script
//     from the user's words, try it on this session's JSON, and add the
//     statusLine setting to their gnosh.json with the Edit tool.
//   - print-wait (PrintWait) wakes the model in a run (-p) that has waited
//     long for background work it started: what still runs and for how
//     long; stop it with TaskStop, or end the turn to wait on.
//   - print-wrap-up (PrintWrapUp) tells the model once, in a run (-p) with
//     GNOSH_PRINT_MAX_DURATION_MS, that most of its time is used: finish,
//     check quickly, and end the turn.
//   - print-leftovers (PrintLeftovers) wakes the model once as a run (-p)
//     is about to end with processes its commands left running, which the
//     end stops: keep one with setsid, or end the turn.
//   - structured-output (StructuredOutput) asks, after a run's turn with
//     --json-schema and beside the session's request, for the result as
//     one JSON value that conforms to the schema, again with what was
//     wrong when the last one did not.
//   - agent-mention (AgentMention) stands after a typed prompt for each
//     kind of sub-agent it names with @agent-: hand the task to it.
//   - mention (Mention) stands after a typed prompt for each path it
//     mentions with @: what Read returns for a file follows it, or a
//     directory's listing, or it says a deny rule kept the path out or
//     why it could not be read.
//   - summary, with summary-extra for what the person asked of it
//     (Summary), is the request for a summary. It is about 200 tokens and
//     asks for at most 500 words: what the user asked, in their words;
//     what is done, and only what tool calls did (a change found or
//     planned is not done: a summary on box 2, 16 November 2026, called
//     32 typo fixes done of which 6 were made); what was found that the
//     rest needs; what is left, every change not yet made among it, with
//     the user's latest request quoted, and every point where they said
//     to stop or ask first. It leaves out file contents and,
//     in a session with mem, everything mem holds, since gnosh hands that
//     back itself. The long form it replaces took a median of five minutes
//     to answer on the person's own machine.
//   - summary-intro and after-summary (SummaryIntro, AfterSummary) stand
//     before and after the summary once it has replaced the conversation:
//     this is a summary you wrote; carry on from it, stopping where the
//     user asked to be told or asked, and otherwise without asking. (A
//     plain "carry on without asking" took the model past such a stop:
//     Claude Code's own summary line did, on 16 November 2026.)
//     earlier-left-out (EarlierLeftOut) stands where part of a summary
//     request did not fit: after its first message, or at its start.
//   - files-back (FileShownBack, FilesNotShownBack) gives back after a
//     summary the files the model read or changed last, as Read returns
//     them now, and names those too long to give back, to be read again
//     before they are changed (as Claude Code does after compacting).
//
// # Notes inside tool results
//
//   - pop-note and slide-note (PopNote, SlideNote) are appended to the
//     answer that stays when what followed it was removed from the view:
//     how many tool calls or messages went, and why. Without the note a
//     long session reads as todos entered and popped at once, and a model
//     copies that.
//   - pop-receipt (PopReceipt) follows mem's own lines in the result of
//     the call that pops a todo: everything since the todo was entered was
//     removed from the context, the result and the thoughts stay, these
//     files were changed in it, this else was changed in mem, and the files
//     read in it must be read again before they are changed. It says
//     "removed from your context", never "left", and gives no number of
//     tokens. Read at the moment of the pop, it is what keeps a model from
//     looking for what is gone, or from editing a file it no longer sees.
//   - plan-gnovm (PlanRefusal) answers gnovm_run, gnovm_new and gnovm_drop
//     while the session is in plan mode, where the model may look and not
//     change: the tools stay in the list, so that the fixed prompt is the
//     same bytes in and out of plan mode, and the call is refused with
//     what plan mode means for the machines and for mem, and what to do
//     once the plan is approved.
//   - report-caution (ReportCaution) stands in front of a sub-agent's
//     report in which ScanReport found text written like a harness's
//     notes or a conversation's turns, which it marked with a backslash,
//     or the name of what turns permission checks off: such text can come
//     from what the sub-agent read, and the report is data all the same.
//     A background sub-agent's notice (agent-ended) carries it too.
//   - missing-result (MissingResult) stands for the result of a call that
//     has none: it never started, it was stopped, its reply was cut off,
//     or gnosh itself was stopped; or, in a side question's request, it
//     is still running. It says whether the call ran at all,
//     and when it may have run in part, to check before relying on it. It
//     is the whole result, so it needs no brackets.
//   - close-entered-here, close-unbacked, close-not-made,
//     close-review-refused and close-cancelled are the refusals of a close
//     (CloseEnteredHere and the rest). Their wording is the earlier
//     gnosh's own, each sentence answering a failure that was seen. They
//     begin "not popped: " or "not closed: " and name the todo first,
//     which is how RefusedClose knows one.
//
// # Side requests
//
// A side request is a short question to a model that sees nothing else of
// the session. Each has a fixed set of instructions and one message.
//
//   - close-review, with close-review-request and close-review-command
//     (CloseReview), checks a close before mem keeps it. The instructions
//     are the earlier gnosh's own, arrived at by replaying 244 real
//     closes. The question comes last, names the todo and asks first
//     whether the result is that todo's own.
//   - goal-judge and goal-judge-request (GoalJudge) judge a goal or a
//     closed task. The answer's "basis" is a field the code reads, not a
//     phrase the judge writes.
//   - namer (Namer) names new todos, thoughts and facts: by their own
//     subject, the thing first and then the action, no two alike. An
//     earlier instruction, to leave out what all the names share, made a
//     model drop the subject itself.
//   - title (Title) gives a session its title from how it begins. It asks
//     for the title alone, because the answer is used as it comes.
//
// # Control sequences
//
// Defuse makes harmless the sequences a model server reads as structure:
// the tokens of chat templates and llama.cpp's media marker. It is for
// every text that is not gnosh's own, and it is safe on a whole message.
// Untrusted does the same and also breaks gnosh's own two tags, the marker
// and the <mem> of the mem code's blocks; it is for what tools and files
// bring, taken by itself, so that such text cannot carry a line that reads
// as gnosh speaking or a block that reads as the user's rules. Undefuse
// takes the marks out again, for what a model writes back. Their
// documentation says exactly which sequences and how.
//
// # Code that fills in texts its own way
//
// The agent loop keeps its words as formats for fmt.Sprintf and wraps the
// result itself. Format turns a text into such a format: a verb, or plain
// words, for each name. Wrap is the marker it then puts around the result.
//
// # Writing a text
//
// A text is plain words, with three things that are not:
//
//	{{name}}    is replaced by a value
//	{{name?}}   prints nothing: it asks whether name has a value
//	[[ ... ]]   is kept when every name inside it has a value, and left
//	            out whole otherwise
//
// Each text has names it must print and names it may use; Texts lists
// them. A text for the user's side does not write the marker: gnosh puts
// it around.
//
// # A person's own texts
//
// Load reads a directory of replacements, one file per text, named as the
// text is. It returns what it found, so that the person can be shown which
// texts are theirs. It refuses a file that could not stand in for its
// text, above all one that drops a name the text must print, and it names
// every such file at once. Export writes gnosh's own text as a file to
// start from; a file that was exported and never edited changes nothing,
// whichever build exported it, and one that was edited against an older
// text is reported as stale. The lesson behind this: a person once ran for
// a week on a three-week-old copy of the whole prompt, kept for the sake
// of one sentence. So the pieces are small, and a piece nobody changed
// keeps following gnosh's own words.
package prompt
