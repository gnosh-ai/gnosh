package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ListItem is one value of a list option, with where it came from.
type ListItem struct {
	V      string
	Origin Origin
}

// Options are the settings of the "options" object, each with its typed
// value and where that value came from. A field whose Set is false holds
// the default. Every option here is one gnosh acts on.
type Options struct {
	// RequestTimeout is how many seconds a model request may go without
	// sending anything before it is given up. Zero waits for ever. When
	// nothing set it, each provider address has the limits that suit it
	// (see Endpoint.Timeouts), and V is meaningless.
	RequestTimeout Value[int]
	// MemEmbeddingsURL is the embedding server for mem. Unset, nothing is
	// sent anywhere.
	MemEmbeddingsURL Value[string]
	// WebSearchURL is the SearXNG server the WebSearch tool asks. Unset,
	// there is no WebSearch tool.
	WebSearchURL Value[string]

	// AutoCompactEnabled switches the compaction backstop.
	AutoCompactEnabled Value[bool]
	// AutoCompactWindow is how full the context may get, in tokens, before
	// the backstop runs. Unset means automatic.
	AutoCompactWindow Value[int]
	// LoopMaxTurns is an optional cap on /loop. Unset, zero or negative
	// means none.
	LoopMaxTurns Value[int]
	// MaxBackgroundTasks is how many background commands may run at once.
	MaxBackgroundTasks Value[int]
	// MemReviewCloses switches the review of a closed todo.
	MemReviewCloses Value[bool]

	// UserInstructionFiles are more instruction files of the person's own,
	// read in every project: files, or folders of Markdown files, as
	// absolute paths.
	UserInstructionFiles []ListItem
	// ProjectInstructionFiles are more instruction files of the project,
	// as absolute paths.
	ProjectInstructionFiles []ListItem
	// HiddenSkills are skills hidden from the model.
	HiddenSkills []ListItem
	// MemoryText chooses which of gnosh's two texts about mem a model is
	// given in its instructions: "lean", the default, or "full".
	MemoryText Value[string]

	// DeliverablesServer is where the files the agent delivers are served
	// on this machine: an address of this machine alone with a port, as
	// "127.0.0.1:7777", or ServerOff to serve none.
	DeliverablesServer Value[string]

	// Debug writes more to the log.
	Debug Value[bool]
	// SidePanel starts an interactive session with the side panel on.
	SidePanel Value[bool]
	// Verbose shows the model's reasoning and the calls' whole output in
	// the conversation as they come (Claude Code's verbose).
	Verbose Value[bool]
	// ProjectDataDir is the name of the project's data folder.
	ProjectDataDir Value[string]

	// OutputStyle is the output style's name (Claude Code's outputStyle):
	// "default", one of gnosh's own, or one of the person's or the
	// project's files.
	OutputStyle Value[string]

	// EditorMode is how the box takes keys: "normal", readline's keys, or
	// "vim". VimInsertModeRemaps are the pairs of characters that, typed
	// one after the other in vim's INSERT mode, stand for Esc ("jj").
	EditorMode Value[string]
	// PreferredNotifChannel is how the interface tells a person who seems
	// to be away that the session waits for them: "auto" (a desktop note
	// through a terminal that is known to take one), "terminal_bell",
	// "iterm2", "iterm2_with_bell", "kitty", "ghostty" or
	// "notifications_disabled".
	PreferredNotifChannel Value[string]
	VimInsertModeRemaps   []ListItem
}

// ServerOff is the value of an option that names where a server of gnosh's
// own listens, when the server is switched off.
const ServerOff = "off"

// DefaultDeliverablesServer is where the files the agent delivers are
// served when nothing says otherwise.
const DefaultDeliverablesServer = "127.0.0.1:7777"

// defaultOptions returns the options as they are when nothing sets them.
func defaultOptions() Options {
	var o Options
	o.AutoCompactEnabled.V = true
	o.MaxBackgroundTasks.V = 50
	o.MemReviewCloses.V = true
	o.MemoryText.V = "lean"
	o.DeliverablesServer.V = DefaultDeliverablesServer
	o.ProjectDataDir.V = ".gnosh"
	return o
}

// optKind is the shape of an option's value.
type optKind int

const (
	oBool     optKind = iota // true or false
	oInt                     // a whole number
	oEnum                    // one of a few words, as a string
	oURL                     // an http or https address, as a string
	oPath                    // a file or directory; a leading ~ is the home directory
	oListen                  // where a server of gnosh's own listens: an address of this machine alone with a port, or "off"
	oPathList                // a list of paths
	oNameList                // a list of names
	oName                    // a name, as a string that is not empty
)

// optionDef describes one key of the "options" object: what its value
// looks like, where its name comes from, and which field it sets.
type optionDef struct {
	key  string
	kind optKind
	def  string // the default, as a person reads it
	what string
	from string // where the name comes from

	enum     []string // for oEnum: the words it takes
	min, max int      // for oInt, when bounded is set: the least and the most
	bounded  bool     // the number must lie between min and max
	trust    string   // what an untrusted folder's setting counts as; "" is KindOption
	fromCwd  bool     // a relative path is under the working directory, not beside the file
	// ownOnly takes the option from the person's own files alone (their
	// gnosh.json, /config, --settings): a project's file is warned about
	// and left out, so that a repository cannot change how the box takes
	// the person's keys.
	ownOnly bool
	// each, for a list, says what is wrong with an entry, or "".
	each func(string) string

	truth func(*Options) *Value[bool]
	num   func(*Options) *Value[int]
	text  func(*Options) *Value[string]
	list  func(*Options) *[]ListItem
}

// optionDefs is every key the "options" object takes. A key that is not
// here is a startup error.
var optionDefs = []optionDef{
	// The model's requests.
	{key: "requestTimeout", kind: oInt, def: "900 on your own machine, 60 elsewhere",
		what: "seconds a model request may go without sending anything; 0 waits for ever", from: fromAPIs + ": the request timeout of the OpenAI and Anthropic SDKs",
		bounded: true, min: 0, max: 1 << 30,
		num: func(o *Options) *Value[int] { return &o.RequestTimeout }},

	// The turn.
	{key: "autoCompactEnabled", kind: oBool, def: "true",
		what: "make room in the model's window when it fills up", from: fromClaudeCode + ": the settings key",
		truth: func(o *Options) *Value[bool] { return &o.AutoCompactEnabled }},
	{key: "autoCompactWindow", kind: oInt, def: "unset (automatic)",
		what: "tokens of context before room is made, from 100000 to 1000000", from: fromClaudeCode + ": the settings key",
		bounded: true, min: 100000, max: 1000000,
		num: func(o *Options) *Value[int] { return &o.AutoCompactWindow }},
	{key: "loopMaxTurns", kind: oInt, def: "unset (no cap)",
		what: "an optional cap on /loop; zero or less means none", from: fromPlain + ", with the words of Claude Code's --max-turns",
		num: func(o *Options) *Value[int] { return &o.LoopMaxTurns }},
	{key: "maxBackgroundTasks", kind: oInt, def: "50",
		what: "background commands that may run at once", from: fromPlain + ", with Claude Code's word for them (background tasks)",
		bounded: true, min: 1, max: 100000,
		num: func(o *Options) *Value[int] { return &o.MaxBackgroundTasks }},

	// What the model reads.
	{key: "userInstructionFiles", kind: oPathList, def: "none beyond your GNOSH.md and AGENTS.md",
		what: "more instruction files of your own, read in every project: files, or folders of Markdown files", from: fromPlain,
		trust: KindExtraPath,
		list:  func(o *Options) *[]ListItem { return &o.UserInstructionFiles }},
	{key: "projectInstructionFiles", kind: oPathList, def: "none beyond the usual names",
		what: "more instruction files of the project", from: fromPlain,
		trust: KindExtraPath, fromCwd: true,
		list: func(o *Options) *[]ListItem { return &o.ProjectInstructionFiles }},
	{key: "hiddenSkills", kind: oNameList, def: "none",
		what: "skills to hide from the model", from: fromPlain,
		trust: KindHiddenSkill,
		list:  func(o *Options) *[]ListItem { return &o.HiddenSkills }},

	// mem.
	{key: "memoryText", kind: oEnum, def: "lean",
		what: "which of gnosh's two texts about mem the model is given: the short one, or the one that says everything", from: fromPlain,
		enum: []string{"lean", "full"},
		text: func(o *Options) *Value[string] { return &o.MemoryText }},
	{key: "memReviewCloses", kind: oBool, def: "true",
		what: "review a todo when it is closed", from: fromPlain,
		truth: func(o *Options) *Value[bool] { return &o.MemReviewCloses }},
	{key: "memEmbeddingsUrl", kind: oURL, def: "unset",
		what: "the embedding server for mem; unset sends nothing anywhere", from: fromPlain + ", with the APIs' word (embeddings)",
		trust: KindAddress,
		text:  func(o *Options) *Value[string] { return &o.MemEmbeddingsURL }},

	// The web.
	{key: "webSearchUrl", kind: oURL, def: "unset (no WebSearch tool)",
		what: "the SearXNG server the WebSearch tool asks, with its JSON format on; unset, there is no WebSearch tool", from: fromPlain + ", with Claude Code's name for the tool",
		trust: KindAddress,
		text:  func(o *Options) *Value[string] { return &o.WebSearchURL }},

	// What the agent hands over.
	{key: "deliverablesServer", kind: oListen, def: DefaultDeliverablesServer,
		what: "where the files the agent delivers are served, to this machine only: an address of this machine with a port, or \"off\" to serve none",
		from: fromPlain + ", with the word the Deliver tool's text uses (deliverables)",
		text: func(o *Options) *Value[string] { return &o.DeliverablesServer }},

	// gnosh itself.
	{key: "debug", kind: oBool, def: "false",
		what: "write more to the log", from: fromClaudeCode + ": the --debug flag",
		truth: func(o *Options) *Value[bool] { return &o.Debug }},
	{key: "sidePanel", kind: oBool, def: "false",
		what: "start the interactive session with the side panel on: the files it changed and its deliverables beside the conversation, on the whole screen (/panel turns it on and off)", from: fromPlain,
		truth: func(o *Options) *Value[bool] { return &o.SidePanel }},
	{key: "verbose", kind: oBool, def: "false",
		what: "show the model's reasoning and every call's whole output in the conversation as they come, not folded or cut short (the transcript view, Ctrl+O, shows them whole either way)", from: fromClaudeCode + ": the settings key",
		truth: func(o *Options) *Value[bool] { return &o.Verbose }},
	{key: "projectDataDir", kind: oPath, def: ".gnosh",
		what: "the name of the project's data folder", from: fromPlain,
		text: func(o *Options) *Value[string] { return &o.ProjectDataDir }},

	// How the model replies.
	{key: "outputStyle", kind: oName, def: "default",
		what: "how the model replies: Proactive, Concise, Explanatory, Learning, or a style of your own (/output-style switches it)",
		from: fromClaudeCode + ": the settings key",
		text: func(o *Options) *Value[string] { return &o.OutputStyle }},

	// The box.
	{key: "editorMode", kind: oEnum, def: "normal",
		what: "how the box takes keys: \"normal\" (readline's keys) or \"vim\" (vim's NORMAL, INSERT and VISUAL modes); not from a project's file",
		from: fromClaudeCode + ": the settings key", enum: []string{"normal", "vim"}, ownOnly: true,
		text: func(o *Options) *Value[string] { return &o.EditorMode }},
	{key: "preferredNotifChannel", kind: oEnum, def: "auto",
		what: "how a person who seems to be away is told the session waits: \"auto\" (a desktop note where the terminal is known to take one), \"terminal_bell\" (the bell, which reaches a terminal over ssh and through tmux too), \"iterm2\", \"iterm2_with_bell\", \"kitty\" or \"ghostty\" (that terminal's desktop note, for one gnosh cannot see, over ssh), or \"notifications_disabled\"; not from a project's file",
		from: fromClaudeCode + ": the settings key", enum: []string{"auto", "terminal_bell", "iterm2", "iterm2_with_bell", "kitty", "ghostty", "notifications_disabled"}, ownOnly: true,
		text: func(o *Options) *Value[string] { return &o.PreferredNotifChannel }},
	{key: "vimInsertModeRemaps", kind: oNameList, def: "none",
		what: "pairs of characters that, typed within a second of each other in vim's INSERT mode, stand for Esc, as [\"jj\"]; not from a project's file",
		from: fromClaudeCode + ": the settings key, as a list, since Esc is the one thing a pair can stand for", ownOnly: true,
		each: func(v string) string {
			if utf8.RuneCountInString(v) != 2 || strings.TrimSpace(v) != v {
				return "is two characters that are not blanks"
			}
			return ""
		},
		list: func(o *Options) *[]ListItem { return &o.VimInsertModeRemaps }},
}

// findOption returns the description of an option key, or nil.
func findOption(key string) *optionDef {
	for i := range optionDefs {
		if optionDefs[i].key == key {
			return &optionDefs[i]
		}
	}
	return nil
}

// optionKeys lists the option keys for a message.
func optionKeys() []string {
	keys := make([]string, len(optionDefs))
	for i, o := range optionDefs {
		keys[i] = o.key
	}
	return keys
}

// ParseOption reads what a person typed as an option's value ("/config
// memoryText=full") into the JSON value a config file's "options" would
// hold, checked as gnosh.json's reader checks it. Quotes around the text
// are taken off.
func ParseOption(key, text string) (json.RawMessage, error) {
	def := findOption(key)
	if def == nil {
		return nil, unknownOption(key)
	}
	t := strings.TrimSpace(text)
	if len(t) >= 2 && (t[0] == '"' && t[len(t)-1] == '"' || t[0] == '\'' && t[len(t)-1] == '\'') {
		t = t[1 : len(t)-1]
	}
	var v any = t
	switch def.kind {
	case oPathList, oNameList:
		return nil, fmt.Errorf("%s holds a list, and a list is set in gnosh.json: \"options\": {%q: [...]}", key, key)
	case oBool:
		switch t {
		case "true":
			v = true
		case "false":
			v = false
		default:
			return nil, fmt.Errorf("%s is true or false, and %s is neither", key, showName(t))
		}
	case oInt:
		n, err := strconv.Atoi(t)
		if err != nil {
			return nil, fmt.Errorf("%s is a whole number, written with digits alone, and %s is not one", key, showName(t))
		}
		v = n
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if err := CheckOption(key, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// CheckOption says what is wrong with raw as the value of an option, in
// the words gnosh.json's reader would use, or nil.
func CheckOption(key string, raw json.RawMessage) error {
	if findOption(key) == nil {
		return unknownOption(key)
	}
	_, err := optionOps(key, raw, &source{probs: &problems{}, secrets: &secrets{}})
	return err
}

// unknownOption is the mistake of naming an option gnosh does not have.
func unknownOption(key string) error {
	return fmt.Errorf("%s is not an option gnosh knows. The options are: %s", showName(key), list(optionKeys()))
}

// optionOps reads one option's value with s, as a config file's "options"
// would hold it, and gives the ops it makes or the first mistake.
func optionOps(key string, raw json.RawMessage, s *source) ([]op, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("the value given for %s is not one JSON value", showName(key))
	}
	name, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	mark := len(s.probs.list)
	readFile(s, []byte(`{"options": {`+string(name)+`: `+string(raw)+`}}`))
	if len(s.probs.list) > mark {
		return nil, errors.New(s.probs.list[mark].Message)
	}
	return s.ops, nil
}

// choices are the values an option takes, where they are few: true and
// false, or the words of an option that takes one of a few.
func (o *optionDef) choices() []string {
	switch o.kind {
	case oBool:
		return []string{"true", "false"}
	case oEnum:
		return slices.Clone(o.enum)
	}
	return nil
}

// takes says in a few words what values the option takes.
func (o *optionDef) takes() string {
	switch o.kind {
	case oBool:
		return "true or false"
	case oInt:
		switch {
		case o.bounded && o.max < 1<<30:
			return fmt.Sprintf("a whole number from %d to %d", o.min, o.max)
		case o.bounded:
			return fmt.Sprintf("a whole number of at least %d", o.min)
		}
		return "a whole number"
	case oEnum:
		return "one of " + strings.Join(o.enum, ", ")
	case oURL:
		return "an http or https address"
	case oListen:
		return "an address of this machine with a port, or " + ServerOff
	case oPath:
		return "a path"
	case oPathList:
		return "a list of paths, set in gnosh.json"
	case oName:
		return "a name"
	}
	return "a list of names, set in gnosh.json"
}

// trustKind is what a setting of this option counts as when the folder it
// is in is not trusted.
func (o *optionDef) trustKind() string {
	if o.trust != "" {
		return o.trust
	}
	return KindOption
}

// reset puts an option back as it is when nothing sets it.
func (o *optionDef) reset(opts *Options) {
	def := defaultOptions()
	switch {
	case o.truth != nil:
		*o.truth(opts) = *o.truth(&def)
	case o.num != nil:
		*o.num(opts) = *o.num(&def)
	case o.text != nil:
		*o.text(opts) = *o.text(&def)
	case o.list != nil:
		*o.list(opts) = nil
	}
}

// checkURL says what is wrong with an address, or "".
func checkURL(s string) string {
	if s == "" {
		return "it is empty"
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return "it must start with http:// or https://"
	}
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return "it cannot be read as an address"
	case u.Hostname() == "":
		return "it names no host"
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return "its port is not a number from 1 to 65535"
		}
	}
	return ""
}

// checkListen reads where a server of gnosh's own is to listen: the word
// off, or an address of this machine alone with a port. It returns the
// value as it is kept, or what is wrong with it. Port 0 means any port
// that is free.
func checkListen(s string) (value, wrong string) {
	if strings.EqualFold(strings.TrimSpace(s), ServerOff) {
		return ServerOff, ""
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" {
		return "", "it must be an address of this machine with a port, such as " + DefaultDeliverablesServer + ", or \"" + ServerOff + "\""
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 || strconv.Itoa(n) != port {
		return "", "its port is not a number from 0 to 65535"
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", "it must be an address of this machine alone (127.0.0.1, ::1 or localhost): gnosh serves what it serves to this machine only, and from another machine it is reached through an ssh port forward"
	}
	return net.JoinHostPort(host, port), ""
}

// expandHome replaces a leading ~ or ~/ with the home directory.
func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// absPath makes path absolute: a leading ~ is the home directory, and a
// relative path is under base.
func absPath(path, home, base string) string {
	path = expandHome(path, home)
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}
