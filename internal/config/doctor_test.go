package config

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeMachine is what doctor's probes see in a test: which addresses
// answer, which models each server lists, which programs and files are
// there. It records every request made to a model server.
type fakeMachine struct {
	mu       sync.Mutex
	down     []string            // addresses (substrings) that do not answer
	models   map[string][]string // provider ID → models its server lists
	programs []string
	files    []string
	requests []string
}

func (m *fakeMachine) probes() Probes {
	return Probes{
		Connect: func(ctx context.Context, url string) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.requests = append(m.requests, "connect "+url)
			for _, d := range m.down {
				if strings.Contains(url, d) {
					return errors.New("connection refused")
				}
			}
			return nil
		},
		ListModels: func(ctx context.Context, p Provider, e Endpoint) ([]string, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.requests = append(m.requests, "GET "+e.URL+"/models")
			list, ok := m.models[p.ID]
			if !ok {
				return nil, errors.New("HTTP 500")
			}
			return list, nil
		},
		LookPath: func(name string) (string, error) {
			if slices.Contains(m.programs, name) {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
		Exists: func(path string) bool { return slices.Contains(m.files, path) },
	}
}

// doctor loads a configuration and runs Doctor over it.
func doctor(t *testing.T, in Inputs, m *fakeMachine) []Finding {
	t.Helper()
	cfg, problems, _ := Load(in)
	findings := Doctor(context.Background(), cfg, problems, m.probes())
	for _, f := range findings {
		if strings.Contains(f.Text, "\n") || strings.TrimSpace(f.Text) == "" || f.Area == "" {
			t.Errorf("a finding is one line with an area: %+v", f)
		}
	}
	return findings
}

// wantFinding checks that a finding of the given area and level holds
// every piece.
func wantFinding(t *testing.T, findings []Finding, area string, level Level, pieces ...string) {
	t.Helper()
next:
	for _, f := range findings {
		if f.Area != area || f.Level != level {
			continue
		}
		for _, p := range pieces {
			if !strings.Contains(f.Text, p) {
				continue next
			}
		}
		return
	}
	var all []string
	for _, f := range findings {
		all = append(all, f.String())
	}
	t.Errorf("no %s finding (%s) holding %q; got:\n  %s", area, level, pieces, strings.Join(all, "\n  "))
}

// box is a file's worth of settings for one model server on this machine,
// with its model registered and chosen, without the braces around it, so
// that a test can add to it.
const box = `"providers": {"qwen": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8080"}}, "models": {"qwen/qwen3.8-27b": {"contextWindow": 262144}}, "model": "qwen/qwen3.8-27b"`

func TestDoctorAllWell(t *testing.T) {
	m := &fakeMachine{}
	findings := doctor(t, withConfig("{"+box+"}"), m)
	if !CanRun(findings) {
		t.Errorf("a good setup can run a turn: %v", findings)
	}
	for _, f := range findings {
		if f.Level != Fine {
			t.Errorf("a good setup has nothing to note: %s", f)
		}
	}
	wantFinding(t, findings, "config", Fine, "read "+userJSON, "user settings")
	wantFinding(t, findings, "providers", Fine, "qwen at http://127.0.0.1:8080/v1", "1 models")
	wantFinding(t, findings, "models", Fine, "model: qwen/qwen3.8-27b", "thinking on")
	wantFinding(t, findings, "models", Fine, "small model: not chosen", "the session's model does the side jobs")
	wantFinding(t, findings, "tools", Fine, "needs no outside program")
	wantFinding(t, findings, "trust", Fine, "no settings of its own")
	wantFinding(t, findings, "environment", Fine)
}

// Doctor must not disturb a running model server: it connects, and it
// asks for the list of models, and it asks nothing else of any server.
func TestDoctorOnlyListsModels(t *testing.T) {
	m := &fakeMachine{models: map[string][]string{"a": {"one", "two"}, "b": {"three"}}}
	text := `{"providers": {"a": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8080"}, "b": {"type": "openai-compatible", "baseUrl": "http://10.0.0.5:9000/v1"}},
  "model": "a/one", "smallModel": "b/three"}`
	findings := doctor(t, withConfig(text), m)
	if !CanRun(findings) {
		t.Errorf("findings: %v", findings)
	}
	want := []string{
		"connect http://127.0.0.1:8080/v1", "GET http://127.0.0.1:8080/v1/models",
		"connect http://10.0.0.5:9000/v1", "GET http://10.0.0.5:9000/v1/models",
	}
	if !slices.Equal(m.requests, want) {
		t.Errorf("doctor asked a server for more than its list of models:\n got %q\nwant %q", m.requests, want)
	}
	for _, r := range m.requests {
		for _, never := range []string{"/slots", "/props", "/chat", "/completions", "/tokenize", "/embeddings", "/models/"} {
			if strings.Contains(r, never) {
				t.Errorf("doctor sent %q, which can load or disturb a model", r)
			}
		}
	}
	wantFinding(t, findings, "providers", Fine, "a at http://127.0.0.1:8080/v1", "2 models")
	wantFinding(t, findings, "models", Fine, "model: a/one", "context window is not configured", "32768")
	wantFinding(t, findings, "models", Fine, "small model: b/three")
}

func TestDoctorProviders(t *testing.T) {
	// No provider at all: doctor shows what to write, and where.
	findings := doctor(t, inputs(newFS()), &fakeMachine{})
	if CanRun(findings) {
		t.Errorf("with no provider a turn cannot run")
	}
	wantFinding(t, findings, "providers", Fail, "no provider is configured", "Add one to "+userJSON, `{"providers": {"local": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8080"}}}`)
	wantFinding(t, findings, "config", Note, "no config file was found", userJSON)

	// A server that does not answer: the provider is left out, with the
	// reason, and a turn cannot run on nothing.
	findings = doctor(t, withConfig("{"+box+"}"), &fakeMachine{down: []string{"127.0.0.1:8080"}})
	wantFinding(t, findings, "providers", Note, "qwen is left out", "did not answer at http://127.0.0.1:8080/v1")
	wantFinding(t, findings, "providers", Fail, "none of the configured providers can be used")
	wantFinding(t, findings, "models", Fail, "model: qwen/qwen3.8-27b", "cannot be used right now")

	// Several addresses: doctor says which answered.
	two := `{"providers": {"qwen": {"type": "llamacpp", "baseUrl": ["http://192.168.1.9:8080", "http://127.0.0.1:18080"]}}, "models": {"qwen/m": {}}, "model": "qwen/m"}`
	findings = doctor(t, withConfig(two), &fakeMachine{down: []string{"192.168.1.9"}})
	wantFinding(t, findings, "providers", Fine, "qwen at http://127.0.0.1:18080/v1", "address 2 of 2", "http://192.168.1.9:8080/v1 did not answer")
	if !CanRun(findings) {
		t.Errorf("the second address answers, so a turn can run: %v", findings)
	}

	// Left out by the configuration, and disabled.
	more := strings.Replace(box, `"providers": {`, `"providers": {"claude": {"type": "anthropic"}, "off": {"type": "llamacpp", "disabled": true}, `, 1)
	findings = doctor(t, withConfig("{"+more+"}"), &fakeMachine{})
	wantFinding(t, findings, "providers", Note, "claude is left out", "ANTHROPIC_API_KEY")
	wantFinding(t, findings, "providers", Note, "off is disabled")
	if !CanRun(findings) {
		t.Errorf("one usable provider is enough: %v", findings)
	}

	// With no probes, nothing is claimed about the servers.
	cfg, problems, _ := Load(withConfig("{" + box + "}"))
	findings = Doctor(context.Background(), cfg, problems, Probes{})
	wantFinding(t, findings, "providers", Note, "was not checked")
}

// With more than one model available and none chosen, gnosh does not
// guess, and doctor says a turn cannot run.
func TestDoctorModels(t *testing.T) {
	const provider = `"providers": {"box": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8080"}}`
	several := &fakeMachine{models: map[string][]string{"box": {"qwen3.8-27b", "gpt-oss-120b"}}}
	findings := doctor(t, withConfig("{"+provider+"}"), several)
	if CanRun(findings) {
		t.Errorf("several models and none chosen: a turn cannot run")
	}
	wantFinding(t, findings, "models", Fail, "model: not chosen", "2 models are available", "does not guess", "box/qwen3.8-27b, box/gpt-oss-120b", "-m", `with "model" in gnosh.json`)

	single := &fakeMachine{models: map[string][]string{"box": {"qwen3.8-27b"}}}
	findings = doctor(t, withConfig("{"+provider+"}"), single)
	wantFinding(t, findings, "models", Fine, "box/qwen3.8-27b is the only model there is")
	if !CanRun(findings) {
		t.Errorf("one model: %v", findings)
	}

	// A server that answers and lists nothing.
	findings = doctor(t, withConfig("{"+provider+"}"), &fakeMachine{models: map[string][]string{"box": {}}})
	wantFinding(t, findings, "models", Fail, "model: not chosen, and no model is known", `register one under "models" in gnosh.json`)

	findings = doctor(t, withConfig("{"+provider+`, "model": "box/llama"}`), several)
	wantFinding(t, findings, "models", Fail, "model: provider box does not offer llama", "qwen3.8-27b, gpt-oss-120b")
	findings = doctor(t, withConfig("{"+provider+`, "model": "box/qwen3.8-27b", "smallModel": "box/llama"}`), several)
	wantFinding(t, findings, "models", Note, "small model: provider box does not offer llama")
	if !CanRun(findings) {
		t.Errorf("a small model that is missing stops nothing: %s", showFindings(findings))
	}

	// A model named without its provider on the command line.
	in := withConfig("{" + strings.Replace(provider, `"providers": {`, `"providers": {"other": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:8081"}, `, 1) + "}")
	in.Flags.Model = "gpt-oss-120b"
	findings = doctor(t, in, &fakeMachine{models: map[string][]string{"box": {"qwen3.8-27b", "gpt-oss-120b"}, "other": {"x"}}})
	wantFinding(t, findings, "models", Fine, "model: box/gpt-oss-120b", "--model")
	in.Flags.Model = "nothing-has-this"
	findings = doctor(t, in, &fakeMachine{models: map[string][]string{"box": {"a"}, "other": {"b"}}})
	wantFinding(t, findings, "models", Fail, "no provider is known to offer nothing-has-this")

	// A server whose list cannot be read.
	findings = doctor(t, withConfig("{"+provider+`, "model": "box/qwen"}`), &fakeMachine{})
	wantFinding(t, findings, "providers", Note, "list of models could not be read", "HTTP 500")
	wantFinding(t, findings, "models", Fine, "model: box/qwen")
}

// hookFile is a file with a hook of every kind doctor tells apart.
const hookFile = `{` + box + `,
  "hooks": {
    "Stop": [{"hooks": [
      {"type": "command", "command": "${CLAUDE_PROJECT_DIR}/hooks/tests.sh"},
      {"type": "command", "command": "$GNOSH_PROJECT_DIR/hooks/present.sh"}]}],
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "python3 ./hooks/guard.py --strict"}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "NO_COLOR=1 nosuchprogram --check"}]}],
    "PostToolUse": [
      {"matcher": "Edit", "hooks": [{"type": "command", "command": "gofmt -l ."}]},
      {"hooks": [{"type": "command", "command": "./hooks/missing.sh"}]}],
    "PermissionRequest": [{"hooks": [{"type": "command", "command": "./hooks/answer.sh"}]}],
    "SessionStart": [{"hooks": [{"type": "command", "command": "[ -f .env ] && echo ready"}]}],
    "PreModelSwitch": [{"hooks": [{"type": "command", "command": "./hooks/models.sh"}]}],
    "PostToolBatch": [{"hooks": [{"type": "command", "command": "./hooks/batch.sh"}]}]
  }
}`

// A hook that cannot run is a failure where it would block, and a note
// elsewhere.
func TestDoctorHooks(t *testing.T) {
	m := &fakeMachine{programs: []string{"python3"}, files: []string{"/work/proj/hooks/present.sh"}}
	findings := doctor(t, withConfig(hookFile), m)
	if CanRun(findings) {
		t.Errorf("a Stop hook that cannot run holds every turn open: a turn cannot run")
	}
	wantFinding(t, findings, "hooks", Fail, "Stop:", "the file /work/proj/hooks/tests.sh is not there", "A check that cannot run does not pass", "the end of every turn")
	wantFinding(t, findings, "hooks", Fail, "PreToolUse [Bash]", "the script /work/proj/hooks/guard.py is not there", "every tool call it matches")
	wantFinding(t, findings, "hooks", Fail, "UserPromptSubmit", "the program nosuchprogram is not installed", "every prompt")
	wantFinding(t, findings, "hooks", Note, "PostToolUse [Edit]", "the program gofmt is not installed")
	wantFinding(t, findings, "hooks", Note, "PostToolUse:", "the file /work/proj/hooks/missing.sh is not there")
	wantFinding(t, findings, "hooks", Note, "PermissionRequest", "every question it matches is put to you instead")
	wantFinding(t, findings, "hooks", Fine, "SessionStart", "echo ready")
	wantFinding(t, findings, "hooks", Fail, "PreModelSwitch", "every switch to a model it matches")
	wantFinding(t, findings, "hooks", Fail, "PostToolBatch", "every turn at its first round of tool calls")
	wantFinding(t, findings, "hooks", Fine, "Stop:", "present.sh")

	// With everything in place there is nothing to fail on.
	m = &fakeMachine{programs: []string{"python3", "gofmt", "nosuchprogram"}, files: []string{
		"/work/proj/hooks/present.sh", "/work/proj/hooks/tests.sh", "/work/proj/hooks/guard.py", "/work/proj/hooks/missing.sh", "/work/proj/hooks/answer.sh",
		"/work/proj/hooks/models.sh", "/work/proj/hooks/batch.sh"}}
	if findings = doctor(t, withConfig(hookFile), m); !CanRun(findings) {
		t.Errorf("with every program and script there, a turn can run: %v", findings)
	}

	// Hooks switched off cannot hold anything up.
	in := withConfig(hookFile)
	in.Flags.SafeMode = true
	findings = doctor(t, in, &fakeMachine{})
	if !CanRun(findings) {
		t.Errorf("in safe mode a hook that cannot run holds nothing up: %v", findings)
	}
	wantFinding(t, findings, "hooks", Note, "10 hooks are configured and none runs", "safe mode")
	wantFinding(t, findings, "config", Note, "safe mode is on", "--safe-mode", "hooks, instruction files, skills on disk and custom commands are off")
}

func TestDoctorTheRest(t *testing.T) {
	// Trust: what is held.
	in := inputs(untrustedProject())
	in.Trusted = false
	findings := doctor(t, in, &fakeMachine{})
	wantFinding(t, findings, "trust", Note, "not trusted", "23 of its settings are held", `"provider evil (openai-compatible) at https://203.0.113.5/v1"`, "and 18 more", "gnosh trust", "--trust-folder")
	wantFinding(t, findings, "config", Note, "held: "+projJSON, "the project's settings", "only what restricts the agent")
	wantFinding(t, findings, "config", Note, "held: "+localJSON)
	in.Trusted = true
	findings = doctor(t, in, &fakeMachine{})
	wantFinding(t, findings, "trust", Fine, "is trusted", "in force")
	in = inputs(newFS().put(projJSON, `{"permissions": {"deny": ["Bash"]}}`))
	in.Trusted = false
	wantFinding(t, doctor(t, in, &fakeMachine{}), "trust", Fine, "none of its settings waits for trust")

	// The environment: a value gnosh could not use.
	in = withConfig("{" + box + "}")
	in.Environ = append(in.Environ, "BASH_MAX_OUTPUT_LENGTH=lots")
	findings = doctor(t, in, &fakeMachine{})
	wantFinding(t, findings, "environment", Note, "$BASH_MAX_OUTPUT_LENGTH", `"lots"`)
	if !CanRun(findings) {
		t.Errorf("a bad value in one of Claude Code's variables stops nothing: %v", findings)
	}

	// A warning from loading is kept and shown.
	in = withConfig("{" + box + `, "permissions": {"defaultMode": "auto"}}`)
	wantFinding(t, doctor(t, in, &fakeMachine{}), "config", Note, userJSON+":1:", `"auto"`, "asks more, not less")

	// A provider whose variable is not set is said once, with the
	// providers, and the reason is whole.
	in = withConfig("{" + strings.Replace(box, `"providers": {`, `"providers": {"x": {"type": "llamacpp", "baseUrl": "http://${NOPE}:1"}, `, 1) + "}")
	findings = doctor(t, in, &fakeMachine{})
	wantFinding(t, findings, "providers", Note, "x is left out", `its "baseUrl" is filled in from the environment variable NOPE, which is not set`)
	for _, f := range findings {
		if f.Area == "config" && strings.Contains(f.Text, "NOPE") {
			t.Errorf("the provider left out is said twice: %s", f)
		}
	}

	// The servers the options name for tools are connected to, and sent
	// nothing; one that does not answer is a note, and stops no turn.
	m := &fakeMachine{models: map[string][]string{"qwen": {"qwen3.8-27b"}}}
	in = withConfig("{" + box + `, "options": {"webSearchUrl": "http://203.0.113.20:8888/?token=s3cret", "memEmbeddingsUrl": "http://203.0.113.21:8090"}}`)
	findings = doctor(t, in, m)
	wantFinding(t, findings, "tools", Fine, "WebSearch's search server at http://203.0.113.20:8888/?… answers.", userJSON)
	wantFinding(t, findings, "tools", Fine, "mem's embedding server at http://203.0.113.21:8090 answers.")
	if !slices.Contains(m.requests, "connect http://203.0.113.21:8090") || slices.ContainsFunc(m.requests, func(r string) bool { return strings.HasPrefix(r, "GET http://203.0.113.2") }) {
		t.Errorf("the tools' servers are only connected to: %q", m.requests)
	}
	m.down = []string{"203.0.113.20", "203.0.113.21"}
	findings = doctor(t, in, m)
	wantFinding(t, findings, "tools", Note, "WebSearch's search server at http://203.0.113.20:8888/?… did not answer (connection refused): every search fails until it does.")
	wantFinding(t, findings, "tools", Note, "mem's embedding server at http://203.0.113.21:8090 did not answer", "mem's searches match words only until it does.")
	if !CanRun(findings) {
		t.Errorf("a tool's server that does not answer stops a turn:\n%s", showFindings(findings))
	}
	for _, f := range findings {
		if strings.Contains(f.Text, "s3cret") {
			t.Errorf("the address's query is shown: %s", f)
		}
	}
	findings = Doctor(context.Background(), mustLoad(t, in), nil, Probes{})
	wantFinding(t, findings, "tools", Note, "WebSearch's search server is http://203.0.113.20:8888/?…", "Whether it answers was not checked.")

	// A configuration that does not load is printed with its mistakes,
	// and a turn cannot run.
	findings = doctor(t, withConfig("{\n\"provders\": {},\n\"options\": {\"nope\": 1}}"), &fakeMachine{})
	if CanRun(findings) || len(findings) != 2 {
		t.Errorf("a refused configuration: %v", findings)
	}
	wantFinding(t, findings, "config", Fail, userJSON+":2:", "not a key gnosh knows")
	wantFinding(t, findings, "config", Fail, userJSON+":3:", "not an option")
}

// Doctor lists every declared host and reaches none of them unless it was
// asked to: a host that was not tried is never called fine.
func TestDoctorHosts(t *testing.T) {
	in := withConfig("{" + box + `, "hosts": {"render": {"ssh": "root@203.0.113.10", "description": "the render box"}, "web": {"ssh": "deploy@203.0.113.11"}}}`)
	m := &fakeMachine{models: map[string][]string{"qwen": {"qwen3.8-27b"}}, programs: []string{"ssh"}}

	// Not asked: listed, not tried, and not marked ok.
	findings := doctor(t, in, m)
	wantFinding(t, findings, "hosts", Note, "render → root@203.0.113.10 (the render box) is declared ("+userJSON+":", "It was not tried", "gnosh doctor --hosts logs in to each host with ssh and runs nothing there")
	wantFinding(t, findings, "hosts", Note, "web → deploy@203.0.113.11 is declared", "It was not tried")
	for _, f := range findings {
		if f.Area == "hosts" && f.Level == Fine {
			t.Errorf("a host that was not tried is reported as fine: %s", f)
		}
	}
	if !CanRun(findings) {
		t.Errorf("hosts that were not tried stop a turn:\n%s", showFindings(findings))
	}

	// Asked: each host is tried, and what was found is said.
	var tried []string
	pr := m.probes()
	pr.TryHost = func(ctx context.Context, h Host) (string, string) {
		tried = append(tried, h.Name+"="+h.SSH)
		if h.Name == "web" {
			return "", "web (deploy@203.0.113.11) refused the connection: nothing answers ssh at its address. Nothing was run on web"
		}
		return "/root", ""
	}
	cfg := mustLoad(t, in)
	findings = Doctor(context.Background(), cfg, nil, pr)
	if !slices.Equal(tried, []string{"render=root@203.0.113.10", "web=deploy@203.0.113.11"}) {
		t.Errorf("tried %q", tried)
	}
	wantFinding(t, findings, "hosts", Fine, "render → root@203.0.113.10 (the render box) answers: a command there starts in /root.")
	wantFinding(t, findings, "hosts", Note, "web → deploy@203.0.113.11 cannot be used right now: web (deploy@203.0.113.11) refused the connection", "Nothing was run on web.")
	if !CanRun(findings) {
		t.Errorf("a host that cannot be reached stops a turn:\n%s", showFindings(findings))
	}

	// With no ssh program, no host can be used, and none is tried.
	tried = nil
	m.programs = nil
	pr = m.probes()
	pr.TryHost = func(ctx context.Context, h Host) (string, string) {
		tried = append(tried, h.Name)
		return "", ""
	}
	findings = Doctor(context.Background(), cfg, nil, pr)
	wantFinding(t, findings, "hosts", Note, "render → root@203.0.113.10 (the render box) cannot be used: the program ssh is not installed")
	if len(tried) != 0 {
		t.Errorf("hosts were tried without an ssh program: %q", tried)
	}

	// No hosts: nothing is said of them.
	for _, f := range doctor(t, withConfig("{"+box+"}"), m) {
		if f.Area == "hosts" {
			t.Errorf("a finding about hosts with none declared: %s", f)
		}
	}
}

// showFindings lays findings out for a failure message.
func showFindings(findings []Finding) string {
	var all []string
	for _, f := range findings {
		all = append(all, f.String())
	}
	return strings.Join(all, "\n")
}
