#!/bin/bash
# dryrun.sh: proves the bench's plumbing without a model.
#
# It builds gnosh from the tree this script is in, and stands the scripted
# model server (tools/dev/scriptmodel) where a model server would be. Each of
# the four tasks is then run through run.sh exactly as a real run would be:
# the scripted "model" makes a few tool calls and stops, the metering proxy
# counts what passed through it, and the task's own check scores what was
# left. The dry run holds every number it can to what the scripted server is
# known to have served (dryrun.py says how), runs the same short task the
# other ways a run can be made (without mem, with the fuller text about mem,
# thinking off, a pinned slot, a wall cap, a turn cap, a swarm), puts the
# rows through the scoring scripts, and checks at the end that every run
# kept to its own directory and that nothing is left running.
#
# No model is asked anything, and nothing outside this machine is reached.
# A failing score is the expected one: the scripted model solves no task,
# except that in the two-session task it is handed the solved files, so
# that a check is seen to pass too.
#
# Usage: bench/dryrun.sh
# Environment:
#   GO     the go binary (default: go on PATH). Go 1.27 or newer: it builds
#          gnosh, from the module cache alone
#   DRY    where to work (default: a new folder under ${TMPDIR:-/tmp}); it is
#          removed at the end, unless KEEP is set
#   KEEP   set to keep the folder, to look at what the runs left
#   ONLY   run only the parts named, of: selftest server tasks ways scoring
#
# Exit 0 when every check passed, 1 otherwise. Linux, bash, python3, git.
set -u
BENCH=$(cd "$(dirname "$0")" && pwd)
TREE=$(dirname "$BENCH")
GO=${GO:-$(command -v go)}
[ -x "$GO" ] || { echo "dryrun.sh: no go binary (set GO)" >&2; exit 2; }
for tool in python3 git timeout; do command -v "$tool" >/dev/null || { echo "dryrun.sh: $tool is needed" >&2; exit 2; }; done
[ -d /proc/self ] || { echo "dryrun.sh: this needs /proc, to see what is left running" >&2; exit 2; }
export GO PATH="$(dirname "$GO"):$PATH"
# The go command's caches stay where they are when HOME is moved, below.
GOCACHE=$("$GO" env GOCACHE); GOMODCACHE=$("$GO" env GOMODCACHE); GOPATH=$("$GO" env GOPATH)
export GOCACHE GOMODCACHE GOPATH

DRY=${DRY:-$(mktemp -d "${TMPDIR:-/tmp}/gnosh-bench-dryrun.XXXXXX")}
mkdir -p "$DRY/bin" "$DRY/runs" "$DRY/scripts" "$DRY/solved" "$DRY/home"
# Nothing is to be written into the tree, python's own files beside the
# scripts it runs included. What is newer than this file at the end was.
export PYTHONDONTWRITEBYTECODE=1
touch "$DRY/started"
started=$(date +%s)
bad=0; checks=0
models=()   # the scripted model servers started, by pid
say() { printf '\n== %s\n' "$*"; }
fail() { bad=$((bad + 1)); echo "  FAIL $*"; }
ok() { checks=$((checks + 1)); echo "  ok   $*"; }
want() { case " ${ONLY:-selftest server tasks ways scoring} " in *" $1 "*) return 0 ;; esac; return 1; }

cleanup() {
  for pid in "${models[@]:-}"; do [ -n "$pid" ] && kill "$pid" 2>/dev/null; done
  [ -n "${standin:-}" ] && kill "$standin" 2>/dev/null
  wait 2>/dev/null
  [ -n "${KEEP:-}" ] || rm -rf "$DRY"
}
trap cleanup EXIT

say "building gnosh and the scripted model server from $TREE"
id=dryrun-tree  # a tree that is no checkout (a copy, an archive) has no commit to name the build by
[ -e "$TREE/.git" ] && id=dryrun-$(git -C "$TREE" rev-parse --short=12 HEAD)
# The build may fetch nothing: neither a module nor a toolchain.
(cd "$TREE" && export GOPROXY=off GOTOOLCHAIN=local \
  && "$GO" build -ldflags "-X github.com/gnosh-ai/gnosh/internal/buildinfo.BuildID=$id" -o "$DRY/bin/gnosh" . \
  && "$GO" build -o "$DRY/bin/scriptmodel" ./tools/dev/scriptmodel) \
  || { echo "dryrun.sh: the build failed. It is made with GOPROXY=off, so that nothing is fetched: the module cache must already hold what gnosh is built from" >&2; exit 1; }
echo "  $("$DRY/bin/gnosh" --version)"

# The dry run's own home: nothing gnosh does may land in it, since every run
# has a home of its own. It is looked at again at the end.
export HOME="$DRY/home"
export LANE=dryrun MODEL=test-model WINDOW=65536 GNOSH_BIN="$DRY/bin/gnosh" TIMEOUT=300
export RUNS="$DRY/runs" RESULTS="$DRY/results.jsonl" DUMP=1

if want selftest; then
  say "the tasks' own checks: each fails on the task as built and passes on the solved copy"
  if out=$(bash "$BENCH/tasks/selftest.sh" 2>&1); then ok "tasks/selftest.sh"; else fail "tasks/selftest.sh"; fi
  echo "$out" | sed 's/^/       /'
fi

if want server; then
  say "serverfacts.py against stand-ins for the three kinds of server"
  python3 "$BENCH/dryrun.py" standin "$DRY/standin.port" & standin=$!
  for _ in $(seq 100); do [ -s "$DRY/standin.port" ] && break; sleep 0.1; done
  S=http://127.0.0.1:$(cat "$DRY/standin.port")
  facts() { python3 "$BENCH/serverfacts.py" "$@" 2>&1; }
  field() { python3 -c 'import json, sys; print(json.dumps(json.loads(sys.argv[1]).get(sys.argv[2])))' "$1" "$2"; }
  is() { if [ "$2" = "$3" ]; then ok "$1: $2"; else fail "$1: $2, want $3"; fi; }
  f=$(facts "$S/plain" qwen)
  is "one llama.cpp model: the window is the slot's, from /props" "$(field "$f" window_server) $(field "$f" window_source)" '32768 "props.n_ctx"'
  f=$(facts --gate --idle-slots 2 "$S/plain" qwen); is "its gate with two slots idle" "$? $(field "$f" idle_slots)" "0 2"
  f=$(facts --gate --idle-slots 3 "$S/plain" qwen); is "its gate when three are wanted" "$?" "1"
  f=$(facts --gate "$S/plain" other); is "its gate for a model it does not list" "$?" "1"
  f=$(facts "$S/router" qwen)
  is "a router: loaded, and no window to be had" "$(field "$f" router) $(field "$f" status) $(field "$f" window_server)" 'true "loaded" null'
  f=$(facts --gate --idle-slots 1 "$S/router" qwen); is "its gate for the loaded model, one slot idle" "$? $(field "$f" idle_slots)" "0 1"
  f=$(facts --gate --idle-slots 1 "$S/router" other); is "its gate for a model that is not loaded" "$? $(field "$f" idle_slots)" "1 null"
  f=$(facts "$S/vllm" qwen); is "vLLM: the window from the list" "$(field "$f" window_server) $(field "$f" window_source)" '131072 "models.max_model_len"'
  f=$(facts http://127.0.0.1:9 qwen); is "no server: said, not thrown" "$? $(field "$f" model_listed)" "0 false"
  asked=$(python3 -c 'import json, sys, urllib.request; print(" ".join(json.load(urllib.request.urlopen(sys.argv[1] + "/asked"))))' "$S")
  # The router was asked for its list, and for the slots of the model it
  # calls loaded, with autoload off: never for /props, and never about the
  # model that is not loaded.
  case "$asked" in *"/router/props"*|*"model=other"*) fail "the router was asked something that could load a model: $asked" ;; *) ok "the router was asked nothing that could load a model" ;; esac
  kill "$standin" 2>/dev/null; wait "$standin" 2>/dev/null; standin=""
fi

# one <script name> <harness> [check option...]: a run of run.sh against a
# scripted model server of its own, then the check of what it left.
# Variables set before the call (MEM=off one ...) reach run.sh alone.
runs=0
one() {
  local name=$1 harness=$2; shift 2
  runs=$((runs + 1))
  local s=$DRY/scripts/$runs-$name
  python3 "$BENCH/dryrun.py" script "$name" "$DRY/solved" "$s.json" "$s.expect" || { fail "$name: its script could not be written"; return; }
  rm -f "$s.url"
  "$DRY/bin/scriptmodel" "$s.json" > "$s.url" 2> "$s.err" &
  local model=$!; models+=("$model")
  for _ in $(seq 100); do [ -s "$s.url" ] && break; sleep 0.1; done
  local task before after dir
  task=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["task"])' "$s.expect")
  before=$(ls "$RUNS")
  UPSTREAM=$(cat "$s.url") bash "$BENCH/run.sh" "$harness" "$task" "$runs" > "$s.row" 2> "$s.runerr"
  local rc=$?
  kill "$model" 2>/dev/null; wait "$model" 2>/dev/null
  after=$(ls "$RUNS")
  dir=$(comm -13 <(echo "$before") <(echo "$after") | head -1)
  echo
  if [ $rc != 0 ] || [ -z "$dir" ] || [ ! -s "$RUNS/$dir/row.json" ]; then
    fail "$name: run.sh exited $rc and left no row: $(head -3 "$s.runerr" | tr '\n' ' ')"; return
  fi
  local n
  python3 "$BENCH/dryrun.py" check "$RUNS/$dir" "$s.expect" "$@" > "$s.check" 2>&1; rc=$?
  cat "$s.check"
  n=$(grep -c '^  ok  ' "$s.check"); checks=$((checks + n))
  n=$(grep -c '^  FAIL' "$s.check"); bad=$((bad + n))
  [ $rc != 0 ] && [ "$n" = 0 ] && fail "$name: the check itself broke"
  last=$RUNS/$dir
}

if want tasks || want ways; then
  # The solved copies: the scripted "model" is handed a file from them
  # where a run is to show a part of its task done.
  for t in rename algos bugs continue; do
    bash "$BENCH/tasks/$t/make.sh" --solved "$DRY/solved/$t" > /dev/null 2>&1 || { echo "dryrun.sh: tasks/$t/make.sh --solved failed" >&2; exit 1; }
  done
fi

LEAN=$TREE/internal/prompt/texts/memory.md
FULL=$TREE/internal/prompt/texts/memory-full.md

if want tasks; then
  say "the four tasks, each run once as a real run would be"
  export LANE=dryrun
  for t in rename algos bugs continue; do
    one "$t" gnosh harness=gnosh memory="$LEAN" thinking=on
  done
fi

if want ways; then
  say "the same short run, the ways a run can be made"
  export LANE=dryrun-ways
  one short gnosh-nomem mem=off harness=gnosh-nomem not_memory="$LEAN,$FULL"
  one planned gnosh-full harness=gnosh-full memory="$FULL"
  THINKING=off one short gnosh thinking=off harness=gnosh
  PROVIDER_TYPE=openai-compatible THINKING=off one short gnosh provider=openai-compatible thinking=off
  SLOT=2 SIDE_SLOT=1 one planned gnosh slot=2 side_slot=1
  # The switches of a prompt experiment: a text added to the system prompt,
  # one added to the prompt, the user's own prompt texts, and their names
  # in the row.
  mkdir -p "$DRY/exp/fulltext"; cp "$FULL" "$DRY/exp/fulltext/memory.md"
  echo "Work in small steps." > "$DRY/exp/append.txt"; echo " Say when you are done." > "$DRY/exp/suffix.txt"
  GNOSH_APPEND_FILE=$DRY/exp/append.txt PROMPT_SUFFIX_FILE=$DRY/exp/suffix.txt VARIANT=steps GNOSH_PROMPTS=$DRY/exp/fulltext REASONING=low \
    one short gnosh harness=gnosh memory="$FULL" appended="$DRY/exp/append.txt" suffix="$DRY/exp/suffix.txt" variant=steps prompts=fulltext
  CONTINUE_MODE=new one fresh gnosh session_files=2
  # A switch that cannot be used stops the run before anything is started.
  before=$(ls "$RUNS" | wc -l)
  if MEMORY_TEXT=full GNOSH_PROMPTS=$DRY/exp/fulltext UPSTREAM=http://127.0.0.1:9 bash "$BENCH/run.sh" gnosh rename 0 2> "$DRY/refused.err" \
     || MEM=maybe UPSTREAM=http://127.0.0.1:9 bash "$BENCH/run.sh" gnosh rename 0 2>> "$DRY/refused.err"; then
    fail "run.sh went ahead with switches it cannot use"
  elif [ "$(ls "$RUNS" | wc -l)" = "$before" ] && [ "$(wc -l < "$DRY/refused.err")" = 2 ]; then
    ok "two switches that cannot be used were refused, each with a line saying why, before a run directory was made"
  else
    fail "a refused run left something behind: $(cat "$DRY/refused.err")"
  fi
  MAX_TURNS=2 one long gnosh outcome=stopped stopped_by="turn cap" turn_cap=2 exit_codes=1
  TIMEOUT=6 one sleeper gnosh outcome=stopped stopped_by="wall cap" exit_codes=124
  # The command the stopped session had started is gone with it.
  left=""
  for p in /proc/[0-9]*; do
    [ "$(readlink "$p/cwd" 2>/dev/null)" = "$last/work" ] && left="$left ${p#/proc/}"
  done
  if [ -z "$left" ]; then ok "nothing the stopped session started is still running"; else fail "still running in the stopped run's folder: pids$left"; fi
  # A cap on the whole run: it stops the first session though that session's
  # own cap is far off, and the second session is not started.
  RUN_TIMEOUT=3 one sleeper2 gnosh outcome=stopped stopped_by="wall cap" exit_codes=124 started=1
  SWARM=2 one swarm gnosh-swarm harness=gnosh-swarm
  if [ -n "$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1])).get("planned_s") or "")' "$last/row.json")" ]; then
    ok "the swarm's helper started once the plan was in mem"
  else
    fail "the swarm's plan was never seen: $(cat "$last/row.json")"
  fi

  say "matrix.sh: two ways of running gnosh side by side, once the gate lets them start"
  # Both runs are given the same one reply, so that it does not matter
  # which of them the scripted server answers first.
  s=$DRY/scripts/matrix
  printf '[{"text": "Nothing done in this dry run."}, {"text": "Nothing done in this dry run."}]\n' > "$s.json"
  "$DRY/bin/scriptmodel" "$s.json" > "$s.url" 2> "$s.err" & model=$!; models+=("$model")
  for _ in $(seq 100); do [ -s "$s.url" ] && break; sleep 0.1; done
  {
    echo "export LANE=dryrun-matrix UPSTREAM=$(cat "$s.url") HARNESSES=\"gnosh gnosh-nomem\" MATRIX_LOG=$s.log"
    echo "export MATRIX_GATE=\"python3 $BENCH/serverfacts.py --gate $(cat "$s.url") test-model\""
  } > "$s.env"
  bash "$BENCH/matrix.sh" "$s.env" 1 rename
  kill "$model" 2>/dev/null; wait "$model" 2>/dev/null
  got=$(python3 -c 'import json, sys; rows = [r for r in map(json.loads, open(sys.argv[1])) if r["lane"] == "dryrun-matrix"]; print(sorted((r["harness"], r["requests"], r["outcome"]) for r in rows))' "$RESULTS")
  if [ "$got" = "[('gnosh', 1, 'failed'), ('gnosh-nomem', 1, 'failed')]" ] && grep -q "MATRIX DONE dryrun-matrix" "$s.log"; then
    ok "matrix.sh ran gnosh and gnosh-nomem on the task, one row and one request each"
  else
    fail "matrix.sh: rows $got; its log: $(tail -3 "$s.log" 2>/dev/null | tr '\n' ' ')"
  fi
  runs=$((runs + 2))

  say "a summary, counted by the proxy and by gnosh alike"
  # Two prompts in one session, the second the command that summarizes:
  # the proxy must count the request it makes as a summary, by the words
  # gnosh's own text begins with, and the session file must hold one.
  s=$DRY/scripts/summary; d=$DRY/runs/summary; mkdir -p "$d/home/.config/gnosh" "$d/work"
  printf '[{"text": "A first answer."}, {"text": "The user asked for a first answer and got one."}]\n' > "$s.json"
  "$DRY/bin/scriptmodel" "$s.json" > "$s.url" 2> "$s.err" & model=$!; models+=("$model")
  for _ in $(seq 100); do [ -s "$s.url" ] && break; sleep 0.1; done
  python3 "$BENCH/proxy.py" serve --listen 127.0.0.1:0 --port-file "$d/port" --upstream "$(cat "$s.url")" \
    --model test-model --window 65536 --log "$d/proxy.jsonl" > "$d/proxy.err" 2>&1 & proxy=$!
  echo "$proxy" > "$d/proxy.pid"
  for _ in $(seq 100); do [ -s "$d/port" ] && break; sleep 0.1; done
  printf '{"providers": {"box": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:%s", "listModels": false}},\n "models": {"box/test-model": {"contextWindow": 65536}},\n "model": "box/test-model"}\n' "$(cat "$d/port")" > "$d/home/.config/gnosh/gnosh.json"
  g=(env -i PATH="$PATH" HOME="$d/home" XDG_CONFIG_HOME="$d/home/.config" XDG_DATA_HOME="$d/home/.local/share" XDG_CACHE_HOME="$d/home/.cache" TERM=dumb
     "$DRY/bin/gnosh" -p -y --cwd "$d/work" --output-format stream-json)
  "${g[@]}" -- "Say something." < /dev/null > "$d/session-1.out" 2> "$d/session-1.err"
  "${g[@]}" --continue -- "/compact" < /dev/null > "$d/session-2.out" 2> "$d/session-2.err"
  kill "$proxy" "$model" 2>/dev/null; wait "$proxy" "$model" 2>/dev/null
  sum=$(python3 "$BENCH/proxy.py" summarize "$d/proxy.jsonl")
  got="$(python3 -c 'import json, sys; s = json.loads(sys.argv[1]); print(s["requests"], s["summaries"])' "$sum") $(python3 "$BENCH/analyze.py" "$d" | python3 -c 'import json, sys; print(json.load(sys.stdin).get("summary_messages"))')"
  if [ "$got" = "2 1 1" ]; then ok "two requests, one of them a summary; one summary in the session file"; else fail "requests, summaries by the proxy, summaries in the session file: $got, want 2 1 1 ($(head -2 "$d/session-2.err" | tr '\n' ' '))"; fi
  # What started it: /compact is manual, in the session file (analyze.py) and
  # in stream-json's compact_boundary alike.
  got="$(python3 "$BENCH/analyze.py" "$d" | python3 -c 'import json, sys; a = json.load(sys.stdin); print(a.get("summaries_manual"), a.get("summaries_auto"), a.get("summary_pre_tokens_max", 0) > 0)') $(python3 -c '
import json, sys
n = 0
for line in open(sys.argv[1]):
    try:
        e = json.loads(line)
    except ValueError:
        continue
    if e.get("subtype") == "compact_boundary" and e.get("compact_metadata", {}).get("trigger") == "manual":
        n += 1
print(n)' "$d/session-2.out")"
  if [ "$got" = "1 0 True 1" ]; then ok "the /compact summary counts as manual, with its size, and stream-json says the same"; else fail "manual and auto summaries in the session file, a size, manual compact_boundary lines: $got, want 1 0 True 1"; fi
  # The words the proxy tells a summary request by are the first words of
  # gnosh's own text for it: if that text changes, this says so.
  if grep -q "about to be replaced by a summary" "$TREE/internal/prompt/texts/summary.md"; then ok "gnosh's text for a summary still begins as the proxy expects"; else fail "internal/prompt/texts/summary.md no longer says what proxy.py's SUMMARY_RE looks for"; fi
fi

if want scoring; then
  say "the rows, through the scoring scripts"
  rows=$(wc -l < "$RESULTS" 2>/dev/null | tr -d ' ')
  if [ "${rows:-0}" -gt 0 ]; then
    dirs=$(python3 -c 'import json, sys; print(" ".join(json.loads(l)["dir"] for l in open(sys.argv[1])))' "$RESULTS")
    step() { local what=$1; shift; if "$@" > "$DRY/step.out" 2> "$DRY/step.err"; then ok "$what"; else fail "$what: $(head -3 "$DRY/step.err" | tr '\n' ' ')"; fi; }
    step "rescore.py recounts $rows rows" python3 "$BENCH/rescore.py" "$RESULTS"; cp "$DRY/step.out" "$DRY/rows.jsonl"
    [ "$(wc -l < "$DRY/rows.jsonl" | tr -d ' ')" = "$rows" ] || fail "rescore.py wrote $(wc -l < "$DRY/rows.jsonl") rows of $rows"
    grep -q analyze_error "$DRY/rows.jsonl" && fail "rescore.py could not read a run's sessions: $(grep -o 'analyze_error[^,]*' "$DRY/rows.jsonl" | head -1)"
    # A recounted row keeps how the run was made, and gains what the model
    # did: its tool calls by name, mem's calls by name, the todos popped.
    if python3 -c 'import json, sys
rows = [json.loads(line) for line in open(sys.argv[1])]
kept = all(isinstance(r["mem_on"], bool) and r["outcome"] in ("passed", "failed", "stopped") for r in rows)
gained = all(isinstance(r.get("tools"), dict) and isinstance(r.get("mem", {}), dict) and "pops" in r and "gnovm_runs" in r for r in rows)
sys.exit(0 if kept and gained else 1)' "$DRY/rows.jsonl"; then
      ok "a recounted row keeps how the run was made and gains what the model did"
    else
      fail "a recounted row lacks a field: $(head -c 600 "$DRY/rows.jsonl")"
    fi
    step "table.py" python3 "$BENCH/table.py" "$DRY/rows.jsonl"; cp "$DRY/step.out" "$DRY/table.md"
    step "compare.py" python3 "$BENCH/compare.py" "$DRY/rows.jsonl"; cp "$DRY/step.out" "$DRY/compare.md"
    step "rounds.py" python3 "$BENCH/rounds.py" "$DRY/rows.jsonl"
    # shellcheck disable=SC2086
    step "reads.py" python3 "$BENCH/reads.py" $dirs
    # shellcheck disable=SC2086
    step "memerrors.py" python3 "$BENCH/memerrors.py" $dirs; cp "$DRY/step.out" "$DRY/memerrors.txt"
    # shellcheck disable=SC2086
    step "analyze.py" python3 "$BENCH/analyze.py" $dirs
    step "prefixdiff.py" python3 "$BENCH/prefixdiff.py" "${dirs%% *}"
    if want tasks; then
      grep -q "close refused by the close review" "$DRY/memerrors.txt" && ok "memerrors.py names the refused close" || fail "memerrors.py did not name the refused close: $(cat "$DRY/memerrors.txt")"
      grep -q '^| gnosh | 1/4 ' "$DRY/table.md" && ok "table.py: gnosh passed 1 of the 4 tasks (the one it was handed)" || fail "table.py's line for gnosh: $(grep '^| gnosh ' "$DRY/table.md" | head -1)"
    fi
    if want ways; then
      grep -q -- "- STOPPED gnosh r[0-9]* (wall cap)" "$DRY/table.md" && ok "table.py lists the capped run as stopped, not failed" || fail "table.py does not list the capped run as stopped"
      grep -q "| gnosh-nomem |" "$DRY/table.md" && grep -q "| gnosh-full |" "$DRY/table.md" && ok "table.py tells the three ways of running gnosh apart" || fail "table.py lacks a line for gnosh-nomem or gnosh-full"
    fi
    echo; sed 's/^/       /' "$DRY/table.md" | sed -n '1,14p'
  else
    fail "there are no rows to score"
  fi
fi

say "every run kept to its own directory, and nothing is left running"
n=$(find "$RUNS" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')
homes=$(find "$RUNS" -mindepth 2 -maxdepth 2 -type d -name home | wc -l | tr -d ' ')
[ "$n" = "$homes" ] && ok "$n run directories, each with a home of its own" || fail "$n run directories and $homes homes"
stray=$(find "$HOME" -mindepth 1 \( -name gnosh -o -name '.gnosh' \) 2>/dev/null | head -3)
[ -z "$stray" ] && ok "nothing of gnosh's is in the home the dry run itself ran under" || fail "gnosh wrote outside its run's home: $stray"
written=$(find "$TREE" -newer "$DRY/started" -not -path "$TREE/.git/*" -not -path "$DRY/*" 2>/dev/null | head -3)
[ -z "$written" ] && ok "nothing was written into the tree" || fail "written into the tree while the dry run ran: $(echo $written)"
alive=""
for f in "$RUNS"/*/proxy.pid; do [ -f "$f" ] && kill -0 "$(cat "$f")" 2>/dev/null && alive="$alive $(cat "$f")"; done
for pid in "${models[@]:-}"; do [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && alive="$alive $pid"; done
for p in /proc/[0-9]*; do  # anything still running one of the dry run's own binaries: gnosh, its GnoVM worker, the scripted server
  case "$(readlink "$p/exe" 2>/dev/null)" in "$DRY"/bin/*) alive="$alive ${p#/proc/}" ;; esac
done
[ -z "$alive" ] && ok "no proxy, no scripted server, no gnosh and no GnoVM worker is left running" || fail "still running: pids$alive"

took=$(( $(date +%s) - started ))
trap - EXIT; cleanup
if [ -z "${KEEP:-}" ]; then
  [ ! -e "$DRY" ] && ok "the run directories are removed ($DRY)" || fail "$DRY is still there"
else
  echo "  kept: $DRY"
fi
echo
if [ $bad = 0 ]; then
  echo "dry run: ok. $checks checks passed, $runs runs through run.sh, ${took} s. No model was asked anything."
  exit 0
fi
echo "dry run: $bad of $((checks + bad)) checks FAILED (${took} s)."
exit 1
