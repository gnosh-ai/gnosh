# GnoVM machines in gnosh — design and plan (v4)

Status: revision 4 (2026-10-29), after review rounds 1–3. Phases 1 (the core
host) and 2 (the worker, its protocol and the client), and the four `gnovm_*`
tools of phase 3, are implemented and tested, and the code follows this text;
Jae had it published on 29 October 2026. GNOMEM, the model's board in a `mem`
VM (§19, Appendix E), was added on 30 October. Goals (the rest of phase 3)
are next.
Licensed with the rest of `internal/gnovm/` under the GNO Network GPL v6 (or a
later version the Licensor accepts as proxy), with the Additional Terms in
[LICENSE.md](LICENSE.md). Appendices A–C record every review finding and what
was done with it. In prose, "VM" means a Gno virtual machine; "machine"
already means an ssh host in gnosh.

## 1. What Jae asked for

Gnosh should (1) know how much memory is available for Gno VMs; (2) create
VMs with limits; (3) declare state and evaluate expressions in them; (4)
optionally persist them; (5) name each VM, with a persistence option; (6) list
them; (7) evict VMs to make room for large work; (8) understand gas, 1 gas ≈
1 ns, configurable per VM; (9) use this as the harness's way to keep track of
long-standing goals and success conditions; (10) be planned, reviewed until
convergence, and tested on long goals. The code is published under the GNO
Network GPL with a standing temporary strong attribution clause in Jae's
favour (§14).

## 2. Evidence (devbox, gno pinned at 5cdbc25fcde0)

| Measurement | Result |
|---|---|
| Empty VM | 43 KB, 0.1–2.3 ms |
| Gas vs wall | 0.35–1.3 ns/gas for executed code; **15–41 ns/gas for 120–250 KB declaration inputs** (preprocessing is mostly unmetered) |
| 1 MB cap, 20 MB garbage / 8 MB live | GC collects / `allocation limit exceeded` |
| Per-call gas limit; 200 ms deadline | both stop a `for {}` exactly |
| One store per VM vs shared base + overlay | ~88 MB, ~1 s per VM vs **345 KB, 8 ms** |
| All 50 stdlibs into a base | 1.8 s, +149 MB; decoded copies per VM ≤ 0.2 MB per package |
| 800 VMs created and dropped; `noEvictStore` at 315 GCs | heap flat |
| `x := 1` then `x` | persists; gno's `Eval` wrapper shadows `x`, so gnosh wraps itself |
| Declarations in one file: `main` first, mutual recursion, method before type, `const (iota)` | all work through `RunFiles` |
| An import inside a file | **visible only in that file**: imports are bound package-wide instead (§6) |
| A file's `var y = x + 1` after a top-level `x := 1` | **gno panics** (`*AssignStmt is not Decl`); a `var` from `RunDeclaration` is refused too ("unsatisfied deps"); a `var` in a file works, so `x := …` of new names becomes `var x = …` (§6) |
| Redeclaring a `func` across files | silently keeps the first body; gnosh refuses redeclaration itself |
| Heap kept per declaration | **291–375× its source** (measured 291× in gnosh's test); statements keep nothing; ~1 KB of the VM's cap per declaration input |
| Replay cost | statements linear (50,000 in 0.72 s); **declarations quadratic** (0.13 ms per file at 300 files, 17 ms at 20,000; 20,000 inputs 151 s): bounded by limits (§7) |
| GC stack | 0.5–1.1 B per byte of cap (64 MB chain: +64 MB stack, 195 MB RSS) |
| A panicking statement | VM unusable (`m.Exception` set, stacks half-run) |

## 3. Architecture

```
gnosh process ── refcounted process-wide client (lazy spawn) ─────────┐
   │   holds .clients/<client>.lock for its life                      │ fds 3/4 (ExtraFiles):
   ▼                                                                  │ length-prefixed JSON
gnovm worker = the gnosh binary as `gnosh gnovm-worker` (child) ◄──────┘ + heartbeats
   └── gnovm.Host   one VM operation at a time; a reader goroutine takes cancels
       ├── base            shared gno store; stdlibs + klashlibs loaded on demand
       ├── vms             journal on disk (+ resident VM when admitted)
       ├── budget          host probe, accounting, LRU eviction
       └── per-VM locks    cross-process safety for persistent VMs
gnosh reads the goals snapshots directly for injection, checking them against the journal (§12)
```

- **Why a worker**: it isolates what the VM can do to a process — a Go
  *fatal* (deep GC recursion, a gno bug), heap the allocator does not count,
  and the OOM killer's choice of victim (the AI box lost llama-server twice in
  October). The worker sets `oom_score_adj=1000`, `debug.SetMemoryLimit` and an
  RSS watchdog. Every journal is on disk (§7), so a restarted worker loses
  nothing. IPC adds ~0.1 ms per call. The worker is the *same binary*, so this
  is fault isolation, not licence separation (§14).
- **Spawn safety** (the October fork bomb came from a test re-exec'ing its own
  binary; the self-improvement loop runs these tests on the box):
  - the spawner refuses under `testing.Testing()` (precedents:
    `config/load.go:1200`, `db/connect.go:38`) unless handed an explicit binary,
    and refuses that binary too if it is `os.Executable()` itself (same inode);
  - `gnovm-worker` is dispatched first in `main()`, before cobra/fang; it exits
    at once unless a handshake (magic + versions) arrives on fd 3 within 2 s;
  - App-level tests use `in_process`, and a test asserts that `go test` cannot
    spawn a worker.
- **Worker hygiene**: exec `/proc/self/exe` on Linux (survives a deploy that
  replaced the binary); the handshake compares protocol, gnosh, gno and stdlib
  versions; `Setpgid`, SIGINT/SIGHUP ignored; exits on EOF of fd 3; reaped with
  `cmd.Wait`; `GNOSH_PROFILE` dropped; **`cmd.Dir` is the gnovm data directory**
  (`godotenv/autoload` in `main.go` loads `.env` from the working directory
  before main dispatches, and the project's `.env` is agent-writable);
  **stdout to `/dev/null`, stderr to a bounded log** (`worker.log`, 1 MB,
  one old copy), so a Go fatal's dump never lands on the TUI. Windows has no
  `ExtraFiles`: there the Host runs in-process.
- **Liveness**: a reader goroutine acknowledges a cancel at once, and the meter
  stops the run within ~1 ms of gas. **A ticker sends a heartbeat with a
  progress counter every 500 ms for the whole life of every request**, metered
  or not. The client kills the worker only after 10 s without any frame, or 30
  s past a request's deadline. A request whose outcome is unknown after a kill
  is resolved after restart by its request id (`Host.Kept`): "kept" or "not
  kept".
- **One operation at a time** keeps the GnoVM single-threaded (its stores and
  shared stdlib nodes are not safe for concurrent use). One agent's tool calls
  are sequential, so only *different sessions* in one gnosh (server mode,
  channels) wait on each other. The wait is bounded by the running call's wall
  limit (default 5 s; up to 10 min only when a session asks for high gas) plus
  one replay slice (§7). In server mode, per-request wall is clamped to 60 s
  until the phase-6 worker pool. A queued call's own budget starts when it
  runs, and its result says how long it waited.
- **Lifecycle**: the client is refcounted per process (server mode has one App
  per workspace); each App acquires it lazily on first VM use and releases it in
  `Shutdown` right after `Messages.FlushAll`, cancelling only that App's
  requests. The worker is closed (cancel, 3 s, kill) at refcount zero; the
  client then deletes its memory-only VMs and drops its client lock.

## 4. Shared base store

- In the worker, lazily: `memdb` → `dbadapter` → `gno.NewStore(nil, kv, kv)`,
  `SetNativeResolver(stdlibs.NativeResolver)`, a package getter serving the
  embedded `stdlibs/` (synced from go.mod's gno by `go generate`; VERSION and
  byte-for-byte reader tests) and `klashlibs/`: `fmt` (gno's `ufmt` v0, renamed,
  with a dated modification notice; its `%v` and float limits are in the tool
  description) and `gnosh/goals/v1` (§12).
- **On demand**: the imports of an input — or of a whole journal before replay —
  are loaded into the base first (`base.ensure`; required: an overlay's cache
  remembers a miss). The heap delta of each load is measured (single-threaded,
  so clean) and charged (§8).
- **Refused imports**, with what to do instead: `os`, `net/http` and kin (no
  files, network or processes), `sync`, `context` (one goroutine),
  `encoding/json` (build strings; csv, hex, base64 exist), `slices`, `maps`,
  `reflect`, `testing`, and `chain/banker`, `chain/params`, `sys/params` (VMs
  run on no chain, so there are no coins or chain parameters).

## 5. A VM

- **Name** `^[a-z][a-z0-9_-]{0,31}$`; reserved: `goals`, `scratch-*`.
- **Options** (`machine.json`): `memory_mb` (default 16, 1–64; may only grow),
  `gas` per call (default 1e9, max 1e11), `ns_per_gas` (default 1, 0.1–100),
  `persist` (may only go false→true), `pinned`, `description`.
- **Resident form**: overlay `tx := base.BeginTransaction(kv, kv, nil, meter)`
  over `cache.New(baseKV)`, `tx.SetPackageGetter(nil)`; the machine sees
  `noEvictStore{tx}`; package `repl` registered before
  `NewMachineWithOptions{Store, Alloc: NewAllocator(cap), GasMeter: meter,
  Context, BoundedPanicRender: true}`. `repl` is a stdlib-shaped path, so gno
  skips its own duplicate check for it: gnosh checks names itself (§6). Never
  `m.Release()` a kept VM; a changed `memory_mb` rebuilds the VM (an
  allocator's cap is fixed).
- **Every entry** (live or replayed) runs between `meter.begin` and
  `meter.end`, with a **fresh** preprocess allocator capped at the VM's cap and
  charged to its meter (it never collects).
- **ExecContext** per entry: ChainID `gnosh`, Height = seq, Timestamp from the
  entry's recorded time (so `time.Now` replays identically); Banker and Params
  nil (their packages cannot be imported); a fresh `sdk.NewEventLogger()`.
- **Evaluation** uses gnosh's own wrapper, `func() any { return (<expr>) }()`; a
  call expression is evaluated as is.

## 6. Running code (`gnovm_run`)

1. **Choose the VM.** With no `name`: code with a `package` clause runs as a
   **program**, in a fresh VM thrown away after (as `go run` would: a program
   is a whole, and running it again after a fix must not meet its own earlier
   declarations); other code runs in the session's scratch VM. `reset: true`
   starts a memory-only VM over (refused for a persistent one).
2. **Check syntax** (pure, before any rebuild): input ≤ 256 KB; tokenise with
   `go/scanner`.
3. **Bring the VM up to date** (§7: lock, sync, admission, replay in slices).
4. **Normalise** at depth 0, with the VM's names in hand:
   - a leading `package` clause is dropped (positions kept);
   - imports are collected and become **import ops bound package-wide** with
     `RunDeclaration` (gno scopes a file's import to that file); the name is
     the alias or the package's own name; an import the VM has is skipped; a
     name bound to another path is refused; dot imports are refused; `_`
     imports only load;
   - consecutive declarations (`func`, `type`, `var`, `const`, and a method
     only when a name and `(` follow its receiver) become **one file**, so they
     may refer to each other in any order; consecutive statements become one
     statement run; separators are kept so line numbers match the input;
   - a top-level `a, b := …` whose names are all new (to the VM and to the
     input so far) becomes `var a, b = …`, a declaration (§2); one that reuses
     a name stays a statement, which gno runs as an assignment;
   - a `func main` is renamed `main_<seq>` (every identifier `main` not after a
     `.`) and called after everything else, so one VM can run many programs;
   - a trailing lone expression becomes an **eval**, whose value is shown.
5. **Load imports** into the base; **parse everything** with the meter armed
   (gno charges parsing per token); a parse error means nothing ran.
6. **Refuse redeclaration** before anything runs: each file's names, methods as
   `T.M`, imports, and names a `:=` declares earlier in the input, against the
   package's names and each other — "`helper` is already declared in this VM;
   assign with `=`, use a new name, or pass reset: true".
7. **Check limits** before running (§7).
8. **Run in order**: import ops with `RunDeclaration`, files with `RunFiles`
   (named `in<seq>_<k>.gno`: unique, and the same on replay), statements with
   `RunStatement`, the eval last.
9. **Journal the ops**, not the text: `{"import":{"name","path"}}`,
   `{"file":…,"line":n}`, `{"stmts":…,"line":n}`, `{"call":"main_7"}`,
   `{"eval":…}`. An eval is left out only if it is **pure** — it calls nothing
   but `len`, `cap`, `min`, `max`, `real`, `imag`, `complex`, `new`, `make` and
   conversions to builtin types, receives from no channel, holds no function
   literal — and the VM redeclares none of those names (`delete`, `append`,
   `copy`, `clear` and `close` change state).
10. **Render** by structure only, never running user methods: strings quoted,
    one pointer followed (`&P{…}`), a struct's type named, `BoundedSprintTV`
    otherwise (16 KB); printed output keeps 64 KB.
11. **Outcome**: success = no meter trip (trips are sticky), then append (§7).
    A failure after code ran journals nothing and the VM is rebuilt on next
    use, except that an input that is only an expression and fails to compile
    ran nothing (gno preprocesses an expression before pushing anything), so
    a typo costs no rebuild.
12. **Errors** say that nothing from the call was kept and name the next step,
    with the exact call where there is one ("memory cap exceeded (16 MB): raise
    it with `gnovm_new{name: "scratch-1a2b3c4d", memory_mb: 32}`"); `panic:` +
    the bounded gno stack; out of gas after N (≈T at X ns/gas, with the
    measured wall time); wall or cancel; refused packages with what to use.

## 7. Persistence and durability

- **Layout**, all under `<XDG data>/gnosh/gnovm/` (a cloned repo cannot ship
  VMs or goals; the agent-writable project `.gnosh` stays out):
  - `.clients/<client>.lock`: held by each gnosh process for its life; client
    = `<pid>-<8 random hex>`;
  - `<project-key>/vms/<name>/`: a persistent VM's `machine.json`,
    `journal.jsonl` (and for `goals`, `summary.json` and `state.json`);
  - `<project-key>/.ephemeral/<client>/<name>/`: a memory-only VM's files,
    keyed by the gnosh process so they survive a worker restart; deleted when
    the client closes, or swept by any host whose client lock is found free or
    missing;
  - `<project-key>/locks/<name>.lock`: persistent VMs only, outside the VM's
    directory and never unlinked, so a path always names the same inode (the
    lock API gives no descriptor, so no inode check is needed or possible);
    the holder writes its pid into it, for "busy — in use by gnosh process N";
  - `<project-key>/.trash/`: deleted persistent VMs.
  The project key is the sanitised path plus 8 hex of its hash.
- **Creation** builds the VM in a temporary directory and renames it into
  place: no half VM after a crash, and two processes making the same
  persistent VM cannot both succeed.
- **Journal**: a header line (format, generation, created, gno version), then
  one line per kept call (seq, request id, time, `wall_ns`, gas, ops). The
  generation is new on create, repair and compaction.
- **A call on a VM**:
  1. lock: `TryFile` with 50 ms backoff, up to the call's wall budget, never
     blocking the worker;
  2. sync: re-read `machine.json` (deleted → refused; cap or creation time
     changed → rebuild); check the journal's generation and last line against
     what this host saw (rewritten → rebuild; appended → catch up); cut a torn
     tail left by a crashed writer;
  3. resident: admit (§8) and replay in slices (below);
  4. normalise, limits, run (§6);
  5. append: write at the known end, `fsync`; on a write error truncate back;
     on a sync error mark the VM broken (nothing is known about the disk);
  6. unlock.
- **Replay**: per entry, a fresh preprocess allocator, no gas limit (meaning
  must not change), and a safety deadline of 10× its recorded wall time + 5 s.
  **Slices**: at least one entry per call, then stop between entries once the
  elapsed time plus 3× the next entry's recorded time passes the caller's
  budget; the partial VM stays resident and the call answers "VM x is being
  rebuilt (N of M entries); call again", so every call makes progress. A replay
  that fails other than by cancellation marks the VM broken at that seq. A
  different gno version than the journal's is noted.
- **Limits** that bound replay cost and memory, checked **before** a call runs:
  ≤ 256 KB per journaled entry (encoded); declarations ≤ 32 KB per call,
  ≤ 256 KB and ≤ 2,000 declaration inputs per VM; once a VM's kept calls have
  run 120 s in all, it keeps nothing more (it still answers pure expressions).
  After a call: one that ran over 30 s is shown but not kept (the VM is
  rebuilt). Journal ≤ 8 MB; 64 named VMs per project (scratch VMs exempt).
- **No replaying marker.** A worker death during a replay says nothing about
  the journal: the OOM killer picks the worker first by design. A journal that
  kills every replay shows itself by that; repeated worker deaths on one VM are
  reported to the model and in `gnovm_list`, never turned into "broken".
- **Broken VMs** are listed with the error; runs are refused until `/goals
  repair <vm>` (cut to the last good entry, keep `.bak`, new generation) or
  `gnovm_drop delete`.
- **Outcome by request id**: `Host.Kept(project, name, req)`.

## 8. Memory: probe, budget, eviction

- **Probe**: Linux `MemAvailable` bounded by the tightest cgroup v2 headroom up
  to the root, where a level's limit is the lower of `memory.max` and
  `memory.high`, and its usage is `memory.current` less `inactive_file` from
  `memory.stat` (reclaimable cache; counting it would leave a busy lane no
  room). macOS: free + speculative + file-backed pages. Failure: 256 MB.
- **Budget**: `memory_budget_mb` (global config only, §13), or min(1 GB, 10% of
  what the host could give), never above half of that; "could give" adds what
  the VMs already use to what is available, so the budget does not shrink as
  they grow.
- **Accounting**: the base, by its measured heap; each resident VM, cap +
  **400×** its declaration bytes (291× measured; a test holds the factor to
  the measure) + 0.3 MB per imported package + 1 MB; plus a reserve of 3× the
  largest resident cap for the GC's stack and copies.
- **Admission** before a VM becomes resident: evict LRU unpinned resident VMs,
  never the one admitted; otherwise refuse with the numbers and the pinned VMs.
- **Worker self-protection**: `debug.SetMemoryLimit(budget + base)`, an RSS
  watchdog that exits above 2 × (budget + base) and records why,
  `oom_score_adj=1000`. Ops on the box: lanes under `systemd-run --scope -p
  MemoryMax=…`.

## 9. Gas

- `callMeter`: tm2's basicGasMeter rules (negative amounts panic; refunds are
  capped at what this call used, so `used()` never goes negative); per-call
  limit; polls the wall deadline and the context every 1M gas; **trips are
  sticky** until the next call (gno swallows some panics itself:
  `tryEvalStatic`, `boundedUserSprint`).
- Gas counts CPU ops (gno's table: 1 gas ≈ 1 ns on its reference machine),
  allocation, object loads and stores, parsing (per token) and preprocessing's
  allocations; preprocessing work itself is largely unmetered, which is why big
  declaration inputs run at 15–41 ns/gas. Results show the measured wall time
  beside gas, and declaration inputs are capped (§7).
- `ns_per_gas` sets the estimate and the wall limit clamp(3 × gas ×
  ns_per_gas, 5 s, 10 min).

## 10. Tools

| Tool | Parameters | Notes |
|---|---|---|
| `gnovm_run` | `code`, `name?`, `gas?`, `reset?` | no `name`: a program VM for code with a package clause, else the session's `scratch-<8 hex of SHA-256(session)>`, created on demand ("new scratch VM: memory only; name a VM with gnovm_new to keep state"); refuses `goals` |
| `gnovm_new` | `name`, `memory_mb?`, `gas?`, `ns_per_gas?`, `persist?`, `pinned?`, `description?` | creates, or updates the given options: `memory_mb` only up, `persist` only false→true; the calling session may name its own scratch VM (or `scratch`) |
| `gnovm_list` | — | host memory and source, budget and use, base, every VM (resident, cap and use, charge, lifetime gas, entries, journal size, persist, pinned, last use, broken), the importable packages, the attribution notices |
| `gnovm_drop` | `name`, `action: evict\|delete` | delete asks permission before any lock; refused for `goals` |
| `goals_list` | — | read-only; the plan agent's too |
| `goals` | `action: add\|fact\|note\|check\|close\|adopt`, then per action: add `id,title,success,check?`; fact `id,key,value`; note `id,text`; check `id`; close `id,status: met\|dropped`; adopt `id` | errors name the missing field; one description line on goals vs todos vs `/goal` |

## 11. Tool results, registration and where the code lives

- Numbers decode as JSON numbers (a model's `1e9` works). A result is the value
  or output first, then one line such as `ok · 1.2M gas (≈1.2 ms, ran 1.4 ms) ·
  mem 0.3/16 MB · kept`; capped at 30,000 characters with the rest in a file;
  descriptions ≈1.2 KB in all.
- Built with `fantasy.NewAgentTool` (calls run one at a time, in order);
  registered in `tooldefs.names` and the two pinned fixtures; the plan agent
  gets `goals_list` and `gnovm_list`; the task sub-agent gets none.
- **All of the feature's logic lives under `internal/gnovm/`** (GNGPL):
  `gnovm` (host), `gnovm/tools` (tools, descriptions, renderer data),
  `gnovm/goals` (library glue, check language, rendering, snapshots,
  injection decisions), `gnovm/client` (worker client) and
  `gnovm.WorkerMain`. The FSL files hold only call sites, listed in §14.

## 12. Goals

> **Superseded (2026-10-30).** The harness goals below were never built; their check
> language (`internal/gnovm/goals`) and the reserved `goals` VM are deleted. mem v2
> (gnomem v2, in a realm on disk) is the one memory, and `/goal` is the one driver:
> the user's goal is a mem goal, judged when the agent closes it. This section stays
> as the record of the design it replaced.

- **Library** `gnosh/goals/v1` (embedded, versioned, immutable). The `goals`
  VM's journal begins `import goals "gnosh/goals/v1"; var G = goals.New()`;
  later entries are calls on `G` with literal arguments, generated by the
  goals operations in the worker. **Replay of the goals VM accepts only those
  shapes** (the prelude, `G.<Method>(<literals>)`, and an `Import` of a state
  literal); anything else marks it broken.
- **Ownership** decides only how a goal is shown: owners are the sessions that
  added it, adopted it (`goals adopt`, `/goals adopt`), or wrote to it (a fact,
  note, check or close adds the writer); `App.BranchSession` adds a branch to
  its source's open goals. Goals whose owners are all deleted are **unowned**
  and shown in full everywhere.
- **Fields and caps**: ids and fact keys `[a-z0-9_.-]{1,64}` (the `gnosh.`
  prefix is the harness's); title ≤ 200 B, success ≤ 500 B, check ≤ 512 B, fact
  value and note ≤ 1 KB; per goal, the latest 64 facts and notes (older dropped
  with a count); at most 100 open goals; goals closed over 7 days move to
  `closed.jsonl` (`G.Archive`).
- **Checks** are expressions in a small language, parsed with
  `go/parser.ParseExpr` and evaluated **in Go**, so no model text becomes Gno
  code: `facts["k"]` (a string), `num(facts["k"])` (a number; an error if not
  one), `has("k")`, `len(facts["k"])`, string, number and bool literals, unary
  minus, comparisons, `&&`, `||`, `!`, parentheses. **A missing fact is an
  evaluation error**: `has` is the only way to test absence. A check is
  type-checked when added, must refer to at least one fact, and is refused if
  it already passes ("record facts that make it false first"), so a goal
  cannot be met by its own wording.
- **One locked operation**: evaluating a check, recording the result
  (`G.RecordCheck`) and closing (`G.Close`) happen in the worker, in one entry
  under the goals VM's lock, against the VM's own facts: another session's
  `Fact` cannot slip between them. `Fact` clears the recorded result; closing as
  `met` needs a check that passes in that same operation, if the goal has a
  check.
- **Snapshots**: after every goals operation the worker writes `summary.json`
  (what gnosh renders) and `state.json` (`G.Export()`), both stamped with the
  journal's generation, seq and last-line hash. Upgrade, repair and compaction
  rebuild from `state.json` only if its stamp matches the journal's last good
  entry; otherwise they replay and export afresh. The worker refreshes stale
  snapshots whenever it loads the VM. Compaction runs when the journal passes
  4 MB and `state.json` is under 1 MB; a large state is imported in chunks of
  ≤ 128 KB, under the per-entry cap.
- **Injection** (inert while no goal has ever been open, so the test cassettes
  stay valid; off when `options.gnovm.goals` is false):
  - a separate `call.goalsBlock`, a hidden part after the hook block, in both
    `createUserMessage` and the live turn (re-rendered identically,
    `message/content.go:613-629`);
  - before rendering, gnosh compares the snapshot's stamp with the journal's
    header and last line (read without the lock); if they differ it renders
    "(may be stale: goals_list shows the current state)" and asks the worker to
    refresh;
  - given (a) at a session's first prompt in this process, typed or hidden;
    (b) in the restored-context message after a compaction, which is created
    even when the block is its only part; (c) on any prompt, typed or hidden
    (`/loop` and `/goal` send hidden ones), when this session's rendered block,
    without its `as-of`, differs from what it last received; after the
    session's own goals call the hash is recomputed from the new snapshot, so a
    concurrent change by another session still fires; (d) once, a "no open
    goals" block when the last open goal closes;
  - never to sub-agent sessions (as `remindOfTodos`); the "given" mark is set
    only after the message is stored; `queueContinuation` clears the block;
    `/rewind` clears the mark when it cuts the message that carried it;
  - rendering: this session's goals in full (open first, newest first), then
    unowned open goals, then a count line for other sessions' goals ("3 open
    goals of other sessions: goals_list"), within 1.5 KB; per goal the latest 5
    facts and 3 notes with counts; goals closed in the last 24 h as one line
    each; `</goals>` escaped regardless of case and spacing; titles and
    conditions shown as the goals, labelled by source ("added by the agent in
    session S" or "added by the user"), and facts and notes labelled "recorded
    claims".
- **Notices**: every add and close is announced — a TUI notice; on stderr in
  `gnosh run` (as hook warnings are); in a channel's reply — and recent closes
  are in the injected block, so an unattended lane leaves a trace.
- **`/goals`**: list, `adopt <id>`, `close <id> --force`, `reopen <id>`,
  `repair [vm]`. `/goal` refuses a condition whose first word is a `/goals`
  subcommand ("did you mean /goals …?"). Setting `/goal X` also adds a
  user-sourced goal (no check), which clearing or meeting the `/goal` closes:
  the record survives a restart (re-arming `/goal` itself is phase 6).
- **Protection**: the gnovm data root is a protected path (edit tools always
  ask); the replay shape check above limits what a hand-edited goals journal
  can do.

## 13. Configuration

`options.gnovm` (pointer struct; gnoshrc `option gnovm …` special-cased like
`option ui`): `memory_budget_mb` (0 = auto), `default_memory_mb` (16),
`default_gas` (1e9), `ns_per_gas` (1), `goals` (true: the goals tools, the
injection and the template flag together), `in_process` (false). Numbers are
float64. `memory_budget_mb` and `in_process` are honoured only from the
global config (`GlobalConfig()`, its rc siblings, `GlobalConfigData()`); a
project or workspace file (`.gnosh/gnosh.json` included) may lower the budget
but not raise it, and cannot set `in_process`. Lands with the worker (phase
2); `schema.json`, `docs/config/README.md` and the gnosh-config skill follow.

## 14. Licensing and attribution

- **`LICENSE.md`** (round-3 fixes applied): GNGPL v6 as published by
  NewTendermint, LLC or its successor, or any later version the Licensor, as
  §14 proxy, accepts by a statement in the canonical copy; copyright Jae Kwon.
  The standing temporary Strong Attribution clause defines "Licensor" (Jae
  Kwon, a designee named in the canonical copy, or his successors), the
  canonical copy, and "version" (each commit or tag published there). It
  changes only by a dated change to the canonical copy, binding only versions
  first published after it. The Attribution URL and words are those in the
  Designation that came with the version received, and the Licensor's
  reasonable interpretation cannot ask for others. It adds severability for
  anything beyond §7(g), and reserves the right to offer this program and
  contributions licensed to the Maintainer under other terms. Every source file
  carries the proxy wording, `.gno` files included.
- **The boundary**: the FSL files hold only these call sites, each one line or
  a few: `main.go` (worker dispatch), `tooldefs` (names), `coordinator.go`
  (`buildTools`, `Rewound`, `Branched`), `agent.go` (the three
  `createUserMessage` sites, the live turn, compaction's restored-context
  message, `queueContinuation`), `config.go` (the options field and the plan
  agent's tools), `app.go` (client acquire and release), `app/branch.go`,
  `cmd/run.go` (stderr notices), `permission` (the protected path), and the UI
  (`/goals`, `/goal`'s refusal, the notices, the attribution line, the License
  palette item).
- **Known interactions**, stated plainly in LICENSE.md: the gnosh binary is a
  combined work, and the GNGPL admits no further restrictions, which the FSL's
  Competing Use clause is. This subsystem is what put the GnoVM (and its
  embedded standard library) into the gnosh binary: before it, gnosh's x402
  client used only tm2 and gno.land's small `ugnot` package (round 4 corrected
  an earlier claim that x402 already linked the GnoVM). The worker, being the
  same binary, does not change that. The ways out (§18) are (a) one binary,
  with linking exceptions (NewTendermint for the GnoVM, the Licensor for this
  subsystem) or time (each Crush version's FSL becomes MIT two years after
  release); or (b) a separate `gnosh-gnovm-worker` program holding everything
  that links gno, with gnosh's own binary free of it. Changing Charm's licence
  is not the Licensor's to do. No gnosh binary is released meanwhile (the
  nightly workflow is gated to upstream). CONTRIBUTING.md, the README's Contributing and License sections, the
  CLA bot text and the release archive (`LICENSE-gnovm*.md`) match the
  `internal/gnovm/` exception. GNGPL §13 binds gnosh's own remote interfaces,
  so server mode and channels link the source.
- **Attribution notices**, in every layout: the landing screen, the compact
  header and the sidebar footer (OSC 8 links); `gnosh --version` and `gnosh run
  --help`; a **License** item in the command palette (GNGPL §5(d) Appropriate
  Legal Notices); the README and gnosh.ai pages that promote gnosh. Each says
  "built on gno.land" (NewTendermint's, owed since gnosh includes the GnoVM)
  and gives this subsystem's Designation. Landed so far: `gnosh --version`, the
  landing screen and `gnovm_list`. When the tools are usable, the README opens with
  Jae's line: "Yo dawg, I heard you like Go, so I put Go in your Go and slapped
  it onto Qwen, so it can run Go while it writes Go."
- **Timing**: kept private while it was built, then published at Jae's word on
  29 October 2026, with phases 1 and 2.

## 15. Failure modes

| Risk | Mitigation |
|---|---|
| Go fatal in the VM | worker isolation; caps ≤ 64 MB bound GC depth (measured 0.5–1.1 B/B) |
| A test spawns a worker (fork bomb) | `testing.Testing()` refusal, own-binary refusal, fd-3 handshake, in-process tests, a test that proves it |
| `.env` or a fatal dump from the worker | `cmd.Dir` = data dir; stderr to a bounded log |
| Constant folding blowup | fresh capped, metered preprocess allocator per entry |
| Heap beyond the caps | accounting at 400× declarations, imports, reserve; worker memory limit; RSS watchdog; `oom_score_adj` |
| Replay too slow or quadratic | limits before running; slices within the caller's budget; resident partial VM |
| Long calls starve other sessions | wall limits; heartbeats for every request; per-request clamp in server mode |
| Two gnosh on one VM; delete, repair, recreate | per-VM lock outside the VM dir; generation + last-line hash; machine.json re-read; torn tail cut |
| Worker killed (cancel, watchdog, OOM, deploy) | journals on disk; replay on demand; outcome by request id |
| Import scope, `:=` deps, redeclaration | imports bound package-wide; `:=` of new names becomes `var`; names checked before running |
| A state-changing eval not kept | only provably pure evals are left out; shadowed builtins counted |
| Rendering runs user code | structural rendering only |
| Swallowed meter trips | sticky trips |
| Goals: forged close, stale check, stale snapshot | Go-evaluated check language; one locked operation; stamped snapshots; shape-checked replay; protected path |
| Goals: premature close in an unattended lane | notices on stderr and in channels; recent closes in the block; `/goals reopen` |
| Goals injected where unwanted | ownership, count lines for others, inert without goals, a switch |
| Version skew after deploy | `/proc/self/exe`; handshake versions; gno version in each journal |

## 16. Tests

- **Phase 1, on `Host` in-process (37 tests, `-race` clean)**: the meter
  (limits, deadline, cancel, sticky trips, refund floor); scratch state, mixed
  input, forward references, a second `main`, `x` not shadowed, redeclaration
  refused and `reset`, programs run fresh, a failed call keeps nothing, gas and
  memory caps; persistence across a new Host, `time.Now` replayed, two Hosts
  catching up, a deletion noticed, a torn tail cut, LRU eviction, unavailable
  packages; and round 3's cases: imports seen everywhere, `var` depending on
  `:=`, mutating builtins kept across a rebuild, a shadowed builtin, a compile
  error in an expression keeping the VM, the declaration charge holding the
  measured heap, the per-call declaration limit, resumable replay, and the
  replay-cost limits.
- **Still to add in phase 1**: a differential test (random workloads at 1–2 MB
  caps, every global dumped live vs replayed, with rebuilds midway); journal
  unit tests (sequence gap, corrupt middle line, generation change, header
  version); a 50,000-statement replay; repair.
- **Phase 2 (worker)**: protocol over an in-process pipe pair; cancel ack;
  heartbeats during a 15 s metered run and a 15 s replay (neither killed); the
  client's kill rules; outcome by request id with a failpoint between the write
  and the `fsync`; two Apps sharing a client; `go test` cannot spawn a worker,
  nor can a test hand over its own binary; on the devbox, under `systemd-run
  --scope -p TasksMax=64`, a real worker: a memory-only VM surviving a worker
  restart, a 64 MB `&q` chain with RSS measured.
- **Phase 3 (goals)**: the library (each method, refusals, export/import fuzzed
  with control characters, invalid UTF-8, U+2028 and `</goals>`); the check
  language (whitelist, missing facts, types, a check that already passes
  refused, hostile inputs); the locked check-and-close; snapshot staleness;
  ownership, adoption, branches; injection (a)–(d), never twice, hidden
  prompts, continuations, rewind, sub-agents excluded, the stored message for
  turn N byte-identical in request N+1.
- **Long-goal end to end (A), deterministic, in CI**: a scripted model works a
  multi-step goal with a check over many turns, including a stale and a hostile
  check and a premature close refused; a second Host changes a goal (trigger
  c); a compaction (the block in the restored-context message, not repeated on
  the next prompt); then **the client is closed and the App rebuilt**, and the
  scripted model **reads the goal id and the next missing fact from the
  injected block**, records it and closes the goal as met — which succeeds only
  because the replayed VM kept the earlier facts and the check. The injected
  block must equal a rendering of the replayed state. A variant fails the
  snapshot write after the append; the restarted injection must still match
  the journal. Run once more through a real worker on the devbox.
- **Real model (B)** on the AI box, isolated XDG and project, quiet time:
  - a fixed task with a script-checked outcome (N functions with tests in a
    scratch repo), split over batches;
  - pinned model, reasoning level and context size;
  - a scripted `/compact` at batch k and a SIGKILL of gnosh and the worker at
    batch m;
  - at least 3 runs with goals on, at least 1 resumed in a fresh session (via
    `goals adopt` or an unowned goal), and at least 1 control with goals off.
  - Pass when no goal is re-added or duplicated, the goal closes as met only
    when the script's check passes, there is no premature close, and the
    tool-error rate stays under 10%.
  - Recorded per run: tool calls and their errors, check failures, refusals,
    turns to completion, versus the control.

## 17. Phases

1. **Done** (private commits): core Host (base and libraries, VM,
   normalisation, meter, journal and durability, limits, budget, locks),
   licensing files, tests. Still to do: the phase-1 test additions in §16.
2. Worker, IPC, refcounted client, lifecycle wiring, config (`in_process`,
   `memory_budget_mb`); tests.
3. The `gnovm_*` tools (**done**); the goals library, goals operations,
   snapshots, injection, `/goals`, the `/goal` record, notices, the protected
   path; tests.
4. Attribution in every layout, the License palette item, the README (and its
   opening line), docs, schema, the renderer.
5. End to end (A) and (B); landing and deployment; memory notes.
6. An LLM judge on close via a cache-friendly request; re-arming `/goal` after a
   restart; a cross-process memory ledger; a worker pool.

## 18. Open questions for Jae

- The attribution designation for this subsystem (placeholder: https://gnosh.ai,
  "built on gnosh"), and NewTendermint's for the GnoVM notice (assumed
  https://gno.land, "built on gno.land").
- The binary-level licence conflict (§14), and which way out to take: (a) one
  binary, with NewTendermint's linking exception (or waiting for FSL→MIT); or
  (b) a separate `gnosh-gnovm-worker` binary, which needs nobody's consent but
  the Licensor's and also ends the self-re-exec hazard that §3's spawn rules
  guard against.
- (Answered: the work went public on 29 October 2026.)

## 19. GNOMEM: the agent's board (added 2026-10-30)

Real-model scenario runs (`testdata/scenarios`, `TestScenarios`) showed that
with a short recipe in the system prompt the model keeps its checklist and
memory in persistent VMs, but writes its own structs each session (about 400
tokens at 5-20 tokens/s) in a different shape each time. Jae asked for "memory
Go structs for goals, subgoals, research, whatever": a minimal starting point,
"a good foundation", which others may replace. It is model-side. (It was
written beside the harness's goals of §12, which were never built; mem v2
supersedes both, see the note at §12.)

- **`klash/gnomem/v1`** (`klashlibs/klash/gnomem/v1`): `Board{Items, Facts}`
  and `Item{Title, Parent, Status, Result, Notes}`, plain data (Items[0] is the
  root; Status "" is open and any other word closes); `Add(title, steps...)`,
  `Step`, `Done(id, result)`, `Drop(id, why)`, `Reopen`, `Note`,
  `Show(ids...)`, `String`. One item type serves as goal, step and question; a
  condition is a step (Done refuses while a step is open); a question's notes
  are its findings and its result its answer. Each change prints a line with
  the ids it made and what is next. Reopen restores the steps dropped with an
  item. Titles clip at 200 bytes and notes and results at 1000, never refused;
  Show's outline is capped near 3000 bytes.
- **Frozen storage.** A persistent VM is rebuilt by replaying its calls against
  the library this gnosh embeds, so a call never changes what it stores or the
  ids it returns, and never refuses what it once accepted; it may store new
  trailing fields, refuse less, and print or show things differently.
  `TestMemV1IsFrozen` replays a fixed corpus against a field-by-field dump that
  is never rewritten, and against format files that may be, after review.
  `importByName` binds `gnomem` to the newest version.
- **`mem`.** The host makes a persistent VM `mem` per project on first use; its
  first entry, `import gnomem "klash/gnomem/v1"; var mem = gnomem.New()`, is
  written under the VM's lock when the journal is empty, so racing processes
  end with one board. A VM named mem whose mem is not a board is refused with
  what to do; assigning to `mem`, or a call that leaves it nil, keeps nothing;
  code using `mem` elsewhere is pointed there.
- **Reads are not journaled.** An eval or print that only reads a board
  (`R.Show(...)`, `R.String()`, `R` alone, `println(R)`, `fmt.Println(R)` when
  fmt is the real package) is dropped from the entry once `vm.isBoard`
  confirms R's static type is `*klash/gnomem/v1.Board` and no builtin is
  shadowed. A board read's value is shown unquoted.
- **A session starts with the board in view.** After each change the host
  writes the outline's first line to the VM's `summary.txt`; a new top-level
  session's first prompt carries it (as SessionStart hook output does) when an
  item is open, with the file's time. No worker starts and no VM is rebuilt to
  read it.
- **Recipe.** `<gno_machines>` in the coder prompt teaches `mem` in eight
  lines, with examples that are not the scenarios' own data.

Review findings are in Appendices E (round 1) and F (round 2).

---

## Appendix A. Review round 1: findings and dispositions

Three independent reviewers (GnoVM correctness; gnosh integration and UX;
failure modes, safety and licensing). Every factual claim used was checked in
the source.

| # | Finding | Verified | Disposition |
|---|---|---|---|
| C1 / S1 | Preprocess (constant folding) has no cap, gas or cancel | yes: machine.go:153-181; keeper.go:443-446 | accepted (§5) |
| C2 / S3 | `Sprint` runs user `String()`/`Error()` | yes: values_string_stream.go:405-421 | accepted: `BoundedSprintTV(nil)` (§6) |
| C3 | GC evicts live store objects, giving a second copy | yes: garbage_collector.go:118; store.go:1510 | accepted: `noEvictStore`; spike shows no leak |
| C4 | Prelude and journal share a namespace across upgrades | design | accepted: versioned library, expression checks (§12) |
| C5 | Replay preconditions | design | accepted (§7) |
| C6 | Meter: recovered trips, parse charges, refunds, no store gas | yes: store.go consumeGas no-op with a nil meter | accepted (§5, §9) |
| C7 | Rebuild is the only correct reset; never `Release` | yes | accepted |
| C8 | Measurements over-generalised | accepted | differential test (§16) |
| I1 | The data-dir lock is opt-in, so two processes can corrupt journals | yes: connect.go `WithDataDirLock` off by default | accepted: per-VM flock and catch-up (§7) |
| I2 | Nothing injected after a restart | design | accepted: `summary.txt` |
| I3 | Go-shaped input fails (`package`, mixed chunks, no `fmt`) | yes: go2gno.go:104-108, 154-157; no fmt dir | accepted: normalisation and the `fmt` shim (§6) |
| I4 | Raw gno goals are hard for a 27B model | design | accepted: the `goals` tool (§10, §12) |
| I5 | Injection plumbing (hidden prompts, tags, atomic mark) | yes: prompt_hook.go:153-161 | accepted (§12) |
| I6 | Goals shown too rarely / to the wrong sessions | design | accepted: (c), owning-session scoping |
| I7 | The model grades its own checks; `/goal` survives only as a record | design | accepted: judge on close; re-arm in phase 6 |
| I8 | Sub-agent and plan registration; deleting `goals` | yes: config.go readOnly/plan subsets | accepted (§10) |
| I9 | Server mode has one App per workspace; shutdown order | yes: backend.go `app.New` per workspace | accepted: process singleton (§3) |
| I10 | Tool shape (action enum, float gas, output format, renderer) | yes (JSON `1e9` into int64 fails) | accepted |
| S2 | Budget unsafe on the box; caps ≠ heap; fatal GC recursion | yes: garbage_collector.go `VisitAssociated` recursion | accepted: worker, 2× accounting, ≤64 MB caps, 10%/1 GB; a ledger deferred to phase 6 |
| S4 | Locking and liveness; shutdown ordering | yes: CancelAll 5 s, then `db.Release` in parallel cleanups | accepted, except "manager holds `gnovm/LOCK` for the process's life": **rejected**, it would lock other lanes out; per-VM locks instead |
| S5 | Journal durability gaps | design | accepted (§7) |
| S6 | Goals summary as an injection channel that persists | design | accepted: global dir, fencing, caps |
| S7 | Licence interactions understated | yes: CONTRIBUTING.md:5-7, 98-100; the x402→gnoclient→vm chain | accepted: restated (§14) |
| S8 | Notices must be in the UI itself | yes: both clauses say "any user interface" | accepted: TUI footer and `--version` |
| S9 | Drafting gaps in the clause | yes | accepted (§14) |
| S10 | Spec gaps (probe text, `memory.high`, store gas, chain stubs, reserved name, parse-error rebuild) | yes: banker.go deref; hostmem_darwin counts speculative and file-backed pages | accepted |

## Appendix B. Review round 2: findings and dispositions

Two fresh reviewers read revision 2 only (systems and GnoVM correctness; goals,
tools, injection and licence). Every claim used was checked in the source or
re-run in the spike. The spike also corrected one of my own round-1
conclusions: `x := 1` does persist; gno's `Eval` wrapper had shadowed `x`.

| # | Finding | Verified | Disposition |
|---|---|---|---|
| S1 | **Blocker**: the spawner's env-var guard is unset by `testenv`, so App-level tests would exec the test binary (the Oct 25 fork bomb again) | yes: testenv.go:14, 33-44 | accepted: `testing.Testing()` refusal, fd-3 handshake, in-process App tests, a test that `go test` cannot spawn (§3, §16) |
| S2 | Redefining a func silently keeps the old body; forward references fail | yes, spike | accepted: declarations run as files through `RunFiles`, explicit refusal, `main` renamed per input, splitter rules, parse everything first (§6) |
| S3 | A worker kill loses memory-only VMs and misreports outcomes | yes | accepted differently: every journal is on disk (ephemeral ones per gnosh process); outcomes resolved by request id (§3, §7) |
| S4 | Replay can fail or never finish (preprocess allocator never collects; budget from gas) | yes: alloc.go:409; store.go:177-191 | accepted: fresh preprocess allocator per entry; budget from recorded `wall_ns`; the partial VM stays resident (§5, §7) |
| S5 | Catch-up trusts sequence numbers alone (delete renames the lock's dir; repair; torn tail) | yes: lock.go:12-15 | accepted: locks outside the VM dir, inode check, generation and last-line hash, torn tail truncated, raised cap persisted first (§7) |
| S6 | The `replaying` marker brands healthy VMs broken | design | accepted: lock held across load and replay; marker with pid, boot id, start; broken only on an unexplained death (§7) |
| S7 | One App's Shutdown kills the process-wide worker (server mode) | yes: app.go:1296; backend.go:255-270 | accepted: refcounted client, lazy spawn (§3) |
| S8 | One-at-a-time worker blocks every session; spurious kills during base loads and fsync | yes: lock_unix.go:20-35 | accepted: reader goroutine, cancel ack, heartbeats, `TryFile` "busy"; the remaining cross-session wait is stated and bounded by the 5 s default, a worker pool is phase 6 (§3) |
| S9 | Worker hygiene: version skew, Ctrl-C, framing, orphans; §15 wording on `Error()` | yes: root.go:310-315; op_call.go:912-916 | accepted (§3, §6 step 7) |
| S10 | Memory: GC stack, retained ASTs, pinned stdlib objects, base growth | estimate | accepted: base charged per load, ASTs and imports charged, transient reserve; RSS measured on the devbox (§8, §16) |
| G1 | Close-as-met bypassed by code in a check; stale checks; `gnovm_run` can write `goals` | yes | accepted, stronger: checks are a whitelisted language evaluated in Go, so no check text becomes Gno code; `Fact` clears; a fresh check at close; `gnovm_run` refuses `goals`; `gnosh.` prefix reserved; every close shows a notice (§12) |
| G2 | The close judge costs minutes on the box and cannot see older evidence | yes: agent.go:1136-1140; goal.go:36 | **deferred to phase 6** (asked the way compaction asks, to hit the prompt cache); until then checks, the notice and `/goals reopen` guard closes |
| G3 | Test (A) can pass without any replay | yes | accepted: singleton closed between Apps, replay asserted, stored vs live message byte-identical, more cases, a real-worker run, ≥3 real-model runs (§16) |
| G4 | Injection: (c) re-fires, continuations re-send, `/rewind` loses the block, ownership unrecorded | yes: agent.go:569-572; coordinator.go:454-472 | accepted, plus branch lineage (§12) |
| G5 | Licence notes overclaim the worker, the CONTRIBUTING exception does not exist, §13 offer and promotional material missing, timing | yes: CONTRIBUTING.md:5-7, 38-42, 98-100 | accepted: claim dropped, exception written, source offer, README and gnosh.ai, decided before phase 1 lands (§14) |
| G6 | Drafting holes: headers, publisher, proxy, canonical copy, prospective rule, "conclusively govern", severability | yes | accepted (LICENSE.md, §14) |
| G7 | Scratch VMs vanish on a worker restart, are never cleaned up, and their names break the 32-character rule | yes: session.go:114 | accepted: on-disk ephemeral journals, hashed names, LRU, exempt from the VM cap, "new scratch VM" notice (§7, §10) |
| G8 | The `goals` tool's shape is hard for a 27B model | design | accepted: `goals_list` and `goals`, per-action fields, `num`/`has`, one line on goals vs todos vs `/goal` (§10) |
| G9 | The library upgrade path cannot be carried out | design | accepted: `state.json` after every operation; upgrade, repair and compaction rebase from it in phase 3 (§12) |
| G10 | A project config can raise the budget or set `in_process`; float decoding; three switches | yes: config/load.go:989-1007 | accepted (§13) |

Not adopted: a separate worker binary (on the premise that x402 already linked
the GnoVM into gnosh, which round 4 found false: it is an open choice again,
§18); a cross-process memory ledger
(phase 6, as in round 1).

## Appendix C. Review round 3: findings and dispositions

Two fresh reviewers read revision 3. The systems reviewer ran experiments on
the devbox; phase 1 had meanwhile been implemented, so its findings were
checked against the code as well. Where a fix is in code, the test that holds
it is named.

| # | Finding | Verified | Disposition |
|---|---|---|---|
| S1 | **Blocker**: an import in a file is visible only in that file | yes (measured; preprocess.go:483-510) | fixed: imports are bound package-wide with `RunDeclaration` (§6; `TestImportIsSeenEverywhere`) |
| S2 | A file's var initializer depending on a `:=` name panics gno | yes (measured; preprocess.go:6368-6372); `RunDeclaration` vars are refused too ("unsatisfied deps") | fixed: `:=` of new names becomes `var` in a file (§6; `TestVarDependsOnShortDeclaration`) |
| S3 | Builtins change state in "pure" evals (`delete`, `append`, `copy`, …); a redeclared `len` | yes | fixed: only provably pure evals are left out, and shadowing is checked (§6; `TestMutatingBuiltinsAreJournaled`, `TestShadowedBuiltinIsJournaled`) |
| S4 | Declarations keep ~300–375× their source, not 64× | yes (measured 291×) | fixed: 400× charged, a calibration test, per-call and per-VM caps (§7, §8; `TestDeclarationChargeCoversHeap`) |
| S5 | A run or replay over 10 s gets the worker killed | design | accepted: heartbeats for every request's whole life; kill rules (§3) |
| S6 | Replay unbounded and quadratic; the wait is not "5 s" | yes (measured) | fixed: limits before running, replay in slices, honest wait bound, server-mode clamp (§3, §7; `TestReplayIsResumable`, `TestReplayCostLimits`) |
| S7 | Redeclaration check narrower than gno's gaps; the `func (` rule | yes | fixed: names accumulated across the input incl. `:=` and imports; the rule needs a name and `(` (§6) |
| S8 | File names must be unique and the same on replay | yes | fixed: `in<seq>_<k>.gno` (§6) |
| S9 | The replaying marker misfires across processes | design | resolved by removing the marker (§7) |
| S10 | Worker hygiene: stderr, `.env`, Windows, own-binary hatch | yes (main.go:14) | accepted (§3) |
| S11 | Lock API gives no fd; ephemeral VMs need no locks | yes (lock.go:56-71) | accepted: no inode check (locks never unlinked), pid in the lock file, memory-only VMs unlocked (§7) |
| S12 | Durability edges: encoded size, append failure, replay failure, atomic create, stale snapshots, gno version | yes | fixed in code (size checked first, VM rebuilt on append failure, broken on replay failure, temp-dir create, gno version in the header); snapshots stamped (§12) |
| S13 | Probe counts page cache; `memory.high` missing | yes | fixed (§8; `TestCgroupHeadroom`) |
| S14 | Preprocessing runs at 15–41 ns/gas | yes (measured) | accepted: stated; wall time shown beside gas; declarations capped (§9) |
| S15 | §16 tests would have caught none of S1–S6 | — | accepted: those tests now exist; the rest are planned (§16) |
| G1 | The Designation can still change retroactively ("until …", "as the Licensor interprets it") | yes | fixed in LICENSE.md: binds only the versions carrying it; interpretation limited to the Designation's words; "version" defined; designees and §14 acceptances published in the canonical copy; reservation narrowed |
| G2 | The licence boundary is undrawn; feature logic would land under FSL | yes | accepted: everything under `internal/gnovm/`; FSL call sites listed (§11, §14) |
| G3 | Goals snapshots are not tied to the journal | yes | accepted: stamped snapshots, staleness checked before injection, rebase only from a current `state.json` (§12) |
| G4 | No session can adopt a goal | yes | accepted: writes adopt, `goals adopt`, `/goals adopt`, unowned goals in full (§12) |
| G5 | The close safeguards exist only in the TUI | yes (cmd/run.go) | accepted: stderr and channel notices; recent closes in the block (§12) |
| G6 | The tools cannot raise `memory_mb` as their error says | yes | fixed in code: `gnovm_new` updates an existing VM (memory up, persist false→true); the session's own scratch VM allowed (§10) |
| G7 | Test (A) can pass without goals surviving | yes | accepted: the model reads the next fact from the injected block after a rebuild; block = rendering of replayed state; a snapshot-failure variant (§16) |
| G8 | Test (B) cannot fail | yes | accepted: script-checked task, pinned model, compact and kill, fresh-session resume, control run, pass criteria (§16) |
| G9 | Check language semantics; evaluation races | yes | accepted: missing fact is an error, type-checked, must use a fact, refused if already passing, unary minus, caps, one locked operation (§12) |
| G10 | Injection edge cases (as-of in the hash, hidden prompts, last goal closing, own-call hash, sub-agents, mark timing, hidden first prompt) | yes | accepted, all seven (§12) |
| G11 | Goals files can be forged; adds are silent | yes | accepted: protected path, shape-checked replay, add notices, source labels (§12) |
| G12 | Size limits and growth | yes | accepted: field caps, latest facts and notes, archive, chunked import, compaction rule, fuzzing (§12, §16) |
| G13 | `/goal` vs `/goals`; repair takes no VM; `/goal` not recorded | yes (ui/model/goal.go) | accepted (§12) |
| G14 | The workspace `.gnosh/gnosh.json` must not raise the budget either | yes (config/load.go) | accepted (§13) |
| G15 | CONTRIBUTING, README and CLA bot text do not match the exception | yes | fixed: all three, plus the date and mixed contributions |
| G16 | Attribution missing in compact layouts and `gnosh run`; legal notices; `.gno` headers; release archive; §13 wording; "change gnosh's licence" | yes | fixed: dated notice in `klashlibs/fmt`, release archive, §13 and ways-out wording; the rest planned for phase 4 (§14) |
| G17 | Phase order (licence files, config) | yes | accepted (§17) |

Not adopted: nothing in round 3. Deferred as before: the LLM judge on close,
re-arming `/goal`, a cross-process ledger and a worker pool (phase 6).

## Appendix D. Review round 4: findings and dispositions

Two fresh reviewers read revision 4 and the phase-1 code; the systems
reviewer backed each finding with a failing probe test, and the probes are now
regression tests (`round4_test.go`).

| # | Finding | Disposition |
|---|---|---|
| S1 | **Blocker**: VM names reach paths (`./goals`, `../../<project>/vms/x`) | fixed: names checked in `lookup`, every entry point (`TestRound4NamesAreNotPaths`, `TestRound4VMNameTraversal`) |
| S2 | `chain.Emit` holds Go heap outside the cap | fixed: `chain.emit` is a no-op native; growth now bounded by Go's GC pacing (+85 MB at 1e10 gas, was 1.33 GB) |
| S3 | Closures in statements keep syntax trees, uncharged | fixed: function literals count as declaration bytes, for the charge and the caps |
| S4 | The replay limit is bypassed by a trailing expression | fixed: limits apply to anything the call keeps |
| S5 | A builtin shadowed in the same input | fixed: purity rechecked after the run |
| S6 | Persistence read from machine.json, not location | fixed: location decides; machine.json written before the move |
| S7 | A second, stale repair cuts new entries | fixed: repair syncs first; the break point is tied to its generation |
| S8 | Mixed `:=` still reaches gno's panic | fixed: refused with how to write it |
| S9 | Budget not enforced as VMs grow | fixed: LRU eviction after each kept call |
| S10 | A replay wall timeout marks the VM broken | fixed: treated as "call again" |
| S11 | Wait bound understated | stated; one deadline per call is a follow-up |
| S12 | A full journal refuses pure reads | fixed |
| S13 | Small items (imports count, temp dirs, .bak fsync, empty-cgroup budget, lookup order) | fixed; tiny declarations measured at ~420× (charge 400×) noted |
| G1 | The licence record's central fact is false: gnosh did not link the GnoVM before | fixed in LICENSE.md and §14; the choice is put to Jae (§18); nightly gated; notice in `--version` and on the landing screen |
| G2 | Goals export in Gno costs seconds per op; the store can wedge at its caps | accepted for phase 3: snapshots produced in Go by walking the VM's values; bounded state; fixed goals-VM options; `/goals compact` |
| G3 | Test (B) can pass without the goal being met | accepted: pass needs the script's check met in ≥2 of 3 runs, one a fresh-session resume, and an improvement over the control |
| G4–G16 | Keying by worktree root; G10 hash rule; `/goal` record rules; snapshot-failure variant; block size; channel notices; `.env` moving the root; call sites; `/goal` refusal; tool alias; headers; attribution coverage; stale status | the alias, headers and status fixed now; the rest carried into phase 3–4 work |


## Appendix E. GNOMEM review round 1: findings and dispositions

Three fresh reviewers read the draft board (v0: `Board{Items []*Item, Facts
map[string]Fact, byID, last}`, `Item{ID, Title, When, Check func() bool,
Status int, Result, Notes []Note, Subs, Parent}`, 13 functions) with three
lenses: minimalism (M), how a 27B model would use it (E), and durability as a
foundation (F).

| # | Finding | Disposition |
|---|---|---|
| F1 | No versioning: replay runs old calls against the current library, so a rename or a new refusal breaks old boards, and a changed meaning silently changes them | fixed: `klash/gnomem/v1`, grow-only, golden-file test, newest version auto-imported |
| F2 | Reads are journaled as writes, so reading wears out a board | fixed: board reads not journaled after a type check (§19) |
| F3 | Show had no real bound, rendered twice, and a newline could fake a line | fixed: one pass, closed items one line, newest 10 closed top-level, 20 facts, hard cap, newlines shown as ⏎ |
| F4/M1 | The tree stored four ways (`Items`/`Subs`/`Parent`/`byID`), so hand edits break it | fixed: one append-only `Items` slice indexed by id, `Parent` the only link |
| F5 | Caps must exist from the first release | fixed: clipping (not refusal) at 200 and 1000 bytes; Show's cap |
| F6 | `ImportablePackages` would expose the harness-only goals library | fixed: `gnosh/goals` excluded |
| F7 | `TestGnomemSurvivesARestart` compared a value with itself | fixed: captured on the first host |
| M2/E4/F-b | `Check` closures prove nothing (a VM cannot run `go test`), cannot be shown or exported, and capture variables that later change; `When` is just more title | removed: a condition is a step |
| M3 | `Fact` and `Note` structs carry timestamps nothing shows | removed: `Facts map[string]string`, `Notes []string`; the board keeps one `Changed` time |
| M4/E4 | `Done` both a constant and a method; `Status` printed as 0 | fixed: `Status` is a string |
| M5 | Too many readers, and `Done(Next(), …)` closes whatever is first | fixed: `Show`/`String` only; the outline and receipts name the next item |
| M6 | No version in the path | fixed (F1) |
| E1 | Resume depends on names the model chose, and `B := gnomem.New()` again wipes the board | fixed: the harness's `mem` VM and variable; assigning to `mem` refused |
| E2 | Only `println(B.Show())` was readable; a trailing `B.Show()` came back as one quoted line | fixed: a multi-line string value is shown as it is |
| E3 | Show hides what was tried | fixed: the next item's last three notes, a header with counts, the next item and the time |
| E5 | Wrong ids pass silently; errors cost round trips | fixed: receipts from Done/Drop/Reopen; errors list open items with titles |
| E6 | A checklist took three calls and ~100 tokens | fixed: `Add(title, steps...)` in one call, and `mem` needs no setup |
| F-c | Should the harness's goals and the model's board be one design? | no: separate authorities sharing a vocabulary; the harness never takes the board as proof (DESIGN §12 stays). Superseded 2026-10-30: mem v2 is one design for both, see the note at §12 |
| E-recipe | Examples in the recipe must not be the scenarios' data | done |

## Appendix F. GNOMEM review round 2: findings and dispositions

Two fresh reviewers read v1 (§19): one for simplicity and convergence (S),
one hunting bugs adversarially (A). Real runs added one finding (R). All were
settled before any board outside the tests depended on v1, so the golden
files were written again once.

| # | Finding | Disposition |
|---|---|---|
| R1 | `Add(goal, steps...)` showed only the goal's id; the model guessed the steps' ids and was refused | fixed: every change prints a line with the ids it made and what is next |
| S1 | Freeze what calls store, not what Show prints: the text ships with the recipe | fixed: rule reworded (§19); data.golden is a field-by-field dump, never rewritten; the show, items and printed files are format tests, rewritten after review |
| S2 | The frozen corpus skipped the state rules that matter | fixed: nested drop cascades, Reopen up the parents, Step on a step, Note on a closed item, Reopen of an open item, multi-byte clipping |
| S3 | `Changed` was wrong (facts never bumped it, an open item's reopen did) | cut; the session summary uses the summary file's time |
| S4 | Show printed facts last, so a cut dropped them; an unknown status was drawn open but counted closed | fixed: facts after the header; any non-empty status closes and is shown as its word |
| S5 | Recipe: its example contradicted "a condition is a step", it never taught Step, and `q := mem.Add` made a permanent declaration in mem per use | fixed |
| S6 | Boards have no way out of v1 when they reach the journal or replay limits | open: a harness rebase (replay, then rewrite the journal as the first entry plus the data) needs no v1 change while the data stays plain |
| A1 | **State divergence**: `fmt` was trusted by name; a VM's own `var fmt` with a mutating Println had its print dropped from the journal | fixed: prints count as reads only when the VM's fmt is the real package |
| A2 | **State divergence**: `%p` printed a Go address, different on every rebuild, into stored strings | fixed: refused |
| A3 | ensureMem could delete, without the VM lock, a board another gnosh had written | fixed: the first entry is written under the lock on first use; ensureMem never deletes |
| A4 | Show cut the next item when an open goal had many closed steps; Show(ids...) had no cap | fixed: the newest five closed steps under an open item, the rest counted; both capped |
| A5 | Drop then Reopen left a goal's condition step dropped, so the goal could be marked done untested | fixed: Reopen restores the steps dropped with an item; Drop needs a reason |
| A6 | An older VM named mem was quietly used, or its prelude refused as "already declared" | fixed: refused with what to do; its Show never runs for the summary |
| A7 | Minor: stale journal cache, the first call's rebuild inside a short lock budget, `gnovm_new` mem with persist false, `{ mem = nil }` | fixed (the first two by A3; persist false refused; a nil board keeps nothing) |
| A8 | Raw display of every multi-line string lost newlines and hid quotes | fixed: only board reads are shown unquoted |
| A9 | fmt applied String methods for every verb | fixed: only for %v, %s, %q, %x and %X, as Go does; nested Stringers are still printed by structure |
