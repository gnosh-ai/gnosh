package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// slashSet is the registry of a session's slash commands: gnosh's own, and
// the ones the features add. A command is found by its exact name or one
// of its aliases, and by nothing else: there is no fuzzy matching, so that
// a word can never run the wrong command.
type slashSet struct {
	l    *live
	cmds []SlashCommand
}

// slashName is what a command's name and its aliases look like.
var slashName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// newSlashSet makes the registry of a session, with gnosh's own commands
// in it.
func newSlashSet(l *live) *slashSet {
	s := &slashSet{l: l}
	for _, cmd := range s.own() {
		if err := s.add(cmd); err != nil {
			panic("app: " + err.Error())
		}
	}
	return s
}

// add puts a command in the registry. A name that is taken is refused, so
// that no feature can quietly replace another's command or one of gnosh's.
func (s *slashSet) add(cmd SlashCommand) error {
	if cmd.Run == nil {
		return fmt.Errorf("the slash command %q has nothing to run", cmd.Name)
	}
	for _, name := range append([]string{cmd.Name}, cmd.Aliases...) {
		if !slashName.MatchString(name) {
			return fmt.Errorf("%q cannot be the name of a slash command: a name is lower-case letters, digits, - and _, and begins with a letter", name)
		}
		if s.find(name) != nil {
			return fmt.Errorf("there is already a slash command named /%s", name)
		}
	}
	s.cmds = append(s.cmds, cmd)
	return nil
}

// find returns the command with that name or alias, or nil.
func (s *slashSet) find(name string) *SlashCommand {
	for i := range s.cmds {
		if s.cmds[i].Name == name || slices.Contains(s.cmds[i].Aliases, name) {
			return &s.cmds[i]
		}
	}
	return nil
}

// names lists the commands by name, in the order they were added.
func (s *slashSet) names() []string {
	out := make([]string, len(s.cmds))
	for i, c := range s.cmds {
		out[i] = c.Name
	}
	return out
}

// list describes the commands to a front end, in the order they were
// added.
func (s *slashSet) list() []CommandInfo {
	out := make([]CommandInfo, len(s.cmds))
	for i, c := range s.cmds {
		out[i] = CommandInfo{
			Name: c.Name, Aliases: slices.Clone(c.Aliases), Args: c.Args, Summary: c.Summary,
			Sensitive: slices.Clone(c.Sensitive), InRun: c.InRun,
		}
	}
	return out
}

// run runs a line as a slash command. A line that names no command is not
// handled, and the front end then sends it as a prompt or gives it back.
func (s *slashSet) run(ctx context.Context, line string) (res CommandResult, err error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return CommandResult{}, nil
	}
	word, args := line[1:], ""
	if i := strings.IndexFunc(word, unicode.IsSpace); i >= 0 {
		word, args = word[:i], strings.TrimSpace(word[i:])
	}
	cmd := s.find(strings.ToLower(word))
	if cmd == nil {
		// Not a command of gnosh's or of a feature: it may be a skill of
		// the person's.
		res, _, err := s.l.typedSkill(word, args, line)
		if res.Handled {
			s.l.flog.logf("turn", "the skill /%s was typed (%d bytes of arguments)%s", word, len(args), failedWith(err))
			return res, err
		}
		// Or a prompt of a connected MCP server.
		res, _, err = s.l.typedMCPPrompt(word, args, line)
		if res.Handled {
			s.l.flog.logf("turn", "the MCP prompt /%s was typed (%d bytes of arguments)%s", word, len(args), failedWith(err))
		}
		return res, err
	}
	res = CommandResult{Handled: true, Name: cmd.Name}
	if !s.l.interactive && !cmd.InRun {
		return res, fmt.Errorf("/%s works in an interactive session and does nothing as the prompt of a run, so nothing was done", cmd.Name)
	}
	var out strings.Builder
	c := &SlashContext{
		Args:    args,
		Session: s.l.facts,
		Agent:   s.l.agent,
		Say: func(format string, a ...any) {
			text := format
			if len(a) > 0 {
				text = fmt.Sprintf(format, a...)
			}
			out.WriteString(strings.TrimRight(text, "\n"))
			out.WriteString("\n")
		},
		Send: func(p agent.Prompt) error {
			if res.Prompt != "" {
				return fmt.Errorf("/%s sends one prompt at most", cmd.Name)
			}
			id, err := s.l.agent.Submit(p)
			if err != nil {
				return fmt.Errorf("the prompt could not be sent to the model: %w", err)
			}
			res.Prompt = id
			return nil
		},
		Copy: func(text string) { res.Copy = text },
	}
	func() {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("/%s crashed (%v). This is a fault in gnosh, not in what you typed", cmd.Name, v)
			}
		}()
		err = cmd.Run(ctx, c)
	}()
	res.Text = strings.TrimRight(out.String(), "\n")
	// (/cd ends the session only once it has moved it: saying where the
	// session is, or that it is there already, ends nothing.)
	res.Quit = err == nil && (cmd.Name == "exit" || cmd.Name == "restart" || cmd.Name == "background" || cmd.Name == "cd" && s.l.p.restartArgs() != nil)
	s.l.flog.logf("turn", "slash command /%s (%d bytes of arguments)%s", cmd.Name, len(args), failedWith(err))
	// A command may have changed what the status line is told: the
	// session's name (/rename), the model, its thinking, the style.
	s.l.statusLine.poke()
	return res, err
}

// own lists gnosh's own slash commands.
func (s *slashSet) own() []SlashCommand {
	l := s.l
	return []SlashCommand{
		{Name: "help", Summary: "List the commands.", InRun: true, Run: s.help},
		{Name: "exit", Aliases: []string{"quit"}, Summary: "End the session. It is kept, and can be resumed.",
			Run: func(context.Context, *SlashContext) error { return nil }},
		{Name: "background", Aliases: []string{"bg"}, Args: "<prompt>", Summary: "Let the session go on without this terminal: it ends here and works on the prompt as a run of its own (gnosh -p), and gnosh -r opens it again once that is done.",
			Run: l.slashBackground},
		{Name: "restart", Aliases: []string{"update"}, Summary: "Start gnosh again in this session, with the same flags: a newer gnosh installed since this one started is the one that runs.",
			Run: l.slashRestart},
		{Name: "clear", Aliases: []string{"new", "reset"}, Args: "[name]", Summary: "Start a fresh session. The one before is kept, under the name given, if one is.",
			Run: l.slashClear},
		{Name: "compact", Args: "[instructions]", Summary: "Summarize the conversation now, to make room.", InRun: true, Run: l.slashCompact},
		{Name: "think", Summary: "The most thinking the model has, for every later request. It sticks.", InRun: true,
			Run: func(ctx context.Context, c *SlashContext) error { return l.slashThink(c, true) }},
		{Name: "no_think", Aliases: []string{"nothink", "no-think"}, Summary: "The least thinking the model has, for every later request. It sticks.", InRun: true,
			Run: func(ctx context.Context, c *SlashContext) error { return l.slashThink(c, false) }},
		{Name: "effort", Args: "[level|max|auto]", Summary: "Show or set how much the model thinks: one of its levels, max for the most, or auto for its own default. It sticks.", InRun: true, Run: l.slashEffort},
		{Name: "model", Args: "[provider/model [level]]", Summary: "List the models, or switch to one. The choice is saved.", InRun: true, Run: l.slashModel},
		{Name: "status", Summary: "What this session is, what it is doing, and every setting with where it came from.", InRun: true, Run: l.slashStatus},
		{Name: "release-notes", Summary: "The changes this build of gnosh was made from, newest first.", InRun: true, Run: l.slashReleaseNotes},
		{Name: "license", Aliases: []string{"licence"}, Summary: "gnosh's copyright, licence and credits.", InRun: true, Run: l.slashLicense},
		{Name: "focus", Args: "[on|off]", Summary: "Focus view: each run of tool calls folded into one line that sums it up, with what the edits changed; the prompts and the model's words as they are. A bare /focus turns it over. Kept for every project.", InRun: true, Run: l.slashFocus},
		{Name: "theme", Args: "[auto|dark|light|ansi]", Summary: "The interface's colours: for the terminal's background, dark, light, or the terminal's own sixteen. Kept for every project.", InRun: true, Run: l.slashTheme},
		{Name: "config", Args: "[key[=value] ...]", Summary: "gnosh's options and where each value comes from; key=value sets one for every project, from gnosh's next start, and key= takes it away.", InRun: true, Run: l.slashConfig},
		{Name: "loop", Summary: "Keep the session working, turn after turn, until the model shows the task is done; /loop 5m <prompt> sends a prompt every 5 minutes.", Run: l.slashLoop},
		{Name: "noloop", Aliases: []string{"no_loop", "no-loop"}, Summary: "Turn /loop off.",
			Run: func(ctx context.Context, c *SlashContext) error {
				if l.endSelfLoop("/noloop ended it") {
					return nil
				}
				if !c.Agent.Loop() {
					c.Say("/loop is not on.")
					return nil
				}
				c.Agent.SetLoop(false)
				c.Say("/loop is off. The turn that is running, if any, finishes and nothing follows it.")
				return nil
			}},
		{Name: "permissions", Args: "[allow|ask|deny|remove <rule>]", Summary: "The permission mode, the folders, and every rule with where it came from; or add or take out a rule.", InRun: true, Run: l.slashPermissions},
		{Name: "hooks", Summary: "Every hook that is configured, by the moment it runs at.", InRun: true, Run: l.slashHooks},
		{Name: "output-style", Args: "[<style>]", Summary: "The output styles there are, or switch to one: how the model replies, from your next message on.", InRun: true, Run: l.slashOutputStyle},
		{Name: "skills", Args: "[<name> on|name-only|user-invocable-only|off]", Summary: "Every skill there is, where it came from, and who may run it; or how far one is seen.", InRun: true, Run: l.slashSkills},
		{Name: "reload-skills", Summary: "Read the skill and command folders again now, and say what changed.", InRun: true, Run: l.slashReloadSkills},
		{Name: "autocompact", Args: "[auto|<tokens>]", Summary: "How full the model's window may get before room is made: a number of tokens, or auto.", InRun: true, Run: l.slashAutocompact},
		{Name: "context", Args: "[all]", Summary: "What fills the model's window, by kind.", InRun: true, Run: l.slashContext},
		{Name: "btw", Args: "<question>", Summary: "Ask the model a side question about the conversation, also while it works. Neither the question nor the answer is kept.", InRun: true, Run: l.slashBtw},
		{Name: "statusline", Args: "[what it shows]", Summary: "Have the model write a status line command from your words, try it, and add it to your gnosh.json.", InRun: true, Run: l.slashStatusLine},
		{Name: "recap", Summary: "The session in one line: what it has done and where it stands. Nothing of it is kept.", InRun: true, Run: l.slashRecap},
		{Name: "rename", Args: "[name]", Summary: "Name this session, or show its name.", InRun: true, Run: l.slashRename},
		{Name: "keybindings", Summary: "Your keyboard shortcuts file, as Claude Code's keybindings.json: typed alone in the full interface, it opens in your editor.", InRun: true, Run: l.slashKeybindings},
		{Name: "color", Args: "[" + strings.Join(PromptColors, "|") + "|default]", Summary: "Give this session's prompt mark a colour, to tell it from other sessions; with none named, one at random. It is kept with the session.", InRun: true, Run: l.slashColor},
		{Name: "resume", Aliases: []string{"continue"}, Args: "[number, id, name or words]", Summary: "Open another session of this folder in place of this one, which is kept. Words find the session whose name or first prompt has them.", Run: l.slashResume},
		{Name: "fork", Args: "<prompt>", Summary: "Copy the conversation into a new session that works on the prompt in the background, as a run of its own, while this one goes on here.", Run: l.slashFork},
		{Name: "subtask", Args: "<task>", Summary: "Copy the conversation into a run in the background that works on the task while this session goes on; what it answers comes back here when it is done.", Run: l.slashSubtask},
		{Name: "branch", Args: "[name]", Summary: "Copy the conversation into a new session and go on in the copy. The original is kept.", Run: l.slashBranch},
		{Name: "rewind", Aliases: []string{"checkpoint", "undo"}, Args: "[number [all|files]]", Summary: "Take the conversation back to before one of your prompts; with all, the files Write and Edit changed too; with files, the files alone.", Run: l.slashRewind},
		{Name: "plan", Args: "[task]", Summary: "Switch to plan mode: the model looks and plans, and changes nothing. With a task after it, starts on it.", Run: l.slashPlan},
		{Name: "add-dir", Args: "<directory>", Summary: "Add a working directory for the rest of this session. With nothing after it, lists them.", Run: l.slashAddDir},
		{Name: "cd", Args: "<folder>", Summary: "Move this session to another folder, keeping the conversation: from then on that folder's own settings, instructions and skills are in force, and gnosh -c there goes on with it.", Run: l.slashCd},
		{Name: "panel", Aliases: []string{"sidebar"}, Summary: "Show or hide the side panel: the files this session changed and its deliverables, beside the conversation, on the whole screen.",
			Run: func(context.Context, *SlashContext) error {
				return errors.New("the side panel is the interactive interface's own; /panel does nothing here")
			}},
		{Name: "tasks", Aliases: []string{"bashes"}, Args: "[stop <id>]", Summary: "The commands running in the background and the ones that ran, and the sessions sent to the background; stop <id> stops a command.", InRun: true, Run: l.slashTasks},
		{Name: "cost", Aliases: []string{"usage", "stats"}, Summary: "What this session has used: requests, tokens and time, by model.", InRun: true, Run: l.slashCost},
		{Name: "agents", Summary: "The kinds of sub-agent there are, and where each is defined.", InRun: true, Run: l.slashAgents},
		{Name: "memory", Summary: "The context files the model is given (GNOSH.md, AGENTS.md, CLAUDE.md), and where to put one.", InRun: true, Run: l.slashMemory},
		{Name: "mcp", Args: "[reconnect|enable|disable <server>|all] [login|logout <server>]", Summary: "The MCP servers: where each is configured, whether it is connected, and its tools; or reconnect, enable or disable one, or all; or sign in to one at a URL, or out.", InRun: true, Run: l.slashMCP},
		{Name: "diff", Summary: "What has changed in the working tree since the last commit, the model's edits among it.", InRun: true, Run: l.slashDiff},
		{Name: "export", Args: "[file | --clipboard]", Summary: "Write the conversation to a file as plain text: the name given, or <date>-<session>.txt here; --clipboard puts it on the clipboard instead.", Run: l.slashExport},
		{Name: "copy", Args: "[N]", Summary: "Copy the model's last reply, or the Nth from the end, to the clipboard.", Run: l.slashCopy},
		{Name: "review", Aliases: []string{"code-review"}, Args: "[low|medium|high|xhigh|max] [--fix] [what to look at]", Summary: "Have the model review the changes on this branch for bugs, changing nothing; with --fix it fixes the ones it is sure of.", InRun: true, Run: l.slashReview},
		{Name: "security-review", Args: "[what to look at]", Summary: "Have the model review the changes on this branch for security vulnerabilities, changing nothing.", InRun: true, Run: l.slashSecurityReview},
		{Name: "simplify", Args: "[what to look at]", Summary: "Have the model make the changes on this branch simpler (reuse, fewer parts, less work, the right place), the code doing what it did.", InRun: true, Run: l.slashSimplify},
		{Name: "init", Summary: "Have the model look through the project and write its GNOSH.md, or improve the one there is.", Run: l.slashInit},
		{Name: "debug", Args: "[what is wrong]", Summary: "Turn debug logging on for the rest of the session, and have the model find out from gnosh's log what is going wrong.", InRun: true, Run: l.slashDebug},
		{Name: "doctor", Args: "[hosts]", Summary: "Check that a turn can run: config, providers, models, hooks. With hosts after it, each declared host is logged in to as well.", InRun: true,
			Run: func(ctx context.Context, c *SlashContext) error {
				// The declared hosts are listed, and reached only when the
				// person says so.
				tryHosts := strings.TrimSpace(c.Args) == "hosts"
				if !tryHosts && strings.TrimSpace(c.Args) != "" {
					return fmt.Errorf("/doctor takes nothing after it, or the word hosts, which also logs in to each declared host; %q is neither", oneLine(c.Args, 80))
				}
				findings := append(l.p.doctor(ctx, l.cfg, l.st.problems, tryHosts), defFindings(l.cfg, l.st.trusted)...)
				for _, f := range findings {
					c.Say("%s", f.String())
				}
				if !config.CanRun(findings) {
					return errors.New("a turn cannot run until what is marked FAIL is put right")
				}
				c.Say("A turn can run.")
				return nil
			}},
	}
}

// help lists the commands.
func (s *slashSet) help(ctx context.Context, c *SlashContext) error {
	c.Say("The commands of this session:")
	for _, cmd := range s.cmds {
		if !s.l.interactive && !cmd.InRun {
			continue
		}
		name := "/" + cmd.Name
		if cmd.Args != "" {
			name += " " + cmd.Args
		}
		also := ""
		if len(cmd.Aliases) > 0 {
			also = " (also /" + strings.Join(cmd.Aliases, ", /") + ")"
		}
		c.Say("  %-32s %s%s", name, cmd.Summary, also)
	}
	if s.l.interactive {
		c.Say("Anything else you type is sent to the model. Ctrl+C stops the turn; a second one quits.")
	}
	return nil
}

// modelRef returns the session's large model as provider/id, and whether
// there is one.
func (l *live) modelRef() (config.ModelRef, bool) {
	l.modelMu.Lock()
	defer l.modelMu.Unlock()
	if l.large == nil || l.large.pu == nil {
		return config.ModelRef{}, false
	}
	return l.large.ref, true
}

// saveState keeps a choice made inside gnosh for later sessions, in the
// user's state file. A choice that cannot be saved still holds for this
// session, and the person is told.
func (l *live) saveState(c *SlashContext, change func(*config.State) error) {
	var inner error
	err := config.UpdateState(config.OS{}, l.cfg.Paths.StateFile, func(st *config.State) { inner = change(st) })
	if err == nil {
		err = inner
	}
	if err != nil {
		c.Say("The choice holds for this session, but it could not be saved for later ones: %v", err)
	}
}

// slashThink is /think and /no_think: the model's most or least thinking,
// for every later request, in this session and later ones.
func (l *live) slashThink(c *SlashContext, most bool) error {
	ref, ok := l.modelRef()
	if !ok {
		return errors.New("this session has no model, so there is no thinking level to set. Choose a model with /model")
	}
	var level string
	if most {
		level, ok = c.Agent.Think()
	} else {
		level, ok = c.Agent.NoThink()
	}
	if !ok {
		c.Say("gnosh knows of no thinking switch for %s, so nothing changed. If the model has levels, declare them in gnosh.json: \"models\": {%q: {\"thinkingLevels\": [\"low\", \"medium\", \"high\"]}}", ref, ref.String())
		return nil
	}
	l.saveState(c, func(st *config.State) error { return st.SetThinking(ref, level) })
	levels := c.Agent.Model().Levels
	c.Say("Thinking is %s for %s, from the next request on, in this session and in later ones.", level, ref)
	if len(levels) > 2 {
		c.Say("Its levels are %s; /model %s <level> sets one in between.", strings.Join(levels, ", "), ref)
	}
	return nil
}

// btwTokens caps the answer to a side question, and btwWait is how long
// one may take, the wait for a running request of the session's own
// included.
const (
	btwTokens = 4096
	btwWait   = 10 * time.Minute
)

// slashBtw is /btw: a side question about the conversation, asked of the
// session's own model beside the work. The request is the session's next
// one with the question after it (agent.SideRequest.Session), so that a
// model server that kept the conversation reads only the question; nothing
// of it is kept, and a turn that is running goes on meanwhile. The answer
// is the command's output.
func (l *live) slashBtw(ctx context.Context, c *SlashContext) error {
	q := strings.TrimSpace(c.Args)
	if q == "" {
		return errors.New("/btw takes a question after it, such as /btw which test was failing?")
	}
	res, err := c.Agent.Side(ctx, agent.SideRequest{
		Purpose: "btw", Session: true, Prompt: l.texts.Btw() + "\n\n" + q,
		MaxTokens: btwTokens, Timeout: btwWait,
	})
	switch {
	case errors.Is(err, agent.ErrCutOff) && res.Text != "":
		res.Text += "\n(The answer was cut off at its length limit.)"
	case err != nil:
		return fmt.Errorf("the side question was not answered: %w", err)
	}
	for _, line := range strings.Split(res.Text, "\n") {
		if strings.TrimSpace(line) != "" {
			c.Say("%s", line)
		}
	}
	return nil
}

// slashRecap is /recap: the session in one line, from the session's own
// model, asked as /btw asks (beside the work, nothing kept).
func (l *live) slashRecap(ctx context.Context, c *SlashContext) error {
	if !slices.ContainsFunc(l.ts.Entries(), func(e transcript.Entry) bool { return e.Type == transcript.TypeUser }) {
		c.Say("There is nothing to recap yet: the conversation has not begun.")
		return nil
	}
	res, err := c.Agent.Side(ctx, agent.SideRequest{Purpose: "recap", Session: true, Prompt: l.texts.Recap(), MaxTokens: 512, Timeout: btwWait})
	if err != nil {
		return fmt.Errorf("no recap was made: %w", err)
	}
	for _, line := range strings.Split(res.Text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			c.Say("%s", line)
			break
		}
	}
	return nil
}

// slashEffort is /effort, Claude Code's name for the thinking level. With
// nothing after it, or status, it says the level in force and the model's
// levels; with one of them, or max for the most, it sets that one; with
// auto it goes back to the model's own default. What it sets is saved for
// later sessions, as /think's is.
func (l *live) slashEffort(ctx context.Context, c *SlashContext) error {
	ref, ok := l.modelRef()
	if !ok {
		return errors.New("this session has no model, so there is no thinking level. Choose a model with /model")
	}
	m := c.Agent.Model()
	word := strings.ToLower(strings.TrimSpace(c.Args))
	switch word {
	case "", "status":
		if len(m.Levels) == 0 {
			c.Say("gnosh knows of no thinking levels for %s. If the model has them, declare them in gnosh.json: \"models\": {%q: {\"thinkingLevels\": [\"low\", \"medium\", \"high\"]}}", ref, ref.String())
			return nil
		}
		c.Say("Thinking for %s: %s. Its levels are %s; /effort and one of them sets it, /effort auto goes back to the model's own default.",
			ref, firstOf(m.Level, "the model's own default"), strings.Join(m.Levels, ", "))
		return nil
	case "auto":
		if _, err := c.Agent.SetThinking(""); err != nil {
			return err
		}
		l.saveState(c, func(st *config.State) error { return st.SetThinking(ref, "") })
		c.Say("Thinking for %s is the model's own default, from the next request on, in this session and in later ones.", ref)
		return nil
	case "max":
		if len(m.Levels) > 0 {
			word = m.Levels[len(m.Levels)-1]
		}
	}
	switch {
	case len(m.Levels) == 0:
		return fmt.Errorf("gnosh knows of no thinking levels for %s, so %q cannot be set. Nothing changed", ref, word)
	case !slices.Contains(m.Levels, word):
		return fmt.Errorf("%q is not one of the thinking levels of %s (%s). Nothing changed", word, ref, strings.Join(m.Levels, ", "))
	}
	if _, err := c.Agent.SetThinking(word); err != nil {
		return err
	}
	l.saveState(c, func(st *config.State) error { return st.SetThinking(ref, word) })
	c.Say("Thinking is %s for %s, from the next request on, in this session and in later ones.", word, ref)
	return nil
}

// slashCompact is /compact: a summary of the conversation now. In a
// session a person attends it is asked for and reported as it happens; as
// the prompt of a run it is waited for, and how it went is the result.
func (l *live) slashCompact(ctx context.Context, c *SlashContext) error {
	l.lastSummary.Store(nil)
	if err := c.Agent.Compact(c.Args); err != nil {
		return fmt.Errorf("no summary was asked for: %w", err)
	}
	if l.interactive {
		if c.Agent.State().Turn != "" {
			c.Say("A summary will be made when the turn that is running has ended.")
		}
		return nil
	}
	if err := c.Agent.Wait(ctx); err != nil {
		return err
	}
	if err := c.Agent.Drain(ctx); err != nil {
		return err
	}
	ev := l.lastSummary.Load()
	switch {
	case ev == nil:
		return errors.New("no summary was made: see the lines above for why")
	case ev.Err != nil:
		return fmt.Errorf("no summary was made: %w", ev.Err)
	}
	c.Say("The conversation was summarized: about %s tokens before, about %s after.", tokenCount(ev.PreTokens), tokenCount(ev.PostTokens))
	return nil
}

// slashLoop is /loop.
func (l *live) slashLoop(ctx context.Context, c *SlashContext) error {
	if c.Args != "" {
		return l.slashLoopEvery(c)
	}
	if _, ok := l.modelRef(); !ok {
		return errors.New("this session has no model, so there is nothing to keep working. Choose a model with /model")
	}
	if c.Agent.Loop() {
		c.Say("/loop is already on. /noloop turns it off.")
		return nil
	}
	c.Agent.SetLoop(true)
	return nil
}

// slashModel is /model: with nothing after it, the models there are; with
// a name, a switch to that model, saved for later sessions.
func (l *live) slashModel(ctx context.Context, c *SlashContext) error {
	fields := strings.Fields(c.Args)
	if len(fields) > 2 {
		return errors.New("/model takes a model, as provider/model, and then at most a thinking level")
	}
	l.switchMu.Lock()
	defer l.switchMu.Unlock()
	if len(fields) == 0 {
		return l.listModelsFor(ctx, c)
	}

	name, level := fields[0], ""
	if len(fields) == 2 {
		level = fields[1]
	}
	ref := config.ModelRef{ID: name}
	if prov, id, ok := strings.Cut(name, "/"); ok {
		if _, known := l.cfg.Provider(prov); known {
			ref = config.ModelRef{Provider: prov, ID: id}
		}
	}
	ref, err := l.resolveRef(ctx, ref, true, nil)
	if err != nil {
		return err
	}
	// The thinking level saved for this model, if any, comes back with it.
	slot := l.cfg.Large
	slot.Thinking = config.Value[string]{}
	if st, probs := config.ReadState(config.OS{}, l.cfg.Paths.StateFile); len(probs) == 0 {
		if saved, ok := st.Thinking[ref.String()]; ok {
			slot.Thinking = config.Value[string]{V: saved, Set: true, Origin: config.Origin{Layer: config.LayerState, File: l.cfg.Paths.StateFile}}
		}
	}
	mu, err := l.buildModel(ctx, ref, slot, true)
	if err != nil {
		return err
	}
	if level != "" {
		if !slices.Contains(mu.model.Levels, level) {
			if len(mu.model.Levels) == 0 {
				return fmt.Errorf("gnosh knows of no thinking levels for %s, so %q cannot be set. Nothing changed", ref, level)
			}
			return fmt.Errorf("%q is not one of the thinking levels of %s (%s). Nothing changed", level, ref, strings.Join(mu.model.Levels, ", "))
		}
		mu.model.Level = level
	}
	from := c.Agent.Model().Info.ID
	if fromRef, ok := l.modelRef(); ok {
		from = fromRef.String()
	}
	// The hooks may take their time, and may ask the person: the lock is
	// let go meanwhile, so that what else needs the model (a sub-agent
	// asking for its own) does not wait on them. A switch made meanwhile
	// stands, and this one is not made over it.
	l.switchMu.Unlock()
	err = l.beforeModelSwitch(ctx, c, from, ref.String(), name)
	l.switchMu.Lock()
	if err != nil {
		return err
	}
	now := c.Agent.Model().Info.ID
	if nowRef, ok := l.modelRef(); ok {
		now = nowRef.String()
	}
	if now != from {
		return fmt.Errorf("the model was not changed to %s: it became %s while the PreModelSwitch hooks ran", ref, now)
	}
	if err := c.Agent.SetModel(mu.model); err != nil {
		return fmt.Errorf("the model was not changed: %w", err)
	}
	l.modelMu.Lock()
	l.large = mu
	l.modelMu.Unlock()
	l.modelSwitched(ctx, from, ref.String(), name)
	l.saveState(c, func(st *config.State) error {
		if err := st.SetModel("model", ref); err != nil {
			return err
		}
		if level != "" {
			return st.SetThinking(ref, level)
		}
		return nil
	})
	thinking := ""
	if mu.model.Level != "" {
		thinking = ", thinking " + mu.model.Level
	}
	window := "its context window is not known yet"
	if w := mu.model.Info.ContextWindow; w > 0 {
		window = "context window " + tokenCount(w) + " tokens"
	}
	c.Say("The model is %s at %s (%s%s), from the next request on. The choice is saved for later sessions; -m names another for one run.", ref, mu.model.Address, window, thinking)
	return nil
}

// listModelsFor lists the models of every provider that can be used, and
// says which providers were left out and why.
func (l *live) listModelsFor(ctx context.Context, c *SlashContext) error {
	cur, has := l.modelRef()
	m := c.Agent.Model()
	if has {
		thinking := ""
		if m.Level != "" {
			thinking = ", thinking " + m.Level
		}
		c.Say("In use: %s at %s%s.", cur, m.Address, thinking)
	} else {
		c.Say("No model is in use.")
	}
	usable := l.cfg.Usable()
	for _, pc := range usable {
		ids, note := l.offered(ctx, pc)
		where := ""
		if pu := l.providerUsed(pc.ID); pu != nil {
			where = " at " + pu.endpoint.Display
		}
		c.Say("%s%s:", pc.ID, where)
		for _, id := range ids {
			mark := "  "
			if has && cur.Provider == pc.ID && cur.ID == id {
				mark = "* "
			}
			line := "  " + mark + pc.ID + "/" + id
			if cm, ok := l.cfg.Model(config.ModelRef{Provider: pc.ID, ID: id}); ok && cm.ContextWindow.Set {
				line += "  (window " + tokenCount(cm.ContextWindow.V) + ")"
			}
			if levels := thinkingLevelsOf(l.cfg, pc, id); len(levels) > 0 {
				line += "  thinking: " + strings.Join(levels, ", ")
			}
			c.Say("%s", line)
		}
		if len(ids) == 0 {
			c.Say("    (no model is known: none is registered under \"models\" in gnosh.json, and the server lists none)")
		}
		if note != "" {
			c.Say("    note: %s", note)
		}
	}
	for _, pc := range l.cfg.Providers {
		switch {
		case pc.Disabled:
			c.Say("%s: disabled (%s).", pc.ID, pc.Origin)
		case pc.LeftOut != "":
			c.Say("%s: left out: %s.", pc.ID, pc.LeftOut)
		}
	}
	if len(usable) == 0 {
		c.Say("No provider can be used. Add one to %s, for example: {%s}", l.cfg.Paths.UserSettings, config.ProviderExample)
	}
	c.Say("To switch: /model provider/model, and a thinking level after it if you want one. The choice is saved.")
	return nil
}

// thinkingLevelsOf returns the thinking levels of a model of a provider.
func thinkingLevelsOf(cfg *config.Config, pc config.Provider, id string) []string {
	cm, registered := cfg.Model(config.ModelRef{Provider: pc.ID, ID: id})
	return thinkingLevels(pc, cm, registered, id)
}

// slashStatus is /status: what the session is and what it is doing right
// now, and then every setting with where it came from.
func (l *live) slashStatus(ctx context.Context, c *SlashContext) error {
	a := c.Agent
	c.Say("gnosh %s", l.p.versionLine())
	c.Say("session      %s (%s)", l.ts.ID(), l.source)
	c.Say("file         %s", l.ts.Path())
	c.Say("folder       %s on %s", l.cwd(), hostName())
	trust := firstOf(l.st.decision.Reason, "not trusted")
	if l.st.trusted && !l.st.decision.Trusted {
		trust = "trusted: you answered yes when this session asked"
	}
	c.Say("project      %s (%s)", l.cfg.Paths.ProjectRoot, trust)
	if def := l.asAgent; def != nil {
		c.Say("agent        %s (%s): its prompt, tools and model", def.Name, firstOf(l.tilde(def.Source), def.Scope))
	}

	m := a.Model()
	if ref, ok := l.modelRef(); ok {
		origin := l.cfg.Large.Model.Origin.String()
		if !l.cfg.Large.Model.Set || l.cfg.Large.Model.V != ref {
			origin = "chosen in this session"
		}
		c.Say("model        %s at %s (%s)", ref, m.Address, origin)
		level := firstOf(m.Level, "the model's own default")
		if len(m.Levels) == 0 {
			level = "no switch known for this model"
		}
		if m.ThinkingBudget > 0 && m.Level != "off" {
			// (thinkingBudget, MAX_THINKING_TOKENS: the server ends the
			// thinking there.)
			level += fmt.Sprintf(", at most %d tokens before a reply", m.ThinkingBudget)
		}
		c.Say("thinking     %s", level)
	} else {
		c.Say("model        none: prompts fail until one is chosen with /model")
	}
	if l.small != nil {
		c.Say("small model  %s at %s (the short side jobs)", l.small.ref, l.small.model.Address)
	}
	c.Say("permissions  mode %s", l.engine.Mode())
	if color := l.sessionColor(); color != "" {
		c.Say("color        %s (/color)", color)
	}
	// The machines commands run on: this one as results name it, and the
	// declared hosts.
	c.Say("machine      %s (%s)", shelltool.LocalName, firstOf(l.p.whoAndWhere(), "its name is not known"))
	for i, h := range l.currentHosts() {
		label := "hosts"
		if i > 0 {
			label = ""
		}
		about := ""
		if h.Description != "" {
			about = ": " + h.Description
		}
		c.Say("%-12s %s (%s)%s", label, h.Name, h.SSH, about)
	}

	cs := a.Context()
	if cs.Window > 0 {
		known := "learned from the server or the config"
		if !cs.Known {
			known = "assumed: nobody could say how large it is"
		}
		c.Say("context      about %s of %s tokens (%d%%); the window is %s; room is made at %s",
			tokenCount(cs.Used), tokenCount(cs.Window), cs.Used*100/cs.Window, known, tokenCount(cs.Point))
		if cs.AutoOff {
			c.Say("             automatic summaries are off for this session: one did not help. /compact makes one by hand.")
		}
	}
	st := a.State()
	switch {
	case !st.Busy:
		c.Say("state        idle: waiting for your prompt")
	default:
		line := string(st.Phase)
		if st.Waiting != "" {
			line += ": " + st.Waiting
		}
		if !st.Since.IsZero() {
			line += fmt.Sprintf(" (for %s)", l.p.now().Sub(st.Since).Round(time.Second))
		}
		c.Say("state        %s", line)
		for _, r := range st.Running {
			c.Say("             running %s for %s", r.Name, l.p.now().Sub(r.Since).Round(time.Second))
		}
	}
	if st.Queued > 0 {
		c.Say("queued       %d prompt(s) wait for their turn", st.Queued)
	}
	if st.Loop {
		c.Say("/loop        on")
	}
	facts := l.statusFacts()
	for _, name := range []string{"frame", "goal"} {
		if v := facts[name]; v != "" {
			c.Say("%-12s %s", name, v)
		}
	}
	if tasks := l.taskList(); len(tasks) > 0 {
		for i, line := range taskLines(tasks) {
			if i == 0 {
				c.Say("tasks        %s", line)
			} else {
				c.Say("             %s", line)
			}
		}
	}
	if counts := DeliverableCount(l.deliverables(), ", "); counts != "" {
		c.Say("deliverables %s (/deliverables lists them)", counts)
	}
	if l.shell != nil {
		running := 0
		for _, t := range l.shell.Tasks() {
			if t.Running {
				running++
				c.Say("background   %s: %s (running for %s, output in %s)", t.ID, oneLine(t.Command, 80), t.Duration.Round(time.Second), t.OutputFile)
			}
		}
		if running == 0 {
			c.Say("background   no command is running in the background")
		}
	}
	c.Say("tools        %s", firstOf(strings.Join(l.toolNames(), ", "), "none"))
	if slices.Contains(l.toolNames(), agentToolName) {
		var kinds []string
		for _, d := range l.agentDefs().All() {
			if d.Scope == "built-in" {
				kinds = append(kinds, d.Name)
			} else {
				kinds = append(kinds, d.Name+" ("+d.Source+")")
			}
		}
		l.subMu.Lock()
		running, started := len(l.subs), l.agentCalls
		l.subMu.Unlock()
		c.Say("sub-agents   kinds: %s; %d started in this session, %d running now", strings.Join(kinds, ", "), started, running)
	}
	if !l.skillsOff() {
		var names []string
		for _, sk := range l.skills().All() {
			names = append(names, sk.Name+" ("+sk.Source+")")
		}
		c.Say("skills       %s", firstOf(strings.Join(names, ", "), "none; /skills says where they go"))
	}
	for i, line := range l.switchedOff(m) {
		label := "off"
		if i > 0 {
			label = ""
		}
		c.Say("%-12s %s", label, line)
	}
	for i, line := range l.unusedFiles() {
		label := "not used"
		if i > 0 {
			label = ""
		}
		c.Say("%-12s %s", label, line)
	}
	// What the configuration was warned of as the session started: a
	// setting that does nothing, a hook whose matcher fits nothing (Claude
	// Code 2.1.327's /status lists these too).
	if l.st != nil {
		for i, pr := range config.Warnings(l.st.problems) {
			label := "warnings"
			if i > 0 {
				label = ""
			}
			c.Say("%-12s %s", label, oneLine(pr.String(), 400))
		}
	}
	for _, j := range l.contribs {
		if j.f.Name != "" {
			c.Say("feature      %s", j.f.Name)
		}
	}
	u := a.Usage()
	c.Say("usage        %d requests; %s tokens in, %s out (/cost says more)", u.Requests, tokenCount(u.Main.PromptTokens()), tokenCount(u.Main.OutputTokens))
	c.Say("The configuration in force, each setting with where it came from:")
	c.Say("%s", strings.TrimRight(l.cfg.String(), "\n"))
	return nil
}

// unusedFiles are the agent and skill files this session found and does
// not use, each with why, for /status (Claude Code 2.1.327 lists its
// broken agent, skill and command files there).
func (l *live) unusedFiles() []string {
	var out []string
	for _, sk := range l.agentDefs().Skipped() {
		out = append(out, fmt.Sprintf("%s: no kind of sub-agent, %s", l.tilde(sk.Path), oneLine(sk.Reason, 200)))
	}
	if l.skillsOff() {
		return out
	}
	set := l.skills()
	for _, sk := range set.Skipped() {
		out = append(out, fmt.Sprintf("%s: no skill, %s", l.tilde(sk.Path), oneLine(sk.Reason, 200)))
	}
	for _, sk := range set.All() {
		if len(sk.Unbuilt) > 0 {
			out = append(out, fmt.Sprintf("%s: the skill %s asks for %s, which this gnosh cannot run", l.tilde(sk.Source), sk.Name, strings.Join(sk.Unbuilt, ", ")))
		}
	}
	return out
}

// switchedOff are the features of gnosh that are off in this session, each
// with what switched it off and what turns it on, for /status (Claude Code
// 2.1.326 has a row of the features its provider or variables turned off).
func (l *live) switchedOff(m agent.Model) []string {
	var off []string
	if strings.TrimSpace(l.cfg.Options.WebSearchURL.V) == "" {
		off = append(off, "WebSearch, until webSearchUrl names a SearXNG server")
	}
	if strings.TrimSpace(l.cfg.Options.MemEmbeddingsURL.V) == "" {
		off = append(off, "mem's search by meaning, until memEmbeddingsUrl names an embedding server: it matches words")
	}
	if !l.suggestOn.Load() {
		off = append(off, "prompt suggestions, until promptSuggestionEnabled is on")
	}
	if d, ok := m.Provider.(core.ImageDropper); ok && d.LeavesOutImages(m.Info.ID) {
		off = append(off, "images: the model's server cannot take them, so each is sent as a line saying it was left out")
	}
	if !l.cfg.Options.AutoCompactEnabled.V {
		off = append(off, "automatic summaries, until autoCompactEnabled is on: /compact makes one by hand")
	}
	if l.cfg.HooksOff != "" {
		off = append(off, "hooks ("+l.cfg.HooksOff+")")
	}
	if l.cfg.StatusLineOff != "" {
		off = append(off, "the status line ("+l.cfg.StatusLineOff+")")
	}
	if l.cfg.FileSuggestionOff != "" {
		off = append(off, "the fileSuggestion command, so @ offers gnosh's own search ("+l.cfg.FileSuggestionOff+")")
	}
	return off
}

// slashPermissions is /permissions: the mode, the folders and every rule,
// each with where it came from. It only shows; rules are changed in the
// settings files.
func (l *live) slashPermissions(ctx context.Context, c *SlashContext) error {
	if verb, rule, ok := strings.Cut(strings.TrimSpace(c.Args), " "); ok || verb != "" {
		l.permissionsAct(c, verb, strings.TrimSpace(rule))
		return nil
	}
	x := l.engine.Explain()
	c.Say("Permission mode: %s", x.Mode)
	if x.Trusted {
		c.Say("The folder is trusted: its own rules are in force.")
	} else {
		c.Say("The folder is not trusted: of its own rules, only the ones that restrict are in force.")
	}
	c.Say("Folders the tools work in without a question:")
	for _, d := range x.Dirs {
		c.Say("  %s", d)
	}
	for _, d := range x.HeldDirs {
		c.Say("  %s  (waits for the folder to be trusted)", d)
	}
	for _, list := range []permission.List{permission.ListDeny, permission.ListAsk, permission.ListAllow} {
		var lines []string
		for _, r := range x.Rules {
			if r.List != list {
				continue
			}
			held := ""
			if r.Held {
				held = "  (waits for the folder to be trusted)"
			}
			lines = append(lines, fmt.Sprintf("  %s    from %s%s", r.Rule, r.Source, held))
		}
		if len(lines) == 0 {
			c.Say("No %s rules.", list)
			continue
		}
		c.Say("%s rules, in the order they are looked at:", strings.ToUpper(string(list)[:1])+string(list)[1:])
		for _, line := range lines {
			c.Say("%s", line)
		}
	}
	for _, n := range x.Notes {
		c.Say("note: %s", n)
	}
	c.Say("Rules are written in the settings files; an approval you save goes to %s, and so does /permissions allow, ask or deny <rule>. /permissions remove <rule> takes one out.", l.cfg.Paths.LocalSettings)
	return nil
}

// permissionsAct is /permissions allow|ask|deny <rule>, which puts the
// rule in force and in the project's local settings file, and /permissions
// remove <rule>, which takes it out of both, as Claude Code's /permissions
// does. A rule in another file is named and left: gnosh's own files may
// hold the person's comments, which a rewrite would lose.
func (l *live) permissionsAct(c *SlashContext, verb, rule string) {
	const usage = "/permissions takes allow, ask, deny or remove, and a rule: /permissions deny Bash(rm *), say. With nothing after it, it lists the rules."
	local := l.cfg.Paths.LocalSettings
	if n := len(rule); n >= 2 && (rule[0] == '"' || rule[0] == '\'') && rule[n-1] == rule[0] {
		rule = rule[1 : n-1]
	}
	switch verb {
	case "allow", "ask", "deny":
		if rule == "" {
			c.Say("%s", usage)
			return
		}
		list := permission.List(verb)
		added, err := l.engine.Keep(rule, list)
		if err != nil {
			c.Say("%s was not added: %v.", rule, strings.TrimRight(err.Error(), "."))
			return
		}
		err = l.editLocalRules(func(lists map[string][]string) {
			if !slices.Contains(lists[verb], rule) {
				lists[verb] = append(lists[verb], rule)
			}
		})
		if err != nil {
			if added {
				// (Only what this command put in force is taken back: a rule
				// that was in force already stays.)
				l.engine.Unkeep(rule, list)
			}
			c.Say("%s was not added: %v.", rule, strings.TrimRight(err.Error(), "."))
			return
		}
		c.Say("The %s rule %s is in force, and kept in %s.", verb, rule, local)
	case "remove", "rm", "delete":
		// remove [allow|ask|deny] <rule>: with a list named, the rule is
		// taken out of that list alone (the picker names it), so that an
		// ask rule beside an allow rule of the same words stays.
		only := ""
		if w, rest, ok := strings.Cut(rule, " "); ok && (w == "allow" || w == "ask" || w == "deny") {
			only, rule = w, strings.TrimSpace(rest)
			if n := len(rule); n >= 2 && (rule[0] == '"' || rule[0] == '\'') && rule[n-1] == rule[0] {
				rule = rule[1 : n-1]
			}
		}
		if rule == "" {
			c.Say("%s", usage)
			return
		}
		want := rule
		if r, err := permission.ParseRule(rule); err == nil {
			want = r.String()
		}
		var found bool
		var elsewhere []string
		done := map[permission.List]bool{}
		for _, r := range l.engine.Explain().Rules {
			if r.Rule != want || only != "" && string(r.List) != only {
				continue
			}
			found = true
			switch r.Source {
			case permission.KeptLayer, permission.SessionLayer, local:
				if done[r.List] {
					continue
				}
				done[r.List] = true
				l.engine.Drop(want, r.List, local)
				err := l.editLocalRules(func(lists map[string][]string) {
					lists[string(r.List)] = slices.DeleteFunc(lists[string(r.List)], func(s string) bool {
						p, err := permission.ParseRule(s)
						return s == rule || err == nil && p.String() == want
					})
				})
				if err != nil {
					c.Say("The %s rule %s is out of force for this session, and could not be taken out of %s: %v.", r.List, want, local, strings.TrimRight(err.Error(), "."))
					continue
				}
				c.Say("The %s rule %s is taken out.", r.List, want)
			default:
				elsewhere = append(elsewhere, fmt.Sprintf("the %s rule %s comes from %s", r.List, want, r.Source))
			}
		}
		switch {
		case !found:
			c.Say("No rule %s is in force; /permissions lists them.", want)
		case len(elsewhere) > 0:
			c.Say("%s, which gnosh does not write (it may hold your comments): take it out there, and it goes when gnosh starts next.", strings.Join(elsewhere, "; "))
		}
	default:
		c.Say("%s", usage)
	}
}

// slashHooks is /hooks: every hook in force, by the moment it runs at.
func (l *live) slashHooks(ctx context.Context, c *SlashContext) error {
	switch {
	case l.cfg.Switches.SafeMode.V:
		c.Say("Safe mode is on, so no hook runs.")
	case l.cfg.HooksOff != "":
		c.Say("No hook runs: %s.", l.cfg.HooksOff)
	}
	n := 0
	for _, e := range hooks.Events() {
		list := l.hooks.r.Hooks(e)
		if len(list) == 0 {
			continue
		}
		c.Say("%s:", e)
		for _, h := range list {
			n++
			line := "  "
			if h.Name != "" {
				line += h.Name + ": "
			}
			line += oneLine(h.CommandLine(), 200)
			if h.Matcher != "" && h.Matcher != "*" {
				line += "  [matches " + h.Matcher + "]"
			}
			if h.If != "" {
				line += "  [if " + h.If + "]"
			}
			if h.Timeout > 0 {
				line += "  [timeout " + h.Timeout.String() + "]"
			}
			c.Say("%s", line)
			c.Say("      from %s", h.Source)
		}
	}
	if n == 0 {
		c.Say("No hook is in force in this session.")
	}
	if note := l.hooksHeld(n); note != "" {
		c.Say("%s", note)
	}
	return nil
}

// summaryEnd keeps how the last summary went, for /compact as the prompt
// of a run.
func (l *live) summaryEnd(ev any) {
	if s, ok := ev.(*agent.SummaryEvent); ok && s.Phase == "end" {
		l.lastSummary.Store(s)
	}
}
