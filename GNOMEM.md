# GNOMEM: the agent's memory, in Gno

Klash runs the GnoVM, gno.land's interpreter for its Go dialect, inside
itself. The agent keeps its working state in Gno machines: its task
checklist, the facts it was told to remember, and soon its goals and their
success conditions. A machine holds typed data, and the code that works on
that data, and it can be saved on disk. A new session, on a new day, asks the
machine what is left to do. It does not re-read a page of notes.

```
gnovm_new  {"name": "tasks", "persist": true}
gnovm_run  {"name": "tasks", "code": "type Task struct { Title string; Done bool }\nvar Tasks []Task\nfunc Add(t string) { Tasks = append(Tasks, Task{Title: t}) }\nfunc Done(i int) { Tasks[i].Done = true }\n..."}
gnovm_run  {"name": "tasks", "code": "Done(0)\nprintln(Show())"}
  [x] 0 implement Add
  [ ] 1 implement Sub
  ok · VM tasks · 74.3k of 1G gas (≈74µs at 1 ns/gas; ran 380µs) · memory 0.1 MB of 16.0 MB · kept as entry 3
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

Klash's system prompt carries a short recipe, so the model needs no setup.

- **At the start of a task**, `gnovm_list` shows the machines this project
  already has.
- **For a task of several steps**, the model declares a checklist once in a
  persistent `tasks` machine and marks each step done as it finishes. Work
  that stops halfway picks up from `Show()` in the next session.
- **For facts that should outlive the session**, it uses a `memory` machine,
  with `Remember` and `Recall`.
- **For anything that needs computing**, it uses its scratch machine.

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
  call's results ("8, undefined"), and a nil value shows as `nil`.
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
