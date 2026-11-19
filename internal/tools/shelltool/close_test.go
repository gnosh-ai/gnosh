package shelltool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

func TestCloseStopsEverything(t *testing.T) {
	t.Parallel()
	news := newEnds()
	m := newManager(t, Options{OnEnd: news.take})
	env := newEnv(t)
	live := newCollector()
	env.Progress = live.take

	// Two foreground commands in flight, two background ones, and one that
	// has ended but left a process holding its output.
	left := bash(t, m, env, `sleep 300 & echo "left=$!"`)
	background := make(map[string]int)
	var ids []string
	for i := range 2 {
		id, _ := startBackground(t, m, env, fmt.Sprintf(`echo "bg%d=$$"; echo ready; sleep 300 & sleep 300 & wait`, i))
		ids = append(ids, id)
		for name, pid := range pidsIn(readUntil(t, m, env, id, "ready\n")) {
			background[name] = pid
		}
	}
	first := begin(t, t.Context(), m, BashName, env, args{argCommand: tree, argTimeout: 600_000})
	second := begin(t, t.Context(), m, BashName, env, args{argCommand: `echo "other=$$"; echo other-is-up; sleep 300`, argTimeout: 600_000})
	live.waitFor(t, "ready\n")
	live.waitFor(t, "other-is-up\n")

	start := time.Now()
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	took := time.Since(start)

	// The calls in flight come back and say what happened.
	all := pidsIn(printed(left))
	for _, done := range []<-chan core.ToolResult{first, second} {
		res := <-done
		headline, body, _ := parts(res)
		if !res.IsError || !strings.Contains(headline, "because the session is ending") || res.Meta[metaReason] != ReasonClosed {
			t.Errorf("a call in flight at Close came back with %q, Meta %v", headline, res.Meta)
		}
		for name, pid := range pidsIn(body) {
			all[name] = pid
		}
	}
	for name, pid := range background {
		all[name] = pid
	}
	if len(all) != 7 {
		t.Fatalf("pids = %v, want seven", all)
	}
	// Close waits: by the time it returns, nothing is left. (Only Linux
	// lets the test tell a process that is dead and not yet reaped from a
	// live one at once; elsewhere it gives the reaper a moment.)
	if runtime.GOOS != "linux" {
		allGone(t, all)
	}
	for name, pid := range all {
		if alive(pid) {
			t.Errorf("%s (pid %d) is still alive after Close returned", name, pid)
		}
	}
	if took > m.opt.TermGrace+5*time.Second {
		t.Errorf("Close took %v although everything dies on SIGTERM", took)
	}
	if _, err := os.Stat(m.dir); !os.IsNotExist(err) {
		t.Errorf("the output directory %s is still there after Close: %v", m.dir, err)
	}
	for _, task := range m.Tasks() {
		if task.Running || task.Reason != ReasonClosed {
			t.Errorf("after Close: %+v", task)
		}
	}
	if n := news.count(); n != 0 {
		t.Errorf("OnEnd was called %d times for commands that Close stopped", n)
	}

	// A closed manager runs nothing, and says so as the harness's error.
	_, err := toolNamed(t, m, BashName).Run(t.Context(), core.ToolCall{Input: json.RawMessage(`{"command": "echo late"}`)}, env)
	if !errors.Is(err, errClosed) {
		t.Errorf("Run after Close: %v, want the closed error", err)
	}
	// What is left of the tasks can still be asked about.
	res := run(t, t.Context(), m, StopName, env, args{argTaskID: ids[0]})
	if text := core.TextOf(res.Content); !strings.Contains(text, "had already ended: it was stopped after") {
		t.Errorf("TaskStop after Close: %q", text)
	}
	if err := m.Close(); err != nil {
		t.Errorf("a second Close: %v", err)
	}
}

// TestNothingIsLeftOpen runs some dozens of commands of every kind through
// one manager and checks that, once it is closed, the test binary holds no
// more open files than before. It is not parallel: other tests' files would
// be counted.
func TestNothingIsLeftOpen(t *testing.T) {
	// list names the files this process has open; the one that reading the
	// list itself opens is among them, each time.
	list := func() []string {
		entries, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Skipf("this system does not list open files: %v", err)
		}
		var names []string
		for _, entry := range entries {
			target, err := os.Readlink("/dev/fd/" + entry.Name())
			if err != nil {
				target = "(closed again)"
			}
			names = append(names, entry.Name()+" -> "+target)
		}
		return names
	}
	openFiles := func() int { return len(list()) }
	env := newEnv(t)
	// The Go runtime opens a few files of its own the first time a pipe or
	// a process is used. One command through a manager that is then closed
	// gets that out of the way before the count is taken.
	warm := newManager(t, Options{})
	bash(t, warm, env, "true")
	if err := warm.Close(); err != nil {
		t.Fatal(err)
	}
	before := openFiles()
	m := newManager(t, Options{MaxOutputChars: 200, PipeGrace: 20 * time.Millisecond, TermGrace: 200 * time.Millisecond})
	live := newCollector()
	env.Progress = live.take
	for i := range 15 {
		// One that simply runs, one whose output is cut and kept in a file,
		// one that fails to start a program, one that is stopped at its
		// timeout, one that leaves a process behind, and a background one.
		bash(t, m, env, "echo plain")
		bash(t, m, env, "seq 1 200")
		bash(t, m, env, "no-such-program-here")
		run(t, t.Context(), m, BashName, env, args{argCommand: "sleep 300", argTimeout: 20})
		if i%5 == 0 {
			bash(t, m, env, "sleep 300 & echo left")
			startBackground(t, m, env, "echo background; sleep 300")
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for openFiles() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := list(); len(after) != before {
		t.Errorf("%d files are open after the manager was closed, %d before it was made:\n%s", len(after), before, strings.Join(after, "\n"))
	}
}

func TestCloseWithNothingStarted(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	m := New(Options{SpoolDir: parent})
	if err := m.Close(); err != nil {
		t.Error(err)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Errorf("a manager that ran nothing left files: %v", entries)
	}
	// The parent directory is the caller's and stays.
	if _, err := os.Stat(parent); err != nil {
		t.Error(err)
	}
}

func TestCloseRacesWithStart(t *testing.T) {
	t.Parallel()
	// Commands being started while the manager closes must either be
	// refused or be stopped: none may slip through and run on. The check
	// at the end of the test run would find one that did.
	for round := range 5 {
		m := newManager(t, Options{})
		env := newEnv(t)
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				input := mustJSON(t, args{argCommand: "sleep 300", argBackground: i%2 == 0, argTimeout: 600_000})
				res, err := toolNamed(t, m, BashName).Run(t.Context(), core.ToolCall{ID: fmt.Sprint(round, i), Input: input}, env)
				if err != nil && !errors.Is(err, errClosed) {
					t.Errorf("Run: %v", err)
				}
				if err == nil && i%2 != 0 && res.Meta[metaReason] != ReasonClosed {
					t.Errorf("a foreground sleep ended with %v", res.Meta)
				}
			})
		}
		time.Sleep(time.Duration(round) * time.Millisecond)
		if err := m.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		wg.Wait()
	}
}

func TestManyCommandsAtOnce(t *testing.T) {
	t.Parallel()
	news := newEnds()
	m := newManager(t, Options{OnEnd: news.take})
	env := newEnv(t)
	const n = 48
	// Meanwhile the person's task list is being refreshed and a call names a
	// command that is not there: both look at every background command, the
	// ones that are only just starting included.
	watching, stop := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watching)
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, task := range m.Tasks() {
				if task.ID == "" || task.OutputFile == "" || task.PID <= 1 || task.Started.IsZero() {
					t.Errorf("a half-made task was listed: %+v", task)
				}
			}
			res := run(t, t.Context(), m, OutputName, env, args{argTaskID: "bg999"})
			if !res.IsError {
				t.Errorf("TaskOutput for a command that is not there: %s", core.TextOf(res.Content))
			}
		}
	}()
	defer func() {
		close(stop)
		<-watching
	}()
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			switch i % 4 {
			case 0, 1: // runs to its end
				res := bash(t, m, env, fmt.Sprintf("echo start %d; sleep 0.0%d; echo end %d >&2; exit %d", i, i%7, i, i%5))
				if got, want := printed(res), fmt.Sprintf("start %d\nend %d", i, i); got != want {
					t.Errorf("command %d: output %q, want %q", i, got, want)
				}
				if code := exitCode(t, res); code != i%5 {
					t.Errorf("command %d: exit code %d, want %d", i, code, i%5)
				}
			case 2: // is stopped at its timeout
				res := run(t, t.Context(), m, BashName, env, args{argCommand: fmt.Sprintf("echo waiting %d; sleep 300", i), argTimeout: 1000 + i})
				if headline, body, _ := parts(res); !strings.HasPrefix(headline, "timed out") || body != fmt.Sprintf("waiting %d", i) {
					t.Errorf("command %d: %q, %q", i, headline, body)
				}
			case 3: // runs in the background and is read
				id, _ := startBackground(t, m, env, fmt.Sprintf("echo background %d; exit %d", i, i%3))
				want := fmt.Sprintf("background %d\n", i)
				if got := readUntil(t, m, env, id, want); got != want {
					t.Errorf("command %d: output %q, want %q", i, got, want)
				}
			}
		})
	}
	wg.Wait()

	tasks := m.Tasks()
	if len(tasks) != n/4 {
		t.Fatalf("%d background commands, want %d", len(tasks), n/4)
	}
	seen := make(map[string]bool)
	for _, task := range tasks {
		if seen[task.ID] {
			t.Errorf("the id %s was given out twice", task.ID)
		}
		seen[task.ID] = true
	}
	eventually(t, "every background command to end", func() bool {
		for _, task := range m.Tasks() {
			if task.Running {
				return false
			}
		}
		return true
	})
	eventually(t, "the manager's books to be empty", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.running) == 0 && len(m.lingering) == 0 && m.bgRunning == 0 && len(m.calls) == 0
	})
}

func TestTimeoutMovesToBackground(t *testing.T) {
	t.Parallel()
	news := newEnds()
	m := newManager(t, Options{TimeoutToBackground: true, OnEnd: news.take})
	env := newEnv(t)
	live := newCollector()
	env.Progress = live.take
	command := `echo "parent=$$"; cd sub 2>/dev/null; while [ ! -e finish ]; do sleep 0.02; done; echo "the rest"; exit 5`
	res := run(t, t.Context(), m, BashName, env, args{argCommand: command, argTimeout: 400})

	headline, body, notes := parts(res)
	id, _ := res.Meta[metaTaskID].(string)
	if res.IsError || id != "bg1" || !strings.HasPrefix(headline, "still running after 0.4 s, so it was moved to the background as bg1") {
		t.Fatalf("headline = %q, id %q, IsError %v", headline, id, res.IsError)
	}
	pids := pidsIn(body)
	if len(pids) != 1 || !alive(pids["parent"]) {
		t.Fatalf("output so far = %q; the command should still be running", body)
	}
	if res.Cwd != "" || !strings.Contains(strings.Join(notes, "\n"), "a change of directory it makes will not carry over") {
		t.Errorf("Cwd = %q, notes %q", res.Cwd, notes)
	}
	if res.Meta[metaTimedOutAfter] != int64(400) {
		t.Errorf("Meta = %v", res.Meta)
	}
	if tasks := m.Tasks(); len(tasks) != 1 || !tasks[0].Running || tasks[0].ID != id {
		t.Errorf("Tasks() = %+v", tasks)
	}

	// It is an ordinary background command now. A read gives only what the
	// result of the call did not show.
	if body := taskBody(readTask(t, m, env, id, "")); body != "(no new output)" {
		t.Errorf("first read after the move = %q", body)
	}
	seenLive := live.text()
	touch(t, env.Cwd, "finish")
	ended := news.next(t)
	if ended.ID != id || ended.ExitCode != 5 || ended.Reason != ReasonExited {
		t.Errorf("news = %+v", ended.Task)
	}
	if body := taskBody(readTask(t, m, env, id, "")); body != "the rest" {
		t.Errorf("read after it ended = %q", body)
	}
	// The call was over, so nothing more was sent to its listener.
	if got := live.text(); got != seenLive {
		t.Errorf("live output went on after the call returned: %q", got[len(seenLive):])
	}
	allGone(t, pids)
}

func TestSleepIsNotMovedToBackground(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{TimeoutToBackground: true})
	env := newEnv(t)
	// A command that only waits has nothing to go on with.
	res := run(t, t.Context(), m, BashName, env, args{argCommand: "sleep 300", argTimeout: 300})
	if headline, _, _ := parts(res); !res.IsError || !strings.HasPrefix(headline, "timed out after 0.3 s and was stopped") {
		t.Errorf("headline = %q", headline)
	}
	if len(m.Tasks()) != 0 {
		t.Errorf("Tasks() = %+v", m.Tasks())
	}
	for command, want := range map[string]bool{
		"sleep 5": true, "  sleep 5; echo x": true, "sleep": true, "sleep\t1": true,
		"sleeper 5": false, "echo sleep": false, "": false, "nosleep": false,
	} {
		if got := startsWithSleep(command); got != want {
			t.Errorf("startsWithSleep(%q) = %v", command, got)
		}
	}
}

// With background commands off (--bare), a call that asks for one is
// refused, a command whose time is up is stopped although
// TimeoutToBackground is set, the person's move does nothing, and the
// tool's description says so.
func TestNoBackground(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{TimeoutToBackground: true, NoBackground: true})
	env := newEnv(t)
	res := run(t, t.Context(), m, BashName, env, args{argCommand: "echo hi", argBackground: true})
	if !res.IsError || !strings.Contains(core.TextOf(res.Content), "Background commands are off in this session") {
		t.Errorf("a background call: IsError %v, %q", res.IsError, core.TextOf(res.Content))
	}
	res = run(t, t.Context(), m, BashName, env, args{argCommand: "while :; do sleep 0.02; done", argTimeout: 300})
	if headline, _, _ := parts(res); !res.IsError || !strings.HasPrefix(headline, "timed out after 0.3 s and was stopped") {
		t.Errorf("a command whose time was up: %q", headline)
	}
	if len(m.Tasks()) != 0 {
		t.Errorf("Tasks() = %+v", m.Tasks())
	}
	if m.Background("no-such-call") {
		t.Errorf("Background reported a command to move")
	}
	desc := m.bashSpec().Description
	if !strings.Contains(desc, "Background commands are off in this session") || strings.Contains(desc, "goes on in the background") {
		t.Errorf("the description does not say background commands are off:\n%s", desc)
	}
}

// Asked to go to the background before its command has started (the
// person saw the call first), a command goes there as it starts.
func TestMoveToBackgroundBeforeItStarts(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	forget := m.expect("early-call")
	if !m.Background("early-call") {
		t.Fatal("Background did not take the call about to start")
	}
	input := mustJSON(t, args{argCommand: `echo started; sleep 300`, argTimeout: 600_000})
	res, err := toolNamed(t, m, BashName).Run(t.Context(), core.ToolCall{ID: "early-call", Name: BashName, Input: input}, env)
	forget()
	if err != nil {
		t.Fatal(err)
	}
	headline, _, _ := parts(res)
	id, _ := res.Meta[metaTaskID].(string)
	if res.IsError || id == "" || !strings.HasPrefix(headline, "moved to the background as "+id+" at the person's request") {
		t.Fatalf("headline = %q, Meta %v", headline, res.Meta)
	}
	if _, err := m.Stop(t.Context(), id); err != nil {
		t.Error(err)
	}
	if m.Background("early-call") {
		t.Error("Background found the call after it returned")
	}
}

func TestMoveToBackgroundOnRequest(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	live := newCollector()
	env.Progress = live.take
	input := mustJSON(t, args{argCommand: `echo "parent=$$"; echo ready; while [ ! -e finish ]; do sleep 0.02; done; echo "the rest"`, argTimeout: 600_000})
	done := make(chan core.ToolResult, 1)
	go func() {
		res, err := toolNamed(t, m, BashName).Run(t.Context(), core.ToolCall{ID: "the-call", Name: BashName, Input: input}, env)
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	live.waitFor(t, "ready\n")

	if m.Background("some-other-call") {
		t.Error("Background found a command for a call that has none")
	}
	if !m.Background("the-call") {
		t.Fatal("Background did not find the command of the call in flight")
	}
	res := <-done
	headline, body, _ := parts(res)
	id, _ := res.Meta[metaTaskID].(string)
	if res.IsError || id == "" || !strings.HasPrefix(headline, "moved to the background as "+id+" at the person's request") {
		t.Fatalf("headline = %q, Meta %v", headline, res.Meta)
	}
	pids := pidsIn(body)
	if !strings.HasSuffix(body, "\nready") || !alive(pids["parent"]) {
		t.Errorf("output so far = %q", body)
	}
	if m.Background("the-call") {
		t.Error("Background found the call again after it returned")
	}
	touch(t, env.Cwd, "finish")
	if got := readUntil(t, m, env, id, "the rest\n"); got != "the rest\n" {
		t.Errorf("read after the move = %q", got)
	}
	allGone(t, pids)
}

func TestMoveToBackgroundWhenThereIsNoRoom(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{TimeoutToBackground: true, MaxBackground: 1})
	env := newEnv(t)
	startBackground(t, m, env, "sleep 300")
	// With the background full, a command whose time is up is stopped, as
	// it would be without the option.
	res := run(t, t.Context(), m, BashName, env, args{argCommand: `echo "parent=$$"; while :; do sleep 300; done`, argTimeout: 300})
	headline, body, _ := parts(res)
	if !res.IsError || !strings.HasPrefix(headline, "timed out after 0.3 s and was stopped") {
		t.Errorf("headline = %q", headline)
	}
	allGone(t, pidsIn(body))
}

func TestCancelWhileStopToolWaits(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{TermGrace: 600 * time.Millisecond})
	env := newEnv(t)
	id, _ := startBackground(t, m, env, `trap '' TERM; echo "parent=$$"; echo ready; while :; do sleep 300; done`)
	pids := pidsIn(readUntil(t, m, env, id, "ready\n"))
	// The call to TaskStop is itself cancelled while the command takes its
	// time to die. The stop goes on without it.
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	res := run(t, ctx, m, StopName, env, args{argTaskID: id})
	if text := core.TextOf(res.Content); !res.IsError || !strings.Contains(text, "interrupted while "+id+" was being stopped") {
		t.Errorf("result = %q", text)
	}
	allGone(t, pids)
}

// Asked for while its permission is still checked or its hooks run, before
// the manager has heard of the call, a Bash call's command goes to the
// background as it starts (BackgroundSoon). Background alone does not take
// a call it has not heard of.
func TestMoveToBackgroundBeforeTheCallIsKnown(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	env := newEnv(t)
	if m.Background("hooked-call") {
		t.Fatal("Background took a call it has not heard of")
	}
	if !m.BackgroundSoon("hooked-call") {
		t.Fatal("BackgroundSoon did not take the call")
	}
	input := mustJSON(t, args{argCommand: `echo started; sleep 300`, argTimeout: 600_000})
	res, err := toolNamed(t, m, BashName).Run(t.Context(), core.ToolCall{ID: "hooked-call", Name: BashName, Input: input}, env)
	if err != nil {
		t.Fatal(err)
	}
	headline, _, _ := parts(res)
	id, _ := res.Meta[metaTaskID].(string)
	if res.IsError || id == "" || !strings.HasPrefix(headline, "moved to the background as "+id+" at the person's request") {
		t.Fatalf("headline = %q, Meta %v", headline, res.Meta)
	}
	if _, err := m.Stop(t.Context(), id); err != nil {
		t.Error(err)
	}
}

// What a command leaves running is stopped when the manager closes even
// when none of the processes seen as the command ended is left: a server
// a launcher started a moment later, the launcher gone since, carries the
// session's mark (on Linux: macOS shows no process another's
// environment); and a process that held the output as the command ended
// and let go of it later is still on the books.
func TestLeftoversThatOutliveTheLauncherAreStopped(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("leftovers are known by their session here")
	}
	marks := runtime.GOOS == "linux"
	m := newManager(t, Options{PipeGrace: 200 * time.Millisecond})
	env := newEnv(t)
	launcher := `echo 1 > server.pid`
	if marks {
		launcher = `(sleep 0.3; sleep 300 & echo $! > server.pid) >/dev/null 2>&1 &`
	}
	bash(t, m, env, launcher)
	bash(t, m, env, `sh -c 'echo $$ > late.pid; sleep 0.3; exec >/dev/null 2>&1; exec sleep 300' &`)
	pidIn := func(name string) int {
		deadline := time.Now().Add(10 * time.Second)
		for {
			b, _ := os.ReadFile(filepath.Join(env.Cwd, name))
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
			if time.Now().After(deadline) {
				t.Fatalf("no pid in %s", name)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	server, late := pidIn("server.pid"), pidIn("late.pid")
	t.Cleanup(func() {
		if marks {
			syscall.Kill(server, syscall.SIGKILL)
		}
		syscall.Kill(late, syscall.SIGKILL)
	})
	// The launcher ends, and what held the output lets go of it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		m.mu.Lock()
		lingering := len(m.lingering)
		m.mu.Unlock()
		if lingering == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("what held the output never let go of it")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if marks && !alive(server) || !alive(late) {
		t.Fatalf("before Close: server alive %v, late alive %v", alive(server), alive(late))
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if marks && alive(server) {
		t.Errorf("the server the launcher started is still there after Close")
	}
	if alive(late) {
		t.Errorf("the process that let go of the output late is still there after Close")
	}
}

// The books of what let go of a command's output forget a session whose
// processes have all ended, as the next one comes.
func TestLeftoversThatEndedAreForgotten(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("leftovers are known by their session here")
	}
	m := newManager(t, Options{})
	env := newEnv(t)
	for range 5 {
		bash(t, m, env, `sleep 0.05 >/dev/null 2>&1 &`)
	}
	// (Until their processes have ended, however busy the machine.)
	deadline := time.Now().Add(20 * time.Second)
	for {
		m.mu.Lock()
		sids := make([]int, 0, len(m.detached))
		for sid := range m.detached {
			sids = append(sids, sid)
		}
		m.mu.Unlock()
		alive := 0
		for _, sid := range sids {
			if ps, err := m.procs(sid); err == nil && len(ps) > 0 {
				alive++
			}
		}
		if alive == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions still have a process after 20 s", alive)
		}
		time.Sleep(50 * time.Millisecond)
	}
	bash(t, m, env, `sleep 0.05 >/dev/null 2>&1 &`)
	m.mu.Lock()
	n := len(m.detached)
	m.mu.Unlock()
	if n > 1 {
		t.Errorf("%d sessions on the books, want at most the last one's", n)
	}
}

// A session on the books that looks empty once, as a look at it caught a
// process that started a last one and ended, is not forgotten: it must
// look empty twice, so that Close still stops what it left running.
func TestBooksKeepASessionThatLookedEmptyOnce(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	server := []procInfo{{pid: 5002, pgid: 5002, start: 7, image: "server"}}
	var mu sync.Mutex
	looks := 0
	m.procs = func(sid int) ([]procInfo, error) {
		mu.Lock()
		defer mu.Unlock()
		if sid != 5000 {
			return nil, nil
		}
		looks++
		if looks == 1 {
			return nil, nil // the launcher ended, its server not seen yet
		}
		return server, nil
	}
	m.mu.Lock()
	m.detached[5000] = []procInfo{{pid: 5001, pgid: 5000, start: 3, image: "launcher"}}
	m.mu.Unlock()
	m.pruneDetached()
	m.mu.Lock()
	_, kept := m.detached[5000]
	m.mu.Unlock()
	if !kept {
		t.Fatal("a session that looked empty once was forgotten, and Close would leave its server running")
	}
	// One that stays empty is forgotten.
	m.procs = func(int) ([]procInfo, error) { return nil, nil }
	m.pruneDetached()
	m.mu.Lock()
	_, kept = m.detached[5000]
	m.mu.Unlock()
	if kept {
		t.Error("a session that stayed empty was kept")
	}
}

// When nothing seen as a command ended is left in its session, the
// session's id may belong to something else by then: only the processes
// that carry the manager's mark are signalled.
func TestSweepStopsOnlyMarkedProcesses(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{TermGrace: 30 * time.Millisecond, KillWait: 30 * time.Millisecond})
	var mu sync.Mutex
	var sent []int
	m.signal = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, pid)
		return nil
	}
	m.procs = func(sid int) ([]procInfo, error) {
		return []procInfo{{pid: 4001, pgid: 4001, start: 9, image: "a"}, {pid: 4002, pgid: 4002, start: 9, image: "b"}}, nil
	}
	m.envHas = func(pid int, entry string) (bool, error) { return pid == 4002 && entry == m.markEntry(), nil }
	m.sweep(4000, []procInfo{{pid: 3999, pgid: 4000, start: 1}})
	mu.Lock()
	defer mu.Unlock()
	if slices.Contains(sent, -4001) || slices.Contains(sent, 4001) {
		t.Errorf("a process without the mark was signalled: %v", sent)
	}
	if !slices.Contains(sent, -4002) {
		t.Errorf("the marked process's group was not signalled: %v", sent)
	}
	// A process seen as the command ended still being there proves the
	// session is the command's: then all of it is stopped.
	sent = nil
	mu.Unlock()
	m.sweep(4000, []procInfo{{pid: 4001, pgid: 4001, start: 9}})
	mu.Lock()
	if !slices.Contains(sent, -4001) || !slices.Contains(sent, -4002) {
		t.Errorf("with the session shown to be the command's, signalled %v", sent)
	}
}

// Every command's environment carries the manager's mark, last, where the
// settings' own env cannot take it out.
func TestCommandsCarryTheMark(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{Env: []string{MarkVar + "=forged"}})
	env := newEnv(t)
	res := bash(t, m, env, `echo "mark=$`+MarkVar+`"`)
	if got := printed(res); !strings.Contains(got, "mark="+m.mark) || len(m.mark) != 32 {
		t.Errorf("printed %q, want the mark %q", got, m.mark)
	}
	if runtime.GOOS != "linux" {
		if _, err := procEnvHas(os.Getpid(), "PATH="+os.Getenv("PATH")); !errors.Is(err, errNoEnviron) {
			t.Errorf("procEnvHas here: %v, want errNoEnviron", err)
		}
		return
	}
	res = bash(t, m, env, `sleep 300 >/dev/null 2>&1 & echo "server=$!"`)
	server := pidsIn(printed(res))["server"]
	t.Cleanup(func() { syscall.Kill(server, syscall.SIGKILL) })
	if ok, err := procEnvHas(server, m.markEntry()); err != nil || !ok {
		t.Errorf("procEnvHas of the command's process: %v, %v", ok, err)
	}
	if ok, _ := procEnvHas(server, MarkVar+"=forged"); ok {
		t.Errorf("procEnvHas found an entry the process does not have")
	}
}

// A process a command left running that let go of its output (a server
// started with its output sent to a file) is said in the result, is what
// LeftRunning names, and is stopped when the manager closes; one in a
// session of its own (setsid) is left alone, and not named.
func TestLetGoIsStoppedAtClose(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("the test starts its own session with setsid(1)")
	}
	m := newManager(t, Options{})
	env := newEnv(t)
	res := bash(t, m, env, `sleep 300 >/dev/null 2>&1 & echo "server=$!"`)
	server := pidsIn(printed(res))["server"]
	if server == 0 || !hasNote(res, "left a process running that let go of its output") || !hasNote(res, "stopped when the session ends") {
		t.Fatalf("the result does not say what was left running:\n%s", core.TextOf(res.Content))
	}
	res = bash(t, m, env, `setsid sleep 300 >/dev/null 2>&1 & echo "kept=$!"`)
	kept := pidsIn(printed(res))["kept"]
	t.Cleanup(func() { syscall.Kill(kept, syscall.SIGKILL) })
	time.Sleep(200 * time.Millisecond) // (setsid execs sleep in its own session)
	if !alive(server) || !alive(kept) {
		t.Fatalf("before Close: server alive %v, kept alive %v", alive(server), alive(kept))
	}
	// (The command's shell leads the process group the sleep is in.)
	if left := m.LeftRunning(); len(left) != 1 || !strings.HasPrefix(left[0], "process group ") || !strings.HasSuffix(left[0], " (sleep)") {
		t.Errorf("LeftRunning before Close: %q", left)
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if left := m.LeftRunning(); len(left) != 0 {
		t.Errorf("LeftRunning after Close: %q", left)
	}
	if alive(server) {
		t.Errorf("the process left running is still there after Close")
	}
	if !alive(kept) {
		t.Errorf("the process in a session of its own was stopped")
	}
}

// A command whose output closes once the manager is closing stays on the
// list of those whose leftovers held it open, so that close stops what it
// left: close looks at that list only after it is marked closed.
func TestSettleWhileClosingLeavesItToClose(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{TermGrace: 30 * time.Millisecond, KillWait: 30 * time.Millisecond})
	var mu sync.Mutex
	var sent []int
	m.signal = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, pid)
		return nil
	}
	left := []procInfo{{pid: 5001, pgid: 5001, start: 9, image: "server"}}
	m.procs = func(int) ([]procInfo, error) { return left, nil }
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	j := &job{pid: 5000, pipe: r, pumped: make(chan struct{})}
	close(j.pumped)
	m.mu.Lock()
	m.lingering[j] = left
	m.closed = true // (as close begins)
	m.mu.Unlock()
	m.settle(j)
	m.mu.Lock()
	_, held := m.lingering[j]
	m.mu.Unlock()
	if !held {
		t.Fatal("settle took the command off the list while the manager was closing")
	}
	m.Close()
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(sent, -5001) {
		t.Errorf("close did not stop what the command left: signalled %v", sent)
	}
}

// A command whose output closed between the check that it was held and
// its going on the list (settle came and found nothing) has what is still
// in its session put on the books of what let go of the output, so that
// Close stops it all the same.
func TestLingerAfterTheOutputClosed(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{TermGrace: 30 * time.Millisecond, KillWait: 30 * time.Millisecond})
	var mu sync.Mutex
	var sent []int
	m.signal = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, pid)
		return nil
	}
	left := []procInfo{{pid: 6001, pgid: 6001, start: 9, image: "server"}}
	m.procs = func(int) ([]procInfo, error) { return left, nil }
	j := &job{pid: 6000, pumped: make(chan struct{})}
	close(j.pumped)
	if !m.linger(j, left) {
		t.Fatal("linger refused a command while the manager was open")
	}
	m.mu.Lock()
	_, held := m.lingering[j]
	_, booked := m.detached[6000]
	m.mu.Unlock()
	if held || !booked || !j.letGo.Load() {
		t.Fatalf("after the output closed: on the lingering list %v, on the books %v, let go %v", held, booked, j.letGo.Load())
	}
	m.Close()
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(sent, -6001) {
		t.Errorf("close did not stop what the command left: signalled %v", sent)
	}
}

// What a command seems to leave running is looked at again before it is
// said to be: a process that closed the output as it ended may still be
// seen at the first look. One whose pid now has another start is another
// process.
func TestLeftRunningIsLookedAtTwice(t *testing.T) {
	t.Parallel()
	m := newManager(t, Options{})
	first := []procInfo{{pid: 6001, pgid: 6000, start: 9, image: "bash"}, {pid: 6002, pgid: 6000, start: 9, image: "server"}}
	m.procs = func(int) ([]procInfo, error) {
		return []procInfo{{pid: 6002, pgid: 6000, start: 9, image: "server"}, {pid: 6003, pgid: 6000, start: 10, image: "new"}}, nil
	}
	got := m.stillThere(6000, first)
	if len(got) != 1 || got[0].pid != 6002 {
		t.Errorf("still there: %+v, want the server alone", got)
	}
	m.procs = func(int) ([]procInfo, error) {
		return []procInfo{{pid: 6002, pgid: 6000, start: 11, image: "server"}}, nil
	}
	if got := m.stillThere(6000, first); len(got) != 0 {
		t.Errorf("a pid that changed hands counted as still there: %+v", got)
	}
}

// Of the background commands that ended, the last maxEndedTasks are kept:
// an older one is let go of, and its output file with it. (The shell tool
// review of 2026-11-19: a long session kept every ended one, in memory and
// on disk, until it ended.)
func TestEndedTasksAreLetGoOf(t *testing.T) {
	defer func(was int) { maxEndedTasks = was }(maxEndedTasks)
	maxEndedTasks = 2
	m := newManager(t, Options{})
	env := newEnv(t)
	for i := range 3 {
		run(t, t.Context(), m, BashName, env, args{argCommand: fmt.Sprintf("echo task %d", i), argBackground: true})
		for deadline := time.Now().Add(20 * time.Second); ; {
			tasks := m.Tasks()
			if len(tasks) > 0 && !tasks[len(tasks)-1].Running {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("task %d did not end", i)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	tasks := m.Tasks()
	if len(tasks) != 2 {
		t.Fatalf("%d ended tasks kept, want 2: %+v", len(tasks), tasks)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(tasks[0].OutputFile), "*"))
	for _, f := range files {
		if data, _ := os.ReadFile(f); strings.Contains(string(data), "task 0") {
			t.Errorf("the output file of the task let go of is still there: %s", f)
		}
	}
}
