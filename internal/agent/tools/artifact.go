package tools

import (
	"context"
	"fmt"
	"strings"

	"charm.land/fantasy"
)

// ArtifactToolName is the tool the agent calls to surface a finished
// deliverable in the TUI sidebar's Artifacts section.
const ArtifactToolName = "artifact"

const artifactDescription = `Record a finished deliverable ("artifact") so it shows at the top of the sidebar, above the modified files.

Call this once for the main thing the user asked you to produce, once it is ready and the user can use it: a rendered video they can open, a generated report or file, a published URL to share. Do not call it for incidental or intermediate files, or for every file you touched — the Artifacts section is for the headline result, not a file log.

- label: a short, stable name for the deliverable, e.g. "hebrew ad video". Re-use the same label when you produce a newer version; the sidebar replaces the old entry with the latest, so it shows the most recent version of each thing rather than every version.
- target: the path or URL the user would open — an absolute path, a path relative to the working directory, or an http(s) URL.`

// ArtifactParams is the input to the artifact tool.
type ArtifactParams struct {
	Label  string `json:"label" description:"A short, stable name for the deliverable, e.g. \"hebrew ad video\". Re-using a label replaces the older version in the sidebar."`
	Target string `json:"target" description:"The file path or URL the user would open to get the deliverable."`
}

// NewArtifactTool returns the artifact tool. It is registered only for
// interactive sessions (the Artifacts sidebar is a TUI feature), like the
// question tool. The tool only validates and confirms the registration; the
// TUI derives the Artifacts list from these persisted tool calls, so nothing
// extra is stored here.
func NewArtifactTool() fantasy.AgentTool {
	return fantasy.NewAgentTool(
		ArtifactToolName,
		artifactDescription,
		func(ctx context.Context, params ArtifactParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			label := strings.TrimSpace(params.Label)
			target := strings.TrimSpace(params.Target)
			if label == "" {
				return fantasy.NewTextErrorResponse("label is required"), nil
			}
			if target == "" {
				return fantasy.NewTextErrorResponse("target is required"), nil
			}
			noun := "file"
			if ArtifactTargetIsURL(target) {
				noun = "link"
			}
			return fantasy.NewTextResponse(fmt.Sprintf(
				"Recorded %s artifact %q → %s. It now shows at the top of the sidebar.",
				noun, label, target,
			)), nil
		},
	)
}

// ArtifactTargetIsURL reports whether target is an http(s) URL rather than a
// file path. Exported so the TUI classifies artifacts the same way the tool
// does.
func ArtifactTargetIsURL(target string) bool {
	t := strings.TrimSpace(target)
	return strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://")
}
