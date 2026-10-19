package agent

import (
	"bufio"
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/config"
)

// UserToolsDir is where a user's own tool descriptions live, beside crushrc:
// ~/.config/crush/tools. A file <name>.md replaces what the model is told
// about that tool, and a name listed in disabled.txt (one per line, # for
// comments) is not offered to the model at all. `crush prompt tools --export`
// writes the current descriptions there to start from.
func UserToolsDir() string {
	return filepath.Join(filepath.Dir(config.GlobalConfig()), "tools")
}

// DisabledToolsFile is the list of tools the model must not be offered.
func DisabledToolsFile() string {
	return filepath.Join(UserToolsDir(), "disabled.txt")
}

// describedTool is a tool whose description comes from the user's file.
type describedTool struct {
	fantasy.AgentTool
	description string
}

func (t describedTool) Info() fantasy.ToolInfo {
	info := t.AgentTool.Info()
	info.Description = t.description
	return info
}

// applyUserTools drops the tools listed in disabled.txt and swaps in the
// descriptions the user wrote. Everything else passes through untouched.
func applyUserTools(tools []fantasy.AgentTool) []fantasy.AgentTool {
	dir := UserToolsDir()
	disabled := readDisabledTools(DisabledToolsFile())
	out := make([]fantasy.AgentTool, 0, len(tools))
	for _, t := range tools {
		name := t.Info().Name
		if disabled[name] {
			slog.Info("Tool left out by the user's disabled.txt", "tool", name)
			continue
		}
		if b, err := os.ReadFile(filepath.Join(dir, name+".md")); err == nil && len(bytes.TrimSpace(b)) > 0 {
			t = describedTool{AgentTool: t, description: strings.TrimSpace(string(b))}
		}
		out = append(out, t)
	}
	return out
}

func readDisabledTools(path string) map[string]bool {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	names := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		names[line] = true
	}
	return names
}

// UserToolDescription reports whether the user's file for that tool is in use.
func UserToolDescription(name string) (path string, used bool) {
	path = filepath.Join(UserToolsDir(), name+".md")
	b, err := os.ReadFile(path)
	return path, err == nil && len(bytes.TrimSpace(b)) > 0
}
