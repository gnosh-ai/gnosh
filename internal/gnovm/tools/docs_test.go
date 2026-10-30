// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovmtools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gnovm_run examples in Klash's documents are runs, not illustrations.
// In a fenced block, a line `gnovm_run {...}` is a call, its JSON exactly the
// tool's input, and the lines under it indented by two spaces are what the
// call shows the model: everything above the status line. The status line
// (gas and time, which vary from run to run) and notes are left out. Each
// block runs on a fresh project, call by call, through the real tool, so a
// change to what the board prints names the example that no longer says what
// Klash does. Every call in a block is checked, so one run lists every
// example that has drifted, not only the first.

// docsWithExamples are the documents checked, relative to the repository
// root, and the fewest examples each must hold: a parser that silently found
// nothing would otherwise pass by checking nothing.
var docsWithExamples = map[string]int{
	"GNOMEM.md": 3,
	"README.md": 0,
}

// docCall is one gnovm_run call in a document and what the document says it
// shows.
type docCall struct {
	line  int // 1-based, in the document
	input string
	want  string
}

// docBlocks returns the gnovm_run calls in doc, one slice per fenced block
// that has any.
func docBlocks(t *testing.T, doc string) [][]docCall {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("..", "..", "..", doc))
	require.NoError(t, err)

	var blocks [][]docCall
	var block []docCall
	var want []string
	inFence, collecting := false, false
	flush := func() {
		if collecting {
			block[len(block)-1].want = strings.Join(want, "\n")
		}
		want, collecting = nil, false
	}
	for i, line := range strings.Split(string(text), "\n") {
		switch {
		case strings.HasPrefix(line, "```"):
			flush()
			if inFence && len(block) > 0 {
				blocks = append(blocks, block)
			}
			block, inFence = nil, !inFence
		case !inFence:
		case strings.HasPrefix(line, "gnovm_run "):
			flush()
			block = append(block, docCall{line: i + 1, input: strings.TrimPrefix(line, "gnovm_run ")})
			collecting = true
		case collecting && strings.HasPrefix(line, "  "):
			want = append(want, strings.TrimPrefix(line, "  "))
		default:
			flush()
		}
	}
	return blocks
}

// shown is what a gnovm_run result shows above its status line.
func shown(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	for len(lines) > 0 && strings.HasPrefix(lines[len(lines)-1], "note: ") {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 {
		lines = lines[:len(lines)-1] // the status line
	}
	return strings.Join(lines, "\n")
}

func TestDocExamplesRun(t *testing.T) {
	for doc, least := range docsWithExamples {
		blocks := docBlocks(t, doc)
		n := 0
		for _, b := range blocks {
			n += len(b)
		}
		require.GreaterOrEqual(t, n, least,
			"%s should hold at least %d gnovm_run examples, and %d were found: if the document still has them, the parser no longer sees them", doc, least, n)

		for _, block := range blocks {
			t.Run(fmt.Sprintf("%s:%d", doc, block[0].line), func(t *testing.T) {
				run := NewRunTool(newLazy(t), "/proj")
				for _, c := range block {
					resp := call(t, run, c.input)
					assert.Equal(t, c.want, shown(resp.Content),
						"%s line %d: gnovm_run %s no longer shows what the document says. Update the example, or the code if the document is right.\nThe full result was:\n%s",
						doc, c.line, c.input, resp.Content)
				}
			})
		}
	}
}

// TestDocBlocksParse pins the parser, so a document's examples cannot drop
// out of TestDocExamplesRun unnoticed.
func TestDocBlocksParse(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "a", "b", "c")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "DOC.md"), []byte(strings.Join([]string{
		"gnovm_run {\"code\": \"outside a fence, ignored\"}",
		"```",
		`gnovm_run {"name": "mem", "code": "mem.Show()"}`,
		"  line one",
		"    indented two",
		`gnovm_run {"code": "1+1"}`,
		"  2",
		"```",
		"",
		"```sh",
		"echo no calls here",
		"```",
		"```",
		`gnovm_run {"code": "x := 1"}`,
		"",
		"```",
	}, "\n")), 0o644))
	t.Chdir(root)

	blocks := docBlocks(t, "DOC.md")
	require.Equal(t, [][]docCall{
		{
			{line: 3, input: `{"name": "mem", "code": "mem.Show()"}`, want: "line one\n  indented two"},
			{line: 6, input: `{"code": "1+1"}`, want: "2"},
		},
		{
			{line: 14, input: `{"code": "x := 1"}`, want: ""},
		},
	}, blocks)

	require.Equal(t, "40", shown("40\nok · VM s · gas 12 · kept as entry 1\nnote: new scratch VM\n"))
	require.Equal(t, "", shown("ok · VM s · nothing to keep\n"))
}
