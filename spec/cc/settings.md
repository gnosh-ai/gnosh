# settings.md — settings files, precedence, the keys that matter

*Since 14 November 2026 gnosh is configured by one JSON file, `gnosh.json`, and no longer by `gnoshrc` (`spec/DECISIONS.md`, items 17 and 23 to 28). Where this file speaks of `gnoshrc`, of its directives or of its dashed option names, it records what the build was first given.*

What this file is: where Claude Code keeps its settings, which file wins
when two disagree, the keys a harness cares about, and a recommendation for
how gnosh — which has its own `gnoshrc` — should treat all of that.

In one paragraph: settings are plain JSON files. There are three you own: one
for you everywhere (`~/.claude/settings.json`), one shared with a project
(`.claude/settings.json`), and one private to you in a project
(`.claude/settings.local.json`). When the same key appears in several, the
more specific file wins: local over project over user; a `--settings` flag
beats all three. Lists such as permission rules and hooks are *combined*
across files instead of replaced. The keys that matter to a harness are
`permissions`, `hooks` and `env`.

Source: code.claude.com/docs/en — settings, settings-reference,
claude-directory, env-vars, permissions; docs version 2.1.323, read
2026-11-13. Own words; key names are literal. "(unverified)" = docs silent.

---

## 1. The files

| Scope | File | Applies to | Typical content |
| :- | :- | :- | :- |
| User | `~/.claude/settings.json` | You, in every project | Preferences, your own permission rules and hooks |
| Project | `<project>/.claude/settings.json` | Everyone using that folder; meant to be committed | Team permissions, hooks, `env` |
| Local | `<project>/.claude/settings.local.json` | You, in that project; kept out of git | Personal overrides; approvals saved from prompts |
| Managed | `managed-settings.json` in a system directory, MDM, or a server | An organization | Policy (not adopted) |
| Command line | `--settings <file-or-json>` | One session | Anything a user file may set |

Details:

- `~/.claude` is replaced by the directory in `CLAUDE_CONFIG_DIR` when set.
- **Strict JSON**: no comments, no trailing commas.
- Nothing is created at install. The user file is first written when an
  option is changed from `/config`; the local file when the user first picks
  "don't ask again" on a permission prompt.
- The first time the local file is written inside a git repository that does
  not already ignore it, `**/.claude/settings.local.json` is added to the
  user's **global** git excludes file (`core.excludesFile`, else
  `$XDG_CONFIG_HOME/git/ignore`, else `~/.config/git/ignore`).
- **Where the two project files are read from**:
  - `.claude/settings.json` — from the session's primary working directory
    only. There is **no parent-directory fallback**: a file at the
    repository root is not read when the session starts in a subdirectory.
  - `.claude/settings.local.json` — from the **git repository root** (the
    main checkout for a worktree), even when started in a subdirectory.
    Exceptions that keep it beside `.claude/settings.json`: outside a
    repository, when the repository root is the home directory, or when the
    root or its `.git`/`.claude` entry is not owned by the user.
  - Paths inside the local file still anchor at the primary working
    directory, not at the repository root.
- A fifth file, `~/.claude.json`, is the program's own state: per-project
  trust decisions, user- and local-scope MCP servers, and a few "global
  config" toggles. Users are not meant to edit it. An earlier copy is kept
  in `~/.claude/backups/` each time it is rewritten (the five newest).

## 2. Precedence

Highest first. A key at a higher level overrides the same key below it.

1. Managed settings
2. Command line: `--settings`
3. Local: `.claude/settings.local.json`
4. Project: `.claude/settings.json`
5. User: `~/.claude/settings.json`

Rules that go with the stack:

- **Lists merge.** A list key set in several files (`permissions.allow`,
  `permissions.deny`, `permissions.ask`, `permissions.additionalDirectories`)
  is the concatenation of all of them. Because deny is checked before
  allow, a deny from any file wins over an allow from any other.
- **Hooks merge** across files; identical handlers run once.
- `--settings` overrides only the keys it names; omitted keys keep their
  file values.
- Single-purpose flags (`--model`, `--permission-mode`, `--verbose`...) are
  not part of the stack; each overrides its own key for the session.
- **Environment variables are not a level.** Whether a shell variable or the
  settings key wins is decided pair by pair (for the model, the shell
  variable `ANTHROPIC_MODEL` beats the `model` key; `--model` beats both).
  An `env` block inside a settings file is an ordinary key and follows the
  stack.
- `--setting-sources user,project,local` chooses which files are loaded at
  all.
- Keys a repository must not be able to set are ignored in project and
  local files: `permissions.defaultMode` values `auto` and
  `bypassPermissions`, `permissions.denyInsteadOfPrompting`, and
  environment variables that move the program's own directories
  (`CLAUDE_CONFIG_DIR`, `HOME`, `TMPDIR`, `XDG_*`).

## 3. When edits take effect

- Settings files are **watched and reloaded**: edits to `permissions`,
  `hooks` and most other keys reach the running session without a restart.
  A settings file created mid-session is picked up too.
- A direct edit fires the `ConfigChange` hook.
- Read once at start, so not affected by a mid-session edit: `model`
  (use `/model`), `effortLevel` (use `/effort`).

## 4. Broken files

| Situation | Interactive | `-p` |
| :- | :- | :- |
| A user/project/local file is not valid JSON, or a value fails validation ("Settings Error") | A dialog: fix it with the model's help, exit, or continue without the broken settings | No dialog; the broken file or value is skipped and the run continues |
| Only individual entries are bad — a malformed permission rule, **an unknown hook event name** ("Settings Warning") | Those entries are skipped; the rest of the file applies | Same |

`claude doctor` lists every rejected entry; `/status` lists which files
loaded (`Setting sources` line), not which file supplied which key.

## 5. Keys

Scope column: *any* = user, project, local or `--settings`; *user* = not
from project or local files.

### 5.1 Core (gnosh P0)

| Key | Type | Scope | Meaning |
| :- | :- | :- | :- |
| `permissions` | object | any | `allow`, `ask`, `deny` (rule arrays), `defaultMode`, `additionalDirectories`, `disableBypassPermissionsMode`, `denyInsteadOfPrompting`, ... — see `permissions.md`. |
| `hooks` | object | any | Event → matcher groups → handlers — see `hooks.md`. |
| `disableAllHooks` | boolean | any | Switch off all hooks, the custom status line and the custom file-suggestion command. |
| `env` | object of strings | any | Environment variables for the session and for every subprocess it starts. A value here overwrites the same variable from the shell; `""` cancels a shell export. From user settings and `--settings` it applies at startup; from project and local settings after the workspace is trusted (at startup in `-p`). |
| `model` | string | any | Model for new sessions. `--model` and the shell variable override it. |
| `fallbackModel` | string or list of up to three | any | Models a turn goes on with, in order, when the model cannot answer; the next turn tries the model first again. `--fallback-model` overrides it. In gnosh, names are "<provider>/<model id>" (DECISIONS 140). |

### 5.2 Daily use (gnosh P1)

| Key | Type | Default | Meaning |
| :- | :- | :- | :- |
| `statusLine` | `{ "type": "command", "command": "...", "padding"?: n, "refreshInterval"?: seconds (min 1), "hideVimModeIndicator"?: bool }` | none | Command that renders the status line — `interactive.md`. |
| `cleanupPeriodDays` | whole number ≥ 1 | 30 | Days transcripts and other session data are kept before a background sweep deletes them. `0` is invalid; use a large number for long retention. |
| `attribution` | `{ "commit": string, "pr": string, "sessionUrl": bool }` or `false` | Claude Code adds a `Co-Authored-By` commit trailer and a "Generated with" line in PR descriptions | Text added to commits and PR descriptions; an empty string hides that part; `false` hides all. The model is told that the user's own instructions about attribution take precedence. |
| `includeCoAuthoredBy` | boolean | `true` | Deprecated (since v2.0.62) predecessor of `attribution`; `false` hides both, unless `attribution` sets `commit` or `pr`. |
| `includeGitInstructions` | boolean | `true` | Include the built-in commit/PR workflow instructions and the git-status snapshot (branch, main branch, status, recent commits) taken when a conversation starts. |
| `autoCompactEnabled` | boolean | `true` | Compact automatically near the context limit. `DISABLE_AUTO_COMPACT=1` also turns it off. |
| `autoCompactWindow` | tokens, 100,000–1,000,000 | per model | How full the context gets before auto-compaction. |
| `fileCheckpointingEnabled` | boolean | `true` | Snapshot files before each edit so `/rewind` can restore them. |
| `outputStyle` | string | default style | Name of an output style (a saved instruction set that changes role, tone, format). |
| `verbose` | boolean | `false` | Show full tool input and output inline instead of short summaries. |
| `viewMode` | `default` \| `verbose` \| `focus` | unset | Transcript view at start. |
| `editorMode` | `normal` \| `vim` | `normal` | Key-binding mode of the input box. |
| `respondToBashCommands` | boolean | `true` | After a `!` shell command typed by the user, have the model reply to its output (`false` = just add it to context). |
| `respectGitignore` | boolean | `true` | Leave gitignored files out of the `@` file picker. |
| `fileSuggestion` | `{ "type": "command", "command": "..." }` | built-in | Custom command supplying `@` path completion. |
| `claudeMdExcludes` | string[] (globs or absolute paths) | none | Memory files to skip — `memory-files.md`. |
| `agent` | string | none | Run the main session as this named agent. |
| `askUserQuestionTimeout` | `60s` \| `5m` \| `10m` | none | Auto-continue an unanswered `AskUserQuestion`. User scope. |
| `bashOutputMaxChars` | number ≤ 128,000 | 30,000 | Inline ceiling for successful command output. |
| `showTurnDuration` | boolean | `true` | Show how long each turn took. |
| `language` | string | none | Reply in this language (passed to the model as an instruction). |
| `spinnerVerbs` | `{ "verbs": string[], "mode": "append" \| "replace" }` | built-in | Words shown by the working indicator. |
| `spinnerTipsEnabled` | boolean | `true` | One-line tips under the working indicator. |
| `skipDangerousModePermissionPrompt` | boolean | unset | The bypass-mode warning was accepted. User, local, `--settings`. |

### 5.3 Later or mapped (gnosh P2)

`effortLevel`, `alwaysThinkingEnabled`,
`showThinkingSummaries`, `plansDirectory` (default `~/.claude/plans`; gnosh
keeps plans in `mem`), `autoMemoryEnabled`, `autoMemoryDirectory` (gnosh
memory is its own design), `idleCompaction`, `tui` (`theme` is an option of
gnosh's own, auto, dark, light or ansi, with `/theme`),
`enabledMcpjsonServers`, `disabledMcpjsonServers`,
`enableAllProjectMcpServers`, `allowedMcpServers`, `deniedMcpServers`,
`allowedHttpHookUrls`, `httpHookAllowedEnvVars`, `worktree.*`,
`sandbox.*`.

## 6. Environment variables a harness honours

Set in the shell or in an `env` block. Tool limits are in `tools.md`.

| Variable | Effect |
| :- | :- |
| `CLAUDE_CONFIG_DIR` | Configuration directory instead of `~/.claude`. Not settable from project/local `env`. |
| `CLAUDE_CODE_TMPDIR` | Base temp directory. |
| `BASH_DEFAULT_TIMEOUT_MS`, `BASH_MAX_TIMEOUT_MS`, `BASH_MAX_OUTPUT_LENGTH` | Bash timeouts and output window. |
| `CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS` | Read's per-call token cap (25,000). |
| `CLAUDE_CODE_GLOB_HIDDEN`, `CLAUDE_CODE_GLOB_NO_IGNORE`, `CLAUDE_CODE_GLOB_TIMEOUT_SECONDS` | Glob/Grep behaviour. |
| `CLAUDE_CODE_SHELL`, `CLAUDE_CODE_SHELL_PREFIX`, `CLAUDE_ENV_FILE`, `CLAUDE_BASH_MAINTAIN_PROJECT_WORKING_DIR` | Shell selection and state. |
| `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS` | No background commands or agents. |
| `DISABLE_AUTO_COMPACT`, `DISABLE_COMPACT`, `CLAUDE_CODE_AUTO_COMPACT_WINDOW`, `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE` | Compaction: off automatically / off entirely / window size / trigger percentage (1–100, can only lower). |
| `CLAUDE_CODE_MAX_OUTPUT_TOKENS`, `MAX_THINKING_TOKENS` | Output and thinking budgets. |
| `API_TIMEOUT_MS` (600,000), `CLAUDE_CODE_MAX_RETRIES` (10) | Model request header timeout and retry count. |
| `MCP_TIMEOUT` (30,000), `MCP_TOOL_TIMEOUT`, `MAX_MCP_OUTPUT_TOKENS` (25,000) | MCP startup, per-tool timeout, result size. |
| `CLAUDE_CODE_DISABLE_FILE_CHECKPOINTING` | No edit snapshots. |
| `CLAUDE_CODE_DISABLE_CLAUDE_MDS` | Load no memory files at all. |
| `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD` | Load memory files from `--add-dir` directories. |
| `CLAUDE_CODE_DISABLE_FILE_MENTIONS` | Do not expand `@` mentions; pass the paths with a note instead. |
| `CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS` | Same as `includeGitInstructions: false`. |
| `CLAUDE_CODE_STOP_HOOK_BLOCK_CAP` (8; `0` = no cap), `CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS` | Hook limits. |
| `CLAUDE_CODE_SIMPLE`, `CLAUDE_CODE_SAFE_MODE`, `CLAUDE_CODE_SKIP_PROMPT_HISTORY`, `CLAUDE_CODE_RESUME_INTERRUPTED_TURN` | Same as `--bare`, `--safe-mode`, no persistence, continue an interrupted turn. |
| `CLAUDE_CODE_DISABLE_TERMINAL_TITLE` | Do not set the terminal title or generate a session title. |
| `CLAUDE_CODE_SUBAGENT_MODEL` | Default model for sub-agents. |
| `CLAUDECODE` | Set to `1` *by* the harness in every subprocess. |

## 7. On-disk layout of the configuration directory

What a user authors (`~/.claude/` and `<project>/.claude/`):

| Path | What |
| :- | :- |
| `CLAUDE.md`, `rules/*.md` | Instructions — `memory-files.md` |
| `settings.json`, `settings.local.json` | Settings |
| `skills/<name>/SKILL.md`, `commands/*.md` | Skills and commands |
| `agents/*.md` | Sub-agent definitions |
| `output-styles/*.md` | Output styles |
| `keybindings.json` (user only) | Key bindings |
| `<project>/.mcp.json` | Project MCP servers |

What the program writes under `~/.claude/` (plain text, deleted after
`cleanupPeriodDays` unless noted):

| Path | What |
| :- | :- |
| `projects/<project>/<session-id>.jsonl` | Transcript |
| `projects/<project>/<session-id>/subagents/` | Sub-agent transcripts |
| `projects/<project>/<session-id>/tool-results/` | Large tool outputs spilled to files |
| `projects/<project>/memory/` | Auto memory (kept) |
| `file-history/<session-id>/` | Pre-edit snapshots for `/rewind` (the 100 most recent checkpoints) |
| `plans/` | Plan files |
| `debug/` | Debug logs |
| `paste-cache/` | Large pastes |
| `shell-snapshots/` | Captured aliases and functions for the Bash tool |
| `sessions/` | One small file per running session (crash and concurrency detection) |
| `history.jsonl` | Typed prompts with timestamp and project path (kept) |
| `backups/` | Earlier copies of `~/.claude.json` |

`<project>` is the working directory path with every character other than
letters and digits replaced by `-`, for example `-Users-you-my-project`.

Each session also gets a scratch directory under the temp directory:
`/tmp/claude-<uid>/<project>/<session-id>/scratchpad/` (macOS:
`/private/tmp/...`), where the model may create files without a prompt.

---

## Recommendation for Jae: settings.json for the shared parts, gnoshrc for gnosh

gnosh has its own config file, `gnoshrc`. Jae also has working Claude Code
settings: permission rules, hooks, `env`. The recommendation is to use both,
each for what it is good at.

1. **gnosh reads Claude Code-style `settings.json` files for the things
   whose format this spec copies**: `permissions`, `hooks`,
   `disableAllHooks`, `env`, `statusLine`, `cleanupPeriodDays`,
   `attribution`, and the small interface keys of 5.2. Same three scopes,
   same precedence, same list-merging, same file watching.
2. **`gnoshrc` holds everything that is gnosh's own**: model providers and
   model names, `mem` settings, the look, anything with no Claude Code
   counterpart. Where a key exists in both, `gnoshrc` wins for gnosh.
3. **Which directories.** Read gnosh's own first and Claude Code's as a
   fallback layer, lowest precedence within each scope. (`~/.gnosh/` and
   `.gnosh/` below are placeholders for whatever directory names gnosh's
   own spec settles on.)

   | Scope | gnosh's own | Also read |
   | :- | :- | :- |
   | User | `~/.gnosh/settings.json` | `~/.claude/settings.json` |
   | Project | `.gnosh/settings.json` | `.claude/settings.json` |
   | Local | `.gnosh/settings.local.json` | `.claude/settings.local.json` |

   So Jae's existing files work on day one with nothing copied, and he can
   override or add gnosh-only rules without touching the files Claude Code
   reads. A `gnoshrc` switch turns the fallback off.
4. **Keys gnosh must ignore even though it reads the file**: `model`,
   `effortLevel`, `fallbackModel` and every Anthropic model, account,
   plugin, marketplace, sandbox and managed-policy key — a Claude model name
   means nothing to gnosh. Ignored keys are listed by `gnosh doctor`, never
   warned about at startup.
5. **`env`**: apply the whole block to the subprocesses gnosh starts (so
   hooks and commands see what they expect); for gnosh's own behaviour
   honour only the variables in section 6.
6. **Where gnosh writes.** "Don't ask again" approvals go to
   `.gnosh/settings.local.json`, not into Claude Code's file — reading
   Claude Code's files is safe, writing them is not gnosh's business.
7. **State** (trust decisions, session index): gnosh's own file under
   `~/.gnosh/`. gnosh never reads or writes `~/.claude.json`, with one
   optional exception in `mcp.md` (importing MCP servers).
8. **Commit attribution**: Jae's standing rule for his own repositories is
   no agent trailers. gnosh's default is therefore *no* attribution
   (`attribution: false`), and it honours an `attribution` block if one is
   set.

Things for Jae to say yes or no to: the fallback read of `~/.claude` and
`.claude` (point 3), and writing approvals to `.gnosh/` rather than
`.claude/` (point 6).

## What gnosh adopts

**P0**

- [ ] Three scopes with the precedence of section 2; `--settings` (file or
      inline JSON, 2 MiB limit); list and hook merging.
- [ ] Keys `permissions`, `hooks`, `disableAllHooks`, `env`.
- [ ] Project file read from the primary working directory only; local file
      from the git repository root.
- [ ] Broken-file behaviour in `-p`: skip and continue; `gnosh doctor`
      reports what was skipped. Unknown keys and unknown hook events never
      stop startup.
- [ ] Environment variables of section 6 that concern P0 tools.

**P1**

- [ ] File watching and live reload; `--setting-sources`.
- [ ] `statusLine`, `cleanupPeriodDays` and the sweep, `attribution`,
      `includeGitInstructions`, `autoCompactEnabled`, `autoCompactWindow`,
      `fileCheckpointingEnabled`, `verbose`, `respondToBashCommands`,
      `respectGitignore`, `claudeMdExcludes`, `agent`, `outputStyle`.
- [ ] The settings-error dialog; `/status`; `/config key=value`.
- [ ] Global git-excludes entry for the local settings file.

**P2**

- [ ] The keys of 5.3; `fileSuggestion`; `spinnerVerbs`; `language`;
      `editorMode: "vim"`.

## Deliberately not adopted

- Managed settings in every form (system file, MDM, server-delivered), the
  managed-only keys, and the "exceptions to managed precedence" table.
- `~/.claude.json` as a shared state file.
- Account and provider keys: `apiKeyHelper`, `awsAuthRefresh`,
  `forceLoginMethod`, `allowedProviders`, `availableModels`, `modelPicker`,
  `modelPricing`, `advisorModel`, `fastMode`.
- Plugins and marketplaces: `enabledPlugins`, `extraKnownMarketplaces`,
  `strictKnownMarketplaces`, `pluginConfigs`.
- Cloud, desktop, remote control and channel keys; telemetry and feedback
  keys (`feedbackDrafts`, `feedbackSurveyRate`, OpenTelemetry variables).
- `autoMode`, `useAutoModeDuringPlan`.
- The published JSON schema URL (`$schema`) is accepted and ignored.

## Unverified (docs silent)

- The exact merge rule for non-list object keys such as `env` across files
  (the docs say the highest-precedence file wins per variable; gnosh merges
  per variable).
- Whether a duplicate rule in two files is de-duplicated (gnosh: yes).
- The debounce and granularity of the settings file watcher.
