package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/mcp"
)

// This file is signing in to MCP servers reached at a URL, from the
// session (the protocol is mcp/oauth.go): a server that answers its
// handshake with 401 or 403 stands as wanting a sign-in, and the person is
// told; /mcp login <name> starts one: gnosh finds the server's
// authorization server, registers with it when no oauth.clientId is
// configured, opens the redirect address on this machine, and says the
// address to sign in at (and opens a browser there, when gnosh runs at a
// desktop). When the browser comes back to this machine the sign-in ends
// by itself; when it cannot (gnosh runs over ssh), the person copies the
// address the browser tried to go to and sends /mcp login <name>
// <address>. The token is kept by the server's URL in mcp-logins.json in
// gnosh's state folder, readable by its owner alone, and the server is
// connected again. /mcp logout <name> forgets the sign-in (the token is
// not revoked at the authorization server). As in Claude Code, a run
// (gnosh -p) cannot sign in: it says to sign in from a session first.

// mcpLoginWait is how long a sign-in waits for the browser.
const mcpLoginWait = 10 * time.Minute

// signIn is one sign-in under way: the flow, the redirect address's
// listener and what it hears, for the session (/mcp login) and for a
// shell (gnosh mcp login) alike.
type signIn struct {
	flow *mcp.Flow
	back <-chan url.Values
	stop func()
}

// mcpFlow is a sign-in the session has under way, by server name.
type mcpFlow struct {
	*signIn
	at time.Time
}

// startSignIn finds the server's authorization server (www is what its
// 401 said, or ""), registers gnosh with it when the entry names no
// oauth.clientId, opens the redirect address on this machine, and makes
// the flow; offline_access is asked for when the server offers it.
func startSignIn(ctx context.Context, cfg mcp.Config, www string, store *mcp.LoginStore) (*signIn, error) {
	hc := &http.Client{Timeout: 30 * time.Second}
	as, scopes, err := mcp.Discover(ctx, hc, cfg.URL, www, cfg.OAuth)
	if err != nil {
		return nil, fmt.Errorf("the sign-in to %s could not start: %w", cfg.Name, err)
	}
	if cfg.OAuth.Scopes != "" {
		scopes = cfg.OAuth.Scopes
	}
	if containsWord(as.ScopesSupported, "offline_access") && !containsWord(strings.Fields(scopes), "offline_access") {
		// (So that the token can be refreshed without another sign-in.)
		scopes = strings.TrimSpace(scopes + " offline_access")
	}
	// (The listener takes only the answer that carries the flow's state,
	// and the flow is made once the redirect address is known.)
	var flow atomic.Pointer[mcp.Flow]
	redirect, back, stop, err := mcp.Listen(cfg.OAuth.CallbackPort, func() string {
		if f := flow.Load(); f != nil {
			return f.State()
		}
		return ""
	})
	if err != nil {
		return nil, err
	}
	clientID, secret := cfg.OAuth.ClientID, ""
	if clientID != "" {
		if kept, ok := store.Get(cfg.URL); ok && kept.ClientID == clientID {
			secret = kept.ClientSecret
		}
	} else if clientID, secret, err = mcp.Register(ctx, hc, as, redirect); err != nil {
		stop()
		return nil, fmt.Errorf("the sign-in to %s could not start: %w", cfg.Name, err)
	}
	f := mcp.NewFlow(as, clientID, secret, redirect, scopes, cfg.URL)
	flow.Store(f)
	return &signIn{flow: f, back: back, stop: stop}, nil
}

// finishSignIn trades what the browser came back with for a token, and
// keeps it.
func finishSignIn(ctx context.Context, cfg mcp.Config, si *signIn, back url.Values, store *mcp.LoginStore) error {
	si.stop()
	tok, err := si.flow.Finish(ctx, &http.Client{Timeout: 30 * time.Second}, back)
	if err != nil {
		return fmt.Errorf("the sign-in to %s did not go through: %w", cfg.Name, err)
	}
	login := mcp.Login{Server: cfg.URL, Issuer: si.flow.AS.Issuer, TokenEndpoint: si.flow.AS.TokenEndpoint,
		ClientID: si.flow.ClientID, ClientSecret: si.flow.ClientSecret, Token: tok}
	if err := store.Put(login); err != nil {
		return fmt.Errorf("the sign-in to %s could not be kept: %w", cfg.Name, err)
	}
	return nil
}

// mcpLogins is where the sign-ins are kept. (It takes no lock of the
// servers': the picker asks for it with theirs held.)
func (l *live) mcpLogins() *mcp.LoginStore {
	l.mcp.loginsOnce.Do(func() {
		// (The person's, in gnosh's own state folder beside the trusted
		// folders: never the project's data folder.)
		l.mcp.logins = &mcp.LoginStore{Path: filepath.Join(l.cfg.Paths.StateDir, "mcp-logins.json")}
	})
	return l.mcp.logins
}

// mcpAuthorizer gives a server at a URL its sign-in.
func (l *live) mcpAuthorizer(s *mcpServer) *mcp.Authorizer {
	if s.cfg.Type != "http" {
		return nil
	}
	return &mcp.Authorizer{Logins: l.mcpLogins(), Server: s.cfg.URL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// mcpNeedsAuth marks a server whose handshake was refused as wanting a
// sign-in, and says so.
func (l *live) mcpNeedsAuth(s *mcpServer, gen int, na *mcp.NeedsAuth) {
	l.mcp.mu.Lock()
	if s.gen != gen {
		l.mcp.mu.Unlock()
		return
	}
	s.status, s.err, s.www = "needs-auth", na.Error(), na.WWWAuthenticate
	l.mcp.mu.Unlock()
	l.flog.logf("mcp", "the MCP server %s wants a sign-in: %v", s.cfg.Name, na)
	if l.interactive {
		l.notice(agent.LevelWarning, "mcp", fmt.Sprintf("The MCP server %s wants you to sign in: /mcp login %s starts it.", s.cfg.Name, s.cfg.Name))
		return
	}
	l.notice(agent.LevelWarning, "mcp", fmt.Sprintf("The MCP server %s wants a sign-in, which a run cannot make: sign in first, with gnosh mcp login %s from a shell or /mcp login %s in a session.", s.cfg.Name, s.cfg.Name, s.cfg.Name))
}

// mcpLogin is /mcp login <name> [address].
func (l *live) mcpLogin(ctx context.Context, c *SlashContext, s *mcpServer, rest []string) error {
	name := s.cfg.Name
	if s.cfg.Type != "http" {
		return fmt.Errorf("the MCP server %s is started by gnosh, not reached at a URL: it has no sign-in", name)
	}
	if !l.interactive {
		return fmt.Errorf("a run cannot sign in to %s: sign in from an interactive session first, or with gnosh mcp login %s", name, name)
	}
	if len(rest) > 0 {
		// The address the browser tried to go to, pasted.
		back, err := mcp.Callback(strings.Join(rest, " "))
		if err != nil {
			return err
		}
		l.mcp.mu.Lock()
		f := l.mcp.flows[name]
		l.mcp.mu.Unlock()
		if f == nil {
			return fmt.Errorf("no sign-in to %s is under way: /mcp login %s starts one", name, name)
		}
		return l.mcpFinish(ctx, c.Say, s, f, back)
	}
	l.mcp.mu.Lock()
	www := s.www
	l.mcp.mu.Unlock()
	si, err := startSignIn(ctx, s.cfg, www, l.mcpLogins())
	if err != nil {
		return err
	}
	f := &mcpFlow{signIn: si, at: l.p.now()}
	l.mcp.mu.Lock()
	if l.mcp.flows == nil {
		l.mcp.flows = map[string]*mcpFlow{}
	}
	if old := l.mcp.flows[name]; old != nil {
		old.stop()
	}
	l.mcp.flows[name] = f
	l.mcp.mu.Unlock()
	address := f.flow.URL()
	c.Say("Sign in to %s at this address:", name)
	c.Say("%s", address)
	c.Say("When the browser comes back to this machine, the sign-in ends by itself. When it cannot (gnosh runs over ssh), copy the address the browser tried to open, and send /mcp login %s <that address>.", name)
	if openBrowser(address) {
		c.Say("(A browser was opened at it.)")
	}
	// The browser's answer, when it reaches this machine.
	go func() {
		select {
		case got := <-f.back:
			if err := l.mcpFinish(l.ctx, func(format string, args ...any) {
				l.notice(agent.LevelInfo, "mcp", fmt.Sprintf(format, args...))
			}, s, f, got); err != nil {
				l.notice(agent.LevelWarning, "mcp", err.Error())
			}
		case <-time.After(mcpLoginWait):
			l.mcpDropFlow(name, f)
		case <-l.ctx.Done():
			l.mcpDropFlow(name, f)
		}
	}()
	return nil
}

// mcpFinish ends a sign-in with what the browser came back with: the code
// is traded for a token, which is kept, and the server is connected again.
// A sign-in ends once: what comes after is told so.
func (l *live) mcpFinish(ctx context.Context, say func(string, ...any), s *mcpServer, f *mcpFlow, back url.Values) error {
	name := s.cfg.Name
	l.mcp.mu.Lock()
	current := l.mcp.flows[name] == f
	if current {
		delete(l.mcp.flows, name)
	}
	l.mcp.mu.Unlock()
	if !current {
		return fmt.Errorf("the sign-in to %s has ended already", name)
	}
	if err := finishSignIn(ctx, s.cfg, f.signIn, back, l.mcpLogins()); err != nil {
		return err
	}
	l.flog.logf("mcp", "signed in to the MCP server %s", name)
	l.mcpReconnect(s)
	l.mcp.mu.Lock()
	line := l.mcpLine(s)
	l.mcp.mu.Unlock()
	say("Signed in to %s. %s", name, line)
	return nil
}

// mcpDropFlow forgets a sign-in nobody finished.
func (l *live) mcpDropFlow(name string, f *mcpFlow) {
	l.mcp.mu.Lock()
	if l.mcp.flows[name] == f {
		delete(l.mcp.flows, name)
	}
	l.mcp.mu.Unlock()
	f.stop()
}

// mcpLogout is /mcp logout <name>: the sign-in kept for the server is
// forgotten, and the server connected again without it.
func (l *live) mcpLogout(c *SlashContext, s *mcpServer) error {
	if s.cfg.Type != "http" {
		return fmt.Errorf("the MCP server %s is started by gnosh, not reached at a URL: it has no sign-in", s.cfg.Name)
	}
	had, err := l.mcpLogins().Delete(s.cfg.URL)
	if err != nil {
		return err
	}
	if !had {
		c.Say("No sign-in to %s is kept.", s.cfg.Name)
		return nil
	}
	l.mcpReconnect(s)
	l.mcp.mu.Lock()
	line := l.mcpLine(s)
	l.mcp.mu.Unlock()
	c.Say("The sign-in to %s is forgotten (the authorization server may still hold the token until it runs out). %s", s.cfg.Name, line)
	return nil
}

// mcpReconnect lets go of a server's connection and makes it again, and
// waits for it. A server the person disabled stays so: a sign-in that
// ends after /mcp disable, or /mcp logout, does not start it again.
func (l *live) mcpReconnect(s *mcpServer) {
	l.mcp.mu.Lock()
	if s.status == "disabled" {
		l.mcp.mu.Unlock()
		return
	}
	old := s.client
	s.gen++
	gen := s.gen
	s.client, s.tools, s.err, s.status = nil, nil, "", "pending"
	l.mcp.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	l.offerMCPTools()
	l.connectMCP(s, gen)
}

// mcpSignedIn reports whether a sign-in is kept for the server.
func (l *live) mcpSignedIn(s *mcpServer) bool {
	if s.cfg.Type != "http" {
		return false
	}
	_, ok := l.mcpLogins().Get(s.cfg.URL)
	return ok
}

// openBrowser opens the address in a browser, where gnosh runs at a
// desktop and not over ssh, and reports whether it did. Only a web address
// is opened: a scheme of another kind could start a program.
func openBrowser(address string) bool {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", address)
	case "linux", "freebsd", "openbsd":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return false
		}
		cmd = exec.Command("xdg-open", address)
	default:
		return false
	}
	if cmd.Start() != nil {
		return false
	}
	go cmd.Wait()
	return true
}

// containsWord reports whether list holds w.
func containsWord(list []string, w string) bool {
	for _, x := range list {
		if x == w {
			return true
		}
	}
	return false
}

// errNeedsAuth reports whether err is a server's wanting a sign-in.
func errNeedsAuth(err error) (*mcp.NeedsAuth, bool) {
	var na *mcp.NeedsAuth
	if errors.As(err, &na) {
		return na, true
	}
	return nil, false
}
