package app

import (
	"os"
	"path/filepath"
	"testing"
)

// A remote's address gives the host, the owner and the name, in each of
// the forms git takes.
func TestParseRepoURL(t *testing.T) {
	for raw, want := range map[string][3]string{
		"https://github.com/gnosh-ai/gnosh.git":       {"github.com", "gnosh-ai", "gnosh"},
		"https://github.com/gnosh-ai/gnosh":           {"github.com", "gnosh-ai", "gnosh"},
		"git@github.com:gnosh-ai/gnosh.git":           {"github.com", "gnosh-ai", "gnosh"},
		"ssh://git@GitLab.example.com:2222/a/b/c.git": {"gitlab.example.com", "a/b", "c"},
		"host.example:owner/repo/":                    {"host.example", "owner", "repo"},
	} {
		host, owner, name, ok := parseRepoURL(raw)
		if !ok || [3]string{host, owner, name} != want {
			t.Errorf("parseRepoURL(%q) = %q %q %q %v, want %q", raw, host, owner, name, ok, want)
		}
	}
	for _, bad := range []string{"", "/srv/git/repo.git", "https://github.com/onlyname", "file:///x/y", "git@host:"} {
		if host, owner, name, ok := parseRepoURL(bad); ok {
			t.Errorf("parseRepoURL(%q) = %q %q %q", bad, host, owner, name)
		}
	}
}

// The origin is read from the repository's own config file, and from the
// main repository's for a work tree, whose .git is a file.
func TestOriginRepo(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(root, "repo")
	write(filepath.Join(repo, ".git", "config"), "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = https://example.com/x/y.git\n[remote \"origin\"]\n\turl = git@github.com:gnosh-ai/gnosh.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n")
	if host, owner, name, ok := originRepo(repo); !ok || host != "github.com" || owner != "gnosh-ai" || name != "gnosh" {
		t.Errorf("the repository: %q %q %q %v", host, owner, name, ok)
	}
	tree := filepath.Join(root, "tree")
	write(filepath.Join(repo, ".git", "worktrees", "tree", "commondir"), "../..\n")
	write(filepath.Join(tree, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "tree")+"\n")
	if host, owner, name, ok := originRepo(tree); !ok || host != "github.com" || owner != "gnosh-ai" || name != "gnosh" {
		t.Errorf("the work tree: %q %q %q %v", host, owner, name, ok)
	}
	if _, _, _, ok := originRepo(t.TempDir()); ok {
		t.Error("a folder that is no repository has an origin")
	}
}
