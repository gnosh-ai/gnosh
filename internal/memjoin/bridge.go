package memjoin

import (
	"context"
	"fmt"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/app"
	"github.com/gnosh-ai/gnosh/internal/bridge"
	"github.com/gnosh-ai/gnosh/internal/buildinfo"
	"github.com/gnosh-ai/gnosh/internal/core"
	gnovmtools "github.com/gnosh-ai/gnosh/internal/gnovm/tools"
)

// This file is `gnosh bridge` (spec/gnosh/bridge.md): the project's
// machines and mem, and the frames view, for a harness that is not gnosh.
// The protocol is internal/bridge's; what is joined here is what a session
// would be given: the four machine tools on the process's one client, the
// project's mem, and the build the worker's handshake compares.

// bridgeCommand is `gnosh bridge`: it serves the host on standard input
// and output until standard input ends, then lets the machines go.
func (p *process) bridgeCommand(ctx context.Context, c *app.CommandContext) int {
	if c.Dirs.State == "" {
		fmt.Fprintln(c.Stderr, "gnosh bridge: gnosh could not work out where its data is kept, so it cannot tell which mem to serve:")
		for _, pr := range c.Problems {
			fmt.Fprintln(c.Stderr, "  "+pr.String())
		}
		return 1
	}
	project := c.Dirs.ProjectRoot
	if project == "" {
		project = c.Dirs.Cwd
	}
	embeddings := ""
	if c.Config != nil {
		embeddings = c.Config.Options.MemEmbeddingsURL.V
	}
	vms := p.client(DataDir(c.Dirs.State), embeddings)
	defer p.shutdown(ctx)

	srv := bridge.New(bridge.Options{
		Project: project,
		Build:   buildinfo.BuildID,
		Version: bridgeVersion(),
		Tools: func(stack func(string) []string) []core.Tool {
			return gnovmtools.New(gnovmtools.Options{
				Client:  gnovmtools.Lazy(vms),
				Project: project,
				MemStack: func(_ context.Context, session string) []string {
					return stack(session)
				},
			}).Tools()
		},
		Frames:   bridgeFrames,
		Confirms: map[string]bool{gnovmtools.DropToolName: true},
		Log:      func(line string) { fmt.Fprintln(c.Stderr, line) },
	})
	if err := srv.Serve(ctx, c.Stdin, c.Stdout); err != nil {
		fmt.Fprintf(c.Stderr, "gnosh bridge: %v\n", err)
		return 1
	}
	return 0
}

// bridgeFrames is the frames a gnovm_run result keeps, in the protocol's
// shape.
func bridgeFrames(res core.ToolResult) []bridge.Frame {
	kept := gnovmtools.FramesOf(res.Meta)
	if len(kept) == 0 {
		return nil
	}
	frames := make([]bridge.Frame, len(kept))
	for i, f := range kept {
		frames[i] = bridge.Frame{Kind: f.Kind, ID: f.Id, Purpose: f.Purpose, Result: f.Result, Thoughts: f.Thoughts, Session: f.Session}
	}
	return frames
}

// bridgeVersion names this build for hello: its id, and its commit's time
// when the build was given one.
func bridgeVersion() string {
	parts := []string{"gnosh", buildinfo.BuildID}
	if buildinfo.BuildTime != "" {
		parts = append(parts, buildinfo.BuildTime)
	}
	return strings.Join(parts, " ")
}
