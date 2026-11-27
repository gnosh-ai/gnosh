package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// Commands on other machines, through the whole program. No test here
// leaves this machine: the world's PATH begins with a stand-in for ssh
// (apptest.FarMachine) that notes the destination it was called with and
// runs the command in a directory of its own.

// hostWorld is a world whose user config declares two hosts, with the
// made-up machine behind them.
func hostWorld(t *testing.T, replies ...apptest.Reply) (*apptest.World, *apptest.FarMachine) {
	t.Helper()
	w := apptest.NewWorld(t, replies...)
	far := w.FarMachine()
	w.Settings(`{"hosts": {"render": {"ssh": "root@203.0.113.7", "description": "the render box"}, "web": {"ssh": "deploy@203.0.113.8"}}}`)
	return w, far
}

// A shell call that names a declared host runs there, and what the model
// reads says which machine it came from; a call without one runs here and
// says so too. The machine is in the call's record, in what hooks are
// given, and in the line a person is shown.
func TestCommandOnAHost(t *testing.T) {
	w, far := hostWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"echo made there > there.txt; echo it ran","host":"render","description":"make a file"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Bash", Args: `{"command":"echo it ran here"}`}}},
		apptest.Reply{Text: "Both ran."})
	seen := filepath.Join(w.Home, "hook-input.json")
	hook := w.Script(w.Home+"/hooks/note.sh", `cat >> "`+seen+`"`)
	w.Hook("PreToolUse", "Bash", hook)
	r := run(t, w, "-y", "-p", "--verbose", "--output-format", "stream-json", "run it on the render box").wantCode(t, 0)

	// What the model read: the machine first, then the output.
	if got, want := toolResult(t, w, 1, "c1"), "<host>render (root@203.0.113.7)</host><cwd>"+far.Home+"</cwd>\nit ran"; got != want {
		t.Errorf("the model read %q of the command on the host, want %q", got, want)
	}
	here := toolResult(t, w, 2, "c2")
	if !strings.HasPrefix(here, "<host>local (") || !strings.HasSuffix(here, "</host><cwd>"+w.Dir+"</cwd>\nit ran here") {
		t.Errorf("the model read %q of the command on this machine", here)
	}

	// Where it ran: on the made-up machine, in its own directory, and not
	// in the session's folder.
	if data, err := os.ReadFile(filepath.Join(far.Home, "there.txt")); err != nil || string(data) != "made there\n" {
		t.Errorf("the command did not run on the made-up machine: %q, %v", data, err)
	}
	if w.Exists("there.txt") {
		t.Errorf("the command for the host ran in the session's own folder")
	}
	if calls := far.Calls(); !slices.Equal(calls, []string{"root@203.0.113.7"}) {
		t.Errorf("ssh was called for %q, want once for root@203.0.113.7", calls)
	}

	// The record kept of each call names the machine.
	var metas []map[string]any
	for _, line := range jsonLines(t, r.out) {
		if meta, ok := line["tool_use_result"].(map[string]any); ok {
			metas = append(metas, meta)
		}
	}
	if len(metas) != 2 || metas[0]["host"] != "render" || metas[0]["machine"] != "root@203.0.113.7" || metas[0]["cwd"] != far.Home || metas[0]["exitCode"] != float64(0) ||
		metas[1]["host"] != "local" || metas[1]["cwd"] != w.Dir {
		t.Errorf("the calls' records: %v", metas)
	}
	file, err := os.ReadFile(w.Sessions()[0])
	if err != nil {
		t.Fatal(err)
	}
	has(t, "the session's file", string(file), `"host":"render"`, `"machine":"root@203.0.113.7"`, `"host":"local"`)

	// A hook sees the host among the call's arguments, like any other.
	has(t, "what the PreToolUse hook was given", w.Read(seen), `"host":"render"`, `"tool_name":"Bash"`)

	// The one line a person is shown for the call names the machine when
	// it is not this one.
	has(t, "stderr", r.err, "Bash on render: echo made there > there.txt; echo it ran (exit code 0)", "Bash: echo it ran here (exit code 0)")
	hasNot(t, "stderr", r.err, "Bash on local")
}

// The model is told which machines are declared, with what the person
// wrote about each, in the block that goes with the first prompt, and only
// when there are any. The system prompt and the tools are the same bytes
// either way.
func TestHostsAreNamedWithTheFirstPrompt(t *testing.T) {
	with, _ := hostWorld(t, apptest.Reply{Text: "ok"})
	run(t, with, "-p", "hello").wantCode(t, 0)
	without := apptest.NewWorld(t, apptest.Reply{Text: "ok"})
	run(t, without, "-p", "hello").wantCode(t, 0)

	first := strings.Join(userTexts(with.Model.Requests()[0]), "\n")
	has(t, "the first prompt with hosts declared", first,
		"- Other machines you may run commands on, by naming one as `host` in a Bash call (never with ssh or scp in a command):",
		"  - render (root@203.0.113.7): the render box",
		"  - web (deploy@203.0.113.8)",
		"A path belongs to the machine it was seen on, and the file tools reach only this machine.",
		"- You run as ")
	hasNot(t, "the first prompt with no hosts", strings.Join(userTexts(without.Model.Requests()[0]), "\n"), "Other machines", "203.0.113", "A path belongs to")

	p1, t1 := fixedPart(t, requestText(t, with, 0))
	p2, t2 := fixedPart(t, requestText(t, without, 0))
	if p1 != p2 {
		t.Errorf("the system prompt differs when hosts are declared:\n%s\n---\n%s", clip(p1), clip(p2))
	}
	if t1 != t2 {
		t.Errorf("the tools differ when hosts are declared:\n%s\n---\n%s", clip(t1), clip(t2))
	}
	hasNot(t, "the fixed part of a request", p1+t1, "the render box", "203.0.113")
	has(t, "the tools", t1, `"host":{"type":"string"`, "<host>name (account@address)</host><cwd>directory</cwd>")
}

// A call for another machine always asks, when anything asks at all: the
// allow rules and the read-only list are about this machine. The person's
// deny rules are held against the command before it leaves. With
// permissions skipped the declaration is the whole gate.
func TestHostCallsAndPermissions(t *testing.T) {
	onRender := func(command string) []apptest.Reply {
		args, _ := json.Marshal(map[string]string{"command": command, "host": "render"})
		return []apptest.Reply{{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: string(args)}}}, {Text: "So it went."}}
	}
	t.Run("nobody to ask: it does not run, and the model reads why", func(t *testing.T) {
		w, far := hostWorld(t, onRender("ls")...)
		run(t, w, "-p", "list the box").wantCode(t, 0)
		has(t, "what the model read", toolResult(t, w, 1, "c1"), "needs a person's approval", "The command runs on the host render")
		if calls := far.Calls(); len(calls) != 0 {
			t.Errorf("ssh was called (%q) for a call nobody approved", calls)
		}
	})
	t.Run("an allow rule is about this machine", func(t *testing.T) {
		w, far := hostWorld(t, onRender("touch made.txt")...)
		w.Allow("Bash(touch *)")
		run(t, w, "-p", "make it").wantCode(t, 0)
		has(t, "what the model read", toolResult(t, w, 1, "c1"), "needs a person's approval", "The command runs on the host render")
		if calls := far.Calls(); len(calls) != 0 {
			t.Errorf("ssh was called (%q): an allow rule let a command run on another machine", calls)
		}
	})
	t.Run("a deny rule stops the command before it leaves", func(t *testing.T) {
		w, far := hostWorld(t, onRender("cd /srv && rm -rf build")...)
		w.Deny("Bash(rm *)")
		run(t, w, "-y", "-p", "clean up").wantCode(t, 0)
		has(t, "what the model read", toolResult(t, w, 1, "c1"), "Bash(rm *)")
		if calls := far.Calls(); len(calls) != 0 {
			t.Errorf("ssh was called (%q) for a command a deny rule names", calls)
		}
	})
	t.Run("with permissions skipped it runs", func(t *testing.T) {
		w, far := hostWorld(t, onRender("touch made.txt")...)
		run(t, w, "-y", "-p", "make it").wantCode(t, 0)
		if _, err := os.Stat(filepath.Join(far.Home, "made.txt")); err != nil {
			t.Errorf("the command did not run on the host: %s", toolResult(t, w, 1, "c1"))
		}
	})
	t.Run("a host that cannot be reached: the model and the person read why", func(t *testing.T) {
		w, far := hostWorld(t, onRender("touch made.txt")...)
		far.Fail("ssh: connect to host 203.0.113.7 port 22: Connection refused")
		r := run(t, w, "-y", "-p", "--verbose", "make it").wantCode(t, 0)
		got := toolResult(t, w, 1, "c1")
		if !strings.HasPrefix(got, "<host>render (root@203.0.113.7)</host>\n[not run: render (root@203.0.113.7) refused the connection") {
			t.Errorf("the model read %q", got)
		}
		has(t, "what the model read", got, "Nothing was run on render", "ssh on this machine, or the login on render, said: ssh: connect to host 203.0.113.7 port 22: Connection refused")
		// The person's line says what failed, without the line that is
		// there for the model.
		has(t, "stderr", r.err, "Bash on render: touch made.txt (failed: [not run: render (root@203.0.113.7) refused the connection")
		hasNot(t, "stderr", r.err, "<host>")
	})
	t.Run("this machine named as the host is this machine", func(t *testing.T) {
		// A command that only reads runs without a question here, and
		// naming the machine it would run on anyway changes nothing.
		w, far := hostWorld(t,
			apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"ls","host":"local"}`}}},
			apptest.Reply{Text: "Listed."})
		w.Write("a.txt", "here\n")
		run(t, w, "-p", "list it").wantCode(t, 0).wantNoErr(t)
		got := toolResult(t, w, 1, "c1")
		if !strings.HasPrefix(got, "<host>local (") || !strings.Contains(got, "a.txt") {
			t.Errorf("the model read %q", got)
		}
		if calls := far.Calls(); len(calls) != 0 {
			t.Errorf("ssh was called (%q) for this machine", calls)
		}
	})
	t.Run("a person is shown the machine, and their yes runs it", func(t *testing.T) {
		w, far := hostWorld(t,
			apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"echo line one; echo line two; touch made.txt","host":"render","description":"make the file"}`}}},
			apptest.Reply{Text: "Made."})
		s := sit(t, w)
		s.see("Type a prompt and Enter.")
		s.typeLine("make the file on the render box")
		s.see("Bash on render: echo line one; echo line two; touch made.txt")
		s.see("? Allow Bash?")
		s.see("On render (root@203.0.113.7): make the file")
		s.see("$ echo line one; echo line two; touch made.txt")
		s.see("The command runs on the host render")
		s.see("  1. Yes, this once")
		if calls := far.Calls(); len(calls) != 0 {
			t.Fatalf("ssh was called (%q) before the question was answered", calls)
		}
		s.typeLine("1")
		s.see("    line one")
		s.see("    line two")
		s.see("Made.")
		s.see("-- done in ")
		if _, err := os.Stat(filepath.Join(far.Home, "made.txt")); err != nil {
			t.Errorf("the command did not run on the host after the yes")
		}
		// The line that tells the model where the command ran is not
		// printed at the person: the call's own line named the machine.
		hasNot(t, "the screen", s.out.String(), "<host>", "<cwd>")
		s.typeLine("/exit")
		s.exit()
	})
}

// A folder's own hosts wait for the folder to be trusted, like everything
// else of its own that reaches out.
func TestHostsOfAFolderWaitForTrust(t *testing.T) {
	script := func() []apptest.Reply {
		return []apptest.Reply{
			{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"touch made.txt","host":"far"}`}}},
			{Text: "So it went."},
		}
	}
	w := apptest.NewWorld(t, script()...)
	far := w.FarMachine()
	w.Write("gnosh.json", `{"hosts": {"far": {"ssh": "root@203.0.113.9", "description": "a stranger's machine"}}}`+"\n")
	r := run(t, w, "-y", "-p", "make it there").wantCode(t, 0)
	has(t, "stderr", r.err, "is not trusted")
	has(t, "what the model read", toolResult(t, w, 1, "c1"), `the host "far" was given`, "no other machine is declared for this session", "ask the user to declare it")
	hasNot(t, "the first prompt", strings.Join(userTexts(w.Model.Requests()[0]), "\n"), "Other machines", "203.0.113.9", "a stranger's machine")
	if calls := far.Calls(); len(calls) != 0 {
		t.Errorf("ssh was called (%q) for the host of a folder that is not trusted", calls)
	}
	if _, err := os.Stat(filepath.Join(far.Home, "made.txt")); err == nil {
		t.Error("the command ran on the host of a folder that is not trusted")
	}

	// Trusted for the run, the host is declared and the command runs there.
	w = apptest.NewWorld(t, script()...)
	far = w.FarMachine()
	w.Write("gnosh.json", `{"hosts": {"far": {"ssh": "root@203.0.113.9", "description": "a stranger's machine"}}}`+"\n")
	run(t, w, "--trust-folder", "-y", "-p", "make it there").wantCode(t, 0)
	has(t, "the first prompt", strings.Join(userTexts(w.Model.Requests()[0]), "\n"), "  - far (root@203.0.113.9): a stranger's machine")
	if calls := far.Calls(); !slices.Equal(calls, []string{"root@203.0.113.9"}) {
		t.Errorf("ssh was called for %q", calls)
	}
}

// With hosts declared, ssh or scp typed into a command is refused with a
// pointer to the host argument, whatever the permissions. Without hosts it
// is a command like any other, and the person's own command line is never
// held to the rule.
func TestSSHInACommand(t *testing.T) {
	w, far := hostWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"ssh root@203.0.113.7 'echo reached'"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Bash", Args: `{"command":"cd /tmp && scp notes.txt root@203.0.113.7:"}`}}},
		apptest.Reply{Text: "So it went."})
	run(t, w, "-y", "-p", "see how the box is").wantCode(t, 0)
	has(t, "what the model read of the ssh", toolResult(t, w, 1, "c1"),
		"This call did not run: the command runs ssh", "naming it as host in the call, never with ssh in a command",
		"The declared hosts are: render (root@203.0.113.7), web (deploy@203.0.113.8)")
	has(t, "what the model read of the scp", toolResult(t, w, 2, "c2"), "This call did not run: the command runs scp", "tell the user what needs copying")
	if calls := far.Calls(); len(calls) != 0 {
		t.Errorf("ssh was run (%q) by a command that was refused", calls)
	}

	// No hosts declared: the same ssh command runs (here it finds the
	// stand-in, which notes the call and runs the command in its own
	// directory).
	w = apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"ssh root@203.0.113.7 'echo reached'"}`}}},
		apptest.Reply{Text: "So it went."})
	far = w.FarMachine()
	run(t, w, "-y", "-p", "see how the box is").wantCode(t, 0)
	got := toolResult(t, w, 1, "c1")
	hasNot(t, "what the model read", got, "This call did not run")
	if !strings.HasPrefix(got, "<host>local (") || !strings.HasSuffix(got, "\nreached") {
		t.Errorf("without hosts declared, the model read %q of ssh in a command", got)
	}
	if calls := far.Calls(); !slices.Equal(calls, []string{"root@203.0.113.7"}) {
		t.Errorf("without hosts declared, ssh in a command was run for %q, want once", calls)
	}

	// The person's own command, typed behind "!", is theirs.
	w, far = hostWorld(t)
	s := sit(t, w)
	s.see("Type a prompt and Enter.")
	s.typeLine(`!ssh -- root@203.0.113.7 'printf "%s %s\n" typed "by hand"'`)
	s.see("typed by hand")
	s.typeLine("/exit")
	s.exit()
	if calls := far.Calls(); !slices.Equal(calls, []string{"root@203.0.113.7"}) {
		t.Errorf("the person's own ssh: calls %q", calls)
	}
	if n := len(w.Model.Requests()); n != 0 {
		t.Errorf("the person's own ssh triggered %d model requests, want 0", n)
	}
	hasNot(t, "the screen", s.out.String(), "the command runs ssh")
}

// gnosh doctor and /status list the declared hosts. Doctor reaches none of
// them unless it is asked to with --hosts, and a host that was not tried
// is not called fine.
func TestDoctorAndStatusListHosts(t *testing.T) {
	// finding is a line of doctor's report about hosts, as it is printed.
	finding := func(level, text string) string { return fmt.Sprintf("%-4s %-11s %s", level, "hosts", text) }
	w, far := hostWorld(t)
	r := run(t, w, "doctor").wantCode(t, 0)
	has(t, "doctor", r.out,
		finding("note", "render → root@203.0.113.7 (the render box) is declared"),
		finding("note", "web → deploy@203.0.113.8 is declared"),
		"It was not tried: gnosh doctor --hosts logs in to each host with ssh and runs nothing there.",
		"A turn can run.")
	hasNot(t, "doctor", r.out, finding("ok", ""))
	if calls := far.Calls(); len(calls) != 0 {
		t.Fatalf("gnosh doctor reached out to %q without being asked", calls)
	}

	// Asked: each host is logged in to, and nothing is run there.
	r = run(t, w, "doctor", "--hosts").wantCode(t, 0)
	has(t, "doctor --hosts", r.out,
		finding("ok", "render → root@203.0.113.7 (the render box) answers: a command there starts in "+far.Home+"."),
		finding("ok", "web → deploy@203.0.113.8 answers"))
	if calls := far.Calls(); !slices.Equal(calls, []string{"root@203.0.113.7", "deploy@203.0.113.8"}) {
		t.Errorf("gnosh doctor --hosts called ssh for %q", calls)
	}
	if entries, _ := os.ReadDir(far.Home); len(entries) != 0 {
		t.Errorf("trying the hosts left %d files on the made-up machine", len(entries))
	}

	// A host that cannot be reached is said so, in plain words, and stops
	// no turn.
	far.Fail("No ED25519 host key is known for 203.0.113.7 and you have requested strict checking.\nHost key verification failed.")
	r = run(t, w, "doctor", "--hosts").wantCode(t, 0)
	has(t, "doctor --hosts with a key that is not known", r.out,
		finding("note", "render → root@203.0.113.7 (the render box) cannot be used right now: this account's ssh has no key on record for render (root@203.0.113.7)"),
		"gnosh never accepts a machine's key by itself", "connect once by hand", "A turn can run.")
	far.Fail("")

	// Inside a session: /status shows them, and /doctor tries them only
	// with the word hosts after it.
	before := len(far.Calls())
	has(t, "/status", run(t, w, "-p", "/status").wantCode(t, 0).out,
		fmt.Sprintf("%-12s %s (", "machine", "local"),
		fmt.Sprintf("%-12s %s", "hosts", "render (root@203.0.113.7): the render box"),
		fmt.Sprintf("%-12s %s", "", "web (deploy@203.0.113.8)"),
		"render → root@203.0.113.7: the render box")
	has(t, "/doctor", run(t, w, "-p", "/doctor").wantCode(t, 0).out, "render → root@203.0.113.7 (the render box) is declared", "It was not tried")
	if n := len(far.Calls()); n != before {
		t.Errorf("/status and /doctor called ssh %d times", n-before)
	}
	has(t, "/doctor hosts", run(t, w, "-p", "/doctor hosts").wantCode(t, 0).out, "render → root@203.0.113.7 (the render box) answers")
	if n := len(far.Calls()); n != before+2 {
		t.Errorf("/doctor hosts called ssh %d times, want 2", n-before)
	}
	has(t, "/doctor with another word", run(t, w, "-p", "/doctor everything").err, "/doctor takes nothing after it, or the word hosts")
}

// A mistake in a host's declaration stops startup like any other, with the
// file and the line.
func TestHostMistakesStopStartup(t *testing.T) {
	// Each is one more host in the person's own file, which declares two
	// already; the mistake is on the line the host is written on.
	for host, pieces := range map[string][]string{
		`"Local": {"ssh": "root@203.0.113.9"}`:  {"a host cannot be named Local", "local is the name of this machine"},
		`"far": {"ssh": "root@"}`:               {"the ssh destination of the host far", "is not a user and an address with one @ between them"},
		`"far": {"ssh": "root@203.0.113.9:22"}`: {"ends in a port number"},
		`"far": {"description": "no machine"}`:  {"the host far has no ssh destination"},
		`"far": "root@203.0.113.9"`:             {"a host is an object", `"ssh"`},
	} {
		w, far := hostWorld(t)
		w.Write(w.SettingsFile(), "{\n  \"hosts\": {\n    \"render\": {\"ssh\": \"root@203.0.113.7\"},\n    "+host+"\n  }\n}\n")
		r := run(t, w, "-p", "hello").wantCode(t, 1)
		has(t, "stderr for "+host, r.err, append(pieces, "gnosh.json:4:", "the configuration has a mistake, so nothing was started")...)
		if n := len(w.Model.Requests()); n != 0 || len(far.Calls()) != 0 {
			t.Errorf("%q: the model was sent %d requests and ssh was called %d times", host, n, len(far.Calls()))
		}
	}
	// A name stands for one machine: the folder's file may not declare a
	// host the person's own file declares, and both places are named.
	w, far := hostWorld(t)
	w.Write("gnosh.json", "{\n  \"hosts\": {\"render\": {\"ssh\": \"root@203.0.113.9\"}}\n}\n")
	r := run(t, w, "--trust-folder", "-p", "hello").wantCode(t, 1)
	has(t, "stderr", r.err, w.Path("gnosh.json")+":2:", "the host render is declared twice", `here as "root@203.0.113.9"`, w.SettingsFile()+":", `as "root@203.0.113.7"`)
	if n := len(w.Model.Requests()); n != 0 || len(far.Calls()) != 0 {
		t.Errorf("the model was sent %d requests and ssh was called %d times", n, len(far.Calls()))
	}
}
