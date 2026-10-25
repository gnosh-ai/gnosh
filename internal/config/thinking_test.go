package config

import "testing"

func TestParseThinkingCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		line        string
		enabled, ok bool
	}{
		{"/think", true, true},
		{"  /THINK \n", true, true},
		{"/no_think", false, true},
		{"/nothink", false, true},
		{"/no-think", false, true},
		{"/think about it", false, false},
		{"think", false, false},
		{"", false, false},
	} {
		enabled, ok := ParseThinkingCommand(tc.line)
		if enabled != tc.enabled || ok != tc.ok {
			t.Errorf("ParseThinkingCommand(%q) = %v, %v; want %v, %v", tc.line, enabled, ok, tc.enabled, tc.ok)
		}
	}
}

func TestConfigReasoningLevel(t *testing.T) {
	t.Parallel()
	var nilCfg *Config
	if _, set := nilCfg.ReasoningLevel("qwen", "qwen3.8-27b"); set {
		t.Fatal("nil config reports a stored level")
	}
	if _, set := (&Config{}).ReasoningLevel("qwen", "qwen3.8-27b"); set {
		t.Fatal("empty config reports a stored level")
	}
	if _, set := (&Config{Options: &Options{}}).ReasoningLevel("qwen", "qwen3.8-27b"); set {
		t.Fatal("config with no reasoning map reports a stored level")
	}
	cfg := &Config{Options: &Options{Reasoning: map[string]string{"qwen/qwen3.8-27b": "nothink"}}}
	level, set := cfg.ReasoningLevel("qwen", "qwen3.8-27b")
	if !set || level != "nothink" {
		t.Fatalf("stored nothink: got level=%q set=%v", level, set)
	}
	// An empty string is not a choice.
	cfg = &Config{Options: &Options{Reasoning: map[string]string{"qwen/qwen3.8-27b": ""}}}
	if _, set := cfg.ReasoningLevel("qwen", "qwen3.8-27b"); set {
		t.Fatal("empty level reports as set")
	}
}
