package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The answers a hook can give about a tool call, as commands.
func allow(t *testing.T, reason string) string {
	return prints(t, `{"hookSpecificOutput": {"permissionDecision": "allow", "permissionDecisionReason": "`+reason+`"}}`)
}

func ask(t *testing.T, reason string) string {
	return prints(t, `{"hookSpecificOutput": {"permissionDecision": "ask", "permissionDecisionReason": "`+reason+`"}}`)
}

func deny(t *testing.T, reason string) string {
	return prints(t, `{"hookSpecificOutput": {"permissionDecision": "deny", "permissionDecisionReason": "`+reason+`"}}`)
}

func rewrite(t *testing.T, input string) string {
	return prints(t, `{"hookSpecificOutput": {"updatedInput": `+input+`}}`)
}

func allowRewrite(t *testing.T, input string) string {
	return prints(t, `{"hookSpecificOutput": {"permissionDecision": "allow", "updatedInput": `+input+`}}`)
}

// Several hooks on one tool call: the most restrictive answer wins, in
// whatever order the hooks are configured.
func TestPreToolUseAnswersCombine(t *testing.T) {
	silent := "exit 0"
	broken := "echo 'the rules file is missing' >&2; exit 3"
	tests := []struct {
		name     string
		commands func(t *testing.T) []string
		blocked  bool
		failed   bool
		decision Decision
		reason   string // "~" before a part
	}{
		{"no opinions", func(t *testing.T) []string { return []string{silent, "exit 1"} }, false, false, NoDecision, ""},
		{"an approval and no opinion", func(t *testing.T) []string { return []string{silent, allow(t, "safe")} }, false, false, Allow, "safe"},
		{"two approvals", func(t *testing.T) []string { return []string{allow(t, "rule 1"), allow(t, "rule 2")} }, false, false, Allow, "rule 1\n\nrule 2"},
		{"an approval, then an ask", func(t *testing.T) []string { return []string{allow(t, "safe"), ask(t, "a push")} }, false, false, Ask, "a push"},
		{"an ask, then an approval", func(t *testing.T) []string { return []string{ask(t, "a push"), allow(t, "safe")} }, false, false, Ask, "a push"},
		{"two asks", func(t *testing.T) []string { return []string{ask(t, "a push"), ask(t, "to main")} }, false, false, Ask, "a push\n\nto main"},
		{"an approval, then a deny", func(t *testing.T) []string { return []string{allow(t, "safe"), deny(t, "never")} }, true, false, Deny, "never"},
		{"a deny, then an approval", func(t *testing.T) []string { return []string{deny(t, "never"), allow(t, "safe")} }, true, false, Deny, "never"},
		{"an ask, then a deny", func(t *testing.T) []string { return []string{ask(t, "a push"), deny(t, "never")} }, true, false, Deny, "never"},
		{"a deny, an ask and an approval", func(t *testing.T) []string { return []string{deny(t, "never"), ask(t, "a push"), allow(t, "safe")} }, true, false, Deny, "never"},
		{"an approval and exit 2", func(t *testing.T) []string { return []string{allow(t, "safe"), "echo 'not here' >&2; exit 2"} }, true, false, Deny, "not here"},
		{"two blocks", func(t *testing.T) []string { return []string{"echo first >&2; exit 2", deny(t, "second")} }, true, false, Deny, "first\n\nsecond"},
		{"an approval and a hook that could not evaluate", func(t *testing.T) []string { return []string{allow(t, "safe"), broken} }, true, true, Deny, "~the rules file is missing"},
		{"a hook that could not evaluate, then an approval", func(t *testing.T) []string { return []string{broken, allow(t, "safe")} }, true, true, Deny, "~could not check this"},
		{"an ask and a hook that could not evaluate", func(t *testing.T) []string { return []string{ask(t, "a push"), broken} }, true, true, Deny, "~could not check this"},
		{"a deny and a hook that could not evaluate", func(t *testing.T) []string { return []string{deny(t, "never"), broken} }, true, true, Deny, "~never\n\nThe PreToolUse hook"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hooks []Hook
			for _, c := range tt.commands(t) {
				hooks = append(hooks, Hook{Event: PreToolUse, Command: c})
			}
			out := run(t, newRunner(t, options(t), hooks...), input(PreToolUse))
			if out.Blocked != tt.blocked || out.CouldNotEvaluate != tt.failed || out.PermissionDecision != tt.decision {
				t.Errorf("Blocked %v, CouldNotEvaluate %v, PermissionDecision %q; want %v, %v, %q", out.Blocked, out.CouldNotEvaluate, out.PermissionDecision, tt.blocked, tt.failed, tt.decision)
			}
			if part, ok := strings.CutPrefix(tt.reason, "~"); ok {
				contains(t, "the reason", out.Reason, part)
			} else if out.Reason != tt.reason {
				t.Errorf("Reason = %q, want %q", out.Reason, tt.reason)
			}
			if len(out.Records) != len(hooks) {
				t.Errorf("got %d records for %d hooks", len(out.Records), len(hooks))
			}
		})
	}
}

// Two hooks that change a call's input must agree, and an approval is for
// the input its hook saw or wrote.
func TestChangedInputsCombine(t *testing.T) {
	const safer = `{"command": "rm -i build/"}`
	tests := []struct {
		name     string
		commands func(t *testing.T) []string
		blocked  bool
		decision Decision
		input    string
		reason   string
		note     string // a note on the first hook's record
	}{
		{"one rewrite", func(t *testing.T) []string { return []string{"exit 0", rewrite(t, safer)} }, false, NoDecision, `{"command":"rm -i build/"}`, "", ""},
		{"the same rewrite twice, written differently", func(t *testing.T) []string {
			return []string{rewrite(t, safer), rewrite(t, `{ "command":"rm -i build/" }`)}
		}, false, NoDecision, `{"command":"rm -i build/"}`, "", ""},
		{"two different rewrites", func(t *testing.T) []string {
			return []string{rewrite(t, safer), rewrite(t, `{"command": "timeout 60 rm -r build/"}`)}
		}, true, Deny, "", "~each changed the call's input, in different ways", "its changed input was not used"},
		{"an approval of the call as written, and another hook's rewrite", func(t *testing.T) []string {
			return []string{allow(t, "rm in build/ is fine"), rewrite(t, safer)}
		}, false, NoDecision, `{"command":"rm -i build/"}`, "", "it approved the call as the model wrote it"},
		{"an approval that wrote the rewrite itself", func(t *testing.T) []string {
			return []string{allowRewrite(t, safer), "exit 0"}
		}, false, Allow, `{"command":"rm -i build/"}`, "", ""},
		{"two approvals, one of which wrote the rewrite", func(t *testing.T) []string {
			return []string{allow(t, "fine as it is"), allowRewrite(t, safer)}
		}, false, Allow, `{"command":"rm -i build/"}`, "", "it approved the call as the model wrote it"},
		{"a rewrite and an ask", func(t *testing.T) []string {
			return []string{rewrite(t, safer), ask(t, "delete build/?")}
		}, false, Ask, `{"command":"rm -i build/"}`, "delete build/?", ""},
		{"a rewrite and a deny", func(t *testing.T) []string {
			return []string{rewrite(t, safer), deny(t, "never rm")}
		}, true, Deny, "", "never rm", ""},
		{"a rewrite from a hook that could not evaluate is not used", func(t *testing.T) []string {
			return []string{rewrite(t, safer) + "; exit 3", "exit 0"}
		}, true, Deny, "", "~status 3", ""},
		{"a rewrite from a hook that blocks is not used", func(t *testing.T) []string {
			return []string{rewrite(t, `{"command": "x"}`) + "; exit 2", rewrite(t, safer)}
		}, true, Deny, "", "~blocked this and gave no reason", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hooks []Hook
			for _, c := range tt.commands(t) {
				hooks = append(hooks, Hook{Event: PreToolUse, Command: c})
			}
			in := input(PreToolUse)
			in.ToolInput = []byte(`{"command": "rm -r build/"}`)
			out := run(t, newRunner(t, options(t), hooks...), in)
			if out.Blocked != tt.blocked || out.PermissionDecision != tt.decision || string(out.UpdatedInput) != tt.input {
				t.Errorf("Blocked %v, PermissionDecision %q, UpdatedInput %s; want %v, %q, %s", out.Blocked, out.PermissionDecision, out.UpdatedInput, tt.blocked, tt.decision, tt.input)
			}
			if part, ok := strings.CutPrefix(tt.reason, "~"); ok {
				contains(t, "the reason", out.Reason, part)
			} else if out.Reason != tt.reason && tt.decision != Allow {
				t.Errorf("Reason = %q, want %q", out.Reason, tt.reason)
			}
			if tt.note != "" {
				contains(t, "the first hook's notes", strings.Join(out.Records[0].Notes, "\n"), tt.note)
				if out.Records[0].Result == ResultApproved {
					t.Error("the first hook's record still says it approved")
				}
			}
			if out.CouldNotEvaluate != strings.Contains(tt.name, "could not evaluate") {
				t.Errorf("CouldNotEvaluate = %v", out.CouldNotEvaluate)
			}
		})
	}
}

// A guard written for one of Claude Code's tools also covers a gnosh tool
// that does the same job, for blocking only.
func TestGuardsBlockAndDoNotApprove(t *testing.T) {
	opts := options(t)
	opts.Guards = map[string][]string{"Download": {"WebFetch", "Write"}}
	download := input(PreToolUse)
	download.ToolName = "Download"
	download.ToolInput = []byte(`{"url": "https://example.com/a.tgz", "file_path": "/project/a.tgz"}`)

	tests := []struct {
		name     string
		hooks    func(t *testing.T) []Hook
		blocked  bool
		decision Decision
		input    string
		note     string
	}{
		{"a guard's deny holds", func(t *testing.T) []Hook {
			return []Hook{{Matcher: "WebFetch", Command: deny(t, "no downloads from that host")}}
		}, true, Deny, "", ""},
		{"a guard's exit 2 holds", func(t *testing.T) []Hook {
			return []Hook{{Matcher: "Write", Command: "echo 'no writes outside src/' >&2; exit 2"}}
		}, true, Deny, "", ""},
		{"a guard's ask holds", func(t *testing.T) []Hook {
			return []Hook{{Matcher: "WebFetch", Command: ask(t, "fetch this?")}}
		}, false, Ask, "", ""},
		{"a guard's approval does not count", func(t *testing.T) []Hook {
			return []Hook{{Matcher: "WebFetch", Command: allow(t, "this host is fine for a fetch")}}
		}, false, NoDecision, "", `it is written for "WebFetch" and ran here only as a guard`},
		{"a guard's changed input is used, its approval is not", func(t *testing.T) []Hook {
			return []Hook{{Matcher: "WebFetch|Write", Command: allowRewrite(t, `{"url": "https://example.com/a.tgz", "file_path": "/project/tmp/a.tgz"}`)}}
		}, false, NoDecision, `{"url":"https://example.com/a.tgz","file_path":"/project/tmp/a.tgz"}`, "only as a guard"},
		{"a hook that names the tool approves it", func(t *testing.T) []Hook {
			return []Hook{{Matcher: "Download", Command: allow(t, "downloads are fine")}}
		}, false, Allow, "", ""},
		{"a hook for everything approves it", func(t *testing.T) []Hook {
			return []Hook{{Command: allow(t, "anything goes")}}
		}, false, Allow, "", ""},
		{"a hook that names the tool among others approves it", func(t *testing.T) []Hook {
			return []Hook{{Matcher: "WebFetch|Download", Command: allow(t, "both are fine")}}
		}, false, Allow, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hooks := tt.hooks(t)
			for i := range hooks {
				hooks[i].Event = PreToolUse
			}
			out := run(t, newRunner(t, opts, hooks...), download)
			if out.Blocked != tt.blocked || out.PermissionDecision != tt.decision || string(out.UpdatedInput) != tt.input {
				t.Errorf("Blocked %v, PermissionDecision %q, UpdatedInput %s; want %v, %q, %s", out.Blocked, out.PermissionDecision, out.UpdatedInput, tt.blocked, tt.decision, tt.input)
			}
			contains(t, "the notes", strings.Join(out.Records[0].Notes, "\n"), tt.note)
			if guard := tt.note != "" || tt.blocked || tt.decision == Ask; out.Records[0].GuardOnly != guard {
				t.Errorf("GuardOnly = %v, want %v", out.Records[0].GuardOnly, guard)
			}
		})
	}

	// The same command reached once as a guard and once by the tool's own
	// name runs once, and approves.
	same := allow(t, "checked both ways")
	r := newRunner(t, opts,
		Hook{Event: PreToolUse, Matcher: "WebFetch", Command: same},
		Hook{Event: PreToolUse, Matcher: "Download", Command: same},
	)
	out := run(t, r, download)
	if len(out.Records) != 1 || out.PermissionDecision != Allow || out.Records[0].GuardOnly {
		t.Errorf("got %d records, PermissionDecision %q", len(out.Records), out.PermissionDecision)
	}

	// On the tool the guard was written for, its approval counts.
	fetch := input(PreToolUse)
	fetch.ToolName = "WebFetch"
	fetch.ToolInput = []byte(`{"url": "https://example.com/", "prompt": "what is this?"}`)
	if out := run(t, r, fetch); out.PermissionDecision != Allow {
		t.Errorf("WebFetch: PermissionDecision %q, want allow", out.PermissionDecision)
	}

	// A permission question is answered the same way: a guard may refuse,
	// and may not allow.
	pr := newRunner(t, opts, Hook{Event: PermissionRequest, Matcher: "WebFetch", Command: prints(t, `{"hookSpecificOutput": {"decision": {"behavior": "allow"}}}`)})
	question := input(PermissionRequest)
	question.ToolName = "Download"
	out = run(t, pr, question)
	if out.PermissionDecision != NoDecision || len(out.Warnings) != 1 {
		t.Fatalf("a guard answered a permission question: %+v", out)
	}
	contains(t, "the warning", out.Warnings[0], "did not answer the permission question", "only as a guard")
}

// Several hooks answering a permission question: a refusal wins.
func TestPermissionRequestAnswersCombine(t *testing.T) {
	yes := func(t *testing.T) string {
		return prints(t, `{"hookSpecificOutput": {"decision": {"behavior": "allow"}}}`)
	}
	no := func(t *testing.T, msg string) string {
		return prints(t, `{"hookSpecificOutput": {"decision": {"behavior": "deny", "message": "`+msg+`"}}}`)
	}
	tests := []struct {
		name     string
		commands func(t *testing.T) []string
		decision Decision
		reason   string
		warnings int
	}{
		{"nobody answers", func(t *testing.T) []string { return []string{"exit 0", "exit 1"} }, NoDecision, "", 0},
		{"one allows", func(t *testing.T) []string { return []string{"exit 0", yes(t)} }, Allow, "", 0},
		{"one allows, one refuses", func(t *testing.T) []string { return []string{yes(t), no(t, "not this one")} }, Deny, "not this one", 0},
		{"one refuses, one allows", func(t *testing.T) []string { return []string{no(t, "not this one"), yes(t)} }, Deny, "not this one", 0},
		{"two refuse", func(t *testing.T) []string { return []string{no(t, "first"), no(t, "second")} }, Deny, "first\n\nsecond", 0},
		// (A hook that could not check answers nothing, and the other's
		// approval does not stand in for it: the question goes to the
		// person, as the warning says. Review of 2026-11-19.)
		{"one allows, one could not evaluate", func(t *testing.T) []string { return []string{yes(t), "exit 3"} }, NoDecision, "", 1},
		{"one could not evaluate", func(t *testing.T) []string { return []string{"exit 127"} }, NoDecision, "", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var hooks []Hook
			for _, c := range tt.commands(t) {
				hooks = append(hooks, Hook{Event: PermissionRequest, Command: c})
			}
			out := run(t, newRunner(t, options(t), hooks...), input(PermissionRequest))
			if out.PermissionDecision != tt.decision || out.Reason != tt.reason || len(out.Warnings) != tt.warnings || out.Blocked {
				t.Errorf("PermissionDecision %q, Reason %q, %d warnings, Blocked %v; want %q, %q, %d, false", out.PermissionDecision, out.Reason, len(out.Warnings), out.Blocked, tt.decision, tt.reason, tt.warnings)
			}
		})
	}
}

// Reasons, added text, warnings and stop reasons are joined in the order
// the hooks are configured, whichever finished first; and a hook that
// ends the turn ends it whatever the others said.
func TestEverythingIsJoinedInConfiguredOrder(t *testing.T) {
	dir := t.TempDir()
	// The first hook waits for the second to finish, so the order of
	// finishing is the reverse of the order configured.
	// Each notes when it is done in the file order, which says the order
	// of finishing for certain.
	first := script(t, `while [ ! -e `+shq(dir+"/second-done")+` ]; do sleep 0.01; done
cat <<'EOF'
{"decision": "block", "reason": "first reason", "systemMessage": "first warning",
 "hookSpecificOutput": {"additionalContext": "first context"}}
EOF
echo first >> `+shq(dir+"/order"))
	second := script(t, `cat <<'EOF'
{"decision": "block", "reason": "second reason", "systemMessage": "second warning",
 "hookSpecificOutput": {"additionalContext": ["second context", "more of it"]}}
EOF
echo second >> `+shq(dir+"/order")+`
: > `+shq(dir+"/second-done"))
	third := script(t, `echo 'third reason' >&2; exit 2`)
	r := newRunner(t, options(t),
		Hook{Event: Stop, Name: "one", Command: first},
		Hook{Event: Stop, Name: "two", Command: second},
		Hook{Event: Stop, Name: "three", Command: third},
		Hook{Event: Stop, Name: "four", Command: "exit 0"},
	)
	out := run(t, r, input(Stop))
	if !out.Blocked || out.Reason != "first reason\n\nsecond reason\n\nthird reason" {
		t.Errorf("Blocked %v, Reason %q", out.Blocked, out.Reason)
	}
	if out.AdditionalContext != "first context\n\nsecond context\nmore of it" {
		t.Errorf("AdditionalContext = %q", out.AdditionalContext)
	}
	if strings.Join(out.SystemMessages, "|") != "first warning|second warning" {
		t.Errorf("SystemMessages = %q", out.SystemMessages)
	}
	var names, results []string
	for _, rec := range out.Records {
		names = append(names, rec.Name)
		results = append(results, string(rec.Result))
	}
	if strings.Join(names, " ") != "one two three four" || strings.Join(results, " ") != "blocked blocked blocked none" {
		t.Errorf("records: %v %v", names, results)
	}
	if order, _ := os.ReadFile(filepath.Join(dir, "order")); string(order) != "second\nfirst\n" {
		t.Errorf("the hooks finished in the order %q, and the test needs the second first, or it proves nothing", order)
	}

	// One hook ends the turn; the others block, approve and add text.
	ends := newRunner(t, options(t),
		Hook{Event: Stop, Command: prints(t, `{"decision": "block", "reason": "not done"}`)},
		Hook{Event: Stop, Command: prints(t, `{"continue": false, "stopReason": "out of budget"}`)},
		Hook{Event: Stop, Command: prints(t, `{"continue": false}`), Name: "quiet"},
		Hook{Event: Stop, Command: prints(t, `{"continue": true, "hookSpecificOutput": {"additionalContext": "x"}}`)},
	)
	out = run(t, ends, input(Stop))
	if !out.EndTurn || out.StopReason != "out of budget\n\nThe Stop hook \"quiet\" ended the turn and gave no reason." {
		t.Errorf("EndTurn %v, StopReason %q", out.EndTurn, out.StopReason)
	}
	if !out.Blocked || out.Reason != "not done" || out.AdditionalContext != "x" {
		t.Errorf("the other hooks' answers were lost: %+v", out)
	}
}

// The hooks of one event run at the same time: each of these two waits
// for the other to have started, which hooks run one after the other
// never could.
func TestHooksRunAtTheSameTime(t *testing.T) {
	dir := t.TempDir()
	each := func(mine, other string) string {
		return script(t, `: > `+shq(filepath.Join(dir, mine))+`
while [ ! -e `+shq(filepath.Join(dir, other))+` ]; do sleep 0.01; done
echo "`+mine+` saw `+other+`" >&2
exit 2`)
	}
	limit := 20 * time.Second // only reached if the hooks do not overlap
	r := newRunner(t, options(t),
		Hook{Event: PreToolUse, Command: each("a", "b"), Timeout: limit},
		Hook{Event: PreToolUse, Command: each("b", "c"), Timeout: limit},
		Hook{Event: PreToolUse, Command: each("c", "a"), Timeout: limit},
	)
	began := time.Now()
	out := run(t, r, input(PreToolUse))
	if out.CouldNotEvaluate || out.Reason != "a saw b\n\nb saw c\n\nc saw a" {
		t.Fatalf("the hooks did not all see each other: CouldNotEvaluate %v, Reason %q", out.CouldNotEvaluate, out.Reason)
	}
	if took := time.Since(began); took > limit/2 {
		t.Errorf("Run took %v", took)
	}
}

// One Runner serves several callers at once.
func TestRunFromSeveralGoroutines(t *testing.T) {
	r := newRunner(t, options(t),
		Hook{Event: PreToolUse, Matcher: "Bash", Command: `echo "blocked $GNOSH_TOOL_USE_ID" >&2; exit 2`},
		Hook{Event: PostToolUse, Command: prints(t, `{"hookSpecificOutput": {"additionalContext": "after"}}`)},
		Hook{Event: Stop, Command: "exit 0"},
	)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			id := "toolu_" + string(rune('a'+i))
			pre := input(PreToolUse)
			pre.ToolUseID = id
			if out, err := r.Run(context.Background(), pre); err != nil || out.Reason != "blocked "+id {
				t.Errorf("PreToolUse %s: %q, %v", id, out.Reason, err)
			}
			if out, err := r.Run(context.Background(), input(PostToolUse)); err != nil || out.AdditionalContext != "after" {
				t.Errorf("PostToolUse: %q, %v", out.AdditionalContext, err)
			}
			if out, err := r.Run(context.Background(), input(Stop)); err != nil || out.Blocked {
				t.Errorf("Stop: %+v, %v", out, err)
			}
		})
	}
	wg.Wait()
}

// A record says, for every hook that ran, what it was and what it did.
func TestRecords(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	opts := options(t)
	opts.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(250 * time.Millisecond)
		return now
	}
	r := newRunner(t, opts, Hook{
		Event: PreToolUse, Matcher: "Bash|Edit", Name: "no-force-push", Source: "gnosh.json:4",
		Command: "echo 'checking the command' >&2; echo 'force pushes are not allowed' >&2; exit 2",
	})
	out := run(t, r, input(PreToolUse))
	if len(out.Records) != 1 {
		t.Fatalf("got %d records", len(out.Records))
	}
	rec := out.Records[0]
	want := Record{
		Event: PreToolUse, Matcher: "Bash|Edit", Command: "echo 'checking the command' >&2; echo 'force pushes are not allowed' >&2; exit 2",
		Name: "no-force-push", Source: "gnosh.json:4",
		Duration: 250 * time.Millisecond, ExitCode: 2,
		Result: ResultBlocked, Reason: "checking the command\nforce pushes are not allowed",
		Stderr: "checking the command\nforce pushes are not allowed",
	}
	if rec.Event != want.Event || rec.Matcher != want.Matcher || rec.Command != want.Command || rec.Name != want.Name ||
		rec.Source != want.Source || rec.Duration != want.Duration || rec.ExitCode != want.ExitCode ||
		rec.Result != want.Result || rec.Reason != want.Reason || rec.Stderr != want.Stderr || rec.GuardOnly || len(rec.Notes) != 0 {
		t.Errorf("record:\n got %+v\nwant %+v", rec, want)
	}
	if got, want := rec.String(), `PreToolUse hook "no-force-push" blocked (250ms): checking the command force pushes are not allowed`; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}

	// Each kind of result has its words, and none of them is "fine" for
	// a hook that could not evaluate.
	lines := map[Result]string{}
	for _, result := range []Result{ResultNone, ResultApproved, ResultAsked, ResultBlocked, ResultFeedback, ResultWarning, ResultEndedTurn, ResultCouldNotEvaluate, ResultCancelled} {
		line := Record{Event: Stop, Command: "./check.sh --all", Result: result, Duration: 12 * time.Millisecond}.String()
		contains(t, "the line", line, "Stop hook `./check.sh --all` ", "(12ms)")
		lines[result] = line
	}
	contains(t, "the line", lines[ResultCouldNotEvaluate], "could not evaluate")
	if got, want := (Record{Event: SessionStart, Name: "greeting", Result: ResultWarning, SystemMessage: "three todos\nare overdue"}).String(), `SessionStart hook "greeting" warned (0s): three todos are overdue`; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	contains(t, "the line", lines[ResultCancelled], "was cancelled")
	if len(lines) != 9 {
		t.Errorf("two results share a line: %v", lines)
	}

	// Only the start of a long stderr is kept in the record.
	long := newRunner(t, options(t), Hook{Event: Stop, Command: `i=0; while [ $i -lt 400 ]; do echo "line $i of the log" >&2; i=$((i+1)); done; exit 0`})
	rec = run(t, long, input(Stop)).Records[0]
	if !strings.HasPrefix(rec.Stderr, "line 0 of the log\nline 1 of the log\n") || !strings.HasSuffix(rec.Stderr, "[...]") || len([]rune(rec.Stderr)) > stderrStart+10 {
		t.Errorf("Stderr is %d characters: %q...", len(rec.Stderr), rec.Stderr[:min(80, len(rec.Stderr))])
	}
}
