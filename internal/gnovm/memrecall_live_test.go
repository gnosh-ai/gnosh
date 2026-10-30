// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMemRecallLive measures recall with a real embedding server, named by
// GNOSH_TEST_EMBEDDINGS (the AI box's: http://127.0.0.1:8091/v1), through
// gnosh's own code: a project's mem of 31 entries, and 28 prompts, 17 of
// which mem has the answer to. With Qwen3-Embedding-0.6B on 2026-10-30, at
// memRecallScore, 12 of the 17 were given their answer and none of the
// other 11 was given anything; each prompt's best rows are logged with
// their scores. It fails when a model, or a change to what is embedded,
// makes recall miss more or add noise. Run it where the server is:
//
//	GNOSH_TEST_EMBEDDINGS=http://127.0.0.1:8091/v1 go test -run TestMemRecallLive -v ./internal/gnovm/
func TestMemRecallLive(t *testing.T) {
	url := os.Getenv("GNOSH_TEST_EMBEDDINGS")
	if url == "" {
		t.Skip("GNOSH_TEST_EMBEDDINGS names no embedding server")
	}
	h := newTestHost(t, t.TempDir())
	h.opts.Embeddings = url
	run := func(code string) {
		t.Helper()
		res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s0", Name: MemVM, Code: code})
		require.NoError(t, err, code)
		require.NoError(t, res.Err, code)
	}
	for _, f := range [][2]string{
		{"cmd/test", "go test -race ./..."},
		{"cmd/build", "make build (CGO_ENABLED=0)"},
		{"cmd/lint", "golangci-lint run ./... with the repo's .golangci.yml"},
		{"cmd/deploy", "scripts/deploy.sh kourt, then curl -I https://gno.shop"},
		{"models/qwen", "the abliterated build on the AI box, served by llama-router on port 8080"},
		{"models/embedding", "Qwen3-Embedding-0.6B on port 8091, CPU only"},
		{"hosts/devbox", "the build machine, where heavy builds and tests run"},
		{"hosts/backup-droplet", "holds the git bundles and journal backups"},
		{"db/staging", "the staging database password rotates every 90 days; ask ops for the new one"},
		{"db/migrations", "goose migrations in internal/db/migrations, run with make migrate"},
		{"api/rate-limit", "the public API allows 60 requests a minute per key"},
		{"people/guilhem", "maintains x402 and gno-mcp; reviews Go clients"},
		{"decisions/sqlite", "we use modernc SQLite, not cgo, so cross builds work"},
		{"decisions/license", "the fork is FSL-1.1-MIT; gnovm is GNGPL"},
		{"ci/flaky", "TestWorkerHeartbeatsAndCancel flakes under load (one heartbeat in 1.6 s)"},
		{"ci/cache", "the Go build cache is keyed by go.sum; clear it when modules act stale"},
		{"ui/colors", "purple and hot pink, faded gradients; green and gold sparingly"},
		{"release/qa-signoff", "Lin signs off QA before anything ships to production"},
	} {
		run(`mem.Fact("` + f[0] + `", "` + f[1] + `")`)
	}
	for _, code := range []string{
		`mem.Goal("release v2").Steps("bump the version", "tag v2.0.0", "CI is green")`,
		`mem.Done("release-v2/1", "now 2.0.0")`,
		`mem.Note("release-v2/2", "the tag needs the release key")`,
		`mem.Goal("renew the gno.shop TLS cert", "renew-cert")`,
		`mem.Goal("fix the login redirect loop", "fix-login")`,
		`mem.Done("fix-login", "the session cookie lacked SameSite=Lax; fixed in auth.go")`,
		`mem.Goal("make mem's index faster", "speed-index")`,
		`mem.Note("speed-index", "FTS5 trigram; open per call is 8 ms")`,
		`mem.Goal("rewrite the README's /goal section", "docs-readme")`,
		`mem.Goal("buy oat milk", "oat-milk")`,
		`mem.Goal("upgrade to Go 1.26", "upgrade-go")`,
		`mem.Note("upgrade-go", "blocked on gofumpt's release")`,
		`mem.Goal("cap the x402 fee", "x402-fee")`,
		`mem.Note("x402-fee", "the client signs whatever fee the node's gas price implies")`,
		`mem.Goal("record the launch video", "video")`,
		`mem.Goal("back up mem's journal to the droplet after each erasure", "backup-journal")`,
	} {
		run(code)
	}
	dir := memDir(h.opts.Dir, "/proj")
	require.NoError(t, fillMemVectorsNow(context.Background(), dir, newMemEmbedder(url)))

	// Each prompt with the names that answer it; none, for a prompt mem
	// has nothing on. What is measured is what memRecallScore governs:
	// the rows recall would give, best first, before it leaves out what a
	// session was shown already (in so small a mem, the <mem> block names
	// most of it).
	e := newMemEmbedder(url)
	s, ok, err := readMemSnapshot(context.Background(), dir)
	require.NoError(t, err)
	require.True(t, ok)
	answered, noise, wrong := 0, 0, 0
	prompts := []struct {
		prompt string
		want   []string
	}{
		{"how do I run the tests?", []string{"cmd/test"}},
		{"run the linter before you commit", []string{"cmd/lint"}},
		{"ship the shop site to production", []string{"cmd/deploy"}},
		{"which model runs on the AI box?", []string{"models/qwen"}},
		{"the certificate for the store expires soon", []string{"renew-cert"}},
		{"the heartbeat test failed again in CI", []string{"ci/flaky"}},
		{"I need the staging db credentials", []string{"db/staging"}},
		{"add a column to the users table", []string{"db/migrations"}},
		{"why are we not using cgo for sqlite?", []string{"decisions/sqlite"}},
		{"users get logged out and bounced back to the login page", []string{"fix-login"}},
		{"who should review the x402 Go client?", []string{"people/guilhem"}},
		{"what's left before we ship 2.0?", []string{"release-v2", "release-v2/2", "release-v2/3"}},
		{"pick colors for the new banner", []string{"ui/colors"}},
		{"go builds act weird after bumping modules", []string{"ci/cache"}},
		{"where do the backups go?", []string{"hosts/backup-droplet", "backup-journal"}},
		{"build it on a bigger machine", []string{"hosts/devbox"}},
		// Two questions in one prompt, from the recall-beyond-the-block
		// scenario: close to neither, asked whole.
		{"How do I push this to production, and who has to check it first?", []string{"cmd/deploy", "release/qa-signoff"}},
		{"what's the weather tomorrow?", nil},
		{"refactor the parser in internal/lexer", nil},
		{"add a --verbose flag to the CLI", nil},
		{"explain this stack trace", nil},
		{"write a haiku about autumn", nil},
		{"rename the variable foo to count in main.go", nil},
		{"why is the TUI flickering when I resize?", nil},
		{"translate the error messages to French", nil},
		{"what does this regex match?", nil},
		{"summarize the last commit", nil},
		{"write a haiku about autumn, and then translate it to French.", nil},
	}
	for _, p := range prompts {
		qs, model, err := e.query(context.Background(), p.prompt)
		require.NoError(t, err, "the embedding server answered")
		var recalled, top []string
		hit := false
		for i, r := range s.nearest(qs, model, 0, "item", "fact") {
			if i < 3 {
				top = append(top, fmt.Sprintf("%s %.2f", r.name, r.score))
			}
			if r.score < memRecallScore || len(recalled) == memRecalls {
				continue
			}
			recalled = append(recalled, r.name)
			if slices.Contains(p.want, r.name) {
				hit = true
			} else {
				wrong++
			}
		}
		switch {
		case p.want != nil && hit:
			answered++
		case p.want == nil && recalled != nil:
			noise++
		}
		t.Logf("%-58q → %v (best: %s)", p.prompt, recalled, strings.Join(top, ", "))
	}
	t.Logf("answered %d of 17; gave something to %d of 11 with no answer; %d wrong recalls", answered, noise, wrong)
	require.GreaterOrEqual(t, answered, 12, "recall misses more than it did")
	require.LessOrEqual(t, noise, 1, "recall adds noise to prompts mem has nothing on")
}
