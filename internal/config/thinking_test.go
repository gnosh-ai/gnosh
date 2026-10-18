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

func TestConfigThinking(t *testing.T) {
	t.Parallel()
	var nilCfg *Config
	if _, set := nilCfg.Thinking(); set {
		t.Fatal("nil config reports a set switch")
	}
	if _, set := (&Config{}).Thinking(); set {
		t.Fatal("empty config reports a set switch")
	}
	off := false
	enabled, set := (&Config{Options: &Options{Thinking: &off}}).Thinking()
	if !set || enabled {
		t.Fatalf("stored false: got enabled=%v set=%v", enabled, set)
	}
}
