package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/dustin/go-humanize"
	"golang.org/x/net/html"
)

// binaryMediaPrefixes and binaryMediaTypes are content types that are
// never text: fetch and web_fetch do not read such a body at all.
var (
	binaryMediaPrefixes = []string{"audio/", "video/", "image/", "font/"}
	binaryMediaTypes    = map[string]bool{
		"application/pdf":                         true,
		"application/zip":                         true,
		"application/gzip":                        true,
		"application/x-gzip":                      true,
		"application/x-tar":                       true,
		"application/x-bzip2":                     true,
		"application/x-xz":                        true,
		"application/zstd":                        true,
		"application/x-7z-compressed":             true,
		"application/vnd.rar":                     true,
		"application/x-rar-compressed":            true,
		"application/wasm":                        true,
		"application/java-archive":                true,
		"application/vnd.android.package-archive": true,
		"application/x-msdownload":                true,
		"application/x-executable":                true,
	}
)

// isBinaryMedia reports whether contentType is a type that is never text.
// SVG and other +xml images are text.
func isBinaryMedia(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || strings.HasSuffix(mediaType, "+xml") {
		return false
	}
	if binaryMediaTypes[mediaType] {
		return true
	}
	for _, prefix := range binaryMediaPrefixes {
		if strings.HasPrefix(mediaType, prefix) {
			return true
		}
	}
	return false
}

// isTextMedia reports whether contentType is a text type: text/*, JSON,
// XML or JavaScript.
func isTextMedia(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch mediaType {
	case "application/json", "application/xml", "application/javascript", "application/ecmascript":
		return true
	}
	return strings.HasPrefix(mediaType, "text/") || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

// notUTF8 is the error for a body that is not UTF-8: a text page in
// another encoding, or a file that is not text at all (notText).
func notUTF8(contentType string, size int64) error {
	if isTextMedia(contentType) {
		return errors.New("response content is not valid UTF-8")
	}
	return notText(contentType, size)
}

// notText says what a response that is not text is, and how to get it
// instead: fetching reads text pages, and an agent on the AI box had a
// song, a video and a model file read up to the size limit, to be told
// only that their content was "not valid UTF-8". size is the response's
// length, or not more than 0 when it is not known.
func notText(contentType string, size int64) error {
	what, _, err := mime.ParseMediaType(contentType)
	if err != nil || what == "" {
		what = "a file of unknown type"
	}
	if size > 0 {
		what += " of " + humanize.Bytes(uint64(size))
	}
	return fmt.Errorf("this is not a text page but %s; fetching reads text. Save it with the download tool", what)
}

// BrowserUserAgent is a realistic browser User-Agent for better compatibility.
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

var multipleNewlinesRe = regexp.MustCompile(`\n{3,}`)

// FetchURLAndConvert fetches a URL and converts HTML content to markdown.
func FetchURLAndConvert(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// Use realistic browser headers for better compatibility.
	req.Header.Set("User-Agent", BrowserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request failed with status code: %d", resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	if isBinaryMedia(contentType) {
		return "", notText(contentType, resp.ContentLength)
	}

	maxSize := int64(5 * 1024 * 1024) // 5MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize))
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	content := string(body)

	if !utf8.ValidString(content) {
		return "", notUTF8(contentType, resp.ContentLength)
	}

	// Convert HTML to markdown for better AI processing.
	if strings.Contains(contentType, "text/html") {
		// Remove noisy elements before conversion.
		cleanedHTML := removeNoisyElements(content)
		markdown, err := ConvertHTMLToMarkdown(cleanedHTML)
		if err != nil {
			return "", fmt.Errorf("failed to convert HTML to markdown: %w", err)
		}
		content = cleanupMarkdown(markdown)
	} else if strings.Contains(contentType, "application/json") || strings.Contains(contentType, "text/json") {
		// Format JSON for better readability.
		formatted, err := FormatJSON(content)
		if err == nil {
			content = formatted
		}
		// If formatting fails, keep original content.
	}

	return content, nil
}

// removeNoisyElements removes script, style, nav, header, footer, and other
// noisy elements from HTML to improve content extraction.
func removeNoisyElements(htmlContent string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		// If parsing fails, return original content.
		return htmlContent
	}

	// Elements to remove entirely.
	noisyTags := map[string]bool{
		"script":   true,
		"style":    true,
		"nav":      true,
		"header":   true,
		"footer":   true,
		"aside":    true,
		"noscript": true,
		"iframe":   true,
		"svg":      true,
	}

	var removeNodes func(*html.Node)
	removeNodes = func(n *html.Node) {
		var toRemove []*html.Node

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && noisyTags[c.Data] {
				toRemove = append(toRemove, c)
			} else {
				removeNodes(c)
			}
		}

		for _, node := range toRemove {
			n.RemoveChild(node)
		}
	}

	removeNodes(doc)

	var buf bytes.Buffer
	if err := html.Render(&buf, doc); err != nil {
		return htmlContent
	}

	return buf.String()
}

// cleanupMarkdown removes excessive whitespace and blank lines from markdown.
func cleanupMarkdown(content string) string {
	// Collapse multiple blank lines into at most two.
	content = multipleNewlinesRe.ReplaceAllString(content, "\n\n")

	// Remove trailing whitespace from each line.
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	content = strings.Join(lines, "\n")

	// Trim leading/trailing whitespace.
	content = strings.TrimSpace(content)

	return content
}

// ConvertHTMLToMarkdown converts HTML content to markdown format.
func ConvertHTMLToMarkdown(htmlContent string) (string, error) {
	converter := md.NewConverter("", true, nil)

	markdown, err := converter.ConvertString(htmlContent)
	if err != nil {
		return "", err
	}

	return markdown, nil
}

// FormatJSON formats JSON content with proper indentation.
func FormatJSON(content string) (string, error) {
	var data any
	if err := json.Unmarshal([]byte(content), &data); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(data); err != nil {
		return "", err
	}

	return buf.String(), nil
}
