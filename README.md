# gnosh

An AI harness that thinks in Gno, built on [gno.land](https://gno.land).

gnosh sits between a person, a language model and the machine. The model
reads, edits and runs things under the person's rules, and it keeps its plan
in `mem`, in Gno, on disk. Each step of a task is a frame: when the step is
done and popped, its work leaves what the model is sent and its result stays,
the way a function's locals go when it returns. A new session starts from
what is still open.

This is gnosh written from scratch in Go. It is not a fork of anything.

## Where it stands

It runs. Built from this tree, it has answered prompts, used tools, asked
permission, planned and finished a task in `mem`, and popped frames against
a real model (Qwen 3.8 27B on llama.cpp). `tools/dev/remote-ci.sh` builds,
vets and tests every package from a clean copy.

Built:

- **Print mode**, `gnosh -p`: text, JSON and streamed JSON output, streamed
  JSON input for a conversation over standard input, `--json-schema` for a
  result as JSON that conforms to a schema, `--bare` for minimal mode,
  `--no-session-persistence` to keep nothing, `--permission-prompts none`
  for a run nobody is there to answer, `--agents` for kinds of sub-agent of
  the run's own, and `--agent` to run the session as one. It waits for the
  background commands its turns start and lets the model answer how they
  ended.
- **A session** in the terminal's own scrollback. Opened again (`-r`,
  `-c`, `/resume`), it shows where it left off: its last prompt and the
  model's last answer.
- **Runs in the background**, as Claude Code's background sessions: a
  session goes on without the terminal (`/background`, `/fork`), or
  starts that way from a shell (`gnosh --bg "fix the flaky test"`, as
  `claude --bg`; `gnosh --bg -r <session> "go on"` sends one back to work).
  `/tasks` lists the folder's runs; `gnosh agents` lists those of every
  folder (`--json`, `--all`, `--cwd`), `gnosh logs <session>` shows (or
  follows) what one printed, `gnosh stop <session>` stops one, and `gnosh
  session list` marks the sessions open now.
- **The agent view**, as Claude Code's: `gnosh agents` on a terminal,
  `gnosh attach <session>` on one run, or ← twice in a session's empty
  prompt box, over the session (Esc comes back; the status line counts
  the runs that wait on you with a ←, or those done since you last
  looked). In it:
  - Space peeks at a run, Enter watches a working one's conversation or
    opens one that is over, Ctrl+X stops one, and Ctrl+S groups them by
    folder.
  - A run that waits on a permission question is under Needs input, and
    a number answers it, in the peek or while watching it; the view tells
    you by your preferredNotifChannel, and in the title.
  - Words typed while peeking or watching reach a working run between its
    steps, or send one that is over back to work on them.
  - A prompt typed at the bottom starts a run; `/model` and `/effort` set
    what those runs get, and `@name` first starts one as that kind of
    agent, or in that folder (a git repo below the view's, or one runs
    work in).
  - A run that ended with an error, or was killed, is marked failed (✗)
    and says how it ended.
- **Tools:** the file and shell tools, WebFetch (a page read by the small
  model with the model's request, from the open web only), WebSearch on a
  SearXNG server of your own (`"options": {"webSearchUrl": "http://..."}`,
  with SearXNG's JSON format on; no tool without it), commands on
  declared hosts over ssh, and MCP servers, those gnosh starts itself and
  those at a URL (from `~/.config/gnosh/mcp.json`, a trusted project's
  `.mcp.json`, and `--mcp-config` for one run, in Claude Code's shape; their
  tools are `mcp__<server>__<tool>`, and a server may ask you for a few
  values in the middle of a call, Claude Code's elicitation).
- **Permissions:** modes, plan mode included (the model presents its plan
  with ExitPlanMode and you approve it), and rules. The model's own
  questions (AskUserQuestion) can close by themselves after a while
  without a key, so that a session you left goes on
  (`"options": {"askUserQuestionTimeout": "5m"}`, or 60s or 10m).
- **Hooks:** command hooks in the shell and exec forms, in the background
  too, http hooks, mcp_tool hooks, prompt hooks (a rule or a condition
  the model judges, `{"ok": false, "reason": ...}` blocking as a command
  hook's answer would) and agent hooks (the same question put to a
  sub-agent that may read, search and run commands first), with Claude
  Code's `if` filters (one
  permission rule each) and their status messages, on 25 of Claude Code's
  events: the tool and prompt events, Stop and SubagentStop, the session
  and summary events, PreModelSwitch and PostModelSwitch, ConfigChange,
  PostToolBatch, CwdChanged, DirectoryAdded, FileChanged, Elicitation and
  ElicitationResult among them. Not yet: WorktreeCreate, WorktreeRemove
  and MessageDisplay; Setup, PermissionDenied, TaskCreated, TaskCompleted
  and TeammateIdle belong to things gnosh does not have.
- **What the model is given:** instruction files, skills (with
  allowed-tools and disallowed-tools, effort, `context: fork` in a
  sub-agent, paths, hooks of their own, and commands run before the model
  sees them; up to six typed at the start of one line, `/write-tests
  /fix-issue 123`, go together with the same arguments, as Claude Code's
  prompt has it), and a status line command (Claude Code's `statusLine`).
- **Sub-agents** in the foreground and, in a session, in the background:
  general-purpose, Explore, Plan and your own, given more to do once they
  end with SendMessage. Your own may name MCP servers (`mcpServers`): the
  session's, or servers of their own, connected only while they run.
- **`mem`** and frames, with the close check, `/goal`, the slide, peers'
  turn locks, and `gnosh mem`.
- **Commands:** `gnosh doctor`, `gnosh trust`, `gnosh mcp` (add, add-json,
  get, list and remove, with Claude Code's local, project and user scopes),
  `gnosh ssh` (ssh that puts what the far side copies on this machine's
  clipboard: see below), and `--worktree` (a session in a git work tree of
  its own, as Claude Code's).
- **Work trees that keep apart**, as Claude Code's: a sub-agent given
  `isolation: worktree`, or a `--worktree` session, works in a git work
  tree of its own. Its edits and commands are refused in your checkout,
  and its git may not change what the two share: your branches, tags and
  git settings. A new work tree gets copies of the ignored files your
  `.worktreeinclude` names (a `.env`, say). Asked to work in a worktree,
  the model moves a running session into one with `EnterWorktree`, and
  back with `ExitWorktree` (keep or remove).
- **Scheduled prompts**, as Claude Code's: `/loop 5m <prompt>` sends a
  prompt every five minutes between turns, and the model schedules,
  lists and cancels prompts with `CronCreate`, `CronList` and
  `CronDelete` ("remind me at 3pm", "check the build every 20 minutes").
  `/loop <prompt>` with no interval lets the model choose when to come
  back (`ScheduleWakeup`); tasks come back with `-c` and `-r`.
- **Channels**, as Claude Code's: `--channels server:<name>` lets an MCP
  server of yours push events into the running session (a CI webhook, a
  chat bridge), which the model reads and acts on as they come; one that
  relays permission questions lets you answer them from another device
  (`yes abcde`), while the question stays open at the terminal too.
- **PushNotification**, as Claude Code's: the model calls you back when
  you have walked away (a desktop note through your terminal, and your
  Notification hooks with the type `push_notification`).
- **Deliverables:** the `Deliver` tool, by which the model names what was
  asked for, `/deliverables`, and each delivered file served at an address
  on this machine.
- **`/review` (also `/code-review`), `/security-review` and `/simplify`**,
  as Claude Code's: the model reviews the changes since the branch left the
  default branch, committed or not, for bugs or for ways in, and changes no
  file (`/review --fix` fixes the bugs it is sure of; `low` to `max` first
  says how thorough to be); `/simplify` makes those changes simpler (reuse
  of what is there, fewer parts, less work, the right place), the code
  doing what it did. All run in `gnosh -p` too; in a Gno project the
  security review holds Gno code against the Gno guides.
- **gnokey, with a password the model never sees:** in a Gno project,
  where gno.land's `gnokey` is installed, the model runs it as a tool (queries, unsigned
  transactions, broadcasts, `maketx` and `sign`), and before anything is
  signed gnosh asks you for the key's password at a masked prompt that
  names the key and shows the exact command, saying so when the chain is
  gno.land's main network. The password goes to gnokey alone, never to the
  model, the session's file or its log; making, changing or handing out
  keys stays in your own terminal, and the shell refuses gnokey's password
  flag, so nothing else can sign.
- **gnosh's own words to the model, yours to read and change:** `gnosh
  prompt show` lists every text gnosh sends (where it goes and about how
  many tokens), `gnosh prompt show <name>` prints one as it is sent, and
  `gnosh prompt export <name>` writes it into `~/.config/gnosh/prompts/`
  (`gnosh prompt path`), where your edited copy stands in for it in every
  new session. What fills the window, tool by tool, is `/context all`.
- **Output styles**, as Claude Code's: `/output-style Concise` (or
  Proactive, Explanatory, Learning) changes how the model replies from the
  next message on, kept in the project's local settings. A style of your
  own is a Markdown file in `~/.config/gnosh/output-styles/` (or a trusted
  project's `.gnosh/output-styles/`) with `name`, `description` and
  `keep-coding-instructions` in its frontmatter.
- **Vim's keys** in the box, as Claude Code's editor mode: `"options":
  {"editorMode": "vim"}` in your gnosh.json, or `/config editorMode=vim`
  at once. NORMAL, INSERT and VISUAL modes, motions, operators with counts,
  text objects, `.` and undo by changes; the mode is on the status line,
  and `"vimInsertModeRemaps": ["jj"]` makes a pair stand for Esc.
- **Being told it waits**, as Claude Code's `preferredNotifChannel`: a
  desktop note in a terminal known to take one, or, with `"options":
  {"preferredNotifChannel": "terminal_bell"}` (or `/config
  preferredNotifChannel=terminal_bell`), the terminal's bell, which reaches
  a terminal on the other side of ssh or tmux too. Your prompts are white
  and the model's answers green; on a terminal that says its background is
  light, both take colours that read there.
- **What changed**, as Claude Code's `/release-notes`: a build made for
  installing carries the subjects of the commits it was made from;
  `/release-notes` lists them, and after `/restart` into a newer build
  the session says what changed since the one it replaced.
- **Themes**, as Claude Code's `/theme`: `auto` (for the background the
  terminal says it has), `dark`, `light`, or `ansi` (the terminal's own
  sixteen colours). Bare `/theme` opens a picker; `/theme light` or
  `/config theme=light` sets it, at once and for every project.
- **A budget for thinking**: a model on a llama.cpp server thinks at most
  4,096 tokens before a reply, and is then told to go on with what it has
  (the server's reasoning_budget_tokens). `"models": {"<provider>/<id>":
  {"thinkingBudget": N}}` sets a model's own (0 for no cap), and Claude
  Code's `MAX_THINKING_TOKENS` sets it for every model (0 turns thinking
  off).
- **Fallback models**, as Claude Code's `fallbackModel`: `"fallbackModel":
  ["box2/qwen3.8-27b"]` (up to three; `--fallback-model` for one run).
  When the model's server cannot be reached, is not ready or too busy,
  does not have the model, or keeps failing until the wait for it runs
  out, the turn goes on with the first fallback that answers, at once and
  with a line that says so; the next turn asks the model first again. A
  limit on your requests is waited out as always. A fallback that cannot
  hold the conversation as it stands, or that is on a model server of
  your own that failed in the turn, is passed over; when every fallback
  fails too, the turn waits for the model as it would without them.
- **No invisible characters in a prompt**, as in Claude Code: Enter takes
  out what a terminal draws as nothing (tag characters, the controls of
  the drawing order, zero-width spaces), keeping what Persian, the
  scripts of India and emoji need. When they could have hidden text,
  nothing is sent: the cleaned prompt is back in the box to read.
- **Your likely next prompt**, as Claude Code's prompt suggestions: with
  `"options": {"promptSuggestionEnabled": true}` (or `/config
  promptSuggestionEnabled=true`), after a turn the model is asked, beside
  the session, what you would most likely send next, and the empty box
  shows it; Tab or the right arrow takes it, typing drops it. Off unless
  set, as Claude Code has it for other providers' models: each one is a
  short request to the model.

In a session, Claude Code's commands work as there: `/help`, `/clear`,
`/restart` (gnosh starts again in the same session, the newer one once a new
build is installed, which the session says), `/background` (the session goes
on without the terminal, as a run of its own on the prompt given), `/fork` (a
copy of the conversation works on the prompt in the background while this
session goes on), `/subtask` (as `/fork`, and what the copy answers comes
back to this conversation when it is done), `/compact`, `/resume` (by number, id, name, or words from
a session's name or first prompt), `/branch`, `/rewind` (the
conversation, the files Write and Edit changed, or both; what a file holds
is kept before it is put back, and the copies of your 100 most recent
prompts are kept), `/model`, `/effort`,
`/config`, `/plan`, `/permissions`, `/add-dir`, `/cd` (the session moves to
another folder and goes on there, with that folder's settings, instructions
and skills; Cd rules say where it may go), `/status`, `/context`, `/btw`
(a side question, also while the model works; nothing of it is kept),
`/recap`, `/color` (this session's prompt mark in a colour of its own, kept
with the session), `/keybindings` (Claude Code's keybindings.json: a key bound
to an action, null to take one away, command:<name> to run one), `/cost`,
`/tasks`, `/diff`, `/export`, `/copy`, `/init`, `/memory`, `/agents`, `/mcp`
(with reconnect, enable and disable), `/skills`, `/reload-skills`, `/hooks`,
`/statusline`, `/debug` (debug logging on, and the model finds out from
gnosh's log what went wrong) and `/doctor`, with gnosh's own `/mem`, `/goal`,
`/loop` and `/deliverables` beside them; a `/` partway through a prompt
offers the commands and skills whose names begin with what follows it, as
Claude Code's does (`run the tests, then /rev`). Shift+Tab goes through the permission
modes, Esc stops a turn and Esc twice goes back to an earlier prompt, Ctrl+O
shows the whole transcript (every thought, call and result) on the whole
screen, Ctrl+B moves a running command to the background, `@path` in a prompt
brings that file (or a directory's listing) along with it (`@focus` offers
the project's files whose paths hold the word, as Claude Code's `@` does),
and a line that
begins with `!` is a command of your own: the model answers what it printed
(with `"options": {"respondToBashCommands": false}` it waits for your next
prompt instead), Up brings it back in the folder's later sessions, and Tab
offers the commands you ran before that begin with what you typed, the
programs on the PATH where a command goes (also after `&&`, `;` or `|`), and
files anywhere else. With nothing after them, `/model`,
`/effort`, `/config`, `/export`, `/memory`, `/mcp`, `/permissions` and
`/skills` open pickers, `/hooks` a browser of the hooks in force and
`/agents` one of the kinds of sub-agent, as Claude Code's do (`/permissions allow`, `ask`,
`deny` or `remove` and a rule changes the rules at once, and `/skills <name>
off` and the like set Claude Code's `skillOverrides`); Alt+T turns thinking on
or off, Alt+P switches the model, and Ctrl+X Ctrl+K twice stops the sub-agents
in the background. `/panel` (or the `sidePanel` option) draws the session on
the whole screen beside a side panel of the files it changed and its
deliverables, with their addresses; `/panel` again gives the screen back, with
all that was said in the scrollback. `/focus` (or the `viewMode` option,
`"focus"`) folds each run of tool calls into one line that sums it up (files
read, files edited with the lines added and taken out, commands run, how many
failed), as Claude Code's focus view does; `/focus` again turns it off.

An MCP server at a URL that wants a sign-in is signed in to by OAuth, as
Claude Code does it: `/mcp login <name>` gives the address to sign in at,
and over ssh the address the browser tried to come back to is pasted after
it (`oauth.clientId`, `callbackPort`, `scopes` and `authServerMetadataUrl`
in the server's entry, as Claude Code's). WebSearch asks a SearXNG server
you name (the `webSearchUrl` option); without one there is no WebSearch
tool, and a search engine's MCP server can give the model one. The
interface is in use over ssh, in tmux. Its wording to the model has not yet been compared run
against run. Nothing here is released.

## Licence

The GNO Network General Public License, version 6, with Additional Terms
(Strong Attribution): see [LICENSE.md](LICENSE.md). The GnoVM subsystem has
its own copy in `internal/gnovm/LICENSE.md`. A user interface built on gnosh
shows "built on gnosh" with a link to <https://gnosh.ai>. gnosh includes
gno.land's GnoVM, whose licence asks the same with "built on gno.land" and
<https://gno.land>: every session's header shows both, and `/license` the
legal notices. The libraries gnosh is built with keep their own licences
(BSD, MIT, ISC, Apache-2.0, MPL-2.0), and none conflicts with gnosh's.

## Build

    go build -o gnosh .

Go 1.27 or newer. The harness uses the standard library and the Go team's
`x/term` and `x/sys`; the GnoVM part uses gno and a pure-Go SQLite.

## A model

In `~/.config/gnosh/gnosh.json`, for a llama.cpp server on this machine:

    {
      // a model server on this machine
      "providers": {
        "local": { "type": "llamacpp", "baseUrl": "http://127.0.0.1:8080" }
      }
    }

The file is JSON in which comments and a comma after the last item are
allowed. It holds `permissions`, `hooks`, `env` and `model` with Claude
Code's names and meaning, and gnosh's own `providers`, `models`,
`smallModel`, `hosts` and `options` beside them; a project may keep a
`gnosh.json` of its own, which counts once the folder is trusted. The
package documentation of `internal/config` lists every key.

Then `gnosh doctor` says whether a turn can run, and `gnosh` starts a
session. Nothing leaves the machine except what is sent to the model server
you name.

## Copying over ssh

A program on the far side of ssh copies to your clipboard through the
terminal (OSC 52): gnosh's `/copy` does, and so does tmux's copy mode.
Apple's Terminal drops such copies. `gnosh ssh` runs ssh and puts them on
this machine's clipboard itself. In zsh, this sends every interactive ssh
typed in Apple's Terminal through it, and leaves scripts' ssh alone:

```sh
if [[ $TERM_PROGRAM == Apple_Terminal ]]; then
  ssh() {
    if [[ -t 0 && -t 1 ]] && (( $+commands[gnosh] )); then
      gnosh ssh "$@"
    else
      command ssh "$@"
    fi
  }
fi
```

ssh keeps your terminal: it reads what you type, asks for passwords, and
stops with Ctrl+Z or `~^Z` as it does alone, and its exit status is
gnosh's. Only what it writes passes through gnosh. The clipboard is only
ever written, never read, but anything shown on the far side can write it,
as in terminals that take OSC 52 themselves.

## Reading on

- [ARCHITECTURE.md](ARCHITECTURE.md): how it is put together.
- [PROVENANCE.md](PROVENANCE.md): how it was written and what it is built from.
- [CARRIED.md](CARRIED.md): the files brought over from the earlier gnosh.
- [AUTHORSHIP.md](AUTHORSHIP.md): who asked for what.
- [GNOMEM.md](GNOMEM.md): `mem`, from the model's side.
- `spec/`: what it does, in words.
