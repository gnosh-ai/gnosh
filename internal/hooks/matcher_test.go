package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestMatcher(t *testing.T) {
	tests := []struct {
		pattern string
		narrow  bool
		fits    []string
		misses  []string
	}{
		// Empty and "*": everything, the empty name included.
		{pattern: "", fits: []string{"Bash", "mcp__github__create_issue", ""}},
		{pattern: "*", fits: []string{"Bash", "Edit", ""}},

		// One exact name.
		{pattern: "Bash", fits: []string{"Bash"}, misses: []string{"BashOutput", "bash", "MyBash", "", "Bash "}},
		{pattern: "general-purpose", fits: []string{"general-purpose"}, misses: []string{"general", "purpose"}},
		{pattern: "mcp__memory", fits: []string{"mcp__memory"}, misses: []string{"mcp__memory__create_entities"}},

		// A list of exact names, with either separator and with spaces.
		{pattern: "Edit|Write", fits: []string{"Edit", "Write"}, misses: []string{"NotebookEdit", "MultiEdit", "Edit|Write", ""}},
		{pattern: "Edit,Write", fits: []string{"Edit", "Write"}, misses: []string{"Read"}},
		{pattern: "Edit, Write", fits: []string{"Edit", "Write"}, misses: []string{" Write"}},
		{pattern: " Edit | Write | Edit ", fits: []string{"Edit", "Write"}, misses: []string{"Read"}},
		{pattern: "Edit||Write|", fits: []string{"Edit", "Write"}, misses: []string{""}},
		{pattern: "Explore|Plan", fits: []string{"Explore", "Plan"}, misses: []string{"", "explore"}},

		// Anything else is a regular expression that may match anywhere.
		{pattern: "Edit.*", fits: []string{"Edit", "NotebookEdit", "MultiEdit", "Editor"}, misses: []string{"Write"}},
		{pattern: "^Edit$", fits: []string{"Edit"}, misses: []string{"NotebookEdit", "Editor"}},
		{pattern: "^Notebook", fits: []string{"NotebookEdit"}, misses: []string{"Edit", "MyNotebook"}},
		{pattern: "mcp__memory__.*", fits: []string{"mcp__memory__create_entities"}, misses: []string{"mcp__memory", "mcp__github__create_issue"}},
		{pattern: "mcp__.*__write.*", fits: []string{"mcp__fs__write_file", "mcp__db__write"}, misses: []string{"mcp__fs__read_file"}},
		{pattern: "mcp__github__(create|update)_issue", fits: []string{"mcp__github__create_issue", "mcp__github__update_issue"}, misses: []string{"mcp__github__close_issue"}},
		{pattern: "Bash|Edit.*", fits: []string{"Bash", "BashOutput", "NotebookEdit"}, misses: []string{"Read"}},
		{pattern: ".", fits: []string{"Bash"}, misses: []string{""}},
		{pattern: ".*", fits: []string{"Bash", ""}},
		{pattern: "(?i)bash", fits: []string{"Bash", "BASH"}, misses: []string{"Edit"}},

		// The narrower list of the one event matched on error kinds: "-",
		// spaces and "," make a regular expression there, and only "|"
		// separates.
		{pattern: "rate_limit|overloaded", narrow: true, fits: []string{"rate_limit", "overloaded"}, misses: []string{"server_error", "rate"}},
		{pattern: "rate_limit,overloaded", narrow: true, fits: []string{"rate_limit,overloaded"}, misses: []string{"rate_limit", "overloaded"}},
		{pattern: "server_error", narrow: true, fits: []string{"server_error"}, misses: []string{"server_error_x", "x_server_error"}},
		{pattern: "server-error", narrow: true, fits: []string{"a server-error"}, misses: []string{"server_error"}},
	}
	for _, tt := range tests {
		m, err := compileMatcher(tt.pattern, tt.narrow)
		if err != nil {
			t.Errorf("compileMatcher(%q): %v", tt.pattern, err)
			continue
		}
		for _, name := range tt.fits {
			if !m.matches(name) {
				t.Errorf("matcher %q does not fit %q", tt.pattern, name)
			}
		}
		for _, name := range tt.misses {
			if m.matches(name) {
				t.Errorf("matcher %q fits %q", tt.pattern, name)
			}
		}
	}
}

func TestMatcherRefuses(t *testing.T) {
	tests := []struct {
		pattern string
		narrow  bool
		want    string
	}{
		{"|", false, "names nothing"},
		{",", false, "names nothing"},
		{" ", false, "names nothing"},
		{" | , ", false, "names nothing"},
		{"||", true, "names nothing"},
		{"Bash(", false, "cannot be read as a regular expression"},
		{"[a-", false, "cannot be read as a regular expression"},
		{"*Edit", false, "cannot be read as a regular expression"},
		{"Bash|*", false, "cannot be read as a regular expression"},
		{"(?=Bash)", false, "look-ahead"},
		{"(?<!Not)Edit", false, "look-behind"},
		{`(\w+)\1`, false, "backreferences"},
	}
	for _, tt := range tests {
		_, err := compileMatcher(tt.pattern, tt.narrow)
		if err == nil {
			t.Errorf("compileMatcher(%q) accepted it", tt.pattern)
			continue
		}
		contains(t, "the error", err.Error(), tt.want, quote(tt.pattern))
	}
}

// ran returns the matchers of the hooks that ran, sorted, with "(guard)"
// after one that ran only as a guard.
func ran(out Outcome) []string {
	names := []string{}
	for _, rec := range out.Records {
		name := rec.Matcher
		if name == "" {
			name = "(none)"
		}
		if rec.GuardOnly {
			name += " (guard)"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Which hooks run for which tool: exact names, lists, regular
// expressions, MCP names, a tool's older names, and guards.
func TestToolMatchersChooseTheHooks(t *testing.T) {
	opts := options(t)
	opts.Guards = map[string][]string{
		"Download":   {"WebFetch", "Write"},
		"CodeSearch": {"WebSearch"},
		"Spawn":      {"Agent"},
	}
	opts.Renamed = map[string]string{"View": "Read"}
	matchers := []string{
		"Bash", "Edit|Write", "", "*", "mcp__github__.*", "mcp__github", "^Notebook",
		"Task", "KillShell", "WebFetch", "WebSearch", "MultiEdit", "Edit.*", "View", "Download",
	}
	var hooks []Hook
	for _, m := range matchers {
		// Each hook gets a command of its own: the same command twice
		// would run once.
		hooks = append(hooks, Hook{Event: PreToolUse, Matcher: m, Command: ": " + shq(m)})
	}
	r := newRunner(t, opts, hooks...)

	tests := []struct {
		tool string
		want []string
	}{
		{"Bash", []string{"(none)", "*", "Bash"}},
		{"BashOutput", []string{"(none)", "*"}},
		{"Edit", []string{"(none)", "*", "Edit.*", "Edit|Write", "MultiEdit (guard)"}},
		{"Write", []string{"(none)", "*", "Edit|Write"}},
		{"NotebookEdit", []string{"(none)", "*", "Edit.*", "^Notebook"}},
		{"Read", []string{"(none)", "*", "View"}},
		{"mcp__github__create_issue", []string{"(none)", "*", "mcp__github__.*"}},
		{"mcp__gitlab__create_issue", []string{"(none)", "*"}},
		{"Agent", []string{"(none)", "*", "Task"}},
		{"TaskStop", []string{"(none)", "*", "KillShell"}},
		{"WebFetch", []string{"(none)", "*", "WebFetch"}},
		{"Download", []string{"(none)", "*", "Download", "Edit|Write (guard)", "WebFetch (guard)"}},
		{"CodeSearch", []string{"(none)", "*", "WebSearch (guard)"}},
		{"Spawn", []string{"(none)", "*", "Task (guard)"}},
		{"Monitor", []string{"(none)", "*", "Bash (guard)"}},
	}
	for _, tt := range tests {
		in := input(PreToolUse)
		in.ToolName = tt.tool
		got := ran(run(t, r, in))
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("tool %s: ran %s, want %s", tt.tool, strings.Join(got, " / "), strings.Join(tt.want, " / "))
		}
	}
}

// Events that are not about a tool match on a value of their own, and
// events with nothing to match run every hook.
func TestValueMatchersChooseTheHooks(t *testing.T) {
	hooks := func(event Event, matchers ...string) []Hook {
		var out []Hook
		for _, m := range matchers {
			out = append(out, Hook{Event: event, Matcher: m, Command: ": " + shq(m)})
		}
		return out
	}
	var all []Hook
	all = append(all, hooks(SessionStart, "startup", "resume|clear", "", "^(compact|fork)$")...)
	all = append(all, hooks(SessionEnd, "prompt_input_exit", "other", "*")...)
	all = append(all, hooks(PreCompact, "manual", "auto")...)
	all = append(all, hooks(PostCompact, "auto")...)
	all = append(all, hooks(StopFailure, "rate_limit|overloaded", "server_error", "")...)
	all = append(all, hooks(InstructionsLoaded, "session_start", "include|compact")...)
	all = append(all, hooks(Notification, "permission_prompt", "idle_prompt")...)
	all = append(all, hooks(SubagentStart, "Explore", "Explore|Plan", "general-purpose", "", ".*")...)
	all = append(all, hooks(Stop, "", "*")...)
	all = append(all, hooks(UserPromptSubmit, "")...)
	all = append(all, hooks(SubagentStop, "*")...)
	all = append(all, hooks(PreModelSwitch, "test-model", "local/test-model", "other-model|big")...)
	all = append(all, hooks(PostModelSwitch, "test-model")...)
	all = append(all, hooks(ConfigChange, "project_settings|local_settings", "user_settings", "")...)
	r := newRunner(t, options(t), all...)

	tests := []struct {
		name string
		in   func(*Input)
		ev   Event
		want []string
	}{
		{"a new session", func(in *Input) { in.Source = "startup" }, SessionStart, []string{"(none)", "startup"}},
		{"a resumed session", func(in *Input) { in.Source = "resume" }, SessionStart, []string{"(none)", "resume|clear"}},
		{"after /clear", func(in *Input) { in.Source = "clear" }, SessionStart, []string{"(none)", "resume|clear"}},
		{"after a summary", func(in *Input) { in.Source = "compact" }, SessionStart, []string{"(none)", "^(compact|fork)$"}},
		{"a forked session", func(in *Input) { in.Source = "fork" }, SessionStart, []string{"(none)", "^(compact|fork)$"}},
		{"the user quits", func(in *Input) { in.Reason = "prompt_input_exit" }, SessionEnd, []string{"*", "prompt_input_exit"}},
		{"a session is cleared", func(in *Input) { in.Reason = "clear" }, SessionEnd, []string{"*"}},
		{"a summary asked for", func(in *Input) { in.Trigger = "manual" }, PreCompact, []string{"manual"}},
		{"an automatic summary", func(in *Input) { in.Trigger = "auto" }, PreCompact, []string{"auto"}},
		{"after a summary asked for", func(in *Input) { in.Trigger = "manual" }, PostCompact, []string{}},
		{"a rate limit", func(in *Input) { in.Error = "rate_limit" }, StopFailure, []string{"(none)", "rate_limit|overloaded"}},
		{"a server error", func(in *Input) { in.Error = "server_error" }, StopFailure, []string{"(none)", "server_error"}},
		{"an unknown error", func(in *Input) { in.Error = "unknown" }, StopFailure, []string{"(none)"}},
		{"a file loaded by an import", func(in *Input) { in.LoadReason = "include" }, InstructionsLoaded, []string{"include|compact"}},
		{"a permission question that waited", func(in *Input) { in.NotificationType = "permission_prompt" }, Notification, []string{"permission_prompt"}},
		{"an exploring sub-agent", func(in *Input) { in.AgentType = "Explore" }, SubagentStart, []string{"(none)", ".*", "Explore", "Explore|Plan"}},
		{"a general sub-agent", func(in *Input) { in.AgentType = "general-purpose" }, SubagentStart, []string{"(none)", ".*", "general-purpose"}},
		{"a helper with no type", func(in *Input) { in.AgentType = "" }, SubagentStart, []string{"(none)", ".*"}},
		{"the end of a turn", func(in *Input) {}, Stop, []string{"(none)", "*"}},
		{"a prompt", func(in *Input) { in.Prompt = "Bash Stop startup" }, UserPromptSubmit, []string{"(none)"}},
		{"the end of a sub-agent", func(in *Input) { in.AgentType = "Explore" }, SubagentStop, []string{"*"}},
		// A model is matched as provider/model and by its own name.
		{"a switch", func(in *Input) { in.ToModel = "local/test-model" }, PreModelSwitch, []string{"local/test-model", "test-model"}},
		{"a switch to a model named like it", func(in *Input) { in.ToModel = "local/test-model-2" }, PreModelSwitch, []string{"local/test-model"}},
		{"a switch to another provider's model", func(in *Input) { in.ToModel = "remote/big" }, PreModelSwitch, []string{"other-model|big"}},
		{"a model with no provider", func(in *Input) { in.ToModel = "test-model" }, PreModelSwitch, []string{"test-model"}},
		{"after a switch", func(in *Input) { in.ToModel = "remote/test-model" }, PostModelSwitch, []string{"test-model"}},
		{"the project's settings changed", func(in *Input) { in.Source = "project_settings" }, ConfigChange, []string{"(none)", "project_settings|local_settings"}},
		{"the person's settings changed", func(in *Input) { in.Source = "user_settings" }, ConfigChange, []string{"(none)", "user_settings"}},
	}
	for _, tt := range tests {
		in := input(tt.ev)
		tt.in(&in)
		if got := ran(run(t, r, in)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: ran %v, want %v", tt.name, got, tt.want)
		}
	}
}

// The same command configured twice for one event runs once, whichever
// matchers brought it there.
func TestIdenticalCommandsRunOnce(t *testing.T) {
	dir := t.TempDir()
	count := "echo ran >> " + shq(dir+"/count")
	r := newRunner(t, options(t),
		Hook{Event: PreToolUse, Matcher: "Bash", Command: count, Source: "user"},
		Hook{Event: PreToolUse, Matcher: "*", Command: count, Source: "project"},
		Hook{Event: PreToolUse, Matcher: "Bash|Edit", Command: count + " ", Source: "another command"},
		Hook{Event: PostToolUse, Matcher: "Bash", Command: count, Source: "another event"},
	)
	out := run(t, r, input(PreToolUse))
	if len(out.Records) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(out.Records), out.Records)
	}
	if out.Records[0].Source != "user" || out.Records[1].Source != "another command" {
		t.Errorf("the records are of %q and %q; want the first of the two alike, then the other command", out.Records[0].Source, out.Records[1].Source)
	}
	if got := strings.Count(waitFor(t, dir+"/count"), "ran"); got != 2 {
		t.Errorf("the commands ran %d times, want 2", got)
	}
}

// A Bash hook guards the commands Monitor starts, as in Claude Code: before
// they run, seeing each as a Bash call, and its changed input counts for
// nothing; after them (PostToolUse) it does not run for them.
func TestBashHooksGuardMonitorCommands(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "seen.json")
	rewrite := prints(t, `{"hookSpecificOutput": {"hookEventName": "PreToolUse", "updatedInput": {"command": "echo rewritten"}}}`)
	r := newRunner(t, options(t),
		Hook{Event: PreToolUse, Matcher: "Bash", Command: "cat > " + shq(seen) + "; " + rewrite},
		Hook{Event: PostToolUse, Matcher: "Bash", Command: "true"})
	in := input(PreToolUse)
	in.ToolName, in.ToolInput = "Monitor", json.RawMessage(`{"command":"tail -f log","description":"the log","timeout_ms":1000}`)
	out := run(t, r, in)
	if out.UpdatedInput != nil {
		t.Errorf("a Bash hook's changed input was applied to a Monitor call: %s", out.UpdatedInput)
	}
	data, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ToolName != "Bash" || got.ToolInput["command"] != "tail -f log" || got.ToolInput["timeout_ms"] != nil {
		t.Errorf("the Bash hook was given %s", data)
	}

	post := input(PostToolUse)
	post.ToolName, post.ToolInput = "Monitor", json.RawMessage(`{"command":"tail -f log"}`)
	if names := ran(run(t, r, post)); len(names) != 0 {
		t.Errorf("after a Monitor call, the hooks %v ran", names)
	}
}
