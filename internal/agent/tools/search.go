package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// SearchResult represents a single search result, from DuckDuckGo or Bing.
type SearchResult struct {
	Title    string
	Link     string
	Snippet  string
	Position int
}

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
}

var acceptLanguages = []string{
	"en-US,en;q=0.9",
	"en-US,en;q=0.9,es;q=0.8",
	"en-GB,en;q=0.9,en-US;q=0.8",
	"en-US,en;q=0.5",
	"en-CA,en;q=0.9,en-US;q=0.8",
}

// errSearchRateLimited reports that DuckDuckGo served a bot-check page
// instead of results.
var errSearchRateLimited = errors.New(
	"DuckDuckGo is rate-limiting this machine. " +
		"Do not retry or rephrase; wait a few minutes or fetch known URLs directly",
)

// ddgAnomalyMarkers are substrings of the bot-detection page DuckDuckGo
// Lite serves (with HTTP 200) instead of results once a client trips its
// rate limiter.  Parsing that page yields zero results, which would
// otherwise masquerade as "your query found nothing".
var ddgAnomalyMarkers = []string{
	"anomaly-modal",
	"/anomaly.js",
	"Unfortunately, bots use DuckDuckGo too",
}

// ddgLiteEndpoint is a package var so tests can point the search at a
// local httptest server.
var ddgLiteEndpoint = "https://lite.duckduckgo.com/lite/?q="

func searchDuckDuckGo(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, error) {
	if maxResults <= 0 {
		maxResults = 10
	}

	searchURL := ddgLiteEndpoint + url.QueryEscape(query)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	setRandomizedHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute search: %w", err)
	}
	defer resp.Body.Close()

	// A 202 from DuckDuckGo is the anomaly-challenge interstitial, not a
	// result page; report throttling rather than parsing it into an
	// empty result set.
	if resp.StatusCode == http.StatusAccepted {
		return nil, errSearchRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search failed with status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	content := string(body)
	for _, marker := range ddgAnomalyMarkers {
		if strings.Contains(content, marker) {
			return nil, errSearchRateLimited
		}
	}

	return parseLiteSearchResults(content, maxResults)
}

// bingEndpoint is a package var so tests can point the search at a
// local httptest server.
var bingEndpoint = "https://www.bing.com/search?setlang=en&q="

// ddgCooldown is how long DuckDuckGo is left alone once it rate-limits
// this machine, asking again only keeps the block on; searches go to
// Bing meanwhile. A var so tests can change it.
var ddgCooldown = 10 * time.Minute

var (
	ddgLimitedMu sync.Mutex
	ddgLimitedAt time.Time
)

// search searches DuckDuckGo, or Bing while DuckDuckGo turns this
// machine away: an agent on the AI box had a third of its searches
// refused, and went on to fetch Bing's result pages itself.
func search(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, error) {
	ddgLimitedMu.Lock()
	cooling := !ddgLimitedAt.IsZero() && time.Since(ddgLimitedAt) < ddgCooldown
	ddgLimitedMu.Unlock()
	if !cooling {
		results, err := searchDuckDuckGo(ctx, client, query, maxResults)
		if !errors.Is(err, errSearchRateLimited) {
			return results, err
		}
		ddgLimitedMu.Lock()
		ddgLimitedAt = time.Now()
		ddgLimitedMu.Unlock()
	}
	results, err := searchBing(ctx, client, query, maxResults)
	if err != nil {
		return nil, fmt.Errorf("DuckDuckGo is rate-limiting this machine, and Bing failed too (%w). "+
			"Do not retry or rephrase; wait a few minutes or fetch known URLs directly", err)
	}
	return results, nil
}

// searchBing searches Bing's result page.
func searchBing(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, error) {
	if maxResults <= 0 {
		maxResults = 10
	}
	req, err := http.NewRequestWithContext(ctx, "GET", bingEndpoint+url.QueryEscape(query), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	setRandomizedHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search failed with status code: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	// A result page has its results list even when nothing matched; a
	// page without one is a challenge, not "no results".
	if !strings.Contains(string(body), `id="b_results"`) {
		return nil, errors.New("Bing did not serve a result page")
	}
	return parseBingResults(string(body), maxResults)
}

// parseBingResults reads the results of Bing's result page: each
// li.b_algo, with the link of its h2 and the text of its caption.
func parseBingResults(htmlContent string, maxResults int) ([]SearchResult, error) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}
	var results []SearchResult
	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if len(results) >= maxResults {
			return
		}
		if n.Type == html.ElementNode && n.Data == "li" && hasClass(n, "b_algo") {
			if result, ok := bingResult(n); ok {
				result.Position = len(results) + 1
				results = append(results, result)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(doc)
	return results, nil
}

// bingResult is the result an li.b_algo holds.
func bingResult(li *html.Node) (SearchResult, bool) {
	var result SearchResult
	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch {
			case n.Data == "h2" && result.Link == "":
				if a := firstElement(n, "a"); a != nil {
					result.Title = getTextContent(a)
					result.Link = bingTarget(attrValue(a, "href"))
				}
				return
			case n.Data == "div" && hasClass(n, "b_caption") && result.Snippet == "":
				if p := firstElement(n, "p"); p != nil {
					n = p
				}
				result.Snippet = getTextContent(n)
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(li)
	return result, result.Link != ""
}

// bingTarget is the page a Bing result link leads to. Bing's click
// tracker, https://www.bing.com/ck/a?...&u=a1<base64>, carries it in u
// after "a1"; any other link is the page itself.
func bingTarget(href string) string {
	u, err := url.Parse(href)
	if err != nil || !strings.HasSuffix(u.Hostname(), "bing.com") || u.Path != "/ck/a" {
		return href
	}
	encoded, ok := strings.CutPrefix(u.Query().Get("u"), "a1")
	if !ok {
		return href
	}
	encoded = strings.TrimRight(encoded, "=")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || !strings.HasPrefix(string(decoded), "http") {
		return href
	}
	return string(decoded)
}

// firstElement is the first element named name in n's subtree, n
// excluded, or nil.
func firstElement(n *html.Node, name string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == name {
			return c
		}
		if found := firstElement(c, name); found != nil {
			return found
		}
	}
	return nil
}

// attrValue is the value of n's attribute key, or "".
func attrValue(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func setRandomizedHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgents[rand.IntN(len(userAgents))])
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", acceptLanguages[rand.IntN(len(acceptLanguages))])
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Cache-Control", "max-age=0")
	if rand.IntN(2) == 0 {
		req.Header.Set("DNT", "1")
	}
}

func parseLiteSearchResults(htmlContent string, maxResults int) ([]SearchResult, error) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	var results []SearchResult
	var currentResult *SearchResult

	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "a" && hasClass(n, "result-link") {
				if currentResult != nil && currentResult.Link != "" {
					currentResult.Position = len(results) + 1
					results = append(results, *currentResult)
					if len(results) >= maxResults {
						return
					}
				}
				currentResult = &SearchResult{Title: getTextContent(n)}
				for _, attr := range n.Attr {
					if attr.Key == "href" {
						currentResult.Link = cleanDuckDuckGoURL(attr.Val)
						break
					}
				}
			}
			if n.Data == "td" && hasClass(n, "result-snippet") && currentResult != nil {
				currentResult.Snippet = getTextContent(n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if len(results) >= maxResults {
				return
			}
			traverse(c)
		}
	}

	traverse(doc)

	if currentResult != nil && currentResult.Link != "" && len(results) < maxResults {
		currentResult.Position = len(results) + 1
		results = append(results, *currentResult)
	}

	return results, nil
}

func hasClass(n *html.Node, class string) bool {
	for _, attr := range n.Attr {
		if attr.Key == "class" {
			if slices.Contains(strings.Fields(attr.Val), class) {
				return true
			}
		}
	}
	return false
}

func getTextContent(n *html.Node) string {
	var text strings.Builder
	var traverse func(*html.Node)
	traverse = func(node *html.Node) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(n)
	return strings.TrimSpace(text.String())
}

func cleanDuckDuckGoURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "//duckduckgo.com/l/?uddg=") {
		if _, after, ok := strings.Cut(rawURL, "uddg="); ok {
			encoded := after
			if ampIdx := strings.Index(encoded, "&"); ampIdx != -1 {
				encoded = encoded[:ampIdx]
			}
			if decoded, err := url.QueryUnescape(encoded); err == nil {
				return decoded
			}
		}
	}
	return rawURL
}

func formatSearchResults(results []SearchResult) string {
	if len(results) == 0 {
		return "No results found. Try rephrasing your search."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d search results:\n\n", len(results))
	for _, result := range results {
		fmt.Fprintf(&sb, "%d. %s\n", result.Position, result.Title)
		fmt.Fprintf(&sb, "   URL: %s\n", result.Link)
		fmt.Fprintf(&sb, "   Summary: %s\n\n", result.Snippet)
	}
	return sb.String()
}

var (
	lastSearchMu   sync.Mutex
	lastSearchTime time.Time
)

// maybeDelaySearch adds a random delay if the last search was recent.
func maybeDelaySearch() {
	lastSearchMu.Lock()
	defer lastSearchMu.Unlock()

	minGap := time.Duration(500+rand.IntN(1500)) * time.Millisecond
	elapsed := time.Since(lastSearchTime)
	if elapsed < minGap {
		time.Sleep(minGap - elapsed)
	}
	lastSearchTime = time.Now()
}
