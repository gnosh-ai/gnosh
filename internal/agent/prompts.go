package agent

import (
	"bytes"
	"context"
	_ "embed"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed templates/coder.md.tpl
var coderPromptTmpl []byte

//go:embed templates/task.md.tpl
var taskPromptTmpl []byte

//go:embed templates/plan.md.tpl
var planPromptTmpl []byte

//go:embed templates/initialize.md.tpl
var initializePromptTmpl []byte

func coderPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("coder", loadTemplate("coder.md.tpl", coderPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

// coderPromptReplacing builds the coder agent's system prompt with
// instructions, the text of --system-prompt, in place of the template's
// own: the part before its <env> line. The environment, skills and
// context files from that line on are kept, as Claude Code keeps
// CLAUDE.md under its --system-prompt. They come from the built-in
// template, since a user's template need not have an <env> line.
func coderPromptReplacing(instructions string, opts ...prompt.Option) (*prompt.Prompt, error) {
	return prompt.NewPrompt("coder", builtinCoderContext(), append(opts, prompt.WithTextBefore(instructions+"\n\n"))...)
}

// builtinCoderContext is the built-in coder template from its <env> line
// to the end.
func builtinCoderContext() string {
	t := string(coderPromptTmpl)
	if i := strings.Index(t, "\n<env>\n"); i >= 0 {
		return t[i+1:]
	}
	return ""
}

func taskPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("task", loadTemplate("task.md.tpl", taskPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func planPrompt(opts ...prompt.Option) (*prompt.Prompt, error) {
	systemPrompt, err := prompt.NewPrompt("plan", loadTemplate("plan.md.tpl", planPromptTmpl), opts...)
	if err != nil {
		return nil, err
	}
	return systemPrompt, nil
}

func InitializePrompt(cfg *config.ConfigStore) (string, error) {
	systemPrompt, err := prompt.NewPrompt("initialize", loadTemplate("initialize.md.tpl", initializePromptTmpl))
	if err != nil {
		return "", err
	}
	return systemPrompt.Build(context.Background(), "", "", cfg)
}

// UserTemplateDir is where a user's own prompt templates live, beside crushrc:
// ~/.config/crush/templates. A file named like a built-in template
// (coder.md.tpl, plan.md.tpl, task.md.tpl, initialize.md.tpl) replaces it.
// `crush prompt export` writes the built-in one there to start from.
func UserTemplateDir() string {
	return filepath.Join(filepath.Dir(config.GlobalConfig()), "templates")
}

// BuiltinTemplate returns the embedded template of that name.
func BuiltinTemplate(name string) ([]byte, bool) {
	switch name {
	case "coder.md.tpl":
		return coderPromptTmpl, true
	case "task.md.tpl":
		return taskPromptTmpl, true
	case "plan.md.tpl":
		return planPromptTmpl, true
	case "initialize.md.tpl":
		return initializePromptTmpl, true
	}
	return nil, false
}

// EffectiveTemplate returns the template Crush would use for that name: the
// user's file when it exists and is not empty, else the built-in one.
func EffectiveTemplate(name string) (text string, path string, user bool) {
	builtin, _ := BuiltinTemplate(name)
	path = filepath.Join(UserTemplateDir(), name)
	b, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return string(builtin), path, false
	}
	return string(b), path, true
}

func loadTemplate(name string, builtin []byte) string {
	text, path, user := EffectiveTemplate(name)
	if user {
		slog.Info("Using the user's prompt template", "path", path)
	}
	if text == "" {
		return string(builtin)
	}
	return text
}
