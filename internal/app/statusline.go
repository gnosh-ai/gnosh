package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
)

// This file runs the status line command a configuration names (Claude
// Code's statusLine, spec/cc/interactive.md section 5) and hands the front
// end what it prints, which takes the place of gnosh's own status line.
//
// The command is run by sh with a JSON object about the session on its
// standard input, and COLUMNS and LINES set to the terminal's size. It runs
// when the session opens and again when a reply comes in, when room has
// been made in the model's window, when the permission mode changes, and
// every refreshInterval, if one is given. Runs are put off by 300 ms, so
// that a burst of moments makes one run, and a run still going when the
// next one is due is stopped. It is a program to run, so a project's waits
// for trust, and safe mode and disableAllHooks turn it off, as they do
// hooks (config.StatusLine).

// statusDebounce is how long a run waits for more moments, and
// statusTimeout how long one may take.
const (
	statusDebounce = 300 * time.Millisecond
	statusTimeout  = 10 * time.Second
	statusMaxBytes = 64 << 10
	statusMaxLines = 8
)

// Sizer is a front end that knows the terminal's size, for COLUMNS and
// LINES.
type Sizer interface {
	Size() (cols, rows int)
}

// StatusLineEvent carries what the status line command printed: the lines
// a front end shows in place of its own status line, set in by Padding
// columns. It is sent after every run that finished.
type StatusLineEvent struct {
	Lines   []string
	Padding int
}

// statusLiner runs the session's status line command.
type statusLiner struct {
	l *live

	mu      sync.Mutex
	timer   *time.Timer
	cancel  context.CancelFunc
	ticker  *time.Ticker
	ended   chan struct{} // closed by stop, which ends the ticker's goroutine
	stopped bool
	runs    sync.WaitGroup
}

// startStatusLine starts the status line of an interactive session, when
// one is configured and in force.
func (l *live) startStatusLine() {
	sl := l.cfg.StatusLine
	if sl == nil || !l.interactive {
		return
	}
	if l.gitRunsPrograms() {
		// The person's command may well call git, and git would run what
		// the repository's own settings name: gnosh's own git status is
		// held there for that (the config review of 2026-11-19).
		l.notice(agent.LevelInfo, "statusline", "Your status line command is not run in this folder: the repository's own git settings name programs that git would run, and the folder is not trusted. gnosh trust trusts it.")
		return
	}
	s := &statusLiner{l: l, ended: make(chan struct{})}
	l.statusLine = s
	if sl.Refresh > 0 {
		s.ticker = time.NewTicker(sl.Refresh)
		go func(t *time.Ticker) {
			// A stopped ticker's channel is never closed: ended is what
			// lets this go.
			for {
				select {
				case <-t.C:
					s.poke()
				case <-s.ended:
					return
				}
			}
		}(s.ticker)
	}
	s.poke()
}

// poke asks for a run, statusDebounce from now.
func (s *statusLiner) poke() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(statusDebounce, s.run)
}

// stop ends the status line: no run starts after it, and the one going is
// stopped and waited for.
func (s *statusLiner) stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.stopped {
		close(s.ended)
	}
	s.stopped = true
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.ticker != nil {
		s.ticker.Stop()
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.runs.Wait()
}

// run runs the command once, and hands the front end what it printed,
// unless a later run took its place.
func (s *statusLiner) run() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	ctx, cancel := context.WithTimeout(s.l.ctx, statusTimeout)
	s.cancel = cancel
	s.runs.Add(1)
	s.mu.Unlock()
	defer s.runs.Done()
	defer cancel()

	l := s.l
	sl := l.cfg.StatusLine
	input, err := json.Marshal(l.statusInput())
	if err != nil {
		return
	}
	cols, rows := 80, 24
	if sz, ok := l.front.(Sizer); ok {
		if c, r := sz.Size(); c > 0 && r > 0 {
			cols, rows = c, r
		}
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", sl.Command)
	cmd.Dir = l.cwd()
	cmd.Env = append(l.ownCommandEnv(), fmt.Sprintf("COLUMNS=%d", max(cols-sl.Padding, 1)), fmt.Sprintf("LINES=%d", rows))
	cmd.Stdin = bytes.NewReader(input)
	var out capBuffer
	out.max = statusMaxBytes
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		// A later run took its place, or it took too long; the session may
		// be closing. Nothing it printed is shown.
		if ctx.Err() == context.DeadlineExceeded {
			l.flog.logf("statusline", "the status line command took more than %s and was stopped", statusTimeout)
		}
		return
	}
	if runErr != nil {
		l.flog.logf("statusline", "the status line command failed: %v", runErr)
	}
	l.deliver(&StatusLineEvent{Lines: statusLines(out.String()), Padding: sl.Padding})
}

// statusLines are the lines a status line command printed, as many as are
// shown: blank ones are left out, as gnosh shows no empty lines.
func statusLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(stripSGR(line)) == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) == statusMaxLines {
			break
		}
	}
	return lines
}

// stripSGR takes colour sequences out of a line, to see whether anything
// else is in it.
func stripSGR(s string) string {
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], 'm')
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}

// capBuffer keeps the first max bytes written to it.
type capBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (c *capBuffer) String() string { return c.buf.String() }

// statusInput is the JSON object a status line command reads: the fields
// of Claude Code's that gnosh has (spec/cc/interactive.md section 5).
type statusInput struct {
	Cwd            string `json:"cwd"`
	SessionID      string `json:"session_id"`
	SessionName    string `json:"session_name,omitempty"`
	TranscriptPath string `json:"transcript_path"`
	Model          struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir  string      `json:"current_dir"`
		ProjectDir  string      `json:"project_dir"`
		AddedDirs   []string    `json:"added_dirs"`
		GitWorktree string      `json:"git_worktree,omitempty"`
		Repo        *statusRepo `json:"repo,omitempty"`
	} `json:"workspace"`
	// PromptID is the id of the last prompt the person sent.
	PromptID    string      `json:"prompt_id,omitempty"`
	Worktree    *statusTree `json:"worktree,omitempty"`
	Version     string      `json:"version"`
	OutputStyle struct {
		Name string `json:"name"`
	} `json:"output_style"`
	Cost struct {
		TotalCostUSD       float64 `json:"total_cost_usd"`
		TotalDurationMS    int64   `json:"total_duration_ms"`
		TotalAPIDurationMS int64   `json:"total_api_duration_ms"`
		TotalLinesAdded    int     `json:"total_lines_added"`
		TotalLinesRemoved  int     `json:"total_lines_removed"`
	} `json:"cost"`
	ContextWindow struct {
		TotalInputTokens    int           `json:"total_input_tokens"`
		TotalOutputTokens   int           `json:"total_output_tokens"`
		ContextWindowSize   int           `json:"context_window_size"`
		UsedPercentage      *int          `json:"used_percentage"`
		RemainingPercentage *int          `json:"remaining_percentage"`
		CurrentUsage        *statusTokens `json:"current_usage"`
	} `json:"context_window"`
	Exceeds200k bool `json:"exceeds_200k_tokens"`
	Thinking    *struct {
		Enabled bool `json:"enabled"`
	} `json:"thinking,omitempty"`
	// Effort is the thinking level of a model that has levels beyond on
	// and off.
	Effort *struct {
		Level string `json:"level"`
	} `json:"effort,omitempty"`
	// Agent is the kind of agent the session runs as (--agent).
	Agent *struct {
		Name string `json:"name"`
	} `json:"agent,omitempty"`
	PermissionMode string `json:"permission_mode"`
	Vim            *struct {
		Mode string `json:"mode"`
	} `json:"vim,omitempty"`
	// PromptCache is how the model server's prompt cache served the main
	// requests, from the first reply on.
	PromptCache *statusCache `json:"prompt_cache,omitempty"`
}

// statusCache is Claude Code's prompt_cache object, in the fields gnosh
// can tell (agent.CacheTotals): a model server of the person's own has no
// cache lifetime, so ttl and expires_at are left out, as is why a miss
// was one.
type statusCache struct {
	Warm             bool     `json:"warm"`
	CachingObserved  bool     `json:"caching_observed"`
	Requests         int      `json:"requests"`
	Misses           int      `json:"misses"`
	ExpectedRebuilds int      `json:"expected_rebuilds"`
	HitRatio         *float64 `json:"hit_ratio"`
	CacheWriteTokens int      `json:"cache_write_tokens"`
	MissRecache      int      `json:"miss_recache_tokens"`
	LastMissAt       *int64   `json:"last_miss_at"`
}

// statusRepo is the repository the project is, as its origin remote names
// it: "github.com", "gnosh-ai", "gnosh".
type statusRepo struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

// statusTree is the git work tree a --worktree session works in.
type statusTree struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Branch         string `json:"branch"`
	OriginalCwd    string `json:"original_cwd"`
	OriginalBranch string `json:"original_branch,omitempty"`
}

// statusTokens is the token count of the most recent request.
type statusTokens struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// statusInput gathers the session's facts for the status line command.
func (l *live) statusInput() statusInput {
	var in statusInput
	in.Cwd = l.cwd()
	in.SessionID = l.ts.ID()
	if name, _ := l.title(); name != "" {
		in.SessionName = name
	}
	in.TranscriptPath = l.ts.Path()
	if ref, ok := l.modelRef(); ok {
		in.Model.ID = ref.String()
		in.Model.DisplayName = ref.ID
	}
	in.Workspace.CurrentDir = in.Cwd
	in.Workspace.ProjectDir = l.cfg.Paths.Cwd
	in.Workspace.AddedDirs = []string{}
	for _, r := range l.roots {
		if r != in.Cwd && r != l.cfg.Paths.Cwd {
			in.Workspace.AddedDirs = append(in.Workspace.AddedDirs, r)
		}
	}
	if t := l.st.tree; t != nil {
		in.Workspace.GitWorktree = t.name
		in.Worktree = &statusTree{Name: t.name, Path: t.path, Branch: t.branch, OriginalCwd: t.from, OriginalBranch: t.fromBranch}
	}
	if host, owner, name, ok := originRepo(l.cfg.Paths.ProjectRoot); ok {
		in.Workspace.Repo = &statusRepo{Host: host, Owner: owner, Name: name}
	}
	in.PromptID = l.lastPrompt()
	in.Version = firstOf(l.p.o.BuildID, "devel")
	in.OutputStyle.Name = l.styleNow()
	in.Cost.TotalDurationMS = l.p.now().Sub(l.p.start).Milliseconds()
	in.PermissionMode = string(l.engine.Mode())
	if l.asAgent != nil {
		in.Agent = &struct {
			Name string `json:"name"`
		}{Name: l.asAgent.Name}
	}
	if mode := l.vimNow(); mode != "" {
		in.Vim = &struct {
			Mode string `json:"mode"`
		}{Mode: mode}
	}
	if l.agent == nil {
		return in
	}
	u := l.agent.Usage()
	in.Cost.TotalAPIDurationMS = u.APITime.Milliseconds()
	in.ContextWindow.TotalInputTokens = u.Main.PromptTokens()
	in.ContextWindow.TotalOutputTokens = u.Main.OutputTokens
	cs := l.agent.Context()
	if cs.Window == 0 {
		// Before the first request: the window the model is configured
		// with, and nothing used yet.
		cs.Window = l.agent.Model().Info.ContextWindow
	}
	if cs.Window > 0 {
		in.ContextWindow.ContextWindowSize = cs.Window
		used := min(cs.Used*100/cs.Window, 100)
		left := 100 - used
		in.ContextWindow.UsedPercentage, in.ContextWindow.RemainingPercentage = &used, &left
		in.Exceeds200k = cs.Used > 200_000
	}
	if u.Requests > 0 {
		last := u.Last
		in.ContextWindow.CurrentUsage = &statusTokens{InputTokens: last.InputTokens, OutputTokens: last.OutputTokens,
			CacheCreationInputTokens: last.CacheWriteTokens, CacheReadInputTokens: last.CacheReadTokens}
		c := u.Cache
		pc := &statusCache{Warm: c.Warm, CachingObserved: c.Observed, Requests: u.Requests, Misses: c.Misses,
			ExpectedRebuilds: c.Rebuilds, CacheWriteTokens: u.Main.CacheWriteTokens, MissRecache: c.MissTokens}
		if all := u.Main.PromptTokens(); all > 0 {
			r := float64(u.Main.CacheReadTokens) / float64(all)
			pc.HitRatio = &r
		}
		if !c.LastMiss.IsZero() {
			at := c.LastMiss.Unix()
			pc.LastMissAt = &at
		}
		in.PromptCache = pc
	}
	if lvl := l.agent.Model().Level; lvl != "" {
		in.Thinking = &struct {
			Enabled bool `json:"enabled"`
		}{Enabled: lvl != "off"}
		if lvl != "on" && lvl != "off" {
			in.Effort = &struct {
				Level string `json:"level"`
			}{Level: lvl}
		}
	}
	return in
}

// setVimMode notes the mode of the box's vim keys, and has the status line
// run again when it changed.
func (l *live) setVimMode(mode string) {
	l.vimMu.Lock()
	changed := l.vimMode != mode
	l.vimMode = mode
	l.vimMu.Unlock()
	if changed {
		l.statusLine.poke()
	}
}

// vimNow is the mode of the box's vim keys, "" when they are off.
func (l *live) vimNow() string {
	l.vimMu.Lock()
	defer l.vimMu.Unlock()
	return l.vimMode
}

// statusPoke is the moments the status line runs at, among the events the
// front end is told of.
func (l *live) statusPoke(ev any) {
	switch ev := ev.(type) {
	case *agent.ReplyEvent, *agent.RoomEvent, *agent.SummaryEvent, *agent.TurnEndEvent:
		l.statusLine.poke()
	case *agent.PromptEvent:
		if !ev.Prompt.Hidden {
			l.promptMu.Lock()
			l.promptID = ev.ID
			l.promptMu.Unlock()
			l.statusLine.poke()
		}
	}
}

// lastPrompt is the id of the last prompt the person sent, "" before one.
func (l *live) lastPrompt() string {
	l.promptMu.Lock()
	defer l.promptMu.Unlock()
	return l.promptID
}

// slashStatusLine is /statusline: the model writes a status line command
// from the person's words (or a plain one), tries it on the session's own
// JSON, and adds the setting to the person's gnosh.json with the Edit
// tool, which asks as any edit does. gnosh itself writes no gnosh.json.
func (l *live) slashStatusLine(ctx context.Context, c *SlashContext) error {
	wish := strings.TrimSpace(c.Args)
	if wish == "" {
		wish = "the model, the folder, and how full the context window is"
	}
	sample, err := json.MarshalIndent(l.statusInput(), "", "  ")
	if err != nil {
		return err
	}
	script := filepath.Join(l.cfg.Paths.ConfigDir, "statusline.sh")
	settings := filepath.Join(l.cfg.Paths.ConfigDir, "gnosh.json")
	text := l.texts.StatusLine(wish, script, settings, "```json\n"+string(sample)+"\n```")
	return c.Send(agent.Prompt{Hidden: true, Source: "statusline", Text: text, Notice: "The model is asked to write a status line: " + oneLine(wish, 200)})
}
