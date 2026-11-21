package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestProviders(t *testing.T) {
	cfg := mustLoad(t, withConfig(`{
  "providers": {
    "box": {
      "type": "llamacpp",
      "name": "The box",
      "baseUrl": ["http://127.0.0.1:8080", "http://192.168.1.9:8080/v1"],
      "apiKey": "",
      "listModels": false,
      "extraBody": {"id_slot": 1, "nested": {"list": [1.5, "two", null, true]}},
      "headers": {"X-One": "1", "X-Two": "two words"},
      "disabled": false
    },
    "other": {"type": "openai-compatible", "baseUrl": "https://api.example.com"}
  }
}`))
	if len(cfg.Providers) != 2 {
		t.Fatalf("want 2 providers, got %d", len(cfg.Providers))
	}
	p := cfg.Providers[0]
	if p.ID != "box" || p.Name != "The box" || p.Type != "llamacpp" || p.Wire != "llamacpp" {
		t.Errorf("provider: %+v", p)
	}
	if len(p.Endpoints) != 2 || p.Endpoints[0].Given != "http://127.0.0.1:8080" || p.Endpoints[1].Given != "http://192.168.1.9:8080/v1" {
		t.Errorf("endpoints: %+v", p.Endpoints)
	}
	if p.ListModels.V || !p.ListModels.Set || p.ListModels.Origin.Line != 8 {
		t.Errorf("listModels false was not taken, or not from its own line: %+v", p.ListModels)
	}
	if n, ok := p.ExtraBody["id_slot"].(json.Number); !ok || n.String() != "1" {
		t.Errorf("extra body: %#v", p.ExtraBody)
	}
	if want := map[string]any{"list": []any{json.Number("1.5"), "two", nil, true}}; !reflect.DeepEqual(p.ExtraBody["nested"], want) {
		t.Errorf("extra body, nested: %#v", p.ExtraBody["nested"])
	}
	if len(p.Headers) != 2 || p.Headers[0].Name != "X-One" || p.Headers[1].Value.Reveal() != "two words" {
		t.Errorf("headers: %v", p.Headers)
	}
	if p.Disabled || !p.APIKey.Empty() {
		t.Errorf("provider: %+v", p)
	}
	if p.Origin.File != userJSON || p.Origin.Line != 3 || p.Origin.Layer != LayerUser {
		t.Errorf("origin: %+v", p.Origin)
	}
	if o := cfg.Providers[1]; o.Name != "other" || o.Type != "openai-compatible" || o.Wire != "openai" || !o.ListModels.V || len(o.Endpoints) != 1 {
		t.Errorf("second provider: %+v", o)
	}
}

// Files are merged by key and then field by field: what a stronger file
// gives replaces what was there, what it leaves out stays, and null takes
// away.
func TestProvidersAcrossFiles(t *testing.T) {
	fsys := newFS().put(userJSON, `{
  "providers": {
    "a": {"type": "llamacpp", "name": "First", "baseUrl": ["http://127.0.0.1:1", "http://127.0.0.1:2"],
          "apiKey": "key-of-the-user-0123", "headers": {"X-One": "1", "X-Two": "2"}, "extraBody": {"id_slot": 1}, "listModels": false},
    "b": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:3"},
    "c": {"type": "openai-compatible", "baseUrl": "http://127.0.0.1:4"}
  },
  "models": {"b/m": {"contextWindow": 1000}, "c/m": {}},
  "model": "b/m"
}`).put(projJSON, `{
  "providers": {
    "a": {"baseUrl": "http://127.0.0.1:9", "headers": {"X-Three": "3"}, "disabled": true},
    "b": null
  }
}`)
	cfg := mustLoad(t, inputs(fsys))
	if len(cfg.Providers) != 2 || cfg.Providers[0].ID != "a" || cfg.Providers[1].ID != "c" {
		t.Fatalf("providers: %+v", cfg.Providers)
	}
	a := cfg.Providers[0]
	if a.Name != "First" || a.Type != "llamacpp" || a.APIKey.Reveal() != "key-of-the-user-0123" || a.ListModels.V || !a.ListModels.Set ||
		len(a.ExtraBody) != 1 {
		t.Errorf("what the stronger file left out did not stay: %+v", a)
	}
	if !a.Disabled || len(a.Endpoints) != 1 || a.Endpoints[0].Given != "http://127.0.0.1:9" || len(a.Headers) != 1 || a.Headers[0].Name != "X-Three" {
		t.Errorf("what the stronger file gave did not replace what was there, each field as a whole: %+v", a)
	}
	if a.Origin.File != projJSON || a.Origin.Line != 3 {
		t.Errorf("the provider's origin is the last place that set it: %+v", a.Origin)
	}
	// Taking a provider away takes its models and a choice of one with it.
	if len(cfg.Models) != 1 || cfg.Models[0].Ref() != "c/m" || cfg.Large.Model.Set {
		t.Errorf("removing a provider leaves its models or a choice of one behind: models %v, model %+v", cfg.Models, cfg.Large.Model)
	}

	// null for a field puts it back to not given.
	fsys.put(localJSON, `{"providers": {"a": {"name": null, "apiKey": null, "headers": null, "extraBody": null, "listModels": null, "disabled": null, "baseUrl": null}}}`)
	cfg = mustLoad(t, inputs(fsys))
	a = cfg.Providers[0]
	if a.Name != "a" || !a.APIKey.Empty() || a.Headers != nil || a.ExtraBody != nil || a.ListModels.Set || a.Disabled || len(a.Endpoints) != 0 ||
		!strings.Contains(a.LeftOut, "no address was given") {
		t.Errorf("null did not put the fields back: %+v", a)
	}
}

func TestModels(t *testing.T) {
	text := `{
  "providers": {"q": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8080"}},
  "models": {
    "q/qwen3.8-27b": {
      "name": "Qwen",
      "contextWindow": 262144,
      "maxOutputTokens": 8000,
      "thinkingLevels": ["off", "on"],
      "thinkingBudget": 2048
    },
    "q/org/name:tag": {},
    "q/quiet": {"thinkingLevels": []}
  },
  "model": {"id": "q/qwen3.8-27b", "thinking": false, "temperature": 0.2},
  "smallModel": {"id": "q/org/name:tag", "thinking": "low"}
}`
	cfg := mustLoad(t, withConfig(text))
	if len(cfg.Models) != 3 {
		t.Fatalf("want 3 models, got %+v", cfg.Models)
	}
	m := cfg.Models[0]
	if m.Ref() != "q/qwen3.8-27b" || m.Name != "Qwen" || m.ContextWindow.V != 262144 || m.MaxOutputTokens.V != 8000 ||
		!m.ThinkingLevels.Set || !slices.Equal(m.ThinkingLevels.V, []string{"off", "on"}) || !m.ThinkingBudget.Set || m.ThinkingBudget.V != 2048 {
		t.Errorf("model: %+v", m)
	}
	if o := m.ContextWindow.Origin; o.File != userJSON || o.Line != lineOf(t, text, `"contextWindow"`) || m.Origin.Line != 4 {
		t.Errorf("a field's origin is its own line, the model's the line of its name: %+v %+v", o, m.Origin)
	}
	if other := cfg.Models[1]; other.ID != "org/name:tag" || other.Name != "org/name:tag" || other.ContextWindow.Set || other.ThinkingLevels.Set {
		t.Errorf("a model id may hold slashes after the provider, and an empty object says nothing more: %+v", other)
	}
	if quiet := cfg.Models[2]; !quiet.ThinkingLevels.Set || len(quiet.ThinkingLevels.V) != 0 {
		t.Errorf("an empty list of levels says the model has no thinking switch: %+v", quiet.ThinkingLevels)
	}
	l := cfg.Large
	if l.Model.V.String() != "q/qwen3.8-27b" || l.Thinking.V != "off" || !l.Temperature.Set || l.Temperature.V != 0.2 ||
		l.Model.Origin.Line != lineOf(t, text, `"model"`) {
		t.Errorf("the session's model: %+v", l)
	}
	if s := cfg.Small; s.Model.V.ID != "org/name:tag" || s.Thinking.V != "low" || s.Temperature.Set {
		t.Errorf("the small model: %+v", s)
	}
	if cfg.Providers[0].ListModels.V {
		t.Errorf("a provider with a registered model does not ask its server by default")
	}

	// As a string, and with thinking alone.
	cfg = mustLoad(t, withConfig(`{"providers": {"q": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8080"}}, "model": "q/m", "smallModel": {"thinking": true}}`))
	if cfg.Large.Model.V.String() != "q/m" || cfg.Large.Thinking.Set || cfg.Small.Model.Set || cfg.Small.Thinking.V != "on" || !cfg.Small.Thinking.Set {
		t.Errorf("model as a string, smallModel with thinking alone: %+v %+v", cfg.Large, cfg.Small)
	}
}

// Models and the two choices across files: fields merge, and null takes
// away what a weaker file said.
func TestModelsAcrossFiles(t *testing.T) {
	fsys := newFS().put(userJSON, `{
  "providers": {"q": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8080"}},
  "models": {"q/a": {"name": "A", "contextWindow": 1000, "maxOutputTokens": 100, "thinkingLevels": ["low", "high"]}, "q/b": {"contextWindow": 2000}},
  "model": {"id": "q/a", "thinking": "low", "temperature": 1},
  "smallModel": "q/b"
}`).put(projJSON, `{
  "models": {"q/a": {"contextWindow": 5000, "maxOutputTokens": null}, "q/b": null},
  "model": {"temperature": null, "thinking": "high"},
  "smallModel": null
}`)
	cfg := mustLoad(t, inputs(fsys))
	if len(cfg.Models) != 1 {
		t.Fatalf("models: %+v", cfg.Models)
	}
	a := cfg.Models[0]
	if a.Name != "A" || a.ContextWindow.V != 5000 || a.ContextWindow.Origin.File != projJSON || a.MaxOutputTokens.Set || len(a.ThinkingLevels.V) != 2 {
		t.Errorf("the model's fields, merged: %+v", a)
	}
	if l := cfg.Large; l.Model.V.String() != "q/a" || l.Model.Origin.File != userJSON || l.Thinking.V != "high" || l.Thinking.Origin.File != projJSON || l.Temperature.Set {
		t.Errorf("the session's model, merged: %+v", l)
	}
	if cfg.Small.Model.Set {
		t.Errorf("null did not take the small model away: %+v", cfg.Small)
	}
	fsys.put(localJSON, `{"model": {"id": null}}`)
	if cfg = mustLoad(t, inputs(fsys)); cfg.Large.Model.Set || cfg.Large.Thinking.V != "high" {
		t.Errorf("null for the id takes the choice away and leaves the rest: %+v", cfg.Large)
	}
}

func TestHosts(t *testing.T) {
	fsys := newFS().put(userJSON, `{
  "hosts": {
    "render": {"ssh": "root@203.0.113.10", "description": "the render box"},
    "web": {"ssh": "deploy@203.0.113.11"},
    "alias": {"ssh": "my-alias"}
  }
}`)
	cfg := mustLoad(t, inputs(fsys))
	want := []Host{
		{Name: "render", SSH: "root@203.0.113.10", Description: "the render box"},
		{Name: "web", SSH: "deploy@203.0.113.11"},
		{Name: "alias", SSH: "my-alias"},
	}
	check := func(want []Host) {
		t.Helper()
		if len(cfg.Hosts) != len(want) {
			t.Fatalf("hosts: %+v", cfg.Hosts)
		}
		for i, h := range cfg.Hosts {
			if h.Name != want[i].Name || h.SSH != want[i].SSH || h.Description != want[i].Description {
				t.Errorf("host %d: got %+v, want %+v", i, h, want[i])
			}
		}
	}
	check(want)
	if o := cfg.Hosts[1].Origin; o.File != userJSON || o.Line != 4 {
		t.Errorf("a host's origin: %+v", o)
	}
	// A stronger file takes a host away with null, and may then declare
	// the name itself: the name stands for one machine at every moment.
	fsys.put(projJSON, `{"hosts": {"web": null, "alias": null}}`).put(localJSON, `{"hosts": {"web": {"ssh": "deploy@203.0.113.12"}}}`)
	cfg = mustLoad(t, inputs(fsys))
	check([]Host{want[0], {Name: "web", SSH: "deploy@203.0.113.12"}})
}

// The rules of permissions: every kind of rule gnosh takes, each kept
// once, in the layer of its file.
func TestPermissionRules(t *testing.T) {
	cfg := mustLoad(t, withConfig(`{
  "permissions": {
    "allow": ["Read", "Bash(git diff *)", "Edit(/src/**)", "mcp__github__get_issue", "mcp__github__get_*", "Read"],
    "deny": ["Bash",
             "Bash(git push *)", "WebFetch", "*"]
  }
}`))
	if len(cfg.Permissions.Layers) != 1 {
		t.Fatalf("layers: %+v", cfg.Permissions.Layers)
	}
	l := cfg.Permissions.Layers[0]
	texts := func(rules []Rule) []string {
		var out []string
		for _, r := range rules {
			out = append(out, r.Text)
		}
		return out
	}
	if got := texts(l.Allow); !slices.Equal(got, []string{"Read", "Bash(git diff *)", "Edit(/src/**)", "mcp__github__get_issue", "mcp__github__get_*"}) {
		t.Errorf("allow: %q", got)
	}
	if got := texts(l.Deny); !slices.Equal(got, []string{"Bash", "Bash(git push *)", "WebFetch", "*"}) {
		t.Errorf("deny: %q", got)
	}
	if l.Scope != "user" || l.File != userJSON || l.BaseDir != userDir {
		t.Errorf("layer source: %+v", l)
	}
	if r := cfg.Permissions.Rules("deny")[1]; r.Origin.Line != 5 {
		t.Errorf("a rule's origin is its own line: %+v", r.Origin)
	}
}

// Every option key, with a value it takes, and the field it lands in.
func TestOptions(t *testing.T) {
	text := `{
  "options": {
    "requestTimeout": 900,
    "autoCompactEnabled": false,
    "autoCompactWindow": 200000,
    "loopMaxTurns": 25,
    "maxBackgroundTasks": 7,
    "bashOutputMaxChars": 12000,
    "userInstructionFiles": ["~/notes", "/etc/gnosh/RULES.md", "rules"],
    "projectInstructionFiles": ["docs/AGENT-NOTES.md"],
    "hiddenSkills": ["pdf", "xlsx", "pdf"],
    "claudeMdExcludes": ["**/vendor/**/CLAUDE.md", "/work/old"],
    "memoryText": "full",
    "memReviewCloses": false,
    "memEmbeddingsUrl": "http://127.0.0.1:8090",
    "webSearchUrl": "http://127.0.0.1:8888",
    "deliverablesServer": "127.0.0.1:8123",
    "debug": true,
    "sidePanel": true,
    "verbose": true,
    "projectDataDir": ".agent",
    "outputStyle": "Concise",
    "editorMode": "vim",
    "preferredNotifChannel": "terminal_bell",
    "vimInsertModeRemaps": ["jj", "jk"],
    "promptSuggestionEnabled": true,
    "respondToBashCommands": true,
    "fileCheckpointingEnabled": false,
    "askUserQuestionTimeout": "5m",
    "theme": "light",
    "viewMode": "focus",
    "showTurnDuration": false,
    "respectGitignore": false,
    "language": "japanese",
    "includeGitInstructions": false
  }
}`
	cfg := mustLoad(t, withConfig(text))
	o := cfg.Options
	check := func(name string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	items := func(list []ListItem) []string {
		var out []string
		for _, it := range list {
			out = append(out, it.V)
		}
		return out
	}
	at := func(key string) Origin {
		return Origin{Layer: LayerUser, File: userJSON, Line: lineOf(t, text, `"`+key+`"`)}
	}
	check("requestTimeout", o.RequestTimeout, Value[int]{V: 900, Set: true, Origin: at("requestTimeout")})
	check("autoCompactEnabled", o.AutoCompactEnabled, Value[bool]{V: false, Set: true, Origin: at("autoCompactEnabled")})
	check("autoCompactWindow", o.AutoCompactWindow.V, 200000)
	check("loopMaxTurns", o.LoopMaxTurns.V, 25)
	check("maxBackgroundTasks", o.MaxBackgroundTasks.V, 7)
	check("bashOutputMaxChars", o.BashOutputMaxChars.V, 12000)
	check("userInstructionFiles", items(o.UserInstructionFiles), []string{testHome + "/notes", "/etc/gnosh/RULES.md", userDir + "/rules"})
	check("projectInstructionFiles", items(o.ProjectInstructionFiles), []string{testCwd + "/docs/AGENT-NOTES.md"})
	check("hiddenSkills", items(o.HiddenSkills), []string{"pdf", "xlsx"})
	check("claudeMdExcludes", items(o.ClaudeMdExcludes), []string{"**/vendor/**/CLAUDE.md", "/work/old"})
	check("the files left out", items(cfg.Context.Exclude), []string{"**/vendor/**/CLAUDE.md", "/work/old"})
	check("memoryText", o.MemoryText.V, "full")
	check("memReviewCloses", o.MemReviewCloses.V, false)
	check("memEmbeddingsUrl", o.MemEmbeddingsURL.V, "http://127.0.0.1:8090")
	check("webSearchUrl", o.WebSearchURL.V, "http://127.0.0.1:8888")
	check("deliverablesServer", o.DeliverablesServer, Value[string]{V: "127.0.0.1:8123", Set: true, Origin: at("deliverablesServer")})
	check("debug", o.Debug.V, true)
	check("sidePanel", o.SidePanel.V, true)
	check("verbose", o.Verbose.V, true)
	check("projectDataDir", o.ProjectDataDir.V, ".agent")
	check("outputStyle", o.OutputStyle.V, "Concise")
	check("editorMode", o.EditorMode.V, "vim")
	check("preferredNotifChannel", o.PreferredNotifChannel.V, "terminal_bell")
	check("vimInsertModeRemaps", items(o.VimInsertModeRemaps), []string{"jj", "jk"})
	check("promptSuggestionEnabled", o.PromptSuggestionEnabled.V, true)
	check("respondToBashCommands", o.RespondToBashCommands.V, true)
	check("fileCheckpointingEnabled", o.FileCheckpointingEnabled.V, false)
	check("askUserQuestionTimeout", o.AskUserQuestionTimeout.V, "5m")
	check("theme", o.Theme.V, "light")
	check("viewMode", o.ViewMode.V, "focus")
	check("showTurnDuration", o.ShowTurnDuration.V, false)
	check("respectGitignore", o.RespectGitignore.V, false)
	check("language", o.Language.V, "japanese")
	check("includeGitInstructions", o.IncludeGitInstructions.V, false)

	// The table lists every key exactly once, All reports each, and the
	// file above sets every one of them.
	all := o.All()
	if len(all) != len(optionDefs) {
		t.Fatalf("All lists %d options, the table has %d", len(all), len(optionDefs))
	}
	for _, v := range all {
		if !v.Set {
			t.Errorf("option %s is not set by the file of this test, or is reported as not set", v.Key)
		}
		if !strings.Contains(text, `"`+v.Key+`"`) {
			t.Errorf("option %s is not in the file of this test", v.Key)
		}
	}
}

// Where the files the agent delivers are served: this machine only, at
// port 7777, until the option moves it to another port of this machine or
// switches it off.
func TestDeliverablesServerOption(t *testing.T) {
	if got := mustLoad(t, withConfig(`{}`)).Options.DeliverablesServer; got.V != "127.0.0.1:7777" || got.Set {
		t.Errorf("with nothing set the option is %+v, want 127.0.0.1:7777 as the default", got)
	}
	for text, want := range map[string]string{
		`"off"`:            ServerOff,
		`"OFF"`:            ServerOff,
		`" off "`:          ServerOff,
		`"127.0.0.1:8080"`: "127.0.0.1:8080",
		`"127.0.0.1:0"`:    "127.0.0.1:0",
		`"127.9.9.9:7777"`: "127.9.9.9:7777",
		`"localhost:7777"`: "localhost:7777",
		`"[::1]:7777"`:     "[::1]:7777",
	} {
		cfg := mustLoad(t, withConfig(`{"options": {"deliverablesServer": `+text+`}}`))
		if got := cfg.Options.DeliverablesServer; got.V != want || !got.Set {
			t.Errorf("deliverablesServer %s is kept as %+v, want %q", text, got, want)
		}
	}
	// null puts it back, and the listing of the configuration shows it.
	cfg := mustLoad(t, inputs(newFS().
		put(userJSON, `{"options": {"deliverablesServer": "off"}}`).
		put(projJSON, `{"options": {"deliverablesServer": null}}`)))
	if got := cfg.Options.DeliverablesServer; got.V != DefaultDeliverablesServer || got.Set {
		t.Errorf("null left the option as %+v", got)
	}
	cfg = mustLoad(t, withConfig(`{"options": {"deliverablesServer": "off"}}`))
	found := false
	for _, v := range cfg.Options.All() {
		found = found || (v.Key == "deliverablesServer" && v.Text == "off" && v.Set)
	}
	if !found {
		t.Errorf("the listing of the options does not show deliverablesServer as off: %+v", cfg.Options.All())
	}
}

// null puts an option back to its default, and takes away what weaker
// files added to a list; lists add up across files.
func TestOptionDefaultsAndNull(t *testing.T) {
	fsys := newFS().
		put(userJSON, `{"options": {"memoryText": "full", "maxBackgroundTasks": 3, "hiddenSkills": ["a", "b"], "userInstructionFiles": ["/one"]}}`).
		put(projJSON, `{"options": {"memoryText": null, "maxBackgroundTasks": null, "userInstructionFiles": ["/two", "/one"]}}`)
	cfg := mustLoad(t, inputs(fsys))
	o := cfg.Options
	if o.MemoryText.V != "lean" || o.MemoryText.Set || o.MaxBackgroundTasks.V != 50 || o.MaxBackgroundTasks.Set {
		t.Errorf("null did not put the defaults back: %+v %+v", o.MemoryText, o.MaxBackgroundTasks)
	}
	if len(o.UserInstructionFiles) != 2 || o.UserInstructionFiles[0].V != "/one" || o.UserInstructionFiles[1].V != "/two" || len(o.HiddenSkills) != 2 {
		t.Errorf("lists add up across files, each value once: %+v %+v", o.UserInstructionFiles, o.HiddenSkills)
	}
	fsys.put(localJSON, `{"options": {"hiddenSkills": null}}`)
	fsys.put("/work/x.json", `{"options": {"hiddenSkills": ["c"]}}`)
	in := inputs(fsys)
	in.Flags.Settings = "/work/x.json"
	cfg = mustLoad(t, in)
	if got := cfg.Options.HiddenSkills; len(got) != 1 || got[0].V != "c" {
		t.Errorf("null clears what weaker files added to a list: %+v", got)
	}

	def := mustLoad(t, inputs(newFS())).Options
	if !def.AutoCompactEnabled.V || !def.MemReviewCloses.V || def.MemoryText.V != "lean" || def.ProjectDataDir.V != ".gnosh" || def.Debug.V ||
		def.RequestTimeout.Set || def.MaxBackgroundTasks.V != 50 || def.AutoCompactWindow.Set || def.MemEmbeddingsURL.Set {
		t.Errorf("defaults: %+v", def)
	}
	for _, v := range def.All() {
		if v.Set || v.Origin.Layer != LayerDefault || v.Text == "" {
			t.Errorf("with no config, option %s is reported as set by %v, or has no default to show", v.Key, v.Origin)
		}
	}
}

// The order of a file's keys does not matter: a model may name a provider
// that the same file declares further down.
func TestKeyOrderDoesNotMatter(t *testing.T) {
	cfg := mustLoad(t, withConfig(`{
  "smallModel": "q/small",
  "model": "q/m",
  "models": {"q/m": {"contextWindow": 1000}},
  "providers": {"q": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8080"}}
}`))
	if cfg.Large.Model.V.String() != "q/m" || cfg.Small.Model.V.String() != "q/small" || len(cfg.Models) != 1 {
		t.Errorf("a file with its keys in another order: %+v %+v", cfg.Large, cfg.Models)
	}
}

// Variables are filled in inside a provider's addresses, key and header
// values, and nowhere else.
func TestVariables(t *testing.T) {
	text := `{
  "providers": {
    "box": {
      "type": "openai-compatible",
      "name": "${NAME} $NAME",
      "baseUrl": ["http://${BOX_HOST}:${BOX_PORT:-8080}/v1", "https://${EMPTY}api.example.com${UNSET:-}", "http://127.0.0.1:1/$BOX_HOST"],
      "apiKey": "${BOX_KEY}",
      "headers": {"Authorization": "Bearer ${BOX_KEY}", "X-Plain": "$BOX_KEY ${UNSET:-a default} $", "X-Empty": "${EMPTY:-not used}"},
      "extraBody": {"note": "${BOX_KEY}"}
    }
  },
  "hosts": {"h": {"ssh": "a@203.0.113.1", "description": "${BOX_HOST}"}},
  "env": {"KEEP": "${BOX_KEY}"},
  "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "${CLAUDE_PROJECT_DIR}/x.sh ${BOX_HOST}"}]}]}
}`
	in := withConfig(text)
	in.Environ = append(in.Environ, "BOX_HOST=box.example.com", "BOX_KEY=bk-0123456789abcdef", "EMPTY=", "NAME=nope")
	cfg, problems := load(t, in)
	if cfg == nil || len(problems) != 0 {
		t.Fatalf("the file must load without a word:\n%s", showProblems(problems))
	}
	p := cfg.Providers[0]
	var given []string
	for _, e := range p.Endpoints {
		given = append(given, e.Given)
	}
	if !slices.Equal(given, []string{"http://box.example.com:8080/v1", "https://api.example.com", "http://127.0.0.1:1/$BOX_HOST"}) {
		t.Errorf("addresses: %q", given)
	}
	if p.APIKey.Reveal() != "bk-0123456789abcdef" || p.KeyFrom != "$BOX_KEY" {
		t.Errorf("a key that is one variable is shown by the variable's name: %+v", p)
	}
	headers := map[string]string{}
	for _, h := range p.Headers {
		headers[h.Name] = h.Value.Reveal()
	}
	want := map[string]string{"Authorization": "Bearer bk-0123456789abcdef", "X-Plain": "$BOX_KEY a default $", "X-Empty": ""}
	if !reflect.DeepEqual(headers, want) {
		t.Errorf("headers: %q, want %q (a variable that is set and empty counts as set)", headers, want)
	}
	// Nowhere else: the name, the extra body, a host, the env block and a
	// hook's command are as written.
	if p.Name != "${NAME} $NAME" || p.ExtraBody["note"] != "${BOX_KEY}" || cfg.Hosts[0].Description != "${BOX_HOST}" ||
		cfg.Env[0].Value.Reveal() != "${BOX_KEY}" || cfg.Hooks[0].Command != "${CLAUDE_PROJECT_DIR}/x.sh ${BOX_HOST}" {
		t.Errorf("a variable was filled in outside a provider's address, key or header: %+v", p)
	}
	if dump := cfg.String(); strings.Contains(dump, "bk-0123456789abcdef") || !strings.Contains(dump, "key: from $BOX_KEY") {
		t.Errorf("the listing shows the key, or not where it came from:\n%s", dump)
	}

	// A key written out is not said to come from a variable, and neither
	// is one made of more than a variable.
	in = withConfig(`{"providers": {"a": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:1", "apiKey": "x-${BOX_KEY}"},
  "b": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:1", "apiKey": "${NOPE:-fallback-key-0123}"}}}`)
	in.Environ = append(in.Environ, "BOX_KEY=bk-0123456789abcdef")
	cfg = mustLoad(t, in)
	if a, b := cfg.Providers[0], cfg.Providers[1]; a.KeyFrom != "" || a.APIKey.Reveal() != "x-bk-0123456789abcdef" || b.KeyFrom != "" || b.APIKey.Reveal() != "fallback-key-0123" {
		t.Errorf("keys: %+v %+v", a, b)
	}
}

// A variable that is not set and has no default is never read as nothing:
// the provider is left out, and a warning names the variable, the key and
// the line. gnosh still starts.
func TestUnsetVariableLeavesTheProviderOut(t *testing.T) {
	text := `{
  "providers": {
    "qwen": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8080"},
    "claude": {
      "type": "anthropic",
      "apiKey": "${CLAUDE_KEY}"
    },
    "far": {"type": "openai-compatible", "baseUrl": ["https://${FAR_HOST}/v1", "https://api.example.com"],
            "headers": {"X-One": "${FAR_ONE}${FAR_TWO}"}},
    "off": {"type": "llamacpp", "baseUrl": "http://${NOPE}:1", "disabled": true}
  },
  "model": "qwen/m"
}`
	in := withConfig(text)
	in.Environ = append(in.Environ, "ANTHROPIC_API_KEY=sk-ant-not-the-one-asked-for")
	cfg, problems := load(t, in)
	if cfg == nil {
		t.Fatalf("an unset variable must not stop startup:\n%s", showProblems(problems))
	}
	wantProblem(t, problems, Warning, userJSON, lineOf(t, text, `"apiKey"`), "provider claude is left out", `its "apiKey" is filled in from the environment variable CLAUDE_KEY, which is not set`, "gnosh starts without it", "${CLAUDE_KEY:-}")
	wantProblem(t, problems, Warning, userJSON, lineOf(t, text, `"far"`), "provider far is left out", `its "baseUrl" is filled in from the environment variable FAR_HOST, which is not set`)
	if len(problems) != 2 {
		t.Errorf("one warning for each provider left out, none for a disabled one:\n%s", showProblems(problems))
	}
	usable := cfg.Usable()
	if len(usable) != 1 || usable[0].ID != "qwen" {
		t.Errorf("usable: %+v", usable)
	}
	if claude := cfg.Providers[1]; !strings.Contains(claude.LeftOut, "CLAUDE_KEY") || claude.Usable() {
		t.Errorf("claude: %+v", claude)
	}
	findings := doctor(t, in, &fakeMachine{})
	wantFinding(t, findings, "providers", Note, "claude is left out", "CLAUDE_KEY, which is not set")
	if !CanRun(findings) {
		t.Errorf("one usable provider is enough: %s", showFindings(findings))
	}

	// A stronger file that gives the field stands for the weaker one's,
	// and its variables with it.
	fsys := newFS().put(userJSON, text).put(projJSON, `{"providers": {"claude": {"apiKey": "sk-ant-written-out-0123"},
  "far": {"baseUrl": "https://api.example.com", "headers": null}}}`)
	cfg, problems = load(t, inputs(fsys))
	if cfg == nil || len(problems) != 0 || len(cfg.Usable()) != 3 {
		t.Errorf("with the fields given by a stronger file nothing is left out:\n%s", showProblems(problems))
	}

	// With the variables set, nothing is left out either; two variables of
	// one field are both named when both are missing.
	in = withConfig(text)
	in.Environ = append(in.Environ, "CLAUDE_KEY=sk-ant-from-the-variable-0123", "FAR_HOST=far.example.com")
	_, problems = load(t, in)
	wantProblem(t, problems, Warning, userJSON, lineOf(t, text, `"headers"`), "provider far is left out", `its "headers" is filled in from the environment variables FAR_ONE, FAR_TWO, which are not set`)
	if len(problems) != 1 {
		t.Errorf("want the one warning:\n%s", showProblems(problems))
	}
}

// Every way a file's own keys can be wrong, with the words the message
// must hold. Each case is a user gnosh.json; at is a piece of the line the
// mistake must be reported on.
func TestFileRefusals(t *testing.T) {
	const q = `"providers": {"q": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:1"}}`
	tests := []struct {
		name   string
		text   string
		at     string
		pieces []string
	}{
		// The top of the file.
		{"an unknown key", "{\n  \"provders\": {}\n}", "provders", []string{`"provders"`, "not a key gnosh knows", "providers, models, model, smallModel, fallbackModel, hosts, options, permissions, hooks, env, disableAllHooks"}},
		{"an option at the top", "{\n  \"requestTimeout\": 900\n}", "requestTimeout", []string{`"requestTimeout" is one of gnosh's options`, `"options": {"requestTimeout": …}`}},
		{"a Claude Code option at the top", `{"autoCompactWindow": 200000}`, "", []string{`"autoCompactWindow" is one of gnosh's options`, `inside "options"`}},
		{"a setting Claude Code has and gnosh has not built", "{\n  \"spinnerVerbs\": {\"mode\": \"append\", \"verbs\": [\"Pondering\"]}\n}", "spinnerVerbs", []string{`"spinnerVerbs" is a setting Claude Code has and gnosh has not built`, "would do nothing here", "Take it out"}},
		{"a status line of another type", `{"statusLine": {"type": "static", "command": "x"}}`, "", []string{`the status line's "type" is "command"`}},
		{"a status line with no command", `{"statusLine": {"type": "command"}}`, "", []string{`the status line has no "command"`}},
		{"a status line refreshed too often", `{"statusLine": {"type": "command", "command": "x", "refreshInterval": 0.5}}`, "", []string{`"refreshInterval" is a number of seconds, at least 1`}},
		{"a status line field gnosh does not know", `{"statusLine": {"type": "command", "command": "x", "color": "red"}}`, "", []string{"the status line has no field"}},
		{"a file suggestion of another type", `{"fileSuggestion": {"type": "static", "command": "x"}}`, "", []string{`the file suggestion's "type" is "command"`}},
		{"a file suggestion with no command", `{"fileSuggestion": {"type": "command"}}`, "", []string{`the file suggestion has no "command"`}},
		{"a file suggestion field gnosh does not know", `{"fileSuggestion": {"type": "command", "command": "x", "limit": 5}}`, "", []string{"the file suggestion has no field"}},
		{"a file suggestion that is not an object", `{"fileSuggestion": "x"}`, "", []string{`"fileSuggestion" is an object`}},
		{"attribution true", `{"attribution": true}`, "", []string{`"attribution" is an object`, "or false for none"}},
		{"an attribution that is not text", `{"attribution": {"commit": 3}}`, "", []string{`the attribution's "commit" is the text added`}},
		{"an attribution field gnosh does not know", `{"attribution": {"commit": "x", "footer": "y"}}`, "", []string{"the attribution has no field"}},
		{"includeCoAuthoredBy not a boolean", `{"includeCoAuthoredBy": "no"}`, "", []string{`"includeCoAuthoredBy" is true or false`}},
		{"another one", `{"cleanupPeriodDays": 30}`, "", []string{`"cleanupPeriodDays" is a setting Claude Code has and gnosh has not built`}},
		{"a key of the earlier format", `{"mcp": {}}`, "", []string{`"mcp"`, "not a key gnosh knows"}},
		{"a key given twice", "{\n  \"options\": {},\n  \"options\": {}\n}", "\"options\": {}\n}", []string{`"options"`, "given twice", "line 2"}},
		{"a bare list", `["providers"]`, "", []string{"holds one JSON object", "a list", "none of it is in force"}},

		// providers.
		{"providers of the wrong shape", `{"providers": [{"id": "x"}]}`, "", []string{`"providers" is an object`, `"local": {"type": "llamacpp"`, "a list"}},
		{"a provider of the wrong shape", `{"providers": {"x": "llamacpp"}}`, "", []string{"the provider x is an object", "type, name, baseUrl, apiKey, headers, extraBody, listModels, disabled", "a string"}},
		{"a bad id", `{"providers": {"a/b": {"type": "llamacpp"}}}`, "", []string{"not a usable id for a provider", "letters, digits, dots, dashes and underscores"}},
		{"an unknown field", "{\"providers\": {\"x\": {\"type\": \"llamacpp\",\n \"baseURL\": \"http://h\"}}}", "baseURL", []string{`"baseURL"`, "not a field of a provider", "baseUrl"}},
		{"a field given twice", "{\"providers\": {\"x\": {\"type\": \"llamacpp\",\n \"type\": \"openai\"}}}", "\"openai\"", []string{`"providers.x.type"`, "given twice"}},
		{"an unknown provider type", `{"providers": {"x": {"type": "llamacp"}}}`, "", []string{`"llamacp"`, "not a provider type", "The types are: llamacpp, openai-compatible, anthropic, openai."}},
		{"a server's name that is no type of its own", `{"providers": {"x": {"type": "vllm", "baseUrl": "http://127.0.0.1:1"}}}`, "", []string{`"vllm"`, "not a provider type", "The types are: llamacpp, openai-compatible, anthropic, openai."}},
		{"a type that is not a string", `{"providers": {"x": {"type": 5}}}`, "", []string{`"providers.x.type" is one of`, "and here it is 5"}},
		{"a type of null", `{"providers": {"x": {"type": null}}}`, "", []string{`"providers.x.type" cannot be null`, "write null for the provider itself"}},
		{"first declared without a type", `{"providers": {"x": {"baseUrl": "http://127.0.0.1:1"}}}`, "", []string{`the provider x has no "type"`, "where it is first declared", "llamacpp"}},
		{"an address with no scheme", `{"providers": {"x": {"type": "llamacpp", "baseUrl": "127.0.0.1:8080"}}}`, "", []string{`the address given in "providers.x.baseUrl" cannot be used`, "http:// or https://"}},
		{"an address with no host", `{"providers": {"x": {"type": "llamacpp", "baseUrl": "http://"}}}`, "", []string{"cannot be used", "names no host"}},
		{"an empty address", `{"providers": {"x": {"type": "llamacpp", "baseUrl": ""}}}`, "", []string{"cannot be used", "empty"}},
		{"an address in a list that is wrong", "{\"providers\": {\"x\": {\"type\": \"llamacpp\", \"baseUrl\": [\"http://127.0.0.1:1\",\n \"box:8080\"]}}}", "box:8080", []string{"cannot be used", "http:// or https://"}},
		{"an empty list of addresses", `{"providers": {"x": {"type": "llamacpp", "baseUrl": []}}}`, "", []string{`"providers.x.baseUrl" is an address as a string`, "an empty list"}},
		{"an address that is a number", `{"providers": {"x": {"type": "llamacpp", "baseUrl": 8080}}}`, "", []string{`"providers.x.baseUrl" is an address as a string`, "or a list of addresses to try in order", "8080"}},
		{"extra body that is not an object", `{"providers": {"x": {"type": "llamacpp", "extraBody": [1]}}}`, "", []string{`"providers.x.extraBody" is a JSON object`, `{"id_slot": 1}`}},
		{"a key twice in the extra body", "{\"providers\": {\"x\": {\"type\": \"llamacpp\", \"extraBody\": {\"a\": 1,\n \"a\": 2}}}}", "\"a\": 2", []string{`"providers.x.extraBody.a"`, "given twice"}},
		{"headers that are not an object", `{"providers": {"x": {"type": "llamacpp", "headers": ["X-One: 1"]}}}`, "", []string{`"providers.x.headers" is an object of header names and their values`}},
		{"a bad header name", `{"providers": {"x": {"type": "llamacpp", "headers": {"X One": "v"}}}}`, "", []string{"not a usable HTTP header name"}},
		{"a header value that is not a string", `{"providers": {"x": {"type": "llamacpp", "headers": {"X-One": 1}}}}`, "", []string{"the value of the header X-One", "is a string", "a number"}},
		{"a truth value in quotes", `{"providers": {"x": {"type": "llamacpp", "disabled": "yes"}}}`, "", []string{`"providers.x.disabled" is true or false`, "a string"}},
		{"listModels as a number", `{"providers": {"x": {"type": "llamacpp", "listModels": 1}}}`, "", []string{`"providers.x.listModels" is true or false`}},
		{"taking away what is not there", `{"providers": {"nope": null}}`, "", []string{`"providers" takes nope away with null`, "nothing to take away", "none"}},
		{"a brace left open in a variable", `{"providers": {"x": {"type": "llamacpp", "baseUrl": "http://${HOST:8080"}}}`, "", []string{`"providers.x.baseUrl" holds ${ with no } after it`, "${NAME:-default}"}},
		{"something of the shell's in a variable", `{"providers": {"x": {"type": "llamacpp", "apiKey": "${KEY:?export it first}"}}}`, "", []string{`"providers.x.apiKey" holds ${ and }`, "not a variable's name", "nothing else is filled in"}},
		{"a command in a variable", `{"providers": {"x": {"type": "llamacpp", "headers": {"X-One": "${$(pass show key)}"}}}}`, "", []string{`"providers.x.headers.X-One" holds ${ and }`, "not a variable's name"}},
		{"a variable gnosh sets for hooks", `{"providers": {"x": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:1/${CLAUDE_PROJECT_DIR}"}}}`, "", []string{`"providers.x.baseUrl" names the variable CLAUDE_PROJECT_DIR`, "which gnosh sets for each hook and command it runs", "cannot be filled in here"}},
		{"another variable gnosh sets", `{"providers": {"x": {"type": "llamacpp", "apiKey": "${GNOSH_SESSION_ID}"}}}`, "", []string{`"providers.x.apiKey" names the variable GNOSH_SESSION_ID`, "which gnosh sets"}},

		// models.
		{"models of the wrong shape", `{` + q + `, "models": ["q/m"]}`, "", []string{`"models" is an object`, `"<provider>/<model id>"`}},
		{"a model with no provider in front", `{` + q + `, "models": {"qwen": {}}}`, "", []string{`"models" names a model as "<provider>/<model id>"`, "no provider in front"}},
		{"a model with a blank in its name", `{` + q + `, "models": {"q/a b": {}}}`, "", []string{"not a usable model name"}},
		{"a model on a provider that is not there", `{` + q + `, "models": {"nope/m": {}}}`, "", []string{`"models" registers nope/m`, "no provider named nope", `Declare it under "providers"`, "The providers so far are: q"}},
		{"a model of the wrong shape", `{` + q + `, "models": {"q/m": 262144}}`, "", []string{"the model q/m is an object", "name, contextWindow, maxOutputTokens, thinkingLevels, thinkingBudget", "262144"}},
		{"an unknown field of a model", `{` + q + `, "models": {"q/m": {"window": 1000}}}`, "", []string{`"window"`, "not a field of a model", "contextWindow"}},
		{"a window that is not a number", `{` + q + `, "models": {"q/m": {"contextWindow": "big"}}}`, "", []string{`"models.q/m.contextWindow" is a number of tokens`, "a string"}},
		{"a window of zero", `{` + q + `, "models": {"q/m": {"contextWindow": 0}}}`, "", []string{"a whole number of at least 1", "Here it is 0"}},
		{"a window with a fraction", `{` + q + `, "models": {"q/m": {"contextWindow": 262144.5}}}`, "", []string{"written with digits alone", "262144.5"}},
		{"a window with an exponent", `{` + q + `, "models": {"q/m": {"maxOutputTokens": 8e3}}}`, "", []string{`"models.q/m.maxOutputTokens"`, "written with digits alone", "8e3"}},
		{"levels that are not a list", `{` + q + `, "models": {"q/m": {"thinkingLevels": "off on"}}}`, "", []string{`"models.q/m.thinkingLevels" is the list of the model's thinking levels`, `["low", "medium", "high"]`}},
		{"a level that is not a word", `{` + q + `, "models": {"q/m": {"thinkingLevels": ["off", "ON!"]}}}`, "", []string{"not a usable thinking level", "lower-case word"}},
		{"a level twice", `{` + q + `, "models": {"q/m": {"thinkingLevels": ["off", "off"]}}}`, "", []string{"the thinking level off stands twice"}},
		{"taking away a model that is not there", `{` + q + `, "models": {"q/m": null}}`, "", []string{`"models" takes q/m away with null`, "nothing to take away"}},

		// model and smallModel.
		{"a model that is a list", `{` + q + `, "model": ["q/m"]}`, "", []string{`"model" is the model as a string, "<provider>/<model id>", or an object when how the model is asked matters`, `{"id": "<provider>/<model id>", "thinking": false, "temperature": 0.7}`, "a list"}},
		{"a small model that is a number", `{` + q + `, "smallModel": 3}`, "", []string{`"smallModel" is the model as a string`, "or an object when how the model is asked matters", "Here it is 3"}},
		{"a model with no provider in front", `{` + q + `, "model": "opus"}`, "", []string{`"model" names a model as "<provider>/<model id>"`, `"opus" has no provider in front`}},
		{"a model on a provider that is not there", `{` + q + `, "model": "nope/m"}`, "", []string{`"model" names nope/m`, "no provider named nope"}},
		{"a small model on a provider that is not there", `{` + q + `, "smallModel": {"id": "nope/m"}}`, "", []string{`"smallModel" names nope/m`, "no provider named nope"}},
		{"an empty object", `{` + q + `, "model": {}}`, "", []string{`"model" is an empty object`, "says nothing"}},
		{"an unknown field of a choice", `{` + q + `, "model": {"id": "q/m", "effort": "low"}}`, "", []string{`"effort"`, `not a field of "model"`, "id, thinking, temperature"}},
		{"an id that is not a string", `{` + q + `, "model": {"id": 7}}`, "", []string{`"model.id" is the model`, "here it is 7"}},
		{"a thinking level that is not a word", `{` + q + `, "model": {"thinking": "LOUD"}}`, "", []string{`"LOUD"`, `not a usable thinking level for "model.thinking"`, "true, false"}},
		{"thinking as a number", `{` + q + `, "model": {"thinking": 1}}`, "", []string{`"model.thinking" is true, false, or one of the model's own levels`}},
		{"a temperature out of range", `{` + q + `, "model": {"temperature": 9}}`, "", []string{`"model.temperature" is a number from 0 to 2`, "9"}},
		{"a temperature in quotes", `{` + q + `, "smallModel": {"temperature": "0.5"}}`, "", []string{`"smallModel.temperature" is a number from 0 to 2`, "a string"}},

		// hosts.
		{"hosts of the wrong shape", `{"hosts": ["render"]}`, "", []string{`"hosts" is an object`, `"ssh": "root@203.0.113.10"`}},
		{"a host of the wrong shape", `{"hosts": {"h": "a@203.0.113.1"}}`, "", []string{"a host is an object", `"ssh"`, "a string"}},
		{"a host named local", `{"hosts": {"local": {"ssh": "a@203.0.113.1"}}}`, "", []string{"a host cannot be named local", "this machine"}},
		{"a host with no destination", `{"hosts": {"h": {"description": "x"}}}`, "", []string{"the host h has no ssh destination"}},
		{"an empty destination", `{"hosts": {"h": {"ssh": ""}}}`, "", []string{"the host h has no ssh destination"}},
		{"a destination that starts with a dash", `{"hosts": {"h": {"ssh": "-oProxyCommand=x"}}}`, "", []string{"starts with a dash", "ssh would read it as an option"}},
		{"a destination with shell syntax", `{"hosts": {"h": {"ssh": "a@b; rm -rf ~"}}}`, "", []string{"characters a shell would act on"}},
		{"a destination with a space", `{"hosts": {"h": {"ssh": "a@b -p 22"}}}`, "", []string{"characters a shell would act on", "~/.ssh/config"}},
		{"a bad host name", `{"hosts": {"my host": {"ssh": "a@203.0.113.1"}}}`, "", []string{"not a usable name for a host"}},
		{"a destination that is not a string", `{"hosts": {"h": {"ssh": ["a@203.0.113.1"]}}}`, "", []string{`the "ssh" of a host is a string`, "a list"}},
		{"an unknown field of a host", `{"hosts": {"h": {"ssh": "a@203.0.113.1", "port": 22}}}`, "", []string{`"port"`, "not a field of a host", "ssh, description"}},
		{"a description with a line break", `{"hosts": {"h": {"ssh": "a@203.0.113.1", "description": "one\ntwo"}}}`, "", []string{"the description of the host h holds a line break"}},
		{"taking away a host that is not there", `{"hosts": {"h": null}}`, "", []string{`"hosts" takes "h" away with null`, "nothing to take away"}},

		// options.
		{"options of the wrong shape", `{"options": ["debug"]}`, "", []string{`"options" is an object`, `"requestTimeout": 900`}},
		{"an unknown option", "{\"options\": {\n \"reqestTimeout\": 900}}", "reqestTimeout", []string{`"reqestTimeout"`, "not an option gnosh knows", "requestTimeout, autoCompactEnabled"}},
		{"an option of the earlier format", `{"options": {"request-timeout": 900}}`, "", []string{`"request-timeout"`, "not an option gnosh knows"}},
		{"an option that is gone", `{"options": {"notifications": false}}`, "", []string{`"notifications"`, "not an option gnosh knows"}},
		{"a timeout in quotes", `{"options": {"requestTimeout": "900"}}`, "", []string{`"options.requestTimeout" is a whole number of at least 0`, "a string"}},
		{"a negative timeout", `{"options": {"requestTimeout": -1}}`, "", []string{"a whole number of at least 0", "-1"}},
		{"a timeout with a fraction", `{"options": {"requestTimeout": 1.5}}`, "", []string{"written with digits alone", "1.5"}},
		{"a window out of range", `{"options": {"autoCompactWindow": 5000}}`, "", []string{`"options.autoCompactWindow" is a whole number from 100000 to 1000000`, "5000"}},
		{"no background tasks", `{"options": {"maxBackgroundTasks": 0}}`, "", []string{"a whole number from 1 to"}},
		{"more output than Claude Code allows", `{"options": {"bashOutputMaxChars": 200000}}`, "", []string{"a whole number from 1 to 128000"}},
		{"a switch in quotes", `{"options": {"debug": "true"}}`, "", []string{`"options.debug" is true or false`, "a string"}},
		{"a switch as a number", `{"options": {"autoCompactEnabled": 1}}`, "", []string{`"options.autoCompactEnabled" is true or false`, "and here it is 1"}},
		{"a text gnosh does not have", `{"options": {"memoryText": "long"}}`, "", []string{`"options.memoryText" is one of "lean", "full"`, `"long"`}},
		{"an address that is not one", `{"options": {"memEmbeddingsUrl": "box:8090"}}`, "", []string{`the address given in "options.memEmbeddingsUrl" cannot be used`}},
		{"a variable outside providers", `{"options": {"memEmbeddingsUrl": "http://${EMBED_HOST}:8090"}}`, "", []string{"cannot be used", `Variables are filled in only inside "providers"`}},
		{"a list that is one string", `{"options": {"hiddenSkills": "pdf"}}`, "", []string{`"options.hiddenSkills" is a list of names`, "a string"}},
		{"a list with a number in it", `{"options": {"userInstructionFiles": ["/a", 2]}}`, "", []string{`"options.userInstructionFiles" is a list of paths`, "a list that holds more than strings"}},
		{"an empty entry of a list", `{"options": {"projectInstructionFiles": [""]}}`, "", []string{`an entry of "options.projectInstructionFiles" is empty`}},
		{"an empty folder name", `{"options": {"projectDataDir": ""}}`, "", []string{`"options.projectDataDir" is a path as a string that is not empty`, "an empty string"}},
		{"a server switched off with false", `{"options": {"deliverablesServer": false}}`, "", []string{`"options.deliverablesServer" is an address of this machine with a port, as a string, such as "127.0.0.1:7777", or "off"`, "false"}},
		{"a server on a port alone", `{"options": {"deliverablesServer": "7777"}}`, "", []string{`the address given in "options.deliverablesServer" cannot be used`, "an address of this machine with a port, such as 127.0.0.1:7777"}},
		{"a server on every address", `{"options": {"deliverablesServer": ":7777"}}`, "", []string{"cannot be used", "an address of this machine with a port"}},
		{"a server other machines could reach", `{"options": {"deliverablesServer": "0.0.0.0:7777"}}`, "", []string{"cannot be used", "an address of this machine alone (127.0.0.1, ::1 or localhost)", "an ssh port forward"}},
		{"a server on a machine's own address", `{"options": {"deliverablesServer": "203.0.113.5:7777"}}`, "", []string{"an address of this machine alone"}},
		{"a server on a named machine", `{"options": {"deliverablesServer": "example.com:7777"}}`, "", []string{"an address of this machine alone"}},
		{"a server on a port that is none", `{"options": {"deliverablesServer": "127.0.0.1:99999"}}`, "", []string{"its port is not a number from 0 to 65535"}},
		{"a server on a port with a sign", `{"options": {"deliverablesServer": "127.0.0.1:+80"}}`, "", []string{"its port is not a number from 0 to 65535"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			refuse(t, tt.text, tt.at, tt.pieces...)
			_, problems := load(t, withConfig(tt.text))
			for _, p := range Errors(problems) {
				if p.File != userJSON || p.Line == 0 {
					t.Errorf("a mistake in a config file names the file and a line: %+v", p)
				}
			}
		})
	}
}

// One mistake does not hide the others: every one comes back at once, in
// the order of the file's lines.
func TestEveryMistakeAtOnce(t *testing.T) {
	_, problems := load(t, withConfig(`{
  "permissions": {"deny": ["websearch"]},
  "options": {"nope": 1},
  "hooks": {"Stopp": []},
  "providers": {
    "a": {"type": "nope"},
    "b": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:1"}
  },
  "model": "b/m",
  "smallModel": {"effort": "low"}
}`))
	errs := Errors(problems)
	var lines []int
	for _, p := range errs {
		if p.File != userJSON {
			t.Errorf("a mistake without its file: %+v", p)
		}
		lines = append(lines, p.Line)
	}
	if !slices.Equal(lines, []int{2, 3, 4, 6, 10}) {
		t.Errorf("mistakes on lines %v, want 2, 3, 4, 6 and 10:\n%s", lines, showProblems(problems))
	}
}

func TestSplitRules(t *testing.T) {
	tests := map[string][]string{
		"Bash,Read,Edit":                   {"Bash", "Read", "Edit"},
		"Bash(git *) Edit":                 {"Bash(git *)", "Edit"},
		"Bash(git log *), Bash(a, b) Read": {"Bash(git log *)", "Bash(a, b)", "Read"},
		"  Read  ":                         {"Read"},
		"":                                 nil,
		"Bash(echo (x) y) Z":               {"Bash(echo (x) y)", "Z"},
	}
	for in, want := range tests {
		if got := SplitRules(in); !slices.Equal(got, want) {
			t.Errorf("SplitRules(%q) = %q, want %q", in, got, want)
		}
	}
}
