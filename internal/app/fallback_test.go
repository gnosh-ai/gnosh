package app

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// goneURL is the address of a model server that is not there.
func goneURL() string {
	s := httptest.NewServer(nil)
	s.Close()
	return s.URL
}

// A run whose model's server is not there goes on with the fallback model
// the configuration names, and says so.
func TestFallbackModelTakesOverARun(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "from the fallback"})
	w.Settings(`{"providers": {"gone": {"type": "llamacpp", "baseUrl": "` + goneURL() + `"}}, "model": "gone/big", "fallbackModel": ["local/test-model"]}`)
	r := run(t, w, "-p", "hello").wantCode(t, 0)
	if r.out != "from the fallback\n" {
		t.Errorf("stdout = %q", r.out)
	}
	has(t, "stderr", r.err, "gone (big)", "could not be reached", "This turn goes on with the fallback model local (test-model)",
		"the next turn asks gone (big)")
}

// --fallback-model names them for one run, a bare name as -m takes it.
func TestFallbackModelFlag(t *testing.T) {
	w := apptest.NewWorld(t, apptest.Reply{Text: "flagged"})
	w.Settings(`{"providers": {"gone": {"type": "llamacpp", "baseUrl": "` + goneURL() + `"}}, "model": "gone/big"}`)
	r := run(t, w, "-p", "--fallback-model", "local/test-model", "hello").wantCode(t, 0)
	if r.out != "flagged\n" {
		t.Errorf("stdout = %q", r.out)
	}
	has(t, "stderr", r.err, "This turn goes on with the fallback model local (test-model)")

	// Without one, the run waits for the model and then fails, as before.
	w = apptest.NewWorld(t)
	w.Settings(`{"providers": {"gone": {"type": "llamacpp", "baseUrl": "` + goneURL() + `"}}, "model": "gone/big"}`)
	r = run(t, w, "-p", "hello")
	if r.code == 0 {
		t.Fatalf("a run with its model gone and no fallback ended well: %q", r.out)
	}
	has(t, "stderr", r.err, "is not running")
}

// A fallback with the same model id on a provider of another type does not
// change how the session's own model is asked afterwards: what each
// provider's type adds to a request is kept by provider and model, not by
// the model's id alone.
func TestFallbackKeepsTheSessionModelsRequests(t *testing.T) {
	primary := apptest.NewModel(t,
		apptest.Reply{Status: 503, Body: `{"error":{"code":503,"message":"Loading model","type":"unavailable_error"}}`},
		apptest.Reply{Text: "The session's own model again."})
	w := apptest.NewWorld(t, apptest.Reply{Text: "From the fallback."})
	w.Settings(`{"providers": {"vllm": {"type": "openai-compatible", "baseUrl": "` + primary.URL + `"}}, "models": {"vllm/test-model": {"thinkingLevels": ["off", "on"]}}, "model": {"id": "vllm/test-model", "thinking": false}, "fallbackModel": "local/test-model"}`)
	s := sit(t, w)
	s.see("Type a prompt and Enter.")
	s.typeLine("first")
	s.see("This turn goes on with the fallback model local (test-model)")
	s.see("From the fallback.")
	s.see("-- done in ")
	s.typeLine("second")
	s.see("The session's own model again.")
	s.typeLine("/exit")
	s.exit()
	reqs := primary.Requests()
	if len(reqs) != 2 {
		t.Fatalf("the session's own model was asked %d times", len(reqs))
	}
	for i, r := range reqs {
		has(t, fmt.Sprintf("request %d to the session's own model", i+1), string(r.Body), `"chat_template_kwargs":{"enable_thinking":false}`)
	}
}

// A Read deny rule keeps a file out of what Grep finds in a folder it
// searches, as it keeps Read away from it.
func TestGrepLeavesOutWhatReadIsDenied(t *testing.T) {
	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Grep", Args: `{"pattern":"TOKEN","output_mode":"content"}`}}},
		apptest.Reply{Text: "Searched."},
	)
	w.Deny("Read(./secret/**)")
	w.Write("secret/key.txt", "TOKEN-in-the-secret\n")
	w.Write("open.txt", "TOKEN-in-the-open\n")
	run(t, w, "-p", "find the token").wantCode(t, 0)
	got := toolResult(t, w, 1, "c1")
	if !strings.Contains(got, "TOKEN-in-the-open") || strings.Contains(got, "TOKEN-in-the-secret") {
		t.Errorf("Grep returned:\n%s", got)
	}
}
