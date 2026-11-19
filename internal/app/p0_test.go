package app

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// A session that is busy can always be asked what it is doing, without
// stopping it: /status answers in the middle of a turn, a prompt typed
// meanwhile waits its turn and is then answered, and with GNOSH_DEBUG_PAGE
// set a page on this machine shows the state and every goroutine.
func TestBusySessionCanBeExamined(t *testing.T) {
	hold := make(chan struct{})
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "This first answer is held back by the model server.", Hold: hold},
		apptest.Reply{Text: "And this is the answer to the second prompt."})
	pr, pw := io.Pipe()
	s := &seat{t: t, in: pw, out: &safeBuffer{}, errs: &safeBuffer{}, done: make(chan int, 1)}
	o := testOptions(w)
	o.Environ = append(o.Environ, "GNOSH_DEBUG_PAGE=1")
	o.Stdin, o.Stdout, o.Stderr = pr, s.out, s.errs
	o.StdinIsTerminal, o.StdoutIsTerminal = true, true
	go func() { s.done <- Main(t.Context(), o) }()
	defer func() { pw.Close(); <-s.done }()

	// The notice about the page is raised by the session as it starts and
	// shown by a goroutine of its own: it may stand before or after the
	// lines the front end writes first, so neither is looked for after
	// the other.
	s.seeAnywhere("the debugging page is at http://127.0.0.1:")
	page := regexp.MustCompile(`http://127\.0\.0\.1:\d+/`).FindString(s.out.String())
	s.seeAnywhere("Type a prompt and Enter.")
	s.typeLine("the first prompt")
	s.see("This first answer")

	// Typed while the turn runs: it waits.
	s.typeLine("the second prompt")
	s.see("(1 prompt(s) wait for their turn.)")
	// Asked while the turn runs: it answers.
	s.typeLine("/status")
	s.see("state        reading-reply")
	s.see("queued       1 prompt(s) wait for their turn")

	// The page, from this machine.
	get := func(path, origin string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, page+path, nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("the debugging page: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if code, body := get("", ""); code != 200 || !strings.Contains(body, "busy: true") || !strings.Contains(body, "queued prompts: 1") {
		t.Errorf("the debugging page: %d\n%s", code, body)
	}
	if code, body := get("goroutines", ""); code != 200 || !strings.Contains(body, "goroutine ") {
		t.Errorf("the goroutines page: %d\n%s", code, clip(body))
	}
	if code, _ := get("", "http://example.com"); code != http.StatusForbidden {
		t.Errorf("a page of another site was answered with %d, want 403", code)
	}

	close(hold)
	s.see("back by the model server.")
	s.see("And this is the answer to the second prompt.")
	s.see("-- done in ")
	s.typeLine("/exit")
	s.exit()
	if users := userTexts(w.Model.Requests()[1]); len(users) != 2 || users[1] != "the second prompt" {
		t.Errorf("the queued prompt went out as %q", users)
	}
}

// The ways gnosh is started that the first binary must have.
func TestCommandLineForms(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "one"}, apptest.Reply{Text: "two"}, apptest.Reply{Text: "three"}, apptest.Reply{Text: "four"})
	w.Model.SetModels("test-model", "other-model")

	// gnosh -p answers one prompt and exits.
	r := run(t, w, "-p", "--output-format", "json", "first").wantCode(t, 0)
	id := jsonLines(t, r.out)[0]["session_id"].(string)
	// -r names the session by its id; -C continues, as -c does.
	r = run(t, w, "-r", id, "-p", "--output-format", "json", "second").wantCode(t, 0)
	if got := jsonLines(t, r.out)[0]["session_id"]; got != id {
		t.Errorf("-r went on in %v", got)
	}
	r = run(t, w, "-C", "-p", "--output-format", "json", "third").wantCode(t, 0)
	if got := jsonLines(t, r.out)[0]["session_id"]; got != id {
		t.Errorf("-C went on in %v", got)
	}
	if users := userTexts(w.Model.Requests()[2]); len(users) != 3 {
		t.Errorf("the third run's user messages: %q", users)
	}
	// -m names the model for one run, and the choice is not kept.
	run(t, w, "-m", "local/other-model", "-p", "fourth").wantCode(t, 0)
	if got := w.Model.Requests()[3].JSON["model"]; got != "other-model" {
		t.Errorf("-m: the request names the model %v", got)
	}
	if w.Exists(w.DataDir() + "/state.json") {
		t.Errorf("-m was saved for later sessions")
	}
	w.Model.Done()

	// What was taken off the command line is refused, never read as what
	// it once meant: no model is asked anything.
	gone := apptest.NewWorld(t)
	for _, args := range [][]string{
		{"-p", "-q", "x"}, {"-s", id, "-p", "x"}, {"-D", gone.Root + "/elsewhere", "-p", "x"},
		{"session", "delete", id}, {"-p", "--input-format", "stream-json"},
	} {
		r := run(t, gone, args...).wantCode(t, 1)
		has(t, "stderr for "+strings.Join(args, " "), r.err, "nothing was")
	}
	if n := len(gone.Model.Requests()); n != 0 {
		t.Errorf("a command line that was refused sent %d requests", n)
	}
	if found := globAll(gone.Root + "/elsewhere/*"); len(found) != 0 {
		t.Errorf("-D made something under the directory it named: %v", found)
	}

	// gnosh "prompt" opens a session with the prompt as its first message.
	w2 := apptest.NewWorld(t, apptest.Reply{Text: "Hello to you."})
	s := sit(t, w2, "hello there")
	s.see("Hello to you.")
	s.see("-- done in ")
	s.typeLine("/exit")
	if code := s.exit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// The instruction files of the folder, of the folders above it and of the
// person reach the model, the most general first.
func TestInstructionFilesAreRead(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "ok"})
	w.Write(w.ConfigDir()+"/GNOSH.md", "The person's own rule: answer briefly.\n")
	w.Write("AGENTS.md", "The project's rule: tests live in ./tests.\n")
	w.Write("sub/AGENTS.md", "The subfolder's rule: this part is in Go.\n")
	w.Write("sub/deeper/AGENTS.md", "A rule for a folder nobody has worked in yet.\n")
	o := testOptions(w, "-p", "hello")
	o.Cwd = w.Path("sub")
	runWith(t, t.Context(), o).wantCode(t, 0).wantNoErr(t)
	first := userTexts(w.Model.Requests()[0])[0]
	a := strings.Index(first, "The person's own rule")
	b := strings.Index(first, "The project's rule")
	c := strings.Index(first, "The subfolder's rule")
	if a < 0 || b < a || c < b {
		t.Errorf("the instruction files are not all there, the most general first (%d, %d, %d): %s", a, b, c, clip(first))
	}
	hasNot(t, "the first message", first, "nobody has worked in yet")
	if !strings.HasPrefix(first, "<gnosh>") {
		t.Errorf("the instruction files are not marked as gnosh's own words: %s", clip(first))
	}
}

// claudeMdExcludes keeps the files it names out of what the model reads.
// A pattern the reader cannot use stops startup, at the line that sets
// it: it would keep nothing out.
func TestClaudeMdExcludesLeaveFilesOut(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "ok"})
	w.Settings(`{"options": {"claudeMdExcludes": ["**/vendor/**/AGENTS.md"]}}`)
	w.Write("AGENTS.md", "The project's rule: tests live in ./tests.\n")
	w.Write("vendor/lib/AGENTS.md", "A vendored rule nobody here wrote.\n")
	o := testOptions(w, "-p", "hello")
	o.Cwd = w.Path("vendor/lib")
	runWith(t, t.Context(), o).wantCode(t, 0).wantNoErr(t)
	first := userTexts(w.Model.Requests()[0])[0]
	has(t, "the first message", first, "The project's rule")
	hasNot(t, "the first message", first, "nobody here wrote")

	bad := apptest.NewWorld(t)
	bad.Settings(`{"options": {"claudeMdExcludes": ["src/[a-"]}}`)
	r := run(t, bad, "-p", "hello").wantCode(t, 1)
	has(t, "stderr", r.err, "gnosh.json:", "claudeMdExcludes", "no ] closes")
	if n := len(bad.Model.Requests()); n != 0 {
		t.Errorf("the model was sent %d requests", n)
	}
}

// A setting Claude Code has and this gnosh has not built stops startup,
// with a message that says so. It is never taken and left unused.
func TestConfigForWhatIsNotBuiltStopsStartup(t *testing.T) {
	for _, key := range []string{"spinnerVerbs", "enabledMcpjsonServers", "sandbox"} {
		t.Run(key, func(t *testing.T) {
			w := apptest.NewWorld(t)
			w.Write(w.SettingsFile(), "{\n  \""+key+"\": {}\n}\n")
			r := run(t, w, "-p", "hello").wantCode(t, 1)
			has(t, "stderr", r.err, "gnosh.json:2:", `"`+key+`" is a setting Claude Code has and gnosh has not built`, "would do nothing here", "so nothing was started")
			if n := len(w.Model.Requests()); n != 0 {
				t.Errorf("the model was sent %d requests", n)
			}
		})
	}
}
