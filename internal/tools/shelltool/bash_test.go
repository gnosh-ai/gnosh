package shelltool

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

func TestExitCodes(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	tests := []struct {
		name    string
		command string
		code    int
		isError bool
		output  string // "*" for whatever the shell says
		note    string // a note the result must carry; empty for none at all
		signal  string
	}{
		{name: "success", command: "true", code: 0},
		{name: "success with output", command: "echo hello", code: 0, output: "hello"},
		{name: "exit 3", command: "exit 3", code: 3, isError: true, note: "exit code 3"},
		{name: "output then failure", command: "echo out; echo err >&2; exit 7", code: 7, isError: true, output: "out\nerr", note: "exit code 7"},
		{name: "false", command: "false", code: 1, isError: true, note: "exit code 1"},
		{name: "last command decides", command: "false; true", code: 0},
		{name: "a child killed by a signal", command: "sh -c 'kill -TERM $$' 2>/dev/null; exit $?", code: 143, isError: true, output: "*", note: "exit code 143"},
		{name: "the shell killed by a signal", command: "kill -KILL $$", code: 137, isError: true, note: "the shell was killed by SIGKILL", signal: "SIGKILL"},
		{name: "exit code 1 from grep is an answer", command: "echo abc | grep xyz", code: 1, note: "exit code 1: no line matched"},
		{name: "exit code 2 from grep is a failure", command: "grep xyz /nonexistent/file 2>/dev/null", code: 2, isError: true, note: "exit code 2"},
		{name: "exit code 1 from test is an answer", command: "[ -e /nonexistent ]", code: 1, note: "exit code 1: the test was false"},
		{name: "exit code 1 after a list is a failure", command: "cd . && grep xyz /dev/null", code: 1, isError: true, note: "exit code 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := bash(t, m, env, tt.command)
			if got := exitCode(t, res); got != tt.code {
				t.Errorf("exit code = %d, want %d", got, tt.code)
			}
			if res.IsError != tt.isError {
				t.Errorf("IsError = %v, want %v", res.IsError, tt.isError)
			}
			headline, body, notes := parts(res)
			if headline != "" {
				t.Errorf("unexpected headline %q", headline)
			}
			if body != tt.output && tt.output != "*" {
				t.Errorf("output = %q, want %q", body, tt.output)
			}
			switch {
			case tt.note == "" && len(notes) > 0:
				t.Errorf("unexpected notes %q", notes)
			case tt.note != "" && !hasNote(res, tt.note):
				t.Errorf("notes = %q, want one with %q", notes, tt.note)
			}
			if got, _ := res.Meta[metaSignal].(string); got != tt.signal {
				t.Errorf("signal = %q, want %q", got, tt.signal)
			}
			if got := res.Meta[metaReason]; got != ReasonExited {
				t.Errorf("reason = %v, want %q", got, ReasonExited)
			}
			if _, err := json.Marshal(res.Meta); err != nil {
				t.Errorf("Meta cannot be encoded as JSON: %v", err)
			}
		})
	}
}

func TestCommandNotFound(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	res := bash(t, m, newEnv(t), "gnosh_no_such_command_xyz --flag")
	if code := exitCode(t, res); code != 127 || !res.IsError {
		t.Errorf("exit code = %d, IsError = %v; want 127 and an error", code, res.IsError)
	}
	if !strings.Contains(printed(res), "gnosh_no_such_command_xyz") || !strings.Contains(printed(res), "not found") {
		t.Errorf("the shell's own message is missing from %q", printed(res))
	}
}

func TestOutputOrder(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	res := bash(t, m, newEnv(t), "echo one; echo two >&2; echo three; echo four >&2; printf five")
	if got, want := printed(res), "one\ntwo\nthree\nfour\nfive"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if res.Display == nil || res.Display.Kind != "command" {
		t.Errorf("Display = %+v, want one of kind command", res.Display)
	}
}

// TestRunsAsWritten checks that a command behaves under the tool as it
// does when the shell is given it directly: the same output, in the same
// order, and the same exit code. Each script is run both ways.
func TestRunsAsWritten(t *testing.T) {
	t.Parallel()
	scripts := map[string]string{
		"several lines":            "x=1\nif [ \"$x\" = 1 ]; then\n  echo yes\nelse\n  echo no\nfi\nfor i in a b c; do printf '%s,' \"$i\"; done\necho",
		"a function":               "f() {\n  local n=$1\n  echo \"got $n\"\n  return 4\n}\nf 12\necho \"rc=$?\"",
		"case":                     "case abc in\n  a*) echo starts-with-a ;;\n  *) echo other ;;\nesac",
		"here-document":            "name=world\ncat <<EOF\nhello $name\n  indented \\$name $(echo sub) `echo back`\nEOF\necho after",
		"quoted here-document":     "cat <<'EOF'\nraw $HOME \\n `x` $(y)\nEOF",
		"here-document with tabs":  "cat <<-EOF\n\tone\n\t\ttwo\n\tEOF",
		"here-document to a file":  "cat > note.txt <<EOF\nline 1\nline 2\nEOF\nwc -l < note.txt | tr -d ' '\ncat note.txt",
		"single quotes":            `printf '%s\n' 'a b' 'c $HOME' 'd "q" e' 'it'\''s' 'back\slash' '\n'`,
		"double quotes":            `x='a  b'; printf '%s\n' "$x" "c \"q\" d" "e \$x f" "g \\ h" "tab	here"`,
		"dollars":                  `x=5; echo "$x ${x}0 $((x * 2 + 1)) $(echo sub) ${x:-d} ${y:-default} ${#x} \$x $ x"`,
		"backslashes":              `echo a\\b c\ d; printf 'x\ty\n'; echo 'one \ two'; echo "three \\\\ four"`,
		"positional parameters":    `echo "$# [$*] [$@]"; set -- x 'y z'; echo "$# [$2]"; shift; echo "$1"`,
		"exit status of the last":  "true; false",
		"exit in the middle":       "echo a; exit 4; echo b",
		"subshell status":          "(exit 5); echo \"rc=$?\"",
		"or and and":               "false || echo recovered; true && echo both; false && echo never; echo end",
		"errexit":                  "set -e\necho before\nfalse\necho never",
		"nounset":                  "set -u\necho ${unset_variable:-fallback}\necho $really_unset\necho never",
		"pipes":                    "printf 'b\\na\\nc\\n' | sort | tr a-z A-Z | paste -sd, -",
		"pipe status":              "false | true; echo \"rc=$?\"",
		"command substitution":     "echo \"[$(printf 'a\\nb')]\" \"[$(basename /x/y/z)]\"",
		"arithmetic":               "i=0; while [ $i -lt 3 ]; do i=$((i + 1)); done; echo $i",
		"unicode":                  "echo 'héllo 한글 😀'; printf '%s\\n' 'naïve café'",
		"a comment at the end":     "echo hi # and a comment",
		"only a comment":           "# nothing to do",
		"standard input is empty":  "cat; echo \"cat=$?\"; read line; echo \"read=$? [$line]\"; wc -c | tr -d ' '",
		"redirections":             "echo to-err >&2; echo to-null >/dev/null; echo kept 2>/dev/null; { echo grouped; echo also; } | cat",
		"a trap of its own":        "trap 'echo cleanup' EXIT; echo working",
		"globbing":                 "touch g1.txt g2.txt; echo g*.txt; echo 'g*.txt' \"g*.txt\"",
		"a variable does not leak": "echo \"[${GNOSH_TEST_LEAK-unset}]\"; GNOSH_TEST_LEAK=1; export GNOSH_TEST_LEAK",
		"background and wait":      "(echo from-child) & wait; echo done",
		"syntax error":             "echo one\nif then\necho two",
		"unterminated quote":       "echo one\necho \"two",
		"unterminated here-doc":    "cat <<EOF\nnever closed",
		"bad substitution":         "echo ${x!}; echo after",
		"a failing command":        "ls /nonexistent/gnosh-test-path; echo \"rc=$?\"",
	}
	bashOnly := map[string]string{
		"arrays":               "a=(one 'two three' four); echo \"${#a[@]} ${a[1]}\"; for x in \"${a[@]}\"; do echo \"<$x>\"; done",
		"brace expansion":      "echo {1..3} x{a,b}y",
		"process substitution": "cat <(echo from-fd); diff <(echo a) <(echo a) && echo same",
		"double brackets":      "s=hello; if [[ $s == h* && ! $s =~ ^x ]]; then echo match; fi",
		"ansi-c quotes":        "printf '%s\\n' $'tab\\there' $'nl\\nthere'",
		"pipefail":             "set -o pipefail; false | true; echo \"rc=$?\"",
		"here-string":          "tr a-z A-Z <<< 'shout'",
	}
	// Once with the shell the manager picks by itself, which is bash where
	// there is one, and once with plain sh.
	for _, shell := range []string{"", "/bin/sh"} {
		m := newManager(t, Options{Shell: shell})
		if shell != "" && m.shell == New(Options{}).shell {
			continue // there is no bash here, so both are the same shell
		}
		for name, script := range scripts {
			runsAsWritten(t, m, filepath.Base(m.shell)+"/"+name, script)
		}
		if filepath.Base(m.shell) == "bash" {
			for name, script := range bashOnly {
				runsAsWritten(t, m, "bash only/"+name, script)
			}
		}
	}
}

// runsAsWritten runs one script both ways, as a subtest, and compares.
func runsAsWritten(t *testing.T, m *Manager, name, script string) {
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		// What the shell does with it, given it directly.
		env := newEnv(t)
		direct := exec.Command(m.shell, "-c", script)
		direct.Dir = env.Cwd
		direct.Env = commandEnv(os.Environ(), env.Cwd, nil)
		want, err := direct.CombinedOutput()
		wantCode := 0
		if exit, ok := err.(*exec.ExitError); ok {
			wantCode = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("running the script directly: %v", err)
		}

		// What the tool does with it, in the same directory.
		res := bash(t, m, env, script)
		if got := exitCode(t, res); got != wantCode {
			t.Errorf("exit code = %d, the shell itself gives %d", got, wantCode)
		}
		if res.IsError != (wantCode != 0) {
			t.Errorf("IsError = %v with exit code %d", res.IsError, wantCode)
		}
		if got, want := shellNeutral(printed(res)), shellNeutral(strings.TrimRight(string(want), "\n")); got != want {
			t.Errorf("output differs from the shell's own.\n got: %q\nwant: %q", got, want)
		}
		if got := res.Meta[metaDir]; got != env.Cwd {
			t.Errorf("Meta says the command ran in %v, want %s", got, env.Cwd)
		}
	})
}

// shellNeutral takes out of a shell's messages the parts that tell how the
// command was handed to it: the tool hands it over as an argument to eval,
// "sh -c" as a script, and the shells name themselves differently in the
// two cases.
func shellNeutral(s string) string {
	for _, pair := range [][2]string{
		{"/bin/bash: -c: ", "bash: "},
		{"bash: -c: ", "bash: "},
		{"/bin/bash: eval: ", "bash: "},
		{"bash: eval: ", "bash: "},
		{"/bin/bash: ", "bash: "},
		{"/bin/sh: ", "sh: "},
		{": eval: ", ": "},
		{" -c: ", " "},
	} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	// Old shells count the lines of a -c script from 0 and those of an eval
	// from 1.
	return lineNumber.ReplaceAllString(s, "line N:")
}

var lineNumber = regexp.MustCompile(`line \d+:`)

func TestLongCommand(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	// Far longer than a single argument may be on Linux (128 KiB), so the
	// command has to reach the shell through a file.
	body := strings.Repeat("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde\n", 4096)
	command := "cat > big.txt <<'EOF'\n" + body + "EOF\nwc -c < big.txt | tr -d ' '\ncd sub 2>/dev/null; exit 9"
	if err := os.Mkdir(filepath.Join(env.Cwd, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := bash(t, m, env, command)
	if got, want := printed(res), "262144"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if code := exitCode(t, res); code != 9 {
		t.Errorf("exit code = %d, want 9", code)
	}
	if want := filepath.Join(env.Cwd, "sub"); res.Cwd != want {
		t.Errorf("Cwd = %q, want %q", res.Cwd, want)
	}
	written, err := os.ReadFile(filepath.Join(env.Cwd, "big.txt"))
	if err != nil || string(written) != body {
		t.Errorf("the here-document did not arrive whole: %d bytes, err %v", len(written), err)
	}
	if left, _ := filepath.Glob(filepath.Join(m.dir, "*.sh")); len(left) > 0 {
		t.Errorf("the command's own file was left behind: %v", left)
	}
}

func TestNoStandardInput(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	start := time.Now()
	res := bash(t, m, newEnv(t), "cat; echo \"cat exited $?\"; if read answer; then echo \"read got [$answer]\"; else echo 'read saw end of file'; fi")
	if got, want := printed(res), "cat exited 0\nread saw end of file"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("a command reading standard input took %v; it should see end of file at once", took)
	}
}

func TestNoTerminal(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	// With no terminal to open, a program that insists on asking a person
	// fails at once instead of waiting.
	res := bash(t, m, env, "if (exec </dev/tty) 2>/dev/null; then echo has-terminal; else echo no-terminal; fi")
	if got := printed(res); got != "no-terminal" {
		t.Errorf("output = %q, want no-terminal", got)
	}
}

func TestEnvironment(t *testing.T) {
	t.Parallel()
	parent := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=/home/somebody",
		"TERM=xterm-256color",
		"PAGER=less",
		"OLDPWD=/somewhere/else",
		"PWD=/not/where/it/runs",
		"KEPT=from-parent",
		"DROPPED=from-parent",
		"REPLACED=from-parent",
	}
	m := newManager(t, Options{
		Environ: func() []string { return parent },
		Env:     []string{"REPLACED=from-options", "ADDED=from-options", "DROPPED", "GIT_PAGER=delta"},
	})
	env := newEnv(t)
	res := bash(t, m, env, `for v in PAGER GIT_PAGER GIT_TERMINAL_PROMPT TERM GNOSH AGENT AI_AGENT HOME KEPT DROPPED REPLACED ADDED OLDPWD; do eval "echo $v=\${$v-<unset>}"; done; echo "PWD=$PWD"; pwd`)
	want := strings.Join([]string{
		"PAGER=cat",
		"GIT_PAGER=delta", // Options.Env has the last word
		"GIT_TERMINAL_PROMPT=0",
		"TERM=dumb",
		"GNOSH=1",
		"AGENT=gnosh",
		"AI_AGENT=gnosh",
		"HOME=/home/somebody",
		"KEPT=from-parent",
		"DROPPED=<unset>",
		"REPLACED=from-options",
		"ADDED=from-options",
		"OLDPWD=<unset>",
		"PWD=" + env.Cwd,
		env.Cwd,
	}, "\n")
	if got := printed(res); got != want {
		t.Errorf("environment:\n got: %s\nwant: %s", got, want)
	}
}

func TestStateDoesNotCarryOver(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	bash(t, m, env, "export CARRIED=yes; later() { echo fn; }; alias ll='ls -l'; set -e")
	res := bash(t, m, env, `echo "[${CARRIED-unset}]"; type later >/dev/null 2>&1 && echo function-kept; false; echo errexit-off`)
	if got, want := printed(res), "[unset]\nerrexit-off"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	tool := toolNamed(t, m, BashName).(core.Describer)
	command := "  echo one &&\n\tprintf '%s' \"$HOME\"  \n"

	action, err := tool.Describe(core.ToolCall{Input: mustJSON(t, args{argCommand: command})}, env)
	if err != nil {
		t.Fatal(err)
	}
	if action.Kind != core.ActionExecute || action.Command != command {
		t.Errorf("action = %+v; want an execute of exactly the command", action)
	}
	if want := "echo one &&"; action.Summary != want {
		t.Errorf("summary = %q, want the first line, %q", action.Summary, want)
	}

	action, err = tool.Describe(core.ToolCall{Input: mustJSON(t, args{argCommand: command, argDescription: "  Print one,\n then the home directory "})}, env)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Print one, then the home directory"; action.Summary != want {
		t.Errorf("summary = %q, want the description on one line, %q", action.Summary, want)
	}
}

func TestUnusableArguments(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	tests := []struct {
		name  string
		input string
		want  string // part of the refusal
	}{
		{"no arguments", ``, "command is missing"},
		{"empty object", `{}`, "command is missing"},
		{"null command", `{"command": null}`, "command is missing"},
		{"empty command", `{"command": ""}`, "command is empty"},
		{"blank command", `{"command": "  \n\t "}`, "command is empty"},
		{"command not a string", `{"command": ["ls"]}`, "command must be a string"},
		{"NUL in command", `{"command": "echo a\u0000b"}`, "NUL"},
		{"not an object", `["ls"]`, "not one JSON object"},
		{"not JSON", `{"command": "ls"`, "not one JSON object"},
		{"negative timeout", `{"command": "ls", "timeout": -5}`, "timeout must not be negative"},
		{"timeout not a number", `{"command": "ls", "timeout": "soon"}`, "timeout must be a number"},
		{"timeout a list", `{"command": "ls", "timeout": [1]}`, "timeout must be a number"},
		{"background not a boolean", `{"command": "ls", "run_in_background": "maybe"}`, "run_in_background must be true or false"},
		{"description not a string", `{"command": "ls", "description": 7}`, "description must be a string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			call := core.ToolCall{ID: "c", Name: BashName, Input: json.RawMessage(tt.input)}
			tool := toolNamed(t, m, BashName)
			_, err := tool.(core.Describer).Describe(call, env)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Describe error = %v, want one with %q", err, tt.want)
			}
			res, runErr := tool.Run(t.Context(), call, env)
			if runErr != nil {
				t.Fatalf("Run returned a Go error, %v; a refusal belongs in the result", runErr)
			}
			if !res.IsError || core.TextOf(res.Content) != err.Error() {
				t.Errorf("Run said %q (IsError %v); Describe said %q", core.TextOf(res.Content), res.IsError, err)
			}
		})
	}
}

func TestTimeoutArgument(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{DefaultTimeout: 3 * time.Second, MaxTimeout: 5 * time.Second, MaxBackgroundTimeout: time.Minute})
	tests := []struct {
		input string
		limit time.Duration
		asked time.Duration
	}{
		{`{"command": "ls"}`, 3 * time.Second, 0},
		{`{"command": "ls", "timeout": null}`, 3 * time.Second, 0},
		{`{"command": "ls", "timeout": 0}`, 3 * time.Second, 0},
		{`{"command": "ls", "timeout": 1500}`, 1500 * time.Millisecond, 0},
		{`{"command": "ls", "timeout": 1500.2}`, 1501 * time.Millisecond, 0},
		{`{"command": "ls", "timeout": "2000"}`, 2 * time.Second, 0},
		{`{"command": "ls", "timeout": 1e3}`, time.Second, 0},
		{`{"command": "ls", "timeout": 5000}`, 5 * time.Second, 0},
		{`{"command": "ls", "timeout": 5001}`, 5 * time.Second, 5001 * time.Millisecond},
		{`{"command": "ls", "timeout": 1e300}`, 5 * time.Second, 365 * 24 * time.Hour},
		{`{"command": "ls", "run_in_background": true}`, time.Minute, 0},
		{`{"command": "ls", "run_in_background": "true", "timeout": 30000}`, 30 * time.Second, 0},
		{`{"command": "ls", "run_in_background": true, "timeout": 3600000}`, time.Minute, time.Hour},
	}
	for _, tt := range tests {
		a, err := m.bashArgs(json.RawMessage(tt.input))
		if err != nil {
			t.Errorf("%s: %v", tt.input, err)
			continue
		}
		if a.limit != tt.limit || a.asked != tt.asked {
			t.Errorf("%s: limit %v (asked %v), want %v (asked %v)", tt.input, a.limit, a.asked, tt.limit, tt.asked)
		}
	}

	// With nothing set, a background command has no time limit.
	plain := newManager(t, Options{})
	if a, err := plain.bashArgs(json.RawMessage(`{"command": "ls", "run_in_background": true}`)); err != nil || a.limit != 0 {
		t.Errorf("background limit = %v, err %v; want none", a.limit, err)
	}
	if a, err := plain.bashArgs(json.RawMessage(`{"command": "ls"}`)); err != nil || a.limit != 2*time.Minute {
		t.Errorf("default limit = %v, err %v; want 2 minutes", a.limit, err)
	}
	if a, err := plain.bashArgs(json.RawMessage(`{"command": "ls", "timeout": 900000}`)); err != nil || a.limit != 10*time.Minute {
		t.Errorf("limit for a timeout over the maximum = %v, err %v; want 10 minutes", a.limit, err)
	}
}

func TestToolSpecs(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	var names []string
	for _, tool := range m.Tools() {
		spec := tool.Spec()
		names = append(names, spec.Name)
		if spec.Description == "" {
			t.Errorf("%s has no description", spec.Name)
		}
		var schema struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
			t.Errorf("%s: the schema is not JSON: %v\n%s", spec.Name, err, spec.InputSchema)
			continue
		}
		if schema.Type != "object" || len(schema.Properties) == 0 || len(schema.Required) != 1 {
			t.Errorf("%s: unexpected schema %s", spec.Name, spec.InputSchema)
		}
		if _, ok := tool.(core.Describer); !ok {
			t.Errorf("%s cannot say what a call would do", spec.Name)
		}
	}
	if got, want := strings.Join(names, " "), "Bash TaskOutput TaskStop"; got != want {
		t.Errorf("tools = %q, want %q", got, want)
	}
	bashSpec := toolNamed(t, m, BashName).Spec()
	for _, arg := range []string{"command", "description", "timeout", "run_in_background"} {
		if !strings.Contains(string(bashSpec.InputSchema), `"`+arg+`"`) {
			t.Errorf("the Bash schema does not have %s: %s", arg, bashSpec.InputSchema)
		}
	}
	if !strings.Contains(bashSpec.Description, "120 s") || !strings.Contains(bashSpec.Description, "600 s") {
		t.Errorf("the Bash description does not give the time limits:\n%s", bashSpec.Description)
	}

	// Without the output tool the others must not point the model at it.
	bare := newManager(t, Options{NoOutputTool: true})
	names = names[:0]
	for _, tool := range bare.Tools() {
		names = append(names, tool.Spec().Name)
		if strings.Contains(tool.Spec().Description, OutputName) {
			t.Errorf("%s still speaks of %s", tool.Spec().Name, OutputName)
		}
	}
	if got, want := strings.Join(names, " "), "Bash TaskStop"; got != want {
		t.Errorf("tools without the output tool = %q, want %q", got, want)
	}
}

func TestBadEnvironment(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	tool := toolNamed(t, m, BashName)
	call := core.ToolCall{ID: "c", Name: BashName, Input: json.RawMessage(`{"command": "echo hi"}`)}

	// No working directory at all is the harness's mistake, not the model's.
	if _, err := tool.Run(t.Context(), call, nil); err == nil {
		t.Error("Run with no environment returned no error")
	}
	if _, err := tool.Run(t.Context(), call, &core.ToolEnv{Cwd: "relative/dir"}); err == nil {
		t.Error("Run with a relative working directory returned no error")
	}

	// A working directory that has gone: the command runs in the nearest
	// folder above it that is there, and the session moves there.
	env := newEnv(t)
	root := env.Cwd
	env.Cwd = filepath.Join(root, "gone")
	res, err := tool.Run(t.Context(), call, env)
	if err != nil {
		t.Fatalf("Run in a missing directory: %v", err)
	}
	if res.IsError || res.Cwd != root || !strings.Contains(core.TextOf(res.Content), "was not there any more") {
		t.Errorf("result = %q (IsError %v, Cwd %q); want it run in %s", core.TextOf(res.Content), res.IsError, res.Cwd, root)
	}
	// With no folder of the session's there at all, it is something the
	// model can be told.
	env = newEnv(t)
	env.Roots = []string{filepath.Join(env.Cwd, "absent")}
	env.Cwd = filepath.Join(env.Cwd, "absent", "deeper")
	if res, err = tool.Run(t.Context(), call, env); err != nil {
		t.Fatalf("Run with no folder there: %v", err)
	}
	if !res.IsError || !strings.Contains(core.TextOf(res.Content), "is not there any more") {
		t.Errorf("result = %q (IsError %v); want a plain refusal", core.TextOf(res.Content), res.IsError)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
