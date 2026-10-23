package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/stretchr/testify/require"
)

func doctorConfig() *config.Config {
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("qwen", config.ProviderConfig{ID: "qwen", BaseURL: "http://127.0.0.1:8080/v1", Models: []catwalk.Model{{ID: "qwen3.8-27b"}}})
	return &config.Config{
		Providers: providers,
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {Provider: "qwen", Model: "qwen3.8-27b"},
			config.SelectedModelTypeSmall: {Provider: "qwen", Model: "qwen3.8-27b"},
		},
		Hooks:   map[string][]config.HookConfig{config.EventStop: {{Command: "true"}}, config.EventUserPromptSubmit: {{Command: "a"}, {Command: "b"}}},
		LSP:     config.LSPs{"gopls": {Command: "gopls"}, "off": {Command: "nope", Disabled: true}},
		Options: &config.Options{DataDirectory: "/tmp/proj/.klash"},
	}
}

func found(name string) (string, error) { return "/usr/bin/" + name, nil }

// A setup that can run a turn reports each part as fine.
func TestDoctorReportOnAWorkingSetup(t *testing.T) {
	t.Parallel()

	lines, ok := doctorReport(config.NewTestStore(doctorConfig()), found, nil)
	report := strings.Join(lines, "\n")
	require.True(t, ok, report)
	require.Contains(t, report, "✓ provider qwen: 1 models (http://127.0.0.1:8080/v1)")
	require.Contains(t, report, "✓ large model: qwen/qwen3.8-27b")
	require.Contains(t, report, "✓ hooks: UserPromptSubmit 2, Stop 1")
	require.Contains(t, report, "✓ ripgrep: /usr/bin/rg")
	require.Contains(t, report, "✓ lsp gopls: /usr/bin/gopls")
	require.NotContains(t, report, "nope", "a disabled LSP is not checked")
	require.NotContains(t, report, "✗")
}

// A chosen model no provider offers means no turn can run; a missing
// ripgrep or LSP binary is worth knowing but does not.
func TestDoctorReportSaysWhatIsWrong(t *testing.T) {
	t.Parallel()

	cfg := doctorConfig()
	cfg.Models[config.SelectedModelTypeLarge] = config.SelectedModel{Provider: "qwen", Model: "gone"}
	missing := func(name string) (string, error) { return "", errors.New("not found") }

	lines, ok := doctorReport(config.NewTestStore(cfg), missing, nil)
	report := strings.Join(lines, "\n")
	require.False(t, ok)
	require.Contains(t, report, "✗ large model: qwen/gone is not offered by an available provider")
	require.Contains(t, report, "! ripgrep (rg) not found")
	require.Contains(t, report, "! lsp gopls: gopls not found")

	empty := doctorConfig()
	empty.Providers = csync.NewMap[string, config.ProviderConfig]()
	lines, ok = doctorReport(config.NewTestStore(empty), found, nil)
	require.False(t, ok)
	require.Contains(t, strings.Join(lines, "\n"), "✗ providers: none configured")
}

// A hook that runs a script by path is checked: one that is missing, is a
// directory or cannot be run fails every time it fires. Bare names may be
// the hook shell's own builtins and are left alone.
func TestHookScriptProblem(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain.sh"), []byte("#!/bin/sh\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ok.sh"), []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "hooks"), 0o755))

	require.Equal(t, "./missing.sh does not exist", hookScriptProblem("./missing.sh --flag", dir))
	require.Equal(t, "./plain.sh is not executable", hookScriptProblem("./plain.sh", dir))
	require.Equal(t, "./hooks is a directory", hookScriptProblem("./hooks", dir))
	require.Empty(t, hookScriptProblem("./ok.sh arg", dir))
	require.Empty(t, hookScriptProblem(filepath.Join(dir, "ok.sh"), "/elsewhere"))
	require.Empty(t, hookScriptProblem("bash ./missing.sh", dir), "the shell runs it; bash is a bare name")
	require.Empty(t, hookScriptProblem(`echo '{"decision":"allow"}'`, dir))
	require.Empty(t, hookScriptProblem("$HOME/bin/x", dir), "a variable is the shell's to expand")
}

// The report names the hook whose script is missing.
func TestDoctorReportNamesABrokenHook(t *testing.T) {
	t.Parallel()

	cfg := doctorConfig()
	missing := filepath.Join(t.TempDir(), "gate.sh")
	cfg.Hooks = map[string][]config.HookConfig{config.EventStop: {{Name: "gate", Command: missing}}}

	lines, ok := doctorReport(config.NewTestStore(cfg), found, nil)
	require.True(t, ok, "a broken hook is worth knowing, but a turn can still run")
	require.Contains(t, strings.Join(lines, "\n"), `! hook Stop "gate": `+missing+" does not exist, so it fails each time it fires")
}

// Names from before the rename to Klash are listed as not read, a warning
// rather than a failure.
func TestDoctorReportNamesRetiredNames(t *testing.T) {
	t.Parallel()

	lines, ok := doctorReport(config.NewTestStore(doctorConfig()), found, []string{"~/proj/crash.json (now klash.json)", "CRASH_DISABLE_METRICS (now KLASH_DISABLE_METRICS)"})
	report := strings.Join(lines, "\n")
	require.True(t, ok, report)
	require.Contains(t, report, "! not read since the rename to Klash: ~/proj/crash.json (now klash.json)")
	require.Contains(t, report, "! not read since the rename to Klash: CRASH_DISABLE_METRICS (now KLASH_DISABLE_METRICS)")
}
