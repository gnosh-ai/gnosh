package config

import (
	"fmt"
	"maps"
	"os"
	"os/user"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// LocalHostName is this machine's name in the bash tool's host parameter:
// the machine every file tool reaches and every command without a host
// runs on. It cannot be declared as another host.
const LocalHostName = "local"

// HostConfig declares another machine the bash tool can run a command on,
// through the system ssh, when a call names the host. Where a command ran
// is then a fact the harness knows and stamps on the result, not something
// buried in a wrapper script the model wrote, which is how a path seen on
// one machine ends up tried on another.
type HostConfig struct {
	SSH         string `json:"ssh" jsonschema:"description=Where ssh connects: user@address or a Host alias from ~/.ssh/config,example=root@203.0.113.10,example=render"`
	Description string `json:"description,omitempty" jsonschema:"description=What the machine is for; the model reads it when choosing where a command runs,example=renders the videos (ffmpeg); outputs in /root/renders"`
}

// Hosts are the declared hosts by name.
type Hosts map[string]HostConfig

// Host is a declared host with its name, for listing.
type Host struct {
	Name string     `json:"name"`
	Host HostConfig `json:"host"`
}

// Sorted returns the hosts in name order.
func (h Hosts) Sorted() []Host {
	sorted := make([]Host, 0, len(h))
	for name, cfg := range h {
		sorted = append(sorted, Host{Name: name, Host: cfg})
	}
	slices.SortFunc(sorted, func(a, b Host) int {
		return strings.Compare(a.Name, b.Name)
	})
	return sorted
}

// Names returns the host names in order.
func (h Hosts) Names() []string {
	return slices.Sorted(maps.Keys(h))
}

var (
	hostNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// sshDestRe admits user@address, a bracketed IPv6 address and an ssh
	// config alias, and nothing ssh could read as an option (a leading
	// dash) or the shell as syntax (spaces, quotes, $, ;).
	sshDestRe = regexp.MustCompile(`^[A-Za-z0-9\[][A-Za-z0-9._@:%\[\]-]*$`)
)

// ValidateHosts checks every declared host: a name the model can type that
// is not this machine's, and an ssh destination ssh will read as one.
func (c *Config) ValidateHosts() error {
	for _, name := range c.Hosts.Names() {
		h := c.Hosts[name]
		switch {
		case name == LocalHostName:
			return fmt.Errorf("host %q: that name is this machine's; pick another", name)
		case !hostNameRe.MatchString(name):
			return fmt.Errorf("host %q: a name is letters, digits, dots, dashes and underscores", name)
		case h.SSH == "":
			return fmt.Errorf("host %q: ssh is required (user@address, or a Host from ~/.ssh/config)", name)
		case !sshDestRe.MatchString(h.SSH):
			return fmt.Errorf("host %q: ssh %q is not an ssh destination (user@address, or a Host from ~/.ssh/config)", name, h.SSH)
		}
	}
	return nil
}

// LocalMachine names this machine as user@hostname: the identity every
// file tool and every command without a host acts with.
var LocalMachine = sync.OnceValue(func() string {
	username := "?"
	if u, err := user.Current(); err == nil && u.Username != "" {
		username = u.Username
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "?"
	}
	return username + "@" + hostname
})
