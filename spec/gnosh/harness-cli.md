# harness-cli.md — commands and flags, as Jae ended up with them

What this file is: the command line of the earlier gnosh at the end of its
four weeks, with Claude Code's spelling beside each item, the places gnosh
differs on purpose, and the commands that are gnosh's own. `spec/cc/cli.md`
is the full account of Claude Code's command line and of how arguments are
parsed; this file does not repeat it. Where the two disagree about what gnosh
should do, this file records the disagreement and a recommended reading.

In one paragraph: `gnosh` opens a session, `gnosh "prompt"` opens one with the
prompt sent, `gnosh -p "prompt"` (also `gnosh run "prompt"`) answers once and
exits. Jae's own habits are `gnosh -y` (skip permissions), `gnosh -y -C`
(continue the last session) and `gnosh -y -s <id>` (resume one), and scripts
around him run `gnosh run -q -y -c DIR -D DATADIR -m provider/model`. All of
those must keep working. Everything else follows Claude Code.

Read first: `harness-00-principles.md` (PRIN-3), `spec/cc/cli.md`,
`spec/cc/output-formats.md`.

---

## 1. Starting gnosh

### CLI-1 Invocation forms (P0)

| Type this | It does | CC |
| :- | :- | :- |
| `gnosh` | Opens an interactive session in the working directory. | `claude` |
| `gnosh "prompt"` | Opens it with the prompt already sent. With `-r <id>`, sends it into that session. | `claude "query"` |
| `gnosh -p "prompt"` or `gnosh run "prompt"` | Answers one prompt without the interactive interface and exits. | `claude -p` |
| `cat file \| gnosh -p "prompt"` | The piped text and the prompt together. | same |
| `gnosh -c` or `gnosh -C` | Continues the most recent session of this project. | `claude -c` |
| `gnosh -r` | Shows the sessions to pick from. | `claude -r` |
| `gnosh -r <id-or-name>`, `gnosh -s <id>` | Resumes that session. | `claude -r <id>` |

### CLI-2 `run` and `-p` are one thing (P0)

`gnosh run [prompt...]` (alias `gnosh r`) was the earlier gnosh's name for
print mode, and `-p` / `--print` was added on 2026-10-22 as CC's spelling. A
leading `-p` behaves exactly as `run`, so every flag of `run` works after it.
Both spellings stay: the scripts say `run`, the fingers say `-p`.

### CLI-3 Standard input in print mode (P0)

- When stdin is not a terminal, it is read to its end and put before the
  prompt, so `cat file | gnosh run "..."` works.
- A run whose stdin is a pipe that **stays silent** (started over ssh with an
  open stdin, say) must not wait on it for ever: it goes on with the prompt
  it was given. This hung real runs.
- A prompt that is only whitespace is an error ("empty prompt"), not a
  request.

---

## 2. Flags

Columns: gnosh's flag as Jae has it, CC's spelling, what it does, priority.
"Both" means the flag exists on `gnosh` and on `gnosh run`.

### CLI-4 Which conversation

| gnosh | CC | Does | Pri |
| :- | :- | :- | :- |
| `-C`, `--continue`; also `-c` (see CLI-30) | `-c`, `--continue` | Continue the most recent session. Both. | P0 |
| `-s <id>`, `--session <id>` | (none; CC resumes with `-r <id>`. Its `--session-id` is a different thing, see `spec/cc/cli.md`) | Continue a session by ID. ID only, never a name. Both. | P0 |
| `-r [id\|name]`, `--resume` | `-r`, `--resume` | By ID, or by a session's name. Interactive: with no value, or a name that no session or several sessions have, open the session list filtered by what was typed. In `run`: one exact name resumes it; several is an error that lists the matching IDs with their update times; none is "not found". A bare `run -r` says a session ID or name must follow. | P0 by ID; P1 name and list |
| `--fork-session` | `--fork-session` | With `-C`, `-r` or `-s`: go on in a **copy** of the session and leave the original as it was. SessionStart hooks see source `fork`. Both. | P1 |
| `-n <name>`, `--name <name>` | `-n`, `--name` | Name the session as `/rename` does: a new session from its start (no title is generated for it), the session resumed, or the `--fork-session` copy. Both. | P1 |

Names match as in CC: the whole name, ignoring letter case and outer spaces;
newest first.

### CLI-5 Which model

| gnosh | CC | Does | Pri |
| :- | :- | :- | :- |
| `-m <model>`, `--model` | `--model` | The model for this run only, as `model` or `provider/model`. **In memory only**: nothing is saved, `/model` still switches and saves as before, and a session resumed with `-m` does not switch back to the model it last used. Both. | P0 |
| `--small-model <model>` | (none) | The model for the short side jobs in a non-interactive run. Scripts on the AI box pass both `-m` and `--small-model` naming the same Qwen, so that no request can name another model. With only `-m` given, the side jobs use that model too (LM-30). | P1 |
| `--reasoning-effort <level>` | `--effort` | The thinking level for the run (`run` only in the earlier gnosh). Accept `--effort` too. | P1 |

### CLI-6 Permissions

| gnosh | CC | Does | Pri |
| :- | :- | :- | :- |
| `-y`, `--yolo` | (none) | Skip every permission question. Jae starts every lane this way. | P0 |
| `--dangerously-skip-permissions` | same | The same, by CC's name. | P0 |
| `--permission-mode <mode>` | same | `default` (also `manual`), `plan`, `acceptEdits`, `dontAsk`, `bypassPermissions`. `auto` is refused as not supported. Meanings: `spec/cc/permissions.md`, with gnosh's limits in SAFE-12 and SAFE-13. Both. | P1 |
| `--allowedTools <rules>`, `--allowed-tools` | same | Let these tools, or the commands a `Bash(pattern)` rule matches, run without asking. The file tools still ask before changing a protected file (SAFE-13). Both. | P1 |
| `--disallowedTools <rules>`, `--disallowed-tools` | same | Do not give the model these tools; a `Bash(pattern)` rule refuses the commands it matches. Both. | P1 |
| `--tools <names>` | same | Give the model **only** these built-in tools. `""` gives none; `default` gives all. Reviews on the AI box run with `--tools ""`. Both. | P1 |
| `--add-dir <dir>` (repeatable) | same | Let the agent read another directory without asking. Only reading and listing files there is approved by this; nothing else is (SAFE-14). Each must exist. Both; `/add-dir` inside a session. | P1 |
| `--trust-folder` | (none) | Use this folder's own settings for this one run without recording the trust (SAFE-7). Both. | P1 |

On `gnosh run`, `--yolo` and `--dangerously-skip-permissions` are accepted
and change nothing, since a run approves its own session anyway (CLI-31).
The help text says so.

Tool names in these flags may be CC's (`Read`, `Bash`, `Edit`) or gnosh's
own. A name gnosh does not know is a startup error (CFG-40).

### CLI-7 What the model is told

| gnosh | CC | Does | Pri |
| :- | :- | :- | :- |
| `--system-prompt <text>`, `--system-prompt-file <path>` | same | Use these instructions **in place of gnosh's own**. The environment block, the skills and the context files (CFG-20) are kept, as CC keeps `CLAUDE.md`. Plan mode keeps its own instructions. Both. | P1 |
| `--append-system-prompt <text>`, `--append-system-prompt-file <path>` | same | Add this at the end of the system prompt (in plan mode too). Both. | P1 |

### CLI-8 Settings for the run

| gnosh | CC | Does | Pri |
| :- | :- | :- | :- |
| `--cwd <dir>` (earlier also `-c <dir>`; CLI-30) | (none; `cd` first) | Work in this directory. | P0 |
| `-D <dir>`, `--data-dir <dir>` | (none) | Keep this run's sessions and data in this directory instead of the project's `.gnosh/`. Every test, benchmark and review run on the AI box uses it, so that nothing they do lands in a real project. | P0 |
| `--safe-mode` | `--safe-mode`; `CLAUDE_CODE_SAFE_MODE=1` | Start with the config's customizations off (CLI-20). Both. | P1 |
| `--mcp-config <file-or-json>` (repeatable), `--strict-mcp-config` | same | Load MCP servers from a file in `.mcp.json`'s shape, or from that JSON given directly; with the second flag, only those. Both. | P2 |
| `-d`, `--debug` | `-d`, `--debug` | Write more to the log. | P1 |

### CLI-9 Print-mode output

| gnosh | CC | Does | Pri |
| :- | :- | :- | :- |
| `--output-format text\|json\|stream-json` | same | `text`: the answer as it streams. `json`: one result object at the end. `stream-json`: a JSON line per message and tool result, then the result. Shapes: `spec/cc/output-formats.md`, and CLI-40. | P0 text; P1 the others |
| `-q`, `--quiet` | (none) | No progress indicator on stderr. | P0 |
| `-v`, `--verbose` | `--verbose` | On stderr: the log, and one line per tool call as it finishes (CLI-41). | P1 |
| `--goal <condition>` | (see `spec/cc`) | Keep working until the condition holds, as `/goal` does (the other spec set, `mem.md`). stderr says whether the goal was met; the run exits non-zero while the goal is still set. A goal found met is a success, not a failure. | P1 |

---

## 3. Subcommands

### CLI-10 The ones Jae uses (P0 and P1)

| Command | Does | CC | Pri |
| :- | :- | :- | :- |
| `gnosh run [prompt...]` | Print mode (CLI-2). | `claude -p` | P0 |
| `gnosh doctor` | Checks that a turn could run here (CFG-60). `/doctor` inside a session. | `claude doctor` | P1 |
| `gnosh trust [folder]`, `--list`, `--forget [folder]` | Trust a folder's settings, list the trusted folders, forget one (SAFE-7). | the trust dialog | P1 |
| `gnosh models` | Lists the models each usable provider offers, and the providers that were left out with the reason. Never loads a model (CFG-61). | (none) | P1 |
| `gnosh session list\|show <id>\|last\|delete <id>\|rename <id> <title>` (aliases `sessions`, `s`; `ls`; `rm`), each with `--json` | Sessions from the command line. Starts no provider (CFG-70). | (none) | P1 |
| `gnosh mem [show [name...] \| find <words> \| log [n]]` | The project's `mem` from a terminal, without a model. What `mem` refuses exits 1. The other spec set. | (none) | P1 |
| `gnosh prompt show [name]\|path\|export [name...] [--force]` | What the model is sent, and where to change it (CLI-12). `tools` and `context` are `/context all`. | `/context` | P1 |
| `gnosh agents [--json] [--all] [--cwd dir]` | The sessions in the background (`--bg`, `/background`, `/fork`) of every folder, or of one and the folders in it: the working ones, then those done in the last week (`--all`: every one kept). `--json` lists the working ones, and with `--all` the rest. | `claude agents` | P1 |
| `gnosh logs [-f] [-t N] [session]` | The project's log: the last N lines (1000 unless said), or follow it; with a session, what that run in the background printed. | `claude logs` | P1 |
| `gnosh stop <session>` (also `kill`) | Stops a session in the background. | `claude stop` | P1 |
| `gnosh dirs` | Which config and data directories are in use. | (none) | P1 |
| `gnosh --version` | The build: a time and a commit. A session started before a new build was installed must report **its own** build, not the file now on disk. | `--version` | P0 |

### CLI-11 The ones that can wait (P2)

| Command | Does |
| :- | :- |
| `gnosh ssh [ssh arguments]` | Runs ssh and puts whatever is copied on the far side onto this computer's clipboard (UI-12). |
| `gnosh bridge [--cwd <folder>]` | Gives another harness gnosh's machine tools and the frames view over JSON lines on standard input and output (`bridge.md`). |
| `gnosh session web [--port N]` | Serves a page on this machine to read and edit a session's messages (UI-40). |
| `gnosh projects [--json]` | The project folders gnosh has been used in. |
| `gnosh stats [--all] [--crawl-dir <dir>]` | Usage totals across sessions. |

### CLI-12 `gnosh prompt` (P1)

Jae, 2026-10-19: "how do i inspect or edit the context window for each prompt
using crush?" These answer it without starting a session:

- `gnosh prompt show [name]` prints the instructions in effect **and says
  where they came from** (built in, or the user's file).
- `gnosh prompt path` prints the directory where the user's own versions go.
- `gnosh prompt export [name] [--force]` writes the built-in instructions
  there as a file to edit.
- `gnosh prompt tools [--json] [--export]` prints, for each tool the model is
  given here, its name, description, parameters and size, with a total.
  `--json` prints the exact objects sent. `--export` writes each description
  to a file the user can edit, and a commented list for turning tools off.
- `gnosh prompt context [-s <session> | -C]` prints what fills the context
  window by category, as CC's `/context` does: system prompt, tools, MCP
  tools, memory files, skills, messages, free space, the compaction buffer.

Rules that came from getting this wrong (UI-30): an exported file that was
never edited changes nothing; an edited one is plainly reported wherever the
prompt is shown; and the command to check that a new tool really reaches the
model is `gnosh prompt tools` on the built binary, since a tool once passed
every test while no model was ever given it.

### CLI-13 Not carried over

| The earlier gnosh had | Why not |
| :- | :- |
| `gnosh server`, `-H`/`--host`, and running the interface as a client of a shared backend | CLI-50. |
| `gnosh login`, `gnosh logout` | For the upstream vendor's hosted service and other OAuth providers. P2 if a provider needs it. |
| `gnosh update-providers` | For the vendor catalog (OQ-3). |
| `gnosh schema` | Printed the JSON settings schema. P2 with CFG-19. |
| `--channels` | PRIN section 3. |

---

## 4. Safe mode

### CLI-20 `--safe-mode` (P1)

As CC's: start with the config's customizations off, to fix a broken one.
Off: hooks, MCP servers (except those given with `--mcp-config`), context
files, skills found on disk (built-in ones stay), custom commands. Still on:
providers, the model choice, built-in tools, permissions.

Why it matters more in gnosh than in CC: **hooks fail closed** (HOOK-10), so
one broken hook stops every prompt, and without this flag there would be no
way to start gnosh to fix it.

The session says, once, that safe mode is on, what is off, and that starting
without the flag (or the variable) turns it back on.

---

## 5. Where gnosh differs from Claude Code on the command line

### CLI-30 `-c`: continue, not "change directory" (P0; OQ-9)

In the earlier gnosh `-c` was `--cwd` and continue was `-C`. On 2026-10-22
this was left "a decision for Jae", because taking `-c` over would break
existing use. It was never decided.

`spec/cc/cli.md` gives `-c` CC's meaning. Recommended reading, so both
habits work:

- `-c` and `-C` both mean `--continue`. (`gnosh -y -C` is how he restarted
  his lane on 2026-11-10.)
- `--cwd <dir>` has no short form.
- `-c <dir>` with a directory after it cannot be told from `-c` plus a
  prompt, so it is not supported. The scripts that pass `-c DIR` (the
  benchmark and scenario runners) change to `--cwd DIR`. They are ours to
  change.

### CLI-31 A non-interactive run approves its own session (P0; OQ-2)

`gnosh run` grants every permission request of its own session **and of the
sub-agents it starts**. (A sub-agent once waited for ever for an answer
nobody could give, because only the run's own session was approved.)

CC changed in 2.1.302: a plain `claude -p` now denies what would need
approval. The earlier gnosh did not follow, because the unattended runs on
the AI box rely on a run being able to act. For CC's behaviour there is
`--permission-mode dontAsk`: whatever is not read-only or allowed is
**denied without asking**, the model reads why, and the run goes on. That is
the mode for scripts and CI:

```
gnosh run --permission-mode dontAsk --allowedTools "Bash(go test *)" "run the tests and report"
```

Things a run never has, whatever the mode: the signing and paying tools
(TOOL-40), the question tool, the artifact tool, background-job end notices.

### CLI-32 Tool lists (P1)

The earlier gnosh could not take several values after one flag, so a list
went in one argument (`--disallowedTools "Bash Edit"` or `Bash,Edit`).
`spec/cc/cli.md` section 2 gives the rule gnosh follows now. The
one-argument forms must keep working.

### CLI-33 Error messages print what was typed (P1)

An error that begins with something the user typed (a flag, a rule, a file
name) prints it exactly as typed. The earlier gnosh's message formatter
capitalized words inside it, so `--disallowed-tools` came out as
"--Disallowed-Tools" and `Bash(git:*)` as "Bash(Git:*)".

### CLI-34 What `--add-dir` approves (P1)

Narrower than it sounds: reading and listing **files** under the added
directory. It once also approved any request that looked like a read or a
list with a path there, which let through reads of MCP resources and a
listing of the user's keys. SAFE-14.

---

## 6. Print-mode details that are gnosh's own

`spec/cc/output-formats.md` has the shapes. These are the points the earlier
gnosh had to get right.

### CLI-40 Result and stream (P1)

- `json` prints one object at the end, with at least `type` (`"result"`),
  `subtype` (`"success"` or `"error_during_execution"`), `is_error`,
  `result` (the answer), `session_id`, `duration_ms` and `num_turns`. A
  failed run still prints it, and exits 1.
- `num_turns` counts the run's own replies. A sub-agent's messages are never
  counted, and a sub-agent's text can never become the `result`.
- `stream-json`'s first line says what the run uses: the model (the restored
  one when resuming), the permission mode, the tools by name, and each MCP
  server with its state in CC's words. It is written when the run's first
  message appears, not before the session is loaded.
- Messages are separated from each other in `text` output, and the `json`
  result is the answer alone.
- **A message cut from the model's context by a pop is not news.** When a
  session is run one `gnosh run` per turn, earlier turns' rows that a pop
  cut must not be printed again in this turn's stream. They were, and a
  test report once showed work "done again" that never happened.
- A failed turn is reported as a failed turn, not as "failed to start".

### CLI-41 `--verbose` (P1)

One line on stderr per finished tool call: the tool and a one-line summary of
its input (never more than one line, whatever newlines the input holds). A
failed call is marked. A bash call shows its exit code. A sub-agent's calls
are shown indented under the call that started it. stdout carries only the
answer.

### CLI-42 Warnings reach stderr (P0)

Anything the interactive interface would show as a warning (a hook that
failed or could not check, the model choosing to stop at a checkpoint with
its reason, an untrusted folder's settings being left out, a goal's verdict)
is printed on stderr in a run. A warning never ends the run as an error.

### CLI-43 Exit codes and signals (P0)

| Situation | Exit |
| :- | :- |
| The turn finished | 0 |
| The turn failed, a prompt was blocked by a hook, or `--goal` is still unmet | 1 |
| `gnosh doctor` found that a turn cannot run | 1 |
| Stopped by Ctrl-C (SIGINT) | 130, with nothing printed (it used to print "Context canceled." and exit 1) |
| Stopped by SIGTERM | 143 |

On SIGINT or SIGTERM, in a run and in an interactive session alike:

1. The turn is cancelled; its hooks, its tools and **its background jobs and
   their process groups** are stopped. (A run killed by SIGTERM once left its
   Stop hook's reviewers running under init, holding the model server.)
2. SessionEnd hooks run, with reason `other`.
3. An interactive session prints how to resume it.
4. Only the **first** signal is handled this way. A second one kills at once.

### CLI-44 A session left unfinished is closed on the way back in (P0)

A reply cut short by a kill or a crash has no end. When the session is next
opened, every such reply is marked as interrupted before anything else
happens, so it never shows as a turn still running (LOOP-6).

---

## 7. Client and server mode

### CLI-50 Not carried over (see OQ-25)

The earlier gnosh could run as a backend that several terminal clients
joined, sharing sessions and the permission queue. There is no sign Jae used
it (unconfirmed), and it cost something every week: `--model`, `--safe-mode`,
and the tool flags each had to be refused under it, block reasons and errors
were lost on the way to the client, and a non-interactive run could change
settings a later client inherited.

What Jae does use, and what replaces it: several **independent** gnosh
processes in one project, sharing `mem` (the other spec set, `peers.md`).
