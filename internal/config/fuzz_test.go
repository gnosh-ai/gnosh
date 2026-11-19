package config

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// fuzzSeeds are config files of every shape the tests know, good and bad.
var fuzzSeeds = []string{
	websiteFile, aiBoxFile, macFile, userOwn, pastedSettings, hookFile,
	"{}", "[]", `"x"`, "null", "", "{", `{"a":`, "{\n\"env\": {},\n}", "{ // c\n}", "/* open",
	`{"providers": {"x": {"type": "llamacpp", "baseUrl": ["http://127.0.0.1:1", "http://${HOME}:2/${NOPE:-v1}"], "apiKey": "${NOPE}", "extraBody": {"id_slot": 1, "a": {"b": [1, null]}}, "headers": {"A": "b ${HOME}"}, "listModels": false, "disabled": true, "name": "X"}}}`,
	`{"providers": {"x": null, "y": {"type": null}, "z": {"baseUrl": null, "apiKey": null, "headers": null, "extraBody": null, "listModels": null, "disabled": null, "name": null}}}`,
	`{"providers": {"x": {"type": "anthropic", "apiKey": "${", "baseUrl": "${A B}", "headers": {"X": "${CLAUDE_PROJECT_DIR}"}}}}`,
	`{"providers": {"p": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:1"}}, "models": {"p/m": {"name": "M", "contextWindow": 1, "maxOutputTokens": 2, "thinkingLevels": ["off", "on"]}, "p/n": {}, "p/o": null, "q/r": {}}, "model": {"id": "p/m", "thinking": "low", "temperature": 0.5}, "smallModel": "p/n"}`,
	`{"model": null, "smallModel": {"id": null, "thinking": null, "temperature": null}}`, `{"model": {}, "smallModel": []}`, `{"model": "nope", "smallModel": {"thinking": true}}`,
	`{"providers": {"p": {"type": "llamacpp", "baseUrl": "http://127.0.0.1:1"}}, "model": "p/m", "fallbackModel": ["p/n", "q/r", "p/n"]}`,
	`{"fallbackModel": null}`, `{"fallbackModel": []}`, `{"fallbackModel": "x"}`, `{"fallbackModel": [1, {}, "a/b", "c/d", "e/f", "g/h"]}`, `{"fallbackModel": {"id": "p/m"}}`,
	`{"hosts": {"h": {"ssh": "a@203.0.113.10", "description": "d"}, "local": {"ssh": "x"}, "g": null, "bad name": {"ssh": "-o x"}}}`,
	`{"options": {"requestTimeout": 900, "autoCompactEnabled": false, "autoCompactWindow": 200000, "loopMaxTurns": 3, "maxBackgroundTasks": 2, "userInstructionFiles": ["~/a", "b"], "projectInstructionFiles": ["c"], "hiddenSkills": ["x"], "memoryText": "full", "memReviewCloses": false, "memEmbeddingsUrl": "http://127.0.0.1:1", "deliverablesServer": "127.0.0.1:0", "debug": true, "projectDataDir": ".x"}}`,
	`{"options": {"requestTimeout": null, "hiddenSkills": null, "debug": "yes", "nope": 1, "autoCompactWindow": 1e5, "memEmbeddingsUrl": "http://${X}", "deliverablesServer": "0.0.0.0:1"}}`,
	`{"permissions": {"allow": ["Read"], "deny": ["Bash(rm *)"], "ask": [], "defaultMode": "plan", "additionalDirectories": ["/x"]}}`,
	`{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "x", "timeout": 5, "async": false}]}]}}`,
	`{"hooks": {"PreToolUse": [{"matcher": "(", "hooks": [{"type": "http", "if": "x", "args": []}]}]}}`,
	`{"env": {"A": "b", "A": "c", "1": 2}, "disableAllHooks": true, "nope": [[[[]]]]}`,
	`{"permissions": {"defaultMode": "auto", "disableBypassPermissionsMode": "disable", "denyInsteadOfPrompting": true}}`,
	`{"statusLine": {}, "requestTimeout": 1, "autoCompactWindow": 2, "$schema": "x"}`,
	strings.Repeat("[", 200), strings.Repeat(`{"a":`, 100),
	"{\"hooks\": {\"Stop\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"\\u0000\\u001b\"}]}]}}",
	"{\"models\": {\"p/\\u001b[31m\": {}}, \"hosts\": {\"h\": {\"ssh\": \"a@b\", \"description\": \"\\u001b[31m\"}}, \"options\": {\"projectDataDir\": \"\\u0007\"}}",
	"{\"providers\": {\"\xff\xfe\": {}}}",
}

// FuzzConfigFile: whatever a config file holds, reading it never panics,
// every problem it reports names the file and a line of it, and the load
// ends in a Config or in mistakes. And whatever an untrusted folder's
// files hold, they change nothing but what such a folder may: the
// configuration is the one there would be without them, but for deny
// rules, ask rules and hidden skills.
func FuzzConfigFile(f *testing.F) {
	for _, seed := range fuzzSeeds {
		f.Add(seed)
	}
	env := func(name string) (string, bool) {
		if name == "HOME" {
			return "/h", true
		}
		return "", false
	}
	bare := inputs(newFS().put(userJSON, userOwn))
	bare.Trusted = false
	without, _, err := Load(bare)
	if err != nil {
		f.Fatalf("the person's own file of this test does not load: %v", err)
	}
	f.Fuzz(func(t *testing.T, text string) {
		probs := &problems{}
		src := &source{file: "fuzz.json", layer: LayerUser, scope: "user", baseDir: "/c", ruleDir: "/c", home: "/h", cwd: "/w",
			tools: testTools, getenv: env, probs: probs, secrets: &secrets{}}
		readFile(src, []byte(text))
		lines := strings.Count(text, "\n") + 1
		for _, p := range probs.list {
			if p.File != "fuzz.json" || p.Line < 1 || p.Line > lines || strings.TrimSpace(p.Message) == "" {
				t.Fatalf("a problem without its file, a line of the file or its words: %+v\nfor %q", p, text)
			}
		}
		for _, o := range src.ops {
			if o.kind == "" || o.text == "" || o.apply == nil || o.origin.File != "fuzz.json" || o.origin.Line < 1 || o.origin.Line > lines {
				t.Fatalf("a setting without its kind, its words or its place: %+v\nfor %q", o, text)
			}
		}

		// The whole way through Load: as the user's own file, in a trusted
		// folder as the project's files, and as what --settings gives.
		in := inputs(newFS().put(userJSON, text).put(projJSON, text).put(localJSON, text))
		if strings.HasPrefix(strings.TrimSpace(text), "{") {
			in.Flags.Settings = text
		}
		cfg, found, err := Load(in)
		if (cfg == nil) != (err != nil) {
			t.Fatalf("Load: cfg %v, err %v", cfg != nil, err)
		}
		for _, p := range found {
			if p.File == "" && p.Where == "" {
				t.Fatalf("a problem that says nowhere: %+v\nfor %q", p, text)
			}
		}
		if cfg != nil {
			_ = cfg.String()
			_ = cfg.HooksJSON()
		}

		// In a folder that is not trusted, as the project's two files
		// beside the person's own.
		in = inputs(newFS().put(userJSON, userOwn).put(projJSON, text).put(localJSON, text))
		in.Trusted = false
		cfg, _, err = Load(in)
		if (cfg == nil) != (err != nil) {
			t.Fatalf("Load in an untrusted folder: cfg %v, err %v", cfg != nil, err)
		}
		if cfg != nil {
			sameButRestrictions(t, without, cfg, []string{projJSON, localJSON})
			if t.Failed() {
				t.Fatalf("for the project file %q", text)
			}
		}
	})
}

// variable finds every ${…} of a text the way the reader is said to:
// from a ${ to the first } after it.
var variable = regexp.MustCompile(`\$\{([^}]*)\}`)

// FuzzFill holds the filling in of variables to a second way of doing it.
// Whatever the text: it never panics; text with no ${ comes back as it is;
// every ${NAME} and ${NAME:-default} is replaced, by the variable when it
// is set and by the default when it is not; and a ${ that is not closed,
// or that holds something other than a name, is a mistake.
func FuzzFill(f *testing.F) {
	for _, seed := range []string{
		"", "plain", "$HOME", "${HOME}", "a${HOME}b${HOME}", "${NOPE}", "${NOPE:-}", "${NOPE:-a default}", "${EMPTY:-not used}", "${HOME:-x}${NOPE:-y}",
		"${", "${}", "${A B}", "${1A}", "${HOME", "$${HOME}", "${HOME}}", "${NOPE:-a}b}", "${NOPE:?must}", "${NOPE-x}", "${NOPE:-${HOME}}", "${$(pass)}",
		"${CLAUDE_PROJECT_DIR}", "${GNOSH_SESSION_ID:-x}", "http://${HOST:-127.0.0.1}:${PORT:-8080}/v1", "Bearer ${KEY}", "\xff${HOME}\xfe", "${HOME}\n${NOPE}",
	} {
		f.Add(seed)
	}
	set := map[string]string{"HOME": "/h", "EMPTY": "", "KEY": "k-0123456789", "HOST": "box.example.com"}
	getenv := func(name string) (string, bool) {
		v, ok := set[name]
		return v, ok
	}
	f.Fuzz(func(t *testing.T, text string) {
		got, why := fill(text, getenv)

		// The second way: every ${…} in turn, then a look for a ${ that
		// was left.
		wrong := false
		var vars, unset []string
		at := 0
		var b strings.Builder
		for _, m := range variable.FindAllStringSubmatchIndex(text, -1) {
			if strings.Contains(text[at:m[0]], "${") {
				break
			}
			b.WriteString(text[at:m[0]])
			at = m[1]
			name, def, hasDefault := strings.Cut(text[m[2]:m[3]], ":-")
			if !isName(name) || runTimeVariable(name) {
				wrong = true
				break
			}
			if !slices.Contains(vars, name) {
				vars = append(vars, name)
			}
			switch value, ok := set[name]; {
			case ok:
				b.WriteString(value)
			case hasDefault:
				b.WriteString(def)
			case !slices.Contains(unset, name):
				unset = append(unset, name)
			}
		}
		if !wrong && strings.Contains(text[at:], "${") {
			wrong = true
		}
		b.WriteString(text[at:])

		if wrong != (why != "") {
			t.Fatalf("fill(%q): mistake %q, and the second way says wrong is %v", text, why, wrong)
		}
		if wrong {
			return
		}
		if got.text != b.String() || !slices.Equal(got.vars, vars) || !slices.Equal(got.unset, unset) {
			t.Fatalf("fill(%q) = %q, vars %q, unset %q; the second way gives %q, %q, %q", text, got.text, got.vars, got.unset, b.String(), vars, unset)
		}
		if !strings.Contains(text, "${") && (got.text != text || len(got.vars) != 0 || got.only != "") {
			t.Fatalf("fill(%q) changed text that holds no variable: %+v", text, got)
		}
		if got.only != "" {
			if value, ok := set[got.only]; !ok || got.text != value || len(got.vars) != 1 || !strings.HasPrefix(text, "${"+got.only) || !strings.HasSuffix(text, "}") {
				t.Fatalf("fill(%q) says the value is the one variable %s: %+v", text, got.only, got)
			}
		}
	})
}

// FuzzState: whatever the state file holds, reading it never panics and
// never stops startup.
func FuzzState(f *testing.F) {
	for _, seed := range []string{
		"", "{}", "{{{", `{"version": 1, "models": {"model": "p/m", "smallModel": "x"}, "thinking": {"p/m": "off", "q": "LOUD"}, "autoCompactWindow": 5}`,
		`{"models": "p/m"}`, `{"thinking": ["x"]}`, `[1, 2]`, `{"version": 99, "future": {"a": [1]}}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		const user = "{" + one + `, "models": {"p/m": {"thinkingLevels": ["off", "on"]}}}`
		cfg, found, err := Load(inputs(newFS().put(userJSON, user).put(statePath, text)))
		if err != nil || cfg == nil {
			t.Fatalf("a state file stopped startup: %v\n%s\nfor %q", err, showProblems(found), text)
		}
		if st, _ := ReadState(newFS().put(statePath, text), statePath); st.check() == nil {
			// A State the reader gives back whole can be written again.
			if err := WriteState(newFS().put(statePath, ""), statePath, st); err != nil {
				t.Fatalf("a State that was read cannot be written: %v", err)
			}
		}
	})
}
