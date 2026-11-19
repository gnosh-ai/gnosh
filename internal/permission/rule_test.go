package permission

import (
	"errors"
	"strings"
	"testing"
)

func TestParseRuleRoundTrip(t *testing.T) {
	for _, s := range []string{
		"Bash", "Read", "Bash(npm run build)", "Bash(git diff *)", "Bash(ls:*)",
		"Bash(echo (a) (b))", "Edit(/src/**/*.ts)", "Read(//Users/alice/secrets/**)",
		"Read(~/Documents/*.pdf)", "Read(*.env)", "Read(!public/**)", "WebFetch(domain:example.com)",
		"WebFetch(domain:*.example.com)", "WebSearch", "mcp__github", "mcp__github__*",
		"mcp__github__get_issue", "mcp__*", "*", "Agent(Explore)", "Task(Explore)", "Agent(model:opus)",
		"Skill(deploy)", "Skill(deploy *)", "Skill(skill:deploy)", "Bash(run_in_background:true)",
		"KillShell", "TodoWrite", "MultiEdit(/x)",
	} {
		r, err := ParseRule(s)
		if err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		if r.String() != s {
			t.Errorf("%s printed as %s", s, r.String())
		}
	}
	// Bash(*) is the bare rule.
	if r, _ := ParseRule("Bash(*)"); r.String() != "Bash" {
		t.Errorf("Bash(*) printed as %s", r)
	}
	if r, _ := ParseRule("  Bash(ls)  "); r.String() != "Bash(ls)" {
		t.Errorf("surrounding space kept: %q", r)
	}
	for _, s := range []string{"", "   ", "Bash(", "Bash(ls", "Bash()", "Bash( )", "(ls)", "Ba sh(ls)", "Bash)", "two words", ")(*)", "Ba$h", "Bash(ls) x"} {
		if _, err := ParseRule(s); err == nil {
			t.Errorf("%q: want an error", s)
		}
	}
}

func TestBadRulesStopStartup(t *testing.T) {
	tests := []struct {
		list List
		rule string
		want string // a piece of the message
	}{
		{ListAllow, "Bash(", "never closed"},
		{ListAllow, "Bsah(ls)", "no tool named"},
		{ListDeny, "websearch", "no tool named"},
		{ListAllow, "Write(/src/**)", "write Edit(/src/**)"},
		{ListDeny, "Write(.env)", "write Edit(.env)"},
		{ListDeny, "NotebookEdit(x)", "write Edit(x)"},
		{ListAllow, "Glob(src/**)", "write Read(src/**)"},
		{ListDeny, "Grep(.env)", "write Read(.env)"},
		{ListAllow, "*", "pattern over tool names"},
		{ListAllow, "B*", "pattern over tool names"},
		{ListAllow, "mcp__*", "pattern over tool names"},
		{ListAllow, "mcp__*__read", "pattern over tool names"},
		{ListDeny, "mcp__github__get_issue(x)", "takes no specifier"},
		{ListAllow, "mcp__github(x)", "takes no specifier"},
		{ListAllow, "Bash(run_in_background:true)", "only deny and ask"},
		{ListAllow, "Agent(model:opus)", "only deny and ask"},
		{ListDeny, "Bash(command:rm *)", "main input"},
		{ListDeny, "Read(file_path:/x)", "main input"},
		{ListAllow, "Bash(PORT=* npm run dev)", "variable assignment"},
		{ListAllow, "Read(!secret)", "means nothing in an allow rule"},
		{ListDeny, "Read(../x)", "\"..\""},
		{ListAllow, "WebFetch(example.com)", "names a host"},
		{ListAllow, "WebFetch(domain:)", "names a host"},
		{ListAllow, "WebFetch(domain:example.com/path)", "names a host"},
		{ListAllow, "WebSearch(cats)", "takes no specifier"},
		{ListAllow, "Skill(a b)", "names one skill"},
		{ListAllow, "Monitor(tail *)", "takes no specifier"},
	}
	for _, tt := range tests {
		o := testOptions(ModeDefault, rules{})
		switch tt.list {
		case ListAllow:
			o.Layers[0].Allow = []string{tt.rule}
		case ListDeny:
			o.Layers[0].Deny = []string{tt.rule}
		}
		_, err := New(o)
		if err == nil {
			t.Errorf("%s rule %q: want startup to stop", tt.list, tt.rule)
			continue
		}
		var re *RuleError
		if !errors.As(err, &re) || re.Rule != tt.rule || re.Source != "user settings" || re.List != tt.list {
			t.Errorf("%q: error does not name the rule and its source: %v", tt.rule, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, tt.rule) || !strings.Contains(msg, "user settings") || !strings.Contains(msg, tt.want) {
			t.Errorf("%q: message %q lacks the rule, the source or %q", tt.rule, msg, tt.want)
		}
	}

	// Every bad rule is reported, not only the first.
	o := testOptions(ModeDefault, rules{allow: []string{"Nope", "Bash(ls)"}, deny: []string{"AlsoNope(x)"}})
	_, err := New(o)
	if err == nil || !strings.Contains(err.Error(), "Nope") || !strings.Contains(err.Error(), "AlsoNope") {
		t.Errorf("want both bad rules named, got %v", err)
	}

	// A /path rule needs a settings directory to be read from.
	o = testOptions(ModeDefault, rules{deny: []string{"Read(/secrets/**)"}})
	o.Layers[0].Base = ""
	if _, err := New(o); err == nil || !strings.Contains(err.Error(), "one slash") {
		t.Errorf("want an error about the anchor, got %v", err)
	}

	// Grant holds a rule to the same standard.
	e := engine(t, ModeDefault, rules{})
	if err := e.Grant("Write(/x)", GrantSession); err == nil {
		t.Errorf("Grant took a rule that can never match")
	}
	if err := e.Grant("Bash(ls *)", "forever"); err == nil {
		t.Errorf("Grant took an unknown scope")
	}
}

func TestKnownNames(t *testing.T) {
	// Names Claude Code has or had, and gnosh has no tool for, load and
	// never match.
	e := engine(t, ModeDefault, rules{
		allow: []string{"TodoWrite", "TaskOutput", "LS", "MultiEdit(/proj/**)"},
		deny:  []string{"BashOutput", "MultiEdit(/proj/**)"},
	})
	if d := file(e, "Edit", "/proj/a.go"); d.Behavior != Ask {
		t.Errorf("a MultiEdit rule must not touch Edit: %+v", d)
	}
	if !e.ToolEnabled("Edit") || !e.ToolEnabled("Bash") {
		t.Errorf("an absent name's deny switched a real tool off")
	}

	// The program's own tools are known through the options.
	o := testOptions(ModeDefault, rules{allow: []string{"gnosh_info"}, deny: []string{"gnokey"}})
	if _, err := New(o); err == nil {
		t.Fatalf("unknown tool names should stop startup")
	}
	o.KnownTools = []string{"gnokey", "gnosh_info", "TaskOutput"}
	o.QuietTools = []string{"mem_read"}
	e = mustEngine(t, o)
	if e.ToolEnabled("gnokey") {
		t.Errorf("gnokey should be off")
	}
	if d := other(e, "gnosh_info"); d.Behavior != Allow {
		t.Errorf("allowed program tool: %+v", d)
	}
	if d := other(e, "mem_read"); d.Behavior != Allow {
		t.Errorf("quiet program tool: %+v", d)
	}
	if d := other(e, "TaskOutput"); d.Behavior != Ask {
		t.Errorf("a known tool with no rule asks: %+v", d)
	}
}

func TestAliases(t *testing.T) {
	e := engine(t, ModeDefault, rules{deny: []string{"Task(Explore)", "KillShell"}})
	if d := other(e, "Agent", "subagent_type", "Explore", "prompt", "x"); d.Behavior != Deny || d.Rule != "Task(Explore)" {
		t.Errorf("Task(Explore) should stop Agent's Explore: %+v", d)
	}
	if d := other(e, "Task", "subagent_type", "Explore"); d.Behavior != Deny {
		t.Errorf("the old tool name is judged as the new one: %+v", d)
	}
	if d := other(e, "Agent", "subagent_type", "Plan"); d.Behavior != Allow {
		t.Errorf("another agent type runs: %+v", d)
	}
	if d := other(e, "Agent", "prompt", "x"); d.Behavior != Allow {
		t.Errorf("the default agent type runs: %+v", d)
	}
	if e.ToolEnabled("TaskStop") || e.ToolEnabled("KillBash") {
		t.Errorf("KillShell names TaskStop")
	}
	if !e.ToolEnabled("Agent") {
		t.Errorf("a scoped deny leaves the tool")
	}
	e = engine(t, ModeDefault, rules{deny: []string{"Agent(general-purpose)"}})
	if d := other(e, "Agent", "prompt", "x"); d.Behavior != Deny {
		t.Errorf("an Agent call with no type is the general-purpose agent: %+v", d)
	}
}

func TestMCPRules(t *testing.T) {
	e := engine(t, ModeDefault, rules{
		allow: []string{"mcp__docs", "mcp__github__get_*", "mcp__files__read"},
		deny:  []string{"mcp__github__get_secret", "mcp__evil__*"},
		ask:   []string{"mcp__docs__publish"},
	})
	for tool, want := range map[string]Behavior{
		"mcp__docs__search":       Allow, // mcp__server is every tool of the server
		"mcp__docs__publish":      Ask,   // ask beats allow
		"mcp__github__get_issue":  Allow,
		"mcp__github__get_secret": Deny, // deny beats allow
		"mcp__github__create_pr":  Ask,
		"mcp__files__read":        Allow,
		"mcp__files__write":       Ask,
		"mcp__evil__anything":     Deny,
		"mcp__docsx__search":      Ask, // another server whose name merely starts the same
	} {
		if d := other(e, tool); d.Behavior != want {
			t.Errorf("%s: got %s (%s), want %s", tool, d.Behavior, d.Reason, want)
		}
	}
	if e.ToolEnabled("mcp__evil__x") || e.ToolEnabled("mcp__github__get_secret") || !e.ToolEnabled("mcp__github__get_issue") {
		t.Errorf("a deny that names whole tools removes those tools and no others")
	}
	e = engine(t, ModeDefault, rules{deny: []string{"mcp__*"}})
	if e.ToolEnabled("mcp__a__b") || !e.ToolEnabled("Read") {
		t.Errorf("mcp__* removes every MCP tool and nothing else")
	}
	e = engine(t, ModeBypass, rules{deny: []string{"*"}})
	for _, tool := range []string{"Read", "Bash", "mcp__a__b", "Agent"} {
		if e.ToolEnabled(tool) {
			t.Errorf("* should remove %s", tool)
		}
	}
	if d := bash(e, "ls"); d.Behavior != Deny || d.Stage != StageTool {
		t.Errorf("a removed tool is refused: %+v", d)
	}
}

func TestParamRules(t *testing.T) {
	e := engine(t, ModeBypass, rules{
		deny: []string{"Bash(run_in_background:true)", "Agent(model : op*)", "Grep(output_mode:content)"},
		ask:  []string{"Bash(timeout:6*)"},
	})
	call := func(tool string, kv ...any) Behavior {
		return e.Decide(tool, coreCall(tool, kv...), coreAction("make")).Behavior
	}
	if got := call("Bash", "command", "make", "run_in_background", true); got != Deny {
		t.Errorf("run_in_background:true: %s", got)
	}
	if got := call("Bash", "command", "make", "run_in_background", false); got != Allow {
		t.Errorf("run_in_background:false: %s", got)
	}
	if got := call("Bash", "command", "make"); got != Allow {
		t.Errorf("an omitted parameter never matches: %s", got)
	}
	if got := call("Bash", "command", "make", "timeout", 600000); got != Ask {
		t.Errorf("a number is matched as written: %s", got)
	}
	if got := call("Bash", "command", "make", "timeout", 5000); got != Allow {
		t.Errorf("timeout 5000: %s", got)
	}
	if d := other(e, "Agent", "model", "opus"); d.Behavior != Deny {
		t.Errorf("Agent(model : op*): %+v", d)
	}
	if d := other(e, "Agent", "model", "haiku"); d.Behavior != Allow {
		t.Errorf("another model: %+v", d)
	}
	if d := other(e, "Agent", "model", map[string]any{"x": 1}); d.Behavior != Allow {
		t.Errorf("only scalars match: %+v", d)
	}
}

func TestSkillRules(t *testing.T) {
	e := engine(t, ModeDefault, rules{
		allow: []string{"Skill(deploy)", "Skill(report *)"},
		deny:  []string{"Skill(skill:danger)"},
	})
	for _, tt := range []struct {
		name, args string
		want       Behavior
	}{
		{"deploy", "", Allow},
		{"deploy", "prod", Ask}, // the exact form is the skill with no arguments
		{"report", "", Allow},
		{"report", "q3 --pdf", Allow},
		{"danger", "", Deny},
		{"danger", "anything", Deny},
		{"other", "", Ask},
	} {
		kv := []any{"skill", tt.name}
		if tt.args != "" {
			kv = append(kv, "args", tt.args)
		}
		if d := other(e, "Skill", kv...); d.Behavior != tt.want {
			t.Errorf("Skill %s %q: got %s, want %s", tt.name, tt.args, d.Behavior, tt.want)
		}
	}
	e = engine(t, ModeDefault, rules{deny: []string{"Skill"}})
	if e.ToolEnabled("Skill") {
		t.Errorf("a bare Skill deny removes the tool")
	}
}

func TestWebFetchRules(t *testing.T) {
	e := engine(t, ModeDefault, rules{
		allow: []string{"WebFetch(domain:example.com)", "WebFetch(domain:*.docs.example.org)", "WebFetch(domain:static.*)"},
		deny:  []string{"WebFetch(domain:evil.example.com)", "WebFetch(domain:*.internal)"},
	})
	for url, want := range map[string]Behavior{
		"https://example.com/a":                  Allow,
		"https://EXAMPLE.com./a":                 Allow, // case and a trailing dot are ignored
		"https://example.com:8443/a":             Allow,
		"https://www.example.com/":               Ask, // not a subdomain rule
		"https://a.docs.example.org/x":           Allow,
		"https://a.b.docs.example.org/x":         Allow, // any depth
		"https://docs.example.org/x":             Ask,   // not the domain itself
		"https://static.org/x":                   Allow,
		"https://static.evil.com/x":              Ask, // the wildcard does not cross a dot
		"https://evil.example.com/":              Deny,
		"https://db.internal/x":                  Deny,
		"https://example.com@evil.example.com/x": Deny, // the host is what follows the @
		"https://other.net/":                     Ask,
		"not a url at all":                       Ask,
		"":                                       Ask,
	} {
		if d := fetch(e, url); d.Behavior != want {
			t.Errorf("%q: got %s (%s), want %s", url, d.Behavior, d.Reason, want)
		}
	}
	d := fetch(e, "https://Other.NET/page")
	if got := suggested(d); len(got) != 1 || got[0] != "WebFetch(domain:other.net)" || d.Suggestions[0].Scope != GrantLocal {
		t.Errorf("suggestions: %q %+v", got, d.Suggestions)
	}
	if d := fetch(engine(t, ModeDefault, rules{allow: []string{"WebFetch(domain:*)"}}), "https://anything.example"); d.Behavior != Allow {
		t.Errorf("domain:* is every host: %+v", d)
	}
	if d := fetch(engine(t, ModeDefault, rules{allow: []string{"WebFetch"}}), "https://anything.example"); d.Behavior != Allow {
		t.Errorf("bare WebFetch allows every fetch: %+v", d)
	}
	e = engine(t, ModeDefault, rules{deny: []string{"WebFetch"}})
	if e.ToolEnabled("WebFetch") {
		t.Errorf("a bare WebFetch deny removes the tool")
	}
	// Fetches and searches still ask in acceptEdits.
	e = engine(t, ModeAcceptEdits, rules{})
	if d := fetch(e, "https://example.com"); d.Behavior != Ask {
		t.Errorf("acceptEdits does not cover fetches: %+v", d)
	}
	d = other(e, "WebSearch", "query", "x")
	if d.Behavior != Ask || len(d.Suggestions) != 1 || d.Suggestions[0].Rules[0] != "WebSearch" || d.Suggestions[0].Scope != GrantLocal {
		t.Errorf("WebSearch: %+v", d)
	}
}

// An Agent(kind) rule judges the kind as the tool reads it: blanks around
// it left off, and none (or only blanks) the general-purpose kind. Read as
// written, "" and " Explore" went past the rules (review of 2026-11-19).
func TestAgentKindAsTheToolReadsIt(t *testing.T) {
	e := engine(t, ModeDefault, rules{deny: []string{"Agent(general-purpose)", "Agent(Explore)"}})
	for _, typ := range []string{"", "   ", " Explore", "Explore\t", "general-purpose "} {
		if d := other(e, "Agent", "subagent_type", typ, "prompt", "x"); d.Behavior != Deny {
			t.Errorf("subagent_type %q went past the deny rules: %+v", typ, d)
		}
	}
	if d := other(e, "Agent", "subagent_type", "Plan", "prompt", "x"); d.Behavior != Allow {
		t.Errorf("another kind is refused: %+v", d)
	}
}
