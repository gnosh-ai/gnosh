package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// WebSearch asks the SearXNG server webSearchUrl names, and lists what it
// found: titles, addresses and a line of each, without the hosts the call
// leaves out. Without the option there is no WebSearch tool at all.
func TestWebSearch(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			http.Error(w, "not here", http.StatusNotFound)
			return
		}
		q := r.URL.Query().Get("q")
		asked = append(asked, q)
		if q == "formats off" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"query": "x", "results": [
			{"title": "Go docs", "url": "https://go.dev/doc/", "content": "Documentation for Go."},
			{"title": "Spam", "url": "https://spam.example/x", "content": "buy now"},
			{"title": "Go blog", "url": "https://blog.go.dev/post", "content": "The Go blog."}]}`)
	}))
	defer srv.Close()
	settings := `{"options": {"webSearchUrl": "` + srv.URL + `/"}}`

	w := apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "WebSearch", Args: `{"query":"golang docs","blocked_domains":["spam.example"]}`}}},
		apptest.Reply{Text: "Found it."},
	)
	run(t, w, "--settings", settings, "--allowedTools", "WebSearch", "-p", "look it up").wantCode(t, 0)
	if !slices.Contains(toolNamesOf(w.Model.Requests()[0]), "WebSearch") {
		t.Fatalf("with webSearchUrl the session has no WebSearch tool: %v", toolNamesOf(w.Model.Requests()[0]))
	}
	got := toolResult(t, w, 1, "c1")
	has(t, "the results", got, `Results for "golang docs"`, "1. Go docs", "https://go.dev/doc/", "Documentation for Go.", "2. Go blog", "https://blog.go.dev/post", "Read a page with WebFetch")
	hasNot(t, "a blocked host", got, "spam.example", "buy now")
	if len(asked) != 1 || asked[0] != "golang docs" {
		t.Errorf("the server was asked %q", asked)
	}

	// Only the hosts asked for, and the server's own words when its JSON
	// format is off.
	w = apptest.NewWorld(t,
		apptest.Reply{Calls: []apptest.Call{
			{ID: "c1", Name: "WebSearch", Args: `{"query":"go","allowed_domains":["go.dev"],"mode":"standard"}`},
			{ID: "c2", Name: "WebSearch", Args: `{"query":"formats off"}`},
			{ID: "c3", Name: "WebSearch", Args: `{"query":"x","allowed_domains":["a"],"blocked_domains":["b"]}`},
		}},
		apptest.Reply{Text: "Done."},
	)
	run(t, w, "--settings", settings, "--allowedTools", "WebSearch", "-p", "look").wantCode(t, 0)
	got = toolResult(t, w, 1, "c1")
	has(t, "allowed_domains", got, "go.dev/doc/", "blog.go.dev/post")
	hasNot(t, "allowed_domains", got, "spam.example")
	has(t, "a 403", toolResult(t, w, 1, "c2"), "403", "search.formats in its settings.yml must list json")
	has(t, "both lists", toolResult(t, w, 1, "c3"), "allowed_domains and blocked_domains are given together")

	// No option, no tool.
	w = apptest.NewWorld(t, apptest.Reply{Text: "Hi."})
	run(t, w, "-p", "hi").wantCode(t, 0)
	if slices.Contains(toolNamesOf(w.Model.Requests()[0]), "WebSearch") {
		t.Error("without webSearchUrl the session offers WebSearch")
	}
}
