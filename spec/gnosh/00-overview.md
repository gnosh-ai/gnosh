# 00-overview.md — gnosh's own core: "thinks in Gno", functional AI

What this file is: the idea in one page, the vocabulary, and a map of the
other files in this set. This set covers the part of gnosh that is Jae's
side's own invention: GnoVM machines, `mem`, frames (the context call stack),
the close check, peers, the swarm on gno.land, and the contract between all
of that and the harness around it. General harness behaviour (hooks, config,
CLI, trust) is in the `harness-*.md` files; Claude Code's public conventions
are in `spec/cc/`.

State described: the earlier gnosh at GitHub main `1c77d2bd` (2026-11-11),
mem layout 6. Where a later, unlanded branch matters it is named as such.

---

## 1. The idea

**The headline** (Jae, 2026-10-31): "An AI harness that thinks in Gno." He
chose it over "thinks, plans, and remembers in Gno": "i think 'thinks in gno'
is better than the 'plans, remembers'".

**The term** (Jae, 2026-10-31, said yes to the lede): "Gnosh is functional
AI: the agent's context is a call stack." Use "functional AI" wherever frames
are explained, in docs and in what the model is told.

In plain words:

1. **The Gno VM runs inside the harness.** The model can run Go-shaped code
   in small virtual machines with a memory cap and a gas limit, and gets the
   value and the printed output back. It works a sum out instead of guessing.
2. **The plan, the findings and the facts live in Gno, on disk.** One of
   those machines is special: `mem`, the project's memory. It holds the todos
   and the todos under them, what came of each, thoughts noted on the way,
   questions for the user, things that wait, facts, and the user's standing
   rules. Every change is journaled; the journal is the source of truth.
3. **The context is a call stack.** A todo is a *frame*. The model enters a
   todo (push), works, and leaves it with a result (pop). From then on the
   harness leaves everything between the push and the pop out of what the
   model is sent. The result stays. "Locals die on return, and only the
   return value comes back." Nothing is deleted: the user still sees it all.
4. **A new session starts from what is still open.** With its first prompt a
   session is told what mem holds: the rules, what is due, what waits on the
   user, the open todos with what is next in each, and the newest facts.
5. **Compaction becomes a backstop.** With push/pop per todo the context is
   the stack path plus one todo's work, however long the task. When one
   todo's work outgrows the window anyway, the oldest rounds *slide* out
   instead of a summary being written. A summary is made only when there is
   nothing to slide. Jae, 2026-10-31: "phrase needed somewhere 'no more
   compactions'".
6. **Many sessions can work one plan.** They take turns on mem's one lock, so
   two sessions asking for the next todo get different ones. One with nothing
   to do sleeps until mem changes; nothing polls.
7. **The same plan can be a realm on gno.land**, where the chain is the lock,
   so sessions on many machines can share it (the swarm). The realm and its
   demo are built; syncing a local session's mem with it is not.

What makes it different from other context-folding work (own survey,
`priorart.md`, 2026-11-06): the fold falls where a piece of work ends, chosen
by the agent; what survives is typed state in a journaled store outside the
context, not a summary; that state outlives the session; no training is
needed; reads made inside a popped frame are forgotten; and the operations
are statements in a language an interpreter runs, so one input can make a
plan in a loop, and an input that fails halfway keeps nothing.

---

## 2. Vocabulary

Use these words, and only these, in code comments, messages and docs.

| Word | Meaning |
|---|---|
| **VM** / **machine** | A Gno virtual machine run by the harness for the model: a name, a memory cap, a gas limit per call, state that stays between calls. The design doc says "VM" because "machine" once meant an ssh host; the user docs say "machine". Both are the same thing. |
| **scratch machine** | The session's own unnamed machine. Memory only. |
| **program** | Code with a `package` clause: run whole in a fresh machine that is thrown away after, like `go run`. |
| **mem** | The project's memory: a Gno realm kept on disk, reached by the model as the machine named `mem`. One per project (per repository), shared by its sessions, worktrees and subdirectories. |
| **todo** | A piece of work in mem. A todo may have todos under it. There is no separate "goal" or "step" type any more (Jae, 2026-10-31: "The top level todo is just a top level todo."). A top-level todo is also called a *task* in some texts. |
| **thought** | A labelled line kept on a todo. It costs nothing, cuts nothing, and is how anything a frame learned outlives its pop. |
| **fact** | Something true about the project, kept under a name (any path): `cmd/test`. Replaced in place. |
| **rule** | The user's standing instruction. Given to every session in full. Only the user's words become rules. |
| **Op** | One change to mem, as the journal records it. One function applies Ops. |
| **frame** | A todo seen through a session's stack: the stretch of the conversation between entering it and leaving it. |
| **stack** | The frames a session has entered and not left, bottom to top. It belongs to the session, never to mem. |
| **root** | Where a session stands with an empty stack. Unbound: "everything open". Bound: the todo the user's `/goal` binds the session to. |
| **push** | Enter a frame: `.Push()` on a todo, or `mem.Next()`. Marks where the context will be cut. |
| **pop** | Leave the present frame done, with a result: `mem.Pop("…")`. Everything since the push leaves the model's context. |
| **next** | `mem.Next()`: step into the next open todo. The *trampoline* is `mem.Pop("…")` then `mem.Next()` in one input. |
| **leave** | `mem.Leave("why")`: step out of the present frame and leave its todo open. Cuts like a pop. |
| **round** | One model call in the agent loop: the assistant message and the results that answer its tool calls. (A todo under a todo used to be called a "step"; "round" is kept apart from it on purpose.) |
| **turn** | Everything from one prompt of the user's until the model stops and the session goes idle, hidden continuations included. |
| **slide** | What the harness does instead of compacting while the session stands in frames: the oldest rounds leave the context, with no summary. |
| **hold** | A todo a session has entered is held by that session: other sessions do not take, close or drop it. A hold on a todo with todos under it is *soft*: it stands only while its session is working. |
| **hold/release** | The first design for cutting a side quest out of the context (`mem.Hold`, `mem.Release`). Never built; frames replaced it. Its mechanics carried over. See `frames.md` §15. |
| **close check** | Before mem keeps a call that closes a todo as done, the harness checks the close against what happened in the todo. |
| **judge** | A small-model call that says whether a closed task, or the user's `/goal`, really holds. |
| **peer** | One of several sessions working one mem at once, each in its own process. |
| **working** | A session is working when it is in a busy stretch: its *turn lock* is held. |
| **swarm** | Many sessions on one plan. Locally: peers. Across machines: the plan as a realm on gno.land. |
| **lease** | In the swarm realm, how long a hold lasts unless renewed. Counted in blocks. |
| **the digest** / **`<mem>` block** | What a session is told of mem with its first prompt. |
| **recall** | With each typed prompt, up to five items or facts close to it in meaning, when an embedding server is set. |
| **receipt** | What a mem call prints: what it made or changed, by name, and what is next. |

Names in mem are lowercase, printed in backticks, and typed back exactly:
`release-v2`, `release-v2/bump-version`. Titles are printed in quotes.

---

## 3. The files in this set

| File | What it holds |
|---|---|
| `00-overview.md` | this page |
| `machines.md` | GnoVM machines: kinds, limits, gas, memory, persistence, the worker process, the four tools with their arguments and result formats |
| `mem.md` | mem: the data model, every call, names, receipts, the journal/store/index, layouts and what never changes, the session-start digest, `/mem`, `/goal`, recall, what the model must be told |
| `frames.md` | the exact rule for what the model is sent: the cut, the stack, the slide, accounting, caching, every settled edge case, worked examples, measured numbers |
| `close-check.md` | the checks on a close, the judge prompts' requirements, the option, the evidence |
| `peers.md` | many sessions on one mem: the lock, order, `.Parallel()`, the pick, waiting, turn locks, what was measured |
| `swarm.md` | mem as a realm on gno.land: the realm's calls, leases, events, the chain client, the demo runner, measured runs, what is not built |
| `bridge.md` | `gnosh bridge`: the machine tools and the frames view for another harness, as JSON lines over standard input and output |
| `integration-contract.md` | the list the clean side builds from: every hook-up point between the harness and these subsystems, with a priority |
| `open-questions.md` | what the sources contradict or leave unsettled, with a recommended reading |

---

## 4. What is carried over, and what has to be rebuilt

Another agent carries these directories of the earlier tree into the new
repository as they are. They are Jae's side's own code (GNO Network GPL v6
with a Strong Attribution clause; see `machines.md` §12):

- `internal/gnovm/` — the machine host, the worker, the mem host (journal,
  store, index, vectors, the digest, `/mem`, the goal table, peers, waits),
  the Gno library `klashlibs/klash/gnomem/v2` (the mem API), the embedded
  Gno standard library, and `tools/` (the four tools).
- `internal/gnochain/` — the gno.land chain client.
- `internal/swarm/` — the demo runner.
- `gno/r/gnosh/mem/` — the swarm realm.

So this set does not re-specify their insides. It says what they promise,
and what the harness must do for them.

**What has to be rebuilt from these words** is everything that lived on the
harness side: deriving a session's stack, cutting the model's list, the
slide, forgetting reads, the token accounting, the close check's three
checks, the lines put on prompts, the reminders, the turn-end send-backs,
the goal driver's use of the goal table, the erasure of the harness's own
copies, the notices to the user, the commands, the options. All of that is
in `frames.md`, `close-check.md` and `integration-contract.md`.

**The carried code leans on a few packages of the earlier tree.** They must
be supplied by the new harness, or the imports re-pointed
(`integration-contract.md` §2):

- a file-lock helper (try-lock, and a lock that waits in the kernel and can
  be cancelled), used by the host, the client, mem's lock and the turn locks;
- the program's build id, for the worker handshake;
- for `internal/gnovm/tools/` only: the harness's tool interface, the
  session id of the running call, the permission prompt (for deleting a
  machine), and the session's task-list type. That directory is an adapter:
  expect to write it again against the new harness's tool interface, from
  `machines.md` §9.

---

## 5. How to read quoted text

Model-facing wording matters here: several lines were A/B tested on a real
model, and "A note shown right after the tool result, at the moment of the
mistake, keeps working. A prompt line that sounds right can hurt" (mem v2
notes, 2026-10-30). So this set quotes exact text where it is load-bearing,
and marks where each quote comes from:

- **[D]** a design doc of Jae's side (FRAMES.md, HOLD.md, PARALLEL.md,
  SWARM.md, CLOSE-CHECK.md, MEMPLAN.md, internal/gnovm/DESIGN.md) or a
  memory note of the sessions that built it.
- **[G]** the earlier repo's own user docs: GNOMEM.md, and the gnosh-own
  sections of its README.
- **[K]** text printed by the carried code under `internal/gnovm/`. The clean
  side does not write these; they are shown so the rest makes sense.
- **[F]** wording as it landed in a file the fork added on the harness side,
  which upstream Crush never had (checked against the upstream repository on
  2026-11-13). It is Jae's side's text. The surrounding code is not
  described.
- **[R]** restated: the requirement in this author's words, because the
  original wording sat in a file that came from Crush (the system prompt
  template, the main agent file). Write new wording from the requirement.

Nothing in this set is copied or closely paraphrased from a Crush-derived
file, and no Crush type, function or package is named.

"(unconfirmed)" marks a statement no source settles. Where two sources
disagree the newest is taken and the place says so.

---

## 6. How the design got here (dates UTC)

| When | What |
|---|---|
| 2026-10-29 | GnoVM machines: named VMs with memory caps, gas, journals, eviction, a worker process; the four tools. Published at Jae's word. |
| 2026-10-30 | GNOMEM v1, "the board" (`Board{Items, Facts}`), for hours. Then mem v2 (MEMPLAN draft 7): a realm on disk, B+trees keyed by name, a journal of Ops, an index, `/mem`, `/goal` as the one driver, the old `todos` tool removed, embeddings and recall. |
| 2026-10-30 | Hold/Release designed (four drafts), then superseded the same evening by Jae's frame stack. |
| 2026-10-31 | Frames landed (layout 4: named steps, thoughts). Then Jae's rulings: `mem.Todo(task)` replaces Goal/Steps/NewFrame; names made by a small model and made unique; no `todo#thought` addresses; `mem.Next()` replaces the bare `mem.Push()`; the slide. |
| 2026-11-01 | Peers (layout 5: `.Parallel()`, the pick, waits). The fresh-pop guard. The note on a popped push's answer. The swarm realm and demo A. |
| 2026-11-04 | QA loop: a pop spares the user's words of its own turn (A18); the fresh guard counts only work (A20); many receipts. Then layout 6: creation order, `Via` (entered on the way), `Leave`, `Later`, `Help`, verbs on the todo, a plan in one call. Lean swarm realm with `PopNext`. |
| 2026-11-05 | The close check and the soft hold on a plan (A11). Swarm leases counted in blocks; `Purge`; failure runs. |
| 2026-11-06 | A pop's note is said in every request after it; a mid-turn slide takes the oldest rounds first. Provisional patent filed by Jae. |
| 2026-11-07 | Benchmarks against Crush and Codex: gnosh not faster on short tasks; the only pass at a 32K window. |
| 2026-11-11 | Fix A (a todo's evidence counts from where its plan was made) and C (the review accepts a view, diff or passing test). Swarm bench: six sessions 1.6x one. Two swarm flaws fixed. |
