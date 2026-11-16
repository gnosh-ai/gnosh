#!/bin/bash
# An interactive check of the installed gnosh on an AI box, with the box's
# model (Qwen alone: the router holds one model), in a tmux session of its
# own and a scratch home, with a status line command configured: the screen
# before and after one scenario. It touches no file of the person's, and
# ends its own tmux session by name. Run it on the box:
#
#	ssh ai-remote bash -s < tools/dev/box-ui-check.sh            # basic
#	ssh ai-remote bash -s -- plan < tools/dev/box-ui-check.sh
#	ssh ai-remote bash -s -- background < tools/dev/box-ui-check.sh
#	ssh ai-remote bash -s -- btw < tools/dev/box-ui-check.sh
#	ssh ai-remote bash -s -- mention < tools/dev/box-ui-check.sh
#	ssh ai-remote bash -s -- config < tools/dev/box-ui-check.sh
#	ssh ai-remote bash -s -- hookwake < tools/dev/box-ui-check.sh
#	ssh ai-remote bash -s -- agent < tools/dev/box-ui-check.sh
#	ssh ai-remote bash -s -- fork < tools/dev/box-ui-check.sh
#	ssh ai-remote bash -s -- monitor < tools/dev/box-ui-check.sh
#	ssh ai-remote bash -s -- worktree < tools/dev/box-ui-check.sh
#	ssh ai-remote bash -s -- panel < tools/dev/box-ui-check.sh
#
#   basic       one short turn
#   plan        plan mode: the model presents a plan with ExitPlanMode, and
#               the question is refused (nothing is written)
#   background  a sub-agent in the background, whose report wakes the
#               session (permissions skipped, in the scratch folder)
#   btw         a side question (/btw) asked while a slow command runs:
#               answered beside the turn, which goes on (permissions
#               skipped, in the scratch folder)
#   mention     a file mentioned with @: read in front of the model with
#               the prompt, and said so under it
#   config      /config lists the options, sets one in the scratch state
#               file and says it is in force from the next start (no model)
#   hookwake    a Stop hook in the background ("asyncRewake") exits with 2
#               after the first turn and wakes the model, which answers it
#   agent       the session runs as the Explore agent (--agent): its prompt
#               and its read-only tools
#   fork        a skill with context: fork, typed: a sub-agent counts the
#               lines of notes.txt, and the model answers its report
#   monitor     the model watches a command with Monitor: its lines come as
#               notes, then the news that it ended (permissions skipped, in
#               the scratch folder)
#   panel       /panel takes the whole screen: the model writes hello.txt,
#               and the panel lists it under the files changed (permissions
#               skipped, in the scratch folder)
#   worktree    the scratch folder is a git repository, trusted: a sub-agent
#               with isolation worktree adds a line to notes.txt in a work
#               tree of its own, and the folder's notes.txt stays as it was
#               (acceptEdits)
#   wtflag      the same repository, and gnosh started with --worktree demo:
#               the session adds a line to notes.txt in .gnosh/worktrees/demo,
#               and the folder's notes.txt stays as it was (acceptEdits)
#   secreview   a branch that adds a handler with a shell injection:
#               /security-review should find it
#   vim         editorMode vim: a prompt typed in INSERT mode, Esc, x in
#               NORMAL mode, and Enter sends it from NORMAL mode
#   restart     a copy of the installed gnosh, replaced by rename after the
#               first turn as an install does it: the next turn's end says a
#               newer gnosh is installed, /restart starts it in the same
#               session, and the model still knows the first turn
#   keys        a keybindings.json: Ctrl+K stashes the box and brings it
#               back, Ctrl+Y runs /cost, and the prompt then goes out
#   bgsession   /background: the session ends in the pane and goes on as a
#               run of its own, whose log gets the model's answer; once the
#               run lets go of it, gnosh -r opens it again and shows where
#               it left off
#
# The full interface is tested in CI with a scripted model; this is the
# look at it with a real one, which is how a model writing gnosh's tags
# into its own reply was found.
set -u
scenario=${1:-basic}
T=$HOME/newgnosh-test/uicheck-$scenario
S=gnosh-uicheck-$scenario
flags=""
hooks=""
opts=""
case "$scenario" in
basic | mention | config | fork | model | transcript | restart | keys | bgsession) ;;
vim) opts='"editorMode": "vim", ' ;;
secreview) flags="--trust-folder" ;;
agent) flags="--agent Explore" ;;
plan) flags="--permission-mode plan" ;;
background | btw | monitor | panel) flags="-y" ;;
worktree) flags="--permission-mode acceptEdits --trust-folder" ;;
wtflag) flags="--permission-mode acceptEdits --trust-folder --worktree demo" ;;
hookwake) hooks="\"hooks\": { \"Stop\": [ { \"hooks\": [ { \"type\": \"command\", \"asyncRewake\": true, \"command\": \"$T/config/gnosh/wake.sh\" } ] } ] }," ;;
*)
	echo "box-ui-check: no scenario $scenario (basic, plan, background, btw, mention, config, hookwake, agent, fork, monitor, worktree, wtflag, panel, model, transcript, vim, secreview, restart, keys, bgsession)" >&2
	exit 2
	;;
esac
tmux kill-session -t "$S" 2>/dev/null
rm -rf "$T"
mkdir -p "$T/home" "$T/config/gnosh" "$T/data" "$T/cache" "$T/work"
cat > "$T/config/gnosh/statusline.sh" <<'EOF'
#!/bin/sh
in=$(cat)
model=$(printf '%s' "$in" | sed -n 's/.*"model":{"id":"\([^"]*\)".*/\1/p')
pct=$(printf '%s' "$in" | sed -n 's/.*"used_percentage":\([0-9]*\).*/\1/p')
printf 'SL %s ctx=%s%% cols=%s\n' "$model" "${pct:-?}" "$COLUMNS"
EOF
chmod 755 "$T/config/gnosh/statusline.sh"
cat > "$T/config/gnosh/wake.sh" <<EOF
#!/bin/sh
cat > /dev/null
[ -e "$T/woke" ] && exit 0
touch "$T/woke"
echo 'HOOK-WAKE: the build check failed. Reply with the one word pineapple and nothing else.' >&2
exit 2
EOF
chmod 755 "$T/config/gnosh/wake.sh"
cat > "$T/config/gnosh/gnosh.json" <<EOF
{
  "providers": { "qwen": { "type": "llamacpp", "baseUrl": "http://127.0.0.1:8080", "listModels": false } },
  "models": { "qwen/qwen3.8-27b": { "name": "Qwen 3.8 27B", "contextWindow": 262144 } },
  "model": { "id": "qwen/qwen3.8-27b", "thinking": false },
  "options": { $opts"requestTimeout": 900 },
  $hooks
  "statusLine": { "type": "command", "command": "$T/config/gnosh/statusline.sh", "padding": 1 }
}
EOF
printf 'alpha\nbeta\ngamma\n' > "$T/work/notes.txt"
if [ "$scenario" = keys ]; then
	cat > "$T/config/gnosh/keybindings.json" <<'EOF'
{"bindings": [{"context": "Chat", "bindings": {"ctrl+k": "chat:stash", "ctrl+y": "command:cost"}}]}
EOF
fi
# The skill only where it is used: it is about notes.txt, and a model
# asked to change that file in another scenario loads it.
if [ "$scenario" = fork ]; then
	mkdir -p "$T/config/gnosh/skills/countlines"
	cat > "$T/config/gnosh/skills/countlines/SKILL.md" <<'EOF'
---
description: Counts the lines of a file in a sub-agent.
context: fork
---
Count the lines of the file notes.txt in the working directory. Report the count as one number.
EOF
fi
cd "$T/work" || exit 1
if [ "$scenario" = worktree ] || [ "$scenario" = wtflag ]; then
	git init -q && git -c user.name=check -c user.email=check@example.com add notes.txt &&
		git -c user.name=check -c user.email=check@example.com commit -q -m notes || exit 1
fi
if [ "$scenario" = secreview ]; then
	# A branch that adds a handler putting a query parameter into a shell
	# command line: the one real way in for the review to find.
	g() { git -c user.name=check -c user.email=check@example.com "$@"; }
	printf 'module thumbs\n\ngo 1.22\n' > go.mod
	g init -q -b main && g add go.mod notes.txt && g commit -q -m base || exit 1
	g checkout -q -b thumbnails || exit 1
	cat > handler.go <<'EOF'
package thumbs

import (
	"net/http"
	"os/exec"
)

// Thumb makes a thumbnail of the file named in the request.
func Thumb(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("file")
	out, err := exec.Command("sh", "-c", "convert uploads/"+name+" -resize 64x64 png:-").Output()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(out)
}
EOF
	g add handler.go && g commit -q -m "thumbnails" || exit 1
fi
bin=$HOME/.local/bin/gnosh
if [ "$scenario" = restart ]; then
	# A copy of its own, so that "installing" over it touches nothing else.
	mkdir -p "$T/bin" && cp "$bin" "$T/bin/gnosh" || exit 1
	bin=$T/bin/gnosh
fi
envs="env -i HOME=$T/home XDG_CONFIG_HOME=$T/config XDG_DATA_HOME=$T/data XDG_CACHE_HOME=$T/cache PATH=/usr/local/bin:/usr/bin:/bin TERM=tmux-256color LANG=C.UTF-8"
tmux new-session -d -s "$S" -x 110 -y 40 "$envs $bin $flags"
sleep 4
echo "=== before"
tmux capture-pane -p -t "$S"
screen() { tmux capture-pane -p -S -80 -t "$S"; }
case "$scenario" in
basic)
	tmux send-keys -t "$S" "Reply with exactly these three words: all is well" Enter
	done_when() { [ "$(screen | grep -c -i "all is well")" -ge 2 ] && screen | grep -q "ctx=[0-9]"; }
	;;
plan)
	tmux send-keys -t "$S" "Create a file hello.txt that says hi. Plan it first, then present the plan for approval." Enter
	done_when() { screen | grep -q "Carry out this plan"; }
	;;
background)
	tmux send-keys -t "$S" "Call the Agent tool with run_in_background set to true, to have a sub-agent count the lines of notes.txt. Then end your turn; when its report comes, tell me the count." Enter
	done_when() { screen | grep -q "background sub-agent finished" && [ "$(screen | grep -c "done in")" -ge 2 ]; }
	;;
mention)
	tmux send-keys -t "$S" "What is the second line of @notes.txt? Answer with that one word and call no tool." Enter
	done_when() { screen | grep -q "@notes.txt read" && screen | grep -q "done in"; }
	;;
config)
	tmux send-keys -t "$S" "/config" Enter
	sleep 2
	tmux send-keys -t "$S" "/config memoryText=full" Enter
	sleep 2
	tmux send-keys -t "$S" "/config memoryText" Enter
	done_when() { screen | grep -q "set with /config to full: in force when gnosh next starts"; }
	;;
agent)
	tmux send-keys -t "$S" "Which files are in this folder? Answer in one line." Enter
	done_when() { screen | grep -q "notes.txt" && screen | grep -q "done in"; }
	;;
fork)
	tmux send-keys -t "$S" "/countlines" Enter
	done_when() { screen | grep -q "ran in a sub-agent" && [ "$(screen | grep -c "done in")" -ge 1 ]; }
	;;
model)
	# The picker only: Esc keeps the model, so the router swaps nothing.
	tmux send-keys -t "$S" "/model" Enter
	done_when() { screen | grep -q "Which model?"; }
	;;
transcript)
	tmux send-keys -t "$S" "Reply with exactly these three words: all is well" Enter
	for i in $(seq 1 300); do
		sleep 1
		screen | grep -q "done in" && break
	done
	tmux send-keys -t "$S" C-o
	done_when() { screen | grep -q "transcript ·"; }
	;;
panel)
	tmux send-keys -t "$S" "/panel" Enter
	sleep 2
	tmux send-keys -t "$S" "Create the file hello.txt that says hi, with the Write tool, and say done." Enter
	done_when() { screen | grep -q "Files changed" && screen | grep -q "hello.txt +1" && screen | grep -q "done in"; }
	;;
worktree)
	tmux send-keys -t "$S" "Call the Agent tool with isolation set to worktree, to have a sub-agent add the line delta at the end of notes.txt. Then tell me where its change is." Enter
	done_when() { screen | grep -q "work tree" && screen | grep -q "done in"; }
	;;
wtflag)
	tmux send-keys -t "$S" "Add the line delta at the end of notes.txt, then say where notes.txt is." Enter
	done_when() { screen | grep -q "done in"; }
	;;
secreview)
	tmux send-keys -t "$S" "/security-review" Enter
	done_when() { screen | grep -q "done in"; }
	;;
vim)
	# Typed in INSERT mode, Esc to NORMAL (the status line says so), x takes
	# the stray last letter, and Enter sends from NORMAL mode; the empty box
	# is in INSERT mode again.
	tmux send-keys -t "$S" "Reply with exactly these three words: all is wellX" Escape
	sleep 1
	echo "=== after Esc: $(screen | grep -E -o -- "-- (NORMAL|INSERT|VISUAL) --" | tail -1)"
	tmux send-keys -t "$S" x
	sleep 1
	tmux send-keys -t "$S" Enter
	done_when() { [ "$(screen | grep -c -i "all is well")" -ge 2 ] && screen | grep -q "done in" && screen | grep -q -- "-- INSERT --"; }
	;;
monitor)
	tmux send-keys -t "$S" "Use the Monitor tool to watch this exact command: for i in 1 2 3; do echo tick \$i; sleep 3; done; echo MONITOR-DONE. Then end your turn. When its lines come, say only the last line you were given." Enter
	done_when() { screen | grep -q "passed on" && screen | grep -q "background command ended" && [ "$(screen | grep -c "done in")" -ge 2 ]; }
	;;
restart)
	tmux send-keys -t "$S" "Reply with exactly these three words: all is well" Enter
	for i in $(seq 1 300); do
		sleep 1
		screen | grep -q "done in" && break
	done
	cp "$bin" "$bin.new" && mv "$bin.new" "$bin"
	tmux send-keys -t "$S" "Reply with exactly the one word ready, and call no tool." Enter
	for i in $(seq 1 300); do
		sleep 1
		screen | grep -q "A newer gnosh has been installed" && break
	done
	echo "=== the newer gnosh said: $(screen | grep -c "A newer gnosh has been installed") time(s)"
	tmux send-keys -t "$S" "/restart" Enter
	sleep 8
	echo "=== after /restart"
	screen | tail -12
	tmux send-keys -t "$S" "What were the three words I first asked you to reply with? Answer with only those three words and call no tool." Enter
	done_when() { [ "$(screen | grep -c -i "all is well")" -ge 3 ] && [ "$(screen | grep -c "done in")" -ge 3 ]; }
	;;
bgsession)
	# The pane stays when gnosh ends, so that what it says last is seen.
	tmux set-option -t "$S" remain-on-exit on >/dev/null
	tmux send-keys -t "$S" "Reply with exactly the one word ready, and call no tool." Enter
	for i in $(seq 1 300); do
		sleep 1
		screen | grep -q "done in" && break
	done
	tmux send-keys -t "$S" "/background Reply with exactly these three words: all is well" Enter
	# (The log's first line holds the prompt: the answer is a line of its own.)
	done_when() { grep -qix "all is well" "$T"/data/gnosh/projects/*/*.background.log 2>/dev/null; }
	;;
keys)
	tmux send-keys -t "$S" "Reply with exactly these three words: all is well"
	sleep 1
	tmux send-keys -t "$S" C-k
	sleep 1
	echo "=== after Ctrl+K the box holds: $(screen | grep -c "Reply with exactly these three words") line(s) of the prompt"
	tmux send-keys -t "$S" C-y
	sleep 2
	tmux send-keys -t "$S" C-k
	sleep 1
	tmux send-keys -t "$S" Enter
	done_when() { screen | grep -q "Nothing has been asked of a model" && [ "$(screen | grep -c -i "all is well")" -ge 2 ] && screen | grep -q "done in"; }
	;;
hookwake)
	tmux send-keys -t "$S" "Reply with exactly these three words: all is well" Enter
	done_when() { screen | grep -q "HOOK-WAKE" && [ "$(screen | grep -c "done in")" -ge 2 ]; }
	;;
btw)
	tmux send-keys -t "$S" "Run this exact shell command with the Bash tool and nothing else: sleep 40 && echo slept" Enter
	for i in $(seq 1 120); do
		sleep 1
		screen | grep -q "sleep 40" && break
	done
	sleep 3
	tmux send-keys -t "$S" "/btw what command is running right now?" Enter
	done_when() { screen | grep -A4 "/btw what command" | tail -n +2 | grep -q -i "sleep" && screen | grep -q "done in"; }
	;;
esac
for i in $(seq 1 300); do
	sleep 1
	if done_when; then
		sleep 2
		break
	fi
done
echo "=== after ($i s)"
screen
if [ "$scenario" = bgsession ]; then
	echo "=== the run's log: $(cat "$T"/data/gnosh/projects/*/*.background.log 2>/dev/null | tr '\n' ' ')"
	log=$(ls "$T"/data/gnosh/projects/*/*.background.log 2>/dev/null | head -1)
	id=$(basename "$log" .background.log)
	for i in $(seq 1 120); do
		$envs $bin session list 2>/dev/null | grep "$id" | grep -q "(open now)" || break
		sleep 1
	done
	tmux respawn-pane -k -t "$S" "$envs $bin -r $id"
	sleep 6
	echo "=== opened again with gnosh -r"
	screen | grep -v '^ *$' | tail -12
fi
if [ "$scenario" = config ]; then
	echo "=== state file: $(tr -d '\n' < "$T/data/gnosh/state.json" 2>/dev/null || echo none)"
fi
if [ "$scenario" = worktree ]; then
	echo "=== notes.txt here: $(tr '\n' ' ' < "$T/work/notes.txt")"
	echo "=== work trees: $(git -C "$T/work" worktree list | tr '\n' ';')"
	for d in "$T"/work/.gnosh/worktrees/agent-*; do
		[ -d "$d" ] && echo "=== notes.txt in $d: $(tr '\n' ' ' < "$d/notes.txt")"
	done
fi
if [ "$scenario" = wtflag ]; then
	echo "=== notes.txt here: $(tr '\n' ' ' < "$T/work/notes.txt")"
	echo "=== notes.txt in the work tree: $(tr '\n' ' ' < "$T/work/.gnosh/worktrees/demo/notes.txt" 2>/dev/null || echo none)"
	echo "=== branches: $(git -C "$T/work" branch --list | tr '\n' ' ')"
fi
if [ "$scenario" = plan ]; then
	echo "=== hello.txt: $(cat "$T/work/hello.txt" 2>/dev/null || echo none)"
	tmux send-keys -t "$S" Escape
	sleep 2
fi
if [ "$scenario" = model ] || [ "$scenario" = transcript ]; then
	# Esc keeps the model; q leaves the view, and the screen is as it was.
	if [ "$scenario" = model ]; then key=Escape; else key=q; fi
	tmux send-keys -t "$S" "$key"
	sleep 2
	echo "=== after $key"
	screen
fi
tmux send-keys -t "$S" "/exit" Enter
sleep 2
tmux kill-session -t "$S" 2>/dev/null
echo "=== done"
