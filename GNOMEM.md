# GNOMEM: the agent's memory, in Gno

Klash runs the GnoVM, gno.land's interpreter for its Go dialect, inside
itself. The agent keeps its working state in a Gno machine: its goals and
their steps, what it tried and found, and the facts it was told to remember.
A machine holds typed data, and the code that works on that data, and it is
saved on disk. A new session, on a new day, asks the machine what is next.
It does not re-read a page of notes.

Each project gets a persistent machine named `mem`, whose variable `mem` is
the agent's board ([klash/gnomem/v1](internal/gnovm/klashlibs/klash/gnomem/v1/gnomem.gno)):

```
gnovm_run {"name": "mem", "code": "mem.Add(\"release v2; done when CI is green\", \"bump the version\", \"update CHANGELOG\", \"tag v2.0.0\")"}
gnovm_run {"name": "mem", "code": "mem.Done(2, \"now 2.0.0\")"}
  done 2 "bump the version"; next: 3 "update CHANGELOG"
gnovm_run {"name": "mem", "code": "mem.Show()"}
  board: 3 open, 1 done · next: 3 "update CHANGELOG" · changed 2026-10-30 01:12 UTC
  [ ] 1 release v2; done when CI is green
    [x] 2 bump the version → now 2.0.0
    [ ] 3 update CHANGELOG  ← next
    [ ] 4 tag v2.0.0
```

## Why this matters

- **Models forget.** Each session starts empty, and a compaction throws away
  most of what came before. Notes in a Markdown file are prose that the next
  session has to find, read and interpret. A machine answers questions:
  `Show()`, `Recall("staging db")`.
- **Exact, not estimated.** The model runs a computation instead of guessing
  at its result. The sum of the first 1000 primes takes 45 ms in a machine.
- **Safe by construction.** A machine has no files, no network and no
  processes. It has a memory cap, and gas: 1 gas is about 1 ns of work. A
  call that runs out of either is stopped and keeps nothing. The machines run
  in a worker process of their own.
- **Durable and checkable.** Every call that changes a persistent machine is
  journaled, and replaying the journal rebuilds the same state, byte for
  byte, after a crash or on another Klash. A torn journal tail is cut off, and
  a damaged journal is reported, never run.
- **Private.** Everything lives in Klash's data directory, on your machine.

## See it work

These are real runs of Klash with the open-weight Qwen3.8-27B on a local
llama.cpp server. Each runs in a fresh project and a fresh config, with only
Klash's built-in prompt. The checks are scripted: files, test results, the
final answer, and what the machines hold at the end.

| Use case | What the model had to do | Machine | Kept on disk | Sessions | Time | Checks |
|---|---|---|---|---|---|---|
| checklist-calc | implement 4 functions one at a time, keeping track | `tasks` | yes | 1 | 4m30s | 4/4 |
| resume-checklist | plan 4 jobs, do 2, then finish in a new session from the checklist alone | `tasks` | yes, read back | 2 | 4m12s + 1m09s | 8/8 |
| memory-across-sessions | remember two facts; a new session is asked for them | `memory` | yes, read back | 2 | 39s + 22s | 4/4 |
| compute-primes | the exact sum of the first 1000 primes | scratch | no | 1 | 52s | 2/2 |

Before the built-in prompt, the model never kept a checklist in a machine,
and it put memory in a MEMORY.md file. The use cases are txtar files in
[internal/gnovm/testdata/scenarios](internal/gnovm/testdata/scenarios); add
one and run them all:

```sh
GNOVM_SCENARIO_KLASH=$(command -v klash) GNOVM_SCENARIO_OUT=/tmp/gnomem \
  go test ./internal/gnovm -run TestScenarios -v -test.parallel 2 -test.timeout 6h
```

Each run writes a report: every tool call and result, the answer, each check,
and a dump of every persistent machine.

## How the agent uses it

Klash's system prompt carries a short recipe, so the model needs no setup:
`mem` is made the first time it is used.

- **At the start of a session** the model is told, with its first prompt,
  what the board has open (`board: 2 open, 5 done · next: 3 "update
  CHANGELOG"`), so it needs no tool call to find earlier work; `mem.Show()`
  gives the whole outline. Klash rewrites that line after every change, and
  reading it starts nothing.
- **A task of two or more steps**: `mem.Add(goal, steps...)`, then
  `mem.Done(id, what came of it)` as each finishes; Done answers with what is
  next. A goal cannot be done while a step of it is open, so a success
  condition is a step ("go test ./... passes").
- **A question**: an item whose notes are its findings (`mem.Note(id, "the
  claim — path:line")`) and whose result is its answer.
- **Facts**: `mem.Facts["deploy cmd"] = "make ship ENV=prod"`.
- **Anything that needs computing** runs in the session's scratch machine.

## The board, as a foundation

`klash/gnomem/v1` is deliberately small: two plain-data types and seven
calls. Three independent reviews (minimalism, how a 27B model would use it,
durability) shaped it.

```go
type Board struct {
	Items   []Item            // by id; Items[0] is the root
	Facts   map[string]string
	Changed time.Time
}

type Item struct {
	Title  string
	Parent int
	Status string   // "" open, "done", "dropped"
	Result string   // a summary, an answer, or why it was dropped
	Notes  []string // findings and attempts, oldest first
}

func New() *Board
func (b *Board) Add(title string, steps ...string) int
func (b *Board) Step(parent int, title string) int
func (b *Board) Done(id int, result string) string // refuses while a step is open
func (b *Board) Drop(id int, why string) string    // drops its open steps too
func (b *Board) Reopen(id int) string
func (b *Board) Note(id int, text string)
func (b *Board) Show(ids ...int) string            // the outline, or items in full
```

- **Plain data.** `mem.Items[3].Title` and `mem.Facts["k"]` read directly.
  Another package can build on the same items, or replace them: the board is
  only the starting point.
- **It only ever grows.** A persistent machine is rebuilt by running its calls
  again, so what a call does may never change; a frozen test replays a fixed
  board against golden files, and a change that breaks it belongs in
  `klash/gnomem/v2`.
- **Reading costs nothing to keep.** `mem.Show()` and `println(mem)` are
  recognised as reads, once the machine confirms `mem` is a board, and are
  not journaled: reading the board never wears out its journal.
- **Short by construction.** Show's outline stays under about 3000 bytes:
  closed items take one line each, older ones are counted, and the next item
  is named in the header line.
- **The harness's goals stay separate.** Planned harness-tracked goals keep
  their own machine and their own checks, evaluated in Go, so nothing the
  model writes on its board is taken as proof that a goal is met.

## The machines

| Tool | What it does |
|---|---|
| `gnovm_run` | runs code: an expression, statements, declarations, or a whole program; a trailing expression's value is shown |
| `gnovm_new` | makes a machine or changes it: memory cap (1-64 MB), gas per call, ns per gas, persistence, pinning, a description |
| `gnovm_list` | the host's memory, the budget, every machine, and the importable packages |
| `gnovm_drop` | evicts a machine from memory (rebuilt when next used), or deletes it after asking you |

- **Scratch, named, persistent.** Each session has a scratch machine. A named
  machine lasts as long as the Klash process; a persistent one is kept on
  disk, and another Klash on the same project catches up with it.
- **Memory, managed.** Klash reads how much memory the host can give, cgroup
  limits included. It keeps the machines within a budget, and evicts the
  least recently used one when a large task needs the room.
- **Gas you can reason about.** gno's calibration makes 1 gas about 1 ns, and
  each machine can set its own nanoseconds per gas. Every result shows the gas
  used, the time that gas stands for, and the time the call actually took.

## Made not to trip

Each failed call costs the model a round trip, which takes several seconds to
a minute on a local model. The real runs showed where it stumbled, and Klash
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
- **Quiet results.** A trailing `fmt.Println(x)` shows its output, not the
  call's results ("8, undefined"), and a nil value shows as `nil`. A value
  that is a string of several lines is shown as it is, not as one quoted
  line.
- **Go's fmt.** Gno has no fmt. Klash's runs Go's own formatting natively, so
  `%-10s`, `%5.2f`, `%+v` of a struct, `%v` of a slice or a map and `%w` in
  Errorf all work as in Go. The earlier fmt printed "(unhandled)" for most of
  them.
- **Prompt reuse.** Klash keeps everything that changes from one session to
  the next (the date, the working directory, git status) out of the system
  prompt, so a local model server reuses the prompt it has already read. A
  new session reads about 30 new tokens instead of 14,000, which took one to
  two minutes.

## Coming next

- **Goals.** The harness keeps long-standing goals in a machine of its own,
  with success checks that it evaluates itself. A goal closes as met only
  when its check passes. Open goals are put back in front of the model after
  a compaction and in every new session, and `/goals` shows and manages them.
- **Longer runs.** Goals that span sessions, compactions, crashes and
  restarts; stop conditions; real-model runs against control runs with the
  machines off.

The full design, with every review of it, is in
[internal/gnovm/DESIGN.md](internal/gnovm/DESIGN.md).

## License

This part of Klash is under the GNO Network General Public License, version
6, with Additional Terms (Strong Attribution); see
[internal/gnovm/LICENSE.md](internal/gnovm/LICENSE.md). Built on
[gno.land](https://gno.land) · built on [klash](https://klash.pro).
