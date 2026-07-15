// Package config loads the gateway's settings.
//
// Settings come from, in ascending order of precedence:
//
//  1. the config file at the per-platform default path (see defaultConfigPath),
//     or wherever TASK_TIMER_SERVER_CONFIG points
//  2. environment variables prefixed TASK_TIMER_SERVER_
//  3. the credential directory, for the two secrets that must never sit in a
//     shared config file (see credentialDir): systemd credentials on Linux, the
//     config directory on macOS and Windows
//
// The default config path and credential directory differ per OS - /etc on
// Linux, ~/Library/Application Support on macOS, %ProgramData% on Windows - so
// the packaged binary reads the right place on each without an env var. Those
// per-platform details live in paths_<goos>.go.
//
// The Atlassian client secret and the token-encryption key are deliberately NOT
// given defaults. A server that generates its own encryption key on first boot
// would quietly invalidate every stored refresh token the next time it restarts,
// and every user would have to re-consent without being told why.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// ConfigPathEnv overrides the config file's location. It wins over the
// per-platform default (see defaultConfigPath), so a test, a second instance, or
// a deployment whose layout differs from the packaged one can point elsewhere.
const ConfigPathEnv = "TASK_TIMER_SERVER_CONFIG"

// Settings is the fully-resolved configuration. Every field has a sane zero
// value except the three that MUST be provided; those are checked by
// RequireOAuth rather than defaulted, on purpose.
type Settings struct {
	// --- service ---
	Host        string
	Port        int
	PublicURL   string
	DatabaseURL string

	// --- atlassian oauth 2.0 (3LO) ---
	AtlassianClientID     string
	AtlassianClientSecret string

	// --- jira ---
	JiraJQL            string
	JiraDoneTransition string
	JiraAllowComplete  bool

	// --- registration ---
	AllowedEmailDomains []string

	// --- secrets ---
	// TokenEncryptionKey is base64 (std or url) of 32 bytes: an AES-256 key that
	// encrypts Jira refresh tokens at rest.
	TokenEncryptionKey string
}

// tomlFile mirrors the on-disk config's table layout. Flattening one level of
// tables ([jira] jql -> JiraJQL) keeps the file readable without a nested model.
type tomlFile struct {
	Host        string `toml:"host"`
	Port        int    `toml:"port"`
	PublicURL   string `toml:"public_url"`
	DatabaseURL string `toml:"database_url"`

	Atlassian struct {
		ClientID     string `toml:"client_id"`
		ClientSecret string `toml:"client_secret"`
	} `toml:"atlassian"`

	Jira struct {
		JQL            string `toml:"jql"`
		DoneTransition string `toml:"done_transition"`
		AllowComplete  bool   `toml:"allow_complete"`
	} `toml:"jira"`

	AllowedEmailDomains []string `toml:"allowed_email_domains"`
}

// defaults returns Settings with every non-secret field at its documented
// default, matching the old pydantic model one for one.
func defaults() Settings {
	return Settings{
		Host:               "127.0.0.1",
		Port:               8080,
		PublicURL:          "http://127.0.0.1:8080",
		DatabaseURL:        "sqlite:////var/lib/task-timer-server/server.db",
		JiraJQL:            "assignee = currentUser() AND statusCategory != Done",
		JiraDoneTransition: "Done",
	}
}

// Load resolves the configuration from the config file, the environment, and
// systemd credentials, in that order of increasing precedence.
func Load() (Settings, error) {
	s := defaults()

	if err := applyTOML(&s); err != nil {
		return Settings{}, err
	}
	applyEnv(&s)
	applyCredentials(&s)

	s.PublicURL = strings.TrimRight(s.PublicURL, "/")
	s.AllowedEmailDomains = normalizeDomains(s.AllowedEmailDomains)
	return s, nil
}

func configPath() string {
	if p := os.Getenv(ConfigPathEnv); p != "" {
		return p
	}
	return defaultConfigPath()
}

func applyTOML(s *Settings) error {
	path := configPath()
	if _, err := os.Stat(path); err != nil {
		// A missing config file is normal (env-only deployments, tests); only a
		// present-but-unreadable one is worth failing over.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading %s: %w", path, err)
	}

	var f tomlFile
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	if f.Host != "" {
		s.Host = f.Host
	}
	if f.Port != 0 {
		s.Port = f.Port
	}
	if f.PublicURL != "" {
		s.PublicURL = f.PublicURL
	}
	if f.DatabaseURL != "" {
		s.DatabaseURL = f.DatabaseURL
	}
	if f.Atlassian.ClientID != "" {
		s.AtlassianClientID = f.Atlassian.ClientID
	}
	if f.Atlassian.ClientSecret != "" {
		s.AtlassianClientSecret = f.Atlassian.ClientSecret
	}
	if f.Jira.JQL != "" {
		s.JiraJQL = f.Jira.JQL
	}
	if f.Jira.DoneTransition != "" {
		s.JiraDoneTransition = f.Jira.DoneTransition
	}
	// A bool cannot distinguish "absent" from "false", so allow_complete is taken
	// verbatim: the config file's value wins, and its default there is false.
	s.JiraAllowComplete = f.Jira.AllowComplete
	if len(f.AllowedEmailDomains) > 0 {
		s.AllowedEmailDomains = f.AllowedEmailDomains
	}
	return nil
}

// applyEnv overlays TASK_TIMER_SERVER_* variables. Present-and-empty is treated
// as "unset" so an exported-but-blank variable does not wipe a config value.
func applyEnv(s *Settings) {
	const prefix = "TASK_TIMER_SERVER_"
	get := func(name string) (string, bool) {
		v, ok := os.LookupEnv(prefix + name)
		if !ok || v == "" {
			return "", false
		}
		return v, true
	}

	if v, ok := get("HOST"); ok {
		s.Host = v
	}
	if v, ok := get("PORT"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			s.Port = n
		}
	}
	if v, ok := get("PUBLIC_URL"); ok {
		s.PublicURL = v
	}
	if v, ok := get("DATABASE_URL"); ok {
		s.DatabaseURL = v
	}
	if v, ok := get("ATLASSIAN_CLIENT_ID"); ok {
		s.AtlassianClientID = v
	}
	if v, ok := get("ATLASSIAN_CLIENT_SECRET"); ok {
		s.AtlassianClientSecret = v
	}
	if v, ok := get("JIRA_JQL"); ok {
		s.JiraJQL = v
	}
	if v, ok := get("JIRA_DONE_TRANSITION"); ok {
		s.JiraDoneTransition = v
	}
	if v, ok := get("JIRA_ALLOW_COMPLETE"); ok {
		s.JiraAllowComplete = parseBool(v)
	}
	if v, ok := get("ALLOWED_EMAIL_DOMAINS"); ok {
		s.AllowedEmailDomains = strings.Split(v, ",")
	}
	if v, ok := get("TOKEN_ENCRYPTION_KEY"); ok {
		s.TokenEncryptionKey = v
	}
}

// applyCredentials reads the two secrets from the platform's credential
// directory (see credentialDir): the systemd credential store on Linux, or the
// config directory on macOS and Windows. They land mode 0400/ACL'd to the
// service account. Preferred over the environment: /proc/<pid>/environ is
// readable by the same user, and an env var leaks into every child process and
// crash dump.
func applyCredentials(s *Settings) {
	dir := credentialDir()
	if dir == "" {
		return
	}
	for name, dst := range map[string]*string{
		"atlassian_client_secret": &s.AtlassianClientSecret,
		"token_encryption_key":    &s.TokenEncryptionKey,
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(data)); v != "" {
			*dst = v
		}
	}
}

// RedirectURI is the Atlassian callback, derived from PublicURL. It must match
// the URI registered with Atlassian byte for byte.
func (s Settings) RedirectURI() string {
	return s.PublicURL + "/auth/callback"
}

// Addr is the host:port the HTTP server binds.
func (s Settings) Addr() string {
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

// RequireOAuth fails loudly at startup rather than with a 500 on the first login.
func (s Settings) RequireOAuth() error {
	var missing []string
	if s.AtlassianClientID == "" {
		missing = append(missing, "atlassian_client_id")
	}
	if s.AtlassianClientSecret == "" {
		missing = append(missing, "atlassian_client_secret")
	}
	if s.TokenEncryptionKey == "" {
		missing = append(missing, "token_encryption_key")
	}
	if len(missing) == 0 {
		return nil
	}

	env := make([]string, len(missing))
	for i, m := range missing {
		env[i] = "TASK_TIMER_SERVER_" + strings.ToUpper(m)
	}
	return fmt.Errorf(
		"task-timer-server is not configured: missing %s. Set them in %s, in the "+
			"environment as %s, or as systemd credentials",
		strings.Join(missing, ", "), configPath(), strings.Join(env, ", "),
	)
}

// DomainAllowed reports whether an email may register. An empty allow-list means
// "anyone who can consent to our Atlassian app", which is already bounded.
func (s Settings) DomainAllowed(email string) bool {
	if len(s.AllowedEmailDomains) == 0 {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range s.AllowedEmailDomains {
		if d == domain {
			return true
		}
	}
	return false
}

func normalizeDomains(in []string) []string {
	out := make([]string, 0, len(in))
	for _, d := range in {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			out = append(out, d)
		}
	}
	return out
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
