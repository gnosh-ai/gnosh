# GnoVM worker protocol

This is the on-the-wire contract between a gnosh process and the GnoVM
worker it spawns. The implementation is `internal/gnovm/api` (framing and
types) and `internal/gnovm` (client, worker, host). It is already in
production as fault isolation: the worker is still the same binary
(`gnosh gnovm-worker`). This document does not change the protocol.

A design overview, including why there is a worker at all, lives in
[DESIGN.md](DESIGN.md) §3.

## Framing

Messages are **not** JSON-RPC and do not travel on stdio. Each message is
a frame: a 4-byte big-endian length, then that many bytes of JSON, with a
maximum of 16 MiB (`api.MaxFrame`). Frames go on two pipes the client
hands the worker as **fd 3 (requests)** and **fd 4 (replies)**
(`exec.Cmd.ExtraFiles`). The worker's stdout is discarded; stderr is a
bounded `worker.log` in the data directory. The extra fds exist so
nothing the worker prints can corrupt the protocol.

The JSON envelope (`api.Frame`) is:

| Field | Meaning |
|---|---|
| `id` | Request id; replies, acks, heartbeats and cancels carry the same id |
| `type` | Message type (below) |
| `body` | Type-specific JSON, omitted when empty |
| `err` | A `WireError` when the request failed |
| `progress` | Heartbeat count within a request, so a stuck worker and a slow one look different |
| `waiting` | Set on a heartbeat sent while the request waits for mem's lock, which another gnosh process holds |

## Handshake and versioning

Magic string: `gnosh-gnovm`. Protocol version: `1`.

The client execs the worker (`gnosh gnovm-worker`; on Linux,
`/proc/self/exe`) and immediately writes a `hello` frame on fd 3. The
worker exits unless a valid hello arrives within **2 seconds**. It
refuses a hello whose magic is wrong, or whose protocol, gnosh build id,
or gno/stdlib version differs from its own (it replies with an error
naming both sides, then exits). On agreement it answers with its own
`hello` and builds a `Host` from the client's fields.

The client's hello also carries:

- `dir` — data directory (`<XDG data>/gnosh/gnovm`)
- `client` — id of this gnosh process, for its memory-only VMs
- `budget_bytes`, `default_memory_mb`, `default_gas`, `ns_per_gas`
- `embeddings` — optional OpenAI-compatible base URL for mem's vectors

Windows cannot pass the extra fds; the client runs the host in-process
and never starts a worker.

Tests must not spawn their own test binary as the worker (a re-exec
caused a fork bomb). They set `ClientOptions.InProcess` or name another
binary.

## Message types

Requests the client may send:

| Type | Body | Result body |
|---|---|---|
| `hello` | `Hello` | `Hello` |
| `run` | `RunRequest` | `WireRun` (`RunResult` plus `run_err`, so the error keeps its kind) |
| `new` | `NewRequest` | `{info, created}` |
| `list` | `{project}` | `ListResult` |
| `drop` | `{project, name, action}` | empty |
| `kept` | `{project, name, req, frames?}` | a bool, or `{kept, frames}` when `frames` is set (mem) |
| `repair` | `{project, name}` | `{kept, cut}` |
| `mem` | `{project, session, args, titles}` | `{text, erased}` |
| `goal` | `{op, project, session, …}` | `{binding, turn, text, closed}` |
| `cancel` | none | an `ack` |

The worker may also send `result` (the reply to a request), `ack` (a
cancel was received), and `heartbeat`. Unknown request types fail.

`WireError` carries `kind` (`run`, `refusal`, `busy`, `parse`, `limit`,
`compile`, `panic`, `broken`, or `other`), `msg`, and optional `again`,
`limit`, `stack`, and a nested `inner`. The client rebuilds the same
error types the host would have returned.

## Lifecycle

One refcounted, process-wide `Client` (`Acquire` / `Release`), wrapped
by `Lazy` so a gnosh that never runs a VM never starts a worker. The
first request spawns the worker; if the worker dies, the next request
spawns another. The last `Release` closes the connection and deletes
this process's memory-only VMs.

The host runs **one VM operation at a time**. Persistent VMs, including
mem, take a cross-process file lock. Two gnosh processes on the same
project already serialise mem; `waiting` heartbeats cover the wait.

A cancel is a `cancel` frame for a running request id. The worker's
reader goroutine takes it at once (it is not queued behind the running
call) and cancels that call's context. The client may await the reply
even after its own context ends, so a cancelled mem call that was
already journaled is not lost.

The worker ignores SIGINT and SIGHUP so a Ctrl-C in gnosh cannot kill it
in the middle of a journal append; gnosh stops it by closing the request
pipe. On Unix it raises `oom_score_adj` to 1000, sets a Go memory limit
from the budget, and an RSS watchdog exits it if it grows well past
that.

## Heartbeats and timeouts

While a request runs, the worker sends a `heartbeat` every **500 ms**,
including during metered runs, journal replay, and base-store load.

The client kills the worker if:

- it is silent (no heartbeat and no reply) for **10 seconds**, or
- a request runs **30 seconds** past its deadline.

Each request is bounded by a **15 minute** deadline. When a heartbeat
has `waiting` set, the client restarts that request's deadline, so a
lock held by another process is not taken for a stuck worker.

## Crash and Kept semantics

Journals are on disk. A killed or crashed worker loses in-RAM VMs but
not persistent state; the next worker replays the journal on demand.

A `run` whose worker died before the reply is a `workerDied` error: the
outcome is unknown until it is asked again. The client can call `kept`
(and `KeptFrames` for mem) with the original request id. Tools already
recover a mem call that way (`memOutcome`).

A `busy` error may set `again`; the client retries those calls itself,
including a mem catch-up that spent its slice, so the model does not
retry them.
