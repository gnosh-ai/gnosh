# integration-contract.md — what the harness must do for machines, mem and frames

What this file is: the list the clean side builds from. Every hook-up point
between the harness and the carried subsystems, each with the required
behaviour and a priority. The detail behind each line is in the file named
beside it.

**Priorities**

- **P0** — needed for a first working run of `mem` with frames: the model
  can plan in mem, push, pop, and the popped work leaves what it is sent.
- **P1** — needed before Jae switches over from the earlier gnosh: nothing he
  relies on is missing, and the guards that keep mem true are in.
- **P2** — later.

**The shape of the seam.** Two things meet here:

1. **The carried code** (`internal/gnovm/`, plus `internal/gnochain/`,
   `internal/swarm/`, `gno/r/gnosh/mem/`): Jae's side's own, brought over as
   it is. It offers a small API (§1) and needs a few things supplied (§2).
2. **The harness-side behaviour** that used to live in files derived from
   another project and so is rebuilt from words: deriving the stack, the
   cut, the slide, the checks, the lines on prompts, the drivers, the
   commands (§4 onward).

---

## 1. What the carried code offers

All in package `gnovm` unless said. These are gnosh's own interfaces.

**Process and lifecycle**

| Call | Use |
|---|---|
| `WorkerMain() int` | the worker's main; run it when the program is started as `gnovm-worker` |
| `NewLazy(ClientOptions) *Lazy` | one per app; holds the process's client, made on first use |
| `(*Lazy).Get() (*Client, error)` | the client; a failure is not remembered |
| `(*Lazy).Close()` | release at shutdown |
| `ClientOptions{Options{Dir, Embeddings, BudgetBytes, DefaultMemoryMB, DefaultGas, NsPerGas}, InProcess, Binary}` | `Dir` is required: the gnovm data directory. `Embeddings` is the embedding server URL or "". |
| `NewHost(Options)`, `(*Host).Close()` | an in-process host, for the read-only CLI |
| `AttributionNotice` | the notice text (`machines.md` §12) |
| `ImportablePackages() []string`, `ScratchName(session) string`, `NewClientID() string` | helpers |

**Machines and mem calls** (on `*Client`)

| Call | Use |
|---|---|
| `Run(ctx, RunRequest) (*RunResult, error)` | every `gnovm_run`. For mem it waits for peers when the input may wait, and calls again by itself after a catch-up. |
| `New`, `List`, `Drop`, `Repair` | the other tools and `/mem repair <vm>` |
| `Kept(ctx, project, name, req)`, `KeptFrames(ctx, project, req)` | was a call kept, after the worker died before replying; for mem, with the frames it pushed and popped |
| `MemCommand(ctx, project, session, args, titles) (text, *MemErasure, error)` | `/mem` |
| `Deaths() []string` | why workers ended |

**mem for the session** (on `*Lazy`)

| Call | Use |
|---|---|
| `MemBlock(project, session) string` | the digest for a session; also notes what it showed, so recall does not repeat it |
| `MemChanged(project, session, since) string` | the digest again, only if mem changed after `since` |
| `MemRecall(ctx, project, session, prompt) string` | the recall block for a typed prompt, or "" |
| `MemSummarized(session)` | the session was summarized: recall may repeat |
| `MemNotice(project) string` | the one line for the user: what is due, what waits on them |
| `MemStatusPath(project) string` | where `status.json` is, for hooks |
| `HoldTurn(ctx, session) func()` | hold the session's turn lock; returns the release |

**The goal table** (on `*Client`)

| Call | Use |
|---|---|
| `BindGoal(ctx, project, session, condition, prev) (GoalBinding, directive, error)` | `/goal <condition>` |
| `GoalTurnEnd(ctx, project, session, binding, stopMarker) (GoalTurn, GoalBinding, error)` | what to do as a turn ends |
| `GoalCheckpoint(binding, continued, stopMarker) string` (a package function) | the every-tenth question |
| `GoalDirective(binding) string` (a package function) | the directive text; `BindGoal` already returns it |
| `GoalReopen(ctx, project, binding, reason) error` | the judge said no |
| `ClosedGoals(ctx, project, session, since, except) ([]ClosedGoal, error)` | the top-level todos the session closed as done this turn |
| `GoalClear(ctx, project, session, binding) (notice, error)` | `/goal clear` |

**The read-only CLI**: `(*Host).MemRead(ctx, project, command, args, titles)
(text, refused, error)`.

**Types that cross the seam**: `RunRequest`, `RunResult`, `MemFrame`,
`MemClose`, `MemGoal`, `MemStep`, `MemRule`, `MemErasure` (with `Scrub` and
`Secrets`), `MemWait`, `GoalBinding`, `GoalTurn`, `ClosedGoal`, `VMInfo`,
`VMOptions`, `ListResult`, `NewRequest`. The fields of `RunRequest` the
harness fills are in §5.2.

---

## 2. What the carried code needs supplied

| # | Need | Detail | P |
|---|---|---|---|
| 2.1 | **Re-point the imports** between the carried packages (they name each other under the earlier module path). | Mechanical. | P0 |
| 2.2 | **A file-lock helper.** | Used by the VM host, the client lock, mem's lock and the turn locks. It must offer: a try-lock on a path that returns at once, with a distinguishable "contended" error or a release function; and a lock that **waits in the kernel**, takes a context so cancellation stops the wait, and returns a release function. A wait given up on must not leak the file. Lock files are created as needed and never unlinked by the helper. Where the platform has no blocking file lock, polling is acceptable. The exact names and signatures are at the call sites in the carried code. (`peers.md` §2) | P0 (try-lock and a correct blocking lock); P1 (kernel wait, cancellable) |
| 2.3 | **The program's build id.** | One string, compared in the worker handshake so a worker from another build is refused. | P0 |
| 2.4 | **The tools adapter.** | The carried `tools/` directory is written against the earlier harness's tool interface, its permission prompt, its session task-list type and its way of reading the calling session's id. Write the four tools again against the new tool interface, from `machines.md` §9 and §5 below. Keep the tool names, argument names, result text and error text exactly. | P0 (`gnovm_run`); P1 (the other three) |
| 2.5 | **Third-party modules**: the gno modules (the VM and its storage), a SQLite driver with full-text search (the carried code has two build variants), a semantic-version helper. The embedded Gno standard library is regenerated from the pinned gno version by a generate step. | Pin the same gno version the carried `stdlibs/VERSION` names, or regenerate and re-run the layout test. | P0 |
| 2.6 | **The licence files and notices** of the carried directories stay with them; the notice shows where the program names itself. | `machines.md` §12 | P1 |
| 2.7 | **The carried tests.** Some run the real program end to end (the scenario suite: txtar files driven through a non-interactive run, reading the session store and the stream output); one replays the examples in GNOMEM.md and the README's mem sections byte for byte. | They need: a non-interactive `run` with a per-run data directory, a model flag, a session name (so no title request is made), a quiet flag and a streamed message output; the docs carried too. Expect to adapt the harness-facing parts. | P1 |

---

## 3. Process level

| # | Hook-up | Required behaviour | P |
|---|---|---|---|
| 3.1 | **Worker dispatch** | At the very start of the program's entry point, before any CLI parsing, config or environment-file loading: if the first argument is `gnovm-worker`, run the worker main and exit with its code. | P0 |
| 3.2 | **Client lifecycle** | One `Lazy` per app, built with the gnovm data directory and the embedding URL. Nothing starts until a VM or mem is first used. At shutdown, close it **after** the session store is flushed. In a multi-workspace server the process shares one client (the carried code counts references). | P0 |
| 3.3 | **The gnovm data directory** | `gnovm/` beside the harness's global data file: `<XDG data>/gnosh/gnovm/`. Not inside any project. Honour `XDG_DATA_HOME` (tests and benches isolate with it). | P0 |
| 3.4 | **Existing mems must open** | Jae's mems on the AI box and the Mac were written under this directory, some under earlier product names. Find them, or move them once. | P1 (`open-questions.md` Q9) |
| 3.5 | **Protected path** | File-changing tools always ask before touching the gnovm data directory. The model is told never to read mem's files or the harness's own data. | P1 |
| 3.6 | **Never spawn a worker under test** unless a test names a binary that is not itself. Harness tests use the in-process host. | P0 |
| 3.7 | **No tool runs beside `gnovm_run`.** | A `gnovm_run` call runs alone and in order: every row of the rounds before it is stored before it runs, and its own effects are applied before the next call. | P0 |

---

## 4. The system prompt, and what goes on prompts

| # | Hook-up | Required behaviour | Detail | P |
|---|---|---|---|---|
| 4.1 | **The memory section of the system prompt** | Tell the model what `mem.md` §14.1 lists. Write new wording. Keep it out of anything that changes per session. | `mem.md` §14.1 | P0 |
| 4.2 | **Nothing volatile in the system prompt** | The date, working directory and git status go with the first prompt, not in the system prompt, so a local server reuses the prompt it has read. mem's digest follows the same rule. | `machines.md` §10 | P0 |
| 4.3 | **The digest with a new session's first prompt** | For a top-level session with no earlier prompt: call `MemBlock`; if not empty, store it as a hidden part of that prompt's message, after the prompt text and after anything session-start hooks added. | `mem.md` §10 | P0 |
| 4.4 | **The mem line** | Last hidden part on every prompt the user types (and every channel message) in a main session whose tools include `gnovm_run`. Not on hidden prompts. Not for sub-agents. | `mem.md` §14.2 | P0 |
| 4.5 | **The frame line** | With the mem line, while the session's stack is not empty; computed as the prompt goes out (also for a prompt folded into a running turn). Carries the slide's line when one applies. | `frames.md` §7, §8.6 | P0 |
| 4.6 | **The digest again when mem changed elsewhere** | On a typed prompt of a top-level session that already has prompts: `MemChanged(project, session, time of the session's newest message)`; add it the same way if not empty. | `mem.md` §10.1 | P1 |
| 4.7 | **After a summary** | Call `MemSummarized(session)`, then include `MemBlock` in what the session is given back after the summary. | `mem.md` §10.1 | P1 |
| 4.8 | **Recall** | On each typed prompt and channel message, as it goes out (start of a turn or folded in): `MemRecall`; add the result as a hidden part. It returns "" within half a second when there is no server. Never on hidden prompts or for sub-agents. | `mem.md` §11 | P2 |
| 4.9 | **The mem reminder** | After ten model replies with no mem call and no reminder (since the last summary), a hidden reminder before the next reply. | `mem.md` §14.3 | P1 |
| 4.10 | **The notice to the user** | `MemNotice`: in the UI's status area at start, with the first prompt of each new session, and with the first prompt of a new day; on stderr before a non-interactive run starts work. | `mem.md` §10.3 | P1 |
| 4.11 | **The Gno-project rule** | In a project with Gno code, the prompt adds the audit-todo rule. | `mem.md` §14.1 item 18 | P2 |
| 4.12 | **Plan mode** | No `gnovm_run`; the digest and recall still go with prompts; "implement the plan" says to put the plan on mem first (planned in MEMPLAN; unconfirmed that it landed). | `mem.md` §17 | P2 |

All hidden parts are **stored with the message**, in the order they were
sent, so a conversation rebuilt from the store is byte-identical to what
went out.

Where this set says "system reminder" it means text the harness adds on the
user's side of the conversation, marked the way the harness marks its own
words so the model can tell them from the user's, and not shown in the chat.
The marking is the harness's to choose (it is not particular to mem); what
matters here is that it is the same every time and is stored as sent. The
digest and the recall block come from the carried code already wrapped
(`<mem>…</mem>`), and go in as they are.

---

## 5. The `gnovm_run` tool

### 5.1 Registration

| # | Required behaviour | P |
|---|---|---|
| 5.1.1 | Register `gnovm_run`, `gnovm_new`, `gnovm_list`, `gnovm_drop` under exactly those names; add them to whatever list of known tool names the harness filters agents' tool sets by. | P0 / P1 |
| 5.1.2 | The main agent gets all four; the plan agent `gnovm_list`; the stock sub-agent none. | P0 |
| 5.1.3 | Hooks and permission rules see the tool as `gnovm_run` with its JSON arguments. `gnovm_drop` with `action: "delete"` asks the user through the permission prompt. | P1 |

### 5.2 Building the request

For every call: `Project` (the session's project directory), `Session` (the
calling session's id), `Name`, `Code`, `Gas` (the JSON number as an
integer), `Reset`, **`Req` = the tool call's id**, `SubAgent` (true for a
sub-agent's session).

For a call whose `name` is `"mem"`, also:

| Field | From | Detail | P |
|---|---|---|---|
| `Frames` | the session's stack, bottom to top (§6.1) | `frames.md` §3 | P0 |
| `Goal` | the name of the todo the session's `/goal` binds, or "" | `mem.md` §13 | P1 |
| `Fresh` | when the stack is not empty and the code contains `Pop(` or `Done(`: the top frame's id if it is fresh, else "" | `frames.md` §9 | P1 |
| `Names` | names the small model made for the code's unnamed texts, by text (§5.5) | `mem.md` §4 | P1 |
| `CheckCloses` | true when the close check is wired (§8) | `close-check.md` §2 | P1 |
| `Checked` | the closes approved so far in this call | same | P1 |
| `WaitPipe` | filled by the carried client; leave it | `peers.md` §5 | — |

With `Fresh`, `Names` and `CheckCloses` left out the tool still works: mem
names from the words and refuses nothing as fresh. That is the P0 run.

### 5.3 The result

| # | Required behaviour | Detail | P |
|---|---|---|---|
| 5.3.1 | Render the result text exactly: value, output, frame clauses, one status line, notes; the two error prefixes; the `Next:` hints; cut at 30,000 characters. | `machines.md` §9.5 | P0 |
| 5.3.2 | A failure of the code is an **error result** the model reads; it is not a harness error. | | P0 |
| 5.3.3 | **Keep the frame events with the result**: when `RunResult.Frames` is not empty, store it as the result's metadata under `frames`. The metadata is persisted with the result row and copied when the row is copied. | `frames.md` §3 | P0 |
| 5.3.4 | **After a kept call, with a context that cancellation does not reach**, in this order: (a) hand the frame events to the frames layer and put the text it returns into the result (§6.2); (b) hand `MemGoals` to the task list (§10); (c) hand `Erased` to the scrubber (§11.2); (d) hand `Rules` to the user notice (§11.1). | `frames.md` §11 | P0 (a); P1 (b–d) |
| 5.3.5 | **The close-check loop**: while the result has an error and `Closes`, check the not-yet-approved ones; on a refusal return it as the call's error with no output; else add them to `Checked` (by id and result, and by id with any result) and run the same request again. Afterwards tell the close check whether the call was kept. | `close-check.md` §2 | P1 |
| 5.3.6 | **A worker that died before replying**: ask `Kept` (a VM) or `KeptFrames` (mem) and say which; for mem, apply the frames as in 5.3.4. | `machines.md` §9.5 | P1 |
| 5.3.7 | **A waiting call may take minutes.** No tool timeout on `gnovm_run`; the user's cancel ends it. | `peers.md` §10 | P1 |

### 5.4 How it shows

| # | Required behaviour | P |
|---|---|---|
| 5.4.1 | The call is headed by its Gno code, highlighted as Go, shown whole; the VM's name leads unless it is `mem` or scratch. | P2 |
| 5.4.2 | In a non-interactive run's streamed output, a row that is only being flagged as popped is not printed again. | P1 |

### 5.5 The namer

Before a mem call, find the unnamed text literals (`mem.md` §5.9). If there
are any, ask the small model for names and pass them by text.

- One text: ask for one name. Several: **one ask for all**, one name per
  line in the same order; the names must differ. The instruction that won
  when tested (2026-10-31, temperature 0, seven near-alike rename tasks):
  name each by its own subject, the thing it is about and then the action;
  no two names the same; when lines are alike, the word that sets a line
  apart goes first. An earlier instruction ("leave out what they all share")
  made the model drop the subject itself.
- A name is two to four lowercase words joined by dashes. Clean the answer:
  first line only (or one per line; a numbered or bulleted line counts);
  strip quotes and punctuation; keep `[a-z0-9]` words; at most four words
  and 32 bytes; it must start with a letter; else no name.
- **Match names to texts** when several were asked: a name fits a text by
  the words they share (the same word, or one beginning the other with four
  letters or more). If another order fits better in all, take it (try every
  order up to seven texts). A name that shares no word with its text is
  dropped. (A small model answered in another order and swapped a task's
  name with its todo's.)
- **Keep made names per session by text**, so the same text gets the same
  name when a refused call is sent again. (Without it a refusal named a todo
  the retry no longer made: 37 refusals in 10 minutes.)
- Small model first, then the large one if it fails; no tools; thinking off;
  a short answer budget (about 24 tokens for one name, about 16 per name
  for several); a timeout of about 30 s plus 10 s per text.
- On any failure pass no names: mem names from the words. The journal keeps
  the final name, so nothing else depends on the model.

P1. (P0 may skip the namer.)

---

## 6. Frames

### 6.1 The stack

| # | Required behaviour | Detail | P |
|---|---|---|---|
| 6.1.1 | Derive a session's stack and its pop ranges from its own result rows' `frames` metadata, counting only events stamped with this session's id. Cache per session; update as calls happen. | `frames.md` §3 | P0 |
| 6.1.2 | Drop the cached record when rows are removed or the session is rewound; derive again on next use. | §3.5 | P1 |
| 6.1.3 | A branch and a rewind copy carry rows, metadata and popped markers, and start at the root. | §12.5 | P1 |

### 6.2 On a pop event

When the tool hands over events (5.3.4a), for each **pop** of this session:

| # | Required behaviour | Detail | P |
|---|---|---|---|
| 6.2.1 | Flush pending row writes; read the session's rows. | §11 | P0 |
| 6.2.2 | Compute the range between the push round and the pop round; take out the prompt rows of the pop's own turn. | §4.1–4.3 | P0 (the range); P1 (the sparing, the turn-start rule) |
| 6.2.3 | Flag the removed rows with the frame's id (the `popped` marker), publishing each change. | §11 | P0 |
| 6.2.4 | Forget the reads of every path the removed rows touched, the views given in them, and the subdirectory context files given in them. | §6 | P0 (reads); P1 (views, context files) |
| 6.2.5 | Estimate the removed tokens and add them to the session's pending estimate. | §10 | P1 |
| 6.2.6 | Work out the range's note once and keep it with the range. | §4.4 | P1 |
| 6.2.7 | Return the clauses for the tool result: removed; thoughts; files edited; other mem changes; reads forgotten; or "nothing came between". | §5 | P0 |
| 6.2.8 | Tell the user: "popped `x`: about N tokens out of the model's context". | §10 | P1 |

A **push** only appends to the stack (with the call's id, and the session's
token total at that moment for 6.5.3).

### 6.3 Building each request

| # | Required behaviour | Detail | P |
|---|---|---|---|
| 6.3.1 | The model-facing history skips every row flagged popped. | §11 | P0 |
| 6.3.2 | Within a turn, before every model call: put back what earlier steps injected (folded prompts, reminders) at its recorded position; drop the messages inside every range of this session, keeping the spared prompt messages; append each range's note to its push's answer. | §4, §11 | P0 (drop); P1 (notes, sparing) |
| 6.3.3 | The list stays valid: no call without its result. The provider's cache marks are placed on the cut list. | §11, §12.2 | P0 |
| 6.3.4 | A summary's request is built from the same cut list with the same notes. | §12.1 | P1 |
| 6.3.5 | Human-facing readers (chat, export, rewind's scan, erasure scans) and the goal judge read the full list. | §11 | P1 |

### 6.4 The stored flag

One marker per stored row: empty, a frame id, or a slide mark
`slide:<frame>:<push call id>:<pop call id>`. Set by row id. Copied with the
row. Never set on a summary row. (P0)

### 6.5 The slide, the estimate, the large-step line

| # | Required behaviour | Detail | P |
|---|---|---|---|
| 6.5.1 | Keep a per-session pending estimate; use (total − estimate) for every compaction and room decision; spend the estimate when a usage report arrives for a request sent with the cut. | §10 | P1 |
| 6.5.2 | At the two compaction points, a session in frames slides when it can; a summary is made only when nothing can slide. The slide is tried whatever the compaction level. | §8 | P1 |
| 6.5.3 | The "this step is large" line, once per frame. | §8.7 | P2 |
| 6.5.4 | The slide's line reaches the model by the frame line or a hidden reminder; the user is told. | §8.6 | P1 |

### 6.6 The fresh guard's input

Compute `Fresh` for 5.2 from the rows between the top frame's push round and
the present call's round, by the definition of work in `frames.md` §9. (P1)

### 6.7 The task list's frame

While the session stands in a frame, the task list's in-progress line reads
"in `id` "purpose"". (P2)

---

## 7. Peers

| # | Hook-up | Required behaviour | Detail | P |
|---|---|---|---|---|
| 7.1 | **The turn lock** | Call `HoldTurn(ctx, session)` when a prompt makes a session busy, with the run's own cancellable context; call the release only when the session goes idle, after any hand-over to a queued prompt. Runs started from inside a run share the hold (the carried code counts). Idle compaction does not take it. Sub-agent sessions take their own. | `peers.md` §6 | P1 |
| 7.2 | **Sub-agent flag** | `RunRequest.SubAgent` for a sub-agent's session. | §4 | P1 |
| 7.3 | **Session id on every mem request**, and the stack (already in §5.2). | §6 | P0 |
| 7.4 | **Separate session stores per process, or tolerate lock retries.** | §10 | P2 |
| 7.5 | **The prompt rules for peers.** | `mem.md` §14.1 item 8 | P1 |

Without 7.1 nothing breaks for one session, but a plan pushed by an ended
session never frees up (`close-check.md` §6) and Next never waits.

---

## 8. The close check

| # | Hook-up | Required behaviour | Detail | P |
|---|---|---|---|---|
| 8.1 | **The check callback** | Given (session, call id, closes): for each, entered-here; the claim check; the review. Return "" or the first refusal. | `close-check.md` §3 | P1 |
| 8.2 | **The window** | Rows from the start point to the closing call, plus the calls before it in its own round; the start time; the files named; the user's ask. | §3.2 | P1 |
| 8.3 | **The review call** | Small model, no tools, thinking off, temperature 0, 1,500 answer tokens, 3-minute timeout; the header and transcript; the JSON verdict; fail open with a warning to the user; a cancelled turn is not a pass; the verdict cache. | §3.4 | P1 |
| 8.4 | **Kept or not** | After a checked call, learn whether mem kept it; a reviewed top-level todo in a kept call is not judged again at the turn's end. | §5.4 | P1 |
| 8.5 | **The turn-end send-back** | Once per turn, when the turn would end in a frame whose last close was refused. | §5.1 | P1 |
| 8.6 | **Loop detection** sees mem loops: blank the numbers in mem's answers; one or two distinct steps in a ten-step window is a loop; a refused close gets its own nudge; no cap ends the turn. | §5.2 | P1 |
| 8.7 | **The option** `mem_review_closes`, default true. | §7 | P1 |

---

## 9. `/goal`

| # | Hook-up | Required behaviour | Detail | P |
|---|---|---|---|---|
| 9.1 | **Where the binding lives** | A JSON `GoalBinding` stored with the session, in a field of its own, read and written by compare-and-set (old text → new text). Ordinary session updates must not overwrite it. | `mem.md` §13 | P1 |
| 9.2 | **The command** | `/goal <condition>` (at most 2,000 characters): make a session if there is none, `BindGoal`, store the binding, say "Goal set: … (`name` in mem)", and send the directive as a hidden prompt (queued if the agent is busy). `/goal`: show condition, name and last verdict. `/goal clear|off|reset|none|cancel`: `GoalClear`, unbind, show its notice. | | P1 |
| 9.3 | **The flag** | `run --goal <condition>` binds before the prompt is composed, prints how the goal ended, exits non-zero while it is still set. | | P1 |
| 9.4 | **The bound name on every mem call** (`RunRequest.Goal`). | | P1 |
| 9.5 | **At each turn's end** of a main session, not in plan mode, after the close-check send-back: (a) judge every top-level todo the session closed as done this turn (`ClosedGoals`), except the bound one and those the review approved; (b) for the bound todo call `GoalTurnEnd` and act on `do`: continue, checkpoint, end, judge, rebind, clear. Every tenth continuation in a row send `GoalCheckpoint` instead. A reply that ends with the stop marker ends the turn, goal still set. Store the binding it returns, compare-and-set; if it changed meanwhile, leave it as the user left it. | `mem.md` §13 | P1 |
| 9.6 | **The judge call** | Small model, no tools, thinking off, 1,500 answer tokens (asked once more with 6,000 when cut off); the condition, what mem shows, the last 24,000 characters of the **full** conversation; the JSON verdict with `basis`. Met: unbind, tell the user "Goal met: …". Not met: `GoalReopen`, tell the user, send the agent back. Unreachable: end the turn, goal set, tell the user. | `mem.md` §13 | P1 |
| 9.7 | **After a turn that ended in an automatic summary** with nothing further queued, the goal driver still runs. | | P1 |
| 9.8 | **Notices**: every outcome reaches the user (UI notice; stderr in a non-interactive run). | | P1 |

The stop marker is the same line the harness's stop hooks use (its text is
the harness specs' to fix).

---

## 10. The task list

After a kept mem call, show the last `MemGoal` the call changed as the
session's task list, by the mapping in `mem.md` §15. The task list is only
ever written from mem. There is no separate todo tool. (P1)

---

## 11. Rules and erasures

| # | Hook-up | Required behaviour | Detail | P |
|---|---|---|---|---|
| 11.1 | **Rules** | For each `MemRule` a kept call returns, warn the user with the rule's text and how to undo it. | `mem.md` §16 | P1 |
| 11.2 | **Erasures** | For a `MemErasure` (from a tool call or from `/mem`), scrub the harness's own copies in every session of the project: goal bindings, task lists, hidden parts after prompts, summary messages. Name the sessions whose visible messages still hold a redacted secret. **Never log or store the erasure itself.** | `mem.md` §16 | P1 |

---

## 12. Commands, options, environment, files

**Commands**

| Command | Behaviour | Detail | P |
|---|---|---|---|
| `/mem …` in a session, and "mem" in the command palette | Pass the arguments to `MemCommand` with the session id and a map of session id → title; show the text. **Never log the arguments.** Run the scrubber if it returns an erasure. | `mem.md` §12 | P1 |
| `gnosh mem [show\|find\|log]` | Needs no provider or model; in-process host; exit codes as specified. | `mem.md` §12 | P1 |
| `/goal …`, `run --goal` | §9 | | P1 |
| `gnosh gnovm-worker` | §3.1; not for users | | P0 |
| a **License** item in the command palette | shows the notices | `machines.md` §12 | P2 |

**Options**

| Option | Type, default | Meaning | P |
|---|---|---|---|
| `mem_embeddings_url` (rc: `mem-embeddings-url`) | string, unset | The embedding server; unset sends nothing anywhere. Passed to the client at start. | P2 |
| `mem_review_closes` (rc: `mem-review-closes`) | bool, true | The close review. | P1 |
| machine defaults (`memory_budget_mb`, `default_memory_mb`, `default_gas`, `ns_per_gas`, `in_process`), from the user's global config only | designed, never wired | `machines.md` §11 | P2 |

Every option needs a name in the rc file format too (a past test failed
without one).

**Environment**

| Variable | Meaning | P |
|---|---|---|
| `GNOSH_MEM_STATUS` | Given to hooks: the path of the project's `status.json`. Set even before mem is first used ("so `test -f` it"); reading it starts nothing. Its fields are a stable hook interface. | P1 |
| `XDG_DATA_HOME`, `XDG_CONFIG_HOME` | Decide where the gnovm data directory is. Tests that run the real program as the same OS user as a live lane must set both to temporary directories. | P0 |
| test and bench switches read by the carried tests (`GNOVM_SCENARIO_*`, `GNOMEM_V2_WRITE`, `GNOMEM_REBUILD_SEEDS`, `GNOSH_TEST_EMBEDDINGS`, `GNOSH_PEER_CHILD`, `GNOSH_PEERS_SWARM`, `GNOSH_PEER_WORK`, `GNOCHAIN_LIVE_*`) | Leave them to the carried tests. | — |

**Files** (all written by the carried code; listed so nothing else writes
there): see `machines.md` §5 and `mem.md` §8.

---

## 13. A small-model call, used three ways

The harness needs one facility: ask a model a short question with **no
tools**, a **cap on answer tokens**, an optional **temperature**, **thinking
turned off** where the provider has a switch, the **small model first and
the large one if it fails or is cut off**, a **timeout**, and the answer
read **past any thinking tags**.

| User | Answer room | Temperature | Timeout |
|---|---|---|---|
| the namer (§5.5) | ~24 tokens, or ~16 per name | the model's own | ~30 s + 10 s per text |
| the close review (§8.3) | 1,500 | 0 | 3 min |
| the goal judge (§9.6) | 1,500, and 6,000 once when cut off | the model's own | (unconfirmed) |

On a local server each such call is a new prompt and may push a saved
session out of the server's prompt cache; name sessions without a model call
where possible, and keep these calls few. On a provider whose thinking
switch the harness does not know, the judge thinks and may be cut off:
that is why the review has headroom and fails open. The goal judge was given
~300 and was cut off on long tasks (a FrontierHarness run with the
provider forcing reasoning on), so its close stood unchecked; it now has
the review's headroom and one retry with more.

(P1)

---

## 14. What "P0" comes to

A first working run needs exactly this:

1. the carried packages build in the new module (2.1–2.5);
2. the worker dispatch and the client lifecycle (3.1–3.3, 3.6–3.7);
3. `gnovm_run` registered for the main agent, building the request with
   project, session, call id and the stack, rendering the result, and
   keeping the frame events with it (5.1–5.3.4a);
4. the stack derived from rows (6.1.1);
5. on a pop: the range, the flag, reads forgotten, the clauses (6.2.1–6.2.4,
   6.2.7);
6. every request built without the flagged rows (6.3.1–6.3.3, 6.4);
7. the memory section of the system prompt, the digest with the first
   prompt, the mem line and the frame line (4.1–4.5).

With that a model can plan in mem, work a todo in a frame, pop it, and see
its context shrink; a new session starts from what is open.

What P0 leaves out, and why it matters before Jae switches: the note on the
push's answer and the fresh guard (without them a long session copies the
row of instant pops); the sparing of this turn's prompts (without it a pop
can cut the question being answered); the close check (about one close in
nine was false with thinking off); the slide (without it a full window still
means a summary); the turn lock (without it a plan pushed by an ended
session never closes); `/mem` and `/goal`.

---

## 15. What the user sees

There was **no "mem board" or tab view** in the earlier UI. What existed,
and what to build:

| Where | What | Detail | P |
|---|---|---|---|
| the chat | every mem call as a `gnovm_run` call headed by its Gno code, with mem's receipt and the pop's clauses as the result; popped rows stay visible, not dimmed or folded | §5.4, `frames.md` §12.10 | P0 (plain); P2 (the header) |
| the task list under the prompt | mem's todos for the last task a call changed; the frame the session is in | §10, §6.7 | P1 |
| the status area | the start notice (what is due, what waits on the user); "Goal met: …"; goal warnings; rule warnings; "popped `x`: about N tokens…"; "slid `x`…"; an unchecked close; where an erased secret is still shown | §4.10, §6.2.8, §9.8, §11 | P1 |
| `/mem` | text, as `mem.md` §12 | §12 | P1 |
| stderr of a non-interactive run | the same notices, one line each | | P1 |
| the header's context figure | drops at the next answer after a pop | `frames.md` §10 | P1 |

## 16. Swarm

Nothing in the harness calls the chain client or the realm. Carry them
(2.1); keep the demo runner as its own command. Everything else is P2 and
waits on Jae (`swarm.md` §8, `open-questions.md` Q5, Q6).
