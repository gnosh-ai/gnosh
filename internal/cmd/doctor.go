package cmd

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check that Crash can run here: config, providers, models, hooks and tools",
	Long: `Check the setup Crash runs with in this directory, as Claude Code's
doctor does: the config files it loads, the providers it can use and why it
cannot use the others, the models it picks, the hooks it runs, and whether
the programs it calls are installed. It exits non-zero when Crash could not
run a turn here.`,
	Example: `
# Check the setup for the current directory
crash doctor
  `,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := ResolveCwd(cmd)
		if err != nil {
			return err
		}
		dataDir, _ := cmd.Flags().GetString("data-dir")
		store, err := config.Init(cwd, dataDir, false)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "✗ config: %v\n", err)
			return errors.New("the config does not load; see above")
		}
		lines, ok := doctorReport(store, exec.LookPath)
		fmt.Fprintln(cmd.OutOrStdout(), strings.Join(lines, "\n"))
		if !ok {
			return errors.New("crash could not run a turn here; see above")
		}
		return nil
	},
}

// doctorReport checks the setup store holds and returns a line per check
// ("✓" fine, "!" works but worth knowing, "✗" a turn could not run), and
// whether a turn could run. lookPath finds a program, as exec.LookPath.
func doctorReport(store *config.ConfigStore, lookPath func(string) (string, error)) (lines []string, ok bool) {
	ok = true
	add := func(mark, format string, args ...any) {
		lines = append(lines, mark+" "+fmt.Sprintf(format, args...))
	}
	cfg := store.Config()

	add("✓", "crash %s", version.Version)

	if paths := store.LoadedPaths(); len(paths) > 0 {
		add("✓", "config: %s", strings.Join(paths, ", "))
	} else {
		add("!", "config: no config files, defaults only")
	}

	enabled := cfg.EnabledProviders()
	slices.SortFunc(enabled, func(a, b config.ProviderConfig) int { return strings.Compare(a.ID, b.ID) })
	for _, p := range enabled {
		add("✓", "provider %s: %d models (%s)", p.ID, len(p.Models), p.BaseURL)
	}
	skipped := cfg.SkippedProviders()
	for _, id := range slices.Sorted(maps.Keys(skipped)) {
		add("✗", "provider %s: %s", id, skipped[id])
	}
	if len(enabled) == 0 {
		ok = false
		if len(skipped) == 0 {
			add("✗", "providers: none configured; run '%s' to set one up", version.Command)
		}
	}

	for _, t := range []config.SelectedModelType{config.SelectedModelTypeLarge, config.SelectedModelTypeSmall} {
		selected, chosen := cfg.Models[t]
		switch {
		case !chosen:
			add("✗", "%s model: none chosen", t)
			ok = false
		case cfg.GetModelByType(t) == nil:
			add("✗", "%s model: %s/%s is not offered by an available provider", t, selected.Provider, selected.Model)
			ok = false
		default:
			add("✓", "%s model: %s/%s", t, selected.Provider, selected.Model)
		}
	}

	var hookCounts []string
	for _, event := range config.KnownHookEvents() {
		if n := len(cfg.Hooks[event]); n > 0 {
			hookCounts = append(hookCounts, fmt.Sprintf("%s %d", event, n))
		}
	}
	if len(hookCounts) > 0 {
		add("✓", "hooks: %s", strings.Join(hookCounts, ", "))
	} else {
		add("✓", "hooks: none")
	}
	for _, event := range config.KnownHookEvents() {
		for _, h := range cfg.Hooks[event] {
			if problem := hookScriptProblem(h.Command, store.WorkingDir()); problem != "" {
				add("!", "hook %s %q: %s, so it fails each time it fires", event, h.DisplayName(), problem)
			}
		}
	}

	if path, err := lookPath("rg"); err == nil {
		add("✓", "ripgrep: %s", path)
	} else {
		add("!", "ripgrep (rg) not found: grep and glob fall back to slower searches")
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.LSP)) {
		lsp := cfg.LSP[name]
		if lsp.Disabled || lsp.Command == "" {
			continue
		}
		if path, err := lookPath(lsp.Command); err == nil {
			add("✓", "lsp %s: %s", name, path)
		} else {
			add("!", "lsp %s: %s not found, so it does not start", name, lsp.Command)
		}
	}

	if cfg.Options != nil && cfg.Options.DataDirectory != "" {
		add("✓", "data: %s", cfg.Options.DataDirectory)
	}
	return lines, ok
}

// hookScriptProblem says what is wrong with the script a hook command
// runs, when the command starts with a path to one (./hooks/check.sh,
// ~/bin/gate): it does not exist, is a directory, or is not executable.
// Hooks run in dir. A command that starts with a bare name is left alone,
// since the hook shell has builtins of its own.
func hookScriptProblem(command, dir string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	script := fields[0]
	if !strings.Contains(script, "/") || strings.ContainsAny(script, "$`\"'") {
		return ""
	}
	path := home.Long(script)
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return script + " does not exist"
	case info.IsDir():
		return script + " is a directory"
	case info.Mode()&0o111 == 0:
		return script + " is not executable"
	}
	return ""
}
