package dialog

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/crush/internal/commands"
)

// slashName is a slash command's name as Claude Code reads one: a letter,
// digit or underscore, then those, colons and hyphens. "/etc/hosts" has
// none, and is a prompt.
var slashName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9:_-]*$`)

// SlashCommandName is the name of the slash command line is, "/name" and
// whatever follows it, as Claude Code reads one; false when line is no
// slash command.
func SlashCommandName(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return "", false
	}
	name := line[1:]
	if i := strings.IndexFunc(name, unicode.IsSpace); i >= 0 {
		name = name[:i]
	}
	if !slashName.MatchString(name) {
		return "", false
	}
	return name, true
}

// UnknownCommand is what Claude Code says of a slash command it does not
// have, naming the one meant when suggestion is not "".
func UnknownCommand(name, suggestion string) string {
	if suggestion != "" {
		return "Unknown command: /" + name + ". Did you mean /" + suggestion + "?"
	}
	return "Unknown command: /" + name
}

// slashCommand is a command the palette has, with the names a slash
// command finds it by.
type slashCommand struct {
	names  []string
	action Action
}

// slashCommands is every command the palette has, each with the names a
// typed /name finds it by: an item's aliases (Claude Code's names, as
// "clear" for New Session), a custom command's name with and without its
// user: or project: prefix (a skill's own name too), and an MCP prompt's
// name.
func (c *Commands) slashCommands() []slashCommand {
	var cmds []slashCommand
	for _, item := range c.defaultCommands() {
		if len(item.aliases) > 0 {
			cmds = append(cmds, slashCommand{names: item.aliases, action: item.Action()})
		}
	}
	for _, cmd := range c.customCommands {
		names := []string{cmd.ID, cmd.ShortName()}
		if cmd.Skill != nil {
			names = append(names, cmd.Skill.Name)
		}
		cmds = append(cmds, slashCommand{names: names, action: customCommandAction(cmd)})
	}
	for _, prompt := range c.mcpPrompts {
		cmds = append(cmds, slashCommand{names: []string{prompt.PromptID}, action: mcpPromptAction(prompt)})
	}
	return cmds
}

// Lookup is the action of the command a typed /name runs, by any of its
// names in any case, as the palette runs it; false when there is none.
func (c *Commands) Lookup(name string) (Action, bool) {
	for _, cmd := range c.slashCommands() {
		if slices.ContainsFunc(cmd.names, func(n string) bool { return strings.EqualFold(n, name) }) {
			return cmd.action, true
		}
	}
	return nil, false
}

// Suggest is the command name nearest name, at most two edits away, as
// Claude Code's "Did you mean" suggests one; "" when none is that near.
// The first of the nearest wins.
func (c *Commands) Suggest(name string) string {
	best, bestDistance := "", 3
	for _, cmd := range c.slashCommands() {
		for _, n := range cmd.names {
			if d := editDistance(strings.ToLower(name), strings.ToLower(n)); d < bestDistance {
				best, bestDistance = n, d
			}
		}
	}
	return best
}

// editDistance is how many insertions, deletions, substitutions and swaps
// of neighbours turn a into b, as Claude Code counts them for its
// suggestions (the optimal string alignment distance).
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}

// customCommandAction is what running cmd from the palette does: attach
// the skill it stands for, or run the command.
func customCommandAction(cmd commands.CustomCommand) Action {
	if cmd.Skill != nil {
		return ActionAttachSkill{ID: cmd.Skill.SkillFilePath, Name: cmd.Skill.Name}
	}
	return ActionRunCustomCommand{
		Content:   cmd.Content,
		Arguments: cmd.Arguments,
		Skill:     cmd.Skill,
	}
}

// mcpPromptAction is what running an MCP prompt from the palette does.
func mcpPromptAction(prompt commands.MCPPrompt) Action {
	return ActionRunMCPPrompt{
		Title:       prompt.Title,
		Description: prompt.Description,
		PromptID:    prompt.PromptID,
		ClientID:    prompt.ClientID,
		Arguments:   prompt.Arguments,
	}
}
