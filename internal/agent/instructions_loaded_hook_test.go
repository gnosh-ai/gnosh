package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/stretchr/testify/require"
)

// instructionsLoadedHooks returns the given commands as InstructionsLoaded
// hook configs with the given matcher.
func instructionsLoadedHooks(matcher string, commands ...string) []config.HookConfig {
	cfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		cfgs[i] = config.HookConfig{Command: cmd, Matcher: matcher}
	}
	return cfgs
}

// TestInstructionsLoadedConfigLoads checks that a config with an
// InstructionsLoaded hook, with or without a matcher, passes
// ValidateHooks, and that the matcher is tested against load_reason.
func TestInstructionsLoadedConfigLoads(t *testing.T) {
	t.Parallel()

	// With a matcher.
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{
		hooks.EventInstructionsLoaded: {{Command: "true", Matcher: "^session_start$"}},
	}}
	require.NoError(t, cfg.ValidateHooks(), "a matcher on InstructionsLoaded must be accepted")

	// Without a matcher.
	cfg2 := &config.Config{Hooks: map[string][]config.HookConfig{
		hooks.EventInstructionsLoaded: {{Command: "true"}},
	}}
	require.NoError(t, cfg2.ValidateHooks(), "no matcher must also be accepted")

	// The event is known.
	require.True(t, config.IsKnownHookEvent(hooks.EventInstructionsLoaded))
}

// TestInstructionsLoadedMatcherOnTheReason checks that a hook matched on
// "session_start" fires only for that reason, and a hook with no matcher
// fires for every reason.
func TestInstructionsLoadedMatcherOnTheReason(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		matcher string
		reason  string
		want    int
	}{
		{"^session_start$", hooks.InstructionsLoadedSessionStart, 1},
		{"^session_start$", hooks.InstructionsLoadedCompact, 0},
		{"^session_start$", hooks.InstructionsLoadedNestedTraversal, 0},
		{"^compact$", hooks.InstructionsLoadedSessionStart, 0},
		{"^compact$", hooks.InstructionsLoadedCompact, 1},
		{"^nested_traversal$", hooks.InstructionsLoadedSessionStart, 0},
		{"^nested_traversal$", hooks.InstructionsLoadedNestedTraversal, 1},
		{"", hooks.InstructionsLoadedSessionStart, 1},
		{"", hooks.InstructionsLoadedCompact, 1},
		{"", hooks.InstructionsLoadedNestedTraversal, 1},
		{"", hooks.InstructionsLoadedInclude, 1},
	} {
		t.Run(tc.matcher+"_"+tc.reason, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{Hooks: map[string][]config.HookConfig{
				hooks.EventInstructionsLoaded: {{Command: "true", Matcher: tc.matcher}},
			}}
			require.NoError(t, cfg.ValidateHooks())
			runner := hooks.NewRunner(cfg.Hooks[hooks.EventInstructionsLoaded], t.TempDir(), t.TempDir())
			agg, err := runner.RunInstructionsLoaded(t.Context(), "s", "/tmp/f", "Project", tc.reason, "", "")
			require.NoError(t, err)
			require.Equal(t, tc.want, agg.HookCount,
				"the hook fires (or not) based on whether the load reason matches")
		})
	}
}

// instructionsLoadedAgent builds a sessionAgent with the given
// InstructionsLoaded hooks configured, through ValidateHooks, and a
// working directory with a project AGENTS.md.
func instructionsLoadedAgent(t *testing.T, hookCfgs ...config.HookConfig) (*sessionAgent, string) {
	t.Helper()
	env := testEnv(t)
	wd := env.workingDir
	// Write a project context file.
	require.NoError(t, os.WriteFile(filepath.Join(wd, "AGENTS.md"), []byte("project rules"), 0o644))
	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: hookCfgs},
		Options: &config.Options{ContextPaths: []string{"AGENTS.md"}},
	}
	require.NoError(t, cfg.ValidateHooks())
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:   Model{},
		SmallModel:   Model{},
		SystemPrompt: "test",
		IsYolo:       true,
		Sessions:     env.sessions,
		Messages:     env.messages,
		Cfg:          config.NewTestStoreIn(cfg, wd),
	}).(*sessionAgent)
	return a, wd
}

// TestInstructionsLoadedFiresOnSessionStart checks that when a session
// starts (the agent's Run is called), the InstructionsLoaded hooks fire
// once for each context file in the system prompt with load_reason
// "session_start", the correct file_path, memory_type, and
// hook_event_name.
func TestInstructionsLoadedFiresOnSessionStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	a, wd := instructionsLoadedAgent(t,
		instructionsLoadedHooks("", `cat > `+payloadPath)...)

	// Fire the hooks directly (the goroutine in fireInstructionsLoadedForSession
	// is hard to await in a test, so call the runner through the same path).
	files := sessionInstructionsFiles(a.cfg)
	require.NotEmpty(t, files, "the project has a context file")

	runner := hooks.NewRunner(a.cfg.Config().Hooks[hooks.EventInstructionsLoaded], wd, wd)
	agg, err := runner.RunInstructionsLoaded(t.Context(), "sess-1", files[0].path,
		memoryTypeFor(files[0].path, files[0].global), hooks.InstructionsLoadedSessionStart, "", "")
	require.NoError(t, err)
	require.Equal(t, 1, agg.HookCount, "the hook ran")

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "InstructionsLoaded", payload["hook_event_name"])
	require.Equal(t, "InstructionsLoaded", payload["event"])
	require.Equal(t, "sess-1", payload["session_id"])
	require.Equal(t, wd, payload["cwd"])
	require.Equal(t, hooks.InstructionsLoadedSessionStart, payload["load_reason"])
	require.Equal(t, wd, payload["cwd"])
	// file_path is absolute.
	fp, _ := payload["file_path"].(string)
	require.True(t, filepath.IsAbs(fp), "file_path must be absolute, got %q", fp)
	// memory_type is "Project" for a project context file.
	require.Equal(t, hooks.MemoryTypeProject, payload["memory_type"])
	// No trigger_file_path or parent_file_path for a session_start load.
	_, hasTrigger := payload["trigger_file_path"]
	require.False(t, hasTrigger, "no trigger_file_path for session_start")
	_, hasParent := payload["parent_file_path"]
	require.False(t, hasParent, "no parent_file_path for session_start")
	// No globs.
	_, hasGlobs := payload["globs"]
	require.False(t, hasGlobs, "no globs in the payload")
}

// TestInstructionsLoadedCompactReason checks the real path: the first
// prompt fires the hooks with "session_start", the second none, and the
// first after a summary "compact".
func TestInstructionsLoadedCompactReason(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "reasons")
	env := testEnv(t)
	wd := env.workingDir
	require.NoError(t, os.WriteFile(filepath.Join(wd, "AGENTS.md"), []byte("project rules"), 0o644))
	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `echo "$KLASH_INSTRUCTIONS_LOAD_REASON" >> `+out)},
		Options: &config.Options{ContextPaths: []string{"AGENTS.md"}},
	}
	require.NoError(t, cfg.ValidateHooks())
	model := Model{
		Model:      &promptRecordingModel{},
		CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000},
		ModelCfg:   config.SelectedModel{Model: "test-model", Provider: "test"},
	}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  config.NewTestStoreIn(cfg, wd),
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "compact")
	require.NoError(t, err)
	reasons := func() string {
		data, _ := os.ReadFile(out)
		return string(data)
	}

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "first"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return reasons() == "session_start\n" }, 3*time.Second, 20*time.Millisecond, "after the first prompt")
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "second"})
	require.NoError(t, err)

	sumModel := Model{Model: &subSummarizeModel{}, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}, ModelCfg: config.SelectedModel{Model: "test-model", Provider: "test"}}
	a.SetModels(sumModel, sumModel)
	require.NoError(t, a.Summarize(t.Context(), sess.ID, "", nil, nil, preCompactTriggerManual))
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "after summary"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return reasons() == "session_start\ncompact\n" }, 3*time.Second, 20*time.Millisecond,
		"once at the start, not on the second prompt, and again after the summary")
}

// TestInstructionsLoadedIncludeReason checks that a file imported by
// another context file fires with load_reason "include" and
// parent_file_path set to the importing file, through Run.
func TestInstructionsLoadedIncludeReason(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "payloads")
	env := testEnv(t)
	wd := env.workingDir
	require.NoError(t, os.WriteFile(filepath.Join(wd, "AGENTS.md"), []byte("@extra.md\nparent"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wd, "extra.md"), []byte("imported rules"), 0o644))
	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `cat >> `+out+`; echo >> `+out)},
		Options: &config.Options{ContextPaths: []string{"AGENTS.md"}},
	}
	require.NoError(t, cfg.ValidateHooks())
	model := Model{
		Model:      &promptRecordingModel{},
		CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000},
		ModelCfg:   config.SelectedModel{Model: "test-model", Provider: "test"},
	}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  config.NewTestStoreIn(cfg, wd),
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "include")
	require.NoError(t, err)
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hello"})
	require.NoError(t, err)

	// One payload per file: the import and the file that imports it.
	var payloads []map[string]any
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(out)
		payloads = nil
		for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			var p map[string]any
			if json.Unmarshal([]byte(line), &p) == nil {
				payloads = append(payloads, p)
			}
		}
		return len(payloads) == 2
	}, 3*time.Second, 20*time.Millisecond, "a payload for each of the two files")
	byFile := map[string]map[string]any{}
	for _, p := range payloads {
		byFile[p["file_path"].(string)] = p
	}
	imported := byFile[filepath.Join(wd, "extra.md")]
	require.NotNil(t, imported, "the imported file fired")
	require.Equal(t, hooks.InstructionsLoadedInclude, imported["load_reason"])
	require.Equal(t, filepath.Join(wd, "AGENTS.md"), imported["parent_file_path"], "parent_file_path names the importing file")
	parent := byFile[filepath.Join(wd, "AGENTS.md")]
	require.NotNil(t, parent, "the importing file fired")
	require.Equal(t, hooks.InstructionsLoadedSessionStart, parent["load_reason"])
	_, hasParent := parent["parent_file_path"]
	require.False(t, hasParent)
}

// TestInstructionsLoadedNestedTraversal checks that a subdirectory
// context file gets load_reason "nested_traversal" and
// trigger_file_path set to the file the tool touched.
func TestInstructionsLoadedNestedTraversal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	env := testEnv(t)
	wd := env.workingDir

	sub := filepath.Join(wd, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("sub rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "a.go"), []byte("package main"), 0o644))

	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `cat > `+payloadPath)},
		Options: &config.Options{ContextPaths: []string{"AGENTS.md"}},
	}
	require.NoError(t, cfg.ValidateHooks())

	// A tool touching sub/a.go gives the session sub/AGENTS.md, which
	// fires the hooks.
	c := &coordinator{
		cfg:               config.NewTestStoreIn(cfg, wd),
		sessions:          env.sessions,
		subdirectoryGiven: map[string]bool{},
	}
	sess, err := env.sessions.Create(t.Context(), "nested")
	require.NoError(t, err)

	triggerFile := filepath.Join(sub, "a.go")
	require.Contains(t, c.subdirectoryContext(t.Context(), sess.ID, triggerFile), "sub rules")

	// Wait for the goroutine to finish (poll for the file).
	require.Eventually(t, func() bool {
		_, err := os.Stat(payloadPath)
		return err == nil
	}, 2*time.Second, 10*time.Millisecond, "the hook must have written the payload")

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, hooks.InstructionsLoadedNestedTraversal, payload["load_reason"])
	require.Equal(t, filepath.Join(sub, "AGENTS.md"), payload["file_path"])
	require.Equal(t, triggerFile, payload["trigger_file_path"],
		"trigger_file_path must name the file the tool touched")
	require.Equal(t, hooks.MemoryTypeProject, payload["memory_type"])
}

// TestInstructionsLoadedFiresThroughRun checks that when a session's
// first prompt goes through agent.Run, the InstructionsLoaded hooks fire
// once for the context files in the system prompt with load_reason
// "session_start". This is the end-to-end path: the hooks run
// asynchronously, so the test waits for the payload file.
func TestInstructionsLoadedFiresThroughRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	env := testEnv(t)
	wd := env.workingDir

	// Write a project context file so there is something to fire for.
	require.NoError(t, os.WriteFile(filepath.Join(wd, "AGENTS.md"), []byte("project rules"), 0o644))

	cfg := &config.Config{
		Hooks: map[string][]config.HookConfig{
			hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `cat > `+payloadPath),
		},
		Options: &config.Options{ContextPaths: []string{"AGENTS.md"}},
	}
	require.NoError(t, cfg.ValidateHooks())

	llm := &promptRecordingModel{}
	model := Model{
		Model:      llm,
		CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000},
		ModelCfg:   config.SelectedModel{Model: "test-model", Provider: "test"},
	}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsSubAgent:           false,
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  config.NewTestStoreIn(cfg, wd),
	}).(*sessionAgent)

	sess, err := env.sessions.Create(t.Context(), "instructions loaded run")
	require.NoError(t, err)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hello"})
	require.NoError(t, err)

	// The hooks run asynchronously; wait for the payload file to be
	// written. The file is created (truncated) by `cat >` before the
	// data is written, so check for non-empty content.
	var data []byte
	require.Eventually(t, func() bool {
		d, err := os.ReadFile(payloadPath)
		if err != nil || len(d) == 0 {
			return false
		}
		data = d
		return true
	}, 3*time.Second, 20*time.Millisecond, "the InstructionsLoaded hook must have written the payload")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "InstructionsLoaded", payload["hook_event_name"])
	require.Equal(t, sess.ID, payload["session_id"])
	require.Equal(t, hooks.InstructionsLoadedSessionStart, payload["load_reason"],
		"the first prompt to a new session is a session_start")
	fp, _ := payload["file_path"].(string)
	require.True(t, filepath.IsAbs(fp), "file_path must be absolute")
	require.Equal(t, hooks.MemoryTypeProject, payload["memory_type"])
}

// TestInstructionsLoadedFailingHookDoesNotBlock checks that a hook that
// exits with a non-zero code does not change the turn: the context files
// are still returned, and the error is logged but not propagated.
func TestInstructionsLoadedFailingHookDoesNotBlock(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	wd := env.workingDir
	require.NoError(t, os.WriteFile(filepath.Join(wd, "AGENTS.md"), []byte("rules"), 0o644))

	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `exit 1`)},
		Options: &config.Options{ContextPaths: []string{"AGENTS.md"}},
	}
	require.NoError(t, cfg.ValidateHooks())

	c := &coordinator{
		cfg:               config.NewTestStoreIn(cfg, wd),
		sessions:          env.sessions,
		subdirectoryGiven: map[string]bool{},
	}

	// The subdirectory context is still returned even though the hook fails.
	sub := filepath.Join(wd, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("sub rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "a.go"), []byte("package main"), 0o644))

	out := c.subdirectoryContext(context.Background(), "sess-fail", filepath.Join(sub, "a.go"))
	require.Contains(t, out, "sub rules", "the context files are still given even though the hook failed")
}

// TestInstructionsLoadedSlowHookDoesNotBlock checks that a slow hook
// (one that sleeps longer than the turn needs) does not hold up the
// subdirectory context: the context is returned immediately, before the
// hook finishes.
func TestInstructionsLoadedSlowHookDoesNotBlock(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	wd := env.workingDir

	sub := filepath.Join(wd, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("sub rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "a.go"), []byte("package main"), 0o644))

	// A hook that sleeps 300 ms; the subdirectoryContext call must return
	// well before that.
	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `sleep 0.3`)},
		Options: &config.Options{ContextPaths: []string{"AGENTS.md"}},
	}
	require.NoError(t, cfg.ValidateHooks())

	c := &coordinator{
		cfg:               config.NewTestStoreIn(cfg, wd),
		sessions:          env.sessions,
		subdirectoryGiven: map[string]bool{},
	}

	start := time.Now()
	out := c.subdirectoryContext(context.Background(), "sess-slow", filepath.Join(sub, "a.go"))
	elapsed := time.Since(start)

	require.Contains(t, out, "sub rules", "the context is returned even while the hook is still running")
	require.Less(t, elapsed, 250*time.Millisecond,
		"subdirectoryContext must not wait for the slow hook; took %s", elapsed)
}

// TestInstructionsLoadedEnvVar checks that the hook's environment has
// KLASH_INSTRUCTIONS_LOAD_REASON set to the load reason.
func TestInstructionsLoadedEnvVar(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	outPath := filepath.Join(dir, "reason")
	a, wd := instructionsLoadedAgent(t,
		instructionsLoadedHooks("", `echo "$KLASH_INSTRUCTIONS_LOAD_REASON" > `+outPath)...)

	files := sessionInstructionsFiles(a.cfg)
	require.NotEmpty(t, files)

	runner := hooks.NewRunner(a.cfg.Config().Hooks[hooks.EventInstructionsLoaded], wd, wd)
	_, err := runner.RunInstructionsLoaded(t.Context(), "s", files[0].path,
		memoryTypeFor(files[0].path, files[0].global), hooks.InstructionsLoadedSessionStart, "", "")
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	require.Equal(t, hooks.InstructionsLoadedSessionStart+"\n", string(data),
		"KLASH_INSTRUCTIONS_LOAD_REASON must be set to the load reason")
}

// TestInstructionsLoadedLocalMemoryType checks that a CLAUDE.local.md
// file gets memory_type "Local".
func TestInstructionsLoadedLocalMemoryType(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	env := testEnv(t)
	wd := env.workingDir

	require.NoError(t, os.WriteFile(filepath.Join(wd, "CLAUDE.local.md"), []byte("local rules"), 0o644))

	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `cat > `+payloadPath)},
		Options: &config.Options{ContextPaths: []string{"CLAUDE.local.md"}},
	}
	require.NoError(t, cfg.ValidateHooks())

	files := sessionInstructionsFiles(config.NewTestStoreIn(cfg, wd))
	require.NotEmpty(t, files)

	runner := hooks.NewRunner(cfg.Hooks[hooks.EventInstructionsLoaded], wd, wd)
	agg, err := runner.RunInstructionsLoaded(t.Context(), "s", files[0].path,
		memoryTypeFor(files[0].path, files[0].global), hooks.InstructionsLoadedSessionStart, "", "")
	require.NoError(t, err)
	require.Equal(t, 1, agg.HookCount)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, hooks.MemoryTypeLocal, payload["memory_type"],
		"a .local.md file must be memory_type Local")
}

// TestInstructionsLoadedUserMemoryType checks that a global context file
// gets memory_type "User".
func TestInstructionsLoadedUserMemoryType(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	globalDir := t.TempDir()
	env := testEnv(t)
	wd := env.workingDir

	// Write a global context file.
	globalFile := filepath.Join(globalDir, "KLASH.md")
	require.NoError(t, os.WriteFile(globalFile, []byte("global rules"), 0o644))

	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `cat > `+payloadPath)},
		Options: &config.Options{GlobalContextPaths: []string{globalFile}},
	}
	require.NoError(t, cfg.ValidateHooks())

	files := sessionInstructionsFiles(config.NewTestStoreIn(cfg, wd))
	require.NotEmpty(t, files)
	require.True(t, files[0].global, "the file must be marked as global")

	runner := hooks.NewRunner(cfg.Hooks[hooks.EventInstructionsLoaded], wd, wd)
	agg, err := runner.RunInstructionsLoaded(t.Context(), "s", files[0].path,
		memoryTypeFor(files[0].path, files[0].global), hooks.InstructionsLoadedSessionStart, "", "")
	require.NoError(t, err)
	require.Equal(t, 1, agg.HookCount)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, hooks.MemoryTypeUser, payload["memory_type"],
		"a global context file must be memory_type User")
}

// TestInstructionsLoadedFiresOncePerSession checks that the hooks fire
// once for the first prompt to a session and not again for a second
// prompt, as the "once" guarantee requires.
func TestInstructionsLoadedFiresOncePerSession(t *testing.T) {
	t.Parallel()

	a, _ := instructionsLoadedAgent(t)

	// First claim: fires with session_start.
	reason1, fire1 := a.instructionsLoaded.claim("sess-once")
	require.True(t, fire1, "the first claim fires")
	require.Equal(t, hooks.InstructionsLoadedSessionStart, reason1)

	// Second claim (no summary): does not fire.
	_, fire2 := a.instructionsLoaded.claim("sess-once")
	require.False(t, fire2, "the second claim does not fire again")

	// After a summary: fires with compact.
	a.instructionsLoaded.markSummarized("sess-once")
	reason3, fire3 := a.instructionsLoaded.claim("sess-once")
	require.True(t, fire3, "the claim after a summary fires")
	require.Equal(t, hooks.InstructionsLoadedCompact, reason3)

	// Another prompt after the compact fire: does not fire.
	_, fire4 := a.instructionsLoaded.claim("sess-once")
	require.False(t, fire4, "the claim after the compact fire does not fire again")
}

// TestInstructionsLoadedConcurrentClaims checks that two concurrent
// claims for the same session fire the hooks exactly once, not twice.
func TestInstructionsLoadedConcurrentClaims(t *testing.T) {
	t.Parallel()

	a, _ := instructionsLoadedAgent(t)

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	fired := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			reason, shouldFire := a.instructionsLoaded.claim("sess-race")
			if shouldFire {
				fired <- reason
			}
		}()
	}
	wg.Wait()
	close(fired)

	count := 0
	for reason := range fired {
		count++
		require.Equal(t, hooks.InstructionsLoadedSessionStart, reason,
			"the only fire must be session_start")
	}
	require.Equal(t, 1, count, "exactly one concurrent claim fires, not %d", count)
}

// TestInstructionsLoadedGlobalLocalFile checks that a global context
// file named .local.md gets memory_type "User", not "Local".
func TestInstructionsLoadedGlobalLocalFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	globalDir := t.TempDir()
	env := testEnv(t)
	wd := env.workingDir

	// A global context file with a .local.md name.
	globalFile := filepath.Join(globalDir, "KLASH.local.md")
	require.NoError(t, os.WriteFile(globalFile, []byte("global local rules"), 0o644))

	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `cat > `+payloadPath)},
		Options: &config.Options{GlobalContextPaths: []string{globalFile}},
	}
	require.NoError(t, cfg.ValidateHooks())

	files := sessionInstructionsFiles(config.NewTestStoreIn(cfg, wd))
	require.NotEmpty(t, files)
	require.True(t, files[0].global, "the file must be marked as global")

	runner := hooks.NewRunner(cfg.Hooks[hooks.EventInstructionsLoaded], wd, wd)
	agg, err := runner.RunInstructionsLoaded(t.Context(), "s", files[0].path,
		memoryTypeFor(files[0].path, files[0].global), hooks.InstructionsLoadedSessionStart, "", "")
	require.NoError(t, err)
	require.Equal(t, 1, agg.HookCount)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, hooks.MemoryTypeUser, payload["memory_type"],
		"a global .local.md file must be memory_type User, not Local")
}

// TestInstructionsLoadedSubdirectoryLocalFile checks that a .local.md
// file in a subdirectory gets memory_type "Local", not "Project".
func TestInstructionsLoadedSubdirectoryLocalFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	env := testEnv(t)
	wd := env.workingDir

	sub := filepath.Join(wd, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "CLAUDE.local.md"), []byte("sub local rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "a.go"), []byte("package main"), 0o644))

	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `cat > `+payloadPath)},
		Options: &config.Options{ContextPaths: []string{"CLAUDE.local.md"}},
	}
	require.NoError(t, cfg.ValidateHooks())

	c := &coordinator{
		cfg:               config.NewTestStoreIn(cfg, wd),
		sessions:          env.sessions,
		subdirectoryGiven: map[string]bool{},
	}

	sess, err := env.sessions.Create(t.Context(), "sub local")
	require.NoError(t, err)
	require.Contains(t, c.subdirectoryContext(t.Context(), sess.ID, filepath.Join(sub, "a.go")), "sub local rules")

	require.Eventually(t, func() bool {
		d, err := os.ReadFile(payloadPath)
		return err == nil && len(d) > 0
	}, 2*time.Second, 20*time.Millisecond, "the hook must have written the payload")

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, hooks.MemoryTypeLocal, payload["memory_type"],
		"a .local.md file in a subdirectory must be memory_type Local, not Project")
	require.Equal(t, hooks.InstructionsLoadedNestedTraversal, payload["load_reason"])
}

// TestInstructionsLoadedSubAgentSessionDoesNotFire checks that a
// sub-agent's session does not fire the InstructionsLoaded hooks when
// its tool touches a subdirectory file. The check is in
// subdirectoryContext, which looks up the session's ParentSessionID.
func TestInstructionsLoadedSubAgentSessionDoesNotFire(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	env := testEnv(t)
	wd := env.workingDir

	sub := filepath.Join(wd, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("sub rules"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "a.go"), []byte("package main"), 0o644))

	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{hooks.EventInstructionsLoaded: instructionsLoadedHooks("", `cat > `+payloadPath)},
		Options: &config.Options{ContextPaths: []string{"AGENTS.md"}},
	}
	require.NoError(t, cfg.ValidateHooks())

	c := &coordinator{
		cfg:               config.NewTestStoreIn(cfg, wd),
		sessions:          env.sessions,
		subdirectoryGiven: map[string]bool{},
	}

	// Create a sub-agent session (has a ParentSessionID).
	parent, err := env.sessions.Create(t.Context(), "parent")
	require.NoError(t, err)
	subSess, err := env.sessions.CreateTaskSession(t.Context(), "tool-call-1", parent.ID, "sub task")
	require.NoError(t, err)

	// Drive through subdirectoryContext, which checks ParentSessionID.
	out := c.subdirectoryContext(t.Context(), subSess.ID, filepath.Join(sub, "a.go"))
	require.Contains(t, out, "sub rules", "the context file is still given to the sub-agent")

	// The payload file should NOT be written: subdirectoryContext skips
	// the hook fire for a sub-agent session.
	time.Sleep(50 * time.Millisecond)
	_, err = os.Stat(payloadPath)
	require.True(t, os.IsNotExist(err), "the hooks must not fire for a sub-agent session")
}

// TestInstructionsLoadedSubAgentClaimDoesNotPollute checks that a
// sub-agent's fireInstructionsLoadedForSession does not claim the
// shared instructionsLoaded state, so the main agent's session_start
// fire is not skipped.
func TestInstructionsLoadedSubAgentClaimDoesNotPollute(t *testing.T) {
	t.Parallel()

	// A hook is configured, so only the sub-agent check stands between
	// the call and the claim.
	a, _ := instructionsLoadedAgent(t, instructionsLoadedHooks("", "true")...)

	// The main agent's call claims its session: the control.
	a.fireInstructionsLoadedForSession("main-sess")
	_, fire := a.instructionsLoaded.claim("main-sess")
	require.False(t, fire, "the main agent's call claimed its session")

	// Simulate a sub-agent: isSubAgent is true, so
	// fireInstructionsLoadedForSession should return before claiming.
	sub := &sessionAgent{
		isSubAgent:         true,
		instructionsLoaded: a.instructionsLoaded,
		cfg:                a.cfg,
	}
	sub.fireInstructionsLoadedForSession("sub-agent-sess")

	// The shared state should not have the sub-agent's session claimed:
	// a subsequent claim for the same ID should still fire.
	reason, fire := a.instructionsLoaded.claim("sub-agent-sess")
	require.True(t, fire, "the session ID is still claimable after a sub-agent's Run")
	require.Equal(t, hooks.InstructionsLoadedSessionStart, reason)
}
