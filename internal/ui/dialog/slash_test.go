package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/commands"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/stretchr/testify/require"
)

// A slash command is "/name" and what follows, its name a letter, digit
// or underscore and then those, colons and hyphens, as Claude Code reads
// one; a path is not one.
func TestSlashCommandName(t *testing.T) {
	t.Parallel()

	for line, want := range map[string]string{
		"/clear":                   "clear",
		"  /compact keep the API ": "compact",
		"/user:review-pr_2 123":    "user:review-pr_2",
		"/help\tme":                "help",
	} {
		name, ok := SlashCommandName(line)
		require.True(t, ok, line)
		require.Equal(t, want, name, line)
	}
	for _, line := range []string{"/etc/hosts what is this", "/", "/ clear", "/-x", "clear", "", "/stat?"} {
		_, ok := SlashCommandName(line)
		require.False(t, ok, line)
	}
}

// Claude Code's words for a command it does not have.
func TestUnknownCommand(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Unknown command: /stauts. Did you mean /status?", UnknownCommand("stauts", "status"))
	require.Equal(t, "Unknown command: /zqx", UnknownCommand("zqx", ""))
}

// Edits are insertions, deletions, substitutions and swaps of neighbours,
// counted as the optimal string alignment counts them.
func TestEditDistance(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		a, b string
		want int
	}{
		{"status", "status", 0},
		{"stauts", "status", 1},
		{"stats", "status", 1},
		{"statuss", "status", 1},
		{"", "ab", 2},
		{"kitten", "sitting", 3},
		{"ca", "abc", 3},
		{"héllo", "hello", 1},
	} {
		require.Equal(t, c.want, editDistance(c.a, c.b), "%q %q", c.a, c.b)
	}
}

// A typed /name finds the palette's command of that name in any case: an
// item by its aliases, a custom command by its name with or without its
// prefix, a skill by its own name too, and an MCP prompt by its name; the
// suggestion is the name at most two edits away.
func TestLookupAndSuggest(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	review := commands.CustomCommand{ID: "project:review", Name: "project:review", Content: "Review $PR", Arguments: []commands.Argument{{ID: "PR", Title: "PR", Required: true}}}
	gno := commands.CustomCommand{ID: "user:gno", Name: "user:gno", Skill: &skills.Skill{Name: "gno-dev", SkillFilePath: "/s/gno/SKILL.md"}}
	prompt := commands.MCPPrompt{ID: "docs:summarize", PromptID: "summarize", ClientID: "docs", Title: "Summarize"}
	c, err := NewCommands(com, "s1", true, false, false, []commands.CustomCommand{review, gno}, []commands.MCPPrompt{prompt})
	require.NoError(t, err)

	for name, want := range map[string]Action{
		"clear":          ActionNewSession{},
		"STATUS":         ActionStatusReport{},
		"bashes":         ActionTasks{},
		"review":         ActionRunCustomCommand{Content: "Review $PR", Arguments: review.Arguments},
		"project:review": ActionRunCustomCommand{Content: "Review $PR", Arguments: review.Arguments},
		"gno":            ActionAttachSkill{ID: "/s/gno/SKILL.md", Name: "gno-dev"},
		"gno-dev":        ActionAttachSkill{ID: "/s/gno/SKILL.md", Name: "gno-dev"},
		"summarize":      ActionRunMCPPrompt{Title: "Summarize", PromptID: "summarize", ClientID: "docs"},
	} {
		action, ok := c.Lookup(name)
		require.True(t, ok, name)
		require.Equal(t, want, action, name)
	}
	_, ok := c.Lookup("zqx")
	require.False(t, ok)

	require.Equal(t, "status", c.Suggest("stauts"))
	require.Equal(t, "new", c.Suggest("nem"), "the nearest, not the last near one (name)")
	require.Equal(t, "review", c.Suggest("reveiw"))
	require.Equal(t, "", c.Suggest("zqxzqx"))
}

// Enter on what matches nothing in the palette says it is unknown, as
// Claude Code says it, and leaves the palette open to be fixed.
func TestPaletteSaysUnknownCommand(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	c, err := NewCommands(com, "", false, false, false, nil, nil)
	require.NoError(t, err)
	for _, r := range "zqx" {
		c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	action, ok := c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}).(ActionCmd)
	require.True(t, ok)
	require.Equal(t, util.NewWarnMsg("Unknown command: /zqx"), action.Cmd())
}

// What follows a slash command's name is its arguments, as typed.
func TestSlashCommandArgs(t *testing.T) {
	t.Parallel()

	require.Equal(t, "fix the  bug", SlashCommandArgs("  /review fix the  bug "))
	require.Equal(t, "", SlashCommandArgs("/review"))
	require.Equal(t, "line one\nline two", SlashCommandArgs("/note\tline one\nline two"))
}

// A command gets the text typed after its name as Claude Code gives it:
// one argument takes it all as typed; more take a word each and the last
// the rest, or are asked for when the words are too few; a skill takes it
// for its prompt; other commands take none.
func TestWithArgs(t *testing.T) {
	t.Parallel()

	one := []commands.Argument{{ID: "ARGUMENTS"}}
	two := []commands.Argument{{ID: "ISSUE"}, {ID: "NOTE"}}
	run := func(arguments []commands.Argument) ActionRunCustomCommand {
		return ActionRunCustomCommand{Content: "x", Arguments: arguments}
	}
	withArgs := func(arguments []commands.Argument, args map[string]string) ActionRunCustomCommand {
		a := run(arguments)
		a.Args = args
		return a
	}

	require.Equal(t, withArgs(one, map[string]string{"ARGUMENTS": "fix the  bug"}), WithArgs(run(one), "fix the  bug"))
	require.Equal(t, withArgs(two, map[string]string{"ISSUE": "123", "NOTE": "high  priority"}), WithArgs(run(two), "123  high  priority"))
	require.Equal(t, run(two), WithArgs(run(two), "123"), "too few words: asked for")
	require.Equal(t, run(nil), WithArgs(run(nil), "123"), "no arguments to take them")
	require.Equal(t, run(one), WithArgs(run(one), ""))
	require.Equal(t, run(one), WithArgs(run(one), " \t "), "only spaces: none")
	require.Equal(t, withArgs(one, map[string]string{"ARGUMENTS": "fix it"}), WithArgs(run(one), "  fix it \n"))

	prompt := ActionRunMCPPrompt{PromptID: "summarize", Arguments: []commands.Argument{{ID: "text"}}}
	require.Equal(t, map[string]string{"text": "the README"}, WithArgs(prompt, "the README").(ActionRunMCPPrompt).Args)
	require.Equal(t, ActionAttachSkill{ID: "s", Name: "gno", Args: "deploy it"}, WithArgs(ActionAttachSkill{ID: "s", Name: "gno"}, "deploy it"))
	require.Equal(t, ActionStatusReport{}, WithArgs(ActionStatusReport{}, "now"))
}

// A custom command, which the palette's system list does not show, runs
// by its name typed there, with what follows as its arguments.
func TestPaletteRunsACustomCommandByName(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	review := commands.CustomCommand{ID: "project:review", Name: "project:review", Content: "Review $PR", Arguments: []commands.Argument{{ID: "PR", Title: "PR", Required: true}}}
	c, err := NewCommands(com, "s1", true, false, false, []commands.CustomCommand{review}, nil)
	require.NoError(t, err)
	for _, r := range "review 123" {
		c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	require.Equal(t, ActionRunCustomCommand{Content: "Review $PR", Arguments: review.Arguments, Args: map[string]string{"PR": "123"}},
		c.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
}
