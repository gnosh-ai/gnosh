package shelltool

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/shellquote"
)

// Copying a file back from another machine.
//
// A file on a declared host reaches this machine by the road every command
// for that host takes, and by no other: the system's ssh, run with the
// arguments of sshArgs, runs one command there, and what the command
// prints comes down ssh's standard output into the command's output file
// on this machine. There is no scp and no second kind of connection, so
// everything that is true of a command on a host is true of a copy: ssh
// never asks, a machine whose key is not on record is not connected to,
// and what ssh itself says is kept apart from what arrives.
//
// The command (CopyCommand) is made of plain steps, with the path written
// out in single quotes in each and nothing left for the other machine's
// shell to fill in, so that whoever judges the command before it leaves
// reads exactly what will run:
//
//	ls -ld 'P' && test -f 'P' && test -r 'P' && echo gnosh-file-follows &&
//	wc -c < 'P' && wc -c < 'P' | awk '$1 > MAX { exit 1 }' && cat 'P'
//
// ls says what is at the path, in the words of that machine, which is what
// the model is told when there is no file to copy. The line
// gnosh-file-follows marks where the copy begins, whatever the account's
// own start-up files printed before it. Then comes the file's size, on a
// line of its own, and then the file, unless it is larger than MAX bytes:
// the other machine then sends nothing more, and the size says why.
//
// On this side the size line says how many bytes to take. Fewer mean the
// copy was cut short; more mean the file grew while it was read. Either
// way there is no copy.

// copyMarker is the line the other machine prints before the size and the
// bytes of the file.
const copyMarker = "gnosh-file-follows"

// copySlack is how much room the output file of a copy has beyond the
// file itself: for the lines before the file, and for whatever the
// account's start-up files print.
const copySlack = 256 << 10

// FileCopy is how copying a file back from a declared host came out.
type FileCopy struct {
	// Size is how many bytes the file has on the other machine, or -1
	// when that was never learned.
	Size int64
	// TooBig says the file is larger than the most that was to be copied.
	// Nothing of it was sent.
	TooBig bool
	// Problem says why there is no copy, in plain words that name the
	// machine and the path: it is what a model is told. It is empty when
	// the whole file is at the destination.
	Problem string
}

// farPath is a path as the copy command names it on the other machine. A
// command there starts in the account's home directory, so "~/x" is "x";
// and a name that begins with a dash is given a "./" in front, so that no
// program takes it for an option.
func farPath(path string) string {
	switch {
	case path == "~":
		return "."
	case strings.HasPrefix(path, "~/"):
		path = strings.TrimLeft(path[2:], "/")
		if path == "" {
			return "."
		}
	}
	if strings.HasPrefix(path, "-") {
		return "./" + path
	}
	return path
}

// CopyCommand returns the command CopyFromHost runs on the other machine
// for the file at path, which is copied when it is at most most bytes long. It is
// what a caller shows a person who is asked whether the copy may be made,
// and what the permission rules are held against.
func CopyCommand(path string, most int64) string {
	p := shellquote.Quote(farPath(path))
	return "ls -ld " + p + " && test -f " + p + " && test -r " + p +
		" && echo " + copyMarker +
		" && wc -c < " + p +
		" && wc -c < " + p + " | awk '$1 > " + strconv.FormatInt(max(most, 0), 10) + " { exit 1 }'" +
		" && cat " + p
}

// CopyFromHost copies the regular file at path on the declared host called
// name to dst on this machine, when the file is at most most bytes long. A
// relative path is under the home directory of the account the host was
// declared with, where every command there starts.
//
// It logs in exactly as a command for the host does and runs CopyCommand
// there; nothing else is run and nothing is written on the other machine.
// The copy may take as long as the longest foreground command
// (Options.MaxTimeout), and it stops when ctx is cancelled or the manager
// is closed. dst is written only when the whole file has arrived: on any
// failure it is not there afterwards, and FileCopy.Problem says what
// happened.
func (m *Manager) CopyFromHost(ctx context.Context, name, path, dst string, most int64) FileCopy {
	none := func(format string, args ...any) FileCopy {
		return FileCopy{Size: -1, Problem: fmt.Sprintf(format, args...)}
	}
	h, err := m.findHost(name)
	switch {
	case err != nil:
		return none("%s", err.Error())
	case h == nil:
		return none("%q is this machine, not a declared host, so there is nothing to copy back", name)
	case strings.TrimSpace(path) == "" || strings.IndexByte(path, 0) >= 0:
		return none("the path of the file on %s is empty, or holds a NUL byte", h.Name)
	}
	most = max(most, 0)
	a := bashArgs{command: CopyCommand(path, most), host: h, limit: m.opt.MaxTimeout, spool: most + copySlack}
	j, err := m.start(a, &core.ToolEnv{Cwd: string(filepath.Separator)}, "")
	if err != nil {
		return none("%s", strings.TrimPrefix(err.Error(), "shelltool: "))
	}
	j.attend(ctx, a.limit)
	m.unlist(j)
	defer os.Remove(j.out.path)
	e := j.ending()
	far := j.farEnd(e)

	where := path + " on " + h.Name
	notes := func(first string) string {
		parts := []string{strings.TrimSuffix(first, ".")}
		for _, n := range far.notes {
			if n = strings.TrimSpace(n); n != "" {
				parts = append(parts, strings.TrimSuffix(n, "."))
			}
		}
		return strings.Join(parts, ". ")
	}
	switch {
	case e.reason == endTimeout:
		return none("%s", notes(fmt.Sprintf("the copy of %s was still running after %s and was stopped", where, seconds(a.limit))))
	case e.reason == endOutputLimit:
		return none("%s", notes(fmt.Sprintf("%s sent more than %s for %s, so the copy was stopped", h.Name, byteSize(m.opt.MaxOutputBytes), path)))
	case e.reason != endExited:
		return none("%s", notes(fmt.Sprintf("the copy of %s was stopped before it finished", where)))
	case far.reason == ReasonNoConnection:
		return none("%s", notes(strings.TrimPrefix(far.headline, "not run: ")))
	}

	got, err := readCopy(j.out.path, dst, most)
	if err != nil {
		os.Remove(dst)
		return none("the copy of %s arrived, and could not be written to %s on this machine: %v", where, dst, err)
	}
	fail := func(c FileCopy) FileCopy {
		os.Remove(dst)
		return c
	}
	said := ""
	if got.said != "" {
		said = " " + h.Name + " said: " + got.said
	}
	switch {
	case !got.marked && far.unsure:
		return fail(none("%s", notes("nothing was copied from "+h.Name)))
	case !got.marked && far.reason == ReasonConnectionLost:
		return fail(none("%s", notes(strings.TrimSuffix(far.headline, "; what it had printed is below"))))
	case !got.marked:
		home := ""
		if !filepath.IsAbs(farPath(path)) && far.dir != "" {
			home = fmt.Sprintf(" A path that does not begin with a slash is read from %s there, the home directory of %s.", far.dir, h.Target)
		}
		switch {
		case strings.Contains(strings.ToLower(got.said), "no such file"):
			return fail(none("there is no file at %s.%s%s", where, home, said))
		case got.listed == 'd':
			return fail(none("%s is a directory, not a file.%s", where, said))
		case got.listed == '-' || got.listed == 'l':
			return fail(none("%s cannot be read by %s, or is not a regular file.%s", where, h.Target, said))
		}
		return fail(none("%s is not a file %s can read.%s%s", where, h.Target, home, said))
	case got.size < 0:
		return fail(none("%s did not say how large %s is, so nothing was copied.%s", h.Name, path, said))
	case got.size > most:
		return fail(FileCopy{Size: got.size, TooBig: true,
			Problem: fmt.Sprintf("%s is %s, and a file is copied back from another machine only up to %s", where, byteSize(got.size), byteSize(most))})
	case got.copied < got.size:
		c := none("%s", notes(fmt.Sprintf("the copy of %s was cut short: %d of its %d bytes arrived", where, got.copied, got.size)))
		c.Size = got.size
		return fail(c)
	case got.more:
		return fail(FileCopy{Size: got.size,
			Problem: fmt.Sprintf("%s grew while it was copied (it was %d bytes long when it was measured), so the copy is not the whole file. Copy it again once whatever writes it has finished", where, got.size)})
	case e.code != 0 || far.reason != "":
		return fail(FileCopy{Size: got.size,
			Problem: notes(fmt.Sprintf("the copy of %s ended with exit code %d, so it is not known to be whole", where, e.code))})
	}
	return FileCopy{Size: got.size}
}

// copyRead is what the output file of a copy command held.
type copyRead struct {
	// marked says the line that announces the file was found.
	marked bool
	// size is the size the other machine gave, or -1 when the line after
	// the marker was not a number.
	size int64
	// copied is how many bytes of the file were written to the
	// destination, and more says bytes followed the last of them.
	copied int64
	more   bool
	// said is the end of what the other machine printed before the
	// marker, or in place of it: what ls reported, on one line. listed is
	// the first character of ls's line for the path when there is one: "-"
	// for a file, "d" for a directory, "l" for a link.
	said   string
	listed byte
}

// readCopy reads the output file of a copy command and writes the file it
// carries to dst. An error is this machine's own trouble with one of the
// two files; what the other machine sent, or did not, is in the result.
func readCopy(spool, dst string, most int64) (copyRead, error) {
	got := copyRead{size: -1}
	f, err := os.Open(spool)
	if err != nil {
		return got, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)

	// The lines before the marker: the account's own start-up output, if
	// it has any, and then what ls said. Only their end is kept.
	const keepBefore = 4 << 10
	var before []byte
	keep := func(line []byte) {
		before = append(before, line...)
		if len(before) > keepBefore {
			before = before[len(before)-keepBefore:]
		}
	}
	for !got.marked {
		line, err := r.ReadSlice('\n')
		if bytes.Equal(bytes.TrimRight(line, "\r\n"), []byte(copyMarker)) && err == nil {
			got.marked = true
			break
		}
		keep(line)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			break
		}
	}
	lines := strings.Split(strings.TrimSpace(clean(before)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		got.said = oneLine(last, 300)
		if len(last) > 10 && strings.IndexByte("-dlbcps", last[0]) >= 0 && strings.IndexByte("r-", last[1]) >= 0 {
			// The line ls writes for a path begins with its kind and
			// its permissions: "-rw-r--r--", "drwxr-xr-x".
			got.listed = last[0]
		}
	}
	if !got.marked {
		return got, nil
	}

	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return got, nil
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
	if perr != nil || n < 0 {
		got.said = oneLine(line, 300)
		return got, nil
	}
	got.size = n
	if n > most {
		return got, nil
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return got, err
	}
	copied, err := io.CopyN(out, r, n)
	got.copied = copied
	if cerr := out.Close(); err == nil || errors.Is(err, io.EOF) {
		err = cerr
	}
	if err != nil {
		return got, err
	}
	if _, err := r.ReadByte(); err == nil {
		got.more = true
	}
	return got, nil
}
