package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
)

// publisher stands in for the artifact server: it hands out a URL for a
// file of this machine and says where a copy from another belongs.
type publisher struct {
	cacheDir string
	served   []string
	hosts    []string
	err      error
}

func (p *publisher) Register(path, host string) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	p.served = append(p.served, path)
	p.hosts = append(p.hosts, host)
	return "http://192.0.2.7:7777/" + strings.Repeat("a", 32) + "/" + filepath.Base(path), nil
}

func (p *publisher) CacheDir() string { return p.cacheDir }

func runArtifactWith(t *testing.T, pub ArtifactPublisher, workingDir string, hosts config.Hosts, params ArtifactParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	resp, err := NewArtifactTool(pub, nil, workingDir, hosts).Run(context.Background(), fantasy.ToolCall{
		ID:    "call-1",
		Name:  ArtifactToolName,
		Input: string(input),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return resp
}

func runArtifactTool(t *testing.T, params ArtifactParams) fantasy.ToolResponse {
	t.Helper()
	return runArtifactWith(t, &publisher{}, t.TempDir(), nil, params)
}

// metaOf reads the structured answer the TUI gets from a result.
func metaOf(t *testing.T, resp fantasy.ToolResponse) ArtifactResponseMetadata {
	t.Helper()
	var meta ArtifactResponseMetadata
	if err := json.Unmarshal([]byte(resp.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal metadata %s: %v", resp.Metadata, err)
	}
	return meta
}

func TestArtifactTool_RequiresLabel(t *testing.T) {
	resp := runArtifactTool(t, ArtifactParams{Label: "  ", Target: "/x.mp4"})
	if !resp.IsError {
		t.Fatalf("expected an error for an empty label, got %q", resp.Content)
	}
}

// A deliverable can be named before it exists, so the user sees what the
// agent is working toward from the start. No file is wanted yet, and
// nothing is served.
func TestArtifactTool_ClaimsADeliverableBeforeItExists(t *testing.T) {
	pub := &publisher{}
	resp := runArtifactWith(t, pub, t.TempDir(), nil, ArtifactParams{Label: "hebrew ad video", Target: "   "})
	if resp.IsError {
		t.Fatalf("naming a deliverable before producing it is not an error: %q", resp.Content)
	}
	meta := metaOf(t, resp)
	if !meta.Pending || meta.Label != "hebrew ad video" {
		t.Fatalf("the claim is not pending under its label: %+v", meta)
	}
	if meta.Target != "" || meta.URL != "" {
		t.Fatalf("a claim has nothing to open yet: %+v", meta)
	}
	if len(pub.served) != 0 {
		t.Fatalf("nothing exists to serve: %v", pub.served)
	}
	if !strings.Contains(resp.Content, "pending") {
		t.Fatalf("the answer does not tell the model what the user now sees: %q", resp.Content)
	}
}

// A file of this machine is served, and the URL comes back both in words
// for the model and as metadata for the sidebar. A path relative to the
// working directory is the same file.
func TestArtifactTool_ServesAFileAtAURL(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "video_a_v7.mp4")
	if err := os.WriteFile(video, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pub := &publisher{}

	resp := runArtifactWith(t, pub, dir, nil, ArtifactParams{Label: "hebrew ad video", Target: "video_a_v7.mp4"})
	if resp.IsError {
		t.Fatalf("unexpected error: %q", resp.Content)
	}
	meta := metaOf(t, resp)
	if meta.URL == "" || !strings.Contains(resp.Content, meta.URL) {
		t.Fatalf("the URL is missing from the metadata or the text: meta=%+v content=%q", meta, resp.Content)
	}
	if meta.Target != video {
		t.Fatalf("target = %q, want the resolved path %q", meta.Target, video)
	}
	if len(pub.served) != 1 || pub.served[0] != video {
		t.Fatalf("served %v, want just %q", pub.served, video)
	}
}

// Recording a deliverable is the claim that it exists, so a file that is
// not there is refused, and the answer says so plainly.
func TestArtifactTool_RefusesAFileThatIsNotThere(t *testing.T) {
	dir := t.TempDir()
	pub := &publisher{}

	resp := runArtifactWith(t, pub, dir, nil, ArtifactParams{Label: "video", Target: "video_a_v7.mp4"})
	if !resp.IsError {
		t.Fatalf("expected an error for a missing file, got %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "video_a_v7.mp4") || !strings.Contains(resp.Content, "not on this machine") {
		t.Fatalf("the refusal does not say what is missing where: %q", resp.Content)
	}
	if len(pub.served) != 0 {
		t.Fatalf("a missing file was served: %v", pub.served)
	}
}

// A target that is already a URL is recorded as it is: there is nothing
// to serve and no file to find.
func TestArtifactTool_KeepsALinkAsItIs(t *testing.T) {
	resp := runArtifactTool(t, ArtifactParams{Label: "docs", Target: "https://example.com/x"})
	if resp.IsError || !strings.Contains(resp.Content, "link") {
		t.Fatalf("link confirmation wrong: content=%q isError=%v", resp.Content, resp.IsError)
	}
	if meta := metaOf(t, resp); meta.URL != "https://example.com/x" {
		t.Fatalf("a link is its own URL, got %q", meta.URL)
	}
}

// A host the user never declared is named as such, with the ones they
// did declare, rather than being tried.
func TestArtifactTool_RefusesAnUndeclaredHost(t *testing.T) {
	hosts := config.Hosts{"render": {SSH: "root@198.51.100.4"}}
	resp := runArtifactWith(t, &publisher{cacheDir: t.TempDir()}, t.TempDir(), hosts,
		ArtifactParams{Label: "video", Target: "/root/renders/v7.mp4", Host: "droplet"})
	if !resp.IsError {
		t.Fatalf("expected an error for an undeclared host, got %q", resp.Content)
	}
	// Not merely "render" somewhere: "/root/renders/v7.mp4" contains it,
	// and a refusal that only read as a missing local file would pass.
	if !strings.Contains(resp.Content, `host "droplet" is not declared`) ||
		!strings.Contains(resp.Content, "the declared hosts are render") {
		t.Fatalf("the refusal does not name the host asked for and the ones declared: %q", resp.Content)
	}
}

// A file on another machine is named by the path it has there, which the
// tool cannot guess from a relative one.
func TestArtifactTool_WantsAnAbsolutePathOnAHost(t *testing.T) {
	hosts := config.Hosts{"render": {SSH: "root@198.51.100.4"}}
	resp := runArtifactWith(t, &publisher{cacheDir: t.TempDir()}, t.TempDir(), hosts,
		ArtifactParams{Label: "video", Target: "renders/v7.mp4", Host: "render"})
	if !resp.IsError || !strings.Contains(resp.Content, "relative") {
		t.Fatalf("expected a refusal naming the relative path, got %q", resp.Content)
	}
}

// With nothing served — artifact_server "off" — a file on another
// machine is still recorded, as the path it has there, and nothing is
// copied back.
func TestArtifactTool_RecordsAHostPathWhenNothingIsServed(t *testing.T) {
	hosts := config.Hosts{"render": {SSH: "root@198.51.100.4"}}
	pub := &publisher{} // no cache dir: nothing is served
	resp := runArtifactWith(t, pub, t.TempDir(), hosts,
		ArtifactParams{Label: "video", Target: "/root/renders/v7.mp4", Host: "render"})
	if resp.IsError {
		t.Fatalf("unexpected error: %q", resp.Content)
	}
	meta := metaOf(t, resp)
	if meta.URL != "" {
		t.Fatalf("nothing is served, so there is no URL: %q", meta.URL)
	}
	if meta.Target != "/root/renders/v7.mp4" {
		t.Fatalf("target = %q, want the path on the host", meta.Target)
	}
	if !strings.Contains(resp.Content, "render") {
		t.Fatalf("the answer does not say which machine it is on: %q", resp.Content)
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
