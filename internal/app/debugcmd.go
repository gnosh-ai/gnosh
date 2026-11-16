package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/prompt"
)

// This file is /debug, as Claude Code has it: debug logging goes on for the
// rest of the session (it is off unless gnosh was started with --debug),
// and the model is handed the end of what this session wrote to gnosh's
// log, to find out what went wrong, with the person's words about it. The
// log is outside the project, so its lines go along with the prompt; the
// model asks to read more of it only when it needs to.

// debugLines and debugBytes bound the log lines /debug hands the model:
// the last ones of the session, as many as fit.
const (
	debugLines = 120
	debugBytes = 16 << 10
)

// path is the file the log writes to, or "".
func (g *fileLog) path() string {
	if g == nil || g.f == nil {
		return ""
	}
	return g.f.Name()
}

// slashDebug is /debug [what is wrong].
func (l *live) slashDebug(ctx context.Context, c *SlashContext) error {
	path := l.flog.path()
	if path == "" {
		return errors.New("this session writes no log (it could not be opened as the session started), so there is nothing for /debug to read")
	}
	on := "Debug logging was on already."
	if !l.flog.debug.Swap(true) {
		on = "Debug logging is on for the rest of this session; what came before was logged in less detail."
	}
	id := shortID(l.ts.ID())
	lines := sessionLogTail(path, id, debugLines, debugBytes)
	text := fmt.Sprintf(debugTask, path, id)
	if lines == "" {
		text += "\n\nThis session has written nothing to it yet."
	} else {
		text += "\n\nThe last lines this session wrote to it:\n" + lines
	}
	if what := strings.TrimSpace(c.Args); what != "" {
		text += "\n\nThe user describes the problem so: " + what
	} else {
		text += "\n\nThe user did not say what the problem is: look for errors, warnings and anything that failed, the latest first."
	}
	c.Say("%s The model looks into gnosh's log, %s.", on, l.tilde(path))
	return c.Send(agent.Prompt{Hidden: true, Source: "debug", Text: prompt.Wrap(text),
		Notice: "/debug: the model is reading gnosh's log."})
}

// sessionLogTail returns the last lines of the log at path that the session
// with the short id wrote, at most max of them and about size bytes, oldest
// first. Only the log's end is read.
func sessionLogTail(path, id string, max, size int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	window := int64(size) * 16
	if info, err := f.Stat(); err == nil && info.Size() > window {
		if _, err := f.Seek(info.Size()-window, io.SeekStart); err != nil {
			return ""
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, window))
	if err != nil {
		return ""
	}
	var kept []string
	total := 0
	all := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	for i := len(all) - 1; i >= 0 && len(kept) < max; i-- {
		// A line is "<time> <session> <kind> <text>".
		fields := strings.SplitN(string(all[i]), " ", 3)
		if len(fields) < 3 || fields[1] != id {
			continue
		}
		if total+len(all[i])+1 > size {
			break
		}
		total += len(all[i]) + 1
		kept = append(kept, string(all[i]))
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return strings.Join(kept, "\n")
}

// debugTask is what /debug asks of the model: the log's path and the
// session's short id.
const debugTask = `The user typed /debug: something about gnosh, the program you run in, is going wrong, and you are to find out what from gnosh's log, %s. Each line holds a time, a session's short id, a kind and the text; this session's lines carry %s, and the project's other sessions write to the same file. Debug logging has just been turned on, so the lines before it say less.

Say what went wrong, which lines show it, the likely cause, and what the user can do about it: a setting, a command, or a report to gnosh's developers with the lines that show it. If the lines do not explain it, read further back in the log, and if it still does not, say so and say what would. Change no file.`
