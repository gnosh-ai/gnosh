package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/skills"
)

// ContextUsage is what fills a session's context window, estimated as
// Claude Code's /context estimates it: tokens by category, and the MCP
// tools, memory files and skills that take them.
type ContextUsage struct {
	Model  string
	Window int64
	// Used is the tokens the categories before Free space take.
	Used int64
	// Categories are Claude Code's, in its order: System prompt, System
	// tools, MCP tools, Memory files, Skills, Messages, Free space and
	// Autocompact buffer.
	Categories  []ContextCategory
	MCPTools    []ContextItem
	MemoryFiles []ContextItem
	Skills      []ContextItem
}

// ContextCategory is one share of the context window.
type ContextCategory struct {
	Name   string
	Tokens int64
}

// ContextItem is one MCP tool, memory file or skill, where it comes from
// (its MCP server, the memory file's kind, the skill's origin), and the
// tokens it takes.
type ContextItem struct {
	Name   string
	Source string
	Tokens int64
}

// contextUsageInput is what a context usage report is made from.
type contextUsageInput struct {
	model  string
	window int64
	// systemPrompt is the system prompt the requests carry, and data what
	// its template was filled with: its context files and skills.
	systemPrompt string
	data         prompt.PromptDat
	tools        []fantasy.AgentTool
	messages     []message.Message
	// compactAt is the token count at which the session compacts, 0 when
	// it never does.
	compactAt int64
}

// mcpTool is a tool an MCP server provides, as tools.Tool is.
type mcpTool interface {
	MCP() string
	MCPToolName() string
}

// skillListWrapper is what skills.ToPromptXML puts around the skills it
// lists, taken off one skill's listing to count the skill alone.
var skillListWrapper = approxTokenCount("<available_skills>\n</available_skills>")

// contextUsage estimates, as approxTokenCount does, what each part of a
// request takes. The memory files and skills are within the system
// prompt, so they are taken off it; the tools are their definitions as
// sent; the messages are the conversation as the model is sent it.
func contextUsage(in contextUsageInput) ContextUsage {
	u := ContextUsage{Model: in.model, Window: in.window}
	var memory, skillTokens, systemTools, mcpTools, messages int64
	for _, f := range in.data.GlobalContextFiles {
		u.MemoryFiles = append(u.MemoryFiles, ContextItem{Name: f.Path, Source: memoryTypeFor(f.Path, true), Tokens: approxTokenCount(f.Content)})
	}
	for _, f := range in.data.ContextFiles {
		u.MemoryFiles = append(u.MemoryFiles, ContextItem{Name: f.Path, Source: memoryTypeFor(f.Path, false), Tokens: approxTokenCount(f.Content)})
	}
	for _, f := range u.MemoryFiles {
		memory += f.Tokens
	}
	for _, s := range in.data.Skills {
		if s.DisableModelInvocation {
			continue
		}
		source := "user"
		if s.Builtin {
			source = "builtin"
		}
		tokens := max(0, approxTokenCount(skills.ToPromptXML([]*skills.Skill{s}))-skillListWrapper)
		u.Skills = append(u.Skills, ContextItem{Name: s.Name, Source: source, Tokens: tokens})
		skillTokens += tokens
	}
	for _, t := range in.tools {
		definition, _ := json.Marshal(t.Info())
		tokens := approxTokenCount(string(definition))
		if mcp, ok := t.(mcpTool); ok {
			u.MCPTools = append(u.MCPTools, ContextItem{Name: mcp.MCPToolName(), Source: mcp.MCP(), Tokens: tokens})
			mcpTools += tokens
			continue
		}
		systemTools += tokens
	}
	for _, msg := range in.messages {
		for _, m := range msg.ToAIMessage() {
			sent, _ := json.Marshal(m)
			messages += approxTokenCount(string(sent))
		}
	}
	system := max(0, approxTokenCount(in.systemPrompt)-memory-skillTokens)

	u.Used = system + systemTools + mcpTools + memory + skillTokens + messages
	var buffer int64
	if in.compactAt > 0 && in.window > 0 {
		buffer = max(0, in.window-in.compactAt)
	}
	free := max(0, in.window-u.Used-buffer)
	u.Categories = []ContextCategory{
		{"System prompt", system},
		{"System tools", systemTools},
		{"MCP tools", mcpTools},
		{"Memory files", memory},
		{"Skills", skillTokens},
		{"Messages", messages},
		{"Free space", free},
		{"Autocompact buffer", buffer},
	}
	return u
}

// Markdown is the report as Claude Code's /context prints it without its
// grid: the model, the tokens in use out of the window, a table by
// category, and tables of the MCP tools, memory files and skills.
func (u ContextUsage) Markdown() string {
	var sb strings.Builder
	sb.WriteString("## Context Usage\n\n")
	fmt.Fprintf(&sb, "**Model:** %s  \n", u.Model)
	fmt.Fprintf(&sb, "**Tokens:** %s / %s (%s)\n", formatTokens(u.Used), formatWindow(u.Window), percentOf(u.Used, u.Window))
	sb.WriteString("\n### Estimated usage by category\n\n")
	sb.WriteString("| Category | Tokens | Percentage |\n|----------|--------|------------|\n")
	for _, c := range u.Categories {
		fmt.Fprintf(&sb, "| %s | %s | %s |\n", c.Name, formatTokens(c.Tokens), percentOf(c.Tokens, u.Window))
	}
	table := func(title, header string, items []ContextItem, row func(ContextItem) string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&sb, "\n### %s\n\n%s\n", title, header)
		for _, item := range items {
			sb.WriteString(row(item))
		}
	}
	table("MCP Tools", "| Tool | Server | Tokens |\n|------|--------|--------|", u.MCPTools, func(i ContextItem) string {
		return fmt.Sprintf("| %s | %s | %s |\n", i.Name, i.Source, formatTokens(i.Tokens))
	})
	table("Memory Files", "| Type | Path | Tokens |\n|------|------|--------|", u.MemoryFiles, func(i ContextItem) string {
		return fmt.Sprintf("| %s | %s | %s |\n", i.Source, i.Name, formatTokens(i.Tokens))
	})
	table("Skills", "| Skill | Source | Tokens |\n|-------|--------|--------|", u.Skills, func(i ContextItem) string {
		return fmt.Sprintf("| %s | %s | %s |\n", i.Name, i.Source, formatTokens(i.Tokens))
	})
	return sb.String()
}

// formatTokens writes a token count as Claude Code's reports do: 950,
// 12.3k, 1.2M.
func formatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// formatWindow is formatTokens for a context window, which a model may
// not report.
func formatWindow(n int64) string {
	if n <= 0 {
		return "an unknown window"
	}
	return formatTokens(n)
}

// percentOf is n as a share of window, "?" when the window is unknown.
func percentOf(n, window int64) string {
	if window <= 0 {
		return "?"
	}
	return fmt.Sprintf("%.1f%%", float64(n)*100/float64(window))
}
