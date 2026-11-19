package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/gnodocs"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/instructions"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools"
)

// loadInstructions reads the instruction files in effect from the start of
// the session: the person's own, then the project's, from the names the
// config gives. They are text for the model and run nothing, so a folder
// that is not trusted has its files read too; what they import stays
// inside the project either way.
func (l *live) loadInstructions(ctx context.Context) error {
	set, err := l.readInstructions(ctx)
	if err != nil {
		return err
	}
	l.instr = set
	for _, w := range set.Warnings() {
		l.notice(agent.LevelWarning, "instructions", w.Text)
	}
	l.instructionsLoaded(set.Files(), "")
	return nil
}

// readInstructions reads the instruction files as the config says to. A
// sub-agent has a reading of its own: which of the files that load along
// the way it has been given is its own business, not its parent's.
func (l *live) readInstructions(ctx context.Context) (*instructions.Set, error) {
	c := l.cfg.Context
	o := instructions.Options{
		Cwd:          c.Cwd,
		ProjectRoot:  c.ProjectRoot,
		Home:         l.cfg.Paths.Home,
		ProjectNames: c.ProjectNames,
		LocalNames:   c.LocalNames,
		Trusted:      true,
		MaxDepth:     c.ImportDepth,
		MaxBytes:     c.MaxFileBytes,
		LateMaxChars: c.NestedMaxChars,
	}
	for _, g := range c.Global {
		o.UserFiles = append(o.UserFiles, g.V)
	}
	for _, e := range c.Extra {
		o.ProjectFiles = append(o.ProjectFiles, e.V)
	}
	for _, e := range c.Exclude {
		o.Exclude = append(o.Exclude, e.V)
	}
	if c.Off {
		// Safe mode: no instruction file is read at all.
		o = instructions.Options{Cwd: c.Cwd, ProjectRoot: c.ProjectRoot, Home: l.cfg.Paths.Home}
	}
	set, err := instructions.Load(ctx, o)
	if err != nil {
		return nil, fmt.Errorf("the instruction files could not be read: %w", err)
	}
	return set, nil
}

// instructionsLoaded fires the InstructionsLoaded hooks for files that
// were just loaded. Nothing those hooks say is read, and they are not
// waited for.
func (l *live) instructionsLoaded(files []instructions.File, trigger string) {
	if l.hooks == nil || !l.hooks.has(hooks.InstructionsLoaded) {
		return
	}
	for _, f := range files {
		in := hooks.Input{
			Event:           hooks.InstructionsLoaded,
			FilePath:        f.Path,
			MemoryType:      string(f.Scope),
			LoadReason:      string(f.Reason),
			TriggerFilePath: trigger,
			ParentFilePath:  f.Parent,
		}
		l.background(func(ctx context.Context) { _, _ = l.hooks.run(ctx, in) })
	}
}

// lateInstructions hands the model the instruction files of a directory
// the first time it reads, writes or edits a file there, and the rules
// whose patterns that file matches. They arrive right after the round's
// results. Each is given once, until the part of the conversation that
// held it is cut away.
func (l *live) lateInstructions(ctx context.Context, done agent.ToolDone) []agent.Part {
	return l.lateInstructionsFor(ctx, l.instr, done)
}

// lateInstructionsFor is lateInstructions for one reading of the
// instruction files: the session's own, or a sub-agent's.
func (l *live) lateInstructionsFor(ctx context.Context, instr *instructions.Set, done agent.ToolDone) []agent.Part {
	if instr == nil || done.Result.IsError {
		return nil
	}
	names := fstools.Defaults()
	switch done.Call.Name {
	case names.ReadName, names.WriteName, names.EditName:
	default:
		return nil
	}
	path := touchedPath(done)
	if path == "" {
		return nil
	}
	files, warnings := instr.ForPath(ctx, path)
	for _, w := range warnings {
		l.notice(agent.LevelWarning, "instructions", w.Text)
	}
	if len(files) == 0 {
		return nil
	}
	l.instructionsLoaded(files, path)
	text := instructions.RenderFiles(files)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []agent.Part{{Source: "instructions", Text: prompt.Wrap(text)}}
}

// touchedPath is the file a call of a file tool worked on: the path its
// result reports, or the one its arguments name.
func touchedPath(done agent.ToolDone) string {
	if p, ok := done.Result.Meta["filePath"].(string); ok && p != "" {
		return p
	}
	var in struct {
		FilePath string `json:"file_path"`
	}
	if json.Unmarshal(done.Call.Input, &in) != nil {
		return ""
	}
	return in.FilePath
}

// environment is the block that goes ahead of a session's first prompt:
// what differs from session to session, which is why none of it is in the
// system prompt.
func (l *live) environment() string {
	e := prompt.Environment{
		Now:      l.p.now(),
		Cwd:      l.cfg.Paths.Cwd,
		User:     l.p.userName(),
		Machine:  hostName(),
		Platform: runtime.GOOS + ", " + runtime.GOARCH,
	}
	for _, d := range l.cfg.Permissions.Dirs {
		e.AddedDirs = append(e.AddedDirs, d.V)
	}
	// The other machines a command may be run on, with what the person
	// wrote about each. They are named here and not in the system prompt
	// or in a tool's description, which are the same bytes for every
	// session whatever is declared.
	for _, h := range l.currentHosts() {
		e.Hosts = append(e.Hosts, prompt.Host{Name: h.Name, Machine: h.SSH, Description: h.Description})
	}
	if l.cfg.Options.IncludeGitInstructions.V {
		e.Git = l.gitState()
	}
	return l.texts.Environment(e)
}

// hostName is the machine's name, or "" when it has none to give.
func hostName() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

// gitState says what gnosh knows of git in the working directory, from
// files alone: git itself is never run, because a repository's own
// settings can name programs that git runs on a mere look.
func (l *live) gitState() prompt.Git {
	f := l.st.folder
	if !f.Repo || f.GitDir == "" {
		return prompt.Git{State: prompt.GitNone}
	}
	g := prompt.Git{State: prompt.GitRepo}
	if head, err := os.ReadFile(filepath.Join(f.GitDir, "HEAD")); err == nil {
		if ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/"); ok {
			g.Branch = ref
		}
	}
	if l.gitRunsPrograms() {
		g.State = prompt.GitUnread
		g.Why = "this repository's own git settings name programs that git would run, and the folder is not trusted"
	}
	return g
}

// gnoGuides writes the Gno project's interrealm and security guides
// (gnodocs) to a folder in gnosh's cache, once a session, lets Read open
// them there without a question, and returns their paths for the system
// prompt's part about Gno, which asks for an audit against them; nil when
// they could not be written, and the part is then left out.
func (l *live) gnoGuides() []string {
	l.gnoGuidesOnce.Do(func() {
		dir := filepath.Join(l.cfg.Paths.CacheDir, "gno-docs")
		paths, err := gnodocs.Write(dir)
		if err != nil {
			l.flog.logf("prompt", "the Gno guides could not be written to %s (%v), so the model is not asked to audit Gno code against them", dir, err)
			return
		}
		rule := "Read(/" + filepath.ToSlash(dir) + "/**)"
		if err := l.engine.AddLayer(permission.Layer{Scope: permission.ScopeSession, Name: "the Gno project's guides, which gnosh keeps", Allow: []string{rule}}); err != nil {
			l.flog.logf("permission", "the rule %s could not be added (%v); reading the Gno guides will ask", rule, err)
		}
		l.gnoGuidePaths = paths
	})
	return l.gnoGuidePaths
}

// hasGnoCode reports whether a folder holds Gno code: a Gno module file at
// its top or in the folder gnosh was started in, or a .gno file not far
// below. The look is bounded, so that a huge tree costs nothing.
func hasGnoCode(root, cwd string) bool {
	for _, dir := range []string{root, cwd} {
		for _, name := range []string{"gnomod.toml", "gno.mod", "gnowork.toml"} {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
				return true
			}
		}
	}
	const maxEntries, maxDepth = 4000, 4
	seen := 0
	found := false
	base := strings.Count(filepath.Clean(cwd), string(filepath.Separator))
	_ = filepath.WalkDir(cwd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > maxEntries {
			return filepath.SkipAll
		}
		if d.IsDir() {
			name := d.Name()
			deep := strings.Count(filepath.Clean(path), string(filepath.Separator))-base >= maxDepth
			if path != cwd && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || deep) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".gno") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// systemPrompt builds the session's system prompt: gnosh's own parts, and
// what the command line replaces or appends. Nothing in it changes from
// one session to the next, whatever the folder: the instruction files,
// which differ from project to project, are not in it. They go to the
// model with the first prompt (startInstructions), so that a model server
// that has read the system prompt and the tools once does not read them
// again for another project.
func (l *live) systemPrompt() ([]string, error) {
	replace, err := l.promptFlag("--system-prompt", l.inv.SystemPrompt, l.inv.SystemPromptFile)
	if err != nil {
		return nil, err
	}
	extra, err := l.promptFlag("--append-system-prompt", l.inv.AppendSystemPrompt, l.inv.AppendSystemPromptFile)
	if err != nil {
		return nil, err
	}
	in := prompt.SystemInput{
		Tools:      l.toolSpecs(),
		GnoProject: hasGnoCode(l.cfg.Paths.ProjectRoot, l.cfg.Paths.Cwd),
		Replace:    replace,
		Append:     extra,
	}
	if in.GnoProject {
		in.GnoGuides = l.gnoGuides()
	}
	// Which of the two texts about mem is sent is an option of the
	// config (Options.MemoryText).
	if l.cfg.Options.MemoryText.V == "full" {
		in.Memory = prompt.MemoryFull
	}
	// The output style chosen (outputstyle.go), and the language replies
	// are in.
	in.Style = l.currentStyle()
	in.Language = l.cfg.Options.Language.V
	in.AttributionCommit, in.AttributionPR = l.cfg.Attribution.Commit, l.cfg.Attribution.PR
	for _, c := range l.contribs {
		if c.c.System != nil {
			c.c.System(&in)
		}
	}
	return l.texts.System(in), nil
}

// startInstructions is the instruction files in effect from the start of
// the session, as the model is given them: marked as gnosh's own words,
// each file under its path. It goes out ahead of the session's first
// prompt and is stored with it, and again after a summary has replaced the
// part of the conversation that held it. With no files it is empty.
func (l *live) startInstructions() string {
	if l.instr == nil {
		return ""
	}
	return prompt.Wrap(l.instr.Render())
}

// promptFlag reads one of the two prompt flags and its file form. When
// both are given the file's text comes first, then a blank line, then the
// text typed on the command line.
func (l *live) promptFlag(flag, text, file string) (string, error) {
	if file == "" {
		return text, nil
	}
	path := l.p.absFrom(file)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s-file: the file %s could not be read (%v), so nothing was started", flag, path, err)
	}
	fromFile := strings.TrimSpace(string(data))
	if strings.TrimSpace(text) == "" {
		return fromFile, nil
	}
	return fromFile + "\n\n" + text, nil
}
