package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// PublicOnly returns a copy of client that connects to public addresses
// only: not loopback, private, link-local (the cloud metadata service at
// 169.254.169.254 among them), carrier-grade NAT (Tailscale's 100.64/10),
// unique-local, unspecified or multicast ones. The check is made on the
// address each connection is about to use, so a redirect, or a name that
// resolves to a private address, is refused too. It takes no proxy from
// the environment, whose address would be the one checked.
//
// It is for the fetches a model makes on its own from pages it read,
// agentic_fetch's sub-agent's: a page could otherwise send it to a
// service on the user's machine or network (the model server, a router's
// admin page) and have it pass on what it read there in another fetch.
func PublicOnly(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	base, ok := client.Transport.(*http.Transport)
	if !ok || base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	transport := base.Clone()
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: refuseNonPublic}
	transport.DialContext = dialer.DialContext
	out := *client
	out.Transport = transport
	return &out
}

// refuseNonPublic is a net.Dialer Control: it refuses a connection to an
// address that is not public.
func refuseNonPublic(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !isPublicAddr(addr) {
		return fmt.Errorf("%s is a local or private address; this fetch reaches public sites only", host)
	}
	return nil
}

// notPublic are the address blocks IsGlobalUnicast counts as global that
// are not public: carrier-grade NAT, which Tailscale uses for its network,
// and 0.0.0.0/8, which reaches this machine.
var notPublic = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("0.0.0.0/8")}

func isPublicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// ApprovedFetchClient is the client for fetching a URL the user approved,
// agentic_fetch's: client itself when the URL names a local or private
// address, which the user then asked for by name, and else PublicOnly(client),
// so that a redirect from the approved page to an address on the user's
// network is refused. The plain client followed it, and the page it led
// to went into the fetch agent's prompt, for it to pass on in its own
// fetches.
func ApprovedFetchClient(ctx context.Context, client *http.Client, rawURL string) *http.Client {
	if namesLocalAddress(ctx, rawURL) {
		return client
	}
	return PublicOnly(client)
}

// namesLocalAddress reports whether rawURL's host is a local or private
// address: localhost, such an IP address, or a name whose every address
// is one.
func namesLocalAddress(ctx context.Context, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return !isPublicAddr(addr)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, addr := range addrs {
		if isPublicAddr(addr) {
			return false
		}
	}
	return true
}
