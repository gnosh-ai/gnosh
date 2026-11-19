package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file is signing in to a server reached at a URL by OAuth, as MCP's
// authorization specification has it and as Claude Code does it (its MCP
// page, "Authenticate with remote MCP servers"): the server answers 401,
// gnosh finds its authorization server, registers itself there when no
// client was configured, and sends the person to sign in in a browser,
// with PKCE; the browser comes back to http://localhost:PORT/callback, or,
// when gnosh runs elsewhere (over ssh), the person pastes the address it
// tried to go to. The token is kept per server URL (LoginStore), sent with
// every request, and refreshed when the server refuses it.
//
// Discovery, in order: the authorization server metadata the
// configuration names (oauth.authServerMetadataUrl, https only); the
// server's protected resource metadata (RFC 9728), at the address its
// WWW-Authenticate gave or at /.well-known/oauth-protected-resource, which
// names the authorization server; and the server's own origin. An
// authorization server's metadata is read as RFC 8414 has it, and as
// OpenID Connect's discovery has it after that.
//
// Credentials go only to a token endpoint served over https, or at
// localhost; the issuer the authorization server answers with (RFC 9207's
// iss) must be the one discovered.

// OAuth is a server's oauth settings, the configuration's "oauth" object.
type OAuth struct {
	// ClientID is a client registered with the authorization server by
	// hand; empty, gnosh registers one (dynamic client registration).
	ClientID string
	// ClientSecret is that client's secret, when it has one. It is kept
	// with the sign-ins, never in a configuration file.
	ClientSecret string
	// CallbackPort is the port of the redirect address,
	// http://localhost:PORT/callback; 0 takes any free one.
	CallbackPort int
	// Scopes are the scopes asked for, separated by blanks; empty lets the
	// server decide.
	Scopes string
	// AuthServerMetadataURL is the authorization server's metadata, read
	// in place of the discovery; it must be an https address.
	AuthServerMetadataURL string
}

// AuthServer is what an authorization server says of itself (RFC 8414).
type AuthServer struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported,omitempty"`
}

// Token is what a token endpoint gave.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	Expiry       time.Time `json:"expiry,omitzero"`
}

// Login is a sign-in kept for a server: its token, and what it takes to
// refresh it.
type Login struct {
	// Server is the MCP server's URL, which the sign-in is kept under.
	Server        string `json:"server"`
	Issuer        string `json:"issuer"`
	TokenEndpoint string `json:"token_endpoint"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret,omitempty"`
	Token
}

// ErrNeedsAuth is what a request to a server that wants a sign-in comes
// to: the server answered 401 or 403, with no token, or with one it
// refused and that could not be refreshed. errors.As finds a *NeedsAuth
// with what the server said.
var ErrNeedsAuth = errors.New("the server needs a sign-in")

// NeedsAuth is ErrNeedsAuth with what the server said: its status and its
// WWW-Authenticate header, which may name its protected resource metadata
// and the scope it wants.
type NeedsAuth struct {
	Status          int
	WWWAuthenticate string
}

func (e *NeedsAuth) Error() string {
	return fmt.Sprintf("the server needs a sign-in (it answered %d %s)", e.Status, http.StatusText(e.Status))
}

func (e *NeedsAuth) Unwrap() error { return ErrNeedsAuth }

// oauthTimeout bounds each request of a sign-in.
const oauthTimeout = 15 * time.Second

// maxOAuthBody is the most of an answer that is read.
const maxOAuthBody = 1 << 20

// Discover finds the authorization server of the MCP server at serverURL,
// given the WWW-Authenticate header of its 401 (or ""). It returns that
// server's metadata and the scopes the resource says it takes (RFC 9728's
// scopes_supported, or the scope its WWW-Authenticate named).
func Discover(ctx context.Context, hc *http.Client, serverURL, wwwAuthenticate string, cfg OAuth) (AuthServer, string, error) {
	if !Secure(serverURL) {
		return AuthServer{}, "", fmt.Errorf("the server is reached over plain http at %s, so gnosh signs in to it nowhere: its sign-in and its token would cross the network in the clear", plain(siteOf(serverURL)))
	}
	params := authParams(wwwAuthenticate)
	// The scope the server asked for in its challenge comes first.
	scopes := params["scope"]
	if cfg.AuthServerMetadataURL != "" {
		if !strings.HasPrefix(cfg.AuthServerMetadataURL, "https://") {
			return AuthServer{}, "", fmt.Errorf("oauth.authServerMetadataUrl %q is not an https address", plain(cfg.AuthServerMetadataURL))
		}
		var as AuthServer
		if err := getJSON(ctx, hc, cfg.AuthServerMetadataURL, &as); err != nil {
			return AuthServer{}, "", fmt.Errorf("the authorization server's metadata at %s could not be read: %w", plain(cfg.AuthServerMetadataURL), err)
		}
		if err := as.check(""); err != nil {
			return AuthServer{}, "", err
		}
		if scopes == "" {
			// (As Claude Code takes them from a metadata address it was
			// given.)
			scopes = strings.Join(as.ScopesSupported, " ")
		}
		return as, scopes, nil
	}
	// The protected resource's metadata names its authorization server.
	var issuers []string
	var candidates []string
	if rm := params["resource_metadata"]; rm != "" && Secure(rm) {
		candidates = append(candidates, rm)
	}
	candidates = append(candidates, wellKnown(serverURL, "oauth-protected-resource", true)...)
	for _, c := range candidates {
		var prm struct {
			AuthorizationServers []string `json:"authorization_servers"`
			ScopesSupported      []string `json:"scopes_supported"`
		}
		if getJSON(ctx, hc, c, &prm) == nil && len(prm.AuthorizationServers) > 0 {
			for _, is := range prm.AuthorizationServers {
				if Secure(is) {
					issuers = append(issuers, is)
				}
			}
			if scopes == "" {
				scopes = strings.Join(prm.ScopesSupported, " ")
			}
			if len(issuers) == 0 {
				return AuthServer{}, "", errors.New("the server names no authorization server reached over https")
			}
			break
		}
	}
	if len(issuers) == 0 {
		// A server without the metadata: its own origin is its
		// authorization server.
		u, err := url.Parse(serverURL)
		if err != nil {
			return AuthServer{}, "", fmt.Errorf("the server's address %q cannot be read", plain(serverURL))
		}
		issuers = []string{u.Scheme + "://" + u.Host}
	}
	var lastErr error
	for _, issuer := range issuers {
		for _, m := range authServerMetadata(issuer) {
			var as AuthServer
			if err := getJSON(ctx, hc, m, &as); err != nil {
				lastErr = err
				continue
			}
			if err := as.check(issuer); err != nil {
				lastErr = err
				continue
			}
			return as, scopes, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("it gave no metadata")
	}
	return AuthServer{}, "", fmt.Errorf("the server's authorization server could not be found (%w)", lastErr)
}

// check holds an authorization server's metadata to what a sign-in needs:
// its endpoints, all reached over https or on this machine, its issuer
// (when one was expected), and PKCE with S256 when it says which methods
// it takes.
func (as AuthServer) check(issuer string) error {
	switch {
	case as.AuthorizationEndpoint == "" || as.TokenEndpoint == "":
		return errors.New("the authorization server's metadata names no authorization or no token endpoint")
	case issuer != "" && as.Issuer != "" && strings.TrimSuffix(as.Issuer, "/") != strings.TrimSuffix(issuer, "/"):
		return fmt.Errorf("the authorization server's metadata names the issuer %s, not %s", plain(as.Issuer), plain(issuer))
	case len(as.CodeChallengeMethods) > 0 && !contains(as.CodeChallengeMethods, "S256"):
		return errors.New("the authorization server does not take PKCE with S256")
	}
	for _, e := range []string{as.TokenEndpoint, as.AuthorizationEndpoint} {
		if err := credentialsMayGo(e); err != nil {
			return err
		}
	}
	if as.RegistrationEndpoint != "" {
		if err := credentialsMayGo(as.RegistrationEndpoint); err != nil {
			return err
		}
	}
	return nil
}

// Secure reports whether an address is one a sign-in may use: https, or
// plain http to this machine.
func Secure(address string) bool {
	return credentialsMayGo(address) == nil
}

// credentialsMayGo says why a sign-in's credentials, or the person's
// browser, may not be sent to the endpoint, or nil: they go over https,
// or to this machine.
func credentialsMayGo(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("the endpoint %q cannot be read", plain(endpoint))
	}
	if u.Scheme == "https" && u.Host != "" {
		return nil
	}
	if u.Scheme == "http" {
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return nil
		}
	}
	return fmt.Errorf("the endpoint %s is not served over https, so no sign-in goes through it", plain(endpoint))
}

// plain makes text a server gave fit to be shown: control characters
// (an escape, which a terminal would act on) written as "?", and cut at
// 300 characters.
func plain(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return '?'
		}
		return r
	}, strings.ToValidUTF8(s, "?"))
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

// wellKnown is the addresses of a well-known document for a resource or an
// issuer: with the path put after the well-known name (RFC 8414 and 9728),
// and with appendRoot the one at the root after it.
func wellKnown(base, name string, appendRoot bool) []string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	out := []string{origin + "/.well-known/" + name + path}
	if appendRoot && path != "" {
		out = append(out, origin+"/.well-known/"+name)
	}
	return out
}

// authServerMetadata is where an issuer's metadata may be: RFC 8414's
// place, then OpenID Connect's two.
func authServerMetadata(issuer string) []string {
	out := wellKnown(issuer, "oauth-authorization-server", false)
	out = append(out, wellKnown(issuer, "openid-configuration", false)...)
	if u, err := url.Parse(issuer); err == nil && strings.Trim(u.Path, "/") != "" {
		out = append(out, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration")
	}
	return out
}

// authParams reads the parameters of a Bearer challenge in a
// WWW-Authenticate header: resource_metadata, scope, error.
func authParams(header string) map[string]string {
	out := map[string]string{}
	h := strings.TrimSpace(header)
	if len(h) < 6 || !strings.EqualFold(h[:6], "bearer") {
		return out
	}
	rest := h[6:]
	for rest != "" {
		rest = strings.TrimLeft(rest, " ,\t")
		eq := strings.IndexByte(rest, '=')
		if eq <= 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(rest[:eq]))
		rest = rest[eq+1:]
		var val string
		if strings.HasPrefix(rest, `"`) {
			end := 1
			var b strings.Builder
			for end < len(rest) && rest[end] != '"' {
				if rest[end] == '\\' && end+1 < len(rest) {
					end++
				}
				b.WriteByte(rest[end])
				end++
			}
			val = b.String()
			rest = rest[min(end+1, len(rest)):]
		} else {
			end := strings.IndexAny(rest, ", \t")
			if end < 0 {
				end = len(rest)
			}
			val, rest = rest[:end], rest[end:]
		}
		out[key] = val
	}
	return out
}

// Register registers gnosh with the authorization server as a public
// client with the redirect address (dynamic client registration, RFC
// 7591), and returns the client's id and, when the server gave one, its
// secret.
func Register(ctx context.Context, hc *http.Client, as AuthServer, redirectURI string) (id, secret string, err error) {
	if as.RegistrationEndpoint == "" {
		return "", "", errors.New("the authorization server takes no registration of clients (dynamic client registration): register one by hand and name it with oauth.clientId")
	}
	if err := credentialsMayGo(as.RegistrationEndpoint); err != nil {
		return "", "", err
	}
	body, _ := json.Marshal(map[string]any{
		"client_name":                "gnosh",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, as.RegistrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := doJSON(hc, req, &out); err != nil {
		return "", "", fmt.Errorf("gnosh could not register with the authorization server: %w", err)
	}
	if out.ClientID == "" {
		return "", "", errors.New("the authorization server registered gnosh and gave no client id")
	}
	return out.ClientID, out.ClientSecret, nil
}

// Flow is one sign-in under way: what the browser is sent to, and what is
// needed to take the code it comes back with.
type Flow struct {
	AS           AuthServer
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Scopes       string
	// Resource is the MCP server's URL, the resource the token is for
	// (RFC 8707).
	Resource string
	verifier string
	state    string
}

// State is the sign-in's state, which the browser's answer must carry.
func (f *Flow) State() string { return f.state }

// NewFlow makes a sign-in, with a PKCE verifier and a state of its own.
func NewFlow(as AuthServer, clientID, clientSecret, redirectURI, scopes, resource string) *Flow {
	return &Flow{AS: as, ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirectURI, Scopes: scopes, Resource: resource,
		verifier: randomText(32), state: randomText(16)}
}

// URL is the address the person signs in at.
func (f *Flow) URL() string {
	sum := sha256.Sum256([]byte(f.verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.ClientID},
		"redirect_uri":          {f.RedirectURI},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"state":                 {f.state},
	}
	if f.Scopes != "" {
		q.Set("scope", f.Scopes)
	}
	if f.Resource != "" {
		q.Set("resource", f.Resource)
	}
	sep := "?"
	if strings.Contains(f.AS.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return f.AS.AuthorizationEndpoint + sep + q.Encode()
}

// Finish takes what the browser came back with, the parameters of the
// redirect address (as the listener got them, or from the address the
// person pasted), and trades the code in it for a token.
func (f *Flow) Finish(ctx context.Context, hc *http.Client, back url.Values) (Token, error) {
	if back.Get("state") != f.state {
		return Token{}, errors.New("the address the browser came back with is not this sign-in's (its state differs): start the sign-in again")
	}
	if e := back.Get("error"); e != "" {
		why := e
		if d := back.Get("error_description"); d != "" {
			why += ": " + d
		}
		return Token{}, fmt.Errorf("the sign-in was refused (%s)", plain(why))
	}
	if iss := back.Get("iss"); iss != "" && f.AS.Issuer != "" && strings.TrimSuffix(iss, "/") != strings.TrimSuffix(f.AS.Issuer, "/") {
		return Token{}, fmt.Errorf("the sign-in came back from the issuer %s, not %s", plain(iss), plain(f.AS.Issuer))
	}
	code := back.Get("code")
	if code == "" {
		return Token{}, errors.New("the address the browser came back with holds no code")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {f.RedirectURI},
		"client_id":     {f.ClientID},
		"code_verifier": {f.verifier},
	}
	if f.Resource != "" {
		form.Set("resource", f.Resource)
	}
	return tokenRequest(ctx, hc, f.AS.TokenEndpoint, f.ClientID, f.ClientSecret, form)
}

// Callback reads the parameters out of what the person pasted: the whole
// address the browser tried to go to, or only its part after the "?".
func Callback(pasted string) (url.Values, error) {
	s := strings.TrimSpace(pasted)
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[i+1:]
	}
	if j := strings.IndexByte(s, '#'); j >= 0 {
		s = s[:j]
	}
	v, err := url.ParseQuery(s)
	if err != nil || (v.Get("code") == "" && v.Get("error") == "") {
		return nil, errors.New("that is not the address the browser came back to: it holds no code")
	}
	return v, nil
}

// Refresh trades a sign-in's refresh token for a new token. A token
// endpoint that gives no new refresh token leaves the old one in force.
func Refresh(ctx context.Context, hc *http.Client, l Login) (Token, error) {
	if l.RefreshToken == "" {
		return Token{}, errors.New("the sign-in has no refresh token")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {l.RefreshToken},
		"client_id":     {l.ClientID},
	}
	if l.Server != "" {
		form.Set("resource", l.Server)
	}
	t, err := tokenRequest(ctx, hc, l.TokenEndpoint, l.ClientID, l.ClientSecret, form)
	if err != nil {
		return Token{}, err
	}
	if t.RefreshToken == "" {
		t.RefreshToken = l.RefreshToken
	}
	return t, nil
}

// tokenRequest asks a token endpoint for a token.
func tokenRequest(ctx context.Context, hc *http.Client, endpoint, clientID, secret string, form url.Values) (Token, error) {
	if err := credentialsMayGo(endpoint); err != nil {
		return Token{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if secret != "" {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	}
	var out struct {
		AccessToken  string      `json:"access_token"`
		TokenType    string      `json:"token_type"`
		ExpiresIn    json.Number `json:"expires_in"`
		RefreshToken string      `json:"refresh_token"`
		Scope        string      `json:"scope"`
		Error        string      `json:"error"`
		Description  string      `json:"error_description"`
	}
	if err := doJSON(hc, req, &out); err != nil {
		return Token{}, fmt.Errorf("the token endpoint refused: %w", err)
	}
	if out.Error != "" {
		return Token{}, fmt.Errorf("the token endpoint refused: %w", &OAuthError{Code: out.Error, Description: out.Description})
	}
	if out.AccessToken == "" {
		return Token{}, errors.New("the token endpoint gave no access token")
	}
	if out.TokenType != "" && !strings.EqualFold(out.TokenType, "bearer") {
		return Token{}, fmt.Errorf("the token endpoint gave a %s token, and gnosh sends bearer tokens alone", plain(out.TokenType))
	}
	t := Token{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, TokenType: "Bearer", Scope: out.Scope}
	if secs, err := strconv.ParseInt(out.ExpiresIn.String(), 10, 64); err == nil && secs > 0 {
		t.Expiry = time.Now().Add(time.Duration(secs) * time.Second)
	}
	return t, nil
}

// Listen opens the redirect address's listener on this machine: port 0
// takes any free one. It returns the redirect address,
// http://localhost:PORT/callback, a channel that gets the parameters the
// browser comes back with, and how to close it. Only an answer that
// carries the state state returns is taken, once: anything else that
// reaches the address (another program on this machine, a page that
// found the port) is turned away and ends nothing. The same port on ::1
// is listened on too, where a browser takes localhost to be; when another
// program holds it there, the sign-in does not start.
func Listen(port int, state func() string) (redirectURI string, back <-chan url.Values, stop func(), err error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return "", nil, nil, fmt.Errorf("the sign-in's redirect address could not be opened on port %d: %w", port, err)
	}
	port = ln.Addr().(*net.TCPAddr).Port
	got := make(chan url.Values, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		want := state()
		if want == "" || q.Get("state") != want || q.Get("code") == "" && q.Get("error") == "" {
			http.Error(w, "This is gnosh's sign-in address, and this is not the answer of the sign-in it waits for.", http.StatusBadRequest)
			return
		}
		once.Do(func() { got <- q })
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if q.Get("error") != "" {
			io.WriteString(w, "The sign-in was refused. gnosh says why; this tab can be closed.\n")
			return
		}
		io.WriteString(w, "Signed in. gnosh goes on; this tab can be closed.\n")
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	servers := []*http.Server{srv}
	go srv.Serve(ln)
	stop = func() {
		// Gracefully: the answer to the browser that brought the code may
		// still be on its way (the code goes to the sign-in before the
		// page is written), and a server closed under it leaves the tab
		// with an error where "Signed in" belongs. What does not finish
		// in a moment is closed.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for _, s := range servers {
			if s.Shutdown(ctx) != nil {
				s.Close()
			}
		}
	}
	ln6, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	switch {
	case err == nil:
		srv6 := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, srv6)
		go srv6.Serve(ln6)
	case errors.Is(err, syscall.EADDRINUSE):
		stop()
		return "", nil, nil, fmt.Errorf("another program holds port %d on ::1, where a browser may take localhost to be, so the sign-in's answer could go to it: choose another oauth.callbackPort", port)
	}
	// (No ::1 on this machine: localhost is 127.0.0.1 alone.)
	return "http://localhost:" + strconv.Itoa(port) + "/callback", got, stop, nil
}

// getJSON reads a JSON document. A redirect is followed only to an
// address a sign-in may use (Secure), and five times at most.
func getJSON(ctx context.Context, hc *http.Client, address string, v any) error {
	if !Secure(address) {
		return fmt.Errorf("%s is not reached over https", plain(siteOf(address)))
	}
	ctx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	return doJSON(hc, req, v)
}

// OAuthError is what an authorization server said when it refused: its
// error code ("invalid_grant") and description.
type OAuthError struct {
	Code, Description string
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return plain(e.Code + ": " + e.Description)
	}
	return plain(e.Code)
}

// IsInvalidGrant reports whether err is an authorization server's
// invalid_grant: the code or the refresh token sent is no good, for good.
func IsInvalidGrant(err error) bool {
	var oe *OAuthError
	return errors.As(err, &oe) && oe.Code == "invalid_grant"
}

// doJSON makes a request and reads its JSON answer; an answer that is not
// a success is an error, with the OAuth error it gives when it gives one.
// A POST, which carries a code, a verifier or a refresh token, follows no
// redirect: a 307 would send its body on, to an address nobody checked. A
// GET follows one only to an address a sign-in may use.
func doJSON(hc *http.Client, req *http.Request, v any) error {
	if hc == nil {
		hc = http.DefaultClient
	}
	c := *hc
	c.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if req.Method != http.MethodGet {
			return http.ErrUseLastResponse
		}
		if len(via) >= 5 || !Secure(next.URL.String()) {
			return fmt.Errorf("the answer sent the request on to %s, which is not reached over https", plain(siteOf(next.URL.String())))
		}
		return nil
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var oe struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &oe) == nil && oe.Error != "" {
			return fmt.Errorf("%s (%w)", resp.Status, &OAuthError{Code: oe.Error, Description: oe.Description})
		}
		return errors.New(resp.Status)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("the answer is not JSON: %w", err)
	}
	return nil
}

// randomText is n random bytes, written for a URL.
func randomText(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
