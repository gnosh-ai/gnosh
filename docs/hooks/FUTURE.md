# Hooks: Future Work

This document tracks planned features and design notes for hooks that are not
yet implemented. Nothing here is part of the current contract. Treat it as a
scratchpad for what's next, not as documentation of current behavior.

> [!NOTE]
> This document was largely LLM-generated.

## `context_files`

**Status:** planned, not implemented.

### Motivation

Today, a hook that wants to inject reference material into the agent's context
has exactly one knob: `context` (string or array of strings). Whatever the hook
puts there is concatenated into what the model sees. That's fine for short notes
("current branch: main", "scrubbed secrets") but it scales badly:

- Dumping a whole `README.md` or `package.json` into `context` burns tokens on
  every tool call where the hook fires.
- The model sees the file contents even if it doesn't need them.
- Large files can push the turn past the context window.

`context_files` is the lazy alternative: the hook returns **paths**, not
contents. Gnosh tells the agent the files exist and are relevant, and the agent
decides whether to open them with its existing `view` tool.

### Proposed shape

Additive envelope field. Accepts a list of strings:

```jsonc
{
  "decision": "allow",
  "context": "Scrubbed one secret",
  "context_files": ["README.md", "docs/ARCHITECTURE.md"],
}
```

Paths are resolved relative to `GNOSH_CWD`. Non-existent paths are dropped with
a debug log (don't fail the hook over a missing file).

### How the agent sees it

Gnosh appends a short note to the turn's context along the lines of:

```
## Referenced files
- README.md
- docs/ARCHITECTURE.md
```

No file contents are inlined. The agent opens them with `view` if it decides
they're relevant. This keeps cost proportional to need.

### Aggregation

Matches the existing rules for lists:

- Concatenates across matching hooks in config order.
- Deduplicates paths (same file referenced by two hooks → listed once).
- Dropped entirely if the final decision is `deny` or `halt`.

### Backwards compatibility

Purely additive. Hooks that don't emit `context_files` are unaffected. Existing
envelopes keep working unchanged. No version bump required.

### Open questions

- Should `context_files` paths be constrained to `GNOSH_PROJECT_DIR`? Probably
  yes, to avoid hooks smuggling in arbitrary filesystem reads.
- Do we want a per-file line range (`"README.md:1-40"`) or keep it dead simple
  (whole-file references only)? Start simple; add ranges only if asked for.
- Should we annotate "why this file is relevant" per entry? An object form
  (`{"path": "...", "reason": "..."}`) would allow that but complicates the
  schema. Defer until there's a real user need.

## Sub-agent opt-in

**Status:** superseded. Hooks fire on a sub-agent's tool calls too, as in
Claude Code, with the sub-agent named in `agent_id` and `agent_type`. See the
[PreToolUse](README.md#pretooluse) section in `README.md`.

## `UserPromptSubmit` event

**Status:** implemented. See the [UserPromptSubmit](README.md#userpromptsubmit)
section in `README.md` for the current behavior and contract.

## Cross-platform shell (Windows support)

**Status:** implemented. See the [Execution model](README.md#execution-model)
section in `README.md` for the current behavior and contract.
