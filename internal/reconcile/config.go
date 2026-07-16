package reconcile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"task-timer-app/internal/task"
)

// ConfigFileName is the config file's name inside the data directory. It is YAML:
// the whole configuration surface lives in a file a person edits by hand, with
// nothing compiled in.
const ConfigFileName = "config.yaml"

// DefaultPollInterval is used when the config omits one.
const DefaultPollInterval = 60 * time.Second

// DefaultPollIntervalText is DefaultPollInterval written the way a person would.
// time.Duration.String renders it as "1m0s" — correct, but it reads like a
// machine wrote the config file. TestDefaultPollIntervalTextMatches keeps the
// two in step.
const DefaultPollIntervalText = "60s"

// Config is the daemon's on-disk configuration.
//
// In memory a provider's Settings stay an opaque json.RawMessage — that is what
// lets a key the settings form does not know about survive a save untouched. The
// on-disk form is YAML; the two are bridged at the file boundary (see fileConfig)
// so nothing above this file has to care what the file format is.
type Config struct {
	// PollInterval is how often the engine runs a full reconcile cycle, as a Go
	// duration string such as "60s" or "5m".
	PollInterval string `json:"poll_interval"`
	// Providers lists the backends to run. Order is not significant.
	Providers []ProviderConfig `json:"providers"`
}

// ProviderConfig enables one backend and carries its opaque settings.
type ProviderConfig struct {
	// Name selects a registered provider, e.g. "gateway".
	Name string `json:"name"`
	// Enabled allows a provider to stay configured but dormant.
	Enabled bool `json:"enabled"`
	// Settings is passed verbatim to the provider's factory. Its shape is
	// defined by the provider, not by the engine.
	Settings json.RawMessage `json:"settings"`
}

// fileConfig mirrors Config for YAML, differing only in that a provider's
// settings are a plain map rather than opaque bytes — YAML has no equivalent of
// json.RawMessage. Conversion to and from Config happens in load/save, so the
// in-memory model stays byte-opaque while the file stays human-editable YAML.
type fileConfig struct {
	PollInterval string         `yaml:"poll_interval"`
	Providers    []fileProvider `yaml:"providers"`
}

type fileProvider struct {
	Name     string         `yaml:"name"`
	Enabled  bool           `yaml:"enabled"`
	Settings map[string]any `yaml:"settings"`
}

// Interval returns the configured poll interval, falling back to the default.
func (c Config) Interval() (time.Duration, error) {
	if c.PollInterval == "" {
		return DefaultPollInterval, nil
	}
	d, err := time.ParseDuration(c.PollInterval)
	if err != nil {
		return 0, fmt.Errorf("parsing poll_interval %q: %w", c.PollInterval, err)
	}
	if d < time.Second {
		return 0, fmt.Errorf("poll_interval %s is too short; use at least 1s", d)
	}
	return d, nil
}

// ConfigPath returns the location of the config file.
func ConfigPath() string {
	return filepath.Join(task.DataDir(), ConfigFileName)
}

// LoadConfig reads the config file. When the file does not exist it writes a
// commented example and returns it with every provider disabled, so a first run
// is a no-op that leaves the user something concrete to edit rather than an
// error telling them to invent a file from scratch.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		cfg := exampleConfig()
		if err := writeExample(path, cfg); err != nil {
			return Config{}, err
		}
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}

	cfg, err := fc.toConfig()
	if err != nil {
		return Config{}, fmt.Errorf("in %s: %w", path, err)
	}
	if _, err := cfg.Interval(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// toConfig converts the YAML-facing form into the byte-opaque in-memory Config,
// re-encoding each provider's settings map as the json.RawMessage the rest of the
// program expects.
func (fc fileConfig) toConfig() (Config, error) {
	cfg := Config{PollInterval: fc.PollInterval}
	for _, fp := range fc.Providers {
		pc := ProviderConfig{Name: fp.Name, Enabled: fp.Enabled}
		if len(fp.Settings) > 0 {
			raw, err := json.Marshal(fp.Settings)
			if err != nil {
				return Config{}, fmt.Errorf("provider %q settings: %w", fp.Name, err)
			}
			pc.Settings = raw
		}
		cfg.Providers = append(cfg.Providers, pc)
	}
	return cfg, nil
}

// toFileConfig is the reverse: it decodes each opaque settings block into a map
// so the whole thing can be written as readable YAML.
func (c Config) toFileConfig() (fileConfig, error) {
	fc := fileConfig{PollInterval: c.PollInterval}
	for _, pc := range c.Providers {
		fp := fileProvider{Name: pc.Name, Enabled: pc.Enabled, Settings: map[string]any{}}
		if len(pc.Settings) > 0 {
			if err := json.Unmarshal(pc.Settings, &fp.Settings); err != nil {
				return fileConfig{}, fmt.Errorf("provider %q settings: %w", pc.Name, err)
			}
		}
		fc.Providers = append(fc.Providers, fp)
	}
	return fc, nil
}

// exampleConfig is the starter file written on first run: every compiled-in
// provider, present but disabled, with the fields a user has to fill in already
// spelled out.
//
// It is generated from the registry rather than written out by hand. A hard-coded
// example would mean the framework knowing the names and settings of its own
// plugins — so adding a backend would mean editing this file, and forgetting to
// would leave the new provider invisible to anyone who had not read the source.
func exampleConfig() Config {
	descriptors := Descriptors()

	providers := make([]ProviderConfig, 0, len(descriptors))
	for _, r := range descriptors {
		providers = append(providers, ProviderConfig{
			Name:     r.Name,
			Enabled:  false,
			Settings: DefaultSettings(r.Name),
		})
	}

	return Config{
		PollInterval: DefaultPollIntervalText,
		Providers:    providers,
	}
}

// SaveConfig writes the config back to disk. The desktop app's Settings page
// is the only caller: the daemon only ever reads.
//
// The write goes to a temporary file in the same directory and is then renamed
// over the target, so a crash mid-write cannot leave the daemon with a
// half-written config it will refuse to parse on next start.
func SaveConfig(path string, cfg Config) error {
	if _, err := cfg.Interval(); err != nil {
		return err
	}

	data, err := marshalConfig(cfg)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	// Created in the target's directory so the rename is same-filesystem and
	// therefore atomic.
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("creating temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	// 0600 before any content is written: the file may carry an API token.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("securing temporary config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temporary config: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

func writeExample(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	data, err := marshalConfig(cfg)
	if err != nil {
		return err
	}

	// 0600: the file is where an API token would go if the user chooses to
	// inline one rather than use api_token_env.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing example config to %s: %w", path, err)
	}
	return nil
}

// marshalConfig renders a Config as the YAML written to disk, with a short header
// comment so the file explains itself to whoever opens it next.
func marshalConfig(cfg Config) ([]byte, error) {
	fc, err := cfg.toFileConfig()
	if err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(fc)
	if err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	header := "# Task Timer configuration.\n" +
		"# Enable a provider by setting its `enabled` to true and filling in its\n" +
		"# settings. Secrets (bearer tokens) do not belong here — they live in\n" +
		"# credentials.env beside this file. Keys not shown here are preserved.\n"
	return append([]byte(header), body...), nil
}
