package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// This file is WebSearch: Claude Code's tool, with its name and its
// inputs, on a search server the person names (the webSearchUrl option). It
// is a SearXNG server, whose JSON answer to /search?q=...&format=json gives
// each result's title, address and a few words of it. Without the option
// there is no WebSearch tool; with it, a query goes to that server and to
// nowhere else, and a page found is read with WebFetch.

// webSearchName is the tool's name, Claude Code's.
const webSearchName = "WebSearch"

// webSearchTimeout bounds one search.
const webSearchTimeout = 30 * time.Second

// webSearchResults is how many results a search returns, and how many an
// extended one does.
const (
	webSearchResults         = 10
	webSearchResultsExtended = 20
)

const webSearchDescription = `Searches the web and returns the results' titles and addresses, each with a line of what it says. Read a page you want with WebFetch.
- query: what to look for, in a few words.
- allowed_domains: only results from these hosts (and their subdomains); blocked_domains: never results from these. Not both at once.
- mode: "standard" for the first results, "extended" (the default) for more.`

var webSearchSchema = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"What to look for."},"allowed_domains":{"type":"array","items":{"type":"string"},"description":"Only results from these hosts."},"blocked_domains":{"type":"array","items":{"type":"string"},"description":"Never results from these hosts."},"mode":{"type":"string","enum":["standard","extended"],"description":"standard for fewer results, extended (the default) for more."}},"required":["query"],"additionalProperties":false}`)

type webSearchInput struct {
	Query   string   `json:"query"`
	Allowed []string `json:"allowed_domains"`
	Blocked []string `json:"blocked_domains"`
	Mode    string   `json:"mode"`
}

type webSearchTool struct {
	l    *live
	base string // the server's address, without a slash at its end
}

func (t *webSearchTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: webSearchName, Description: webSearchDescription, InputSchema: webSearchSchema}
}

// read reads a call's arguments and says, in words for the model, what is
// wrong with them when something is.
func (t *webSearchTool) read(input json.RawMessage) (webSearchInput, string) {
	var in webSearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return in, fmt.Sprintf("the arguments cannot be read (%v): query is a string, allowed_domains and blocked_domains lists of strings", err)
	}
	in.Query = strings.TrimSpace(in.Query)
	switch {
	case in.Query == "":
		return in, "query is empty: say what to look for"
	case len(in.Allowed) > 0 && len(in.Blocked) > 0:
		return in, "allowed_domains and blocked_domains are given together; give one of them"
	case in.Mode != "" && in.Mode != "standard" && in.Mode != "extended":
		return in, fmt.Sprintf("mode is %q, and is standard or extended", in.Mode)
	}
	return in, ""
}

// Describe says what a call would do: send a query to the search server.
func (t *webSearchTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	in, wrong := t.read(call.Input)
	act := core.Action{Kind: core.ActionNetwork, URL: t.base, Summary: "Search the web: " + oneLine(in.Query, 80)}
	if wrong != "" {
		return act, errors.New(wrong)
	}
	return act, nil
}

// searxResult is one result of SearXNG's JSON answer, as far as it is read.
type searxResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// Run sends the query and lists what came back.
func (t *webSearchTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	in, wrong := t.read(call.Input)
	if wrong != "" {
		return core.ErrorResult("Nothing was searched: " + wrong + "."), nil
	}
	server := hostOf(t.base)
	began := time.Now()
	ctx, cancel := context.WithTimeout(ctx, webSearchTimeout)
	defer cancel()
	address := t.base + "/search?" + url.Values{"q": {in.Query}, "format": {"json"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("Nothing was searched: the search server's address cannot be used (%v). Tell the user: webSearchUrl in gnosh's config.", err)), nil
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gnosh/"+firstOf(t.l.p.o.BuildID, "devel"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("The search server at %s did not answer (%v). Search another way, or tell the user.", server, err)), nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return core.ErrorResult(fmt.Sprintf("The search server at %s did not finish its answer (%v).", server, err)), nil
	}
	if resp.StatusCode != http.StatusOK {
		hint := ""
		if resp.StatusCode == http.StatusForbidden {
			// SearXNG answers 403 when its JSON format is off.
			hint = " (A SearXNG server answers so when its JSON format is not switched on: search.formats in its settings.yml must list json. Tell the user.)"
		}
		return core.ErrorResult(fmt.Sprintf("The search server at %s answered %s.%s", server, resp.Status, hint)), nil
	}
	var answer struct {
		Results []searxResult `json:"results"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return core.ErrorResult(fmt.Sprintf("The search server at %s did not answer in JSON gnosh can read (%v). It is meant to be a SearXNG server; tell the user.", server, err)), nil
	}
	limit := webSearchResultsExtended
	if in.Mode == "standard" {
		limit = webSearchResults
	}
	var kept []searxResult
	for _, r := range answer.Results {
		host := hostOf(r.URL)
		if host == "" || !domainMatch(host, in.Allowed, true) || domainMatch(host, in.Blocked, false) {
			continue
		}
		kept = append(kept, r)
		if len(kept) == limit {
			break
		}
	}
	meta := map[string]any{"query": in.Query, "durationSeconds": time.Since(began).Seconds(), "searchCount": 1}
	if len(kept) == 0 {
		res := core.TextResult(fmt.Sprintf("No results for %q.", in.Query))
		res.Meta = meta
		return res, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Results for %q, from %s:\n", in.Query, server)
	list := make([]map[string]string, 0, len(kept))
	for i, r := range kept {
		title := oneLine(firstOf(strings.TrimSpace(r.Title), r.URL), 200)
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, title, r.URL)
		if c := oneLine(strings.TrimSpace(r.Content), 300); c != "" {
			fmt.Fprintf(&b, "   %s\n", c)
		}
		list = append(list, map[string]string{"title": title, "url": r.URL})
	}
	b.WriteString("Read a page with WebFetch.")
	meta["results"] = list
	res := core.TextResult(b.String())
	res.Meta = meta
	return res, nil
}

// hostOf is an address's host, in lower case, or "".
func hostOf(address string) string {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// domainMatch reports whether host is one of domains or under one. An
// empty list matches whatever empty says.
func domainMatch(host string, domains []string, empty bool) bool {
	if len(domains) == 0 {
		return empty
	}
	for _, d := range domains {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "."))
		if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}
