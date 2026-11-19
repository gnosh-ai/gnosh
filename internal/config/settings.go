package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// This file reads the keys a config file shares with Claude Code's
// settings files, in Claude Code's shape and with its meaning:
// permissions, hooks and env. A block copied out of a Claude Code settings
// file works as it is.

// settingsPermissions reads the permissions object.
func (s *source) settingsPermissions(f jfield) {
	if f.val.kind != jObject {
		s.errorf(f.line, "\"permissions\" is an object holding lists named allow, ask and deny, and here it is %s, so none of it is in force.", f.val.kind.word())
		return
	}
	for _, m := range s.unique(f.val, "permissions.") {
		o := s.at(m.line)
		switch m.key {
		case "allow", "ask", "deny":
			rules, ok := m.val.strings()
			if !ok {
				s.errorf(m.line, "\"permissions.%s\" is a list of rules, each a string such as \"Bash(git diff *)\", and here it is not, so none of its rules is in force.", m.key)
				continue
			}
			for _, r := range rules {
				s.rule(m.key, r.str, s.at(r.line))
			}
		case "defaultMode":
			s.settingsMode(m)
		case "additionalDirectories":
			dirs, ok := m.val.strings()
			if !ok {
				s.errorf(m.line, "\"permissions.additionalDirectories\" is a list of directories, each a string, and here it is not.")
				continue
			}
			for _, d := range dirs {
				if d.str == "" {
					s.errorf(d.line, "an entry of \"permissions.additionalDirectories\" is empty.")
					continue
				}
				dir, at := absPath(d.str, s.home, s.cwd), s.at(d.line)
				s.add(op{kind: KindAddDir, text: "additional directory " + dir, origin: at, feature: FeaturePermissions, what: "an additional directory",
					apply: func(b *builder) { addItem(&b.dirs, dir, at) }})
			}
		case "disableBypassPermissionsMode":
			if m.val.kind != jString || m.val.str != "disable" {
				s.errorf(m.line, "\"permissions.disableBypassPermissionsMode\" takes the one value \"disable\".")
				continue
			}
			// (It only takes a mode away, so a folder that is not trusted
			// keeps it too, as DECISIONS 2 says; the config review of
			// 2026-11-19 found it held as a mode.)
			s.add(op{kind: KindRestriction, text: "disableBypassPermissionsMode", origin: o, feature: FeaturePermissions, what: "a permission setting",
				apply: func(b *builder) { b.bypassDisabled.put(true, o) }})
		case "disableAutoMode":
			if m.val.kind != jString || m.val.str != "disable" {
				s.errorf(m.line, "\"permissions.disableAutoMode\" takes the one value \"disable\".")
			}
		case "denyInsteadOfPrompting":
			if m.val.kind != jBool {
				s.errorf(m.line, "\"permissions.denyInsteadOfPrompting\" is true or false, and here it is %s.", m.val.kind.word())
				continue
			}
			if s.scope == "project" || s.scope == "local" {
				s.probs.warnf(s.file, m.line, "\"permissions.denyInsteadOfPrompting\" is not taken from a project's file, so it is not in force here. Set it in your own gnosh.json, or with --settings.")
				continue
			}
			v := m.val.b
			s.add(op{kind: KindMode, text: "denyInsteadOfPrompting", origin: o, feature: FeaturePermissions, what: "a permission setting",
				apply: func(b *builder) { b.denyInstead.put(v, o) }})
		default:
			if slices.Contains(claudeCodePermissionKeys, m.key) {
				s.errorf(m.line, "\"permissions.%s\" is a setting Claude Code has and gnosh has not built, so it would not hold anything back here. Take it out of this file.", m.key)
				continue
			}
			s.errorf(m.line, "%s is not a key gnosh reads under \"permissions\". The keys are: %s.", showName(m.key), keyNames(permissionKeys))
		}
	}
}

// settingsMode reads permissions.defaultMode.
func (s *source) settingsMode(m jfield) {
	if m.val.kind != jString {
		s.errorf(m.line, "\"permissions.defaultMode\" is one of %s, and here it is %s.", list(permissionModes), m.val.kind.word())
		return
	}
	mode, o := m.val.str, s.at(m.line)
	if mode == "manual" {
		mode = "default"
	}
	fromProject := s.scope == "project" || s.scope == "local"
	switch {
	case mode == "auto":
		s.probs.warnf(s.file, m.line, "\"permissions.defaultMode\" is \"auto\", which gnosh does not have (it needs a second model to review each action). The session starts in the default mode, which asks more, not less.")
		return
	case !slices.Contains(permissionModes, mode):
		s.errorf(m.line, "%s is not a permission mode. \"permissions.defaultMode\" is one of %s.", showName(mode), list(permissionModes))
		return
	case mode == "bypassPermissions" && fromProject:
		s.probs.warnf(s.file, m.line, "\"permissions.defaultMode\" is \"bypassPermissions\" in a project's file, and a project cannot switch the questions off, so it is not in force. Set it in your own gnosh.json, or start gnosh with -y.")
		return
	}
	user := s.scope == "user" || s.scope == "command line"
	s.add(op{kind: KindMode, text: "defaultMode " + mode, origin: o, feature: FeaturePermissions, what: "a starting permission mode",
		apply: func(b *builder) {
			b.mode.put(mode, o)
			if b.mode.Origin == o {
				b.modeFromUser = user
			}
		}})
}

// settingsHooks reads the hooks object: event, then matcher groups, then
// handlers.
func (s *source) settingsHooks(f jfield) {
	if f.val.kind != jObject {
		s.errorf(f.line, "\"hooks\" is an object with one key for each event, and here it is %s, so none of the file's hooks is in force.", f.val.kind.word())
		return
	}
	for _, ev := range s.unique(f.val, "hooks.") {
		e := s.hookEventFor(ev.line, ev.key)
		if e == nil {
			continue
		}
		if ev.val.kind != jArray {
			s.errorf(ev.line, "\"hooks.%s\" is a list of matcher groups, and here it is %s, so none of its hooks is in force.", e.name, ev.val.kind.word())
			continue
		}
		for _, group := range ev.val.items {
			if group.kind != jObject {
				s.errorf(group.line, "an entry of \"hooks.%s\" is an object with a \"hooks\" list and, where the event has one, a \"matcher\"; this one is %s.", e.name, group.kind.word())
				continue
			}
			matcher := ""
			var handlers *jnode
			good := true
			for _, m := range s.unique(group, "hooks."+e.name+".") {
				switch m.key {
				case "matcher":
					if m.val.kind != jString {
						s.errorf(m.line, "a \"matcher\" is one string, such as \"Edit|Write\", and here it is %s.", m.val.kind.word())
						good = false
						continue
					}
					matcher = m.val.str
					if why := checkMatcher(e, matcher); why != "" {
						s.errorf(m.line, "%s.", why)
						good = false
					}
				case "hooks":
					if m.val.kind != jArray {
						s.errorf(m.line, "the \"hooks\" of an entry is a list of handlers, and here it is %s.", m.val.kind.word())
						good = false
						continue
					}
					handlers = m.val
				default:
					s.errorf(m.line, "%s is not a key of an entry under \"hooks.%s\". An entry holds \"matcher\" and \"hooks\".", showName(m.key), e.name)
					good = false
				}
			}
			if handlers == nil && good {
				s.errorf(group.line, "an entry of \"hooks.%s\" has no \"hooks\" list, so it would run nothing.", e.name)
			}
			if handlers == nil || !good {
				continue
			}
			for _, h := range handlers.items {
				s.settingsHandler(e, matcher, h)
			}
		}
	}
}

// settingsHandler reads one hook handler.
func (s *source) settingsHandler(e *hookEvent, matcher string, n *jnode) {
	if n.kind != jObject {
		s.errorf(n.line, "a hook handler is an object with \"type\" and \"command\", and here it is %s.", n.kind.word())
		return
	}
	h := Hook{Event: e.name, Matcher: matcher, Scope: s.scope, BaseDir: s.baseDir, Origin: s.at(n.line)}
	typ, good := "", true
	bad := func(line int, format string, args ...any) {
		s.errorf(line, format, args...)
		good = false
	}
	for _, m := range s.unique(n, "") {
		how, known := hookFields[m.key]
		switch {
		case !known:
			bad(m.line, "%s is not a field of a hook handler that gnosh knows, so the hook might not run as it was written. gnosh reads \"type\", \"command\", \"args\", \"timeout\" and \"if\" (and the fields of http, mcp_tool, prompt and agent hooks).", showName(m.key))
		case m.key == "type":
			if m.val.kind != jString {
				bad(m.line, "a hook's \"type\" is a string, and here it is %s.", m.val.kind.word())
				break
			}
			if typ = m.val.str; typ != "command" && typ != "http" && typ != "mcp_tool" && typ != "prompt" && typ != "agent" {
				bad(m.line, "this hook has \"type\": %s, and gnosh runs hooks of type \"command\", \"http\", \"mcp_tool\", \"prompt\" and \"agent\" only. A hook it cannot run as written is not run some other way: rewrite it as a command, or take it out.", showName(typ))
			}
		case m.key == "command":
			if m.val.kind != jString || strings.TrimSpace(m.val.str) == "" {
				bad(m.line, "a hook's \"command\" is the command line to run, as a string that is not empty.")
				break
			}
			h.Command = m.val.str
		case m.key == "args":
			items, isList := m.val.strings()
			if !isList {
				bad(m.line, "a hook's \"args\" is a list of strings, one argument each, and here it is %s.", m.val.kind.word())
				break
			}
			h.Args = make([]string, 0, len(items))
			for _, it := range items {
				h.Args = append(h.Args, it.str)
			}
		case m.key == "url":
			if m.val.kind != jString || m.val.str == "" {
				bad(m.line, "an http hook's \"url\" is the address the event is sent to, as a string.")
				break
			}
			s.secrets.addURL(m.val.str)
			if why := checkURL(m.val.str); why != "" {
				bad(m.line, "the address of the http hook cannot be used: %s.", why)
				break
			}
			h.URL = m.val.str
		case m.key == "headers":
			if m.val.kind != jObject {
				bad(m.line, "an http hook's \"headers\" is an object of header names and their values, and here it is %s.", m.val.kind.word())
				break
			}
			h.Headers = map[string]string{}
			for _, hd := range s.unique(m.val, "headers.") {
				if hd.val.kind != jString {
					bad(hd.line, "the value of the header %s is a string, and here it is %s.", showName(hd.key), hd.val.kind.word())
					continue
				}
				s.secrets.add(hd.val.str)
				h.Headers[hd.key] = hd.val.str
			}
		case m.key == "allowedEnvVars":
			items, isList := m.val.strings()
			if !isList {
				bad(m.line, "an http hook's \"allowedEnvVars\" is a list of the names of the variables its headers may use, and here it is %s.", m.val.kind.word())
				break
			}
			for _, it := range items {
				h.AllowedEnvVars = append(h.AllowedEnvVars, it.str)
			}
		case m.key == "server" || m.key == "tool":
			if m.val.kind != jString || strings.TrimSpace(m.val.str) == "" {
				bad(m.line, "an mcp_tool hook's \"%s\" names the MCP %s it calls, as a string.", m.key, m.key)
				break
			}
			if m.key == "server" {
				h.Server = m.val.str
			} else {
				h.Tool = m.val.str
			}
		case m.key == "input":
			if m.val.kind != jObject {
				bad(m.line, "an mcp_tool hook's \"input\" is an object of the tool's arguments, and here it is %s.", m.val.kind.word())
				break
			}
			h.Input = m.val.marshal()
		case m.key == "prompt":
			if m.val.kind != jString || strings.TrimSpace(m.val.str) == "" {
				bad(m.line, "a prompt or agent hook's \"prompt\" is the rule, the condition or the task the model is given, as a string that is not empty.")
				break
			}
			h.Prompt = m.val.str
		case m.key == "model":
			if m.val.kind != jString {
				bad(m.line, "a prompt or agent hook's \"model\" is a string, and here it is %s.", m.val.kind.word())
				break
			}
			h.Model = m.val.str
		case m.key == "continueOnBlock":
			if m.val.kind != jBool {
				bad(m.line, "a prompt hook's \"continueOnBlock\" is true or false, and here it is %s.", m.val.kind.word())
				break
			}
			h.ContinueOnBlock = m.val.b
		case m.key == "statusMessage":
			if m.val.kind != jString {
				bad(m.line, "a hook's \"statusMessage\" is the text shown while it runs, as a string, and here it is %s.", m.val.kind.word())
				break
			}
			h.StatusMessage = m.val.str
		case m.key == "async" || m.key == "asyncRewake":
			if m.val.kind != jBool {
				bad(m.line, "a hook's \"%s\" is true or false, and here it is %s.", m.key, m.val.kind.word())
				break
			}
			if m.key == "async" {
				h.Async = m.val.b
			} else {
				h.AsyncRewake = m.val.b
			}
		case m.key == "timeout":
			secs, err := strconv.ParseFloat(m.val.str, 64)
			if m.val.kind != jNumber || err != nil || secs <= 0 || secs > 1e7 {
				bad(m.line, "a hook's \"timeout\" is a number of seconds above zero.")
				break
			}
			h.Timeout = time.Duration(secs * float64(time.Second))
		case m.key == "if":
			if m.val.kind != jString || strings.TrimSpace(m.val.str) == "" {
				bad(m.line, "a hook's \"if\" is one permission rule, as a string, such as \"Bash(git push *)\".")
				break
			}
			h.If = strings.TrimSpace(m.val.str)
		case how == "ignored":
		case how == "same":
			want := hookFieldSame[m.key]
			got := fmt.Sprintf("%q", m.val.str)
			if m.val.kind == jBool {
				got = strconv.FormatBool(m.val.b)
			}
			if (m.val.kind != jString && m.val.kind != jBool) || got != want {
				bad(m.line, "this hook sets \"%s\", and gnosh can only run a hook with \"%s\": %s (which is what leaving it out means). A hook it cannot run as written is not run some other way: take the field out, or the hook.", m.key, m.key, want)
			}
		default:
			bad(m.line, "this hook has \"%s\". %s, or take the hook out.", m.key, how)
		}
	}
	if good && typ == "" {
		bad(n.line, "this hook handler has no \"type\". Add \"type\": \"command\".")
	}
	switch {
	case !good:
	case typ == "http" && (h.Command != "" || h.Args != nil || h.Async || h.AsyncRewake):
		bad(n.line, "this http hook has a \"command\", \"args\", \"async\" or \"asyncRewake\", which only a command hook takes.")
	case typ == "http" && h.URL == "":
		bad(n.line, "this http hook has no \"url\" to send the event to.")
	case typ == "mcp_tool" && (h.Server == "" || h.Tool == ""):
		bad(n.line, "this mcp_tool hook names no \"server\" or no \"tool\" to call.")
	case typ == "mcp_tool" && (h.Command != "" || h.Args != nil || h.URL != "" || h.Async || h.AsyncRewake):
		bad(n.line, "this mcp_tool hook has fields of a command hook or an http hook, which it does not take.")
	case typ != "mcp_tool" && (h.Server != "" || h.Tool != "" || h.Input != nil):
		bad(n.line, "this hook has \"server\", \"tool\" or \"input\", which only an mcp_tool hook takes. Write \"type\": \"mcp_tool\" for one.")
	case typ == "command" && (h.URL != "" || h.Headers != nil || h.AllowedEnvVars != nil):
		bad(n.line, "this command hook has a \"url\", \"headers\" or \"allowedEnvVars\", which only an http hook takes. Write \"type\": \"http\" for one.")
	case typ == "command" && h.Command == "":
		bad(n.line, "this hook handler has no \"command\", so it would run nothing.")
	case typ == "prompt" && h.Prompt == "":
		bad(n.line, "this prompt hook has no \"prompt\": the rule or the condition the model judges.")
	case typ == "agent" && h.Prompt == "":
		bad(n.line, "this agent hook has no \"prompt\": the task its sub-agent is given.")
	case (typ == "prompt" || typ == "agent") && (h.Command != "" || h.Args != nil || h.URL != "" || h.Async || h.AsyncRewake):
		bad(n.line, "this %s hook has fields of a command hook or an http hook, which it does not take.", typ)
	case typ == "agent" && h.ContinueOnBlock:
		bad(n.line, "an agent hook takes no \"continueOnBlock\": its \"ok\": false always goes back to the model, and the turn goes on.")
	case typ != "prompt" && typ != "agent" && (h.Prompt != "" || h.Model != "" || h.ContinueOnBlock):
		bad(n.line, "this hook has \"prompt\", \"model\" or \"continueOnBlock\", which only a prompt hook (or an agent hook, for \"prompt\" and \"model\") takes. Write \"type\": \"prompt\" or \"agent\" for one.")
	}
	h.Agent = typ == "agent"
	if !good {
		return
	}
	text := h.CommandLine()
	s.add(op{kind: KindHook, text: "hook " + e.name + ": " + text, origin: h.Origin, feature: FeatureHooks, what: "a hook",
		apply: func(b *builder) { b.addHook(h) }})
}

// statusLine reads the statusLine object: {"type": "command", "command":
// "...", "padding": 2, "refreshInterval": 5}.
func (s *source) statusLine(f jfield) {
	if f.val.kind != jObject {
		s.errorf(f.line, "\"statusLine\" is an object such as {\"type\": \"command\", \"command\": \"~/.config/gnosh/statusline.sh\"}, and here it is %s.", f.val.kind.word())
		return
	}
	sl := StatusLine{Origin: s.at(f.line)}
	typ, good := "", true
	bad := func(line int, format string, a ...any) {
		s.errorf(line, format, a...)
		good = false
	}
	for _, m := range s.unique(f.val, "statusLine.") {
		switch m.key {
		case "type":
			if m.val.kind != jString || m.val.str != "command" {
				bad(m.line, "the status line's \"type\" is \"command\", the one kind there is.")
			}
			typ = "command"
		case "command":
			if m.val.kind != jString || strings.TrimSpace(m.val.str) == "" {
				bad(m.line, "the status line's \"command\" is the command line to run, as a string that is not empty.")
				break
			}
			sl.Command = m.val.str
		case "padding":
			n, err := strconv.Atoi(m.val.str)
			if m.val.kind != jNumber || err != nil || n < 0 || n > 40 {
				bad(m.line, "the status line's \"padding\" is a whole number of columns from 0 to 40.")
				break
			}
			sl.Padding = n
		case "refreshInterval":
			secs, err := strconv.ParseFloat(m.val.str, 64)
			if m.val.kind != jNumber || err != nil || secs < 1 || secs > 86400 {
				bad(m.line, "the status line's \"refreshInterval\" is a number of seconds, at least 1.")
				break
			}
			sl.Refresh = time.Duration(secs * float64(time.Second))
		default:
			bad(m.line, "the status line has no field %s; its fields are type, command, padding and refreshInterval.", showName(m.key))
		}
	}
	if good && typ == "" {
		bad(f.line, "the status line has no \"type\". Add \"type\": \"command\".")
	}
	if good && sl.Command == "" {
		bad(f.line, "the status line has no \"command\", so it would run nothing.")
	}
	if !good {
		return
	}
	s.add(op{kind: KindStatusLine, text: "statusLine: " + sl.Command, origin: sl.Origin, what: "a status line",
		apply: func(b *builder) { b.statusLine.put(sl, sl.Origin) }})
}

// attribution reads the attribution setting: {"commit": "...", "pr":
// "...", "sessionUrl": false}, or false for none. sessionUrl is taken and
// adds nothing: a gnosh session has no web address to link.
func (s *source) attribution(f jfield) {
	a := Attribution{Origin: s.at(f.line)}
	switch {
	case f.val.kind == jBool && !f.val.b:
	case f.val.kind != jObject:
		s.errorf(f.line, "\"attribution\" is an object such as {\"commit\": \"...\", \"pr\": \"...\"}, or false for none, and here it is %s.", f.val.kind.word())
		return
	default:
		good := true
		for _, m := range s.unique(f.val, "attribution.") {
			switch m.key {
			case "commit", "pr":
				if m.val.kind != jString {
					s.errorf(m.line, "the attribution's %q is the text added, as a string (an empty one adds none).", m.key)
					good = false
					break
				}
				if m.key == "commit" {
					a.Commit = strings.TrimSpace(m.val.str)
				} else {
					a.PR = strings.TrimSpace(m.val.str)
				}
			case "sessionUrl":
				if m.val.kind != jBool {
					s.errorf(m.line, "the attribution's \"sessionUrl\" is true or false.")
					good = false
				}
			default:
				s.errorf(m.line, "the attribution has no field %s; its fields are commit, pr and sessionUrl.", showName(m.key))
				good = false
			}
		}
		if !good {
			return
		}
	}
	s.add(op{kind: KindOption, text: "attribution: commit " + strconv.Quote(a.Commit) + ", pull request " + strconv.Quote(a.PR), origin: a.Origin, what: "what the model signs its commits and pull requests with",
		apply: func(b *builder) { b.attribution.put(a, a.Origin) }})
}

// fileSuggestion reads the fileSuggestion object: {"type": "command",
// "command": "..."}.
func (s *source) fileSuggestion(f jfield) {
	if f.val.kind != jObject {
		s.errorf(f.line, "\"fileSuggestion\" is an object such as {\"type\": \"command\", \"command\": \"~/.config/gnosh/file-suggestion.sh\"}, and here it is %s.", f.val.kind.word())
		return
	}
	fs := FileSuggestion{Origin: s.at(f.line)}
	typ, good := "", true
	bad := func(line int, format string, a ...any) {
		s.errorf(line, format, a...)
		good = false
	}
	for _, m := range s.unique(f.val, "fileSuggestion.") {
		switch m.key {
		case "type":
			if m.val.kind != jString || m.val.str != "command" {
				bad(m.line, "the file suggestion's \"type\" is \"command\", the one kind there is.")
			}
			typ = "command"
		case "command":
			if m.val.kind != jString || strings.TrimSpace(m.val.str) == "" {
				bad(m.line, "the file suggestion's \"command\" is the command line to run, as a string that is not empty.")
				break
			}
			fs.Command = m.val.str
		default:
			bad(m.line, "the file suggestion has no field %s; its fields are type and command.", showName(m.key))
		}
	}
	if good && typ == "" {
		bad(f.line, "the file suggestion has no \"type\". Add \"type\": \"command\".")
	}
	if good && fs.Command == "" {
		bad(f.line, "the file suggestion has no \"command\", so it would run nothing.")
	}
	if !good {
		return
	}
	s.add(op{kind: KindFileSuggest, text: "fileSuggestion: " + fs.Command, origin: fs.Origin, what: "a command that offers the paths for @",
		apply: func(b *builder) { b.fileSuggestion.put(fs, fs.Origin) }})
}

// settingsEnv reads the env object: variables for the commands and hooks
// gnosh starts.
// SkillVisibilities are the values of skillOverrides, as Claude Code's,
// from the most seen to the least.
var SkillVisibilities = []string{"on", "name-only", "user-invocable-only", "off"}

// settingsSkillOverrides reads skillOverrides: how far each skill is seen,
// by its name. Each value only takes away (a skill's own file says the
// most it is seen), so a folder that is not trusted keeps its own too.
func (s *source) settingsSkillOverrides(f jfield) {
	if f.val.kind != jObject {
		s.errorf(f.line, "\"skillOverrides\" is an object of skill names and how far each is seen (%s), and here it is %s, so none of it is in force.", strings.Join(SkillVisibilities, ", "), f.val.kind.word())
		return
	}
	for _, m := range s.unique(f.val, "skillOverrides.") {
		if m.val.kind != jString || !slices.Contains(SkillVisibilities, m.val.str) {
			s.errorf(m.line, "the value of %s is one of %s, and here it is %s.", showName("skillOverrides."+m.key), strings.Join(SkillVisibilities, ", "), shown(m.val))
			continue
		}
		name, v := m.key, Value[string]{V: m.val.str, Set: true, Origin: s.at(m.line)}
		s.add(op{kind: KindHiddenSkill, text: "skillOverrides " + name + ": " + v.V, origin: v.Origin, what: "how far a skill is seen",
			apply: func(b *builder) { b.setSkillOverride(name, v) }})
	}
}

func (s *source) settingsEnv(f jfield) {
	if f.val.kind != jObject {
		s.errorf(f.line, "\"env\" is an object of variable names and string values, and here it is %s, so none of it is in force.", f.val.kind.word())
		return
	}
	for _, m := range s.unique(f.val, "env.") {
		if !isName(m.key) {
			s.errorf(m.line, "%s is not a usable variable name under \"env\".", showName(m.key))
			continue
		}
		if m.val.kind != jString {
			s.errorf(m.line, "the value of \"env.%s\" is a string (put a number in quotes), and here it is %s.", m.key, m.val.kind.word())
			continue
		}
		if movesOwnDirs(m.key) && (s.scope == "project" || s.scope == "local") {
			s.probs.warnf(s.file, m.line, "\"env.%s\" is not taken from a project's file: it moves where gnosh and the programs it starts keep their own files. Set it in your own gnosh.json if you mean it.", m.key)
			continue
		}
		s.secrets.add(m.val.str)
		v := EnvVar{Name: m.key, Value: NewSecret(m.val.str), Origin: s.at(m.line)}
		s.add(op{kind: KindEnv, text: "env " + m.key, origin: v.Origin, feature: FeatureEnv, what: "a variable for commands",
			apply: func(b *builder) { b.setEnv(v) }})
	}
}

// movesOwnDirs reports whether a variable moves where gnosh, or a program
// it starts, keeps its own files and reads its own settings: a
// repository's files must not set those (spec/cc/settings.md 2; a trusted
// project's env block could, until the config review of 2026-11-19).
func movesOwnDirs(name string) bool {
	switch name {
	case "HOME", "TMPDIR", "CLAUDE_CONFIG_DIR":
		return true
	}
	return strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "GNOSH_") && strings.HasSuffix(name, "_DIR")
}
