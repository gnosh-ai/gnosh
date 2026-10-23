---
name: crash-hooks
description: Use when the user wants to add, write, debug, or configure a Crash hook — gating or blocking tool calls, approving or rewriting tool input before execution, running a formatter or linter after a tool call, injecting context into tool results, or troubleshooting hook behavior in crash.json.
---

# Crash Hooks

Hooks are user-defined commands in `crash.json` that fire at specific points
during execution, giving deterministic control over tool behavior. They run
**before** permission checks and **only on the top-level agent's** tool calls —
sub-agent calls (task tool, agentic_fetch, etc.) are not intercepted, though
the sub-agent tool call itself is.

For the full reference, see `docs/hooks/README.md`. This skill covers what you
need to author correct hooks.

## Supported Events

`PreToolUse` fires before every tool call. `PostToolUse` fires after a tool
call succeeds, and `PostToolUseFailure` after one fails. `UserPromptSubmit`
fires when the user sends a prompt, before the agent sees it. `Stop` fires when
the agent is about to finish its turn.

Event names are matched **exactly**. `pretooluse`, `pre_tool_use` and
`PRE_TOOL_USE` are config errors, not spellings of `PreToolUse`: Crash refuses
to start on an event name it doesn't fire, rather than accepting a hook that
would look configured and silently never run.

A `matcher` only makes sense for `PreToolUse`, `PostToolUse` and
`PostToolUseFailure`. There is no tool in play when a prompt is sent or a turn
ends, so a `matcher` on a `UserPromptSubmit` or `Stop` hook is a config error
too, and every configured hook for those events runs.

## Configuration

```jsonc
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^bash$",              // regex against tool name (optional; omit to match all)
        "command": "./hooks/my-hook.sh",   // required: shell command to run
        "timeout": 10                     // optional: seconds, default 30
      }
    ]
  }
}
```

Project-level hooks take precedence over global. Matching hooks are deduped by
`command`, run in parallel, and aggregated in **config order** (not finish order).

## Language

`command` is a shell command, so hooks can be written in any language by
invoking the interpreter: `node ./hooks/h.js`, `python3 ./hooks/h.py`,
`./hooks/h.sh`, inline `echo '…'`, etc. The rest of this skill shows bash, but
the input/output contract is identical regardless of language.

## Input

**Environment variables:**

| Variable                     | Description                              |
| ---------------------------- | ---------------------------------------- |
| `CRASH_EVENT`                | Event name (`PreToolUse`, `PostToolUse` or `Stop`) |
| `CRASH_TOOL_NAME`            | Tool being called; empty on `Stop`       |
| `CRASH_SESSION_ID`           | Current session ID                       |
| `CRASH_CWD`                  | Working directory                        |
| `CRASH_PROJECT_DIR`          | Project root directory                   |
| `CRASH_TOOL_INPUT_COMMAND`   | For `bash` calls: the shell command      |
| `CRASH_TOOL_INPUT_FILE_PATH` | For file tools: the target file path     |
| `CRASH_TOOL_USE_ID`          | The tool call's ID, the same in its `PreToolUse` and `PostToolUse` hooks |
| `CRASH_STOP_HOOK_ACTIVE`     | `Stop` only: `1` if an earlier `Stop` hook is why the turn is still running |

Each is also set under its `CRASH_` name (`CRASH_EVENT`, ...), the name after
the rename; either works.

**JSON on stdin:**

```json
{
  "event": "PreToolUse",
  "hook_event_name": "PreToolUse",
  "session_id": "313909e",
  "cwd": "/home/user/project",
  "tool_name": "bash",
  "tool_use_id": "call_8f2a",
  "tool_input": {"command": "rm -rf /"}
}
```

A `PostToolUse` payload also carries what the tool returned, as in Claude
Code: `"tool_response": {"output": "…", "metadata": {…}}` (for `bash`, the
metadata includes `exit_code`).

## Output

Communicate back via exit code (+ stderr) or JSON on stdout.

| Exit Code | Meaning                                                       |
| --------- | ------------------------------------------------------------- |
| 0         | Success. Stdout is parsed as the JSON envelope below.         |
| 2         | Block this tool call. Stderr becomes the deny reason.         |
| 49        | Halt the whole turn. Stderr becomes the halt reason.          |
| Other     | Non-blocking error. Logged and ignored; tool call proceeds.   |

Exit 2 blocks one tool call (agent sees the reason and can try again); exit 49
ends the whole turn (user takes over). Default to deny — reach for halt only
when letting the agent retry is itself the problem (e.g. secrets detected,
policy violation).

**JSON envelope (exit 0):**

```json
{
  "version": 1,
  "decision": "allow",
  "halt": false,
  "reason": "...",
  "context": "Extra info for the model",
  "updated_input": {"command": "rewritten"}
}
```

- **`decision`**: `"allow"`, `"deny"`, or omit. `"allow"` is **affirmative
  pre-approval** — it bypasses the permission prompt entirely. Omit it
  (or `null`) when you only want to inject context or rewrite input without
  also auto-approving the call.
- **`halt: true`**: ends the turn (same as exit 49).
- **`reason`**: shown to the model on deny; to model and user on halt.
- **`context`**: string **or array of strings**. Appended to what the model
  sees. Empty entries are dropped.
- **`updated_input`**: **shallow-merge patch** against `tool_input`, not a
  replacement. Keys you include overwrite; keys you don't are preserved.
  Nested objects are replaced wholesale, not deep-merged. Ignored on deny/halt.
  It must be a JSON object; anything else is a failed hook, and an `allow`
  given with it is dropped, so the normal permission check runs.

**On `PostToolUse`** the call has already run, so nothing is approved,
blocked or rewritten. Exit `2` (or `"decision": "block"`/`"deny"`) hands your
reason to the model as feedback after the tool's output; `context` (or
Claude Code's `additionalContext`) is appended the same way; exit `49` ends the
turn. A failed call does not fire `PostToolUse`; it fires `PostToolUseFailure`,
which works the same way and gets `"error"` (the error the model reads) instead
of `tool_response`. Both get `duration_ms`.

**On `UserPromptSubmit`** the input is `{"prompt": ..., "attachments": [...]}`
beside the common fields. Plain stdout (or `context`, or Claude Code's
`additionalContext`) is added after the prompt for the agent to read, in a
`<user-prompt-submit-hook>` tag; the chat shows only what the user typed. Exit
`2` (or `"decision": "block"`) blocks the prompt: it is not sent or stored, and
the user sees the reason. `prompt` includes the text of pasted or attached text
files, as the agent will read it. It runs once per prompt the user sends, also
for one queued behind a running turn, and never for the agent's own
continuations or for channel messages; Esc while it runs cancels the prompt.

## Aggregation (Multiple Hooks)

Composed in **config order**:

- `deny` > `allow` > no opinion. First deny decides; subsequent allows don't override.
- `halt` is sticky: any hook halting ends the turn.
- `reason` and `context` concatenate in config order (newline-joined).
- `updated_input` patches shallow-merge sequentially; later patches win on colliding keys.

## Canonical Examples

### Block destructive commands

```bash
#!/usr/bin/env bash
set -euo pipefail

if echo "$CRASH_TOOL_INPUT_COMMAND" | grep -qE 'rm\s+-(rf|fr)\s+/'; then
  echo "Refusing to run rm -rf against root" >&2
  exit 2
fi
```

Config: `{"matcher": "^bash$", "command": "./hooks/no-rm-rf.sh"}`

### Auto-approve read-only tools (inline, no script)

```jsonc
{"matcher": "^(view|ls|grep|glob)$", "command": "echo '{\"decision\":\"allow\"}'"}
```

Every `view`/`ls`/`grep`/`glob` call now runs without prompting.

### Inject context without auto-approving

Emit only `context` — omit `decision` so the normal permission flow still runs.

```bash
#!/usr/bin/env bash
set -euo pipefail

if [[ "$CRASH_TOOL_INPUT_FILE_PATH" == *.go ]]; then
  echo '{"context": "Remember: run gofumpt after editing Go files."}'
else
  echo '{}'
fi
```

Config: `{"matcher": "^(edit|write|multiedit)$", "command": "./hooks/go-context.sh"}`

### Rewrite tool input (shallow merge)

```bash
#!/usr/bin/env bash
set -euo pipefail

read -r input
rewritten=$(echo "$input" | jq -r '.tool_input.command' | some-rewriter)

cat <<EOF
{
  "context": "Rewrote command",
  "updated_input": {"command": "$rewritten"}
}
EOF
```

If the original call was `{"command": "npm test", "timeout": 60000}`, the
tool runs with `{"command": "<rewritten>", "timeout": 60000}` — `timeout` is
preserved.

### Format a file after every edit (PostToolUse)

```jsonc
{ "hooks": { "PostToolUse": [{ "matcher": "^(edit|multiedit|write)$", "command": "./hooks/gofumpt.sh" }] } }
```

```bash
#!/usr/bin/env bash
set -euo pipefail
f="$CRASH_TOOL_INPUT_FILE_PATH"
[[ "$f" == *.go ]] || exit 0
if ! out=$(gofumpt -w "$f" 2>&1); then
  echo "gofumpt failed on $f: $out" >&2
  exit 2
fi
```

When a hook changes the file the agent just wrote, Crash records a fresh
read of it, so the agent's next edit isn't refused as "modified since it was
read", and tells the agent the file changed.

### Refuse to let the turn end (Stop)

A `Stop` hook fires when the agent is about to finish. Exit `2` means "you are
not finished": the turn stays alive and your stderr becomes the reason the model
is told to keep working. Exit `49` means end the turn now, and wins over any
other hook's block.

```jsonc
{ "hooks": { "Stop": [{ "command": "./hooks/tests-must-pass.sh" }] } }
```

```bash
#!/usr/bin/env bash
set -euo pipefail
if ! go test ./... >/tmp/stop-hook.log 2>&1; then
  echo "Tests are still failing:" >&2
  tail -20 /tmp/stop-hook.log >&2
  exit 2
fi
```

There is no cap on how many times a Stop hook may block: if the work is not
done, it is not done. Every 10th consecutive block, the agent is instead asked
to judge whether there is a genuine reason to stop (impossible as asked,
blocked on the user, a check that cannot pass here, no progress). If so it
explains why and ends its reply with `<!-- CRASH_STOP -->` on its own line;
Crash ends the turn and warns the user. Otherwise it keeps working. A hook
that wants to object only once can check `CRASH_STOP_HOOK_ACTIVE` and exit 0.

A Claude Code-style Stop hook that prints `{"decision": "block", "reason": "…"}`
works the same as exit `2`. Decision values are matched exactly: `allow`,
`deny`, `approve`, `block`, or `ask` (via `hookSpecificOutput`). Any other
value is reported as a failed hook, not silently ignored.

## Authoring Checklist

1. Add `#!/usr/bin/env bash` and `set -euo pipefail` (for shell scripts).
2. `chmod +x` the script.
3. Add the entry under `hooks.PreToolUse` or `hooks.PostToolUse` (with the
   right matcher) or `hooks.Stop` (no matcher) in `crash.json`. Spell the event
   name exactly; anything else is a startup error.
4. Decide intent: inject context (omit `decision`), auto-approve (`"allow"`),
   block (`exit 2`), or halt (`exit 49`). On `Stop`, `exit 2` means "keep
   working" and `exit 49` means "end now".
5. On `Stop`, check `CRASH_STOP_HOOK_ACTIVE` so a hook that blocks can't loop.
6. If rewriting input, remember `updated_input` is a shallow merge — only
   include the keys you want to change (`PreToolUse` only).

## Debugging

- Timeouts kill the hook silently and the tool call proceeds. Bump `timeout` if needed.
- Non-zero exit codes other than 2/49 are logged but don't block — check Crash logs.
- Use `echo "debug info" >&2` for logging without corrupting stdout JSON.
- `matcher` is a regex against the tool name. Use `^bash$` (not `bash`) if you
  don't also want to match `mcp_something_bash`.

## Claude Code Compatibility

Crash also accepts Claude Code's `hookSpecificOutput` envelope. One intentional
divergence: Crash treats `updated_input` as shallow-merge, Claude Code replaces.
Existing Claude Code hooks work without modification for the matcher/decision
parts; revisit any that relied on `updatedInput` fully replacing tool input.
