package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/text"
)

func TestDraftContextTokens(t *testing.T) {
	if got := draftContextTokens("", "", ""); got != 0 {
		t.Fatalf("empty draft = %d, want 0", got)
	}
	// Plain text uses one token per three bytes, matching agent.defaultEstimate.
	if got := draftContextTokens(strings.Repeat("a", 300), "", ""); got != 100 {
		t.Fatalf("300 prose bytes = %d, want 100", got)
	}
	// JSON counts denser at two bytes per token, bounded by the 64 KiB sniff.
	jsonPayload := `{"items":[` + strings.Repeat(`{"k":"vvvv"},`, 200) + `{"k":"end"}]}`
	if got, want := draftContextTokens(jsonPayload, "", ""), (len(jsonPayload)+1)/2; got != want {
		t.Fatalf("json payload = %d, want %d", got, want)
	}

	// @file mentions add the file's os.Stat size; @image-N and image files add 1600 tokens.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "trace.log")
	if err := os.WriteFile(logPath, []byte(strings.Repeat("x", 9000)), 0o600); err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(imgPath, []byte("pngdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := "inspect @trace.log and @shot.png and @image-1 and @trace.log"
	got := draftContextTokens(prompt, dir, "")
	want := draftEstimateText(prompt) + 3000 + 1600 + 1600 // duplicate @trace.log counted once
	if got != want {
		t.Fatalf("mentions estimate = %d, want %d", got, want)
	}
}

func TestFormatContextSegment(t *testing.T) {
	c := agent.ContextStatus{Window: 65536, Known: true, Point: 52429, Used: 48000}

	// Short draft stays quiet: no noise on ordinary prompts.
	words, full := formatContextSegment(c, 50)
	if words != "context 73%" || !full {
		t.Fatalf("small draft = (%q, %v), want (%q, true)", words, full, "context 73%")
	}

	// A paste that stays below Point but is >= 1% of window shows +draft.
	cLow := agent.ContextStatus{Window: 65536, Known: true, Point: 52429, Used: 20000}
	words, full = formatContextSegment(cLow, 6000)
	if words != "context 30% (+6k draft)" || full {
		t.Fatalf("fits draft = (%q, %v), want (%q, false)", words, full, "context 30% (+6k draft)")
	}

	// Crossing Point warns before sending that the turn will compact.
	words, full = formatContextSegment(c, 6000)
	if words != "context 73% (+6k draft, will compact)" || !full {
		t.Fatalf("compact draft = (%q, %v), want (%q, true)", words, full, "context 73% (+6k draft, will compact)")
	}

	// Exceeding Window warns over window.
	words, full = formatContextSegment(c, 22000)
	if words != "context 73% (+22k draft, over window)" || !full {
		t.Fatalf("over draft = (%q, %v), want (%q, true)", words, full, "context 73% (+22k draft, over window)")
	}
}

func TestStatusLineShowsDraftBeforeSending(t *testing.T) {
	g := newRig(t, 120, 24, text.NoColor)
	g.ev(&agent.ContextEvent{Context: agent.ContextStatus{Window: 65536, Known: true, Point: 52429, Used: 48000}})
	g.liveHas("context 73%").liveLacks("will compact", "over window")

	// Pasting an 18 KB log into the prompt box crosses the 52,429 compaction point.
	g.s.ed.SetText(strings.Repeat("log line here\n", 1400))
	g.frame()
	g.liveHas("will compact")

	// Shell (!) mode does not count as a prompt draft.
	g.s.ed.SetText("!" + strings.Repeat("x", 18000))
	g.frame()
	g.liveLacks("will compact", "over window")
}
