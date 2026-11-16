package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// /debug turns debug logging on and hands the model the end of what this
// session wrote to gnosh's log, with what the person says is wrong. It
// works in a run that continues a session, which is how a session that
// went wrong is looked into afterwards.
func TestDebugCommand(t *testing.T) {
	w := apptest.NewWorld(t)
	w.Model.Respond(func(n int, r apptest.Request) *apptest.Reply { return &apptest.Reply{Text: "Nothing is wrong."} })
	run(t, w, "-p", "say hi").wantCode(t, 0)
	logFile := filepath.Join(filepath.Dir(w.Sessions()[0]), "gnosh.log")
	before, _ := os.ReadFile(logFile)
	if strings.Contains(string(before), " model     request (") {
		t.Fatalf("the log has debug lines before /debug:\n%s", before)
	}
	run(t, w, "-c", "-p", "/debug the replies are slow").wantCode(t, 0)
	// Debug logging is on from the command on: the request it sends is
	// logged in full.
	after, _ := os.ReadFile(logFile)
	has(t, "the log after /debug", string(after), " model     request (")
	reqs := w.Model.Requests()
	body := string(reqs[len(reqs)-1].Body)
	has(t, "what /debug asks", body, "The user typed /debug", "gnosh.log", "The last lines this session wrote to it:", "The user describes the problem so: the replies are slow", "Change no file.")
	id := shortID(strings.TrimSuffix(filepath.Base(w.Sessions()[0]), ".jsonl"))
	if n := strings.Count(body, " "+id+" "); n < 2 {
		t.Errorf("the session's own log lines are not in what /debug asks (%s %d times):\n%s", id, n, body)
	}
}

// sessionLogTail keeps the session's own lines, the last ones, oldest
// first, as many as the count and the size let it.
func TestSessionLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gnosh.log")
	var b strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "2026-11-16T10:00:%02d.000Z aaaaaaaa turn      line %d of a\n", i, i)
		fmt.Fprintf(&b, "2026-11-16T10:00:%02d.000Z bbbbbbbb turn      line %d of b\n", i, i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	line := func(i int) string {
		return fmt.Sprintf("2026-11-16T10:00:%02d.000Z aaaaaaaa turn      line %d of a", i, i)
	}
	if got, want := sessionLogTail(path, "aaaaaaaa", 3, 1<<10), line(47)+"\n"+line(48)+"\n"+line(49); got != want {
		t.Errorf("the last three:\n%s\nwant:\n%s", got, want)
	}
	if got, want := sessionLogTail(path, "aaaaaaaa", 100, 2*len(line(49))+2), line(48)+"\n"+line(49); got != want {
		t.Errorf("as many as fit:\n%s\nwant:\n%s", got, want)
	}
	if got := sessionLogTail(path, "cccccccc", 10, 1<<10); got != "" {
		t.Errorf("another session's lines: %q", got)
	}
	if got := sessionLogTail(filepath.Join(t.TempDir(), "none.log"), "aaaaaaaa", 10, 1<<10); got != "" {
		t.Errorf("no log: %q", got)
	}
}
