package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/diff"
	"github.com/gnosh-ai/gnosh/internal/hooks"
	"github.com/gnosh-ai/gnosh/internal/term"
	"github.com/gnosh-ai/gnosh/internal/text"
	"github.com/gnosh-ai/gnosh/internal/tools/shelltool"
)

// plain is the interactive session at its plainest: it reads a line at a
// time and writes lines, with no raw mode and no cursor movement, so that
// it works in any terminal, over ssh, inside tmux and through a pipe.
// Everything it prints is written once and stays in the terminal's own
// scrollback, where it can be selected, copied and scrolled.
//
// What it shows: the model's words as they arrive; its reasoning, set
// apart; each tool call as one line when it starts and a short result
// when it ends; questions as numbered choices answered by a line of
// input; notices and warnings; and, when the model is silent for a while,
// what gnosh is waiting for. A line that begins with a slash is a command.
// Ctrl+C stops the turn, and a second one within three seconds quits.
//
// Where its lines come from: standard input when that is a terminal. When
// it is not, what was piped in is the session's first prompt, all of it,
// and the lines after that are typed at the terminal itself
// (Options.OpenTerminal); with no terminal to type at, the session does
// its piped prompt, answers no to every question, and ends.
//
// It stands in until the full interface is attached in its place
// (Options.Interactive), through the same Frontend interface.
type plain struct {
	// hookSaid is the reason of the last hook that blocked, when its
	// record said it whole: the note that the turn goes on does not say it
	// again.
	hookSaid string
	p        *program
	inv      cli.Invocation
	out      io.Writer
	// in hands out the lines a person types, or is nil when nobody can
	// type. piped is standard input when it is not a terminal, and term
	// the terminal that was opened to type at in its place.
	in    *lineReader
	piped io.Reader
	term  io.Closer
	// inputFd is the terminal the lines are typed at, as a file
	// descriptor, or -1 when it is not known to be one. restoreEcho, when
	// not nil, puts back the showing of what is typed, which is off while
	// a secret is being asked for.
	inputFd     int
	restoreEcho func()
	// shownTasks is the task list as it was last printed.
	shownTasks []Task
	// profile is how much colour the terminal takes, and width how wide a
	// change is drawn when a question shows one: the terminal's width when
	// the environment gives it (COLUMNS), and 80 otherwise.
	profile text.Profile
	width   int

	mu sync.Mutex
	c  Controls
	// atStart says the last thing written ended a line. kind is what is
	// being written as it arrives: "", "text", "thinking" or "prompt".
	atStart bool
	kind    string
	// question is the question that waits for a line of input, and form
	// the model's questions when that is what it holds: they are asked
	// one after another.
	question *agent.Question
	// behind are the questions behind the one being answered, the
	// oldest first.
	behind []agent.Question
	form   *plainForm
	// nobody says no more lines will come: questions are answered no.
	nobody bool
	// waiting ticks while a request is out and nothing has come back.
	waiting   *time.Timer
	waitSince time.Time
	// idle fires when a turn ended a while ago and nothing was typed, and
	// ask when a question has waited a while.
	idle *time.Timer
	ask  *time.Timer

	wake chan struct{}
	// restarting says a tool moved the session (RestartEvent): the loop
	// closes it, and gnosh starts again where it moved.
	restarting atomic.Bool
}

// lineReader hands out the lines of a stream, one at a time, to whoever
// asks next: the question before a session, and then the session.
type lineReader struct {
	once  sync.Once
	r     io.Reader
	lines chan string
}

// start begins reading, the first time a line is wanted.
func (lr *lineReader) start() {
	lr.once.Do(func() {
		go func() {
			defer close(lr.lines)
			br := bufio.NewReader(lr.r)
			for {
				s, err := br.ReadString('\n')
				if s != "" {
					lr.lines <- strings.TrimRight(s, "\r\n")
				}
				if err != nil {
					return
				}
			}
		}()
	})
}

// newPlain makes the plain front end.
func newPlain(p *program, inv cli.Invocation) *plain {
	f := &plain{p: p, inv: inv, out: p.stdout, atStart: true, wake: make(chan struct{}, 1), width: 80}
	f.profile = text.DetectProfile(func(name string) (string, bool) {
		v, ok := p.env[name]
		return v, ok
	}, p.o.StdoutIsTerminal)
	if n, err := strconv.Atoi(p.env["COLUMNS"]); err == nil && n >= 40 && n <= 400 {
		f.width = n
	}
	f.inputFd = -1
	var typed io.Reader
	switch {
	case p.o.StdinIsTerminal:
		typed = p.o.Stdin
		f.in = &lineReader{r: p.o.Stdin, lines: make(chan string)}
	default:
		f.piped = p.o.Stdin
		if p.o.OpenTerminal != nil {
			if term, err := p.o.OpenTerminal(); err == nil {
				typed = term
				f.term = term
				f.in = &lineReader{r: term, lines: make(chan string)}
			}
		}
	}
	if file, ok := typed.(interface{ Fd() uintptr }); ok {
		f.inputFd = int(file.Fd())
	}
	return f
}

// secretKind is the kind of a question whose answer is a secret: a
// passphrase, a key. What is typed for it is not shown while it is typed,
// and is never printed or logged afterwards.
const secretKind = "secret"

// hideTyping stops the terminal from showing what is typed, for a secret.
// It reports whether it could. The caller holds the lock.
func (f *plain) hideTyping() bool {
	if f.restoreEcho != nil {
		return true
	}
	if f.inputFd < 0 {
		// Not a terminal that gnosh can ask: a pipe shows nothing of what
		// passes through it.
		return false
	}
	restore, ok := echoOff(f.inputFd)
	if ok {
		f.restoreEcho = restore
	}
	return ok
}

// showTyping puts the showing of what is typed back. The caller holds the
// lock.
func (f *plain) showTyping() {
	if f.restoreEcho != nil {
		f.restoreEcho()
		f.restoreEcho = nil
	}
}

// canAsk reports whether somebody can type an answer.
func (f *plain) canAsk() bool { return f.in != nil }

// The marks the plain front end leads its own lines with.
const (
	markTool   = "* "
	markResult = "  = "
	markThink  = "  ~ "
	markHook   = "  hook: "
)

// visible makes text from a model, a tool or a file safe for a terminal:
// control characters other than the line break and the tab are written as
// visible escapes, so that nothing printed can act as a control sequence.
func visible(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\n' && c != '\t') || c == 0x7f || c == 0xc2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || r >= 0x80 && r <= 0x9f {
			// (The C1 controls, from U+0080, some terminals read as the
			// escape sequences they stand for.)
			fmt.Fprintf(&b, `\x%02x`, r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fresh ends whatever was being written as it arrived, so that the next
// thing starts on a line of its own. The caller holds the lock.
func (f *plain) fresh() {
	if !f.atStart {
		fmt.Fprintln(f.out)
		f.atStart = true
	}
	f.kind = ""
}

// say writes whole lines. The caller holds the lock.
func (f *plain) say(format string, args ...any) {
	f.fresh()
	fmt.Fprintln(f.out, strings.TrimRight(fmt.Sprintf(format, args...), "\n"))
}

// note writes one line of the front end's own.
func (f *plain) note(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.say(format, args...)
}

// flow writes a piece of text that arrives bit by bit, each of its lines
// led by prefix. The caller holds the lock.
func (f *plain) flow(kind, prefix, text string) {
	if text == "" {
		return
	}
	if f.kind != kind {
		f.fresh()
		f.kind = kind
	}
	text = visible(text)
	for text != "" {
		line, rest, found := strings.Cut(text, "\n")
		if f.atStart && (line != "" || found) {
			fmt.Fprint(f.out, prefix)
		}
		fmt.Fprint(f.out, line)
		if line != "" {
			f.atStart = false
		}
		if found {
			fmt.Fprintln(f.out)
			f.atStart = true
		}
		text = rest
	}
}

// poke tells Run that something it may be waiting for has happened.
func (f *plain) poke() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// AskBefore puts a question to the person before a session exists: the
// trust question, the choice of a model or of a session to resume.
func (f *plain) AskBefore(q agent.Question) (agent.Answer, error) {
	if f.in == nil {
		return agent.Answer{}, errors.New("nobody can be asked: there is no terminal to type an answer at")
	}
	f.mu.Lock()
	f.printQuestion(q)
	f.mu.Unlock()
	f.in.start()
	signals := f.p.o.Signals
	for {
		var line string
		select {
		case l, ok := <-f.in.lines:
			if !ok {
				return agent.Answer{}, errors.New("the input ended before the question was answered")
			}
			line = l
		case <-signals:
			return agent.Answer{}, errors.New("stopped before the question was answered")
		}
		f.mu.Lock()
		ans, good := f.parseAnswer(q, line)
		if !good {
			f.say("%s", f.answerHint(q))
		}
		f.mu.Unlock()
		if good {
			return ans, nil
		}
	}
}

// printQuestion writes a question with its choices numbered. The caller
// holds the lock.
func (f *plain) printQuestion(q agent.Question) {
	f.fresh()
	fmt.Fprintln(f.out)
	if q.Title != "" {
		fmt.Fprintln(f.out, "? "+visible(q.Title))
	}
	if q.Text != "" {
		fmt.Fprintln(f.out, visible(strings.TrimRight(q.Text, "\n")))
	}
	// A person who approves a change must see the change: the hunks that
	// differ, with a few lines around each. The lines come from the diff
	// package, which draws nothing a terminal could take as an instruction.
	if a := q.Action; a != nil && a.Preview != nil && a.Preview.Kind == "diff" {
		for _, line := range diff.Render(*a.Preview, diff.RenderOptions{Width: f.width, Profile: f.profile, MaxLines: 60}) {
			fmt.Fprintln(f.out, line)
		}
	}
	for i, c := range q.Choices {
		fmt.Fprintf(f.out, "  %d. %s\n", i+1, visible(c.Label))
	}
	switch {
	case q.Kind == secretKind:
		if f.hideTyping() {
			fmt.Fprintln(f.out, "Type it and Enter. What you type is not shown, and gnosh keeps it nowhere.")
		} else if f.inputFd >= 0 {
			fmt.Fprintln(f.out, "Type it and Enter. This terminal cannot be told to hide it: what you type will show on this screen. gnosh keeps it nowhere.")
		} else {
			fmt.Fprintln(f.out, "Type it and Enter. gnosh keeps it nowhere.")
		}
	case len(q.Choices) > 0:
		fmt.Fprintln(f.out, "Type a number and Enter. Words after the number go with the answer.")
	default:
		fmt.Fprintln(f.out, "Type your answer and Enter.")
	}
}

// plainForm is the model's questions (AskUserQuestion) while the plain
// session asks them: one at a time, each with its options numbered.
type plainForm struct {
	form QuestionForm
	// at is the question that waits for its line.
	at      int
	answers map[string]string
	said    []string
}

// printFormQuestion writes the question of the form that waits. The
// caller holds the lock.
func (f *plain) printFormQuestion() {
	pf := f.form
	q := pf.form.Questions[pf.at]
	lead := ""
	if n := len(pf.form.Questions); n > 1 {
		lead = fmt.Sprintf("Question %d of %d", pf.at+1, n)
	}
	if q.Header != "" {
		lead = strings.TrimSpace(lead + " [" + visible(oneLine(q.Header, 40)) + "]")
	}
	if lead != "" {
		lead += ": "
	}
	fmt.Fprintln(f.out, lead+visible(q.Question))
	for i, o := range q.Options {
		line := fmt.Sprintf("  %d. %s", i+1, visible(o.Label))
		if d := strings.TrimSpace(o.Description); d != "" {
			line += ": " + visible(oneLine(d, 300))
		}
		fmt.Fprintln(f.out, line)
		for _, p := range strings.Split(strings.TrimRight(o.Preview, "\n"), "\n") {
			if strings.TrimSpace(p) != "" {
				fmt.Fprintln(f.out, "       "+visible(p))
			}
		}
	}
	switch {
	case len(q.Options) == 0:
		fmt.Fprintln(f.out, "Type your answer and Enter, or Enter alone to leave it empty.")
	case q.NoOther:
		fmt.Fprintln(f.out, "Type a number and Enter.")
	case q.MultiSelect:
		fmt.Fprintln(f.out, "Type the numbers of all that apply, with commas between them, and Enter. Or answer in your own words.")
	default:
		fmt.Fprintln(f.out, "Type a number and Enter. Or answer in your own words.")
	}
}

// hint says how the question that waits is answered, after a line that
// did not answer it.
func (pf *plainForm) hint() string {
	q := pf.form.Questions[pf.at]
	if q.NoOther {
		return fmt.Sprintf("That is not an answer. Type one number from 1 to %d.", len(q.Options))
	}
	if q.MultiSelect {
		return fmt.Sprintf("That is not an answer. Type numbers from 1 to %d with commas between them, or your own words. (For an answer of your own that is only a number, put a word before it.)", len(q.Options))
	}
	return fmt.Sprintf("That is not an answer. Type one number from 1 to %d, or your own words. (For an answer of your own that is only a number, put a word before it.)", len(q.Options))
}

// take reads a line as the answer to the question that waits. good says
// the line was an answer, and more that another question waits after it.
// With the last answer it returns the answer to the whole form.
func (pf *plainForm) take(line string) (ans agent.Answer, more, good bool) {
	if pf.form.EscCancels && strings.TrimSpace(line) == "/cancel" {
		// A server's form, cancelled: the server is told so.
		return agent.Answer{Choice: "cancel"}, false, true
	}
	q := pf.form.Questions[pf.at]
	said, ok := formAnswer(q, line)
	if !ok {
		return agent.Answer{}, false, false
	}
	pf.answers[q.Question] = said
	pf.said = append(pf.said, firstOf(q.Header, q.Question)+": "+said)
	pf.at++
	if pf.at < len(pf.form.Questions) {
		return agent.Answer{}, true, true
	}
	return agent.Answer{Data: pf.answers, Text: strings.Join(pf.said, "\n")}, false, true
}

// formAnswer reads a typed line as the answer to one question of the
// model's: the number of an option (several, for a question that takes
// several), which stands for its label, or the person's own words.
func formAnswer(q FormQuestion, line string) (string, bool) {
	line = strings.TrimSpace(line)
	if len(q.Options) == 0 {
		// A question answered by typing (a server's field): the line as
		// it is, a number too; an empty one leaves it unanswered.
		return line, true
	}
	if line == "" {
		return "", false
	}
	fields := strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == ';' })
	var labels []string
	seen := map[int]bool{}
	for _, word := range fields {
		n, err := strconv.Atoi(strings.TrimRight(word, "."))
		if err != nil && q.NoOther {
			// Words, where only the options will do: an option's label.
			for _, o := range q.Options {
				if strings.EqualFold(strings.TrimSpace(o.Label), line) {
					return o.Label, true
				}
			}
			return "", false
		}
		if err != nil {
			// Words: the line is the person's own answer, as typed.
			return line, true
		}
		if n < 1 || n > len(q.Options) {
			return "", false
		}
		if !seen[n] {
			seen[n] = true
			labels = append(labels, q.Options[n-1].Label)
		}
	}
	if len(labels) == 0 || (len(labels) > 1 && !q.MultiSelect) {
		return "", false
	}
	return strings.Join(labels, ", "), true
}

// answerHint says how a question is answered, after a line that did not
// answer it.
func (f *plain) answerHint(q agent.Question) string {
	if len(q.Choices) == 0 {
		return "Type your answer and Enter."
	}
	return fmt.Sprintf("That is not one of the answers. Type a number from 1 to %d and Enter.", len(q.Choices))
}

// parseAnswer reads a line as the answer to a question: the number of a
// choice, with anything after it as words that go with the answer; or,
// for a question with no choices, the line itself.
func (f *plain) parseAnswer(q agent.Question, line string) (agent.Answer, bool) {
	line = strings.TrimSpace(line)
	if len(q.Choices) == 0 {
		return agent.Answer{Text: line}, line != ""
	}
	word, rest, _ := strings.Cut(line, " ")
	rest = strings.TrimSpace(rest)
	// "2." and "no," are a 2 and a no.
	word = strings.TrimRight(word, ".,:;!")
	if n, err := strconv.Atoi(word); err == nil && n >= 1 && n <= len(q.Choices) {
		return agent.Answer{Choice: q.Choices[n-1].Key, Text: rest}, true
	}
	// A plain yes or no is understood where the question has that choice.
	want := ""
	switch strings.ToLower(word) {
	case "y", "yes":
		want = "yes"
	case "n", "no":
		want = "no"
	}
	for _, c := range q.Choices {
		if want != "" && c.Key == want {
			return agent.Answer{Choice: c.Key, Text: rest}, true
		}
	}
	return agent.Answer{}, false
}

// commandName matches a word that reads as a slash command: a slash and a
// plain word. A path, such as /usr/local, does not.
var commandName = regexp.MustCompile(`^/[a-z][a-z0-9_-]*$`)

// quitWindow is how soon after a Ctrl+C a second one quits.
const quitWindow = 3 * time.Second

// How long a question waits, and how long a finished session waits for
// the next prompt, before the person's Notification hooks are told. They
// are variables so that a test need not wait as long as a person would.
var (
	askNotifyAfter  = 6 * time.Second
	idleNotifyAfter = time.Minute
)

// Run reads lines until the person leaves: a prompt is sent, a line that
// begins with a slash is run as a command, and while a question waits the
// line answers it.
func (f *plain) Run(ctx context.Context, c Controls) int {
	f.mu.Lock()
	f.c = c
	info := c.Info()
	f.say("gnosh %s", info.Version)
	f.say("session %s in %s", info.ID, info.Cwd)
	f.say("%s", f.statusLine())
	f.mu.Unlock()
	defer f.stopTimers()
	if f.term != nil {
		defer f.term.Close()
	}

	// The first prompt: what was piped in, then the prompt of the command
	// line.
	first := f.inv.Prompt
	if f.piped != nil {
		piped, err := f.p.readPiped(ctx, first != "")
		if err != nil {
			f.p.errf("%v", err)
			return cli.ExitFailure
		}
		first = joinPrompt(piped, first)
	}
	var lines <-chan string
	ending := false
	if f.in != nil {
		f.in.start()
		lines = f.in.lines
		f.note("Type a prompt and Enter. /help lists the commands; a line that begins with ! is run as a command of your own. Ctrl+C stops a turn; a second one quits.")
	} else {
		ending = true
		f.setNobody(c)
		if strings.TrimSpace(first) == "" {
			f.p.errf("there is no prompt and no terminal to type one at, so the session has nothing to do. Give the prompt on the command line, or pipe it in.")
			c.Quit("other")
			return cli.ExitFailure
		}
		f.note("Nobody can type here (standard input is not a terminal), so the session does this one prompt, answers no to every question, and ends.")
	}
	if strings.TrimSpace(first) != "" {
		f.submit(c, first)
	}

	var lastInterrupt time.Time
	// While the session is only waiting to end, its state is looked at
	// again now and then, in case the event that says it has nothing left
	// came just before a look. Nothing ticks while a person can type.
	var recheck *time.Ticker
	defer func() {
		if recheck != nil {
			recheck.Stop()
		}
	}()
	for {
		if f.restarting.Load() {
			c.Quit("other")
			return f.leave(c, cli.ExitOK)
		}
		if ending && !c.State().Busy {
			// Everything that was under way has ended, and nobody can
			// type more.
			_ = c.Wait(ctx)
			c.Quit("prompt_input_exit")
			return f.leave(c, cli.ExitOK)
		}
		var again <-chan time.Time
		if ending {
			if recheck == nil {
				recheck = time.NewTicker(250 * time.Millisecond)
			}
			again = recheck.C
		}
		f.prompt()
		select {
		case line, ok := <-lines:
			if !ok {
				// The input ended (Ctrl+D): what is under way is let
				// finish, and then the session ends.
				lines, ending = nil, true
				f.setNobody(c)
				if c.State().Busy {
					f.note("The input has ended. The session ends when what is under way has finished; Ctrl+C stops it now.")
				}
				continue
			}
			f.typed()
			if quit := f.line(ctx, c, line); quit {
				c.Quit("prompt_input_exit")
				return f.leave(c, cli.ExitOK)
			}
		case <-f.wake:
		case <-again:
		case sig := <-c.Signals():
			if sig != os.Interrupt {
				c.Interrupt()
				c.Quit("other")
				return f.leave(c, signalExit(sig))
			}
			now := time.Now()
			if !lastInterrupt.IsZero() && now.Sub(lastInterrupt) < quitWindow {
				c.Interrupt()
				c.Quit("prompt_input_exit")
				return f.leave(c, cli.ExitInterrupted)
			}
			lastInterrupt = now
			f.interrupted()
			if c.State().Busy {
				c.Interrupt()
				f.note("Stopping the turn. Ctrl+C again within %d seconds quits.", int(quitWindow.Seconds()))
			} else {
				f.note("Nothing is running. Ctrl+C again within %d seconds quits; so does /exit.", int(quitWindow.Seconds()))
			}
		case <-ctx.Done():
			return cli.ExitFailure
		}
	}
}

// put puts a question to the person: the one being answered from now on.
// The caller holds f.mu.
func (f *plain) put(q agent.Question) {
	f.question = &q
	if form, ok := q.Data.(QuestionForm); ok && q.Kind == questionKind && len(form.Questions) > 0 {
		f.form = &plainForm{form: form, answers: map[string]string{}}
		f.fresh()
		fmt.Fprintln(f.out)
		head := q.Title
		if form.Lead != "" {
			head = form.Lead
		}
		fmt.Fprintln(f.out, "? "+visible(head))
		if form.EscCancels {
			fmt.Fprintln(f.out, "(/cancel answers that you will not say.)")
		}
		f.printFormQuestion()
	} else {
		f.printQuestion(q)
	}
	f.poke()
	if f.ask != nil {
		f.ask.Stop()
		f.ask = nil
	}
	if c := f.c; c != nil && c.Info().NotifyHooks {
		title := q.Title
		f.ask = time.AfterFunc(askNotifyAfter, func() { c.Notify("permission_prompt", title, "gnosh is waiting for an answer: "+title) })
	}
}

// next puts the question that waited longest, if one does. The caller
// holds f.mu.
func (f *plain) next() {
	if len(f.behind) == 0 {
		return
	}
	q := f.behind[0]
	f.behind = f.behind[1:]
	f.put(q)
}

// setNobody notes that no more lines will come, and answers the question
// that waits, if one does: a question nobody can answer must not hold the
// session for ever.
func (f *plain) setNobody(c Controls) {
	f.mu.Lock()
	f.nobody = true
	q := f.question
	behind := f.behind
	f.question, f.behind = nil, nil
	f.mu.Unlock()
	if q != nil {
		f.refuse(c, *q)
	}
	for _, w := range behind {
		f.refuse(c, w)
	}
}

// refuse answers a question that nobody can answer.
func (f *plain) refuse(c Controls, q agent.Question) {
	const why = "nobody is at the keyboard to answer (the input has ended), so this was not approved"
	ans := agent.Answer{Text: why}
	for _, ch := range q.Choices {
		if ch.Key == "no" {
			ans.Choice = "no"
		}
	}
	if q.Kind == questionKind {
		// The model's own questions: they are left unanswered, which the
		// tool that put them says in its own words.
		ans = agent.Answer{}
	}
	_ = c.Answer(q.ID, ans)
}

// prompt shows that a line may be typed, when nothing is being written.
func (f *plain) prompt() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nobody || f.question != nil || !f.atStart || f.c == nil || f.c.State().Busy {
		return
	}
	fmt.Fprint(f.out, "> ")
	f.atStart = false
	f.kind = "prompt"
}

// typed notes that the person typed a line: their Enter ended the line the
// terminal was on, and the timers that wait for them start over.
func (f *plain) typed() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.kind == "prompt" {
		f.atStart = true
		f.kind = ""
	}
	f.stopNotifyLocked()
}

// interrupted notes that the terminal echoed a Ctrl+C on the line it was
// on.
func (f *plain) interrupted() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.kind == "prompt" {
		fmt.Fprintln(f.out)
		f.atStart = true
		f.kind = ""
	}
}

// line acts on one line of input, and reports whether the person asked
// to leave.
func (f *plain) line(ctx context.Context, c Controls, line string) (quit bool) {
	f.mu.Lock()
	q := f.question
	f.mu.Unlock()
	if q != nil {
		f.mu.Lock()
		var ans agent.Answer
		var good bool
		if f.form != nil {
			var more bool
			if ans, more, good = f.form.take(line); !good || more {
				// Not an answer, or not the last one: the question that
				// waits now is said (again).
				if !good {
					f.say("%s", f.form.hint())
				} else {
					f.printFormQuestion()
				}
				f.mu.Unlock()
				return false
			}
			f.form = nil
		} else if ans, good = f.parseAnswer(*q, line); !good {
			f.say("%s", f.answerHint(*q))
			f.mu.Unlock()
			return false
		}
		f.question = nil
		if f.ask != nil {
			f.ask.Stop()
			f.ask = nil
		}
		f.showTyping()
		f.mu.Unlock()
		if err := c.Answer(q.ID, ans); err != nil {
			f.note("That question is no longer waiting for an answer.")
		}
		f.mu.Lock()
		if f.question == nil {
			f.next()
		}
		f.mu.Unlock()
		return false
	}
	line, ok := f.cleanLine(line)
	if !ok {
		return false
	}
	text := strings.TrimSpace(line)
	if text == "" {
		return false
	}
	if strings.HasPrefix(text, "!") {
		// A command of the person's own: it is run, shown as a tool call
		// is, and handed to the model with what it printed.
		more, ok := c.(SessionControls)
		if !ok {
			f.note("The command was not run: this session cannot run a command of your own.")
			return false
		}
		if err := more.Shell(strings.TrimPrefix(text, "!")); err != nil {
			f.note("The command was not run: %v", err)
		}
		return false
	}
	if strings.HasPrefix(text, "/") {
		res, err := f.command(ctx, c, text)
		if res.Handled || err != nil {
			if res.Copy != "" {
				fmt.Fprint(f.out, term.ClipboardWrite(res.Copy))
			}
			if res.Text != "" {
				f.note("%s", visible(res.Text))
			}
			if err != nil {
				f.note("error: %s", visible(err.Error()))
			}
			return res.Quit
		}
		if word, _, _ := strings.Cut(text, " "); commandName.MatchString(word) {
			f.note("There is no command %s. /help lists the commands. To send a prompt that begins with a slash, put a space in front of it.", word)
			return false
		}
	}
	f.submit(c, line)
	return false
}

// command runs a typed /command. A command may put a question while it
// runs (a PreModelSwitch hook's "ask" does), so while the session's
// question waits the lines typed are read here, and the first answers it.
// Lines are read only then: a question put before a session exists
// (AskBefore: which model, after /clear) reads its own, and the rest wait
// for the command to be done.
func (f *plain) command(ctx context.Context, c Controls, text string) (CommandResult, error) {
	type result struct {
		res CommandResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := c.Command(ctx, text)
		done <- result{res, err}
	}()
	for {
		f.mu.Lock()
		asked, nobody := f.question != nil, f.nobody
		f.mu.Unlock()
		var lines <-chan string
		if asked && f.in != nil && !nobody {
			lines = f.in.lines
		}
		select {
		case r := <-done:
			return r.res, r.err
		case <-f.wake:
			// (A question was put or taken back: look again.)
		case line, ok := <-lines:
			if !ok {
				// The input ended: the question that waits is answered
				// no, and the loop finds the end again afterwards.
				f.setNobody(c)
				continue
			}
			f.typed()
			f.line(ctx, c, line)
		}
	}
}

// cleanLine takes the characters a terminal draws as nothing out of a
// line typed to be sent, as the full interface does on Enter
// (ui/invisible.go), and says so. It returns false, with nothing to send,
// when what was taken out could hide text or change its order, or when
// what is left begins as a command or a shell line and what was typed did
// not: there is no box here to put the line back in, so what is left is
// shown, to be typed again if it is meant.
func (f *plain) cleanLine(line string) (string, bool) {
	clean, found := text.CleanInvisible(line)
	if found.Removed == 0 {
		return line, true
	}
	what := countWords(found.Removed, "invisible character", "invisible characters")
	// (Trimmed as line() trims a line before it looks for a ! or a /.)
	typed := strings.TrimSpace(line)
	lead := strings.TrimSpace(clean)
	switch {
	case strings.TrimSpace(clean) == "":
		f.note("The line held only invisible characters, so nothing was sent.")
		return "", false
	case found.Hiding:
		f.note("Took out %s that could hide text or reorder it; nothing was sent. What is left: %s", what, visible(clean))
		return "", false
	case strings.HasPrefix(typed, "!"):
		// What is taken out of a command changes what it does, so it is
		// never run before it is read.
		f.note("Took out %s from the shell line, which changes what it runs, so it was not run: %s", what, visible(clean))
		return "", false
	case (strings.HasPrefix(lead, "/") || strings.HasPrefix(lead, "!")) && !strings.HasPrefix(typed, "/"):
		f.note("Took out %s, and what is left reads as a command, so it was not run: %s", what, visible(clean))
		return "", false
	}
	f.note("Took out %s before sending the line.", what)
	return clean, true
}

// submit sends a prompt.
func (f *plain) submit(c Controls, text string) {
	if _, err := c.Submit(agent.Prompt{Text: text}); err != nil {
		f.note("The prompt was not sent: %v", err)
	}
}

// leave says how to get the session back, and returns the exit code.
func (f *plain) leave(c Controls, code int) int {
	f.stopTimers()
	f.mu.Lock()
	defer f.mu.Unlock()
	info := c.Info()
	if _, err := os.Stat(info.Path); err == nil {
		f.say("To resume this session: %s", info.Resume)
	} else {
		f.fresh()
	}
	return code
}

// stopTimers stops the timers that would print something later, and puts
// the terminal back as it was.
func (f *plain) stopTimers() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopWaiting()
	f.stopNotifyLocked()
	f.showTyping()
}

// printTasks writes the task list, when it is not the one last written.
// The caller holds the lock.
func (f *plain) printTasks(tasks []Task) {
	if slices.Equal(tasks, f.shownTasks) {
		return
	}
	f.shownTasks = slices.Clone(tasks)
	if len(tasks) == 0 {
		f.say("tasks: none left on the list")
		return
	}
	done := 0
	for _, t := range tasks {
		if t.Status == TaskCompleted {
			done++
		}
	}
	f.say("tasks (%d of %d done):", done, len(tasks))
	for _, line := range taskLines(tasks) {
		f.say("  %s", visible(line))
	}
}

// taskLines writes a task list as lines: a box for each task, ticked when
// it is done and pointed at when it is the one in hand, which is written
// the way it reads while it is being done.
func taskLines(tasks []Task) []string {
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		switch t.Status {
		case TaskCompleted:
			out = append(out, "[x] "+oneLine(t.Content, 200))
		case TaskInProgress:
			out = append(out, "[>] "+oneLine(firstOf(t.ActiveForm, t.Content), 200))
		default:
			out = append(out, "[ ] "+oneLine(t.Content, 200))
		}
	}
	return out
}

// stopNotifyLocked stops the two timers behind the Notification hooks. The
// caller holds the lock.
func (f *plain) stopNotifyLocked() {
	for _, t := range []**time.Timer{&f.idle, &f.ask} {
		if *t != nil {
			(*t).Stop()
			*t = nil
		}
	}
}

// statusLine says at a glance which model answers, how hard it thinks, how
// full its window is and which permission mode is in force. The caller
// holds the lock.
func (f *plain) statusLine() string {
	info := f.c.Info()
	parts := []string{"model " + firstOf(info.Model, "(none)")}
	if info.Provider != "" && info.Model != "" {
		parts[0] = "model " + info.Provider + "/" + info.Model
	}
	if info.Thinking != "" {
		parts = append(parts, "thinking "+info.Thinking)
	}
	if cs := f.c.Context(); cs.Window > 0 {
		window := tokenCount(cs.Window)
		if !cs.Known {
			window += " (assumed)"
		}
		parts = append(parts, fmt.Sprintf("context %d%% of %s", cs.Used*100/cs.Window, window))
	}
	if info.Mode != "default" {
		parts = append(parts, "permission mode "+info.Mode)
	}
	st := f.c.State()
	if st.Loop {
		parts = append(parts, "/loop on")
	}
	if st.Queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", st.Queued))
	}
	var facts map[string]string
	if more, ok := f.c.(SessionControls); ok {
		facts = more.Status()
	}
	if v := facts["frame"]; v != "" {
		parts = append(parts, "in "+visible(oneLine(v, 60)))
	}
	if v := facts["background"]; v != "" {
		parts = append(parts, v+" in the background")
	}
	if more, ok := f.c.(SessionControls); ok {
		if counts := DeliverableCount(more.Deliverables(), ", "); counts != "" {
			parts = append(parts, "deliverables: "+counts)
		}
	}
	return strings.Join(parts, " | ")
}

// startWaiting begins to tell the person, after a few quiet seconds and
// then every half minute, what gnosh is waiting for. A local model can be
// silent for minutes; this is how slow is told from stuck. The caller
// holds the lock.
func (f *plain) startWaiting() {
	f.stopWaiting()
	f.waitSince = time.Now()
	f.waiting = time.AfterFunc(5*time.Second, f.waitTick)
}

// stopWaiting ends that. The caller holds the lock.
func (f *plain) stopWaiting() {
	if f.waiting != nil {
		f.waiting.Stop()
		f.waiting = nil
	}
}

// waitTick prints what the session is waiting for, and comes again.
func (f *plain) waitTick() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.waiting == nil || f.c == nil {
		return
	}
	st := f.c.State()
	if !st.Busy || st.Waiting == "" {
		f.waiting = nil
		return
	}
	f.say("(%s, %s so far. Ctrl+C stops the turn.)", st.Waiting, time.Since(f.waitSince).Round(time.Second))
	f.waiting = time.AfterFunc(30*time.Second, f.waitTick)
}

// Event shows what happens in the session.
func (f *plain) Event(ev any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A sub-agent's own turns, requests, replies and states are not the
	// session's: of its events, its tool calls and their results, its
	// questions and its notices are shown (below, each marked as its).
	if e, ok := ev.(agent.Event); ok {
		if m := e.EventMeta(); m != nil && m.Agent != "" {
			switch ev.(type) {
			case *agent.ToolStartEvent, *agent.ToolEndEvent, *agent.QuestionEvent, *agent.AnswerEvent, *agent.NoticeEvent:
			default:
				return
			}
		}
	}
	switch ev := ev.(type) {
	case *agent.RequestEvent:
		if ev.Purpose == "main" || ev.Purpose == "summary" {
			f.startWaiting()
		}
	case *agent.TextEvent:
		f.stopWaiting()
		if ev.Agent == "" {
			f.flow("text", "", ev.Text)
		}
	case *agent.ThinkingEvent:
		f.stopWaiting()
		if ev.Agent == "" {
			f.flow("thinking", markThink, ev.Text)
		}
	case *agent.ToolCallEvent:
		f.stopWaiting()
	case *agent.ReplyEvent:
		f.stopWaiting()
		f.fresh()
	case *agent.RetryEvent:
		f.stopWaiting()
		if ev.FellBack != "" {
			f.say("%s", visible(ev.Message))
			break
		}
		f.say("waiting: %s", visible(ev.Message))
	case *agent.PromptEvent:
		// One of gnosh's own prompts says why a turn started in a notice
		// that the loop gives right after this event.
	case *agent.ToolStartEvent:
		if ev.Name == DeliverToolName && ev.Agent == "" {
			// What a call of Deliver did is one line, written when the
			// call ends.
			break
		}
		indent := ""
		if ev.Agent != "" {
			indent = "  "
		}
		f.say("%s%s%s", indent, markTool, visible(toolLine(ev.Name, ev.Input)))
	case *agent.ToolEndEvent:
		if d, replaced, ok := DeliverableOf(ev.Result.Meta); ok && !ev.Denied && !ev.Result.IsError {
			// A deliverable was claimed, or delivered: one line that
			// holds its address and its file whole.
			f.say("%s", visible(d.Line(replaced)))
			break
		}
		if ev.Name == DeliverToolName && ev.Agent == "" {
			// A call of Deliver that did not go well is shown as any
			// other call is: what it was, and then why.
			f.say("%s%s", markTool, visible(toolLine(ev.Name, ev.Input)))
		}
		f.say("%s%s", markResult, visible(resultLine(ev.Result, ev.Denied, ev.Interrupted)))
		if !ev.Denied && !ev.Result.IsError {
			if _, isCommand := metaInt(ev.Result.Meta, "exitCode"); isCommand {
				// The line a command's result begins with, which says
				// where it ran, is for the model: the call's own line
				// above has named the machine.
				_, output := shelltool.SplitStamp(core.TextOf(ev.Result.Content))
				for _, line := range lastLines(output, 3) {
					f.say("    %s", visible(line))
				}
			}
		}
	case *agent.QuestionEvent:
		q := ev.Question
		if f.nobody {
			if c := f.c; c != nil {
				go f.refuse(c, q)
			}
			return
		}
		if f.question != nil {
			// One question at a time: this one waits, and is put once the
			// one being answered is done. Put in its place, it would take
			// the answer typed for that one (a background sub-agent's
			// question over the session's own), and leave that one
			// waiting for good.
			f.behind = append(f.behind, q)
			f.say("(Another question waits behind this one: %s)", visible(q.Title))
			return
		}
		f.put(q)
	case *agent.AnswerEvent:
		f.behind = slices.DeleteFunc(f.behind, func(w agent.Question) bool { return w.ID == ev.ID })
		if f.question != nil && f.question.ID == ev.ID {
			f.question, f.form = nil, nil
		}
		f.showTyping()
		switch {
		case ev.Withdrawn:
			f.say("(The question was taken back: the turn was stopped.)")
		case ev.Answer.By != "":
			f.say("(%s answered the question: %s.)", visible(ev.Answer.By), visible(ev.Answer.Choice))
		}
		if f.ask != nil && f.question == nil {
			// (The reminder was the answered question's. One put since
			// has its own.)
			f.ask.Stop()
			f.ask = nil
		}
		f.poke()
		if f.question == nil {
			f.next()
		}
	case *agent.NoticeEvent:
		lead := "note: "
		switch ev.Level {
		case agent.LevelWarning:
			lead = "warning: "
		case agent.LevelError:
			lead = "error: "
		}
		f.say("%s%s", lead, visible(ev.Text))
	case *agent.QueueEvent:
		if n := len(ev.Queued); n > 0 {
			f.say("(%d prompt(s) wait for their turn.)", n)
		}
	case *agent.PromptReturnedEvent:
		if !ev.Prompt.Hidden {
			f.say("Your prompt was not sent: %s", visible(ev.Reason))
			f.say("It was: %s", visible(oneLine(ev.Prompt.Text, 2000)))
		}
		f.poke()
	case *agent.DriverEvent:
		switch {
		case ev.Action == agent.Halt:
		case ev.Checkpoint:
			f.say("The turn goes on: %s objects, %d times in a row now, so the model is asked whether there is a real reason to stop: %s", ev.Name, ev.Streak, visible(oneLine(ev.Objection, 300)))
		case f.hookSaid != "" && strings.Join(strings.Fields(ev.Objection), " ") == f.hookSaid:
			f.say("The turn goes on: %s objects, as the hook above says.", ev.Name)
		default:
			f.say("The turn goes on: %s objects: %s", ev.Name, visible(oneLine(ev.Objection, 300)))
		}
		f.hookSaid = ""
	case *agent.LoopEvent:
		switch {
		case ev.On:
			f.say("/loop is on: gnosh keeps the session working until the model shows the task is done. /noloop turns it off.")
		case ev.Why != "":
			f.say("/loop is off after %d turn(s): %s.", ev.Turns, visible(ev.Why))
		}
	case *agent.SummaryEvent:
		switch {
		case ev.Phase == "start":
			f.say("Summarizing the conversation to make room (%s). This can take minutes on a local model; Ctrl+C stops it.", ev.Trigger)
		case ev.Err != nil:
			f.say("No summary was made: %v", ev.Err)
		default:
			f.say("The conversation was summarized: about %s tokens before, about %s after.", tokenCount(ev.PreTokens), tokenCount(ev.PostTokens))
		}
	case *HookEvent:
		for _, r := range ev.Records {
			line := r.String()
			if reason := strings.Join(strings.Fields(r.Reason), " "); r.Result == hooks.ResultBlocked && reason != "" && strings.HasSuffix(line, reason) {
				f.hookSaid = reason
			}
			f.say("%s%s", markHook, visible(line))
		}
	case *TasksEvent:
		f.printTasks(ev.Tasks)
	case *PushEvent:
		ev.Reply <- "not sent: the plain interface sends no notifications"
	case *RestartEvent:
		f.say("%s", visible(ev.Why))
		f.restarting.Store(true)
		select {
		case f.wake <- struct{}{}:
		default:
		}
	case *agent.TurnEndEvent:
		f.stopWaiting()
		how := "done"
		switch ev.Result.Reason {
		case agent.EndStopped:
			how = "stopped"
		case agent.EndFailed:
			how = "failed"
		case agent.EndHalted:
			how = "ended"
			if d := strings.TrimSpace(ev.Result.Detail); d != "" {
				how = "ended (" + visible(oneLine(d, 160)) + ")"
			}
		case agent.EndModelStopped:
			how = "the model chose to stop"
		}
		f.say("-- %s in %s | %s", how, ev.Result.Duration.Round(time.Second), f.statusLine())
		f.poke()
	case *agent.IdleEvent:
		f.stopWaiting()
		f.poke()
		if c := f.c; c != nil && !f.nobody && c.Info().NotifyHooks {
			f.idle = time.AfterFunc(idleNotifyAfter, func() { c.Notify("idle_prompt", "gnosh", "gnosh has finished and waits for your next prompt") })
		}
	case *agent.BusyEvent:
		if f.idle != nil {
			f.idle.Stop()
			f.idle = nil
		}
	}
}

// lastLines returns the last n lines of s that are not blank.
func lastLines(s string, n int) []string {
	var out []string
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append([]string{oneLine(lines[i], 200)}, out...)
		}
	}
	return out
}
