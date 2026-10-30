package model

import (
	"cmp"
	"encoding/json"
	"fmt"
	"image"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/anim"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// SessionArtifact is a finished deliverable the agent surfaced with the
// artifact tool, shown at the top of the sidebar. Entries are deduped by
// Label (the newest wins), so the sidebar shows the most recent version of
// each thing rather than every version.
type SessionArtifact struct {
	Label     string
	Target    string // a file path or an http(s) URL
	IsURL     bool
	CreatedAt int64
	// CallID is the artifact tool call this came from, which its result
	// carries the URL under.
	CallID string
	// URL is where Gnosh serves the artifact, so the user can open it
	// from the machine they are sitting at. Empty when it is not served
	// (artifact_server "off", or a session from before it was): then the
	// sidebar shows the path, which only helps on this machine.
	URL string
	// Pending is a deliverable the agent named before producing it, so
	// the user can see what it is working toward. It has no target yet,
	// and the entry shows an animation where the target will go until
	// the agent records the real thing under the same label.
	Pending bool
}

// Open is what the user is given for the artifact: the URL it is served
// at, or the path when nothing serves it. A pending artifact has neither
// yet, which is what the animation in its place says.
func (a SessionArtifact) Open() string {
	if a.URL != "" {
		return a.URL
	}
	return a.Target
}

// artifactsFromMessage extracts artifact-tool calls from a single message.
// A call with an unparseable input or an empty label is skipped (the tool
// rejects those). A call with no target is the agent naming a deliverable
// before producing it, and becomes a pending artifact.
func artifactsFromMessage(msg message.Message) []SessionArtifact {
	var out []SessionArtifact
	for _, call := range msg.ToolCalls() {
		if call.Name != tools.ArtifactToolName {
			continue
		}
		var p tools.ArtifactParams
		if err := json.Unmarshal([]byte(call.Input), &p); err != nil {
			continue
		}
		label := strings.TrimSpace(p.Label)
		target := strings.TrimSpace(p.Target)
		if label == "" {
			continue
		}
		out = append(out, SessionArtifact{
			Label:     label,
			Target:    target,
			IsURL:     tools.ArtifactTargetIsURL(target),
			CreatedAt: msg.CreatedAt,
			CallID:    call.ID,
			Pending:   target == "",
		})
	}
	return out
}

// artifactURLsFromMessage reads, from the results of artifact-tool
// calls in msg, where each artifact is served, by tool call ID. The tool
// puts it in the result's metadata rather than its text, so the sidebar
// and the tool never disagree about the URL. A result from before the
// server existed has no metadata and is skipped.
func artifactURLsFromMessage(msg message.Message) map[string]string {
	var urls map[string]string
	for _, result := range msg.ToolResults() {
		if result.Name != tools.ArtifactToolName || result.IsError || result.Metadata == "" {
			continue
		}
		var meta tools.ArtifactResponseMetadata
		if err := json.Unmarshal([]byte(result.Metadata), &meta); err != nil || meta.URL == "" {
			continue
		}
		if urls == nil {
			urls = make(map[string]string)
		}
		urls[result.ToolCallID] = meta.URL
	}
	return urls
}

// deriveArtifacts builds the deduped, newest-first artifact list from a
// session's messages. It is used when a session loads; live updates go
// through upsertArtifactsFromMessage.
func deriveArtifacts(messages []message.Message) []SessionArtifact {
	byLabel := make(map[string]SessionArtifact)
	urls := make(map[string]string)
	for _, msg := range messages {
		for callID, url := range artifactURLsFromMessage(msg) {
			urls[callID] = url
		}
		for _, art := range artifactsFromMessage(msg) {
			if cur, ok := byLabel[art.Label]; !ok || art.CreatedAt >= cur.CreatedAt {
				byLabel[art.Label] = art
			}
		}
	}
	for label, art := range byLabel {
		if url, ok := urls[art.CallID]; ok {
			art.URL = url
			byLabel[label] = art
		}
	}
	out := make([]SessionArtifact, 0, len(byLabel))
	for _, a := range byLabel {
		out = append(out, a)
	}
	sortArtifacts(out)
	return out
}

// sortArtifacts orders artifacts newest-first, breaking ties by label so the
// order is stable.
func sortArtifacts(arts []SessionArtifact) {
	slices.SortFunc(arts, func(a, b SessionArtifact) int {
		if a.CreatedAt != b.CreatedAt {
			return cmp.Compare(b.CreatedAt, a.CreatedAt)
		}
		return cmp.Compare(a.Label, b.Label)
	})
}

// upsertArtifactsFromMessage merges any artifact calls in msg into the
// live list, replacing an existing entry that shares a label (so the
// newest version of a thing wins), and fills in where an artifact is
// served from any artifact results in msg — the call is stored before
// the tool has run, so the URL arrives in a later message. It reports
// whether anything changed.
func (m *UI) upsertArtifactsFromMessage(msg message.Message) bool {
	arts := artifactsFromMessage(msg)
	urls := artifactURLsFromMessage(msg)
	if len(arts) == 0 && len(urls) == 0 {
		return false
	}
	changed := false
	for callID, url := range urls {
		for i, ex := range m.sessionArtifacts {
			if ex.CallID == callID && ex.URL != url {
				m.sessionArtifacts[i].URL = url
				changed = true
			}
		}
	}
	if len(arts) == 0 {
		return changed
	}
	for _, art := range arts {
		if url, ok := urls[art.CallID]; ok {
			art.URL = url
		}
		filtered := make([]SessionArtifact, 0, len(m.sessionArtifacts)+1)
		for _, ex := range m.sessionArtifacts {
			if ex.Label != art.Label {
				filtered = append(filtered, ex)
			}
		}
		m.sessionArtifacts = append(filtered, art)
	}
	sortArtifacts(m.sessionArtifacts)
	return true
}

// artifactRows is where an artifact is drawn in the Artifacts list: its
// first and last line, so that a click on any line of it finds it.
type artifactRows struct {
	first, last int
	artifact    SessionArtifact
}

// artifactsInfo renders the Artifacts section for the sidebar: each
// deliverable's label with its URL or path beneath it, newest first. It
// also reports the lines each artifact took, counted from the section's
// first line.
func (m *UI) artifactsInfo(width, maxItems int, isSection bool) (string, []artifactRows) {
	t := m.com.Styles

	title := t.Files.SectionTitle.Render("Artifacts")
	if isSection {
		title = common.Section(t, "Artifacts", width)
	}
	list := t.Files.EmptyMessage.Render("None")
	var rows []artifactRows
	if len(m.sessionArtifacts) > 0 {
		list, rows = artifactList(t, m.sessionArtifacts, width, maxItems, m.pendingArtifactFrame)
	}
	// The list starts under the title and a blank line.
	top := lipgloss.Height(title) + 1
	for i := range rows {
		rows[i].first += top
		rows[i].last += top
	}
	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list)), rows
}

// artifactList renders the artifacts newest-first, truncating to maxItems
// with a "…and N more" hint, and reports the lines each one took. An entry
// is a sparkle marker and the label in one plain color, then what the user
// opens beneath it: the URL the artifact is served at, or its path when
// nothing serves it. Neither is cut short: what does not fit wraps onto
// more lines, a URL or path at its slashes. Every line of an entry with a
// URL links to the whole URL, so a terminal that knows hyperlinks opens it
// from whichever line is clicked. The marker alone is colored, sparingly:
// faint gold for a URL the user can open from their own machine, faint
// green for a path that only means something on this one.
func artifactList(t *styles.Styles, arts []SessionArtifact, width, maxItems int, pending func(label string) string) (string, []artifactRows) {
	if maxItems <= 0 {
		return "", nil
	}
	textWidth := max(width-2, 1)
	link := t.Files.Path.Underline(true)

	var lines []string
	var rows []artifactRows
	for i, a := range arts {
		if i >= maxItems {
			break
		}
		marker := t.Files.Additions.Render("✦")
		label := t.Files.ArtifactLabel
		var target []string
		switch {
		case a.Pending:
			// Nothing to open yet: where the target will go, the
			// scrambling glyphs say the agent is making it.
			marker = t.Files.TruncationHint.Render("✦")
			target = []string{ansi.Truncate(pending(a.Label), textWidth, "")}
		case a.IsURL || a.URL != "":
			marker = t.LSP.WarningDiagnostic.Render("✦")
			open := a.Open()
			// One id for all the lines, so the terminal takes them for
			// one link.
			id := "id=artifact-" + strconv.Itoa(i)
			label = label.Hyperlink(open, id)
			linked := link.Hyperlink(open, id)
			for _, piece := range wrapArtifact(open, textWidth) {
				target = append(target, linked.Render(piece))
			}
		default:
			for _, piece := range wrapArtifact(a.Target, textWidth) {
				target = append(target, t.Files.Path.Render(piece))
			}
		}
		first := len(lines)
		for j, piece := range wrapArtifact(a.Label, textWidth) {
			lead := "  "
			if j == 0 {
				lead = marker + " "
			}
			lines = append(lines, lead+label.Render(piece))
		}
		for _, piece := range target {
			lines = append(lines, "  "+piece)
		}
		rows = append(rows, artifactRows{first: first, last: len(lines) - 1, artifact: a})
	}
	if len(arts) > maxItems {
		remaining := len(arts) - maxItems
		lines = append(lines, t.Files.TruncationHint.Render(fmt.Sprintf("…and %d more", remaining)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...), rows
}

// wrapArtifact breaks s into lines at most width cells wide, at its spaces
// and at the slashes of a URL or path, so that a long one is shown whole
// rather than cut off; a run with neither is broken where it must be.
func wrapArtifact(s string, width int) []string {
	return strings.Split(ansi.Wrap(s, width, "/"), "\n")
}

// copyToClipboard is common.CopyToClipboard, a variable so a test can see
// what a click on an artifact copies without writing the clipboard of the
// machine it runs on.
var copyToClipboard = common.CopyToClipboard

// artifactAt is the artifact the sidebar shows at the screen point x, y,
// if any. The content scrolls under the fixed logo, so the point's row is
// a line of the content, counted from where it is scrolled to.
func (m *UI) artifactAt(x, y int) (SessionArtifact, bool) {
	if m.session == nil || m.isCompact {
		return SessionArtifact{}, false
	}
	_, content := sidebarRects(m.layout.sidebar, m.sidebarDrawLogo)
	if !image.Pt(x, y).In(content) {
		return SessionArtifact{}, false
	}
	line := m.sidebarOffset + y - content.Min.Y
	for _, r := range m.sidebarArtifacts {
		if line >= r.first && line <= r.last {
			return r.artifact, true
		}
	}
	return SessionArtifact{}, false
}

// copyArtifactAt copies the artifact under a left click in the sidebar —
// the URL it is served at, or its path when nothing serves it — so the
// user can paste it into a browser on their own machine: Gnosh usually
// runs on another, where it cannot open anything for them. A pending
// artifact has nothing to copy yet.
func (m *UI) copyArtifactAt(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != uv.MouseLeft {
		return nil
	}
	a, ok := m.artifactAt(msg.X, msg.Y)
	if !ok || a.Pending {
		return nil
	}
	return copyToClipboard(a.Open(), artifactCopied(a))
}

// artifactCopied says what a click on an artifact put on the clipboard.
func artifactCopied(a SessionArtifact) string {
	if a.IsURL || a.URL != "" {
		return fmt.Sprintf("Copied the URL of %q: %s", a.Label, a.Open())
	}
	return fmt.Sprintf("Copied the path of %q (it has no URL): %s", a.Label, a.Open())
}

// pendingArtifactFrame renders this frame of the animation shown where a
// pending artifact's target will go. One animation per label, made the
// first time the label is seen and advanced by the shared clock.
func (m *UI) pendingArtifactFrame(label string) string {
	return m.pendingArtifactAnim(label).Render()
}

// pendingArtifactAnim is the animation for a pending artifact's label.
func (m *UI) pendingArtifactAnim(label string) *anim.Anim {
	if m.artifactAnims == nil {
		m.artifactAnims = make(map[string]*anim.Anim)
	}
	if a, ok := m.artifactAnims[label]; ok {
		return a
	}
	t := m.com.Styles
	a := anim.New(anim.Settings{
		ID:          "artifact:" + label,
		Size:        pendingArtifactWidth,
		GradColorA:  t.Header.LogoGradFromColor,
		GradColorB:  t.Header.LogoGradToColor,
		LabelColor:  t.Header.LogoGradFromColor,
		CycleColors: true,
	})
	m.artifactAnims[label] = a
	return a
}

// pendingArtifactWidth is how many scrambling glyphs stand in for a
// target that does not exist yet: enough to read as "being made", short
// enough for a sidebar.
const pendingArtifactWidth = 12

// advancePendingArtifacts moves every pending artifact's animation on by
// a frame and reports whether any of them is showing, which is what
// keeps the shared animation clock running for the sidebar. Animations
// for labels that are no longer pending are dropped, so a long session
// does not hold one per deliverable it finished.
func (m *UI) advancePendingArtifacts() bool {
	if len(m.artifactAnims) == 0 && !m.hasPendingArtifact() {
		return false
	}
	pending := make(map[string]bool, len(m.sessionArtifacts))
	for _, a := range m.sessionArtifacts {
		if a.Pending {
			pending[a.Label] = true
			m.pendingArtifactAnim(a.Label).Advance()
		}
	}
	for label := range m.artifactAnims {
		if !pending[label] {
			delete(m.artifactAnims, label)
		}
	}
	return len(pending) > 0
}

// hasPendingArtifact reports whether the sidebar is showing a deliverable
// the agent has named but not yet produced.
func (m *UI) hasPendingArtifact() bool {
	for _, a := range m.sessionArtifacts {
		if a.Pending {
			return true
		}
	}
	return false
}
