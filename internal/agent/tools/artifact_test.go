package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"charm.land/fantasy"
)

func runArtifactTool(t *testing.T, params ArtifactParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp, err := NewArtifactTool().Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  ArtifactToolName,
		Input: string(input),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return resp
}

func TestArtifactTool_RequiresLabel(t *testing.T) {
	resp := runArtifactTool(t, ArtifactParams{Label: "  ", Target: "/x.mp4"})
	if !resp.IsError {
		t.Fatalf("expected an error for an empty label, got %q", resp.Content)
	}
}

func TestArtifactTool_RequiresTarget(t *testing.T) {
	resp := runArtifactTool(t, ArtifactParams{Label: "video", Target: "   "})
	if !resp.IsError {
		t.Fatalf("expected an error for an empty target, got %q", resp.Content)
	}
}

func TestArtifactTool_ConfirmsFileAndLink(t *testing.T) {
	resp := runArtifactTool(t, ArtifactParams{Label: "hebrew ad video", Target: "~/Downloads/video_a_v5.mp4"})
	if resp.IsError {
		t.Fatalf("unexpected error: %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "hebrew ad video") || !strings.Contains(resp.Content, "file") {
		t.Fatalf("file confirmation is missing details: %q", resp.Content)
	}

	resp = runArtifactTool(t, ArtifactParams{Label: "docs", Target: "https://example.com/x"})
	if resp.IsError || !strings.Contains(resp.Content, "link") {
		t.Fatalf("link confirmation wrong: content=%q isError=%v", resp.Content, resp.IsError)
	}
}

func TestArtifactTargetIsURL(t *testing.T) {
	cases := map[string]bool{
		"https://a.com":   true,
		"http://a.com/x":  true,
		"  https://a.com": true,
		"/tmp/x.mp4":      false,
		"~/x":             false,
		"ftp://a":         false,
		"":                false,
	}
	for in, want := range cases {
		if got := ArtifactTargetIsURL(in); got != want {
			t.Errorf("ArtifactTargetIsURL(%q) = %v, want %v", in, got, want)
		}
	}
}
