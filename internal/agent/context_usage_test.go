package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/stretchr/testify/require"
)

type contextProbeParams struct {
	Path string `json:"path"`
}

// contextProbe is a built-in tool with a definition of some length.
func contextProbe(name string) fantasy.AgentTool {
	return fantasy.NewAgentTool(name, "Look at a file and say what is in it.",
		func(context.Context, contextProbeParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse(""), nil
		})
}

// serverTool is a tool an MCP server provides.
type serverTool struct {
	fantasy.AgentTool
	server string
}

func (t serverTool) MCP() string         { return t.server }
func (t serverTool) MCPToolName() string { return t.Info().Name }

func definitionTokens(t fantasy.AgentTool) int64 {
	b, _ := json.Marshal(t.Info())
	return approxTokenCount(string(b))
}

func categories(u ContextUsage) map[string]int64 {
	out := map[string]int64{}
	for _, c := range u.Categories {
		out[c.Name] = c.Tokens
	}
	return out
}

// The report, as Claude Code's /context estimates it: the memory files
// and skills counted apart from the rest of the system prompt they are
// in, the MCP servers' tools apart from the built-in ones, the
// conversation as it is sent, and the rest of the window split into the
// auto-compaction buffer and free space.
func TestContextUsage(t *testing.T) {
	t.Parallel()

	global := prompt.ContextFile{Path: "/home/u/.config/crush/CRUSH.md", Content: strings.Repeat("g", 400)}
	project := prompt.ContextFile{Path: "/p/AGENTS.md", Content: strings.Repeat("p", 800)}
	local := prompt.ContextFile{Path: "/p/AGENTS.local.md", Content: strings.Repeat("l", 40)}
	gno := &skills.Skill{Name: "gno", Description: strings.Repeat("d", 200), SkillFilePath: "/skills/gno/SKILL.md", Builtin: true}
	mine := &skills.Skill{Name: "mine", Description: "my own", SkillFilePath: "/home/u/skills/mine/SKILL.md"}
	hidden := &skills.Skill{Name: "hidden", Description: "not for the model", DisableModelInvocation: true}
	skillsXML := skills.ToPromptXML([]*skills.Skill{gno, mine, hidden})
	systemPrompt := strings.Repeat("s", 2000) + global.Content + project.Content + local.Content + skillsXML
	builtin, search := contextProbe("view"), serverTool{contextProbe("search"), "docs"}
	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello there"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "hi"}}},
	}
	var msgTokens int64
	for _, msg := range msgs {
		for _, m := range msg.ToAIMessage() {
			b, _ := json.Marshal(m)
			msgTokens += approxTokenCount(string(b))
		}
	}

	u := contextUsage(contextUsageInput{
		model:        "qwen3.8-27b",
		window:       100_000,
		systemPrompt: systemPrompt,
		data: prompt.PromptDat{
			GlobalContextFiles: []prompt.ContextFile{global},
			ContextFiles:       []prompt.ContextFile{project, local},
			Skills:             []*skills.Skill{gno, mine, hidden},
		},
		tools:     []fantasy.AgentTool{builtin, search},
		messages:  msgs,
		compactAt: 80_000,
	})

	names := make([]string, len(u.Categories))
	for i, c := range u.Categories {
		names[i] = c.Name
	}
	require.Equal(t, []string{"System prompt", "System tools", "MCP tools", "Memory files", "Skills", "Messages", "Free space", "Autocompact buffer"}, names)

	require.Equal(t, []ContextItem{
		{Name: global.Path, Source: "User", Tokens: 100},
		{Name: project.Path, Source: "Project", Tokens: 200},
		{Name: local.Path, Source: "Local", Tokens: 10},
	}, u.MemoryFiles)
	require.Len(t, u.Skills, 2, "a skill the model may not invoke is not listed")
	require.Equal(t, "gno", u.Skills[0].Name)
	require.Equal(t, "builtin", u.Skills[0].Source)
	require.Equal(t, "user", u.Skills[1].Source)
	require.Greater(t, u.Skills[0].Tokens, u.Skills[1].Tokens, "the longer description takes more")
	require.Equal(t, []ContextItem{{Name: "search", Source: "docs", Tokens: definitionTokens(search)}}, u.MCPTools)

	c := categories(u)
	require.Equal(t, int64(310), c["Memory files"])
	require.Equal(t, u.Skills[0].Tokens+u.Skills[1].Tokens, c["Skills"])
	require.Equal(t, approxTokenCount(systemPrompt)-310-c["Skills"], c["System prompt"])
	require.Equal(t, definitionTokens(builtin), c["System tools"])
	require.Equal(t, definitionTokens(search), c["MCP tools"])
	require.Equal(t, msgTokens, c["Messages"])
	require.Equal(t, c["System prompt"]+c["System tools"]+c["MCP tools"]+c["Memory files"]+c["Skills"]+c["Messages"], u.Used)
	require.Equal(t, int64(20_000), c["Autocompact buffer"])
	require.Equal(t, 100_000-u.Used-20_000, c["Free space"])

	// With auto-compaction off there is no buffer: the rest is free.
	off := contextUsage(contextUsageInput{model: "m", window: 100_000, systemPrompt: systemPrompt})
	require.Zero(t, categories(off)["Autocompact buffer"])
	require.Equal(t, 100_000-off.Used, categories(off)["Free space"])
}

// The report reads as Claude Code's /context does without its grid.
func TestContextUsageMarkdown(t *testing.T) {
	t.Parallel()

	u := ContextUsage{
		Model:  "qwen3.8-27b",
		Window: 200_000,
		Used:   12_345,
		Categories: []ContextCategory{
			{"System prompt", 3_000},
			{"System tools", 9_000},
			{"MCP tools", 0},
			{"Memory files", 300},
			{"Skills", 45},
			{"Messages", 0},
			{"Free space", 167_655},
			{"Autocompact buffer", 20_000},
		},
		MCPTools:    []ContextItem{{Name: "search", Source: "docs", Tokens: 120}},
		MemoryFiles: []ContextItem{{Name: "/p/AGENTS.md", Source: "Project", Tokens: 300}},
	}
	md := u.Markdown()
	for _, want := range []string{
		"## Context Usage\n",
		"**Model:** qwen3.8-27b",
		"**Tokens:** 12.3k / 200.0k (6.2%)",
		"| Category | Tokens | Percentage |",
		"| System tools | 9.0k | 4.5% |",
		"| Free space | 167.7k | 83.8% |",
		"| Autocompact buffer | 20.0k | 10.0% |",
		"### MCP Tools\n\n| Tool | Server | Tokens |",
		"| search | docs | 120 |",
		"### Memory Files\n\n| Type | Path | Tokens |",
		"| Project | /p/AGENTS.md | 300 |",
	} {
		require.Contains(t, md, want)
	}
	require.NotContains(t, md, "### Skills", "an empty table is left out")

	unknown := ContextUsage{Model: "m", Used: 50, Categories: []ContextCategory{{"Messages", 50}}}.Markdown()
	require.Contains(t, unknown, "**Tokens:** 50 / an unknown window (?)")
	require.Contains(t, unknown, "| Messages | 50 | ? |")

	require.Equal(t, "950", formatTokens(950))
	require.Equal(t, "1.2M", formatTokens(1_200_000))
}
