package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/config"
)

// This file is /config (spec/cc/slash-commands-and-skills.md 2): gnosh's
// options, each with its value and what set it, and key=value to change
// one. gnosh never writes a gnosh.json, which is the person's, comments and
// all; what /config sets is kept in the user's state file beside the model
// chosen with /model. That file stands above the user's gnosh.json and
// below a project's files, as Claude Code's /config writes the user's own
// settings and a project's files win over them. A value set is in force
// when gnosh next starts; the few options a running session can change
// (live) are in force at once as well.

// slashConfig is /config: with nothing, the options; with a key, that one;
// with key=value pairs, each is set (key= takes the setting away).
func (l *live) slashConfig(ctx context.Context, c *SlashContext) error {
	args, err := configArgs(c.Args)
	if err != nil {
		return err
	}
	rows := l.optionRows()
	saved := l.savedOptions()
	switch {
	case len(args) == 0:
		l.listOptions(c, rows, saved)
		return nil
	case len(args) == 1 && !strings.Contains(args[0], "="):
		r, ok := findRow(rows, args[0])
		if !ok {
			_, err := config.ParseOption(args[0], "")
			return err
		}
		c.Say("%s: %s.", r.Key, r.What)
		c.Say("  It takes %s; when nothing sets it, it is %s.", r.Takes, r.Default)
		c.Say("  Now: %s (%s).", r.Text, l.originText(r))
		if note := pendingNote(r, saved); note != "" {
			c.Say("  %s", note)
		}
		return nil
	}
	// Every pair is checked before any is saved.
	type change struct {
		key, text string
		raw       json.RawMessage
	}
	var changes []change
	for _, a := range args {
		key, text, ok := strings.Cut(a, "=")
		if !ok {
			return fmt.Errorf("/config takes key=value pairs, a key alone to show that option, or nothing to list them all; %s has no =", a)
		}
		ch := change{key: key, text: text}
		if strings.TrimSpace(text) != "" {
			if ch.raw, err = config.ParseOption(key, text); err != nil {
				return err
			}
		} else if r, ok := findRow(rows, key); !ok {
			_, err := config.ParseOption(key, "")
			return err
		} else if r.List {
			return fmt.Errorf("%s holds a list, and a list is set in gnosh.json", key)
		}
		changes = append(changes, ch)
	}
	err = config.UpdateState(config.OS{}, l.cfg.Paths.StateFile, func(st *config.State) {
		for _, ch := range changes {
			// (Each value was checked above, so this cannot fail.)
			_ = st.SetOption(ch.key, ch.raw)
		}
	})
	if err != nil {
		return fmt.Errorf("nothing was saved: %w", err)
	}
	for _, ch := range changes {
		r, _ := findRow(rows, ch.key)
		if ch.raw != nil && r.Origin.Layer <= config.LayerState && l.applyOption(ch.key, ch.raw) {
			c.Say("%s is %s from now on, in this session and later ones, in every project whose own files do not set it.", ch.key, jsonText(ch.raw))
			continue
		}
		if _, was := saved[ch.key]; ch.raw == nil && !was {
			c.Say("%s was not set with /config, so nothing changed.", ch.key)
			continue
		}
		if ch.raw == nil {
			c.Say("%s is no longer set with /config: when gnosh next starts the config files decide (in this session it is %s, from %s).", ch.key, r.Text, l.originText(r))
			continue
		}
		if ch.raw == nil {
			l.forgetLive(ch.key)
		}
		c.Say("%s is %s when gnosh next starts, in every project whose own files do not set it.", ch.key, jsonText(ch.raw))
		if r.Origin.Layer > config.LayerState {
			c.Say("  Here %s sets it to %s, and that wins.", l.originText(r), r.Text)
		}
	}
	c.Say("(Kept in %s, never in your gnosh.json.)", l.tilde(l.cfg.Paths.StateFile))
	return nil
}

// slashAutocompact is /autocompact [auto|<tokens>], Claude Code's: the
// autoCompactWindow option, shown, set to a number of tokens, or with auto
// taken away so that the window decides, as /config sets it.
func (l *live) slashAutocompact(ctx context.Context, c *SlashContext) error {
	arg := strings.TrimSpace(c.Args)
	sub := *c
	switch {
	case arg == "":
		sub.Args = "autoCompactWindow"
	case strings.EqualFold(arg, "auto"):
		sub.Args = "autoCompactWindow="
	default:
		n, err := strconv.Atoi(strings.ReplaceAll(strings.ReplaceAll(arg, ",", ""), "_", ""))
		if err != nil {
			return fmt.Errorf("/autocompact takes auto, or a number of tokens such as 200000; %q is neither", arg)
		}
		sub.Args = "autoCompactWindow=" + strconv.Itoa(n)
	}
	return l.slashConfig(ctx, &sub)
}

// liveOptions are the options a running session can change: /config puts
// them in force at once.
var liveOptions = map[string]bool{"autoCompactEnabled": true, "autoCompactWindow": true, "debug": true, "verbose": true, "editorMode": true, "outputStyle": true, "preferredNotifChannel": true}

// applyOption puts what /config set in force in the running session, for
// an option that can be (liveOptions), and reports whether it was.
func (l *live) applyOption(key string, raw json.RawMessage) bool {
	if !liveOptions[key] {
		return false
	}
	switch key {
	case "outputStyle":
		var name string
		if json.Unmarshal(raw, &name) != nil || l.setStyle(name) != nil {
			return false
		}
	case "preferredNotifChannel":
		var channel string
		if json.Unmarshal(raw, &channel) != nil {
			return false
		}
		l.notifMu.Lock()
		l.notif = channel
		l.notifMu.Unlock()
	case "editorMode":
		// (The interface's to put in force, as verbose is: the box is
		// its own.)
	case "autoCompactEnabled", "debug", "verbose":
		var on bool
		if json.Unmarshal(raw, &on) != nil {
			return false
		}
		switch key {
		case "debug":
			if l.flog == nil {
				return false // (a session without a log)
			}
			l.flog.debug.Store(on)
		case "autoCompactEnabled":
			if l.agent != nil {
				l.agent.SetAutoCompact(on)
			}
		}
		// (verbose is the interface's to show: it puts what /config set in
		// force itself, when the command comes back.)
	case "autoCompactWindow":
		var n int
		if json.Unmarshal(raw, &n) != nil || l.agent == nil {
			return false
		}
		l.agent.SetCompactAt(l.compactAtWindow(n))
	}
	l.configMu.Lock()
	if l.configNow == nil {
		l.configNow = map[string]json.RawMessage{}
	}
	l.configNow[key] = raw
	l.configMu.Unlock()
	return true
}

// forgetLive drops what /config put in force in this session for an
// option whose choice was taken away: the value the session started with
// is listed again (and in force again at its next start).
func (l *live) forgetLive(key string) {
	l.configMu.Lock()
	delete(l.configNow, key)
	l.configMu.Unlock()
}

// optionRows are the options as /config lists them: as the session
// started, with what /config has put in force since.
func (l *live) optionRows() []config.OptionValue {
	rows := l.cfg.Options.All()
	l.configMu.Lock()
	defer l.configMu.Unlock()
	for i, r := range rows {
		if raw, ok := l.configNow[r.Key]; ok {
			rows[i].Text, rows[i].JSON, rows[i].Set = jsonText(raw), raw, true
			rows[i].Origin = config.Origin{Layer: config.LayerState, File: l.cfg.Paths.StateFile}
		}
	}
	return rows
}

// jsonText is a JSON value as a person reads it: a string without its
// quotes, anything else as it is written.
func jsonText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// listOptions says every option's value in this session and what set it.
func (l *live) listOptions(c *SlashContext, rows []config.OptionValue, saved map[string]json.RawMessage) {
	keyW, valW := 0, 0
	for _, r := range rows {
		keyW = max(keyW, len(r.Key))
		// (A long value, such as a default said in words, pushes its own
		// line's last column along, not every line's.)
		valW = max(valW, min(len(r.Text), 24))
	}
	c.Say("gnosh's options in this session (/config key says more, /config key=value sets one):")
	for _, r := range rows {
		c.Say("  %-*s  %-*s  %s", keyW, r.Key, valW, r.Text, l.originText(r))
		if note := pendingNote(r, saved); note != "" {
			c.Say("  %*s  %s", keyW, "", note)
		}
	}
	c.Say("What /config sets is kept in %s: above your gnosh.json, below a project's files.", l.tilde(l.cfg.Paths.StateFile))
}

// savedOptions reads the options set with /config from the state file as
// it is now, which another session may have changed since this one began.
func (l *live) savedOptions() map[string]json.RawMessage {
	st, _ := config.ReadState(config.OS{}, l.cfg.Paths.StateFile)
	saved := make(map[string]json.RawMessage, len(st.Options)+1)
	for k, v := range st.Options {
		saved[k] = v
	}
	if st.AutoCompactWindow != 0 {
		saved["autoCompactWindow"], _ = json.Marshal(st.AutoCompactWindow)
	}
	return saved
}

// pendingNote says how what /config set differs from what is in force in
// this session, or "".
func pendingNote(r config.OptionValue, saved map[string]json.RawMessage) string {
	raw, ok := saved[r.Key]
	switch {
	case !ok && r.Origin.Layer == config.LayerState:
		return "(taken away with /config: when gnosh next starts the config files decide)"
	case !ok:
		return ""
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) == nil && bytes.Equal(buf.Bytes(), r.JSON) {
		return ""
	}
	shown := buf.String()
	var s string
	if json.Unmarshal(raw, &s) == nil {
		shown = s
	}
	if r.Origin.Layer > config.LayerState {
		return "(set with /config to " + shown + "; what sets it here outranks that)"
	}
	return "(set with /config to " + shown + ": in force when gnosh next starts)"
}

// originText names what set an option's value.
func (l *live) originText(r config.OptionValue) string {
	switch {
	case !r.Set || r.Origin.Layer == config.LayerDefault:
		return "the default"
	case r.Origin.Layer == config.LayerState:
		return "/config"
	case r.Origin.File != "":
		return l.tilde(r.Origin.String())
	}
	return r.Origin.String()
}

// tilde writes a path under the home directory with ~ in its place.
func (l *live) tilde(path string) string {
	home := l.cfg.Paths.Home
	if home != "" && home != "/" && (path == home || strings.HasPrefix(path, home+"/")) {
		return "~" + path[len(home):]
	}
	return path
}

// findRow finds an option's row by its key.
func findRow(rows []config.OptionValue, key string) (config.OptionValue, bool) {
	for _, r := range rows {
		if r.Key == key {
			return r, true
		}
	}
	return config.OptionValue{}, false
}

// configArgs cuts /config's arguments at blanks, except blanks inside
// quotes, which stay in the word with the quotes ("key='a b'").
func configArgs(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	var quote rune
	in := false
	for _, r := range s {
		switch {
		case quote != 0:
			b.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote, in = r, true
			b.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\n':
			if in {
				out = append(out, b.String())
				b.Reset()
				in = false
			}
		default:
			in = true
			b.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, errors.New("a quote in /config's arguments is not closed")
	}
	if in {
		out = append(out, b.String())
	}
	return out, nil
}
