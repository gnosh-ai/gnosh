# cli.md — command line: flags, subcommands, stdin, exit codes

What this file is: the command-line surface gnosh copies from Claude Code, so
that a command Jae types from habit (`-p`, `-c`, `-r`, `--allowedTools`,
`--permission-mode`, `--output-format json`...) does the same thing in gnosh.

In one paragraph: with no flags the program opens an interactive session. With
`-p` it answers one prompt and exits, reading the prompt from the argument,
from stdin, or both; `--output-format` picks plain text, one JSON object, or a
stream of JSON lines. `-c` continues the last conversation in this directory
and `-r` resumes a named one. The permission flags decide what may run without
asking. Everything else is detail.

Source: public docs at code.claude.com/docs/en (cli-reference, headless,
errors, permission-modes), docs version 2.1.323 (changelog entry dated
2026-11-12), read 2026-11-13, plus `claude --help` of the installed 2.1.323.
Written in our own words; only flag names, values and field names are taken
literally. "(unverified)" marks things the docs do not state.

Throughout: `claude` in the docs = `gnosh` here.

---

## 1. Invocation forms

| Form | Meaning |
| :- | :- |
| `gnosh` | Start an interactive session. |
| `gnosh "query"` | Start an interactive session with `query` as the first prompt. |
| `gnosh -p "query"` | Print mode: run the prompt non-interactively, print the answer, exit. |
| `cat file \| gnosh -p "query"` | Print mode with piped content plus a prompt argument. |
| `echo "query" \| gnosh -p` | Print mode, prompt from stdin only. |
| `gnosh -c` | Continue the most recent conversation in the current directory (interactive). |
| `gnosh -c -p "query"` | Continue it in print mode. |
| `gnosh -r "<id-or-name>" "query"` | Resume a specific session, with an optional new prompt. |
| `gnosh -r` | Open the interactive session picker. |
| `gnosh <subcommand> ...` | See section 5. |

The usage line is `claude [options] [command] [prompt]`: options and the
positional prompt may be interleaved.

### When print mode is chosen implicitly

- Bare `claude` needs stdout to be a terminal. When stdout is redirected or is
  not a real terminal, it runs non-interactively, exactly as `-p` would, and
  therefore requires a prompt.
- Interactive mode with stdin piped (macOS and Linux): keystrokes are read
  from `/dev/tty` and the piped text becomes the first prompt. If `/dev/tty`
  cannot be opened the session refuses to start with a message saying stdin is
  not a terminal and suggesting `-p`.

## 2. Argument-parsing rules gnosh must reproduce

These come from the `--help` output and the documented examples. Go's stdlib
`flag` package does not do any of them, so gnosh needs its own small parser.

1. **Long flags with two dashes**, plus these short flags: `-p`, `-c`, `-r`,
   `-n`, `-d`, `-w`, `-v`, `-h`.
2. **Both spellings for the tool flags**: `--allowedTools` = `--allowed-tools`,
   `--disallowedTools` = `--disallowed-tools`.
3. **`--flag value` and `--flag=value`** both work. For `--debug` the filter
   binds only in the `=` form (`--debug='mcp,startup'`); `--debug mcp` turns
   debug on without a filter and leaves `mcp` as a positional.
4. **Variadic flags** (shown as `<x...>` in `--help`) take one or more values:
   `--add-dir`, `--allowedTools`, `--disallowedTools`, `--tools`,
   `--mcp-config`, `--betas`, `--file`. Example from the docs:
   `claude --add-dir ../apps ../lib`.
   How far a variadic flag reaches is not stated in the docs. gnosh rule:
   it consumes following arguments until one starts with `-` or the arguments
   end (unverified against Claude Code; this is the usual behaviour of such
   parsers). Consequence worth documenting in `--help`: put the prompt before
   a variadic flag, or pass the list as one comma-separated argument —
   every documented example does one or the other
   (`claude -p "query" --allowedTools "Read,Edit,Bash"`).
5. **Tool lists split on commas or spaces, but not inside parentheses.**
   `--allowedTools "Bash(git log *)" "Bash(git diff *)" "Read"`,
   `--allowedTools "Bash,Read,Edit"` and `--allowedTools "Bash(git *) Edit"`
   are all valid. A rule's specifier (the part in parentheses) may contain
   spaces and commas.
6. **Optional-value flags** (shown as `[x]`): `-r/--resume [value]`,
   `-d/--debug [filter]`, `-w/--worktree [name]`, `--from-pr [value]`.
   `-r` with no value opens the picker; with a value it is a session ID, a
   session name, a picker search term, or an absolute path to a transcript
   `.jsonl` file.
7. **Mistyped subcommand**: suggest the closest match and exit without
   starting a session (docs example: `claude udpate` prints
   `Did you mean claude update?`).
8. **Invalid flag**: report on stderr before the run starts and exit non-zero.

## 3. Flags

Columns: flag, value, meaning, gnosh priority. "print only" = only has an
effect with `-p`.

### 3.1 Mode and I/O

| Flag | Value | Meaning | Pri |
| :- | :- | :- | :- |
| `-p`, `--print` | — | Non-interactive: print the response and exit. No workspace-trust dialog is shown. Settings files that fail validation are silently ignored in this mode. | P0 |
| `--output-format` | `text` \| `json` \| `stream-json` | Print only. `text` (default) = the final answer as plain text. `json` = one JSON result object. `stream-json` = newline-delimited JSON events ending with a result line. Shapes: `output-formats.md`. | P0 |
| `--input-format` | `text` \| `stream-json` | Print only. `text` (default) = prompt from argument/stdin. `stream-json` = one JSON user message per line on stdin, a long-lived multi-turn session. | P0 text, P1 stream-json |
| `--include-partial-messages` | — | Add token-level `stream_event` lines. Requires `-p` and `--output-format stream-json`. | P1 |
| `--replay-user-messages` | — | Echo each user message read from stdin back on stdout. Requires stream-json for both input and output. | P1 |
| `--include-hook-events` | — | Put hook lifecycle events (`hook_started`, `hook_progress`, `hook_response`) into the stream. `SessionStart` and `Setup` hook events are always included without it. Requires stream-json output. | P2 |
| `--verbose` | — | Full turn-by-turn output. Overrides the `viewMode` setting for the session. Every documented `stream-json` example passes it. | P0 |
| `--json-schema` | JSON Schema string | Print only. After the agent finishes, return JSON conforming to the schema in the result's `structured_output` field. An invalid schema is a startup error (`Error: --json-schema is not a valid JSON Schema` + diagnostic). The `format` keyword is accepted but not enforced. | P2 |
| `--max-turns` | integer | Print only. Cap on agentic turns; no limit by default. Hitting it ends with result subtype `error_max_turns`. Count one turn per model response that contains tool calls, plus one for the final text-only response. With stream-json input, a message still queued when the cap ends a turn starts a new turn with a fresh count. | P0 |
| `--max-budget-usd` | amount | Print only. Stop once the estimated spend reaches the amount (result subtype `error_max_budget_usd`). Sub-agent spend counts. | P2 |
| `--no-session-persistence` | — | Print only. Do not save the session; it cannot be resumed. | P1 |
| `--permission-prompts` | `host` \| `none` | Print only. Who answers permission prompts: `host` (default; the embedding program or the `--permission-prompt-tool`) or `none` (nobody: anything that would prompt is denied, the model is told not to retry, tools that need a person such as AskUserQuestion are removed). v2.1.259+. | P1 |
| `--permission-prompt-tool` | `mcp__<server>__<tool>` | Print only. An MCP tool that answers permission prompts. | P2 |
| `--forward-subagent-text` | — | In stream-json, also emit sub-agents' text and thinking blocks (with `parent_tool_use_id` set). v2.1.211+. | P2 |
| `--prompt-suggestions` | — | Emit a `prompt_suggestion` line after turns. | not adopted |

### 3.2 Which conversation

| Flag | Value | Meaning | Pri |
| :- | :- | :- | :- |
| `-c`, `--continue` | — | Load the most recent conversation in the current directory. Interactive `-c` skips sessions created by `-p`/SDK runs and sessions whose first prompt was `/loop`; `-p -c` includes them. Includes sessions that added this directory with `/add-dir`. | P0 |
| `-r`, `--resume` | `[id \| name \| search \| /abs/path.jsonl]` | Resume by session ID or name, or open the picker (optionally pre-filtered). An ID is searched in the current project and its git worktrees first, then every other project on the machine (v2.1.223+). | P0 by ID, P1 picker/name |
| `--session-id` | UUID | Use this exact session ID for the new conversation. Must be a valid UUID. | P0 |
| `--fork-session` | — | With `-r` or `-c`: copy into a new session ID instead of appending to the original. | P1 |
| `-n`, `--name` | name | Display name for the session (shown in the resume picker and the terminal title); resumable by name. | P1 |
| `--from-pr` | `[number \| url]` | Picker filtered to sessions linked to a pull request. | not adopted |

### 3.3 Model

| Flag | Value | Meaning | Pri |
| :- | :- | :- | :- |
| `--model` | alias or full model name | Model for this session. Overrides the `model` setting and the `ANTHROPIC_MODEL` environment variable. | P0 (gnosh model names) |
| `--fallback-model` | comma-separated list | Models to fall back to, in order, when the primary is overloaded or unavailable; the primary is retried at the start of each user turn. Overrides the `fallbackModel` setting. | P2 (built: DECISIONS 140) |
| `--effort` | `low` \| `medium` \| `high` \| `xhigh` \| `max` | Reasoning effort for the session; available levels depend on the model. Not persisted. (`ultracode` is an extra Anthropic value: not adopted.) | P2 |
| `--betas` | list | Anthropic API beta headers. | not adopted |

### 3.4 Permissions and tools

| Flag | Value | Meaning | Pri |
| :- | :- | :- | :- |
| `--permission-mode` | `default` \| `acceptEdits` \| `plan` \| `auto` \| `dontAsk` \| `bypassPermissions`; `manual` = alias of `default` | Starting permission mode. Overrides `permissions.defaultMode` from settings. `claude --help` lists `manual` in place of `default`; both are accepted. See `permissions.md`. | P0 |
| `--dangerously-skip-permissions` | — | Same as `--permission-mode bypassPermissions`. | P0 |
| `--allow-dangerously-skip-permissions` | — | Make `bypassPermissions` reachable by Shift+Tab without starting in it. | P1 |
| `--allowedTools`, `--allowed-tools` | rules... | Allow rules: matching calls run without a prompt. Rule syntax in `permissions.md`. Does not restrict which tools exist (that is `--tools`). | P0 |
| `--disallowedTools`, `--disallowed-tools` | rules... | Deny rules. A bare tool name removes the tool from the model's view (`"Edit"`; `"*"` removes every tool; `"mcp__*"` removes every MCP tool). A scoped rule such as `Bash(rm *)` leaves the tool available and denies only matching calls. | P0 |
| `--tools` | names... | Restrict the built-in tool set: `""` = none, `"default"` = the default set, or names such as `"Bash,Edit,Read"`. Does not affect MCP tools. | P0 |
| `--add-dir` | dirs... | Extra working directories the file tools may read and edit. Each must exist and be a directory. Grants file access only: most `.claude/` configuration in those directories is not discovered (skills are the exception). | P0 |

Which mode a session starts in, first match wins: (1) `--permission-mode` or
`--dangerously-skip-permissions`; (2) `permissions.defaultMode` from settings;
(3) the built-in default. The built-in default for `-p` runs is `default`
(Manual) since v2.1.302: a call that needs approval is denied unless an allow
rule, a hook, or a prompt handler approves it. (Claude Code's interactive
built-in default is now `auto`, its classifier mode; gnosh's interactive
default is `default` — see `permissions.md`.)

### 3.5 System prompt

| Flag | Value | Meaning | Pri |
| :- | :- | :- | :- |
| `--system-prompt` | text | Replace the whole default system prompt. | P0 |
| `--system-prompt-file` | path | Same, text from a file. | P0 |
| `--append-system-prompt` | text | Append to the default system prompt. | P0 |
| `--append-system-prompt-file` | path | Same, text from a file. | P0 |
| `--system-prompt-snapshot` | `on` \| `off` | `on` (default): the system prompt is built on the conversation's first request, recorded in the session, and reused verbatim on every later request and resume until the conversation is compacted — even if a later launch passes different prompt flags. `off`: rebuild every request. v2.1.257+. | P2 |
| `--exclude-dynamic-system-prompt-sections` | — | Move per-machine parts (cwd, environment, memory paths, git status) out of the system prompt into the first user message, for prompt-cache reuse across machines. Ignored with `--system-prompt`. | P2 |

Combination rules (all four text flags work in interactive and print mode):

- A replace flag and an append flag may be combined: replacement first, then
  the appended text.
- A flag may be combined with its own file form (v2.1.283+); **the file's text
  comes first**, then a blank line, then the inline text, regardless of the
  order on the command line.
- In replacement text, a line containing only
  `__SYSTEM_PROMPT_DYNAMIC_BOUNDARY__` splits a stable part (above, cacheable)
  from a per-run part (below); the marker line is removed. v2.1.275+. (P2)
- Replacing the prompt also drops the default git-status snapshot.

### 3.6 Settings, configuration and extensions

| Flag | Value | Meaning | Pri |
| :- | :- | :- | :- |
| `--settings` | path or inline JSON | Extra settings for this session. Keys given here override the same keys from settings files; omitted keys keep their file values. A file must be a regular file of at most 2 MiB. | P0 |
| `--setting-sources` | comma list of `user`, `project`, `local` | Which settings files to load. | P1 |
| `--bare` | — | Minimal mode: skip auto-discovery of hooks, skills, custom commands, sub-agents, plugins, MCP servers, auto memory and CLAUDE.md. Only Bash, file read and file edit tools. Context must be passed by flag (`--append-system-prompt[-file]`, `--settings`, `--mcp-config`, `--agents`, `--add-dir`). Hooks do not run even when passed via `--settings`. No background commands or background sub-agents. Sets `CLAUDE_CODE_SIMPLE=1`. Docs say it is to become the `-p` default in a future release. | P1 |
| `--safe-mode` | — | Start with every customization off (memory files, skills, hooks, MCP servers, custom commands and agents, output styles, keybindings, status line) to debug a broken setup; permissions, model choice and built-in tools work normally. Sets `CLAUDE_CODE_SAFE_MODE=1`. | P1 |
| `--disable-slash-commands` | — | Disable all skills and commands for the session. | P1 |
| `--mcp-config` | files or JSON strings... | Load MCP servers. With `-p`, wait for pending servers before the first turn, up to `MCP_TIMEOUT` (30 s default). | P1 |
| `--strict-mcp-config` | — | Use only the servers from `--mcp-config`. | P1 |
| `--agents` | JSON object (or, with `-p`, a path to a JSON file) | Define sub-agents for this session. Validated at startup; invalid → exit 1. | P1 |
| `--agent` | name | Run the main session as this agent (overrides the `agent` setting). | P2 |
| `--append-subagent-system-prompt[-file]` | text / path | Print only. Append text to every sub-agent's system prompt. The two forms cannot be combined (exit 1). | P2 |
| `--init`, `--maintenance` | — | Print only. Run `Setup` hooks with matcher `init` / `maintenance` before the session. | P2 |
| `--init-only` | — | Run `Setup` and `SessionStart` hooks, then exit. | P2 |
| `--plugin-dir`, `--plugin-url` | path / URL | Load a plugin for this session. | not adopted |

### 3.7 Diagnostics and misc

| Flag | Value | Meaning | Pri |
| :- | :- | :- | :- |
| `-v`, `--version` | — | Print the version (Claude Code prints e.g. `2.1.323 (Claude Code)`). | P0 |
| `-h`, `--help` | — | Help. Note: Claude Code's `--help` does not list every flag. | P0 |
| `-d`, `--debug` | `[filter]` | Debug logging, optional category filter in the `=` form (`--debug=hooks,mcp`, `--debug='!hooks,!mcp'`). | P1 |
| `--debug-file` | path | Write debug logs to this file; implies debug mode. | P1 |
| `-w`, `--worktree` | `[name]` | Start in a new git worktree at `<repo>/.claude/worktrees/<name>`. | P2 |
| `--autocompact` | `auto` \| token count (100k–1M) | Auto-compact window for this session. v2.1.221+. | P2 |
| `--ax-screen-reader` | — | Flat-text rendering without borders or animation. | P2 |
| `--bg`, `--background` | — | Start as a background session and return at once. Cannot be combined with `-p`. | P2 (own design) |

Anthropic-only flags, listed so nobody re-adds them by accident: `--advisor`,
`--brief`, `--chrome`/`--no-chrome`, `--cloud`/`--remote`,
`--desktop`, `--environment`, `--ref`, `--file`, `--ide`, `--remote-control`
(`--rc`), `--remote-control-session-name-prefix`, `--restricted`,
`--teleport`, `--teammate-mode`, `--tmux`, `--exec`. (`--enable-auto-mode`
was removed in v2.1.111.) `--channels` and
`--dangerously-load-development-channels` were on this list; gnosh has them
since spec/DECISIONS.md 82, for channels that are MCP servers of one's own
(`server:<name>`).

## 4. Print mode in detail

### 4.1 Where the prompt comes from

- The positional `prompt` argument, piped stdin, or both. With neither:
  `Error: Input must be provided either through stdin or as a prompt argument when using --print`.
- A prompt that is only whitespace is refused:
  `Error: Input contained only whitespace. Provide a prompt with text through stdin or as a prompt argument when using --print`.
- Piped stdin is capped at **10 MB**; above that, a clear error and a non-zero
  exit.
- If stdin cannot be read (the launching process closed its end), print a
  warning on stderr and continue with the command-line prompt.
- How argument and stdin are joined when both are present is not stated
  (unverified). gnosh rule: stdin content first, then a blank line, then the
  argument — the argument is the instruction, stdin the material.
- A prompt starting with `/name` runs that skill or custom command (it is
  expanded before the run). Terminal-only built-ins such as `/login` are not
  available; `/model`, `/effort`, `/rename` take their value as an argument;
  `/config key=value` changes a setting; `/compact`, `/context`, `/clear`
  work as prompts.

### 4.2 What loads

Without `--bare`, a `-p` run discovers and loads exactly what an interactive
session would (hooks, skills, commands, sub-agents, MCP servers, memory
files), including from a folder that was never trusted — there is no trust
dialog and no per-server approval in `-p`. See `permissions.md` (workspace
trust) for what is withheld.

### 4.3 Exit codes

| Code | When |
| :- | :- |
| `0` | The run succeeded. |
| non-zero | The run failed. An invalid flag is reported on stderr before the run starts. A failure inside the run (for example missing credentials) is printed as the result on stdout, and the exit code is still non-zero. |
| `1` | The documented value for specific startup refusals: invalid `--agents`, conflicting flags, stream-json input over the line budget, a session that cannot be resumed, `auth status` when not logged in, `daemon status` when not running. |
| `143` | Stopped by SIGTERM. |

The docs give no table of other codes. Unverified: the code for a result with
`is_error: true` or an `error_*` subtype (gnosh: `1`), and the code after
SIGINT (gnosh: `130`).

### 4.4 Signals

- **SIGTERM**: kill the process tree of any running Bash command, record that
  command as killed, run `SessionEnd` hooks, exit `143`. The turn in progress
  is left unfinished and no result is written for it. No new tool call, model
  request, or other hook starts while exiting. A pending permission prompt is
  left unanswered.
- **SIGINT**: ends the turn (the way to get a clean end before stopping).
- On resume, an interrupted turn is left as it is and the next prompt drives
  the conversation. `CLAUDE_CODE_RESUME_INTERRUPTED_TURN=1` makes the resume
  continue the interrupted turn instead. (P2)

### 4.5 Background work at exit

After the model's turn ends and stdin is closed, a `-p` run stays open for
background work it started:

- a background shell command started by the main conversation: wait until it
  exits or hits its time limit, then give the model one more turn with the
  outcome;
- background sub-agents: wait until they finish (their result is part of the
  output);
- otherwise stop whatever is left after **10 minutes** of continuous idle
  waiting (`CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS`; `0` = no cap).

After 5 seconds of waiting, and only if stderr is a terminal, print a line on
stderr beginning `Waiting for background work to finish`. When background
work starts another turn, `text` output prints each turn's result and `json`
prints the last turn's result.

### 4.6 stream-json input

- One JSON object per line, newline-terminated. Line shape in
  `output-formats.md` (section 5).
- More than 268,435,456 characters without a newline: error on stderr, exit 1.
- A whitespace-only message ends that turn without a model call; the session
  stays usable.
- The session ends when stdin closes and work is finished.

### 4.7 If the working directory disappears

The session keeps running; when a turn starts while the directory is missing,
a warning message is emitted in the stream and shell commands fail until the
directory exists again.

## 5. Subcommands

| Command | Meaning | gnosh |
| :- | :- | :- |
| `doctor` | Print read-only installation and settings diagnostics without starting a session (install health, settings-file validation errors). Reads settings in the current directory without a trust prompt. | P0 |
| `mcp` (`add`, `add-json`, `get`, `list`, `remove`, `login`, `logout`, `reset-project-choices`, `serve`, `add-from-claude-desktop`) | Manage MCP servers. `add [options] <name> <commandOrUrl> [args...]` with `-s/--scope local\|user\|project` (default `local`), `-t/--transport stdio\|sse\|http` (default `stdio`), `-e/--env KEY=value`, `-H/--header`. See `mcp.md`. | P1 (`add`, `add-json`, `get`, `list`, `remove`) |
| `update` (alias `upgrade`) | Check for and install an update. | P2 |
| `install [target]` | Install a specific build (`stable`, `latest`, or a version). | not adopted |
| `purge [path]` | Delete all local state for a project (transcripts, task lists, debug logs, file-edit history, prompt history, the project entry in the global config). Flags `--dry-run`, `-y/--yes`, `-i/--interactive`, `--all`. | P2 |
| `agents`, `attach <id>`, `logs <id>`, `stop <id>` (alias `kill`), `rm <id>`, `respawn <id>`, `daemon ...` | Background sessions and their supervisor. | P2 (own design) |
| `auth` (`login`, `logout`, `status`), `setup-token` | Anthropic account login. | not adopted |
| `auto-mode`, `gateway`, `import`, `plugin`, `remote-control`, `self-hosted-runner`, `ultrareview` | Anthropic services. | not adopted |

## 6. Environment variables that act like flags

Only the ones a harness needs; the full list is on the env-vars page and the
tool-related ones are in `tools.md` and `settings.md`.

| Variable | Effect |
| :- | :- |
| `CLAUDE_CONFIG_DIR` | Use this directory instead of `~/.claude`. |
| `CLAUDE_CODE_SIMPLE=1` | Same as `--bare`. |
| `CLAUDE_CODE_SAFE_MODE=1` | Same as `--safe-mode`. |
| `CLAUDE_CODE_SKIP_PROMPT_HISTORY` | Same as `--no-session-persistence`, in any mode. |
| `CLAUDE_CODE_FORWARD_SUBAGENT_TEXT` | Same as `--forward-subagent-text`. |
| `MCP_TIMEOUT` | MCP server startup timeout (30 s default). |

gnosh reads its own `GNOSH_*` equivalents and may also honour the `CLAUDE_*`
names above (Jae's call; see `settings.md`).

---

## What gnosh adopts

**P0 — first usable binary**

- [ ] Argument parser per section 2 (short flags, both tool-flag spellings,
      `=` form, variadic and optional-value flags, paren-aware list split).
- [ ] `-p/--print`, positional prompt, piped stdin (10 MB cap, the
      missing-prompt and blank-prompt errors), implicit print mode when
      stdout is not a terminal.
- [ ] `--output-format text|json|stream-json`, `--verbose`.
- [ ] `-c/--continue`, `-r/--resume <id|path>`, `--session-id`.
- [ ] `--model`.
- [ ] `--permission-mode` (all six values + `manual`),
      `--dangerously-skip-permissions`, `--allowedTools`, `--disallowedTools`,
      `--tools`, `--add-dir`.
- [ ] `--system-prompt[-file]`, `--append-system-prompt[-file]` with the
      file-first ordering.
- [ ] `--max-turns`, `--settings`.
- [ ] Exit codes 0 / 1 / 143, SIGTERM and SIGINT handling.
- [ ] `-v/--version`, `-h/--help`, `doctor`, the "did you mean" check.

**P1 — interactive daily use**

- [ ] `-r` picker and resume by name, `-n/--name`, `--fork-session`.
- [ ] `--input-format stream-json`, `--include-partial-messages`,
      `--replay-user-messages`, `--permission-prompts none`.
- [ ] `--bare`, `--safe-mode`, `--setting-sources`, `--disable-slash-commands`.
- [ ] `--allow-dangerously-skip-permissions`.
- [ ] `--mcp-config`, `--strict-mcp-config`, `mcp` subcommands.
- [ ] `--agents`.
- [ ] `-d/--debug`, `--debug-file`, `--no-session-persistence`.
- [ ] Background-work wait at exit (section 4.5).

**P2 — later**

- [ ] `--json-schema`, `--max-budget-usd`, `--fallback-model`, `--effort`.
- [ ] `--agent`, `--append-subagent-system-prompt[-file]`,
      `--forward-subagent-text`, `--include-hook-events`,
      `--permission-prompt-tool`.
- [ ] `--system-prompt-snapshot`, `--exclude-dynamic-system-prompt-sections`,
      the dynamic-boundary marker.
- [ ] `-w/--worktree`, `--autocompact`, `--init`, `--init-only`,
      `--maintenance`, `--ax-screen-reader`.
- [ ] `update`, `purge`, background sessions (`--bg`, `attach`, `logs`,
      `stop`, `rm`).

## Deliberately not adopted

- Accounts and login: `auth`, `setup-token`, `--betas`, OAuth, keychain.
- Anthropic cloud and apps: `--cloud`, `--remote`, `--teleport`,
  `--environment`, `--ref`, `--desktop`, `--chrome`, `--ide`,
  `--remote-control`, `gateway`, `self-hosted-runner`,
  `ultrareview`, `--from-pr`, `--file` (file IDs from Anthropic's API).
- Plugins and their marketplace: `plugin`, `--plugin-dir`, `--plugin-url`.
- `--advisor`, `--brief`, `--prompt-suggestions`, `--teammate-mode`,
  `--tmux`, `auto-mode` subcommand (the `auto` mode value itself is discussed
  in `permissions.md`), `import`, `install`, `--restricted`.
- Model aliases `sonnet`/`opus`/`haiku`/`fable`: gnosh's `--model` takes
  gnosh's own model names.

## Unverified (docs silent)

- How far a variadic flag reaches, and what happens to a prompt placed after
  one.
- How stdin and the prompt argument are concatenated.
- Exit code for an error result and for SIGINT; no exit-code table exists.
- Whether `stream-json` output without `--verbose` is an error (every example
  passes `--verbose`; gnosh accepts both).
- Exact text layout of `--output-format text` beyond "the answer".
