package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// serveSearchStub points the DuckDuckGo endpoint at a local server
// returning the given status and body, and restores it on cleanup.
func serveSearchStub(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	orig := ddgLiteEndpoint
	ddgLiteEndpoint = srv.URL + "/lite/?q="
	t.Cleanup(func() { ddgLiteEndpoint = orig })
}

// loadAnomalyPage reads the real bot-check payload captured from
// lite.duckduckgo.com on 2026-07-29 (HTTP 202, captcha modal).
func loadAnomalyPage(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/ddg_anomaly_202.html")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	return string(data)
}

// TestSearchRateLimitedOn202 verifies the 202 anomaly-challenge
// interstitial is reported as throttling, not parsed into an empty
// result set.
func TestSearchRateLimitedOn202(t *testing.T) {
	serveSearchStub(t, http.StatusAccepted, loadAnomalyPage(t))
	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "anything", 10)
	if !errors.Is(err, errSearchRateLimited) {
		t.Fatalf("expected errSearchRateLimited, got %v", err)
	}
}

// TestSearchRateLimitedOnAnomalyPage verifies the captcha modal is
// detected by content as well: DuckDuckGo also serves the same page
// with HTTP 200 once a client is flagged.
func TestSearchRateLimitedOnAnomalyPage(t *testing.T) {
	serveSearchStub(t, http.StatusOK, loadAnomalyPage(t))
	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "anything", 10)
	if !errors.Is(err, errSearchRateLimited) {
		t.Fatalf("expected errSearchRateLimited, got %v", err)
	}
}

// TestSearchParsesNormalResults guards against false positives: a
// genuine results page still parses.
func TestSearchParsesNormalResults(t *testing.T) {
	page := `<html><body><table>
<tr><td><a class="result-link" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpost">Example Post</a></td></tr>
<tr><td class="result-snippet">A snippet about the example post.</td></tr>
</table></body></html>`
	serveSearchStub(t, http.StatusOK, page)
	results, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "example", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Link != "https://example.com/post" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

// serveBingStub points the Bing endpoint at a local server returning the
// given status and body, and counts the requests it gets.
func serveBingStub(t *testing.T, status int, body string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	orig := bingEndpoint
	bingEndpoint = srv.URL + "/search?q="
	t.Cleanup(func() { bingEndpoint = orig })
	return &hits
}

// countSearchStub is serveSearchStub counting DuckDuckGo's requests, with
// the rate-limit cooldown cleared before and after.
func countSearchStub(t *testing.T, status int, body string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	orig := ddgLiteEndpoint
	ddgLiteEndpoint = srv.URL + "/lite/?q="
	t.Cleanup(func() { ddgLiteEndpoint = orig })
	ddgLimitedAt = time.Time{}
	t.Cleanup(func() { ddgLimitedAt = time.Time{} })
	return &hits
}

// loadBingPage reads a Bing result page trimmed from the one served to
// the AI box on 2026-10-25.
func loadBingPage(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/bing_results.html")
	require.NoError(t, err)
	return string(data)
}

var bingPageResults = []SearchResult{
	{Title: "The Go Programming Language", Link: "https://go.dev/", Snippet: "Go is an open source programming language that makes it simple to build secure, scalable systems.", Position: 1},
	{Title: "Go (programming language) - Wikipedia", Link: "https://en.wikipedia.org/wiki/Go_(programming_language)", Snippet: "Go (often referred to as Golang) is a high-level, general-purpose programming language that is statically typed and compiled. …", Position: 2},
	{Title: "context package - context - Go Packages", Link: "https://pkg.go.dev/context", Snippet: "Package context defines the Context type, which carries deadlines, cancellation signals, and other request-scoped values.", Position: 3},
}

// Bing's results are read from its li.b_algo items, with the pages its
// click tracker leads to, up to the number asked for.
func TestParseBingResults(t *testing.T) {
	got, err := parseBingResults(loadBingPage(t), 10)
	require.NoError(t, err)
	require.Equal(t, bingPageResults, got)
	got, err = parseBingResults(loadBingPage(t), 2)
	require.NoError(t, err)
	require.Equal(t, bingPageResults[:2], got)
}

// A link through Bing's click tracker is the page it leads to; another
// link, or a tracker without a page, is itself.
func TestBingTarget(t *testing.T) {
	for href, want := range map[string]string{
		"https://www.bing.com/ck/a?!&&p=1&u=a1aHR0cHM6Ly9nby5kZXYv&ntb=1": "https://go.dev/",
		"https://pkg.go.dev/context":                                      "https://pkg.go.dev/context",
		"https://www.bing.com/ck/a?!&&p=1&u=a1%21%21%21&ntb=1":            "https://www.bing.com/ck/a?!&&p=1&u=a1%21%21%21&ntb=1",
		"https://www.bing.com/ck/a?!&&p=1&ntb=1":                          "https://www.bing.com/ck/a?!&&p=1&ntb=1",
		"https://www.bing.com/ck/a?u=a1Zm9vYmFy":                          "https://www.bing.com/ck/a?u=a1Zm9vYmFy",
		"https://example.com/ck/a?u=a1aHR0cHM6Ly9nby5kZXYv":               "https://example.com/ck/a?u=a1aHR0cHM6Ly9nby5kZXYv",
	} {
		require.Equal(t, want, bingTarget(href), href)
	}
}

// While DuckDuckGo rate-limits this machine, the search goes to Bing,
// and DuckDuckGo is not asked again until the cooldown has passed.
func TestSearchFallsBackToBingWhileDuckDuckGoRateLimits(t *testing.T) {
	ddg := countSearchStub(t, http.StatusAccepted, loadAnomalyPage(t))
	bing := serveBingStub(t, http.StatusOK, loadBingPage(t))

	results, err := search(context.Background(), http.DefaultClient, "golang context cancel", 10)
	require.NoError(t, err)
	require.Equal(t, bingPageResults, results)
	require.Equal(t, int32(1), ddg.Load())

	_, err = search(context.Background(), http.DefaultClient, "golang context cancel", 10)
	require.NoError(t, err)
	require.Equal(t, int32(1), ddg.Load(), "left alone while cooling down")
	require.Equal(t, int32(2), bing.Load())

	ddgLimitedAt = time.Now().Add(-ddgCooldown - time.Second)
	_, err = search(context.Background(), http.DefaultClient, "golang context cancel", 10)
	require.NoError(t, err)
	require.Equal(t, int32(2), ddg.Load(), "asked again once the cooldown passed")
}

// A DuckDuckGo that answers is used, and Bing is not asked.
func TestSearchUsesDuckDuckGoWhenItAnswers(t *testing.T) {
	page := `<html><body><table>
<tr><td><a class="result-link" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpost">Example Post</a></td></tr>
<tr><td class="result-snippet">A snippet about the example post.</td></tr>
</table></body></html>`
	countSearchStub(t, http.StatusOK, page)
	bing := serveBingStub(t, http.StatusOK, loadBingPage(t))
	results, err := search(context.Background(), http.DefaultClient, "example", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "https://example.com/post", results[0].Link)
	require.Zero(t, bing.Load())
}

// When Bing fails too, the error says both, and to wait; a Bing page
// without its results list is a challenge, not a search that found
// nothing.
func TestSearchSaysWhenBingFailsToo(t *testing.T) {
	countSearchStub(t, http.StatusAccepted, loadAnomalyPage(t))
	serveBingStub(t, http.StatusServiceUnavailable, "")
	_, err := search(context.Background(), http.DefaultClient, "anything", 10)
	require.ErrorContains(t, err, "DuckDuckGo is rate-limiting this machine, and Bing failed too (search failed with status code: 503). Do not retry")

	serveBingStub(t, http.StatusOK, "<html><body>Please solve the challenge</body></html>")
	_, err = search(context.Background(), http.DefaultClient, "anything", 10)
	require.ErrorContains(t, err, "Bing did not serve a result page")

	serveBingStub(t, http.StatusOK, `<html><body><ol id="b_results"></ol></body></html>`)
	results, err := search(context.Background(), http.DefaultClient, "nothing matches this", 10)
	require.NoError(t, err)
	require.Empty(t, results)
}

// Bing's live result page still parses: set GNOSH_LIVE_SEARCH=1 to ask
// the real Bing.
func TestSearchBingLive(t *testing.T) {
	if os.Getenv("GNOSH_LIVE_SEARCH") == "" {
		t.Skip("set GNOSH_LIVE_SEARCH=1 to search the real Bing")
	}
	results, err := searchBing(context.Background(), &http.Client{Timeout: 30 * time.Second}, "golang context package", 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(results), 3)
	for _, r := range results {
		require.True(t, strings.HasPrefix(r.Link, "http"), r.Link)
		require.NotContains(t, r.Link, "bing.com/ck/a", "the tracker was not followed")
		require.NotEmpty(t, r.Title)
	}
	t.Logf("%+v", results[:3])
}

// Other DuckDuckGo failures are reported as they are: Bing is for a
// machine DuckDuckGo turns away, not a second try at every error.
func TestSearchReportsOtherDuckDuckGoFailures(t *testing.T) {
	countSearchStub(t, http.StatusInternalServerError, "")
	bing := serveBingStub(t, http.StatusOK, loadBingPage(t))
	_, err := search(context.Background(), http.DefaultClient, "anything", 10)
	require.EqualError(t, err, "search failed with status code: 500")
	require.Zero(t, bing.Load())
}

// The web_search tool searches Bing while DuckDuckGo rate-limits.
func TestWebSearchToolFallsBackToBing(t *testing.T) {
	countSearchStub(t, http.StatusAccepted, loadAnomalyPage(t))
	serveBingStub(t, http.StatusOK, loadBingPage(t))
	resp, err := NewWebSearchTool(http.DefaultClient).Run(context.Background(), fantasy.ToolCall{ID: "call-1", Name: WebSearchToolName, Input: `{"query":"golang context cancel"}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "Found 3 search results")
	require.Contains(t, resp.Content, "URL: https://pkg.go.dev/context")
}
