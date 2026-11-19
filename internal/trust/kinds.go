package trust

// The kinds of thing a folder's own settings can hold. The config package
// sorts every setting of a project's files into one of these words (it has
// the ones a config file can say today, as its Kind constants; the rest
// are for what is found some other way, or not read from a config file
// yet), and Restricts says which of them still apply in a folder that is
// not trusted. A word that is not in this list is treated as the most
// dangerous kind: it is held, and the question mentions it.
const (
	KindProvider     = "provider"        // a model provider: its address, its key
	KindModel        = "model"           // a model registered or chosen
	KindHost         = "host"            // a machine to log in to
	KindHook         = "hook"            // a command run at a named moment
	KindHookOff      = "hook-off"        // switching all hooks off
	KindMCPLocal     = "mcp-local"       // an MCP server started on this machine
	KindMCPOnline    = "mcp-online"      // an MCP server at an address
	KindLSP          = "lsp"             // a language server
	KindAllowRule    = "allow-rule"      // a tool or command let run without asking
	KindAskRule      = "ask-rule"        // a tool or command that always asks
	KindDenyRule     = "deny-rule"       // a tool or command refused
	KindMode         = "mode"            // the starting permission mode and its switches
	KindAddDir       = "add-dir"         // another directory the file tools may read
	KindEnv          = "env"             // a variable for commands
	KindOption       = "option"          // any other option
	KindExtraPath    = "extra-path"      // an extra context or skill path
	KindAddress      = "address"         // an address for analytics, embeddings or deliverables
	KindHiddenSkill  = "hidden-skill"    // a skill hidden from the model
	KindDisabledTool = "disabled-tool"   // a tool taken away from the model
	KindRestriction  = "restriction"     // a setting that only takes a power away
	KindRemoval      = "removal"         // taking away a provider, a model or a host
	KindRunsCommand  = "runs-command"    // a value gnosh would expand by running something
	KindGitProgram   = "git-program"     // a program the repository's own git settings name
	KindSkillsLink   = "skills-link"     // a skills folder that is a link out of the folder
	KindStoredState  = "stored-state"    // something the project's stored state would turn on
	KindStatusLine   = "status-line"     // a command run to draw the status line
	KindFileSuggest  = "file-suggestion" // a command run to offer paths for an @ mention
)

// Restricts reports whether a setting of this kind only takes something
// away from the agent. Such a setting applies even in a folder that is not
// trusted: a deny rule, a rule that makes a tool ask first, a tool turned
// off, a skill hidden from the model. Everything else waits for trust,
// including every kind this package has never heard of. It is the
// function the config loader is given to filter a project's files
// (config.Inputs.KeepUntrusted).
func Restricts(kind string) bool {
	switch kind {
	case KindDenyRule, KindAskRule, KindDisabledTool, KindHiddenSkill, KindRestriction:
		return true
	}
	return false
}

// risk is one way a folder's settings could do harm.
type risk struct {
	// kinds are the kinds of setting that carry the risk.
	kinds []string
	// says is the sentence shown when any of them is held.
	says string
}

// risks is what could go wrong, one sentence per kind of harm, in the
// order they are shown. Only the sentences whose kinds are present are
// shown.
var risks = []risk{
	{[]string{KindHook, KindMCPLocal, KindLSP, KindStatusLine, KindFileSuggest},
		"Some of these run commands (hooks, servers started on this machine, a status line, the paths offered after @). They would run as you: a stranger's command could steal your SSH and wallet keys, wipe your files, or plant a program that stays."},
	{[]string{KindRunsCommand},
		"A setting here is filled in by running something. Any command hidden in a setting runs as you."},
	{[]string{KindGitProgram},
		"This repository's own git settings name programs. Git would run them as you whenever the agent, or gnosh itself, so much as looks at the repository."},
	{[]string{KindAllowRule, KindMode},
		"Some let tools run without asking. A line hidden in any file here could then get the agent to run what these settings allow, without asking you."},
	{[]string{KindProvider},
		"A provider set here would read everything the agent sees, could take your API key, and could tell the agent what to do."},
	{[]string{KindModel},
		"A model chosen here changes which model answers. On a server that holds one model at a time, that unloads the one you are using."},
	{[]string{KindMCPOnline},
		"An online MCP server would see what the agent sends it, and could steer the agent."},
	{[]string{KindEnv},
		"Environment variables change what commands do: for example, a changed search path that swaps one program for another."},
	{[]string{KindHost},
		"Hosts mean logging in to other machines with your SSH keys."},
	{[]string{KindSkillsLink, KindExtraPath, KindAddDir},
		"Linked skills, extra paths and added directories mean reading files outside this folder."},
	{[]string{KindAddress},
		"An address for analytics, embeddings or the deliverables server means sending your work off this computer."},
	{[]string{KindHookOff, KindRemoval},
		"Some switch off or remove things you set up yourself, such as your own hooks."},
	{[]string{KindStoredState},
		"The project's stored state would turn on things your own settings turned off."},
}

// otherRisk is the sentence for held settings that fit none of the above.
const otherRisk = "The rest change how gnosh behaves in this folder."
