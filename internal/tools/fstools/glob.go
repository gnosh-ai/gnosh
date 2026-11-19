package fstools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools/ignore"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools/wild"
)

// globTool is the Glob tool.
type globTool struct {
	opts *Options
}

func (t *globTool) props() []prop {
	return []prop{
		{name: "pattern", kind: "string", desc: "The pattern file paths must match, such as **/*.go, src/**/*.ts or *.{json,yaml}."},
		{name: "path", kind: "string", desc: "The directory to search in. Leave out for the working directory."},
	}
}

// Spec tells the model what Glob does.
func (t *globTool) Spec() core.ToolSpec {
	o := t.opts
	var rules []string
	if o.GlobNoIgnore {
		rules = append(rules, "Files that .gitignore rules leave out are listed too.")
	} else {
		rules = append(rules, "Files that .gitignore rules leave out are not listed; to find those, name their directory in path or in the pattern.")
	}
	if o.GlobSkipHidden {
		rules = append(rules, "Names that start with a dot are passed over.")
	} else {
		rules = append(rules, "Names that start with a dot are listed like any other.")
	}
	desc := fmt.Sprintf(`Finds files by the pattern of their paths.

In a pattern, * stands for any run of characters inside one name, ? for one character, [abc] for one of a set, and {a,b} for either of two spellings. ** as a whole path segment stands for any number of directories. The pattern is matched against each file's path below the search directory, so *.go finds Go files in that directory only and **/*.go finds them at any depth. Letter case matters.

The result is the full paths of the files that match, most recently changed first, %[1]d at most; a note says when there were more. Only files are listed, never directories. Nothing inside a .git directory is listed, and directories reached through symbolic links are not entered. %[2]s

To search inside files, use %[3]s.`,
		o.GlobMaxResults, strings.Join(rules, " "), o.GrepName)
	return core.ToolSpec{
		Name:        o.GlobName,
		Description: desc,
		InputSchema: schema([]string{"pattern"}, t.props()...),
	}
}

// globPlan is a Glob call worked out: where to look, and for what.
type globPlan struct {
	pattern string
	// start is the directory the call named, or the working directory.
	start location
	// base is where the walk begins: start, moved down by the directories
	// the pattern begins with by name.
	base location
	// rest is the pattern for paths below base.
	rest *wild.Glob
}

func (t *globTool) plan(call core.ToolCall, env *core.ToolEnv) (*globPlan, *failure) {
	o := t.opts
	a, f := parseArgs(o.GlobName, call.Input, propNames(t.props())...)
	if f != nil {
		return nil, f
	}
	pattern, f := a.required("pattern")
	if f != nil {
		return nil, f
	}
	dir, _, f := a.text("path")
	if f != nil {
		return nil, f
	}
	if pattern == "" {
		return nil, failf("%s: pattern is empty.", o.GlobName)
	}
	if strings.ContainsRune(pattern, 0) {
		return nil, failf("%s: pattern holds a NUL byte, which no file name can. Remove it.", o.GlobName)
	}
	p := &globPlan{pattern: pattern}
	pat := pattern
	switch {
	case strings.HasPrefix(pattern, "/"):
		// An absolute pattern says where to search by itself.
		dir = "/"
		pat = strings.TrimLeft(pattern, "/")
	case strings.HasPrefix(pattern, "~/") && o.HomeDir != "":
		dir = o.HomeDir
		pat = strings.TrimLeft(pattern[2:], "/")
	case strings.HasPrefix(pattern, "~"):
		return nil, failf("%s: pattern starts with \"~\", which is not expanded here. Start it with the absolute path, or give the directory in path.", o.GlobName)
	case dir == "":
		if env == nil || env.Cwd == "" {
			return nil, failf("%s: there is no path and the session has no working directory. Give the directory to search in path.", o.GlobName)
		}
		dir = env.Cwd
	}
	if pat == "" || strings.HasSuffix(pat, "/") {
		return nil, failf("%s: the pattern %q names a directory and nothing in it. Add what to look for: %s* for the files in it, %s** for everything below it.", o.GlobName, pattern, pattern, pattern)
	}
	g, err := wild.CompileGlob(pat)
	if err != nil {
		return nil, failf("%s: %v.", o.GlobName, patternProblem(err, pattern))
	}
	if p.start, f = o.locate(env, o.GlobName, "path", dir); f != nil {
		return nil, f
	}
	lead, rest := g.SplitDir()
	p.rest = rest
	p.base = p.start
	if lead != "" && p.start.trouble == nil {
		if p.base, f = o.locate(env, o.GlobName, "path", filepath.Join(p.start.abs, filepath.FromSlash(lead))); f != nil {
			return nil, f
		}
	}
	return p, nil
}

// patternProblem words a pattern's syntax error, naming the pattern as
// the model wrote it.
func patternProblem(err error, pattern string) string {
	var se *wild.SyntaxError
	if errors.As(err, &se) {
		return fmt.Sprintf("the pattern %q cannot be used: %s", short(pattern, 200), se.Problem)
	}
	return err.Error()
}

// Describe says where a Glob call would look. It does not say whether the
// directory is there.
func (t *globTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	p, f := t.plan(call, env)
	if f != nil {
		return core.Action{}, f
	}
	return core.Action{
		Kind:    core.ActionRead,
		Paths:   p.base.paths(),
		Summary: fmt.Sprintf("Find files matching %s in %s", short(p.pattern, 200), p.base.abs),
	}, nil
}

// foundFile is a file a Glob call found.
type foundFile struct {
	path    string
	modTime time.Time
}

// newer orders found files most recently changed first, and files changed
// at the same moment by path.
func newer(a, b foundFile) bool {
	if !a.modTime.Equal(b.modTime) {
		return a.modTime.After(b.modTime)
	}
	return a.path < b.path
}

// newest keeps the most recently changed of the files it is given, and
// counts them all. Many goroutines may add to it.
type newest struct {
	mu    sync.Mutex
	keep  int
	files []foundFile
	total int
}

func (n *newest) add(f foundFile) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.total++
	n.files = append(n.files, f)
	if len(n.files) >= 2*n.keep+64 {
		n.trim()
	}
}

// trim sorts what is held and drops all but the newest keep.
func (n *newest) trim() {
	sort.Slice(n.files, func(i, j int) bool { return newer(n.files[i], n.files[j]) })
	if len(n.files) > n.keep {
		n.files = n.files[:n.keep]
	}
}

// searchContext gives a search its time limit.
func (o *Options) searchContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if o.SearchTimeout > 0 {
		return context.WithTimeout(ctx, o.SearchTimeout)
	}
	return context.WithCancel(ctx)
}

// searchStopped turns the error of a search that did not finish into what
// Run returns: the caller's own cancellation is an error for the harness,
// and running out of time, or a directory that cannot be opened, is a
// result for the model.
func (o *Options) searchStopped(parent context.Context, tool, where string, err error) (core.ToolResult, error) {
	switch {
	case parent.Err() != nil:
		return core.ToolResult{}, fmt.Errorf("%s in %s stopped: %w", tool, where, context.Cause(parent))
	case errors.Is(err, context.DeadlineExceeded):
		return refused(failf("%s ran out of time: searching %s took more than %s, so it was stopped and there is no result. This says nothing about whether there are matches. Search a smaller directory or use a narrower pattern.", tool, where, o.SearchTimeout))
	}
	return refused(failf("%s could not search %s: %v.", tool, where, underlying(err)))
}

// ignoreRules returns the ignore rules in force in a directory, or nil
// when they are turned off.
func (o *Options) ignoreRules(off bool, dir string) *ignore.Rules {
	if off {
		return nil
	}
	return ignore.For(dir, ignore.Options{Names: o.IgnoreFiles, NoGitExclude: o.NoGitExclude})
}

// notADirectory says why a search cannot start at a location, or returns
// nil when it can.
func notADirectory(loc *location, argName string) *failure {
	switch {
	case loc.trouble != nil:
		return loc.trouble
	case !loc.exists:
		return failf("%s does not exist, so there is nothing to search. Check %s.", loc.abs, argName)
	case !loc.info.IsDir():
		return failf("%s is %s, not a directory.", loc.abs, kindOf(loc.info.Mode()))
	}
	return nil
}

// Run finds the files.
func (t *globTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	o := t.opts
	p, f := t.plan(call, env)
	if f != nil {
		return refused(f)
	}
	if f := notADirectory(&p.start, "path"); f != nil {
		return refused(f)
	}
	// The count is always of every match: the walk finishes or the call
	// fails.
	meta := map[string]any{"numFiles": 0, "filenames": []string{}, "truncated": false, "totalMatches": 0, "countIsComplete": true}
	nothing := func(note string) (core.ToolResult, error) {
		text := fmt.Sprintf("No files match %s under %s.\n", p.pattern, p.start.abs)
		if note != "" {
			text += "\n[" + note + "]\n"
		}
		res := core.TextResult(text)
		res.Meta = meta
		return res, nil
	}
	if p.base.trouble != nil {
		return refused(p.base.trouble)
	}
	if !p.base.exists || !p.base.info.IsDir() {
		// The pattern names a directory that is not there: no file can
		// match it.
		return nothing("")
	}

	sctx, cancel := o.searchContext(ctx)
	defer cancel()
	found := &newest{keep: o.GlobMaxResults}
	problems := &walkProblems{}
	job := &walkJob{
		root:    p.base.real,
		hidden:  !o.GlobSkipHidden,
		rules:   o.ignoreRules(o.GlobNoIgnore, p.base.real),
		workers: o.SearchWorkers,
		enter:   p.rest.MatchPrefix,
		visit: func(f walkedFile) {
			if !p.rest.Match(f.rel) {
				return
			}
			shown := filepath.Join(p.base.abs, filepath.FromSlash(f.rel))
			if o.denied(shown, f.path) {
				return
			}
			var info fs.FileInfo
			var err error
			if f.isLink() {
				// A link counts as the file it leads to, when it leads to
				// one.
				info, err = os.Stat(f.path)
			} else {
				info, err = f.entry.Info()
			}
			if err != nil || !info.Mode().IsRegular() {
				return
			}
			found.add(foundFile{path: shown, modTime: info.ModTime()})
		},
	}
	if err := walkTree(sctx, job, problems); err != nil {
		return o.searchStopped(ctx, o.GlobName, p.base.abs, err)
	}
	found.trim()
	if found.total == 0 {
		return nothing(problems.note())
	}

	var out strings.Builder
	names := make([]string, len(found.files))
	for i, f := range found.files {
		names[i] = f.path
		out.WriteString(f.path)
		out.WriteByte('\n')
	}
	var notes []string
	if found.total > len(found.files) {
		notes = append(notes, fmt.Sprintf("Showing the %d most recently changed of %d matching files; %d more are not shown. Narrow the pattern or the path to see them.", len(found.files), found.total, found.total-len(found.files)))
	}
	if note := problems.note(); note != "" {
		notes = append(notes, note)
	}
	if len(notes) > 0 {
		out.WriteByte('\n')
		for _, note := range notes {
			out.WriteString("[" + note + "]\n")
		}
	}
	res := core.TextResult(out.String())
	meta["numFiles"] = len(names)
	meta["filenames"] = names
	meta["truncated"] = found.total > len(names)
	meta["totalMatches"] = found.total
	res.Meta = meta
	return res, nil
}
