//go:build windows

package config

import (
	"os"
	"path/filepath"
)

// defaultConfigPath on Windows is under %ProgramData%, the machine-wide location
// a service (running as LocalSystem, with no user profile) can always read. The
// NSIS installer writes config.toml there, and `service install` points the
// service at this same tree.
func defaultConfigPath() string {
	return filepath.Join(programDataDir(), "config.toml")
}

// credentialDir prefers a systemd-style credential directory if set, and
// otherwise falls back to the config directory, so the two secret files sit
// beside config.toml under %ProgramData%\TaskTimerServer. That directory should
// be ACL'd to the service account and Administrators; it is the Windows analogue
// of the systemd credential store the Linux unit uses. Empty means "none".
func credentialDir() string {
	if d := os.Getenv("CREDENTIALS_DIRECTORY"); d != "" {
		return d
	}
	return filepath.Dir(configPath())
}

func programDataDir() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, "TaskTimerServer")
	}
	return `C:\ProgramData\TaskTimerServer`
}
