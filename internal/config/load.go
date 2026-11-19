package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Inputs is everything Load is given. Load reads the world through it and
// through nothing else: no file, variable or directory is looked up behind
// its back, so a test that fills in Inputs can never touch the user's real
// configuration.
type Inputs struct {
	// FS is the file system to read.
	FS FS
	// Environ is the environment, as os.Environ gives it: "NAME=value".
	Environ []string
	// Home is the user's home directory, as an absolute path.
	Home string
	// Cwd is the working directory, as an absolute path, after --cwd.
	Cwd string
	// ProjectRoot is where the project begins: the top folder of the
	// repository that holds Cwd, as the trust package works it out. Empty
	// means Cwd. Load never works this out for itself, so that there is
	// one idea of where a project ends. The trust package resolves links
	// in the paths it returns; give Cwd resolved the same way (its
	// Folder.Start beside its Folder.Root), or a project reached through a
	// link will not be seen to hold the working directory, and ProjectRoot
	// falls back to Cwd.
	ProjectRoot string

	// Trusted reports that the folder's own settings may be put in force:
	// the folder is in the trust record, was trusted for this run with
	// --trust-folder, or the person just said yes.
	Trusted bool
	// KeepUntrusted says which kinds of setting (the Kind constants) an
	// untrusted folder's files may still contribute: the ones that only
	// take something away from the agent. It is the trust package's
	// Restricts. Nil keeps nothing at all.
	KeepUntrusted func(kind string) bool

	// Flags are the command-line flags that feed the configuration, as
	// the command-line package parsed them.
	Flags Flags
	// Built says which parts of gnosh exist in the program that is
	// loading. A setting for a part that does not exist stops startup.
	Built Built
	// Tools is the one list of tool names: every name a permission rule
	// or --tools may use. It is needed as soon as anything names a tool.
	Tools []string
	// KnownEnv names more GNOSH_ variables that are not mistakes, for a
	// part of gnosh that reads one of its own.
	KnownEnv []string
	// Checks are more checks, run on the finished Config. Another package
	// adds one for what only it can judge, such as whether a permission
	// rule's pattern can be applied. The problems they return are treated
	// as Load's own.
	Checks []Check
	// LookupUser turns a user id into a name for a message. Nil shows the
	// number.
	LookupUser func(uid int) string
}

// Check looks over a finished Config and returns what is wrong with it.
type Check func(*Config) []Problem

// Flags are the command-line flags that feed the configuration. They hold
// for the one run and are never saved.
type Flags struct {
	// Model is -m / --model: the model for this run, as "model" or
	// "provider/model".
	Model string
	// SmallModel is --small-model: the model for the short side jobs.
	SmallModel string
	// FallbackModel is --fallback-model: the models a turn goes on with
	// when the session's model cannot answer, separated by commas.
	FallbackModel string
	// ReasoningEffort is --reasoning-effort / --effort: the thinking level.
	ReasoningEffort string
	// PermissionMode is --permission-mode.
	PermissionMode string
	// SkipPermissions is -y, --yolo or --dangerously-skip-permissions:
	// start with every permission question skipped. A command line that
	// has already folded these into PermissionMode leaves it false.
	SkipPermissions bool
	// AllowSkipPermissions is --allow-dangerously-skip-permissions: the
	// session may switch to skipping permissions later, without starting
	// that way.
	AllowSkipPermissions bool
	// AllowedTools and DisallowedTools are the rules of --allowedTools and
	// --disallowedTools. Each element may itself be a list, split as
	// SplitRules splits it.
	AllowedTools, DisallowedTools []string
	// ToolsGiven reports that --tools was given, and Tools is its names:
	// empty for --tools "", which gives the model no built-in tool.
	ToolsGiven bool
	Tools      []string
	// AddDirs is --add-dir: more directories the file tools may read.
	AddDirs []string
	// Settings is --settings: a path to a settings file, or the JSON
	// itself.
	Settings string
	// SettingSources is --setting-sources: whose files are read at all,
	// any of "user" (the user's gnosh.json), "project" (the project's)
	// and "local" (the settings file in the project's data folder). Nil
	// means all three. The state file and --settings are read either way.
	SettingSources []string
	// SafeMode is --safe-mode, and Bare is --bare, which turns safe mode
	// on as well.
	SafeMode bool
	Bare     bool
	// Debug is -d / --debug.
	Debug bool
}

// Built says which parts of gnosh the calling program has. The zero value
// has none: every setting that needs a part stops startup, which is right
// for a first binary that has not built them.
type Built struct {
	// Hooks is the hook runner.
	Hooks bool
	// HookEvents are the events the hook runner fires. Nil, with Hooks
	// set, means every event gnosh knows.
	HookEvents []string
	// Permissions is permission rules and modes.
	Permissions bool
	// Hosts is commands on declared machines.
	Hosts bool
	// Env is the env block reaching the commands and hooks gnosh starts.
	Env bool
}

// Everything is a Built with every part present.
func Everything() Built {
	return Built{Hooks: true, Permissions: true, Hosts: true, Env: true}
}

// has reports whether the program has a part.
func (b Built) has(f Feature) bool {
	switch f {
	case FeatureHooks:
		return b.Hooks
	case FeaturePermissions:
		return b.Permissions
	case FeatureHosts:
		return b.Hosts
	case FeatureEnv:
		return b.Env
	}
	return false
}

// Load reads the configuration: the user's file, the state file, the
// project's files, the environment and the command line, weakest first.
//
// It returns every problem it found, not only the first: mistakes
// (Severity Error) and things worth knowing (Severity Warning). When there
// is any mistake the Config is nil and the error is a *LoadError holding
// the mistakes: nothing is in force, and a caller that was loading again
// keeps the settings it had. Load prints nothing; the warnings are the
// caller's to log.
//
// Load is the one check of the configuration. It runs when gnosh starts
// and when the configuration is loaded again, and CheckWrite runs it
// before a command of gnosh's own changes a config file.
func Load(in Inputs) (*Config, []Problem, error) {
	switch {
	case in.FS == nil:
		return nil, nil, errors.New("config: Load was given no file system")
	case !filepath.IsAbs(in.Home):
		return nil, nil, fmt.Errorf("config: the home directory %q is not an absolute path", in.Home)
	case !filepath.IsAbs(in.Cwd):
		return nil, nil, fmt.Errorf("config: the working directory %q is not an absolute path", in.Cwd)
	case in.ProjectRoot != "" && !filepath.IsAbs(in.ProjectRoot):
		return nil, nil, fmt.Errorf("config: the project root %q is not an absolute path", in.ProjectRoot)
	}
	in.Home, in.Cwd = filepath.Clean(in.Home), filepath.Clean(in.Cwd)
	if in.ProjectRoot == "" || !within(in.Cwd, filepath.Clean(in.ProjectRoot)) {
		in.ProjectRoot = in.Cwd
	}
	in.ProjectRoot = filepath.Clean(in.ProjectRoot)

	ld := &loader{in: &in, env: map[string]string{}, probs: &problems{}, secrets: &secrets{}}
	for _, kv := range in.Environ {
		if name, value, ok := strings.Cut(kv, "="); ok && name != "" {
			ld.env[name] = value
		}
	}
	ld.b = newBuilder(&in, ld.probs)
	cfg := ld.run()
	for _, check := range in.Checks {
		ld.probs.list = append(ld.probs.list, check(cfg)...)
	}

	found := dedupe(ld.probs.list)
	for i := range found {
		found[i].Message = ld.secrets.scrub(found[i].Message)
	}
	if mistakes := Errors(found); len(mistakes) > 0 {
		return nil, found, &LoadError{Problems: mistakes}
	}
	return cfg, found, nil
}

// within reports whether path is dir or lies under it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// loader carries one Load from its first file to the finished Config.
type loader struct {
	in      *Inputs
	env     map[string]string
	probs   *problems
	secrets *secrets
	b       *builder

	paths Paths
	files []File
	held  []Held
	// projectFiles are the paths of the project's own config files,
	// whether or not they are there and whether or not they were read.
	projectFiles []string

	// agentsFile is the shared AGENTS.md of the user's config home.
	agentsFile string
	badEnv     []Problem
}

// getenv looks a variable up in the environment Load was given.
func (ld *loader) getenv(name string) (string, bool) {
	v, ok := ld.env[name]
	return v, ok
}

// source makes the reader's view of one file.
func (ld *loader) source(file string, layer Layer, scope, baseDir, ruleDir string) *source {
	return &source{
		file: file, layer: layer, scope: scope, baseDir: baseDir, ruleDir: ruleDir,
		home: ld.in.Home, cwd: ld.in.Cwd, tools: ld.in.Tools, getenv: ld.getenv,
		probs: ld.probs, secrets: ld.secrets,
	}
}

// exists reports whether there is anything at path. A path that cannot be
// looked at counts as there, so that reading it reports why.
func (ld *loader) exists(path string) bool {
	_, err := ld.in.FS.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// read returns the bytes of a config file. ok is false when the file is
// not there, which is no mistake, and when it cannot be used, which is one
// and has been recorded.
func (ld *loader) read(path string) ([]byte, bool) {
	info, err := ld.in.FS.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false
	case err != nil:
		ld.probs.errorf(path, 0, "this config file is there and could not be looked at (%v), so nothing in it is in force. Make it readable, or remove it.", err)
		return nil, false
	case info.IsDir():
		ld.probs.errorf(path, 0, "this is a folder where gnosh looks for a config file, so nothing was read from it. Rename or remove it.")
		return nil, false
	case !info.Mode().IsRegular():
		ld.probs.errorf(path, 0, "this is not an ordinary file, so gnosh did not read it as a config file. Replace it with one, or remove it.")
		return nil, false
	case info.Size() > maxConfigBytes:
		ld.probs.errorf(path, 0, "this config file is %d bytes, and gnosh reads config files of up to %d, so nothing in it is in force.", info.Size(), maxConfigBytes)
		return nil, false
	}
	data, err := ld.in.FS.ReadFile(path)
	if err != nil {
		ld.probs.errorf(path, 0, "this config file could not be read (%v), so nothing in it is in force. Make it readable, or remove it.", err)
		return nil, false
	}
	return data, true
}

// userName names a user id in a message.
func (ld *loader) userName(uid int) string {
	if ld.in.LookupUser != nil {
		if name := ld.in.LookupUser(uid); name != "" {
			return name
		}
	}
	if uid == 0 {
		return "root (user id 0)"
	}
	return "user id " + strconv.Itoa(uid)
}

// ownedByFolderOwner checks that a project's config file belongs to
// whoever owns the folder it is in. A file someone else wrote there (with
// sudo, say) is not used, and that is said out loud, with the fix.
func (ld *loader) ownedByFolderOwner(path string) bool {
	fileUID, ok1 := ld.in.FS.Owner(path)
	dirUID, ok2 := ld.in.FS.Owner(filepath.Dir(path))
	if !ok1 || !ok2 || fileUID == dirUID {
		return true
	}
	ld.probs.errorf(path, 0, "this file belongs to %s, and the folder it is in belongs to %s. gnosh does not take a project's settings from a file someone else owns, so nothing in it is in force, including anything it switches off. If it is yours (written with sudo, say), give it back with: chown %d %s — or remove it.",
		ld.userName(fileUID), ld.userName(dirUID), dirUID, path)
	return false
}

// apply puts a file's ops in force. In a folder that is not trusted, the
// ops of a project's file are held unless they are of a kind that only
// restricts.
func (ld *loader) apply(src *source, f File, project bool) {
	f.Held = project && !ld.in.Trusted
	ld.files = append(ld.files, f)
	for _, o := range src.ops {
		if f.Held && !(ld.in.KeepUntrusted != nil && ld.in.KeepUntrusted(o.kind)) {
			ld.held = append(ld.held, Held{Kind: o.kind, Text: o.text, Origin: o.origin})
			continue
		}
		ld.b.run(o)
	}
}

// file reads a config file, if it is there, and puts what it says in
// force or holds it. ruleDir is what a permission rule's "/path" is under.
func (ld *loader) file(path string, layer Layer, scope, ruleDir string, project bool) bool {
	data, ok := ld.read(path)
	if !ok {
		return false
	}
	if project && !ld.ownedByFolderOwner(path) {
		return false
	}
	src := ld.source(path, layer, scope, filepath.Dir(path), ruleDir)
	readFile(src, data)
	ld.apply(src, File{Path: path, Role: "settings", Scope: scope}, project)
	return true
}

// run reads everything, weakest first, and returns the Config.
func (ld *loader) run() *Config {
	in, p := ld.in, &ld.paths
	ld.dirs()
	ld.ownEnv()

	// --setting-sources names whose files are read at all.
	reads := func(scope string) bool {
		return in.Flags.SettingSources == nil || slices.Contains(in.Flags.SettingSources, scope)
	}
	for _, scope := range in.Flags.SettingSources {
		if scope != "user" && scope != "project" && scope != "local" {
			ld.probs.at(Error, flagOrigin("--setting-sources"), "%s is not a source of settings. The sources are user, project and local.", showName(scope))
		}
	}

	// The user's own file.
	p.UserSettings = filepath.Join(p.ConfigDir, "gnosh.json")
	if reads("user") {
		ld.file(p.UserSettings, LayerUser, "user", p.ConfigDir, false)
	}

	// What was chosen inside gnosh.
	ld.state()

	// The project's file, in the working directory. It is passed over
	// when the working directory is the config directory itself, where it
	// would be the user's file read twice.
	project := filepath.Join(in.Cwd, "gnosh.json")
	if project != p.UserSettings {
		ld.projectFiles = append(ld.projectFiles, project)
		if reads("project") && ld.file(project, LayerProject, "project", in.Cwd, true) {
			p.ProjectSettings = project
		}
	}

	// The project's data folder, and the settings file gnosh itself
	// writes there (saved approvals).
	ld.dataDir()
	p.LocalSettings = filepath.Join(p.DataDir, "settings.local.json")
	ld.projectFiles = append(ld.projectFiles, p.LocalSettings)
	if reads("local") {
		ld.file(p.LocalSettings, LayerProject, "local", in.Cwd, true)
	}

	// --settings: a file, or the JSON itself.
	if given := strings.TrimSpace(in.Flags.Settings); strings.HasPrefix(given, "{") {
		src := ld.source("--settings", LayerFlag, "command line", in.Cwd, in.Cwd)
		readFile(src, []byte(given))
		ld.apply(src, File{Path: "--settings", Role: "settings", Scope: "command line"}, false)
	} else if given != "" {
		path := absPath(given, in.Home, in.Cwd)
		switch {
		case !ld.exists(path):
			ld.probs.at(Error, flagOrigin("--settings"), "the file %s is not there. --settings takes the path of a settings file, or the JSON itself, starting with a brace.", path)
		case !in.Trusted && ld.isProjectFile(path):
			// Naming the folder's own file is not a way round the gate,
			// and neither is naming it while --setting-sources leaves
			// the folder's files out.
			ld.probs.at(Error, flagOrigin("--settings"), "%s is one of this folder's own settings files, and the folder is not trusted, so naming it with --settings does not put it in force. To use the folder's settings, trust the folder (gnosh trust), or start with --trust-folder for this one run.", path)
		default:
			ld.file(path, LayerFlag, "command line", filepath.Dir(path), false)
		}
	}

	// What is held is taken away after every merge, whatever put it there.
	if !in.Trusted {
		for _, what := range ld.b.sweep(in.KeepUntrusted, ld.projectFiles) {
			ld.probs.list = append(ld.probs.list, Problem{Severity: Error, Message: fmt.Sprintf("gnosh found a held setting in force (%s) in a folder that is not trusted, and took it out. This is a fault in gnosh, not in your files; please report it.", what)})
		}
	}
	return ld.finish()
}

// flagOrigin is the Origin of a command-line flag.
func flagOrigin(name string) Origin { return Origin{Layer: LayerFlag, Name: name} }

// envOrigin is the Origin of an environment variable.
func envOrigin(name string) Origin { return Origin{Layer: LayerEnv, Name: "$" + name} }

// dirs works out the directories gnosh uses (CFG-2).
func (ld *loader) dirs() {
	in, p := ld.in, &ld.paths
	p.Home, p.Cwd, p.ProjectRoot = in.Home, in.Cwd, in.ProjectRoot
	xdg := func(name, fallback string) string {
		def := filepath.Join(in.Home, fallback)
		v := ld.env[name]
		switch {
		case v == "":
			return def
		case !filepath.IsAbs(v):
			ld.probs.at(Warning, envOrigin(name), "this variable must hold an absolute path and does not, so it was not used; %s is used.", def)
			return def
		}
		return filepath.Clean(v)
	}
	own := func(name, def string) (string, bool) {
		v := ld.env[name]
		switch {
		case v == "":
			return def, false
		case !filepath.IsAbs(v):
			ld.probs.at(Error, envOrigin(name), "this variable names the directory gnosh uses, and it must be an absolute path, which it is not. Nothing was read from it.")
			return def, false
		}
		return filepath.Clean(v), true
	}
	configHome := xdg("XDG_CONFIG_HOME", ".config")
	ld.agentsFile = filepath.Join(configHome, "AGENTS.md")
	var explicit bool
	if p.ConfigDir, explicit = own("GNOSH_CONFIG_DIR", filepath.Join(configHome, "gnosh")); explicit {
		// A directory named outright is the only place the user's files
		// are read from: no shared file from a config home that was not
		// named.
		ld.agentsFile = filepath.Join(p.ConfigDir, "AGENTS.md")
	}
	p.StateDir, _ = own("GNOSH_DATA_DIR", filepath.Join(xdg("XDG_DATA_HOME", filepath.Join(".local", "share")), "gnosh"))
	p.CacheDir = filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), "gnosh")
	p.StateFile = filepath.Join(p.StateDir, "state.json")
	p.TrustDir = filepath.Join(p.StateDir, "trusted-folders")
}

// isProjectFile reports whether path is one of the folder's own settings
// files: by its name, or as the same file under another (a link in the
// path, as /tmp is on a Mac, or another case on a disk that ignores case).
// Compared by name alone, such a spelling put an untrusted folder's file in
// force as the command line's (the config review of 2026-11-19).
func (ld *loader) isProjectFile(path string) bool {
	if slices.Contains(ld.projectFiles, path) {
		return true
	}
	info, err := ld.in.FS.Stat(path)
	if err != nil {
		return false
	}
	for _, p := range ld.projectFiles {
		if other, err := ld.in.FS.Stat(p); err == nil && os.SameFile(info, other) {
			return true
		}
	}
	return false
}

// dataDir finds the project's data folder: the closest folder of the
// configured name on the way up to the project root, or a new one at the
// project root.
func (ld *loader) dataDir() {
	in, p := ld.in, &ld.paths
	name := ld.b.opts.ProjectDataDir.V
	if filepath.IsAbs(name) {
		p.DataDir = filepath.Clean(name)
		return
	}
	for dir := in.Cwd; ; dir = filepath.Dir(dir) {
		if info, err := in.FS.Stat(filepath.Join(dir, name)); err == nil && info.IsDir() {
			p.DataDir = filepath.Join(dir, name)
			return
		}
		if dir == in.ProjectRoot || dir == filepath.Dir(dir) {
			break
		}
	}
	p.DataDir = filepath.Join(in.ProjectRoot, name)
}

// ownEnv checks the GNOSH_ variables in the environment. One gnosh does
// not know is most likely a misspelling, and would do nothing.
func (ld *loader) ownEnv() {
	var unknown []string
	for name := range ld.env {
		if (name == "GNOSH" || strings.HasPrefix(name, "GNOSH_")) && !knownOwnEnv(name, ld.in.KnownEnv) {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	for _, name := range unknown {
		ld.probs.at(Error, envOrigin(name), "this is not a variable gnosh knows, so it would do nothing. If it is a misspelling, the variables gnosh reads are: %s. Otherwise unset it.", list(readEnvNames()))
	}
}

// state reads the state file and puts its choices in force, above the
// user's gnosh.json and below the project's. Nothing in it can stop
// startup.
func (ld *loader) state() {
	path := ld.paths.StateFile
	st, probs := ReadState(ld.in.FS, path)
	ld.probs.list = append(ld.probs.list, probs...)
	if len(probs) > 0 {
		return
	}
	if !ld.exists(path) {
		return
	}
	ld.files = append(ld.files, File{Path: path, Role: "state", Scope: "user"})
	o := Origin{Layer: LayerState, File: path}
	for _, choice := range []struct {
		what, name string
		sl         *Slot
		before     *Value[ModelRef]
	}{
		{"model", st.Model, &ld.b.large, &ld.b.largeBefore},
		{"small model", st.SmallModel, &ld.b.small, &ld.b.smallBefore},
	} {
		name, sl, before := choice.name, choice.sl, choice.before
		if name == "" {
			continue
		}
		prov, id, ok := strings.Cut(name, "/")
		if !ok || prov == "" || id == "" {
			ld.probs.warnf(path, 0, "the %s saved here, %s, is not of the form provider/id, so it was not used.", choice.what, showName(name))
			continue
		}
		*before = sl.Model
		sl.Model.put(ModelRef{Provider: prov, ID: id}, o)
	}
	for _, name := range slices.Sorted(maps.Keys(st.Thinking)) {
		level := st.Thinking[name]
		if !levelPattern.MatchString(level) {
			ld.probs.warnf(path, 0, "the thinking level saved here for %s is not a level gnosh can read, so it was not used.", showName(name))
			continue
		}
		ld.b.stateThinking[name] = Value[string]{V: level, Set: true, Origin: o}
	}
	if w := st.AutoCompactWindow; w != 0 {
		if w < 100000 || w > 1000000 {
			ld.probs.warnf(path, 0, "the auto-compact window saved here, %d, is not between 100000 and 1000000, so it was not used.", w)
		} else {
			ld.b.opts.AutoCompactWindow.put(w, o)
		}
	}
	ld.stateOptions(path, st.Options)
}

// stateOptions puts in force the options set with /config, each read as a
// config file's "options" are. One this gnosh cannot use (a newer one wrote
// it, say) is left out with a warning: what gnosh wrote itself never stops
// it from starting.
func (ld *loader) stateOptions(path string, chosen map[string]json.RawMessage) {
	for _, key := range slices.Sorted(maps.Keys(chosen)) {
		src := ld.source(path, LayerState, "user", filepath.Dir(path), "")
		src.probs, src.noLines = &problems{}, true
		ops, err := optionOps(key, chosen[key], src)
		if err != nil {
			ld.probs.warnf(path, 0, "the option %s set here with /config was not used: %s", showName(key), err)
			continue
		}
		for _, o := range ops {
			ld.b.run(o)
		}
	}
}

// CheckWrite checks what the configuration would be if the file at path
// held content, without writing anything. A command of gnosh's own that
// changes a config file calls it first and refuses on the spot when it
// reports a mistake, so that no command can write a file gnosh would then
// refuse to load. It is Load itself, reading content in place of the file.
func CheckWrite(in Inputs, path string, content []byte) ([]Problem, error) {
	if in.FS == nil {
		return nil, errors.New("config: CheckWrite was given no file system")
	}
	in.FS = overlay{FS: in.FS, path: filepath.Clean(path), data: content}
	_, problems, err := Load(in)
	return problems, err
}

// overlay is a file system in which one file holds other bytes.
type overlay struct {
	FS
	path string
	data []byte
}

// ReadFile gives the stand-in bytes for the one file, and reads any other.
func (o overlay) ReadFile(name string) ([]byte, error) {
	if filepath.Clean(name) == o.path {
		return o.data, nil
	}
	return o.FS.ReadFile(name)
}

// Stat describes the one file as an ordinary file of the stand-in size.
func (o overlay) Stat(name string) (fs.FileInfo, error) {
	if filepath.Clean(name) == o.path {
		return overlayInfo{name: filepath.Base(name), size: int64(len(o.data))}, nil
	}
	return o.FS.Stat(name)
}

// Owner is the owner of the one file when it exists, and otherwise of the
// folder it would be written in, which is who would own it once written.
func (o overlay) Owner(name string) (int, bool) {
	if filepath.Clean(name) == o.path {
		if uid, ok := o.FS.Owner(name); ok {
			return uid, true
		}
		return o.FS.Owner(filepath.Dir(name))
	}
	return o.FS.Owner(name)
}

// overlayInfo describes the file an overlay stands in for.
type overlayInfo struct {
	name string
	size int64
}

// The methods of fs.FileInfo.
func (i overlayInfo) Name() string       { return i.name }
func (i overlayInfo) Size() int64        { return i.size }
func (i overlayInfo) Mode() fs.FileMode  { return 0o600 }
func (i overlayInfo) ModTime() time.Time { return time.Time{} }
func (i overlayInfo) IsDir() bool        { return false }
func (i overlayInfo) Sys() any           { return nil }
