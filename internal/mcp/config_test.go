package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A server whose name, as its tools' names write it, holds "__", ends in
// "_" or is too long to stay whole in them is not used: a rule for the
// tools of server github (mcp__github__*) covered those of a server named
// github__x or github_, and one for a server whose name a shortened tool
// name cut short covered none (the MCP review of 2026-11-19).
func TestServerNamesThatBlur(t *testing.T) {
	entry := json.RawMessage(`{"command": "x"}`)
	servers := map[string]json.RawMessage{"github": entry, "a.b": entry, "_x": entry,
		"github__x": entry, "github_": entry, "x.": entry, "a._b": entry, strings.Repeat("y", 49): entry}
	got, probs := ParseServers("mcp.json", "user", servers, Sources{})
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"_x", "a.b", "github"}) {
		t.Errorf("the servers used: %v", names)
	}
	if len(probs) != 5 {
		t.Errorf("the problems: %v", probs)
	}
	for _, p := range probs {
		if !strings.HasSuffix(p.Message, ", so it is not used: rename it") {
			t.Errorf("the problem: %v", p)
		}
	}
	// Two characters short of the separator that a shortened name keeps.
	if long := ToolName(strings.Repeat("y", maxServerName), strings.Repeat("t", 40)); !strings.HasPrefix(FitToolName(long), "mcp__"+strings.Repeat("y", maxServerName)+"__") {
		t.Errorf("a shortened name lost its server: %s", FitToolName(long))
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "mcp.json")
	project := filepath.Join(dir, "proj", ".mcp.json")
	os.MkdirAll(filepath.Dir(project), 0o755)
	os.WriteFile(user, []byte(`{"mcpServers": {
		"both": {"command": "user-version"},
		"mine": {"command": "${TOOLS:-/opt/tools}/server", "args": ["--key", "${API_KEY}", "--root", "${CLAUDE_PROJECT_DIR:-.}"], "env": {"TOKEN": "${API_KEY}"}, "timeout": 5000},
		"short": {"command": "x", "timeout": 500},
		"web": {"type": "http", "url": "https://mcp.example.org/${PATH_PART:-v1}", "headers": {"Authorization": "Bearer ${MISSING}"}},
		"leaky": {"type": "http", "url": "https://x.example.org/?k=${OPENAI_API_KEY}&p=${CLAUDE_PROJECT_DIR:-none}", "headers": {"X-Key": "${MY_MODEL_KEY:-dflt}", "X-Proxy": "${HTTPS_PROXY}", "X-Ok": "${API_KEY}"}, "env": {"A": "b"}},
		"nourl": {"type": "http"},
		"untyped": {"url": "https://example.org/mcp"},
		"old": {"type": "sse", "url": "https://example.org/sse"},
		"empty": {"args": ["a"]}
	}}`), 0o644)
	os.WriteFile(project, []byte(`{"mcpServers": {"both": {"command": "project-version"}, "repo": {"command": "npx", "args": ["-y", "repo-server"]}}}`), 0o644)
	env := map[string]string{"API_KEY": "k123", "PATH_PART": "", "OPENAI_API_KEY": "sk-1", "MY_MODEL_KEY": "mk-2", "HTTPS_PROXY": "http://u:pw@proxy"}
	getenv := func(n string) (string, bool) { v, ok := env[n]; return v, ok }
	root := filepath.Dir(project)

	byName := func(cs []Config) map[string]Config {
		out := map[string]Config{}
		for _, c := range cs {
			out[c.Name] = c
		}
		return out
	}

	src := Sources{User: user, Project: project, Root: root, Getenv: getenv, Withheld: []string{"MY_MODEL_KEY"}}

	// Not trusted: the user's file alone.
	got, probs := LoadConfig(src)
	m := byName(got)
	if _, ok := m["repo"]; ok {
		t.Error("the project's server was read in a folder that is not trusted")
	}
	if m["both"].Command != "user-version" {
		t.Errorf("both: %+v", m["both"])
	}
	mine := m["mine"]
	if mine.Type != "stdio" || mine.Command != "/opt/tools/server" || strings.Join(mine.Args, " ") != "--key k123 --root "+root || mine.Env["TOKEN"] != "k123" || mine.Timeout != 5*time.Second || mine.Scope != "user" {
		t.Errorf("mine: %+v", mine)
	}
	if m["short"].Timeout != 0 {
		t.Errorf("a timeout under a second counts: %v", m["short"].Timeout)
	}
	// A variable set to nothing is set: PATH_PART fills in empty.
	if web := m["web"]; web.Type != "http" || web.URL != "https://mcp.example.org/" || web.Headers["Authorization"] != "Bearer ${MISSING}" {
		t.Errorf("web: %+v", web)
	}
	// A server at a URL is sent no credential, gnosh's own model keys
	// included, nor the project's folder; other variables it is.
	if l := m["leaky"]; l.URL != "https://x.example.org/?k=&p=none" || l.Headers["X-Key"] != "" || l.Headers["X-Proxy"] != "" || l.Headers["X-Ok"] != "k123" {
		t.Errorf("leaky: %+v", l)
	}
	for _, name := range []string{"nourl", "untyped", "old", "empty"} {
		if _, ok := m[name]; ok {
			t.Errorf("%s is in force, and its entry is wrong", name)
		}
	}
	text := ""
	for _, p := range probs {
		text += p.String() + "\n"
	}
	for _, want := range []string{`"nourl": it is an http server with no url`, `"untyped": it has a url and no type`, `"old": the sse transport is not one gnosh speaks`, `"empty": it has no command`, `"web": it names variables that are not set and have no default: MISSING`, `"leaky": its url or headers name HTTPS_PROXY, MY_MODEL_KEY, OPENAI_API_KEY, which a server at a URL is never sent`} {
		if !strings.Contains(text, want) {
			t.Errorf("the problems do not say %q:\n%s", want, text)
		}
	}

	// Trusted: the project's file too, and its entry wins whole.
	src.Trusted = true
	got, _ = LoadConfig(src)
	m = byName(got)
	if m["both"].Command != "project-version" || m["both"].Scope != "project" || m["repo"].Command != "npx" {
		t.Errorf("trusted: both %+v, repo %+v", m["both"], m["repo"])
	}
	// The servers come in the order of their names.
	for i := 1; i < len(got); i++ {
		if got[i-1].Name > got[i].Name {
			t.Errorf("not in order: %s before %s", got[i-1].Name, got[i].Name)
		}
	}

	// A file that is not JSON is one problem, and nothing of it is used.
	os.WriteFile(user, []byte(`{"mcpServers": `), 0o644)
	got, probs = LoadConfig(Sources{User: user, Root: root, Getenv: getenv})
	if len(got) != 0 || len(probs) != 1 || !strings.Contains(probs[0].String(), "none of it is used") {
		t.Errorf("a broken file: %v, %v", got, probs)
	}
	// Files that are not there are no problem.
	if got, probs := LoadConfig(Sources{User: filepath.Join(dir, "none.json"), Project: filepath.Join(dir, "nope.json"), Root: root, Trusted: true, Getenv: getenv}); len(got)+len(probs) != 0 {
		t.Errorf("files that are not there: %v, %v", got, probs)
	}
}

func TestToolName(t *testing.T) {
	for _, tc := range [][3]string{
		{"github", "get_issue", "mcp__github__get_issue"},
		{"my.server", "list", "mcp__my_server__list"},
		{"a b/c", "tool-1", "mcp__a_b_c__tool-1"},
		{"ünï", "x.y", "mcp___n___x_y"},
	} {
		if got := ToolName(tc[0], tc[1]); got != tc[2] {
			t.Errorf("ToolName(%q, %q) = %q, want %q", tc[0], tc[1], got, tc[2])
		}
	}
}

// A name longer than the model's APIs take is cut to 64 characters, its
// start kept and its end a hash of the whole: two long names that begin
// alike stay apart, and a name that fits is left as it is.
func TestFitToolName(t *testing.T) {
	short := ToolName("github", "get_issue")
	if got := FitToolName(short); got != short {
		t.Errorf("a name that fits came back as %q", got)
	}
	a := ToolName("company-internal-documentation", "search_documents_by_full_text_query")
	b := ToolName("company-internal-documentation", "search_documents_by_full_text_query_v2")
	fa, fb := FitToolName(a), FitToolName(b)
	if len(fa) != MaxToolName || len(fb) != MaxToolName || fa == fb {
		t.Errorf("FitToolName gave %q and %q", fa, fb)
	}
	if !strings.HasPrefix(fa, "mcp__company-internal-documentation__search_") {
		t.Errorf("the start of the name was not kept: %q", fa)
	}
	if FitToolName(a) != fa {
		t.Error("FitToolName is not the same twice")
	}
}

// Servers given on the command line (--mcp-config): files, found from
// where gnosh was started, and JSON given whole. Their entries win over the
// files', and with Strict they are the only ones.
func TestGivenConfig(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "mcp.json")
	os.WriteFile(user, []byte(`{"mcpServers": {"mine": {"command": "user-mine"}, "both": {"command": "user-both"}}}`), 0o600)
	os.WriteFile(filepath.Join(dir, "extra.json"), []byte(`{"mcpServers": {"fromfile": {"command": "f"}}}`), 0o600)
	getenv := func(string) (string, bool) { return "", false }
	src := Sources{User: user, Root: dir, Getenv: getenv, GivenFrom: dir, Given: []string{
		"extra.json",
		` {"mcpServers": {"both": {"type": "http", "url": "https://b.example.org/mcp"}}}`,
		"missing.json",
		`{"mcpServers": `,
	}}
	got, probs := LoadConfig(src)
	m := map[string]Config{}
	for _, c := range got {
		m[c.Name] = c
	}
	if m["mine"].Scope != "user" {
		t.Errorf("mine: %+v", m["mine"])
	}
	if f := m["fromfile"]; f.Scope != "command line" || f.Path != filepath.Join(dir, "extra.json") {
		t.Errorf("fromfile: %+v", f)
	}
	if b := m["both"]; b.Type != "http" || b.Scope != "command line" || b.Path != "--mcp-config" {
		t.Errorf("both: %+v", b)
	}
	text := ""
	for _, p := range probs {
		text += p.String() + "\n"
	}
	for _, want := range []string{"missing.json: --mcp-config names it, and it cannot be read", "--mcp-config: the JSON given is not in the shape"} {
		if !strings.Contains(text, want) {
			t.Errorf("the problems do not say %q:\n%s", want, text)
		}
	}

	src.Strict = true
	got, _ = LoadConfig(src)
	for _, c := range got {
		if c.Scope != "command line" {
			t.Errorf("with Strict, %s of %s is in force", c.Name, c.Scope)
		}
	}
	if len(got) != 2 {
		t.Errorf("with Strict: %d servers, want the 2 given", len(got))
	}
}

// A server at a URL may carry an oauth object, read into Config.OAuth; a
// port that is not one, or a metadata address not over https, keeps the
// entry out, and an oauth object on a started server is said and left out.
func TestOAuthConfig(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "mcp.json")
	os.WriteFile(user, []byte(`{"mcpServers": {
  "far": {"type": "http", "url": "https://far.example.org/mcp", "oauth": {"clientId": "c-1", "callbackPort": 8080, "scopes": " read write ", "authServerMetadataUrl": "https://auth.example.org/meta"}},
  "badport": {"type": "http", "url": "https://b.example.org/mcp", "oauth": {"callbackPort": 70000}},
  "plain": {"type": "http", "url": "https://p.example.org/mcp", "oauth": {"authServerMetadataUrl": "http://auth.example.org/meta"}},
  "local": {"command": "srv", "oauth": {"clientId": "x"}}
}}`), 0o600)
	got, probs := LoadConfig(Sources{User: user, Root: dir, Getenv: func(string) (string, bool) { return "", false }})
	m := map[string]Config{}
	for _, c := range got {
		m[c.Name] = c
	}
	if o := m["far"].OAuth; o != (OAuth{ClientID: "c-1", CallbackPort: 8080, Scopes: "read write", AuthServerMetadataURL: "https://auth.example.org/meta"}) {
		t.Errorf("far's oauth: %+v", o)
	}
	if _, ok := m["badport"]; ok {
		t.Errorf("an entry with a callbackPort that is not a port was kept")
	}
	if _, ok := m["plain"]; ok {
		t.Errorf("an entry with an authServerMetadataUrl over plain http was kept")
	}
	if _, ok := m["local"]; !ok {
		t.Errorf("a started server with an oauth object was left out whole")
	}
	text := ""
	for _, p := range probs {
		text += p.String() + "\n"
	}
	for _, want := range []string{"oauth.callbackPort 70000 is not a port", "authServerMetadataUrl is not an https address", "oauth object is for a server reached at a URL"} {
		if !strings.Contains(text, want) {
			t.Errorf("the problems do not say %q:\n%s", want, text)
		}
	}
}
