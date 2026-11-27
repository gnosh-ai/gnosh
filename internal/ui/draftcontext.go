package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
)

// Pre-send context estimate for what is currently in the prompt box (typed
// text, collapsed pastes, @file mentions, and pasted @image-N pictures).
//
// Purely local and bounded: it matches internal/agent's defaultEstimate
// (one token per three bytes, or per two bytes for JSON), looks up at most
// ten @file mentions with os.Stat only (never reading file contents on the
// draw path), and stays silent on the status line while a draft is small
// enough not to matter.

const (
	draftJSONSniffBytes = 64 * 1024
	draftImageTokens    = 1600
	draftMaxMentions    = 10
	draftMaxMentionScan = 100
	draftMaxFileBytes   = 256 * 1024
)

var (
	draftMentionWord = regexp.MustCompile(`(?:^|\s)@("[^"\n]+"|[^\s"]+)`)
	draftPastedImage = regexp.MustCompile(`^image-[0-9]+$`)
	draftResourceRef = regexp.MustCompile(`^[A-Za-z0-9_.-]+:[A-Za-z][A-Za-z0-9+.-]*://\S+$`)
)

// draftEstimateText matches agent.defaultEstimate (bytes / 3) for prose and
// uses bytes / 2 for JSON, sniffing at most draftJSONSniffBytes so huge
// pastes never stall the draw loop.
func draftEstimateText(s string) int {
	if s == "" {
		return 0
	}
	if draftLooksLikeJSON(s) {
		return (len(s) + 1) / 2
	}
	return (len(s) + 2) / 3
}

func draftLooksLikeJSON(s string) bool {
	head := s
	if len(head) > draftJSONSniffBytes {
		head = head[:draftJSONSniffBytes]
	}
	head = strings.TrimLeft(head, " \t\r\n")
	if head == "" || (head[0] != '{' && head[0] != '[') {
		return false
	}
	sample := head
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	var punct int
	for i := 0; i < len(sample); i++ {
		switch sample[i] {
		case '{', '}', '[', ']', '"', ':', ',':
			punct++
		}
	}
	return punct*100 > len(sample)*8
}

func isDraftImageExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".avif":
		return true
	}
	return false
}

func isDraftJSONExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json", ".jsonl", ".jsonc":
		return true
	}
	return false
}

// draftContextTokens estimates the tokens the current prompt text and its
// @file / @image mentions will add when sent. It never opens or reads files.
func draftContextTokens(text, cwd, home string) int {
	if text == "" {
		return 0
	}
	total := draftEstimateText(text)
	if !strings.Contains(text, "@") {
		return total
	}
	seen := make(map[string]bool)
	counted := 0
	for _, m := range draftMentionWord.FindAllStringSubmatch(text, draftMaxMentionScan) {
		if counted >= draftMaxMentions {
			break
		}
		raw := m[1]
		if strings.HasPrefix(raw, `"`) {
			raw = strings.Trim(raw, `"`)
		} else {
			raw = strings.TrimRight(raw, ".,;:!?)'")
		}
		if raw == "" || draftResourceRef.MatchString(raw) {
			continue
		}
		if draftPastedImage.MatchString(raw) {
			if !seen[raw] {
				seen[raw] = true
				total += draftImageTokens
				counted++
			}
			continue
		}
		resolved := resolveDraftPath(raw, cwd, home)
		if resolved == "" || seen[resolved] {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || info.IsDir() || !info.Mode().IsRegular() {
			continue
		}
		seen[resolved] = true
		counted++
		if isDraftImageExt(resolved) {
			total += draftImageTokens
			continue
		}
		sz := info.Size()
		if sz <= 0 {
			continue
		}
		if sz > draftMaxFileBytes {
			sz = draftMaxFileBytes
		}
		if isDraftJSONExt(resolved) {
			total += int((sz + 1) / 2)
		} else {
			total += int((sz + 2) / 3)
		}
	}
	return total
}

func resolveDraftPath(raw, cwd, home string) string {
	if strings.HasPrefix(raw, "~/") || raw == "~" {
		if home == "" {
			return ""
		}
		if raw == "~" {
			return home
		}
		return filepath.Join(home, raw[2:])
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	if cwd == "" {
		return ""
	}
	return filepath.Join(cwd, raw)
}

// formatContextSegment formats the status line's context readout, adding
// the pre-send draft estimate only when the draft is at least 1% of the
// window or pushes the next request across the compaction point or window.
func formatContextSegment(c agent.ContextStatus, draft int) (words string, full bool) {
	pct := c.Used * 100 / c.Window
	base := fmt.Sprintf("context %d%%", pct)
	if !c.Known {
		base += " of an assumed " + tokens(c.Window)
	}
	full = c.Point > 0 && c.Used*10 >= c.Point*9
	if draft <= 0 {
		return base, full
	}
	total := c.Used + draft
	crossesPoint := c.Point > 0 && total >= c.Point
	overWindow := total > c.Window
	significant := draft*100 >= c.Window
	if !significant && !crossesPoint && !overWindow {
		return base, full
	}
	add := "+" + tokens(draft) + " draft"
	switch {
	case overWindow:
		return fmt.Sprintf("%s (%s, over window)", base, add), true
	case crossesPoint:
		return fmt.Sprintf("%s (%s, will compact)", base, add), true
	default:
		if c.Point > 0 && total*10 >= c.Point*9 {
			full = true
		}
		return fmt.Sprintf("%s (%s)", base, add), full
	}
}
