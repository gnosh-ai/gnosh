package permission

import (
	"slices"
	"testing"
)

// A ":" is never taken for a command the engine had to read, whatever the
// lines glued to it: the stages of a pipeline trace to one file at once,
// and a line another stage wrote in the middle of a word of its own (here,
// export with the list "set" printed) is glued to the ":" before it.
// (Oracle seed 156.) The commands around it are read as they were.
func TestParseTraceColonWithAnotherStagesLines(t *testing.T) {
	trace := "+" + traceMark + "eval 'x'\n" +
		"++" + traceMark + "export 'HOME=/h\n" +
		"++" + traceMark + ":\n" +
		"'\\''+@T@HOME=/h\n" +
		"PAGER=rm'\n" +
		"++" + traceMark + "echo done\n"
	var names []string
	for _, c := range parseTrace(trace, true) {
		names = append(names, c.words[0])
	}
	if !slices.Equal(names, []string{"export", "echo"}) {
		t.Errorf("the trace read as %q, want export and echo", names)
	}
}
