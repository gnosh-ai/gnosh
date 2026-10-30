package config

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// flattenHookGroups rewrites the hooks a config file gives in Claude
// Code's shape into Gnosh's. Claude Code files each event's hooks in
// groups, a matcher and the handlers it runs:
//
//	{"matcher": "Bash", "hooks": [{"type": "command", "command": "./check.sh"}]}
//
// and Gnosh takes one entry per command, {"matcher": "Bash", "command":
// "./check.sh"}. So a Claude Code settings.json hooks block works pasted
// in as it is. Entries already in Gnosh's shape are left as they are.
//
// Only what Gnosh can do as Claude Code would is taken: command handlers,
// with a timeout, and the fields that change nothing here (statusMessage,
// once, which Claude Code ignores in settings files, and the defaults of
// shell, async, asyncRewake and onFailure). A handler that asks for more,
// another type (http, mcp_tool, prompt, agent), an "if" filter, "args",
// a background hook, is an error naming it, not a hook run some other way
// than written.
func flattenHookGroups(data []byte) ([]byte, error) {
	hooks := gjson.GetBytes(data, "hooks")
	if !hooks.IsObject() {
		return data, nil
	}
	flattened := map[string]json.RawMessage{}
	changed := false
	var err error
	hooks.ForEach(func(event, entries gjson.Result) bool {
		if !entries.IsArray() || !slices.ContainsFunc(entries.Array(), isHookGroup) {
			flattened[event.String()] = json.RawMessage(entries.Raw)
			return true
		}
		changed = true
		var flat []json.RawMessage
		for i, entry := range entries.Array() {
			if !isHookGroup(entry) {
				flat = append(flat, json.RawMessage(entry.Raw))
				continue
			}
			var group []json.RawMessage
			group, err = flattenHookGroup(entry, fmt.Sprintf("hooks.%s[%d]", event.String(), i))
			if err != nil {
				return false
			}
			flat = append(flat, group...)
		}
		flattened[event.String()], err = json.Marshal(flat)
		return err == nil
	})
	if err != nil || !changed {
		return data, err
	}
	raw, err := json.Marshal(flattened)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(data, "hooks", raw)
}

// isHookGroup reports whether a hooks entry is a Claude Code group: it
// holds its commands in a "hooks" list.
func isHookGroup(entry gjson.Result) bool {
	return entry.IsObject() && entry.Get("hooks").Exists()
}

// flattenHookGroup turns one Claude Code group, at where in the file,
// into Gnosh hook entries, one per handler.
func flattenHookGroup(group gjson.Result, where string) ([]json.RawMessage, error) {
	var err error
	group.ForEach(func(key, _ gjson.Result) bool {
		if k := key.String(); k != "matcher" && k != "hooks" {
			err = fmt.Errorf("%s: unknown field %q in a hook group; a group has a matcher and its hooks", where, k)
		}
		return err == nil
	})
	if err != nil {
		return nil, err
	}
	handlers := group.Get("hooks")
	if !handlers.IsArray() {
		return nil, fmt.Errorf("%s.hooks: expected a list of hooks", where)
	}
	matcher := group.Get("matcher")
	if matcher.Exists() && matcher.Type != gjson.String {
		return nil, fmt.Errorf("%s.matcher: expected a string", where)
	}
	var out []json.RawMessage
	for j, handler := range handlers.Array() {
		entry, err := flattenHookHandler(handler, matcher.String(), fmt.Sprintf("%s.hooks[%d]", where, j))
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// flattenHookHandler turns one Claude Code hook handler into a Gnosh hook
// entry under matcher, or says why it cannot be one.
func flattenHookHandler(handler gjson.Result, matcher, where string) (json.RawMessage, error) {
	if !handler.IsObject() {
		return nil, fmt.Errorf("%s: expected a hook object", where)
	}
	entry := HookConfig{Matcher: matcher}
	var err error
	handler.ForEach(func(key, value gjson.Result) bool {
		k := key.String()
		switch k {
		case "type":
			if value.String() != "command" {
				err = fmt.Errorf("%s: hook type %q is not supported; Gnosh runs command hooks only", where, value.String())
			}
		case "command":
			if value.Type != gjson.String || value.String() == "" {
				err = fmt.Errorf("%s.command: expected the command to run", where)
			}
			entry.Command = value.String()
		case "timeout":
			if value.Type != gjson.Number || value.Num <= 0 || value.Num != math.Trunc(value.Num) {
				err = fmt.Errorf("%s.timeout: expected a whole number of seconds", where)
			}
			entry.Timeout = int(value.Num)
		case "statusMessage", "once":
			// What a spinner says while the hook runs, and a skill's
			// run-once flag, which Claude Code ignores in settings files.
		case "shell":
			if value.String() != "bash" {
				err = fmt.Errorf("%s.shell: %q is not supported; Gnosh runs hooks in its POSIX shell", where, value.String())
			}
		case "async", "asyncRewake":
			if value.Type != gjson.False {
				err = fmt.Errorf("%s.%s: background hooks are not supported; Gnosh waits for every hook", where, k)
			}
		case "onFailure":
			if value.String() != "continue" {
				err = fmt.Errorf("%s.onFailure: %q is not supported; a hook that fails lets the action go ahead, as onFailure continue does", where, value.String())
			}
		case "if":
			err = fmt.Errorf("%s.if: not supported yet; without it the hook would run for every call its matcher matches", where)
		default:
			err = fmt.Errorf("%s: %q is not supported in a hook (%s)", where, k, strings.TrimSpace(value.Raw))
		}
		return err == nil
	})
	if err != nil {
		return nil, err
	}
	if !handler.Get("type").Exists() {
		return nil, fmt.Errorf("%s.type: required; Gnosh runs \"command\" hooks", where)
	}
	if entry.Command == "" {
		return nil, fmt.Errorf("%s.command: required", where)
	}
	return json.Marshal(entry)
}
