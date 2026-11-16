package permission

import (
	"sort"
	"strings"
)

// toolClass sorts tools by how their calls are judged.
type toolClass uint8

const (
	// classOther: asks unless a rule allows it.
	classOther toolClass = iota
	// classShell: runs a command line; judged by Bash rules.
	classShell
	// classFileRead: reads files; judged by Read rules and the working
	// directories.
	classFileRead
	// classFileEdit: changes files; judged by Edit rules, the mode, the
	// working directories and the protected paths.
	classFileEdit
	// classFetch: fetches a URL; judged by WebFetch(domain:...) rules.
	classFetch
	// classAgent: starts a sub-agent, whose own calls are judged as they
	// happen.
	classAgent
	// classSkill: runs a skill.
	classSkill
	// classQuiet: never asks.
	classQuiet
	// classPerson: is itself a question to the person.
	classPerson
	// classAbsent: a name Claude Code has or had that gnosh has no tool
	// for. Rules that name it load and never match.
	classAbsent
)

// toolInfo is what the engine knows about one tool name.
type toolInfo struct {
	class toolClass
	// primary is the input field the tool's own rule form is about. A
	// Tool(param:value) rule may not name it.
	primary string
	// params are the tool's other top-level inputs, for Tool(param:value).
	params []string
}

// Tool names, spelled as Claude Code spells them.
const (
	toolBash         = "Bash"
	toolMonitor      = "Monitor"
	toolRead         = "Read"
	toolGrep         = "Grep"
	toolGlob         = "Glob"
	toolEdit         = "Edit"
	toolWrite        = "Write"
	toolNotebookEdit = "NotebookEdit"
	toolWebFetch     = "WebFetch"
	toolWebSearch    = "WebSearch"
	toolAgent        = "Agent"
	toolSkill        = "Skill"
	toolAskUser      = "AskUserQuestion"
	mcpPrefix        = "mcp__"
)

// localHost is the name this machine goes by as the host of a Bash call
// (the shell tool's LocalName).
const localHost = "local"

// builtinTools is the table of names this package knows by itself. The
// program's own tools are added through Options.KnownTools and
// Options.QuietTools.
var builtinTools = map[string]toolInfo{
	toolBash:         {classShell, "command", []string{"timeout", "description", "run_in_background", "dangerouslyDisableSandbox", "host"}},
	toolMonitor:      {classShell, "command", []string{"description", "timeout_ms"}},
	toolRead:         {classFileRead, "file_path", []string{"offset", "limit", "pages", "allow_large"}},
	toolGrep:         {classFileRead, "path", []string{"pattern", "glob", "type", "output_mode", "-i", "-n", "-o", "-A", "-B", "-C", "context", "head_limit", "offset", "multiline"}},
	toolGlob:         {classFileRead, "path", []string{"pattern"}},
	toolEdit:         {classFileEdit, "file_path", []string{"old_string", "new_string", "replace_all"}},
	toolWrite:        {classFileEdit, "file_path", []string{"content"}},
	toolNotebookEdit: {classFileEdit, "notebook_path", []string{"cell_id", "new_source", "cell_type", "edit_mode"}},
	toolWebFetch:     {classFetch, "url", []string{"prompt", "offset"}},
	toolWebSearch:    {classOther, "", []string{"query", "mode", "allowed_domains", "blocked_domains"}},
	toolAgent:        {classAgent, "", []string{"description", "prompt", "subagent_type", "model", "effort", "run_in_background", "name", "isolation"}},
	toolSkill:        {classSkill, "", []string{"skill", "args"}},
	toolAskUser:      {classPerson, "", nil},

	"TaskStop":             {classQuiet, "", []string{"task_id", "shell_id"}},
	"SendMessage":          {classQuiet, "", []string{"to", "message", "summary"}},
	"EnterPlanMode":        {classQuiet, "", nil},
	"ExitPlanMode":         {classOther, "", nil},
	"ToolSearch":           {classQuiet, "", nil},
	"ListMcpResourcesTool": {classQuiet, "", nil},
	"ReadMcpResourceTool":  {classQuiet, "", nil},
	"WaitForMcpServers":    {classQuiet, "", nil},
	"EnterWorktree":        {classOther, "", nil},
	"ExitWorktree":         {classQuiet, "", nil},
	"CronCreate":           {classQuiet, "", nil},
	"CronDelete":           {classQuiet, "", nil},
	"CronList":             {classQuiet, "", nil},
	"ScheduleWakeup":       {classQuiet, "", nil},
	"LSP":                  {classQuiet, "", nil},

	// Names Claude Code has or had and gnosh does not: settings written
	// for Claude Code load, and these rules do nothing because there is
	// nothing for them to do.
	"TodoWrite": {class: classAbsent}, "TaskCreate": {class: classAbsent},
	"TaskGet": {class: classAbsent}, "TaskList": {class: classAbsent},
	"TaskUpdate": {class: classAbsent}, "BashOutput": {class: classAbsent},
	"TaskOutput": {class: classAbsent}, "MultiEdit": {class: classAbsent},
	"LS": {class: classAbsent}, "PowerShell": {class: classAbsent},
	"Artifact": {class: classAbsent}, "EndConversation": {class: classAbsent},
	"ListAgents":   {class: classAbsent},
	"SendUserFile": {class: classAbsent}, "SendFeedback": {class: classAbsent},
	"PushNotification": {class: classAbsent}, "RemoteTrigger": {class: classAbsent},
	"ReportFindings": {class: classAbsent}, "ShareOnboardingGuide": {class: classAbsent},
	"SubagentHandback": {class: classAbsent}, "Workflow": {class: classAbsent},
}

// toolAliases maps the old names Claude Code still accepts to today's.
var toolAliases = map[string]string{
	"Task":      toolAgent,
	"KillShell": "TaskStop",
	"KillBash":  "TaskStop",
}

// CanonicalTool returns the current name for a tool name that may be one
// of the old aliases (Task, KillShell, KillBash).
func CanonicalTool(name string) string {
	if c, ok := toolAliases[name]; ok {
		return c
	}
	return name
}

// isMCP reports whether name is the name of an MCP server's tool, or of a
// whole server.
func isMCP(name string) bool { return strings.HasPrefix(name, mcpPrefix) }

// names is the engine's view of which tools exist.
type names struct {
	extra map[string]toolInfo
}

func newNames(known, quiet []string) names {
	n := names{extra: map[string]toolInfo{}}
	for _, k := range known {
		if info, ok := builtinTools[k]; !ok || info.class == classAbsent {
			n.extra[k] = toolInfo{class: classOther}
		}
	}
	for _, k := range quiet {
		n.extra[k] = toolInfo{class: classQuiet}
	}
	return n
}

// info returns what is known about the canonical name, and whether the
// name is known at all.
func (n names) info(canon string) (toolInfo, bool) {
	if info, ok := n.extra[canon]; ok {
		return info, true
	}
	if info, ok := builtinTools[canon]; ok {
		return info, true
	}
	if isMCP(canon) {
		return toolInfo{class: classOther}, true
	}
	return toolInfo{}, false
}

// all lists every known name (aliases and absent names left out), sorted,
// for error messages.
func (n names) all() []string {
	var out []string
	for k, info := range builtinTools {
		if info.class != classAbsent {
			if _, shadow := n.extra[k]; !shadow {
				out = append(out, k)
			}
		}
	}
	for k := range n.extra {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hasGlob reports whether a tool-name position holds a wildcard.
func hasGlob(s string) bool { return strings.ContainsAny(s, "*?") }

// globMatch matches a whole name against a pattern in which * is any run
// of characters and ? is any one.
func globMatch(pat, s string) bool {
	px, sx := 0, 0
	star, mark := -1, 0
	for sx < len(s) {
		switch {
		case px < len(pat) && pat[px] == '*':
			star, mark = px, sx
			px++
		case px < len(pat) && (pat[px] == '?' || pat[px] == s[sx]):
			px++
			sx++
		case star >= 0:
			px = star + 1
			mark++
			sx = mark
		default:
			return false
		}
	}
	for px < len(pat) && pat[px] == '*' {
		px++
	}
	return px == len(pat)
}
