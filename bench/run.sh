#!/bin/bash
# run.sh: one harness x one task x one repetition -> one JSON row.
# See bench/README.md.
#
# Usage: run.sh <gnosh|gnosh-full|gnosh-nomem|gnosh-swarm|crush|codex> <task> <rep>
#
# gnosh is the gnosh of this repository. gnosh-full and gnosh-nomem are the
# same binary run two other ways (MEMORY_TEXT=full, MEM=off, below), under
# names of their own so that matrix.sh can run them side by side and the
# tables can tell them apart.
#
# Environment:
#   LANE       label for the row (qwen-64k, h100-gpt-oss-120b, ...)
#   UPSTREAM   model server base URL, e.g. http://127.0.0.1:8080
#   MODEL      the model id the server knows; the proxy pins every request to it
#   WINDOW     context window told to every harness (default 65536)
#   GATE       optional command, polled every 30 s until it exits 0, before the run
#   GNOSH_BIN, CRUSH_BIN, CODEX_BIN   harness binaries
#   GO         go binary; its directory goes first on PATH
#   TASKS      tasks directory (default: tasks/ next to this script)
#   RUNS       where run directories go (default: ~/runs)
#   RESULTS    JSONL file the row is appended to (default: ~/results.jsonl)
#   CLAUDE_AUTOCOMPACT_PCT_OVERRIDE
#              optional 1..100 threshold forwarded into the clean harness env
#              for functional checks of automatic context management (#7);
#              recorded on row.json so lowered-threshold runs stay marked
#   TIMEOUT    seconds one harness session may take (default 3600); a session
#              that runs out is stopped and the row says timed_out, and its
#              outcome is "stopped", never "failed", unless the check passes
#   RUN_TIMEOUT  optional seconds for the whole run, its sessions and what lies
#              between them; what is left of it caps each session, and a
#              session it leaves no time for is not started
#   MAX_TOKENS_CAP  optional cap the proxy puts on max_tokens (default 0 = off)
#   REASONING  optional reasoning effort passed to every harness (low|medium|high),
#              for a model whose thinking has levels
#   THINKING   gnosh only: "on" or "off" for a model that thinks or does not
#              ("thinking" of its config's "model"); unset leaves gnosh's default, on
#   CODEX_REASONING  reasoning effort for Codex alone (its model_reasoning_effort)
#   SERVER_REASONING  the proxy puts this reasoning effort on every request of every
#              harness, as a local server's default would be (the fair way to set it)
#   MEM        gnosh only: "off" disallows the machine tools (gnovm_run, gnovm_new,
#              gnovm_list, gnovm_drop), and with them mem, frames and gnosh's
#              text about mem: the plain-harness baseline. Default on
#   MEMORY_TEXT  gnosh only: "full" sends the fuller of gnosh's two texts about
#              mem in place of the lean one, as the user's own text "memory"
#              (MEMORY_FULL_FILE, default internal/prompt/texts/memory-full.md of
#              the tree this script is in). Default lean
#   SLOT       gnosh only: pin the run's requests to this llama.cpp slot ("id_slot"
#              in every request body), so that a run cannot take a slot another
#              session's conversation is cached in. SIDE_SLOT, with it, sends
#              gnosh's short side requests (names, close reviews) to a second
#              slot, so that they do not push the run's own conversation out
#   MAX_TURNS  gnosh only: stop a session after this many turns of the model
#              (--max-turns); the row's outcome is then "stopped"
#   PROVIDER_TYPE  gnosh only: how gnosh speaks to the server: llamacpp (the
#              default: llama.cpp's own additions to the format) or
#              openai-compatible (the plain format, for vLLM and the like)
#   SERVER_WINDOW  the context per slot the server was started with, for a
#              server that cannot be asked (serverfacts.py); the row keeps it
#              apart from what the server itself said
#   GNOSH_APPEND_FILE, PROMPT_SUFFIX_FILE, VARIANT   gnosh prompt experiments: text
#              added to the end of the system prompt, text added to the end of
#              each user prompt, and the experiment's name for the row
#   DUMP       if set, the proxy saves the first 40 request bodies in requests/
#   CONTINUE_MODE  "new" gives prompt2.txt to a brand-new session instead of the
#              harness's continue flag (what carries over is only what the
#              harness keeps outside the conversation: gnosh's mem, Codex's memories)
#   GNOSH_PROMPTS  optional dir of prompt texts (core.md, memory.md, ...) copied to
#              the folder gnosh reads the user's own texts from, for prompt
#              experiments
#   SWARM      gnosh-swarm only: how many gnosh sessions work the task at once
#              (default 3). The first gets the task and a line saying the others
#              will help; once its plan is in mem (waitplan.py), the others start
#              with SWARM_HELPER_PROMPT. All share the run's HOME, so one mem and
#              one session store. Wall time runs from the first session's start
#              until the last one ends.
#
# A run directory holds: home/ (the harness's HOME: its config, and for gnosh
# its sessions and mem, under home/.local/share/gnosh), work/ (the task's
# repository), data/ (crush's -D), proxy.jsonl (one line per model request),
# session-N.out/.err (for gnosh, .out is the run's stream-json), server.json
# (what the model server said of itself), mem.txt (gnosh mem show, after the
# run), check.txt and row.json.
set -u
harness=${1:?harness}; task=${2:?task}; rep=${3:?rep}
BENCH=$(cd "$(dirname "$0")" && pwd)
TASKS=${TASKS:-$BENCH/tasks}
RUNS=${RUNS:-$HOME/runs}
RESULTS=${RESULTS:-$HOME/results.jsonl}
WINDOW=${WINDOW:-65536}
TIMEOUT=${TIMEOUT:-3600}
LANE=${LANE:-unknown}
: "${UPSTREAM:?UPSTREAM}" "${MODEL:?MODEL}"
[ -n "${GO:-}" ] && export PATH="$(dirname "$GO"):$PATH"
tdir=$TASKS/$task
[ -x "$tdir/make.sh" ] && [ -x "$tdir/check.sh" ] && [ -f "$tdir/prompt.txt" ] || { echo "run.sh: no task $task in $TASKS" >&2; exit 2; }

# How gnosh is to be run is settled before anything is started, so that the
# run's directory and its row carry the same name, and a switch that cannot
# be used stops the run here.
mem=""; memtext=""; thinking=""; ptype=""
case $harness in gnosh|gnosh-full|gnosh-nomem|gnosh-swarm)
  [ "$harness" = gnosh-full ] && MEMORY_TEXT=full
  [ "$harness" = gnosh-nomem ] && MEM=off
  mem=${MEM:-on}; memtext=${MEMORY_TEXT:-lean}; thinking=${THINKING:-default}; ptype=${PROVIDER_TYPE:-llamacpp}
  case $mem in on|off) ;; *) echo "run.sh: MEM is on or off, not $mem" >&2; exit 2 ;; esac
  case $memtext in lean|full) ;; *) echo "run.sh: MEMORY_TEXT is lean or full, not $memtext" >&2; exit 2 ;; esac
  case $thinking in on|off|default) ;; *) echo "run.sh: THINKING is on or off, not $thinking" >&2; exit 2 ;; esac
  [ "$mem" = off ] && memtext=none  # without the machine tools gnosh sends neither text
  [ "$mem" = off ] && [ "$harness" = gnosh-swarm ] && { echo "run.sh: a swarm shares mem, so it cannot run with MEM=off" >&2; exit 2; }
  if [ "$memtext" = full ]; then  # the fuller text about mem is a file of the tree gnosh was built from
    full=${MEMORY_FULL_FILE:-$BENCH/../internal/prompt/texts/memory-full.md}
    [ -f "$full" ] || { echo "run.sh: MEMORY_TEXT=full needs gnosh's text memory-full.md, and $full is not there: set MEMORY_FULL_FILE" >&2; exit 2; }
    [ -n "${GNOSH_PROMPTS:-}" ] && [ -e "$GNOSH_PROMPTS/memory.md" ] && { echo "run.sh: GNOSH_PROMPTS already holds a memory.md, so MEMORY_TEXT=full has nowhere to go" >&2; exit 2; }
  fi
  if [ "$harness" != gnosh-swarm ]; then  # the row names the way gnosh was run
    harness=gnosh; [ "$memtext" = full ] && harness=gnosh-full; [ "$mem" = off ] && harness=gnosh-nomem
  fi ;;
esac

if [ -n "${GATE:-}" ]; then
  until bash -c "$GATE" >/dev/null 2>&1; do sleep 30; done
fi

id=$(date -u +%Y%m%dT%H%M%S)-$LANE-$harness-$task-r$rep-$$
dir=$RUNS/$id
mkdir -p "$dir/home" "$dir/data"
work=$dir/work
"$tdir/make.sh" "$work" > "$dir/make.log" 2>&1 || { echo "run.sh: make.sh failed, see $dir/make.log" >&2; exit 2; }

# What the model server says of itself, asked in the way that cannot make it
# load or unload anything (serverfacts.py), so that the row can say which
# window the server really has beside the one the harness is told.
python3 "$BENCH/serverfacts.py" "$UPSTREAM" "$MODEL" > "$dir/server.json" 2> "$dir/server.err"

# The proxy: one per run, its own port, its own log.
python3 "$BENCH/proxy.py" serve --listen 127.0.0.1:0 --port-file "$dir/port" \
  --upstream "$UPSTREAM" --model "$MODEL" --window "$WINDOW" \
  --max-tokens-cap "${MAX_TOKENS_CAP:-0}" ${DUMP:+--dump "$dir/requests"} \
  ${SERVER_REASONING:+--reasoning-effort "$SERVER_REASONING"} \
  --log "$dir/proxy.jsonl" > "$dir/proxy.err" 2>&1 &
proxy=$!
echo "$proxy" > "$dir/proxy.pid"
trap 'kill $proxy 2>/dev/null' EXIT
for _ in $(seq 100); do [ -s "$dir/port" ] && break; sleep 0.1; done
port=$(cat "$dir/port")

# A clean environment: the run's own HOME and XDG dirs, telemetry and update
# checks off, no keys of anyone's.
H=$dir/home
runenv=(env -i PATH="$PATH" HOME="$H" USER="$(id -un)" LANG=C.UTF-8 TERM=dumb
  XDG_CONFIG_HOME="$H/.config" XDG_DATA_HOME="$H/.local/share" XDG_CACHE_HOME="$H/.cache"
  XDG_STATE_HOME="$H/.local/state" GOPATH="$H/go" GOCACHE="$H/.cache/go-build" GOFLAGS=-mod=mod
  GOTOOLCHAIN=local DO_NOT_TRACK=1
  CRUSH_DISABLE_METRICS=1 CRUSH_DISABLE_PROVIDER_AUTO_UPDATE=1)
if [ -n "${CLAUDE_AUTOCOMPACT_PCT_OVERRIDE:-}" ]; then
  case "$CLAUDE_AUTOCOMPACT_PCT_OVERRIDE" in
    *[!0-9]*) echo "run.sh: CLAUDE_AUTOCOMPACT_PCT_OVERRIDE must be 1..100, got $CLAUDE_AUTOCOMPACT_PCT_OVERRIDE" >&2; exit 2 ;;
  esac
  if [ "$CLAUDE_AUTOCOMPACT_PCT_OVERRIDE" -lt 1 ] || [ "$CLAUDE_AUTOCOMPACT_PCT_OVERRIDE" -gt 100 ]; then
    echo "run.sh: CLAUDE_AUTOCOMPACT_PCT_OVERRIDE must be 1..100, got $CLAUDE_AUTOCOMPACT_PCT_OVERRIDE" >&2; exit 2
  fi
  runenv+=(CLAUDE_AUTOCOMPACT_PCT_OVERRIDE="$CLAUDE_AUTOCOMPACT_PCT_OVERRIDE")
fi
reason=()
# cap: the seconds the next session may take: TIMEOUT, or what is left of
# RUN_TIMEOUT when that is less. left: whether RUN_TIMEOUT leaves any.
cap() { [ -n "${RUN_TIMEOUT:-}" ] || { echo "$TIMEOUT"; return; }
  python3 -c 'import sys, time; t0, run, one = map(float, sys.argv[1:4]); print(max(1, int(min(one, run - (time.time() - t0)))))' "$t0" "$RUN_TIMEOUT" "$TIMEOUT"; }
left() { [ -z "${RUN_TIMEOUT:-}" ] || python3 -c 'import sys, time; t0, run = map(float, sys.argv[1:3]); sys.exit(0 if time.time() - t0 < run else 1)' "$t0" "$RUN_TIMEOUT"; }
case $harness in
  gnosh|gnosh-full|gnosh-nomem|gnosh-swarm)
    # The run's own config. It names one model and gives its window, and
    # listModels is off, so gnosh asks the server for nothing and can name no
    # other model. The model's own line is the thinking switch.
    mkdir -p "$H/.config/gnosh"
    pin=""; [ -n "${SLOT:-}" ] && pin=", \"extraBody\": {\"id_slot\": $SLOT}"
    think=""; [ "$thinking" = on ] && think=", \"thinking\": true"; [ "$thinking" = off ] && think=", \"thinking\": false"
    small="box/$MODEL"
    side=""; sidemodel=""
    if [ -n "${SIDE_SLOT:-}" ]; then  # the short side requests, on a slot of their own
      side=$(printf ',\n    "side": {"type": "%s", "baseUrl": "http://127.0.0.1:%s", "listModels": false, "extraBody": {"id_slot": %s}}' "$ptype" "$port" "$SIDE_SLOT")
      sidemodel=$(printf ',\n    "side/%s": {"contextWindow": %s}' "$MODEL" "$WINDOW")
    fi
    {
      printf '{\n  "providers": {\n'
      printf '    "box": {"type": "%s", "baseUrl": "http://127.0.0.1:%s", "listModels": false%s}%s\n  },\n' "$ptype" "$port" "$pin" "$side"
      printf '  "models": {\n    "box/%s": {"contextWindow": %s}%s\n  },\n' "$MODEL" "$WINDOW" "$sidemodel"
      printf '  "model": {"id": "box/%s"%s}\n}\n' "$MODEL" "$think"
    } > "$H/.config/gnosh/gnosh.json"
    [ -n "${SIDE_SLOT:-}" ] && small="side/$MODEL"
    if [ -n "${GNOSH_PROMPTS:-}" ]; then  # experiments: the user's own prompt texts
      mkdir -p "$H/.config/gnosh/prompts" && cp "$GNOSH_PROMPTS"/*.md "$H/.config/gnosh/prompts/"
    fi
    if [ "$memtext" = full ]; then  # the fuller text about mem, as the user's own text "memory"
      mkdir -p "$H/.config/gnosh/prompts" && cp "$full" "$H/.config/gnosh/prompts/memory.md"
    fi
    nomem=(); [ "$mem" = off ] && nomem=(--disallowedTools=gnovm_run,gnovm_new,gnovm_list,gnovm_drop)
    turns=(); [ -n "${MAX_TURNS:-}" ] && turns=(--max-turns "$MAX_TURNS")
    [ -n "${REASONING:-}" ] && reason=(--reasoning-effort "$REASONING")
    # Prompt experiments (gnosh only): GNOSH_APPEND_FILE is added to the end of
    # the system prompt, PROMPT_SUFFIX_FILE to the end of each user prompt, and
    # VARIANT names the experiment in the row.
    append=(); [ -n "${GNOSH_APPEND_FILE:-}" ] && append=(--append-system-prompt-file "$GNOSH_APPEND_FILE")
    suffix=""; [ -n "${PROMPT_SUFFIX_FILE:-}" ] && suffix=$(cat "$PROMPT_SUFFIX_FILE")
    version=$("$GNOSH_BIN" --version 2>&1 | head -1)${GNOSH_PROMPTS:+ +prompts $(basename "$GNOSH_PROMPTS")}${VARIANT:+ +variant $VARIANT}
    # A session answers one prompt and exits (-p), allowed everything (-y: a
    # run refuses what would have asked otherwise), and prints what happened as
    # JSON lines with its result object last, which is what the row reads.
    session() { local cont=(); [ "$1" -gt 1 ] && [ "${CONTINUE_MODE:-}" != new ] && cont=(--continue)
      "${runenv[@]}" timeout -k 30 "$(cap)" "$GNOSH_BIN" -p -y --cwd "$work" --output-format stream-json \
      -m "box/$MODEL" --small-model "$small" "${nomem[@]}" "${turns[@]}" "${reason[@]}" "${append[@]}" "${cont[@]}" -- "$2$suffix" < /dev/null; }
    # swarm_session <prompt>: one session of a swarm.
    swarm_session() { "${runenv[@]}" timeout -k 30 "$(cap)" "$GNOSH_BIN" -p -y --cwd "$work" --output-format stream-json \
      -m "box/$MODEL" --small-model "$small" "${turns[@]}" "${reason[@]}" "${append[@]}" -- "$1" < /dev/null; }
    [ "$harness" = gnosh-swarm ] && version="$version +swarm ${SWARM:-3}" ;;
  crush)
    mkdir -p "$H/.config/crush"
    printf 'provider add box --type llamacpp --base-url "http://127.0.0.1:%s"\n' "$port" > "$H/.config/crush/crushrc"
    [ -n "${REASONING:-}" ] && reason=(--reasoning-effort "$REASONING")
    version=$("$CRUSH_BIN" --version 2>&1 | head -1)
    session() { local cont=(); [ "$1" -gt 1 ] && [ "${CONTINUE_MODE:-}" != new ] && cont=(--continue)
      "${runenv[@]}" timeout -k 30 "$(cap)" "$CRUSH_BIN" run -q -y -c "$work" -D "$dir/data" \
      -m "box/$MODEL" --small-model "box/$MODEL" "${reason[@]}" "${cont[@]}" "$2" < /dev/null; } ;;
  codex)
    mkdir -p "$H/.codex"
    reason_line=""
    r=${CODEX_REASONING:-${REASONING:-}}
    [ -n "$r" ] && reason_line="model_reasoning_effort = \"$r\""
    cat > "$H/.codex/config.toml" <<EOF
model = "$MODEL"
model_provider = "bench"
model_context_window = $WINDOW
approval_policy = "never"
sandbox_mode = "danger-full-access"
check_for_update_on_startup = false
web_search = "disabled"
$reason_line

[analytics]
enabled = false

# Codex groups its sub-agent tools in a "namespace" tool, which vLLM's and
# llama.cpp's Responses endpoints reject ("tool type 'namespace' is not
# supported"), so sub-agents are off for Codex.
[features]
multi_agent = false

[model_providers.bench]
name = "bench"
base_url = "http://127.0.0.1:$port/v1"
wire_api = "responses"
request_max_retries = 2
stream_max_retries = 2
stream_idle_timeout_ms = 1800000
EOF
    version=$("$CODEX_BIN" --version 2>&1 | head -1)
    session() { local cont=(); [ "$1" -gt 1 ] && [ "${CONTINUE_MODE:-}" != new ] && cont=(resume --last)
      "${runenv[@]}" CODEX_HOME="$H/.codex" timeout -k 30 "$(cap)" "$CODEX_BIN" exec \
      --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox -C "$work" "${cont[@]}" "$2" < /dev/null; } ;;
  *) echo "run.sh: unknown harness $harness" >&2; exit 2 ;;
esac

# Sessions: prompt.txt, then prompt2.txt if the task has one, given with the
# harness's continue flag in the same work dir and HOME. Between the two, the
# check runs once and its line is kept as check1.
runenv+=(GO="${GO:-go}")
t0=$(date +%s.%N); exits=(); timed_out=false; n=0; check1=""; planned_s=""
if [ "$harness" = gnosh-swarm ]; then
  N=${SWARM:-3}
  [ -f "$tdir/prompt2.txt" ] && { echo "run.sh: gnosh-swarm runs one-prompt tasks only" >&2; exit 2; }
  note=${SWARM_PLANNER_NOTE:-"$((N-1)) more gnosh sessions in this project share your mem and will work on this with you. First plan it in mem, with the todos that do not depend on each other under .Parallel(); then take todos with mem.Next(), as they will."}
  helper=${SWARM_HELPER_PROMPT:-"Another gnosh session in this project has planned a task in mem, and you are here to help with it. Take the next free todo with mem.Next(), do its work, pop it with what came of it, and keep taking todos until mem.Next() has nothing left for you. Do not plan the task again."}
  pids=()
  { swarm_session "$(cat "$tdir/prompt.txt")

$note" > "$dir/session-1.out" 2> "$dir/session-1.err"; echo $? > "$dir/exit-1"; } &
  pids+=($!)
  python3 "$BENCH/waitplan.py" "$H/.local/share/gnosh/projects" "$TIMEOUT" "${pids[0]}" && planned_s=$(python3 -c "import time; print(round(time.time()-$t0,1))")
  for i in $(seq 2 "$N"); do
    { swarm_session "$helper" > "$dir/session-$i.out" 2> "$dir/session-$i.err"; echo $? > "$dir/exit-$i"; } &
    pids+=($!)
  done
  wait "${pids[@]}"
  for i in $(seq 1 "$N"); do
    rc=$(cat "$dir/exit-$i" 2>/dev/null || echo 255); exits+=("$rc")
    { [ "$rc" = 124 ] || [ "$rc" = 137 ]; } && timed_out=true
  done
fi
for p in "$tdir/prompt.txt" "$tdir/prompt2.txt"; do
  [ "$harness" = gnosh-swarm ] && break
  [ -f "$p" ] || continue
  n=$((n+1))
  if [ "$n" -gt 1 ]; then
    check1=$("${runenv[@]}" "$tdir/check.sh" "$work" 2>&1 | tail -1)
  fi
  left || { timed_out=true; break; }  # RUN_TIMEOUT leaves this session no time
  session "$n" "$(cat "$p")" > "$dir/session-$n.out" 2> "$dir/session-$n.err"
  rc=$?
  exits+=("$rc")
  { [ "$rc" = 124 ] || [ "$rc" = 137 ]; } && timed_out=true
done
t1=$(date +%s.%N)

case $harness in gnosh*)  # what mem holds when the run is over, read without a model
  [ "$mem" = on ] && "${runenv[@]}" timeout -k 5 120 "$GNOSH_BIN" mem --cwd "$work" show > "$dir/mem.txt" 2>&1 ;;
esac
"${runenv[@]}" "$tdir/check.sh" "$work" > "$dir/check.txt" 2>&1; passed_rc=$?
check_msg=$(tail -1 "$dir/check.txt")
kill $proxy 2>/dev/null; wait $proxy 2>/dev/null
summary=$(python3 "$BENCH/proxy.py" summarize "$dir/proxy.jsonl")

python3 - "$dir" "$LANE" "$MODEL" "$WINDOW" "$harness" "$version" "$task" "$rep" "$passed_rc" "$check_msg" \
  "$t0" "$t1" "$timed_out" "$summary" "${exits[*]}" "$check1" "${VARIANT:-}" "${N:-}" "$planned_s" \
  "$BENCH" "$mem" "$memtext" "$thinking" "${SLOT:-}" "${SIDE_SLOT:-}" "${MAX_TURNS:-}" "$TIMEOUT" "${RUN_TIMEOUT:-}" \
  "${SERVER_WINDOW:-}" "${REASONING:-}" "${SERVER_REASONING:-}" "$ptype" "${CLAUDE_AUTOCOMPACT_PCT_OVERRIDE:-}" <<'PY' | tee "$dir/row.json" >> "$RESULTS"
import json, os, sys
(d, lane, model, window, harness, version, task, rep, rc, msg, t0, t1, to, summ, exits, check1, variant, swarm, planned) = sys.argv[1:20]
(bench, mem, memtext, thinking, slot, side_slot, max_turns, timeout, run_timeout, server_window, reasoning, server_reasoning, ptype, autocompact_pct) = sys.argv[20:34]
row = {"lane": lane, "model": model, "window": int(window), "harness": harness, "version": version,
       "task": task, "rep": int(rep), "passed": rc == "0", "check_msg": msg,
       "wall_s": round(float(t1) - float(t0), 1), "timed_out": to == "true",
       "exit_codes": [int(x) for x in exits.split()], "dir": d}
if autocompact_pct:
  row["autocompact_pct"] = int(autocompact_pct)
if check1:
    row["check1"] = check1
if variant:
    row["variant"] = variant
if harness == "gnosh-swarm":
    row["swarm"] = int(swarm)
    row["planned_s"] = float(planned) if planned else None
row["timeout"] = int(timeout)
if run_timeout:
    row["run_timeout"] = int(run_timeout)
if reasoning:
    row["reasoning"] = reasoning
if server_reasoning:
    row["server_reasoning"] = server_reasoning

# The window the server really has, beside the one the harness was told:
# what the server itself said (serverfacts.py), and what the person running
# the bench said of it (SERVER_WINDOW), kept apart.
try:
    server = json.load(open(os.path.join(d, "server.json")))
except (OSError, ValueError):
    server = {}
row["window_server"] = server.get("window_server")
row["window_server_from"] = server.get("window_source")
if server_window:
    row["window_server_stated"] = int(server_window)
true = row["window_server"] or row.get("window_server_stated")
row["window_true"] = None if not true else true == int(window)

stopped = ""
if harness.startswith("gnosh"):
    # How gnosh was run, and what it printed of itself (sessions.py).
    sys.path.insert(0, bench)
    import sessions
    # (mem_on and not mem: rescore.py gives a row "mem", mem's calls by name.)
    row.update({"mem_on": mem == "on", "memory_text": memtext, "thinking": thinking,
                "slot": int(slot) if slot else None, "provider_type": ptype})
    if side_slot:
        row["side_slot"] = int(side_slot)
    if max_turns:
        row["max_turns"] = int(max_turns)
    facts = sessions.facts(d)
    row.update(facts)
    if "error_max_turns" in facts["results"]:
        stopped = "turn cap"
if row["timed_out"]:
    stopped = "wall cap"
# A run that a cap ended was stopped: its check says how far it had come,
# not that the model failed.
row["outcome"] = "passed" if row["passed"] else ("stopped" if stopped else "failed")
if stopped:
    row["stopped_by"] = stopped
row.update(json.loads(summ))
print(json.dumps(row))
PY
