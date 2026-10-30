package model

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
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
		got, _ := artifactList(st, arts, 60, 10, noPending)
		require.Contains(t, stripANSI(got), "hebrew ad video")
		require.Contains(t, stripANSI(got), "video_a_v5.mp4")
	})

	t.Run("every line fits the sidebar width", func(t *testing.T) {
		t.Parallel()
		st := minimalFileStyles()
		arts := []SessionArtifact{
			{Label: "a very long deliverable label that overflows the column", Target: "https://example.com/a/very/long/url/that/keeps/going", CreatedAt: 1},
		}
		got, _ := artifactList(st, arts, 12, 10, noPending)
		for _, line := range strings.Split(stripANSI(got), "\n") {
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
		got, rows := artifactList(st, arts, 40, 2, noPending)
		require.Contains(t, stripANSI(got), "alpha")
		require.Contains(t, stripANSI(got), "beta")
		require.NotContains(t, stripANSI(got), "gamma")
		require.Contains(t, stripANSI(got), "and 1 more")
		require.Len(t, rows, 2, "only what is shown can be clicked")
	})

	// Jae, 2026-10-30: "i don't like how 'hebrew ad video' is
	// multicolored". The label is one color, not a fade of many.
	t.Run("the label is one color", func(t *testing.T) {
		t.Parallel()
		st := minimalFileStyles()
		arts := []SessionArtifact{{Label: "hebrew ad video", Target: "/x.mp4", CreatedAt: 1}}
		got, _ := artifactList(st, arts, 60, 10, noPending)
		label := strings.Split(got, "\n")[0]
		require.Contains(t, stripANSI(label), "hebrew ad video")
		require.Equal(t, 1, strings.Count(label, "\x1b[38;"), "one foreground color for the whole label: %q", label)
	})

	t.Run("max items zero returns empty", func(t *testing.T) {
		t.Parallel()
		st := minimalFileStyles()
		got, rows := artifactList(st, []SessionArtifact{{Label: "x", Target: "/x"}}, 40, 0, noPending)
		require.Empty(t, got)
		require.Empty(t, rows)
	})
}

// servedURL is where the artifact server serves a video, as long as the
// URLs it hands out are.
const servedURL = "http://127.0.0.1:7777/0123456789abcdef0123456789abcdef/video_a_v14e.mp4"

// textLines is what the user reads of rendered: its lines, without
// styling or hyperlinks, and without the indent under the marker.
func textLines(rendered string) []string {
	var out []string
	for _, line := range strings.Split(ansi.Strip(rendered), "\n") {
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

// Jae, 2026-10-30: "the file name is clipped with '...'". A URL or path
// too long for the sidebar wraps onto more lines, at its slashes, and is
// never cut short.
func TestArtifactList_ShowsTheWholeURLAndPath(t *testing.T) {
	t.Parallel()

	st := minimalFileStyles()
	path := "/root/renders/fromhebrew-ad/final/video_a_v14e_with_captions.mp4"
	arts := []SessionArtifact{
		{Label: "hebrew ad video", Target: "/root/renders/video_a_v14e.mp4", URL: servedURL, CreatedAt: 2},
		{Label: "captioned video", Target: path, CreatedAt: 1},
	}
	// 29 is the sidebar's content width.
	got, rows := artifactList(st, arts, 29, 10, noPending)
	lines := textLines(got)
	require.NotContains(t, ansi.Strip(got), "…", "nothing is cut short")
	for _, line := range strings.Split(ansi.Strip(got), "\n") {
		require.LessOrEqual(t, lipgloss.Width(line), 29, "line exceeds the sidebar: %q", line)
	}

	require.Len(t, rows, 2)
	url := lines[rows[0].first+1 : rows[0].last+1]
	require.Greater(t, len(url), 1, "the URL is longer than a line, so it wraps")
	require.Equal(t, servedURL, strings.Join(url, ""), "all of the URL is shown, in order")
	require.Equal(t, "http://127.0.0.1:7777/", url[0], "it breaks at a slash where it can")
	require.Equal(t, "video_a_v14e.mp4", url[len(url)-1][strings.LastIndex(url[len(url)-1], "/")+1:], "the file name is whole")

	shown := lines[rows[1].first+1 : rows[1].last+1]
	require.Equal(t, path, strings.Join(shown, ""))
}

// Every line of an artifact with a URL — its label and each piece of the
// URL — links to the whole URL, so a terminal that knows hyperlinks opens
// it from whichever line is clicked. A path links nowhere: it only means
// something on this machine.
func TestArtifactList_EveryPieceOfAURLLinksToAllOfIt(t *testing.T) {
	t.Parallel()

	st := minimalFileStyles()
	arts := []SessionArtifact{
		{Label: "hebrew ad video", Target: "/root/renders/video_a_v14e.mp4", URL: servedURL, CreatedAt: 2},
		{Label: "script", Target: "/home/crush/src/fromhebrewad/script.md", CreatedAt: 1},
	}
	got, rows := artifactList(st, arts, 29, 10, noPending)
	scr := uv.NewScreenBuffer(29, lipgloss.Height(got))
	uv.NewStyledString(got).Draw(scr, scr.Bounds())

	// links is where each character of line y links to, past the marker
	// and its space.
	links := func(y int) (urls []string) {
		for x := 2; x < 29; x++ {
			if c := scr.CellAt(x, y); c != nil && strings.TrimSpace(c.Content) != "" {
				urls = append(urls, c.Link.URL)
			}
		}
		return urls
	}
	for y := rows[0].first; y <= rows[0].last; y++ {
		urls := links(y)
		require.NotEmpty(t, urls, "line %d is empty", y)
		for _, u := range urls {
			require.Equal(t, servedURL, u, "line %d links to the wrong place", y)
		}
	}
	for y := rows[1].first; y <= rows[1].last; y++ {
		for _, u := range links(y) {
			require.Empty(t, u, "a path is not a link")
		}
	}
}

// sidebarUI is a UI whose sidebar shows three artifacts — one served at a
// URL, one with only a path, one still pending — drawn on scr.
func sidebarUI(t *testing.T, height int) (*UI, uv.ScreenBuffer) {
	t.Helper()
	u := newTestUI()
	u.height = height
	u.dialog = dialog.NewOverlay()
	u.attachments = attachments.New(attachments.NewRenderer(u.com.Styles.Attachments), attachments.Keymap{})
	u.com.Workspace = &exportWorkspace{dir: t.TempDir()}
	u.session = &session.Session{ID: "s1", Title: "hebrew ad"}
	u.sessionArtifacts = []SessionArtifact{
		{Label: "hebrew ad video", Target: "/root/renders/video_a_v14e.mp4", URL: servedURL, CreatedAt: 3},
		{Label: "script", Target: "/home/crush/src/fromhebrewad/script.md", CreatedAt: 2},
		{Label: "subtitles", Pending: true, CreatedAt: 1},
	}
	u.updateLayoutAndSize()
	return u, drawSidebarOf(u)
}

// drawSidebarOf draws u's sidebar as the UI does, on a screen of u's size.
func drawSidebarOf(u *UI) uv.ScreenBuffer {
	scr := uv.NewScreenBuffer(u.width, u.height)
	u.updateSidebarScrollState()
	u.drawSidebar(scr, u.layout.sidebar)
	return scr
}

// rowShowing is the screen row on which the sidebar shows text, or -1.
func rowShowing(u *UI, scr uv.ScreenBuffer, text string) int {
	area := u.layout.sidebar
	for y := area.Min.Y; y < area.Max.Y; y++ {
		var b strings.Builder
		for x := area.Min.X; x < area.Max.X; x++ {
			if c := scr.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
		if strings.Contains(b.String(), text) {
			return y
		}
	}
	return -1
}

// recordCopies makes copying to the clipboard record what it was given
// instead, for the rest of the test.
func recordCopies(t *testing.T) *[]string {
	t.Helper()
	var copied []string
	prev := copyToClipboard
	copyToClipboard = func(text, _ string) tea.Cmd {
		copied = append(copied, text)
		return nil
	}
	t.Cleanup(func() { copyToClipboard = prev })
	return &copied
}

// Jae, 2026-10-30: "is it possible to make the sidebar artifact url
// clickable? or at least click url to copy to clipboard so i can paste on
// my browser?" A click on any line of an artifact copies what opens it:
// its URL, or its path when nothing serves it. A pending artifact, and a
// line that is no artifact's, copy nothing.
func TestSidebarClickCopiesTheArtifact(t *testing.T) {
	copied := recordCopies(t)
	u, scr := sidebarUI(t, 45)
	click := func(text string, button uv.MouseButton) []string {
		t.Helper()
		y := rowShowing(u, scr, text)
		require.GreaterOrEqual(t, y, 0, "the sidebar does not show %q", text)
		*copied = nil
		_, _ = u.Update(tea.MouseClickMsg(tea.Mouse{X: u.layout.sidebar.Min.X + 3, Y: y, Button: button}))
		return *copied
	}

	for _, text := range []string{"hebrew ad video", "http://127.0.0.1:7777/", "video_a_v14e.mp4"} {
		require.Equal(t, []string{servedURL}, click(text, uv.MouseLeft), "a click on %q", text)
	}
	require.Empty(t, click("video_a_v14e.mp4", uv.MouseRight), "only the left button copies")
	require.Equal(t, []string{"/home/crush/src/fromhebrewad/script.md"}, click("script.md", uv.MouseLeft))
	require.Empty(t, click("subtitles", uv.MouseLeft), "a pending artifact has nothing to copy")
	require.Empty(t, click("Artifacts", uv.MouseLeft))
	require.Empty(t, click("Modified Files", uv.MouseLeft))
}

// The sidebar's content scrolls under its logo, and a click still finds
// the artifact it lands on.
func TestSidebarClickFindsTheArtifactWhenScrolled(t *testing.T) {
	copied := recordCopies(t)
	u, _ := sidebarUI(t, 31)
	require.True(t, u.sidebarScrollable, "the test needs a sidebar that scrolls")

	scrolled := 0
	for offset := 0; offset <= u.sidebarMaxOffsetVal; offset++ {
		u.sidebarOffset = offset
		scr := drawSidebarOf(u)
		y := rowShowing(u, scr, "script.md")
		if y < 0 {
			continue
		}
		*copied = nil
		_, _ = u.Update(tea.MouseClickMsg(tea.Mouse{X: u.layout.sidebar.Min.X + 3, Y: y, Button: uv.MouseLeft}))
		require.Equal(t, []string{"/home/crush/src/fromhebrewad/script.md"}, *copied, "scrolled %d lines", offset)
		if offset > 0 {
			scrolled++
		}
	}
	require.NotZero(t, scrolled, "no click landed on a scrolled sidebar")
}

// What a click copied is said in words, naming the artifact.
func TestArtifactCopied(t *testing.T) {
	t.Parallel()
	require.Equal(t, `Copied the URL of "hebrew ad video": `+servedURL,
		artifactCopied(SessionArtifact{Label: "hebrew ad video", Target: "/v.mp4", URL: servedURL}))
	require.Equal(t, `Copied the path of "script" (it has no URL): /s.md`,
		artifactCopied(SessionArtifact{Label: "script", Target: "/s.md"}))
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
	got, _ := artifactList(st, arts, 60, 10, func(label string) string {
		require.Equal(t, "hebrew ad video", label, "each pending artifact animates under its own label")
		return "@#$sodF"
	})
	require.Contains(t, stripANSI(got), "hebrew ad video")
	require.Contains(t, stripANSI(got), "@#$sodF")
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
