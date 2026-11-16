package app

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// /color keeps the session's colour in its file: a session opened again
// has it, a name that is no colour is refused, default takes it away, and
// with nothing named one of the colours is chosen.
func TestColorCommand(t *testing.T) {
	w := apptest.NewWorld(t)
	has(t, "/color purple", run(t, w, "-p", "/color purple").wantCode(t, 0).out, "This session's prompt mark is purple from now on")
	data, _ := os.ReadFile(w.Sessions()[0])
	has(t, "the session's file", string(data), `"key":"color"`, `"value":"purple"`)
	has(t, "the session opened again", run(t, w, "-c", "-p", "/status").wantCode(t, 0).out, "color        purple (/color)")
	r := run(t, w, "-c", "-p", "/color mauve")
	has(t, "a colour /color does not give", r.out+r.err, `"mauve" is no colour /color gives: red, blue`)
	has(t, "/color default", run(t, w, "-c", "-p", "/color default").wantCode(t, 0).out, "the interface's own colour again")
	hasNot(t, "the session after default", run(t, w, "-c", "-p", "/status").wantCode(t, 0).out, "color        ")
	out := run(t, w, "-c", "-p", "/color").wantCode(t, 0).out
	if !slices.ContainsFunc(PromptColors, func(c string) bool { return strings.Contains(out, "prompt mark is "+c+" from now on") }) {
		t.Errorf("/color alone chose no colour: %s", out)
	}
}
