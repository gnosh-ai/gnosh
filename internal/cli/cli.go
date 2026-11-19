package cli

// Env is the little that reading a command line needs to know about the
// process it was typed into. The caller fills it in; Parse reads nothing by
// itself.
type Env struct {
	// StdinIsTerminal says that standard input is a terminal: a person can
	// type, and nothing is piped in.
	StdinIsTerminal bool
	// StdoutIsTerminal says that standard output is a terminal. When it is
	// not, `gnosh` with no command answers once and exits, as -p would,
	// because there is no screen to draw a session on.
	StdoutIsTerminal bool
}

// Command names what a command line asks for.
type Command string

// The commands. A session is CmdInteractive or CmdPrint; the others are the
// subcommands, named by the words that select them.
const (
	// CmdInteractive opens a session a person types into: `gnosh`.
	CmdInteractive Command = "interactive"
	// CmdPrint answers one prompt and exits: `gnosh -p`, or `gnosh` with
	// its output going somewhere other than a terminal.
	CmdPrint Command = "print"
	// CmdHelp prints help: -h, --help, or `gnosh help`. Invocation.HelpFor
	// says for which command.
	CmdHelp Command = "help"
	// CmdVersion prints which build this is: --version, -v outside print
	// mode, or `gnosh version`.
	CmdVersion Command = "version"

	// CmdDoctor checks that a turn could run here.
	CmdDoctor Command = "doctor"
	// CmdTrust trusts a folder's settings, lists the trusted folders
	// (Invocation.List) or forgets one (Invocation.Forget).
	CmdTrust Command = "trust"
	// CmdModels lists the models each provider offers.
	CmdModels Command = "models"
	// CmdSessionList and the three after it are `gnosh session ...`.
	CmdSessionList   Command = "session list"
	CmdSessionShow   Command = "session show"
	CmdSessionLast   Command = "session last"
	CmdSessionRename Command = "session rename"
	// CmdMem reads the project's mem; Invocation.Args holds the words after
	// `mem`, for mem itself to read.
	CmdMem Command = "mem"
	// CmdPromptShow and the two after it are `gnosh prompt ...`: gnosh's
	// texts to the model listed or printed, the folder of the person's own,
	// and gnosh's written there to edit (Invocation.Force writes over a
	// file that is there).
	CmdPromptShow   Command = "prompt show"
	CmdPromptPath   Command = "prompt path"
	CmdPromptExport Command = "prompt export"
	// CmdAgents lists the sessions in the background, of every folder or
	// of Invocation.Cwd's.
	CmdAgents Command = "agents"
	// CmdLogs prints the project's log, or a background run's.
	CmdLogs Command = "logs"
	// CmdStop stops a session that runs in the background.
	CmdStop Command = "stop"
	// CmdAttach opens the agent view on one session in the background.
	CmdAttach Command = "attach"
	// CmdDirs says which config and data folders are in use.
	CmdDirs Command = "dirs"
	// CmdSSH is `gnosh ssh`: ssh, with what the far side copies put on
	// this machine's clipboard (internal/relay). main runs it before
	// anything else is read, so that every word after ssh is ssh's; the
	// table has it for help.
	CmdSSH Command = "ssh"
	// CmdMCPAdd and the four after it are `gnosh mcp ...`: they add, show,
	// list and remove the MCP servers of the files they are configured
	// in. Invocation.Args holds the server's name, and for add its command
	// or URL and arguments, or for add-json its entry.
	CmdMCPAdd     Command = "mcp add"
	CmdMCPAddJSON Command = "mcp add-json"
	CmdMCPGet     Command = "mcp get"
	CmdMCPList    Command = "mcp list"
	CmdMCPRemove  Command = "mcp remove"
	// CmdMCPLogin and CmdMCPLogout sign in to a server reached at a URL,
	// and forget the sign-in; Invocation.Args holds its name.
	CmdMCPLogin  Command = "mcp login"
	CmdMCPLogout Command = "mcp logout"
)

// Conversation says which conversation a session command works in.
type Conversation string

// The ways to choose a conversation.
const (
	// ConversationNew starts a new one. It is what a session gets when no
	// flag says otherwise.
	ConversationNew Conversation = "new"
	// ConversationContinue goes on with the most recent one of the project:
	// -c, -C, --continue.
	ConversationContinue Conversation = "continue"
	// ConversationResume goes on with the one -r names (Invocation.Resume),
	// or lets the person pick when it names none.
	ConversationResume Conversation = "resume"
)

// The values of --permission-mode, in Claude Code's spelling.
const (
	ModeDefault           = "default"
	ModePlan              = "plan"
	ModeAcceptEdits       = "acceptEdits"
	ModeDontAsk           = "dontAsk"
	ModeBypassPermissions = "bypassPermissions"
)

// The values of --output-format. --input-format takes the first of them.
const (
	FormatText       = "text"
	FormatJSON       = "json"
	FormatStreamJSON = "stream-json"
)

// Invocation is what a command line asks gnosh to do, as plain data. Parse
// fills it in and acts on none of it: no file is read, no directory checked,
// no tool name looked up. A field the command line did not set holds its
// zero value, unless its comment names a default.
type Invocation struct {
	// Command is what to do.
	Command Command

	// HelpFor, with CmdHelp, is the command whose help is wanted, as the
	// words that lead to it ("session", "list"). Empty means gnosh itself.
	HelpFor []string

	// Args is a subcommand's own arguments, in order and exactly as typed:
	// the folder of `trust`, the ID of `session show`, the ID and then the
	// title of `session rename`, and the words after `mem`.
	Args []string

	// Prompt is the prompt given on the command line: every argument that is
	// not a flag or a flag's value, joined by single spaces. Its text is not
	// changed in any other way.
	Prompt string
	// ReadStdin says standard input is not a terminal, so there may be piped
	// text to read. In print mode that text goes before Prompt, with a blank
	// line between; in an interactive session it is the first prompt.
	ReadStdin bool
	// PrintImplied says print mode was chosen because standard output is not
	// a terminal, not because -p was typed.
	PrintImplied bool

	// Conversation says which conversation to work in. A session always
	// sets it; ConversationNew is the default.
	Conversation Conversation
	// Resume is what followed -r: a session ID, a session's name, words to
	// search the session list for, or the path of a transcript file. Empty
	// with ConversationResume means show the list to pick from.
	Resume string
	// ResumeIsFile says Resume is an absolute path ending in .jsonl, which
	// names a transcript file and not a session.
	ResumeIsFile bool
	// SessionID is the ID the new conversation is to have (--session-id). It
	// is a UUID; Parse has checked its form.
	SessionID string
	// ForkSession says to go on in a copy of the conversation and leave the
	// original as it was.
	ForkSession bool
	// Background says to start the session in the background (--bg): a
	// run of its own on the prompt, while this command returns at once.
	Background bool
	// Name is the name to give the session (-n).
	Name string
	// NoSessionPersistence keeps nothing of a run: its session is not
	// saved and cannot be resumed (print mode only).
	NoSessionPersistence bool
	// PermissionPrompts is --permission-prompts: who answers a permission
	// question in a run, "host" (the default) or "none".
	PermissionPrompts string
	// JSONSchema is --json-schema: after the turn, the result as JSON that
	// conforms to this schema, in the result's structured_output.
	JSONSchema string
	// ReplayUserMessages is --replay-user-messages: with stream-json both
	// ways, each user message read is written back as a user line.
	ReplayUserMessages bool
	// IncludePartialMessages is --include-partial-messages: stream-json
	// also carries the main agent's replies as streaming events.
	IncludePartialMessages bool
	// Agents is --agents: sub-agents defined for this run, as a JSON
	// object of agents by name, or the path of a file that holds one.
	Agents string
	// Agent is --agent: the kind of agent the session runs as.
	Agent string
	// Bare is minimal mode: safe mode's customizations off, mem and the
	// other features off, only Bash, Read, Edit and Write unless --tools
	// says otherwise, and no background commands or sub-agents.
	Bare bool

	// Model is the model for this run, as typed: `model` or `provider/model`.
	Model string
	// SmallModel is the model for a print run's short side jobs.
	SmallModel string
	// FallbackModel is --fallback-model, as typed: the models to go on
	// with when the model cannot answer, separated by commas.
	FallbackModel string
	// Effort is the thinking level for the run (--reasoning-effort or
	// --effort), as typed: the levels depend on the model.
	Effort string

	// PermissionMode is the permission mode to start in, one of the Mode
	// constants; "" means the command line did not say. -y, --yolo and
	// --dangerously-skip-permissions set ModeBypassPermissions, and `manual`
	// is recorded as ModeDefault.
	PermissionMode string
	// AllowSkipPermissions is --allow-dangerously-skip-permissions: the
	// session may switch to skipping permissions later, with Shift+Tab,
	// without starting that way.
	AllowSkipPermissions bool
	// AllowedTools holds the allow rules, one per item, as typed.
	AllowedTools []string
	// DisallowedTools holds the deny rules, one per item, as typed.
	DisallowedTools []string
	// Tools holds the names given to --tools, as typed (`default` included).
	// ToolsGiven tells `--tools ""`, which gives the model no tools, from no
	// --tools at all.
	Tools      []string
	ToolsGiven bool
	// AddDirs holds the extra folders the agent may read, as typed.
	AddDirs []string
	// TrustFolder says to use this folder's own settings for this one run.
	TrustFolder bool

	// SystemPrompt is text to use in place of gnosh's own instructions, and
	// SystemPromptFile the path of a file holding such text. Neither file is
	// read here. When both are given the file's text comes first.
	SystemPrompt     string
	SystemPromptFile string
	// AppendSystemPrompt is text to add to the end of the instructions, and
	// AppendSystemPromptFile the path of a file holding such text. When both
	// are given the file's text comes first.
	AppendSystemPrompt     string
	AppendSystemPromptFile string

	// Cwd is the folder to work in (--cwd).
	Cwd string
	// Worktree says to work in a git work tree of the repository's own
	// (--worktree), named WorktreeName, or with a name made up when
	// that is empty.
	Worktree     bool
	WorktreeName string
	// Settings is what followed --settings: a file's path, or the settings
	// themselves as JSON. SettingsInline says it is JSON (it begins with a
	// brace).
	Settings       string
	SettingsInline bool
	// SettingSources lists which settings files to read: any of "user",
	// "project", "local". Nil means the command line did not say.
	SettingSources []string
	// SafeMode asks for a start with the user's customizations off.
	SafeMode bool
	// DisableSlashCommands turns skills and slash commands off.
	DisableSlashCommands bool
	// Debug asks for more in the log. --debug-file turns it on too.
	Debug bool
	// DebugFilter is the filter attached to --debug with an equals sign.
	DebugFilter string
	// DebugFile is the file to write the debug log to.
	DebugFile string
	// Verbose asks for each turn in full; in print mode, for the log and one
	// line per tool call on stderr.
	Verbose bool

	// OutputFormat is what a print run prints: one of the Format constants.
	// In print mode it is FormatText unless the command line says otherwise;
	// outside print mode it is "".
	OutputFormat string
	// InputFormat is what a print run reads from standard input: FormatText,
	// the one format there is. It defaults as OutputFormat does.
	InputFormat string
	// Goal is the condition a print run works towards (--goal).
	Goal string
	// MaxTurns is the most turns the model may take in a print run. Zero
	// means no limit.
	MaxTurns int

	// JSON asks a subcommand to print JSON (`session ...`, `agents`).
	JSON bool
	// All belongs to `agents`: also every session in the background that
	// is done, not only those of the last week.
	All bool
	// Force belongs to `prompt export`: write over a file that is there.
	Force bool
	// Hosts belongs to `doctor`: also try each declared host, which reaches
	// out to other machines.
	Hosts bool
	// List and Forget belong to `trust`: list the trusted folders, or forget
	// one instead of trusting it.
	List   bool
	Forget bool
	// Follow and Tail belong to `logs`: keep printing as the log grows, and
	// how many lines to print (zero means the command line did not say).
	Follow bool
	Tail   int
	// MCPScope, MCPTransport, MCPEnv and MCPHeaders belong to `mcp`: where
	// a server is kept ("local", "user" or "project"; "" when the command
	// line did not say), how it is reached ("stdio" or "http"; "" means
	// stdio), and its environment (KEY=value) and headers ("Name: value"),
	// as they were typed.
	MCPScope     string
	MCPTransport string
	// NoBrowser says gnosh mcp login opens no browser, and only says the
	// address to sign in at.
	NoBrowser  bool
	MCPEnv     []string
	MCPHeaders []string
	// MCPConfigs are the MCP servers a session is given on its command line
	// (--mcp-config): files, or the JSON itself, each in the shape of
	// .mcp.json. StrictMCPConfig says to use those alone.
	MCPConfigs      []string
	StrictMCPConfig bool
	// Channels are the MCP servers whose pushed events come into the
	// session (--channels, and Claude Code's development spelling of it),
	// each as written: server:<name>.
	Channels []string
}

// Exit codes, for the code that runs what Parse describes.
const (
	// ExitOK: the turn finished, or the command did what it was asked.
	ExitOK = 0
	// ExitFailure: the turn failed, a hook blocked the prompt, --goal is
	// still unmet, `gnosh doctor` found that a turn cannot run, or gnosh
	// refused to start (a command line it could not act on, flags that
	// cannot be combined, a session that cannot be resumed). Every error
	// Parse returns exits with this.
	ExitFailure = 1
	// ExitInterrupted: stopped by Ctrl-C (SIGINT). Nothing is printed.
	ExitInterrupted = 130
	// ExitTerminated: stopped by SIGTERM.
	ExitTerminated = 143
)

// Parse reads a command line (the arguments after the program's name) with
// gnosh's built-in table of commands and flags, and returns what it asks
// for. It reads no file, no environment variable and no input: env carries
// the two facts it needs. An error is a *Error whose text is ready to show.
//
// A flag whose feature is not built yet is an error, not a silent no-op.
// The code that wires features in makes its own table with Default and marks
// what it has built; Parse uses the table as it ships, in which only what
// the specs call P0 is marked.
func Parse(args []string, env Env) (Invocation, error) {
	return Default().Parse(args, env)
}

// Help returns the help text for gnosh, or for the command the words name
// ("session", "list"), from the built-in table. Every line fits 80 columns.
func Help(command ...string) (string, error) {
	return Default().Help(command...)
}
