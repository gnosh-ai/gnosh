// Package artifacts serves the deliverables the agent records with the
// artifact tool, so that the user can open one — a rendered video, a
// generated report, a published page — from the machine they are sitting
// at. Klash usually runs somewhere else: on a box over ssh, in a
// container, on a droplet, and a path printed there is of no use from
// here.
//
// Only a file the agent recorded is served, each behind its own 128-bit
// token, and the register of them is kept in the project's data
// directory, so a URL handed over yesterday still opens today.
package artifacts

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Off is the address that serves nothing: an artifact is then recorded
// as the path it has, with no URL.
const Off = "off"

// DefaultAddress is where the server listens unless the config says
// otherwise: every interface, so the URL works from another machine on
// the network, at a port that is the same from run to run, so a URL
// outlives the session it was made in. A port already taken falls back
// to a free one.
const DefaultAddress = ":7777"

// keep is how many artifacts the register holds; the oldest go first.
const keep = 200

// Entry is a served file: where it is on this machine, the name it is
// served under, and, for a file fetched from a declared host, that host.
type Entry struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Host string `json:"host,omitempty"`
	At   int64  `json:"at"`
}

// Server serves recorded artifacts over HTTP. A nil *Server serves
// nothing and every method on it is safe, which is what `off` and a
// failed start both give the caller.
type Server struct {
	registerPath string
	cacheDir     string
	base         string
	ln           net.Listener
	srv          *http.Server

	// byExtension is what this machine says a file of that extension is.
	// It is a field so a test can stand in for a machine whose
	// mime.types names nothing, which is the only case where the
	// fallback below, and the header at all, make any difference.
	byExtension func(string) string

	mu     sync.Mutex
	files  map[string]Entry  // token -> file
	tokens map[string]string // path -> token
	// lastAt keeps At strictly increasing, so that artifacts recorded
	// within the same millisecond still have an order to prune by.
	lastAt int64
}

// Start listens at addr and serves the artifacts registered under dir
// (the project's data directory, which holds the register and the copies
// fetched from other machines). addr "" takes DefaultAddress; Off, or a
// port already taken on an explicit address, returns a nil Server, which
// serves nothing rather than failing the session.
func Start(addr, dir string) *Server {
	addr = strings.TrimSpace(addr)
	if addr == Off {
		return nil
	}
	ln, err := net.Listen("tcp", cmp.Or(addr, DefaultAddress))
	if err != nil && addr == "" {
		// The default port is taken — by another Klash, most likely.
		// Any free port still serves this session's artifacts.
		ln, err = net.Listen("tcp", ":0")
	}
	if err != nil {
		slog.Warn("Artifacts are not served over HTTP; they are recorded as paths", "address", cmp.Or(addr, DefaultAddress), "error", err)
		return nil
	}
	s := &Server{
		byExtension:  mime.TypeByExtension,
		registerPath: filepath.Join(dir, "artifacts.json"),
		cacheDir:     filepath.Join(dir, "artifacts"),
		base:         baseURL(ln.Addr()),
		ln:           ln,
		files:        map[string]Entry{},
		tokens:       map[string]string{},
	}
	s.load()
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("The artifact server stopped", "error", err)
		}
	}()
	slog.Info("Serving artifacts", "url", s.base)
	return s
}

// BaseURL is where the server answers, or "" when nothing is served.
func (s *Server) BaseURL() string {
	if s == nil {
		return ""
	}
	return s.base
}

// CacheDir is where a copy of a file from another machine belongs, so
// that it can be served from this one. It is "" when nothing is served.
func (s *Server) CacheDir() string {
	if s == nil {
		return ""
	}
	return s.cacheDir
}

// Register records a file of this machine as an artifact and returns the
// URL that serves it, or "" when nothing is served. host names the
// declared host a copied file came from, for the record. Registering the
// same path twice keeps the URL it already had.
func (s *Server) Register(path, host string) (string, error) {
	if s == nil {
		return "", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file", abs)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokens[abs]
	if !ok {
		token = newToken()
		s.lastAt = max(time.Now().UnixMilli(), s.lastAt+1)
		s.files[token] = Entry{Path: abs, Name: filepath.Base(abs), Host: host, At: s.lastAt}
		s.tokens[abs] = token
		s.prune()
		s.save()
	}
	return s.urlOf(token, s.files[token].Name), nil
}

// Close stops serving.
func (s *Server) Close() error {
	if s == nil || s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

// ServeHTTP serves /<token>/<name>, and /<token> as well, since a URL
// pasted without its name is the same artifact. Anything else is a 404:
// a path never reaches the filesystem, only a token the server itself
// handed out.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	s.mu.Lock()
	file, ok := s.files[token]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Content-Type before ServeFile, or an unknown extension is sniffed
	// and nosniff then makes the browser download a video instead of
	// playing it. nosniff stays: a file of the agent's is not to be read
	// as HTML in the artifact's own origin.
	if ct := s.contentType(file.Name); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline; filename*=UTF-8''"+url.PathEscape(file.Name))
	http.ServeFile(w, r, file.Path)
}

// mediaTypes are the types a deliverable usually has, for a machine
// whose mime.types file does not name them: Go's own table is HTML, CSS
// and images, so a video would otherwise be sniffed, and a sniffed video
// is a download rather than something the user can watch.
var mediaTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime",
	".webm": "video/webm", ".mkv": "video/x-matroska", ".ogv": "video/ogg",
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".wav": "audio/wav",
	".ogg": "audio/ogg", ".opus": "audio/ogg", ".flac": "audio/flac",
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".heic": "image/heic",
	".md": "text/markdown; charset=utf-8", ".csv": "text/csv; charset=utf-8",
	".srt": "text/plain; charset=utf-8", ".vtt": "text/vtt",
}

// contentType is what the file is served as: what this machine says for
// the extension, else what a deliverable of that extension usually is.
func (s *Server) contentType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ct := s.byExtension(ext); ct != "" {
		return ct
	}
	return mediaTypes[ext]
}

// urlOf is the URL of a token. The name is in the path so that a browser
// and a download name the file as the agent made it.
func (s *Server) urlOf(token, name string) string {
	return s.base + "/" + token + "/" + url.PathEscape(name)
}

// load reads the register, so that a URL from an earlier run still
// opens. An unreadable register starts an empty one rather than
// stopping the session.
func (s *Server) load() {
	data, err := os.ReadFile(s.registerPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("Could not read the artifact register; earlier URLs will not open", "path", s.registerPath, "error", err)
		}
		return
	}
	var reg struct {
		Files map[string]Entry `json:"files"`
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		slog.Warn("The artifact register is unreadable; earlier URLs will not open", "path", s.registerPath, "error", err)
		return
	}
	for token, file := range reg.Files {
		if token == "" || file.Path == "" {
			continue
		}
		file.Name = cmp.Or(file.Name, filepath.Base(file.Path))
		s.files[token] = file
		s.tokens[file.Path] = token
		s.lastAt = max(s.lastAt, file.At)
	}
}

// save writes the register. Callers hold the lock.
func (s *Server) save() {
	data, err := json.Marshal(struct {
		Files map[string]Entry `json:"files"`
	}{Files: s.files})
	if err != nil {
		slog.Warn("Could not write the artifact register", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.registerPath), 0o700); err != nil {
		slog.Warn("Could not write the artifact register", "path", s.registerPath, "error", err)
		return
	}
	// 0600: a token in it opens the file it names to anyone who can
	// reach the port.
	if err := os.WriteFile(s.registerPath, data, 0o600); err != nil {
		slog.Warn("Could not write the artifact register", "path", s.registerPath, "error", err)
	}
}

// prune drops the oldest artifacts past keep, so the register of a long
// project does not grow without end. Callers hold the lock.
func (s *Server) prune() {
	if len(s.files) <= keep {
		return
	}
	tokens := make([]string, 0, len(s.files))
	for token := range s.files {
		tokens = append(tokens, token)
	}
	slices.SortFunc(tokens, func(a, b string) int {
		return cmp.Compare(s.files[a].At, s.files[b].At)
	})
	for _, token := range tokens[:len(s.files)-keep] {
		delete(s.tokens, s.files[token].Path)
		delete(s.files, token)
	}
}

// newToken is the unguessable part of an artifact's URL: 128 bits, so
// that reaching the port is not enough to read a file.
func newToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform Klash runs on; if
		// it ever did, a guessable token must not be handed out.
		panic("artifacts: no randomness for a token: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// baseURL is the URL the server answers on, as another machine would
// write it. A listener on every interface is advertised at this
// machine's address on the network, since "0.0.0.0" reaches nothing from
// outside.
func baseURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = localAddress()
	}
	return "http://" + net.JoinHostPort(host, port)
}

// localAddress is this machine's address on the network: the source
// address of a route off this machine, which is the one another machine
// can reach it at. No packet is sent — a UDP socket only picks the
// route. With no route out, the loopback address at least works on this
// machine.
func localAddress() string {
	conn, err := net.Dial("udp", "198.51.100.1:9")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return "127.0.0.1"
	}
	return host
}
