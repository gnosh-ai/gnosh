package config

import "path/filepath"

// SessionEnvFile is the file a session's SessionStart hooks write export
// lines to, the one Claude Code hands them as CLAUDE_ENV_FILE, and which
// each bash command of the session and of its helper agents runs before
// itself, so a hook can set the environment of the agent's commands (nvm
// use, a virtualenv's PATH). It is in the user's data directory, not the
// project's: what it holds runs ahead of every later command, unseen by
// permission prompts and hooks, so it must not be where the agent's edits
// of the project go.
func SessionEnvFile(sessionID string) string {
	return filepath.Join(filepath.Dir(GlobalConfigData()), "session-env", sessionID+".sh")
}
