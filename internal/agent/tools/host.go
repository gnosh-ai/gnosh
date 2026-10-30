package tools

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
	"mvdan.cc/sh/v3/syntax"
)

// A bash command runs on this machine unless its host names another
// declared one; then the harness runs it there through the system ssh.
// Where a command ran is a fact of the call, stamped on its result, never
// something hidden in a wrapper script the model wrote, which is how a
// path seen on one machine ends up tried on another.

// sshOptions hold for every host run: ssh must never stop to ask (a
// question would hang the tool), must give up on a dead host in bounded
// time, and takes a host's first key but refuses a changed one.
var sshOptions = []string{
	"-o", "BatchMode=yes",
	"-o", "ConnectTimeout=20",
	"-o", "StrictHostKeyChecking=accept-new",
}

// resolveHost finds the declared host a call names. "" and "local" mean
// this machine. An unknown name errors with the declared names, and with
// none declared says how one is declared, so the model asks the user
// rather than reaching for ssh in a script.
func resolveHost(hosts config.Hosts, name string) (config.HostConfig, bool, error) {
	if name == "" || name == config.LocalHostName {
		return config.HostConfig{}, false, nil
	}
	if h, ok := hosts[name]; ok {
		return h, true, nil
	}
	if len(hosts) == 0 {
		return config.HostConfig{}, false, fmt.Errorf("host %q: no other machine is declared; this machine is %s. A host is declared in the config (\"hosts\" in the JSON config, or `host add <name> --ssh user@address` in the rc file), so ask the user to declare it", name, config.LocalMachine())
	}
	return config.HostConfig{}, false, fmt.Errorf("host %q is not declared; the declared hosts are %s, and this machine (%s) is %q", name, strings.Join(hosts.Names(), ", "), config.LocalMachine(), config.LocalHostName)
}

// hostCommand builds the local command that runs command on the host at
// dest: ssh with the fixed options, "--" so dest is always read as a
// destination, and the command as one quoted argument for the remote
// login shell. With a working directory the command runs there, and does
// not run at all when the directory is missing.
func hostCommand(dest, workingDir, command string) (string, error) {
	script := command
	if workingDir != "" {
		dir, err := syntax.Quote(workingDir, syntax.LangBash)
		if err != nil {
			return "", fmt.Errorf("working_dir: %w", err)
		}
		script = "cd " + dir + " || exit\n" + command
	}
	quoted, err := syntax.Quote(script, syntax.LangBash)
	if err != nil {
		return "", fmt.Errorf("command: %w", err)
	}
	parts := append([]string{"ssh"}, sshOptions...)
	parts = append(parts, "--", dest, quoted)
	return strings.Join(parts, " "), nil
}

// hostDenied is the first of the user's Bash deny rules that a command
// meant for a host matches, or "". This machine's shell refuses such a
// command as it runs; a host's shell is not Gnosh's, so the command is
// read here first, each command in it as plain words. The ban list of
// this machine (ssh among it) is not applied to a host: it protects this
// machine, and the host is the user's declaration plus a permission
// prompt that names it.
func hostDenied(command string, denyRules []string) string {
	if len(denyRules) == 0 {
		return ""
	}
	blockers := make([]shell.BlockFunc, len(denyRules))
	for i, pattern := range denyRules {
		blockers[i] = shell.CommandRuleBlocker(pattern)
	}
	denied := ""
	eachCall(command, func(call *syntax.CallExpr) bool {
		words := plainWords(call)
		if len(words) == 0 {
			return true
		}
		for i, blocked := range blockers {
			if blocked(words) {
				denied = denyRules[i]
				return false
			}
		}
		return true
	})
	return denied
}

// hostPermissionPath keys a permission on the other machine, so a grant
// for the session covers that host and not a local directory of the same
// name. It is a location, in ssh's own URL form.
func hostPermissionPath(dest, workingDir string) string {
	return "ssh://" + dest + "/" + strings.TrimPrefix(workingDir, "/")
}

// resultTags end a bash result with where it ran and, when known, where it
// ended up. This machine's shell reports the directory it was left in;
// another machine's is known only when the call set it.
func resultTags(hostName, machine, cwd string) string {
	tags := fmt.Sprintf("\n\n<host>%s (%s)</host>", hostName, machine)
	if cwd != "" {
		tags += fmt.Sprintf("\n<cwd>%s</cwd>", normalizeWorkingDir(cwd))
	}
	return tags
}
