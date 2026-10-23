package skills

import (
	"embed"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
)

// BuiltinPrefix is the path prefix for builtin skill files. It is used by
// the View tool to distinguish embedded files from disk files.
const BuiltinPrefix = "klash://skills/"

// LegacyBuiltinPrefix is the prefix builtin skill files had before Crush
// was renamed Klash. Old sessions and prompt templates still name files
// under it, so it still resolves (see CanonicalBuiltinPath).
const LegacyBuiltinPrefix = "crush://skills/"

// legacySkillNames maps builtin skills' names from before the rename to
// their names now.
var legacySkillNames = map[string]string{
	"crush-config": "klash-config",
	"crush-hooks":  "klash-hooks",
}

// CanonicalSkillName returns a builtin skill's current name given the name
// it had before the rename; any other name is returned as it is.
func CanonicalSkillName(name string) string {
	if current, ok := legacySkillNames[name]; ok {
		return current
	}
	return name
}

// IsBuiltinPath reports whether path names a builtin skill file, under
// the current prefix or the one from before the rename.
func IsBuiltinPath(path string) bool {
	return strings.HasPrefix(path, BuiltinPrefix) || strings.HasPrefix(path, LegacyBuiltinPrefix)
}

// CanonicalBuiltinPath returns a builtin skill file's path under the
// current prefix and skill name: crush://skills/crush-config/SKILL.md is
// klash://skills/klash-config/SKILL.md. Other paths are returned as they
// are.
func CanonicalBuiltinPath(path string) string {
	rest, ok := strings.CutPrefix(path, LegacyBuiltinPrefix)
	if !ok {
		if rest, ok = strings.CutPrefix(path, BuiltinPrefix); !ok {
			return path
		}
	}
	name, tail, found := strings.Cut(rest, "/")
	name = CanonicalSkillName(name)
	if found {
		return BuiltinPrefix + name + "/" + tail
	}
	return BuiltinPrefix + name
}

//go:embed builtin/*
var builtinFS embed.FS

// BuiltinFS returns the embedded filesystem containing builtin skills.
func BuiltinFS() embed.FS {
	return builtinFS
}

// DiscoverBuiltin finds all valid skills embedded in the binary.
func DiscoverBuiltin() []*Skill {
	skills, _ := DiscoverBuiltinWithStates()
	return skills
}

// DiscoverBuiltinWithStates is like DiscoverBuiltin but additionally returns
// a per-file state slice describing parse/validation outcomes. Useful for
// diagnostics.
func DiscoverBuiltinWithStates() ([]*Skill, []*SkillState) {
	var discovered []*Skill
	var states []*SkillState

	fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || d.Name() != SkillFileName {
			return nil
		}

		content, err := builtinFS.ReadFile(path)
		if err != nil {
			slog.Warn("Failed to read builtin skill file", "path", path, "error", err)
			states = append(states, &SkillState{Path: path, State: StateError, Err: err})
			return nil
		}

		skill, err := ParseContent(content)
		if err != nil {
			slog.Warn("Failed to parse builtin skill file", "path", path, "error", err)
			states = append(states, &SkillState{Path: path, State: StateError, Err: err})
			return nil
		}

		// Set paths using the klash prefix. Strip the leading "builtin/"
		// so the path is relative to the embedded root
		// (e.g., "klash://skills/klash-config/SKILL.md").
		relPath, _ := filepath.Rel("builtin", path)
		relPath = filepath.ToSlash(relPath)
		skill.SkillFilePath = BuiltinPrefix + relPath
		skill.Path = BuiltinPrefix + filepath.Dir(relPath)
		skill.Builtin = true

		if err := skill.Validate(); err != nil {
			slog.Warn("Builtin skill validation failed", "path", path, "error", err)
			states = append(states, &SkillState{Name: skill.Name, Path: path, State: StateError, Err: err})
			return nil
		}

		slog.Debug("Successfully loaded builtin skill", "name", skill.Name, "path", skill.SkillFilePath)
		discovered = append(discovered, skill)
		states = append(states, &SkillState{Name: skill.Name, Path: skill.SkillFilePath, State: StateNormal})
		return nil
	})

	return discovered, states
}
