package cli

import (
	"fmt"
	"strings"
)

// The ids of the flags that the code outside the table asks about by name.
const (
	idHelp      = "--help"
	idVersion   = "--version"
	idPrint     = "--print"
	idContinue  = "--continue"
	idResume    = "--resume"
	idSessionID = "--session-id"
	idFork      = "--fork-session"
	idNoPersist = "--no-session-persistence"
	idYolo      = "--yolo"
	idMode      = "--permission-mode"
	idBack      = "--bg"
)

// The headings flags are listed under in help, in the order they appear.
const (
	groupConversation = "Which conversation"
	groupModel        = "Which model"
	groupPermissions  = "Permissions"
	groupTold         = "What the model is told"
	groupSettings     = "Settings for this run"
	groupPrint        = "Print mode"
	groupOther        = "Help and version"
	groupOwn          = "Flags"
)

// groupOrder is the order of the headings.
var groupOrder = []string{
	groupConversation, groupModel, groupPermissions, groupTold,
	groupSettings, groupPrint, groupOwn, groupOther,
}

// add puts a flag in the table: it takes the spellings apart, names the
// flag after its main spelling unless it has an id, and marks it built if
// the specs call it P0. A value of a flag is built only if both the flag
// and the value are P0.
func (t *Table) add(f *flag) *flag {
	for _, n := range strings.Fields(f.names) {
		if strings.HasPrefix(n, "--") {
			f.long = append(f.long, n[2:])
		} else {
			f.short = append(f.short, []rune(n[1:])...)
		}
	}
	if f.id == "" {
		f.id = f.main()
	}
	if f.group == "" {
		f.group = groupOwn
	}
	if f.pri == 0 {
		f.status = Built
	}
	for i := range f.choices {
		c := &f.choices[i]
		c.pri = max(c.pri, f.pri)
		if c.pri == 0 {
			c.status = Built
		}
	}
	t.flags = append(t.flags, f)
	return f
}

// command puts a command in the table under its parent, and marks it built
// if the specs call it P0.
func (t *Table) command(parent *command, c *command) *command {
	c.parent = parent
	if parent != nil {
		parent.subs = append(parent.subs, c)
	}
	if c.pri == 0 {
		c.status = Built
	}
	t.commands = append(t.commands, c)
	return c
}

// Default returns gnosh's table of commands and flags as it ships: the
// commands and flags of the specs that gnosh has built, and the ones a
// feature of it brings (mem, --goal). What the specs call P0 is marked
// Built and the rest NotBuilt; the code that wires a part in marks it with
// SetStatus. Each call returns a table of its own.
//
// A command or a flag whose part is not built is not in the table at all
// (spec/DECISIONS.md, 26): typed, it is an unknown flag, an unknown
// command of its group, or, as the first word of a line, a prompt. It
// gets its row when it is built.
//
// To add a flag, add a row here and a field to Invocation; to add a
// command, add a row and a Command. Nothing else needs to know.
func Default() *Table {
	t := &Table{}

	// ------------------------------------------------------------------
	// The flags of a session: `gnosh` takes all of them.
	// ------------------------------------------------------------------

	cont := t.add(&flag{names: "-c -C --continue", group: groupConversation, pri: 0,
		help:       "Continue the most recent session of this project. (-c used to take the working folder, which is --cwd now. So a single word right after -c is refused: write -C before a one-word prompt.)",
		refuseNext: wordAfterC})
	resume := t.add(&flag{names: "-r --resume", kind: kindOptional, arg: "session", group: groupConversation, pri: 0,
		help: "Resume a session by its ID or its name, or by words in its name or first prompt when one session alone has them, or from a transcript file given as an absolute path ending in .jsonl. With nothing after it, show the sessions to pick from; in print mode a session must be named. The argument after -r is always taken as the session, never as the prompt.",
		set:  func(i *Invocation, v flagValue) { i.Resume = v.text }})
	sessionID := t.add(&flag{names: "--session-id", kind: kindText, arg: "uuid", group: groupConversation, pri: 0,
		help:  "Give the new conversation this ID, which must be a UUID. It is for a new conversation only: not with -c, -r or --fork-session.",
		check: isUUID,
		set:   func(i *Invocation, v flagValue) { i.SessionID = v.text }})
	back := t.add(&flag{names: "--bg --background", group: groupConversation, pri: 1,
		help: "Start the session in the background and return at once: it works on the prompt as a run of its own, as /background has a session go on, and gnosh says its ID and how to follow it (gnosh agents, gnosh logs, gnosh stop, gnosh -r). A call that needs a yes waits for one in gnosh agents, listed there under Needs input. With -c, or -r and a session's ID or name, it goes on with that session instead of a new one. Not with -p, --fork-session or --no-session-persistence.",
		set:  func(i *Invocation, _ flagValue) { i.Background = true }})
	fork := t.add(&flag{names: "--fork-session", group: groupConversation, pri: 1,
		help: "With -c or -r: go on in a copy of the session and leave the original as it was.",
		set:  func(i *Invocation, _ flagValue) { i.ForkSession = true }})
	name := t.add(&flag{names: "-n --name", kind: kindText, arg: "name", group: groupConversation, pri: 1,
		help: "Name the session: the new one, the one resumed, or the --fork-session copy.",
		set:  func(i *Invocation, v flagValue) { i.Name = v.text }})

	model := t.add(&flag{names: "-m --model", kind: kindText, arg: "model", group: groupModel, pri: 0,
		help: "Use this model for this run only, written as model or provider/model. Nothing is saved.",
		set:  func(i *Invocation, v flagValue) { i.Model = v.text }})
	effort := t.add(&flag{names: "--reasoning-effort --effort", kind: kindText, arg: "level", group: groupModel, pri: 1,
		help: "How hard the model thinks in this run. The levels depend on the model.",
		set:  func(i *Invocation, v flagValue) { i.Effort = v.text }})
	fallback := t.add(&flag{names: "--fallback-model", kind: kindText, arg: "models", group: groupModel, pri: 1,
		help: "Models to go on with, tried in order, when the model cannot answer (its server is down, not ready or too busy): up to three, separated by commas. The switch holds for the rest of the turn; the next one asks the model first again.",
		set:  func(i *Invocation, v flagValue) { i.FallbackModel = v.text }})

	yolo := t.add(&flag{names: "-y --yolo --dangerously-skip-permissions", group: groupPermissions, pri: 0,
		help: "Skip every permission question; the same as --permission-mode bypassPermissions. A print run refuses whatever would have asked, unless it is given this, a permission mode or allow rules."})
	mode := t.add(&flag{names: "--permission-mode", kind: kindText, arg: "mode", group: groupPermissions, pri: 1,
		help: "Start in this permission mode: default (also written manual), plan, acceptEdits, dontAsk or bypassPermissions.",
		choices: []choice{
			{name: ModeDefault}, {name: "manual", means: ModeDefault}, {name: ModePlan},
			{name: ModeAcceptEdits}, {name: ModeDontAsk}, {name: ModeBypassPermissions},
			{name: "auto", refuse: "gnosh has no auto mode (it is Claude Code's, where a second model reviews each action)"},
		},
		set: func(i *Invocation, v flagValue) { i.PermissionMode = v.text }})
	allowSkip := t.add(&flag{names: "--allow-dangerously-skip-permissions", group: groupPermissions, pri: 1,
		help: "Let the session switch to skipping permissions later, with Shift+Tab, without starting that way.",
		set:  func(i *Invocation, _ flagValue) { i.AllowSkipPermissions = true }})
	allowed := t.add(&flag{names: "--allowedTools --allowed-tools", kind: kindList, list: listRules, arg: "rules...", group: groupPermissions, pri: 1,
		help: `Let these run without asking. A rule is a tool's name, or a name with a pattern in parentheses: Read, "Bash(git` + nbsp + `diff` + nbsp + `*)".`,
		set:  func(i *Invocation, v flagValue) { i.AllowedTools = append(i.AllowedTools, v.items...) }})
	disallowed := t.add(&flag{names: "--disallowedTools --disallowed-tools", kind: kindList, list: listRules, arg: "rules...", group: groupPermissions, pri: 1,
		help: "Refuse these. A bare tool name takes the tool away from the model; a rule with a pattern refuses only the calls it matches.",
		set:  func(i *Invocation, v flagValue) { i.DisallowedTools = append(i.DisallowedTools, v.items...) }})
	tools := t.add(&flag{names: "--tools", kind: kindList, list: listRules, arg: "names...", group: groupPermissions, pri: 1, emptyOK: true,
		help: `Give the model only these built-in tools. "" gives it none, and default gives it all of them.`,
		set: func(i *Invocation, v flagValue) {
			i.ToolsGiven = true
			i.Tools = append(i.Tools, v.items...)
		}})
	addDir := t.add(&flag{names: "--add-dir", kind: kindList, list: listPaths, arg: "dirs...", group: groupPermissions, pri: 1,
		help: "Let the agent read files in these other folders without asking. Each must exist.",
		set:  func(i *Invocation, v flagValue) { i.AddDirs = append(i.AddDirs, v.items...) }})
	trustFolder := t.add(&flag{names: "--trust-folder", group: groupPermissions, pri: 1,
		help: "Use this folder's own settings for this one run, without recording the folder as trusted.",
		set:  func(i *Invocation, _ flagValue) { i.TrustFolder = true }})

	system := t.add(&flag{names: "--system-prompt", kind: kindText, arg: "text", group: groupTold, pri: 1, freeText: true,
		help: "Use this text in place of gnosh's own instructions.",
		set:  func(i *Invocation, v flagValue) { i.SystemPrompt = v.text }})
	systemFile := t.add(&flag{names: "--system-prompt-file", kind: kindText, arg: "path", group: groupTold, pri: 1,
		help: "The same, with the text read from a file. Given together with --system-prompt, the file's text comes first.",
		set:  func(i *Invocation, v flagValue) { i.SystemPromptFile = v.text }})
	appendSystem := t.add(&flag{names: "--append-system-prompt", kind: kindText, arg: "text", group: groupTold, pri: 1, freeText: true,
		help: "Add this text to the end of the instructions.",
		set:  func(i *Invocation, v flagValue) { i.AppendSystemPrompt = v.text }})
	appendSystemFile := t.add(&flag{names: "--append-system-prompt-file", kind: kindText, arg: "path", group: groupTold, pri: 1,
		help: "The same, with the text read from a file. Given together with --append-system-prompt, the file's text comes first.",
		set:  func(i *Invocation, v flagValue) { i.AppendSystemPromptFile = v.text }})
	agents := t.add(&flag{names: "--agents", kind: kindText, arg: "json", group: groupTold, pri: 1, freeText: true,
		help: `Kinds of sub-agent for this run, over those of the files and gnosh's own: a JSON object of agents by name, each with "description" and "prompt" and the settings of an agent file ("tools", "model", "maxTurns" ...), or the path of a file that holds one.`,
		set:  func(i *Invocation, v flagValue) { i.Agents = v.text }})
	asAgent := t.add(&flag{names: "--agent", kind: kindText, arg: "name", group: groupTold, pri: 1,
		help: `Run the session as this kind of agent: its prompt in place of gnosh's own, its tools, and its model. It outranks the setting "agent"; a session that ran as an agent runs as it again when it is resumed.`,
		set:  func(i *Invocation, v flagValue) { i.Agent = v.text }})

	cwd := t.add(&flag{names: "--cwd", kind: kindText, arg: "dir", group: groupSettings, pri: 0,
		help: "Work in this folder instead of the current one.",
		set:  func(i *Invocation, v flagValue) { i.Cwd = v.text }})
	worktree := t.add(&flag{names: "--worktree", kind: kindOptional, arg: "name", group: groupSettings, pri: 2,
		help: "Work in a git work tree of the repository's own, in .gnosh/worktrees/<name> on the branch worktree-<name>, made at the commit checked out; one of that name already there is opened again. Without a name, one is made up. A work tree left as it was made is removed when the session ends, unless it was named; any other is kept. The folder must be trusted.",
		set:  func(i *Invocation, v flagValue) { i.Worktree, i.WorktreeName = true, v.text }})
	settings := t.add(&flag{names: "--settings", kind: kindText, arg: "file-or-json", group: groupSettings, pri: 0,
		help: "Extra settings for this run: a file, or the JSON itself. They win over the settings files for the keys they name.",
		set:  func(i *Invocation, v flagValue) { i.Settings = v.text }})
	settingSrc := t.add(&flag{names: "--setting-sources", kind: kindText, arg: "list", group: groupSettings, pri: 1,
		help: "Which settings files to read: any of user, project and local, with commas between.",
		check: func(v string) string {
			_, problem := settingSources(v)
			return problem
		},
		set: func(i *Invocation, v flagValue) { i.SettingSources, _ = settingSources(v.text) }})
	mcpConfig := t.add(&flag{names: "--mcp-config", kind: kindList, list: listPaths, arg: "files-or-json...", group: groupSettings, pri: 1,
		help: "MCP servers for this run, as files or the JSON itself, each in the shape of .mcp.json. A run waits for them before its turn.",
		set:  func(i *Invocation, v flagValue) { i.MCPConfigs = append(i.MCPConfigs, v.items...) }})
	channels := t.add(&flag{names: "--channels", kind: kindList, list: listPaths, arg: "servers...", group: groupSettings, pri: 1,
		help: "MCP servers whose events come into this session as they happen, each as server:<name>: a stdio server of your MCP configuration that declares itself a channel, as Claude Code's do (a CI webhook, a chat bridge). The model sees each event and may answer through the server's own tools.",
		set:  func(i *Invocation, v flagValue) { i.Channels = append(i.Channels, v.items...) }})
	devChannels := t.add(&flag{names: "--dangerously-load-development-channels", kind: kindList, list: listPaths, arg: "servers...", group: groupSettings, pri: 1,
		help: "The same as --channels, which gnosh keeps no list of approved channels for: Claude Code's spelling for a channel of one's own.",
		set:  func(i *Invocation, v flagValue) { i.Channels = append(i.Channels, v.items...) }})
	strictMCP := t.add(&flag{names: "--strict-mcp-config", group: groupSettings, pri: 1,
		help: "Use only the MCP servers --mcp-config gives.",
		set:  func(i *Invocation, _ flagValue) { i.StrictMCPConfig = true }})
	safeMode := t.add(&flag{names: "--safe-mode", group: groupSettings, pri: 1,
		help: "Start with your customizations off (hooks, MCP servers, context files, skills on disk, custom commands), to fix a broken one. Providers, the model, built-in tools and permissions stay as they are.",
		set:  func(i *Invocation, _ flagValue) { i.SafeMode = true }})
	bare := t.add(&flag{names: "--bare", group: groupSettings, pri: 1,
		help: "Minimal mode: no hooks, skills, custom commands, MCP servers from files, context files or mem; only Bash, Read, Edit and Write (unless --tools says otherwise); no background commands or sub-agents. Give the run what it needs with flags: --append-system-prompt, --settings, --mcp-config, --add-dir.",
		set:  func(i *Invocation, _ flagValue) { i.Bare = true }})
	noSlash := t.add(&flag{names: "--disable-slash-commands", group: groupSettings, pri: 1,
		help: "Turn off skills and slash commands for this session.",
		set:  func(i *Invocation, _ flagValue) { i.DisableSlashCommands = true }})
	debug := t.add(&flag{names: "-d --debug", kind: kindAttached, arg: "filter", group: groupSettings, pri: 1,
		help: "Write more to the log. A filter keeps only some kinds, and must be attached with an equals sign: --debug=hooks,mcp.",
		set: func(i *Invocation, v flagValue) {
			i.Debug = true
			i.DebugFilter = v.text
		}})
	debugFile := t.add(&flag{names: "--debug-file", kind: kindText, arg: "path", group: groupSettings, pri: 1,
		help: "Write the debug log to this file. Turns --debug on.",
		set: func(i *Invocation, v flagValue) {
			i.Debug = true
			i.DebugFile = v.text
		}})
	verbose := t.add(&flag{names: "--verbose", printLetter: 'v', group: groupSettings, pri: 1,
		help: "Show each turn in full. In print mode: the log, and one line per finished tool call, on stderr. With -p, -v means this.",
		set:  func(i *Invocation, _ flagValue) { i.Verbose = true }})

	printMode := t.add(&flag{names: "-p --print", group: groupPrint, pri: 0, endsCommands: true,
		help: "Answer one prompt and exit, without the interface. Every word after it that is not a flag is the prompt."})
	output := t.add(&flag{names: "--output-format", kind: kindText, arg: "format", group: groupPrint, pri: 0, printOnly: true,
		help:    "What to print: text (the answer, and the default), json (one result object at the end) or stream-json (a JSON line per message, then the result).",
		choices: []choice{{name: FormatText}, {name: FormatJSON, pri: 1}, {name: FormatStreamJSON, pri: 1}},
		set:     func(i *Invocation, v flagValue) { i.OutputFormat = v.text }})
	input := t.add(&flag{names: "--input-format", kind: kindText, arg: "format", group: groupPrint, pri: 0, printOnly: true,
		help:    "What standard input carries: text (the prompt, and the default), or stream-json (one JSON user message a line, for a conversation that goes on until standard input closes; needs --output-format stream-json).",
		choices: []choice{{name: FormatText}, {name: FormatStreamJSON, pri: 1}},
		set:     func(i *Invocation, v flagValue) { i.InputFormat = v.text }})
	goal := t.add(&flag{names: "--goal", kind: kindText, arg: "condition", group: groupPrint, pri: 1, printOnly: true, freeText: true,
		help: "Keep working until this condition holds, as /goal does in a session. The run exits non-zero while the goal is still unmet.",
		set:  func(i *Invocation, v flagValue) { i.Goal = v.text }})
	maxTurns := t.add(&flag{names: "--max-turns", kind: kindText, arg: "n", group: groupPrint, pri: 0, printOnly: true, number: true, min: 1,
		help: "Stop after this many turns of the model. There is no limit unless this is given.",
		set:  func(i *Invocation, v flagValue) { i.MaxTurns = v.n }})
	smallModel := t.add(&flag{names: "--small-model", kind: kindText, arg: "model", group: groupPrint, pri: 1, printOnly: true,
		help: "The model for the run's short side jobs. Without it they use the main model.",
		set:  func(i *Invocation, v flagValue) { i.SmallModel = v.text }})
	prompts := t.add(&flag{names: "--permission-prompts", kind: kindText, arg: "who", group: groupPrint, pri: 1, printOnly: true,
		help:    "Who answers a call that needs a person's yes: host (the default; a run has no host to ask, so the call is refused) or none (refused, the model is told not to send it again, and the tools that ask a person are taken away).",
		choices: []choice{{name: "host"}, {name: "none", pri: 1}},
		set:     func(i *Invocation, v flagValue) { i.PermissionPrompts = v.text }})
	schema := t.add(&flag{names: "--json-schema", kind: kindText, arg: "schema", group: groupPrint, pri: 1, printOnly: true, freeText: true,
		help: "After the turn, have the result as JSON that conforms to this JSON Schema: in the result's structured_output with --output-format json, and alone on standard output with text.",
		set:  func(i *Invocation, v flagValue) { i.JSONSchema = v.text }})
	replay := t.add(&flag{names: "--replay-user-messages", group: groupPrint, pri: 1, printOnly: true,
		help: "With --input-format and --output-format stream-json: write each user message read back on standard output, as a user line with isReplay.",
		set:  func(i *Invocation, _ flagValue) { i.ReplayUserMessages = true }})
	partial := t.add(&flag{names: "--include-partial-messages", group: groupPrint, pri: 1, printOnly: true,
		help: "With --output-format stream-json: also write the replies as they arrive, as the Messages API's streaming events (stream_event lines).",
		set:  func(i *Invocation, _ flagValue) { i.IncludePartialMessages = true }})
	noPersist := t.add(&flag{names: "--no-session-persistence", group: groupPrint, pri: 1, printOnly: true,
		help: "Keep nothing of the run: its session is not saved, and cannot be resumed.",
		set:  func(i *Invocation, _ flagValue) { i.NoSessionPersistence = true }})

	help := t.add(&flag{names: "-h --help", group: groupOther, pri: 0,
		help: "Show help: gnosh's, or after a command's name that command's."})
	version := t.add(&flag{names: "-v --version", group: groupOther, pri: 0,
		help: "Print which build of gnosh this is: when it was built, and from which commit. (With -p, -v means --verbose.)"})

	sessionFlags := []*flag{
		cont, resume, sessionID, fork, name, back,
		model, effort, fallback,
		yolo, mode, allowSkip, allowed, disallowed, tools, addDir, trustFolder,
		system, systemFile, appendSystem, appendSystemFile, agents, asAgent,
		cwd, worktree, settings, settingSrc, mcpConfig, strictMCP, channels, devChannels, safeMode, bare, noSlash, debug, debugFile, verbose,
		printMode, output, input, goal, maxTurns, smallModel, partial, replay, schema, prompts, noPersist,
		help, version,
	}

	// ------------------------------------------------------------------
	// The flags of single commands.
	// ------------------------------------------------------------------

	doctorHosts := t.add(&flag{names: "--hosts", id: "doctor --hosts", pri: 1,
		help: "Also try each declared host: log in to it with ssh and run nothing there. Without this, doctor reaches no other machine.",
		set:  func(i *Invocation, _ flagValue) { i.Hosts = true }})
	trustList := t.add(&flag{names: "--list", id: "trust --list", pri: 1,
		help: "List the trusted folders.",
		set:  func(i *Invocation, _ flagValue) { i.List = true }})
	trustForget := t.add(&flag{names: "--forget", id: "trust --forget", pri: 1,
		help: "Forget the folder instead of trusting it.",
		set:  func(i *Invocation, _ flagValue) { i.Forget = true }})
	sessionJSON := t.add(&flag{names: "--json", id: "session --json", pri: 1,
		help: "Print JSON instead of text.",
		set:  func(i *Invocation, _ flagValue) { i.JSON = true }})
	promptForce := t.add(&flag{names: "--force", id: "prompt export --force", pri: 1,
		help: "Write over a file of that name that is there already.",
		set:  func(i *Invocation, _ flagValue) { i.Force = true }})
	agentsJSON := t.add(&flag{names: "--json", id: "agents --json", pri: 1,
		help: "Print JSON instead of text: the working sessions, and with --all the others too.",
		set:  func(i *Invocation, _ flagValue) { i.JSON = true }})
	agentsAll := t.add(&flag{names: "--all", id: "agents --all", pri: 1,
		help: "Also list every session that is done, not only those of the last week.",
		set:  func(i *Invocation, _ flagValue) { i.All = true }})
	logsFollow := t.add(&flag{names: "-f --follow", id: "logs --follow", pri: 1,
		help: "Keep printing as the log grows.",
		set:  func(i *Invocation, _ flagValue) { i.Follow = true }})
	logsTail := t.add(&flag{names: "-t --tail", id: "logs --tail", kind: kindText, arg: "n", pri: 1, number: true, min: 1,
		help: "Print the last n lines: 1000 unless this is given.",
		set:  func(i *Invocation, v flagValue) { i.Tail = v.n }})
	mcpScope := t.add(&flag{names: "-s --scope", id: "mcp --scope", kind: kindText, arg: "scope", pri: 1,
		choices: []choice{{name: "local"}, {name: "user"}, {name: "project"}},
		help:    "Where the server is kept: local (this project, for you alone: the default for add), user (every project of yours) or project (the project's .mcp.json, which goes with it).",
		set:     func(i *Invocation, v flagValue) { i.MCPScope = v.text }})
	mcpNoBrowser := t.add(&flag{names: "--no-browser", id: "mcp --no-browser", pri: 1,
		help: "Open no browser: only say the address to sign in at.",
		set:  func(i *Invocation, _ flagValue) { i.NoBrowser = true }})
	mcpTransport := t.add(&flag{names: "-t --transport", id: "mcp --transport", kind: kindText, arg: "transport", pri: 1,
		choices: []choice{{name: "stdio"}, {name: "http"},
			{name: "sse", refuse: "the sse transport is not one gnosh speaks: a server reached at a URL is spoken to over http (Streamable HTTP), which servers that once spoke SSE speak now too"}},
		help: "How the server is reached: stdio (gnosh starts it: the default) or http (it is at a URL).",
		set:  func(i *Invocation, v flagValue) { i.MCPTransport = v.text }})
	mcpEnv := t.add(&flag{names: "-e --env", id: "mcp --env", kind: kindList, list: listPairs, arg: "KEY=value...", pri: 1,
		check: func(v string) string {
			if !isPair(v) {
				return "is not KEY=value"
			}
			return ""
		},
		help: "Set these variables for a server gnosh starts.",
		set:  func(i *Invocation, v flagValue) { i.MCPEnv = append(i.MCPEnv, v.items...) }})
	mcpHeader := t.add(&flag{names: "-H --header", id: "mcp --header", kind: kindList, list: listHeaders, arg: "header...", pri: 1,
		check: func(v string) string {
			if !isHeader(v) {
				return `is not a header ("Name: value")`
			}
			return ""
		},
		help: `Send these headers ("Name: value") to a server at a URL.`,
		set:  func(i *Invocation, v flagValue) { i.MCPHeaders = append(i.MCPHeaders, v.items...) }})

	// ------------------------------------------------------------------
	// The commands.
	// ------------------------------------------------------------------

	root := t.command(nil, &command{name: "gnosh", cmd: CmdInteractive, session: true, maxArgs: -1,
		argsUsage: "[prompt]", flags: sessionFlags, take: takePrompt, finish: finishSession,
		dashHint: promptAfterDashes,
		summary:  "gnosh: an AI harness that thinks in Gno."})
	t.root = root

	t.command(root, &command{name: "doctor", cmd: CmdDoctor, pri: 1,
		flags:   []*flag{doctorHosts, cwd, help},
		summary: "Check that a turn could run here.",
		about:   "Reads the config for this folder and prints one line per check. Exits 1 when a turn could not run. Starts no model."})

	t.command(root, &command{name: "trust", cmd: CmdTrust, pri: 1, maxArgs: 1, argsUsage: "[folder]",
		flags: []*flag{trustList, trustForget, cwd, help}, finish: finishTrust,
		summary: "Trust a folder's own settings, list the trusted folders, or forget one.",
		about:   "With no folder, the current one. --list takes no folder."})

	t.command(root, &command{name: "models", cmd: CmdModels, pri: 1,
		flags:   []*flag{cwd, help},
		summary: "List the models each provider offers.",
		about:   "Also names the providers that were left out, and why. It never loads a model."})

	sess := t.command(root, &command{name: "session", aliases: []string{"sessions", "s"}, needSub: true, pri: 1,
		argsUsage: "<command>", flags: []*flag{help},
		summary: "Look at and manage sessions.",
		about:   "These start no model."})
	sessionCommon := []*flag{sessionJSON, cwd, help}
	t.command(sess, &command{name: "list", aliases: []string{"ls"}, cmd: CmdSessionList, pri: 1,
		flags: sessionCommon, summary: "List this project's sessions."})
	t.command(sess, &command{name: "show", cmd: CmdSessionShow, pri: 1, minArgs: 1, maxArgs: 1, argsUsage: "<id>",
		flags: sessionCommon, summary: "Print one session."})
	t.command(sess, &command{name: "last", cmd: CmdSessionLast, pri: 1,
		flags: sessionCommon, summary: "Print the most recent session."})
	t.command(sess, &command{name: "rename", cmd: CmdSessionRename, pri: 1, minArgs: 2, maxArgs: -1, argsUsage: "<id> <title...>",
		flags: sessionCommon, take: takeRename,
		summary: "Give a session a new title.",
		about:   "The words after the ID are the title."})

	t.command(root, &command{name: "mem", cmd: CmdMem, pri: 1, maxArgs: -1,
		argsUsage: "[show [name...] | find <words> | log [n]]", argsShort: "[show|find|log]",
		passes: true, dashHint: "a word for mem that begins with a dash goes after --",
		flags:   []*flag{cwd, help},
		summary: "Read the project's mem, without a model.",
		about:   "The words after mem go to mem as they are: show, find and log read it. What changes mem is done with /mem inside a session."})

	prm := t.command(root, &command{name: "prompt", needSub: true, pri: 1,
		argsUsage: "<command>", flags: []*flag{help},
		summary: "Read gnosh's own words to the model, and make any of them yours.",
		about:   "These start no model. A file of yours in the folder gnosh prompt path names stands in for gnosh's text of its name in every session from then on. What fills the model's window, each tool's share with it, is /context all (in a session, or gnosh -p '/context all')."})
	t.command(prm, &command{name: "show", cmd: CmdPromptShow, pri: 1, maxArgs: 1, argsUsage: "[name]",
		flags:   []*flag{help},
		summary: "List gnosh's texts to the model, or print one as it is sent.",
		about:   "The list says where each text goes and how large it is, and which are yours. With a name, the text in force is printed, and what it is, when it is sent and where it came from are said on standard error, so that the text alone can be kept in a file."})
	t.command(prm, &command{name: "path", cmd: CmdPromptPath, pri: 1,
		flags:   []*flag{help},
		summary: "Print the folder your own texts go in."})
	t.command(prm, &command{name: "export", cmd: CmdPromptExport, pri: 1, maxArgs: -1, argsUsage: "[name...]",
		flags:   []*flag{promptForce, help},
		summary: "Write gnosh's own texts into the folder yours go in, to edit them.",
		about:   "With no name, every text. A file that is there is kept unless --force is given. An exported text changes nothing until its words are changed: its first line marks it as gnosh's own, and is never sent."})

	t.command(root, &command{name: "agents", cmd: CmdAgents, pri: 1,
		flags:   []*flag{agentsJSON, agentsAll, cwd, yolo, mode, model, effort, help},
		finish:  func(inv *Invocation, p *parsed, _ Env) *Error { return finishYolo(inv, p) },
		summary: "The sessions that run in the background, and those that ran: a view of them on a terminal.",
		about:   "The sessions gnosh --bg, /background and /fork started, from every folder (with --cwd, from that folder and the folders in it): the working ones first, then those done in the last week. On a terminal it is a view to work in, as Claude Code's agent view: Space peeks at a run, Enter watches a working one or opens one that is over, Ctrl+X stops one, a prompt typed at the bottom starts one, and ? lists the keys; -y, --permission-mode, --model and --effort go to the runs it starts. Elsewhere, and with --json, it prints the list, each run named by the start of its ID, which gnosh logs, gnosh stop and gnosh -r take."})

	t.command(root, &command{name: "logs", cmd: CmdLogs, pri: 1, maxArgs: 1, argsUsage: "[session]",
		flags:   []*flag{logsFollow, logsTail, cwd, help},
		summary: "Print the end of the project's log, or follow it.",
		about:   "With a session's ID (or the start of one), what that session printed as a run in the background (gnosh --bg, /background, /fork; gnosh agents lists them); --follow follows it until the run is done."})

	t.command(root, &command{name: "attach", cmd: CmdAttach, pri: 1, minArgs: 1, maxArgs: 1, argsUsage: "<session>",
		flags:   []*flag{cwd, help},
		summary: "Open the agent view on a session that runs in the background.",
		about:   "As Claude Code's claude attach: the session is a run that gnosh --bg, /background or /fork started, named by its ID, the start of one, or its name (gnosh agents lists them). A working run is watched as it goes, one that waits on a question shows the question, and one that is over opens as gnosh -r opens it. It needs a terminal; elsewhere gnosh logs -f follows a run."})

	t.command(root, &command{name: "stop", aliases: []string{"kill"}, cmd: CmdStop, pri: 1, minArgs: 1, maxArgs: 1, argsUsage: "<session>",
		flags:   []*flag{cwd, help},
		summary: "Stop a session that runs in the background.",
		about:   "The session is a run that gnosh --bg, /background or /fork started, named by its ID or the start of one (gnosh agents lists them). It is stopped as Esc stops a turn, and given a minute to end in good order before it is made to; what it did so far is kept: gnosh -r opens it."})

	t.command(root, &command{name: "dirs", cmd: CmdDirs, pri: 1,
		flags:   []*flag{cwd, help},
		summary: "Say which config and data folders are in use."})

	t.command(root, &command{name: "ssh", cmd: CmdSSH, pri: 1, maxArgs: -1,
		argsUsage: "[ssh arguments...]", argsShort: "[ssh args]",
		passes: true, dashHint: "gnosh ssh takes none of gnosh's own flags: it comes right after gnosh, and every word after it is ssh's",
		flags:   []*flag{help},
		summary: "Run ssh, putting what the far side copies on this machine's clipboard.",
		about: "Every word after ssh is ssh's (but for -h or --help alone, which shows this), and gnosh ssh ends with ssh's status. At a terminal, ssh keeps the terminal, its passwords and its job, and what it writes passes through gnosh as it is, " +
			"which puts the text of each copy the far side makes through the terminal (OSC 52: gnosh's /copy, tmux's copy mode) on this machine's clipboard as well. " +
			"Apple's Terminal drops such copies, so there a shell function can send each interactive ssh through gnosh ssh. " +
			"Without a terminal, where this machine has no clipboard tool (pbcopy, wl-copy, xclip, xsel), or with -f, gnosh ssh is plain ssh."})

	mcp := t.command(root, &command{name: "mcp", needSub: true, pri: 1,
		argsUsage: "<command>", flags: []*flag{help},
		summary: "Add, list, show and remove MCP servers.",
		about:   "They are kept in gnosh's own mcp.json (local and user servers) and in a project's .mcp.json (project servers), in Claude Code's shape. A local server is a project's and yours alone; a server in .mcp.json goes with the project, and is used once its folder is trusted."})
	t.command(mcp, &command{name: "add", cmd: CmdMCPAdd, pri: 1, minArgs: 2, maxArgs: -1,
		argsUsage: "<name> <command-or-url> [args...]",
		passes:    true, dashHint: "a word for the server's command that begins with a dash goes after --, as in gnosh mcp add name -- npx -y server",
		flags:   []*flag{mcpScope, mcpTransport, mcpEnv, mcpHeader, cwd, help},
		summary: "Add a server.",
		about:   "A server gnosh starts is its command and arguments; one at a URL is -t http and the URL. Put -- before the command when it has flags of its own."})
	t.command(mcp, &command{name: "add-json", cmd: CmdMCPAddJSON, pri: 1, minArgs: 2, maxArgs: 2, dashIsStdin: true,
		argsUsage: "<name> <json>",
		flags:     []*flag{mcpScope, cwd, help},
		summary:   "Add a server from its entry, as JSON.",
		about:     `The entry is what goes under the server's name in "mcpServers", such as {"type": "http", "url": "https://..."}. With - in its place the entry is read from standard input, so that a key in it stays off the command line.`})
	t.command(mcp, &command{name: "get", cmd: CmdMCPGet, pri: 1, minArgs: 1, maxArgs: 1, argsUsage: "<name>",
		flags:   []*flag{cwd, help},
		summary: "Show one server, and whether it connects.",
		about:   "Values of its variables and headers are not shown."})
	t.command(mcp, &command{name: "list", aliases: []string{"ls"}, cmd: CmdMCPList, pri: 1,
		flags:   []*flag{cwd, help},
		summary: "List the servers this folder uses, and whether each connects.",
		about:   "A project's servers in a folder that is not trusted are named and not started."})
	t.command(mcp, &command{name: "remove", aliases: []string{"rm"}, cmd: CmdMCPRemove, pri: 1, minArgs: 1, maxArgs: 1, argsUsage: "<name>",
		flags:   []*flag{mcpScope, cwd, help},
		summary: "Remove a server.",
		about:   "When the name is kept in more than one place, -s says which."})
	t.command(mcp, &command{name: "login", cmd: CmdMCPLogin, pri: 1, minArgs: 1, maxArgs: 1, argsUsage: "<name>",
		flags:   []*flag{mcpNoBrowser, cwd, help},
		summary: "Sign in to a server at a URL that wants it.",
		about: "gnosh says the address to sign in at, and opens a browser there when it runs at a desktop (not over ssh, and never with --no-browser). " +
			"When the browser cannot come back to this machine, paste the address it tried to open, and Enter. The sign-in is kept for every session."})
	t.command(mcp, &command{name: "logout", cmd: CmdMCPLogout, pri: 1, minArgs: 1, maxArgs: 1, argsUsage: "<name>",
		flags:   []*flag{cwd, help},
		summary: "Forget the sign-in to a server at a URL.",
		about:   "The authorization server is not told: the token it gave may still work until it runs out."})

	t.command(root, &command{name: "help", cmd: CmdHelp, pri: 0, maxArgs: -1, argsUsage: "[command]",
		flags: []*flag{help}, finish: finishHelp, afterAnything: true,
		summary: "Show gnosh's help, or a command's."})
	t.command(root, &command{name: "version", cmd: CmdVersion, pri: 0,
		flags: []*flag{help}, afterAnything: true,
		summary: "Print which build of gnosh this is."})

	for _, c := range t.commands {
		c.index()
	}
	return t
}

// promptAfterDashes is how gnosh says to give a prompt that begins with a
// dash.
const promptAfterDashes = "a prompt that begins with a dash goes after --"

// wordAfterC refuses a single word right after -c. In the earlier gnosh -c
// took the working folder, and scripts still pass `-c DIR`; read the new
// way, that would continue some session, in whatever folder the script was
// started from, with DIR as its prompt. So the one shape that could be the
// old use is refused, and the message gives both ways to say what was
// meant. A prompt of several words after -c is not that shape and is left
// alone, as is anything after -C or --continue, which never took a folder.
func wordAfterC(typed, next string) string {
	if typed != "-c" || next == "" || hasSpace(next) {
		return ""
	}
	return fmt.Sprintf(
		"-c means --continue now. It used to take the working folder, so a single word right after it is refused rather than sent as the prompt, and nothing was started. For the folder, write --cwd %s. For a one-word prompt, write -C %s, or put -- before it.",
		shown(next), shown(next))
}
