package artifacts

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// serving starts a server on the loopback address only, so a test never
// listens where another machine could reach it.
func serving(t *testing.T, dir string) *Server {
	t.Helper()
	s := Start("127.0.0.1:0", dir)
	require.NotNil(t, s, "the server did not start")
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // a loopback request in a test
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// A recorded file is served at its URL, and nothing else is: a token the
// server never handed out is a 404, so reaching the port is not enough
// to read a file.
func TestServesOnlyWhatWasRegistered(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	video := filepath.Join(dir, "video_a_v7.mp4")
	require.NoError(t, os.WriteFile(video, []byte("not really an mp4"), 0o600))

	s := serving(t, dir)
	url, err := s.Register(video, "")
	require.NoError(t, err)
	require.Contains(t, url, "/video_a_v7.mp4", "the name is in the URL")

	code, body := get(t, url)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "not really an mp4", body)

	code, _ = get(t, s.BaseURL()+"/0123456789abcdef0123456789abcdef/video_a_v7.mp4")
	require.Equal(t, http.StatusNotFound, code, "a token the server never gave out")

	code, _ = get(t, s.BaseURL()+"/")
	require.Equal(t, http.StatusNotFound, code)
}

// The URL carries a 128-bit token, which is what keeps the file from
// anyone who can reach the port but was not given the URL.
func TestURLCarriesAnUnguessableToken(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "report.md")
	require.NoError(t, os.WriteFile(path, []byte("#"), 0o600))

	s := serving(t, dir)
	url, err := s.Register(path, "")
	require.NoError(t, err)

	rest := strings.TrimPrefix(url, s.BaseURL()+"/")
	token, name, found := strings.Cut(rest, "/")
	require.True(t, found)
	require.Equal(t, "report.md", name)
	require.Len(t, token, 32, "16 random bytes, hex")
	require.Regexp(t, "^[0-9a-f]{32}$", token)

	other := serving(t, t.TempDir())
	second, err := other.Register(path, "")
	require.NoError(t, err)
	require.NotEqual(t, url, second, "two servers do not agree on a token by chance")
}

// Registering the same file twice keeps the URL already handed out, so
// a newer version of a deliverable does not invalidate the link the user
// was given for the same file.
func TestRegisterIsIdempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "cut.mp4")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	s := serving(t, dir)
	first, err := s.Register(path, "")
	require.NoError(t, err)
	again, err := s.Register(path, "render")
	require.NoError(t, err)
	require.Equal(t, first, again)
}

// Only a file is served: a directory and a path that is not there are
// both refused, so a URL never promises something that is not behind it.
func TestRegisterRefusesWhatCannotBeServed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := serving(t, dir)

	_, err := s.Register(dir, "")
	require.ErrorContains(t, err, "directory")

	_, err = s.Register(filepath.Join(dir, "nope.mp4"), "")
	require.ErrorIs(t, err, os.ErrNotExist)
}

// A URL outlives the run it was made in: the register is kept in the
// project's data directory, so the same token still opens the file after
// a restart.
func TestAURLSurvivesARestart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "deck.pdf")
	require.NoError(t, os.WriteFile(path, []byte("%PDF"), 0o600))

	first := serving(t, dir)
	url, err := first.Register(path, "")
	require.NoError(t, err)
	require.NoError(t, first.Close())

	next := serving(t, dir)
	code, body := get(t, next.BaseURL()+strings.TrimPrefix(url, first.BaseURL()))
	require.Equal(t, http.StatusOK, code, "the token from the earlier run still serves the file")
	require.Equal(t, "%PDF", body)
}

// The register does not grow without end: a long project keeps the
// newest artifacts and lets the oldest go.
func TestTheRegisterIsPruned(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := serving(t, dir)
	var first string
	for i := range keep + 5 {
		path := filepath.Join(dir, fmt.Sprintf("v%d.txt", i))
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
		url, err := s.Register(path, "")
		require.NoError(t, err)
		if i == 0 {
			first = url
		}
	}
	require.Len(t, s.files, keep)
	code, _ := get(t, first)
	require.Equal(t, http.StatusNotFound, code, "the oldest went")
}

// "off" serves nothing, and every method on the nil server it returns is
// safe: an artifact is then recorded as the path it has.
func TestOffServesNothing(t *testing.T) {
	t.Parallel()

	s := Start(Off, t.TempDir())
	require.Nil(t, s)
	url, err := s.Register("/tmp/whatever.mp4", "")
	require.NoError(t, err)
	require.Empty(t, url)
	require.Empty(t, s.BaseURL())
	require.Empty(t, s.CacheDir())
	require.NoError(t, s.Close())
}

// A listener on every interface is advertised at an address another
// machine can reach, never 0.0.0.0, which reaches nothing from outside.
// By default the server is reachable from this machine and no other. An
// artifact is whatever the agent was told to make, and the machine Gnosh
// runs on is often shared; the user at another machine gets to it with an
// ssh -L forward rather than by Gnosh opening a port to the network.
func TestTheDefaultIsThisMachineOnly(t *testing.T) {
	t.Parallel()

	require.Equal(t, "127.0.0.1:7777", DefaultAddress)

	s := Start("", t.TempDir())
	require.NotNil(t, s)
	t.Cleanup(func() { _ = s.Close() })
	require.True(t, strings.HasPrefix(s.BaseURL(), "http://127.0.0.1:"),
		"the default must not advertise an address other machines can reach, got %s", s.BaseURL())
	host, _, err := net.SplitHostPort(s.ln.Addr().String())
	require.NoError(t, err)
	require.True(t, net.ParseIP(host).IsLoopback(), "the default must listen on loopback, got %s", host)
}

func TestEveryInterfaceIsAdvertisedReachably(t *testing.T) {
	t.Parallel()

	s := Start(":0", t.TempDir())
	require.NotNil(t, s)
	t.Cleanup(func() { _ = s.Close() })
	require.NotContains(t, s.BaseURL(), "0.0.0.0")
	require.NotContains(t, s.BaseURL(), "[::]")
	require.True(t, strings.HasPrefix(s.BaseURL(), "http://"))
}

// A video is served as a video: nosniff is set, so a type that got
// sniffed would reach the user as a download rather than something to
// watch. The extension here is one no mime.types on earth names, so
// http.ServeFile has nothing of its own to answer with and the header
// Gnosh computes is the only thing under test.
func TestAVideoIsServedAsAVideo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	video := filepath.Join(dir, "video_a_v7.gnoshvid")
	require.NoError(t, os.WriteFile(video, []byte("not really an mp4"), 0o600))

	s := serving(t, dir)
	s.byExtension = func(ext string) string {
		if ext == ".gnoshvid" {
			return "video/mp4"
		}
		return ""
	}
	url, err := s.Register(video, "")
	require.NoError(t, err)

	resp, err := http.Get(url) //nolint:noctx // a loopback request in a test
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, "video/mp4", resp.Header.Get("Content-Type"))
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Contains(t, resp.Header.Get("Content-Disposition"), "inline")
	require.Equal(t, "bytes", resp.Header.Get("Accept-Ranges"), "a video has to be seekable")
}

// contentType falls back to what a deliverable of that extension usually
// is, for a machine whose mime.types does not name it — which is what the
// stub here stands for, since this machine's own table would answer first
// and the fallback would never be reached.
func TestContentTypeFallsBackForMedia(t *testing.T) {
	t.Parallel()

	bare := &Server{byExtension: func(string) string { return "" }}
	require.Equal(t, "video/mp4", bare.contentType("video_a_v7.mp4"))
	require.Equal(t, "video/quicktime", bare.contentType("cut.MOV"), "the extension is matched whatever its case")
	require.Equal(t, "audio/mp4", bare.contentType("vo.m4a"))
	require.Contains(t, bare.contentType("notes.md"), "markdown")
	require.Empty(t, bare.contentType("cut.gnoshnope"), "an extension no one knows is left to the server to sniff")

	own := &Server{byExtension: func(string) string { return "text/html" }}
	require.Equal(t, "text/html", own.contentType("page.html"), "this machine's own table answers first")
}

// Close hands the port back before it returns. A `gnosh run` closes the
// server app.New made for it and the next listener on that address has
// to succeed; waiting on the Serve goroutine to get round to it is a
// race the caller cannot see.
func TestCloseFreesThePortAtOnce(t *testing.T) {
	t.Parallel()

	for range 20 {
		s := Start("127.0.0.1:0", t.TempDir())
		require.NotNil(t, s)
		addr := strings.TrimPrefix(s.BaseURL(), "http://")
		require.NoError(t, s.Close())
		require.NoError(t, s.Close(), "closing twice is not a failure")

		ln, err := net.Listen("tcp", addr)
		require.NoError(t, err, "the port was still held after Close returned")
		require.NoError(t, ln.Close())
	}
}

// When the default port is taken — by another Gnosh, most likely — the
// fallback stays on this machine too. A fallback that opened every
// interface would quietly undo the default on exactly the machines where
// it matters most: the ones already running a Gnosh.
func TestTheFallbackStaysOnThisMachine(t *testing.T) {
	// Not parallel: it takes the default port for the duration. Whether
	// or not that listen succeeds, the port is in use afterwards, so
	// Start("") has to fall back either way.
	if taken, err := net.Listen("tcp", DefaultAddress); err == nil {
		defer taken.Close()
	}

	s := Start("", t.TempDir())
	require.NotNil(t, s)
	t.Cleanup(func() { _ = s.Close() })

	host, port, err := net.SplitHostPort(strings.TrimPrefix(s.BaseURL(), "http://"))
	require.NoError(t, err)
	require.NotEqual(t, "7777", port, "the default port was taken; this should be the fallback")
	require.True(t, net.ParseIP(host).IsLoopback(), "the fallback must stay on loopback, got %s", host)
}
