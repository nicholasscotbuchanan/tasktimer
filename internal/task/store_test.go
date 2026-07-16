package task

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDataDirUsesEnvironmentOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom-data")
	t.Setenv("TASK_TIMER_DATA_DIR", override)

	if got := DataDir(); got != override {
		t.Fatalf("DataDir() = %q, want %q", got, override)
	}
	if got, want := DBPath(), filepath.Join(override, "tasks.db"); got != want {
		t.Fatalf("DBPath() = %q, want %q", got, want)
	}
}

// openTestStore returns a store backed by a scratch database.
func openTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := OpenAt(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSaveAndRecent(t *testing.T) {
	store := openTestStore(t)

	start := time.Now().Add(-time.Hour)
	err := store.Save(Task{
		Name:     "write the thing",
		Start:    start,
		End:      start.Add(30 * time.Minute),
		Duration: 30 * time.Minute,
		Username: "bucky",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	tasks, err := store.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("Recent returned %d tasks, want 1", len(tasks))
	}

	got := tasks[0]
	if got.Name != "write the thing" {
		t.Errorf("Name = %q, want %q", got.Name, "write the thing")
	}
	if got.Duration != 30*time.Minute {
		t.Errorf("Duration = %s, want 30m0s", got.Duration)
	}
	// Save should default these rather than leaving them empty.
	if got.Source != SourceUserAdded {
		t.Errorf("Source = %q, want %q", got.Source, SourceUserAdded)
	}
	if got.Status != StatusLogged {
		t.Errorf("Status = %q, want %q", got.Status, StatusLogged)
	}
	if got.Instance != 1 {
		t.Errorf("Instance = %d, want 1", got.Instance)
	}
}

// A session logged against a task name that matches a pulled remote task must
// pick up the provider's key and URL, otherwise the daemon has no way to
// know where to push the time.
func TestSaveLinksSessionToRemoteTask(t *testing.T) {
	store := openTestStore(t)

	remote := Remote{
		Provider:  "gateway",
		Key:       "ENG-1234",
		Title:     "Fix the flaky test",
		URL:       "https://example.atlassian.net/browse/ENG-1234",
		UpdatedAt: time.Now(),
	}
	if err := store.UpsertRemote(remote); err != nil {
		t.Fatalf("UpsertRemote: %v", err)
	}

	if err := store.Save(Task{
		Name:     remote.DisplayName(),
		Start:    time.Now(),
		End:      time.Now().Add(time.Minute),
		Duration: time.Minute,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tasks, err := store.Recent(1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	got := tasks[0]

	if got.ForeignKey != "ENG-1234" {
		t.Errorf("ForeignKey = %q, want ENG-1234", got.ForeignKey)
	}
	if got.Source != "gateway" {
		t.Errorf("Source = %q, want gateway", got.Source)
	}
	if got.ForeignURL != remote.URL {
		t.Errorf("ForeignURL = %q, want %q", got.ForeignURL, remote.URL)
	}
}

func TestUpsertRemoteIsIdempotent(t *testing.T) {
	store := openTestStore(t)

	r := Remote{Provider: "gateway", Key: "ENG-1", Title: "First", UpdatedAt: time.Now()}
	if err := store.UpsertRemote(r); err != nil {
		t.Fatalf("UpsertRemote: %v", err)
	}

	r.Title = "Renamed"
	if err := store.UpsertRemote(r); err != nil {
		t.Fatalf("UpsertRemote (second): %v", err)
	}

	remotes, err := store.OpenRemotes()
	if err != nil {
		t.Fatalf("OpenRemotes: %v", err)
	}
	if len(remotes) != 1 {
		t.Fatalf("OpenRemotes returned %d, want 1 (upsert duplicated the row)", len(remotes))
	}
	if remotes[0].Title != "Renamed" {
		t.Errorf("Title = %q, want Renamed", remotes[0].Title)
	}
}

// A remote task the provider has closed should drop out of the picker.
func TestOpenRemotesExcludesDone(t *testing.T) {
	store := openTestStore(t)

	if err := store.UpsertRemote(Remote{Provider: "gateway", Key: "ENG-1", Done: false}); err != nil {
		t.Fatalf("UpsertRemote: %v", err)
	}
	if err := store.UpsertRemote(Remote{Provider: "gateway", Key: "ENG-2", Done: true}); err != nil {
		t.Fatalf("UpsertRemote: %v", err)
	}

	remotes, err := store.OpenRemotes()
	if err != nil {
		t.Fatalf("OpenRemotes: %v", err)
	}
	if len(remotes) != 1 || remotes[0].Key != "ENG-1" {
		t.Fatalf("OpenRemotes = %+v, want only ENG-1", remotes)
	}
}

// PendingPush is the guard against double-billing someone's tracker ticket, so it
// gets its own test: once a session carries a push signature it must never be
// handed to a provider again.
func TestPendingPushExcludesAlreadyPushed(t *testing.T) {
	store := openTestStore(t)

	if err := store.UpsertRemote(Remote{Provider: "gateway", Key: "ENG-7", Title: "Task"}); err != nil {
		t.Fatalf("UpsertRemote: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := store.Save(Task{
			Name:     "ENG-7: Task",
			Start:    time.Now(),
			End:      time.Now().Add(time.Minute),
			Duration: time.Minute,
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	// Nothing is pending until the user asks for it. This is the Push
	// button: sessions sit in the database, finished and eligible, and go nowhere
	// until someone says so.
	if pending, err := store.PendingPush("gateway"); err != nil || len(pending) != 0 {
		t.Fatalf("PendingPush before RequestPush = %d (err %v), want 0", len(pending), err)
	}

	if _, err := store.RequestPush(); err != nil {
		t.Fatalf("RequestPush: %v", err)
	}

	pending, err := store.PendingPush("gateway")
	if err != nil {
		t.Fatalf("PendingPush: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("PendingPush returned %d, want 2", len(pending))
	}

	if err := store.MarkPushed(pending[0].ID, "worklog-100", StatusPushed); err != nil {
		t.Fatalf("MarkPushed: %v", err)
	}

	pending, err = store.PendingPush("gateway")
	if err != nil {
		t.Fatalf("PendingPush: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("PendingPush returned %d after marking one pushed, want 1", len(pending))
	}
}

// A purely local task has no provider to push to and must never appear as
// pending, or the engine would try to file it against an empty issue key.
func TestPendingPushIgnoresLocalOnlyTasks(t *testing.T) {
	store := openTestStore(t)

	if err := store.Save(Task{
		Name:     "local only",
		Start:    time.Now(),
		End:      time.Now().Add(time.Minute),
		Duration: time.Minute,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	pending, err := store.PendingPush("gateway")
	if err != nil {
		t.Fatalf("PendingPush: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("PendingPush returned %d local-only tasks, want 0", len(pending))
	}
}

func TestPendingCompletions(t *testing.T) {
	store := openTestStore(t)

	if err := store.UpsertRemote(Remote{Provider: "gateway", Key: "ENG-9", Title: "Ship it"}); err != nil {
		t.Fatalf("UpsertRemote: %v", err)
	}
	if err := store.Save(Task{
		Name:     "ENG-9: Ship it",
		Start:    time.Now(),
		End:      time.Now().Add(time.Minute),
		Duration: time.Minute,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tasks, err := store.Recent(1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}

	if pending, err := store.PendingCompletions("gateway"); err != nil || len(pending) != 0 {
		t.Fatalf("PendingCompletions before completing = %v (err %v), want empty", pending, err)
	}

	if err := store.Complete(tasks[0].ID, tasks[0].Name); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Completing a task locally is still not a request to tell the provider.
	if pending, err := store.PendingCompletions("gateway"); err != nil || len(pending) != 0 {
		t.Fatalf("PendingCompletions before RequestPush = %v (err %v), want empty", pending, err)
	}

	if _, err := store.RequestPush(); err != nil {
		t.Fatalf("RequestPush: %v", err)
	}

	pending, err := store.PendingCompletions("gateway")
	if err != nil {
		t.Fatalf("PendingCompletions: %v", err)
	}
	if len(pending) != 1 || pending[0].ForeignKey != "ENG-9" {
		t.Fatalf("PendingCompletions = %+v, want one entry for ENG-9", pending)
	}

	if err := store.MarkCompletionPushed("gateway", "ENG-9"); err != nil {
		t.Fatalf("MarkCompletionPushed: %v", err)
	}
	if pending, err := store.PendingCompletions("gateway"); err != nil || len(pending) != 0 {
		t.Fatalf("PendingCompletions after push = %v (err %v), want empty", pending, err)
	}
}

func TestLastPullRoundTrips(t *testing.T) {
	store := openTestStore(t)

	if got, err := store.LastPull("gateway"); err != nil || !got.IsZero() {
		t.Fatalf("LastPull on a fresh store = %v (err %v), want zero time", got, err)
	}

	want := time.Now().Truncate(time.Second)
	if err := store.SetLastPull("gateway", want); err != nil {
		t.Fatalf("SetLastPull: %v", err)
	}

	got, err := store.LastPull("gateway")
	if err != nil {
		t.Fatalf("LastPull: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("LastPull = %v, want %v", got, want)
	}
}

func TestTotalToday(t *testing.T) {
	store := openTestStore(t)

	for _, d := range []time.Duration{30 * time.Minute, 45 * time.Minute} {
		if err := store.Save(Task{
			Name:     "task-" + d.String(),
			Start:    time.Now(),
			End:      time.Now().Add(d),
			Duration: d,
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	total, count, err := store.TotalToday()
	if err != nil {
		t.Fatalf("TotalToday: %v", err)
	}
	if total != 75*time.Minute {
		t.Errorf("total = %s, want 1h15m0s", total)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

// Databases written by earlier versions of the app are missing columns that the
// reconcile feature depends on. Opening one must migrate it rather than fail.
func TestMigrateAddsReconcileColumnsToLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	legacy, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	// Rebuild the table as it looked before reconcile existed.
	if _, err := legacy.DB().Exec(`DROP TABLE tasks;
		CREATE TABLE tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT, start_time TEXT, end_time TEXT, duration TEXT,
			assigned_by TEXT, task_source TEXT, task_status TEXT,
			username TEXT, comment TEXT
		);
		INSERT INTO tasks (name, duration) VALUES ('old task', '1h0m0s');`); err != nil {
		t.Fatalf("building legacy schema: %v", err)
	}
	legacy.Close()

	store, err := OpenAt(path)
	if err != nil {
		t.Fatalf("reopening legacy database: %v", err)
	}
	defer store.Close()

	tasks, err := store.Recent(10)
	if err != nil {
		t.Fatalf("Recent on migrated database: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Name != "old task" {
		t.Fatalf("migration lost the existing row: %+v", tasks)
	}
	if tasks[0].Duration != time.Hour {
		t.Errorf("Duration = %s, want 1h0m0s", tasks[0].Duration)
	}
}
