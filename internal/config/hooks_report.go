package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HooksReport is the hooks that run, as Markdown for the TUI's /hooks,
// which lists them as Claude Code's /hooks does: by event, in the order
// the events fire, each with its matcher ("*" for every tool), its
// command and the timeout it runs under.
func HooksReport(hooks map[string][]HookConfig) string {
	var sb strings.Builder
	sb.WriteString("## Hooks\n\n")
	var rows []string
	for _, event := range KnownHookEvents() {
		for _, h := range hooks[event] {
			matcher := h.Matcher
			if matcher == "" {
				matcher = "*"
			}
			command := h.Command
			if h.Name != "" {
				command = h.Name + ": " + command
			}
			rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s |\n",
				event, markdownCell(matcher), markdownCell(command), formatHookTimeout(h.TimeoutDuration(event))))
		}
	}
	if len(rows) == 0 {
		sb.WriteString("No hooks are configured. They go under `hooks` in gnosh.json, in Claude Code's shape or gnosh's.\n")
		return sb.String()
	}
	sb.WriteString("| Event | Matcher | Command | Timeout |\n|-------|---------|---------|---------|\n")
	for _, row := range rows {
		sb.WriteString(row)
	}
	return sb.String()
}

// markdownCell is s as one Markdown table cell: on one line, with its
// pipes, which shell commands are full of, escaped.
func markdownCell(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	return strings.ReplaceAll(s, "|", `\|`)
}

// formatHookTimeout writes a hook's timeout in seconds: 600 s, 1.5 s.
func formatHookTimeout(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + " s"
}
