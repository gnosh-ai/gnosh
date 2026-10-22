// Package tooldefs names Crush's built-in tools.
//
// The list lives here, in a package that imports nothing, because two
// places need to agree on it and neither can own it: internal/config
// validates the tool names a config file uses, and internal/shellconfig
// validates the ones a crushrc writes — and config imports shellconfig,
// so shellconfig cannot import config back.
//
// Duplicating 29 strings behind a drift-guard test was the alternative.
// A shared list makes the drift impossible instead of merely detectable.
package tooldefs

import "slices"

// MCPPrefix is the prefix every MCP-provided tool name carries
// ("mcp_<server>_<tool>"). Those names are discovered when a server
// connects, so config-time checks can only recognise the shape.
const MCPPrefix = "mcp_"

// names is the built-in tool set the agent can be given. Returned by
// value from Names so a caller cannot reach in and change it.
var names = []string{
	"agent",
	"bash",
	"gnokey",
	"crash_info",
	"crash_logs",
	"job_output",
	"job_kill",
	"download",
	"edit",
	"multiedit",
	"lsp_diagnostics",
	"lsp_references",
	"lsp_restart",
	"lsp_symbols",
	"lsp_definition",
	"lsp_call_hierarchy",
	"lsp_rename",
	"lsp_replace_symbol",
	"fetch",
	"agentic_fetch",
	"x402_fetch",
	"glob",
	"grep",
	"ls",
	"question",
	"sourcegraph",
	"todos",
	"view",
	"write",
	"list_mcp_resources",
	"read_mcp_resource",
}

// Names returns the built-in tool names.
func Names() []string {
	return slices.Clone(names)
}

// legacyNames maps the names tools had before Crush was renamed Crash
// to their names now, so that a config or hook naming the old one keeps
// working.
var legacyNames = map[string]string{
	"crush_info": "crash_info",
	"crush_logs": "crash_logs",
}

// Canonical returns the current name of a built-in tool given by any of
// its names. Any other name is returned as it is.
func Canonical(name string) string {
	if current, ok := legacyNames[name]; ok {
		return current
	}
	return name
}

// LegacyName returns the name a built-in tool had before the rename, or
// "" if it had no other.
func LegacyName(name string) string {
	for legacy, current := range legacyNames {
		if current == name {
			return legacy
		}
	}
	return ""
}

// IsKnown reports whether name is a built-in tool, under its current name
// or the one it had before the rename.
func IsKnown(name string) bool {
	return slices.Contains(names, Canonical(name))
}

// claudeCodeNames maps the names Claude Code gives its tools to the
// built-in tools that do the same job here, so a Claude Code user's
// --allowedTools Read or --disallowedTools Bash means what they meant.
var claudeCodeNames = map[string]string{
	"Agent":      "agent",
	"Bash":       "bash",
	"BashOutput": "job_output",
	"Edit":       "edit",
	"Glob":       "glob",
	"Grep":       "grep",
	"KillShell":  "job_kill",
	"LS":         "ls",
	"MultiEdit":  "multiedit",
	"Read":       "view",
	"Task":       "agent",
	"TodoWrite":  "todos",
	"WebFetch":   "fetch",
	"Write":      "write",
}

// FromClaudeCode returns the built-in tool Claude Code calls name, or ""
// if Claude Code has no tool by that name or Crash none like it.
func FromClaudeCode(name string) string {
	return claudeCodeNames[name]
}

// IsMCPToolName reports whether name has the shape of an MCP tool name.
func IsMCPToolName(name string) bool {
	return len(name) > len(MCPPrefix) && name[:len(MCPPrefix)] == MCPPrefix
}
