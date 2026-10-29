package model

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/stretchr/testify/require"
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
		msgWith(4, artifactCall("ok", "/y")),                                      // valid
	}
	got := deriveArtifacts(msgs)
	if len(got) != 1 || got[0].Label != "ok" || got[0].Target != "/y" {
		t.Fatalf("want only the one valid artifact, got %+v", got)
	}
}

// noPending stands in for the animation shown where a pending
// artifact's target will go, which a list test has no clock for.
func noPending(string) string { return "making it" }

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

func TestArtifactList(t *testing.T) {
	t.Parallel()

	t.Run("shows the label and the target", func(t *testing.T) {
		t.Parallel()
		st := minimalFileStyles()
		arts := []SessionArtifact{
			{Label: "hebrew ad video", Target: "~/Downloads/video_a_v5.mp4", CreatedAt: 1},
		}
		got := stripANSI(artifactList(st, arts, 60, 10, noPending))
		require.Contains(t, got, "hebrew ad video")
		require.Contains(t, got, "video_a_v5.mp4")
	})

	t.Run("every line fits the sidebar width", func(t *testing.T) {
		t.Parallel()
		st := minimalFileStyles()
		arts := []SessionArtifact{
			{Label: "a very long deliverable label that overflows the column", Target: "https://example.com/a/very/long/url/that/keeps/going", CreatedAt: 1},
		}
		got := stripANSI(artifactList(st, arts, 12, 10, noPending))
		for _, line := range strings.Split(got, "\n") {
			require.LessOrEqual(t, lipgloss.Width(line), 12, "line exceeds sidebar width: %q", line)
		}
	})

	t.Run("caps the list and shows a more hint", func(t *testing.T) {
		t.Parallel()
		st := minimalFileStyles()
		arts := []SessionArtifact{
			{Label: "alpha", Target: "/a", CreatedAt: 3},
			{Label: "beta", Target: "/b", CreatedAt: 2},
			{Label: "gamma", Target: "/c", CreatedAt: 1},
		}
		got := stripANSI(artifactList(st, arts, 40, 2, noPending))
		require.Contains(t, got, "alpha")
		require.Contains(t, got, "beta")
		require.NotContains(t, got, "gamma")
		require.Contains(t, got, "and 1 more")
	})

	t.Run("applies nebula styling", func(t *testing.T) {
		t.Parallel()
		st := minimalFileStyles()
		arts := []SessionArtifact{{Label: "deliverable", Target: "/x.mp4", CreatedAt: 1}}
		got := artifactList(st, arts, 60, 10, noPending)
		require.NotEqual(t, stripANSI(got), got, "label should be color-styled (gradient), not plain text")
	})

	t.Run("max items zero returns empty", func(t *testing.T) {
		t.Parallel()
		st := minimalFileStyles()
		got := artifactList(st, []SessionArtifact{{Label: "x", Target: "/x"}}, 40, 0, noPending)
		require.Empty(t, got)
	})
}

// artifactResult is the tool's answer to a recorded artifact, which
// carries where it is served under the call it answers.
func artifactResult(callID, url string) message.ToolResult {
	return message.ToolResult{
		ToolCallID: callID,
		Name:       tools.ArtifactToolName,
		Content:    "Recorded it.",
		Metadata:   fmt.Sprintf(`{"url":%q}`, url),
	}
}

func msgWithResults(createdAt int64, results ...message.ToolResult) message.Message {
	parts := make([]message.ContentPart, 0, len(results))
	for _, r := range results {
		parts = append(parts, r)
	}
	return message.Message{CreatedAt: createdAt, Parts: parts}
}

// The sidebar shows where an artifact is served, which the tool reports
// in its result, in a later message than the call it answers.
func TestDeriveArtifacts_TakesTheURLFromTheResult(t *testing.T) {
	call := artifactCall("hebrew ad video", "/home/crush/renders/v7.mp4")
	call.ID = "call-7"
	msgs := []message.Message{
		msgWith(1, call),
		msgWithResults(2, artifactResult("call-7", "http://192.0.2.7:7777/abc/v7.mp4")),
	}
	arts := deriveArtifacts(msgs)
	require.Len(t, arts, 1)
	require.Equal(t, "http://192.0.2.7:7777/abc/v7.mp4", arts[0].URL)
	require.Equal(t, "http://192.0.2.7:7777/abc/v7.mp4", arts[0].Open(), "the user is given the URL")
	require.Equal(t, "/home/crush/renders/v7.mp4", arts[0].Target, "the path is still known")
}

// An artifact nothing serves — a session from before the server, or
// artifact_server "off" — still shows, as the path it has.
func TestDeriveArtifacts_WithoutAURLShowsThePath(t *testing.T) {
	arts := deriveArtifacts([]message.Message{msgWith(1, artifactCall("report", "/tmp/report.md"))})
	require.Len(t, arts, 1)
	require.Empty(t, arts[0].URL)
	require.Equal(t, "/tmp/report.md", arts[0].Open())
}

// A result that failed, or answers another tool, leaves the artifact as
// it was rather than giving it a URL that serves nothing.
func TestDeriveArtifacts_IgnoresResultsThatAreNotAServedArtifact(t *testing.T) {
	call := artifactCall("video", "/tmp/v7.mp4")
	call.ID = "call-7"
	failed := artifactResult("call-7", "http://192.0.2.7:7777/abc/v7.mp4")
	failed.IsError = true
	other := artifactResult("call-7", "http://192.0.2.7:7777/xyz/v7.mp4")
	other.Name = "bash"

	arts := deriveArtifacts([]message.Message{msgWith(1, call), msgWithResults(2, failed, other)})
	require.Len(t, arts, 1)
	require.Empty(t, arts[0].URL)
}

// Live, the call is stored before the tool has run, so the URL arrives
// in a later message and has to find the artifact it belongs to.
func TestUpsertArtifactsFromMessage_FillsInTheURLLater(t *testing.T) {
	m := &UI{}
	call := artifactCall("video", "/tmp/v7.mp4")
	call.ID = "call-7"

	require.True(t, m.upsertArtifactsFromMessage(msgWith(1, call)))
	require.Empty(t, m.sessionArtifacts[0].URL)

	require.True(t, m.upsertArtifactsFromMessage(msgWithResults(2, artifactResult("call-7", "http://192.0.2.7:7777/abc/v7.mp4"))))
	require.Equal(t, "http://192.0.2.7:7777/abc/v7.mp4", m.sessionArtifacts[0].URL)

	require.False(t, m.upsertArtifactsFromMessage(msgWithResults(3, artifactResult("call-9", "http://192.0.2.7:7777/nope/x.mp4"))),
		"a result for an artifact this session does not have changes nothing")
	require.Len(t, m.sessionArtifacts, 1)
}

// A deliverable named before it exists shows at once, as pending, so the
// user can see what the agent is working toward.
func TestDeriveArtifacts_AClaimShowsAsPending(t *testing.T) {
	arts := deriveArtifacts([]message.Message{msgWith(1, artifactCall("hebrew ad video", ""))})
	require.Len(t, arts, 1)
	require.True(t, arts[0].Pending)
	require.Empty(t, arts[0].Open(), "there is nothing to open yet")
}

// The finished thing replaces the claim it was made under, so the
// sidebar shows one entry that stops being a promise.
func TestDeriveArtifacts_TheDeliverableReplacesTheClaim(t *testing.T) {
	arts := deriveArtifacts([]message.Message{
		msgWith(1, artifactCall("hebrew ad video", "")),
		msgWith(2, artifactCall("hebrew ad video", "/renders/v7.mp4")),
	})
	require.Len(t, arts, 1)
	require.False(t, arts[0].Pending)
	require.Equal(t, "/renders/v7.mp4", arts[0].Open())
}

// A pending artifact is rendered with the animation in place of the
// target it does not have yet, never an empty line.
func TestArtifactList_PendingShowsTheAnimation(t *testing.T) {
	t.Parallel()

	st := minimalFileStyles()
	arts := []SessionArtifact{{Label: "hebrew ad video", Pending: true, CreatedAt: 1}}
	got := stripANSI(artifactList(st, arts, 60, 10, func(label string) string {
		require.Equal(t, "hebrew ad video", label, "each pending artifact animates under its own label")
		return "@#$sodF"
	}))
	require.Contains(t, got, "hebrew ad video")
	require.Contains(t, got, "@#$sodF")
}

// The animations are kept per pending label and let go once the
// deliverable arrives, so a long session does not hold one per thing it
// finished.
func TestAdvancePendingArtifacts(t *testing.T) {
	m := &UI{com: &common.Common{Styles: minimalFileStyles()}}
	require.False(t, m.advancePendingArtifacts(), "nothing pending, nothing to animate")
	require.False(t, m.hasPendingArtifact())

	m.sessionArtifacts = []SessionArtifact{
		{Label: "hebrew ad video", Pending: true},
		{Label: "report", Target: "/r.md"},
	}
	require.True(t, m.hasPendingArtifact())
	require.True(t, m.advancePendingArtifacts())
	require.Len(t, m.artifactAnims, 1, "one animation, for the pending one")
	first := m.pendingArtifactAnim("hebrew ad video")
	require.True(t, m.advancePendingArtifacts())
	require.Same(t, first, m.pendingArtifactAnim("hebrew ad video"), "the same animation keeps running")

	m.sessionArtifacts[0] = SessionArtifact{Label: "hebrew ad video", Target: "/v7.mp4"}
	require.False(t, m.advancePendingArtifacts(), "the deliverable arrived")
	require.Empty(t, m.artifactAnims, "its animation was let go")
}
