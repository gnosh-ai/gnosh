package config

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// memFS is a file system kept in memory, so that no test reads the real
// home directory or anything else on the machine.
type memFS struct {
	mu     sync.Mutex
	files  map[string][]byte
	dirs   map[string]bool
	owners map[string]int
	broken map[string]error
}

// The places the tests use. They are made up.
const (
	testHome  = "/home/dev"
	testCwd   = "/work/proj"
	userDir   = "/home/dev/.config/gnosh"
	userJSON  = "/home/dev/.config/gnosh/gnosh.json"
	statePath = "/home/dev/.local/share/gnosh/state.json"
	projJSON  = "/work/proj/gnosh.json"
	localJSON = "/work/proj/.gnosh/settings.local.json"
	testUID   = 1000
)

// testTools stands for the one list of tool names the program has.
var testTools = []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob", "Agent", "WebFetch", "WebSearch", "TaskStop"}

func newFS() *memFS {
	m := &memFS{files: map[string][]byte{}, dirs: map[string]bool{"/": true}, owners: map[string]int{}, broken: map[string]error{}}
	m.mkdir(testHome)
	m.mkdir(testCwd)
	return m
}

func (m *memFS) mkdir(path string) {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		m.dirs[p] = true
		if p == filepath.Dir(p) {
			return
		}
	}
}

// put writes a file, making the directories above it.
func (m *memFS) put(path, content string) *memFS {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mkdir(filepath.Dir(path))
	m.files[filepath.Clean(path)] = []byte(content)
	return m
}

type memInfo struct {
	name string
	size int64
	dir  bool
}

func (i memInfo) Name() string { return i.name }
func (i memInfo) Size() int64  { return i.size }
func (i memInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return i.dir }
func (i memInfo) Sys() any           { return nil }

func (m *memFS) ReadFile(name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name = filepath.Clean(name)
	if err := m.broken[name]; err != nil {
		return nil, err
	}
	data, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), data...), nil
}

func (m *memFS) Stat(name string) (fs.FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name = filepath.Clean(name)
	if data, ok := m.files[name]; ok {
		return memInfo{name: filepath.Base(name), size: int64(len(data))}, nil
	}
	if m.dirs[name] {
		return memInfo{name: filepath.Base(name), dir: true}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

func (m *memFS) Owner(name string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name = filepath.Clean(name)
	if uid, ok := m.owners[name]; ok {
		return uid, true
	}
	return testUID, true
}

func (m *memFS) MkdirAll(name string, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mkdir(name)
	return nil
}

func (m *memFS) WriteFile(name string, data []byte, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.dirs[filepath.Dir(filepath.Clean(name))] {
		return &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	m.files[filepath.Clean(name)] = append([]byte(nil), data...)
	return nil
}

// inputs returns Inputs for a trusted folder in a program that has every
// part of gnosh, reading fsys.
func inputs(fsys *memFS) Inputs {
	return Inputs{
		FS:            fsys,
		Environ:       []string{"HOME=" + testHome, "PATH=/usr/bin"},
		Home:          testHome,
		Cwd:           testCwd,
		Trusted:       true,
		KeepUntrusted: restricts,
		Built:         Everything(),
		Tools:         testTools,
	}
}

// restricts is the trust package's rule, repeated here because this
// package imports nothing of gnosh's: of everything an untrusted folder's
// files can say, only what takes something away from the agent applies.
func restricts(kind string) bool {
	return kind == KindDenyRule || kind == KindAskRule || kind == KindHiddenSkill || kind == "disabled-tool" || kind == KindRestriction
}

// withConfig returns Inputs whose user gnosh.json holds text.
func withConfig(text string) Inputs {
	return inputs(newFS().put(userJSON, text))
}

// lineOf returns the line of a file's text on which a piece of it first
// stands, counted from 1, so that a test can name a line by what is on it.
func lineOf(t *testing.T, text, piece string) int {
	t.Helper()
	i := strings.Index(text, piece)
	if i < 0 {
		t.Fatalf("the text does not hold %q:\n%s", piece, text)
	}
	return 1 + strings.Count(text[:i], "\n")
}

// load runs Load and fails the test on anything but a refusal.
func load(t *testing.T, in Inputs) (*Config, []Problem) {
	t.Helper()
	cfg, problems, err := Load(in)
	var refused *LoadError
	if err != nil && !errors.As(err, &refused) {
		t.Fatalf("Load: %v", err)
	}
	if (err != nil) != (len(Errors(problems)) > 0) || (cfg == nil) != (err != nil) {
		t.Fatalf("Load returned cfg=%v err=%v with problems %v: a Config comes back exactly when there is no mistake", cfg != nil, err, problems)
	}
	for _, p := range problems {
		if strings.TrimSpace(p.Message) == "" {
			t.Errorf("a problem has no message: %+v", p)
		}
	}
	return cfg, problems
}

// mustLoad runs Load and fails the test if the configuration is refused.
func mustLoad(t *testing.T, in Inputs) *Config {
	t.Helper()
	cfg, problems := load(t, in)
	if cfg == nil {
		t.Fatalf("the configuration was refused:\n%s", showProblems(problems))
	}
	return cfg
}

func showProblems(problems []Problem) string {
	var b strings.Builder
	for _, p := range problems {
		b.WriteString("  " + p.Severity.String() + ": " + p.String() + "\n")
	}
	return b.String()
}

// wantProblem checks that a problem of the given severity is at file and
// line (0 for any line) and holds every one of the given pieces of text.
func wantProblem(t *testing.T, problems []Problem, sev Severity, file string, line int, pieces ...string) {
	t.Helper()
next:
	for _, p := range problems {
		if p.Severity != sev || p.File != file && p.Where != file || line != 0 && p.Line != line {
			continue
		}
		for _, piece := range pieces {
			if !strings.Contains(p.Message, piece) {
				continue next
			}
		}
		return
	}
	t.Errorf("no %s at %s:%d holding %q; got:\n%s", sev, file, line, pieces, showProblems(problems))
}

// refuse loads a user gnosh.json holding text and checks that it is
// refused, with a mistake on the line that holds at (or on any line, when
// at is empty) whose message holds every piece.
func refuse(t *testing.T, text, at string, pieces ...string) {
	t.Helper()
	cfg, problems := load(t, withConfig(text))
	if cfg != nil {
		t.Errorf("%s loaded; it should have been refused", text)
		return
	}
	line := 0
	if at != "" {
		line = lineOf(t, text, at)
	}
	wantProblem(t, problems, Error, userJSON, line, pieces...)
}
