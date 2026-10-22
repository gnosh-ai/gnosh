package agent

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
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
// writes the current descriptions there to start from; see exportMark for
// why an export takes effect only once it is edited.
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
		if b, err := os.ReadFile(filepath.Join(dir, name+".md")); err == nil {
			if desc, ok := userDescription(b); ok {
				t = describedTool{AgentTool: t, description: desc}
			}
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
	if err != nil {
		return path, false
	}
	_, used = userDescription(b)
	return path, used
}

// exportMark opens the first line of a file written by `crush prompt tools
// --export`, and the line ends with the hash of the description written
// below it. While the text still has that hash, nobody has edited it, and
// the current built-in description is used instead: an export is a starting
// point for editing, and must not freeze a description at the version it
// was taken from. The AI box's exports, taken while gpt-oss-120b was the
// model, put that name in the attribution of every commit made after the
// switch to Qwen, and hid every later fix to the descriptions.
const exportMark = "<!-- Exported from the built-in description."

// ExportedDescriptionFile is the content --export writes for a tool whose
// built-in description is desc.
func ExportedDescriptionFile(desc string) string {
	desc = strings.TrimSpace(desc)
	return exportMark + " While the text below is unchanged, the current built-in " +
		"description is used instead; once you edit it, your text is. sha256:" +
		descriptionHash(desc) + " -->\n" + desc + "\n"
}

func descriptionHash(desc string) string {
	sum := sha256.Sum256([]byte(desc))
	return hex.EncodeToString(sum[:])
}

// userDescription returns the description a user's file gives a tool, or
// false when it gives none: the file is empty, or it is an export whose
// text has not been edited.
func userDescription(b []byte) (string, bool) {
	text := strings.TrimSpace(string(b))
	if first, rest, _ := strings.Cut(text, "\n"); strings.HasPrefix(first, exportMark) {
		text = strings.TrimSpace(rest)
		_, sum, _ := strings.Cut(first, "sha256:")
		if strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sum), "-->")) == descriptionHash(text) {
			return "", false
		}
	}
	return text, text != ""
}
