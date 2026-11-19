package webtool

import (
	"strings"
	"testing"
	"time"
)

// A page made to blow the conversion up costs what its size does, not its
// square: nested lists, a paragraph mark between every letter, and links
// left open (the security review of 2026-11-18 found the first two grew
// to gigabytes, or ran for many minutes).
func TestMarkdownOfAPageMadeToBlowUp(t *testing.T) {
	pages := map[string]string{
		"nested lists": strings.Repeat("<ul>", 2_000_000) + strings.Repeat("<li>x", 5000),
		"paragraphs":   strings.Repeat("x<p>", 2_600_000),
		"open links":   strings.Repeat(`<a href="h">x`, 500_000),
	}
	for name, page := range pages {
		start := time.Now()
		got := Markdown(page)
		if took := time.Since(start); took > 20*time.Second {
			t.Errorf("%s: took %s", name, took)
		}
		if len(got) > markdownMax+1000 {
			t.Errorf("%s: %d bytes of Markdown", name, len(got))
		}
	}
	if got := Markdown(strings.Repeat("word ", markdownMax)); !strings.Contains(got, "went on past what gnosh turns into text") {
		t.Errorf("a page cut short does not say so: …%q", got[max(0, len(got)-120):])
	}
}

// A skipped element that closes itself (<svg/>, <iframe/>) skips nothing:
// the text after it stays.
func TestMarkdownSelfClosingSkippedElement(t *testing.T) {
	got := Markdown(`<p>before</p><svg viewBox="0 0 1 1"/><p>after the icon</p><iframe src="x"/><p>and after the frame</p>`)
	for _, want := range []string{"before", "after the icon", "and after the frame"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q: %q", want, got)
		}
	}
}
