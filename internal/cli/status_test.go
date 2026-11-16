package cli

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestDefaultStatuses covers the table as it ships: what the specs call P0
// works, and everything else is refused with a message that says it is not
// built, never accepted and ignored.
func TestDefaultStatuses(t *testing.T) {
	skip := ModeBypassPermissions
	runLines(t, Default(), []lineCase{
		{line: `gnosh`, want: inter(Invocation{})},
		{line: `gnosh "fix the build"`, want: inter(Invocation{Prompt: "fix the build"})},
		{line: `gnosh -p "prompt"`, want: pr(Invocation{Prompt: "prompt"})},
		{line: `gnosh -p -y --cwd DIR -m provider/model "task"`,
			want: pr(Invocation{PermissionMode: skip, Cwd: "DIR", Model: "provider/model", Prompt: "task"})},
		{line: `gnosh -y -C`, want: inter(Invocation{PermissionMode: skip, Conversation: ConversationContinue})},
		{line: `gnosh -y -c`, want: inter(Invocation{PermissionMode: skip, Conversation: ConversationContinue})},
		{line: `gnosh --dangerously-skip-permissions -r abc "more"`, want: inter(Invocation{PermissionMode: skip, Conversation: ConversationResume, Resume: "abc", Prompt: "more"})},
		{line: `gnosh -r`, want: inter(Invocation{Conversation: ConversationResume})},
		{line: `gnosh --session-id ` + sampleUUID, want: inter(Invocation{SessionID: sampleUUID})},
		{line: `gnosh -p x --output-format text --input-format text --max-turns 3`, want: pr(Invocation{Prompt: "x", MaxTurns: 3})},
		{line: `gnosh --settings ./s.json`, want: inter(Invocation{Settings: "./s.json"})},
		{line: `gnosh --version`, want: Invocation{Command: CmdVersion}},
		{line: `gnosh -v`, want: Invocation{Command: CmdVersion}},
		{line: `gnosh version`, want: Invocation{Command: CmdVersion}},
		{line: `gnosh -h`, want: helpFor()},
		{line: `gnosh help doctor`, want: helpFor("doctor")},

		{line: `gnosh --fork-session -c`, kind: ErrNotBuilt, say: []string{"--fork-session is not built yet in this build of gnosh, so nothing was started. Leave it out for now."}},
		{line: `gnosh -n x --verbose --safe-mode`, kind: ErrNotBuilt, say: []string{"-n, --verbose and --safe-mode are not built yet in this build of gnosh, so nothing was started. Leave them out for now."}},
		{line: `gnosh -n x -n y --name z`, kind: ErrNotBuilt, say: []string{"--name is not built yet"}},
		{line: `gnosh -p x --output-format json`, kind: ErrNotBuilt, say: []string{"--output-format json is not built yet"}},
		{line: `gnosh -p x --output-format=stream-json`, kind: ErrNotBuilt, say: []string{"--output-format stream-json is not built yet"}},
		// The value that counts is the last one.
		{line: `gnosh -p x --output-format json --output-format text`, want: pr(Invocation{Prompt: "x"})},
		{line: `gnosh --permission-mode plan`, kind: ErrNotBuilt, say: []string{"--permission-mode is not built yet"}},
		{line: `gnosh --allowed-tools Read`, kind: ErrNotBuilt, say: []string{"--allowed-tools is not built yet"}},
		{line: `gnosh --tools ""`, kind: ErrNotBuilt, say: []string{"--tools is not built yet"}},
		{line: `gnosh --system-prompt x`, kind: ErrNotBuilt},
		{line: `gnosh --debug`, kind: ErrNotBuilt, say: []string{"--debug is not built yet"}},
		{line: `gnosh -p -v x`, kind: ErrNotBuilt, say: []string{"-v is not built yet"}},
		{line: `gnosh -p --goal "done" x`, kind: ErrNotBuilt, say: []string{"--goal is not built yet"}},
		{line: `gnosh doctor`, kind: ErrNotBuilt, say: []string{`gnosh doctor is not built yet in this build of gnosh, so nothing was done. "gnosh doctor --help" says what it is for.`}},
		{line: `gnosh trust --list`, kind: ErrNotBuilt, say: []string{"gnosh trust is not built yet"}},
		{line: `gnosh models`, kind: ErrNotBuilt},
		{line: `gnosh s ls`, kind: ErrNotBuilt, say: []string{"gnosh session list is not built yet"}},
		{line: `gnosh session rename abc A title`, kind: ErrNotBuilt},
		{line: `gnosh mem show`, kind: ErrNotBuilt, say: []string{"gnosh mem is not built yet"}},
		{line: `gnosh logs -f`, kind: ErrNotBuilt},
		{line: `gnosh dirs`, kind: ErrNotBuilt},

		// Help for something not built still works: it is how to find out.
		{line: `gnosh doctor --help`, want: helpFor("doctor")},
		{line: `gnosh --fork-session --help`, want: helpFor()},
		{line: `gnosh mem --help`, want: helpFor("mem")},

		// A slip in the typing is reported before anything else; a value
		// that is wrong in itself before "not built"; "not built" before
		// what flags mean together and before the number of arguments.
		{line: `gnosh --fork-sesion -c`, kind: ErrUnknownFlag, hint: "--fork-session"},
		{line: `gnosh --permission-mode bogus`, kind: ErrBadValue},
		{line: `gnosh --fork-session`, kind: ErrNotBuilt},
		{line: `gnosh doctor now`, kind: ErrNotBuilt},
		{line: `gnosh --small-model m`, kind: ErrNotBuilt},
	})
}

func TestNotBuiltNames(t *testing.T) {
	_, err := Parse([]string{"-p", "x", "--name", "n", "--output-format", "json", "--verbose", "-n", "m"}, tty)
	var e *Error
	if !errors.As(err, &e) || e.Kind != ErrNotBuilt {
		t.Fatalf("Parse = %v; want a not-built error", err)
	}
	// The names are the ones SetStatus takes, in the order typed, each once.
	want := []string{"--output-format=json", "--verbose", "--name"}
	if !reflect.DeepEqual(e.NotBuilt, want) {
		t.Errorf("NotBuilt = %q, want %q", e.NotBuilt, want)
	}
	tab := Default()
	for _, name := range e.NotBuilt {
		if err := tab.SetStatus(name, Built); err != nil {
			t.Errorf("SetStatus(%q): %v", name, err)
		}
	}
	if _, err := tab.Parse([]string{"-p", "x", "--name", "n", "--output-format", "json", "--verbose", "-n", "m"}, tty); err != nil {
		t.Errorf("after marking them built, Parse still fails: %v", err)
	}

	_, err = Parse([]string{"session", "list"}, tty)
	if !errors.As(err, &e) || !reflect.DeepEqual(e.NotBuilt, []string{"session list"}) {
		t.Errorf("Parse(session list) = %v with NotBuilt %q; want [session list]", err, e.NotBuilt)
	}
}

func TestSetStatus(t *testing.T) {
	tab := Default()

	// Each kind of name, and the change it makes.
	for _, c := range []struct {
		name string
		line string
	}{
		{"--fork-session", `gnosh -c --fork-session`},
		{"doctor", `gnosh doctor`},
		{"--output-format=json", `gnosh -p x --output-format json`},
		{"mem", `gnosh mem show`},
	} {
		args := words(t, c.line)
		if _, err := tab.Parse(args, tty); err == nil {
			t.Errorf("%s parses before %s is marked built", c.line, c.name)
		}
		if err := tab.SetStatus(c.name, Built); err != nil {
			t.Fatalf("SetStatus(%q, Built): %v", c.name, err)
		}
		if _, err := tab.Parse(args, tty); err != nil {
			t.Errorf("%s fails after %s is marked built: %v", c.line, c.name, err)
		}
		if err := tab.SetStatus(c.name, NotBuilt); err != nil {
			t.Fatalf("SetStatus(%q, NotBuilt): %v", c.name, err)
		}
		if _, err := tab.Parse(args, tty); err == nil {
			t.Errorf("%s parses after %s is marked not built again", c.line, c.name)
		}
	}

	// A command and its flags are marked separately: nothing becomes built
	// by being near something that is.
	if err := tab.SetStatus("session list", Built); err != nil {
		t.Fatal(err)
	}
	if _, err := tab.Parse(words(t, `gnosh session list`), tty); err != nil {
		t.Errorf("gnosh session list fails though it is built: %v", err)
	}
	var e *Error
	if _, err := tab.Parse(words(t, `gnosh session list --json`), tty); !errors.As(err, &e) || e.Kind != ErrNotBuilt || !reflect.DeepEqual(e.NotBuilt, []string{"session --json"}) {
		t.Errorf("gnosh session list --json = %v; want --json refused as not built", err)
	}
	if err := tab.SetStatus("--permission-mode", Built); err != nil {
		t.Fatal(err)
	}
	if _, err := tab.Parse(words(t, `gnosh --permission-mode plan`), tty); !errors.As(err, &e) || !reflect.DeepEqual(e.NotBuilt, []string{"--permission-mode=plan"}) {
		t.Errorf("gnosh --permission-mode plan = %v; want the value plan refused as not built", err)
	}
	if err := tab.SetStatus("--permission-mode=default", Built); err != nil {
		t.Fatal(err)
	}
	if inv, err := tab.Parse(words(t, `gnosh --permission-mode manual`), tty); err != nil || inv.PermissionMode != ModeDefault {
		t.Errorf("gnosh --permission-mode manual = %+v, %v; want default, since manual is another spelling of it", inv.PermissionMode, err)
	}

	// Something built can be marked not built.
	if err := tab.SetStatus("--model", NotBuilt); err != nil {
		t.Fatal(err)
	}
	if _, err := tab.Parse(words(t, `gnosh -m x`), tty); !errors.As(err, &e) || e.Kind != ErrNotBuilt {
		t.Errorf("gnosh -m x = %v; want not built", err)
	}
	if err := tab.SetStatus("version", NotBuilt); err != nil {
		t.Fatal(err)
	}
	if _, err := tab.Parse(words(t, `gnosh version`), tty); !errors.As(err, &e) || e.Kind != ErrNotBuilt {
		t.Errorf("gnosh version = %v; want not built", err)
	}

	// Another table is not touched.
	if _, err := Default().Parse(words(t, `gnosh -m x`), tty); err != nil {
		t.Errorf("a fresh table was changed by another's SetStatus: %v", err)
	}

	// Names the table does not have are errors, with the nearest real one.
	for name, say := range map[string]string{
		"--fork-sesion":            `did you mean "--fork-session"?`,
		"fork-session":             `did you mean "--fork-session"?`,
		"sesion list":              `did you mean "session list"?`,
		"session":                  "is a group of commands",
		"--output-format=xml":      "has no value",
		"--permission-mode=manual": "has no value",
		"--permission-mode=auto":   "has no value",
		"--model=x":                "has no value",
		"gnosh":                    "has no command or flag named",
		"":                         "has no command or flag named",
		"--json":                   "has no command or flag named",
	} {
		err := tab.SetStatus(name, Built)
		if err == nil || !strings.Contains(err.Error(), say) {
			t.Errorf("SetStatus(%q) = %v; want an error that says %q", name, err, say)
		}
	}
}

// TestEntries checks the list of the table against what the specs give
// each row: P0 is built as shipped and nothing else is.
func TestEntries(t *testing.T) {
	tab := Default()
	seen := map[string]Entry{}
	for _, e := range tab.Entries() {
		if _, dup := seen[e.Name]; dup {
			t.Errorf("two entries are named %q", e.Name)
		}
		seen[e.Name] = e
		if e.Priority < 0 || e.Priority > 2 {
			t.Errorf("%s has priority %d", e.Name, e.Priority)
		}
		if (e.Status == Built) != (e.Priority == 0) {
			t.Errorf("%s is P%d and ships %s; P0 ships built and nothing else does", e.Name, e.Priority, e.Status)
		}
		if len(e.Spellings) == 0 {
			t.Errorf("%s has no spellings", e.Name)
		}
		if e.Kind != EntryValue && e.Help == "" {
			t.Errorf("%s has no help", e.Name)
		}
		if e.Kind == EntryFlag && len(e.Commands) == 0 {
			t.Errorf("no command takes %s", e.Name)
		}
		if err := tab.SetStatus(e.Name, e.Status); err != nil {
			t.Errorf("SetStatus does not take the entry's own name %q: %v", e.Name, err)
		}
	}
	for name, want := range map[string]Entry{
		"--continue":                {Kind: EntryFlag, Spellings: []string{"-c", "-C", "--continue"}, Commands: []string{"gnosh"}, Priority: 0, Status: Built},
		"--verbose":                 {Kind: EntryFlag, Spellings: []string{"--verbose", "-v"}, Commands: []string{"gnosh"}, Priority: 1},
		"--version":                 {Kind: EntryFlag, Spellings: []string{"-v", "--version"}, Commands: []string{"gnosh"}, Priority: 0, Status: Built},
		"--yolo":                    {Kind: EntryFlag, Spellings: []string{"-y", "--yolo", "--dangerously-skip-permissions"}, Commands: []string{"gnosh"}, Priority: 0, Status: Built},
		"--allowedTools":            {Kind: EntryFlag, Spellings: []string{"--allowedTools", "--allowed-tools"}, Commands: []string{"gnosh"}, Priority: 1},
		"--reasoning-effort":        {Kind: EntryFlag, Spellings: []string{"--reasoning-effort", "--effort"}, Commands: []string{"gnosh"}, Priority: 1},
		"--cwd":                     {Kind: EntryFlag, Spellings: []string{"--cwd"}, Commands: []string{"gnosh", "gnosh doctor", "gnosh trust", "gnosh models", "gnosh session list", "gnosh session show", "gnosh session last", "gnosh session rename", "gnosh mem", "gnosh bridge", "gnosh logs", "gnosh dirs", "gnosh mcp add", "gnosh mcp add-json", "gnosh mcp get", "gnosh mcp list", "gnosh mcp remove"}, Priority: 0, Status: Built},
		"session --json":            {Kind: EntryFlag, Spellings: []string{"--json"}, Commands: []string{"gnosh session list", "gnosh session show", "gnosh session last", "gnosh session rename"}, Priority: 1},
		"logs --tail":               {Kind: EntryFlag, Spellings: []string{"-t", "--tail"}, Commands: []string{"gnosh logs"}, Priority: 1},
		"help":                      {Kind: EntryCommand, Spellings: []string{"help"}, Priority: 0, Status: Built},
		"doctor":                    {Kind: EntryCommand, Spellings: []string{"doctor"}, Priority: 1},
		"session list":              {Kind: EntryCommand, Spellings: []string{"session list", "session ls", "sessions list", "sessions ls", "s list", "s ls"}, Priority: 1},
		"session rename":            {Kind: EntryCommand, Spellings: []string{"session rename", "sessions rename", "s rename"}, Priority: 1},
		"--output-format=text":      {Kind: EntryValue, Spellings: []string{"text"}, Priority: 0, Status: Built},
		"--input-format=text":       {Kind: EntryValue, Spellings: []string{"text"}, Priority: 0, Status: Built},
		"--output-format=json":      {Kind: EntryValue, Spellings: []string{"json"}, Priority: 1},
		"--permission-mode=default": {Kind: EntryValue, Spellings: []string{"default", "manual"}, Priority: 1},
	} {
		got, ok := seen[name]
		if !ok {
			t.Errorf("the table has no entry named %q", name)
			continue
		}
		want.Name, want.Help = name, got.Help
		if !reflect.DeepEqual(got, want) {
			t.Errorf("entry %q:\n  got  %+v\n  want %+v", name, got, want)
		}
	}
	for _, absent := range []string{"session", "gnosh", "--permission-mode=auto", "--permission-mode=manual"} {
		if _, ok := seen[absent]; ok {
			t.Errorf("the table lists %q, which has no status of its own", absent)
		}
	}
}

// TestPrintTable prints the table, one row a line, when GNOSH_CLI_PRINT_TABLE
// is set. It is how the table in a report is made, so that it is never
// copied out by hand.
func TestPrintTable(t *testing.T) {
	if os.Getenv("GNOSH_CLI_PRINT_TABLE") == "" {
		t.Skip("set GNOSH_CLI_PRINT_TABLE to print the table")
	}
	for _, e := range Default().Entries() {
		fmt.Printf("TABLE\t%s\tP%d\t%s\t%s\t%s\n", e.Kind, e.Priority, e.Status, e.Name, strings.Join(e.Spellings, " | "))
	}
}
