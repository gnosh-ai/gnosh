# Hooks

> [!NOTE]
> This document was designed for both humans and agents.

Hooks are user-defined shell scripts that run when various events happen during
the agent lifecycle, allowing you to both build on top of Klash, customize
its behavior, and exert deterministic control over an agent's wily behavior.

Hooks are just shell commands, and were designed to be both simple and future
forward.

### Hot Hook Facts

- Hooks are just shell commands
- Hooks can be written in any language because they’re just executables: Bash, Python, Node, Rust, Haskell, whatever
- Hooks are Claude Code-compatible
- Klash ships with a builtin `klash-hook` skill write, edit, and configure
  hooks; just tell Klash how to configure Klash
- Klash supports eleven hooks: `PreToolUse`, which fires before a tool call,
  `PostToolUse` and `PostToolUseFailure`, which fire after one succeeds or
  fails, `UserPromptSubmit`, which fires when you send a prompt and can add
  context to it or block it, `SessionStart`, which fires before the first
  prompt to a session and can add context to it, `Stop`, which fires when
  the agent is about to finish its turn and can refuse to let it end,
  `SubagentStop`, which fires when a sub-agent is about to finish and can
  refuse to let it end, `StopFailure`, which fires instead of `Stop` when a
  turn ends on an error from the model's API, `PreCompact`, which fires just before the session
  is summarized and can object to it, `PostCompact`, which fires once it has
  been, with the summary, and `SessionEnd`, which fires when a session ends; plans are to support the full
  gamut, so please let us know which hooks you'd like to see next
- Hooks run in parallel for speed, but their results compose in config order
  for determinism

### Some things you can do with hooks:

- Block "dangerous" commands: no more `git push -f` or `cabal init`
- Rewrite tool input: turn `node` calls info `deno`, scrub secrets from
  commands, rewrite all mentions of "Haskell" into "Haskell, The Best
  Language", and so on
- Inject context: add notes to the model's context whenever certain tools are
  called. For example: "remember to run gofumpt after editing Go files"
- Auto-approve tools: skip the permission prompt for bash commands that
  you know are safe
- Log certain tool calls

…And lots more. Show us what you're building!

## Baby's First Hook

Let's just dive into it and make a simple hook. This particular hook will
disallow the use of Haskell (but we love you, Simon Peyton Jones).

### Config

The first thing we need to do is hook up our hook. Let's add the following to
our **project-level** `klash.json`. Relative paths like `./no-haskell.sh` work
here because the project root is your working directory. If you're configuring
a global hook (`~/.config/klash/klash.json`), use an absolute path instead.

```jsonc
{
  // As expected, hooks go in a "hooks" object.
  "hooks": {
    // PreToolUse is an event that fires before a tool is used.
    "PreToolUse": [
      {
        // What tool do we want to hook into? In this case, Bash, because it
        // runs the stuff we wanna block.
        "matcher": "^bash$",

        // The path to our actual hook script.
        "command": "./no-haskell.sh",
      },
    ],
  },
}
```

Now, let's make our `no-haskell.sh` hook script.

```bash
#!/usr/bin/env bash

# Disallow ghc, cabal, and stack. Pipe the bash command output
# ($KLASH_TOOL_INPUT_COMMAND) to grep and match on a regexp.
if echo "$KLASH_TOOL_INPUT_COMMAND" | grep -qE '(^| )((ghc|cabal|stack)(\.exe)?)( |$)'; then

  # Someone is trying to use Haskell. Let's send a message back to the model
  # and user explaining why we're blocking this. Note that we send all feedback
  # like this to stderr.
  echo "No Haskell allowed, kiddo." >&2

  # Now, block the tool call by exiting with code 2.
  exit 2
fi
```

That's basically it. For the full guide on how hooks work, however, read on.

---

## Execution model

Hooks run through Klash's embedded POSIX shell (`mvdan.cc/sh`) — the same
interpreter the `bash` tool uses. Inline commands and shebang-less scripts
execute in-process; scripts with a `#!` shebang dispatch to the named
interpreter via `os/exec`. This contract is identical on macOS, Linux, and
Windows.

What this means in practice:

- **Windows without Unix tooling**: inline shell (`echo`, pipelines, `jq`,
  `grep`), shebang-less `.sh` scripts, inline PowerShell
  (`powershell -Command …`), and `.exe` invocations all work out of the box
  with no WSL, Git Bash, Cygwin, or MSYS required.
- **PowerShell scripts** (`.ps1`) are not auto-dispatched by extension.
  Invoke them explicitly: `powershell -File ./audit.ps1` (or
  `pwsh -File ./audit.ps1`).
- **Shebang'd scripts** require the named interpreter on `PATH`. Git for
  Windows ships `bash.exe`, which makes `#!/bin/bash` and
  `#!/usr/bin/env bash` scripts work on Windows the same way they do on
  Unix. CRLF line endings in the shebang line are tolerated.
- **Permissive shebang fallback**: if the absolute path in a shebang
  doesn't exist (e.g. `#!/bin/bash` on Windows), Klash falls back to a
  `PATH` lookup of the base name (`bash`) before giving up. A debug-level
  log records the fallback. If the interpreter isn't on `PATH` either, the
  hook could not run, and it blocks as a hook that could not evaluate does
  (see [Could not evaluate](#could-not-evaluate-klash-fails-closed)).
- **Environment**: every hook sees `KLASH=1`, `AGENT=crush`, and
  `AI_AGENT=crush` on top of the `KLASH_*` hook-specific variables. These
  three markers are guaranteed and match what the `bash` tool sets, so
  scripts that detect "am I being run by an AI agent?" behave the same in
  both contexts.
- **Timeout behavior**: when a hook exceeds its timeout, Klash cancels the
  context and waits a short grace period (~1s) for the interpreter to
  yield. If the hook still hasn't returned, Klash abandons it, logs a
  warning, and treats it as a hook that could not evaluate, which blocks.
  Long-running work should honor context cancellation or run in a
  subprocess via a shebang.

## Configuration

Hooks can be added to your `klash.json` (or `.klash.json`) at both the global
and project-level, with project level hooks taking precedence.

```jsonc
{
  "hooks": {
    "PreToolUse": [
      {
        "name": "no-rm-rf", // friendly name shown in the TUI
        "matcher": "bash", // the tool's name; see "Matchers" below
        "command": "./hooks/my-hot-hook.sh", // the path to the hook
        "timeout": 10, // in seconds; default 600, or 30 for UserPromptSubmit
      },
    ],
  },
}
```

A Claude Code `settings.json` hooks block can also be pasted in as it is. Klash
reads Claude Code's groups, a matcher and the hooks it runs, as one entry per
command:

```jsonc
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{ "type": "command", "command": "./hooks/no-rm-rf.sh", "timeout": 10 }],
      },
    ],
  },
}
```

Klash runs `command` hooks only. `statusMessage` and `once` are accepted and
change nothing, as are the default values of `shell` (`bash`), `async` and
`asyncRewake` (`false`) and `onFailure` (`continue`). Anything else a Claude
Code hook can ask for, another `type` (`http`, `mcp_tool`, `prompt`, `agent`),
an `if` filter, `args`, a background hook, is a config error that names it,
rather than a hook run some other way than written. So are a hook on an event
Klash does not fire (`Notification`, …), a matcher on an event
that takes none (Claude Code ignores it), and a regular expression Go cannot
read (lookahead such as `(?!…)`): take those entries out, or rewrite them.

> [!IMPORTANT]
> The `command` is resolved relative to your **current working directory** —
> not relative to the config file. Relative paths like `./hooks/whatever.sh`
> work fine in project-level `klash.json` because the project root is also
> your working directory. For **global** config (`~/.config/klash/`),
> however, you must use either an absolute path or an inline command:
>
> ```jsonc
> // Global ~/.config/klash/klash.json
> {
>   "hooks": {
>     "PreToolUse": [
>       {
>         "command": "/home/you/.config/klash/hooks/no-haskell.sh"
>         // or use an inline command:
>         // "command": "echo '{\"decision\":\"allow\"}'"
>       }
>     ]
>   }
> }
> ```

Remember, hooks will run in parallel but resolve in config order. Last hook
wins when rewriting input, but first deny wins when blocking.

## Events

Here are the events you can hook into:

### PreToolUse

This hook fires before every tool call. Use it to block dangerous commands,
enforce policies, rewrite tool input, inject context the model should see, log
stuff, and so on.

**Matched against**: the tool name (e.g. `bash`, `edit`, `write`,
`mcp_github_create_pull_request`), and Claude Code's name for the same tool
(`Bash`, `Edit`, `Write`, `mcp__github__create_pull_request`), so a matcher
written for Claude Code matches here too.

**Matchers** are read as Claude Code reads them:

- empty, or `*`: every tool;
- only letters, digits, `_`, `-`, spaces, `,` and `|`: exact names, separated
  by `|` or `,` (`bash`, `Edit|Write`, `edit, write`). `Bash` does not match
  `BashOutput`, and `mcp_github` matches no tool (Klash logs a warning);
- anything else: a regular expression, unanchored (`^mcp_github_`,
  `mcp__github__.*`, `Notebook.*`).

Claude Code's names Klash knows, today's and older ones: `Agent` and `Task`
(agent), `AskUserQuestion` (question), `Bash`, `BashOutput` and `TaskOutput`
(job_output), `Edit`, `Glob`, `Grep`, `KillShell` and `TaskStop` (job_kill),
`ListMcpResourcesTool` (list_mcp_resources), `LS`, `MultiEdit`, `Read` (view),
`ReadMcpResourceTool` (read_mcp_resource), `TodoWrite` (todos), `WebFetch`
(fetch, and agentic_fetch and its sub-agent's web_fetch), `WebSearch`
(agentic_fetch and its sub-agent's web_search) and `Write`. The payload's
`tool_name` is Klash's name, `bash` not `Bash`.

**Tools Claude Code has nothing like** match the name of the Claude Code tool
whose job they do, so a guard written for Claude Code cannot be walked around
through them:

| Klash tool                                      | Also matches                                |
| ----------------------------------------------- | ------------------------------------------- |
| `multiedit`, `lsp_rename`, `lsp_replace_symbol` | `Edit`: they change files                   |
| `download`                                      | `WebFetch` and `Write`: it saves a URL      |
| `x402_fetch`                                    | `WebFetch`: it fetches a page, and pays     |
| `sourcegraph`                                   | `WebSearch`: it searches code on the web    |
| the other `lsp_` tools                          | `LSP`: each does part of what LSP does      |

A hook matched only by one of these names is a guard: it can block the call,
add context or rewrite the input, but its `allow` counts as no opinion, and the
call asks for permission as usual. What the hook checked before approving was
another tool's input: a hook that approves every `Write` has not approved a
download, which also fetches. To approve one of these tools from a hook, name
it: `download`, `Edit|lsp_rename`, or `MultiEdit`, multiedit's own Claude Code
name.

> [!IMPORTANT]
> Event names are matched **exactly**. `pretooluse`, `pre_tool_use` and
> `PRE_TOOL_USE` are all errors, not spellings of `PreToolUse`. Klash refuses to
> start on an event name it doesn't fire, rather than loading a hook that would
> look configured and silently never run.

**Scope**: `PreToolUse` fires on every tool call, a sub-agent's too (those of
the `agent` tool's and `agentic_fetch`'s), as in Claude Code: a hook that keeps
the agent from reading a file also keeps a sub-agent it starts from reading it.
For a sub-agent's call, `session_id` is the session it works for (the one you
are in), and `agent_id` and `agent_type` name the sub-agent (its own session,
and `task` or `agentic_fetch`). The sub-agent tool call itself is hooked too,
so policy like "never let the agent spawn sub-agents" works.

### PostToolUse

This hook fires right after a tool call succeeds. Use it to format or lint the
file the agent just edited, run the tests it touched, or log what happened. A
call that failed — the tool returned an error, a hook blocked it, you denied
the permission — does not fire it.

**Matched against**: the tool name, as for `PreToolUse`.

**The call has already run**, so a `PostToolUse` hook approves, blocks and
rewrites nothing. What it says goes to the model after the tool's output: exit
code `2` (or `"decision": "block"`) hands it your stderr (or `reason`) as
feedback — "lint failed on line 12" — and `context` (or Claude Code's
`additionalContext`) is appended the same way. Exit `49` ends the turn.

```jsonc
{
  "hooks": {
    "PostToolUse": [
      { "matcher": "^(edit|multiedit|write)$", "command": "./hooks/gofumpt.sh" }
    ]
  }
}
```

```bash
#!/usr/bin/env bash
# hooks/gofumpt.sh — format every Go file the agent writes.
f="$KLASH_TOOL_INPUT_FILE_PATH"
[[ "$f" == *.go ]] || exit 0
if ! out=$(gofumpt -w "$f" 2>&1); then
  echo "gofumpt failed on $f: $out" >&2
  exit 2
fi
```

**Formatters**: a hook that changes the file the agent just wrote (with edit,
multiedit, write or lsp_replace_symbol) would make the agent's next edit of it
fail as "modified since it was last read". Klash
records a fresh read of the file instead, and tells the model the hook changed
it, so it can view it again before editing near what changed. Claude Code does
the same.

**Scope**: like `PreToolUse`, every tool call, a sub-agent's too.

### PostToolUseFailure

This hook fires right after a tool call fails: the tool returned an error the
model reads, such as an `edit` whose `old_string` was not found. It works like
`PostToolUse`: it is matched against the tool name, the call has already run,
exit code `2` (or `"decision": "block"`) hands your reason to the model after
the error, `context` is appended the same way, and exit `49` ends the turn. Use
it to log failures or to add a hint, say "view the file first", when a tool
keeps failing.

### UserPromptSubmit

This hook fires when you send a prompt, before the agent sees it. Use it to
hand the agent context you would otherwise type every time (the current branch,
what failed in CI, notes from your own tools), or to stop a prompt that must
not go out, one with a secret in it, say.

**Matched against**: nothing, as for `Stop`. A `matcher` on a `UserPromptSubmit`
hook is a config error, and every configured hook runs.

**Context**: whatever the hook prints on stdout, as plain text, is what the
agent reads after your prompt, inside a `<user-prompt-submit-hook>` tag, as in
Claude Code. JSON output works too: Klash's `context`, or Claude Code's
`hookSpecificOutput.additionalContext`. The chat shows only what you typed; the
context is kept with the prompt, so the agent reads the same thing when the
conversation is rebuilt on a later turn.

**Blocking**: exit code `2` (or `"decision": "block"`) blocks the prompt, with
stderr (or `reason`) as the reason: the agent never sees it, nothing is added to
the session, and you get the reason as an error. Exit `49` (or `"continue":
false`) does the same.

**When it runs**: once, when the prompt is sent. A prompt sent while the agent is
working runs its hook then and waits in the queue with what the hook added; it
does not run the hook again when its turn comes. While the hook runs the session
counts as busy, and Esc stops the hook and the prompt is not sent. Prompts the
agent sends itself (a `Stop` hook's or `/loop`'s continuation), a sub-agent's
prompt and a channel message (from Signal, say, written by someone else) do not
fire it. A hook that could not evaluate (exit `3`, an error it reports on
stderr, or one that could not run or timed out) blocks the prompt.

**What it reads**: `prompt` is the prompt as the agent will read it: what you
typed, followed by the text of any pasted or attached text file. A paste of more
than ten lines becomes an attachment, so a hook that looks for secrets sees it
too.

```bash
#!/usr/bin/env bash
# hooks/branch-context.sh — tell the agent where it is.
echo "Current branch: $(git branch --show-current)"
echo "Last commit: $(git log -1 --format='%h %s')"
```

```bash
#!/usr/bin/env bash
# hooks/no-secrets.sh — keep keys out of prompts.
if jq -r .prompt | grep -qE 'sk-[A-Za-z0-9]{20,}'; then
  echo "That prompt has an API key in it; it was not sent." >&2
  exit 2
fi
```

### SessionStart

This hook fires before the first prompt a process sends to a session, so it
can hand the agent context for the session up front: what the branch is, what
the last failure was, what your notes say about the repo. Use it instead of
typing the same preamble into every new session.

**Matched against**: the source, `"startup"`, `"resume"`, `"clear"` or
`"compact"`. A `matcher` on a `SessionStart` hook is tested against that
source, so a hook matched on `^resume$` only fires when you go back to a
session that already has messages, and a hook with no matcher fires for
every source.

- `"startup"` — the session is new and this process has started no session
  of its own yet.
- `"resume"` — the session already has messages: you went back to it, with
  `--continue`, `--session`, or the session list.
- `"clear"` — the session is new, but this process has already started
  another session: the TUI's New Session command, say.
- `"compact"` — the first prompt after the session's conversation was
  summarized, as Claude Code fires it after a compaction.

**Context**: whatever the hook prints on stdout, as plain text, is what the
agent reads with the first prompt, inside a `<user-prompt-submit-hook>` tag,
as for `UserPromptSubmit`; the prompt's `UserPromptSubmit` hooks run after
`SessionStart`'s, as in Claude Code, and what they print comes after it. JSON output works too: Klash's `context`, or
Claude Code's `hookSpecificOutput.additionalContext`. The context is kept
with the prompt, so the agent reads it again when the conversation is
rebuilt on later turns.

**Blocking**: exit code `2` (or `"decision": "block"`) means the hook
objects, but the prompt goes ahead anyway, as in Claude Code: you get the
hook's reason as a warning. Exit `49` (or `"continue": false`) does the
same. A hook that could not evaluate (exit `3`, an error on stderr, or a
timeout) is shown as a warning too, and the prompt goes ahead without it.

**When it runs**: once per start, before the first prompt the process
sends to the session, whether that prompt is visible or a hidden
continuation. However many prompts are sent at once, the hooks run once:
the first prompt to take the submit lock runs them, and the others wait
and then see the session as already started. Later prompts to the same
session do not run them, except the first one after a summary, which runs
them again with source `"compact"`. The state is shared by every top-level
agent the process runs, so a session started by the coder counts as
started for the plan agent and vice versa, and a summary made in either
mode fires `"compact"` for the next prompt in either. When a
`UserPromptSubmit` hook blocks the first prompt, what `SessionStart`
printed is kept and sent out with the next prompt to the session, so it
is neither lost nor sent twice. While the hooks run the prompt counts as
submitting, as for `UserPromptSubmit`: the session reads as busy, and Esc
stops the hooks and the prompt is not sent.

**Scope**: like `PreToolUse` and `Stop`, `SessionStart` fires only for the
**top-level agent**, and not for sub-agents.

```jsonc
{
  "hooks": {
    "SessionStart": [
      { "command": "./hooks/session-context.sh" },
      { "matcher": "^resume$", "command": "./hooks/what-was-I-doing.sh" }
    ]
  }
}
```

```bash
#!/usr/bin/env bash
# hooks/session-context.sh — tell the agent where it is, on every new
# session.
echo "Current branch: $(git branch --show-current)"
echo "Last commit: $(git log -1 --format='%h %s')"
```

### Stop

This hook fires when the agent is about to finish its turn. Use it to refuse the
stop: run the test suite, check the linter, or look at the answer the agent is
about to end on, and send it back to work if the job isn't done.

**Matched against**: nothing. There is no tool in play at the end of a turn, so
a `matcher` on a `Stop` hook is a config error rather than a setting that
matches nothing. Every configured `Stop` hook runs.

**Blocking**: exit code `2` means "you are not finished" — the turn stays alive
and your stderr is handed to the model as the reason to keep working. Exit code
`49` means the opposite: end the turn now, even if another `Stop` hook blocked
it. A hook that could not evaluate (exit `3`, an error it reports on stderr,
or one that could not run or timed out) keeps the turn going as exit `2` does:
a check that did not run has not passed. The user is told why.

```jsonc
{
  "hooks": {
    "Stop": [
      { "command": "./hooks/tests-must-pass.sh" }
    ]
  }
}
```

```bash
#!/usr/bin/env bash
# hooks/tests-must-pass.sh — don't let the turn end on a red test suite.
if ! go test ./... >/tmp/stop-hook.log 2>&1; then
  echo "Tests are still failing:" >&2
  tail -20 /tmp/stop-hook.log >&2
  exit 2
fi
```

**No cap — the agent reflects instead**: there is no limit on how many times a
`Stop` hook may say "not done"; some tasks genuinely take many attempts, and
giving up on a counter would defeat the point of the hook. Instead, at every
10th consecutive block the agent is asked to step back and judge honestly
whether there is a genuine reason to stop — the task cannot be done as asked,
it is blocked on something only you can provide or decide, the hook's check
cannot pass in this environment, or it is repeating itself without progress. If
it finds one, it explains why and the turn ends, and Klash shows a warning
quoting its reason and the hook's last objection. Otherwise it keeps working.
You can always cancel the turn yourself.

A hook that wants to object only once can still stand down on its own:
`stop_hook_active` in the payload (and `KLASH_STOP_HOOK_ACTIVE` in the
environment) is set when the turn is only still running because an earlier
`Stop` hook blocked it.

**Scope**: like `PreToolUse`, `Stop` fires only for the **top-level agent**, and
not for sub-agents. It is also skipped when the turn is being continued for
auto-summarization, since that turn isn't ending either.

### SubagentStop

This hook fires when a **sub-agent** — one started by the `agent` tool — is
about to finish. It works exactly the way `Stop` does for the main agent:
use it to refuse the stop, check the sub-agent's work, and send it back to
work if the job isn't done. It fires only for sub-agents, and never for the
main agent.

**Matched against**: nothing, as for `Stop`. There is no tool in play when a
sub-agent finishes, so a `matcher` on a `SubagentStop` hook is a config error
rather than a setting that matches nothing. Every configured `SubagentStop`
hook runs.

**Blocking**: exit code `2` (or `"decision": "block"`) means "you are not
finished" — the sub-agent stays alive and your stderr is handed to it as the
reason to keep working. Exit code `49` means the opposite: end the sub-agent's
work now, even if another `SubagentStop` hook blocked it. A hook that could
not evaluate (exit `3`, an error it reports on stderr, or one that could not
run or timed out) keeps it working as exit `2` does, and the user is told why. When the hooks let the sub-agent finish, the `agent`
tool hands the parent its last answer — the one after the hooks let it end.

The same persistence rules as `Stop` apply: there is no cap on how many times
a `SubagentStop` hook may object, and at every 10th consecutive block the
sub-agent is asked to judge whether there is a genuine reason to stop (the
task cannot be done as asked, it is blocked on something only the user can
provide or decide, the check cannot pass in this environment, or it is
repeating itself without progress). If it finds one, it explains why and
finishes, and Klash shows a warning quoting its reason and the hook's last
objection. Otherwise it keeps working. A hook that wants to object only once
can stand down on its own: `stop_hook_active` in the payload (and
`KLASH_STOP_HOOK_ACTIVE` in the environment) is set when the sub-agent is only
still working because an earlier `SubagentStop` hook blocked it.

**Payload**: as in Claude Code, the stdin JSON carries, beside the common
fields, `"hook_event_name": "SubagentStop"`, the session the sub-agent works
for (the one you are in) as `session_id`, the sub-agent's own session as
`agent_id` and its type as `agent_type` (`task`, for the `agent` tool's
sub-agents), `stop_hook_active` as in a `Stop` payload, and
`last_assistant_message`, the text of the answer the sub-agent is about to
finish on. A failed hook's warning shows in the session you are in.

```jsonc
{
  "hooks": {
    "SubagentStop": [
      { "command": "./hooks/subagent-must-verify.sh" }
    ]
  }
}
```

```bash
#!/usr/bin/env bash
# hooks/subagent-must-verify.sh — don't let a sub-agent finish without
# having actually checked its work.
read -r input
msg=$(echo "$input" | jq -r '.last_assistant_message')
if ! echo "$msg" | grep -qi 'verified'; then
  echo "You have not verified your work yet; do that before finishing." >&2
  exit 2
fi
```

**Scope**: `SubagentStop` fires only for **sub-agents**, and never for the
top-level agent — whose gate is `Stop`.

### SessionEnd

This hook fires when a session ends, as in Claude Code. Use it to log the
session, clean up, or record how it ended.

**Matched against**: the reason, `"clear"`, `"other"` or
`"prompt_input_exit"`. A `matcher` on a `SessionEnd` hook is tested against
that reason, so a hook matched on `^clear$` only fires when the user starts
a new session, and a hook with no matcher fires on every reason.

- `"clear"` — the user started a new session: the TUI's New Session
  command, say.
- `"other"` — the user switched to another session, or Klash exited with
  the session open under `klash run`.
- `"prompt_input_exit"` — Klash exited with the session open under the TUI.

**Blocking**: none. As in Claude Code, a `SessionEnd` hook cannot stop
anything and what it prints is ignored: the exit goes ahead whatever the
hook says. A hook that could not evaluate (it could not run, timed out, or
failed with a reason on stderr) is logged, and the exit goes ahead without it.

**Timeout**: Claude Code's default for this event is 1.5 seconds, so
exiting Klash is not held up for long. A hook that sets its own `timeout`
gets it, up to a cap of 60 seconds.

**Scope**: `SessionEnd` fires only for the **top-level agent**, and not for
sub-agents. A session ends once: switching away from it and then exiting
does not end it again, and a session that was never open — the new, still
empty one — does not end at all.

```jsonc
{
  "hooks": {
    "SessionEnd": [
      { "command": "./hooks/log-session-end.sh" },
      { "matcher": "clear", "command": "./hooks/on-new-session.sh" }
    ]
  }
}
```

```bash
#!/usr/bin/env bash
# hooks/log-session-end.sh — record how the session ended.
read -r input
reason=$(echo "$input" | jq -r '.reason')
echo "Session $KLASH_SESSION_ID ended: $reason" >> ~/.local/share/klash/session-ends.log
```

### PreCompact

This hook fires just before a session's conversation is summarized: when you
ask for the summary, or automatically when the context is nearly full. As in
Claude Code, a hook cannot stop the summary — it is told, through a `trigger`
field in its payload, whether the summary is manual or automatic, and a hook
that blocks or fails is shown as a warning while the summary goes ahead.

**Matched against**: the trigger, `"manual"` or `"auto"`. A `matcher` on a
`PreCompact` hook is tested against that trigger, so a hook matched on
`^manual$` only fires when you ask for the summary, and a hook with no matcher
fires on both.

**Blocking**: exit code `2` (or `"decision": "block"`) means the hook objects,
but the summary goes ahead anyway; you get the hook's reason as a warning.
Exit `49` (or `"continue": false`) does the same. A hook that could not
evaluate is also shown as a warning, and the summary goes ahead without it.

**Scope**: like `PreToolUse` and `Stop`, `PreCompact` fires only for the
**top-level agent**, and not for sub-agents.

```jsonc
{
  "hooks": {
    "PreCompact": [
      { "matcher": "^manual$", "command": "./hooks/before-summarize.sh" }
    ]
  }
}
```

```bash
#!/usr/bin/env bash
# hooks/before-summarize.sh — say goodbye before the conversation is
# summarized, only when the user asked for it.
read -r input
trigger=$(echo "$input" | jq -r '.trigger')
[[ "$trigger" == "manual" ]] || exit 0
echo "The user asked for a summary; make sure the important context is captured." >&2
exit 2
```

### StopFailure

This hook fires instead of `Stop` when a turn ends on an error from the
model's API, as in Claude Code: to log it, or to raise an alert when your model
server goes down.

**Matched against**: the error's kind, in Claude Code's names: `rate_limit`,
`overloaded`, `authentication_failed`, `billing_error`, `model_not_found`,
`invalid_request`, `server_error` or `unknown`. A model server that refused the
connection, never answered or timed out is `server_error`. (Klash first waits
out a model server that is restarting; the hook fires once it gives up.)

**Input**: the common fields, `error` (the kind), `error_details` (what the API
said) and `last_assistant_message` (the error as the conversation shows it).
`KLASH_STOP_FAILURE_ERROR` holds the kind.

**Blocking**: none. What the hook prints and how it exits are ignored; one that
fails is logged.

**Scope**: the **top-level agent** only, as for `Stop`.

```jsonc
{
  "hooks": {
    "StopFailure": [
      { "matcher": "server_error", "command": "notify-send 'klash: the model server is down'" }
    ]
  }
}
```

### PostCompact

This hook fires once a session's conversation has been summarized, with the
summary, as in Claude Code: to log the summary, or to update something outside
Klash. It runs after the summary is saved and before any prompt you queued
meanwhile.

**Matched against**: the trigger, `"manual"` or `"auto"`, as for `PreCompact`.

**Input**: the common fields, `trigger`, and `compact_summary`, the summary the
conversation was compacted to. `KLASH_POST_COMPACT_TRIGGER` holds the trigger.

**Blocking**: none. The summary is done; a hook that blocks, or that could not
evaluate, is shown to you as a warning.

**Scope**: like `PreCompact`, only the **top-level agent**'s summaries.

```jsonc
{
  "hooks": {
    "PostCompact": [
      { "command": "jq -r .compact_summary >> ~/.local/share/klash/summaries.log" }
    ]
  }
}
```

Hooks are keyed by event name. Only `command` is required, and you can omit
`matcher` to match all tools.

## Building Hooks

When a hook fires, Klash:

1. Filters hooks whose `matcher` matches the tool's name, or Claude Code's name
   for it (no matcher, or `*`, matches all; see [Matchers](#pretooluse)).
2. Deduplicates by `command` (identical commands run once).
3. Runs all matching hooks **in parallel** through Klash's embedded POSIX
   shell (see [Execution model](#execution-model)).
4. Waits for all to finish (or time out), then aggregates results **in config
   order**: deny wins over allow, allow wins over none; `updated_input` patches
   shallow-merge in order.
5. Applies the result **before** permission checks. If the aggregated decision
   is `deny`, the tool call is blocked and you never see a permission prompt
   for it. If it's `allow`, Klash treats that as affirmative pre-approval and
   also skips the prompt. Silence (no decision) falls through to the normal
   permission flow.

Note that you can omit `matcher` and match in your shell script instead,
however you'll incur some additional overhead as Klash will still parse and
run each hook.

### Input

Each hook receives data two ways: environment variables and stdin (as JSON).
Environment variables are typically easier to work with, with JSON being
available when input is more complex.

#### Environment Variables

The available environment variables are:

| Variable                     | Description                                    |
| ---------------------------- | ---------------------------------------------- |
| `KLASH`                      | Always `1` when running under Klash.           |
| `AGENT`                      | Always `crush`.                                |
| `AI_AGENT`                   | Always `crush`.                                |
| `KLASH_EVENT`                | The hook event name (e.g. `PreToolUse`).       |
| `KLASH_TOOL_NAME`            | The tool being called (e.g. `bash`). Empty on `Stop`. |
| `KLASH_SESSION_ID`           | Current session ID.                            |
| `KLASH_CWD`                  | Working directory.                             |
| `KLASH_PROJECT_DIR`          | Project root directory.                        |
| `CLAUDE_PROJECT_DIR`         | The same, under Claude Code's name, so its hook commands (`"${CLAUDE_PROJECT_DIR}/.claude/hooks/check.sh"`) work unchanged. |
| `KLASH_TOOL_INPUT_COMMAND`   | For `bash` calls: the shell command being run. |
| `KLASH_TOOL_INPUT_FILE_PATH` | For file tools: the target file path.          |
| `KLASH_TOOL_USE_ID`          | `PreToolUse` and `PostToolUse`: the tool call's ID, the same for both. |
| `KLASH_STOP_HOOK_ACTIVE`     | `Stop` and `SubagentStop` only: `1` when the turn is only still running because an earlier hook for the event blocked it, else `0`. |
| `KLASH_AGENT_ID`             | `SubagentStop`, and the tool hooks of a sub-agent's call: the sub-agent's own session ID, as `agent_id` in the payload. |
| `KLASH_AGENT_TYPE`           | `SubagentStop`, and the tool hooks of a sub-agent's call: the sub-agent's type, as `agent_type` in the payload (`task`, or `agentic_fetch`). |
| `KLASH_PRE_COMPACT_TRIGGER`  | `PreCompact` only: `manual` or `auto`, the same as the `trigger` field in the payload. |
| `KLASH_SESSION_START_SOURCE` | `SessionStart` only: `startup`, `resume`, `clear` or `compact`, the same as the `source` field in the payload. |
| `KLASH_SESSION_END_REASON`   | `SessionEnd` only: `clear`, `other` or `prompt_input_exit`, the same as the `reason` field in the payload. |

Klash was called Crush, so every `KLASH_` variable above is also set under its
`CRUSH_` name (`CRUSH_EVENT`, `CRUSH_TOOL_NAME`, and so on), and `CRUSH=1`
beside `KLASH=1`. Either works; the `CRUSH_` names stay for hooks written before
the rename. `AGENT` and `AI_AGENT` still say `crush`, since other tools may
match on that value.

The `KLASH`, `AGENT`, and `AI_AGENT` markers are also set by the `bash`
tool, so a script can detect "am I running under Klash?" the same way in
either context.

#### JSON

Standard input provides the full context as JSON:

```jsonc
{
  "event": "PreToolUse", // Hook event name
  "hook_event_name": "PreToolUse", // The same, under Claude Code's name for it
  "session_id": "313909e", // Current session ID
  "cwd": "/home/user/project", // Working directory
  "tool_name": "bash", // The tool being called
  "tool_use_id": "call_8f2a", // The tool call's ID, the same in its PostToolUse hooks
  "tool_input": { "command": "rm -rf /" }, // The tool's input
  "permission_mode": "default", // The session's permission mode, as in Claude Code
  // A sub-agent's call also has, as in Claude Code:
  // "agent_id": "…",  // the sub-agent's own session
  // "agent_type": "task", // or "agentic_fetch"
}
```

Note that `tool_input` field contains the raw JSON the model sent to the tool.

To parse the stdin JSON in your hook script, read from stdin and use a tool like
`jq`:

```bash
#!/usr/bin/env bash
read -r input
tool_name=$(echo "$input" | jq -r '.tool_name')
command=$(echo "$input" | jq -r '.tool_input.command // empty')
```

You can also use tools like Python:

```python
#!/usr/bin/env python3
import json, sys

data = json.load(sys.stdin)
tool_name = data.get("tool_name", "")
command = data.get("tool_input", {}).get("command", "")
```

### Output

Hooks communicate back to Klash via **exit code** and `stdout`/`stderr`. The
simplest way to do this is to return an error code and print additional context
to stderr. For example:

```bash
# Here, error code 2 blocks the tool, using stderr as the reason:
if some_bad_condition; then
  echo "Blocked: reason here" >&2
  exit 2
fi
```

| Exit Code | Meaning                                                          |
| --------- | ---------------------------------------------------------------- |
| 0         | Success. Stdout is parsed as JSON (see fields below).            |
| 2         | **Block the tool.** Stderr is used as the deny reason (no JSON). |
| 3         | **Could not evaluate.** Blocks as 2 does; stderr says why.       |
| 49        | **Halt the turn.** Stderr is used as the halt reason (no JSON).  |
| 126, 127, 129+ | The hook could not run, or was killed. Blocks as 3 does.    |
| Other     | With a reason on stderr, an error: blocks as 3 does. Silent: no opinion. |

The difference between exit 2 and exit 49:

- **Exit 2** blocks the current tool call. The agent sees the error and can try
  something else.
- **Exit 49** halts the whole turn. The agent doesn't get to respond further;
  the user takes over. Use this when something is wrong enough that the agent
  shouldn't keep trying. 49 sits in an empty slice of the exit-code space —
  between the generic-error range (1-30), the BSD `sysexits.h` range (64-78),
  and the killed-by-signal range (128+) — so it can't be hit by accident.

### Could not evaluate: Klash fails closed

A check can fail to check: the tool it runs is missing, the file it reads is
not there, the service it asks is down. Exiting `0` then passes the work
unchecked, the way "ffmpeg exited 0" once passed for "the audio is fine". Exit
`3` instead, with the reason on stderr:

```bash
#!/usr/bin/env bash
# hooks/video-has-sound.sh: a rendered video must have an audio stream.
command -v ffprobe >/dev/null || { echo "ffprobe is not installed, so the audio was not checked" >&2; exit 3; }
...
```

Klash treats it as a block, as exit `2`, with your reason: the agent reads
that the hook could not check, not that the work is wrong, and the hook's
line says **Could Not Evaluate**. A hook that could not run at all counts the
same: one that never started (its script path is wrong), one whose command
was not found or not executable (exit `127` or `126`, also when a command
inside the script is missing), one that timed out, and one that answered in
JSON that Klash could not read (an unknown `decision` such as `"Deny"`, or an
`updated_input` that is not an object). Claude Code lets all of these pass.

Where a hook cannot block (`SessionStart`, `PreCompact`), it is shown as a
warning. On `Stop` and `SubagentStop` the agent keeps working; at each
reflection checkpoint it may judge that the check cannot run here, say why,
and stop, and you are told.

Any other exit code counts by what the hook says. With a reason on stderr (a
traceback, `jq: error`, `No such file`), the hook ran into trouble and did not
check: it blocks, as an error. Without a word, it is a test that came out false,
as `[[ $f == *.go ]] && gofmt -w "$f"` exits `1` on every other file: no
opinion. A hook killed by a signal (exit above `128`, the out-of-memory killer
say) blocks too, since it says nothing either. So a hook that logs to stderr
should end with its verdict, `exit 0` included, and one that only wants to warn
exits `0` with Claude Code's `{"systemMessage": "…"}`, which shows you the
message and blocks nothing.

That said, if you need more control, or if you need to rewrite input, you can
use JSON on stdout. Exit 0 and print a JSON object to provide context, update
the input, or still deny/halt with a reason:

```jsonc
{
  "version": 1, // Output envelope version. Optional; defaults to 1.
  "decision": "allow", // "allow" or "deny" ("approve" / "block" also accepted, as in Claude Code). Omit for no opinion.
  "halt": false, // If true, halts the turn entirely.
  "reason": "LGTM", // Shown when denying or halting.
  "context": "Scrubbed secrets", // String or array of strings. Appended to what the model sees.
  "updated_input": { "command": "…" }, // Shallow-merged into the tool's input before execution.
}
```

`version` is an optional integer at the top of the envelope. It defaults to `1`
if omitted. Unknown higher versions are still parsed; the field exists so the
envelope can evolve without a compatibility shim.

`decision: "allow"` is **affirmative**: it pre-approves the tool call and
bypasses the permission prompt entirely. Silence (no `decision`, or
`decision: null`) means "no opinion" — the tool still goes through the
normal permission flow. Use `"allow"` when you want to auto-approve; omit it
when you only want to inject context or rewrite input without also vouching
for the call.

`updated_input` is a shallow-merge patch. Keys you include overwrite matching
keys in `tool_input`; keys you don't include are preserved. If the model called
`bash` with `{"command": "npm test", "timeout": 60000}` and your hook returns
`{"updated_input": {"command": "bun test"}}`, the tool runs with
`{"command": "bun test", "timeout": 60000}` — the timeout isn't dropped. The
merge is shallow: nested objects are replaced wholesale, not deep-merged.

`updated_input` must be a JSON object (a string holding one is also read, for
older hooks; Claude Code's `hookSpecificOutput.updatedInput` must be an actual
object). Anything else is reported as a failed hook, and an `allow` given with
it is dropped too, so the call goes to the normal permission check rather than
being approved without the rewrite it was approved with. A `deny` still holds.

`halt: true` stops the turn entirely. The agent doesn't get to respond further;
the user takes over. The exit-code shorthand is `exit 49` with stderr as the
reason.

`context` accepts either a string or an array of strings. Use the string form
for a single observation; use the array form when a hook produces multiple
distinct notes and you'd rather not concatenate them by hand. Empty strings and
empty array entries are dropped.

Here's a full shell script that produces this JSON:

```bash
#!/usr/bin/env bash
# Example: rewrite a bash command using RTK

read -r input
original_cmd=$(echo "$input" | jq -r '.tool_input.command')
rewritten=$(secret-scrubber rewrite "$original_cmd")

cat <<EOF
{
  "decision": "allow",
  "context": "Scrubbed secrets",
  "updated_input": {"command": "$rewritten"}
}
EOF
```

### Multiple Hooks

Hooks run in parallel, but their results compose in config order. Whichever hook
finishes first doesn't get to "win" by virtue of timing; composition is
deterministic based on the order hooks appear in `klash.json`.

When multiple hooks match the same tool call:

- If **any** hook denies, the tool call is blocked. `reason` values are
  concatenated in config order (newline-separated).
- If **any** hook halts, the turn ends after the tool call is blocked.
- If no hook denies or halts but at least one allows, the tool call proceeds
  **and the permission prompt is skipped**.
- `context` values are concatenated in config order. Strings and arrays compose
  uniformly — each string becomes one entry, and array entries are flattened in.
- `updated_input` patches shallow-merge in config order against the original
  tool input. Later hooks override earlier ones on colliding keys. If denied or
  halted, `updated_input` patches are ignored.

### Timeouts

If a hook exceeds its timeout, Klash cancels its context and treats the
hook as one that could not evaluate, which blocks. The default
timeout is Claude Code's for a command hook: 600 seconds, and 30 for
`UserPromptSubmit`, which holds up the prompt just sent. Shebang-dispatched subprocesses are killed through
`exec.CommandContext`; in-process hooks get a short grace period to yield
and are then abandoned (the agent moves on regardless). Long-running work
should honor context cancellation or run out-of-process via a shebang.

## Examples

### Block destructive commands

Prevent the agent from running `rm -rf` in bash:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^bash$",
        "command": "./hooks/no-rm-rf.sh"
      }
    ]
  }
}
```

`hooks/no-rm-rf.sh`:

```bash
#!/usr/bin/env bash
# Block rm -rf commands in the bash tool. Otherwise stay silent so the
# normal permission flow runs.

if echo "$KLASH_TOOL_INPUT_COMMAND" | grep -qE 'rm\s+-(rf|fr)\s+/'; then
  echo "Refusing to run rm -rf against root" >&2
  exit 2
fi

exit 0
```

### Auto-approve read-only tools

Skip the permission prompt for tools that can't change anything. The hook
returns `decision: "allow"`, which tells Klash to pre-approve the call:

```jsonc
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^(view|ls|grep|glob)$",
        "command": "echo '{\"decision\":\"allow\"}'",
      },
    ],
  },
}
```

No script file needed — the command is inline. Every `view`/`ls`/`grep`/`glob`
call now runs without prompting. Add the `bash` tool to this list at your own
risk; consider a more targeted allowlist instead:

```bash
#!/usr/bin/env bash
# hooks/safe-bash.sh — auto-approve read-only bash commands.

case "$KLASH_TOOL_INPUT_COMMAND" in
  ls*|cat*|grep*|rg*|echo*|pwd*)
    echo '{"decision":"allow"}'
    ;;
  *)
    # Silent — fall through to the normal permission prompt.
    exit 0
    ;;
esac
```

### Inject context into file writes

Add a reminder to the model whenever it writes a Go file:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "^(edit|write|multiedit)$",
        "command": "./hooks/go-context.sh"
      }
    ]
  }
}
```

`hooks/go-context.sh`:

```bash
#!/usr/bin/env bash
# Remind the model about Go formatting when editing .go files.
# Emit context only; stay silent on `decision` so the normal permission
# prompt still runs for edits/writes.

if [[ "$KLASH_TOOL_INPUT_FILE_PATH" == *.go ]]; then
  echo '{"context": "Remember: run gofumpt after editing Go files."}'
else
  echo '{}'
fi
```

### Block all MCP tools

The `command` can be inline. This one-liner matches all MCP tools and blocks
them:

```jsonc
{ "matcher": "^mcp_", "command": "echo 'MCP tools are disabled' >&2; exit 2" }
```

### Log every tool call

With no `matcher` this fires for every tool. It exits 0 with no stdout so the
tool call always proceeds.

```jsonc
{ "command": "echo \"$(date -Iseconds) $KLASH_TOOL_NAME\" >> ./tools.log" }
```

### A real-world Example:

For a more practical example, see [`rtk-rewrite.sh`](./examples/rtk-rewrite.sh),
which demonstrates how to rewrite tool input using
[RTK](https://github.com/rtk-ai/rtk) to save tokens.

### Using other languages

Hooks aren't limited to shell scripts: any executable works. Here's the same
"block rm -rf" example in some other languages.

#### Lua

`{"matcher": "^bash$", "command": "lua ./hooks/no-rm-rf.lua"}`

```lua
local input = io.read("*a")
local tool_input = input:match('"command":"(.-)"') or ""

if tool_input:match("rm%s+%-[rf][rf]%s+/") then
  io.stderr:write("Refusing to run rm -rf against root\n")
  os.exit(2)
end
```

#### JavaScript

`{"matcher": "^bash$", "command": "node ./hooks/no-rm-rf.js"}`

```js
let input = "";
process.stdin.on("data", (chunk) => (input += chunk));
process.stdin.on("end", () => {
  const { tool_input: toolInput } = JSON.parse(input);

  if (/rm\s+-[rf]{2}\s+\//.test(toolInput.command)) {
    process.stderr.write("Refusing to run rm -rf against root\n");
    process.exit(2);
  }
});
```

---

## Claude Code compatibility

Klash hooks are broadly compatible with [Claude Code
hooks](https://docs.claude.com/en/docs/claude-code/hooks): the config shape,
stdin payload, output envelope, and exit codes line up so most Claude Code
hooks run under Klash unchanged. This document covers the Klash-specific API
only — anything not documented here isn't guaranteed to work.

Both decision vocabularies are understood. Klash's own top-level `decision`
is `"allow"` or `"deny"`; Claude Code's is `"approve"` or `"block"`, which is
how a Claude Code `Stop` hook keeps the agent working. Claude Code's
`hookSpecificOutput.permissionDecision` of `"allow"`, `"deny"` or `"ask"` is
read too. `"ask"` puts the tool call to you, as in Claude Code, even a call
that would run without asking (yolo, `--allowedTools`, `acceptEdits`, a tool
that never asks), with the hook's `permissionDecisionReason` in the prompt;
when you allow it, it runs without another prompt. Where no one can answer,
in `klash run`, the call is refused and the agent reads the reason, as in a
Claude Code `-p` run. Between hooks, `deny` beats `ask` and `ask` beats
`allow`.

Claude Code's other envelope fields are read too. `"continue": false` stops
everything, as a Klash `halt` does, with `stopReason` as the reason.
`"systemMessage"` is shown to you as a warning and blocks nothing: on a tool's
hook line, or as a hook warning for the other events. Top-level
fields count alongside `hookSpecificOutput`, so a `PostToolUse` or `Stop` hook
that blocks with a top-level `"decision": "block"` and sends `additionalContext`
in `hookSpecificOutput` gets both; where both give a decision,
`permissionDecision` wins, as in Claude Code.

Decision values are matched exactly. Anything else — `"Deny"`, `"denied"`,
`"reject"` — is an answer Klash could not read, so the hook could not evaluate:
it blocks, and its line says **Could Not Evaluate**, rather than being quietly
treated as no opinion. A hook that wrote `"Deny"` meant to deny.

One intentional divergence: Klash treats `updated_input` as a shallow-merge
patch against the original `tool_input` rather than a full replacement. Keys
you omit are preserved. See [Output](#output) for details.

---

## Reference

This is the official reference of the narrative above. If prose and this section
disagree, the prose should be presumed canonical for intent, while this section
is canonical for shape.

Both the stdin payload and the output envelope have **common fields** that apply
to every event and **per-event fields** that only some events recognize. When an
event doesn't understand a field, it's ignored.

### Hook config

Each entry under a `hooks.<EventName>` array:

```jsonc
{
  // string. Optional. Friendly display name shown in the TUI. Falls back to
  // command when omitted.
  "name": "no-rm-rf",

  // string. Optional. Tested against the tool name (Klash's or Claude
  // Code's), or against the PreCompact trigger, or against the SessionStart
  // source, or against the SessionEnd reason, as in Claude Code: exact
  // names separated by | or ,, or else a regular expression. Omit it, or
  // use *, to match all.
  // A matcher on a Stop, UserPromptSubmit or SubagentStop hook is a config
  // error, because there is nothing to match when a turn ends or a prompt
  // is sent.
  "matcher": "^bash$",

  // string. Required. Shell command to run.
  "command": "./hooks/my-hook.sh",

  // number. Optional. Seconds before the hook is killed. Defaults to 60.
  "timeout": 10,
}
```

### Stdin payload (common)

Present in every hook event:

```jsonc
{
  // string. Hook event name.
  "event": "PreToolUse",

  // string. The same, under Claude Code's name for it.
  "hook_event_name": "PreToolUse",

  // string. Current session ID.
  "session_id": "313909e",

  // string. Working directory when invoked.
  "cwd": "/home/user/project",
}
```

On `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `UserPromptSubmit`,
`Stop` and `SubagentStop`, the events Claude Code gives it to, the payload also
names the session's permission mode as the hook fires:

```jsonc
{
  // string. "plan" for the plan agent; "bypassPermissions" when every
  // permission request is granted (yolo, or a `klash run`); "acceptEdits"
  // under --permission-mode acceptEdits; else "default". A sub-agent's
  // hooks get the mode of the agent that started it.
  "permission_mode": "default",
}
```

### Stdin payload — PreToolUse

Extends the common payload:

```jsonc
{
  // ...common fields...

  // string. The tool being called.
  "tool_name": "bash",

  // string. The tool call's ID. A PostToolUse hook for the same call gets
  // the same one, so a pair of hooks can tell they saw the same call.
  "tool_use_id": "call_8f2a",

  // object. Raw JSON input the model sent to the tool. Shape is per-tool.
  "tool_input": {
    "command": "npm test",
  },
}
```

### Stdin payload — PostToolUse

The PreToolUse payload, plus what the tool returned:

```jsonc
{
  // ...PreToolUse fields...

  // object. What the tool returned. "output" is the text the model reads;
  // "metadata" is the tool's metadata object, when it has one (for bash it
  // includes "exit_code"). Claude Code's tool_response is shaped per tool;
  // Klash's is this one shape for every tool.
  "tool_response": {
    "output": "ok  	example.com/pkg	0.012s",
    "metadata": { "exit_code": 0 },
  },
}
```

### Stdin payload — PostToolUseFailure

The PreToolUse payload, plus why the call failed:

```jsonc
{
  // ...PreToolUse fields...

  // string. The error the tool returned, as the model reads it.
  "error": "old_string not found in file",

  // boolean. True when the call failed because the turn was cancelled.
  "is_interrupt": false,

  // number. How long the call took. PostToolUse payloads carry it too.
  "duration_ms": 12,
}
```

### Stdin payload — UserPromptSubmit

Extends the common payload. `tool_name` is empty and `tool_input` is `{}`:

```jsonc
{
  // ...common fields...

  // string. The prompt as the agent will read it: what you typed, then the
  // text of any pasted or attached text file.
  "prompt": "fix the login flow",

  // array of strings. The names of the files sent with it, if any.
  "attachments": ["screenshot.png"],
}
```

### Stdin payload — SessionStart

Extends the common payload. `tool_name` is empty and `tool_input` is `{}`:

```jsonc
{
  // ...common fields...

  // string. The ID of the model answering.
  "model": "gpt-5",

  // string. "startup" for a new session in a process that has started no
  // session of its own, "resume" for a session that already has messages,
  // "clear" for a fresh session in a running process, and "compact" for
  // the first prompt after a summary. A matcher on a SessionStart hook is
  // tested against this.
  "source": "startup",
}
```

### Stdin payload — SessionEnd

Extends the common payload. `tool_name` is empty and `tool_input` is `{}`:

```jsonc
{
  // ...common fields...

  // string. "clear" when the user started a new session, "other" when
  // they switched to another one or klash run exited with the session
  // open, and "prompt_input_exit" when the TUI exited with it open.
  // A matcher on a SessionEnd hook is tested against this.
  "reason": "clear",
}
```

### Stdin payload — Stop

Extends the common payload. `tool_name` is empty and `tool_input` is `{}`:

```jsonc
{
  // ...common fields...

  // boolean. True when the turn is only still running because an earlier
  // Stop hook blocked it. Return success while this is set if your hook
  // should object once and then stand down.
  "stop_hook_active": false,

  // string. The assistant message the turn is about to end on, so the hook
  // can judge the answer without reading the session back out of the database.
  "last_assistant_message": "I've fixed the parser.",
}
```

### Stdin payload — PreCompact

Extends the common payload. `tool_name` is empty and `tool_input` is `{}`:

```jsonc
{
  // ...common fields...

  // string. "manual" when the user asked for the summary, "auto" when the
  // context filled up. A matcher on a PreCompact hook is tested against this.
  "trigger": "manual",

  // string. The instructions to fold into the summary, as in Claude Code.
  // Klash has none of its own, so this is always the empty string.
  "custom_instructions": "",
}
```

### Output envelope (common)

Fields a hook may print to stdout on exit 0. All are optional and apply to every
event:

```jsonc
{
  // number. Defaults to 1. Unknown higher values still parse; exists for
  // forward-compat.
  "version": 1,

  // boolean. If true, ends the turn entirely. User takes over.
  "halt": false,

  // string. Shown when denying (to the model) or halting (to the model and
  // user).
  "reason": "not allowed",

  // string | string[]. Appended to what the model sees. Empty entries are
  // dropped.
  "context": "Rewrote with RTK",
}
```

### Output envelope — PreToolUse

Extends the common envelope:

```jsonc
{
  // ...common fields...

  // "allow" | "deny" | null. null/omitted = no opinion, the tool still goes
  // through the normal permission prompt. "allow" is affirmative: pre-approves
  // the tool call and bypasses the prompt. "deny" blocks the call; the model
  // sees the error and may try something else.
  "decision": "allow",

  // object. Shallow-merge patch against tool_input. Nested objects are
  // replaced wholesale, not deep-merged.
  "updated_input": {
    "command": "bun test",
  },
}
```

### Exit codes

| Code  | Meaning                                                                  |
| ----- | ------------------------------------------------------------------------ |
| `0`   | Success. Stdout is parsed as the output envelope.                        |
| `2`   | Block this tool call. Stderr becomes the deny reason. Stdout is ignored. |
| `3`   | Could not evaluate. Blocks as `2` does; stderr says why.                 |
| `49`  | Halt the whole turn. Stderr becomes the halt reason. Stdout is ignored.  |
| `126`, `127`, `129`+ | The hook could not run, or was killed. Blocks as `3` does. |
| other | With a reason on stderr, an error: blocks as `3` does. Silent: no opinion. |

Exit `2` blocks the tool call on `PreToolUse` and keeps the agent working on
`Stop`. On `PostToolUse`, where the call has already run, it hands stderr to the
model as feedback instead.

### Aggregation

When multiple hooks match the same event, results compose in **config order**.

Universal rules:

1. `halt` is sticky: if any hook halts, the turn ends.
2. `reason` values concatenate with `\n` in config order. Halt-only hooks
   without a deny still contribute their reason.
3. `context` values concatenate with `\n` in config order. String entries and
   array entries flatten uniformly.

PreToolUse-specific rules:

4. `decision` precedence: `deny` > `allow` > `null`. First deny determines the
   outcome; subsequent allows don't override. If the final aggregated decision
   is `allow`, Klash pre-approves the tool call and skips the permission
   prompt. If it's `null` (no hook allowed), the tool goes through the normal
   permission flow.
5. `updated_input` patches shallow-merge sequentially against the original
   `tool_input`. Later patches override earlier ones on colliding keys. Patches
   are **ignored** if the final decision is deny or halt.

### Environment variables

See [Environment Variables](#environment-variables) above for the full list.

---

## Whatcha think?

We'd love to hear your thoughts on this project. Need help? We gotchu. You can
find us on:

- [Twitter](https://twitter.com/charmcli)
- [Slack](https://charm.land/slack)
- [Discord](https://charm.land/discord)
- [The Fediverse](https://mastodon.social/@charmcli)
- [Bluesky](https://bsky.app/profile/charm.land)

---

Part of [Charm](https://charm.land).

<a href="https://charm.land/"><img alt="The Charm logo" width="400" src="https://stuff.charm.sh/charm-banner-softy.jpg" /></a>

<!--prettier-ignore-->
Charm热爱开源 • Charm loves open source
