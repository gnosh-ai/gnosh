# GNOMEM: the agent's memory, in Gno

Gnosh runs the GnoVM, gno.land's interpreter for its Go dialect, inside
itself, and keeps the agent's memory there: the goals it is working on and
their steps, what it tried and found, what it waits for, what it must ask
you, the facts it was told to keep, and the rules you gave it. That memory,
`mem`, is a Gno realm kept on disk for the project, with no size limit. A new
session, on a new day, is told what mem holds and asks it what is next. It
does not re-read a page of notes.

Each project has one mem, shared by its worktrees and its sessions
([klash/gnomem/v2](internal/gnovm/klashlibs/klash/gnomem/v2/gnomem.gno)):

```
gnovm_run {"name": "mem", "code": "mem.Goal(\"release v2\").Steps(\"bump the version\", \"tag v2.0.0\", \"CI is green\")"}
  added goal `release-v2` "release v2"
  added `release-v2/1` "bump the version", `release-v2/2` "tag v2.0.0", `release-v2/3` "CI is green"; next: `release-v2/1` "bump the version"
gnovm_run {"name": "mem", "code": "mem.Done(\"release-v2/1\", \"now 2.0.0\")"}
  done `release-v2/1` "bump the version" → now 2.0.0; next: `release-v2/2` "tag v2.0.0"
gnovm_run {"name": "mem", "code": "mem.Show()"}
  mem: 1 goal open
  goals:
    [ ] `release-v2` "release v2" (1/3 done · this session · just now)
      [x] `release-v2/1` "bump the version" → now 2.0.0
      [ ] `release-v2/2` "tag v2.0.0"  ← next
      [ ] `release-v2/3` "CI is green"
```

## Why this matters

- **Models forget.** Each session starts empty, and a compaction throws away
  most of what came before. Notes in a Markdown file are prose that the next
  session has to find, read and interpret. A machine answers questions:
  `mem.Show()`.
- **Exact, not estimated.** The model runs a computation instead of guessing
  at its result. The sum of the first 1000 primes takes 45 ms in a machine.
- **Safe by construction.** A machine has no files, no network and no
  processes. It has a memory cap, and gas: 1 gas is about 1 ns of work. A
  call that runs out of either is stopped and keeps nothing. The machines run
  in a worker process of their own.
- **Durable and checkable.** Every change to mem is journaled before it is
  kept, and mem is rebuilt from its journal after a crash, an upgrade, or on
  another Gnosh. A torn journal tail is cut off. An erasure writes the
  journal again without the text, and rebuilds, so the text leaves every
  file.
- **Private.** Everything lives in Gnosh's data directory, on your machine.

## In use

### A goal, carried across sessions

> **You:** Build the `greet` package: a go.mod, `Hello` with a test, and a
> `make test` that runs the tests. Do the first step now; we'll carry on
> tomorrow.

The agent puts the goal in mem with each part as a step, does the
first, and marks it done with what came of it. When a step turns out to be
wrong for this machine (there is no `make` here), it notes why, drops that
step and adds the one that replaces it, so mem shows the plan as it now
stands, and why it changed:

```
gnovm_run {"name": "mem", "code": "mem.Goal(\"build greet\").Steps(\"go.mod for module greet\", \"greet.go with Hello\", \"greet_test.go\", \"make test runs the tests\")"}
  added goal `build-greet` "build greet"
  added `build-greet/1` "go.mod for module greet", `build-greet/2` "greet.go with Hello", `build-greet/3` "greet_test.go", `build-greet/4` "make test runs the tests"; next: `build-greet/1` "go.mod for module greet"
gnovm_run {"name": "mem", "code": "mem.Done(\"build-greet/1\", \"module greet, go 1.22\")"}
  done `build-greet/1` "go.mod for module greet" → module greet, go 1.22; next: `build-greet/2` "greet.go with Hello"
gnovm_run {"name": "mem", "code": "mem.Note(\"build-greet/4\", \"make is not installed here\")\nmem.Drop(\"build-greet/4\", \"no make on this machine\")\nmem.Steps(\"build-greet\", \"test.sh runs go test ./...\")"}
  noted on `build-greet/4` "make test runs the tests"
  dropped `build-greet/4` "make test runs the tests"; next: `build-greet/2` "greet.go with Hello"
  added `build-greet/5` "test.sh runs go test ./..."; next: `build-greet/2` "greet.go with Hello"
gnovm_run {"name": "mem", "code": "mem.Show(\"build-greet\")"}
  [ ] `build-greet` "build greet" (1/4 done)
    made by this session, changed just now
    [x] `build-greet/1` "go.mod for module greet" → module greet, go 1.22
    [ ] `build-greet/2` "greet.go with Hello"  ← next
    [ ] `build-greet/3` "greet_test.go"
    [-] `build-greet/4` "make test runs the tests" → no make on this machine
    [ ] `build-greet/5` "test.sh runs go test ./..."
```

Tomorrow's session is told, with your first message, what mem has open and
what is next, so it knows where the goal stands without being told.

### A knowledge base

> **You:** Remember for later sessions: staging's database is `db-stg-4`, the
> on-call engineer is Dana, and we deploy with `make ship ENV=prod`.
>
> *(next week)* **You:** Staging moved to `db-stg-9`.
>
> *(any later session)* **You:** Where's the staging database?

Each fact has a name, any path you like (`staging/database`,
`people/on-call`), and a new session is told the newest facts. So a fact that
changes is set again under the name it already has, rather than added a
second time to disagree with the first, and a question about one is answered
by looking it up, by its name or by its words:

```
gnovm_run {"name": "mem", "code": "mem.Fact(\"staging/database\", \"db-stg-4\")\nmem.Fact(\"people/on-call\", \"Dana\")\nmem.Fact(\"cmd/deploy\", \"make ship ENV=prod\")"}
  fact `staging/database` is new
  fact `people/on-call` is new
  fact `cmd/deploy` is new
gnovm_run {"name": "mem", "code": "mem.Fact(\"staging/database\", \"db-stg-9\")"}
  fact `staging/database` changed (was "db-stg-4")
gnovm_run {"name": "mem", "code": "mem.Find(\"stg\")"}
  fact `staging/database` = db-stg-9
gnovm_run {"name": "mem", "code": "mem.Find(\"deploy\")"}
  fact `cmd/deploy` = make ship ENV=prod
```

What the agent finds while it works is kept on the item it was working on,
as notes, and a question for you is a step of its own (`mem.Ask`), which holds
its goal until you answer.

### A reminder for next time

> **You:** Before we wrap up: remind me next time we talk to rotate the
> staging API key. It expires this week.
>
> *(next session)* **You:** Morning! Anything I should know before I start?

The agent keeps the reminder in mem as an open item. A new session is told
what mem has open along with its first prompt, so a check-in like this one
brings the reminder back.

It is a reminder for the next session, not an alarm. Nothing fires at a time
of day, and the model is told to use mem only where the task at hand is part
of it, so a session that starts straight on something else gets on with that.
In 14 real runs on 30 October 2026 the agent kept the reminder 13 times,
brought it back on every check-in (7 of 7), and never raised it unasked when
the next session began with an unrelated task (0 of 7), although it had said
it would. Ask what is open, or say good morning, and it is there.

## See it work

These are real runs of Gnosh with the open-weight Qwen3.8-27B on a local
llama.cpp server, from every build of 30 October 2026 that had mem (from
01:08 UTC on). Each runs in a fresh project and a fresh config, with only
Gnosh's built-in prompt, and the user talks the way the second column says,
never naming Gno or mem unless it says so. The checks are scripted: files,
test results, the final answer, and what mem holds at the end.

| Use case | What the user asks | Sessions | Passed every check |
|---|---|---|---|
| readme-goal | the README's goal: plan it as steps, do the first now, "we'll carry on in another session" | 2 | 3 of 3 |
| long-goal-sessions | a four-step plan kept in Gno, one step per session | 4 | 3 of 3 |
| resume-checklist | a checklist of four jobs, two now, "we will continue later, in another session" | 2 | 3 of 3 |
| goal-until-green | make `go test ./...` pass, keeping the goal and its condition in Gno | 1 | 3 of 3 |
| goal-replan | a goal to keep whose first approach fails: note why, re-plan | 1 | 2 of 2 |
| research-carries-over | which file declares the most functions, kept in Gno for a later session | 2 | 3 of 3 |
| memory-across-sessions | two facts to remember for a future session, asked for there | 2 | 3 of 3 |
| memory-update | two facts; one changes in a second session; a third asks | 3 | 2 of 3 |
| recall-paraphrased | facts kept in one session, asked for in other words in the next | 2 | 3 of 3 |
| remind-next-time | "remind me next time we talk to …", then a morning check-in | 2 | 5 of 6 |
| compute-primes | the exact sum of the first 1000 primes | 1 | 3 of 3 |
| checklist-calc | four functions in one sitting, "keep track of which ones are done" | 1 | 2 of 8 |
| todo-list-next-day | a todo list made in one session, asked for in the next | 2 | 2 of 2 |

- **memory-update** failed once in a way its check then missed: a session
  stored the moved database under a second name beside the old one. That
  run is why a new session is now told the names of the facts mem holds,
  and why the check now counts them.
- **remind-next-time**: one of its six runs failed by keeping the reminder
  in a MEMORY.md file of its own instead of mem (the reminder still came
  back). Another is counted as the pass it was, though its check, which
  looks for "rotate", missed an answer that said the key "needs rotating".
- **checklist-calc** and **todo-list-next-day** ran while Gnosh still had
  a second list, the session's own To-Do list, which ended with the session:
  a task meant for one sitting mostly went there. That list is gone now
  (f6e71b31), and mem is the agent's
  [one list](README.md#goals-that-outlive-the-session).

Before the built-in prompt, the model never kept a checklist in a machine,
and it put memory in a MEMORY.md file. The use cases are txtar files in
[internal/gnovm/testdata/scenarios](internal/gnovm/testdata/scenarios); add
one and run them all:

```sh
GNOVM_SCENARIO_GNOSH=$(command -v gnosh) GNOVM_SCENARIO_OUT=/tmp/gnomem \
  go test ./internal/gnovm -run TestScenarios -v -test.parallel 2 -test.timeout 6h
```

Each run writes a report: every tool call and result, the answer, each check,
and a dump of every persistent machine.

## How the agent uses it

Gnosh's system prompt carries a short recipe, so the model needs no setup:
`mem` is made the first time it is used.

- **At the start of a session** the model is told, with its first prompt,
  the rules you gave, what is due, what waits on you, the open goals with
  what is next in each, and the newest facts; `mem.Show()` gives the whole
  outline. Gnosh rewrites what it will be told after every change, and
  reading it starts nothing.
- **When a task starts**: `mem.Show()`, and carry on from `← next`.
- **A task of two or more steps**: `mem.Goal("…").Steps("…", "…")`, then
  `mem.Done(name, what came of it)` as each finishes; Done answers with what
  is next. A goal cannot be done while a step of it is open, so a success
  condition is a step ("go test ./... passes").
- **Findings**: `mem.Note(name, "the claim, then path:line")`. **A question
  for you**: `mem.Ask(name, "…")`, answered with Done.
- **Later, or when something happens**: `mem.Goal("…").Wait("2026-12-01")`,
  or `.Wait("PR #6399 merged")`.
- **Facts and rules**: `mem.Fact("cmd/test", "go test -race ./...")`; a
  standing instruction of yours, `mem.Rule("…", "…")`, is given to every
  session.
- **Anything that needs computing** runs in the session's scratch machine.

## mem, inside

`klash/gnomem/v2` keeps work, facts and rules in B+trees keyed by name, so a
call loads only what it touches, and mem has no size limit.

```go
type Todo struct {
	Title   string   // what to get done, or what is asked
	Status  string   // "" while open; "done", "dropped" or "forgotten"
	Result  string   // what came of it: the outcome, your answer, or why it was dropped
	Wait    string   // a day ("2026-12-01") or words ("PR #6399 merged"); "" none
	For     string   // "user": a question or request for you
	Doing   string   // the session that started it
	Notes   []string // findings and attempts, oldest first
	// ...
}

func (m *Mem) Goal(title string, name ...string) Goal // .Steps(...), .Wait(...)
func (m *Mem) Steps(name string, titles ...string)
func (m *Mem) Ask(name, question string)
func (m *Mem) Start(name string)
func (m *Mem) Done(name string, result ...string)
func (m *Mem) Drop(name, why string)
func (m *Mem) Reopen(name string)
func (m *Mem) Note(name, note string)
func (m *Mem) Wait(name, until string)
func (m *Mem) Fact(name, fact string)
func (m *Mem) Rule(name, rule string)
func (m *Mem) Forget(name string)
func (m *Mem) Redact(secret string)
func (m *Mem) Find(words string) string
func (m *Mem) Show(names ...string) string
func (m *Mem) Next(name string) string
```

- **Names, not numbers.** Everything has a lowercase name, printed in
  backticks: a goal's comes from its title (`release-v2`), its steps are
  numbered under it (`release-v2/1`), and a fact's or a rule's is any path
  (`cmd/test`). A name is never reused, and a wrong one is answered with
  what it should have been.
- **One function changes mem.** A call only decides which changes (Ops) to
  make; one function applies them, checking each against what is there, and
  Gnosh journals them. A failed call keeps nothing.
- **The journal is the source.** mem's store, the realm in goleveldb, and its
  index, SQLite with full-text search, are built from the journal, and built
  again from it after damage, an upgrade or an erasure. Two Gnosh processes
  take turns through a lock.
- **Erased means erased.** `mem.Redact(secret)` and `mem.Forget(name)` write
  the journal again without the text and rebuild the store and the index,
  so the text leaves every file mem has.
- **Short by construction.** Show's outline stays under about 3000 bytes:
  the rules, what is due, the open goals newest first (a long run of finished
  steps folded into one line), the goals closed lately, and the newest facts.

## The machines

| Tool | What it does |
|---|---|
| `gnovm_run` | runs code: an expression, statements, declarations, or a whole program; a trailing expression's value is shown |
| `gnovm_new` | makes a machine or changes it: memory cap (1-64 MB), gas per call, ns per gas, persistence, pinning, a description |
| `gnovm_list` | the host's memory, the budget, every machine, and the importable packages |
| `gnovm_drop` | evicts a machine from memory (rebuilt when next used), or deletes it after asking you |

- **Scratch, named, persistent.** Each session has a scratch machine. A named
  machine lasts as long as the Gnosh process; a persistent one is kept on
  disk, and another Gnosh on the same project catches up with it.
- **Memory, managed.** Gnosh reads how much memory the host can give, cgroup
  limits included. It keeps the machines within a budget, and evicts the
  least recently used one when a large task needs the room.
- **Gas you can reason about.** gno's calibration makes 1 gas about 1 ns, and
  each machine can set its own nanoseconds per gas. Every result shows the gas
  used, the time that gas stands for, and the time the call actually took.

## Made not to trip

Each failed call costs the model a round trip, which takes several seconds to
a minute on a local model. The real runs showed where it stumbled, and Gnosh
now handles each case:

- **Imports.** A package used without an import (`fmt.Println`, `strings`,
  `strconv`, ...) is imported for it. The model left out `fmt` in half its
  failed calls.
- **First assignments.** `tasks = []Task{...}` on a new name declares it, as
  `:=` does. This is not done when another machine already has that name:
  then the call fails and says which machine has it.
- **The wrong machine.** When code uses a name another machine declares, or
  uses a machine's name as a value (`memory.Facts`), the error says what was
  meant: `Show is declared in VM tasks, not in this one: run this with name
  "tasks"`.
- **Missing fields.** An error about a missing field lists the fields the
  struct has.
- **Older habits.** A call to one of mem's former calls (`mem.Add`,
  `mem.Facts[…]`), a number where a name goes, or an unquoted name is
  answered with the call or the form that replaced it.
- **Quiet results.** A trailing `fmt.Println(x)` shows its output, not the
  call's results ("8, undefined"), and a nil value shows as `nil`. A value
  that is a string of several lines is shown as it is, not as one quoted
  line.
- **Go's fmt.** Gno has no fmt. Gnosh's runs Go's own formatting natively, so
  `%-10s`, `%5.2f`, `%+v` of a struct, `%v` of a slice or a map and `%w` in
  Errorf all work as in Go. The earlier fmt printed "(unhandled)" for most of
  them.
- **Prompt reuse.** Gnosh keeps everything that changes from one session to
  the next (the date, the working directory, git status) out of the system
  prompt, so a local model server reuses the prompt it has already read. A
  new session reads about 30 new tokens instead of 14,000, which took one to
  two minutes.

The full design, with every review of it, is in
[internal/gnovm/DESIGN.md](internal/gnovm/DESIGN.md).

## License

This part of Gnosh is under the GNO Network General Public License, version
6, with Additional Terms (Strong Attribution); see
[internal/gnovm/LICENSE.md](internal/gnovm/LICENSE.md). Built on
[gno.land](https://gno.land) · built on [gnosh](https://gnosh.ai).
