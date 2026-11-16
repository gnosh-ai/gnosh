package app

import (
	"bufio"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// originRepo reads the address of the origin remote from the git
// repository at root, from its own config file (no git is run, so it may
// be read in a folder that is not trusted), and takes the host, the owner
// and the repository's name from it, as Claude Code's status line gives
// them. ok is false outside a repository, without an origin, or for an
// address that is not of that shape.
func originRepo(root string) (host, owner, name string, ok bool) {
	if root == "" {
		return "", "", "", false
	}
	cfg := gitConfigPath(filepath.Join(root, ".git"))
	if cfg == "" {
		return "", "", "", false
	}
	return parseRepoURL(originURL(cfg))
}

// gitConfigPath is the config file of the repository whose .git is dot: a
// folder holds it; a file names the folder (gitdir: ...), whose commondir,
// for a work tree, names the folder that holds it.
func gitConfigPath(dot string) string {
	info, err := os.Stat(dot)
	if err != nil {
		return ""
	}
	dir := dot
	if !info.IsDir() {
		b, err := os.ReadFile(dot)
		if err != nil {
			return ""
		}
		gitdir, found := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
		if !found {
			return ""
		}
		dir = strings.TrimSpace(gitdir)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(filepath.Dir(dot), dir)
		}
		if common, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil {
			c := strings.TrimSpace(string(common))
			if !filepath.IsAbs(c) {
				c = filepath.Join(dir, c)
			}
			dir = c
		}
	}
	return filepath.Join(dir, "config")
}

// originURL is the url of [remote "origin"] in a git config file, or "".
func originURL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			in = strings.EqualFold(strings.Join(strings.Fields(line), " "), `[remote "origin"]`)
			continue
		}
		if !in {
			continue
		}
		if key, value, found := strings.Cut(line, "="); found && strings.EqualFold(strings.TrimSpace(key), "url") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// parseRepoURL takes host, owner and name from a remote's address:
// https://host/owner/name(.git), ssh://user@host[:port]/owner/name, or
// user@host:owner/name. The owner is all of the path before the name.
func parseRepoURL(raw string) (host, owner, name string, ok bool) {
	var p string
	switch {
	case raw == "":
		return "", "", "", false
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", "", "", false
		}
		host, p = u.Hostname(), u.Path
	default:
		at := strings.LastIndex(raw, "@")
		h, path, found := strings.Cut(raw[at+1:], ":")
		if !found || h == "" || strings.Contains(h, "/") {
			return "", "", "", false
		}
		host, p = h, path
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	i := strings.LastIndex(p, "/")
	if i <= 0 || i == len(p)-1 {
		return "", "", "", false
	}
	return strings.ToLower(host), p[:i], p[i+1:], true
}
