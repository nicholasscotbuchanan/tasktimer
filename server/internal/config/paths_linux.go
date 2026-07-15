//go:build !windows && !darwin

package config

import "os"

// defaultConfigPath is where the config file lives on Linux and other Unixes:
// the FHS location the .deb and .rpm install it to.
func defaultConfigPath() string { return "/etc/task-timer-server/config.toml" }

// credentialDir is the directory the two secrets are read from. On Linux that is
// ONLY the systemd credential directory - there is deliberately no fallback to
// the config directory. /etc/task-timer-server holds no secret by design (the
// systemd unit passes them as credentials, mode 0400), and inventing a
// config-directory fallback here would quietly reintroduce on-disk secrets in a
// world-traversable path. An empty string means "no credential directory".
func credentialDir() string { return os.Getenv("CREDENTIALS_DIRECTORY") }
