package model

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
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
}

// artifactsFromMessage extracts artifact-tool calls from a single message.
// A call with an unparseable input, or an empty label or target, is skipped
// (the tool rejects those, so they never become real artifacts).
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
		if label == "" || target == "" {
			continue
		}
		out = append(out, SessionArtifact{
			Label:     label,
			Target:    target,
			IsURL:     tools.ArtifactTargetIsURL(target),
			CreatedAt: msg.CreatedAt,
		})
	}
	return out
}

// deriveArtifacts builds the deduped, newest-first artifact list from a
// session's messages. It is used when a session loads; live updates go
// through upsertArtifactsFromMessage.
func deriveArtifacts(messages []message.Message) []SessionArtifact {
	byLabel := make(map[string]SessionArtifact)
	for _, msg := range messages {
		for _, art := range artifactsFromMessage(msg) {
			if cur, ok := byLabel[art.Label]; !ok || art.CreatedAt >= cur.CreatedAt {
				byLabel[art.Label] = art
			}
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

// upsertArtifactsFromMessage merges any artifact calls in msg into the live
// list, replacing an existing entry that shares a label (so the newest
// version of a thing wins). It reports whether anything changed.
func (m *UI) upsertArtifactsFromMessage(msg message.Message) bool {
	arts := artifactsFromMessage(msg)
	if len(arts) == 0 {
		return false
	}
	for _, art := range arts {
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

// artifactsInfo renders the Artifacts section for the sidebar: each
// deliverable's label with its path or URL beneath it, newest first.
func (m *UI) artifactsInfo(width, maxItems int, isSection bool) string {
	t := m.com.Styles

	title := t.Files.SectionTitle.Render("Artifacts")
	if isSection {
		title = common.Section(t, "Artifacts", width)
	}
	list := t.Files.EmptyMessage.Render("None")
	if len(m.sessionArtifacts) > 0 {
		list = artifactList(t, m.sessionArtifacts, width, maxItems)
	}
	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
}

// artifactList renders the artifacts newest-first, truncating to maxItems
// with a "…and N more" hint. Each entry is two lines — a sparkle marker and
// the label, then the path or URL beneath it. The label fades through the
// Crush wordmark's purple→hot-pink nebula (flipped row to row so the list
// scintillates); the marker is a sparing faint-green sparkle for a file or
// faint-gold for a link, and a link's target is underlined.
func artifactList(t *styles.Styles, arts []SessionArtifact, width, maxItems int) string {
	if maxItems <= 0 {
		return ""
	}
	grad1, grad2 := t.Header.LogoGradFromColor, t.Header.LogoGradToColor
	dim := t.Files.TruncationHint
	link := dim.Underline(true)

	var lines []string
	shown := 0
	for i, a := range arts {
		if shown >= maxItems {
			break
		}
		// Sparing green/yellow: a faint sparkle marks the kind — green for a
		// file, gold for a link — faded, not solid, against the nebula.
		marker := t.Files.Additions.Render("✦")
		targetText := ansi.Truncate(a.Target, max(width-2, 1), "…")
		target := "  " + dim.Render(targetText)
		if a.IsURL {
			marker = t.LSP.WarningDiagnostic.Render("✦")
			target = "  " + link.Render(targetText)
		}
		// Scintillate: flip the nebula's direction row to row.
		c1, c2 := grad1, grad2
		if i%2 == 1 {
			c1, c2 = grad2, grad1
		}
		label := styles.ApplyForegroundGrad(lipgloss.NewStyle(), ansi.Truncate(a.Label, max(width-2, 1), "…"), c1, c2)
		lines = append(lines, marker+" "+label, target)
		shown++
	}
	if len(arts) > maxItems {
		remaining := len(arts) - maxItems
		lines = append(lines, dim.Render(fmt.Sprintf("…and %d more", remaining)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
