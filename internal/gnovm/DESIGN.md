# GnoVM machines in klash — design and plan (v3)

Status: revision 3 (2026-10-29), after review rounds 1 and 2. Licensed with the
rest of `internal/gnovm/` under the GNO Network GPL v6 (or later as accepted by
the Licensor's proxy) with the Additional Terms in [LICENSE.md](LICENSE.md).
Appendix A and Appendix B record every review finding and what was done with
it. In prose, "VM" means a Gno virtual machine; "machine" already means an ssh
host in klash.

## 1. What Jae asked for

Klash should (1) know how much memory is available for Gno VMs; (2) create
VMs with limits; (3) declare state and evaluate expressions in them; (4)
optionally persist them; (5) name each VM, with a persistence option; (6) list
them; (7) evict VMs to make room for large work; (8) understand gas, 1 gas ≈
1 ns, configurable per VM; (9) use this as the harness's way to keep track of
long-standing goals and success conditions; (10) be planned, reviewed until
convergence, and tested on long goals. The code is published under the GNO
Network GPL with a standing temporary strong attribution clause in Jae's
favour (§14).

## 2. Evidence (devbox spikes, gno pinned at 5cdbc25fcde0)

| Measurement | Result |
|---|---|
| Empty VM | 43 KB, 0.1–2.3 ms |
| Gas vs wall | 0.35–1.3 ns/gas across workloads — "1 gas ≈ 1 ns" within ~3× for *metered* work |
| 1 MB cap, 20 MB garbage / 8 MB live | GC collects / `allocation limit exceeded` |
| Per-call gas limit; 200 ms deadline | both stop a `for {}` exactly |
| One store per VM, import strings+strconv+time | **~88 MB, ~1 s per VM** |
| Shared base + transaction overlay per VM | **345 KB, 8 ms per VM** (lazy decoding grows it with use) |
| All 50 stdlibs into a base | 1.8 s, +149 MB; `regexp` on demand 1.2 s then 1.4 ms per VM |
| 800 VMs created and dropped | heap flat (no leak) |
| Replay into a fresh VM | identical on the probes tried; 2,001 entries in 59 ms |
| `noEvictStore`, 10,000 calls at 2 MB cap (315 GCs) | heap flat; `io.EOF` identity held |
| `x := 1` then evaluate `x` | persists; **gno's `Eval` wraps in `func(x interface{})` and shadows a user `x`**, so klash uses its own parameterless wrapper |
| One input: `main` before `helper`; mutual recursion; method before type; `const (iota)` | all work when declarations run as a file through `RunFiles` |
| Redeclaring a `func` (both `RunDeclaration` and `RunFiles`) | **silently keeps the first body** — klash refuses redeclaration itself |
| `fmt` shim (gno `ufmt` v0 renamed) | `Println`, `Errorf` work; `%v` of a slice prints `%!v(unhandled)`; floats print `%f` style |
| A panicking statement | VM unusable (`m.Exception` set, stacks half-run) |

## 3. Architecture

```
klash process ── refcounted process-wide vmhost client (lazy spawn) ──┐
   │                         fds 3/4 via ExtraFiles: length-prefixed JSON
   ▼                                                                   ▼
gnovm worker = klash binary run as `klash gnovm-worker` (child)        heartbeats
   └── gnovm.Host   one VM operation at a time; a reader goroutine handles cancel
       ├── base            shared gno store; stdlibs + klashlibs loaded on demand
       ├── vms[project][name]   on-disk journal (+ resident VM when admitted)
       ├── budget          host probe, accounting, LRU eviction
       └── per-VM file locks   cross-process journal safety
klash reads goals summary.json directly for injection (never the worker or a lock)
```

- **Why a worker**: it isolates what the VM can do to a process — a Go *fatal*
  (deep GC recursion, a gno bug), heap the allocator does not count (AST,
  types, decoded imports), and the OOM killer's choice of victim (the AI box
  lost llama-server twice in October). The worker sets `oom_score_adj=1000`,
  `debug.SetMemoryLimit`, and an RSS watchdog. All journals are on disk (§7),
  so a restarted worker loses nothing; it replays on demand. IPC adds ~0.1 ms
  per call. The worker is the *same binary*, so this is fault isolation, not
  licence separation (§14).
- **Spawn safety** (the October fork bomb came from a test re-exec'ing its own
  binary, and the self-improvement loop runs these tests on the box):
  - the spawner refuses when `testing.Testing()` (precedent:
    `config/load.go:1200`, `tools/rg.go:17`) unless it is handed an explicit
    non-test binary — an env-var guard would not survive `testenv`, which
    unsets every `KLASH_*`;
  - `gnovm-worker` is dispatched first in `main()`, before cobra/fang; it exits
    at once unless it receives a handshake (magic + versions) on fd 3 within
    2 s, so neither a stray invocation nor a test binary given that argument
    does anything;
  - App-level tests run the `Host` in-process (`in_process`), and a test
    asserts that `go test` cannot spawn a worker.
- **Worker hygiene**: exec `/proc/self/exe` on Linux (survives a deploy that
  replaced the binary); the handshake compares protocol, klash, gno and stdlib
  versions; `Setpgid`, SIGINT/SIGHUP ignored (Ctrl-C in `klash run` must not
  kill it mid-append); exits on EOF of fd 3; reaped with `cmd.Wait`;
  `KLASH_PROFILE` dropped from its environment.
- **Liveness**: a reader goroutine acknowledges a cancel at once and the meter
  stops the run within ~1 ms of gas; during unmetered phases (base loads, fsync)
  the worker sends heartbeats every 500 ms; the client kills it only after 10 s
  without any frame. Requests queue FIFO; a cancelled queued request is
  dropped. A request whose outcome is unknown after a kill (e.g. killed during
  fsync) is resolved after restart by asking whether its request id is in the
  journal; the tool says "kept" or "not kept" accordingly.
- **One operation at a time** keeps the GnoVM single-threaded (its stores and
  shared stdlib nodes are not safe for concurrent use). One agent's tool calls
  are already sequential, so only *different sessions* in one klash (server
  mode, channels) wait on each other; the default wall limit is 5 s, a queued
  call's own budget starts when it runs, and its result says how long it
  waited. A pool of workers is phase 6.
- **Lifecycle**: the client is refcounted per process (server mode has one App
  per workspace); each App acquires it lazily on first VM use and releases it in
  `Shutdown` right after `Messages.FlushAll`, cancelling only that App's
  requests and dropping its sessions' scratch VMs. The worker is closed (cancel,
  3 s, kill) at refcount zero.

## 4. Shared base store

- In the worker, lazily: `memdb` → `dbadapter` → `gno.NewStore(nil, kv, kv)`,
  `SetNativeResolver(stdlibs.NativeResolver)`, `SetPackageGetter(loadLibrary)`.
- `loadLibrary` serves `stdlibs/` (gno's stdlib, synced from go.mod's gno; the
  VERSION and byte-for-byte reader tests pass) and `klashlibs/`: `fmt` (gno's
  `ufmt` v0 renamed — description notes its `%v` and float limits) and
  `klash/goals/v1` (§12).
- **On demand**: the imports of an input — or of a whole journal before replay —
  are loaded into the base first with `base.GetPackage(path, true)` (required:
  the overlay's cache KV caches misses). The heap delta of each base load is
  measured (single-threaded worker, so it is clean) and charged (§8).

## 5. A VM

- **Name** `^[a-z][a-z0-9_-]{0,31}$`; reserved: `goals`, `scratch-*`.
- **Options**: `memory_mb` (default 16, clamp 1–64), `gas` per call (default
  1e9, max 1e11), `ns_per_gas` (default 1, clamp 0.1–100), `persist`,
  `pinned`, `description`.
- **Resident form**: overlay `tx := base.BeginTransaction(kv, kv, nil, meter)`
  over `cache.New(baseKV)`, `tx.SetPackageGetter(nil)`; the machine sees
  `noEvictStore{tx}` (no-op `GarbageCollectObjectCache`); package `repl`
  registered before `NewMachineWithOptions{Store, Alloc: NewAllocator(cap),
  GasMeter: meter, Context, BoundedPanicRender: true}`. Never `m.Release()` a
  kept VM.
- **Every operation that touches the VM** (parse, pre-scan, run, render, each
  replayed entry) runs between `meter.begin`/`meter.end` with a **fresh**
  preprocess allocator: `pa := NewAllocator(cap); pa.SetGasMeter(meter);
  tx.SetPreprocessAllocator(pa)`, reset to nil after (it never collects, so it
  is per entry, not per replay).
- **ExecContext** per entry: ChainID `klash`, Height = seq, Timestamp from the
  entry's recorded time; `Banker` and `Params` are stubs that fail with "not
  available in klash VMs"; `EventLogger` is a fresh `sdk.NewEventLogger()` per
  entry whose events are dropped (it is a concrete type, not an interface);
  `SessionAccount` is nil.
- **Evaluation** uses klash's own wrapper, `func() any { return (<expr>) }()`,
  never gno's `Eval` wrapper (which shadows `x`); a call expression is evaluated
  as is.

## 6. Running code (`gnovm_run`)

1. **Normalise** (meter armed; input ≤ 256 KB): drop a leading `package`
   clause; split at depth 0 with `go/scanner` into declaration groups (`import`,
   `func`, `type`, `var`/`const` — `var(...)`/`const(...)` kept whole for
   `iota`; `func (` is a method declaration only when followed by a receiver and
   a name, otherwise a statement) and statement runs; a lone trailing expression
   is an evaluation.
2. **Pre-scan** every import into the base.
   (Step 4's `main` renaming renames every identifier `main` in the input, so
   a call to `main()` in the same input still works.)
3. **Parse everything first** (declaration groups with `ParseFile`, statements
   with `ParseStmts`); a parse error reports and nothing has run (no rebuild).
4. **Refuse redeclaration**: each group's names (`GetDeclNames`) against the
   package's existing names — "`helper` is already declared in this VM; assign
   to a variable with `=`, or use a new name; to start over, use a fresh VM".
   If the input declares `func main()`, it is renamed `main_<seq>` and called
   after the declarations, so several programs can run in one VM.
5. **Run in order**: each declaration group as a new file through `RunFiles`
   (forward references and mutual recursion work); statements through
   `RunStatement`; the evaluation last.
6. **Journal the normalised operations**, not the raw text:
   `{"ops":[{"file":…},{"stmts":…},{"call":"main_7"},{"eval":…}]}` — replay
   re-executes these, independent of later changes to normalisation.
   Declarations and statements are always journaled; an evaluation only if it
   contains a non-builtin call (a conversion to a user type counts) or a
   function literal.
7. **Render**: `gno.BoundedSprintTV(tv, nil, 16 KB)` — structure only, no user
   methods; `print` output to a writer that keeps 64 KB and returns
   `len(p), nil`. (Panic messages still call a user `Error()`; metered, capped,
   and the VM is discarded, so harmless.)
8. **Success** = the meter is not tripped (trips are sticky), then append
   (§7), then report. **Failure** (anything after parsing): nothing journaled,
   VM discarded and rebuilt on next use.
9. **Errors** start `ERROR — nothing from this call was kept:` and end with the
   next step: parse/preprocess first line; `panic: <bounded>` + bounded gno
   stack; "out of gas after N (≈T at X ns/gas) — raise gas"; "memory cap
   exceeded (M MB) — raise memory_mb (max 64) or keep less"; wall/cancel
   reason; for unavailable packages (`os`, `sync`, `encoding/json`, `slices`)
   what gno offers instead.

## 7. Persistence and durability

- **Every VM's journal is on disk**, in the global data dir (a cloned repo
  cannot ship VMs or goals, and the agent-writable project `.klash` stays out):
  `<XDG data>/klash/gnovm/<project-key>/` with `vms/<name>/`
  (`machine.json`, `journal.jsonl`, and for `goals` `summary.json`,
  `state.json`), `locks/<name>.lock` (outside the VM directory),
  `.ephemeral/<klash pid>-<start time>/` for non-persistent VMs (keyed by the
  klash process, not the worker, so they survive a worker restart; deleted at
  klash exit; directories whose process is gone are swept at start), `.trash/`.
  The project key is the sanitised path plus 8 hex of its hash.
- **Generation**: `machine.json` and the journal's header line carry a
  generation id (new on create, repair, compaction). A resident VM remembers
  `(generation, seq, offset, hash of the last line)`.
- **Run on a VM** (persistent or not):
  1. `lock.TryFile(locks/<name>.lock)`; if held, answer "busy — VM held by pid
     N" with backoff up to the call's wall budget (no blocking inside the
     worker);
  2. check `fstat(lock fd)` equals `stat(path)`, then the generation and
     last-line hash; any mismatch drops the resident VM and rebuilds;
  3. truncate a torn tail left by a crashed process;
  4. catch up: replay entries past the resident seq (written by another klash),
     with replay limits;
  5. run; if the run raised the VM's largest cap, persist it to `machine.json`
     first;
  6. append: note the size, write one line (with the request id and the run's
     `wall_ns`), `fdatasync`; on a write error truncate back and fsync; on an
     fsync error reopen and mark broken;
  7. unlock.
- **Atomic files**: `machine.json`, `summary.json`, `state.json` via temp,
  fsync, rename, fsync of the directory; new directories fsync their parent.
- **Load** holds the VM lock across reading and replay: `json.Decoder` (lines
  can exceed 64 KB), seq continuity, the header's generation; a torn last line
  is truncated with a warning; any other bad line marks the VM **broken**.
- **Replay**: pre-scan all imports; largest cap ever used; fresh preprocess
  allocator per entry; no per-call gas limit (semantics unchanged); budget =
  Σ recorded `wall_ns` × 3 + 2 s. Over budget → "busy, retry" and the partial
  VM stays resident, so a retry resumes. Never compare replay gas with recorded
  gas.
- **Replaying marker** `{pid, boot id, start}` is written before replay and
  removed after, and on cancel and busy. A stale marker marks the VM broken only
  if its pid died unexplained — the client records the kills it made.
- **Broken VMs** are listed with the error; runs are refused until `/goals
  repair` (or `gnovm_drop delete`). Repair cuts to the last good entry, keeps a
  `.bak`, and starts a new generation.
- **Delete** renames `vms/<name>` into `.trash/<name>-<ts>`; the lock stays in
  `locks/`.
- **Limits**: 64 named VMs per project (scratch VMs exempt: evicted LRU like
  any VM, and their journals deleted after 24 h idle or at klash exit); 8 MB
  journal (refuse with "compact or delete"; `goals` compacts itself, §12);
  256 KB per entry.

## 8. Memory: probe, budget, eviction

- **Probe**: Linux `MemAvailable` bounded by the tightest cgroup v2
  `memory.max − memory.current` (and `memory.high`) up to the root; macOS free
  + speculative + file-backed pages; failure → a fixed 256 MB budget.
- **Budget**: `options.gnovm.memory_budget_mb` (global config only, §13) or
  `min(1 GB, 10% of available)`, never above half of current availability.
- **Accounting** (charged while resident):
  - base: the measured heap delta of each base load (§4);
  - each resident VM: cap + 64 × declaration-source bytes (retained AST) +
    0.3 MB per imported package (decoded copies) + 1 MB;
  - ephemeral journals: their size;
  - one transient reserve of 3 × the largest admitted cap (GC's recursion uses
    about 1.3 B of Go stack per byte of cap — ~128–192 MB at 64 MB, an 11×
    margin under Go's 1 GB stack limit — plus transient copies).
- **Admission** before a VM becomes resident: evict LRU unpinned resident VMs,
  never the one being admitted; else refuse with the numbers and the pinned VMs.
- **Worker self-protection**: `debug.SetMemoryLimit(budget + base)`, RSS
  watchdog exiting above 2 × (budget + base), `oom_score_adj=1000`.
  Recommended on the box (ops): lanes under `systemd-run --scope -p
  MemoryMax=…`.

## 9. Gas

- `callMeter` (meter.go, to be updated): tm2's basicGasMeter rules; refunds
  never below the call's start; per-call limit; polls the wall deadline and the
  context every 1M gas; **trips are sticky** (`tryEvalStatic` and
  `boundedUserSprint` swallow panics; a user `recover()` cannot, since only
  `*Exception` is recoverable).
- Gas counts CPU ops, allocation (calibrated to Go's malloc), object loads and
  stores (the overlay's meter), and preprocessing (the preprocess allocator).
  GC charges after its walk; parsing is charged; base loads and fsync are not
  (they heartbeat instead).
- `ns_per_gas` sets the estimate and the wall limit `clamp(3 × gas ×
  ns_per_gas, 5 s, 10 min)`; results show gas used/limit/estimate; wall and
  host ns/gas go to metadata.

## 10. Tools

| Tool | Parameters | Notes |
|---|---|---|
| `gnovm_run` | `code`, `name?`, `gas?` | no `name`: the session's `scratch-<first 8 hex of SHA-256(session id)>`, created on demand (its first result says "new scratch VM, memory only: it ends with this klash process"); refuses `goals` ("use the goals tool") |
| `gnovm_new` | `name`, `memory_mb?`, `gas?`, `ns_per_gas?`, `persist?`, `pinned?`, `description?` | idempotent: given options must equal, omitted ones match; `persist` may go false→true |
| `gnovm_list` | — | host memory, budget/use, base, every VM (state, cap/use, lifetime gas, entries, persisted, last use), attribution notices |
| `gnovm_drop` | `name`, `action: evict\|delete` | delete asks permission before any lock; refused for `goals` |
| `goals_list` | — | read-only; also the plan agent's |
| `goals` | `action: add\|fact\|note\|check\|close`, then per action: add `id,title,success,check?`; fact `id,key,value`; note `id,text`; check `id`; close `id,status: met\|dropped` | errors name the missing field; one description line on goals vs todos vs `/goal` |

## 11. Tool results and registration

- Numbers decode as JSON numbers (a model's `1e9` works); results: value or
  output first, then `ok · 1.2M gas (≈1.2 ms) · mem 0.3/16 MB · journaled`;
  capped at 30,000 characters with the rest in a file; descriptions ≈1.2 KB in
  total (purpose, mixed input, `fmt` works with its limits, no os/sync/json/
  goroutines, a failed call keeps nothing, gas ≈ ns).
- Built with `fantasy.NewAgentTool` (calls run one at a time, in order).
  Registered in `tooldefs.names` and the two pinned fixtures; plan agent:
  `goals_list`, `gnovm_list`; task sub-agent: none. A renderer shows code and
  result (phase 4).

## 12. Goals

- **Library** `klash/goals/v1` (embedded, versioned, immutable). The `goals`
  VM's journal begins `import goals "klash/goals/v1"; var G = goals.New()`;
  later entries are calls on `G` generated by the `goals` tool with
  `strconv.Quote` escaping; each goal records its **owners** (the session that
  added it). Ownership only decides how the goal is shown (below); any session
  may update any goal. `App.BranchSession` adds the branch as an owner of the
  source session's open goals (`G.Branch(child, parent)`, journaled, only when
  the `goals` VM has such goals), since a branch carries on that conversation.
- **Checks**: a goal's `check` is an expression in a small language parsed with
  `go/parser.ParseExpr` and allowed only if it uses `facts["k"]`, string /
  number / bool literals, comparisons, `&&` `||` `!`, parentheses, `has("k")`,
  `num(facts["k"])`, `len(facts["k"])`. The `goals` tool evaluates it **in Go**
  — no model text ever becomes Gno code — and journals the boolean with
  `G.RecordCheck(id, true|false)`. `Fact` clears the recorded result.
- **Closing**: `met` requires a check evaluated fresh in the same call, if the
  goal has one; a goal without a check closes on the model's word. **Every
  close** (any status) shows a TUI notice; `/goals reopen <id>` undoes it.
  Facts whose key begins `klash.` are reserved for the harness. (An LLM judge
  on close is deferred to phase 6: on the box a judge request outside the prompt
  cache takes the parent's slot and costs minutes — `agent.go:1136-1140`.)
- **Snapshots**: after every successful `goals` operation the worker writes,
  under the VM lock, `summary.json` (owner, status, title, success, facts,
  notes, check and result, updated) and `state.json` (`G.Export()`). Upgrade,
  repair and compaction start a fresh journal (new generation) with `var G =
  goals.Import(<state.json>)`; `goals` compacts itself past 4 MB.
- **Injection** (inert when there are no open goals — the cassettes stay
  valid; off when `options.gnovm.goals` is false):
  - a separate `call.goalsBlock`, appended after the hook block as a hidden
    part in both `createUserMessage` and the live turn (`ToAIMessage` re-renders
    hidden parts in order);
  - a per-session "given" mark on the coordinator, beside `sessionStarts`,
    checked and set atomically at the three `createUserMessage` sites and by
    compaction; `queueContinuation` clears `goalsBlock`; `/rewind` clears the
    mark when it cuts the message that carried the block (record its id, as
    `subdirectoryGiven` does);
  - given (a) at a session's first prompt in this process, (b) in the
    restored-context message after any compaction, (c) on the next typed prompt
    when *this session's* rendered block differs from what it last received
    (hash refreshed after the session's own goals calls);
  - rendering by klash from `summary.json`: `<goals as-of="…">`, this session's
    goals in full (open first, newest first) up to 1.5 KB then "…and N more
    (goals_list)", other sessions' open goals and unowned ones (deleted
    sessions) as one count line; `</goals>` in model text escaped; notes and
    fact values labelled "recorded claims", titles and conditions not
    (they are the goals).
  - The goals tool description, plus two lines in `coder.md.tpl` behind a
    template flag (off in the cassette config), tell the model goals exist.
- **`/goals`** TUI command: list, `close <id> --force`, `reopen <id>`, `repair`.

## 13. Configuration

`options.gnovm` (pointer struct; klashrc `option gnovm …` special-cased like
`option ui`): `memory_budget_mb` (0 = auto), `default_memory_mb` (16),
`default_gas` (1e9), `ns_per_gas` (1), `goals` (true: the goals tools, the
injection and the template flag together), `in_process` (false). Numbers are
float64. `memory_budget_mb` and `in_process` are honoured only from the global
config; a project config may lower the budget, not raise it. The worker starts
lazily. `schema.json`, `docs/config/README.md` and the klash-config skill are
updated.

## 14. Licensing and attribution

- `LICENSE.md`: GNGPL v6 as published by NewTendermint, LLC or its successor,
  or any later version the Licensor, as the proxy under §14 of the License,
  publicly accepts; copyright Jae Kwon; a standing temporary Strong
  Attribution clause with "Licensor" defined (Jae Kwon, his written designee, or
  his successors), one change mechanism (a dated Designation in the canonical
  copy in github.com/klash-pro/klash, applying to versions the Licensor first
  publishes after it), the "conclusively govern" sentence tied to the
  Designation in the recipient's copy, and a severability sentence for the parts
  that reach beyond §7(g). Every new source file's header repeats the proxy
  wording.
- **Known interactions**, stated plainly: the klash binary is a combined work
  and the GNGPL admits no further restrictions, which the FSL's Competing Use
  clause is — already true today through x402's link to gnovm; the worker is the
  same binary, so it gives fault isolation, not licence separation;
  CONTRIBUTING.md has an `internal/gnovm/` exception (contributions under this
  license; the separate grants to Charm do not extend to it, the Maintainer's
  do); tm2 is Apache-2.0; Go's BSD code ships with `stdlibs/LICENSE-GO`; GNGPL
  §13 asks that remote users of a modified version (server mode, channels) be
  offered the source, so klash's remote interfaces link
  github.com/klash-pro/klash (phase 4, with the notices).
- **Attribution notices**: in the TUI itself (sidebar footer, OSC 8 links), in
  `klash --version`, in the README and on klash.pro's pages that promote klash
  (both clauses cover promotional material): "built on gno.land"
  (NewTendermint's, already owed via x402) and this subsystem's Designation.
- **Timing**: landing phase 1 pushes main to the public repository, which
  publishes the code under these terms. Jae has said to prioritise the GNGPL
  and settle conflicts later, so it lands under them; the report to him names
  the conflict and the options (linking exceptions from NewTendermint and the
  Licensor, a change to klash's license, or removing the link).

## 15. Failure modes

| Risk | Mitigation |
|---|---|
| Go fatal in the VM | worker isolation; caps ≤ 64 MB bound GC depth (11× margin) |
| A test spawns a worker (fork bomb) | `testing.Testing()` refusal; fd-3 handshake; in-process tests; a test that proves it |
| Constant-folding blowup | fresh capped, metered preprocess allocator per entry |
| Heap beyond the caps | accounting incl. AST and imports, transient reserve, worker memory limit, RSS watchdog, `oom_score_adj` |
| OOM killer picks llama-server | `oom_score_adj=1000`; systemd `MemoryMax` for lanes (ops) |
| Two klash on one VM; delete/repair/compaction races | TryFile lock outside the VM dir, inode check, generation + last-line hash, torn-tail truncation, catch-up |
| Worker killed (cancel, watchdog, OOM, deploy) | journals on disk; replay on demand; outcome resolved by request id |
| Killed mid-replay | marker with pid/boot/start; broken only on unexplained death; resume on retry |
| GC re-decodes live stdlib objects | `noEvictStore` |
| Rendering runs user code | `BoundedSprintTV(…, nil, …)` |
| Swallowed meter trips | sticky trips |
| `x` shadowed by gno's `Eval` | klash's own wrapper |
| Silent redeclaration | explicit refusal |
| Prelude/library upgrade | versioned path; `state.json` rebase |
| Check strings as code injection | whitelisted check language evaluated in Go; `gnovm_run` refuses `goals` |
| Goals as an injection channel | global dir; fenced, scoped, labelled, capped |
| Server mode: one App closes the shared worker | refcounted client |
| One long run blocks other sessions | per-request wall cap; queue with cancel; "busy" answers, never a blocking lock in the worker |
| Version skew after deploy | `/proc/self/exe`; handshake versions |
| Ctrl-C kills the worker mid-append | `Setpgid`, SIGINT ignored |
| Stale embedded stdlibs | VERSION test |

## 16. Tests

- **Unit on `Host` in-process**: meter (limits, deadline, cancel, sticky,
  refund floor); journal (append, truncate-back, torn tail, seq gap, generation,
  corrupt line, marker semantics); normalisation (package clause, mixed chunks,
  `main` first, mutual recursion, method before type, `const (iota)`, `func (`
  disambiguation, second `package main`, redeclaration refused, `x := 1` then
  `x`); the journaling rule; structural rendering; import errors; rebuild after
  every failure kind; preprocess cap (bigint squaring, string-constant
  doubling); budget, eviction, pinned, refusal, base charging; host probe
  fixtures; catch-up with two `Host`s on one dir, including the other repairing,
  deleting and recreating, or leaving a torn tail; a 50,000-entry journal
  replayed at a 1 MB cap; an over-budget replay resuming on retry.
- **Differential**: random workloads at 1–2 MB caps comparing
  `BoundedSprintTV(nil)` dumps of every repl global live vs replayed, including
  rebuilds midway.
- **Goals**: library (add, fact, check, close, refusals, export/import);
  check-language whitelist (hostile inputs rejected); stale check after `fact`;
  ownership; summary and state snapshots; compaction rebase.
- **IPC and lifecycle**: protocol over an in-process pipe pair (no exec); cancel
  ack; heartbeat timeout; outcome resolution by request id; two Apps sharing a
  client, one App's Shutdown not killing the worker; **`go test` cannot spawn a
  worker**.
- **Worker integration (devbox, explicit, `systemd-run --scope -p
  TasksMax=64`)**: build klash; a kill during fdatasync reported as kept; an
  ephemeral VM surviving a worker restart; a cancel-kill mid-replay not marking
  broken; a 64 MB `&q` chain under GC with RSS measured; the long-goal script of
  (A) run once through the real worker.
- **Agent**: no injection without goals (cassettes unchanged); injection at (a),
  (b), (c), never twice, on hidden prompts, cleared by continuations, restored
  after `/rewind`; the stored message for turn N is byte-identical in request
  N+1 to what was sent live; queued prompts.
- **Long-goal end to end**:
  - (A) deterministic, in CI: a scripted model works a multi-step goal with a
    check over many turns; stale and hostile checks; a premature close refused;
    a second session's goals shown as a count; a second `Host` changing goals
    triggers (c); compaction; then **the client singleton is closed and the App
    rebuilt**, and the test asserts that a replay happened and the summary is
    injected.
  - (B) real model on the AI box (isolated XDG and project, quiet time), at
    least three runs: a multi-batch task interrupted by `/compact` and a klash
    restart, with a neutral "continue" after the restart; recorded: tool-call
    errors, check parse failures, refusals, whether the goal is resumed and
    closed as met.

## 17. Phases

1. Core `Host` (base and libraries, VM, normalisation, meter, journal and
   durability, budget, locks); unit and differential tests.
2. Worker, IPC, refcounted client, shutdown wiring; IPC tests; worker
   integration test.
3. Tools, goals library and tools, summary/state snapshots, injection, `/goals`;
   agent tests.
4. Licensing files (CONTRIBUTING exception, headers), TUI attribution footer,
   `--version`, README, config, docs, schema, renderer.
5. End to end (A) and (B); land on box main; deploy; memory notes.
6. LLM judge on close via a cache-friendly request; re-arm `/goal` after a
   restart; a cross-process budget ledger.

## 18. Open questions for Jae

- The attribution designation for this subsystem (placeholder: https://klash.pro,
  "built on klash") and NewTendermint's for the GnoVM notice (assumed
  https://gno.land, "built on gno.land").
- The binary-level licence conflict (§14), named before the first landing.

---

## Appendix A. Review round 1: findings and dispositions

Three independent reviewers (GnoVM correctness; klash integration and UX;
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
| S3 | A worker kill loses memory-only VMs and misreports outcomes | yes | accepted differently: every journal is on disk (ephemeral ones per klash process); outcomes resolved by request id (§3, §7) |
| S4 | Replay can fail or never finish (preprocess allocator never collects; budget from gas) | yes: alloc.go:409; store.go:177-191 | accepted: fresh preprocess allocator per entry; budget from recorded `wall_ns`; the partial VM stays resident (§5, §7) |
| S5 | Catch-up trusts sequence numbers alone (delete renames the lock's dir; repair; torn tail) | yes: lock.go:12-15 | accepted: locks outside the VM dir, inode check, generation and last-line hash, torn tail truncated, raised cap persisted first (§7) |
| S6 | The `replaying` marker brands healthy VMs broken | design | accepted: lock held across load and replay; marker with pid, boot id, start; broken only on an unexplained death (§7) |
| S7 | One App's Shutdown kills the process-wide worker (server mode) | yes: app.go:1296; backend.go:255-270 | accepted: refcounted client, lazy spawn (§3) |
| S8 | One-at-a-time worker blocks every session; spurious kills during base loads and fsync | yes: lock_unix.go:20-35 | accepted: reader goroutine, cancel ack, heartbeats, `TryFile` "busy"; the remaining cross-session wait is stated and bounded by the 5 s default, a worker pool is phase 6 (§3) |
| S9 | Worker hygiene: version skew, Ctrl-C, framing, orphans; §15 wording on `Error()` | yes: root.go:310-315; op_call.go:912-916 | accepted (§3, §6 step 7) |
| S10 | Memory: GC stack, retained ASTs, pinned stdlib objects, base growth | estimate | accepted: base charged per load, ASTs and imports charged, transient reserve; RSS measured on the devbox (§8, §16) |
| G1 | Close-as-met bypassed by code in a check; stale checks; `gnovm_run` can write `goals` | yes | accepted, stronger: checks are a whitelisted language evaluated in Go, so no check text becomes Gno code; `Fact` clears; a fresh check at close; `gnovm_run` refuses `goals`; `klash.` prefix reserved; every close shows a notice (§12) |
| G2 | The close judge costs minutes on the box and cannot see older evidence | yes: agent.go:1136-1140; goal.go:36 | **deferred to phase 6** (asked the way compaction asks, to hit the prompt cache); until then checks, the notice and `/goals reopen` guard closes |
| G3 | Test (A) can pass without any replay | yes | accepted: singleton closed between Apps, replay asserted, stored vs live message byte-identical, more cases, a real-worker run, ≥3 real-model runs (§16) |
| G4 | Injection: (c) re-fires, continuations re-send, `/rewind` loses the block, ownership unrecorded | yes: agent.go:569-572; coordinator.go:454-472 | accepted, plus branch lineage (§12) |
| G5 | Licence notes overclaim the worker, the CONTRIBUTING exception does not exist, §13 offer and promotional material missing, timing | yes: CONTRIBUTING.md:5-7, 38-42, 98-100 | accepted: claim dropped, exception written, source offer, README and klash.pro, decided before phase 1 lands (§14) |
| G6 | Drafting holes: headers, publisher, proxy, canonical copy, prospective rule, "conclusively govern", severability | yes | accepted (LICENSE.md, §14) |
| G7 | Scratch VMs vanish on a worker restart, are never cleaned up, and their names break the 32-character rule | yes: session.go:114 | accepted: on-disk ephemeral journals, hashed names, LRU, exempt from the VM cap, "new scratch VM" notice (§7, §10) |
| G8 | The `goals` tool's shape is hard for a 27B model | design | accepted: `goals_list` and `goals`, per-action fields, `num`/`has`, one line on goals vs todos vs `/goal` (§10) |
| G9 | The library upgrade path cannot be carried out | design | accepted: `state.json` after every operation; upgrade, repair and compaction rebase from it in phase 3 (§12) |
| G10 | A project config can raise the budget or set `in_process`; float decoding; three switches | yes: config/load.go:989-1007 | accepted (§13) |

Not adopted: a separate worker binary (it buys no licence separation while
x402 links the GnoVM into klash anyway, §14); a cross-process memory ledger
(phase 6, as in round 1).
