package agent

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/charmbracelet/crush/internal/message"
)

// The check of a close (CLOSE-CHECK.md, Jae 2026-11-05). Before mem keeps
// a call that closes a todo with nothing under it as done, gnovm_run hands
// the close here (gnovmtools.CheckCloses). With no thinking, a model
// wrote a todo's result before its work ("Median added in stats.go"), and
// one look at a file was enough for mem's guard to let the same claim
// through (QA, 2026-11-04, A20). Cheapest first:
//
//   - a todo the call entered itself and closed with nothing done in it is
//     refused, as the fresh guard refuses one entered the round before;
//   - the claim check: a result that says a file changed, when nothing in
//     the todo changed or showed it, is refused;
//   - the review (options.mem_review_closes, on by default): the small
//     model reads what happened in the todo and judges whether it shows the
//     result.

// checkCloses checks each close a call to mem makes, in order, and is
// the first refusal, or "" when all are approved.
func (c *coordinator) checkCloses(ctx context.Context, sessionID, callID string, closes []gnovm.MemClose) string {
	for _, cl := range closes {
		if refusal := c.checkClose(ctx, sessionID, callID, cl); refusal != "" {
			return refusal
		}
	}
	return ""
}

// checkClose checks one close: "" approves it, else the refusal.
func (c *coordinator) checkClose(ctx context.Context, sessionID, callID string, cl gnovm.MemClose) string {
	w, ok := c.frames.closeWindow(ctx, sessionID, cl, callID)
	if cl.Entered && !cl.Worked && (!ok || !workIn(w.rows, cl.Id)) {
		return enteredHereText(cl.Id)
	}
	if !ok {
		return ""
	}
	verb := "popped"
	if !w.onStack {
		verb = "closed"
	}
	if f, made := unbackedClaim(cl.Result, w.rows, w.start, w.named, c.cfg.WorkingDir()); f != "" {
		return claimText(verb, cl.Id, f, made)
	}
	if !c.cfg.Config().Options.ReviewsCloses() {
		return ""
	}
	agent, ok := c.currentAgent().(closeReviewer)
	if !ok {
		return ""
	}
	key := strings.Join([]string{sessionID, cl.Id, cl.Result, lastWork(w.rows), w.code}, "\x00")
	v, cached := c.closeVerdicts.get(key)
	if !cached {
		review, cancel := context.WithTimeout(ctx, closeReviewTimeout)
		start := time.Now()
		ok, reason, err := agent.reviewClose(review, sessionID, cl, w, c.cfg.WorkingDir())
		cancel()
		switch {
		case ctx.Err() != nil:
			return fmt.Sprintf("not %s: the check of `%s` was cancelled, so nothing was kept", verb, cl.Id)
		case err != nil:
			slog.Warn("A todo's close could not be checked; it is kept unchecked", "session_id", sessionID, "todo", cl.Id, "error", err)
			agent.notifyGoal(sessionID, notify.TypeWarning, fmt.Sprintf("The close of `%s` could not be checked, so it was kept unchecked: %s", cl.Id, err))
			return ""
		}
		slog.Info("Reviewed a todo's close", "session_id", sessionID, "todo", cl.Id, "ok", ok, "reason", reason, "seconds", time.Since(start).Seconds())
		v = closeVerdict{ok: ok, reason: reason}
		c.closeVerdicts.put(key, v)
	}
	if !v.ok {
		return reviewText(verb, cl.Id, v.reason)
	}
	c.reviewedCloses.add(callID, sessionID, cl)
	return ""
}

// closesKept is told, once gnovm_run is done with a call whose closes were
// checked, whether the call was kept: a goal whose close the review
// approved in a call that was kept is not judged again at the turn's end;
// one in a call refused or failed after is.
func (c *coordinator) closesKept(_ context.Context, sessionID, callID string, kept bool) {
	approved := c.reviewedCloses.take(callID)
	if !kept {
		return
	}
	agent, ok := c.currentAgent().(closeReviewer)
	if !ok {
		return
	}
	for _, cl := range approved {
		if !strings.Contains(cl.Id, "/") {
			agent.reviewedGoal(sessionID, cl.Id, cl.Result)
		}
	}
}

// reviewedCloses is, by gnovm_run call, the closes the review approved in
// it, until the call is kept or not (closesKept).
type reviewedCloses struct {
	mu    sync.Mutex
	calls map[string][]gnovm.MemClose
}

func (r *reviewedCloses) add(callID, _ string, cl gnovm.MemClose) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string][]gnovm.MemClose{}
	}
	r.calls[callID] = append(r.calls[callID], cl)
}

func (r *reviewedCloses) take(callID string) []gnovm.MemClose {
	r.mu.Lock()
	defer r.mu.Unlock()
	cl := r.calls[callID]
	delete(r.calls, callID)
	return cl
}

// closeReviewer is what of a session agent the check of closes uses.
type closeReviewer interface {
	reviewClose(ctx context.Context, sessionID string, cl gnovm.MemClose, w closeWindow, workingDir string) (bool, string, error)
	notifyGoal(sessionID string, kind notify.Type, msg string)
	reviewedGoal(sessionID, name, result string)
}

// closeReviewTimeout bounds a review: past it, the close is kept
// unchecked, and the user told.
const closeReviewTimeout = 3 * time.Minute

// The refusals begin as mem's own refusal of a fresh pop does, "not
// popped: … `todo`", which closeRefused reads.
func enteredHereText(id string) string {
	return fmt.Sprintf("not popped: this call entered `%s` and closed it with nothing done in it, so nothing from the call was kept and you are not in `%s`. Enter it, do its work, then pop it with what came of it.", id, id)
}

func claimText(verb, id, file string, made bool) string {
	if made {
		return fmt.Sprintf("not %s: nothing in `%s` made `%s`, which your result says was made, and there is no such file. Make it, then close `%s` with what came of it; or close it with what you did instead.", verb, id, file, id)
	}
	return fmt.Sprintf("not %s: nothing in `%s` changed or showed `%s`, which your result says changed. If the change is still to make, make it; if it was made before, view it here so it shows; then close `%s` with what came of it.", verb, id, file, id)
}

func reviewText(verb, id, reason string) string {
	return fmt.Sprintf("not %s: the check of `%s` found: %s. Do what is missing; if it is done already, show it here (view it, or run what checks it); then close `%s` with what came of it. If it cannot be done now, mem.Leave(\"why\") leaves it open; either way, tell the user plainly how it stands.",
		verb, id, strings.TrimSuffix(compactReason(reason), "."), id)
}

// closeRefusal is a refusal of a close of a todo, by mem (refuseFresh) or
// by the check, with the todo's name: the frame on top, which the guard,
// the loop's nudge and the turn's end read.
var closeRefusal = regexp.MustCompile("not (?:popped|closed): (?:nothing came between your entering|this call entered|nothing in|the check of) `([^`]+)`")

// refusedClose is the todo content refuses to close, or "".
func refusedClose(content string) string {
	if m := closeRefusal.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	return ""
}

// closeWindow is what happened in a todo whose close is checked: rows,
// from the round after the one that entered it up to the call closing it,
// with the calls made before it in its own round and what they returned;
// start is when the window began, a second after its first row, so the
// todo before it (rows keep whole seconds) is never taken for its work.
// When the round that entered it closed nothing, the window starts at the
// session's last frame before it, in the same turn, so work done on the
// way counts. A todo the call itself entered has only the call's own
// round, and code, the call's mem input. A todo not on the stack (Done by
// its name) has the rows since the present frame was entered, or the
// turn's at the root. named is the files the session's calls named, by
// base name, for the claims.
type closeWindow struct {
	rows    []message.Message
	start   time.Time
	onStack bool
	named   map[string][]string
	// prompt is what the user asked in the turn, when the rows hold none
	// of what the user said; code, the closing call's input, for a todo
	// that call entered.
	prompt string
	code   string
}

func (f *frameStacks) closeWindow(ctx context.Context, sessionID string, cl gnovm.MemClose, callID string) (closeWindow, bool) {
	if f == nil || sessionID == "" || callID == "" {
		return closeWindow{}, false
	}
	f.mu.Lock()
	rec := f.record(ctx, sessionID)
	var push, top string
	for _, fr := range rec.stack {
		if fr.id == cl.Id {
			push = fr.callID
		}
		top = fr.callID
	}
	f.mu.Unlock()
	if err := f.messages.FlushAll(ctx); err != nil {
		return closeWindow{}, false
	}
	msgs, err := f.messages.List(ctx, sessionID)
	if err != nil {
		return closeWindow{}, false
	}
	round := func(call string) int {
		if call == "" {
			return -1
		}
		return slices.IndexFunc(msgs, func(m message.Message) bool { return m.Role == message.Assistant && hasToolCall(m, call) })
	}
	end := round(callID)
	if end < 0 {
		return closeWindow{}, false
	}
	w := closeWindow{onStack: push != "", named: namedFiles(msgs)}
	var rows []message.Message
	first := end
	switch at := round(push); {
	case cl.Entered:
		w.code = callCode(msgs[end], callID)
	case w.onStack && at < 0:
		// The push is gone from the rows: what was done in the todo cannot
		// be read.
		return closeWindow{}, false
	case w.onStack && roundPops(msgs, at, sessionID):
		rows = poppedRows(msgs, push, callID)
		first = afterRound(msgs, at, end)
	case w.onStack:
		first = min(at, max(turnStartAt(msgs, at), lastFrameEvent(msgs[:at], sessionID)+1))
		rows = msgs[first:end]
	default:
		// Done by its name: from the session's last frame before the call
		// (a sibling's pop, a Leave of this very todo), never before the
		// present frame's push or the turn, so a sibling's work is not
		// this todo's.
		first = turnStartAt(msgs, end)
		if t := round(top); t >= 0 {
			first = afterRound(msgs, t, end)
		}
		if e := lastFrameEvent(msgs[:end], sessionID); e >= first {
			first = afterRound(msgs, e-1, end)
		}
		rows = msgs[first:end]
	}
	for _, m := range rows {
		if !m.IsSummaryMessage {
			w.rows = append(w.rows, m)
		}
	}
	w.rows = append(w.rows, sameRound(msgs, end, callID)...)
	if first > end {
		first = end
	}
	w.start = time.Unix(msgs[first].CreatedAt+1, 0)
	if !slices.ContainsFunc(w.rows, func(m message.Message) bool { return m.Role == message.User && !m.Content().Hidden }) {
		if t := turnStartAt(msgs, end); t < len(msgs) {
			for _, m := range msgs[t:end] {
				if text := m.Content(); m.Role == message.User && !text.Hidden && strings.TrimSpace(text.Text) != "" {
					w.prompt = text.Text
					break
				}
			}
		}
	}
	return w, true
}

// roundPops reports whether the round at msgs[at] also popped, left or
// dropped a frame of sessionID's: mem.Pop("…") and mem.Next() in one call.
// Then nothing comes between the todo before and this one.
func roundPops(msgs []message.Message, at int, sessionID string) bool {
	calls := map[string]bool{}
	for _, c := range msgs[at].ToolCalls() {
		calls[c.ID] = true
	}
	for _, m := range msgs[at+1:] {
		if m.Role != message.Tool {
			break
		}
		for _, r := range m.ToolResults() {
			if !calls[r.ToolCallID] {
				continue
			}
			for _, ev := range FrameEvents(r) {
				if ev.Session == sessionID && ev.Kind == "pop" {
					return true
				}
			}
		}
	}
	return false
}

// afterRound is the index of the first row after round at's tool rows,
// at most end.
func afterRound(msgs []message.Message, at, end int) int {
	i := at + 1
	for i < end && msgs[i].Role == message.Tool {
		i++
	}
	return min(i, end)
}

// sameRound is what came before callID in its own round, msgs[end]: the
// calls made before it, as an assistant row of their own, and the rows
// that answered them.
func sameRound(msgs []message.Message, end int, callID string) []message.Message {
	var before []message.ContentPart
	ids := map[string]bool{}
	for _, p := range msgs[end].Parts {
		c, ok := p.(message.ToolCall)
		if !ok {
			continue
		}
		if c.ID == callID {
			break
		}
		before = append(before, c)
		ids[c.ID] = true
	}
	if len(before) == 0 {
		return nil
	}
	out := []message.Message{{ID: msgs[end].ID, Role: message.Assistant, SessionID: msgs[end].SessionID, Parts: before, CreatedAt: msgs[end].CreatedAt}}
	for _, m := range msgs[end+1:] {
		if m.Role != message.Tool {
			break
		}
		var answers []message.ContentPart
		for _, r := range m.ToolResults() {
			if ids[r.ToolCallID] {
				answers = append(answers, r)
			}
		}
		if len(answers) > 0 {
			out = append(out, message.Message{ID: m.ID, Role: message.Tool, SessionID: m.SessionID, Parts: answers, CreatedAt: m.CreatedAt})
		}
	}
	return out
}

// callCode is the mem input of call callID in row m.
func callCode(m message.Message, callID string) string {
	for _, c := range m.ToolCalls() {
		if c.ID == callID {
			var in struct {
				Code string `json:"code"`
			}
			if json.Unmarshal([]byte(c.Input), &in) == nil {
				return in.Code
			}
		}
	}
	return ""
}

// lastFrameEvent is the index of the last row of msgs holding a push or
// pop of sessionID's, or -1.
func lastFrameEvent(msgs []message.Message, sessionID string) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, r := range msgs[i].ToolResults() {
			for _, ev := range FrameEvents(r) {
				if ev.Session == sessionID {
					return i
				}
			}
		}
	}
	return -1
}

// namedFiles is the files the calls in msgs named by path (file_path,
// path), by base name.
func namedFiles(msgs []message.Message) map[string][]string {
	named := map[string][]string{}
	for _, m := range msgs {
		for _, call := range m.ToolCalls() {
			var p struct {
				FilePath string `json:"file_path"`
				Path     string `json:"path"`
			}
			if json.Unmarshal([]byte(call.Input), &p) != nil {
				continue
			}
			for _, path := range []string{p.FilePath, p.Path} {
				if path == "" {
					continue
				}
				base := filepath.Base(path)
				if !slices.Contains(named[base], path) {
					named[base] = append(named[base], path)
				}
			}
		}
	}
	return named
}

// claimClause splits a result into clauses: a claim, and what says it is
// not one, are read within one.
var claimClause = regexp.MustCompile(`;|\n|\.\s+|\s+[—–]\s+|\s+-\s+`)

// claimNow is a file said to read some new way: "README.md:3 now says
// Get". claimMade is a change said done to what follows it: "Median added
// in stats.go", "Updated main.go:10 to call Get". claimFile is a word that
// names a file, with an extension that begins with a letter (not v1.4),
// and a line or lines after it. claimNot marks a clause that says what is
// so already, what is to come, or what was done elsewhere: no claim of a
// change made in the todo.
var (
	claimFileExpr = "((?:[A-Za-z0-9_.\\-]+/)*[A-Za-z0-9_\\-][A-Za-z0-9_.\\-]*\\.[A-Za-z][A-Za-z0-9]{0,7})(?::\\d+(?:-\\d+)?)?"
	claimNow      = regexp.MustCompile("(?:^|[\\s(`'\"])" + claimFileExpr + "[`'\")]?\\s+now\\s+(?:says|reads|returns|documents|has|calls|uses|prints|shows|takes|lists|includes|contains|handles|checks|gives|refuses|accepts|is|does)\\b")
	claimMade     = regexp.MustCompile(`(?i)\b(?:added|created|wrote|written|edited|changed|updated|fixed|renamed|removed|deleted|replaced|moved|implemented|inserted|appended|bumped|refactored|rewrote|rewritten|converted|documented)\b`)
	claimFile     = regexp.MustCompile("(?:^|[\\s(`'\"])" + claimFileExpr)
	claimNot      = regexp.MustCompile(`(?i)\b(?:already|still|unchanged|untouched|no\s+change|not\s+changed|no\s+edits?|nothing\s+to|needs?\s+no|needed\s+no|no\s+need|not\s+needed|did\s+not|didn't|does\s+not|doesn't|will|must|should|need\s+to|needs\s+to|next|later|before|earlier|per|todo|pending|left\s+for)\b`)
	claimCreate   = regexp.MustCompile(`(?i)\b(?:added|created|wrote|written)\b`)
)

// fileExts are the file kinds a result may say were made though no such
// file is found: a claim that names something else ("client.Get") is no
// claim of a file.
var fileExts = map[string]bool{
	"go": true, "gno": true, "mod": true, "sum": true, "md": true, "txt": true, "json": true, "yaml": true, "yml": true, "toml": true,
	"py": true, "js": true, "ts": true, "tsx": true, "jsx": true, "rs": true, "c": true, "h": true, "cc": true, "cpp": true,
	"java": true, "rb": true, "sh": true, "html": true, "css": true, "sql": true, "proto": true, "csv": true, "xml": true, "ini": true, "cfg": true,
}

// fileClaim is a file a result says changed, and whether it says the
// file was made.
type fileClaim struct {
	file string
	made bool
}

// changeClaims is the files result says changed in the todo, each once:
// a file said to now read some way, or one named after a word that says a
// change was made, in a clause that does not say it is so already, is to
// come, or was done elsewhere.
func changeClaims(result string) []fileClaim {
	var claims []fileClaim
	seen := map[string]bool{}
	add := func(file string, made bool) {
		file = strings.TrimRight(file, ".")
		if !seen[file] && !strings.Contains(file, "://") {
			seen[file] = true
			claims = append(claims, fileClaim{file, made})
		}
	}
	for _, clause := range claimClause.Split(result, -1) {
		if claimNot.MatchString(clause) {
			continue
		}
		for _, m := range claimNow.FindAllStringSubmatch(clause, -1) {
			add(m[1], false)
		}
		if loc := claimMade.FindStringIndex(clause); loc != nil {
			made := claimCreate.MatchString(clause)
			for _, m := range claimFile.FindAllStringSubmatch(clause[loc[1]:], -1) {
				add(m[1], made)
			}
			// "stats.go updated", the file before the word.
			if m := claimFile.FindAllStringSubmatch(clause[:loc[0]], -1); len(m) > 0 && strings.TrimSpace(claimFile.ReplaceAllString(clause[:loc[0]], "")) == "" {
				for _, x := range m {
					add(x[1], made)
				}
			}
		}
	}
	return claims
}

// unbackedClaim is a file result says changed that nothing in rows, from
// since on, changed or showed: no edit or write of it, no change to it
// since then (changedAt), and no view of it. made is set
// when the result says it was made and there is no such file. "" when
// every file the result says changed is backed, or it names none.
func unbackedClaim(result string, rows []message.Message, since time.Time, named map[string][]string, workingDir string) (file string, made bool) {
	claims := changeClaims(result)
	if len(claims) == 0 {
		return "", false
	}
	calls := map[string]message.ToolCall{}
	for _, row := range rows {
		for _, c := range row.ToolCalls() {
			calls[c.ID] = c
		}
	}
	var touched []string
	for _, row := range rows {
		for _, r := range row.ToolResults() {
			if r.IsError {
				continue
			}
			call := calls[r.ToolCallID]
			for _, f := range tools.ChangedFiles(r.Name, call.Input, r.Metadata) {
				touched = append(touched, normalizePath(workingDir, f.Path))
			}
			switch r.Name {
			case tools.ViewToolName:
				var p struct {
					FilePath string `json:"file_path"`
				}
				if json.Unmarshal([]byte(call.Input), &p) == nil && p.FilePath != "" {
					touched = append(touched, normalizePath(workingDir, p.FilePath))
				}
			case tools.BashToolName:
				if v := fileViewedByBash(fantasy.ToolCall{ID: call.ID, Name: call.Name, Input: call.Input}, fantasy.ToolResponse{Metadata: r.Metadata}); v != "" {
					touched = append(touched, normalizePath(workingDir, v))
				}
			}
		}
	}
	for _, c := range claims {
		paths := claimPaths(c.file, named, workingDir)
		if len(paths) == 0 {
			if c.made && fileExts[strings.TrimPrefix(filepath.Ext(c.file), ".")] {
				return c.file, true
			}
			continue
		}
		backed := false
		for _, p := range paths {
			if slices.ContainsFunc(touched, func(t string) bool { return pathEndsWith(t, c.file) }) {
				backed = true
				break
			}
			if at, ok := changedAt(p); ok && !at.Before(since) {
				backed = true
				break
			}
		}
		if !backed {
			return c.file, false
		}
	}
	return "", false
}

// claimPaths is where file, as a result names it, is: under workingDir as
// written, a file of that name the session's calls named, or one found
// under workingDir; none when there is no such file.
func claimPaths(file string, named map[string][]string, workingDir string) []string {
	if p := filepath.Join(workingDir, file); fileExists(p) {
		return []string{p}
	}
	if filepath.IsAbs(file) && fileExists(file) {
		return []string{file}
	}
	var paths []string
	for _, p := range named[filepath.Base(file)] {
		p = normalizePath(workingDir, p)
		if pathEndsWith(p, file) && fileExists(p) {
			paths = append(paths, p)
		}
	}
	if len(paths) > 0 {
		return paths
	}
	return findFile(workingDir, file)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// findFile is the files under dir whose path ends with file, looking at
// no more than findFileLimit entries and skipping what no result names:
// version control, dependencies, gnosh's own data.
func findFile(dir, file string) []string {
	var found []string
	n := 0
	errStop := errors.New("stop")
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if n++; n > findFileLimit {
			return errStop
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".gnosh", ".crush":
				return filepath.SkipDir
			}
			return nil
		}
		if pathEndsWith(p, file) {
			found = append(found, p)
		}
		return nil
	})
	return found
}

const findFileLimit = 20_000

// pathEndsWith reports whether path ends with file, path part by part.
func pathEndsWith(path, file string) bool {
	path, file = filepath.ToSlash(filepath.Clean(path)), filepath.ToSlash(filepath.Clean(file))
	return path == file || strings.HasSuffix(path, "/"+file)
}

// lastWork is the id of the last row of rows that is work, or "": the
// review's verdict on a close stands until work comes, so a close sent
// again unchanged after a refusal is not reviewed again.
func lastWork(rows []message.Message) string {
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if r.Role == message.Assistant && strings.TrimSpace(r.Content().Text) != "" {
			return r.ID
		}
		for _, call := range r.ToolCalls() {
			if !isMemCall(call) {
				return r.ID
			}
		}
	}
	return ""
}

// closeVerdict is the review's verdict on a close.
type closeVerdict struct {
	ok     bool
	reason string
}

// closeVerdicts keeps the reviews' verdicts, the last few hundred.
type closeVerdicts struct {
	mu    sync.Mutex
	keys  []string
	cache map[string]closeVerdict
}

func (v *closeVerdicts) get(key string) (closeVerdict, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	c, ok := v.cache[key]
	return c, ok
}

func (v *closeVerdicts) put(key string, c closeVerdict) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cache == nil {
		v.cache = map[string]closeVerdict{}
	}
	if _, ok := v.cache[key]; !ok {
		v.keys = append(v.keys, key)
	}
	v.cache[key] = c
	for len(v.keys) > 256 {
		delete(v.cache, v.keys[0])
		v.keys = v.keys[1:]
	}
}

//go:embed templates/close_check.md
var closeCheckPrompt []byte

// closeTranscriptLimit bounds the transcript the review reads, in
// characters, as goalTranscriptLimit bounds the goal check's.
const closeTranscriptLimit = 24_000

// reviewClose asks the small model, without tools and at temperature 0,
// whether what happened in the todo shows the result it is closed with.
// It is told, besides the transcript, which files the tools changed and
// which commands ran, so a long todo's cut transcript does not hide them.
func (a *sessionAgent) reviewClose(ctx context.Context, sessionID string, cl gnovm.MemClose, w closeWindow, workingDir string) (ok bool, reason string, err error) {
	prompt := closePrompt(cl, w, workingDir)
	zero := 0.0
	resp, _, err := a.askJudge(ctx, sessionID, "close check", closeCheckPrompt, prompt, 400, &zero)
	if err != nil {
		return false, "", err
	}
	return parseGoalCheck(resp.Response.Content.Text())
}

// closePrompt is what the review of cl is asked, from w.
func closePrompt(cl gnovm.MemClose, w closeWindow, workingDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The todo: %s\n\n", cmp.Or(cl.Title, cl.Id))
	fmt.Fprintf(&b, "Thoughts kept on it: %s\n\n", cmp.Or(strings.TrimSpace(cl.Thoughts), "(none)"))
	if w.prompt != "" {
		fmt.Fprintf(&b, "What the user asked in this turn: %s\n\n", clip(w.prompt, 2_000))
	}
	edited := EditedFiles(w.rows, workingDir)
	fmt.Fprintf(&b, "Files the agent's edits and writes changed in the todo: %s\n", cmp.Or(strings.Join(edited, ", "), "none"))
	if ran := commandsRun(w.rows, callsOf(w.rows)); len(ran) > 0 {
		fmt.Fprintf(&b, "Commands it ran in the todo: %s\n", strings.Join(ran, "; "))
	} else {
		b.WriteString("Commands it ran in the todo: none\n")
	}
	fmt.Fprintf(&b, "\nThe result it is being closed with (the agent's claim): %s\n\n", cmp.Or(cl.Result, "(none given)"))
	transcript := closeTranscript(w.rows)
	if w.code != "" {
		fmt.Fprintf(&b, "The agent entered the todo and closed it in one call to its memory, mem; that call:\n%s\n\n", clip(w.code, 4_000))
		if transcript == "" {
			transcript = "(nothing but that call)"
		}
	}
	if transcript == "" {
		transcript = "(nothing: the agent did nothing in the todo before closing it)"
	}
	fmt.Fprintf(&b, "What happened in the todo, from when the agent entered it:\n%s\n\nDoes what happened in the todo show the result?", transcript)
	return b.String()
}

// commandsRun is each command a bash call in rows ran, with its exit
// status: "`go test ./...` → exit 0".
func commandsRun(rows []message.Message, calls map[string]message.ToolCall) []string {
	var ran []string
	for _, row := range rows {
		for _, r := range row.ToolResults() {
			if r.Name != tools.BashToolName {
				continue
			}
			var p tools.BashParams
			if json.Unmarshal([]byte(calls[r.ToolCallID].Input), &p) != nil || p.Command == "" {
				continue
			}
			status := "failed to run"
			var meta tools.BashResponseMetadata
			if !r.IsError && json.Unmarshal([]byte(r.Metadata), &meta) == nil {
				status = fmt.Sprintf("exit %d", meta.ExitCode)
			}
			ran = append(ran, fmt.Sprintf("`%s` → %s", clip(p.Command, 200), status))
		}
	}
	return ran
}

// closeClips are the clip lengths the review's transcript tries in turn
// until it fits: what the user said, what the agent said, a call's input,
// a tool's result. A call's input, an edit's own evidence, is cut last.
var closeClips = [][4]int{{2_000, 4_000, 2_000, 2_000}, {1_000, 2_000, 1_200, 800}, {500, 800, 600, 300}}

// closeTranscript is rows as the review reads them: the prompts typed,
// what the agent said and called, and what its tools returned, a failed
// call's result marked so; each clipped, and clipped harder until the
// whole fits, keeping its end last of all.
func closeTranscript(rows []message.Message) string {
	var s string
	for _, c := range closeClips {
		var parts []string
		for _, msg := range rows {
			switch msg.Role {
			case message.User:
				if text := msg.Content(); !text.Hidden && strings.TrimSpace(text.Text) != "" {
					parts = append(parts, "User: "+clip(text.Text, c[0]))
				}
			case message.Assistant:
				if text := strings.TrimSpace(msg.Content().Text); text != "" {
					parts = append(parts, "Agent: "+clip(text, c[1]))
				}
				for _, call := range msg.ToolCalls() {
					parts = append(parts, fmt.Sprintf("Agent called %s: %s", call.Name, clip(call.Input, c[2])))
				}
			case message.Tool:
				for _, r := range msg.ToolResults() {
					verb := " returned: "
					if r.IsError {
						verb = " failed: "
					}
					parts = append(parts, r.Name+verb+clip(r.Content, c[3]))
				}
			}
		}
		s = strings.Join(parts, "\n\n")
		if len([]rune(s)) <= closeTranscriptLimit {
			return s
		}
	}
	r := []rune(s)
	return "…" + string(r[len(r)-closeTranscriptLimit:])
}
