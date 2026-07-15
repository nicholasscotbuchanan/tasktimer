// Package store is the gateway's persistence layer.
//
// Six things live here and nothing else does: who a user is, the Atlassian
// tokens held on their behalf, the bearer keys their clients authenticate with,
// the two short-lived rows of an in-flight login, and a record of which work
// logs have already been pushed.
//
// That last table earns its keep. The desktop client is the system of record for
// local timing and it retries; without a dedup key, a retry after a response we
// never saw would log the same session to Jira twice, and Jira will happily
// accept it.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrDuplicate is returned by InsertPushedWorklog when the (user, idempotency
// key) pair already exists — the unique constraint catching a retry that raced
// past the caller's SELECT.
var ErrDuplicate = errors.New("store: duplicate")

// Store wraps the database handle and the resolved schema.
type Store struct {
	db *sql.DB
}

// Open resolves a SQLAlchemy-style database URL, opens the database, applies the
// pragmas, and creates the schema if it is absent.
//
// Only sqlite is supported, in two forms: `sqlite:////abs/path` for a file and
// `sqlite://` (empty) for a private in-memory database, which is what the tests
// use.
func Open(databaseURL string) (*Store, error) {
	dsn, memory, err := sqliteDSN(databaseURL)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// One connection. This is a single low-traffic process; serialising database
	// access sidesteps SQLite's writer-locking entirely and keeps an in-memory
	// database (which is per-connection) coherent for the test suite.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}
	if !memory {
		if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000`); err != nil {
			db.Close()
			return nil, fmt.Errorf("setting pragmas: %w", err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// sqliteDSN turns a `sqlite://...` URL into a modernc.org/sqlite DSN and reports
// whether it is an in-memory database.
func sqliteDSN(databaseURL string) (dsn string, memory bool, err error) {
	if databaseURL == "" || databaseURL == "sqlite://" || databaseURL == "sqlite:///:memory:" {
		return ":memory:", true, nil
	}
	if !strings.HasPrefix(databaseURL, "sqlite:") {
		return "", false, fmt.Errorf("store: only sqlite database URLs are supported, got %q", databaseURL)
	}

	// SQLAlchemy convention: sqlite:////abs -> "/abs" (four slashes), and
	// sqlite:///rel -> "rel". Parse off the scheme and keep whatever path remains.
	rest := strings.TrimPrefix(databaseURL, "sqlite://")
	// rest now looks like "/abs/path" (from four slashes) or "rel/path".
	path := rest
	if strings.HasPrefix(rest, "/") {
		// sqlite:////abs -> rest == "//abs"? No: TrimPrefix removed two slashes,
		// leaving "//abs/..". Collapse the leading pair to one.
		path = "/" + strings.TrimLeft(rest, "/")
	}
	if path == "" || path == "/" {
		return "", false, fmt.Errorf("store: no database path in %q", databaseURL)
	}

	// Best-effort: ensure the parent directory exists, exactly as the old engine
	// did, so a fresh install does not fail on first boot for want of /var/lib.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o750)
	}

	// file: URI form lets modernc apply pragmas per connection reliably.
	u := &url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	return u.String(), false, nil
}

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    atlassian_account_id TEXT NOT NULL UNIQUE,
    email                TEXT NOT NULL,
    display_name         TEXT NOT NULL DEFAULT '',
    created_at           TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_users_email ON users(email);

CREATE TABLE IF NOT EXISTS jira_tokens (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id           INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    access_token_enc  TEXT NOT NULL,
    refresh_token_enc TEXT NOT NULL,
    expires_at        TEXT NOT NULL,
    cloud_id          TEXT NOT NULL,
    site_url          TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS api_keys (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_hash     TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    label        TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT
);
CREATE INDEX IF NOT EXISTS ix_api_keys_user ON api_keys(user_id);

CREATE TABLE IF NOT EXISTS pending_auth (
    state               TEXT PRIMARY KEY,
    code_challenge      TEXT NOT NULL,
    client_redirect_uri TEXT NOT NULL,
    client_state        TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_codes (
    code           TEXT PRIMARY KEY,
    user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_challenge TEXT NOT NULL,
    expires_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS pushed_worklogs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    issue_key       TEXT NOT NULL,
    jira_worklog_id TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    UNIQUE(user_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS ix_pushed_user ON pushed_worklogs(user_id);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("creating schema: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Row types
// ---------------------------------------------------------------------------

// User is a person, identified by their Atlassian account. There is no password
// and no local registration form; the row is created the first time someone
// completes the Atlassian consent flow.
type User struct {
	ID                 int64
	AtlassianAccountID string
	Email              string
	DisplayName        string
	CreatedAt          time.Time
}

// JiraToken is the Atlassian OAuth grant held for one user. Both tokens are
// stored encrypted. Atlassian rotates the refresh token on every refresh, so a
// failure to persist the new value locks the user out permanently.
type JiraToken struct {
	UserID          int64
	AccessTokenEnc  string
	RefreshTokenEnc string
	ExpiresAt       time.Time
	CloudID         string
	SiteURL         string
}

// APIKey is a bearer key one desktop client authenticates with. Only the
// SHA-256 hash is kept.
type APIKey struct {
	ID        int64
	UserID    int64
	KeyHash   string
	Prefix    string
	Label     string
	RevokedAt *time.Time
}

// PendingAuth is one in-flight login, from /auth/login until /auth/callback.
type PendingAuth struct {
	State             string
	CodeChallenge     string
	ClientRedirectURI string
	ClientState       string
	CreatedAt         time.Time
}

// AuthCode is a one-time code handed to the desktop client over the loopback
// redirect and traded for a bearer key.
type AuthCode struct {
	Code          string
	UserID        int64
	CodeChallenge string
	ExpiresAt     time.Time
}

// PushedWorklog is proof that one client-side session already reached Jira.
type PushedWorklog struct {
	UserID         int64
	IdempotencyKey string
	IssueKey       string
	JiraWorklogID  string
}

// ---------------------------------------------------------------------------
// Pending auth
// ---------------------------------------------------------------------------

// CreatePendingAuth records an in-flight login.
func (s *Store) CreatePendingAuth(p PendingAuth) error {
	_, err := s.db.Exec(
		`INSERT INTO pending_auth(state, code_challenge, client_redirect_uri, client_state, created_at)
		 VALUES(?,?,?,?,?)`,
		p.State, p.CodeChallenge, p.ClientRedirectURI, p.ClientState, fmtTime(time.Now().UTC()),
	)
	return err
}

// TakePendingAuth atomically fetches and deletes a pending login. The second
// return is false when no such row exists (a replay, or a login left past its
// TTL and finished later).
func (s *Store) TakePendingAuth(state string) (PendingAuth, bool, error) {
	var (
		p         PendingAuth
		createdAt string
	)
	err := s.db.QueryRow(
		`DELETE FROM pending_auth WHERE state=?
		 RETURNING state, code_challenge, client_redirect_uri, client_state, created_at`,
		state,
	).Scan(&p.State, &p.CodeChallenge, &p.ClientRedirectURI, &p.ClientState, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PendingAuth{}, false, nil
	}
	if err != nil {
		return PendingAuth{}, false, err
	}
	p.CreatedAt = parseTime(createdAt)
	return p, true, nil
}

// ---------------------------------------------------------------------------
// Auth codes
// ---------------------------------------------------------------------------

// CreateAuthCode stores a one-time code.
func (s *Store) CreateAuthCode(c AuthCode) error {
	_, err := s.db.Exec(
		`INSERT INTO auth_codes(code, user_id, code_challenge, expires_at) VALUES(?,?,?,?)`,
		c.Code, c.UserID, c.CodeChallenge, fmtTime(c.ExpiresAt),
	)
	return err
}

// TakeAuthCode atomically fetches and deletes a one-time code. Deleting before
// the caller's checks means a wrong verifier burns the code rather than letting
// it be brute-forced.
func (s *Store) TakeAuthCode(code string) (AuthCode, bool, error) {
	var (
		c         AuthCode
		expiresAt string
	)
	err := s.db.QueryRow(
		`DELETE FROM auth_codes WHERE code=?
		 RETURNING code, user_id, code_challenge, expires_at`,
		code,
	).Scan(&c.Code, &c.UserID, &c.CodeChallenge, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthCode{}, false, nil
	}
	if err != nil {
		return AuthCode{}, false, err
	}
	c.ExpiresAt = parseTime(expiresAt)
	return c, true, nil
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

// UserByAtlassianID looks up a user by their stable Atlassian account id.
func (s *Store) UserByAtlassianID(accountID string) (User, bool, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, atlassian_account_id, email, display_name, created_at
		 FROM users WHERE atlassian_account_id=?`, accountID))
}

// UserByID looks up a user by primary key.
func (s *Store) UserByID(id int64) (User, bool, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, atlassian_account_id, email, display_name, created_at
		 FROM users WHERE id=?`, id))
}

func (s *Store) scanUser(row *sql.Row) (User, bool, error) {
	var (
		u         User
		createdAt string
	)
	err := row.Scan(&u.ID, &u.AtlassianAccountID, &u.Email, &u.DisplayName, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	u.CreatedAt = parseTime(createdAt)
	return u, true, nil
}

// CreateUser inserts a new user and returns it with its assigned id.
func (s *Store) CreateUser(accountID, email, displayName string) (User, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO users(atlassian_account_id, email, display_name, created_at) VALUES(?,?,?,?)`,
		accountID, email, displayName, fmtTime(now),
	)
	if err != nil {
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return User{ID: id, AtlassianAccountID: accountID, Email: email, DisplayName: displayName, CreatedAt: now}, nil
}

// UpdateUserProfile refreshes the mutable identity fields. People change their
// name and their email; the account id never changes.
func (s *Store) UpdateUserProfile(id int64, email, displayName string) error {
	_, err := s.db.Exec(`UPDATE users SET email=?, display_name=? WHERE id=?`, email, displayName, id)
	return err
}

// CountUsers is used by the tests.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------------------
// Jira tokens
// ---------------------------------------------------------------------------

// GetJiraToken returns the stored grant for a user, if any.
func (s *Store) GetJiraToken(userID int64) (JiraToken, bool, error) {
	var (
		t         JiraToken
		expiresAt string
	)
	err := s.db.QueryRow(
		`SELECT user_id, access_token_enc, refresh_token_enc, expires_at, cloud_id, site_url
		 FROM jira_tokens WHERE user_id=?`, userID,
	).Scan(&t.UserID, &t.AccessTokenEnc, &t.RefreshTokenEnc, &expiresAt, &t.CloudID, &t.SiteURL)
	if errors.Is(err, sql.ErrNoRows) {
		return JiraToken{}, false, nil
	}
	if err != nil {
		return JiraToken{}, false, err
	}
	t.ExpiresAt = parseTime(expiresAt)
	return t, true, nil
}

// UpsertJiraToken writes the grant, replacing any existing one for the user.
func (s *Store) UpsertJiraToken(t JiraToken) error {
	_, err := s.db.Exec(
		`INSERT INTO jira_tokens(user_id, access_token_enc, refresh_token_enc, expires_at, cloud_id, site_url)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(user_id) DO UPDATE SET
		     access_token_enc=excluded.access_token_enc,
		     refresh_token_enc=excluded.refresh_token_enc,
		     expires_at=excluded.expires_at,
		     cloud_id=excluded.cloud_id,
		     site_url=excluded.site_url`,
		t.UserID, t.AccessTokenEnc, t.RefreshTokenEnc, fmtTime(t.ExpiresAt), t.CloudID, t.SiteURL,
	)
	return err
}

// ---------------------------------------------------------------------------
// API keys
// ---------------------------------------------------------------------------

// CreateAPIKey stores a new bearer key (its hash and public prefix).
func (s *Store) CreateAPIKey(userID int64, keyHash, prefix, label string) error {
	_, err := s.db.Exec(
		`INSERT INTO api_keys(user_id, key_hash, prefix, label, created_at) VALUES(?,?,?,?,?)`,
		userID, keyHash, prefix, label, fmtTime(time.Now().UTC()),
	)
	return err
}

// APIKeyByHash returns the key row for a hash, if present.
func (s *Store) APIKeyByHash(keyHash string) (APIKey, bool, error) {
	var (
		k         APIKey
		revokedAt sql.NullString
	)
	err := s.db.QueryRow(
		`SELECT id, user_id, key_hash, prefix, label, revoked_at FROM api_keys WHERE key_hash=?`,
		keyHash,
	).Scan(&k.ID, &k.UserID, &k.KeyHash, &k.Prefix, &k.Label, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, false, nil
	}
	if err != nil {
		return APIKey{}, false, err
	}
	if revokedAt.Valid {
		t := parseTime(revokedAt.String)
		k.RevokedAt = &t
	}
	return k, true, nil
}

// TouchAPIKey records that a key was just used.
func (s *Store) TouchAPIKey(id int64) error {
	_, err := s.db.Exec(`UPDATE api_keys SET last_used_at=? WHERE id=?`, fmtTime(time.Now().UTC()), id)
	return err
}

// ---------------------------------------------------------------------------
// Pushed work logs
// ---------------------------------------------------------------------------

// PushedWorklogByKey looks up an already-pushed session by its idempotency key.
func (s *Store) PushedWorklogByKey(userID int64, idempotencyKey string) (PushedWorklog, bool, error) {
	var w PushedWorklog
	err := s.db.QueryRow(
		`SELECT user_id, idempotency_key, issue_key, jira_worklog_id
		 FROM pushed_worklogs WHERE user_id=? AND idempotency_key=?`,
		userID, idempotencyKey,
	).Scan(&w.UserID, &w.IdempotencyKey, &w.IssueKey, &w.JiraWorklogID)
	if errors.Is(err, sql.ErrNoRows) {
		return PushedWorklog{}, false, nil
	}
	if err != nil {
		return PushedWorklog{}, false, err
	}
	return w, true, nil
}

// InsertPushedWorklog records a push. It returns ErrDuplicate when the unique
// (user, idempotency key) constraint fires — a retry that raced past the SELECT.
func (s *Store) InsertPushedWorklog(w PushedWorklog) error {
	_, err := s.db.Exec(
		`INSERT INTO pushed_worklogs(user_id, idempotency_key, issue_key, jira_worklog_id, created_at)
		 VALUES(?,?,?,?,?)`,
		w.UserID, w.IdempotencyKey, w.IssueKey, w.JiraWorklogID, fmtTime(time.Now().UTC()),
	)
	if err != nil && isUniqueViolation(err) {
		return ErrDuplicate
	}
	return err
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func fmtTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// isUniqueViolation reports whether an error is a SQLite UNIQUE constraint
// failure. modernc.org/sqlite surfaces these in the error string; matching on it
// avoids taking a dependency on the driver's error type.
func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed")
}
