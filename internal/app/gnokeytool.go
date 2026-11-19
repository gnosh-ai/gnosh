package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/shellquote"
)

// This file is the gnokey tool (spec/gnosh/harness-tools.md, TOOL-40 and
// TOOL-41; Jae asked for "a real Gno integration (gno toolchain, realm
// deploy, gnokey signing)"). The model runs gnokey, gno.land's tool for
// keys and transactions, with no shell. When the command signs, gnosh asks
// the person for the key's password at a masked prompt that names the key
// and shows the exact command, hands the password to gnokey on its
// standard input (by gnokey's own flag for that, which gnosh puts in
// itself), and takes it out of what gnokey printed. The model never sees
// the password, and neither do the session's file, its log or a hook.
// Commands that make, change or hand out keys are refused: they stay in
// the person's own terminal. Only a session somebody attends has the tool,
// and only where gnokey is installed; the shell refuses the password flag
// (shelltool), so that nothing but this tool's prompt can sign.

const gnokeyToolName = "gnokey"

const (
	// gnokeyPasswordFlag is gnokey's flag for a password on its standard
	// input: the one way it takes a password without a terminal.
	gnokeyPasswordFlag = "-insecure-password-stdin"
	// gnokeyOutput is the most of gnokey's output the model is given, and
	// gnokeyWait how long a gnokey command may run.
	gnokeyOutput = 30000
	gnokeyWait   = 5 * time.Minute
)

// gnokeyRuns are the gnokey commands the tool runs; gnokeyKeys are those
// that make, change or hand out keys, which it refuses by name. gnokey
// reads a command's name in any letter case.
var (
	gnokeyRuns = []string{"query", "list", "sign", "verify", "broadcast", "maketx", "version"}
	gnokeyKeys = []string{"add", "delete", "rotate", "import", "export", "generate", "recover"}
)

// gnokeyBools are gnokey's flags that take no value of their own; every
// other flag written without = takes the next word as its value (gnokey
// of November 2026, and of March 2026).
var gnokeyBools = []string{"broadcast", "quiet", "insecure-password-stdin", "dry-run", "prove", "session", "h", "help"}

// gnokeyMainnet names gno.land's main network, by its chain ID and the
// host of its node: what is signed there is real.
var (
	gnokeyMainnetIDs  = []string{"gnoland1", "gnoland-1"}
	gnokeyMainnetHost = "rpc.gno.land"
)

const gnokeyDescription = `Runs gnokey, gno.land's tool for keys and transactions, with the command line you give, and returns what it printed. There is no shell: give gnokey's words alone, quoted as you would type them, with no pipe, redirection or variable.

- Queries (gnokey query), listing keys (gnokey list), verifying, broadcasting a transaction that is already signed, and making one with -broadcast=false (maketx then prints it unsigned) run as they are.
- A command that signs (maketx, which signs and broadcasts unless given -broadcast=false; gnokey sign) asks the user for the key's password at a masked prompt that shows them the exact command. You never see the password: never ask the user for it, never put it in a command, and never try to sign in the shell. Do not pass -insecure-password-stdin: gnosh adds what is needed. If the user does not give the password, nothing is signed; say so and stop.
- Making, changing or handing out keys (add, delete, rotate, import, export, generate, recover) is refused: tell the user the command to run in their own terminal.

Put the command right after gnokey (gnokey maketx call ..., not gnokey -home x maketx ...); gnokey -config is refused, so give every flag on the command line. On gno.land's main network (chain ID gnoland1) a transaction moves real value: say so before you sign there.`

var gnokeySchema = json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"The gnokey command line, beginning with gnokey, as it would be typed."},"description":{"type":"string","description":"One line saying what the command does, for the user."},"working_dir":{"type":"string","description":"Optional: the directory to run it in, such as one that holds a package to add. The working directory when left out."}},"required":["command","description"],"additionalProperties":false}`)

// gnokeyTool is the gnokey tool of one session: bin is gnokey's path, its
// links followed, and was what the file there was when the session
// started, which it must still be when a call runs (a model that rebuilt
// or replaced gnokey would be handed the password).
type gnokeyTool struct {
	l   *live
	bin string
	was os.FileInfo
}

// newGnokeyTool is the tool for the gnokey at path, or nil when the file
// cannot be looked at.
func newGnokeyTool(l *live, path string) *gnokeyTool {
	bin, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil
	}
	info, err := os.Stat(bin)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	return &gnokeyTool{l: l, bin: bin, was: info}
}

// gnokeyCall is a call of the tool, read: gnokey's arguments, the command
// they are (with maketx's own, for maketx), and, for one that signs, the
// key, the master account a session key acts for, and whether the chain
// is gno.land's main one; head is how many of the arguments name the
// command, after which gnosh puts the password's flag.
type gnokeyCall struct {
	args        []string
	command     string
	head        int
	signs       bool
	key         string
	master      string
	mainnet     bool
	dir         string
	description string
}

func (t *gnokeyTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: gnokeyToolName, Description: gnokeyDescription, InputSchema: gnokeySchema}
}

// read reads a call as gnokey reads its command line, and says in words
// for the model what is wrong with it when something is.
func (t *gnokeyTool) read(input json.RawMessage, cwd string) (gnokeyCall, string) {
	var in struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		WorkingDir  string `json:"working_dir"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return gnokeyCall{}, fmt.Sprintf("the arguments cannot be read (%v): command is the gnokey command line, description one line saying what it does", err)
	}
	words, err := shellquote.Split(strings.TrimSpace(in.Command))
	if err != nil {
		return gnokeyCall{}, "the command cannot be read as gnokey's words: " + err.Error()
	}
	if len(words) == 0 || words[0] != gnokeyToolName {
		return gnokeyCall{}, `the command must begin with gnokey: give the gnokey command line as it would be typed ("gnokey query auth/accounts/g1...")`
	}
	c := gnokeyCall{args: words[1:], description: strings.TrimSpace(in.Description), dir: cwd}
	for _, w := range c.args {
		if strings.ContainsAny(w, "\n\r") {
			return c, "a word of the command holds a line break, which the user would not be shown as one command: write the command on one line"
		}
	}
	if len(c.args) == 0 || strings.HasPrefix(c.args[0], "-") {
		return c, "put gnokey's command right after gnokey, before any flag (gnokey query ..., gnokey maketx call ...)"
	}
	name := strings.ToLower(c.args[0])
	switch {
	case slices.Contains(gnokeyKeys, name):
		return c, fmt.Sprintf("gnokey %s makes, changes or hands out a key, which stays in the user's own terminal: tell the user the command to run there", name)
	case !slices.Contains(gnokeyRuns, name):
		return c, fmt.Sprintf("gnokey %s is not a command this tool runs; it runs gnokey %s", c.args[0], strings.Join(gnokeyRuns, ", "))
	}
	c.command, c.head = name, 1
	if name == "maketx" {
		if len(c.args) < 2 || strings.HasPrefix(c.args[1], "-") {
			return c, "put maketx's own command right after it (gnokey maketx call, run, send, addpkg or session)"
		}
		c.command, c.head = "maketx "+strings.ToLower(c.args[1]), 2
		if !slices.Contains([]string{"maketx call", "maketx run", "maketx send", "maketx addpkg", "maketx session"}, c.command) {
			return c, fmt.Sprintf("gnokey maketx %s is not a command this tool runs; maketx's are call, run, send, addpkg and session", c.args[1])
		}
	}
	// The flags and their values, and the words that are neither.
	flags := map[string][]string{}
	var positional []string
	rest := c.args[c.head:]
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if w == "--" {
			positional = append(positional, rest[i+1:]...)
			break
		}
		if !strings.HasPrefix(w, "-") || w == "-" {
			positional = append(positional, w)
			continue
		}
		fname, value, set := strings.Cut(strings.TrimLeft(w, "-"), "=")
		if !set && !slices.Contains(gnokeyBools, fname) && i+1 < len(rest) {
			i++
			value = rest[i]
		}
		flags[fname] = append(flags[fname], value)
	}
	switch {
	case flags["insecure-password-stdin"] != nil:
		return c, "the command passes gnokey's " + gnokeyPasswordFlag + " itself, which only gnosh may do: leave it out. When the command signs, gnosh asks the user for the key's password and hands it to gnokey itself"
	case flags["config"] != nil:
		return c, "gnokey -config reads flags from a file, which the user would not be shown: give every flag on the command line instead"
	}
	switch {
	case c.command == "sign":
		c.signs = true
	case strings.HasPrefix(c.command, "maketx "):
		off := false
		for _, v := range flags["broadcast"] {
			off = slices.Contains([]string{"false", "0", "f", "F", "FALSE", "False"}, v)
		}
		c.signs = !off
	}
	if c.signs {
		// gnokey's key is the first word that is no flag: the only one for
		// sign and most of maketx's, the one before the file for maketx
		// run.
		if len(positional) == 0 {
			return c, "the command signs, and the key's name is missing: gnokey takes the key that signs after the flags"
		}
		c.key = positional[0]
		if m := flags["master"]; len(m) > 0 {
			c.master = m[len(m)-1]
		}
	}
	for _, v := range flags["chainid"] {
		c.mainnet = c.mainnet || slices.Contains(gnokeyMainnetIDs, v)
	}
	for _, v := range flags["remote"] {
		host := v
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			host = u.Host
		}
		if h, _, _ := strings.Cut(host, ":"); strings.EqualFold(h, gnokeyMainnetHost) {
			c.mainnet = true
		}
	}
	if d := strings.TrimSpace(in.WorkingDir); d != "" {
		if !filepath.IsAbs(d) {
			d = filepath.Join(cwd, d)
		}
		if info, err := os.Stat(d); err != nil || !info.IsDir() {
			return c, fmt.Sprintf("working_dir %s is not a directory that is there", d)
		}
		c.dir = filepath.Clean(d)
	}
	// A file gnokey is to read that is no file, such as standard input
	// under another name, would read the password.
	words = slices.Clone(positional)
	for _, vs := range flags {
		words = append(words, vs...)
	}
	for _, w := range words {
		if w == "" {
			continue
		}
		p := w
		if !filepath.IsAbs(p) {
			p = filepath.Join(c.dir, p)
		}
		if info, err := os.Stat(p); err == nil && !info.Mode().IsRegular() && !info.IsDir() {
			return c, fmt.Sprintf("%s is no file but a device, a pipe or a socket, which gnokey would read the password from: name a file", w)
		}
	}
	return c, ""
}

// line is the command as gnokey is run with it, without the password
// flag gnosh adds: what the person is shown.
func (c gnokeyCall) line() string {
	return gnokeyToolName + " " + shellquote.Words(c.args)
}

// Describe says what a call would run, so that the person is asked about
// the command itself, as for a shell command.
func (t *gnokeyTool) Describe(call core.ToolCall, env *core.ToolEnv) (core.Action, error) {
	c, wrong := t.read(call.Input, env.Cwd)
	act := core.Action{Kind: core.ActionExecute, Summary: firstOf(c.description, "Run gnokey")}
	if len(c.args) > 0 {
		act.Command = c.line()
	}
	if wrong != "" {
		return act, errors.New(wrong)
	}
	return act, nil
}

// Run runs gnokey, asking for the key's password first when the command
// signs.
func (t *gnokeyTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	c, wrong := t.read(call.Input, env.Cwd)
	if wrong != "" {
		return core.ErrorResult("gnokey was not run: " + wrong + "."), nil
	}
	if now, err := os.Stat(t.bin); err != nil || !sameFile(t.was, now) {
		return core.ErrorResult("gnokey was not run: " + t.bin + " is not the gnokey this session started with (it was changed, replaced or removed since), and gnosh hands a password only to that one. Tell the user; a new session takes the gnokey there now."), nil
	}
	args := c.args
	var password string
	if c.signs {
		a := agent.FromContext(ctx)
		if a == nil {
			return core.ToolResult{}, errors.New("the gnokey tool was run outside a session")
		}
		var body []string
		if c.mainnet {
			body = append(body, "This is gno.land's main network: what it signs moves real value.")
		}
		who := "the key " + c.key
		if c.master != "" {
			who += ", for the account " + c.master
		}
		body = append(body, "gnokey is to sign with "+who+":", "  "+oneLine(c.line(), 600))
		if c.dir != env.Cwd {
			body = append(body, "It runs in "+t.l.tilde(c.dir)+".")
		}
		body = append(body, "The password goes to gnokey alone: the model, the session's file and its log never see it. Esc signs nothing and stops the turn.")
		ans, err := a.Ask(ctx, agent.Question{Kind: secretKind, Title: "Password for the key " + c.key, Text: strings.Join(body, "\n"), CallID: call.ID, Tool: gnokeyToolName})
		switch {
		case errors.Is(err, agent.ErrNobodyToAsk):
			return core.ErrorResult("gnokey was not run: the command signs, and nobody is at the terminal to type the key's password. Give the user the exact command to run in their own terminal, and stop."), nil
		case err != nil:
			return core.ToolResult{}, err
		}
		if password = ans.Text; password == "" {
			return core.ErrorResult("gnokey was not run: no password was given, so nothing was signed. Say so, and stop."), nil
		}
		// The flag goes right after the command's name, before its other
		// flags, where gnokey reads every flag.
		args = slices.Insert(slices.Clone(args), c.head, gnokeyPasswordFlag)
	}
	runCtx, cancel := context.WithTimeout(ctx, gnokeyWait)
	defer cancel()
	cmd := exec.CommandContext(runCtx, t.bin, args...)
	cmd.Dir = c.dir
	cmd.Env = t.l.ownCommandEnv()
	// gnokey runs without the terminal, as the shell's commands do: it
	// reads nothing typed at gnosh's.
	noTerminal(cmd)
	if c.signs {
		cmd.Stdin = strings.NewReader(password + "\n")
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	text := scrubPassword(out.String(), password)
	password = ""
	if len(text) > gnokeyOutput {
		text = text[:gnokeyOutput] + fmt.Sprintf("\n[%d more bytes of gnokey's output were left out]", len(text)-gnokeyOutput)
	}
	text = strings.TrimRight(text, "\n")
	var exit *exec.ExitError
	switch {
	case runErr == nil:
		return core.TextResult(firstOf(text, "(gnokey printed nothing)")), nil
	case runCtx.Err() != nil && c.signs:
		// (Stopped after it had the password: it may have sent the
		// transaction already.)
		said := fmt.Sprintf("gnokey was stopped after it was given the password (it did not end within %s, or the turn was stopped), and the transaction may have been sent already. Check the account's sequence or the transaction before signing it again, or it may be paid for twice.", roundTime(gnokeyWait))
		if ctx.Err() != nil {
			return core.ErrorResult(said), nil
		}
		return core.ErrorResult(said + "\n" + text), nil
	case errors.As(runErr, &exit):
		said := fmt.Sprintf("gnokey exited with status %d.", exit.ExitCode())
		if runCtx.Err() != nil && ctx.Err() == nil {
			said = fmt.Sprintf("gnokey did not end within %s and was stopped.", roundTime(gnokeyWait))
		}
		if text != "" {
			said += "\n" + text
		}
		return core.ErrorResult(said), nil
	case ctx.Err() != nil:
		return core.ToolResult{}, ctx.Err()
	}
	return core.ErrorResult("gnokey could not be run: " + runErr.Error() + "."), nil
}

// scrubPassword takes a password out of what gnokey printed: as typed, and
// as JSON writes it. A password of fewer than three bytes is left: it
// would take out letters of every word.
func scrubPassword(text, password string) string {
	if len(password) < 3 {
		return text
	}
	text = strings.ReplaceAll(text, password, "[the password]")
	if quoted, err := json.Marshal(password); err == nil {
		if inner := string(quoted[1 : len(quoted)-1]); inner != password {
			text = strings.ReplaceAll(text, inner, "[the password]")
		}
	}
	return text
}
