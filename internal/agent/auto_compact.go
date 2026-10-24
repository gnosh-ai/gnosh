package agent

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// compactAt is the running token total at which a session on a model with
// context window cw compacts before its next request: as close to the
// window as leaves room for the reply (largeContextWindowBuffer of a large
// window, smallContextWindowRatio of a small one), or earlier where the
// user says, with Claude Code's variables. CLAUDE_CODE_AUTO_COMPACT_WINDOW
// is how full the context may get, from 100k to 1M tokens, capped at the
// model's window; CLAUDE_AUTOCOMPACT_PCT_OVERRIDE is the percentage of
// that window at which to compact, and like it can only bring compaction
// earlier. Compacting earlier keeps a long session short, and with it the
// time a local model takes to read the conversation again when its cache
// was lost.
func compactAt(cw int64) int64 {
	if cw <= 0 {
		cw = unknownContextWindowFallback
	}
	at := cw - int64(float64(cw)*smallContextWindowRatio)
	if cw > largeContextWindowThreshold {
		at = cw - largeContextWindowBuffer
	}
	window := cw
	if w, ok := envAutoCompactWindow(); ok {
		window = min(w, cw)
		at = min(at, window)
	}
	if p, ok := envAutoCompactPercent(); ok {
		at = min(at, window*p/100)
	}
	return at
}

// envAutoCompactWindow is CLAUDE_CODE_AUTO_COMPACT_WINDOW in tokens, as
// Claude Code reads it: a count, or one with a k or M suffix (200k, 1M),
// raised to 100k or lowered to 1M, and ignored when it is no number.
func envAutoCompactWindow() (int64, bool) {
	value := strings.TrimSpace(os.Getenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW"))
	if value == "" {
		return 0, false
	}
	number, scale := value, int64(1)
	switch {
	case strings.HasSuffix(value, "k"), strings.HasSuffix(value, "K"):
		number, scale = value[:len(value)-1], 1_000
	case strings.HasSuffix(value, "m"), strings.HasSuffix(value, "M"):
		number, scale = value[:len(value)-1], 1_000_000
	}
	n, err := strconv.ParseFloat(number, 64)
	if err != nil || n <= 0 {
		warnCompactVariable("CLAUDE_CODE_AUTO_COMPACT_WINDOW", value, "it is not a token count, so it is ignored")
		return 0, false
	}
	tokens := int64(n * float64(scale))
	switch {
	case tokens < 100_000:
		warnCompactVariable("CLAUDE_CODE_AUTO_COMPACT_WINDOW", value, "it is under 100k tokens, so 100k is used")
		return 100_000, true
	case tokens > 1_000_000:
		warnCompactVariable("CLAUDE_CODE_AUTO_COMPACT_WINDOW", value, "it is over 1M tokens, so 1M is used")
		return 1_000_000, true
	}
	return tokens, true
}

// envAutoCompactPercent is CLAUDE_AUTOCOMPACT_PCT_OVERRIDE, a percentage
// from 1 to 100, or false when it is unset or anything else.
func envAutoCompactPercent() (int64, bool) {
	value := strings.TrimSpace(os.Getenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"))
	if value == "" {
		return 0, false
	}
	p, err := strconv.ParseInt(value, 10, 64)
	if err != nil || p < 1 || p > 100 {
		warnCompactVariable("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", value, "it is not a percentage from 1 to 100, so it is ignored")
		return 0, false
	}
	return p, true
}

// warnedCompactVariables are the values warnCompactVariable has warned
// about, so that each is logged once, not before every step.
var warnedCompactVariables sync.Map

func warnCompactVariable(name, value, why string) {
	if _, seen := warnedCompactVariables.LoadOrStore(name+"="+value, true); !seen {
		slog.Warn("An auto-compaction variable has a value Klash cannot use as given", "variable", name, "value", value, "why", why)
	}
}
