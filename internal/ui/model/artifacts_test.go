package model

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
)

func artifactCall(label, target string) message.ToolCall {
	return message.ToolCall{
		Name:  tools.ArtifactToolName,
		Input: fmt.Sprintf(`{"label":%q,"target":%q}`, label, target),
	}
}

func msgWith(createdAt int64, calls ...message.ToolCall) message.Message {
	parts := make([]message.ContentPart, 0, len(calls))
	for _, c := range calls {
		parts = append(parts, c)
	}
	return message.Message{CreatedAt: createdAt, Parts: parts}
}

func TestDeriveArtifacts_DedupKeepsNewestAndSorts(t *testing.T) {
	msgs := []message.Message{
		msgWith(10, artifactCall("video", "/v1.mp4")),
		msgWith(20, artifactCall("report", "/r.pdf")),
		msgWith(30, artifactCall("video", "/v5.mp4")), // newer "video" wins
	}
	got := deriveArtifacts(msgs)
	if len(got) != 2 {
		t.Fatalf("want 2 artifacts, got %d: %+v", len(got), got)
	}
	if got[0].Label != "video" || got[0].Target != "/v5.mp4" {
		t.Errorf("want newest video /v5.mp4 first, got %+v", got[0])
	}
	if got[1].Label != "report" {
		t.Errorf("want report second, got %+v", got[1])
	}
}

func TestDeriveArtifacts_SkipsInvalidAndOtherTools(t *testing.T) {
	msgs := []message.Message{
		msgWith(1, message.ToolCall{Name: "bash", Input: `{"command":"ls"}`}),     // other tool
		msgWith(2, message.ToolCall{Name: tools.ArtifactToolName, Input: `{bad`}), // unparseable
		msgWith(3, artifactCall("", "/x")),                                        // empty label
		msgWith(4, artifactCall("ok", "")),                                        // empty target
		msgWith(5, artifactCall("ok", "/y")),                                      // valid
	}
	got := deriveArtifacts(msgs)
	if len(got) != 1 || got[0].Label != "ok" || got[0].Target != "/y" {
		t.Fatalf("want only the one valid artifact, got %+v", got)
	}
}

func TestDeriveArtifacts_ClassifiesURL(t *testing.T) {
	got := deriveArtifacts([]message.Message{
		msgWith(1, artifactCall("site", "https://example.com")),
		msgWith(2, artifactCall("clip", "/tmp/a.mp4")),
	})
	byLabel := map[string]SessionArtifact{}
	for _, a := range got {
		byLabel[a.Label] = a
	}
	if !byLabel["site"].IsURL {
		t.Errorf("site target should classify as URL")
	}
	if byLabel["clip"].IsURL {
		t.Errorf("clip target should not classify as URL")
	}
}

func TestUpsertArtifactsFromMessage(t *testing.T) {
	m := &UI{}

	if !m.upsertArtifactsFromMessage(msgWith(10, artifactCall("video", "/v1.mp4"))) || len(m.sessionArtifacts) != 1 {
		t.Fatalf("first upsert failed: %+v", m.sessionArtifacts)
	}
	// Same label, newer version: replace in place, still one entry.
	m.upsertArtifactsFromMessage(msgWith(20, artifactCall("video", "/v5.mp4")))
	if len(m.sessionArtifacts) != 1 || m.sessionArtifacts[0].Target != "/v5.mp4" {
		t.Fatalf("dedup upsert failed: %+v", m.sessionArtifacts)
	}
	// Different label: add, newest first.
	m.upsertArtifactsFromMessage(msgWith(30, artifactCall("report", "/r.pdf")))
	if len(m.sessionArtifacts) != 2 || m.sessionArtifacts[0].Label != "report" {
		t.Fatalf("add upsert failed or order wrong: %+v", m.sessionArtifacts)
	}
	// A message with no artifact call changes nothing.
	if m.upsertArtifactsFromMessage(msgWith(40, message.ToolCall{Name: "bash", Input: "{}"})) {
		t.Fatalf("expected no change for a non-artifact message")
	}
}
