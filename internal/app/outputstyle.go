package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/app/agentdef"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

// This file is the output styles: Claude Code's outputStyle setting and its
// /output-style command. A style is instructions on how the model replies,
// sent in the system prompt right after gnosh's core, from the next message
// on. gnosh has Claude Code's four besides the default (prompt's
// BuiltinStyles), and a person or a trusted project adds more as Markdown
// files in an output-styles folder, beside their skills: frontmatter (name,
// description, keep-coding-instructions) and the instructions.

// defaultStyle is the name of no style at all.
const defaultStyle = "default"

// outputStyle is a style and where it comes from: a file, or gnosh.
type outputStyle struct {
	prompt.Style
	source string
}

// maxStyleBytes is the most of a style file that is read.
const maxStyleBytes = 64 << 10

// loadOutputStyles reads the styles there are: the project's, when the
// folder is trusted, the closest folder first, then the person's, then
// gnosh's own. The first of a name wins. Files that cannot be used are
// said in problems.
func loadOutputStyles(texts *prompt.Set, cfg *config.Config, trusted bool, more []string) (styles []outputStyle, problems []string) {
	seen := map[string]bool{defaultStyle: true}
	var dirs []string
	if trusted && !cfg.Switches.SafeMode.V {
		dirs = append(dirs, projectDefDirs(cfg, "output-styles", more)...)
	}
	if !cfg.Switches.SafeMode.V {
		dirs = append(dirs, userDefDirs(cfg, "output-styles")...)
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			path := filepath.Join(dir, n)
			st, why := readOutputStyle(path)
			if why != "" {
				problems = append(problems, fmt.Sprintf("the output style %s cannot be used: %s", path, why))
				continue
			}
			if seen[st.Name] {
				continue
			}
			seen[st.Name] = true
			styles = append(styles, st)
		}
	}
	for _, b := range texts.BuiltinStyles() {
		if !seen[b.Name] {
			seen[b.Name] = true
			styles = append(styles, outputStyle{Style: b, source: "gnosh"})
		}
	}
	return styles, problems
}

// readOutputStyle reads one style file. Settings that cannot be read are
// no settings, as Claude Code has it: the style loads under its file's
// name with none.
func readOutputStyle(path string) (outputStyle, string) {
	st := outputStyle{source: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return st, err.Error()
	}
	if len(data) > maxStyleBytes {
		return st, fmt.Sprintf("it is larger than %d KiB", maxStyleBytes>>10)
	}
	fields, body, err := agentdef.Split(string(data))
	if err != nil {
		fields, body = nil, string(data)
	}
	st.Name = strings.TrimSuffix(filepath.Base(path), ".md")
	for _, f := range fields {
		switch f.Key {
		case "name":
			if v := strings.TrimSpace(f.Scalar); v != "" {
				st.Name = v
			}
		case "description":
			st.Description = strings.TrimSpace(f.Scalar)
		case "keep-coding-instructions":
			st.KeepCoding = strings.EqualFold(strings.TrimSpace(f.Scalar), "true")
		}
	}
	st.Text = strings.TrimSpace(body)
	switch {
	case st.Text == "":
		return st, "it holds no instructions"
	case strings.EqualFold(st.Name, defaultStyle):
		return st, "\"default\" is the name of no style at all"
	case !printableName(st.Name):
		return st, "its name holds a control character"
	}
	return st, ""
}

// printableName reports whether a name holds no control character.
func printableName(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// findStyle finds a style by its name: exactly, or with exact set off,
// in any case (as /output-style takes it).
func findStyle(styles []outputStyle, name string, exact bool) (outputStyle, bool) {
	for _, s := range styles {
		if s.Name == name || !exact && strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return outputStyle{}, false
}

// currentStyle is the style in force, or nil for the default (and for a
// name no style has, which the session said when it started).
func (l *live) currentStyle() *prompt.Style {
	l.styleMu.Lock()
	defer l.styleMu.Unlock()
	if l.styleName == "" || l.styleName == defaultStyle {
		return nil
	}
	if st, ok := findStyle(l.styles, l.styleName, true); ok {
		return &st.Style
	}
	return nil
}

// styleNow is the name of the style in force, as the status line command is
// told it.
func (l *live) styleNow() string {
	if st := l.currentStyle(); st != nil {
		return st.Name
	}
	return defaultStyle
}

// loadStyles reads the styles at the start of a session and takes the one
// the configuration names. A name no style has is said, and the default is
// used.
func (l *live) loadStyles() {
	styles, problems := loadOutputStyles(l.texts, l.cfg, l.st.trusted, l.defRoots())
	for _, p := range problems {
		l.notice(agent.LevelWarning, "output-style", p+".")
	}
	name := strings.TrimSpace(l.cfg.Options.OutputStyle.V)
	l.styleMu.Lock()
	l.styles, l.styleName = styles, name
	l.styleMu.Unlock()
	if name != "" && name != defaultStyle {
		if _, ok := findStyle(styles, name, true); !ok {
			l.notice(agent.LevelWarning, "output-style", fmt.Sprintf("There is no output style named %q (set in %s), so the default is used. /output-style lists the styles there are.", name, l.cfg.Options.OutputStyle.Origin.String()))
		}
	}
}

// setStyle puts a style in force from the next request on: the system
// prompt is built again with it.
func (l *live) setStyle(name string) error {
	l.styleMu.Lock()
	l.styleName = name
	l.styleMu.Unlock()
	parts, err := l.systemPrompt()
	if err != nil {
		return err
	}
	l.system = parts
	if l.agent != nil {
		l.agent.SetSystem(parts)
	}
	return nil
}

// slashOutputStyle is /output-style: with nothing after it, the styles
// there are, the one in force marked; with a name, that style from the
// next message on, kept in the project's local settings file as Claude
// Code keeps it.
func (l *live) slashOutputStyle(ctx context.Context, c *SlashContext) error {
	l.styleMu.Lock()
	styles := append([]outputStyle(nil), l.styles...)
	l.styleMu.Unlock()
	now := l.styleNow()
	want := strings.TrimSpace(c.Args)
	if want == "" {
		c.Say("Output styles (/output-style <name> switches; it is kept in %s):", l.tilde(l.cfg.Paths.LocalSettings))
		mark := func(name string) string {
			if name == now {
				return "*"
			}
			return " "
		}
		c.Say("%s %s: gnosh's own instructions alone", mark(defaultStyle), defaultStyle)
		for _, s := range styles {
			from := ""
			if s.source != "gnosh" {
				from = " (" + l.tilde(s.source) + ")"
			}
			c.Say("%s %s: %s%s", mark(s.Name), s.Name, firstOf(s.Description, "no description"), from)
		}
		return nil
	}
	name := defaultStyle
	if !strings.EqualFold(want, defaultStyle) {
		st, ok := findStyle(styles, want, false)
		if !ok {
			return fmt.Errorf("there is no output style named %q; /output-style lists the styles there are", want)
		}
		name = st.Name
	}
	err := l.editLocalSettings(func(doc map[string]json.RawMessage) error {
		opts := map[string]json.RawMessage{}
		if raw, ok := doc["options"]; ok {
			if err := json.Unmarshal(raw, &opts); err != nil {
				return fmt.Errorf("its \"options\" is not an object (%v)", err)
			}
		}
		if name == defaultStyle {
			delete(opts, "outputStyle")
		} else {
			raw, _ := json.Marshal(name)
			opts["outputStyle"] = raw
		}
		if len(opts) == 0 {
			delete(doc, "options")
			return nil
		}
		raw, err := json.Marshal(opts)
		doc["options"] = raw
		return err
	})
	if err != nil {
		return fmt.Errorf("the output style was not changed: %w", err)
	}
	if err := l.setStyle(name); err != nil {
		return err
	}
	c.Say("The output style is %s from your next message on (kept in %s).", name, l.tilde(l.cfg.Paths.LocalSettings))
	if !l.st.trusted {
		// A project's files count for nothing in a folder that is not
		// trusted, the one gnosh writes too.
		c.Say("This folder is not trusted, so the choice holds for this session only; trust it (gnosh trust) for it to hold in later ones.")
	}
	return nil
}
