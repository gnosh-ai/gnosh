package app

import (
	"testing"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// What the person is told of a model server's slots while a request of
// about 12k tokens waits.
func TestLoadText(t *testing.T) {
	slot := func(busy bool, prompt, read int) core.SlotLoad {
		return core.SlotLoad{Busy: busy, Prompt: prompt, Read: read}
	}
	// A slot writing its reply, on a server that counts the reply's
	// tokens in with the prompt's.
	replying := func(prompt, read int) core.SlotLoad {
		return core.SlotLoad{Busy: true, Prompt: prompt, Read: read, Replying: true}
	}
	idle := slot(false, 0, 0)
	for _, c := range []struct {
		slots []core.SlotLoad
		want  string
	}{
		{[]core.SlotLoad{idle}, ""},
		{[]core.SlotLoad{slot(true, 12400, 8100)}, "reading the prompt: 8.1k of 12.4k tokens"},
		{[]core.SlotLoad{slot(true, 11000, 5000), idle}, "reading the prompt: 5k of about 12k tokens"},
		{[]core.SlotLoad{slot(true, 12400, 8100), slot(true, 30000, 1000), idle}, "reading the prompt: 8.1k of 12.4k tokens, beside 1 other request"},
		{[]core.SlotLoad{slot(true, 40000, 3000)}, "the server is busy with another request; yours waits"},
		{[]core.SlotLoad{slot(true, 40000, 3000), slot(true, 900, 900), slot(true, 50000, 0)}, "the server's 3 slots are all busy with other requests; yours waits"},
		{[]core.SlotLoad{slot(true, 40000, 3000), idle, idle}, "the server is also answering 1 other request"},
		{[]core.SlotLoad{slot(true, 40000, 3000), slot(true, 900, 900), idle}, "the server is also answering 2 other requests"},
		// A server that counts a prompt only as far as it has read it: the
		// one slot reading a prompt not yet past this one's length, with a
		// slot idle so that this request waits for none, is this one's.
		{[]core.SlotLoad{slot(true, 6100, 4052), idle, idle}, "reading the prompt: 4.1k of about 12k tokens"},
		{[]core.SlotLoad{slot(true, 6100, 4052), replying(1124, 900), idle}, "reading the prompt: 4.1k of about 12k tokens, beside 1 other request"},
		// Two such slots: which is this one's cannot be told.
		{[]core.SlotLoad{slot(true, 6100, 4052), slot(true, 5000, 2952), idle}, "the server is also answering 2 other requests"},
		// No slot idle: this request may be waiting, and the reading slot
		// someone else's.
		{[]core.SlotLoad{slot(true, 6100, 4052)}, "the server is busy with another request; yours waits"},
	} {
		if got := loadText(core.ServerLoad{Slots: c.slots}, 12000); got != c.want {
			t.Errorf("loadText(%+v) = %q, want %q", c.slots, got, c.want)
		}
	}
	// The Mac Studio's slots minutes into reading a long prompt, which
	// were shown as another request.
	studio := []core.SlotLoad{idle, idle, slot(true, 77932, 75884), idle}
	if got, want := loadText(core.ServerLoad{Slots: studio}, 215000), "reading the prompt: 75.9k of about 215k tokens"; got != want {
		t.Errorf("loadText(%+v) = %q, want %q", studio, got, want)
	}
	for n, want := range map[int]string{950: "950", 8100: "8.1k", 12000: "12k", 131072: "131.1k"} {
		if got := kTokens(n); got != want {
			t.Errorf("kTokens(%d) = %q, want %q", n, got, want)
		}
	}
}
