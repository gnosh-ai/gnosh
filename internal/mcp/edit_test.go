package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Put and Delete change one entry and leave the rest of the file, and the
// order of its keys, as they were.
func TestPutAndDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf", "mcp.json")
	user := Place{Path: path}
	local := Place{Path: path, Project: "/work/proj"}

	// A file that is not there is made, its owner's alone.
	if err := Put(user, "zeta", json.RawMessage(`{"command": "z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the new file: %v, %v", info, err)
	}
	if err := Put(user, "alpha", json.RawMessage(`{"type":"http","url":"https://a.example.org/mcp"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Put(local, "mine", json.RawMessage(`{"command":"m","args":["-x"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := `{
  "mcpServers": {
    "zeta": {
      "command": "z"
    },
    "alpha": {
      "type": "http",
      "url": "https://a.example.org/mcp"
    }
  },
  "projects": {
    "/work/proj": {
      "mcpServers": {
        "mine": {
          "command": "m",
          "args": [
            "-x"
          ]
        }
      }
    }
  }
}
`
	if string(data) != want {
		t.Errorf("the file:\n%s\nwant:\n%s", data, want)
	}
	if names, _ := Names(user); strings.Join(names, " ") != "zeta alpha" {
		t.Errorf("Names(user) = %q", names)
	}
	if got, _ := Lookup(local, "mine"); string(got) != `{"command":"m","args":["-x"]}` {
		t.Errorf("Lookup(local, mine) = %s", got)
	}
	if got, _ := Lookup(local, "zeta"); got != nil {
		t.Errorf("the user's server was found among the project's: %s", got)
	}
	if got, _ := Lookup(Place{Path: path, Project: "/other"}, "mine"); got != nil {
		t.Errorf("another project's servers held mine: %s", got)
	}

	// Keys gnosh does not know stay, and where they were; a mode the person
	// gave the file stays.
	os.WriteFile(path, []byte(`{"comment": "mine", "mcpServers": {"b": {"command": "b"}}, "other": [1, 2]}`), 0o640)
	os.Chmod(path, 0o640)
	if err := Put(user, "a", json.RawMessage(`{"command":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.HasPrefix(string(data), "{\n  \"comment\": \"mine\",\n  \"mcpServers\": {\n    \"b\": {") || !strings.Contains(string(data), `"other": [`) || strings.Index(string(data), `"b"`) > strings.Index(string(data), `"a"`) {
		t.Errorf("the file after Put:\n%s", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("the mode became %v", info.Mode().Perm())
	}

	// Delete: a project left with no servers, and "projects" left with no
	// project, are taken out; the top's "mcpServers" stays, empty.
	if err := Put(local, "mine", json.RawMessage(`{"command":"m"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := Delete(local, "mine"); !found || err != nil {
		t.Errorf("Delete(local, mine) = %v, %v", found, err)
	}
	if found, err := Delete(local, "mine"); found || err != nil {
		t.Errorf("a second Delete = %v, %v", found, err)
	}
	for _, name := range []string{"a", "b"} {
		if found, err := Delete(user, name); !found || err != nil {
			t.Errorf("Delete(user, %s) = %v, %v", name, found, err)
		}
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "projects") || !strings.Contains(string(data), `"mcpServers": {}`) || !strings.Contains(string(data), `"comment": "mine"`) {
		t.Errorf("the file after the deletes:\n%s", data)
	}

	// A file that is a link: the file it leads to is written, and the link
	// stays.
	real := filepath.Join(dir, "dotfiles", "mcp.json")
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.WriteFile(real, []byte(`{}`), 0o600)
	link := filepath.Join(dir, "link.json")
	os.Symlink(real, link)
	if err := Put(Place{Path: link}, "x", json.RawMessage(`{"command":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a file")
	}
	if data, _ := os.ReadFile(real); !strings.Contains(string(data), `"x"`) {
		t.Errorf("the file the link leads to: %s", data)
	}

	// A file that is not a JSON object is left alone.
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`[1, 2]`), 0o600)
	if err := Put(Place{Path: bad}, "x", json.RawMessage(`{"command":"x"}`), 0o600); err == nil || !strings.Contains(err.Error(), "left as it is") {
		t.Errorf("Put into a list: %v", err)
	}
	os.WriteFile(bad, []byte(`{"mcpServers": "no"}`), 0o600)
	if err := Put(Place{Path: bad}, "x", json.RawMessage(`{"command":"x"}`), 0o600); err == nil {
		t.Error("Put into a file whose mcpServers is a string worked")
	}
	if data, _ := os.ReadFile(bad); string(data) != `{"mcpServers": "no"}` {
		t.Errorf("the file was changed: %s", data)
	}
}

func TestEntry(t *testing.T) {
	for _, tc := range []struct {
		transport, target  string
		args, env, headers []string
		want, wrong        string
	}{
		{"", "npx", []string{"-y", "srv"}, []string{"A=1", "B=x=y"}, nil, `{"type":"stdio","command":"npx","args":["-y","srv"],"env":{"A":"1","B":"x=y"}}`, ""},
		{"stdio", "/bin/srv", nil, nil, nil, `{"type":"stdio","command":"/bin/srv","args":[]}`, ""},
		{"http", "https://mcp.example.org/x", nil, nil, []string{"Authorization: Bearer ${T}", "X-A:b"}, `{"type":"http","url":"https://mcp.example.org/x","headers":{"Authorization":"Bearer ${T}","X-A":"b"}}`, ""},
		{"http", "mcp.example.org", nil, nil, nil, "", "is not a URL"},
		{"http", "https://x.example.org", []string{"more"}, nil, nil, "", "takes nothing after its URL"},
		{"http", "https://x.example.org", nil, []string{"A=1"}, nil, "", "takes -H"},
		{"stdio", "srv", nil, nil, []string{"A: b"}, "", "takes -e"},
		{"stdio", " ", nil, nil, nil, "", "is empty"},
		{"sse", "https://x.example.org", nil, nil, nil, "", "not a transport gnosh speaks"},
	} {
		got, err := Entry(tc.transport, tc.target, tc.args, tc.env, tc.headers)
		switch {
		case tc.wrong != "" && (err == nil || !strings.Contains(err.Error(), tc.wrong)):
			t.Errorf("Entry(%q, %q): %v, want an error saying %q", tc.transport, tc.target, err, tc.wrong)
		case tc.wrong == "" && (err != nil || string(got) != tc.want):
			t.Errorf("Entry(%q, %q) = %s, %v\n want %s", tc.transport, tc.target, got, err, tc.want)
		}
		if err == nil {
			if err := CheckEntry("x", got); err != nil {
				t.Errorf("the entry Entry made would not be used: %v", err)
			}
		}
	}
	for _, tc := range []struct{ entry, wrong string }{
		{`{"type":"http"}`, "no url"},
		{`{"url":"https://x"}`, "no type"},
		{`{"type":"sse","url":"https://x"}`, "not one gnosh speaks"},
		{`[1]`, "not a JSON object"},
		{`{"command": "${ANY}"}`, ""},
	} {
		err := CheckEntry("x", json.RawMessage(tc.entry))
		if tc.wrong == "" && err != nil || tc.wrong != "" && (err == nil || !strings.Contains(err.Error(), tc.wrong)) {
			t.Errorf("CheckEntry(%s) = %v, want %q", tc.entry, err, tc.wrong)
		}
	}
	for name, ok := range map[string]bool{"github": true, "my-server_2": true, "_x": true, strings.Repeat("x", 48): true,
		"a.b": false, "": false, "ünï": false, strings.Repeat("x", 49): false, "github__x": false, "github_": false} {
		if got := CheckName(name) == ""; got != ok {
			t.Errorf("CheckName(%q) ok = %v", name, got)
		}
	}
}

// A server of the project's own in the user's file (the local scope) wins
// over the project's .mcp.json, which wins over the user's.
func TestLocalScope(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "mcp.json")
	project := filepath.Join(dir, "proj", ".mcp.json")
	os.MkdirAll(filepath.Dir(project), 0o755)
	root := filepath.Dir(project)
	os.WriteFile(user, []byte(`{"mcpServers": {"s": {"command": "user-s"}, "u": {"command": "user-u", "args": ["--key", "${K}"]}},
		"projects": {"`+root+`": {"mcpServers": {"s": {"command": "local-s"}, "l": {"type": "http", "url": "https://l.example.org/p?key=${K}"}}},
		             "/elsewhere": {"mcpServers": {"e": {"command": "e"}}}}}`), 0o600)
	os.WriteFile(project, []byte(`{"mcpServers": {"s": {"command": "project-s"}, "p": {"command": "project-p"}}}`), 0o644)
	getenv := func(n string) (string, bool) {
		if n == "K" {
			return "secret", true
		}
		return "", false
	}
	for _, trusted := range []bool{false, true} {
		got, probs := LoadConfig(Sources{User: user, Project: project, Root: root, Trusted: trusted, Getenv: getenv})
		if len(probs) > 0 {
			t.Errorf("problems: %v", probs)
		}
		m := map[string]Config{}
		for _, c := range got {
			m[c.Name] = c
		}
		if m["s"].Command != "local-s" || m["s"].Scope != "local" || m["s"].Path != user {
			t.Errorf("trusted %v: s = %+v", trusted, m["s"])
		}
		if _, ok := m["e"]; ok {
			t.Error("another project's server is in force")
		}
		if _, ok := m["p"]; ok != trusted {
			t.Errorf("trusted %v: the project's own server in force: %v", trusted, ok)
		}
		// What is shown: a command as it is written, a URL's site.
		if m["u"].Shown != "user-u --key ${K}" || m["l"].Shown != "https://l.example.org" {
			t.Errorf("shown: %q, %q", m["u"].Shown, m["l"].Shown)
		}
	}
}

// A file whose servers were written at its top level, beside "mcpServers"
// instead of inside it, is named when servers are read and is not added
// to, as Claude Code 2.1.327's `mcp add` stops there too.
func TestServersAtTheTopOfAFile(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, ".mcp.json")
	text := `{"github": {"command": "gh-server"}, "docs": {"type": "http", "url": "https://docs.example.org/mcp"}, "note": {"say": "not a server"}, "mcpServers": {"kept": {"command": "k"}}}`
	if err := os.WriteFile(project, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	got, probs := LoadConfig(Sources{Project: project, Root: dir, Trusted: true, Getenv: func(string) (string, bool) { return "", false }})
	if len(got) != 1 || got[0].Name != "kept" {
		t.Errorf("read %+v", got)
	}
	found := false
	for _, p := range probs {
		if p.Path == project && strings.Contains(p.Message, `the servers "docs", "github" at its top level`) && strings.Contains(p.Message, `move them inside "mcpServers"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("the servers at the top were not named: %+v", probs)
	}
	err := Put(Place{Path: project}, "more", json.RawMessage(`{"command": "m"}`), 0o600)
	if err == nil || !strings.Contains(err.Error(), `the servers "docs", "github" at its top level`) || !strings.Contains(err.Error(), "the file was left as it is") {
		t.Errorf("Put into the file: %v", err)
	}
	if data, _ := os.ReadFile(project); string(data) != text {
		t.Errorf("the file was changed:\n%s", data)
	}
	// One server, said as one.
	if err := os.WriteFile(project, []byte(`{"github": {"command": "gh-server"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Put(Place{Path: project}, "more", json.RawMessage(`{"command": "m"}`), 0o600); err == nil || !strings.Contains(err.Error(), `the server "github" at its top level`) || !strings.Contains(err.Error(), "Move it inside") {
		t.Errorf("Put into a file with one server at the top: %v", err)
	}
}
