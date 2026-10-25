package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/session"
)

// viewedFiles keeps, per session, the file reads view gave it since its
// last summary (tools.ViewedFiles), with the file's state then and the
// message whose view call gave them. A summary leaves them behind; a
// rewind forgets those in the messages it cut.
type viewedFiles struct {
	sessions session.Service

	mu    sync.Mutex
	given map[string]viewedFile
}

// viewedFile is a read view gave: the file's state then, and the message
// whose view call gave it ("" when not known).
type viewedFile struct {
	modTime   time.Time
	size      int64
	messageID string
}

func newViewedFiles(sessions session.Service) *viewedFiles {
	return &viewedFiles{sessions: sessions, given: map[string]viewedFile{}}
}

// key is where read is kept for the session: under the session's last
// summary, which leaves behind what was given before it.
func (v *viewedFiles) key(ctx context.Context, sessionID string, read tools.FileRead) string {
	var summary string
	if sess, err := v.sessions.Get(ctx, sessionID); err == nil {
		summary = sess.SummaryMessageID
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", sessionID, summary, read.Path, read.Skipped, read.Limit)
}

// StillGiven implements tools.ViewedFiles.
func (v *viewedFiles) StillGiven(ctx context.Context, sessionID string, read tools.FileRead) bool {
	key := v.key(ctx, sessionID, read)
	v.mu.Lock()
	defer v.mu.Unlock()
	given, ok := v.given[key]
	return ok && given.modTime.Equal(read.ModTime) && given.size == read.Size
}

// Give implements tools.ViewedFiles.
func (v *viewedFiles) Give(ctx context.Context, sessionID string, read tools.FileRead) {
	key := v.key(ctx, sessionID, read)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.given[key] = viewedFile{modTime: read.ModTime, size: read.Size, messageID: tools.GetMessageFromContext(ctx)}
}

// forget forgets the reads the session was given in the messages a
// rewind cut, and those given it with no message known.
func (v *viewedFiles) forget(sessionID string, cut map[string]bool) {
	if v == nil {
		return
	}
	prefix := sessionID + "\x00"
	v.mu.Lock()
	defer v.mu.Unlock()
	for key, given := range v.given {
		if strings.HasPrefix(key, prefix) && (given.messageID == "" || cut[given.messageID]) {
			delete(v.given, key)
		}
	}
}
