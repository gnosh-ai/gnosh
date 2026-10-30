package tools

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
)

const GlobToolName = "glob"

//go:embed glob.md.tpl
var globDescriptionTmpl []byte

var globDescriptionTpl = template.Must(
	template.New("globDescription").
		Parse(string(globDescriptionTmpl)),
)

type globDescriptionData struct {
	MaxResults int
}

func globDescription() string {
	return renderTemplate(globDescriptionTpl, globDescriptionData{
		MaxResults: 100,
	})
}

type GlobParams struct {
	Pattern string `json:"pattern" description:"The glob pattern to match files against"`
	Path    string `json:"path,omitempty" description:"The directory to search in. Defaults to the current working directory."`
}

type GlobResponseMetadata struct {
	NumberOfFiles int  `json:"number_of_files"`
	Truncated     bool `json:"truncated"`
}

func NewGlobTool(permissions permission.Service, workingDir string, cfg config.ToolGlob) fantasy.AgentTool {
	return fantasy.NewAgentTool(
		GlobToolName,
		globDescription(),
		func(ctx context.Context, params GlobParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Pattern == "" {
				return fantasy.NewTextErrorResponse("pattern is required"), nil
			}

			// Relative to the tool's working directory, as for grep.
			searchPath := filepathext.SmartJoin(workingDir, params.Path)
			walkRoot, _ := globWalk(params.Pattern, searchPath)
			if resp, stop, err := askToSearch(ctx, permissions, call, GlobToolName, walkRoot, workingDir, params); stop {
				return resp, err
			}
			if params.Path != "" {
				if msg := searchDirError(params.Path, searchPath, workingDir); msg != "" {
					return fantasy.NewTextErrorResponse(msg), nil
				}
			}

			// Bound the search so a huge or symlink-heavy root (e.g. $HOME
			// or a module cache) fails cleanly instead of pinning the CPU
			// and hanging the agent.
			timeout := cfg.GetTimeout()
			searchCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			files, truncated, err := globFiles(searchCtx, params.Pattern, searchPath, 100)
			if errors.Is(searchCtx.Err(), context.DeadlineExceeded) {
				// Cut short by its timeout, the search answered with what
				// it had found, "No files found" for files that may be
				// there. It says so, as grep does.
				err = fmt.Errorf("search timed out after %s; try a narrower path or pattern", timeout)
			}
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("error finding files: %v", err)), nil
			}

			var output string
			if len(files) == 0 {
				output = "No files found"
			} else {
				normalizeFilePaths(files)
				output = strings.Join(files, "\n")
				if truncated {
					output += "\n\n(Results are truncated. Consider using a more specific path or pattern.)"
				}
			}

			return fantasy.WithResponseMetadata(
				fantasy.NewTextResponse(output),
				GlobResponseMetadata{
					NumberOfFiles: len(files),
					Truncated:     truncated,
				},
			), nil
		},
	)
}

// globWalk is the directory a glob of pattern under searchPath walks, and
// the pattern it matches there. The walk is scoped to the pattern's
// literal directory prefix: "internal/agent/*.go" only needs to walk
// internal/agent, so it starts there instead of enumerating the entire
// tree and filtering. Patterns that begin with a wildcard (e.g.
// "**/foo.go") have no prefix and still walk from searchPath. An
// absolute pattern names its own directory, whatever the search path:
// "/usr/local/bin/k*" walks /usr/local/bin, not a directory of that name
// under the search path.
func globWalk(pattern, searchPath string) (walkRoot, walkPattern string) {
	prefix, rest := filepathext.SplitGlobPrefix(pattern)
	switch {
	case filepath.IsAbs(pattern):
		if prefix == filepath.VolumeName(prefix) {
			prefix += string(filepath.Separator)
		}
		return prefix, rest
	case prefix != "":
		return filepath.Join(searchPath, prefix), rest
	}
	return searchPath, pattern
}

func globFiles(ctx context.Context, pattern, searchPath string, limit int) ([]string, bool, error) {
	walkRoot, walkPattern := globWalk(pattern, searchPath)
	// A pattern whose directory is not there matches nothing, as in
	// Claude Code, rather than failing the search.
	if _, err := os.Stat(walkRoot); errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}

	cmdRg := getRgCmd(ctx, walkPattern)
	if cmdRg != nil {
		cmdRg.Dir = walkRoot
		matches, err := runRipgrep(cmdRg, walkRoot, limit)
		if err == nil {
			return matches, len(matches) >= limit && limit > 0, nil
		}
		slog.Warn("Ripgrep execution failed, falling back to doublestar", "error", err)
	}

	return fsext.GlobGitignoreAware(ctx, walkPattern, walkRoot, limit)
}

func runRipgrep(cmd *exec.Cmd, searchRoot string, limit int) ([]string, error) {
	// Stream ripgrep's stdout instead of buffering the whole file list.
	// Over a huge root (e.g. $HOME) the full --files listing can be
	// hundreds of MB; reading it all at once and then sorting allocated
	// gigabytes. We read incrementally and stop once we have a bounded
	// pool of candidates.
	//
	// We collect more than `limit` so the shortest-path preference below
	// still has something to choose from, but the pool is capped so memory
	// stays small (a few thousand paths) no matter how large the tree is.
	candidatePool := max(limit*20, 1000)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ripgrep: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ripgrep: %w", err)
	}

	var matches []string
	reader := bufio.NewReader(stdout)
	for {
		path, err := reader.ReadString(0)
		if len(path) > 0 {
			path = strings.TrimRight(path, "\x00")
			// Hidden and ignored directories are those below the
			// search root: a root inside one, such as /usr/local/bin
			// or a project under ~/build, still has its files found.
			if path != "" && !fsext.SkipHidden(path) {
				matches = append(matches, filepathext.SmartJoin(searchRoot, path))
			}
		}
		if err != nil {
			break // EOF or read error; drain handled by Wait below.
		}
		if len(matches) >= candidatePool {
			// Enough candidates; stop reading and let the process be
			// killed by the command context / Wait. Draining the rest
			// would just buffer paths we are going to discard.
			break
		}
	}

	// Close our end so ripgrep gets SIGPIPE and stops, then reap it.
	_ = stdout.Close()
	waitErr := cmd.Wait()
	if waitErr != nil && len(matches) == 0 {
		if ee, ok := waitErr.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil // No matches.
		}
		return nil, fmt.Errorf("ripgrep: %w\n%s", waitErr, stderr.String())
	}

	// Newest first, as the walker sorts, as Claude Code's Glob does and as
	// the tool's description says: by path length, the ripgrep path put
	// any short name before the file just edited.
	modTimes := make(map[string]time.Time, len(matches))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil {
			modTimes[m] = info.ModTime()
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return modTimes[matches[i]].After(modTimes[matches[j]])
	})

	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

// searchDirError says why path, the directory a search was asked to look
// in (abs is it resolved), cannot be searched, in Claude Code's words: it
// does not exist, with a guess at the one meant, or it is not a
// directory. It is "" when the directory is there, or when what is wrong
// is for the search itself to report.
func searchDirError(path, abs, workingDir string) string {
	info, err := os.Stat(abs)
	switch {
	case err == nil && !info.IsDir():
		return "Path is not a directory: " + path
	case err == nil, !errors.Is(err, fs.ErrNotExist):
		return ""
	}
	msg := fmt.Sprintf("Directory does not exist: %s. %s", path, workingDirNote(workingDir))
	if meant := pathMeant(abs, workingDir); meant != "" {
		if info, err := os.Stat(meant); err == nil && info.IsDir() {
			msg += fmt.Sprintf(" Did you mean %s?", meant)
		}
	}
	return msg
}

func normalizeFilePaths(paths []string) {
	for i, p := range paths {
		paths[i] = filepath.ToSlash(p)
	}
}
