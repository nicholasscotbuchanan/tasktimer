package sync

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"task-timer-app/internal/task"
)

// EnvFileName is an optional file of environment variables, in the data
// directory beside sync.json.
//
// It exists because of the single most common way a working sync setup fails.
// Providers deliberately take their secrets from the environment rather than
// from the config file — a token in a config file is a token in a backup and a
// screen share. But a daemon started by systemd or launchd does not inherit the
// shell that exported it, so a setup that works perfectly from a terminal breaks
// the moment it is installed as a service, with an opaque 401.
//
// So the daemon reads its own environment file, and the service definitions
// this project ships need no secrets in them at all.
const EnvFileName = "sync.env"

// EnvPath returns the location of the environment file.
func EnvPath() string {
	return filepath.Join(task.DataDir(), EnvFileName)
}

// LoadEnv sets variables from an env file into the process environment and
// returns the names it set — the names only, never the values, so that a caller
// logging the result cannot leak a token into a log file.
//
// A variable already present in the environment wins: an operator overriding a
// value at launch should not be silently undone by a file on disk. A missing
// file is not an error; the file is optional.
func LoadEnv(path string) ([]string, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer file.Close()

	var set []string

	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		// `export FOO=bar` is what people paste in from a shell.
		text = strings.TrimPrefix(text, "export ")

		key, value, found := strings.Cut(text, "=")
		if !found {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, line)
		}

		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty variable name", path, line)
		}

		value = strings.TrimSpace(value)
		value = trimQuotes(value)

		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return nil, fmt.Errorf("%s:%d: setting %s: %w", path, line, key, err)
		}
		set = append(set, key)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return set, nil
}

// EnvNames returns the variable names assigned in the env file, without their
// values, so a reader can check whether a credential has already been written
// without pulling the token into its own environment. A missing file yields no
// names and no error — the file is optional.
func EnvNames(path string) ([]string, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer file.Close()

	var names []string

	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")

		key, _, found := strings.Cut(text, "=")
		if !found {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty variable name", path, line)
		}
		names = append(names, key)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return names, nil
}

// SetEnvVar writes KEY=VALUE into the env file, replacing any existing entry for
// that key and leaving every other line — including comments — exactly as it was.
//
// The file is created 0600 and an existing one is re-chmodded to 0600 on the way
// out. It holds bearer tokens; a file this program wrote itself has no business
// being group-readable, whatever the caller's umask happens to be.
func SetEnvVar(path, key, value string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("sync: cannot write an env var with an empty name")
	}

	var lines []string
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		lines = strings.Split(strings.TrimRight(string(existing), "\n"), "\n")
	case errors.Is(err, fs.ErrNotExist):
		lines = nil
	default:
		return fmt.Errorf("reading %s: %w", path, err)
	}

	entry := key + "=" + value
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimPrefix(strings.TrimSpace(line), "export ")
		name, _, found := strings.Cut(trimmed, "=")
		if !found || strings.TrimSpace(name) != key {
			continue
		}
		lines[i] = entry
		replaced = true
		break
	}
	if !replaced {
		lines = append(lines, entry)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// WriteFile does not chmod a file that already existed.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("securing %s: %w", path, err)
	}
	return nil
}

// trimQuotes removes one matching pair of surrounding quotes.
func trimQuotes(v string) string {
	if len(v) < 2 {
		return v
	}
	if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// EnvFileIsExposed reports whether the env file is readable by anyone other than
// its owner. It holds API tokens, so a group- or world-readable one is worth
// saying out loud rather than quietly tolerating.
func EnvFileIsExposed(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().Perm()&0o077 != 0
}
