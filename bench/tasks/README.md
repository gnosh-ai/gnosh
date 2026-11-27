# Bench tasks

Each task is a directory with:

- `make.sh DIR` builds the task's repository in DIR (a new directory): the
  files, then `git init` and one commit, so a check can diff against it.
  `make.sh --solved DIR` builds a solved copy instead. The checks use it as
  the reference, and running a check on it must pass (`selftest.sh` does
  this for every task).
- `prompt.txt` is the prompt for the harness, given as is.
- `prompt2.txt`, only in `continue`, is the prompt for a second session in
  the same directory and HOME, started with the harness's continue flag
  (`run --continue` for gnosh and crush).
- `check.sh DIR` exits 0 when the task is done and 1 when it is not, and
  prints one line saying why. It uses `$GO` (default `go`) and sets
  GOTOOLCHAIN=local, so it never downloads a toolchain.

The tasks are Go only and need Go 1.21 or later.

| Task | What the harness must do | Why it is here |
|---|---|---|
| rename | Rename the project tallyho to quokka across 15 files: module, package, identifiers, paths, env var, Makefile, scripts, CI, docs | Many small edits across many files, the kind of chore people hand to a harness |
| algos | Implement nine small Go packages whose functions panic with "TODO" | A long task: many steps, each with its own reading and testing, so the context outgrows a 64K window |
| bugs | Fix six seeded bugs, one per package, each caught by a failing test, without touching the tests | Debugging: read the failure, find the cause, fix the code |
| continue | Session 1: make changes 1-2 of a list of six. Session 2: "finish the remaining changes from before" | Carrying work across sessions |
| recall | Read six ~31 KB operational logs (~185 KB total), fix six services, then synthesize all six discovered anomaly codes and limits into a standalone summary package | Cross-context retention of facts from earlier tool results after automatic trimming or summarization (#7, item 3) |

The checks are strict but only about results:
- Test files must match the original byte for byte (algos, bugs), or carry
  every test of a clean rename (rename).
- Every test must pass.
- `continue` runs one hidden test per change, each compiled alone, and
  reports how many of the six changes are done.
