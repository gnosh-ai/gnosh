package app

import (
	"os"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// A summary gives back the files the model read or changed last, read
// again as they now are, and they count as read: an Edit of one right
// after the summary needs no Read first. A file too long to give back is
// named, and must be read again before it is changed.
func TestSummaryGivesBackTheFilesLastWorkedOn(t *testing.T) {
	w := apptest.NewWorld(t)
	big := w.Write("big.txt", strings.Repeat("a big file line\n", 800))
	b := w.Write("b.txt", "beta line\n")
	a := w.Write("a.txt", "alpha line\n")
	read := func(p string) apptest.Reply {
		return apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Read", Args: `{"file_path":"` + p + `"}`}}}
	}
	w.Model.Add(read(big), read(b), read(a),
		apptest.Reply{Text: "Read all three."},
		apptest.Reply{Text: "SUMMARY: three files were read."},
		apptest.Reply{Calls: []apptest.Call{{ID: "c1", Name: "Edit", Args: `{"file_path":"` + a + `","old_string":"alpha","new_string":"ALPHA"}`}}},
		apptest.Reply{Calls: []apptest.Call{{ID: "c2", Name: "Edit", Args: `{"file_path":"` + big + `","old_string":"a big file line","new_string":"x","replace_all":true}`}}},
		apptest.Reply{Text: "One of them is done."})
	s := sit(t, w, "--permission-mode", "acceptEdits")
	s.see("Type a prompt and Enter.")
	s.typeLine("read the three files")
	s.see("Read all three.")
	s.typeLine("/compact")
	s.see("Given back after the summary, as they now are: a.txt, b.txt.")
	s.see("The conversation was summarized")
	s.typeLine("capitalize them")
	s.see("One of them is done.")
	s.typeLine("/exit")
	if code := s.exit(); code != 0 {
		t.Errorf("exit code %d", code)
	}

	reqs := w.Model.Requests()
	after := strings.Join(userTexts(reqs[5]), "\n")
	has(t, "what the model was given back after the summary", after,
		"Before the summary you read or changed the file "+a, "alpha line",
		"Before the summary you read or changed the file "+b, "beta line",
		"Read or changed before the summary, and not shown again: "+big+". Read a file again")
	hasNot(t, "what the model was given back after the summary", after, "a big file line")
	if strings.Index(after, a) > strings.Index(after, b) {
		t.Errorf("the file read last does not come first")
	}
	if data, _ := os.ReadFile(a); string(data) != "ALPHA line\n" {
		t.Errorf("the Edit right after the summary did not work: %q", data)
	}
	_, texts := toolResults(reqs[len(reqs)-1])
	if last := texts[len(texts)-1]; !strings.Contains(last, "has not read") {
		t.Errorf("the Edit of the file not given back was not refused: %s", clip(last))
	}
	if data, _ := os.ReadFile(big); !strings.HasPrefix(string(data), "a big file line\n") {
		t.Errorf("the file not given back was changed")
	}
}
