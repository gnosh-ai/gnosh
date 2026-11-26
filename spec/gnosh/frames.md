# frames.md — the context as a call stack: exactly what the model is sent

What this file is: the precise rule for what goes into each request once the
model works in frames. Where a frame begins and ends, what a pop removes and
what replaces it, nested frames, the slide when one frame outgrows the
window, the token accounting, summaries, sub-agents, resumed sessions,
peers, the provider's prompt cache, and every edge case the design reviews
and QA runs settled. With worked examples and the measured numbers.

**All of this was built on the harness side and must be rebuilt from these
words.** mem (carried over) only reports that a push or a pop happened; the
harness does the rest. Sources: FRAMES.md draft 15 with its dated change
notes (the design, after six review rounds and Jae's rulings), HOLD.md and
HOLD-reviews.md (the mechanics that carried over and every ruling), the
memory notes on the frames implementation and its slide, QA-FRAMES.md and
IMPROVEMENTS.md (the QA runs of 2026-11-04/05), BENCH.md, and, for behaviour
documented nowhere else, a reading of the fork's own harness-side frames
code (its wording is quoted as [F]; its structure is not described).

---

## 1. The model in one paragraph

"A Frame is a piece of work in mem, a todo (what mem used to call a goal or
a step), seen through a stack. Entering one marks where the context will be
cut; leaving it with a result cuts everything since. A thought is a labelled
line logged where the model stands: it costs nothing, cuts nothing, and is
how anything worth keeping survives a pop." [D]

"At any moment the conversation the model is sent is the prompt, the pushes
of the frames it is inside, the pushes and pops of the steps already
finished (each a push line and a pop line with its result), and the work of
the present frame […]. It is a program's stack: locals die on return, and only
the return value comes back; what a frame should remember it writes down as
thoughts, which live in mem under the frame, popped or not." [D]

Jae's rulings that fix the shape (2026-10-30/31, HOLD-reviews.md):

- The original ask: "mem.PushFrame(*Frame), mem.NewFrame(name, purpose?),
  mem.PopFrame(result), mem.Frame(); … the trampoline is
  mem.PushFrame(mem.Frame().NextTodo())". (The spellings changed; the shape
  stood.)
- **A pop removes everything**, the user's words and the model's replies
  included; "what matters is kept as a thought".
- **"Nobody can trim for you … my thoughts are my own"**: only a session's
  own pops change its stack.
- **No folding or dimming of popped rows** in the chat in v1.
- "that way compaction is no longer necessary": compaction is a backstop.
- The slide: "what if instead of compaction we do the context sliding thing
  instead and 'pop' the first frame so to speak?"

---

## 2. Words used in this file

The rule is stated over the session's stored conversation, as a list of
**rows** in order. However the new harness stores messages, these four
kinds must be told apart:

| Row | What it is |
|---|---|
| **prompt row** | user-side text: a prompt the user typed; a prompt folded into a running turn; a *hidden* prompt the harness wrote (a goal continuation, a stop-hook verdict, a send-back); a hidden reminder or notice the harness inserted |
| **assistant row** | one model reply: its text, reasoning, and zero or more tool calls |
| **result row** | the results that answer an assistant row's tool calls |
| **summary row** | a compaction summary |

If the new store carries tool results and prompt text in one user-role
message, treat the results and the text as separate rows for this rule.

- A **round** is an assistant row that has at least one tool call, together
  with the result rows that answer its calls. "A cut between rounds never
  splits a call from its result."
- A **turn** begins at a prompt the user typed and runs until the session
  goes idle, hidden continuations included (§4.3 gives the exact start).
- A session's **stack** is the list of frames it has entered and not left,
  bottom to top.
- The **push round** of a frame is the round holding the `gnovm_run` call
  that entered it. The **pop round** is the round holding the call that left
  it.
- **Model-facing list**: what is sent to the model. **Human-facing list**:
  what the chat, export and transcript show.

---

## 3. Where the stack lives: it is derived

**The harness keeps no stack store.** [D] A session's stack is derived from
its own rows.

1. **Events.** Every kept `gnovm_run` call on mem returns the pushes and
   pops it made, in order (`RunResult.Frames`):

   ```go
   type MemFrame struct {
       Kind     string // "push" or "pop"
       Id       string // the frame: `fix-flaky`, `fix-flaky/find-cause`
       Purpose  string // its title, for the frame line and the task list
       Result   string // a pop's result
       Thoughts int    // how many thoughts the frame holds when popped
       Session  string // the session that made it
   }
   ```

   A pop event is made by `mem.Pop`, by `mem.Done` or `mem.Drop` of the
   present frame (a drop's result reads "dropped: why"), by `mem.Leave`
   ("left open: why"), and when a frame mem already holds closed is taken
   off the stack (§12.6). `mem.Start` is **not** a push.

2. **Kept with the result.** The tool stores those events as metadata of the
   call's result row (key `frames`). The metadata must be **persisted with
   the row and copied with it**. A refused input records no event; a
   cancelled call never ran.

3. **Derivation.** Walk the session's result rows in order. For each
   non-error `gnovm_run` result with frame events, take the events **whose
   `Session` is this session's id** and apply them: a push appends a frame
   (with the id of the call that pushed it); a pop removes the topmost frame
   with that id and records a **range** (frame id, push call id, pop call
   id). The stack is "the pushes without a matching pop, in order".

   - A pop naming a frame that is not on the stack records nothing. (It
     cannot arise while rule 6 holds: mem pops only what the harness told it
     is on the stack.)
   - Each distinct slide mark found on a row (§8.6) puts that slide's range
     and its frame back, so "a record derived again from the rows cuts the
     same".
   - When the walk is done, each pop's range gets its count of spared
     prompts (§4.2) and its note (§4.4), both worked out from the rows.

4. **Why the session stamp.** A branched or rewound copy carries the rows,
   but their events are stamped with the old session. So "a /branch and the
   rewind copy start at the root: only pushes made under their own session
   id count".

5. **Caching.** Derive once per session, keep it in memory, update it as
   calls happen. Drop the cached record whenever rows are removed from under
   it (a rewind), so it is derived again from what remains. Before deriving,
   make sure pending row writes are flushed.

6. **Told to mem on every call.** Before each mem call the harness passes
   the stack, bottom to top (`RunRequest.Frames`). mem checks every
   statement against it ("already on your stack", "you are still in `x`").

**The stack persists across turns and restarts.** "A question to the user,
Esc, a crash or a new day leave the model inside the frame it was in, and
the next prompt's mem block says so." A frame is never auto-popped at a
turn's end: "a frame spans turns by design".

**Only its own session pops it.** While a frame is on a session's stack,
nobody else closes, drops, forgets or takes it (the realm refuses, from the
hold). The soft-hold exception for a todo with todos under it is in
`close-check.md` §6 and `peers.md` §3.

---

## 4. The cut: what a pop removes

### 4.1 The rule

**A pop removes from the model-facing list every row after the frame's push
round and before the pop's round**, except the prompt rows of the turn the
pop is made in.

- The **push round stays whole**: its assistant row, every other call it
  made, and their results. The design's edge case 7 [D]: "Parallel sibling
  of the push: its result stays in the push's tool message."
- The **pop round stays whole**: the assistant row with the pop call, any
  text beside it, every other call that reply made (an edit sent in the
  same reply as the pop), and their results. "The pop call, with the result
  in its arguments, is the record."
- So a call and its result are never on opposite sides of a cut. Calls made
  in the same reply as the push, or as the pop, always stay; everything in
  replies strictly between goes.
- Between them go: the model's rounds, its replies and questions, reasoning,
  what the user said in **earlier** turns while the model stood in the
  frame, prompts the harness folded into those turns, notices and reminders.
- **Nothing is rewritten and nothing synthetic is inserted** in place of the
  removed stretch, with one exception: a short note appended to the push's
  answer (§4.4).

Precisely, given the push call id and the pop call id:

1. Find the assistant row holding the pop call. If it is not among the rows,
   nothing is removed.
2. Find the assistant row holding the push call. If it is the same row
   (pushed and popped in one input), nothing is removed.
3. The range starts **after the last result row that answers any call of the
   push's assistant row**, and ends **before the pop's assistant row**.
4. A summary row inside the range is never removed. After a summary row,
   the rows that continue the summary stay: removal resumes at the model's
   first own (assistant) row after it. So for a push older than the
   session's summary, what leaves the model's context is [D]: "A pop whose
   push is older than the session's summary (the context filled inside the
   frame) removes everything after the summary message; the summary and its
   continuation prompt stay." (The rows between that push and the summary
   are flagged with the rest. The model was not being sent them, so they
   free nothing and are not counted, §10.)
5. If the push call is not known, or its row is not among the rows, the
   range starts after the last summary row before the pop, or at the first
   row when there is none; either way removal begins at the first assistant
   row from there, as in step 4. Such a range carries no note (§4.4).

### 4.2 What stays of the user's words (changed 2026-11-04)

**A pop spares the prompt rows of the turn it is made in:** the prompt the
model is answering, prompts folded into that turn, and its hidden
continuations and reminders. What the user said in *earlier* turns while the
model stood in the frame is cut as §4.1 says; it "was to be kept as a
thought".

Why (QA finding A18, seen seven times, once with thinking on): a frame
entered in an earlier turn spans the current prompt whenever the user speaks
while the model stands in it. With the turn's own prompt cut, a model "left
with an earlier turn's 'do only (1) now' re-planned finished work", went
round a Drop/Reopen loop for 12 minutes, or never answered the question it
was asked. After the fix the same scenario "went clean in 4 minutes".

### 4.3 Where a turn starts

Needed for §4.2. Scan back from the pop's row for a **visible** (not hidden)
prompt row. Take the whole run of consecutive prompt rows that ends at it.
That run starts a turn if what precedes it is:

- nothing (the start of the conversation), or
- a summary row, or
- an assistant row with **no tool call** (the model's answer to the turn
  before), or
- anything else, when the **nearest assistant row before the run ended
  cancelled or failed** (result rows may lie between it and the run): "A
  cancelled turn ends where it stopped. The next prompt starts a new turn,
  so the cancelled turn's prompt is not spared." [D]

Otherwise that prompt was folded into a running turn: keep scanning back. If
no such run is found, the turn starts at the first row.

The spared rows are every prompt row in the range at or after the turn's
start.

### 4.4 The note on the push's answer (2026-11-01; revised 2026-11-06)

Once a range is cut, the push's result in the model-facing list gets one
line appended to its text [D][F]:

```
[Then came the work in it, 7 tool calls (bash 3, edit 1, view 3), removed from your context when you popped it.]
```

- The count is the tool calls of the removed assistant rows; the tools are
  listed by name, sorted, each with its count. One call reads "1 tool call".
- Rows an inner pop or a slide had removed already are counted with the
  rest: the note tells of all the work between the two rounds. Only the
  spared prompt rows are left out of the count.
- With no tool call among the removed rows: `[Then came N messages of work
  in it, removed from your context when you popped it.]`
- When every row of the range was spared, or the push is not among the rows:
  no note.

Why: a pop cuts everything between the push's round and the pop's, so "a
long session's visible history is a row of mem.Next() answers each followed
at once by a mem.Pop, and the model copies the row". One single session
"popped" a todo right after entering it without doing it ten times in one
run; with the note and the fresh guard (§9) that stopped.

Rules for the note:

- It is **worked out once**, from the session's rows, when the pop is made
  (and again whenever the record is derived from rows), **never from the
  list a request happens to send**.
- It is **said in every request after the pop**: the pop's own turn, every
  later turn, and a summary's request alike. So what one request sent, the
  next sends again, and "the prefix a provider cached holds from one request
  to the next".
- It changes only the model-facing copy of the push's result; the stored row
  is not edited.
- One answer carries one note. When several ranges begin at the same push
  (a slide, then the pop), the pop's note wins over a slide's; between two
  of the same kind, the one whose range reaches furthest.

### 4.5 Nested frames

"Stacks nest. An inner pop removes its own range at once; an outer pop later
removes what is left between its push and its pop, the inner push and pop
rounds included, so a finished goal leaves its push and its pop." [D] The
model-facing list is cut by the **union** of all ranges.

### 4.6 Other spellings that pop

- `mem.Done(id, result)` on the present frame is a pop (a result is
  required).
- `mem.Drop(id, why)` on the present frame drops its open todos, pops with
  "dropped: why", and cuts, "since the work is given up".
- `mem.Leave(why)` leaves the todo open and cuts the same way.
- Done or Drop of a frame below the top, or of an ancestor of a frame on the
  stack, is refused by mem. Done of the bound root is a plain call with no
  cut. A plain `mem.Done(name, …)` of a todo not on the stack cuts nothing.

### 4.7 Reopen and undo

"Reopen of a popped frame does not bring its rows back, and undo of the
entry that popped it leaves them removed." [D]

### 4.8 The cut is all-or-nothing with the call

"mem keeps an input's changes only if the whole input succeeds, and only
then is the context cut." A refused or failed input returns no events, so
nothing is cut.

---

## 5. What the pop tells the model

The pop's tool result is built in this order (`machines.md` §9.5):

1. **mem's own lines** [K]: `done `id` "title" → result`; `its thoughts,
   kept: `name`: text; …` (word for word, five at most); then either the
   push receipt of the next frame (when a Next follows in the same input),
   or the `next: …` hint; and, when the pop closes a top-level todo, the
   "That closes …, the whole plan" line.
2. **The harness's clauses**, returned by the frames callback [D][F]:

   ```
   Everything since you entered `fix-flaky/find-cause` was removed from your context: this result stays, and so do its 2 thoughts, listed above and kept in mem; the user still sees all of it. In it you edited internal/net/listen_test.go. In it you also changed mem: Fact `port`. Your reads inside it are forgotten: view a file before you edit it.
   ```

   - After "removed from your context", when prompt rows were spared: ", but
     for what the user said in this turn".
   - The thoughts clause: none for 0; "and so does its thought, listed above
     and kept in mem" for 1; "and so do its N thoughts, …" for more.
   - **Files edited**: the files the file-changing tools changed in the
     removed rows (successful calls only), each once, relative to the working
     directory where under it. Left out when none.
   - **Other changes to mem**: the mem calls in the removed rows that changed
     mem, by first word and name ("Fact `port`", "Done `x/y`"), not counting
     Think and the frame calls themselves, reads, or calls mem refused. Four
     at most, then "and N more". Left out when none. Precisely, in the code
     of each `gnovm_run` call on mem among the removed rows whose result is
     not an error and has no line starting `failed · `:
     - every line that starts with `mem.Word(` is listed as `Word`, followed
       by its first argument in backquotes when that is a string literal;
     - except the words Think, Thought, Push, Pop, Leave, Frame, Recall,
       Show, Find, Next, JSON, Check, String, Bind, Apply and Help, and a
       `mem.Fact("name")` given one name and nothing else (a read);
     - and every line that starts with `mem.Frame("x").Verb(`, for the verbs
       Done, Drop, Reopen, Note, Wait, Ask, Todo and Parallel, is listed as
       ``Verb `x` ``.
   - **The reads sentence**, when any read was forgotten (§6).
   - When nothing came between the push and the pop: `nothing came between,
     so nothing was removed from your context.`
3. The status line, then notes.

One line per pop when an input pops more than once.

The vocabulary is fixed: **"removed from your context"**, never "left"
("nothing left your context" reads as "nothing is left in your context").
**No token number is given to the model.**

---

## 6. What survives, and what is forgotten

**What survives** "is everything the frame did to the world: files edited,
commands run, mem changed, background commands started, and in mem the
frame's result and thoughts." [D]

**What is forgotten**, "because the model no longer has what it vouches
for":

1. **Reads, by path.** The edit and write tools require a file to have been
   read, unchanged, before it is changed. The harness forgets the reads of
   every path the removed rows touched: the `file_path` arguments of the
   view, edit, multi-edit and write tools, and the file a shell command
   showed (so `cat foo.go` counts, using the same shell-command parser the
   harness uses to record reads). "An edit to such a file is refused until
   it is read again."
2. **Views.** If the view tool answers a repeat view of an unchanged range
   with "refer to your earlier result", it must forget the removed rows'
   views, so the next view returns the file.
3. **Subdirectory context files** given in the removed rows are forgotten
   too, so they are given again on the next touch.

Without (2) "the model would be told to read before editing and then told it
already had" (the first blocking finding of the first review).

`priorart.md` states the point of it: "when a todo's work leaves the context,
so do the files the agent read inside it: before it edits one again it must
read it again, so it never edits a file from a copy it can no longer see."

Cost seen (24K-window run): "edits were refused after a slide cut the file's
view, costing a re-view round each time"; that is why the slide's line says
the reads are forgotten too.

---

## 7. What the prompt tells the model: the frame line

While the stack is not empty, every **typed** prompt that carries the mem
line (`mem.md` §14.2) also carries, as a second system reminder after it
[D]:

```
You are in `fix-flaky/find-cause` "find why TestFlaky fails" (your stack: `fix-flaky`, then `fix-flaky/find-cause`): pop it with its result when it is done; everything until then is removed from your context when you pop it, what the user says included, so keep what matters as a thought. Other work the user asks for meanwhile goes in a frame of its own: mem.Todo("rename the port flag").Push(), popped when done.
```

- The stack in parentheses appears only when it holds more than one frame.
- After a slide that concerns a frame still on the stack, the slide's line
  (§8.6) is appended.
- It is computed **as the prompt goes out**, whether it starts a turn or is
  folded into one from the queue, "so a prompt queued behind a turn that
  pushed or popped tells of the stack as it then stands".
- It is stored with the prompt as a hidden part, like the mem line.
- Hidden prompts carry no frame line.
- The wording predates §4.2: it still says "what the user says included",
  though what the user says in the pop's own turn now stays. mem's push
  receipt says the same ("what you were told included"). Neither was
  reworded; erring on the side of "keep it as a thought" costs nothing.

Evidence that it is read (QA, words1): the user's detail for a later todo
arrived while the model stood in a frame, and "its first call of the turn
was mem.Think("readme-14", "user: README must say --shout was added in
version 1.4 …"), before any work".

The mem reminder (`mem.md` §14.3), when it falls due inside a frame, adds the
line's first sentence (through "…keep what matters as a thought.").

---

## 8. The slide: when the window fills anyway

### 8.1 What it is

"At the point where the session would be compacted, standing in frames,
gnosh cuts instead of summarizing." [D] The oldest part of the conversation
leaves the model's context, "since mem holds the plan, the results and the
thoughts", and the model is told so. **No summary is written and no model
call is made.** "Compaction remains only with no frame to cut."

### 8.2 When it is tried

At the two points where a session would be summarized:

1. **Before a prompt goes out**, unless the session was just summarized.
2. **After each step of a turn**, when deciding whether to stop for a
   summary.

At either point, with

- `T` = the session's running token total (prompt + completion tokens of the
  last usage report),
- `E` = the pending estimate of what pops and slides removed since then
  (§10),
- `P` = the compaction point **as asked** (with any user overrides for
  compacting earlier),
- `W` = the model's context window (the harness's fallback window when the
  model's is not known),

if `T − E > P` and the session stands in at least one frame, try a slide
that frees `need = (T − E) − P + W/10`. "The slide frees what is over the
point as asked and a tenth of the window besides, so the rounds that follow
have room."

If a slide is made, **no summary is made**, and the slide stands for the
summary's follow-up check. If nothing can slide, the ordinary compaction
decision runs. With automatic summarizing turned off, neither is tried (as
read in the earlier code).

**The slide is tried whatever the compaction level.** If an earlier summary
left the conversation still over the point, the harness may stop compacting
(to avoid summarizing in a loop). That must not switch the slide off: "a
slide frees what it says or says it cannot, so it is never the loop a
repeated summary would be". (Found in a 24K-window run: 0 slides, 3
summaries, ended over the window.)

### 8.3 What goes, in order

Only rows **the model still has** are counted or cut: rows not already
removed by a pop or slide, and not at or before the last summary row.

**(a) First: the rounds before the first frame.** A model that "explores
first and pushes later had its big rounds outside any frame, with nothing to
slide inside". So the region from the last summary (or the session's start)
up to the bottom frame's push goes first.

- The region's **first round stays**: it "anchors the cut as a push's round
  does".
- The cut runs from after that anchor round to just before a later assistant
  row that has a tool call, **the bottom frame's push row at the latest**.
- If the prompt the turn is answering lies in this region, the region is two
  parts, tried in order: **the turns before the prompt first** (the cut ends
  at a row before the prompt), **then the turn's own rounds after it**; each
  part anchored by its own first round. (Changed 2026-11-06: before, a
  mid-turn slide started at the turn's prompt, so the turns before it were
  never cut and a lower frame's body went first.)

**(b) Then each frame from the bottom of the stack.**

- A frame with a frame above it: its **whole body** goes, "from its push's
  round to the next frame's push". A body with nothing left to cut is passed
  over.
- If the prompt the turn is answering lies inside that body, only the oldest
  rounds from the frame's start up to a row before the prompt go (as (c)).

**(c) Last: the oldest rounds of the top frame**, from after its push round
to just before a later assistant row that has a tool call (and before the
prompt, when the prompt lies in its body).

The walk stops, with no slide, at the first frame whose push row is not
among the rows. When that is the bottom frame, (a) is skipped as well.

### 8.4 How much a partial cut takes

For a cut "to a row of the agent's choosing" ((a) and (c)): let `body` be
the tokens of the still-present rows from after the anchor round to the end
of the part. Walk the candidate end rows (assistant rows with a tool call)
oldest first. Take **the shortest cut that frees at least `max(need, body /
2)`**; if none does, the longest. A cut that frees nothing is no cut.

So: "the shortest cut that frees what is over the point plus a tenth of the
window, at least half of the body, ending at an assistant row with a tool
call so the rest stays whole."

### 8.5 What is never cut

- **The prompt the model is answering**: the row of the newest prompt of the
  turn, typed, folded in or hidden, held **by id**. Before a new prompt goes
  out nothing is protected (the new prompt is not among the rows yet).
- Summary rows and the rows behind them (they are not in the context).
- The anchor round of each cut.

A slide does **not** spare other prompt rows inside its range; that is the
pop's rule only.

### 8.6 What a slide does to the rows, and what the model is told

A slide's cut is recorded and applied exactly as a pop's range (§11): the
rows are flagged, the reads and views in them forgotten (§6), their tokens
added to the estimate (§10), and the range joins the session's ranges so
every later request leaves them out. Its note on the anchor's answer says
`…, removed from your context to make room.]` (with no calls: `[Then came N
messages, removed from your context to make room.]`).

The flag on a slid row is `slide:<frame>:<push call id>:<pop call id>`,
where `<frame>` is the frame whose body was cut (empty for the rounds before
the first frame), `<push call id>` is the first call of the anchor round's
assistant row (the frame's push call for a frame), and `<pop call id>` is
the first call of the row the cut ends before (the next frame's push call
for a whole body). So "a record
derived again from the rows cuts the same".

The model is told [D]:

```
The start of `x` left your context to make room; mem.Show("x") has the plan, the results so far and the thoughts. Your reads there are forgotten: view a file before you edit it.
```

For the rounds before the first frame: "Your rounds before `x` left your
context to make room; …" (`x` the bottom frame). Several are joined with
"and". Where it is said:

- A slide **before a typed prompt**: in that prompt's frame line.
- A slide **before a hidden prompt** (which carries no frame line), or
  **after a step in the middle of a turn**: as a hidden system-reminder row
  before the model's next reply.
- A slide after a turn's last step: by the next prompt's frame line.

The line is repeated in frame lines only while the frame it names is still
on the stack. A pop takes its frame out of the slid list ("pushed again
under the same id, it is a new frame, with nothing cut from it"); a pop that
empties the stack clears the list.

The user is told too: "slid `x`: about N tokens of its start out of the
model's context, instead of compacting" [F].

### 8.7 The "this step is large" line

Well before the compaction point, once per frame: when the session's total
has grown by more than **half the compaction point** since the frame was
pushed, the model gets one hidden system reminder before its next reply
[D][F]:

```
this step is large: pop it with what you have, or split it into sub-steps (you are in `x`, and about 12K tokens came in since you entered it).
```

The total it reads is measured as the request goes out (§10's `T − E`),
so a pop or a slide made since the last request counts. (Read from the
last request's count, a pop in the round between left the todo it
returned to looking as if it had just taken in all the pop removed: told
then that it was large, its one telling, a todo on a Mac Studio grew large
on its own work days later with no word, and slid. DECISIONS 393.)

Not given when automatic summarizing is turned off. The design calls the
line "safe (unlike deferring the compaction check, which was rejected)":
deferring the check by one step to ask for a pop "sends a request already
at the compaction point … inside the 20,000-token buffer that exists to
avoid the hard 'exceeds the context size' refusal".

### 8.8 Measured (rename task, two sessions, Qwen3.8-27B, 2026-10-31)

| Window | Build | Result |
|---|---|---|
| 24,576 | first slide build | 15 slides, 2 pops, no summary; rename done (5/6 checks) but both turns hit the 40-minute cap. The fixed prefix (system prompt, tools, reminders) was about 15K of the 24K, so the session "slid almost every round, re-reading the kept half each time". |
| 24,576 | after review fixes | 0 slides, 3 summaries, ended over the window: the model read six files at the root before pushing anything, so there was nothing to slide in a frame (fixed by §8.3 (a) and §8.2's last rule). |
| 24,576 | landed | **6/6 checks in 19 min**, 1 slide (the rounds before the first frame, 692 tokens, told once), 4 pops, **0 summaries**; sessions ended at 18,319 and 18,788 of a 19,661 point. |
| 32,768 | landed | **6/6 in 27 min**, 1 slide (5K tokens of exploration), 6 pops, **0 summaries**; ended at 21,681 and 19,853 of a 26,214 point. |

"The model's exploration before it pushes is what slides, pops do the rest,
and compaction does not happen." "24K is a stress setting, not a real one:
for the intended regime run 32K or more."

Bench (2026-11-07, nine Go packages at a 32K window): Crush failed twice, 0/9
at the one-hour limit, with one summary that alone took 20 minutes; gnosh
with fix A passed 9/9 in 54:10 with **6 slides and no summary**. One run
each. "So the frames claim holds only past the window, and it is not yet
measured twice."

**An open defect**: in the same bench, stock gnosh "ran its prompt to 40.5K
at a 32K window, after four slides". Not diagnosed: "either the slide
threshold is tuned for big windows, or the window it discovered was not
32K". Jae: "make note to fix it later". See `open-questions.md` Q3.

---

## 9. The fresh guard: a pop straight after the push

A frame is **fresh** when no work came between the round that entered it and
the round now trying to close it. mem refuses to pop a fresh frame [K]:

```
not popped: nothing came between your entering `x` "…" and this pop, so it is not done. Do it, or check that it is done, then pop it.
```

The harness computes it and passes it with the call (`RunRequest.Fresh`,
the top frame's id or ""). It is asked only when the session stands in a
frame and the call's code contains `Pop(` or `Done(`.

The top frame is fresh unless one of these holds:

1. It was entered by this same call (that case is the close check's
   "entered here", `close-check.md` §3.1); or its push call is not known;
   or its push row is not among the rows before this call's row. None of
   these is refused by this guard.
2. **Work came between** the push round and this call's round: in the rows
   §4.1 would take for a pop made by this call, the rows inner pops removed
   already included ("the rounds of the frame's own todos, popped already,
   count"). Work is:
   - a call to a tool other than mem **that did not fail**, a failed call
     being one whose result is an error (it "never ran, or did nothing: two
     failed renames let a todo be closed 'done'");
   - a call to mem that changed it and was not refused (its result is not
     an error and has no line starting `failed · `). The code counts as a
     change when it holds any of: `mem.Think(`, `mem.Note(`, `mem.Rule(`,
     `mem.Todo(`, `mem.Later(`, `mem.Wait(`, `mem.Ask(`, `mem.Done(`,
     `mem.Drop(`, `mem.Reopen(`, `mem.Forget(`, `mem.Redact(`,
     `mem.Start(`; a method call `.Todo(`, `.Wait(`, `.Ask(`, `.Parallel(`,
     `.Done(`, `.Drop(`, `.Reopen(`, `.Note(` or `.Think(`; or a
     `mem.Fact(` or `mem.Thought(` given anything but one name alone (one
     name alone reads it);
   - an answer the model gave the user (an assistant row with text and no
     tool call): "a todo asking for an explanation is done by giving it".
     **But not once a close of this frame has been refused** earlier in
     those rows: "an answer after that is the claim".
   - **Not work**: a prompt; a read of mem (Show, Find, Recall, a fact or
     thought read by name); stepping in or out by itself (a Push of a todo
     planned earlier, a Next, a Pop, a Leave); a pop or close that mem
     refused.
3. **Work came since the plan was made** (fix A, Jae 2026-11-11 "do it"):
   when the frame at the bottom of the stack is another frame, work between
   *its* push and now counts too. "Every model on the bench (Qwen, gpt-oss,
   GLM, Gemma) did a plan's work first and entered each todo only to close
   it, and was refused for nothing done in it."
4. (In mem) the todo has todos under it that are all closed, some done: "its
   work was done in them".

History of the definition (each step from a real false close):

- 2026-11-01: any row between counted. A model refused once ran `mem.Show()`
  and retried; the pop went through.
- 2026-11-04 (A20): only non-mem tool calls and mem changes count; a refused
  pop does not. "Counted over 40 runs, 17 of 90 accepted pops had no work in
  their todo with thinking off, and none of 9 with it on."
- 2026-11-05: a failed tool call is no work.
- 2026-11-11: work since the plan was made counts.

The guard is cheap and deterministic but not enough alone: "a view counts as
work", so a model "viewed README.md and popped 'README.md:3 now says Get'
with no edit". That is what the close check adds (`close-check.md`).

---

## 10. Token accounting

"The check and the room left for tool results read the session's prompt and
completion counts, which the server's usage sets each round. The Run keeps a
pop's estimate beside those counts, and the check and the room read the pair
until the next usage report." [D]

1. When rows are removed (pop or slide), estimate their tokens with the
   harness's usual estimator, counting only rows the model still had (not
   already removed by an inner pop, not behind the summary), and add it to
   the session's pending estimate `E`.
2. Wherever the harness decides about compaction or about room (the two
   slide points, the large-step line, how much a big read may bring in), use
   `T − E`.
3. When a usage report arrives **for a request that was sent with the cut
   applied**, subtract from `E` the amount that was pending when that
   request was built ("that much of the estimate is spent; a pop made since
   stays held"). A report with no usage does not count.
4. The counts shown to the user (header, sidebar, status) change at the next
   usage report, "as after /rewind".

The user is told what a pop took out: "popped `fix-flaky/find-cause`: about
44K tokens out of the model's context" [D] (as a notice in the UI, on
stderr in a non-interactive run). Numbers read 850, 44K, 1.2M.

---

## 11. Persisting the cut

**The flag.** "The removal is a flag on the rows, so every list built for
the model skips them, now and later." Each stored row has a `popped` marker:
empty; or the id of the frame whose pop removed it; or a slide mark (§8.6).
It is set by explicit row ids, for every row inside the range and never the
summary row, and each change is published so an open chat can react. A copy
of a row (branch, rewind copy) carries its marker.

(The unlanded frames-on-chain branch of 2026-11-06 found that a row's marker
names only the *outermost* frame that removed it; code that needs each
frame's own rows derives them from the push and pop call ids.)

**Model-facing readers skip flagged rows:** the history of each later
request; the input of every later summary and the transcript file a summary
points the model to; the reminder counters; the context breakdown; a resumed
session.

**Human-facing readers keep them:** the chat, export, the rewind scan of
changed files, erasure scans. **The goal judge, though a model, reads the
full list**: "a frame popped inside a goal has its evidence there."

**Each request re-applies the ranges.** Within a turn the request list is
rebuilt for every step, so the cut is applied on every step: put back
anything earlier steps of the turn injected (folded prompts, reminders) at
its recorded position, then drop the messages inside every range (keeping
the spared prompt messages), then append the notes. The provider's cache
marks and any prefix handling run on the cut list.

**The list stays valid for every provider**: no tool call is split from its
result, and what follows the push's results is either the pop's assistant
message or, when prompts were spared (§4.2), those prompts and then the
pop's assistant message. In a store where results and prompts are both
user-side, a spared prompt then sits directly after the push's results: if
the provider needs strict alternation, send them as one user-side message,
results first. The note of §4.4 is appended to the text of the result that
answers the push call itself, not to a sibling call's result.

**A summary's request is cut the same way**, with the same notes, "so the
cached prefix still holds".

**Flush first.** `gnovm_run` runs alone and in order, so every row of the
removed rounds exists when the pop runs; flush pending row writes before
reading them.

**Applied even on Esc.** The frame events of a kept call are applied with a
context that cancellation does not reach: "Esc after a call kept its pop or
push must not lose them." The same for a call whose reply was lost to a
worker death and found kept (`machines.md` §9.5).

---

## 12. How it meets the rest of the harness

### 12.1 Summaries and compaction

- Compaction "stays for the two cases the discipline can miss: a step whose
  own work is bigger than the window, and a model that stops popping". With
  the slide it runs only "with no frame to cut, or no body of more than one
  round before the prompt".
- **The stack survives a summary.** "A frame still open when the context
  fills is compacted as today; the stack survives it […], and its next pop
  removes everything after the summary […]."
- Rows at and before the last summary are neither counted nor cut by a later
  pop or slide (they were not in the context).
- After a summary the session is given the mem digest again, and mem is told
  of the summary first so that recall may repeat what it gave before.
- One wording to settle: a summary tells the model where the "full
  transcript" is; after pops that file holds the model's list, not the full
  one.

### 12.2 The provider's prompt cache

"The model has no memory but the message list gnosh sends each round, and
the server's prompt cache keeps the computed state of every prefix. A pop
removes everything after one point and keeps the pop there, so the prefix is
intact through the push and the next request reads the pop and what
follows." [D]

- **A push costs nothing on the server.** "It is a marker gnosh keeps."
- **A pop keeps the prefix** through the push's call. The note is appended at
  the end of the push's answer, so "the cache loses only a few tokens".
- Measured (README, llama.cpp log of a real run): "the request after a
  3K-token pop re-read 2,572 tokens instead of the whole 20K-token
  conversation, and the requests after it were back to a few hundred each".
- On a model with recurrent layers (the box's Qwen: 49 of 65) the server
  restores the nearest checkpoint below the cut, taken every 8,192 tokens of
  reading: "at most 8K tokens re-read once". All-attention models re-read
  nothing.
- **A slide cuts early rounds**, so "everything after the cut is re-read".
  That is why it cuts enough (at least half the body) to be rare.
- On a provider with explicit cache breakpoints, a hit on the prefix through
  the push "needs a cache breakpoint set at or before it and still live". A
  prefix-cache test for each provider family is in the design's test list.
- A hidden prompt row inserted mid-turn (the mem reminder, the slide line,
  the large-step line) breaks the prefix cache on some chat templates
  (`mem.md` §14.3). Not fixed.

Contrast (HOLD.md): another agent's automatic clearing of old tool results
"from the middle of the conversation … invalidates the prompt cache from the
first cleared result on". A pop cuts at the end.

Why the cut matters on a home GPU (measured on the AI box, 2026-11-01): a
real summary there is a median 4,504 tokens (24 summaries; 39 to 13,786) and
takes about 5.6 minutes to write at 13.3 tokens/s. Reading new tokens runs
at 267–291 tokens/s under 10K of context and 53–70 above 120K. Of the
box's requests with over 50K of context, summary writes were 40% of the GPU
time; a frames counterfactual for the same requests came to about 2.3× less.
"The honest realistic figure for a home GPU is about 2× on sessions that
grow big."

### 12.3 Sub-agents

- A sub-agent is a session of its own with its own (empty) stack. The stock
  sub-agent has no `gnovm_run`, so no frames.
- A sub-agent given the tool pushes and pops in its own conversation; its
  mem requests are marked as a sub-agent's, and its Next never waits
  (`peers.md` §4).
- It gets no frame line, mem line, mem reminder or goal driver. (The slide
  and the large-step line follow the stack, so they would reach a sub-agent
  standing in a frame; unconfirmed whether that was meant.)
- For the close review, a sub-agent's report "counts as a tool's output".

### 12.4 Resumed sessions and restarts

The stack is derived again from the rows; the model stands where it stood;
the next typed prompt's frame line says so. Flagged rows stay out of the
model-facing list.

### 12.5 Branch and rewind

- **A branch, and a rewind copy, start at the root** (§3.4); they carry the
  flags.
- **Rewind** itself is unchanged: its points are the user's prompts; one
  inside a popped range is still a point; after a rewind the stack is
  derived again from what remains. It leaves mem alone.

### 12.6 A frame mem already holds closed

After a rewind into a popped range, or an undo, the session can stand in a
frame mem holds closed. Then Pop, Done or Leave on it takes it off the stack
with no change in mem: "`x` was already done → result: it is off your stack
now" [K]. (Next only says so: "`x` is done already …: mem.Pop("…") takes it
off your stack.") The harness applies that pop event like any other.

### 12.7 Peers

Each session has its own stack and its own cut. What peers add: a session's
stack is also written to its peers record under mem's lock by each of its
calls, so mem can tell who works on what (`peers.md` §6). A frame on a
session's stack that another session has since taken (a soft hold that
lapsed) is refused at the pop, with "mem.Leave() steps out of it".

### 12.8 Erasure

`mem.Redact` reaches the frame's kept result and the journal's statement;
"the pop call's own text in the session is not, and gnosh names the session
as still holding it". A Redact that would rename a frame on the stack is
refused until the frame is popped.

### 12.9 Output streams

A non-interactive run that streams messages must not print rows again when a
pop flags them (each flagged row arrives as an update). Before the fix a
turn's stream "holds turn 1's ls, grep, view … in the middle of turn 3", so
a report showed work that never happened.

### 12.10 The user's view

- "The pop's tool result in the chat, which is the notice; no second row and
  no change to the removed rows in v1."
- The task list's in-progress line reads "in `id` "purpose"".
- `/mem log` lists each push and pop with the entry that made them.
- The header's context figure drops at the next answer.
- "Nothing is deleted: you still see all of it, the transcript keeps it, and
  nothing the agent did is undone. Only the model's context shrinks." [G]

---

## 13. Worked examples

### 13.1 The walkthrough the docs show [G]

```go
mem.Todo("rename the project").Push()   // stack: rename-project · tree: rename-project
mem.Todo("rename it in the code")       // stack: rename-project · tree: rename-project › rename-code
mem.Todo("rename it in the tests")      // stack: rename-project · tree: rename-project › rename-code, rename-tests
mem.Next()                              // stack: rename-project › rename-code
mem.Pop("go.mod and main.go say gnosh") // stack: rename-project · rename-code is done; its work leaves the context
mem.Next()                              // stack: rename-project › rename-tests
```

Or the plan in one call: `mem.Todo("rename the project", "rename it in the
code", "rename it in the tests").Push()`.

### 13.2 The list before and after a pop [D, respelled]

Inside `fix-flaky/find-cause` (`fix-flaky/reproduce` already popped):

```
… [user] [assistant: Todo(...).Push()] [result: in `fix-flaky`]
[assistant: Next()] [result: in `fix-flaky/reproduce`]
[assistant: Pop + Next] [result: done `fix-flaky/reproduce` …; in `fix-flaky/find-cause`]
[assistant: view] [result: 900 lines] [assistant: "which port?"] [user: "8080"] [assistant: bash] [result: 12 runs] …
```

After `mem.Pop("…")` and `mem.Next()` in a later turn:

```
… [user] [assistant: Todo(...).Push()] [result: in `fix-flaky`]
[assistant: Next()] [result: in `fix-flaky/reproduce` + note]
[assistant: Pop + Next] [result: done `fix-flaky/reproduce` …; in `fix-flaky/find-cause` + note]
[assistant: Pop + Next] [result: done `fix-flaky/find-cause` …; in `fix-flaky/rerun`]
[assistant: carries on]
```

"The '8080' is gone from the model's context with the question; if the rerun
needs it, the model wrote mem.Think("port", "the user says the shared port is
8080") before popping, or put it in the result." Had "8080" been said in the
same turn as the pop, it would stay (§4.2).

### 13.3 A real pop (rename run, session two, 2026-10-31) [G]

```go
mem.Frame("klash-to-gnosh-rename/gnosh-readme-rename").Push()
// in `klash-to-gnosh-rename/gnosh-readme-rename` "rename klash to gnosh in README.md": do it now, …
// … the README read, edited and searched; then
mem.Pop("README.md fully renamed klash→gnosh (title, prose, build/usage snippet, config file mentions incl. gnosh.toml and gnosh.example.toml); case-insensitive grep for klash finds nothing")
// done `klash-to-gnosh-rename/gnosh-readme-rename` "rename klash to gnosh in README.md" → README.md fully renamed …
// next: `klash-to-gnosh-rename/gnosh-build-output-rename` "…": mem.Frame("klash-to-gnosh-rename/gnosh-build-output-rename").Push()
// Everything since you entered `klash-to-gnosh-rename/gnosh-readme-rename` was removed from your context: this result stays; the user still sees all of it. In it you edited README.md. Your reads inside it are forgotten: view a file before you edit it.
```

### 13.4 Numbers from real runs

- Eight functions, one todo each, thinking off: "the eight pops freed about
  11.6K tokens, and the session's last request was ~17K tokens where ~29K
  would have piled up."
- Per-pop sizes seen: 346 tokens to 11K.
- What a pop leaves in the context: the model's pop line (~80 tokens) and
  mem's reply (210–270 tokens), in place of the frame's work.
- The head-to-head explainer replays a real rename run in a shrunken window:
  the ordinary agent finishes at 16:13 of GPU time (12:22 of it stopped for
  two summaries), gnosh at 4:20. The window and the two summaries are
  illustrative; the calls are real.

---

## 14. Edge cases settled, and still open

**Settled** (each has a test in the earlier tree's suite; write the same):

1. Trampoline: a task with three todos, entered and popped in turn; after
   each pop the list is the pushes, the pops and the present todo's work;
   the last Next is the kept no-op whose text says to pop the task.
2. Nested: pop the task after its todos; its push and its pop remain.
3. A side quest: a new todo pushed inside a todo, popped, carry on.
4. Words inside a frame across turns: the model asks, the turn ends, the
   user answers, a prompt is folded in, a notice arrives; the stack is the
   same across the turns; the pop removes earlier turns' words and spares
   this turn's; a thought written before the pop is found by name after.
5. Resume and restart inside a frame: the stack is derived again.
6. Compaction inside a frame: the next pop removes everything after the
   summary; the summary and its continuation stay.
7. A parallel sibling of the push: its result stays in the push's round.
8. Pop then Next in one input: both applied in order, two events; a refusal
   later in the same input discards the pop too.
9. Done and Drop on the present frame pop and cut; Leave cuts and leaves it
   open.
10. Branch and rewind copies carry the flags and start at the root; a rewind
    into a popped range derives the stack from what remains.
11. Reads and views: `cat` and view inside a popped frame; the next edit is
    refused until a view, which returns the file.
12. A pop on the round that crosses the compaction point: the subtraction
    keeps it under the point.
13. No round between push and pop: the "nothing came between" text, nothing
    flagged, nothing forgotten.
14. Prefix cache: the request after a pop shares the prefix through the
    push's call.
15. The slide: instead of compaction; lower frame first; before the prompt;
    before a hidden prompt; the rounds before the first frame go first; when
    compaction gave up; rows behind a summary neither counted nor cut; a
    frame popped and pushed again is not "slid".
16. The fresh guard: counts work since the plan; a refused pop, a mem read
    and a failed call are no work.
17. The pop spares this turn's prompts; a cancelled turn ends where it
    stopped.
18. The note is the same in the pop's turn, later turns and a summary.
19. Frame events survive Esc and a lost reply.

**Known and left** (each minor unless said):

- "A spared count can disagree with the list until the next run", when the
  turn has a row from a user-run shell command or a row a slide cut.
- "A spared prompt's frame line still names the frame that was popped."
- A pop's sparing covers only its own turn. "The pop's sparing should cover
  every prompt that arrived while the model stood in the frame" was argued
  in QA and not adopted; Jae's ruling stands that earlier words are a
  thought's job. See `open-questions.md` Q2.
- The 32K overflow (§8.8).
- Mid-turn hidden rows and the prefix cache (§12.2).
- A model may still write a result before the work; the guards catch it but
  cost rounds. On short tasks gnosh was 1.3–4× slower than Crush at a 64K
  window where nothing needed cutting: "one step per reply", the
  verification discipline, and a larger fixed prompt (`open-questions.md`
  Q4).
- Not built from the design's phase 4: folding or marking popped rows in the
  chat; self-trimming of a stack; compacting only the present frame's
  range; suggesting a frame when a tool result is very large.

---

## 15. Record: Hold and Release (superseded 2026-10-30)

Before frames, the design was two calls: `mem.Hold("label")` then
`mem.Release("result")`: a side quest whose steps are cut from the model's
context, keeping only the release text. Four drafts, three review rounds;
nothing built. Jae replaced it the same evening with the frame stack: "one
model, not two" (the plan is the context's shape).

What carried over into frames, and is in this file: the cut applied on every
request by call ids; a flag on rows; reads and views forgotten; state in the
harness with mem only journaling; no length refusal and an empty result
refused; the estimate shown to the user, not the model; the compaction
transcript being the model's list while the goal judge reads the full list;
no "pop now" line before compaction; the vocabulary "removed from your
context".

What did not: one hold at a time (frames nest); a hold ending with its turn
(a frame spans turns); a hold never auto-released on the user's words
(replaced by the pop's rule on words).

Rejected alternatives recorded there, still rejected: rewriting the push's
tool result with the result text; moving removed rows to a side session; a
dedicated tool pair instead of mem calls ("two more tools, no journal");
auto-popping at a turn's end; an exact token figure for the model; push and
pop as journal Ops; a stack kept as its own store; `Start` as a push;
refusing a Next with nothing to enter; one push or pop per input.
