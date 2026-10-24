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
	"artifact",
	"bash",
	"gnokey",
	"klash_info",
	"klash_logs",
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

// legacyNames maps the names tools had before Crush was renamed Klash
// to their names now, so that a config or hook naming the old one keeps
// working.
var legacyNames = map[string]string{
	"crush_info": "klash_info",
	"crush_logs": "klash_logs",
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
// The names older versions of Claude Code used (BashOutput, KillShell,
// LS, MultiEdit, Task, TaskOutput) stay, since configs written for them
// still use them.
var claudeCodeNames = map[string]string{
	"Agent":                "agent",
	"AskUserQuestion":      "question",
	"Bash":                 "bash",
	"BashOutput":           "job_output",
	"Edit":                 "edit",
	"Glob":                 "glob",
	"Grep":                 "grep",
	"KillShell":            "job_kill",
	"LS":                   "ls",
	"ListMcpResourcesTool": "list_mcp_resources",
	"MultiEdit":            "multiedit",
	"Read":                 "view",
	"ReadMcpResourceTool":  "read_mcp_resource",
	"Task":                 "agent",
	"TaskOutput":           "job_output",
	"TaskStop":             "job_kill",
	"TodoWrite":            "todos",
	"WebFetch":             "fetch",
	"Write":                "write",
}

// FromClaudeCode returns the built-in tool Claude Code calls name, or ""
// if Claude Code has no tool by that name or Klash none like it.
func FromClaudeCode(name string) string {
	return claudeCodeNames[name]
}

// subAgentNames are the tools only agentic_fetch's sub-agent has. No
// config gives them to the main agent, but a hook can match them.
var subAgentNames = []string{"web_fetch", "web_search"}

// claudeCodeMatchedAs are Claude Code's names a hook matcher can give a
// tool besides the one claudeCodeNames maps to it, which --allowedTools
// also reads: WebFetch fetches a page, as fetch, agentic_fetch and its
// sub-agent's web_fetch do, and WebSearch searches, as agentic_fetch and
// web_search do. So a Claude Code hook guarding WebFetch guards them all.
var claudeCodeMatchedAs = map[string][]string{
	"agentic_fetch": {"WebFetch", "WebSearch"},
	"web_fetch":     {"WebFetch"},
	"web_search":    {"WebSearch"},
}

// ClaudeCodeNamesOf returns the names Claude Code gives the tool name, as
// a hook matcher sees them, sorted: none for a tool Claude Code has no
// counterpart of, two for the agent tool (Agent and Task).
func ClaudeCodeNamesOf(name string) []string {
	names := slices.Clone(claudeCodeMatchedAs[name])
	for claudeCode, ours := range claudeCodeNames {
		if ours == name {
			names = append(names, claudeCode)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// claudeCodeGuardedAs are the Claude Code tools whose job a built-in tool
// does with input of another shape, where Claude Code has no tool of its
// own for it: multiedit, lsp_rename and lsp_replace_symbol change files,
// as Edit does; download fetches a page, as WebFetch does, and writes it
// to a file, as Write does; x402_fetch fetches one, paying for it;
// sourcegraph searches code on the web; and the other lsp_ tools each do
// part of what Claude Code's one LSP tool does. A hook written for Claude
// Code cannot name these tools, so it guards them by these names.
var claudeCodeGuardedAs = map[string][]string{
	"download":           {"WebFetch", "Write"},
	"lsp_call_hierarchy": {"LSP"},
	"lsp_definition":     {"LSP"},
	"lsp_diagnostics":    {"LSP"},
	"lsp_references":     {"LSP"},
	"lsp_rename":         {"Edit"},
	"lsp_replace_symbol": {"Edit"},
	"lsp_restart":        {"LSP"},
	"lsp_symbols":        {"LSP"},
	"multiedit":          {"Edit"},
	"sourcegraph":        {"WebSearch"},
	"x402_fetch":         {"WebFetch"},
}

// ClaudeCodeGuardNamesOf returns the names of the Claude Code tools whose
// job the tool name does with input of another shape (see
// claudeCodeGuardedAs). A hook matcher that matches the tool only by one
// of them can block a call but not approve it: what such a hook checked
// before approving was input of another shape, or for a smaller job, so
// a hook approving Write calls does not approve a download.
func ClaudeCodeGuardNamesOf(name string) []string {
	return slices.Clone(claudeCodeGuardedAs[name])
}

// NamesATool reports whether name, a hook matcher's exact name, can be a
// built-in tool's: its name, its name before the rename, a sub-agent
// tool's, or a name Claude Code gives one or a tool whose job it does.
func NamesATool(name string) bool {
	if IsKnown(name) || slices.Contains(subAgentNames, name) || FromClaudeCode(name) != "" {
		return true
	}
	for _, table := range []map[string][]string{claudeCodeMatchedAs, claudeCodeGuardedAs} {
		for _, claudeCode := range table {
			if slices.Contains(claudeCode, name) {
				return true
			}
		}
	}
	return false
}

// IsMCPToolName reports whether name has the shape of an MCP tool name.
func IsMCPToolName(name string) bool {
	return len(name) > len(MCPPrefix) && name[:len(MCPPrefix)] == MCPPrefix
}
