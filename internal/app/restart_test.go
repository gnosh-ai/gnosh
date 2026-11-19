package app

import (
	"github.com/gnosh-ai/gnosh/internal/buildinfo"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// /restart ends the session and starts gnosh again in it, once nothing
// runs that it would stop: the same flags, and --resume=<the session> for
// the prompt and the choice of conversation. A newer gnosh installed over
// the program file is said once, as a turn ends.
func TestRestart(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Text: "Hi."},
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 60","run_in_background":true}`}}},
		apptest.Reply{Text: "It sleeps."},
		apptest.Reply{Text: "Noted that it was stopped."},
	)
	exe := filepath.Join(t.TempDir(), "gnosh")
	if err := os.WriteFile(exe, []byte("the build that runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	var started [][]string
	s := sitWith(t, w, func(o *Options) {
		o.Executable = exe
		o.Relaunch = func(args, env []string) error {
			started = append(started, args)
			if !slices.Contains(env, RestartedEnv+"="+buildinfo.BuildID) {
				t.Errorf("the gnosh /restart starts is not told it was restarted, and from which build: %q", env)
			}
			if len(w.Sessions()) != 1 {
				t.Errorf("gnosh starts again with %d session files", len(w.Sessions()))
			}
			return os.ErrPermission
		}
	}, "-y", "--allowedTools", "Read", "say hi")
	s.see("Hi.")
	s.see("-- done in ")

	// A newer gnosh is installed, by rename as an install does it.
	if err := os.WriteFile(exe+".new", []byte("a newer build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exe+".new", exe); err != nil {
		t.Fatal(err)
	}
	s.typeLine("start a long job")
	s.see("It sleeps.")
	// (It is said as a turn ends: on a busy machine the first turn's end
	// was still under way when the newer build landed, and it was said
	// there.)
	s.seeAnywhere("A newer gnosh has been installed since this one started: /restart runs it, in this session.")

	// What runs in the background would be stopped, so it waits.
	s.typeLine("/restart")
	s.see("this is still running in the background, and would be stopped: bg1 (sleep 60). /restart once it has ended, or /tasks stop <id> stops it")
	s.typeLine("/tasks stop bg1")
	s.see("bg1 is stopped")
	// (Its end is a turn of its own, and /restart waits for turns.)
	s.see("Noted that it was stopped.")
	s.see("-- done in ")
	s.typeLine("/update")
	s.see("gnosh starts again, the newer one, in this session.")
	if code := s.exit(); code != 1 {
		t.Errorf("exit code %d, want 1 for a start that failed", code)
	}
	id := strings.TrimSuffix(filepath.Base(w.Sessions()[0]), ".jsonl")
	want := []string{"--allowedTools=Read", "--dangerously-skip-permissions", "--resume=" + id}
	if len(started) != 1 || !slices.Equal(started[0], want) {
		t.Errorf("gnosh started again with %q, want %q", started, want)
	}
	has(t, "a start that failed", s.errs.String(), "gnosh could not start again (permission denied). The session is kept: gnosh --allowedTools=Read --dangerously-skip-permissions --resume="+id+" opens it again.")
	if n := strings.Count(s.out.String(), "A newer gnosh has been installed"); n != 1 {
		t.Errorf("the newer gnosh was said %d times", n)
	}

	// A session opened from its file is opened from its file again.
	path := w.Sessions()[0]
	started = nil
	s = sitWith(t, w, func(o *Options) {
		o.Executable = exe
		o.Relaunch = func(args, env []string) error {
			started = append(started, args)
			return os.ErrPermission
		}
	}, "-r", path)
	s.see("Type a prompt and Enter.")
	s.typeLine("/restart")
	s.see("gnosh starts again, in this session.")
	s.exit()
	if len(started) != 1 || !slices.Equal(started[0], []string{"--permission-mode=default", "--resume=" + path}) {
		t.Errorf("gnosh started again with %q", started)
	}

	// A gnosh that cannot start itself again says how to go on, and says
	// nothing of builds.
	w = apptest.NewWorld(t, apptest.Reply{Text: "Hi."})
	s = sit(t, w)
	s.see("Type a prompt and Enter.")
	s.typeLine("/restart")
	s.see("this gnosh cannot start itself again. /exit ends the session, and gnosh -r ")
	s.typeLine("/exit")
	if code := s.exit(); code != 0 {
		t.Errorf("exit code %d", code)
	}
}

// A session that has written nothing yet has no file to resume: /restart
// (and /background) start gnosh again under its id.
func TestRestartBeforeAnything(t *testing.T) {
	w := apptest.NewWorld(t)
	var started [][]string
	s := sitWith(t, w, func(o *Options) {
		o.Relaunch = func(args, env []string) error {
			started = append(started, args)
			return os.ErrPermission
		}
	})
	s.see("Type a prompt and Enter.")
	s.typeLine("/restart")
	s.see("gnosh starts again, in this session.")
	s.exit()
	m := regexp.MustCompile(`session (\S+) in `).FindStringSubmatch(s.out.String())
	if m == nil {
		t.Fatalf("no session id on the screen:\n%s", s.out.String())
	}
	if len(started) != 1 || !slices.Equal(started[0], []string{"--permission-mode=default", "--session-id=" + m[1]}) {
		t.Errorf("gnosh started again with %q", started)
	}
}
