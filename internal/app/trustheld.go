package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/app/agentdef"
	"github.com/gnosh-ai/gnosh/internal/app/skill"
	"github.com/gnosh-ai/gnosh/internal/config"
	"github.com/gnosh-ai/gnosh/internal/trust"
)

// heldList is what a folder that is not trusted keeps waiting: what its
// settings files hold, the programs its git settings name, and what its
// own MCP servers, skills, commands and kinds of sub-agent would run or
// let run (projectHoldings). The trust question shows all of it, since a
// Yes turns all of it on.
func (st *startup) heldList() []trust.Holding {
	var held []trust.Holding
	for _, h := range st.cfg.Held {
		held = append(held, trust.Holding{Kind: h.Kind, Text: h.Text, File: h.Origin.File, Line: h.Origin.Line})
	}
	held = append(held, st.gitPrograms...)
	return append(held, projectHoldings(st.cfg)...)
}

// projectHoldings is what a folder's own definitions would run, or let
// run, once it is trusted, beside what its settings files hold: the MCP
// servers of its .mcp.json, and its skills, commands and kinds of
// sub-agent that have hooks, run commands as they load, let tools run
// without asking, or start MCP servers of their own. The trust question
// listed only the settings files', so a Yes to one harmless option turned
// all of these on unseen, and a folder with nothing else was never asked
// (the config review of 2026-11-19). Safe mode reads none of them.
func projectHoldings(cfg *config.Config) []trust.Holding {
	if cfg.Switches.SafeMode.V {
		return nil
	}
	out := mcpFileHoldings(filepath.Join(cfg.Paths.ProjectRoot, ".mcp.json"))
	skills := skill.Load(skill.Dirs{}, skill.Dirs{Skills: projectDefDirs(cfg, "skills", nil), Commands: projectDefDirs(cfg, "commands", nil)})
	for _, s := range skills.All() {
		if s.Scope != "project" {
			continue
		}
		what := "the skill " + s.Name
		if s.Command {
			what = "the command /" + s.Name
		}
		if hasJSON(s.Hooks) {
			out = append(out, trust.Holding{Kind: trust.KindHook, Text: what + " has hooks of its own, which run commands", File: s.Source})
		}
		if s.Injects {
			out = append(out, trust.Holding{Kind: trust.KindHook, Text: what + " runs commands as it is used (!`...`)", File: s.Source})
		}
		if len(s.AllowedTools) > 0 {
			out = append(out, trust.Holding{Kind: trust.KindAllowRule, Text: what + " lets these run without asking while it is used: " + strings.Join(s.AllowedTools, ", "), File: s.Source})
		}
	}
	for _, d := range agentdef.LoadFrom(nil, projectDefDirs(cfg, "agents", nil)).All() {
		if d.Scope != "project" {
			continue
		}
		what := "the kind of sub-agent " + d.Name
		if hasJSON(d.Hooks) {
			out = append(out, trust.Holding{Kind: trust.KindHook, Text: what + " has hooks of its own, which run commands", File: d.Source})
		}
		for _, srv := range d.MCPServers {
			if len(srv.Inline) == 0 {
				continue // one of the person's own servers, by its name
			}
			if h, ok := mcpHolding(what+" starts the MCP server "+srv.Name, srv.Inline, d.Source); ok {
				out = append(out, h)
			}
		}
	}
	return out
}

// mcpFileHoldings is a holding for each server of a project's .mcp.json,
// as the file writes it (no variable filled in: the question must not put
// a key on the screen). A file that cannot be read is one holding that
// says so: it is there, and a Yes would try it.
func mcpFileHoldings(path string) []trust.Holding {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return []trust.Holding{{Kind: trust.KindMCPLocal, Text: "MCP servers, in a file that cannot be read as JSON", File: path}}
	}
	names := make([]string, 0, len(f.Servers))
	for name := range f.Servers {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []trust.Holding
	for _, name := range names {
		if h, ok := mcpHolding("the MCP server "+name, f.Servers[name], path); ok {
			out = append(out, h)
		}
	}
	return out
}

// mcpHolding is the holding for one server's entry: one started on this
// machine (a command) or one at an address.
func mcpHolding(what string, entry json.RawMessage, file string) (trust.Holding, bool) {
	var e struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		URL     string   `json:"url"`
	}
	if json.Unmarshal(entry, &e) != nil {
		return trust.Holding{Kind: trust.KindMCPLocal, Text: what + ", whose entry cannot be read", File: file}, true
	}
	switch {
	case e.Command != "":
		return trust.Holding{Kind: trust.KindMCPLocal, Text: what + ", run on this machine as " + strings.Join(append([]string{e.Command}, e.Args...), " "), File: file}, true
	case e.URL != "":
		return trust.Holding{Kind: trust.KindMCPOnline, Text: what + " at " + e.URL, File: file}, true
	}
	return trust.Holding{Kind: trust.KindMCPLocal, Text: fmt.Sprintf("%s, of a kind gnosh does not know", what), File: file}, true
}

// hasJSON reports whether a block was given at all.
func hasJSON(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "{}" && s != "[]"
}

// gitRunsPrograms reports whether git, run in this folder, would run
// programs the repository's own settings name, the folder not being
// trusted: gnosh's own look at the repository is held then, and so are
// the person's commands that may call git (the status line, the file
// suggestion command).
func (l *live) gitRunsPrograms() bool {
	return !l.st.trusted && len(l.st.gitPrograms) > 0
}

// ownCommandEnv is the environment of a command of the person's own that
// gnosh starts (the status line, the file suggestion command, gnokey): the
// one gnosh was started with, under the settings' env block, where a
// variable set to nothing is taken away. Appended to the inherited one, as
// it was, a variable the person had taken away stayed in (the config review
// of 2026-11-19).
func (l *live) ownCommandEnv() []string {
	return overlayEnv(l.p.o.Environ, l.cfg.CommandEnv())
}
