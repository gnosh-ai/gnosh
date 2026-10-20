package tools

import (
	"strings"
	"testing"
)

// A binary-like blob (mostly NUL, zero display width) must be truncated by raw
// length even though ansi.StringWidth sees it as tiny. This is the cat-a-binary
// bug that blew past the context window.
func TestTruncateOutput_RawLengthCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	binary := strings.Repeat("\x00", 400_000)
	got := TruncateOutput(binary, dir)
	if len([]rune(got)) > MaxOutputLength+2000 {
		t.Fatalf("binary output not capped: %d runes", len([]rune(got)))
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("expected a truncation marker, got %q", got[:min(80, len(got))])
	}

	// Ordinary short text is returned unchanged.
	short := "hello\nworld\n"
	if out := TruncateOutput(short, dir); out != short {
		t.Fatalf("short output changed: %q", out)
	}

	// Ordinary long text is still truncated (by display width, as before):
	// shorter than the input and carrying a marker. It stays larger in runes
	// than the width budget because zero-width newlines ride along for free.
	long := strings.Repeat("abcdefghij\n", 6000) // ~66k runes
	out := TruncateOutput(long, dir)
	if len([]rune(out)) >= len([]rune(long)) || !strings.Contains(out, "truncated") {
		t.Fatalf("long text not truncated: %d runes", len([]rune(out)))
	}
}
