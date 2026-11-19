package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// ParseConfig reads the "hooks" block of a JSON settings file, in Claude
// Code's shape, pasted as it is:
//
//	{
//	  "PreToolUse": [
//	    { "matcher": "Bash",
//	      "hooks": [ { "type": "command", "command": "./hooks/no-force-push.sh", "timeout": 10 } ] }
//	  ]
//	}
//
// raw is the value of the "hooks" key, and source names the file it came
// from, for messages. An absent block (nil, or JSON null) is an empty
// configuration.
//
// The reading is strict, because a hook that is configured and does not
// run is worse than no hook. Each of these is a problem that names the
// place and the word:
//
//   - a block that is not an object of event names, or a group or a hook
//     of the wrong shape;
//   - an event name that is not exactly one of gnosh's;
//   - a name written twice in one object (JSON readers keep only one of
//     the two, so a list of hooks would vanish);
//   - a hook type other than "command", "http", "mcp_tool", "prompt" and
//     "agent";
//   - a shell other than bash, an "onFailure" other than "continue", an
//     "if" that is not a string, "args" that are not a list of strings,
//     "async" or "asyncRewake" that is not true or false;
//   - any field gnosh does not know;
//   - a matcher on an event that takes none, or one that cannot be read;
//   - no command, or a timeout that is not a number of seconds above zero.
//
// Accepted, and changing nothing: "once", and the default values "shell":
// "bash" and "onFailure": "continue". (gnosh blocks when a
// hook fails, whatever "onFailure" says.)
//
// When anything is wrong the error is a *ConfigError listing every
// problem. The Config returned with it holds the hooks that could be read
// and remembers the problems: merge it with the other layers and Check
// reports everything at once, while NewRunner refuses to run any of it.
func ParseConfig(raw json.RawMessage, source string) (Config, error) {
	var cfg Config
	p := blockParser{source: source, cfg: &cfg}
	p.block(raw)
	if len(cfg.problems) > 0 {
		return cfg, &ConfigError{Problems: cfg.problems}
	}
	return cfg, nil
}

// blockParser walks a "hooks" block and fills a Config.
type blockParser struct {
	source string
	cfg    *Config
}

// at names a place inside the block, for a Problem's or a Hook's Source.
// An empty place is the block as a whole.
func (p *blockParser) at(place string) string {
	switch {
	case place == "":
		return p.source
	case p.source == "":
		return place
	}
	return p.source + ": " + place
}

// fail records a problem at a place inside the block.
func (p *blockParser) fail(place, format string, args ...any) {
	p.cfg.problems = append(p.cfg.problems, Problem{Source: p.at(place), Message: fmt.Sprintf(format, args...)})
}

// object reads the JSON object at place. must says what the value has to
// be, as the start of a sentence, for the problem recorded when it is not.
func (p *blockParser) object(place, must string, raw []byte) ([]member, bool) {
	fields, err := members(raw)
	var twice duplicateKey
	switch {
	case err == nil:
		return fields, true
	case errors.As(err, &twice):
		p.fail(place, "%s is written twice here. JSON keeps only one of the two, so the other would be silently dropped. Write it once, with everything under it.", quote(string(twice)))
	case errors.Is(err, errNotObject):
		p.fail(place, "%s; this one is %s.", must, kindOf(raw))
	default:
		p.fail(place, "%s; this one cannot be read (%s).", must, jsonError(err))
	}
	return nil, false
}

// list reads the JSON list in raw.
func list(raw []byte) ([]json.RawMessage, bool) {
	var items []json.RawMessage
	if kindOf(raw) != "a list" || json.Unmarshal(raw, &items) != nil {
		return nil, false
	}
	return items, true
}

// block reads the whole block: an object of event names.
func (p *blockParser) block(raw json.RawMessage) {
	if len(bytes.TrimSpace(raw)) == 0 || isNull(raw) {
		return
	}
	events, ok := p.object("", "the \"hooks\" setting must be a JSON object that maps event names to lists of matcher groups", raw)
	if !ok {
		return
	}
	for _, ev := range events {
		if msg := eventProblem(ev.key); msg != "" {
			p.fail("", "%s", msg)
			continue
		}
		groups, ok := list(ev.value)
		if !ok {
			p.fail(ev.key, "the value must be a list of matcher groups, as in \"%s\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"...\"}]}]; this one is %s.", ev.key, kindOf(ev.value))
			continue
		}
		for i, g := range groups {
			p.group(Event(ev.key), fmt.Sprintf("%s group %d", ev.key, i+1), g)
		}
	}
}

// group reads one matcher group: a matcher and the hooks it applies to.
func (p *blockParser) group(event Event, place string, raw json.RawMessage) {
	fields, ok := p.object(place, "a matcher group must be a JSON object with \"hooks\" and, where the event takes one, \"matcher\"", raw)
	if !ok {
		return
	}
	if !hasKey(fields, "hooks") && (hasKey(fields, "command") || hasKey(fields, "type")) {
		// A hook written where a group belongs: an easy slip, and one
		// sentence says more than one for each of its fields.
		p.fail(place, "this is a hook, written where a matcher group belongs. An event holds a list of groups, and each group holds its hooks: {\"hooks\": [{\"type\": \"command\", \"command\": \"...\"}]}.")
		return
	}
	var (
		pattern  string
		handlers []json.RawMessage
		hasHooks bool
		bad      bool
	)
	for _, f := range fields {
		switch f.key {
		case "matcher":
			if isNull(f.value) {
				continue
			}
			s, ok := asString(f.value)
			if !ok {
				p.fail(place, "\"matcher\" must be one string, such as \"Edit|Write\"; this one is %s.", kindOf(f.value))
				bad = true
			}
			pattern = s
		case "hooks":
			hasHooks = true
			if handlers, ok = list(f.value); !ok {
				p.fail(place, "\"hooks\" must be a list of hooks; this one is %s.", kindOf(f.value))
				bad = true
			}
		default:
			p.fail(place, "the group has %s, which gnosh does not know. A matcher group has \"matcher\" and \"hooks\".", quote(f.key))
			bad = true
		}
	}
	switch {
	case bad:
		// The group's matcher or its list could not be read, so its hooks
		// cannot be taken to mean what they say.
		return
	case !hasHooks:
		p.fail(place, "the group has no \"hooks\" list, so it configures nothing.")
		return
	}
	for i, h := range handlers {
		p.handler(event, pattern, fmt.Sprintf("%s, hook %d", place, i+1), h)
	}
}

// handler reads one hook of a group.
func (p *blockParser) handler(event Event, pattern, place string, raw json.RawMessage) {
	fields, ok := p.object(place, "a hook must be a JSON object such as {\"type\": \"command\", \"command\": \"...\"}", raw)
	if !ok {
		return
	}
	// The type decides what the other fields mean, so it is read first. A
	// hook of another type gets one sentence, not one for each of its
	// fields.
	kind, hasType := "", false
	for _, f := range fields {
		if f.key == "type" && !isNull(f.value) {
			if kind, hasType = asString(f.value); !hasType {
				p.fail(place, "\"type\" must be a string; this one is %s. gnosh runs command hooks: \"type\": \"command\".", kindOf(f.value))
				return
			}
		}
	}
	switch {
	case !hasType:
		p.fail(place, "the hook has no \"type\". gnosh runs command hooks: \"type\": \"command\".")
		return
	case kind != "command" && kind != "http" && kind != "mcp_tool" && kind != "prompt" && kind != "agent":
		p.fail(place, "the hook has \"type\": %s. gnosh runs command hooks, http hooks, mcp_tool hooks, prompt hooks and agent hooks only, so this hook would not run as written.", quote(kind))
		return
	}
	// The fields of one type are a mistake on another.
	only := map[string][]string{"command": {"command"}, "args": {"command"}, "async": {"command"}, "asyncRewake": {"command"}, "shell": {"command"},
		"url": {"http"}, "headers": {"http"}, "allowedEnvVars": {"http"},
		"server": {"mcp_tool"}, "tool": {"mcp_tool"}, "input": {"mcp_tool"},
		"prompt": {"prompt", "agent"}, "model": {"prompt", "agent"}, "continueOnBlock": {"prompt"}}
	for _, f := range fields {
		if kinds, ok := only[f.key]; ok && !slices.Contains(kinds, kind) && !isNull(f.value) {
			p.fail(place, "the hook has %s, which a hook of \"type\": %s does not take.", quote(f.key), quote(kind))
			return
		}
	}

	h := Hook{Event: event, Matcher: pattern, Source: p.at(place), Agent: kind == "agent"}
	before := len(p.cfg.problems)
	badCommand := false
	for _, f := range fields {
		if isNull(f.value) {
			continue
		}
		written := strings.TrimSpace(string(f.value))
		switch f.key {
		case "type":
		case "command":
			if h.Command, ok = asString(f.value); !ok {
				p.fail(place, "\"command\" must be a string holding a shell command line; this one is %s.", kindOf(f.value))
				badCommand = true
			}
		case "timeout":
			var n float64
			if ok = kindOf(f.value) == "a number" && json.Unmarshal(f.value, &n) == nil; ok {
				h.Timeout, ok = seconds(n)
			}
			switch text, quoted := asString(f.value); {
			case ok:
			case quoted:
				p.fail(place, "%s In JSON a timeout is written as a number, without quotes.", timeoutProblem(text))
			default:
				p.fail(place, "%s", timeoutProblem(written))
			}
		case "statusMessage":
			if h.StatusMessage, ok = asString(f.value); !ok {
				p.fail(place, "\"statusMessage\" must be a string; this one is %s.", kindOf(f.value))
			}
		case "once":
			if h.Once, ok = asBool(f.value); !ok {
				p.fail(place, "\"once\" must be true or false; this one is %s.", kindOf(f.value))
			}
		case "shell":
			if s, ok := asString(f.value); !ok || s != "bash" {
				p.fail(place, "the hook has \"shell\": %s. gnosh runs hooks with a POSIX shell only. Remove \"shell\", or write \"bash\".", written)
			}
		case "async", "asyncRewake":
			on, ok := asBool(f.value)
			switch {
			case !ok:
				p.fail(place, "the hook has \"%s\": %s; it is true or false.", f.key, written)
			case f.key == "async":
				h.Async = on
			default:
				h.AsyncRewake = on
			}
		case "onFailure":
			switch s, ok := asString(f.value); {
			case ok && s == "continue":
			case ok && s == "block":
				p.fail(place, "the hook has \"onFailure\": \"block\". gnosh does not take this setting, and does not need it: a hook that fails already blocks, on every event that can block. Remove \"onFailure\".")
			default:
				p.fail(place, "the hook has \"onFailure\": %s, which gnosh does not know. Remove it: a hook that fails blocks, on every event that can block.", written)
			}
		case "if":
			s, ok := asString(f.value)
			switch {
			case ok && strings.TrimSpace(s) != "":
				h.If = strings.TrimSpace(s)
			case ok:
				p.fail(place, "\"if\" is empty. It is one permission rule, such as \"Bash(git push *)\"; to run the hook for every call its matcher takes, leave \"if\" out.")
			default:
				p.fail(place, "\"if\" is one permission rule, as a string, such as \"Bash(git push *)\"; this one is %s.", kindOf(f.value))
			}
		case "server", "tool":
			v, isText := asString(f.value)
			if !isText || strings.TrimSpace(v) == "" {
				p.fail(place, "%s names the MCP %s, as a string; this one is %s.", quote(f.key), f.key, kindOf(f.value))
				badCommand = true
				break
			}
			if f.key == "server" {
				h.Server = v
			} else {
				h.Tool = v
			}
		case "input":
			if kindOf(f.value) != "an object" {
				p.fail(place, "\"input\" is a JSON object of the tool's arguments; this one is %s.", kindOf(f.value))
				break
			}
			h.Input = append(json.RawMessage(nil), f.value...)
		case "prompt":
			if h.Prompt, ok = asString(f.value); !ok || strings.TrimSpace(h.Prompt) == "" {
				p.fail(place, "\"prompt\" is the rule or the condition the model judges, as a string; this one is %s.", kindOf(f.value))
				badCommand = true
			}
		case "model":
			if h.Model, ok = asString(f.value); !ok {
				p.fail(place, "\"model\" must be a string; this one is %s.", kindOf(f.value))
			}
		case "continueOnBlock":
			if h.ContinueOnBlock, ok = asBool(f.value); !ok {
				p.fail(place, "\"continueOnBlock\" must be true or false; this one is %s.", kindOf(f.value))
			}
		case "url":
			if h.URL, ok = asString(f.value); !ok || h.URL == "" {
				p.fail(place, "\"url\" is the address the event is sent to, as a string; this one is %s.", kindOf(f.value))
				badCommand = true
			}
		case "headers":
			var headers map[string]string
			if kindOf(f.value) != "an object" || json.Unmarshal(f.value, &headers) != nil {
				p.fail(place, "\"headers\" is an object of header names and their values, each a string; this one is %s.", kindOf(f.value))
				break
			}
			h.Headers = headers
		case "allowedEnvVars":
			var names []string
			if kindOf(f.value) != "a list" || json.Unmarshal(f.value, &names) != nil {
				p.fail(place, "\"allowedEnvVars\" is a list of the names of the variables a header may use; this one is %s.", kindOf(f.value))
				break
			}
			h.AllowedEnvVars = names
		case "args":
			var args []string
			if kindOf(f.value) != "a list" || json.Unmarshal(f.value, &args) != nil {
				p.fail(place, "\"args\" is a list of strings, one argument each; this one is %s.", kindOf(f.value))
				break
			}
			h.Args = append([]string{}, args...)
		default:
			p.fail(place, "the hook has %s, which gnosh does not know. A command hook has \"type\", \"command\" and \"timeout\".", quote(f.key))
		}
	}
	if kind == "prompt" && h.Prompt == "" && !badCommand {
		p.fail(place, "the prompt hook has no \"prompt\": the rule or the condition the model judges.")
		badCommand = true
	}
	if kind == "agent" && h.Prompt == "" && !badCommand {
		p.fail(place, "the agent hook has no \"prompt\": the task its sub-agent is given.")
		badCommand = true
	}
	if kind == "http" && h.URL == "" && !badCommand {
		p.fail(place, "the http hook has no \"url\" to send the event to.")
		badCommand = true
	}
	if kind == "mcp_tool" && h.Server == "" && h.Tool == "" && !badCommand {
		// (One of the two alone is said by hookProblems.)
		p.fail(place, "the mcp_tool hook names no \"server\" and no \"tool\" to call.")
		badCommand = true
	}
	for _, msg := range hookProblems(h) {
		if (badCommand || kind != "command") && msg == noCommand {
			continue // already said, more exactly
		}
		p.fail(place, "%s", msg)
	}
	if len(p.cfg.problems) == before {
		p.cfg.Hooks = append(p.cfg.Hooks, h)
	}
}

// member is one "key": value pair of a JSON object.
type member struct {
	key   string
	value json.RawMessage
}

// errNotObject says that a JSON value is not an object.
var errNotObject = errors.New("not a JSON object")

// duplicateKey is the error for an object that has the same key twice. Its
// text is the key.
type duplicateKey string

func (k duplicateKey) Error() string {
	return quote(string(k)) + " is written twice in one object"
}

// members reads raw as one JSON object and returns its members in the
// order they are written. A key written twice is an error: JSON readers
// keep only one of the two and say nothing, which is how half of a
// configuration, or half of an answer, goes missing.
func members(raw []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errNotObject
	}
	var out []member
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errNotObject
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, duplicateKey(key)
		}
		seen[key] = true
		out = append(out, member{key: key, value: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("there is more text after the object")
	}
	return out, nil
}

// jsonError puts a JSON reader's error into plain words.
func jsonError(err error) string {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "it ends before it is complete"
	}
	return err.Error()
}

// kindOf names the kind of a JSON value in plain words.
func kindOf(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "empty"
	}
	switch c := raw[0]; {
	case c == '{':
		return "an object"
	case c == '[':
		return "a list"
	case c == '"':
		return "a string"
	case c == 't' || c == 'f':
		return "true or false"
	case c == 'n':
		return "null"
	case c == '-' || ('0' <= c && c <= '9'):
		return "a number"
	}
	return "not JSON"
}

// isNull reports whether raw is the JSON value null.
func isNull(raw []byte) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// asString reads raw as a JSON string.
func asString(raw []byte) (string, bool) {
	var s string
	if kindOf(raw) != "a string" || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// asBool reads raw as a JSON true or false.
func asBool(raw []byte) (value, ok bool) {
	if kindOf(raw) != "true or false" || json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	return value, true
}
