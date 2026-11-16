# The gnosh bench

Four coding tasks, each with a check of its own, run through gnosh against a
model server, with a small proxy in between that counts what every request
cost. One run gives one row: passed or not, how long, how many requests, how
many tokens.

**The gnosh in this repository was first measured on the night of 13 to 14
November 2026**, on the home AI box: 39 runs of the four tasks, under "The
first measurement" below. The bench was built for the earlier gnosh (the fork
of Charm's Crush that this repository replaces) and measured it, Charm's
crush and OpenAI's Codex CLI in November 2026. It was carried over on 13
November 2026 (`CARRIED.md`, "The bench") and its runners now drive the new
gnosh. Every number outside "The first measurement" is the earlier
programs'.

## What it measures, and what it does not

Per run: whether the task's own check passed (and its partial score); wall
time; model requests; prompt tokens sent; tokens the server actually had to
read (prompt minus what its prefix cache served); tokens generated; the
peak context; compactions (summaries); and, for gnosh, slides, todos popped
and calls to mem.

It does not measure how good the code is beyond the check, or anything about
an interactive session: every run is `gnosh -p`, one prompt, nobody
watching. Four small Go tasks are not a sample of real work. A run varies a
lot from one time to the next on the same machine (the same build took 43
and 57 minutes on one task), so a difference needs several runs behind it.
And the numbers of two harnesses can be set side by side only when model,
server, window and load were the same.

## The four tasks

Each is a small Go repository built by a script, a prompt given as it is, and
a check (see [tasks/README.md](tasks/README.md)).

- **rename**: rename a project from tallyho to quokka across 15 files:
  module, package, identifiers, paths, the environment variable, Makefile,
  scripts, CI, docs.
- **algos**: implement nine small packages whose functions panic with
  "TODO", so that their tests pass. The long one: its context outgrows a
  small window.
- **bugs**: find and fix six seeded bugs, one per package, each caught by a
  failing test, without touching the tests.
- **continue**: two sessions. The first is told to make changes 1 and 2 of a
  list of six and stop; the second is told only "Finish the remaining
  changes from before."

## How a run is scored

`run.sh` builds the task's repository in a new directory, starts a proxy of
its own, gives the prompt to one `gnosh -p` (two for continue, the second
with `--continue`), and then runs the task's `check.sh` on what was left.
The check exits 0 or 1 and prints one line saying why: "PASS: 9/9 packages
pass, test files untouched", "FAIL: 4/6 bugs fixed; still failing: tax
receipt". It looks only at results: every test must pass, the test files
must be the originals byte for byte (algos, bugs) or carry every test of a
clean rename, and continue runs one hidden test per change. `passed` is the
check's verdict and nothing else.

A run has one of three outcomes. **passed**: the check passed. **failed**: the
model stopped by itself and the check did not pass. **stopped**: a cap ended
the run (its wall time, or its turns when `MAX_TURNS` is set) and the check
did not pass; that says how far the run had come, not that the model failed,
and the tables count it apart.

Everything else in the row is counted, not judged: by the proxy (requests,
tokens, time), by gnosh itself (what each session printed: its result
object), and from gnosh's session files.

## The pieces

- `proxy.py` sits between a harness and the model server, one per run. It
  pins every request to the server's model, answers `/models` itself so
  every harness is told the same context window, forwards only chat
  completions and responses, and logs one line per request: what was sent,
  what was cached, what was read and generated, and whether it was a
  summary. With `--reasoning-effort` it puts the same effort on every
  request of every harness, as a local server's default would.
- `run.sh` runs one harness on one task once and writes one JSON row.
  Harnesses: `gnosh`; `gnosh-full` and `gnosh-nomem`, the same binary run
  two other ways (below); `gnosh-swarm` (several gnosh sessions on one plan);
  and, as the earlier bench had them, `crush` (Charm's) and `codex`
  (OpenAI's Codex CLI). Each run has its own home folder, telemetry and
  update checks off, and no keys.
- `matrix.sh` runs several harnesses side by side, task by task, so any
  load on the server falls on all of them alike.
- `tasks/` holds the tasks (`tasks/selftest.sh` checks that each check fails
  on the task and passes on a solved copy).
- `serverfacts.py` asks a model server what it holds and how large a context
  it gives, in the way that cannot make it load or unload a model, and is
  the gate that says a run may start.
- `sessions.py` reads what gnosh leaves in a run directory: its session
  files and what each session printed.
- `rescore.py`, `table.py`, `compare.py` recount rows from each run's own
  logs and make the tables; `analyze.py` counts the tools each model
  called, from each harness's own session store; `memerrors.py` sorts
  gnosh's mem calls that failed.
- `cachetest.py` asks whether a user message in the middle of a turn makes
  the server re-read the turn, for a given model's chat template;
  `reads.py` and `prefixdiff.py` show where a run's uncached reads come from.
- `streams.py` measures the server's total speed with 1 to 8 requests at
  once, which is what a swarm runs on. `rounds.py` lists each run's time by
  round and the median.
- `dryrun.sh`, with `dryrun.py`, is the dry run.
- `RESULTS.md` is the earlier gnosh's results, as they were written.

## What a run can be told

All of it is environment for `run.sh` (its header says the same, and a few
switches more).

| what | how | default |
|---|---|---|
| The server and the model | `UPSTREAM` (its address), `MODEL` (the one model every request names) | none: both must be given |
| How gnosh speaks to it | `PROVIDER_TYPE`: `llamacpp`, or `openai-compatible` for vLLM and the like | `llamacpp` |
| Thinking | `THINKING=on` or `off`, for a model that thinks or does not; `REASONING=low`, `medium` or `high` for one with levels; `SERVER_REASONING` makes the proxy put a level on every request | gnosh's own default, which is on |
| The window | `WINDOW`: what gnosh is told the model's context is. `SERVER_WINDOW`: what the server really gives a slot, when the server cannot be asked | 65536 |
| mem | `MEM=off`, or the harness name `gnosh-nomem`: the machine tools are disallowed, so the model has no mem and no frames, and gnosh sends neither its text about mem nor its line about it. This is the plain-harness baseline | on |
| The text about mem | `MEMORY_TEXT=full`, or the harness name `gnosh-full`: the fuller of gnosh's two texts (`internal/prompt/doc.go`, "The two texts about mem") | lean |
| A slot | `SLOT=n` pins every request of the run to one llama.cpp slot; `SIDE_SLOT=m` sends gnosh's short side requests to another | no pin |
| Repetitions | the second argument of `matrix.sh` | |
| A cap on wall time | `TIMEOUT`: seconds a session may take. `RUN_TIMEOUT`: seconds for the whole run | 3600 a session |
| A cap on turns | `MAX_TURNS` | none |

How each reaches gnosh: every run has a home of its own (`HOME` and the four
`XDG_*` directories inside the run directory), so its config, its sessions
and its mem are its own and no run sees another's. The config written there
names one model with its window and turns `listModels` off:

    {
      "providers": {
        "box": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:<the proxy>", "listModels": false}
      },
      "models": {
        "box/<MODEL>": {"contextWindow": <WINDOW>}
      },
      "model": {"id": "box/<MODEL>"}
    }

so gnosh asks the server for no list and can name no other model. A slot is
the provider's `"extraBody": {"id_slot": n}`; thinking is `"thinking"` beside
the `"id"` of `"model"`; the fuller text about mem is put in the home as the
user's own text `prompts/memory.md`; and a session is

    gnosh -p -y --cwd <the task's folder> --output-format stream-json \
        -m box/<MODEL> --small-model box/<MODEL> -- "<the prompt>"

with `--disallowedTools=gnovm_run,gnovm_new,gnovm_list,gnovm_drop` for
`MEM=off`, `--max-turns` for `MAX_TURNS`, and `--continue` for the second
session.

## A row

`run.sh` appends one JSON object to `$RESULTS` and keeps the run's directory
under `$RUNS`. What the earlier rows had: `lane`, `model`, `window`,
`harness`, `version`, `task`, `rep`, `passed`, `check_msg`, `wall_s`,
`timed_out`, `exit_codes`, `dir`, `check1` (the check between the two
sessions), and the proxy's counts: `requests`, `prompt_tokens`,
`cached_tokens`, `read_tokens`, `generated`, `gpu_ms`, `peak_context`,
`summaries`, `over_window`, `tool_calls`, `errors`. New:

- `outcome` (passed, failed, stopped) and `stopped_by` ("wall cap", "turn cap").
- `mem_on`, `memory_text`, `thinking`, `slot`, `provider_type`: how gnosh was
  run.
- `window_believed`: the window gnosh itself says the model has (its result
  object). `window_server`: what the server said of itself, and
  `window_server_from` where it said so. `window_server_stated`: what
  `SERVER_WINDOW` said. `window_true`: whether the window told is the
  server's. `over_window` counts the requests that grew past the window
  told. Together they answer the question the earlier 32K run left open
  (`spec/gnosh/open-questions.md`, Q3): was the window gnosh believed the
  server's, and did the context stay under it?
- `session_ids`, `results` (how each session ended: `success`,
  `error_max_turns`, or nothing for one that was stopped), `num_turns`
  (gnosh's own count of the model's turns), `gnosh_prompt_tokens` and
  `gnosh_generated` (gnosh's own count over every request, which should be
  the proxy's), `tools_offered`. A session that was stopped prints no
  result, so for it these are empty and the proxy's counts are all there
  is.
- From `rescore.py`: `tools` (calls by name), `gnovm_runs`, `mem` (mem's
  calls by name), `pops`, `slides`, `summary_messages`, `subagents`.

## The earlier results

Not one of these is the new gnosh's. "gnosh" in this table is the earlier
program, at the commit of its own repository given beside it; crush is
Charm's v0.100.0 and Codex is OpenAI's Codex CLI 0.167.0. They are as
[RESULTS.md](RESULTS.md) (carried) and Jae's write-ups of the three nights
record them (`BENCH.md`, `BENCH-SWARM.md`, `BENCH-HF.md` in his notes); a
dash is a number those do not give. Tokens are read / generated: what the
server had to read fresh, and what it wrote.

| when, where | program | model | window told (the server's) | thinking | task | passed | wall | requests | tokens, or what else is on record |
|---|---|---|---|---|---|---|---|---|---|
| 6 Nov 2026, the home AI box, llama.cpp, side by side, one slot each: round 1 | crush 0.100.0 | Qwen 3.8 27B | 65,536 (about 262,000 a slot) | on, the model's default | bugs | 1/1 | 7:23 | 9 | 22K / 4.0K |
| | gnosh `7d508804` | | | | bugs | 1/1 | 27:47 | 65 | 68K / 18.7K |
| | crush | | | | algos | 1/1 | 34:19 | 9 | 32K / 15.5K |
| | gnosh `7d508804` | | | | algos | 1/1 | 43:12 | 38 | 70K / 22.1K |
| | crush | | | | rename | 1/1 | 6:32 | 10 | 17K / 4.2K |
| | gnosh `7d508804` | | | | rename | 1/1 | 13:44 | 27 | 27K / 9.5K |
| | crush | | | | continue | 1/1 | 29:16 | 40 | 43K / 13.3K |
| | gnosh `7d508804` | | | | continue | 1/1 | 41:58 | 51 | 62K / 23.0K |
| 6 to 7 Nov, the same: round 2, the box shared with other work at times | crush | Qwen 3.8 27B | 65,536 | on | bugs | 1/1 | 6:46 | 6 | 20K / 2.8K |
| | gnosh `8ab01eca` | | | | bugs | 1/1 | 27:17 | 33 | 87K / 9.8K |
| | crush | | | | algos | 1/1 | 21:54 | 12 | — |
| | gnosh `7d508804` | | | | algos | 1/1 | 56:50 | 30 | — / 34K |
| | crush | | | | rename | 1/1 | 15:28 | 13 | — |
| | gnosh `8ab01eca` | | | | rename | 1/1 | 43:50 | 34 | 50K / — |
| | crush | | | | continue | 1/1 | 19:22 | — | — |
| | gnosh `8ab01eca` | | | | continue | 1/1 | 35:15 | — | — |
| 7 Nov 00:32 UTC, the same box, shared | crush | Qwen 3.8 27B | 32,768 (about 262,000 a slot) | on | algos | 0/1, stopped at the cap | 1 h, the cap | 15 | 2 summaries |
| | gnosh `8ab01eca` | | | | algos | 0/1, stopped at the cap | 1 h, the cap | 15 | 4 slides; peak 40.5K, past the window told |
| 7 Nov 04:04 UTC, the same box, quiet | crush | | | | algos | 0/1, stopped at the cap | 1 h, the cap | 12 | 1 summary, which took 20 min |
| | gnosh, a prototype (later `fb35b81`) | | | | algos | 1/1 | 54:10 | 45 | 6 slides; peak 33.7K |
| 10 to 11 Nov, a rented RTX 6000 Ada, vLLM, each harness alone on it | gnosh `d06172a`, `818c7bc` | Qwen 3.8 27B FP8 | not recorded (131,072) | effort medium, put on every request by the proxy | algos | 4/4 | 794 s median | 23 to 74 | — / 22.4K and 36.9K in two of the four |
| | crush | | | | algos | 4/4 | 448 s | 11 to 13 | — / 12.3K median |
| | gnosh, six sessions on one plan | | | | algos | 9/9 | 500 s | 95 to 129 | — / 34.8K median |
| | gnosh, three sessions | | | | algos | 2/2 | 623 s | 105 to 107 | — |
| | Codex 0.167.0 | | | | algos | 2/2 | 934 s | 7 to 21 | — |
| | crush | | | | bugs | 2/2 | 129 s | — | — |
| | gnosh, six sessions | | | | bugs | 2/2 | 196 s | — | — |
| | gnosh `d06172a` | | | | bugs | 2/2 | 314 s | — | — |
| | gnosh `5b9ce3e`, before a fix | | | | bugs | 2/2 | 738 s | — | — |
| 6 Nov, a rented H100, vLLM 0.33.0, three side by side, three rounds | gnosh `7d508804` | gpt-oss-120b | 65,536 (131,072) | as each harness sent it | all four | 10/12 | 84 s median | 40.5 median | 23K / 9.9K |
| | crush | | | | all four | 9/12 | 88 s | 35 | 27K / 9.0K |
| | Codex | | | | all four | 5/12 | 76 s | 26 | 18K / 8.5K |
| the same, two rounds | gnosh `7d508804` | GLM-4.7-Flash | 65,536 (131,072) | as sent | all four | 6/8 | 211 s | 69 | 47K / 20K |
| | crush | | | | all four | 5/8 | 168 s | 56.5 | 20K / 15K |
| | Codex | | | | all four | 3/8 | 194 s | 79.5 | 19K / 20K |
| the same, two rounds | gnosh `7d508804` | Gemma 4 31B | 65,536 (98,304) | as sent | all four | 7/8 | 208 s | 71 | 84K / 6.8K |
| | crush | | | | all four | 8/8 | 162 s | 34.5 | 27K / 5.2K |
| | Codex | | | | all four | 5/8 | 221 s | 27 | 20K / 4.7K |
| the same, two rounds | gnosh `7d508804` | gpt-oss-120b | 32,768 (131,072) | as sent | rename, algos, continue | 4/6 | 135 s | 89.5 | 68K / 16K |
| | crush | | | | the same three | 5/6 | 109 s | 75.5 | 65K / 13K |
| | Codex | | | | the same three | 2/6 | 64 s | 37.5 | 19K / 7.1K |

Notes on reading it:

- Every run of the four tasks that is on record had thinking on. There is no
  earlier number with thinking off.
- Not one of the "32K" runs had a server whose window was 32K: the harnesses
  were told 32,768 by the proxy and the server would have taken far more.
  That is how the earlier gnosh could send 40.5K and get an answer.
- On the H100 a session had 15 minutes (30 for GLM's first round of rename
  and algos), and a run cut off there was counted as failed. By the rule
  above it was stopped.
- Codex failed mostly for want of a patch tool on those servers
  (RESULTS.md says why), not for want of ability.
- What the earlier gnosh sent before the first word of a task, by BENCH.md:
  a system prompt of 31,087 characters and 29 tools whose descriptions came
  to 33,187 (crush: 22,786 and 26 tools, 28,548). The dry run shows what the
  new gnosh sends, as the proxy counts it: 8,927 characters and 12 tools of
  19,174 with mem (9,946 with the fuller text), and 2,972 and 8 tools of
  15,045 without. Those are characters. What they are in tokens, and what
  they do to time, only a run can say.

## Running it for real

The commands of this section were first run against a real server on 13
November 2026, on the home AI box, as the user `crush` (the `bench` user of
the earlier runs could not be reached). They are the dry run's own, with a
real server's address where the scripted one's was.

A run needs: this tree; a gnosh binary built from the commit to be measured;
Go (the tasks are Go, the checks run `go test`, and so will the model);
python3, git and GNU `timeout`; and a model server that speaks chat
completions. Build gnosh from a checkout of the commit, so that the build
names it and every row says which build it was (or build it elsewhere for
the machine and copy it over; the machine needs Go all the same):

```bash
cd ~/gnosh        # this repository, at the commit to be measured
go build -o ~/bin/gnosh . && ~/bin/gnosh --version
bench/dryrun.sh   # three minutes, no model: the plumbing works on this machine
```

A tree that is not a checkout (a copy, an archive) has no commit for the
build to name. Give it one:

```bash
go build -ldflags "-X github.com/gnosh-ai/gnosh/internal/buildinfo.BuildID=NAME" -o ~/bin/gnosh .
```

### On a machine with a llama.cpp server of its own

This is the home AI box, or any machine whose llama.cpp server is already
serving the model. Three things matter there besides the bench: the server
holds one model at a time and loads whatever a request names; each slot
remembers one conversation; and other sessions may be using it
(`spec/gnosh/harness-local-models.md`).

**1. A user and a home of the bench's own.** The earlier runs were made as a
user named `bench` with no keys and no config of anyone's. `run.sh` gives
every run a home inside its run directory (`HOME` and the `XDG_*`
directories, with `env -i`), so nothing it does reaches the home it was
started from; a separate user keeps the run directories and the Go caches
apart as well.

**2. The lane's settings**, in a file:

```bash
cat > ~/aibox-64k.env <<'EOF'
export LANE=aibox-qwen-64k UPSTREAM=http://127.0.0.1:8080 MODEL=qwen3.8-27b
export WINDOW=65536 SERVER_WINDOW=262144 THINKING=on
export GNOSH_BIN=$HOME/bin/gnosh GO=/usr/local/go/bin/go
export TIMEOUT=3600 RUNS=$HOME/runs RESULTS=$HOME/results-aibox.jsonl
export HARNESSES="${HARNESSES:-gnosh gnosh-full}"
export MATRIX_GATE="python3 $HOME/gnosh/bench/serverfacts.py --gate --idle-slots 2 $UPSTREAM $MODEL"
EOF
```

`MODEL` is the name the server lists; the proxy puts it on every request,
whatever a harness sends. `SERVER_WINDOW` is what the server gives a slot: a
server that holds several models does not say, so the person says it (the
figure here is the one the box's own config gave its Qwen; check it).

**3. Is the server idle, and does it hold the right model?**

```bash
python3 bench/serverfacts.py --gate --idle-slots 2 http://127.0.0.1:8080 qwen3.8-27b; echo $?
```

It asks for the list of models, which loads nothing. It answers 0 only when
the server lists the model, the list calls it loaded, and two of its slots
are idle; otherwise it says what is wrong, and nothing has been changed by
asking. With `MATRIX_GATE` set as above, `matrix.sh` asks the same before
every task and waits until the answer is yes, so a pass never starts on a
server that is busy or holds another model. The slot count comes from
`/slots`, which is asked only of a model the list calls loaded; it answered
as expected on the real server (three idle slots of three).

**4. A slot.** With nothing else on the server, leave `SLOT` unset: the
earlier runs had none, one run took one slot, and gnosh's short side requests
went to whichever slot was least recently used. While anyone else is using
the server, pin the run: `export SLOT=2` puts every request of a run on slot
2, so no other slot's conversation is pushed out. The price is that gnosh's
side requests then share that slot with the run's own conversation, which
may be read again after each one; that shows in `read_tokens` and in the
time, so a pinned lane is compared only with a pinned lane. One run at a time
on a pinned slot: `export HARNESSES=gnosh`, and a pass for each of the three
ways.

**5. One run**, to see a row before a night of them:

```bash
source ~/aibox-64k.env
LANE=smoke bench/run.sh gnosh bugs 1
```

**6. The pass**: three repetitions of the four tasks, the harnesses in
`HARNESSES` side by side on each; then the same for gnosh without mem.

```bash
nohup bench/matrix.sh ~/aibox-64k.env 3 rename algos bugs continue > /dev/null 2>&1 &
tail -f ~/matrix-aibox-qwen-64k.log
HARNESSES=gnosh-nomem bench/matrix.sh ~/aibox-64k.env 3 rename algos bugs continue
```

**7. The tables**:

```bash
bench/rescore.py ~/results-aibox.jsonl > ~/rows.jsonl
bench/table.py ~/rows.jsonl
bench/compare.py ~/rows.jsonl
bench/memerrors.py ~/runs/*aibox-qwen-64k-gnosh-*
```

**How long.** On that box, at 64K with thinking on, one pass of the four
tasks took the earlier gnosh 2 h 07 min (bugs 27:47, algos 43:12, rename
13:44, continue 41:58), nearly all of it the model's time (7,445 s of GPU),
and took crush 1 h 18 min. Those were measured two at a time, one slot each.
The new gnosh's times are what is not known; for planning, take two hours a
pass with mem and an hour and a half without. A pass can never take more
than five hours: five sessions, each capped at `TIMEOUT`.

**A window that really is 32K.** The box's server gives its model about
262,000 tokens a slot, so telling gnosh 32,768 there repeats the earlier test, in which
nothing stopped a request that grew past the window. A true 32K needs a
server whose slot is 32,768: llama.cpp divides its context among its slots,
so a server of its own started with a context of 98,304 and three slots
(`-c 98304 -np 3`), or 32,768 and one, on a port of its own. That is a
second copy of the model in memory on a machine with one GPU, so it is not
something to start beside the daily server without deciding to. With
`UPSTREAM` pointed at it and `WINDOW=32768`, the row's `window_server`
should come out 32,768 and `window_true` true; a row where it does not is
not a true 32K run. On that server a request that grows past the window
fails, the proxy counts it under `errors`, and gnosh's slide either held
the context under the window or did not.

### On a rented GPU machine

The two rented nights of the earlier bench used a DigitalOcean GPU droplet
and vLLM: an RTX 6000 Ada (48 GB) at $1.57 an hour for the swarm night,
five and a half hours and about $9; an H100 at $4.41 an hour for the other,
four hours and about $17.60. A rented machine costs money from the minute
it exists until it is destroyed, and renting one is Jae's decision.

What differs from the box:

- **The server is the bench's own**, so there is nobody to disturb, no gate
  and no slot to pin. It is bound to 127.0.0.1, with the firewall letting
  in ssh only.
- **The window can be true.** Start the server with the context the lane
  tells: vLLM's `--max-model-len 65536`, then again with `32768` for the
  32K lane. `serverfacts.py` reads it from the server's list and the row's
  `window_true` says so.
- **Several runs at once cost little.** On the Ada, six requests at once
  ran at 28 tokens a second each, against 30 for one alone. So all three
  ways of running gnosh, and the three repetitions too, can run side by
  side; `streams.py` measures what the server gives first.
- **The format.** The earlier gnosh spoke to vLLM as it spoke to llama.cpp.
  The new gnosh's `llamacpp` type adds a field of llama.cpp's own
  (`cache_prompt`) that vLLM does not know; whether vLLM passes it over or
  refuses it has not been tried. `PROVIDER_TYPE=openai-compatible` leaves it
  out.

The commands, on the machine, once a server is up at 127.0.0.1:8000 serving
the model under the name `qwen3.8-27b` with prefix caching and tool calling
on (the earlier night's own start-up scripts are with its run data, which
was not carried):

```bash
python3 bench/serverfacts.py --gate http://127.0.0.1:8000 qwen3.8-27b     # lists the model; says its window
URL=http://127.0.0.1:8000/v1/chat/completions MODEL=qwen3.8-27b python3 bench/streams.py

cat > ~/gpu-64k.env <<'EOF'
export LANE=gpu-qwen-64k UPSTREAM=http://127.0.0.1:8000 MODEL=qwen3.8-27b WINDOW=65536
export PROVIDER_TYPE=openai-compatible THINKING=on
export GNOSH_BIN=$HOME/bin/gnosh GO=/usr/local/go/bin/go
export TIMEOUT=2400 RUNS=$HOME/runs RESULTS=$HOME/results-gpu.jsonl
export HARNESSES="${HARNESSES:-gnosh gnosh-full gnosh-nomem}"
EOF
source ~/gpu-64k.env && LANE=smoke bench/run.sh gnosh bugs 1              # one row first
bench/matrix.sh ~/gpu-64k.env 3 rename algos bugs continue
bench/rescore.py ~/results-gpu.jsonl > ~/rows.jsonl && bench/table.py ~/rows.jsonl
```

Then copy `~/runs`, the results and the env files off the machine, and
destroy it. The earlier night's numbers there: one session of the earlier
gnosh took 794 s on algos at the median and 314 s on bugs, a quarter to a
half of what the same tasks took on the box.

## The first measurement

Night of 13 to 14 November 2026, the home AI box: llama.cpp serving Qwen 3.8
27B, three slots of about 262,000 tokens, gnosh told a window of 65,536,
`TIMEOUT=3600`. The binary was commit `2fa512c` and the bench's tree
`5d80a4d`, the same for every run. One repetition of each pass, so every
figure below is one run and a difference between two of them is not yet a
finding. 39 runs in all (47 with the eight of the newer build further
down); no request failed at the proxy, and no session needed a summary or a
slide (the largest context was 48K of the 64K told).

Wall time is minutes and seconds. Tokens are read / generated: what the
server had to read fresh, and what it wrote.

**Thinking on, mem with the lean text beside mem with the fuller text; then
without mem, alone on the server** (so about twice as fast as a run that
shares the GPU: its wall times are not comparable with the other two):

| task | gnosh (lean) | gnosh-full | gnosh-nomem, alone |
|---|---|---|---|
| rename | passed, 36:52, 31 requests, 70K / 24.3K | passed, 23:26, 39, 49K / 13.1K | passed, 17:57, 10, 25K / 16.4K |
| algos | passed 9/9, cut by the cap at 60:00, 27, 59K / 35.7K | passed 9/9, cut by the cap at 60:00, 21, 138K / 36.1K | passed, 15:46, 9, 17K / 15.5K |
| bugs | passed, 11:08, 7, 11K / 8.0K | passed, 37:56, 37, 102K / 26.9K | passed, 6:24, 8, 10K / 6.4K |
| continue | passed, 37:34, 57, 102K / 18.2K | passed, 48:33, 53, 110K / 24.7K | passed, 10:52, 32, 20K / 10.4K |

**Thinking on, mem beside no mem, under the same load** (the fair pair):

| task | gnosh (lean) | gnosh-nomem |
|---|---|---|
| rename | passed, 23:53, 32, 78K / 14.6K | passed, 32:14, 11, 24K / 20.1K |
| algos | **stopped by the cap, 0/9**, 23, 35K / 48.8K | passed, 29:46, 12, 23K / 22.5K |
| bugs | passed, 8:47, 9, 13K / 6.7K | passed, 5:08, 5, 10K / 3.7K |
| continue | passed, 38:25, 91, 167K / 25.3K | passed, 16:04, 25, 15K / 9.4K |

**Thinking off (how Jae runs his own sessions), mem beside no mem:**

| task | gnosh (lean) | gnosh-nomem |
|---|---|---|
| rename | passed, 2:47, 6, 12K / 1.1K | passed, 9:21, 43, 16K / 7.6K |
| algos | passed, 34:26, 88, 127K / 24.9K | passed, 16:12, 19, 14K / 12.5K |
| bugs | passed, 4:42, 11, 9K / 3.0K | passed, 4:38, 11, 16K / 3.0K |
| continue | **failed, 5/6**, 14:43, 71, 78K / 8.6K | passed, 9:02, 28, 9K / 5.5K |

One run before these, alone on the server to see a row: bugs with the lean
text passed in 5:25 with 8 requests.

**What it says.**

- Without mem the new gnosh passed every run, 12 of 12. With thinking on it
  made about as many requests as crush made on these tasks on 6 November (5
  to 12 where crush made 6 to 13; continue 25 to 32 where crush made 40);
  with thinking off it made more and smaller ones (11 to 43).
- With mem it passed 15 of 17 in the three passes. On these tasks, which
  all fit the window, mem brought no gain to set against its cost: under
  the same load and with thinking on it took 1.7 to 2.4 times as long on
  three of the four tasks (with thinking off, 1.6 and 2.1 times on two, and
  the same on bugs), and it was faster on rename both times.
- The requests the earlier gnosh spent on one tool call a reply are gone
  (bugs: 65 then, 7 to 11 now). The time is the model's writing: with
  thinking on, a run with mem wrote two to three times the thinking of a
  run without (60 to 100 thousand characters on algos, against 24 to 31).
- In the three passes 47 of the 229 calls to `gnovm_run` failed (21%): 12
  pops of a todo whose own todos were still open, 6 closes the review
  refused, 10 that did not compile as Gno, 3 out of gas.

**The two runs that did not pass, and why.**

- *continue, thinking off.* The task's fourth change is a method named
  `WithTag`. The first session closed its todo with what was left "for next
  time" and wrote the method as `With(tag)`. The second session read mem,
  built `With`, and the hidden test calls `WithTag`. mem carried the note
  faithfully; the note was not exact.
- *algos, thinking on, in the fair pair.* The model set out to "validate the
  trickier algorithms in Gno before writing the Go", and spent the hour in
  the scratch machine: a loop of 3,999 steps out of gas, no `reflect` and no
  `sort.Slice` in Gno, the machine's state gone after an error. It never
  wrote a Go file. Counting the experiments below, the scratch machine was
  used to try the project's code in 4 of the 8 runs of algos with mem.

**Two small experiments on those two**, each a sentence added to the end of
the system prompt (`GNOSH_APPEND_FILE`), the same binary beside itself:

| experiment | runs | without the sentence | with it |
|---|---|---|---|
| a note for later is written exactly (continue, thinking off) | 3 each | 3/3 passed; the exact name reached mem in 2 of 3 | 3/3 passed; the exact name reached mem in 3 of 3 |
| the scratch machine is not for the project's code (algos, thinking on) | 2 each | 2/2 passed (53:19; 60:00 at the cap) | 1 passed in 43:40; 1 stopped by the cap at 6/9, having tried code in Gno five times |

The first is weak evidence for its sentence and shows no cost. The second
shows that a sentence at the end of the prompt does not stop it. Both
sentences were then written into the text about mem itself, where the
scratch machine is introduced and where stopping with work left is
described (`internal/prompt/texts/memory.md`, `memory-full.md`, commit
`c8fb08e`).

**The build with that wording, on the same two tasks.** Commit `34ef33a`
(binary and bench tree), 14 November, mem with the lean text, two runs at a
time:

| task | runs | outcome | read beside |
|---|---|---|---|
| continue, thinking off | 4 | 4/4 passed, 18:39 to 26:29; the exact name reached mem in 4 of 4 | the earlier build without the sentence: 3 of 4 passed, 27 to 29 minutes, the exact name in 2 of 4 |
| algos, thinking on | 4 | 2 passed 9/9, both cut by the cap; 1 stopped at 7/9; 1 stopped at 0/9 | the earlier build: 3 of 4 passed, the fourth stopped at 0/9 |

- With the rule in the text or added to it, the exact name reached mem in 7
  of 7 runs; without it, in 2 of 4.
- The scratch machine was used for the project's code once in these four
  runs of algos (one call), where the earlier build did it in 4 of 8 runs
  (3 to 19 calls each).
- algos with mem and thinking on does not reliably fit the hour when two
  runs share the GPU: all four ran to the cap, and the time is thinking.
  The run stopped at 0/9 read the 18 files and then wrote one thought of
  72,696 characters, which was still being written 54 minutes later.
  Nothing in gnosh bounds how long a thought may be. With thinking off,
  which is how Jae runs, there is no such thought.

**Not run:** the three repetitions the plan asked for, the 32K lane on a
server whose slot really is 32,768, the fuller text with thinking off, and
any task larger than the window, which is where mem is meant to pay. The
lean text against the fuller one is not settled by one run each (lean was
faster on bugs and continue, the fuller on rename).

The rows (`results-aibox.jsonl`, with `rows.jsonl` and the tables made from
it) and the run folders are on the AI box under `/home/crush/newgnosh-bench`;
a copy of the rows and tables is in Jae's notes (`bench-2026-11-14/`).

## Several sessions on one plan: a rented H200, 16 November 2026

gnosh.ai's speed claims were the earlier gnosh's (`RESULTS.md`). On 16
November 2026 the gnosh in this repository was measured the same two ways,
on a rented NVIDIA H200 (141 GB, DigitalOcean `gpu-h200x1-141gb`), each run
alone on the server. Every run below passed all of its checks.

**Three sessions on a plan seeded in mem.** The scenario `peers-algos`
(`internal/gnovm/testdata/scenarios`), run by the scenario harness with
`GNOVM_SCENARIO_PEERS=1` and `3`: nine small Go packages in three groups,
the plan seeded in mem, and the same bare prompt to every session ("Work
through the plan in mem until nothing is left for you."). llama.cpp b11714
served Qwen 3.8 27B, the home boxes' Q8 file with no draft model, in eight
slots of 131,072 tokens, thinking on. Times are minutes and seconds.

| gnosh | one session | three sessions |
|---|---|---|
| `e4f17ad` | 15:25, 26:23 | 18:09, 19:23 |
| `716e557` | | 18:44 |
| `f256e4c` (in main as `8aca3c4` and `2f6e839`) | 17:37 | 9:47, 9:49 |

Before `f256e4c` nothing told a session that others worked the plan: each
saw all of it free at first. In the first three-session run one session read
every file and wrote all nine packages without taking a todo, while the
other two found each next todo "already written". `f256e4c` has a person's
session join the plan's peers when its turn begins, and ends an agent's
`mem.Show()` and `mem.Next()` with how many other sessions work the plan
now. With it, in its first run, all three took a todo within 27 seconds and
each wrote only its own packages. Three sessions with it against one: 1.8×.

**Six sessions with a planner.** `SWARM=6 run.sh gnosh-swarm algos`
against `run.sh gnosh algos`, seconds:

| server | gnosh | one session | six sessions | medians |
|---|---|---|---|---|
| llama.cpp as above | `e4f17ad` | 619, 900, 462 | 1,099, 781, 1,223 | 0.56× |
| llama.cpp, the planner told one todo per package | `f256e4c` | (as above) | 896, 1,251 | 0.58× |
| vLLM 0.33.0, Qwen 3.8 27B FP8, fp8 KV cache, effort medium from the proxy | `f256e4c` | 236, 198, 181, 148, 177 | 112, 153, 98, 65, 121 | 1.6× |

On llama.cpp one stream wrote 70 tokens a second and six at once about 36
each (170 in all), while six sessions wrote 1.5 to 2 times the tokens of
one session, so six lost to one. In one run the planner put all nine
packages in one step, and five sessions waited; in another, one session
spent four minutes checking the others' packages after the plan was done.
vLLM serves six at once far better: there six sessions were 1.6× faster
than one (medians of five runs each, 181 and 112 seconds), as the earlier
gnosh had been on vLLM. The runs' logs are kept outside this repository.

## The dry run

`bench/dryrun.sh` proves the plumbing without a model. It builds gnosh from
this tree and stands the scripted model server of `tools/dev/scriptmodel`
where a model server would be. Each of the four tasks goes through `run.sh`
as a real run would; the scripted "model" makes a few tool calls and stops.
The dry run then holds what the proxy counted to what the scripted server
is known to have served (so many replies; a quarter of each request's bytes
as prompt tokens; a quarter of each reply's as generated) and to gnosh's
own count; holds the check's score to the one the script must get (1 of 9
packages, 1 of 6 bugs, and 6 of 6 changes where the script is handed the
solved files, so that a check is seen to pass too); runs the same short task
every other way a run can be made (without mem, the fuller text, thinking
off, the plain format, a pinned slot, a prompt experiment, a second session
that is new, a turn cap, a wall cap on a session and on the run, a swarm of
two, two ways side by side through `matrix.sh`, a summary); puts the rows
through the scoring scripts; and checks that
every run kept to its own directory, that the directories are removed, and
that nothing is left running.

    GO=/usr/local/go/bin/go bench/dryrun.sh

It takes about three minutes on the devbox (16 cores) and prints one line a
check. It needs Linux, Go 1.27, python3 and git, and reaches nothing outside
the machine: even its build is made with `GOPROXY=off`, so the Go module
cache must already hold what gnosh is built from.

## What changed since the earlier bench

For reading a new number beside an old one. The tasks, their prompts, their
checks and the proxy's counting are the same, byte for byte but for the
proxy's test for a summary. What differs:

- **The program.** `gnosh` is now the gnosh of this repository. A session
  is `gnosh -p -y --cwd DIR --output-format stream-json -m … -- prompt`
  where it was `gnosh run -q -y -c DIR -D DATADIR -m … prompt`: print mode
  is `-p`, and this gnosh has no `run` command, no `-q` and no `-D`
  (`spec/DECISIONS.md`, 26); `-y` is needed now, since a run no longer
  approves its own tool calls (`spec/DECISIONS.md`, 11). Sessions and mem
  are kept under the run's own home, through `XDG_DATA_HOME`, where
  sessions were kept in `-D`. Without `-q`, a run's notices go to its
  stderr, which the runner keeps as `session-N.err`; nothing is scored
  from it.
- **The window** is given to gnosh in its config, with `listModels` off.
  The earlier gnosh found it in the proxy's `/models` answer.
- **Summaries.** The proxy knew a summary request by the words of the
  earlier gnosh's, crush's and Codex's own prompts. It now knows it by the
  first words of the new gnosh's (`about to be replaced by a summary`).
  For another harness the words are given in `BENCH_SUMMARY_RE`; without
  them a crush or Codex run's `summaries` would read 0.
- **`popped`** was the number of messages a pop had cut from the model's
  context, read from the earlier session database. The new session file
  records the pops themselves, so the row has **`pops`**: the todos popped.
- **`slides`** are counted from the session file, where they were counted
  from the earlier log. **`slid_tokens`** is gone: the new gnosh records a
  slide, not its size.
- **Turns.** The earlier rows had none; `requests` was, and is, every
  request the proxy saw, side requests included. `num_turns` is new, and is
  gnosh's own count of the model's turns in a session.
- **Stopped runs.** A run ended by its cap was a failed run in the earlier
  tables. It is a stopped one now, counted apart.
- **Wall time** is as it was: from the first session's start to the last
  one's end, which for continue includes the check between the two.
- **`reviewab.py`**, which replayed a session's close reviews under two
  versions of the review's instructions, was not brought: it rebuilt the
  earlier gnosh's review request by hand from its session database.

## The swarm

`gnosh-swarm` starts one gnosh session with the task and a line saying
that N−1 more sessions will help, and asking it to plan the work in mem
with the todos that don't depend on each other under `.Parallel()`. Once
its plan is in mem (`waitplan.py` watches its session file), the other
sessions start, each told to take free todos with `mem.Next()` until none
are left. All of them share one mem, so each todo is taken once, by one
session, under mem's lock, and a session with nothing free waits on mem
instead of polling. Wall time runs from the first session's start until
the last one ends.

    SWARM=3 bench/run.sh gnosh-swarm algos 1

The dry run starts a swarm of two and sees the second session wait for the
first one's plan. Six sessions of the new gnosh finishing algos between
them were measured on 16 November 2026, under "Several sessions on one
plan" above.

## Notes on the other harnesses

`run.sh` still knows how to run Charm's crush and OpenAI's Codex CLI, as it
did, and nothing here has run either. Their numbers above are the earlier
ones.

- Codex 0.167 speaks only the Responses API. It sends its sub-agent tools
  as a `"namespace"` tool that vLLM and llama.cpp reject, so `run.sh` turns
  its sub-agents off; and for a model outside its own catalog it offers no
  patch tool, so on a local server it edits through its shell.
- crush discovers the model's window from the proxy's `/models`
  answer (`meta.n_ctx`), as it would from llama-server.
