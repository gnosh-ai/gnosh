package shelltool

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/shellquote"
)

// The command that copies a file is plain steps with the path written out
// in each, in single quotes, so that nothing in a path is read by the
// other machine's shell.
func TestCopyCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, want string }{
		{"renders/ad.mp4", `ls -ld 'renders/ad.mp4' && test -f 'renders/ad.mp4' && test -r 'renders/ad.mp4' && echo gnosh-file-follows && wc -c < 'renders/ad.mp4' && wc -c < 'renders/ad.mp4' | awk '$1 > 1024 { exit 1 }' && cat 'renders/ad.mp4'`},
		{"/root/it's here.txt", `ls -ld '/root/it'\''s here.txt' && test -f '/root/it'\''s here.txt' && test -r '/root/it'\''s here.txt' && echo gnosh-file-follows && wc -c < '/root/it'\''s here.txt' && wc -c < '/root/it'\''s here.txt' | awk '$1 > 1024 { exit 1 }' && cat '/root/it'\''s here.txt'`},
	} {
		if got := CopyCommand(tc.path, 1024); got != tc.want {
			t.Errorf("CopyCommand(%q) =\n%s\nwant\n%s", tc.path, got, tc.want)
		}
	}
	// A path under the home directory is read from where the command
	// starts, and a name that begins with a dash is no option.
	for path, want := range map[string]string{"~/out/a.txt": "'out/a.txt'", "-rf": "'./-rf'", "~": "'.'", "~/": "'.'", "$(reboot)": "'$(reboot)'"} {
		if got := CopyCommand(path, 1); !strings.HasPrefix(got, "ls -ld "+want+" && ") {
			t.Errorf("CopyCommand(%q) = %s, want the path written as %s", path, got, want)
		}
	}
	if got := CopyCommand("a", -5); !strings.Contains(got, "$1 > 0 {") {
		t.Errorf("a limit below zero is written as %s", got)
	}
}

// copyTo copies a file from the host render into a new directory of the
// test's, and returns how it went and where the copy would be.
func copyTo(t *testing.T, m *Manager, path string, most int64) (FileCopy, string) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "copy")
	return m.CopyFromHost(t.Context(), "render", path, dst, most), dst
}

// A file on a declared host comes back byte for byte, whatever its bytes
// are, through ssh and nothing else; and the manager keeps nothing of it.
func TestCopyFromHost(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	spool := t.TempDir()
	o.SpoolDir = spool
	m := newManager(t, o)

	// Every byte value, NUL and bytes that are not UTF-8 included, and
	// lines that look like the line that announces a file.
	content := make([]byte, 300<<10)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	copy(content, "gnosh-file-follows\n12\n\x00\xff\xfe\r\n")
	if err := os.MkdirAll(filepath.Join(box.home, "renders"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(box.home, "renders", "ad.mp4"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"renders/ad.mp4", "~/renders/ad.mp4", filepath.Join(box.home, "renders", "ad.mp4")} {
		got, dst := copyTo(t, m, path, 1<<20)
		if got.Problem != "" || got.TooBig || got.Size != int64(len(content)) {
			t.Fatalf("copying %s: %+v", path, got)
		}
		data, err := os.ReadFile(dst)
		if err != nil || !bytes.Equal(data, content) {
			t.Fatalf("copying %s: the copy holds %d bytes (%v), and is not the file", path, len(data), err)
		}
		if st, err := os.Stat(dst); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("the copy's mode is %v (%v), want it readable by its owner alone", st.Mode(), err)
		}
	}

	// What ssh was given: its usual arguments, and the copy command as the
	// one line the other machine runs.
	call := box.lastCall(3)
	if got, want := call.Args[len(call.Args)-1], script(shellquote.Quote(CopyCommand(filepath.Join(box.home, "renders", "ad.mp4"), 1<<20))); got != want {
		t.Errorf("ssh was given\n%s\nwant\n%s", got, want)
	}
	if got := strings.Join(call.Args[:len(call.Args)-1], " "); got != "-T -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=20 -- root@203.0.113.7" {
		t.Errorf("ssh's own arguments were %q", got)
	}
	// Nothing is left in the manager's directory, and nothing is listed
	// as a background command.
	left, _ := filepath.Glob(filepath.Join(spool, "*", "*"))
	if len(left) != 0 || len(m.Tasks()) != 0 {
		t.Errorf("after the copies the manager still holds %q and %d tasks", left, len(m.Tasks()))
	}

	// An empty file is a file.
	if err := os.WriteFile(filepath.Join(box.home, "empty"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, dst := copyTo(t, m, "empty", 10)
	if data, err := os.ReadFile(dst); got.Problem != "" || got.Size != 0 || err != nil || len(data) != 0 {
		t.Errorf("copying an empty file: %+v, %q, %v", got, data, err)
	}
}

// What there is no copy of, and what the model is told about each: every
// refusal names the machine and the path, and leaves nothing behind.
func TestCopyFromHostRefusals(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	write := func(name string, n int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(box.home, name), bytes.Repeat([]byte("x"), n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("big.bin", 3000)
	write("ok.bin", 1000)
	if err := os.Mkdir(filepath.Join(box.home, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path string
		most       int64
		tooBig     bool
		size       int64
		want       []string
	}{
		{"a missing file", "nope.mp4", 1000, false, -1, []string{"there is no file at nope.mp4 on render", "is read from " + box.home + " there", "render said:", "nope.mp4"}},
		{"a missing file by its full path", "/nonexistent/nope.mp4", 1000, false, -1, []string{"there is no file at /nonexistent/nope.mp4 on render"}},
		{"a directory", "folder", 1000, false, -1, []string{"folder on render is a directory, not a file"}},
		{"a file over the limit", "big.bin", 1000, true, 3000, []string{"big.bin on render is 2.9 KiB", "only up to 1000 bytes"}},
		{"a file at the limit", "ok.bin", 1000, false, 1000, nil},
	} {
		got, dst := copyTo(t, m, tc.path, tc.most)
		if tc.want == nil {
			if got.Problem != "" {
				t.Errorf("%s: %+v", tc.name, got)
			}
			continue
		}
		if got.Problem == "" || got.TooBig != tc.tooBig || got.Size != tc.size {
			t.Errorf("%s: %+v, want a problem, too big %v and size %d", tc.name, got, tc.tooBig, tc.size)
		}
		for _, piece := range tc.want {
			if !strings.Contains(got.Problem, piece) {
				t.Errorf("%s: the problem is %q, and does not say %q", tc.name, got.Problem, piece)
			}
		}
		if _, err := os.Stat(dst); err == nil {
			t.Errorf("%s: something was left at the destination", tc.name)
		}
	}

	// A file the account cannot read.
	if os.Getuid() != 0 {
		write("closed.bin", 10)
		if err := os.Chmod(filepath.Join(box.home, "closed.bin"), 0); err != nil {
			t.Fatal(err)
		}
		got, _ := copyTo(t, m, "closed.bin", 1000)
		if !strings.Contains(got.Problem, "closed.bin on render cannot be read by root@203.0.113.7") {
			t.Errorf("a file that is closed to the account: %+v", got)
		}
	}

	// A machine that is not declared, and this machine.
	if got := m.CopyFromHost(t.Context(), "elsewhere", "a", filepath.Join(t.TempDir(), "x"), 10); !strings.Contains(got.Problem, `"elsewhere" is not a machine this session may run commands on`) || got.Size != -1 {
		t.Errorf("an undeclared host: %+v", got)
	}
	if got := m.CopyFromHost(t.Context(), LocalName, "a", filepath.Join(t.TempDir(), "x"), 10); !strings.Contains(got.Problem, "is this machine") {
		t.Errorf("this machine as the host: %+v", got)
	}
	if got := m.CopyFromHost(t.Context(), "render", " ", filepath.Join(t.TempDir(), "x"), 10); !strings.Contains(got.Problem, "is empty") {
		t.Errorf("an empty path: %+v", got)
	}
}

// What the account's own start-up files print before the command does not
// end up in the copy.
func TestCopyFromHostPastStartupOutput(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	// The login shell of the made-up machine says something first, as a
	// start-up file that prints does.
	shell := filepath.Join(t.TempDir(), "noisy-sh")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\necho 'Welcome to render.'\necho 'gnosh-file-follows is not said here'\nexec /bin/sh \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	box.plan(plan{Shell: shell})
	m := newManager(t, o)
	if err := os.WriteFile(filepath.Join(box.home, "a.txt"), []byte("the file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, dst := copyTo(t, m, "a.txt", 100)
	if data, err := os.ReadFile(dst); got.Problem != "" || err != nil || string(data) != "the file\n" {
		t.Errorf("the copy: %+v, %q, %v", got, data, err)
	}
}

// A machine that cannot be reached, a login that is refused and a key that
// is not on record are said in the words a command for the host gets, and
// nothing is copied.
func TestCopyFromHostConnectionFailures(t *testing.T) {
	t.Parallel()
	for mode, want := range map[string]string{
		"refuse":      "refused the connection",
		"unknown-key": "gnosh never accepts a machine's key by itself",
		"denied":      "refused the login",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			_, o := newFarBox(t, plan{Mode: mode}, Options{})
			m := newManager(t, o)
			got, dst := copyTo(t, m, "a.txt", 100)
			if !strings.Contains(got.Problem, want) || !strings.Contains(got.Problem, "Nothing was run on render") || got.Size != -1 {
				t.Errorf("the problem is %q, want it to say %q", got.Problem, want)
			}
			if _, err := os.Stat(dst); err == nil {
				t.Error("something was left at the destination")
			}
		})
	}
}

// A copy that loses its connection part of the way is no copy.
func TestCopyFromHostCutShort(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{}, Options{})
	m := newManager(t, o)
	// The far side says a size and then sends less: what a connection
	// that breaks leaves behind. The stand-in's shell is one that prints
	// the start of a copy and stops.
	shell := filepath.Join(t.TempDir(), "short-sh")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nprintf 'gnosh-host-cwd:%s\\n' \"$PWD\" >&2\necho '-rw-r--r-- 1 root root 100 a.bin'\necho gnosh-file-follows\necho 100\nprintf 'only this much'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	box.plan(plan{Shell: shell})
	got, dst := copyTo(t, m, "a.bin", 1000)
	if !strings.Contains(got.Problem, "was cut short: 14 of its 100 bytes arrived") || got.Size != 100 {
		t.Errorf("a copy that was cut short: %+v", got)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("the part that arrived was left at the destination")
	}

	// And one that sends more than it said has grown meanwhile.
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nprintf 'gnosh-host-cwd:%s\\n' \"$PWD\" >&2\necho gnosh-file-follows\necho 4\nprintf 'abcdef'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, dst = copyTo(t, m, "a.bin", 1000)
	if !strings.Contains(got.Problem, "grew while it was copied") {
		t.Errorf("a file that grew: %+v", got)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("the part that arrived was left at the destination")
	}
}

// A copy stops when the call that waits for it is cancelled, and when its
// time is up.
func TestCopyFromHostStops(t *testing.T) {
	t.Parallel()
	box, o := newFarBox(t, plan{Mode: "hang"}, Options{})
	m := newManager(t, o)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan FileCopy, 1)
	dst := filepath.Join(t.TempDir(), "copy")
	go func() { done <- m.CopyFromHost(ctx, "render", "a.bin", dst, 1000) }()
	for len(box.calls()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case got := <-done:
		if !strings.Contains(got.Problem, "was stopped before it finished") {
			t.Errorf("a cancelled copy: %+v", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the copy did not stop when its call was cancelled")
	}

	o.MaxTimeout, o.DefaultTimeout = 300*time.Millisecond, 300*time.Millisecond
	m = newManager(t, o)
	got := m.CopyFromHost(t.Context(), "render", "a.bin", dst, 1000)
	if !strings.Contains(got.Problem, "was still running after 0.3 s and was stopped") {
		t.Errorf("a copy whose time ran out: %+v", got)
	}
}

// Whatever comes down from the other machine, reading it never crashes,
// and no more is written to the destination than the size it gave, and
// nothing at all for a file over the limit.
func FuzzReadCopy(f *testing.F) {
	for _, seed := range []string{
		"gnosh-file-follows\n5\nhello",
		"-rw-r--r-- 1 root root 5 a.txt\ngnosh-file-follows\n     5\nhello",
		"Welcome.\ngnosh-file-follows\n3\nhello world",
		"gnosh-file-follows\n9\nshort",
		"gnosh-file-follows\n100000\nover",
		"gnosh-file-follows\n-4\nx",
		"gnosh-file-follows\nnot a number\nx",
		"gnosh-file-follows",
		"gnosh-file-follows\n",
		"ls: cannot access 'a': No such file or directory\n",
		"drwxr-xr-x 2 root root 4096 Nov 14 10:00 folder\n",
		"", "\n\n\n", "\x00\xff\xfe",
		"gnosh-file-follows is said in the middle\ngnosh-file-follows\n0\n",
	} {
		f.Add([]byte(seed), int64(50))
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, sent []byte, most int64) {
		if most < 0 || most > 1<<20 {
			most %= 1 << 20
			if most < 0 {
				most = -most
			}
		}
		spool, dst := filepath.Join(dir, "spool"), filepath.Join(dir, "dst")
		os.Remove(dst)
		if err := os.WriteFile(spool, sent, 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readCopy(spool, dst, most)
		if err != nil {
			t.Fatalf("reading what was sent failed on this machine: %v", err)
		}
		data, statErr := os.ReadFile(dst)
		switch {
		case !got.marked, got.size < 0, got.size > most:
			if statErr == nil {
				t.Errorf("%d bytes were written for what is no copy: %+v", len(data), got)
			}
		default:
			if statErr != nil || int64(len(data)) != got.copied || got.copied > got.size {
				t.Errorf("the destination holds %d bytes (%v) for %+v", len(data), statErr, got)
			}
			if !bytes.HasSuffix(sent[:len(sent)-restAfter(sent, data)], data) {
				t.Errorf("what was written is not a part of what was sent")
			}
		}
		if strings.ContainsAny(got.said, "\n\r") {
			t.Errorf("what the machine said is not on one line: %q", got.said)
		}
	})
}

// restAfter is how many bytes of sent follow the copy that data is: the
// copy stands right after the first line that announces the file and the
// line that gives its size.
func restAfter(sent, data []byte) int {
	at := 0
	for at < len(sent) {
		end := bytes.IndexByte(sent[at:], '\n')
		if end < 0 {
			return 0
		}
		line := bytes.TrimRight(sent[at:at+end], "\r")
		at += end + 1
		if string(line) == copyMarker {
			break
		}
	}
	end := bytes.IndexByte(sent[at:], '\n')
	if end < 0 {
		return 0
	}
	return len(sent) - (at + end + 1) - len(data)
}
