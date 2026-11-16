package app

import (
	"os"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// /keybindings makes the person's keyboard shortcuts file beside their
// gnosh.json when it is not there, with nothing bound, and says where it
// is; the interface is handed its path.
func TestKeybindingsCommand(t *testing.T) {
	w := apptest.NewWorld(t)
	path := w.ConfigDir() + "/keybindings.json"
	has(t, "/keybindings", run(t, w, "-p", "/keybindings").wantCode(t, 0).out, "Your keyboard shortcuts file is", "keybindings.json, made now with nothing bound.", `"ctrl+e": "chat:externalEditor"`)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != KeybindingsTemplate {
		t.Fatalf("the file made: %q, %v", data, err)
	}
	if err := os.WriteFile(path, []byte(`{"bindings": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := run(t, w, "-p", "/keybindings").wantCode(t, 0).out
	hasNot(t, "/keybindings again", out, "made now")
	if data, _ := os.ReadFile(path); string(data) != `{"bindings": []}` {
		t.Errorf("/keybindings wrote over the file: %q", data)
	}
}
