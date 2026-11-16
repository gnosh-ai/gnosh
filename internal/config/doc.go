// Package config reads gnosh's configuration and checks it.
//
// The rule it is built on: a mistake in the configuration stops gnosh at
// startup and says plainly what is wrong, where, and what to do about it.
// Nothing is ever accepted and then quietly left unused. A key gnosh does
// not know, a name it cannot match, a value it cannot use, a setting for a
// part of gnosh that is not built: each is a Problem with the file and the
// line, and Load returns all of them at once.
//
// # What is read, weakest first
//
//  1. gnosh's built-in defaults.
//  2. The user's own file: gnosh.json in the config directory.
//  3. The state file: what was chosen inside gnosh with /model, /think and
//     /autocompact. It outranks the user's gnosh.json, so a choice sticks.
//  4. The project's files: gnosh.json in the working directory, then the
//     settings.local.json in the project's data folder, where approvals a
//     person saves are written. They are in force only in a trusted folder.
//  5. Environment variables.
//  6. The command line (--settings, then the single flags), which holds for
//     the one run and is never saved.
//
// The user's file, the project's two files and what --settings names all
// have one format and take the same keys. Every value in the Config
// carries its Origin: the layer, and the file and line or the flag or
// variable that set it.
//
// # Where things live
//
// The user's config directory is $GNOSH_CONFIG_DIR, else
// $XDG_CONFIG_HOME/gnosh, else ~/.config/gnosh. The user's state directory
// (the state file, the trust record) is $GNOSH_DATA_DIR, else
// $XDG_DATA_HOME/gnosh, else ~/.local/share/gnosh; no config file is ever
// read from it. A project's data folder is .gnosh: the closest one on the
// way up to the project root, or a new one at the root. Paths has them
// all.
//
// # The format
//
// A config file is JSON, with two allowances for people who edit by hand:
//
//   - comments, from // to the end of the line or between /* and */,
//     wherever white space may stand;
//   - a comma after the last member of an object or a list.
//
// Nothing else is relaxed: no single quotes, no keys without quotes. That
// is so by construction. The comments and the trailing commas are blanked
// out, byte for byte, and what is left is read by the standard library's
// JSON reader (jsonc.go). A file may nest 64 levels deep and hold 2 MiB.
//
// A file holds one object. A key given twice in one object is a mistake,
// and so is a key gnosh does not know, anywhere: the message names the
// key, the file and the line. For a setting that Claude Code has and gnosh
// has not built, it says that.
//
// # The keys
//
// The keys gnosh shares with Claude Code's settings files have Claude
// Code's names, shape and meaning, so that a block copied out of one works
// as it is:
//
//	permissions      allow, ask and deny (lists of rules), defaultMode,
//	                 additionalDirectories, disableBypassPermissionsMode,
//	                 disableAutoMode, denyInsteadOfPrompting
//	hooks            event, then matcher groups, then command handlers, each
//	                 with an "if" filter if it likes (one permission rule)
//	env              variables for the commands and hooks gnosh starts
//	disableAllHooks  true switches every hook off, and the status line
//	statusLine       {"type": "command", "command": ..., "padding": ...,
//	                 "refreshInterval": ...}: a command whose output is the
//	                 status line under the prompt; a program to run, so a
//	                 project's waits for trust, as a hook does
//	model            the session's model
//	skillOverrides   how far each skill is seen, by its name: "on" (as its
//	                 file says), "name-only", "user-invocable-only" or
//	                 "off"; a later file wins, and a folder that is not
//	                 trusted keeps its own, since each only takes away
//	$schema          accepted and not read
//
// gnosh's own keys stand beside them:
//
//	providers   the model servers, each under an id of the person's choosing
//	  type            which kind of server it is: llamacpp, openai-compatible,
//	                  anthropic or openai
//	  name            the name shown to the person
//	  baseUrl         where the API is: one address, or a list tried in
//	                  order, the first that answers being used
//	  apiKey          the key; none for a server on the person's own machine
//	  headers         more request headers, as an object of names and values
//	  extraBody       an object merged into the body of every request
//	  listModels      ask the server which models it has
//	  disabled        keep the entry and do not use it
//	models      what gnosh should know about a model, each under
//	            "<provider>/<model id>"
//	  name            the name shown to the person
//	  contextWindow   the model's window, in tokens
//	  maxOutputTokens the reply limit sent with a request
//	  thinkingLevels  its thinking levels, the least thinking first; an
//	                  empty list says the model has no thinking switch
//	model       the session's model, as "<provider>/<model id>"
//	smallModel  the model for the short side jobs (titles, summaries,
//	            checks); the session's model does them when none is named
//	hosts       other machines the model may run commands on, each under
//	            the name the model calls it by
//	  ssh             user@address, or a Host alias of the person's ssh
//	                  configuration
//	  description     what the machine is for
//	options     gnosh's own switches and limits
//	  requestTimeout           seconds a model request may go quiet; 0 waits
//	                           for ever
//	  autoCompactEnabled       make room in the model's window when it fills
//	  autoCompactWindow        tokens of context before room is made
//	  loopMaxTurns             an optional cap on /loop
//	  maxBackgroundTasks       background commands that may run at once
//	  userInstructionFiles     more instruction files of the person's own
//	  projectInstructionFiles  more instruction files of the project
//	  hiddenSkills             skills to hide from the model
//	  memoryText               "lean" or "full": which of gnosh's two texts
//	                           about mem the model is given
//	  memReviewCloses          review a todo when it is closed
//	  memEmbeddingsUrl         the embedding server for mem
//	  webSearchUrl             the SearXNG server the WebSearch tool asks;
//	                           unset, there is no WebSearch tool
//	  deliverablesServer       where the files the agent delivers are
//	                           served, to this machine only: "127.0.0.1:7777",
//	                           or "off"
//	  debug                    write more to the log
//	  sidePanel                start with the side panel on (/panel)
//	  verbose                  reasoning and whole outputs in the conversation
//	                           as they come (Claude Code's verbose)
//	  projectDataDir           the name of the project's data folder
//	  outputStyle              how the model replies: "Concise", say, or a
//	                           style of your own (/output-style)
//	  editorMode               "normal" or "vim": how the box takes keys
//	                           (not from a project's file)
//	  preferredNotifChannel    how a person who seems away is told the
//	                           session waits: "auto", "terminal_bell",
//	                           "iterm2", "iterm2_with_bell", "kitty",
//	                           "ghostty" or "notifications_disabled" (not
//	                           from a project's file)
//	  vimInsertModeRemaps      pairs typed in vim's INSERT mode that stand
//	                           for Esc, as ["jj"] (not from a project's file)
//
// "model" and "smallModel" are each the model as a string, or an object
// when how the model is asked matters:
//
//	"model": {"id": "qwen/qwen3.8-27b", "thinking": false, "temperature": 0.7}
//
// "thinking" is true, false, or one of the model's own levels, such as
// "low".
//
// A provider's "type" names the server where gnosh treats that server in
// a way of its own: llamacpp, anthropic, openai. Every other server that
// speaks OpenAI's chat-completions format is openai-compatible, whatever
// it is called, and needs no type of its own.
//
// Table lists every key, provider type, hook event and environment
// variable with what it is for and where its name comes from. The names
// follow one rule: the key or the variable Claude Code documents for the
// same thing, where there is one; else the public term of the model
// providers' APIs and of their SDKs (requestTimeout, extraBody,
// listModels); else plain words for what the thing does (options,
// hiddenSkills).
//
// An example file:
//
//	{
//	  // a model server, tried at each address in order; the first that answers wins
//	  "providers": {
//	    "qwen": {
//	      "type": "llamacpp",
//	      "name": "Qwen (ai box)",
//	      "baseUrl": ["http://192.0.2.91:8080", "http://127.0.0.1:18080"],  // or one string
//	      "listModels": false
//	    },
//	    // a hosted provider; the key comes from the environment
//	    "claude": { "type": "anthropic", "apiKey": "${ANTHROPIC_API_KEY}" }
//	  },
//	  // what gnosh should know about a model: "<provider>/<model id>"
//	  "models": {
//	    "qwen/qwen3.8-27b": { "name": "Qwen 3.8 27B", "contextWindow": 262144 }
//	  },
//	  "model": "qwen/qwen3.8-27b",        // the session's model
//	  "smallModel": "qwen/qwen3.8-27b",   // the model for side jobs (titles, summaries)
//	  "hosts": {
//	    "render": { "ssh": "root@203.0.113.10", "description": "the render box" }
//	  },
//	  "options": { "requestTimeout": 900 },  /* seconds a reply may go quiet */
//	  "permissions": { "allow": ["Read", "Bash(git diff *)"], "deny": ["Bash(git push *)"] },
//	  "hooks": {
//	    "Stop": [ { "hooks": [ { "type": "command", "command": "~/.config/gnosh/hooks/tests-must-pass.sh", "timeout": 1800 } ] } ]
//	  },
//	}
//
// # The environment
//
// gnosh reads four variables of its own: GNOSH_CONFIG_DIR and
// GNOSH_DATA_DIR, which name the two directories above; GNOSH_SAFE_MODE,
// which is --safe-mode; and GNOSH_DEBUG_PAGE, which serves a debugging
// page on this machine. Any other variable whose name begins with GNOSH_
// stops startup: it is most likely a misspelling, and would do nothing.
// (The variables gnosh sets for the hooks and commands it runs are known
// too, since a gnosh started from inside one inherits them.)
//
// Of Claude Code's variables it reads, with Claude Code's meaning:
// API_TIMEOUT_MS, BASH_DEFAULT_TIMEOUT_MS, BASH_MAX_TIMEOUT_MS,
// BASH_MAX_OUTPUT_LENGTH, CLAUDE_CODE_MAX_OUTPUT_TOKENS,
// CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS,
// CLAUDE_CODE_GLOB_TIMEOUT_SECONDS, CLAUDE_CODE_AUTO_COMPACT_WINDOW,
// CLAUDE_AUTOCOMPACT_PCT_OVERRIDE and CLAUDE_CODE_SAFE_MODE; and
// ANTHROPIC_API_KEY and OPENAI_API_KEY for a provider of that type that is
// given no key. One of them in the env block of a config file wins over
// the shell's. A value that cannot be used is passed over with a warning,
// never a mistake.
//
// # Variables in a config file
//
// In the string values of "providers" that say where a server is and how
// to be let in (each address of baseUrl, apiKey, each value of headers),
// ${NAME} is replaced by the environment variable NAME, and
// ${NAME:-default} by the text after :- when the variable is not set. That
// is the form Claude Code documents for .mcp.json, with its meaning: a
// variable that is set and empty counts as set. Nothing else is filled in,
// anywhere: $NAME without braces is plain text, and so is ${NAME} under
// any other key. There is no way to write a literal ${ in those three
// places; put such a value in a variable.
//
// A variable that is not set and has no default is never read as nothing:
// the provider is left out, as one whose server cannot be reached is, and
// a warning names the variable, the key and the line. gnosh starts, and
// every place that lists providers says why that one is missing. A ${ that
// is not closed, or that holds something other than a name, is a mistake
// and stops startup; so is the name of a variable gnosh itself sets for
// the hooks and commands it runs ($CLAUDE_PROJECT_DIR, $GNOSH_TOOL_NAME and
// the rest), since its value belongs to one run of a hook. Inside a hook's
// command those reach the command as written.
//
// # How the files are put together
//
// Files are read weakest first, and what a stronger file says is laid over
// what the weaker ones said:
//
//   - "providers" and "models" are merged by key, and then field by field:
//     what a stronger file gives for a field replaces what was there
//     (headers, extraBody and baseUrl each as a whole), and what it leaves
//     out stays. A provider needs its "type" where it is first declared.
//   - null takes away what a weaker file said, as in a JSON merge patch
//     (RFC 7396): null for a provider, a model or a host takes it away;
//     null for one of their fields, for "model" or "smallModel" or one of
//     their fields, puts it back to not given; null for an option puts it
//     back to its default. Taking away what no weaker file declared is a
//     mistake: it is most likely a misspelt name.
//   - Lists add up instead of replacing each other: permission rules,
//     additional directories, hooks, and the options that are lists.
//   - A host is declared once. Its name in two files is a mistake that
//     names both places, unless the stronger file takes it away first: a
//     name stands for one machine.
//   - Everything else is one value, from the strongest file that sets it.
//
// # A folder that is not trusted
//
// A project's files can add a hook, let tools run without asking, or point
// a provider somewhere else. So until the folder is trusted
// (Inputs.Trusted) they are read by the same reader, checked in full, and
// then held: only settings of a kind that Inputs.KeepUntrusted accepts are
// put in force, which with the trust package's rule means deny rules, ask
// rules and hidden skills. Every setting of a file is one of the Kind
// constants, whatever key it stands under and however deep, and there is
// no key by which a file can name another file to read.
// Config.Held lists everything that waits, for the trust question and for
// doctor. What is held is also taken out again after the last merge, so
// that no later step can put it back, and naming one of the folder's two
// files with --settings does not put it in force either: that stops
// startup and says why. A mistake in a project's file stops
// startup whether or not the folder is trusted: a deny rule in a file that
// could not be read would not be in force.
//
// # Hosts
//
// A host is another machine the model may run commands on: a name, the
// destination ssh is given, and what the person wrote about it. The
// declarations are checked as plain values (checkHosts), each one where it
// is read and all of them together once every file has been read, so that
// the same mistakes are found whichever file made the declaration: a name
// that is this machine's own ("local"), a name declared twice, a
// destination that is missing or is not user@address or one word ssh's own
// configuration can know. Each is a Problem that names the host and the
// place that declared it, and like every other mistake it stops startup.
//
// # Secrets
//
// A key, a header value, a variable's value and a password inside an
// address are kept as Secret, which prints as "[hidden]" with every verb,
// in logs and in JSON. No Problem and no line of Config.String or Doctor
// holds one.
//
// # What gnosh writes
//
// gnosh never writes a gnosh.json: that file is the person's, comments and
// all. The one settings file gnosh writes is settings.local.json in the
// project's data folder, when a person answers a permission question with
// "don't ask again". It reads the file as any other, so a person may edit
// it and put comments in it; but when gnosh next saves an approval it
// writes the whole file again as plain JSON, and the comments and trailing
// commas that were in it are gone. What is to be written is checked first
// (CheckWrite), so gnosh cannot write a file it would then refuse to load.
// The state file is gnosh's alone and is strict JSON.
//
// # What a caller does with the Config
//
// The Config is plain data. Each part of gnosh is handed what it needs:
// the provider client gets a Provider's Wire, an Endpoint's URL and
// Timeouts, the key, the headers and ExtraBody; the permission package gets
// Permissions; the hook runner gets Hooks; the shell tool gets CommandEnv,
// Limits and Hosts; the package that reads instruction files gets Context.
// Load reads none of those files and starts nothing.
package config
