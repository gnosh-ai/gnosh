package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// This file is /keybindings, as Claude Code has it: the person's own
// keyboard shortcuts file, keybindings.json beside their gnosh.json, which
// the interface reads (internal/ui/keybindings.go). Typed alone in the full
// interface it opens in their editor; here, and in a run, it is made when
// it is not there, and where it is is said.

// KeybindingsTemplate is a new keybindings file: Claude Code's form, with
// nothing bound.
const KeybindingsTemplate = `{
  "$docs": "https://code.claude.com/docs/en/keybindings",
  "bindings": [
    {
      "context": "Chat",
      "bindings": {}
    }
  ]
}
`

// keybindingsPath is the person's keybindings.json, or "" when there is no
// folder for their own configuration.
func (l *live) keybindingsPath() string {
	if l.cfg == nil || l.cfg.Paths.ConfigDir == "" {
		return ""
	}
	return filepath.Join(l.cfg.Paths.ConfigDir, "keybindings.json")
}

// slashKeybindings is /keybindings.
func (l *live) slashKeybindings(ctx context.Context, c *SlashContext) error {
	path := l.keybindingsPath()
	if path == "" {
		return errors.New("there is no folder for your own configuration, so there is no keybindings file")
	}
	made := false
	if _, err := os.Stat(path); err != nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return fmt.Errorf("the keybindings file %s could not be made: %v", l.tilde(path), err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, err = f.WriteString(KeybindingsTemplate)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			made = err == nil
		}
		if err != nil && !os.IsExist(err) {
			return fmt.Errorf("the keybindings file %s could not be made: %v", l.tilde(path), err)
		}
	}
	if made {
		c.Say("Your keyboard shortcuts file is %s, made now with nothing bound.", l.tilde(path))
	} else {
		c.Say("Your keyboard shortcuts file is %s.", l.tilde(path))
	}
	c.Say(`It binds a key to an action by Claude Code's names ("ctrl+e": "chat:externalEditor"), takes one away with null, or runs a command ("ctrl+y": "command:compact"), in the contexts Global, Chat and Transcript. The interface reads it again when it changes.`)
	return nil
}
