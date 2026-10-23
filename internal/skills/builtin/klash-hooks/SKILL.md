---
name: klash-hooks
description: Use when the user wants to add, write, debug, or configure a Klash hook — gating or blocking tool calls, approving or rewriting tool input before execution, running a formatter or linter after a tool call, injecting context into tool results, or troubleshooting hook behavior in klash.json.
---

# Klash Hooks

Hooks are user-defined commands in `klash.json` that fire at specific points
during execution, giving deterministic control over tool behavior. They run
**before** permission checks, on every tool call, a sub-agent's (task tool,
agentic_fetch) too, as in Claude Code; a sub-agent's call carries `agent_id`
and `agent_type`, and its `session_id` is the session it works for.

For the full reference, see `docs/hooks/README.md`. This skill covers what you
need to author correct hooks.

## Supported Events

`PreToolUse` fires before every tool call. `PostToolUse` fires after a tool
call succeeds, and `PostToolUseFailure` after one fails. `UserPromptSubmit`
fires when the user sends a prompt, before the agent sees it. `SessionStart`
fires before the first prompt a process sends to a session. `Stop` fires when
the agent is about to finish its turn. `SubagentStop` fires when a sub-agent
is about to finish, and works exactly like `Stop` but for sub-agents.
`PreCompact` fires just before a session's conversation is summarized, manual
or automatic.

Event names are matched **exactly**. `pretooluse`, `pre_tool_use` and
`PRE_TOOL_USE` are config errors, not spellings of `PreToolUse`: Klash refuses
to start on an event name it doesn't fire, rather than accepting a hook that
would look configured and silently never run.

A `matcher` only makes sense for `PreToolUse`, `PostToolUse`,
`PostToolUseFailure`, `PreCompact` and `SessionStart`. The first three match
against the tool name; `PreCompact` matches against its trigger (`manual` or
`auto`); `SessionStart` matches against its source (`startup`, `resume`,
`clear` or `compact`). There is nothing to match when a prompt is sent or a
turn ends, so a `matcher` on a `UserPromptSubmit`, `Stop` or `SubagentStop`
hook is a config error too, and every configured hook for those events runs.

## Configuration

```jsonc
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "bash",                // tool name, exact (optional; omit or "*" to match all)
        "command": "./hooks/my-hook.sh",   // required: shell command to run
        "timeout": 10                     // optional: seconds, default 600 (30 for UserPromptSubmit)
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
| `KLASH_EVENT`                | Event name (`PreToolUse`, `PostToolUse` or `Stop`) |
| `KLASH_TOOL_NAME`            | Tool being called; empty on `Stop`       |
| `KLASH_SESSION_ID`           | Current session ID                       |
| `KLASH_CWD`                  | Working directory                        |
| `KLASH_PROJECT_DIR`          | Project root directory                   |
| `CLAUDE_PROJECT_DIR`         | The same, Claude Code's name for it      |
| `KLASH_TOOL_INPUT_COMMAND`   | For `bash` calls: the shell command      |
| `KLASH_TOOL_INPUT_FILE_PATH` | For file tools: the target file path     |
| `KLASH_TOOL_USE_ID`          | The tool call's ID, the same in its `PreToolUse` and `PostToolUse` hooks |
| `KLASH_STOP_HOOK_ACTIVE`     | `Stop` and `SubagentStop` only: `1` if an earlier hook for the event is why the turn is still running |
| `KLASH_AGENT_ID`             | `SubagentStop`, and a sub-agent's tool calls: its own session ID (`agent_id`) |
| `KLASH_AGENT_TYPE`           | `SubagentStop`, and a sub-agent's tool calls: its type (`agent_type`), `task` or `agentic_fetch` |
| `KLASH_PRE_COMPACT_TRIGGER`  | `PreCompact` only: `manual` or `auto`, as in the payload's `trigger` field |
| `KLASH_SESSION_START_SOURCE` | `SessionStart` only: `startup`, `resume`, `clear` or `compact`, as in the payload's `source` field |

Each is also set under its `KLASH_` name (`KLASH_EVENT`, ...), the name after
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

**On `SessionStart`** the input is `{"model": "…", "source": "startup"|"resume"|"clear"|"compact"}`
beside the common fields. `model` is the ID of the model answering; `source`
names what started the session: a new one in a process that has started none
(`startup`), one that already has messages (`resume`), a fresh one in a running
process, the TUI's New Session, say (`clear`), or the first prompt after a
summary (`compact`, as Claude Code fires it after a compaction). A `matcher` on a `SessionStart` hook
is tested against `source`. It fires once, before the process's first prompt to
the session, and not for later prompts except the first one after a summary,
which runs it again with source `compact`. As in Claude Code a hook cannot stop
the prompt: exit `2` (or `"decision": "block"`) or `49` is shown to the user as
a warning and the prompt goes ahead. A hook that fails is also shown as a
warning. Plain stdout (or `context`, or Claude Code's `additionalContext`) is
added with the prompt for the agent to read, in a `<user-prompt-submit-hook>`
tag, and is kept with the prompt, so it is read again when the conversation is
rebuilt on later turns. Esc while it runs cancels the prompt. It fires only for
the top-level agent, not sub-agents.

**On `SubagentStop`** the input is the same as `Stop`, plus Claude Code's
`"agent_id"` (the sub-agent's own session) and `"agent_type"` (`task`);
`session_id` is the session the sub-agent works for. It works exactly like `Stop`, but
for sub-agents started by the `agent` tool: exit `2` (or
`"decision": "block"`) means "you are not finished" — the sub-agent keeps
working, your stderr is its reason, and the `agent` tool eventually returns
the sub-agent's last answer to the parent once its hooks let it finish. Exit
`49` ends the sub-agent's work now. A failed hook lets the sub-agent finish,
and the user is told the check never ran. There is no cap, and the
sub-agent is asked to judge at every 10th consecutive block, exactly as the
main agent is at `Stop` checkpoints. It fires only for sub-agents; the
top-level agent's gate is `Stop`, which never fires for sub-agents.

**On `PreCompact`** the input is `{"trigger": "manual"|"auto", "custom_instructions": ""}`
beside the common fields. `trigger` names what set the summary going: the user
asked (`manual`), or the context filled up (`auto`). A `matcher` on a
`PreCompact` hook is tested against `trigger`. As in Claude Code a hook cannot
stop the summary: exit `2` (or `"decision": "block"`) or `49` is shown to the
user as a warning and the summary goes ahead. A hook that fails is also shown
as a warning. It fires only for the top-level agent, not sub-agents.

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

if echo "$KLASH_TOOL_INPUT_COMMAND" | grep -qE 'rm\s+-(rf|fr)\s+/'; then
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

if [[ "$KLASH_TOOL_INPUT_FILE_PATH" == *.go ]]; then
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
f="$KLASH_TOOL_INPUT_FILE_PATH"
[[ "$f" == *.go ]] || exit 0
if ! out=$(gofumpt -w "$f" 2>&1); then
  echo "gofumpt failed on $f: $out" >&2
  exit 2
fi
```

When a hook changes the file the agent just wrote, Klash records a fresh
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
explains why and ends its reply with `<!-- KLASH_STOP -->` on its own line;
Klash ends the turn and warns the user. Otherwise it keeps working. A hook
that wants to object only once can check `KLASH_STOP_HOOK_ACTIVE` and exit 0.

A Claude Code-style Stop hook that prints `{"decision": "block", "reason": "…"}`
works the same as exit `2`. Decision values are matched exactly: `allow`,
`deny`, `approve`, `block`, or `ask` (via `hookSpecificOutput`). Any other
value is reported as a failed hook, not silently ignored.

## Authoring Checklist

1. Add `#!/usr/bin/env bash` and `set -euo pipefail` (for shell scripts).
2. `chmod +x` the script.
3. Add the entry under `hooks.PreToolUse` or `hooks.PostToolUse` (with the
   right matcher), `hooks.Stop` (no matcher), `hooks.SubagentStop` (no
   matcher; sub-agents only), `hooks.PreCompact` (matcher against
   `manual`/`auto`), or `hooks.SessionStart` (matcher against
   `startup`/`resume`/`clear`/`compact`) in `klash.json`. Spell the event name
   exactly; anything else is a startup error.
4. Decide intent: inject context (omit `decision`), auto-approve (`"allow"`),
   block (`exit 2`), or halt (`exit 49`). On `Stop` and `SubagentStop`,
   `exit 2` means "keep working" and `exit 49` means "end now".
5. On `Stop` and `SubagentStop`, check `KLASH_STOP_HOOK_ACTIVE` so a hook
   that blocks can't loop.
6. If rewriting input, remember `updated_input` is a shallow merge — only
   include the keys you want to change (`PreToolUse` only).

## Debugging

- Timeouts kill the hook silently and the tool call proceeds. Bump `timeout` if needed.
- Non-zero exit codes other than 2/49 are logged but don't block — check Klash logs.
- Use `echo "debug info" >&2` for logging without corrupting stdout JSON.
- `matcher` follows Claude Code's rules: exact names separated by `|` or `,`
  (`bash`, `Edit|Write`; Claude Code's names match Klash's tools), `*` or empty
  for all, and anything else is an unanchored regex (`^mcp_github_`). A plain
  name is exact: `edit` does not match `multiedit`.

## Claude Code Compatibility

Klash also accepts Claude Code's `hookSpecificOutput` envelope, and reads
matchers as Claude Code does, with Claude Code's tool names (`Bash`, `Read`,
`Edit|Write`, `mcp__server__tool`) matching Klash's tools. It reads Claude
Code's groups (`{"matcher": …, "hooks": [{"type": "command", …}]}`) as well as
its own flat entries (`{"matcher": …, "command": …}`), so a Claude Code hooks
block can be pasted in as it is; only `command` hooks run, and a hook asking for
more (`if`, `args`, `async`, another `type`) is a config error naming it.
Divergences: Klash treats `updated_input` as shallow-merge, Claude Code
replaces; and the payload's `tool_name` is Klash's name (`bash`), not Claude
Code's (`Bash`).
