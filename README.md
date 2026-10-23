# Klash

**A security-first coding agent for your terminal, built for gno.land.**

Klash is a fork of Charm's [Crush](https://github.com/charmbracelet/crush):
the same terminal UI, sessions, LSP and MCP support, and any model you like,
from a hosted API to one running on your own machine. On top of that it keeps
your keys and your work to itself, knows Gno and gno.land natively, speaks
Claude Code's flags and hooks, and fixes a long list of things that broke,
hung or failed without saying so.

- [Security first](#security-first)
- [Built for gno.land](#built-for-gnoland)
- [Coming from Claude Code](#coming-from-claude-code)
- [What else changed from Crush](#what-else-changed-from-crush)
- [Installation](#installation)

## Security first

### Nothing phones home

Upstream Crush talks to Charm from its first start: usage metrics to its
analytics service, the provider catalog from its servers, and a release check
on GitHub. Klash does none of that unless you ask. With a fresh config, the
only traffic is to the model provider you chose.

| Upstream Crush does this unless switched off           | Klash does it only when turned on                                             |
| ------------------------------------------------------ | ----------------------------------------------------------------------------- |
| Sends usage metrics to Charm                           | The first-run question, `option metrics true`, or `KLASH_DISABLE_METRICS=0` — and only ever to your own collector |
| Fetches the provider catalog from Charm's servers      | `option provider-auto-update true`, or `KLASH_DISABLE_PROVIDER_AUTO_UPDATE=0` |
| Checks GitHub for a newer release of upstream Crush    | `KLASH_DISABLE_UPDATE_CHECK=0`                                                |

The first time you run Klash on a machine it asks, once, whether to allow
anonymous analytics. Pressing Enter declines, and the answer is remembered, so
nothing is sent until you say yes and nothing goes to Charm either way. Without
the catalog fetch, Klash uses the list built into the binary, and
`klash update-providers` refreshes it by hand. `DO_NOT_TRACK=1` keeps metrics
off whatever else is set. [Metrics](#metrics) is the detail.

### Your keys never reach the model

Signing a gno.land transaction needs a key's password. Klash's `gnokey` tool
shows you the exact command, then asks for the password in a masked prompt.
What you type goes straight to gnokey's standard input and is scrubbed from
its output. The model never sees it, and it is not saved with the session.
Paying for a web resource [with x402](#built-for-gnoland) works the same way:
you see the price first, and gnokey signs.

That prompt is the only route to a signature, and the agent cannot step
around it:

- The shell tool refuses gnokey's password-on-stdin flag, so the agent
  cannot feed a password in by another door.
- Key management (`gnokey add`, `delete`, `export` and the rest) is refused
  outright and stays in your own terminal.
- A run with nobody there to type, such as `klash run`, has no signing tool
  at all, so the agent prints the command for you to run yourself.

So every signature is one you saw and typed for. To let something sign
without you, give it a [session account](#built-for-gnoland) instead: you
authorize it once, at the prompt, and the chain holds it to its limits from
then on.

### Untrusted text stays untrusted

Web pages, command output and server replies can carry a model's own control
tokens, such as `<|im_start|>` or llama.cpp's media marker, to rewrite the
conversation from inside a tool result. Klash defuses them before the model
reads them. Tool output is capped by its raw size, so a runaway command cannot
flood the context.

### Mistakes fail closed

- A hook with a misspelt event name, or a `klashrc` option Klash does not
  know, stops it at startup with the names it does know. Upstream ignored
  such a hook, so a misspelt guard simply never ran.
- A hook whose answer is malformed cannot approve a tool call by accident, and
  a deny it gave still holds.

### What still leaves your machine

- Your prompts, and the files the agent reads, go to the model provider you
  choose. Point Klash at a [local model](#local-models) and nothing leaves.
- `fetch`, `download`, `agentic_fetch` and `x402_fetch` reach the sites the
  agent asks for, after asking you. `x402_fetch` also reads the paying account
  from a node of the seller's chain.
- The `sourcegraph` tool sends the agent's code search to sourcegraph.com
  without asking. Turn it off with `permissions deny sourcegraph`.
- MCP servers you add reach whatever they reach.

## Built for gno.land

[gno.land](https://gno.land) is a smart-contract platform whose contracts,
called realms, are written in Gno, an interpreted and deterministic Go. Klash
knows it the way it knows Go.

**The gno skill.** A built-in skill loads itself whenever a task touches
`.gno` files, `gnomod.toml`, gnokey or gnodev. It teaches the agent how Gno
differs from Go, how realms keep state, and how a realm tells who is calling
it (`cur realm`, `cross(cur)`, `cur.Previous()`). It covers testing with
`gno test` and filetests, running a local chain with `gnodev`, querying any
chain for free, and deploying and calling realms. It carries the review
checklist from gno's own guide for realm authors, and the chain ids and
endpoints of every network, with mainnet called out because its coins are
real.

**Deploy and call with your key, never the model's.** The `gnokey` tool runs
`maketx addpkg`, `call`, `send` and `run`, `sign` and `broadcast`, through the
masked password prompt above. You see the full command before anything is
signed. Read-only queries need no password.

**Session accounts.** gno.land lets a master key authorize a separate signing
key, a session, within limits the chain itself enforces:

- **An expiry**, after which the key is dead.
- **A spend limit per period**, counted across every coin that leaves the
  master's account, gas and storage deposits included. Refunds never top it
  back up.
- **The messages it may send, and the realms it may call**, by path prefix.
- **A floor no setting overrides**: a session can never create or revoke
  sessions, and never add a package.

Realms see the master as the caller, so nothing on the realm side changes.
Klash's `gnokey` tool creates and revokes sessions (`gnokey maketx session
create`, `revoke`, `revokeall`) through the same masked prompt. You can give a
browser wallet, a device or a script bounded authority instead of your master
key. A leaked session key costs at most what is left of its limit before it
expires, and revoking it is one command. Sessions are live on mainnet and on
the onyx testnet.

**Pay for what you fetch, with x402.** Some web resources charge per request
through [x402](https://x402.org): they answer `402 Payment Required` with a
price on a gno.land chain. When you tell the agent to pay, its `x402_fetch`
tool shows you the price, the payee, the chain and the network fee, and asks
for the key's password in the masked prompt above. gnokey signs one bank send
of exactly that price, the seller's facilitator settles it, and the agent gets
the resource and the transaction hash. Klash checks the signature before the
payment leaves, pays only in ugnot, at most 1 GNOT at a time plus a network
fee of at most 1 GNOT, and only on local and test chains: x402 on gno.land is
unaudited, testnet-only work, so mainnet is refused. Pay from a session key and
the chain caps what it can spend as well. Nothing is paid without you at the
prompt: a run with nobody there to type has no paying tool. Klash signs these
payments with the Go x402 client from
[jaekwon/x402](https://github.com/jaekwon/x402).

**gnopls.** Install the [Gno language server](https://github.com/gnoverse/gnopls)
and Klash starts it for `.gno` files on its own, as it does gopls for Go.

**gno-mcp.** The gno team's MCP server, [gno-mcp](https://mcp.gno.dev), runs in
Klash like any other:

```bash
# klashrc
mcp add gnomcp --command gnomcp
```

It adds tools for reading realms, packages, accounts and history. Its own
safety rules confine its writes to local and test chains, keep mainnet
read-only, and never hand it your key: to act as you, it proposes a session
that you sign with gnokey yourself.

## Coming from Claude Code

Claude Code's names work here too.

| Claude Code                      | Klash                                              | What it does                                                |
| -------------------------------- | -------------------------------------------------- | ----------------------------------------------------------- |
| `claude -p "…"`                  | `klash run "…"`, or `klash -p "…"`                 | Answer one prompt without the TUI                           |
| `-p --output-format json`        | `klash run --output-format json`                   | Print one JSON result at the end                            |
| `-p --output-format stream-json` | `klash run --output-format stream-json`            | A JSON line per message and tool result, then the result    |
| `claude -r <id>`                 | `klash -s <id>`, or `klash -r <id>`                | Continue a session by ID                                    |
| `claude -r`                      | `klash -r`                                         | Pick a session to continue from a list                      |
| `claude -c`                      | `klash -C`                                         | Continue the last session; `-c` sets the working directory |
| `--dangerously-skip-permissions` | `--yolo`, or `--dangerously-skip-permissions`      | Skip every permission prompt                                |
| `--permission-mode plan`         | `--permission-mode plan` (or `acceptEdits`, `bypassPermissions`) | Start in plan mode; or let edits, or everything, run without asking |
| `--add-dir ../other`            | `--add-dir ../other` (repeatable)                  | Let the agent read another directory without asking         |
| `--allowedTools Read Grep`       | `--allowedTools "Read Grep"`, or `Read,Grep`       | Let these tools run without asking                          |
| `--disallowedTools Bash Edit`    | `--disallowedTools "Bash Edit"`, or `Bash,Edit`    | Do not give the agent these tools                           |
| `--tools "Read,Grep"`            | `--tools "Read,Grep"` (`""`: none)                 | Give the agent only these built-in tools                    |
| `--system-prompt "…"`            | `--system-prompt "…"`, or `--system-prompt-file`   | Use these instructions in place of Klash's own              |
| `--append-system-prompt "…"`     | `--append-system-prompt "…"`, or its `-file` form  | Add this text at the end of the system prompt               |
| `claude doctor`                  | `klash doctor`                                     | Check config, providers, models, hooks and tools            |

- **Hooks** take Claude Code's events and answers: `PreToolUse`,
  `PostToolUse`, `PostToolUseFailure`, `UserPromptSubmit` and `Stop`, with
  `tool_use_id`, `hook_event_name`, `continue: false` and `hookSpecificOutput`.
  A UserPromptSubmit hook can add context to every prompt or block one, and a
  Stop hook can keep the agent working until your tests pass. See
  [docs/hooks](docs/hooks/README.md).
- **Context and skills.** Klash reads `CLAUDE.md` and `CLAUDE.local.md`
  alongside `KLASH.md` and `AGENTS.md`, and loads skills from `~/.claude/skills`
  and a project's `.claude/skills`. `--system-prompt` keeps them and the
  environment, as Claude Code keeps `CLAUDE.md`: only Klash's own instructions
  give way. Plan mode keeps its own, with `--append-system-prompt` added.
- **`/loop`** keeps a session working on a task until it is done.

## What else changed from Crush

Over a hundred commits, most of them fixing things that broke, hung, lost work
or failed without saying so. The ones you will notice:

**It keeps going.**

- It rides out a restart of the model server instead of losing the turn.
- A slow local model gets longer for its first token, and a stream that stalls
  after that is still caught.
- Replies left half-written by a crash or a kill are finished, so the session
  reopens cleanly.
- The conversation is compacted even when the model's context size is
  unknown.
- `/loop` and Stop hooks keep work going until it is done. An agent caught
  repeating itself is told so, instead of having its turn silently ended.
- Waiting on a background job, and a grep, are both bounded, so neither can
  hang a turn.

**It tells you what happened.**

- `klash run --verbose` shows every tool call, a sub-agent's included, and
  marks the ones that failed. Bash results carry their exit code.
- When an edit is refused because the file changed, when a multi-edit fails,
  or when a grep for code with brackets finds nothing, the agent is told why
  and what to do.
- A language server that is not installed is reported once, and one that
  fails to start is retried with a back-off.

**It is yours to change.**

- The system prompt and every tool description are files you can edit:
  `klash prompt export`, `show`, `path` and `tools`.
- `klash session web` serves a local page to read and edit a session's
  messages.
- `/think` and `/no_think` switch a model's thinking on or off, and it sticks.
- Prompts typed while the agent works are queued, and <kbd>Up</kbd> edits
  them in place.

**Better tools.** `view` reads 2,000 lines by default and cuts an oversized
section instead of failing. `grep` can ignore case, and can search for a
pattern that starts with a dash. Bash points the agent to the file tools for
file work, since they keep track of what it has read.

## Installation

Klash is built from source, with Go 1.27 or newer. From a checkout of this
repository:

```bash
go build -o klash .
install -m 755 klash ~/.local/bin/klash   # or anywhere on your PATH
```

On Oracle Solaris, add `-tags sqlite3_dotlk` so the local database uses
dot-file locking.

Everything named `crush` keeps working. An existing `~/.config/crush`, a
project's `.crush/`, a `crushrc` or `crush.json`, `CRUSH.md` and `CRUSH_*`
variables are all still read, and where a `klash` one exists it wins.
`klash dirs` shows which directories are in use.

## Getting started

Run `klash` in a project directory and press <kbd>ctrl+l</kbd> to pick a
model. Klash works with Anthropic, OpenAI, Google, OpenRouter, Groq and many
more hosted providers, with any OpenAI- or Anthropic-compatible endpoint, and
with models on your own machine through llama.cpp, Ollama or LM Studio (see
[Local Models](#local-models)). Charm's own provider, [Hyper][hyper], is there
too.

## API Keys

You can also use Klash with many other providers such as Anthropic, OpenAI,
Gemini, OpenRouter and so on. Press <kbd>ctrl+l</kbd> to open the model picker,
choose the provider of your choice, and paste your API key.

That said, you can also set environment variables for preferred providers:

| Environment Variable        | Provider                                           |
| --------------------------- | -------------------------------------------------- |
| `HYPER_API_KEY`             | [Charm Hyper][hyper]                               |
| `ANTHROPIC_API_KEY`         | Anthropic                                          |
| `OPENAI_API_KEY`            | OpenAI                                             |
| `VERCEL_API_KEY`            | Vercel AI Gateway                                  |
| `GEMINI_API_KEY`            | Google Gemini                                      |
| `ZAI_API_KEY`               | Z.ai                                               |
| `MINIMAX_API_KEY`           | MiniMax                                            |
| `SYNTHETIC_API_KEY`         | Synthetic                                          |
| `HF_TOKEN`                  | Hugging Face Inference                             |
| `CEREBRAS_API_KEY`          | Cerebras                                           |
| `OPENROUTER_API_KEY`        | OpenRouter                                         |
| `IONET_API_KEY`             | io.net                                             |
| `ALIBABA_SINGAPORE_API_KEY` | Alibaba (Singapore)                                |
| `ALIBABA_US_API_KEY`        | Alibaba (United States)                            |
| `GROQ_API_KEY`              | Groq                                               |
| `AVIAN_API_KEY`             | Avian                                              |
| `OPENCODE_API_KEY`          | OpenCode Zen & Go                                  |
| `VERTEXAI_PROJECT`          | Google Cloud VertexAI (Gemini)                     |
| `VERTEXAI_LOCATION`         | Google Cloud VertexAI (Gemini)                     |
| `AWS_ACCESS_KEY_ID`         | Amazon Bedrock (Claude)                            |
| `AWS_SECRET_ACCESS_KEY`     | Amazon Bedrock (Claude)                            |
| `AWS_REGION`                | Amazon Bedrock (Claude)                            |
| `AWS_PROFILE`               | Amazon Bedrock (Custom Profile)                    |
| `AWS_BEARER_TOKEN_BEDROCK`  | Amazon Bedrock                                     |
| `AZURE_OPENAI_API_ENDPOINT` | Azure OpenAI models                                |
| `AZURE_OPENAI_API_KEY`      | Azure OpenAI models (optional when using Entra ID) |
| `AZURE_OPENAI_API_VERSION`  | Azure OpenAI models                                |
| `MOONSHOT_API_KEY`          | Moonshot                                           |

[hyper]: https://hyper.charm.land

Also note that Klash can support nearly any provider, including
[Local Models](#local-models). For more info see
[Custom Providers](#custom-providers) below.

## Configuration

> [!TIP]
> Klash ships with a builtin skill for configuring itself. Most of the time
> you can just tell what you want it to configure and it will get the job done.

Klash runs great with no configuration. That said, if you do need or want to
customize Klash, you can, with a `klashrc`.

A `klashrc` is just Bash with some Klash-specific builtins. It’s a lot like
a `.bashrc`, just for your Klash. Because Klash has a native, built-in Bash
interpreter, Bash-based config works identically across all platforms, including
Windows.

For example:

```bash
# Add Ollama.
provider add ollama --type ollama --base-url "http://localhost:11434/v1"

# Register a model on Ollama.
model add ollama/llama3.3 --name "Llama 3.3" --context-window 128000

# Auto-approve some tools.
permissions allow view edit

# Include some other file on a specific machine.
if [[ $HOSTNAME == "babysquid" ]]; then
    source ~/my-stuff/babysquid.sh
fi

# Add an MCP server, with a GitHub API token stored in 1Password.
mcp add github \
  --type http \
  --url "https://api.github.com/mcp/" \
  --header Authorization "Bearer $(op read 'op://my-secret-key')"
```

Configuration can be added either local to the project itself, or globally,
with the following priority:

| Priority | Unix-like                 | Windows                               |
| -------- | ------------------------- | ------------------------------------- |
| 1        | `./.klashrc`              | `.\.klashrc`                          |
| 2        | `./klashrc`               | `.\klashrc`                           |
| 3        | `~/.config/klash/klashrc` | `%USERPROFILE%\.config\klash\klashrc` |

(Klash respects the [XDG Base Directory Specification][xdg], so your paths
may differ depending on your `XDG_CONFIG_HOME` value. Data directories such as
`~/.local/share/klash` and `%LOCALAPPDATA%\klash` contain JSON state only; Klash
does not execute a `klashrc` from them.)

Klash was called Crush, and every crush name still works: `~/.config/crush`,
`~/.local/share/crush`, `~/.cache/crush`, a project's `.crush/` data directory
with its `crush.db`, and `crush.json`, `crushrc`, `.crushignore` and `CRUSH.md`.
Wherever only the crush one exists, that one is used, so an existing setup and
its sessions keep working without being moved; once the klash one exists, it
wins. `~/.config/klash` and `~/.local/share/klash` count only once they hold a
config file (`klash.json`, `crush.json`, `klashrc` or `crushrc`), so a `klash`
folder made for something else doesn't hide the `crush` one in use. A project
uses the closest `.klash/` or `.crush/` on the way up to its root.

[xdg]: https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html

What about the old JSON format? It’s still supported, but it should be
considered deprecated. See: [the config docs](./docs/config/) for details.

> [!TIP]
> You can override the user and data config locations by setting:
>
> - `KLASH_GLOBAL_CONFIG`
> - `KLASH_GLOBAL_DATA`
>
> Every `KLASH_` environment variable in this document also works under its
> `CRUSH_` name, from before the rename; when both are set, `KLASH_` wins.

As an additional note, Klash also stores ephemeral data, such as application
state, in one additional location. This is state and should not be edited by
hand, nor should it be considered configuration.

```bash
# Unix
$HOME/.local/share/klash/klash.json

# Windows
%LOCALAPPDATA%\klash\klash.json
```

#### A note on security

Both `klashrc` and `klash.json` are trusted code; `klashrc` runs in a full
shell, and any `$(...)` in `klash.json` runs at load time. Don't launch Klash
in a directory whose config you haven't reviewed, and don't randomly `source`
files from the internet into your config.

### Environment Variables

The top-level `env` field sets environment variables at startup, before
providers are configured. This is useful for variables that affect provider
authentication (e.g. the AWS SDK credential chain) without wrapping the
`klash` command in a shell script or exporting them in your shell profile:

```json
{
  "$schema": "https://charm.land/crush.json",
  "env": {
    "AWS_PROFILE": "my-sso-profile"
  }
}
```

Values support the same `$VAR` and `$(command)` expansion as other config
fields, so you can reference existing environment variables or shell out for
a value.

### Themes

Klash ships with built-in color themes.

#### Switching Themes

Open the command palette with `ctrl+p`, select **Themes**, and browse the
list. The UI previews each theme as you navigate, and pressing `enter`
confirms the selection. Press `esc` to cancel and revert.

#### Editing Themes

Open **Themes**, highlight the theme to customize, and press `ctrl+e`.
Changes preview live as you type. Press `enter` or `ctrl+s` to save, or `esc`
to cancel and revert. User themes are stored globally in the Klash config
directory under `themes/`.

You can also select a theme directly in your config with `active_theme`:

```json
{
  "$schema": "https://charm.land/crush.json",
  "options": {
    "tui": {
      "active_theme": "gruvbox-dark"
    }
  }
}
```

Custom theme palettes are stored as JSON files in the global theme directory.
For example, `~/.config/klash/themes/my-theme.json`:

```json
{
  "base": "gruvbox-dark",
  "primary": "#ff6b6b",
  "bg_base": "#1a1a2e"
}
```

Select it by setting `active_theme` to `my-theme` or from the **Themes**
dialog.

#### Built-In Themes

| Theme | Name |
| --- | --- |
| Charmtone Pantera | `charmtone-panther` (default) |
| Gruvbox Dark | `gruvbox-dark` |

### LSPs

Klash can use LSPs for additional context to help inform its decisions, just
like you would. LSPs can be added manually like so:

```bash
# klashrc

lsp add go --command "gopls" --env "GOTOOLCHAIN go1.24.5"
lsp add typescript --command "typescript-language-server" --args --stdio
lsp add nix --command "nil"
```

### MCPs

Klash also supports Model Context Protocol (MCP) servers through three transport
types: `stdio` for command-line servers, `http` for HTTP endpoints, and `sse`
for Server-Sent Events.

```bash
# klashrc

# Add a local MCP server that runs a Node.js script.
mcp add filesystem --command node --args /path/to/mcp-server.js \
  --timeout 10 --disabled-tools some-tool-name --env NODE_ENV production

# Add a GitHub MCP server that uses an API token.
mcp add github --type http --url https://api.github.com/mcp/ \
  --timeout 10 --header Authorization "Bearer $GH_PAT" \
  --disabled-tools create_issue --disabled-tools create_pull_request

# Add a streaming MCP server that uses SSE.
mcp add streaming-service --type sse --url "https://example.com/mcp/sse" \
  --timeout 10 --header API-Key "$API_KEY"
```

#### MCP OAuth

HTTP and SSE MCP servers that require OAuth can use Klash's built-in
authorization-code flow instead of a static `Authorization` header. Set
`"oauth": true` to enable it:

```json
{
  "mcp": {
    "linear": {
      "type": "http",
      "url": "https://mcp.linear.app/mcp",
      "oauth": true
    }
  }
}
```

##### Pre-registered clients

Some servers (GitHub, Slack) don't support dynamic client registration.
For those, register an OAuth app with the provider and supply the
credentials directly. All values support shell expansion:

```json
{
  "mcp": {
    "github": {
      "type": "http",
      "url": "https://api.github.com/mcp/",
      "oauth": true,
      "oauth_client_id": "Iv1.abc123def456",
      "oauth_client_secret": "$GITHUB_MCP_SECRET",
      "oauth_callback_port": 40704
    }
  }
}
```

When `oauth_client_id` is set, Klash skips dynamic client registration
and authenticates as the specified client. When omitted, Klash attempts
dynamic registration automatically (works with Linear, Notion, and other
servers that support RFC 7591).

#### Sessionless servers

Some HTTP MCP servers are sessionless — they never issue a
`Mcp-Session-Id` and reject the `subscriptions/listen` stream Klash opens
for list-changed notifications, which would otherwise break the
connection. Klash auto-detects known sessionless servers (GitHub MCP,
`api.githubcopilot.com/mcp`), so those need no extra configuration.

For other sessionless servers, mark them explicitly with
`"sessionless": true` (or `--sessionless true` in `klashrc`); set it to
`false` to force the default behavior for an auto-detected URL. The
tradeoff is that a sessionless server won't push live
tool/prompt/resource list-changed notifications.

### Hooks

Klash has preliminary support for hooks. For details, see
[the hook guide](./docs/hooks/).

### Sharing a workspace across clients

When Klash is run against a shared backend (for example two TUIs talking to
the same `klash serve`), clients are grouped into **workspaces** keyed by
their resolved `--cwd`. Two clients with the same `--cwd` join the same
underlying workspace, so they share the session list, message history,
permission queue, LSP, and MCP state.

Joining is implicit: pointing a second client at the same working directory
attaches it to the existing workspace. Each new invocation, however, starts
in its own fresh session by default. To pick up the conversation another
client already has open, use the session manager (the session picker) and
select it. Sessions surface two signals there:

- `IsBusy` is set while an agent turn is in flight for that session.
- `AttachedClients` reports how many clients are currently viewing it.

A non-zero `AttachedClients` (often combined with `IsBusy`) is the cue that a
session is "in progress" on another client and joining it will mirror that
view live.

The first client to create a workspace fixes its process-wide flags. In
particular, `--yolo` and `--debug` follow a **first-wins** rule: later
clients that arrive at the same `--cwd` with different values for those
flags do not change the running workspace. A debug log line is emitted
recording the mismatch, and the workspace keeps the flags it was created
with.

A workspace lives as long as at least one client has an SSE event stream
open against it. When the last stream disconnects, the workspace is torn
down. There is a short grace window right after `POST /v1/workspaces` so a
client that has created the workspace but not yet opened its event stream
does not get reaped before it can attach.

### Global context files

Klash automatically includes two files for cross-project instructions. Think of
these are personal additions to the system prompt.

- `~/.config/klash/KLASH.md`: Klash-specific rules that would confuse other
  agentic coding tools. If you only use Klash, this is the only one you need to
  edit.
- `~/.config/AGENTS.md`: generic instructions that other coding tools might
  read. Avoid referring to Klash-specific features or workflows here. You
  probably only care about this if you use multiple agentic coding tools and
  want to share instructions between them.

You can customize these paths with `option global-context-path`. Repeat the
command to add multiple paths:

```bash
# Load a single markdown file.
option global-context-path "~/path/to/custom/context/file.md"

# Recursively load all Markdown files in the folder.
option global-context-path "/full/path/to/folder/of/files/"
```

### Ignoring Files

Klash respects `.gitignore` files by default, but you can also create a
`.klashignore` file to specify additional files and directories that Klash
should ignore. This is useful for excluding files that you want in version
control but don't want Klash to consider when providing context.

The `.klashignore` file uses the same syntax as `.gitignore` and can be placed
in the root of your project or in subdirectories.

### Allowing Tools

By default, Klash will ask you for permission before running tool calls. If
you'd like, you can allow tools to be executed without prompting you for
permissions. Use this with care.

```bash
permissions allow view ls grep edit mcp_context7_get-library-doc
```

### Disabling Built-In Tools

You can also deny tools, hiding then from the agent entirely:

```bash
permissions deny bash sourcegraph
```

To disable tools from MCP servers, see the [MCP config section](#mcps).

### You only live once

You can also skip all permission prompts completely by running Klash with the
`--yolo` flag (or `--dangerously-skip-permissions`, as Claude Code calls it).
Be very, very careful with this feature.

### Disabling Skills

You can prevent Klash from using certain skills entirely. Disabled skills are
hidden from the agent, including builtin skills and skills discovered from
disk.

```bash
option disable-skill klash-config
```

### Agent Skills

Klash supports the [Agent Skills](https://agentskills.io) open standard for
extending agent capabilities with reusable skill packages. Skills are folders
containing a `SKILL.md` file with instructions that Klash can discover and
activate on demand.

The global paths we looks for skills are:

- `$KLASH_SKILLS_DIR`
- `$XDG_CONFIG_HOME/agents/skills` or `~/.config/agents/skills/`
- `$XDG_CONFIG_HOME/klash/skills` or `~/.config/klash/skills/`
- `~/.agents/skills/`
- `~/.claude/skills/`
- On Windows, we _also_ look at
  - `%LOCALAPPDATA%\agents\skills\` or `%USERPROFILE%\AppData\Local\agents\skills\`
  - `%LOCALAPPDATA%\klash\skills\` or `%USERPROFILE%\AppData\Local\klash\skills\`
- Additional paths configured via `options.skills_paths`

On top of that, we _also_ load skills in your project from the following
relative paths:

- `.agents/skills`
- `.klash/skills`
- `.claude/skills`
- `.cursor/skills`

Or load directories of skills specifically in your config:

```bash
option skill-path "$HOME/squid-skills" "./other-skills"
```

You can get started with example skills from [anthropics/skills](https://github.com/anthropics/skills):

```bash
# Unix
mkdir -p ~/.config/klash/skills
cd ~/.config/klash/skills
git clone https://github.com/anthropics/skills.git _temp
mv _temp/skills/* . && rm -rf _temp
```

```powershell
# Windows (PowerShell)
mkdir -Force "$env:LOCALAPPDATA\klash\skills"
cd "$env:LOCALAPPDATA\klash\skills"
git clone https://github.com/anthropics/skills.git _temp
mv _temp/skills/* . ; rm -r -force _temp
```

#### User-Invocable Skills

Skills can be made invocable as commands from the commands palette
(<kbd>ctrl+p</kbd>). Add `user-invocable: true` to the skill's YAML
frontmatter:

```yaml
---
name: my-hot-skill
description: A skill that can be invoked as a command.
user-invocable: true
---
```

User-invocable skills appear in the commands palette with a `user:` or `project:` prefix:

- Skills from global directories show as `user:skill-name`
- Skills from project directories show as `project:skill-name`

When invoked, the skill's instructions are loaded into the conversation context.

To prevent the model from auto-triggering a skill (while still allowing user invocation), add `disable-model-invocation: true`:

```yaml
---
name: my-skill
description: Only invocable by users, not the model.
user-invocable: true
disable-model-invocation: true
---
```

Skills with `disable-model-invocation` won't appear in the model's available skills list but can still be invoked manually by users.

### Desktop notifications

Klash sends desktop notifications when a tool call requires permission and when
the agent finishes its turn. They're only sent when the terminal window isn't
focused _and_ your terminal supports reporting the focus state.

```bash
# Choose auto, native, osc, bell, or disabled.
option notifications disabled
```

`auto` uses native notifications locally and OSC notifications over SSH when
supported.

### Initialization

When you initialize a project, Klash analyzes your codebase and creates
a context file that helps it work more effectively in future sessions. By
default, this file is named `AGENTS.md`, but you can customize the name and
location with the `initialize-as` option:

```bash
# klashrc
option initialize-as AGENTS.md
```

This is useful if you prefer a different naming convention or want to place the
file in a specific directory (e.g., `KLASH.md` or `docs/LLMs.md`). Klash will
fill the file with project-specific context like build commands, code patterns,
and conventions it discovered during initialization.

### Attribution Settings

By default, Klash adds attribution information to Git commits and pull requests
it creates. You can customize this behavior with `option` commands:

```bash
option attribution-trailer-style co-authored-by
option attribution-generated-with true
```

- `trailer_style`: Controls the attribution trailer added to commit messages
  (default: `assisted-by`)
  - `assisted-by`: Adds `Assisted-by: Klash:[ModelID]` as specified in [the convention](https://docs.kernel.org/process/coding-assistants.html#attribution)
  - `co-authored-by`: Adds `Co-Authored-By: Klash <klash@charm.land>`
  - `none`: No attribution trailer
- `generated_with`: When true (default), adds `💘 Generated with Klash` line to
  commit messages and PR descriptions

### Custom Providers

Klash supports custom provider configurations for both OpenAI-compatible and
Anthropic-compatible APIs.

> [!NOTE]
> Note that we support two "types" for OpenAI. Make sure to choose the right one
> to ensure the best experience!
>
> - `openai` should be used when proxying or routing requests through OpenAI.
> - `openai-compat` should be used when using non-OpenAI providers that have OpenAI-compatible APIs.

#### OpenAI-Compatible APIs

Here’s an example configuration for Deepseek, which uses an OpenAI-compatible
API. Don't forget to set `DEEPSEEK_API_KEY` in your environment.

```bash
provider add deepseek --type openai-compat \
  --base-url "https://api.deepseek.com/v1" \
  --api-key "$DEEPSEEK_API_KEY"

model add deepseek/deepseek-chat \
  --name "Deepseek V3" \
  --context-window 64000 \
  --default-max-tokens 5000 \
  --price-input 0.27 \
  --price-output 1.1 \
  --price-cache-create 1.1 \
  --price-cache-hit 0.07
```

#### Anthropic-Compatible APIs

Custom Anthropic-compatible providers follow this format:

```bash
provider add custom-anthropic \
  --type anthropic \
  --base-url "https://api.anthropic.com/v1" \
  --api-key "$ANTHROPIC_API_KEY" \
  --extra-header anthropic-version 2023-06-01

model add custom-anthropic/claude-sonnet-4-20250514 \
  --name "Claude Sonnet 4" \
  --context-window 200000 \
  --default-max-tokens 50000 \
  --can-reason true \
  --supports-images true \
  --price-input 3 \
  --price-output 15 \
  --price-cache-create 3.75 \
  --price-cache-hit 0.3
```

### Amazon Bedrock

Klash currently supports running Anthropic models through Bedrock, with caching disabled.

A Bedrock provider appears once Klash can find AWS credentials. You can
authenticate in one of two ways:

**API key.** Set `AWS_BEARER_TOKEN_BEDROCK` to a Bedrock API key. This is the
simplest option and never expires mid-session.

**AWS credential chain (SSO, profiles, access keys).** Configure AWS the usual
way with `aws configure` or `aws configure sso`. Klash picks up whatever the
AWS SDK credential chain resolves, including `AWS_PROFILE`, `AWS_ACCESS_KEY_ID`
/ `AWS_SECRET_ACCESS_KEY`, or an SSO session. To select a specific profile,
set `AWS_PROFILE` in your shell (`AWS_PROFILE=myprofile klash`) or in the
top-level [`env`](#environment-variables) config.

If you authenticate via AWS SSO, your session expires periodically. Set
`aws_auth_refresh` to a command that refreshes it. When Bedrock returns a
credential error, Klash runs the command, then retries the request in place
(no duplicate messages, no manual restart):

```json
{
  "$schema": "https://charm.land/crush.json",
  "env": {
    "AWS_PROFILE": "my-sso-profile"
  },
  "providers": {
    "bedrock": {
      "aws_auth_refresh": "aws sso login --profile my-sso-profile"
    },
    "bedrock-europe": {
      "aws_auth_refresh": "aws sso login --profile my-eu-sso-profile"
    }
  }
}
```

- `aws_auth_refresh` — shell command run when AWS credentials expire (e.g. `aws sso login`)

### Vertex AI Platform

Vertex AI will appear in the list of available providers when `VERTEXAI_PROJECT` and `VERTEXAI_LOCATION` are set. You will also need to be authenticated:

```bash
$ gcloud auth application-default login
```

To add specific models to the configuration, configure as such:

```bash
# klashrc — authentication still comes from gcloud and the VERTEXAI_* env vars.
provider add vertexai --type google-vertex

model add vertexai/claude-sonnet-4@20250514 \
  --name "VertexAI Sonnet 4" \
  --context-window 200000 \
  --default-max-tokens 50000 \
  --can-reason true \
  --supports-images true \
  --price-input 3 \
  --price-output 15 \
  --price-cache-create 3.75 \
  --price-cache-hit 0.3
```

### Local Models

Klash can auto-discovers models from local providers. Add a custom provider
with `type` set to `llamacpp`, `omlx`, `lmstudio`, `litellm`, or `ollama`
and leave out the models list. Klash will populate the model list
automatically.

```bash
# Piece of cake.
provider add ollama \
  --name Ollama \
  --type ollama \
  --base-url "http://localhost:11434/v1/"
```

For llama.cpp (`llama-server`), point at the server's base URL:

```bash
provider add llamacpp \
  --name "llama.cpp" \
  --type llamacpp \
  --base-url "http://localhost:2222"
```

#### Manual Model Configuration

You can still list models explicitly. User-defined models always take
precedence over discovered ones, and any fields you set won't be overwritten
by auto-discovery. Auto discovery will run if the model list is empty for any
`openai-compat` provider or if you pass `"discover_models": true` it will merge
the found models with your hand configured ones.

```bash
# klashrc
provider add ollama \
  --name Ollama \
  --type ollama \
  --base-url "http://localhost:11434/v1/" \
  --discover-models true

model add ollama/qwen3:30b \
  --name "Qwen 3 30B" \
  --context-window 256000 \
  --default-max-tokens 20000
```

The `--discover-models true` flag merges discovered models with the one above;
your explicit model fields win on conflicts.

## Logging

Sometimes you need to look at logs. Luckily, Klash logs all sorts of
stuff. Logs are stored in `./.klash/logs/klash.log` relative to the project
(or `./.crush/logs/crush.log` in a project from before the rename).

The CLI also contains some helper commands to make perusing recent logs easier:

```bash
# Print the last 1000 lines
klash logs

# Print the last 500 lines
klash logs --tail 500

# Follow logs in real time
klash logs --follow
```

Want more logging? Run `klash` with the `--debug` flag, or enable it in your
`klashrc`:

```bash
# klashrc
option debug true
option debug-lsp true
```

## The provider catalog

Klash's list of providers and models comes from
[Catwalk](https://github.com/charmbracelet/catwalk), Charm's open source
catalog, and a copy is built into the binary. Upstream Crush fetches a fresh
one from Charm's servers on every start. Klash uses the built-in copy unless
you ask for the fetch:

```bash
# klashrc
option provider-auto-update true
```

Or set `KLASH_DISABLE_PROVIDER_AUTO_UPDATE=0`. To point the fetch at another
catalog, for testing or a fork of Catwalk, set `CATWALK_URL`
(`export CATWALK_URL=http://localhost:8000`).

To refresh the catalog once, by hand, use `klash update-providers`:

```bash
# Update providers remotely from Catwalk.
klash update-providers

# Update providers from a custom Catwalk base URL.
klash update-providers https://example.com/

# Update providers from a local file.
klash update-providers /path/to/local-providers.json

# Reset providers to the copy built into klash.
klash update-providers embedded

# For more info:
klash update-providers --help
```

## Metrics

Klash sends no usage metrics unless you allow them, and never to Charm. The
first time you run Klash on a machine, it asks once:

> Allow anonymous analytics? [y/N] (press Enter to decline)

Pressing Enter, or anything but yes, declines, and the answer is stored per
machine so you are not asked again. A run with no terminal (a pipe, `klash
run`) never asks and sends nothing. You can change the answer any time with
`option metrics true` or `false`, and `DO_NOT_TRACK=1` or
`KLASH_DISABLE_METRICS=1` force it off.

When allowed, Klash sends pseudonymous events keyed to a hash of your machine's
ID:

- when it starts and exits, and how long it ran;
- sessions created, switched and deleted;
- for each prompt and reply, the session ID, provider, model, reasoning effort,
  whether yolo mode is on, and the tokens used;
- errors, your OS and CPU architecture, terminal, shell and Klash's version.

Prompts, replies and code are never sent. The events go to Klash's own
collector, `https://klash.gno.run`, or to a `metrics-url` you set instead;
Charm's analytics is not a destination. The collector is a small open service
(a small Go service, klash-metrics); run your own and point `metrics-url` at it to keep the
data yourself. The events are defined in [internal/event](internal/event) and
[internal/agent/event.go](internal/agent/event.go).

## Q&A

### Why is clipboard copy and paste not working?

Installing an extra tool might be needed on Unix-like environments.

| Environment         | Tool                     |
| ------------------- | ------------------------ |
| Windows             | Native support           |
| macOS               | Native support           |
| Linux/BSD + Wayland | `wl-copy` and `wl-paste` |
| Linux/BSD + X11     | `xclip` or `xsel`        |

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md). A contribution is licensed to this
fork's maintainer and to Charm, so that work done here can go upstream to
Crush without coming back to you. You keep every right to your own work.

## License

[FSL-1.1-MIT](./LICENSE.md), the same licence as upstream Crush. Each version
becomes MIT two years after Charm released it.

Klash is a fork of [Crush](https://github.com/charmbracelet/crush) by
[Charm](https://charm.land). Crush and Charm are Charmbracelet's names; this
fork is not affiliated with or endorsed by them.
