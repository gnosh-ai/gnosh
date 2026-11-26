package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// The model's Restart, as Jae asked for it ("allow gnosh to restart
// itself, like when i say 'restart yourself', or when it thinks it needs
// to"): the turn ends, and gnosh starts again in the session with
// --resume, told that it was the model's call and why. That gnosh tells
// the model and goes on by itself; and it refuses a second Restart until
// the person has said something, so that a model cannot restart for ever.
func TestRestartTool(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Restart", Args: `{"reason":"you asked me to"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Restart", Args: `{}`}}},
		apptest.Reply{Text: "Back, and staying."},
		apptest.Reply{Calls: []apptest.Call{{ID: "c3", Name: "Restart", Args: `{}`}}},
	)
	var started, envs [][]string
	relaunch := func(o *Options) {
		o.Relaunch = func(args, env []string) error {
			started, envs = append(started, args), append(envs, env)
			return os.ErrPermission
		}
	}
	s := sitWith(t, w, relaunch, "-y")
	s.see("Type a prompt and Enter.")
	s.typeLine("restart yourself")
	s.see("gnosh starts again in this session, at the model's call (you asked me to).")
	if code := s.exit(); code != 1 {
		t.Errorf("exit code %d, want 1 for a start that failed", code)
	}
	if len(started) != 1 {
		t.Fatalf("gnosh started again %d times, want once", len(started))
	}
	id := strings.TrimSuffix(filepath.Base(w.Sessions()[0]), ".jsonl")
	if !slices.Contains(started[0], "--resume="+id) || !slices.Contains(started[0], "--dangerously-skip-permissions") {
		t.Errorf("gnosh started again with %q, want --resume=%s and the permission mode", started[0], id)
	}
	if !slices.Contains(envs[0], RestartAskedEnv+"=you asked me to") {
		t.Errorf("the gnosh Restart starts is not told it was the model's call: %q", envs[0])
	}

	// That gnosh tells the model, and goes on by itself; a second Restart
	// before the person has said anything is refused.
	s = sitWith(t, w, func(o *Options) {
		relaunch(o)
		o.Environ = append(slices.Clone(o.Environ), envs[0][len(envs[0])-2:]...)
	}, started[0]...)
	s.see("Back, and staying.")
	s.see("-- done in ")
	reqs := w.Model.Requests()
	has(t, "the request after the restart", string(reqs[1].Body), "at your Restart call (you asked me to)", "Go on with the task where you left off.")
	has(t, "the request after a second Restart", string(reqs[2].Body), "gnosh started again at your Restart call already, and the user has said nothing since")
	if len(started) != 1 {
		t.Errorf("a second Restart before the person said anything started gnosh again")
	}
	// Once the person has said something, the model may restart again.
	s.typeLine("go on")
	s.see("gnosh starts again in this session, at the model's call.")
	s.exit()
	if len(started) != 2 || !slices.Contains(envs[1], RestartAskedEnv+"=1") {
		t.Errorf("after the person spoke: started %d times, the second told %q", len(started), envs[1:])
	}
}

// Restart is refused while work runs in the background, which the restart
// would stop; and it is not offered in a run, nor where gnosh cannot start
// itself again.
func TestRestartToolRefusedAndNotOffered(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Bash", Args: `{"command":"sleep 60","run_in_background":true}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Restart", Args: `{}`}}},
		apptest.Reply{Text: "It waits."},
		apptest.Reply{Text: "ok"},
		apptest.Reply{Text: "ok"},
	)
	var started [][]string
	s := sitWith(t, w, func(o *Options) {
		o.Relaunch = func(args, env []string) error {
			started = append(started, args)
			return os.ErrPermission
		}
	}, "-y")
	s.see("Type a prompt and Enter.")
	s.typeLine("sleep, then restart")
	s.see("It waits.")
	s.see("-- done in ")
	s.typeLine("/exit")
	s.exit()
	if len(started) != 0 {
		t.Errorf("gnosh started again with work in the background")
	}
	has(t, "the refused Restart", string(w.Model.Requests()[2].Body), "would be stopped by the restart: bg1 (sleep 60)")

	run(t, w, "-y", "-p", "hi").wantCode(t, 0)
	if strings.Contains(string(w.Model.Requests()[3].Body), `"name":"Restart"`) {
		t.Errorf("a run was offered Restart")
	}
	s = sit(t, w, "-y")
	s.see("Type a prompt and Enter.")
	s.typeLine("hi")
	s.see("ok")
	s.typeLine("/exit")
	s.exit()
	if strings.Contains(string(w.Model.Requests()[4].Body), `"name":"Restart"`) {
		t.Errorf("a gnosh that cannot start itself again was offered Restart")
	}
}
