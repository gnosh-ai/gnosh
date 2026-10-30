//go:build !windows

package config

// systemConfigPaths are the system-wide configuration files, under the
// name from before the rename and the new one, which wins. They are loaded
// at the lowest priority so user and project configs override them.
var systemConfigPaths = []string{"/etc/crush/crush.json", "/etc/gnosh/gnosh.json"}
