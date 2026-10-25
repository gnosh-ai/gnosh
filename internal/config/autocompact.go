package config

import (
	"strconv"
	"strings"
)

// ParseAutoCompactWindow reads what /autocompact is given as Claude Code
// reads it: "auto", which is 0, or a token count from 100k to 1M, with a
// k or M suffix (150k, 1M), whole (150000), or in thousands (150). ok is
// false for anything else.
func ParseAutoCompactWindow(arg string) (tokens int64, ok bool) {
	value := strings.ToLower(strings.TrimSpace(arg))
	if value == "auto" {
		return 0, true
	}
	number, scale := value, 1.0
	switch {
	case strings.HasSuffix(value, "k"):
		number, scale = value[:len(value)-1], 1_000
	case strings.HasSuffix(value, "m"):
		number, scale = value[:len(value)-1], 1_000_000
	}
	n, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, false
	}
	if scale == 1 && n < 1_000 {
		n *= 1_000
	}
	tokens = int64(n * scale)
	if tokens < 100_000 || tokens > 1_000_000 {
		return 0, false
	}
	return tokens, true
}
