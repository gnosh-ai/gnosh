// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovmtools

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The gnovm_run examples in Gnosh's documents are runs, not illustrations.
// In a fenced block, a line `gnovm_run {...}` is a call, its JSON exactly the
// tool's input, and the lines under it indented by two spaces are what the
// call shows the model: everything above the status line. The status line
// (gas and time, which vary from run to run) and notes are left out. Each
// block runs on a fresh project, call by call, through the real tool, so a
// change to what mem prints names the example that no longer says what
// Gnosh does. Every call in a block is checked, so one run lists every
// example that has drifted, not only the first.
//
// A block can also show what the user does: a line `/mem <args>` is the
// user's /mem, with what it shows indented under it, and a line that starts
// `mem due: ` or `mem waiting on you: ` is the notice gnosh shows the user
// as a session starts, after the calls above it. They run against the same
// mem as the block's gnovm_run calls, the /mem lines in a later session than
// theirs: the user reads what an earlier session of the agent left.

// The sessions a block's calls run in: the agent's, then the user's. mem
// names a session by its id's first 8 characters, so these differ there, as
// two sessions' random ids do.
const (
	docAgentSession = "6f1c2a9e-0b7d-4c55-9a3e-2f8d1b7c4e10"
	docUserSession  = "b3e9d0c4-5a61-4f2e-8c17-9d4a2e6f3b85"
)

// docRun runs a block's gnovm_run call as the agent, in its session.
func docRun(t *testing.T, tool fantasy.AgentTool, input string) fantasy.ToolResponse {
	t.Helper()
	ctx := context.WithValue(context.Background(), tools.SessionIDContextKey, docAgentSession)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-" + t.Name(), Name: tool.Info().Name, Input: input})
	require.NoError(t, err)
	return resp
}

// repoRoot is the repository root, from this package's directory.
var repoRoot = filepath.Join("..", "..", "..")

// checkedDocs are the documents checked, relative to the repository root,
// and the fewest gnovm_run examples each must hold: a parser that silently
// found nothing would otherwise pass by checking nothing.
var checkedDocs = map[string]int{
	"GNOMEM.md": 10,
	"README.md": 10,
}

// docCall is one call in a document and what the document says it shows:
// gnovm_run as the agent makes it, /mem as the user types it, or the notice
// gnosh shows as a session starts.
type docCall struct {
	line  int    // 1-based, in the document
	kind  string // "gnovm_run", "/mem" or "notice"
	input string
	want  string
}

// docBlocks returns the gnovm_run calls in doc, under root, one slice per
// fenced block that has any.
func docBlocks(t *testing.T, root, doc string) [][]docCall {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(root, doc))
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
			block = append(block, docCall{line: i + 1, kind: "gnovm_run", input: strings.TrimPrefix(line, "gnovm_run ")})
			collecting = true
		case line == "/mem" || strings.HasPrefix(line, "/mem "):
			flush()
			block = append(block, docCall{line: i + 1, kind: "/mem", input: strings.TrimSpace(strings.TrimPrefix(line, "/mem"))})
			collecting = true
		case strings.HasPrefix(line, "mem due: ") || strings.HasPrefix(line, "mem waiting on you: "):
			flush()
			block = append(block, docCall{line: i + 1, kind: "notice", want: line})
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
	// An error's stack ends in a blank line, which a document cannot show.
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func TestDocExamplesRun(t *testing.T) {
	for doc, least := range checkedDocs {
		blocks := docBlocks(t, repoRoot, doc)
		n := 0
		for _, b := range blocks {
			n += len(b)
		}
		require.GreaterOrEqual(t, n, least,
			"%s should hold at least %d gnovm_run examples, and %d were found: if the document still has them, the parser no longer sees them", doc, least, n)

		for _, block := range blocks {
			t.Run(fmt.Sprintf("%s:%d", doc, block[0].line), func(t *testing.T) {
				l := newLazy(t)
				run := NewRunTool(l, "/proj")
				titles := docTitles(block)
				for _, c := range block {
					switch c.kind {
					case "/mem":
						client, err := l.Get()
						require.NoError(t, err)
						got, _, err := client.MemCommand(context.Background(), "/proj", docUserSession, c.input, titles)
						require.NoError(t, err, "%s line %d: /mem %s", doc, c.line, c.input)
						assert.Equal(t, docWhen(c.want), docWhen(strings.TrimRight(got, "\n")),
							"%s line %d: /mem %s no longer shows what the document says. Update the example, or the code if the document is right.\nIt showed:\n%s",
							doc, c.line, c.input, got)
					case "notice":
						assert.Equal(t, c.want, l.MemNotice("/proj"),
							"%s line %d: gnosh's notice as a session starts is no longer what the document says", doc, c.line)
					default:
						resp := docRun(t, run, c.input)
						assert.Equal(t, c.want, shown(resp.Content),
							"%s line %d: gnovm_run %s no longer shows what the document says. Update the example, or the code if the document is right.\nThe full result was:\n%s",
							doc, c.line, c.input, resp.Content)
					}
				}
			})
		}
	}
}

// logWhen is when a /mem log entry was made, which no two runs share.
var logWhen = regexp.MustCompile(`(?m)^(#\d+) \d{4}-\d{2}-\d{2} \d{2}:\d{2},`)

// docWhen blanks the times in /mem's text, so a document can show real ones.
func docWhen(s string) string { return logWhen.ReplaceAllString(s, "$1 <when>,") }

// agentIn is how /mem log names the agent: by its session's title.
var agentIn = regexp.MustCompile(`the agent in "([^"]+)"`)

// docTitles titles the block's session as the document does: a session's
// title comes from its first prompt, not from mem, so a document may show
// any.
func docTitles(block []docCall) map[string]string {
	for _, c := range block {
		if m := agentIn.FindStringSubmatch(c.want); m != nil {
			return map[string]string{docAgentSession: m[1]}
		}
	}
	return nil
}

// TestDocBlocksParse pins the parser, so a document's examples cannot drop
// out of TestDocExamplesRun unnoticed.
func TestDocBlocksParse(t *testing.T) {
	dir := t.TempDir()
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
		"```",
		`gnovm_run {"name": "mem", "code": "mem.Fact(\"a\", \"b\")"}`,
		"  fact `a` is new",
		"/mem log",
		`  #1 2026-10-30 10:12, the agent in "Facts": mem.Fact("a", "b")`,
		"      fact `a`: \"b\"",
		"/mem",
		"  mem: 1 fact",
		`mem waiting on you: "which day?" (q-which-day)`,
		"```",
	}, "\n")), 0o644))

	blocks := docBlocks(t, dir, "DOC.md")
	require.Equal(t, [][]docCall{
		{
			{line: 3, kind: "gnovm_run", input: `{"name": "mem", "code": "mem.Show()"}`, want: "line one\n  indented two"},
			{line: 6, kind: "gnovm_run", input: `{"code": "1+1"}`, want: "2"},
		},
		{
			{line: 14, kind: "gnovm_run", input: `{"code": "x := 1"}`, want: ""},
		},
		{
			{line: 18, kind: "gnovm_run", input: `{"name": "mem", "code": "mem.Fact(\"a\", \"b\")"}`, want: "fact `a` is new"},
			{line: 20, kind: "/mem", input: "log", want: "#1 2026-10-30 10:12, the agent in \"Facts\": mem.Fact(\"a\", \"b\")\n    fact `a`: \"b\""},
			{line: 23, kind: "/mem", input: "", want: "mem: 1 fact"},
			{line: 25, kind: "notice", want: `mem waiting on you: "which day?" (q-which-day)`},
		},
	}, blocks)
	require.Equal(t, map[string]string{docAgentSession: "Facts"}, docTitles(blocks[2]))
	require.Nil(t, docTitles(blocks[0]))
	require.Equal(t, "#3 <when>, you: mem.Forget(\"a\")\n    remove fact `a`\n#12 <when>, gnosh: x",
		docWhen("#3 2026-10-30 10:12, you: mem.Forget(\"a\")\n    remove fact `a`\n#12 2026-01-02 03:04, gnosh: x"))

	require.Equal(t, "40", shown("40\nok · VM s · gas 12 · kept as entry 1\nnote: new scratch VM\n"))
	require.Equal(t, "", shown("ok · VM s · nothing to keep\n"))
	require.Equal(t, "ERROR — boom\nf()\n    x.gno:3",
		shown("ERROR — boom\nf()\n    x.gno:3\n\nfailed · VM s · nothing kept\n"))
}

// A link in a document that stays inside the repository has to lead
// somewhere: its path must exist, and its anchor must name a heading of the
// Markdown file it points into. A file deleted or a heading renamed would
// otherwise leave the link pointing at nothing, without a word.

// linkRe finds a Markdown link's target, or an HTML href.
var linkRe = regexp.MustCompile(`\]\(([^)\s]+)\)|href="([^"]+)"`)

// prose is a document's text outside its fenced blocks, where a link or a
// heading is meant as one.
func prose(text string) string {
	var out []string
	in := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "```") {
			in = !in
			continue
		}
		if !in {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// slug is a heading's anchor as GitHub makes it: lower case, each space a
// hyphen, and everything but letters, digits, hyphens and underscores
// dropped, Markdown's backticks and asterisks with it.
func slug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.IsMark(r), r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// anchors are the anchors of text's headings. A repeated heading's are
// numbered from -1, as GitHub numbers them.
func anchors(text string) map[string]bool {
	out := map[string]bool{}
	seen := map[string]int{}
	for _, line := range strings.Split(prose(text), "\n") {
		hashes := len(line) - len(strings.TrimLeft(line, "#"))
		heading, ok := strings.CutPrefix(line[hashes:], " ")
		if hashes == 0 || hashes > 6 || !ok {
			continue
		}
		s := slug(strings.TrimSpace(heading))
		if n := seen[s]; n > 0 {
			out[fmt.Sprintf("%s-%d", s, n)] = true
		} else {
			out[s] = true
		}
		seen[s]++
	}
	return out
}

// linkProblems lists the links in doc, under root, that stay inside the
// repository but lead nowhere, and counts the links it checked.
func linkProblems(root, doc string) (problems []string, checked int, err error) {
	text, err := os.ReadFile(filepath.Join(root, doc))
	if err != nil {
		return nil, 0, err
	}
	for _, m := range linkRe.FindAllStringSubmatch(prose(string(text)), -1) {
		link := m[1] + m[2]
		if strings.Contains(link, ":") { // https:, mailto: and the like
			continue
		}
		checked++
		path, anchor, _ := strings.Cut(link, "#")
		target := filepath.Join(root, filepath.Dir(doc), filepath.FromSlash(path))
		if path != "" {
			if _, err := os.Stat(target); err != nil {
				problems = append(problems, fmt.Sprintf("%s links to %s, which is not in the repository", doc, path))
				continue
			}
		}
		if anchor == "" || (path != "" && !strings.HasSuffix(path, ".md")) {
			continue // no anchor, or a line anchor into source, GitHub's own
		}
		if path == "" {
			target = filepath.Join(root, doc)
		}
		b, err := os.ReadFile(target)
		if err != nil {
			return nil, 0, err
		}
		if !anchors(string(b))[anchor] {
			problems = append(problems, fmt.Sprintf("%s links to %s#%s, and %s has no such heading", doc, path, anchor, cmp.Or(path, doc)))
		}
	}
	return problems, checked, nil
}

func TestDocLinksResolve(t *testing.T) {
	for doc := range checkedDocs {
		problems, checked, err := linkProblems(repoRoot, doc)
		require.NoError(t, err)
		require.Positive(t, checked,
			"%s has no links inside the repository: if it still has them, the pattern no longer finds them", doc)
		assert.Empty(t, problems, "links that lead nowhere: fix each link, or the file or heading it meant")
	}
}

// TestDocLinkProblems pins linkProblems on a document holding every kind of
// link, the broken ones too.
func TestDocLinkProblems(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644))
	}
	write("src/x.go", "package x\n")
	write("OTHER.md", "# Other\n\n## Built for gno.land\n")
	write("DOC.md", strings.Join([]string{
		"# Doc",
		"## Notes",
		"## Notes",
		"## `gnovm_run` and Q&A",
		"[a](src/x.go) [b](src/) [c](src/x.go#L3) [d](#notes) [e](#notes-1) [f](#gnovm_run-and-qa)",
		`<a href="OTHER.md#built-for-gnoland">g</a> [h](https://example.com/gone) [i](mailto:a@b.c)`,
		"[j](gone.md) [k](#nowhere) [l](OTHER.md#notes) [m](#notes-2)",
		"```",
		"# in a fence, not a heading",
		"[n](also-gone.md)",
		"```",
		"[o](#in-a-fence-not-a-heading)",
	}, "\n"))

	problems, checked, err := linkProblems(dir, "DOC.md")
	require.NoError(t, err)
	require.Equal(t, 12, checked)
	require.Equal(t, []string{
		"DOC.md links to gone.md, which is not in the repository",
		"DOC.md links to #nowhere, and DOC.md has no such heading",
		"DOC.md links to OTHER.md#notes, and OTHER.md has no such heading",
		"DOC.md links to #notes-2, and DOC.md has no such heading",
		"DOC.md links to #in-a-fence-not-a-heading, and DOC.md has no such heading",
	}, problems)
}

// docPrompts binds a user's words quoted in a document to the real-model
// scenario that shows Gnosh doing what the document says it does: every
// prompt of the scenario must appear in the document word for word. Change
// the example and this fails until the scenario says the same, and is run.
var docPrompts = []struct{ doc, scenario string }{
	{"README.md", "readme-goal.txtar"},
	{"README.md", "remind-next-time.txtar"},
	{"GNOMEM.md", "remind-next-time.txtar"},
	{"README.md", "memory-update.txtar"},
	{"GNOMEM.md", "recall-paraphrased.txtar"},
	{"README.md", "compute-backoff.txtar"},
}

// quotedProse is a document's prose with block-quote markers, the **You:**
// tag and line breaks taken out, so a prompt quoted across several lines
// reads as one.
func quotedProse(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(prose(text), "\n") {
		line = strings.TrimPrefix(strings.TrimPrefix(line, ">"), " ")
		line = strings.ReplaceAll(line, "**You:** ", "")
		b.WriteString(line)
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// scenarioPrompts are a scenario's prompts, the first session's and each
// new session's, in order, each on one line.
func scenarioPrompts(txtar string) []string {
	var out []string
	for _, sec := range strings.Split("\n"+txtar, "\n-- ")[1:] {
		name, body, _ := strings.Cut(sec, " --\n")
		if name == "prompt" || name == "prompt-new" {
			out = append(out, strings.Join(strings.Fields(body), " "))
		}
	}
	return out
}

func TestDocPromptsAreScenarios(t *testing.T) {
	for _, p := range docPrompts {
		text, err := os.ReadFile(filepath.Join(repoRoot, p.doc))
		require.NoError(t, err)
		sc, err := os.ReadFile(filepath.Join(repoRoot, "internal", "gnovm", "testdata", "scenarios", p.scenario))
		require.NoError(t, err)
		prompts := scenarioPrompts(string(sc))
		require.NotEmpty(t, prompts, "%s has no prompt", p.scenario)
		quoted := quotedProse(string(text))
		for _, prompt := range prompts {
			assert.Contains(t, quoted, prompt,
				"%s no longer quotes %s word for word: change the scenario to match, and run it", p.doc, p.scenario)
		}
	}
}

// TestDocScenarioPrompts pins scenarioPrompts: every session's prompt, in
// order, each on one line, and nothing from the other sections.
func TestDocScenarioPrompts(t *testing.T) {
	require.Equal(t, []string{"first, said once.", "second", "third one"}, scenarioPrompts(
		"A description.\n\n-- project/go.mod --\nmodule x\n-- prompt --\nfirst,\nsaid once.\n"+
			"-- prompt-new --\nsecond\n-- check --\nanswer last contains x\n-- prompt-new --\nthird   one\n"))
}

// installLine is each shape of line README's Installation block may hold:
// the build, and commands that write only under ~, which the test points at
// a temporary directory. A line of any other shape fails the test rather
// than run on the machine running it.
var installLine = regexp.MustCompile(`^(go build -o gnosh \.|mkdir -p ~/[\w./-]+|install -m 755 gnosh ~/[\w./-]+)(\s+#.*)?$`)

// TestDocInstallLinesRun runs README's install lines as written, in a home
// with no ~/.local/bin, as a new machine often has none: install does not
// make the directory it installs into. A file stands in for the build's
// binary; every landing checks the build itself.
func TestDocInstallLinesRun(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("needs bash")
	}
	readme, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	require.NoError(t, err)
	_, after, _ := strings.Cut(string(readme), "\n## Installation\n")
	_, after, _ = strings.Cut(after, "```bash\n")
	block, _, _ := strings.Cut(after, "```")
	require.Contains(t, block, "go build -o gnosh .", "README's Installation block")
	for line := range strings.Lines(block) {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		require.Regexp(t, installLine, line, "README's install block has a line this test cannot run safely")
		require.NotContains(t, line, "..", "README's install block has a line this test cannot run safely")
	}

	dir, home := t.TempDir(), t.TempDir()
	cmd := exec.Command(bash, "-e", "-c", strings.Replace(block, "go build -o gnosh .", "touch gnosh", 1))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "README's install lines, in a home with no ~/.local/bin:\n%s", out)
	info, err := os.Stat(filepath.Join(home, ".local", "bin", "gnosh"))
	require.NoError(t, err, "README's install lines leave gnosh in ~/.local/bin")
	assert.NotZero(t, info.Mode()&0o111, "and it can be run")
}
