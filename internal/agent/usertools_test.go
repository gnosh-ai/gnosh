package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
)

type userToolFake struct{ name, desc string }

func (f userToolFake) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: f.name, Description: f.desc}
}
func (f userToolFake) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.ToolResponse{}, nil
}
func (f userToolFake) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (f userToolFake) SetProviderOptions(fantasy.ProviderOptions) {}

func TestApplyUserTools(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(dir, "crush.json"))
	tools := []fantasy.AgentTool{userToolFake{"bash", "built-in bash"}, userToolFake{"question", "built-in question"}, userToolFake{"view", "built-in view"}}

	got := applyUserTools(tools)
	if len(got) != 3 || got[0].Info().Description != "built-in bash" {
		t.Fatalf("without user files the tools must pass through: %v", got)
	}

	if err := os.MkdirAll(UserToolsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(UserToolsDir(), "bash.md"), []byte("Run a shell command.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(UserToolsDir(), "view.md"), []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(DisabledToolsFile(), []byte("# tools the model is not offered\nquestion\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = applyUserTools(tools)
	if len(got) != 2 {
		t.Fatalf("question should be dropped, got %d tools", len(got))
	}
	if got[0].Info().Name != "bash" || got[0].Info().Description != "Run a shell command." {
		t.Fatalf("bash description not replaced: %+v", got[0].Info())
	}
	if got[1].Info().Name != "view" || got[1].Info().Description != "built-in view" {
		t.Fatalf("an empty file must keep the built-in description: %+v", got[1].Info())
	}
	if _, used := UserToolDescription("bash"); !used {
		t.Fatal("UserToolDescription must report bash as overridden")
	}
}
