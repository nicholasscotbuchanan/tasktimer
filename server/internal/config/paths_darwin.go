//go:build darwin

package config

import (
	"os"
	"path/filepath"
)

// defaultConfigPath on macOS is under the user's Application Support directory,
// where the TaskTimerServer.app bundle and its LaunchAgent expect it. There is
// no /etc equivalent for a user-installed .app, and writing under /etc would
// need root the app does not have.
func defaultConfigPath() string {
	return filepath.Join(appSupportDir(), "config.toml")
}

// credentialDir prefers a systemd-style credential directory if one is somehow
// present, and otherwise falls back to the config directory, so the two secret
// files sit beside config.toml in Application Support - a per-user location, not
// a shared one. An empty string means "no credential directory".
func credentialDir() string {
	if d := os.Getenv("CREDENTIALS_DIRECTORY"); d != "" {
		return d
	}
	return filepath.Dir(configPath())
}

func appSupportDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "Library", "Application Support", "TaskTimerServer")
	}
	return "/Library/Application Support/TaskTimerServer"
}
