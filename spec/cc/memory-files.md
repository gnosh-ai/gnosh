# memory-files.md — CLAUDE.md, AGENTS.md, rules, imports

*Since 14 November 2026 gnosh is configured by one JSON file, `gnosh.json`, and no longer by `gnoshrc` (`spec/DECISIONS.md`, items 17 and 23 to 28). Where this file speaks of `gnoshrc`, of its directives or of its dashed option names, it records what the build was first given.*

What this file is: which instruction files gnosh finds and loads, in what
order, how one file pulls in another, and how the text reaches the model —
all as Claude Code does it, so the `CLAUDE.md` and `AGENTS.md` files Jae
already has are read the same way.

In one paragraph: at the start of a session the harness collects
instruction files from three places — one for the user (`~/.claude/CLAUDE.md`),
and for the project every `CLAUDE.md` and `CLAUDE.local.md` from the
filesystem root down to the working directory. They are joined, not
overridden, outermost first, so the instructions nearest to where the
session started are read last. A file may import another with `@path`. A
`CLAUDE.md` in a *subdirectory* is not loaded at the start; it is loaded the
first time the model touches a file in that subdirectory. If the project has
no `CLAUDE.md` but has an `AGENTS.md`, that is read instead. All of it is
handed to the model as context, not as the system prompt.

Source: code.claude.com/docs/en — memory, context-window, claude-directory,
settings-reference, hooks (InstructionsLoaded), permissions; docs version
2.1.323, read 2026-11-13. Own words; file names and field names are literal.
"(unverified)" = the docs do not say.

---

## 1. The files

Listed in load order, broadest first.

| Scope | Location | Notes |
| :- | :- | :- |
| Managed | `/Library/Application Support/ClaudeCode/CLAUDE.md` (macOS), `/etc/claude-code/CLAUDE.md` (Linux) | Organization-wide. Not adopted. |
| User | `~/.claude/CLAUDE.md` | Applies in every project. |
| User rules | `~/.claude/rules/*.md` | Loaded before project rules. |
| Project | `./CLAUDE.md` or `./.claude/CLAUDE.md` | Meant to be committed. |
| Project rules | `./.claude/rules/**/*.md` | Found recursively. Without `paths` frontmatter they load at launch with the priority of `.claude/CLAUDE.md`. |
| Local | `./CLAUDE.local.md` | Personal, gitignored by hand. Loaded alongside `CLAUDE.md` and treated the same. |

`~/.claude` moves with `CLAUDE_CONFIG_DIR`.

## 2. What loads at launch

1. The user file and user rules.
2. Walking from the **filesystem root down to the working directory**: in
   each directory, `CLAUDE.md` (and `.claude/CLAUDE.md`), then
   `CLAUDE.local.md`, and that directory's unscoped `.claude/rules/` files.

So for a session started in `foo/bar/`: `foo/CLAUDE.md` comes before
`foo/bar/CLAUDE.md`, and within one directory `CLAUDE.local.md` comes after
`CLAUDE.md`.

- Files are **concatenated**; none overrides another. When two conflict the
  model may follow either.
- **Block-level HTML comments** (`<!-- ... -->`) are stripped before the
  text is given to the model; comments inside code blocks are kept. (Reading
  the file with the Read tool shows them.)
- A file over **4 MiB** is skipped entirely. There is no line limit; the
  docs recommend staying under 200 lines, and warn at startup and in
  `/status` about files over the recommended length.
- Rules frontmatter is removed before loading.
- Project files are skipped when `project` is excluded from
  `--setting-sources`; `CLAUDE.local.md` when `local` is excluded.
- `claudeMdExcludes` (a settings key; arrays merge across files) lists glob
  patterns or absolute paths of files to skip, matched against absolute
  paths.
- `CLAUDE_CODE_DISABLE_CLAUDE_MDS=1`, `--bare` and `--safe-mode` load none.
- Additional directories (`--add-dir`) contribute **no** memory files unless
  `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1`; then their `CLAUDE.md`,
  `.claude/CLAUDE.md`, `CLAUDE.local.md` and unscoped `.claude/rules/*.md`
  load.

## 3. What loads later (on demand)

- **Nested files.** `CLAUDE.md` and `CLAUDE.local.md` in subdirectories
  *below* the working directory are loaded when the model first reads,
  writes or edits a file in that subdirectory — including viewing a file
  with a Bash command that counts as a read (`cat`, `head`... on one file;
  list in `tools.md`). The interface shows a line saying the file was
  loaded.
- **Path-scoped rules.** A rules file whose frontmatter has `paths` loads
  when the model uses Read, Write or Edit (or a counting Bash view) on a
  file matching one of the patterns.
- On-demand files land in the message history at that point, so a later
  compaction summarises them away; they load again the next time their
  trigger occurs. The project-root file and unscoped rules are re-read from
  disk and re-injected after every compaction.
- On-demand files have a per-file size limit — 40,000 characters for a
  200K-token context window — beyond which the model gets only the start,
  ending with a note to read the rest with the Read tool. That is about 5%
  of the window; gnosh scales it to its model: characters = 0.2 × the
  context window in tokens.

### Rules frontmatter

```markdown
---
paths:
  - "src/api/**/*.ts"
  - "lib/**/*.{ts,tsx}"
---

# API rules
- ...
```

- `paths` is the only field read (a YAML list or one comma-separated
  string); other fields are ignored without error. YAML that does not parse
  makes the frontmatter ignored and the rule load as unscoped.
- Patterns are relative to the rule's base directory: for a project rule,
  the directory containing its `.claude` folder; for a user rule, the
  session's primary working directory.

| Pattern | Matches |
| :- | :- |
| `**/*.ts` | `.ts` files anywhere under the base |
| `src/**/*` | Everything under the base's `src/` |
| `*.md` | `.md` files anywhere under the base |
| `/*.md` | `.md` files directly in the base only |
| `src/components/*.tsx` | `.tsx` files directly in that folder |

- Brace groups expand (`*.{ts,tsx}`); one rule's whole list shares a budget
  of 1,000 expanded patterns. A `[` that is not a valid bracket expression
  makes that one pattern match nothing; escape a literal one as `\[`.
- `.claude/rules/` may contain symlinks; a link whose target is outside the
  working directory is treated like an external import (section 4).

## 4. Imports

A memory file pulls in another file by writing `@path` anywhere in its text.

- Relative paths resolve **against the file that contains the import**, not
  the working directory. Absolute paths and `~/` paths are allowed.
- The imported file is expanded and loaded along with the importing file.
- Imports nest to a maximum depth of **four hops**.
- A path with spaces needs a backslash before each space
  (`@Design\ Docs/api.md`); a quoted path is not imported.
- `@...` inside inline code spans and fenced code blocks is **not** an
  import — that is how to mention a path literally.
- **External imports**: an import in a *project* memory file that resolves
  outside the working directory (for example `@~/.claude/my-notes.md`)
  needs approval. The first time a project has such imports, an interactive
  session shows a dialog listing the files; declining disables them and the
  dialog does not return. Imports in *user* memory files are trusted without
  a dialog.
- Imports do not save context: the imported text is loaded with its parent.

## 5. AGENTS.md

| The repository has | What is read |
| :- | :- |
| `AGENTS.md`, and no `CLAUDE.md`, `.claude/CLAUDE.md` or `CLAUDE.local.md` in the working directory or any directory above it | The `AGENTS.md` files |
| `AGENTS.md` and one of those three anywhere on that path | The `CLAUDE.md` files only — **every** `AGENTS.md` is dropped, nested ones included |
| A `CLAUDE.md` that imports `AGENTS.md` (`@AGENTS.md`) or is a symlink to it | The `CLAUDE.md`, with `AGENTS.md` inside it, once |

Details (reading `AGENTS.md` directly is v2.1.277+):

- What counts as "has a `CLAUDE.md`": `CLAUDE.md`, `.claude/CLAUDE.md` or
  `CLAUDE.local.md` in the working directory or above. What does **not**
  count, and keeps loading beside `AGENTS.md`: the user file
  `~/.claude/CLAUDE.md` and `.claude/rules/` files.
- At session start: every `AGENTS.md` and `.claude/AGENTS.md` in the working
  directory and the directories above it. The interface shows a line such as
  `no CLAUDE.md found; AGENTS.md loaded: /home/you/repo/AGENTS.md`.
- Later: a subdirectory's `AGENTS.md` loads when the model opens a file
  there with Read and that subdirectory has none of the three `CLAUDE.md`
  files.
- Inside an `AGENTS.md`, `@path` imports are expanded and
  `claudeMdExcludes` applies.
- Never read: `AGENTS.local.md`, `AGENTS.override.md`, anything under
  `.agents/`.
- A user-level choice ("Project instructions") selects the policy:

  | Value | Reads |
  | :- | :- |
  | `claude-md-or-agents-md` (default) | As the table above. |
  | `claude-md-and-agents-md` | Both; in each directory the `CLAUDE.md` files first, then `AGENTS.md`; a file already loaded through an import is not read twice. |
  | `claude-md` | `CLAUDE.md` files only. |

  In Claude Code this lives under a plugin configuration key; gnosh makes it
  a plain `gnoshrc` option with the same three values.
- Differences when `AGENTS.md` is read directly: `InstructionsLoaded` hooks
  do not fire for it; `--add-dir` directories do not contribute it; an
  external import inside it loads only if external imports were already
  approved for the project.

## 6. How the text reaches the model

- Memory content is delivered as a **user-role message after the system
  prompt**, not as part of the system prompt. It is context the model tries
  to follow; nothing enforces it. (To enforce, use a permission rule or a
  hook. For system-prompt-level text, `--append-system-prompt`.)
- The exact wrapper text Claude Code puts around the files is not published
  (unverified, and not something to copy). gnosh writes its own wrapper,
  which states for each file its path and whether it is the user's or the
  project's.
- An edit to a memory file during a session does not reach the running
  conversation until the next compaction or a new session (the docs note
  that such edits "don't apply mid-session").
- Sub-agents: see `subagents.md` for which memory files a sub-agent gets.
- The `InstructionsLoaded` hook fires per file with `load_reason`
  `session_start`, `nested_traversal`, `path_glob_match`, `include` or
  `compact` (`hooks.md`).

## 7. Commands

- `/memory` — lists the memory files in effect (user, project, local,
  including entries for user and project files that do not exist yet) and
  opens the chosen one in the user's editor, creating it first if needed.
  Also toggles auto memory in Claude Code.
- `/init` — has the model analyse the codebase and write a starting
  `CLAUDE.md` (build and test commands, conventions); if one exists it
  proposes improvements instead of overwriting. It also folds in other
  tools' rule files (`.cursor/rules/`, `.cursorrules`,
  `.github/copilot-instructions.md`).
- `/context all` — lists the memory files loaded at launch, under "Memory
  files".
- The **`#` shortcut** (start a line with `#` to append to a memory file)
  that older Claude Code versions had is **not in the 2.1.323 docs**: the
  quick-command table lists only `/`, `!`, `@`, `:` and `?`. Not adopted.
  Asking in words ("add this to CLAUDE.md") is the documented way.

## 8. Auto memory (for reference)

Claude Code also lets the model keep its own notes in
`~/.claude/projects/<project>/memory/`: an index `MEMORY.md` (one line per
memory; the first 200 lines or 25 KB are loaded into every session) plus one
topic file per memory with a `type` in its frontmatter (`user`, `feedback`,
`project`, `reference`), read on demand. The directory is per git
repository, shared by its worktrees, and is exempt from the transcript
retention sweep.

gnosh's long-term memory is `mem`; this layout is recorded so that Jae's
existing memory folders are understood, not to be re-implemented.

---

## What gnosh adopts

**P0**

- [ ] `~/.claude/CLAUDE.md`; project `CLAUDE.md`, `.claude/CLAUDE.md` and
      `CLAUDE.local.md` from the filesystem root down to the working
      directory, concatenated outermost first, local after shared.
- [ ] `AGENTS.md` fallback with the default policy
      `claude-md-or-agents-md`.
- [ ] `@path` imports: relative to the importing file, depth 4, skipped in
      code spans and fences, backslash-escaped spaces.
- [ ] HTML comment stripping; 4 MiB skip.
- [ ] Delivery as a user-role context message with gnosh's own wrapper.
- [ ] `--bare`, `--safe-mode`, `--setting-sources`,
      `CLAUDE_CODE_DISABLE_CLAUDE_MDS`.

**P1**

- [ ] Nested (on-demand) `CLAUDE.md` / `AGENTS.md` in subdirectories, with
      the size limit and the "read the rest" note.
- [ ] `.claude/rules/` and `~/.claude/rules/`, unscoped and path-scoped.
- [ ] Re-injection after compaction.
- [ ] External-import approval dialog. (`claudeMdExcludes` is built:
  gnosh's `options.claudeMdExcludes`.)
- [ ] `/memory`, `/init`, the memory-file list in `/context`.
- [ ] `InstructionsLoaded` hook.
- [ ] The other two instruction-file policies as a `gnoshrc` option.

**P2**

- [ ] `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD`; symlinked rules;
      startup warning for over-long files.

gnosh also reads its own instruction file name if its design defines one
(for example a `GNOSH.md`); that belongs to gnosh's own spec, and such a
file would count as a "`CLAUDE.md`" for the `AGENTS.md` fallback rule.

## Deliberately not adopted

- Managed `CLAUDE.md` and the `claudeMd` managed setting; the
  `managed-only` policy.
- Auto memory (`MEMORY.md` index, `autoMemoryEnabled`,
  `autoMemoryDirectory`, "Saved N memories" notices): gnosh uses `mem`.
- The `#` shortcut.
- `/doctor prompt-audit`, `/import`.
- The plugin-configuration spelling of the instruction-file policy.

## Unverified (docs silent)

- The wrapper text around memory content.
- Whether user rules load before or after `~/.claude/CLAUDE.md` (gnosh:
  after).
- Whether a memory file edited mid-session is re-read before the next
  compaction (the prompt-caching page says edits do not apply mid-session).
- The on-demand size limit for context windows other than 200K and 1M.
- Ordering between a directory's `CLAUDE.md` and `.claude/CLAUDE.md` when
  both exist (gnosh: root file first).
