package agent

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/crush/internal/log"
)

// remoteResponseTimeout is how long a request to a model API elsewhere
// waits for the server to start answering: the SDKs' default, and Claude
// Code's.
const remoteResponseTimeout = 10 * time.Minute

// tailnet is the shared address range (RFC 6598) Tailscale gives the
// machines on a tailnet.
var tailnet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// modelResponseTimeout is how long a request to the model server at
// baseURL waits for the server to start answering, 0 for no limit.
// API_TIMEOUT_MS sets it in milliseconds, as in Claude Code. Otherwise a
// server on this machine or the local network has no limit: it can send
// nothing for longer than 10 minutes while it reads a long conversation
// or works through a request ahead of this one, and one that dies closes
// the connection.
func modelResponseTimeout(baseURL string) time.Duration {
	if ms, err := strconv.Atoi(os.Getenv("API_TIMEOUT_MS")); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	if localModelServer(baseURL) {
		return 0
	}
	return remoteResponseTimeout
}

// localModelServer reports whether baseURL names this machine or the
// local network: localhost, a loopback, private, link-local or tailnet
// address, a host name of one label, or a name under .localhost, .local,
// .lan, .internal or .home.arpa.
func localModelServer(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || tailnet.Contains(ip)
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".localhost", ".local", ".lan", ".internal", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// modelHTTPClient is the HTTP client a provider reaches the model server
// at baseURL through: it waits modelResponseTimeout for the server to
// start answering, and logs each request in debug mode.
func (c *coordinator) modelHTTPClient(baseURL string) *http.Client {
	transport := http.DefaultTransport
	// A DefaultTransport someone wrapped (for tracing, say) is kept as is,
	// as the SDKs do.
	if t, ok := transport.(*http.Transport); ok {
		t = t.Clone()
		t.ResponseHeaderTimeout = modelResponseTimeout(baseURL)
		transport = t
	}
	if c.cfg.Config().Options.Debug {
		transport = &log.HTTPRoundTripLogger{Transport: transport}
	}
	return &http.Client{Transport: transport}
}
