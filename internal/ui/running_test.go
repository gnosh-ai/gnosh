package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/text"
)

// While a command runs, the line that says what is under way names the
// programs its shell runs just now, once the tool says, in place of the
// command's first line, which may be a file the command wrote at once
// (Jae, 26 November 2026, of `cat > main.go <<'EOF'` shown for minutes
// while `go run main.go | tail -15` ran: "why is it stuck seemingly").
func TestWorkingLineNamesWhatACommandRuns(t *testing.T) {
	g := newRig(t, 120, 24, text.NoColor)
	cmd := map[string]any{"command": "cat > /tmp/b/main.go <<'EOF'\npackage main\nEOF\ngo run main.go | tail -15"}
	g.keys(`"check the keys" <Enter>`).start()
	g.ev(toolCall("c1", "Bash"), replyEnd(core.StopToolUse), toolStart("c1", "Bash", cmd))
	g.liveHas("running cat > /tmp/b/main.go <<'EOF'")
	g.ev(&agent.ToolRunningEvent{CallID: "c1", Text: "go run main.go, tail -15"})
	g.liveHas("running go run main.go, tail -15").liveLacks("running cat >")
	// Said of a call that is not there, it changes nothing.
	g.ev(&agent.ToolRunningEvent{CallID: "c9", Text: "something else"})
	g.liveHas("running go run main.go, tail -15").liveLacks("something else")
}

// A command line too long for the line is cut, and how long it has run
// still shows: a line whose time cannot be seen looks stuck (Jae, 26
// November 2026: "is it stuck now?", of a script sent over ssh as one
// argument that took the whole line).
func TestWorkingLineKeepsItsTime(t *testing.T) {
	g := newRig(t, 80, 24, text.NoColor)
	g.keys(`"scan again" <Enter>`).start()
	g.ev(toolCall("c1", "Bash"), replyEnd(core.StopToolUse), toolStart("c1", "Bash", map[string]any{"command": "ssh workbench python3"}))
	g.ev(&agent.ToolRunningEvent{CallID: "c1", Text: `ssh workbench "python3 - <<'PYEOF' …" ` + strings.Repeat("x", 200)})
	g.wait(5 * time.Second)
	g.liveHas("running ssh workbench", "…", "5s")
}
