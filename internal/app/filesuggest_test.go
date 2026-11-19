package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// fileSuggestion, Claude Code's: the person's command is given what was
// typed after the @ as {"query": ...} on its standard input, and the
// project's folder as CLAUDE_PROJECT_DIR; the paths it prints are what is
// offered, fifteen at most; one that takes too long is stopped, and what
// it started with it.
func TestFileSuggestionCommand(t *testing.T) {
	w := apptest.NewWorld(t)
	script := w.Script(w.Home+"/bin/files.sh", `q=$(cat | sed -n 's/.*"query":"\([^"]*\)".*/\1/p')
case "$q" in
slow) sleep 30 ;;
broken) exit 3 ;;
*) echo "dir=$CLAUDE_PROJECT_DIR"; for i in $(seq 1 20); do echo "src/$q-$i.go"; done ;;
esac`)
	w.Settings(`{"fileSuggestion": {"type": "command", "command": "` + script + `"}}`)
	defer func(was time.Duration) { fileSuggestWait = was }(fileSuggestWait)
	fileSuggestWait = time.Second

	var got, slow []string
	var slowErr, brokenErr error
	var ok bool
	var info SessionInfo
	_, r := attach(t, w, nil, func(ctx context.Context, c SessionControls, f *scripted) int {
		info = c.Info()
		fs := c.(interface {
			SuggestFiles(context.Context, string) ([]string, bool, error)
		})
		got, ok, _ = fs.SuggestFiles(ctx, "comp")
		start := time.Now()
		slow, _, slowErr = fs.SuggestFiles(ctx, "slow")
		if took := time.Since(start); took > 4*time.Second {
			slowErr = fmt.Errorf("the slow command took %v", took)
		}
		_, _, brokenErr = fs.SuggestFiles(ctx, "broken")
		c.Quit("prompt_input_exit")
		return 0
	})
	r.wantCode(t, 0)
	if !info.FileSuggestion {
		t.Error("the session's info does not say a command offers the paths")
	}
	if !ok || len(got) != 15 || !strings.HasPrefix(got[0], "dir=/") || got[1] != "src/comp-1.go" {
		t.Errorf("the paths offered: %q (ok %v)", got, ok)
	}
	if len(slow) != 0 || slowErr == nil || !strings.Contains(slowErr.Error(), "did not answer within 1s") {
		t.Errorf("a command that took too long: %q, %v", slow, slowErr)
	}
	if brokenErr == nil || !strings.Contains(brokenErr.Error(), "failed") {
		t.Errorf("a command that failed: %v", brokenErr)
	}
}
