package app

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/apptest"
	"github.com/gnosh-ai/gnosh/internal/cli"
)

// result is how one run of the program ended.
type result struct {
	code     int
	out, err string
}

// safeBuffer is a buffer several goroutines may write to and read.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// testOptions are the Options a test runs the program with: the world's
// environment and folder, nothing typed, and waits short enough for a
// test.
func testOptions(w *apptest.World, args ...string) Options {
	return Options{
		Args:      args,
		Stdin:     strings.NewReader(""),
		Environ:   w.Env(),
		Cwd:       w.Dir,
		Build:     cli.Build{Commit: "0123456789ab", Time: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)},
		BuildID:   "test-build",
		StdinWait: 50 * time.Millisecond,
		// Long enough that a tool which finishes its work after a stop is
		// never given up on because the machine is busy.
		StopGrace: 20 * time.Second,
		ModelWait: 5 * time.Second,
		Retry: agent.RetryPolicy{
			Pauses:          []time.Duration{10 * time.Millisecond},
			GoneAfter:       400 * time.Millisecond,
			NotRunningPause: 10 * time.Millisecond,
			NotRunningAfter: 400 * time.Millisecond,
			BusyMax:         20 * time.Millisecond,
		},
	}
}

// run runs the program in print mode with args, in the world's folder.
func run(t *testing.T, w *apptest.World, args ...string) result {
	t.Helper()
	return runWith(t, context.Background(), testOptions(w, args...))
}

// runWith runs the program with o and waits for it, failing the test when
// it does not end.
func runWith(t *testing.T, ctx context.Context, o Options) result {
	t.Helper()
	var out, errb safeBuffer
	if o.Stdout == nil {
		o.Stdout = &out
	}
	if o.Stderr == nil {
		o.Stderr = &errb
	}
	done := make(chan int, 1)
	go func() { done <- Main(ctx, o) }()
	select {
	case code := <-done:
		return result{code: code, out: out.String(), err: errb.String()}
	case <-time.After(3 * time.Minute):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("the program did not end within three minutes.\nstdout:\n%s\nstderr:\n%s\ngoroutines:\n%s", out.String(), errb.String(), buf[:n])
	}
	return result{}
}

// wantCode fails the test unless the run ended with the code.
func (r result) wantCode(t *testing.T, code int) result {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit code %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.out, r.err)
	}
	return r
}

// wantNoErr fails the test unless nothing was written to stderr.
func (r result) wantNoErr(t *testing.T) result {
	t.Helper()
	if r.err != "" {
		t.Fatalf("stderr is not empty:\n%s", r.err)
	}
	return r
}

// has fails the test unless text holds every one of the pieces.
func has(t *testing.T, what, text string, pieces ...string) {
	t.Helper()
	for _, p := range pieces {
		if !strings.Contains(text, p) {
			t.Errorf("%s does not hold %q:\n%s", what, p, clip(text))
		}
	}
}

// clip shortens a long text for a failure message: its start and its end.
func clip(s string) string {
	if len(s) <= 1600 {
		return s
	}
	return s[:600] + "\n   [... " + strconv.Itoa(len(s)-1600) + " bytes left out ...]\n" + s[len(s)-1000:]
}

// hasNot fails the test if text holds any of the pieces.
func hasNot(t *testing.T, what, text string, pieces ...string) {
	t.Helper()
	for _, p := range pieces {
		if i := strings.Index(text, p); i >= 0 {
			t.Errorf("%s holds %q and must not: ...%s...", what, p, text[max(0, i-200):min(len(text), i+len(p)+200)])
		}
	}
}

// The fields of the output that differ from run to run, masked before a
// comparison with what is expected.
var (
	maskUUID     = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	maskMsg      = regexp.MustCompile(`msg_[0-9a-f]+_`)
	maskDuration = regexp.MustCompile(`"(duration_ms|duration_api_ms)":\d+`)
	maskPort     = regexp.MustCompile(`127\.0\.0\.1:\d+`)
	maskCall     = regexp.MustCompile(`call_[0-9a-f]{24}`)
)

// masked replaces what differs from run to run: ids, durations, the
// world's own directory and the model server's port.
func masked(w *apptest.World, s string) string {
	s = strings.ReplaceAll(s, w.Root, "ROOT")
	s = maskUUID.ReplaceAllString(s, "UUID")
	s = maskMsg.ReplaceAllString(s, "msg_ID_")
	s = maskDuration.ReplaceAllString(s, `"$1":0`)
	s = maskPort.ReplaceAllString(s, "127.0.0.1:PORT")
	s = maskCall.ReplaceAllString(s, "call_ID")
	return s
}

// callIDs returns the ids the tool calls of a request's conversation go
// by, in the order the model made them. They are gnosh's own, not the
// scripted model's: every call is given an id no other call ever has.
func callIDs(req apptest.Request) []string {
	var ids []string
	for _, m := range req.Messages() {
		calls, _ := m["tool_calls"].([]any)
		for _, c := range calls {
			if cm, ok := c.(map[string]any); ok {
				if id, ok := cm["id"].(string); ok {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// callID returns the id that the n-th tool call of a conversation goes by
// (counted from one), as a request holds it. The scripts in these tests
// name their calls c1, c2 and so on in the order they are made, so the
// call a script names cN is the n-th.
func callID(t *testing.T, req apptest.Request, scripted string) string {
	t.Helper()
	n, err := strconv.Atoi(strings.TrimPrefix(scripted, "c"))
	ids := callIDs(req)
	if err != nil || n < 1 || n > len(ids) {
		t.Fatalf("the request holds %d tool calls, and the call %s was asked for", len(ids), scripted)
	}
	return ids[n-1]
}

// toolResults returns the tool results of a request, in order, each as
// the scripted name of its call (c1, c2, ...) and what the model read.
func toolResults(req apptest.Request) (names, texts []string) {
	ids := callIDs(req)
	for _, m := range req.Messages() {
		if m["role"] != "tool" {
			continue
		}
		name := "?"
		for i, id := range ids {
			if id == m["tool_call_id"] {
				name = "c" + strconv.Itoa(i+1)
			}
		}
		names = append(names, name)
		texts = append(texts, apptest.Text(m))
	}
	return names, texts
}

// jsonLines decodes output that holds one JSON object to a line.
func jsonLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("a line of the output is not a JSON object (%v): %s\nall of it:\n%s", err, line, out)
		}
		lines = append(lines, v)
	}
	return lines
}

// lastRequestText returns the body of the model's request number n, as
// text.
func requestText(t *testing.T, w *apptest.World, n int) string {
	t.Helper()
	reqs := w.Model.Requests()
	if n >= len(reqs) {
		t.Fatalf("the model was sent %d requests, and request %d was asked for", len(reqs), n)
	}
	return string(reqs[n].Body)
}

// globAll returns the paths that match a pattern.
func globAll(pattern string) []string {
	found, _ := filepath.Glob(pattern)
	return found
}
