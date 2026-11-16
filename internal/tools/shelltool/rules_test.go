package shelltool

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/shellquote"
)

func TestReaches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		want    string
	}{
		// A command that runs ssh or scp, however it comes to.
		{"ssh root@203.0.113.7 uptime", "ssh"},
		{"  ssh render", "ssh"},
		{"scp a.txt root@203.0.113.7:/tmp/", "scp"},
		{"/usr/bin/ssh render ls", "ssh"},
		{"cd /tmp && ssh render ls", "ssh"},
		{"cd /tmp; scp x render:", "scp"},
		{"echo hi | ssh render cat", "ssh"},
		{"true || ssh render ls", "ssh"},
		{"FOO=1 BAR=2 ssh render ls", "ssh"},
		{"env ssh render ls", "ssh"},
		{"env -i TERM=dumb ssh render ls", "ssh"},
		{"exec ssh render", "ssh"},
		{"time ssh render ls", "ssh"},
		{"nohup ssh render ls &", "ssh"},
		{"timeout 30 ssh render ls", "ssh"},
		{"timeout -k 5 30s scp a b:", "scp"},
		{"sudo ssh render ls", "ssh"},
		{"command ssh render ls", "ssh"},
		{"'ssh' render ls", "ssh"},
		{`"ssh" render ls`, "ssh"},
		{`\ssh render ls`, "ssh"},
		{"x=$(ssh render uptime)", "ssh"},
		{"echo `ssh render uptime`", "ssh"},
		{"(ssh render ls)", "ssh"},
		{"{ ssh render ls; }", "ssh"},
		{"for h in a b; do ssh $h uptime; done", "ssh"},
		{"if ssh render true; then echo up; fi", "ssh"},
		{"! ssh render true", "ssh"},
		{"# first, look\n\nssh render ls", "ssh"},
		{"ssh render ls 2>&1 | tail", "ssh"},

		// Not a command that runs them.
		{"echo ssh render ls", ""},
		{"echo 'ssh render ls' | cat", ""},
		{`git commit -m "use ssh; scp is gone"`, ""},
		{"grep -r ssh .", ""},
		{"man ssh", ""},
		{"which ssh", ""},
		{"command -v ssh", ""},
		{"command -V scp", ""},
		{"ssh-keygen -l -f key.pub", ""},
		{"ssh-add -l", ""},
		{"sshd -t", ""},
		{"cat ~/.ssh/config", ""},
		{"ls ssh", ""},
		{"GIT_SSH_COMMAND='ssh -i key' git push", ""},
		{"git config core.sshCommand 'ssh -i key'", ""},
		{"rsync -e ssh a b:", ""},
		{"ssh() { echo no; }", ""},
		{"echo ok 2>&1", ""},
		{"", ""},
		{"   \n\n", ""},
		// The text of a here-document is no command: a file written with
		// one may say anything.
		{"cat > deploy.sh <<'EOF'\nssh render ./deploy\nscp a b:\nEOF", ""},
		{"cat > d.sh <<-EOF\n\tssh render ./deploy\n\tEOF\necho done", ""},
		{"cat <<A <<\"B\"\nssh x\nA\nscp y\nB\nls", ""},
		// Every line is looked at (box 1, 16 November: ssh under an
		// assignment and a comment ran).
		{"echo one\nssh render ls", "ssh"},
		{"D=104.248.69.97 # the vlm droplet\nssh -o BatchMode=yes root@$D 'bash -s' <<'EOF'\nset -e\nEOF", "ssh"},
		{"cat > d.sh <<'EOF'\nls\nEOF\nscp d.sh render:", "scp"},
	}
	for _, tt := range tests {
		if got := reaches(tt.command); got != tt.want {
			t.Errorf("reaches(%q) = %q, want %q", tt.command, got, tt.want)
		}
	}
}

// With hosts declared, a command that runs ssh or scp is refused, before
// any permission question, with what to do instead. Without hosts nothing
// is refused on that account, and the person's own commands never are.
func TestSSHAndSCPAreRefusedWithHosts(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	env := newEnv(t)
	tool := toolNamed(t, m, BashName)
	for command, wants := range map[string][]string{
		"ssh root@203.0.113.7 'cd /root/renders && ls'": {"the command runs ssh", "naming it as host in the call, never with ssh in a command", "The declared hosts are: render (root@203.0.113.7), web (deploy@203.0.113.8)", "with host set to the machine's name", "ask the user to declare it"},
		"cd out && scp final.mp4 root@203.0.113.7:":     {"the command runs scp", "never with scp or ssh in a command", "The declared hosts are: render (root@203.0.113.7), web (deploy@203.0.113.8)", "read it on one machine with a command and write it on the other", "tell the user what needs copying"},
		"echo hi | env LC_ALL=C ssh web cat":            {"the command runs ssh"},
	} {
		call := core.ToolCall{ID: "c", Name: BashName, Input: mustJSON(t, args{argCommand: command + "; touch ran"})}
		act, err := tool.(core.Describer).Describe(call, env)
		if err == nil {
			t.Errorf("%q was not refused", command)
			continue
		}
		// The gate is still told what the call was aimed at, so that a
		// deny rule is held against it before the model reads why it did
		// not run.
		if act.Kind != core.ActionExecute || act.Command != command+"; touch ran" {
			t.Errorf("%q: the Action beside the refusal is %+v", command, act)
		}
		for _, want := range wants {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: the refusal does not say %q:\n%s", command, want, err)
			}
		}
		res, runErr := tool.Run(t.Context(), call, env)
		if runErr != nil || !res.IsError || core.TextOf(res.Content) != err.Error() {
			t.Errorf("%q: Run said %q, %v; Describe said %q", command, core.TextOf(res.Content), runErr, err)
		}
	}
	if _, err := os.Stat(filepath.Join(env.Cwd, "ran")); err == nil {
		t.Error("a refused command ran")
	}
	if len(box.calls()) != 0 {
		t.Error("ssh was started for a refused command")
	}
	// A command for a host that itself runs ssh from there is refused too:
	// that machine would be the one to reach the next.
	if res := onHost(t, m, env, "render", "ssh web uptime"); !res.IsError || !strings.Contains(core.TextOf(res.Content), "the command runs ssh") {
		t.Errorf("ssh typed in a command for a host: %q", core.TextOf(res.Content))
	}
	// What only mentions ssh runs.
	if res := bash(t, m, env, "echo ssh is not run here; command -v ssh >/dev/null; true"); res.IsError || printed(res) != "ssh is not run here" {
		t.Errorf("a command that mentions ssh: %q", core.TextOf(res.Content))
	}

	// The person's own command line is theirs.
	own := m.PersonsBash()
	if own.Spec().Name != BashName {
		t.Errorf("the person's tool is called %q", own.Spec().Name)
	}
	call := core.ToolCall{ID: "p", Name: BashName, Input: mustJSON(t, args{argCommand: "ssh -V >/dev/null 2>&1; echo mine", argTimeout: 60_000})}
	if _, err := own.(core.Describer).Describe(call, env); err != nil {
		t.Errorf("the person's own ssh was refused: %v", err)
	}
	res, err := own.Run(t.Context(), call, env)
	if err != nil || printed(res) != "mine" || !strings.HasPrefix(core.TextOf(res.Content), "<host>local (dev@box)</host>") {
		t.Errorf("the person's own command: %q, %v", core.TextOf(res.Content), err)
	}

	// No hosts declared: ssh in a command is a command like any other.
	plain := newManager(t, Options{})
	res = bash(t, plain, env, "ssh -V >/dev/null 2>&1; echo ran-it")
	if text := core.TextOf(res.Content); printed(res) != "ran-it" || strings.Contains(text, "the command runs ssh") {
		t.Errorf("without hosts, ssh in a command: %q", text)
	}
}

func TestLeadingSleep(t *testing.T) {
	t.Parallel()
	tests := []struct {
		command string
		want    time.Duration
	}{
		{"sleep 30", 30 * time.Second},
		{"sleep 30s", 30 * time.Second},
		{"sleep 1m", time.Minute},
		{"sleep 0.5h", 30 * time.Minute},
		{"sleep 1 2 3", 6 * time.Second},
		{"  sleep 25 && make test", 25 * time.Second},
		{"sleep 60; echo done", 60 * time.Second},
		{"sleep 10; sleep 10; sleep 10; ./check", 30 * time.Second},
		{"sleep 10 && sleep 10 && sleep 5", 25 * time.Second},
		{"sleep 10\nsleep 10\nsleep 10\n./check", 30 * time.Second},
		{"# wait for it\nsleep 40", 40 * time.Second},
		{"sleep infinity", 365 * 24 * time.Hour},
		{"sleep 30 | cat", 30 * time.Second},
		{"sleep 30 2>&1", 30 * time.Second},
		{"sleep 2 && echo ok", 2 * time.Second},
		{"sleep 0.01", 10 * time.Millisecond},
		// Not a sleep that holds the command up.
		{"sleep 30 & echo started", 0},
		{"echo first; sleep 60", 0},
		{"make test; sleep 60", 0},
		{"sleep", 0},
		{"sleep --help", 0},
		{"sleep $N", 0},
		{"sleeper 30", 0},
		{"timeout 5 sleep 60", 0},
		{"", 0},
	}
	for _, tt := range tests {
		if got := leadingSleep(tt.command); got != tt.want {
			t.Errorf("leadingSleep(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

// Where the model is told when a background command ends, a foreground
// command that begins with a long sleep is refused, with what to do
// instead. The rule is asked for, and holds for a session's own calls only.
func TestLongSleepIsRefused(t *testing.T) {
	t.Parallel()
	_, o := newFarBox(t, plan{}, Options{LongSleep: 25 * time.Second, OnEnd: func(Ended) {}})
	m := newManager(t, o)
	env := newEnv(t)
	tool := toolNamed(t, m, BashName).(core.Describer)
	describe := func(m *Manager, env *core.ToolEnv, a args) error {
		_, err := toolNamed(t, m, BashName).(core.Describer).Describe(core.ToolCall{Input: mustJSON(t, a)}, env)
		return err
	}
	for _, command := range []string{"sleep 25", "sleep 30 && curl -s localhost:8080", "sleep 10; sleep 10; sleep 10; ./check"} {
		_, err := tool.Describe(core.ToolCall{Input: mustJSON(t, args{argCommand: command})}, env)
		if err == nil {
			t.Errorf("%q was not refused", command)
			continue
		}
		for _, want := range []string{"the command begins by sleeping for", "run a loop that checks for it with run_in_background set to true: you are told when it ends", "run that command with run_in_background", "Several shorter sleeps one after another are not the way either"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: the refusal does not say %q:\n%s", command, want, err)
			}
		}
	}
	res := run(t, t.Context(), m, BashName, env, args{argCommand: "sleep 3600; touch ran"})
	if !res.IsError || !strings.Contains(core.TextOf(res.Content), "begins by sleeping for 3600 s") {
		t.Errorf("Run of a long sleep: %q", core.TextOf(res.Content))
	}

	// Allowed: a short sleep, a sleep in the background, a sleep on a
	// host (where nothing can be run in the background), a sub-agent's
	// (which is told of no end), the person's own.
	sub := newEnv(t)
	sub.ParentSessionID = "parent"
	for name, err := range map[string]error{
		"a short sleep":       describe(m, env, args{argCommand: "sleep 24.9 && ./check"}),
		"in the background":   describe(m, env, args{argCommand: "sleep 3600; ./check", argBackground: true}),
		"on a host":           describe(m, env, args{argCommand: "sleep 60; tail log", argHost: "render"}),
		"a sub-agent's":       describe(m, sub, args{argCommand: "sleep 60"}),
		"with the rule unset": describe(newManager(t, Options{OnEnd: func(Ended) {}}), env, args{argCommand: "sleep 60"}),
	} {
		if err != nil {
			t.Errorf("%s was refused: %v", name, err)
		}
	}
	if _, err := m.PersonsBash().(core.Describer).Describe(core.ToolCall{Input: mustJSON(t, args{argCommand: "sleep 60"})}, env); err != nil {
		t.Errorf("the person's own sleep was refused: %v", err)
	}
}

// The notes under a failure that say what it means, at the moment it is
// read.
func TestNotesUnderAFailure(t *testing.T) {
	t.Parallel()
	// Names no program has on any system, so that the test reads the same
	// where file names take no notice of letter case.
	m := newManager(t, Options{ToolNames: []string{"GnoshReadTool", "GnoshEditTool", BashName, "gnovm_run"}})
	env := newEnv(t)

	// A program that is not installed.
	res := bash(t, m, env, "gnosh_no_such_command_xyz --flag")
	if !hasNote(res, "exit code 127") || !hasNote(res, "exit code 127 means a program the command names is not installed on this machine. Do it another way, or ask the user where the program is; do not search the disk for it") {
		t.Errorf("a command that is not installed:\n%s", core.TextOf(res.Content))
	}
	if !strings.Contains(printed(res), "gnosh_no_such_command_xyz") {
		t.Errorf("the shell's own message is gone:\n%s", core.TextOf(res.Content))
	}

	// Misread is asked, before a command runs, about each program of its
	// first line that is not installed, and a command it says was misread
	// does not run: what it says is the refusal. A command on this machine
	// that fails is put to it again, with the programs its first line
	// runs, and what it says then is the only note.
	var asked [][]string
	mr := newManager(t, Options{ToolNames: []string{"gnovm_run"}, Misread: func(command string, programs []string) string {
		asked = append(asked, programs)
		if slices.Contains(programs, "gnosh_mem_xyz") || slices.Contains(programs, "sh") && strings.Contains(command, "x(") {
			return "it was Gno"
		}
		return ""
	}})
	res = bash(t, mr, env, "touch ran && gnosh_mem_xyz show")
	if !res.IsError || core.TextOf(res.Content) != "it was Gno" {
		t.Errorf("a misread command:\n%s", core.TextOf(res.Content))
	}
	if _, err := os.Stat(filepath.Join(env.Cwd, "ran")); err == nil {
		t.Error("a misread command ran")
	}
	res = bash(t, mr, env, "echo 'x(' | sh -s")
	if exitCode(t, res) == 0 || !hasNote(res, "it was Gno") {
		t.Errorf("a misread command that failed:\n%s", core.TextOf(res.Content))
	}
	bash(t, mr, env, "echo fine")
	// (Only the first line is read before the run: a later one may be in
	// a quoted text that goes on, and here it is.)
	if res := bash(t, mr, env, "echo 'one\ngnosh_mem_xyz show'"); res.IsError {
		t.Errorf("a text that goes on to a second line is taken for a command:\n%s", core.TextOf(res.Content))
	}
	if want := [][]string{{"gnosh_mem_xyz"}, {"echo", "sh"}}; !slices.EqualFunc(asked, want, slices.Equal) {
		t.Errorf("Misread was asked about %q, want %q", asked, want)
	}

	// One of the session's tools, typed as a command, does not run either.
	for _, c := range []struct{ command, name string }{
		{"GnoshReadTool notes.txt", "GnoshReadTool"},
		{"gnovm_run 'mem.Next()'", "gnovm_run"},
		{"FOO=1 GnoshEditTool a b", "GnoshEditTool"},
		{"touch ran && GnoshReadTool x", "GnoshReadTool"},
	} {
		res := bash(t, m, env, c.command)
		if want := c.name + " is one of your tools, not a program: call it as a tool, with its arguments, instead of typing it into a command"; !res.IsError || core.TextOf(res.Content) != want {
			t.Errorf("%q:\n%s", c.command, core.TextOf(res.Content))
		}
		if _, err := os.Stat(filepath.Join(env.Cwd, "ran")); err == nil {
			t.Errorf("%q ran", c.command)
		}
	}
	// A program of that name, where there is one, runs.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "GnoshReadTool"), []byte("#!/bin/sh\necho the program\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mp := newManager(t, Options{ToolNames: []string{"GnoshReadTool"}, Env: []string{"PATH=" + bin + ":" + os.Getenv("PATH")}})
	if res := bash(t, mp, env, "GnoshReadTool x"); res.IsError || !strings.Contains(printed(res), "the program") {
		t.Errorf("an installed program named like a tool:\n%s", core.TextOf(res.Content))
	}

	// A lookup that found nothing prints nothing, and is told the same.
	for _, command := range []string{"command -v gnosh_no_such_command_xyz", "which gnosh_no_such_command_xyz"} {
		res := bash(t, m, env, command)
		if !res.IsError || !hasNote(res, "nothing was found: gnosh_no_such_command_xyz is not installed on this machine, or is not on the PATH there. Do it another way, or ask the user where the program is; do not search the disk for it") {
			t.Errorf("%q:\n%s", command, core.TextOf(res.Content))
		}
	}

	// Nothing of the kind where it does not apply.
	for _, command := range []string{"command -v sh", "false", "exit 3", "echo hello", "cd ."} {
		res := bash(t, m, env, command)
		if hasNote(res, "installed") || hasNote(res, "one of your tools") || hasNote(res, "carry over") {
			t.Errorf("%q got a note it should not have:\n%s", command, core.TextOf(res.Content))
		}
	}
	// And none for the person's own command.
	res, err := m.PersonsBash().Run(t.Context(), core.ToolCall{Input: mustJSON(t, args{argCommand: "gnosh_no_such_command_xyz"})}, env)
	if err != nil || hasNote(res, "is not installed") || !hasNote(res, "exit code 127") {
		t.Errorf("the person's own command:\n%s (%v)", core.TextOf(res.Content), err)
	}
}

// An argument the tool does not take is an error that names it. It is never
// passed over in silence: the command would run in a way the model did not
// ask for.
func TestUnknownArgument(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	tool := toolNamed(t, m, BashName)
	for input, wants := range map[string][]string{
		`{"command": "touch ran", "working_dir": "/root/renders"}`: {`"working_dir" is not an argument of Bash`, "Bash takes: command, description, timeout, run_in_background, host", "There is no argument for a directory"},
		`{"command": "touch ran", "timeout_ms": 5000}`:             {`"timeout_ms" is not an argument of Bash`},
		`{"command": "touch ran", "Host": "render"}`:               {`"Host" is not an argument of Bash`},
	} {
		call := core.ToolCall{ID: "c", Name: BashName, Input: json.RawMessage(input)}
		_, err := tool.(core.Describer).Describe(call, env)
		res, runErr := tool.Run(t.Context(), call, env)
		if err == nil || runErr != nil || !res.IsError || core.TextOf(res.Content) != err.Error() {
			t.Errorf("%s: Describe said %v; Run said %q, %v", input, err, core.TextOf(res.Content), runErr)
			continue
		}
		for _, want := range wants {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not say %q:\n%s", input, want, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(env.Cwd, "ran")); err == nil {
		t.Error("a call with an unknown argument ran")
	}
	// The one argument of Claude Code's that changes nothing here is taken.
	res := run(t, t.Context(), m, BashName, env, args{argCommand: "echo fine", argNoSandbox: true})
	if res.IsError || printed(res) != "fine" {
		t.Errorf("a call with %s: %q", argNoSandbox, core.TextOf(res.Content))
	}
}

// A command in single quotes is read by a shell as exactly the text it
// was.
func TestShellQuote(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"", "plain", "two words", "it's", "'", "''", "'''", `'\''`, `a\b`, `a\\b`, `"double"`, "$HOME `id` $(id)", "line one\nline two\n",
		"tab\there", "semi; colon && and | pipe > out", "!history", "#comment", "~", "*?[a-z]", "{a,b}", "\\", "\\'", "%s %d", "-n", "- -- ---",
		"h\u00e9llo \ud55c\uae00",
	} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellquote.Quote(text)).Output()
		if err != nil || string(out) != text {
			t.Errorf("a shell read %s as %q (%v), want %q", shellquote.Quote(text), out, err, text)
		}
	}
}

// The readers of command lines take whatever they are given.
func FuzzRules(f *testing.F) {
	for _, seed := range []string{
		"ssh render ls", "cd x && scp a b:", "sleep 30; sleep 30", "echo 'a;b' | cat", "x=$(ssh h); echo `scp`", "command -v ssh",
		"a\\", "'unterminated", "\"unterminated", ";;;&&||", "sleep 1e309", "sleep -1", "sleep nan", "((1+2))", "\x00", "sleep 9999999999999999999d",
		"which\tx", "cd", "# only a comment", "time", "env", "timeout", "{", "!",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, command string) {
		if prog := reaches(command); prog != "" && prog != "ssh" && prog != "scp" {
			t.Errorf("reaches(%q) = %q", command, prog)
		}
		if d := leadingSleep(command); d < 0 {
			t.Errorf("leadingSleep(%q) = %v", command, d)
		}
		lookedUp(command)
		onlyChangesDirectory(command)
		line, rest := firstCommandLine(command)
		if strings.Contains(line, "\n") || len(line)+len(rest) > len(command) {
			t.Errorf("firstCommandLine(%q) = %q, %q", command, line, rest)
		}
		total := 0
		for _, p := range pieces(line) {
			total += len(p.text) + len(p.sep)
			programOf(p.text)
		}
		if total > len(line) {
			t.Errorf("pieces(%q) hold %d bytes, more than the line", line, total)
		}
	})
}
