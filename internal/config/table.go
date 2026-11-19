package config

import (
	"slices"
	"strings"
)

// This file is the one table of what the configuration can say: every key
// of gnosh.json, every hook event, provider type and environment variable,
// each with what it is for and where its name comes from. The reader works
// from these tables and nothing else, so a word that is not here is a
// startup error. (The options are the other half of the table, in
// options.go, beside the fields they set.)
//
// Every name was chosen by one rule, in this order: the key or the
// variable Claude Code documents for the same thing, where there is one;
// else the public term of the model providers' APIs and of their SDKs;
// else plain words for what the thing does. The from of each entry says
// which, and spec/DECISIONS.md items 26 and 27 give the reasons for the
// names that were changed or weighed.

// Where a name comes from, in the words the table uses.
const (
	fromClaudeCode = "Claude Code"
	fromAPIs       = "provider APIs"
	fromPlain      = "plain words"
)

// Feature is a part of gnosh that some settings are for. The program that
// calls Load says in Inputs.Built which parts it has. A setting for a part
// it does not have would look configured and do nothing, so it stops
// startup instead.
type Feature string

const (
	// FeatureHooks is the hook runner.
	FeatureHooks Feature = "hooks"
	// FeaturePermissions is permission rules and modes.
	FeaturePermissions Feature = "permissions"
	// FeatureHosts is running commands on declared machines.
	FeatureHosts Feature = "hosts"
	// FeatureEnv is the env block reaching the commands and hooks gnosh
	// starts.
	FeatureEnv Feature = "env"
)

// featureWords says what each Feature is in a message.
var featureWords = map[Feature]string{
	FeatureHooks:       "hooks",
	FeaturePermissions: "permission rules",
	FeatureHosts:       "commands on other machines (hosts)",
	FeatureEnv:         "support for the env block",
}

// keyDef describes one key the configuration can hold.
type keyDef struct {
	key string
	// feature is the part of gnosh the key is for, or "".
	feature Feature
	// what says what it is for, in a few words.
	what string
	// from says where the name comes from.
	from string
}

// keyNames lists the keys of a table for a message.
func keyNames(defs []keyDef) string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		if d.key != "$schema" {
			out = append(out, d.key)
		}
	}
	return list(out)
}

// hasKey reports whether a table holds a key.
func hasKey(defs []keyDef, key string) bool {
	return slices.ContainsFunc(defs, func(d keyDef) bool { return d.key == key })
}

// topKeys is every key a config file may hold at its top. Any other key is
// a startup error.
var topKeys = []keyDef{
	{"providers", "", "the model servers, each under an id of the person's choosing", fromPlain},
	{"models", "", "what gnosh should know about a model, under \"<provider>/<model id>\"", fromPlain},
	{"model", "", "the session's model: \"<provider>/<model id>\", or an object that also says how it is asked", fromClaudeCode + ": the settings key"},
	{"smallModel", "", "the model for the short side jobs (titles, summaries, checks); the session's model does them when none is named", fromPlain + ", after \"model\""},
	{"fallbackModel", "", "the models a turn goes on with when the session's model cannot answer: \"<provider>/<model id>\", or a list of up to three, tried in order", fromClaudeCode + ": the settings key"},
	{"hosts", FeatureHosts, "other machines the model may run commands on, each under a name", fromPlain},
	{"options", "", "gnosh's own switches and limits", fromPlain},
	{"permissions", FeaturePermissions, "allow, ask and deny rules, the starting mode, more directories", fromClaudeCode + ": the settings key"},
	{"hooks", FeatureHooks, "hooks, in Claude Code's shape", fromClaudeCode + ": the settings key"},
	{"env", FeatureEnv, "variables for the commands and hooks gnosh starts", fromClaudeCode + ": the settings key"},
	{"disableAllHooks", "", "switch every hook off", fromClaudeCode + ": the settings key"},
	{"agent", "", "the kind of agent the session runs as: its prompt, its tools and its model", fromClaudeCode + ": the settings key"},
	{"statusLine", "", "a command whose output is the status line under the prompt", fromClaudeCode + ": the settings key"},
	{"fileSuggestion", "", "a command that offers the paths an @ mention completes to, in place of gnosh's own search", fromClaudeCode + ": the settings key"},
	{"attribution", "", "the lines the model ends its commits' messages and its pull requests' descriptions with: {\"commit\": ..., \"pr\": ...}; none by default", fromClaudeCode + ": the settings key"},
	{"includeCoAuthoredBy", "", "Claude Code's older word for leaving its attribution out: taken, and changes nothing, as gnosh adds none of its own", fromClaudeCode + ": the settings key"},
	{"skillOverrides", "", "how far each skill is seen, by its name: on, name-only, user-invocable-only or off", fromClaudeCode + ": the settings key"},
	{"$schema", "", "accepted and not read", fromClaudeCode + ": the settings key"},
}

// providerFields is every field of a provider.
var providerFields = []keyDef{
	{"type", "", "which kind of server it is: " + list(providerTypeNames()), fromPlain},
	{"name", "", "the name shown to the person; the id when none is given", fromPlain},
	{"baseUrl", "", "where the API is: one address, or a list tried in order, the first that answers being used", fromAPIs + ": the base URL"},
	{"apiKey", "", "the key; none for a server on the person's own machine", fromAPIs + ": the API key"},
	{"headers", "", "more request headers, as an object of names and values; an empty value is not sent", fromClaudeCode + ": the key of .mcp.json for a server's request headers"},
	{"extraBody", "", "an object merged into the body of every request", fromAPIs + ": the extra_body of the OpenAI and Anthropic SDKs"},
	{"listModels", "", "ask the server which models it has; when not given, it is asked exactly when no model is registered for it", fromAPIs + ": the request they call \"list models\""},
	{"disabled", "", "keep the entry and do not use it", fromPlain},
}

// modelFields is every field of a registered model.
var modelFields = []keyDef{
	{"name", "", "the name shown to the person; the model's id when none is given", fromPlain},
	{"contextWindow", "", "the model's window, in tokens", fromAPIs + ": the context window"},
	{"maxOutputTokens", "", "the reply limit sent with a request", fromAPIs + ": the max output tokens, and Claude Code's CLAUDE_CODE_MAX_OUTPUT_TOKENS"},
	{"thinkingLevels", "", "the model's thinking levels, the least thinking first; an empty list says it has no switch", fromPlain},
	{"thinkingBudget", "", "the most tokens the model may think before a reply, 0 for no cap; 4096 for a llama.cpp server's model when not given", fromClaudeCode + ": its MAX_THINKING_TOKENS, for one model"},
}

// slotFields is every field of "model" and "smallModel" written as an
// object.
var slotFields = []keyDef{
	{"id", "", "the model, as \"<provider>/<model id>\"", fromPlain},
	{"thinking", "", "true, false, or one of the model's own levels such as \"low\"", fromPlain},
	{"temperature", "", "the sampling temperature, from 0 to 2", fromAPIs + ": the temperature"},
}

// hostFields is every field of a host.
var hostFields = []keyDef{
	{"ssh", FeatureHosts, "the destination ssh is given: user@address, or a Host alias of the person's ssh configuration", fromPlain + ": the program's own name"},
	{"description", FeatureHosts, "what the machine is for, in the person's words", fromPlain},
}

// permissionKeys is every key the permissions object may hold.
var permissionKeys = []keyDef{
	{"allow", FeaturePermissions, "rules that run without asking", fromClaudeCode},
	{"ask", FeaturePermissions, "rules that always ask", fromClaudeCode},
	{"deny", FeaturePermissions, "rules that always refuse", fromClaudeCode},
	{"defaultMode", FeaturePermissions, "the mode a session starts in", fromClaudeCode},
	{"additionalDirectories", FeaturePermissions, "more directories the file tools may use", fromClaudeCode},
	{"disableBypassPermissionsMode", FeaturePermissions, "\"disable\" forbids skipping permissions", fromClaudeCode},
	{"disableAutoMode", "", "\"disable\"; gnosh has no auto mode, so it changes nothing", fromClaudeCode},
	{"denyInsteadOfPrompting", FeaturePermissions, "refuse what would ask; not from a project's file", fromClaudeCode},
}

// claudeCodeKeys are the settings keys Claude Code documents that gnosh
// has not built (spec/cc/settings.md). One of them in a config file stops
// startup like any other key gnosh does not read, and the message says
// whose key it is. Those that gnosh has under "options", with Claude
// Code's names and meaning, are in the options table instead.
var claudeCodeKeys = []string{
	"cleanupPeriodDays",
	"spinnerVerbs", "spinnerTipsEnabled",
	"skipDangerousModePermissionPrompt",
	"effortLevel", "alwaysThinkingEnabled", "showThinkingSummaries", "plansDirectory",
	"autoMemoryEnabled", "autoMemoryDirectory", "idleCompaction", "tui", "enabledMcpjsonServers",
	"disabledMcpjsonServers", "enableAllProjectMcpServers", "allowedMcpServers", "deniedMcpServers",
	"allowedHttpHookUrls", "httpHookAllowedEnvVars", "worktree", "sandbox",
	"apiKeyHelper", "awsAuthRefresh", "forceLoginMethod", "allowedProviders", "availableModels", "modelPicker",
	"modelPricing", "advisorModel", "fastMode", "enabledPlugins", "extraKnownMarketplaces",
	"strictKnownMarketplaces", "pluginConfigs", "feedbackDrafts", "feedbackSurveyRate", "autoMode",
	"useAutoModeDuringPlan",
}

// claudeCodePermissionKeys are the keys of Claude Code's permissions object
// that gnosh has not built (spec/cc/permissions.md).
var claudeCodePermissionKeys = []string{"blockReadsOutsideWorkingDirectories", "blockWritesOutsideWorkingDirectories"}

// providerType describes one value of a provider's "type".
type providerType struct {
	name string
	// wire is the request format the type speaks: "llamacpp", "openai" or
	// "anthropic".
	wire string
	// v1 says a base URL of this type is completed with /v1: always, for a
	// server on the user's own machine, or only when the URL has no path.
	v1 string // "always", "bare" or ""
	// url is the service's public address, used when none is given.
	url string
	// keyVar is the environment variable read for the key when the
	// configuration gives none.
	keyVar string
	// what says which server it is.
	what string
	// from says where the name comes from.
	from string
}

// providerTypes is every value of a provider's "type": three servers
// under their own names, and the format every other server speaks, under
// the name such servers give themselves. A server that speaks OpenAI's
// format needs no type of its own (spec/DECISIONS.md, 26 and 27).
var providerTypes = []providerType{
	{name: "llamacpp", wire: "llamacpp", v1: "always", what: "llama.cpp's server", from: fromPlain + ": the server's own name"},
	{name: "openai-compatible", wire: "openai", v1: "bare", what: "any server that speaks OpenAI's chat-completions format", from: fromAPIs + ": what servers that speak OpenAI's format call themselves"},
	{name: "anthropic", wire: "anthropic", url: "https://api.anthropic.com", keyVar: "ANTHROPIC_API_KEY", what: "Anthropic's API", from: fromPlain + ": the provider's own name"},
	{name: "openai", wire: "openai", v1: "bare", url: "https://api.openai.com/v1", keyVar: "OPENAI_API_KEY", what: "OpenAI's API", from: fromPlain + ": the provider's own name"},
}

// providerTypeNames lists the provider types for a message.
func providerTypeNames() []string {
	out := make([]string, len(providerTypes))
	for i, t := range providerTypes {
		out[i] = t.name
	}
	return out
}

// findProviderType returns the description of a provider type, or nil.
func findProviderType(name string) *providerType {
	for i := range providerTypes {
		if providerTypes[i].name == name {
			return &providerTypes[i]
		}
	}
	return nil
}

// hookEvent describes one hook event gnosh knows.
type hookEvent struct {
	name string
	// matcher says what a matcher is tested against, or "" for an event
	// that has nothing to match, where a matcher is a mistake.
	matcher string
	// blocks reports whether a hook that cannot run holds work up: every
	// matching call, every prompt, or the end of every turn.
	blocks bool
}

// hookEvents is the events gnosh has, in the spec's order. Names are exact:
// no other letter case and no underscore form is an event.
var hookEvents = []hookEvent{
	{"PreToolUse", "the tool's name", true},
	{"PostToolUse", "the tool's name", false},
	{"PostToolUseFailure", "the tool's name", false},
	{"UserPromptSubmit", "", true},
	{"SessionStart", "the source", false},
	{"Stop", "", true},
	{"SubagentStart", "the sub-agent's type", false},
	{"SubagentStop", "", true},
	{"StopFailure", "the error's kind", false},
	{"PreCompact", "the trigger", false},
	{"PostCompact", "the trigger", false},
	{"SessionEnd", "the reason", false},
	{"InstructionsLoaded", "the load reason", false},
	{"PermissionRequest", "the tool's name", false},
	{"Notification", "the type", false},
	{"PreModelSwitch", "the model switched to", true},
	{"PostModelSwitch", "the model switched to", false},
	{"DirectoryAdded", "the source", false},
	{"CwdChanged", "", false},
	{"UserPromptExpansion", "the command's name", true},
	{"ConfigChange", "the source", true},
	{"PostToolBatch", "", true},
	{"FileChanged", "the file's name", false},
	{"Elicitation", "the MCP server's name", true},
	{"ElicitationResult", "the MCP server's name", true},
}

// HookEventNames returns the names of the hook events gnosh knows, in the
// spec's order.
func HookEventNames() []string {
	out := make([]string, len(hookEvents))
	for i, e := range hookEvents {
		out[i] = e.name
	}
	return out
}

// findHookEvent returns the description of a hook event, or nil.
func findHookEvent(name string) *hookEvent {
	for i := range hookEvents {
		if hookEvents[i].name == name {
			return &hookEvents[i]
		}
	}
	return nil
}

// nearHookEvent finds the event a misspelt name most likely meant: the one
// with the same letters once case and underscores are set aside.
func nearHookEvent(name string) string {
	fold := func(s string) string {
		return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(s))
	}
	for _, e := range hookEvents {
		if fold(e.name) == fold(name) {
			return e.name
		}
	}
	return ""
}

// hookFields is what gnosh does with each field a Claude Code hook handler
// can carry. "" means the field is read; "same" means it is accepted when
// it has the value gnosh would use anyway, given in hookFieldSame; any
// other text is the reason the field stops startup.
var hookFields = map[string]string{
	"type":          "",
	"command":       "",
	"timeout":       "",
	"statusMessage": "",
	"once":          "ignored",
	"shell":         "same",
	"async":         "",
	"asyncRewake":   "",
	"onFailure":     "same",
	"if":            "",
	"args":          "",
	// The fields of an http hook, and of an mcp_tool hook.
	"url":            "",
	"headers":        "",
	"allowedEnvVars": "",
	"server":         "",
	"tool":           "",
	"input":          "",
	// The fields of a prompt hook (prompt and model those of an agent hook
	// too).
	"prompt":          "",
	"model":           "",
	"continueOnBlock": "",
}

// hookFieldSame is the one value each "same" field may have.
var hookFieldSame = map[string]string{
	"shell":     `"bash"`,
	"onFailure": `"continue"`,
}

// permissionModes is every mode a session can start in. "manual" is
// another word for "default".
var permissionModes = []string{"default", "plan", "acceptEdits", "dontAsk", "bypassPermissions"}

// envVar describes an environment variable gnosh reads or sets.
type envVar struct {
	name string
	use  string // "reads", "sets" or "tests"
	what string
	from string
}

// ownEnv is every GNOSH_ variable gnosh knows. Any other GNOSH_ name in the
// environment is a startup error: it is most likely a misspelling of one of
// these, and would otherwise do nothing without a word.
var ownEnv = []envVar{
	{"GNOSH_CONFIG_DIR", "reads", "the directory to use in place of the user's config directory", fromClaudeCode + ": CLAUDE_CONFIG_DIR"},
	{"GNOSH_DATA_DIR", "reads", "the directory to use in place of the user's state directory", fromPlain + ", after GNOSH_CONFIG_DIR"},
	{"GNOSH_SAFE_MODE", "reads", "the same as --safe-mode", fromClaudeCode + ": CLAUDE_CODE_SAFE_MODE"},
	{"GNOSH_DEBUG_PAGE", "reads", "serve a debugging page on loopback", fromPlain},
	{"GNOSH_PRINT_BG_CHECKPOINT_MS", "reads", "in -p, how long the idle wait for background work lasts before the model is asked whether to wait on (default ten minutes)", fromPlain},
	{"GNOSH_PRINT_MAX_DURATION_MS", "reads", "in -p, the run's time budget: the model is told to wrap up once 80% of it is used", fromPlain},
	// What gnosh sets for the commands and hooks it starts. A gnosh started
	// from inside one of those inherits them. A hook's variables are named
	// for the fields of Claude Code's hook input, in upper case behind
	// GNOSH_ (the hooks package says how).
	{"GNOSH", "sets", "1 in every command gnosh runs", ""},
	{"GNOSH_HOOK_EVENT_NAME", "sets", "a hook's event", ""},
	{"GNOSH_SESSION_ID", "sets", "the session", ""},
	{"GNOSH_CWD", "sets", "the working directory", ""},
	{"GNOSH_PROJECT_DIR", "sets", "the project root", ""},
	{"GNOSH_TOOL_NAME", "sets", "a tool event's tool", ""},
	{"GNOSH_TOOL_USE_ID", "sets", "a tool event's call", ""},
	{"GNOSH_TOOL_INPUT_COMMAND", "sets", "a shell call's command", ""},
	{"GNOSH_TOOL_INPUT_FILE_PATH", "sets", "a file tool's path", ""},
	{"GNOSH_STOP_HOOK_ACTIVE", "sets", "1 when a Stop hook is keeping the turn going", ""},
	{"GNOSH_AGENT_ID", "sets", "a sub-agent's id", ""},
	{"GNOSH_AGENT_TYPE", "sets", "a sub-agent's type", ""},
	{"GNOSH_SESSION_START_SOURCE", "sets", "SessionStart's source", ""},
	{"GNOSH_SESSION_END_REASON", "sets", "SessionEnd's reason", ""},
	{"GNOSH_PRE_COMPACT_TRIGGER", "sets", "PreCompact's trigger", ""},
	{"GNOSH_POST_COMPACT_TRIGGER", "sets", "PostCompact's trigger", ""},
	{"GNOSH_STOP_FAILURE_ERROR", "sets", "StopFailure's error kind", ""},
	{"GNOSH_INSTRUCTIONS_LOAD_REASON", "sets", "InstructionsLoaded's reason", ""},
	{"GNOSH_NOTIFICATION_TYPE", "sets", "Notification's type", ""},
	{"GNOSH_ENV_FILE", "sets", "SessionStart's environment file", ""},
	{"GNOSH_MEM_STATUS", "sets", "the file that says what mem holds", ""},
	// Switches read by gnosh's own tests and development scripts.
	{"GNOSH_PEER_CHILD", "tests", "a test switch", ""},
	{"GNOSH_PEERS_SWARM", "tests", "a test switch", ""},
	{"GNOSH_PEER_WORK", "tests", "a test switch", ""},
	{"GNOSH_IGNORE_ROUNDS", "tests", "a test switch", ""},
	{"GNOSH_IGNORE_SEED", "tests", "a test switch", ""},
}

// ownEnvPrefixes are the families of names kept for tests and development
// scripts. A name under one is never a mistake.
var ownEnvPrefixes = []string{"GNOSH_TEST_", "GNOSH_DEV_"}

// knownOwnEnv reports whether name is a GNOSH_ variable gnosh knows.
func knownOwnEnv(name string, more []string) bool {
	for _, p := range ownEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	if slices.Contains(more, name) {
		return true
	}
	for _, v := range ownEnv {
		if v.name == name {
			return true
		}
	}
	return false
}

// runTimeVariable reports whether gnosh itself sets the variable for the
// hooks and commands it runs, so that its value belongs to one run of one
// of those and never to a config file.
func runTimeVariable(name string) bool {
	if name == "CLAUDE_PROJECT_DIR" || name == "CLAUDE_ENV_FILE" {
		return true
	}
	for _, v := range ownEnv {
		if v.name == name {
			return v.use == "sets"
		}
	}
	return false
}

// readEnvNames lists the GNOSH_ variables gnosh reads, for a message.
func readEnvNames() []string {
	var out []string
	for _, v := range ownEnv {
		if v.use == "reads" {
			out = append(out, v.name)
		}
	}
	return out
}

// Entry is one line of the table of what the configuration can say.
type Entry struct {
	// Name is the key, the value or the variable: "providers.<id>.baseUrl",
	// "options.requestTimeout", "provider type llamacpp", "$GNOSH_CONFIG_DIR".
	Name string
	// What says what it is for.
	What string
	// From says where the name comes from: Claude Code's documentation, the
	// model providers' APIs and SDKs, or plain words.
	From string
	// Feature is the part of gnosh it needs, or "". A setting for a part
	// the calling program does not have stops startup.
	Feature Feature
}

// Table lists everything the configuration can say: every key, provider
// type, hook event and environment variable, with what each is for and
// where its name comes from. It is made from the tables the reader works
// from, so it cannot disagree with them.
func Table() []Entry {
	var out []Entry
	add := func(prefix string, defs []keyDef) {
		for _, d := range defs {
			out = append(out, Entry{Name: prefix + d.key, What: d.what, From: d.from, Feature: d.feature})
		}
	}
	add("", topKeys)
	add("providers.<id>.", providerFields)
	for _, t := range providerTypes {
		out = append(out, Entry{Name: "provider type " + t.name, What: t.what + ", in the " + t.wire + " format", From: t.from})
	}
	add("models.<provider>/<model id>.", modelFields)
	add("model.", slotFields)
	add("smallModel.", slotFields)
	add("hosts.<name>.", hostFields)
	for _, o := range optionDefs {
		out = append(out, Entry{Name: "options." + o.key, What: o.what + " (default: " + o.def + ")", From: o.from})
	}
	add("permissions.", permissionKeys)
	for _, e := range hookEvents {
		out = append(out, Entry{Name: "hook event " + e.name, What: "a moment hooks can run at", From: fromClaudeCode, Feature: FeatureHooks})
	}
	for _, v := range ownEnv {
		if v.use == "reads" {
			out = append(out, Entry{Name: "$" + v.name, What: v.what, From: v.from})
		}
	}
	for _, v := range limitVars {
		out = append(out, Entry{Name: "$" + v.name, What: v.what, From: v.from})
	}
	return out
}
