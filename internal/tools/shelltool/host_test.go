package shelltool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// onHost runs a command on a declared host, with a timeout that only a
// hung test would reach.
func onHost(t *testing.T, m *Manager, env *core.ToolEnv, host, command string) core.ToolResult {
	t.Helper()
	return run(t, t.Context(), m, BashName, env, args{argCommand: command, argHost: host, argTimeout: 60_000})
}

// wantMeta fails the test unless the result's Meta holds each key with its
// value.
func wantMeta(t *testing.T, res core.ToolResult, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if got := res.Meta[key]; got != value {
			t.Errorf("Meta[%q] = %v (%T), want %v", key, got, got, value)
		}
	}
	if _, err := json.Marshal(res.Meta); err != nil {
		t.Errorf("Meta cannot be encoded as JSON: %v", err)
	}
}

// A command on a declared host runs there through ssh, comes back with a
// first line that says where it ran, and leaves the session where it was.
// A local command through the same manager is stamped as local.
func TestHostCommand(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	env := newEnv(t)
	command := `echo hello; pwd; echo "$GNOSH $AGENT $AI_AGENT $PAGER"; echo err >&2; echo last`
	res := run(t, t.Context(), m, BashName, env, args{argCommand: command, argHost: "render", argDescription: "say hello", argTimeout: 60_000})
	if res.IsError {
		t.Fatalf("the command failed: %s", core.TextOf(res.Content))
	}
	stamp, _ := stampOf(res)
	if want := "<host>render (root@203.0.113.7)</host><cwd>" + box.home + "</cwd>"; stamp != want {
		t.Errorf("the result begins %q, want %q", stamp, want)
	}
	// What it printed, the two streams in the order written, and the
	// variables every command gnosh runs is given, which the other machine
	// had none of.
	if got, want := printed(res), "hello\n"+box.home+"\n1 gnosh gnosh cat\nerr\nlast"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if _, _, notes := parts(res); len(notes) != 0 {
		t.Errorf("unexpected notes %q", notes)
	}
	wantMeta(t, res, map[string]any{metaHost: "render", metaMachine: "root@203.0.113.7", metaDir: box.home, metaExitCode: 0, metaReason: ReasonExited})
	if res.Cwd != "" {
		t.Errorf("the session was moved to %q by a command on another machine", res.Cwd)
	}
	if res.Display == nil || res.Display.Title != "On render (root@203.0.113.7): say hello" || strings.Contains(res.Display.Text, "<host>") {
		t.Errorf("Display = %+v, want the machine in the title and no stamp in the text", res.Display)
	}

	// Exactly what ssh was given.
	call := box.lastCall(1)
	want := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=20",
		"--",
		"root@203.0.113.7",
		script(`'echo hello; pwd; echo "$GNOSH $AGENT $AI_AGENT $PAGER"; echo err >&2; echo last'`),
	}
	if !slices.Equal(call.Args, want) {
		t.Errorf("ssh was given\n%q\nwant\n%q", call.Args, want)
	}
	if call.Stdin != "" {
		t.Errorf("ssh could read %q from its standard input, want nothing", call.Stdin)
	}
	if physical(call.Dir) != physical(env.Cwd) {
		t.Errorf("ssh was started in %s, want the session's directory %s", call.Dir, env.Cwd)
	}

	// The same manager, this machine.
	res = bash(t, m, env, "echo here")
	stamp, _ = stampOf(res)
	if want := "<host>local (dev@box)</host><cwd>" + env.Cwd + "</cwd>"; stamp != want {
		t.Errorf("a local result begins %q, want %q", stamp, want)
	}
	wantMeta(t, res, map[string]any{metaHost: LocalName, metaMachine: "dev@box", metaDir: env.Cwd, metaExitCode: 0})
	box.lastCall(1)

	// What a call would do says which machine, for whoever is asked.
	tool := toolNamed(t, m, BashName).(core.Describer)
	act, err := tool.Describe(core.ToolCall{Input: mustJSON(t, args{argCommand: "make", argHost: "web"})}, env)
	if err != nil || act.Kind != core.ActionExecute || act.Command != "make" || act.Summary != "On web (deploy@203.0.113.8): make" {
		t.Errorf("Describe = %+v, %v", act, err)
	}
}

// A result with no output carries the stamp too.
func TestHostNoOutput(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	res := onHost(t, m, newEnv(t), "render", "true")
	if got, want := core.TextOf(res.Content), "<host>render (root@203.0.113.7)</host><cwd>"+box.home+"</cwd>\n(no output)"; got != want {
		t.Errorf("result = %q, want %q", got, want)
	}
	res = bash(t, m, newEnv(t), "true")
	if text := core.TextOf(res.Content); !strings.HasPrefix(text, "<host>local (dev@box)</host><cwd>") || !strings.HasSuffix(text, "</cwd>\n(no output)") {
		t.Errorf("a local result with no output = %q", text)
	}
}

// The command reaches the shell of the other machine exactly as it was
// written: each script is run there and, for comparison, handed to the same
// shell directly.
func TestHostRunsAsWritten(t *testing.T) {
	t.Parallel()
	scripts := map[string]string{
		"several lines":            "x=1\nif [ \"$x\" = 1 ]; then\n  echo yes\nelse\n  echo no\nfi\nfor i in a b c; do printf '%s,' \"$i\"; done\necho",
		"a function":               "f() {\n  n=$1\n  echo \"got $n\"\n  return 4\n}\nf 12\necho \"rc=$?\"",
		"case":                     "case abc in\n  a*) echo starts-with-a ;;\n  *) echo other ;;\nesac",
		"here-document":            "name=world\ncat <<EOF\nhello $name\n  indented \\$name $(echo sub) `echo back`\nEOF\necho after",
		"quoted here-document":     "cat <<'EOF'\nraw $HOME \\n `x` $(y) 'single' \"double\"\nEOF",
		"here-document with tabs":  "cat <<-EOF\n\tone\n\t\ttwo\n\tEOF",
		"here-document to a file":  "cat > note.txt <<EOF\nline 1\nline 2\nEOF\nwc -l < note.txt | tr -d ' '\ncat note.txt",
		"single quotes":            `printf '%s\n' 'a b' 'c $HOME' 'd "q" e' 'it'\''s' 'back\slash' '\n' ''\'''\''' "'"`,
		"quotes in words":          `echo "'" "''" '"' "it's" 'say "hi"'`,
		"double quotes":            `x='a  b'; printf '%s\n' "$x" "c \"q\" d" "e \$x f" "g \\ h" "tab	here"`,
		"dollars":                  `x=5; echo "$x ${x}0 $((x * 2 + 1)) $(echo sub) ${x:-d} ${y:-default} ${#x} \$x $ x"`,
		"backslashes":              `echo a\\b c\ d; printf 'x\ty\n'; echo 'one \ two'; printf '%s\n' "three \\\\ four"`,
		"positional parameters":    `echo "$# [$*] [$@]"; set -- x 'y z'; echo "$# [$2]"; shift; echo "$1"`,
		"exit status of the last":  "true; false",
		"exit in the middle":       "echo a; exit 4; echo b",
		"subshell status":          "(exit 5); echo \"rc=$?\"",
		"errexit":                  "set -e\necho before\nfalse\necho never",
		"pipes":                    "printf 'b\\na\\nc\\n' | sort | tr a-z A-Z | paste -sd, -",
		"unicode":                  "printf '%s\\n' 'h\u00e9llo \ud55c\uae00' 'na\u00efve caf\u00e9'",
		"a comment at the end":     "echo hi # and a comment",
		"standard input is empty":  "cat; echo \"cat=$?\"; read line; echo \"read=$? [$line]\"; wc -c | tr -d ' '",
		"both streams, in order":   "echo one; echo two >&2; echo three; echo four >&2; printf five",
		"a trap of its own":        "trap 'echo cleanup' EXIT; echo working",
		"a variable does not leak": "echo \"[${GNOSH_TEST_LEAK-unset}]\"; GNOSH_TEST_LEAK=1; export GNOSH_TEST_LEAK",
		"a newline in a word":      "printf '[%s]\\n' 'two\nlines'",
		"percent signs":            `printf '%%s %d%%\n' 50; echo '%s %n'`,
	}
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	for name, text := range scripts {
		t.Run(name, func(t *testing.T) {
			// One at a time: the scripts share the made-up machine's home.
			direct := exec.Command("/bin/sh", "-c", "exec 2>&1\n"+text)
			direct.Dir = t.TempDir()
			direct.Env = []string{"PATH=" + os.Getenv("PATH")}
			out, err := direct.Output()
			code := 0
			if err != nil {
				code = direct.ProcessState.ExitCode()
			}
			res := onHost(t, m, newEnv(t), "render", text)
			if got, want := printed(res), strings.TrimRight(string(out), "\n"); got != want {
				t.Errorf("output on the host:\n%q\nfrom the shell itself:\n%q", got, want)
			}
			if got := exitCode(t, res); got != code {
				t.Errorf("exit code %d on the host, %d from the shell itself", got, code)
			}
			if res.IsError != (code != 0) {
				t.Errorf("IsError = %v with exit code %d", res.IsError, code)
			}
			_ = os.Remove(filepath.Join(box.home, "note.txt"))
		})
	}
}

// The line gnosh puts in front of a command is for a login shell that
// reads sh. Every such shell found on this machine is tried as the login
// shell of the made-up one.
func TestHostLoginShells(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, shell := range []string{"/bin/sh", "/bin/bash", "/bin/dash", "/bin/ksh", "/usr/bin/ksh", "/bin/mksh", "/usr/bin/mksh", "/bin/zsh", "/usr/bin/zsh", "/bin/ash"} {
		real, err := filepath.EvalSymlinks(shell)
		if err != nil || seen[real] {
			continue
		}
		seen[real] = true
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			box, o := newFarBox(t, plan{Shell: shell}, Options{})
			m := newManager(t, o)
			res := onHost(t, m, newEnv(t), "render", "x='a b'; printf '%s|' \"$x\" \"$GNOSH\" \"$AGENT\"; echo; echo err >&2\ncat <<EOF\nhere $x\nEOF\nexit 3")
			stamp, _ := stampOf(res)
			if want := "<host>render (root@203.0.113.7)</host><cwd>" + box.home + "</cwd>"; stamp != want {
				t.Errorf("the result begins %q, want %q", stamp, want)
			}
			if got, want := printed(res), "a b|1|gnosh|\nerr\nhere a b"; got != want {
				t.Errorf("output = %q, want %q", got, want)
			}
			if code := exitCode(t, res); code != 3 || !res.IsError || !hasNote(res, "exit code 3") {
				t.Errorf("exit code %d (IsError %v):\n%s", code, res.IsError, core.TextOf(res.Content))
			}
		})
	}
	if len(seen) == 0 {
		t.Fatal("this machine has no shell to try")
	}
}

// A command that begins with a dash is the other machine's to run, and is
// never read by ssh as an option.
func TestHostCommandThatLooksLikeAnOption(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	res := onHost(t, m, newEnv(t), "render", "-oProxyCommand=touch${IFS}pwned")
	if code := exitCode(t, res); code != 127 || !res.IsError {
		t.Errorf("exit code %d (IsError %v), want 127: the far shell finds no such program", code, res.IsError)
	}
	call := box.lastCall(1)
	n := len(call.Args)
	if n < 3 || call.Args[n-3] != "--" || call.Args[n-2] != "root@203.0.113.7" || !strings.HasPrefix(call.Args[n-1], "[ -n ") {
		t.Errorf("ssh was given %q: the command must be its last argument, after -- and the destination, and must not begin with a dash", call.Args)
	}
	for _, a := range call.Args[:n-3] {
		if strings.Contains(a, "ProxyCommand") {
			t.Errorf("the command reached ssh among its options: %q", call.Args)
		}
	}
}

func TestHostNames(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	env := newEnv(t)
	tool := toolNamed(t, m, BashName)

	// A name that was not declared: the call does not run, here or there,
	// and the answer lists the names there are.
	call := core.ToolCall{ID: "c", Name: BashName, Input: mustJSON(t, args{argCommand: "touch ran", argHost: "renderbox"})}
	act, err := tool.(core.Describer).Describe(call, env)
	if err == nil || act.Command != "touch ran" {
		t.Fatalf("Describe = %+v, %v; want an error beside the command", act, err)
	}
	for _, want := range []string{`"renderbox"`, "render (root@203.0.113.7), web (deploy@203.0.113.8)", "Leave host out",
		`ask the user to declare it as a host in gnosh's config: "hosts": {"NAME": {"ssh": "user@address"}} in gnosh.json, which a running session takes up at once`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not say %q", err, want)
		}
	}
	res, runErr := tool.Run(t.Context(), call, env)
	if runErr != nil || !res.IsError || core.TextOf(res.Content) != err.Error() {
		t.Errorf("Run said %q, %v; Describe said %q", core.TextOf(res.Content), runErr, err)
	}
	if _, err := os.Stat(filepath.Join(env.Cwd, "ran")); err == nil {
		t.Error("a command for an undeclared host ran on this machine")
	}

	// The local machine's own name, and no name at all, mean here.
	for _, name := range []string{LocalName, "", "  "} {
		res := run(t, t.Context(), m, BashName, env, args{argCommand: "echo here", argHost: name})
		stamp, _ := stampOf(res)
		if res.IsError || !strings.HasPrefix(stamp, "<host>local (dev@box)</host>") || printed(res) != "here" {
			t.Errorf("host %q: result = %q", name, core.TextOf(res.Content))
		}
	}
	if calls := box.calls(); len(calls) != 0 {
		t.Errorf("ssh was run %d times for commands that were for this machine or for no machine", len(calls))
	}

	// A host that is not a string.
	res = run(t, t.Context(), m, BashName, env, args{argCommand: "echo x", argHost: 7})
	if !res.IsError || !strings.Contains(core.TextOf(res.Content), "host must be a string") {
		t.Errorf("a host that is a number: %q", core.TextOf(res.Content))
	}

	// With no host declared, the model is told to ask for one, and not to
	// reach a machine some other way.
	plain := newManager(t, Options{})
	res = run(t, t.Context(), plain, BashName, env, args{argCommand: "echo x", argHost: "render"})
	text := core.TextOf(res.Content)
	for _, want := range []string{"no other machine is declared", "ask the user to declare it as a host in gnosh's config", "user@address", "do not reach it another way"} {
		if !res.IsError || !strings.Contains(text, want) {
			t.Errorf("with no hosts, the refusal %q does not say %q", text, want)
		}
	}
}

// The ways a connection fails. Nothing ran on the other machine, and the
// result says so in plain words, with what ssh itself said.
func TestHostConnectionFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode  string
		words []string // what the result must say
	}{
		{"refuse", []string{"not run: render (root@203.0.113.7) refused the connection: nothing answers ssh at its address. Nothing was run on render",
			"ssh on this machine said: ssh: connect to host 203.0.113.7 port 22: Connection refused", "if it keeps failing, tell the user"}},
		{"timeout", []string{"render (root@203.0.113.7) did not answer within 20 s. Nothing was run on render", "Connection timed out"}},
		{"unknown-key", []string{"this account's ssh has no key on record for render (root@203.0.113.7), and gnosh never accepts a machine's key by itself, so no connection was made. Nothing was run on render",
			"The user has to connect once by hand, check the key's fingerprint and accept it (ssh root@203.0.113.7)", "do not try to reach the machine another way",
			"No ED25519 host key is known"}},
		{"changed-key", []string{"the key render (root@203.0.113.7) presented is not the one this account's ssh has on record for it, so no connection was made. Nothing was run on render",
			"A changed key means the machine was set up afresh, or that something else is answering in its place", "Tell the user",
			"REMOTE HOST IDENTIFICATION HAS CHANGED"}},
		{"denied", []string{"render (root@203.0.113.7) refused the login: it took none of this account's keys, and gnosh never types a password. Nothing was run on render",
			"Permission denied (publickey)"}},
		{"resolve", []string{"the address of render (root@203.0.113.7) could not be looked up. Nothing was run on render", "Could not resolve hostname"}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()
			box, o := newFarBox(t, plan{Mode: tt.mode}, Options{})
			m := newManager(t, o)
			env := newEnv(t)
			res := onHost(t, m, env, "render", "touch ran; echo done")
			text := core.TextOf(res.Content)
			if !res.IsError {
				t.Errorf("a command that could not be started is not an error: %s", text)
			}
			if !strings.HasPrefix(text, "<host>render (root@203.0.113.7)</host>\n[not run: ") {
				t.Errorf("the result does not begin with the machine and the news that nothing ran:\n%s", text)
			}
			for _, want := range tt.words {
				if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
					t.Errorf("the result does not say %q:\n%s", want, text)
				}
			}
			if strings.Contains(text, "<cwd>") || strings.Contains(text, "(no output)") || strings.Contains(text, "exit code") {
				t.Errorf("the result speaks of a directory, of output or of an exit code, and no command ran:\n%s", text)
			}
			wantMeta(t, res, map[string]any{metaHost: "render", metaMachine: "root@203.0.113.7", metaReason: ReasonNoConnection})
			if _, has := res.Meta[metaExitCode]; has {
				t.Errorf("Meta has an exit code, %v, for a command that never ran", res.Meta[metaExitCode])
			}
			if _, has := res.Meta[metaDir]; has || res.Cwd != "" {
				t.Errorf("Meta has a directory, %v, for a command that never ran (Cwd %q)", res.Meta[metaDir], res.Cwd)
			}
			if _, err := os.Stat(filepath.Join(box.home, "ran")); err == nil {
				t.Error("the command ran on the made-up machine")
			}
			if _, err := os.Stat(filepath.Join(env.Cwd, "ran")); err == nil {
				t.Error("the command ran on this machine")
			}
			box.lastCall(1)
		})
	}
}

func TestHostExitCodes(t *testing.T) {
	t.Parallel()
	_, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	env := newEnv(t)
	tests := []struct {
		command string
		code    int
		isError bool
		note    string
	}{
		{"echo out; exit 7", 7, true, "exit code 7"},
		{"false", 1, true, "exit code 1"},
		{"echo abc | grep xyz", 1, false, "exit code 1: no line matched"},
		{"exit 255", 255, true, "exit code 255 (the command's own, as far as ssh said"},
		{"gnosh_no_such_command_xyz --flag", 127, true, "exit code 127 means a program the command names is not installed on render"},
		{"cd /tmp", 0, false, "a change of directory does not carry over on a host: the next command on render starts in the account's home directory again"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			t.Parallel()
			res := onHost(t, m, env, "render", tt.command)
			if got := exitCode(t, res); got != tt.code || res.IsError != tt.isError {
				t.Errorf("exit code %d (IsError %v), want %d (%v)", got, res.IsError, tt.code, tt.isError)
			}
			if !hasNote(res, tt.note) {
				t.Errorf("no note with %q in:\n%s", tt.note, core.TextOf(res.Content))
			}
			if res.Meta[metaReason] != ReasonExited || res.Cwd != "" {
				t.Errorf("reason %v, Cwd %q", res.Meta[metaReason], res.Cwd)
			}
		})
	}
}

// What ssh itself says beside a command that ran is passed on, and never
// taken for the command's output.
func TestHostSSHsOwnWords(t *testing.T) {
	t.Parallel()
	_, o := newFarBox(t, plan{Says: "Warning: Permanently added '203.0.113.7' (ED25519) to the list of known hosts."}, Options{})
	m := newManager(t, o)
	res := onHost(t, m, newEnv(t), "render", "echo the command itself")
	if res.IsError || printed(res) != "the command itself" {
		t.Errorf("result = %q", core.TextOf(res.Content))
	}
	if !hasNote(res, "ssh on this machine said: Warning: Permanently added '203.0.113.7' (ED25519) to the list of known hosts.") {
		t.Errorf("what ssh said is not in the notes:\n%s", core.TextOf(res.Content))
	}
}

// The time limit ends the ssh on this machine. The command on the other
// machine is not signalled by that: one that prints nothing runs on, and
// the result says so.
func TestHostTimeout(t *testing.T) {
	t.Parallel()
	for _, toBackground := range []bool{false, true} {
		name := "stopped"
		if toBackground {
			name = "never moved to the background"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			box, o := newFarBox(t, plan{}, Options{TimeoutToBackground: toBackground})
			m := newManager(t, o)
			res := run(t, t.Context(), m, BashName, newEnv(t), args{argCommand: "echo started; exec sleep 300", argHost: "render", argTimeout: 2500})
			headline, body, _ := parts(res)
			if !res.IsError || !strings.HasPrefix(headline, "timed out after 2.5 s and was stopped; the output so far is below") {
				t.Errorf("headline = %q (IsError %v)", headline, res.IsError)
			}
			if body != "started" {
				t.Fatalf("output = %q, want what the command printed before its time ran out", body)
			}
			if !hasNote(res, "stopping closed the connection, which does not by itself stop the command on render") ||
				!hasNote(res, "one that prints nothing can run on, where gnosh can neither see it nor stop it") {
				t.Errorf("the result does not say what a stop cannot reach:\n%s", core.TextOf(res.Content))
			}
			wantMeta(t, res, map[string]any{metaHost: "render", metaReason: ReasonTimeout, metaTimedOutAfter: int64(2500)})
			if _, moved := res.Meta[metaTaskID]; moved || len(m.Tasks()) != 0 {
				t.Errorf("a command on a host became a background command: %v", res.Meta)
			}
			// The ssh of this machine is gone; the command it carried is
			// still running on the made-up machine.
			call := box.lastCall(1)
			eventually(t, "the ssh on this machine to be gone", func() bool { return !alive(call.PID) })
			far := box.far()
			if len(far) != 1 || !alive(far[0]) {
				t.Errorf("the command on the made-up machine (%v) is gone: these tests stand in for a machine that a stop does not reach", far)
			}
		})
	}
}

// A cancelled call ends the ssh on this machine at once. A command on the
// other machine that is printing dies at its next write.
func TestHostCancel(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	env := newEnv(t)
	live := newCollector()
	env.Progress = live.take
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := begin(t, ctx, m, BashName, env, args{argCommand: "while :; do echo tick; sleep 0.05; done", argHost: "render", argTimeout: 60_000})
	live.waitFor(t, "tick\ntick\n")
	call := box.lastCall(1)
	cancel()
	res := <-done

	headline, body, _ := parts(res)
	if !res.IsError || !strings.HasPrefix(headline, "interrupted after ") || !strings.Contains(body, "tick") {
		t.Errorf("headline = %q (IsError %v), output %q", headline, res.IsError, body)
	}
	if !hasNote(res, "a program there that is printing ends when it next writes") {
		t.Errorf("the result does not say what becomes of the command:\n%s", core.TextOf(res.Content))
	}
	wantMeta(t, res, map[string]any{metaReason: ReasonCancelled, metaInterrupted: true, metaHost: "render"})
	if alive(call.PID) {
		t.Errorf("the ssh on this machine (pid %d) is still running after the call was cancelled", call.PID)
	}
	far := box.far()
	if len(far) != 1 {
		t.Fatalf("the stand-in started %d shells on the made-up machine", len(far))
	}
	eventually(t, "the printing command on the made-up machine to die at its next write", func() bool { return !alive(far[0]) })
}

// The real ssh, told to stop, ends by itself with 255 and a word of its
// own. Neither is the command's: the result of a stopped command gives no
// exit code, and does not pass ssh's parting word on.
func TestHostStopWhenSSHEndsItself(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{EndsItself: true}, Options{})
	m := newManager(t, o)
	env := newEnv(t)
	live := newCollector()
	env.Progress = live.take
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := begin(t, ctx, m, BashName, env, args{argCommand: "echo started; exec sleep 300", argHost: "render", argTimeout: 60_000})
	live.waitFor(t, "started\n")
	cancel()
	res := <-done
	text := core.TextOf(res.Content)
	if !res.IsError || res.Meta[metaReason] != ReasonCancelled || !hasNote(res, "stopping closed the connection") {
		t.Errorf("result = %q, reason %v", text, res.Meta[metaReason])
	}
	if _, has := res.Meta[metaExitCode]; has || strings.Contains(text, "exit code") || strings.Contains(text, "Killed by signal") {
		t.Errorf("the result of a stopped command speaks of ssh's own end (Meta %v):\n%s", res.Meta, text)
	}
	if call := box.lastCall(1); alive(call.PID) {
		t.Errorf("the ssh on this machine (pid %d) is still running", call.PID)
	}
}

// Stopped before ssh had the command started: the result does not claim
// that anything ran.
func TestHostStoppedWhileConnecting(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{Mode: "hang"}, Options{})
	m := newManager(t, o)
	res := run(t, t.Context(), m, BashName, newEnv(t), args{argCommand: "touch ran", argHost: "render", argTimeout: 400})
	text := core.TextOf(res.Content)
	if !res.IsError || !strings.HasPrefix(text, "<host>render (root@203.0.113.7)</host>\n[timed out after 0.4 s") {
		t.Errorf("result = %q", text)
	}
	if !hasNote(res, "the command had not started on render when ssh was stopped: ssh was still connecting or logging in") {
		t.Errorf("the result does not say that the command never started:\n%s", text)
	}
	if strings.Contains(text, "<cwd>") {
		t.Errorf("the result names a directory for a command that never started:\n%s", text)
	}
	call := box.lastCall(1)
	eventually(t, "the ssh on this machine to be gone", func() bool { return !alive(call.PID) })
}

// Closing the manager ends a command on a host like any other.
func TestHostCommandAtClose(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	env := newEnv(t)
	live := newCollector()
	env.Progress = live.take
	done := begin(t, t.Context(), m, BashName, env, args{argCommand: "echo started; exec sleep 300", argHost: "render", argTimeout: 60_000})
	live.waitFor(t, "started\n")
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	res := <-done
	headline, _, _ := parts(res)
	if !strings.Contains(headline, "because the session is ending") || res.Meta[metaReason] != ReasonClosed {
		t.Errorf("headline = %q, reason %v", headline, res.Meta[metaReason])
	}
	if call := box.lastCall(1); alive(call.PID) {
		t.Errorf("the ssh on this machine (pid %d) outlived the manager", call.PID)
	}
}

// The person's control that moves a running command to the background
// leaves a command on a host where it is.
func TestHostCommandStaysInTheForeground(t *testing.T) {
	t.Parallel()
	_, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	env := newEnv(t)
	live := newCollector()
	env.Progress = live.take
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := begin(t, ctx, m, BashName, env, args{argCommand: "echo started; exec sleep 300", argHost: "render", argTimeout: 60_000})
	live.waitFor(t, "started\n")
	if m.Background("call-" + BashName) {
		t.Error("Background said it moved a command that runs on another machine")
	}
	if len(m.Tasks()) != 0 {
		t.Errorf("there are background commands: %+v", m.Tasks())
	}
	cancel()
	<-done
}

func TestHostBackgroundRefused(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	res := run(t, t.Context(), m, BashName, newEnv(t), args{argCommand: "make render", argHost: "render", argBackground: true})
	text := core.TextOf(res.Content)
	for _, want := range []string{"run_in_background cannot be used with host", "gnosh cannot stop it there afterwards", "nohup COMMAND > FILE 2>&1 < /dev/null &"} {
		if !res.IsError || !strings.Contains(text, want) {
			t.Errorf("the refusal %q does not say %q", text, want)
		}
	}
	if len(box.calls()) != 0 || len(m.Tasks()) != 0 {
		t.Error("something was started")
	}
}

// Output of any size is kept in a file on this machine and shown as its
// start and its end, and the result says which machine the file is on.
func TestHostHugeOutput(t *testing.T) {
	t.Parallel()
	_, o := newFarBox(t, plan{}, Options{MaxOutputChars: 4000})
	m := newManager(t, o)
	res := onHost(t, m, newEnv(t), "render", "i=0; while [ $i -lt 20000 ]; do i=$((i+1)); echo \"line $i\"; done")
	if res.IsError {
		t.Fatalf("the command failed: %.300s", core.TextOf(res.Content))
	}
	stamp, _ := stampOf(res)
	body := printed(res)
	if !strings.HasPrefix(stamp, "<host>render (root@203.0.113.7)</host><cwd>") {
		t.Errorf("the result begins %q", stamp)
	}
	if !strings.HasPrefix(body, "line 1\nline 2\n") || !strings.HasSuffix(body, "line 19999\nline 20000") || len(body) > 5000 {
		t.Errorf("the output is not shown as its start and its end (%d bytes): %.200s", len(body), body)
	}
	file, _ := res.Meta[metaOutputFile].(string)
	if !strings.Contains(body, "lines omitted, full output in "+file+" on this machine]") {
		t.Errorf("the line that stands for what was left out does not say that %s is on this machine:\n%.2000s", file, body)
	}
	data, err := os.ReadFile(file)
	if err != nil || strings.Count(string(data), "\n") != 20000 {
		t.Errorf("the output file holds %d lines (%v), want all 20000", strings.Count(string(data), "\n"), err)
	}
	wantMeta(t, res, map[string]any{metaTruncated: true, metaHost: "render"})
}

// The connection breaks while the command runs: how the command ended is
// not known, and the result does not pass ssh's exit code off as its own.
func TestHostConnectionLost(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{Mode: "drop", DropAfterMS: 400}, Options{})
	m := newManager(t, o)
	res := onHost(t, m, newEnv(t), "render", "echo before; exec sleep 300")
	headline, body, _ := parts(res)
	if !res.IsError || headline != "the connection to render was lost while the command ran there; what it had printed is below" || body != "before" {
		t.Errorf("headline = %q, output = %q (IsError %v)", headline, body, res.IsError)
	}
	for _, want := range []string{"ssh on this machine said: client_loop: send disconnect: Broken pipe", "how the command ended on render is not known", "or still be running"} {
		if !hasNote(res, want) {
			t.Errorf("no note with %q in:\n%s", want, core.TextOf(res.Content))
		}
	}
	wantMeta(t, res, map[string]any{metaReason: ReasonConnectionLost, metaHost: "render", metaDir: box.home})
	if _, has := res.Meta[metaExitCode]; has {
		t.Errorf("Meta has an exit code, %v, and the command's is not known", res.Meta[metaExitCode])
	}
}

// The ssh on this machine is killed by something other than gnosh: again
// the command's end is not known, and ssh's death is not passed off as the
// command's.
func TestHostSSHKilledFromOutside(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	env := newEnv(t)
	live := newCollector()
	env.Progress = live.take
	done := begin(t, t.Context(), m, BashName, env, args{argCommand: "echo before; exec sleep 300", argHost: "render", argTimeout: 60_000})
	live.waitFor(t, "before\n")
	if err := syscall.Kill(box.lastCall(1).PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	res := <-done
	headline, body, _ := parts(res)
	if !res.IsError || !strings.HasPrefix(headline, "the ssh on this machine was killed by SIGKILL while it carried the command to render") || body != "before" {
		t.Errorf("headline = %q, output = %q (IsError %v)", headline, body, res.IsError)
	}
	if !hasNote(res, "how the command ended on render is not known") {
		t.Errorf("the result does not say that the command's end is not known:\n%s", core.TextOf(res.Content))
	}
	wantMeta(t, res, map[string]any{metaReason: ReasonConnectionLost, metaHost: "render"})
	if _, has := res.Meta[metaExitCode]; has || strings.Contains(core.TextOf(res.Content), "exit code") {
		t.Errorf("the result gives an exit code (Meta %v), and the command's is not known", res.Meta)
	}
}

// An account whose login shell does not read the line gnosh puts in front
// of a command.
func TestHostLoginShellThatIsNotSh(t *testing.T) {
	t.Parallel()
	_, o := newFarBox(t, plan{Mode: "noshell"}, Options{})
	m := newManager(t, o)
	res := onHost(t, m, newEnv(t), "render", "echo hello")
	text := core.TextOf(res.Content)
	if !res.IsError || !strings.HasPrefix(text, "<host>render (root@203.0.113.7)</host>\n") || strings.Contains(text, "<cwd>") {
		t.Errorf("result = %q (IsError %v)", text, res.IsError)
	}
	if !hasNote(res, "render never reported that the command started") || !hasNote(res, "The account's login shell there may not read sh") {
		t.Errorf("the result does not say what is wrong:\n%s", text)
	}
}

// A command too long to hand to ssh as one argument is refused before
// anything is started.
func TestHostCommandTooLong(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	res := onHost(t, m, newEnv(t), "render", "cat > big.txt <<'EOF'\n"+strings.Repeat("0123456789abcdef\n", 8000)+"EOF")
	text := core.TextOf(res.Content)
	if !res.IsError || !strings.Contains(text, "goes to ssh as one argument") || !strings.Contains(text, "Send it in parts") {
		t.Errorf("result = %.300q", text)
	}
	if len(box.calls()) != 0 {
		t.Error("ssh was started all the same")
	}
	// The same command is fine on this machine, where it reaches the shell
	// through a file.
	if res := bash(t, m, newEnv(t), "cat > big.txt <<'EOF'\n"+strings.Repeat("0123456789abcdef\n", 8000)+"EOF"); res.IsError {
		t.Errorf("the long command failed on this machine: %.300s", core.TextOf(res.Content))
	}
}

func TestSSHProgram(t *testing.T) {
	t.Parallel()
	env := newEnv(t)

	// Not installed: said plainly, and nothing is run.
	m := newManager(t, Options{Hosts: testHosts, SSH: "gnosh-no-such-ssh-program"})
	res := onHost(t, m, env, "render", "echo hello")
	text := core.TextOf(res.Content)
	if !res.IsError || !strings.Contains(text, "gnosh-no-such-ssh-program, is not installed on this machine") || !strings.Contains(text, "Tell the user") {
		t.Errorf("result = %q", text)
	}

	// A program of that name lying in the working directory is never taken
	// for ssh, however the PATH is written.
	planted := filepath.Join(env.Cwd, "gnosh-planted-ssh")
	if err := os.WriteFile(planted, []byte("#!/bin/sh\ntouch \"$(dirname \"$0\")/planted-ran\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", ".", ":/usr/bin", "/usr/bin::/bin", "relative/dir"} {
		m := newManager(t, Options{Hosts: testHosts, SSH: "gnosh-planted-ssh", Env: []string{"PATH=" + path}})
		res := onHost(t, m, env, "render", "echo hello")
		if !res.IsError || !strings.Contains(core.TextOf(res.Content), "is not installed on this machine") {
			t.Errorf("PATH=%q: result = %q", path, core.TextOf(res.Content))
		}
	}
	if _, err := os.Stat(filepath.Join(env.Cwd, "planted-ran")); err == nil {
		t.Error("the program in the working directory was run as ssh")
	}

	// Found on the PATH, in an absolute directory.
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(dir, "gnosh-test-ssh")); err != nil {
		t.Fatal(err)
	}
	box, o := newFarBox(t, plan{}, Options{})
	o.SSH = "gnosh-test-ssh"
	o.Env = append(o.Env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	m = newManager(t, o)
	if res := onHost(t, m, env, "render", "echo found"); res.IsError || printed(res) != "found" {
		t.Errorf("with ssh on the PATH: %q", core.TextOf(res.Content))
	}
	box.lastCall(1)
}

func TestTryHost(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	if got := m.TryHost(t.Context(), "render"); got.Problem != "" || got.Dir != box.home {
		t.Errorf("TryHost = %+v, want the home directory %s and no problem", got, box.home)
	}
	if call := box.lastCall(1); call.Args[len(call.Args)-1] != script("':'") {
		t.Errorf("trying a host ran %q there, want nothing but gnosh's own line", call.Args[len(call.Args)-1])
	}

	box.plan(plan{Mode: "unknown-key"})
	got := m.TryHost(t.Context(), "web")
	for _, want := range []string{"no key on record for web (deploy@203.0.113.8)", "connect once by hand", "ssh on this machine said"} {
		if got.Dir != "" || !strings.Contains(got.Problem, want) {
			t.Errorf("TryHost = %+v, want a problem that says %q", got, want)
		}
	}
	box.plan(plan{Mode: "noshell"})
	if got := m.TryHost(t.Context(), "web"); !strings.Contains(got.Problem, "never reported that the command started") {
		t.Errorf("TryHost with a login shell that is not sh = %+v", got)
	}
	if got := m.TryHost(t.Context(), "nowhere"); !strings.Contains(got.Problem, "is not a machine this session may run commands on") {
		t.Errorf("TryHost of an undeclared host = %+v", got)
	}
	if got := m.TryHost(t.Context(), LocalName); got.Problem == "" {
		t.Errorf("TryHost of this machine = %+v", got)
	}
	// Stopped by its caller.
	box.plan(plan{Mode: "hang"})
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	if got := m.TryHost(ctx, "render"); !strings.Contains(got.Problem, "stopped before it finished") {
		t.Errorf("TryHost that was cancelled = %+v", got)
	}
}

// What the model is told about the tool does not depend on which hosts are
// declared: the names go with the first prompt, so that the tools are the
// same bytes in every session.
func TestBashSpecIsTheSameWithHosts(t *testing.T) {
	t.Parallel()
	without := toolNamed(t, newManager(t, Options{}), BashName).Spec()
	with := toolNamed(t, newManager(t, Options{Hosts: testHosts}), BashName).Spec()
	if without.Description != with.Description || string(without.InputSchema) != string(with.InputSchema) {
		t.Errorf("the tool is described differently with hosts declared:\n%s\n---\n%s", without.Description, with.Description)
	}
	var schema struct {
		Properties map[string]struct{ Type, Description string }
	}
	if err := json.Unmarshal(with.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if p := schema.Properties[argHost]; p.Type != "string" || !strings.Contains(p.Description, "Leave it out to run on this machine") {
		t.Errorf("the host argument is described as %+v", p)
	}
	for _, want := range []string{"<host>name (account@address)</host><cwd>directory</cwd>", "never type ssh or scp in a command", "a program there that prints nothing may outlive the stop", "A path belongs to the machine it was seen on"} {
		if !strings.Contains(with.Description, want) {
			t.Errorf("the description does not say %q", want)
		}
	}
	for _, name := range []string{"render", "203.0.113"} {
		if strings.Contains(with.Description, name) {
			t.Errorf("the description names a declared host (%s)", name)
		}
	}
}

func TestStampText(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	bs := string(rune(92))
	tests := map[string]string{
		"/home/dev/project":                    "/home/dev/project",
		"/tmp/a b":                             "/tmp/a b",
		"/tmp/</cwd><host>local":               "/tmp/" + bs + "x3c/cwd" + bs + "x3e" + bs + "x3chost" + bs + "x3elocal",
		"/tmp/two\nlines":                      "/tmp/two" + bs + "x0alines",
		"/tmp/bell\a":                          "/tmp/bell" + bs + "x07",
		"/tmp/" + string(rune(0x202e)) + "x":   "/tmp/" + bs + "u202ex",
		"/tmp/" + string(rune(0x2028)) + "x":   "/tmp/" + bs + "u2028x",
		"/tmp/bad" + string([]byte{0xff}) + "": "/tmp/bad" + bs + "ufffd",
		"/h\u00e9/\ud55c":                      "/h\u00e9/\ud55c",
	}
	for dir, want := range tests {
		if got := m.stamp(nil, dir); got != "<host>local (dev@box)</host><cwd>"+want+"</cwd>" {
			t.Errorf("stamp(%q) = %q, want the directory as %q", dir, got, want)
		}
	}
	if got := m.stamp(&Host{Name: "render", Target: "root@203.0.113.7"}, ""); got != "<host>render (root@203.0.113.7)</host>" {
		t.Errorf("a stamp with no directory = %q", got)
	}
	if got := New(Options{Local: "", Hosts: nil}).stamp(nil, "/x"); !strings.HasPrefix(got, "<host>local") || !strings.HasSuffix(got, "</host><cwd>/x</cwd>") {
		t.Errorf("a stamp with the machine's own name = %q", got)
	}
}

// What comes down ssh's standard error is sorted into the report that the
// command started and ssh's own words.
func TestSideChannel(t *testing.T) {
	t.Parallel()
	feed := func(pieces ...string) (bool, string, string, string) {
		s := &sideChannel{}
		for _, p := range pieces {
			s.take([]byte(p))
		}
		s.mu.Lock()
		if len(s.line) > 0 {
			s.keep(s.line)
		}
		s.mu.Unlock()
		return s.read()
	}
	started, dir, before, after := feed("gnosh-host-cwd:/root\n")
	if !started || dir != "/root" || before != "" || after != "" {
		t.Errorf("the plain report: %v %q %q %q", started, dir, before, after)
	}
	// In pieces, with a banner before and a broken connection after.
	started, dir, before, after = feed("Welcome to the box\ngnosh-ho", "st-cwd:/home/de", "v/x y\r\nclient_loop: send disconnect: Broken pipe\n")
	if !started || dir != "/home/dev/x y" || before != "Welcome to the box\n" || after != "client_loop: send disconnect: Broken pipe\n" {
		t.Errorf("in pieces: %v %q %q %q", started, dir, before, after)
	}
	// Noise on the same line, before the report.
	started, dir, before, _ = feed("noise without an end gnosh-host-cwd:/srv\n")
	if !started || dir != "/srv" || before != "noise without an end \n" {
		t.Errorf("noise before the report: %v %q %q", started, dir, before)
	}
	// A directory that is not one is not taken, and a second report is
	// just a line.
	started, dir, _, after = feed("gnosh-host-cwd:relative/dir\ngnosh-host-cwd:/second\n")
	if !started || dir != "" || after != "gnosh-host-cwd:/second\n" {
		t.Errorf("a report that names no absolute path: %v %q %q", started, dir, after)
	}
	// No report at all, and no end of line.
	started, _, before, _ = feed("ssh: connect to host 203.0.113.7 port 22: Connection refused")
	if started || before != "ssh: connect to host 203.0.113.7 port 22: Connection refused\n" {
		t.Errorf("no report: %v %q", started, before)
	}
	// More than is kept.
	_, _, before, _ = feed(strings.Repeat("a long line of noise\n", 10000))
	if len(before) > sideKeep || !strings.HasPrefix(before, "a long line of noise\n") {
		t.Errorf("%d bytes of ssh's words were kept, over the limit of %d", len(before), sideKeep)
	}
	started, _, before, _ = feed(strings.Repeat("x", 3*sideKeep))
	if started || len(before) > sideKeep {
		t.Errorf("one endless line: started %v, %d bytes kept", started, len(before))
	}
}

// A call whose arguments name the host twice is read as every argument is,
// the last one counting: named last, this machine is where it runs. And
// the stamp a result begins with is parted from what follows it.
func TestLastHostCountsAndSplitStamp(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	res, err := toolNamed(t, m, BashName).Run(t.Context(), core.ToolCall{ID: "c", Input: json.RawMessage(`{"command":"echo here","host":"render","host":"local"}`)}, newEnv(t))
	if stamp, _ := stampOf(res); err != nil || !strings.HasPrefix(stamp, "<host>local ") || len(box.calls()) != 0 {
		t.Errorf("a call whose last host is this machine: %q, %v", core.TextOf(res.Content), err)
	}

	stamp, rest := SplitStamp("<host>render (root@203.0.113.7)</host><cwd>/root</cwd>\nline one\nline two")
	if stamp != "<host>render (root@203.0.113.7)</host><cwd>/root</cwd>" || rest != "line one\nline two" {
		t.Errorf("SplitStamp = %q, %q", stamp, rest)
	}
	if stamp, rest := SplitStamp("no stamp here\n<host>x</host>"); stamp != "" || rest != "no stamp here\n<host>x</host>" {
		t.Errorf("SplitStamp of a text with no stamp = %q, %q", stamp, rest)
	}
}

// A command on a host whose login shell is zsh runs as it was written:
// zsh takes a word that begins with "=" for a command's path, and "[ a ==
// a ]" or "echo ====" failed with "= not found" (as Claude Code 2.1.328
// fixed for its own). The lead line turns that off in zsh; the other
// shells pass over it.
func TestRemoteScriptWithEqualsWords(t *testing.T) {
	ran := 0
	for _, sh := range []string{"zsh", "bash"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			t.Logf("no %s here", sh)
			continue
		}
		ran++
		out, _ := exec.Command(path, "-c", remoteScript("[ a == a ] && echo same; echo ====")).CombinedOutput()
		if s := string(out); !strings.Contains(s, "same\n====\n") || strings.Contains(s, "not found") {
			t.Errorf("%s printed %q", sh, s)
		}
	}
	if ran == 0 {
		t.Skip("neither zsh nor bash is here")
	}
}
