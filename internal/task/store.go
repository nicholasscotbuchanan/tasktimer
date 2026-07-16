package task

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	// Registered here so every consumer of the store gets the driver.
	_ "github.com/mattn/go-sqlite3"
)

// Store is the SQLite-backed persistence layer. It is safe for concurrent use:
// the desktop app and the daemon open the same file at the same time.
type Store struct {
	db *sql.DB
}

// schema is the single definition of the database layout. It previously lived
// in two places that had already drifted apart.
const schema = `
CREATE TABLE IF NOT EXISTS tasks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT,
	start_time TEXT,
	end_time TEXT,
	duration TEXT,
	assigned_by TEXT,
	task_source TEXT,
	task_status TEXT,
	timer_app_signature TEXT,
	timer_sync_signature TEXT,
	username TEXT,
	foreign_key TEXT,
	foreign_url TEXT,
	comment TEXT,
	instance INTEGER DEFAULT 1,
	sync_requested INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS remote_tasks (
	provider TEXT NOT NULL,
	key TEXT NOT NULL,
	title TEXT,
	url TEXT,
	status TEXT,
	assigned_by TEXT,
	done INTEGER DEFAULT 0,
	updated_at TEXT,
	PRIMARY KEY (provider, key)
);

CREATE TABLE IF NOT EXISTS sync_state (
	provider TEXT PRIMARY KEY,
	last_pull TEXT
);
`

// indexes are applied after migrate, not with the schema above.
//
// idx_tasks_foreign is defined over foreign_key, a column that only exists on a
// database migrate has already been through. Creating it alongside the tables
// meant a pre-sync database failed on "no such column: foreign_key" during Open
// — before migrate ever ran to add it — so upgrading users could not start the
// app at all.
const indexes = `
CREATE INDEX IF NOT EXISTS idx_tasks_foreign ON tasks(task_source, foreign_key);
CREATE INDEX IF NOT EXISTS idx_tasks_start ON tasks(start_time);
`

// DataDir returns the platform-appropriate directory for the database and
// config file. TASK_TIMER_DATA_DIR overrides it, which is how the tests and the
// packaging scripts point the app at a scratch location.
func DataDir() string {
	if override := os.Getenv("TASK_TIMER_DATA_DIR"); override != "" {
		return override
	}

	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
			return filepath.Join(dir, "TaskTimer")
		}
		return filepath.Join(dir, "task-timer")
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		switch runtime.GOOS {
		case "windows":
			return filepath.Join(home, "AppData", "Roaming", "TaskTimer")
		case "darwin":
			return filepath.Join(home, "Library", "Application Support", "TaskTimer")
		default:
			return filepath.Join(home, ".local", "share", "task-timer")
		}
	}

	return "."
}

// DBPath returns the location of the SQLite database file.
func DBPath() string {
	return filepath.Join(DataDir(), "tasks.db")
}

// Open connects to the database at the default location, creating the data
// directory and applying the schema if needed.
func Open() (*Store, error) {
	dir := DataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating data directory %s: %w", dir, err)
	}
	return OpenAt(DBPath())
}

// OpenAt connects to the database at an explicit path. Callers are responsible
// for the parent directory existing.
func OpenAt(path string) (*Store, error) {
	// Busy timeout matters: the app and the daemon write to the same file, and
	// without it a concurrent write fails immediately rather than waiting.
	dsn := path + "?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on"

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	// Only now that every column is guaranteed to exist.
	if _, err := db.Exec(indexes); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying indexes: %w", err)
	}
	return s, nil
}

// migrate brings databases written by older versions up to the current schema.
// Columns are added one at a time because SQLite has no ADD COLUMN IF NOT
// EXISTS; an error on an existing column is expected and ignored.
func (s *Store) migrate() error {
	rows, err := s.db.Query(`PRAGMA table_info(tasks)`)
	if err != nil {
		return fmt.Errorf("inspecting tasks table: %w", err)
	}
	defer rows.Close()

	existing := map[string]bool{}
	for rows.Next() {
		var (
			cid                 int
			name, colType       string
			notNull, primaryKey int
			defaultValue        sql.NullString
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scanning tasks columns: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading tasks columns: %w", err)
	}

	wanted := []struct{ name, ddl string }{
		{"timer_app_signature", "ALTER TABLE tasks ADD COLUMN timer_app_signature TEXT"},
		{"timer_sync_signature", "ALTER TABLE tasks ADD COLUMN timer_sync_signature TEXT"},
		{"foreign_key", "ALTER TABLE tasks ADD COLUMN foreign_key TEXT"},
		{"foreign_url", "ALTER TABLE tasks ADD COLUMN foreign_url TEXT"},
		{"instance", "ALTER TABLE tasks ADD COLUMN instance INTEGER DEFAULT 1"},
		{"sync_requested", "ALTER TABLE tasks ADD COLUMN sync_requested INTEGER DEFAULT 0"},
	}
	for _, col := range wanted {
		if existing[col.name] {
			continue
		}
		if _, err := s.db.Exec(col.ddl); err != nil {
			return fmt.Errorf("adding column %s: %w", col.name, err)
		}
	}

	// Older databases stored the push lifecycle under different, "sync"-worded
	// status values. Bring existing rows onto the current wording so the table
	// never shows two vocabularies at once. Idempotent: once migrated, the WHERE
	// clauses match nothing.
	for _, r := range []struct{ from, to string }{
		{"Pushing", string(StatusPushing)},
		{"Synchronized Progress", string(StatusPushed)},
		{"Pushed — Complete", string(StatusPushedComplete)},
	} {
		if _, err := s.db.Exec(`UPDATE tasks SET task_status = ? WHERE task_status = ?`, r.to, r.from); err != nil {
			return fmt.Errorf("migrating status %q: %w", r.from, err)
		}
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB exposes the underlying handle for callers that need a query this store
// does not wrap.
func (s *Store) DB() *sql.DB { return s.db }

const taskColumns = `id, name, start_time, end_time, duration, assigned_by, task_source,
	task_status, timer_sync_signature, username, foreign_key, foreign_url, comment, instance`

// scanTasks materialises task rows, tolerating the NULLs that older databases
// contain in almost every column.
func scanTasks(rows *sql.Rows) ([]Task, error) {
	var out []Task
	for rows.Next() {
		var (
			t                                                Task
			start, end, duration, status, signature, comment sql.NullString
			name, assignedBy, source, username, fKey, fURL   sql.NullString
			instance                                         sql.NullInt64
		)
		if err := rows.Scan(&t.ID, &name, &start, &end, &duration, &assignedBy, &source,
			&status, &signature, &username, &fKey, &fURL, &comment, &instance); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}

		t.Name = name.String
		t.Start = parseTime(start.String)
		t.End = parseTime(end.String)
		t.Duration = parseDuration(duration.String)
		t.AssignedBy = assignedBy.String
		t.Source = source.String
		t.Status = Status(status.String)
		t.PushSignature = signature.String
		t.Username = username.String
		t.ForeignKey = fKey.String
		t.ForeignURL = fURL.String
		t.Comment = comment.String
		t.Instance = int(instance.Int64)
		if t.Instance == 0 {
			t.Instance = 1
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// parseTime is lenient because the database has accumulated timestamps in more
// than one layout over the app's life. An unparseable value yields the zero
// time rather than crashing the caller, which is what the previous code did.
func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "01/02/2006 03:04:05 PM"} {
		if parsed, err := time.Parse(layout, v); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func parseDuration(v string) time.Duration {
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0
	}
	return d
}

// Recent returns the most recent sessions, newest first.
func (s *Store) Recent(limit int) ([]Task, error) {
	rows, err := s.db.Query(`SELECT `+taskColumns+` FROM tasks ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("querying recent tasks: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// Between returns sessions that started within [from, to), newest first. It
// backs the Reports page, which aggregates the range in memory — see stats.go.
func (s *Store) Between(from, to time.Time) ([]Task, error) {
	rows, err := s.db.Query(`SELECT `+taskColumns+` FROM tasks
		WHERE start_time >= ? AND start_time < ? ORDER BY id DESC`,
		from.Format(time.RFC3339), to.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("querying tasks between %s and %s: %w",
			from.Format(time.DateOnly), to.Format(time.DateOnly), err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// MostRecentName returns the name of the last task worked on, or "" if the
// database is empty.
func (s *Store) MostRecentName() (string, error) {
	var name sql.NullString
	err := s.db.QueryRow(`SELECT name FROM tasks ORDER BY id DESC LIMIT 1`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("querying most recent task: %w", err)
	}
	return name.String, nil
}

// OpenNames returns distinct names of tasks that are not marked completed, for
// the timer's autocomplete.
func (s *Store) OpenNames() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT name FROM tasks
		WHERE name <> '' AND (comment IS NULL OR comment <> ?) ORDER BY name`, CommentCompleted)
	if err != nil {
		return nil, fmt.Errorf("querying open task names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning task name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// Save records a completed work session. When the task name matches a known
// remote task, the provider's key and URL are attached so the daemon can
// push the session upstream.
func (s *Store) Save(t Task) error {
	if t.Source == "" {
		t.Source = SourceUserAdded
	}
	if t.Status == "" {
		t.Status = StatusLogged
	}
	if t.Instance == 0 {
		t.Instance = 1
	}

	// A local task typed in by hand may still name a remote task, either
	// because the user picked it from the dropdown or typed the key. Link it.
	if t.ForeignKey == "" {
		if remote, err := s.RemoteByDisplayName(t.Name); err == nil && remote != nil {
			t.Source = remote.Provider
			t.ForeignKey = remote.Key
			t.ForeignURL = remote.URL
		}
	}

	_, err := s.db.Exec(`INSERT INTO tasks
		(name, start_time, end_time, duration, assigned_by, task_source, task_status,
		 username, foreign_key, foreign_url, comment, instance)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.Start.Format(time.RFC3339), t.End.Format(time.RFC3339), t.Duration.String(),
		t.AssignedBy, t.Source, string(t.Status), t.Username, t.ForeignKey, t.ForeignURL,
		t.Comment, t.Instance)
	if err != nil {
		return fmt.Errorf("saving task %q: %w", t.Name, err)
	}
	return nil
}

// DurationsToday totals time spent per task name since midnight, excluding
// completed tasks.
func (s *Store) DurationsToday() (map[string]time.Duration, error) {
	rows, err := s.db.Query(`SELECT name, duration FROM tasks
		WHERE start_time >= ? AND (comment IS NULL OR comment <> ?)`,
		startOfDay().Format(time.RFC3339), CommentCompleted)
	if err != nil {
		return nil, fmt.Errorf("querying today's durations: %w", err)
	}
	defer rows.Close()

	totals := map[string]time.Duration{}
	for rows.Next() {
		var name, duration sql.NullString
		if err := rows.Scan(&name, &duration); err != nil {
			return nil, fmt.Errorf("scanning duration: %w", err)
		}
		totals[name.String] += parseDuration(duration.String)
	}
	return totals, rows.Err()
}

// TotalToday returns the summed duration and the number of distinct tasks
// worked on since midnight, including completed ones.
func (s *Store) TotalToday() (time.Duration, int, error) {
	rows, err := s.db.Query(`SELECT name, duration FROM tasks WHERE start_time >= ?`,
		startOfDay().Format(time.RFC3339))
	if err != nil {
		return 0, 0, fmt.Errorf("querying today's total: %w", err)
	}
	defer rows.Close()

	var total time.Duration
	names := map[string]bool{}
	for rows.Next() {
		var name, duration sql.NullString
		if err := rows.Scan(&name, &duration); err != nil {
			return 0, 0, fmt.Errorf("scanning total: %w", err)
		}
		total += parseDuration(duration.String)
		names[name.String] = true
	}
	return total, len(names), rows.Err()
}

func startOfDay() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

// Complete marks every session of a task up to the given id as done.
func (s *Store) Complete(id int, name string) error {
	_, err := s.db.Exec(`UPDATE tasks SET comment = ? WHERE id <= ? AND name = ?`,
		CommentCompleted, id, name)
	if err != nil {
		return fmt.Errorf("completing task %q: %w", name, err)
	}
	return nil
}

// Reopen clears the completed marker on a single session.
func (s *Store) Reopen(id int) error {
	if _, err := s.db.Exec(`UPDATE tasks SET comment = ? WHERE id = ?`, "re-opened", id); err != nil {
		return fmt.Errorf("reopening task %d: %w", id, err)
	}
	return nil
}

// UpsertRemote stores a task pulled from a provider, overwriting any previous
// copy of the same (provider, key) pair.
func (s *Store) UpsertRemote(r Remote) error {
	done := 0
	if r.Done {
		done = 1
	}
	_, err := s.db.Exec(`INSERT INTO remote_tasks
		(provider, key, title, url, status, assigned_by, done, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, key) DO UPDATE SET
			title = excluded.title,
			url = excluded.url,
			status = excluded.status,
			assigned_by = excluded.assigned_by,
			done = excluded.done,
			updated_at = excluded.updated_at`,
		r.Provider, r.Key, r.Title, r.URL, r.Status, r.AssignedBy, done,
		r.UpdatedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("upserting remote task %s/%s: %w", r.Provider, r.Key, err)
	}
	return nil
}

const remoteColumns = `provider, key, title, url, status, assigned_by, done, updated_at`

func scanRemotes(rows *sql.Rows) ([]Remote, error) {
	var out []Remote
	for rows.Next() {
		var (
			r         Remote
			title     sql.NullString
			url       sql.NullString
			status    sql.NullString
			assigned  sql.NullString
			updatedAt sql.NullString
			done      sql.NullInt64
		)
		if err := rows.Scan(&r.Provider, &r.Key, &title, &url, &status, &assigned, &done, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning remote task: %w", err)
		}
		r.Title = title.String
		r.URL = url.String
		r.Status = status.String
		r.AssignedBy = assigned.String
		r.Done = done.Int64 == 1
		r.UpdatedAt = parseTime(updatedAt.String)
		out = append(out, r)
	}
	return out, rows.Err()
}

// OpenRemotes returns remote tasks the provider has not marked done.
func (s *Store) OpenRemotes() ([]Remote, error) {
	rows, err := s.db.Query(`SELECT ` + remoteColumns + ` FROM remote_tasks
		WHERE done = 0 ORDER BY provider, key`)
	if err != nil {
		return nil, fmt.Errorf("querying remote tasks: %w", err)
	}
	defer rows.Close()
	return scanRemotes(rows)
}

// RemoteByDisplayName finds a remote task by the name shown in the picker, so a
// session logged against it can be linked back to the provider. It returns
// (nil, nil) when the name is not a remote task.
func (s *Store) RemoteByDisplayName(name string) (*Remote, error) {
	remotes, err := s.OpenRemotes()
	if err != nil {
		return nil, err
	}
	for _, r := range remotes {
		if r.DisplayName() == name || r.Key == name {
			match := r
			return &match, nil
		}
	}
	return nil, nil
}

// pushEligible is the shape of a session that could be pushed: finished, linked
// to a remote task, and not already carrying a work-log id from a previous push.
//
// It is deliberately separate from the sync_requested check below. Eligibility is
// a property of the session; being *requested* is a decision the user made. The
// Push button turns the first into the second.
const pushEligible = `
	  AND foreign_key IS NOT NULL AND foreign_key <> ''
	  AND (timer_sync_signature IS NULL OR timer_sync_signature = '')
	  AND end_time IS NOT NULL AND end_time <> ''`

// RequestPush marks every eligible session as ready to reconcile and reports
// how many were marked. This is what the Push button calls.
//
// It does not push anything. The reconcile loop scans for these rows on its own
// schedule and does the work; this only tells it there is work to find.
func (s *Store) RequestPush() (int, error) {
	res, err := s.db.Exec(`UPDATE tasks SET sync_requested = 1
		WHERE sync_requested = 0` + pushEligible)
	if err != nil {
		return 0, fmt.Errorf("marking sessions for push: %w", err)
	}

	// Completions are a separate signal from work logs: a task the user finished
	// but never timed has no pushable session, and would otherwise never be
	// requested by the clause above.
	if _, err := s.db.Exec(`UPDATE tasks SET sync_requested = 1
		WHERE sync_requested = 0
		  AND foreign_key IS NOT NULL AND foreign_key <> ''
		  AND comment = ?
		  AND task_status <> ?`, CommentCompleted, string(StatusPushedComplete)); err != nil {
		return 0, fmt.Errorf("marking completions for push: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // the update landed; only the count is unavailable
	}
	return int(n), nil
}

// PendingPush returns sessions the user has asked to reconcile: eligible
// sessions (see pushEligible) that have been marked by RequestPush.
//
// The empty PushSignature check is what makes the push idempotent — a daemon
// that crashes after pushing but before marking will re-push, so providers are
// expected to tolerate that, but a daemon that restarts cleanly will not.
//
// The sync_requested clause is what makes reconciliation explicit. Without it
// the thread would push a session the moment the timer stopped; with it, nothing
// leaves this machine until the user presses Push.
func (s *Store) PendingPush(provider string) ([]Task, error) {
	rows, err := s.db.Query(`SELECT `+taskColumns+` FROM tasks
		WHERE task_source = ?
		  AND sync_requested = 1`+pushEligible+`
		ORDER BY id ASC`, provider)
	if err != nil {
		return nil, fmt.Errorf("querying pending pushes for %s: %w", provider, err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// MarkPushed records the provider's work-log id against a session and advances
// its status, so it is never pushed twice.
func (s *Store) MarkPushed(id int, signature string, status Status) error {
	_, err := s.db.Exec(`UPDATE tasks SET timer_sync_signature = ?, task_status = ? WHERE id = ?`,
		signature, string(status), id)
	if err != nil {
		return fmt.Errorf("marking task %d pushed: %w", id, err)
	}
	return nil
}

// SetStatus updates the push status of a session.
func (s *Store) SetStatus(id int, status Status) error {
	if _, err := s.db.Exec(`UPDATE tasks SET task_status = ? WHERE id = ?`, string(status), id); err != nil {
		return fmt.Errorf("setting status on task %d: %w", id, err)
	}
	return nil
}

// PendingCompletions returns remote-linked tasks the user has marked completed
// locally, has asked to reconcile, and whose provider has not yet been told.
func (s *Store) PendingCompletions(provider string) ([]Task, error) {
	rows, err := s.db.Query(`SELECT `+taskColumns+` FROM tasks
		WHERE task_source = ?
		  AND sync_requested = 1
		  AND foreign_key IS NOT NULL AND foreign_key <> ''
		  AND comment = ?
		  AND task_status <> ?
		GROUP BY foreign_key
		ORDER BY id ASC`, provider, CommentCompleted, string(StatusPushedComplete))
	if err != nil {
		return nil, fmt.Errorf("querying pending completions for %s: %w", provider, err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// MarkCompletionPushed records that the provider has been told the task is done.
func (s *Store) MarkCompletionPushed(provider, foreignKey string) error {
	_, err := s.db.Exec(`UPDATE tasks SET task_status = ?
		WHERE task_source = ? AND foreign_key = ? AND comment = ?`,
		string(StatusPushedComplete), provider, foreignKey, CommentCompleted)
	if err != nil {
		return fmt.Errorf("marking completion pushed for %s: %w", foreignKey, err)
	}
	return nil
}

// LastPull returns the cursor for a provider's incremental pull. A zero time
// means the provider has never been pulled.
func (s *Store) LastPull(provider string) (time.Time, error) {
	var v sql.NullString
	err := s.db.QueryRow(`SELECT last_pull FROM sync_state WHERE provider = ?`, provider).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("reading pull cursor for %s: %w", provider, err)
	}
	return parseTime(v.String), nil
}

// SetLastPull advances a provider's pull cursor.
func (s *Store) SetLastPull(provider string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO sync_state (provider, last_pull) VALUES (?, ?)
		ON CONFLICT(provider) DO UPDATE SET last_pull = excluded.last_pull`,
		provider, at.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("writing pull cursor for %s: %w", provider, err)
	}
	return nil
}
