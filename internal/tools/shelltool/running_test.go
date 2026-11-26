package shelltool

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// runningSaid returns what the person watching was told the command runs,
// in the order it was said.
func (c *collector) runningSaid() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, s := range c.status {
		if said, ok := strings.CutPrefix(s, core.StreamRunning+": "); ok {
			out = append(out, said)
		}
	}
	return out
}

// While a command runs in the foreground, the person watching it is told
// the programs its shell runs, by their command lines: not the shell, nor
// the copy of it that runs a subshell on a pipeline's left, nor the file
// the command's first line wrote (Jae, 26 November 2026, of `cat >
// main.go <<'EOF'` shown for minutes while `go run main.go | tail -15`
// ran: "why is it stuck seemingly").
func TestRunningNamesThePrograms(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{RunningAfter: 20 * time.Millisecond, RunningEvery: 20 * time.Millisecond})
	env := newEnv(t)
	live := newCollector()
	env.Progress = live.take
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	command := "cat > note.txt <<'EOF'\nwritten at once\nEOF\n(sleep 30; true) | sleep 31"
	done := begin(t, ctx, m, BashName, env, args{argCommand: command, argTimeout: 60_000})
	want := []string{"sleep 30", "sleep 31"}
	deadline := time.After(10 * time.Second)
	for {
		said := live.runningSaid()
		if len(said) > 0 {
			got := strings.Split(said[len(said)-1], ", ")
			slices.Sort(got)
			if slices.Equal(got, want) {
				break
			}
		}
		select {
		case <-live.seen:
		case <-deadline:
			t.Fatalf("gave up waiting to be told the command runs %q; told %q", want, live.runningSaid())
		}
	}
	for _, said := range live.runningSaid() {
		if strings.Contains(said, "bash") || strings.Contains(said, "__gnosh") || strings.Contains(said, "cat") {
			t.Errorf("told the command runs %q: the shell or what ended long ago", said)
		}
	}
	cancel()
	<-done
}

// A command over before it has run long enough to be waited on is not
// named; nor is any command while nobody waits for it in the foreground.
func TestRunningNotSaidOfAQuickCommand(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{RunningAfter: 200 * time.Millisecond, RunningEvery: 20 * time.Millisecond})
	env := newEnv(t)
	live := newCollector()
	env.Progress = live.take
	res := <-begin(t, t.Context(), m, BashName, env, args{argCommand: "sleep 0.05 | cat", argTimeout: 60_000})
	if res.IsError {
		t.Fatalf("the command failed: %v", res.Content)
	}
	time.Sleep(300 * time.Millisecond)
	if said := live.runningSaid(); len(said) != 0 {
		t.Errorf("told a quick command runs %q", said)
	}
}

// A program is named for a person to read: its own name without the
// folder it was found in, an argument with a space in double quotes, a
// script handed over as one argument cut at its first line, and the whole
// cut short when long.
func TestProgramLine(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"/usr/local/go/bin/go", "run", "main.go"}, "go run main.go"},
		{[]string{"tail", "-15"}, "tail -15"},
		{[]string{"/usr/bin/python3", "-c", "print('hi there')"}, `python3 -c "print('hi there')"`},
		{[]string{"ssh", "workbench", "python3 - <<'PYEOF'\nsrc = open('x').read()\nPYEOF"}, `ssh workbench "python3 - <<'PYEOF' …"`},
	} {
		if got := programLine(c.args); got != c.want {
			t.Errorf("programLine(%q) = %q, want %q", c.args, got, c.want)
		}
	}
	long := programLine([]string{"echo", strings.Repeat("x", 500)})
	if n := len([]rune(long)); n != maxProgramChars+1 || !strings.HasSuffix(long, "…") {
		t.Errorf("a long command line came out %d runes long: %q", n, long)
	}
}
