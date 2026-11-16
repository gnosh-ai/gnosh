# bridge.md — mem and frames for another harness, over standard input and output

What this file is: `gnosh bridge`, the way a harness that is not gnosh (pi
first) gives its model gnosh's machines, mem and frames without carrying
any of their code. The harness starts `gnosh bridge` as a child process and
speaks JSON lines with it: it asks for gnosh's tools and runs their calls
through it, and before every request to its model it hands over the
conversation and gets back what gnosh would send. The frame rule stays in
one place, `internal/transcript`, as `frames.md` gives it; the other
harness maps its messages to gnosh's entries and back, and nothing more.

Priority: P2 (after everything Jae relies on). The parts marked
**later** are not in the first version.

---

## 1. Why a bridge, and not a port or an MCP server

- **A port** of `frames.md` into another language is a second
  implementation of a rule that is still changing, with its wording to
  the model (`internal/prompt`) copied beside it. Every change to either
  would have to be made twice.
- **An MCP server** alone would give a host mem's calls and none of the
  cut. mem's own receipts tell the model that "everything from here until
  the pop is removed from your context"; on a host that never cuts, that
  is false, and the model is told so on every push. The bridge serves the
  tools and the view together, and §6.4 says what a host that never asks
  for the view is told.
- **The bridge** runs gnosh's own code for both: the tools are
  `internal/gnovm/tools`, the machines run in gnosh's GnoVM worker as in a
  session, and the view is `transcript.View`. The host keeps its own
  conversation, its own permissions and its own model.

## 2. Starting it

    gnosh bridge [--cwd <folder>]

- The project is the folder's project, as for `gnosh mem`: the repository
  root that holds the folder, or the folder. mem and the machines are the
  project's own, shared with every gnosh session on it (`peers.md`).
- It reads requests on standard input and writes replies on standard
  output, and nothing else goes to standard output. What it has to say for
  a person (a worker that died, a problem with the data folder) goes to
  standard error, one line each.
- It needs no model, no provider and no config: like every gnosh
  subcommand it works with the model server off (`app.Command`).
- It ends when standard input ends, or on SIGINT or SIGTERM, after the
  request it is running has answered. Ending it ends its worker.
- A folder that is not trusted may still use the bridge: it reads no
  settings of the project's own (SAFE-7).

## 3. Framing

- One request a line on standard input, one reply a line on standard
  output: a JSON object in UTF-8, ended by `\n`, with no newline inside
  it (JSON escapes them). A line may be up to 64 MiB.
- Requests are answered one at a time, in the order they came. A host may
  write the next request before the last reply came; it is read once the
  one before has answered.
- Every request has `op`, and may have `id`, any JSON string or number the
  host chooses; the reply carries the same `id` back. A reply always has
  `ok`.
- Field names are lower case with underscores. A field the bridge does not
  know is ignored, so a newer host works with an older bridge as far as
  their protocol versions agree (§4). A field the bridge leaves out of a
  reply means its zero value.

## 4. `hello`: the handshake

The first request must be `hello`. Any other first request is answered
with the error `protocol` and the bridge ends.

    → {"op":"hello","id":1,"protocol":1,"client":"pi-gnosh 0.1.0"}
    ← {"ok":true,"id":1,"protocol":1,"build":"<build id>","version":"<gnosh --version line>","project":"/abs/project","ops":["hello","tools","call","view"]}

- `protocol` is the version of this file the host speaks. The bridge
  answers with the version it will speak: the host's, if it can, and
  otherwise the error `protocol` with the versions it can speak in
  `error.protocols`, and then it ends. This file is version **1**.
- `build` is the build id the GnoVM worker's handshake compares
  (`buildinfo.BuildID`): two bridges with the same build id are the same
  program. A host may keep it to say which gnosh it ran against; it is not
  compared. The worker is the bridge's own child, so the host never meets
  the worker's handshake.
- `ops` lists the requests this bridge answers, so a host can tell which
  of the **later** ones it has.
- `client` is only for the bridge's messages on standard error.

## 5. `tools`: what the model may call

    → {"op":"tools"}
    ← {"ok":true,"tools":[{"name":"gnovm_run","description":"…","input_schema":{…},"confirm":false}, …]}

- The tools are gnosh's own machine tools (`gnovm_run`, `gnovm_new`,
  `gnovm_list`, `gnovm_drop`), with the descriptions and input schemas a
  gnosh session gives its model, unchanged. The host gives them to its
  model under these names.
- The list and its order are the same for the life of one bridge. The
  host may ask again; it gets the same answer.
- `confirm` is true for a tool some of whose calls a person must approve
  first (§6.2).
- The wording of the tools, and of the lines a gnosh session puts on its
  prompt about mem (**later**: `prompt_lines`), is gnosh's; a host passes
  it on as it is.

## 6. `call`: run one tool call

    → {"op":"call","session":"S","call_id":"toolu_1","name":"gnovm_run","input":{"name":"mem","code":"mem.Todo(\"fix the port\").Push()"}}
    ← {"ok":true,"content":[{"type":"text","text":"added `fix-port` …"}],"is_error":false,"frames":[{"kind":"push","id":"fix-port","purpose":"fix the port","session":"S"}]}

### 6.1 The fields

- `session` is the host's id for its session: what mem knows the caller
  by, what its scratch machine is named after, and whose stack the call is
  made from. It must not be empty. The same id must be used in `view`.
- `call_id` is the id of the model's tool call. A frame entry the host
  writes names it (§7.2).
- `input` is the call's arguments, one JSON object, as the model gave
  them.
- `content` is what the model is to be shown: gnosh's content blocks
  (`internal/core`: text and image blocks). `is_error` marks a call that
  failed in a way the model should see and act on: a compile error, a
  refused pop, gas spent. Those are `ok: true`; the call ran.
- `frames` are the frames the call pushed and popped, in order, as mem
  kept them (`gnovm.MemFrame`: `kind` `push` or `pop`, `id`, `purpose`,
  `result`, `thoughts`, `session`). A host keeps them with the call's
  result and turns them into frame entries (§7.2). Only frames of this
  session count; a host leaves out any whose `session` is another's.

### 6.2 Calls a person must approve

A call whose description says a person must be asked first
(`core.Action.Confirm`; today `gnovm_drop` with `action: delete`) is not
run unless the request says `"confirmed":true`. Without it the reply is the
error `confirm`, with `error.summary`, the one line that says what the
call would do. The host asks its person and sends the call again with
`"confirmed":true`, or tells its model the call was refused. Every other
call is the host's to allow or refuse by its own rules before it sends it;
the machine tools touch nothing outside gnosh's own data (`app.ToolName`
Quiet).

### 6.3 The stack a call is made from

mem needs the session's stack before each call to `mem` (`MemStack`,
`machines.md`). The bridge keeps it for each session: it is the stack of
the entries of the session's last `view` (`transcript.Stack`), with the
frames of the calls the bridge ran for that session since then applied in
order. A session the bridge has seen no view of starts at the root.

In version 1 the bridge does not apply mem's refusal to pop a fresh frame
(`MemFresh`), which needs the calling reply's place in the conversation:
the reply is not in the last view when its calls run. **Later**: `call`
may carry the session's entries through the calling reply, and the bridge
then decides freshness as a session does.

### 6.4 A host that never asks for the view

The bridge cannot know whether a host cuts. A host that does not use
`view` must not give its model the push and pop calls of mem, since mem's
receipts would tell the model something untrue (§1). In version 1 the
bridge serves them to every host, and keeping this rule is the host's.

## 7. `view`: what the model is sent

    → {"op":"view","session":"S","entries":[…],"budget":0}
    ← {"ok":true,"messages":[{"role":"user","content":[…]}, …],"sources":[["e0"],["e1"],…],"stack":[…],"tokens":1234,"over_budget":false,"new_slides":[]}

### 7.1 The request

- `entries` is the session's conversation as gnosh's own entries
  (`transcript.Entry`, the lines of a session file, `frames.md` §2), in
  order, oldest first. Every entry has a `uuid` unique in the list; the
  entries need no `parentUuid` and no `timestamp`. `sessionId` on a frame
  entry is the `session` of the calls that made it, and on every other
  entry it may be left out.
- `budget` is the most tokens the view should hold, by gnosh's estimate;
  0 means no limit, so the view never slides. `room` and `keep_rounds`
  are `ViewOptions.Room` and `KeepRounds`. `prompt` is the uuid of the
  prompt being answered: a slide never cuts it.
- The view is computed from the entries alone. The bridge keeps nothing
  of them but the stack (§6.3).

### 7.2 Mapping a host's conversation to entries

The host owns the mapping; these are the rules gnosh's view relies on.

| The host's | Entry |
| :- | :- |
| a prompt a person typed | `user`, `source: prompt`, its text |
| a model reply | `assistant`, its text, reasoning (`thinking`) and tool calls (`tool_use`, with the host's call ids), `stop` (`tool_use` when it made calls, else `end_turn`) |
| the results of one reply's calls, together | one `user` entry, `source: tool_results`, one `tool_result` block per call, in the order the calls were made |
| the frames of a call (§6.1) | one `frame` entry each, right after the entry holding the call's result: `op` push with `frame` and `label` (the purpose), or pop with `frame`, `label` and `result`; `toolUseId` the `call_id`; `sessionId` the session |
| a summary that stands in for what came before | `summary`, with `covers` the uuid of the last entry it stands in for |
| a note the harness itself adds | `user`, `source: notice` |

A slide is an entry the bridge plans (§7.3); the host writes the ones it
is handed and sends them back in later views.

### 7.3 The reply

- `messages` is the conversation the model is to be sent, without the
  system prompt or the tools, which stay the host's.
- `sources[i]` is the list of uuids of the entries `messages[i]` came
  from (`ViewInfo.Sources`). A message the view made up itself has none.
  A host that keeps its own message objects maps back through it: it
  sends its own messages for those uuids, in the order given, so that
  what only it knows of them (a provider's signatures, its metadata)
  stays as it was. A `tool_result` block of the view whose text goes on
  past the host's own result holds a note the view added (`frames.md`
  §4.4 and §8.6); the host adds that text to the result it sends.
- `stack` is the frames the session stands in, bottom to top: `id`,
  `label`, `call_id` (the call that pushed it) and `slid` (its start has
  left the view to make room), from `transcript.Frame`.
- `tokens` and `over_budget` are `ViewInfo.Tokens` and `OverBudget`.
- `new_slides` are the slide entries this view planned because it was
  over `budget`; the view has them applied. The host must keep them
  with the conversation and send them in every later view, at the end of
  the entries as they stood, so that every later view cuts the same.

### 7.4 The prompt cache

The view keeps what `frames.md` §12.2 promises: after a pop, the request
shares its prefix with the requests before it through the push's call,
and a request that follows another with nothing popped or slid between
them extends it. A host keeps that promise only if it sends its own
messages unchanged where the view kept them, and puts nothing of its own
in front of them that changes between requests.

## 8. Errors

A request that could not be carried out is answered with

    ← {"ok":false,"id":…,"error":{"code":"…","message":"…"}}

and the bridge goes on with the next request, except where it says it ends.

| `code` | When |
| :- | :- |
| `bad_request` | the line is not a JSON object, or a field the op needs is missing or of the wrong kind; `message` names it |
| `protocol` | the first request was not `hello`, or no common version (`error.protocols`); the bridge ends |
| `unknown_op` | an `op` this bridge does not answer |
| `unknown_tool` | a `call` of a tool not in `tools` |
| `confirm` | a call a person must approve, sent without `confirmed` (§6.2) |
| `unavailable` | reserved: the bridge itself cannot serve calls. Machines that cannot be started are, as in a session, a result the model sees (`is_error`), and the next call tries again. |
| `internal` | anything else; `message` says what was being done |

A failure the model should see is not an error: it is `ok: true` with
`is_error: true` (§6.1).

## 9. What stays stable

- Within a protocol version: the ops, their fields and meaning, and the
  error codes. A new optional field or a new op (listed in `ops`) does not
  change the version; a change to the meaning of anything that exists does.
- The entries are gnosh's session file format, which is versioned on its
  own (`v`) and kept readable across gnosh versions (`transcript`).
- Not stable: the wording of the tools, the receipts and the notes (it is
  gnosh's to improve), and the shape of a view beyond what `frames.md`
  promises. A host must not parse them.
