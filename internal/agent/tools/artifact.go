package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
	"mvdan.cc/sh/v3/syntax"
)

// ArtifactToolName is the tool the agent calls to surface a finished
// deliverable in the TUI sidebar's Artifacts section.
const ArtifactToolName = "artifact"

// artifactMaxBytes is the largest file copied back from another machine
// so that it can be served. A deliverable is a video or a document, not
// a disk image, and an unbounded copy would fill this machine.
const artifactMaxBytes = 2 << 30

const artifactDescription = `Record a deliverable ("artifact") in the sidebar, above the modified files, where the user watches for it.

Call it twice for one deliverable:

1. Before you start, with the label alone, to say what you are going to produce. It shows at once as pending, so the user knows what you are working toward before there is anything to show.
2. When the file exists, with the same label and its target. The pending entry becomes the real thing, served at a URL the user can open.

Do not call it for incidental or intermediate files, or for every file you touched — the Artifacts section is for the headline result, not a file log.

- label: a short, stable name for the deliverable, e.g. "hebrew ad video". Re-use it for the finished thing and for every newer version; the sidebar keeps the latest under each label rather than every version.
- target: the path or URL the user would open — an absolute path, a path relative to the working directory, or an http(s) URL. Leave it out to claim the deliverable before producing it.
- host: the declared host the file is on, when it is not on this machine (the same names the bash tool takes). The file is copied here so it can be served; leave it out for a file here.

The file must exist: recording one that does not is an error, so this doubles as the proof that you really produced it. The result gives the URL — tell the user that URL, since a path on this machine is of no use from theirs.`

// ArtifactParams is the input to the artifact tool.
type ArtifactParams struct {
	Label  string `json:"label" description:"A short, stable name for the deliverable, e.g. \"hebrew ad video\". Re-using a label replaces the older version in the sidebar."`
	Target string `json:"target,omitempty" description:"The file path or URL the user would open to get the deliverable. Leave it out to claim the deliverable before you produce it; it then shows as pending."`
	Host   string `json:"host,omitempty" description:"The declared host the file is on, if not this machine; it is copied here to be served."`
}

// ArtifactResponseMetadata rides along with the tool's result so the TUI
// can show the artifact as the URL it is served at rather than a path on
// a machine the user is not sitting at. URL is "" when nothing is served.
type ArtifactResponseMetadata struct {
	Label  string `json:"label"`
	Target string `json:"target"`
	URL    string `json:"url,omitempty"`
	// Pending is set for a deliverable named before it exists.
	Pending bool `json:"pending,omitempty"`
}

// ArtifactPublisher serves a recorded file so the user can open it from
// another machine; artifacts.Server is the one Klash uses. Register
// returns the URL of a file of this machine, or "" when artifacts are
// not served, and CacheDir is where a copy from another machine belongs.
type ArtifactPublisher interface {
	Register(path, host string) (string, error)
	CacheDir() string
}

// NewArtifactTool returns the artifact tool. It is registered only for
// interactive sessions (the Artifacts sidebar is a TUI feature), like the
// question tool. The TUI derives the Artifacts list from these persisted
// tool calls and their results, so nothing extra is stored here.
func NewArtifactTool(publisher ArtifactPublisher, permissions permission.Service, workingDir string, hosts config.Hosts) fantasy.AgentTool {
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
				return artifactClaimed(label), nil
			}
			if ArtifactTargetIsURL(target) {
				return artifactResponse(label, target, target, "link", ""), nil
			}

			host, onHost, err := resolveHost(hosts, params.Host)
			if err != nil {
				return fantasy.NewTextErrorResponse(err.Error()), nil
			}
			// shown is the artifact as the user reads it: the path on
			// the machine it is on, never the copy made to serve it.
			shown, path := target, target
			if !onHost {
				expanded, err := fsext.Expand(target)
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("target %q: %v", target, err)), nil
				}
				path = filepathext.SmartJoin(workingDir, expanded)
				shown = path
				if _, err := os.Stat(path); err != nil {
					return fantasy.NewTextErrorResponse(artifactMissing(path, "", err)), nil
				}
			}

			served := path
			if onHost {
				copied, msg := copyFromHost(ctx, publisher, permissions, call, params.Host, host, target)
				if msg != "" {
					return fantasy.NewTextErrorResponse(msg), nil
				}
				if copied == "" {
					// Nothing is served, so there is nothing to copy:
					// record the artifact as the path it has there.
					return artifactResponse(label, target, "", "file", params.Host), nil
				}
				served = copied
			}

			url, err := publisherRegister(publisher, served, params.Host)
			if err != nil {
				return fantasy.NewTextErrorResponse(artifactMissing(served, params.Host, err)), nil
			}
			return artifactResponse(label, shown, url, "file", params.Host), nil
		},
	)
}

// publisherRegister registers path, tolerating a tool built without a
// publisher (no TUI to show it in).
func publisherRegister(publisher ArtifactPublisher, path, host string) (string, error) {
	if publisher == nil {
		return "", nil
	}
	return publisher.Register(path, host)
}

// artifactClaimed is the answer to a deliverable named before it exists:
// it shows as pending in the sidebar, and the user is watching for it to
// turn into something they can open.
func artifactClaimed(label string) fantasy.ToolResponse {
	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(fmt.Sprintf(
			"Claimed artifact %q. It shows as pending in the sidebar, where the user can see what you are working toward. "+
				"Call artifact again with the same label and the target once the file exists; until then the user is looking at a promise.",
			label)),
		ArtifactResponseMetadata{Label: label, Pending: true},
	)
}

// artifactResponse is the tool's answer: what was recorded, in words for
// the model, and the same as metadata for the TUI.
func artifactResponse(label, target, url, noun, host string) fantasy.ToolResponse {
	where := target
	if host != "" {
		where = target + " on " + host
	}
	text := fmt.Sprintf("Recorded %s artifact %q → %s. It now shows at the top of the sidebar.", noun, label, where)
	if url != "" {
		text += fmt.Sprintf("\nThe user opens it at %s — give them that URL, not the path.", url)
	} else {
		text += "\nIt is not served at a URL, so the user can only reach it on this machine."
	}
	return fantasy.WithResponseMetadata(
		fantasy.NewTextResponse(text),
		ArtifactResponseMetadata{Label: label, Target: target, URL: url},
	)
}

// artifactMissing explains a target that could not be recorded. The
// agent is told plainly that the file is not there, since recording a
// deliverable is also the claim that it exists.
func artifactMissing(path, host string, err error) string {
	where := "this machine"
	if host != "" {
		where = host
	}
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("Nothing to record: %s is not on %s. List it first; an artifact is a file the user can open, so it has to be there.", path, where)
	}
	return fmt.Sprintf("Could not record %s on %s: %v", path, where, err)
}

// copyFromHost copies the file at remote on the declared host to this
// machine, so that it can be served, and returns where it landed. It
// returns "" with no message when artifacts are not served and so
// nothing needs copying, and a message for the model when the copy could
// not be made.
func copyFromHost(ctx context.Context, publisher ArtifactPublisher, permissions permission.Service, call fantasy.ToolCall, name string, host config.HostConfig, remote string) (string, string) {
	if publisher == nil || publisher.CacheDir() == "" {
		return "", ""
	}
	if !filepath.IsAbs(remote) && !strings.HasPrefix(remote, "/") {
		return "", fmt.Sprintf("target %q is relative, and this tool does not know %s's working directory: give the path it has on %s.", remote, name, name)
	}
	dir := filepath.Join(publisher.CacheDir(), name)
	local := filepath.Join(dir, filepath.Base(remote))

	sessionID := GetSessionFromContext(ctx)
	if sessionID == "" {
		return "", "session ID is required to copy a file from another machine"
	}
	granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:   sessionID,
		ToolCallID:  call.ID,
		Path:        local,
		FilePath:    local,
		ToolName:    ArtifactToolName,
		Action:      "copy",
		Description: fmt.Sprintf("Copy %s from %s so it can be served at a URL", remote, name),
		Params:      ArtifactParams{Target: remote, Host: name},
	})
	if err != nil {
		return "", fmt.Sprintf("could not ask to copy %s from %s: %v", remote, name, err)
	}
	if !granted {
		return "", fmt.Sprintf("copying %s from %s was not allowed, so it cannot be served", remote, name)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Sprintf("could not make room for %s: %v", local, err)
	}
	quoted, err := syntax.Quote(remote, syntax.LangBash)
	if err != nil {
		return "", fmt.Sprintf("target %q cannot be written as a shell word: %v", remote, err)
	}
	args := append(append([]string{}, sshOptions...), "--", host.SSH, "cat -- "+quoted)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := os.Create(local)
	if err != nil {
		return "", fmt.Sprintf("could not write %s: %v", local, err)
	}
	defer out.Close()
	var stderr strings.Builder
	cmd.Stdout = &limitedWriter{w: out, left: artifactMaxBytes}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(local)
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Sprintf("Nothing to record: could not read %s on %s: %s. List it there first.", remote, name, detail)
	}
	return local, ""
}

// limitedWriter writes at most left bytes and then fails, so a copy from
// another machine cannot fill this one.
type limitedWriter struct {
	w    io.Writer
	left int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.left {
		return 0, fmt.Errorf("the file is over %d bytes, too big to serve", int64(artifactMaxBytes))
	}
	n, err := l.w.Write(p)
	l.left -= int64(n)
	return n, err
}

// ArtifactTargetIsURL reports whether target is an http(s) URL rather than a
// file path. Exported so the TUI classifies artifacts the same way the tool
// does.
func ArtifactTargetIsURL(target string) bool {
	t := strings.TrimSpace(target)
	return strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://")
}
