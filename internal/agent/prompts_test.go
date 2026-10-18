package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTemplateUsesTheUsersFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(dir, "crush.json"))
	if got := loadTemplate("coder.md.tpl", coderPromptTmpl); got != string(coderPromptTmpl) {
		t.Fatal("without a user file the built-in template must be used")
	}
	if err := os.MkdirAll(UserTemplateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(UserTemplateDir(), "coder.md.tpl")
	if err := os.WriteFile(path, []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadTemplate("coder.md.tpl", coderPromptTmpl); got != string(coderPromptTmpl) {
		t.Fatal("an empty user file must fall back to the built-in template")
	}
	if err := os.WriteFile(path, []byte("You are Jae's agent. {{.GitStatus}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadTemplate("coder.md.tpl", coderPromptTmpl)
	if !strings.HasPrefix(got, "You are Jae's agent.") {
		t.Fatalf("the user file was not used: %q", got[:40])
	}
	if _, _, user := EffectiveTemplate("coder.md.tpl"); !user {
		t.Fatal("EffectiveTemplate must report the user's file")
	}
	if _, ok := BuiltinTemplate("nope.md.tpl"); ok {
		t.Fatal("unknown template names must not resolve")
	}
}
