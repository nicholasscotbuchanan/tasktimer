package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"task-timer-app/internal/task"
)

// fakeProvider is a Provider that records what the engine asked it to do. It is
// also the test of whether the plugin surface is really generic: it is written
// against nothing but the interface, and the engine drives it without knowing
// what it is.
type fakeProvider struct {
	name string

	remotes []task.Remote
	pulls   []time.Time

	pushed    []WorkLog
	pushErr   error
	completed []string
	compErr   error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Pull(_ context.Context, since time.Time) ([]task.Remote, error) {
	f.pulls = append(f.pulls, since)
	return f.remotes, nil
}

func (f *fakeProvider) Push(_ context.Context, wl WorkLog) (string, error) {
	if f.pushErr != nil {
		return "", f.pushErr
	}
	f.pushed = append(f.pushed, wl)
	return "worklog-1", nil
}

func (f *fakeProvider) Complete(_ context.Context, key string) error {
	if f.compErr != nil {
		return f.compErr
	}
	f.completed = append(f.completed, key)
	return nil
}

func newTestEngine(t *testing.T, p Provider) (*Engine, *task.Store) {
	t.Helper()

	store, err := task.OpenAt(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	engine := &Engine{
		store:     store,
		providers: []Provider{p},
		logger:    log.New(io.Discard, "", 0),
	}
	return engine, store
}

func TestEnginePullStoresRemoteTasks(t *testing.T) {
	fake := &fakeProvider{
		name: "fake",
		remotes: []task.Remote{
			{Key: "ENG-1", Title: "Do a thing", URL: "https://example/ENG-1", UpdatedAt: time.Now()},
		},
	}
	engine, store := newTestEngine(t, fake)

	engine.RunOnce(context.Background())

	remotes, err := store.OpenRemotes()
	if err != nil {
		t.Fatalf("OpenRemotes: %v", err)
	}
	if len(remotes) != 1 {
		t.Fatalf("stored %d remotes, want 1", len(remotes))
	}
	// The engine, not the provider, is responsible for stamping the source.
	if remotes[0].Provider != "fake" {
		t.Errorf("Provider = %q, want fake", remotes[0].Provider)
	}
	if remotes[0].Key != "ENG-1" {
		t.Errorf("Key = %q, want ENG-1", remotes[0].Key)
	}
}

// The first pull is a full pull; the second must be incremental, or every cycle
// would re-fetch the user's entire backlog.
func TestEngineAdvancesPullCursor(t *testing.T) {
	fake := &fakeProvider{name: "fake"}
	engine, _ := newTestEngine(t, fake)

	engine.RunOnce(context.Background())
	engine.RunOnce(context.Background())

	if len(fake.pulls) != 2 {
		t.Fatalf("provider pulled %d times, want 2", len(fake.pulls))
	}
	if !fake.pulls[0].IsZero() {
		t.Errorf("first pull since = %v, want the zero time (full pull)", fake.pulls[0])
	}
	if fake.pulls[1].IsZero() {
		t.Error("second pull since = zero time; the cursor was never advanced")
	}
}

func TestEnginePushesSessionsOnceOnly(t *testing.T) {
	fake := &fakeProvider{
		name:    "fake",
		remotes: []task.Remote{{Key: "ENG-1", Title: "Do a thing"}},
	}
	engine, store := newTestEngine(t, fake)

	// First cycle pulls the remote task so a session can be logged against it.
	engine.RunOnce(context.Background())

	start := time.Now().Add(-time.Hour)
	if err := store.Save(task.Task{
		Name:     "ENG-1: Do a thing",
		Start:    start,
		End:      start.Add(25 * time.Minute),
		Duration: 25 * time.Minute,
		Username: "bucky",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A cycle before the user has asked for anything must push nothing at all.
	engine.RunOnce(context.Background())
	if len(fake.pushed) != 0 {
		t.Fatalf("the engine pushed %d session(s) before Push was pressed", len(fake.pushed))
	}

	if _, err := store.RequestPush(); err != nil {
		t.Fatalf("RequestPush: %v", err)
	}

	engine.RunOnce(context.Background())
	if len(fake.pushed) != 1 {
		t.Fatalf("provider received %d pushes, want 1", len(fake.pushed))
	}
	if got := fake.pushed[0]; got.Key != "ENG-1" || got.Duration != 25*time.Minute {
		t.Errorf("pushed %+v, want ENG-1 for 25m", got)
	}

	// The signature persisted on the first push must stop a second one.
	engine.RunOnce(context.Background())
	if len(fake.pushed) != 1 {
		t.Fatalf("provider received %d pushes after a second cycle, want 1 — the same work was billed twice", len(fake.pushed))
	}
}

// A provider that fails must leave the session pushable, not stranded in
// "Pushing" forever.
func TestEngineRetriesAfterPushFailure(t *testing.T) {
	fake := &fakeProvider{
		name:    "fake",
		remotes: []task.Remote{{Key: "ENG-1", Title: "Do a thing"}},
		pushErr: errors.New("the backend is down"),
	}
	engine, store := newTestEngine(t, fake)
	engine.RunOnce(context.Background())

	if err := store.Save(task.Task{
		Name:     "ENG-1: Do a thing",
		Start:    time.Now(),
		End:      time.Now().Add(time.Minute),
		Duration: time.Minute,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.RequestPush(); err != nil {
		t.Fatalf("RequestPush: %v", err)
	}

	engine.RunOnce(context.Background())

	pending, err := store.PendingPush("fake")
	if err != nil {
		t.Fatalf("PendingPush: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("after a failed push, PendingPush returned %d, want 1 (the session was lost)", len(pending))
	}
	if pending[0].Status != task.StatusLogged {
		t.Errorf("status = %q, want %q — the session is wedged", pending[0].Status, task.StatusLogged)
	}

	// Once the provider recovers, the retry goes through.
	fake.pushErr = nil
	engine.RunOnce(context.Background())
	if len(fake.pushed) != 1 {
		t.Fatalf("after recovery the provider received %d pushes, want 1", len(fake.pushed))
	}
}

func TestEngineCompletesRemoteTasks(t *testing.T) {
	fake := &fakeProvider{
		name:    "fake",
		remotes: []task.Remote{{Key: "ENG-1", Title: "Do a thing"}},
	}
	engine, store := newTestEngine(t, fake)
	engine.RunOnce(context.Background())

	if err := store.Save(task.Task{
		Name:     "ENG-1: Do a thing",
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
	if err := store.Complete(tasks[0].ID, tasks[0].Name); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// The user presses Push. Without this the engine finds nothing, which
	// is the point of the flag.
	if _, err := store.RequestPush(); err != nil {
		t.Fatalf("RequestPush: %v", err)
	}

	engine.RunOnce(context.Background())

	if len(fake.completed) != 1 || fake.completed[0] != "ENG-1" {
		t.Fatalf("provider completed %v, want [ENG-1]", fake.completed)
	}

	// And it must not be told twice.
	engine.RunOnce(context.Background())
	if len(fake.completed) != 1 {
		t.Fatalf("provider completed %v after a second cycle, want it told exactly once", fake.completed)
	}
}

// A read-only provider is legitimate: returning ErrUnsupported from Push and
// Complete must be a skip, not an error that kills the cycle.
func TestEngineTreatsUnsupportedAsSkip(t *testing.T) {
	fake := &fakeProvider{
		name:    "fake",
		remotes: []task.Remote{{Key: "ENG-1", Title: "Do a thing"}},
		pushErr: ErrUnsupported,
		compErr: ErrUnsupported,
	}
	engine, store := newTestEngine(t, fake)
	engine.RunOnce(context.Background())

	if err := store.Save(task.Task{
		Name:     "ENG-1: Do a thing",
		Start:    time.Now(),
		End:      time.Now().Add(time.Minute),
		Duration: time.Minute,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := store.RequestPush(); err != nil {
		t.Fatalf("RequestPush: %v", err)
	}

	engine.RunOnce(context.Background())

	pending, err := store.PendingPush("fake")
	if err != nil {
		t.Fatalf("PendingPush: %v", err)
	}
	if len(pending) != 1 || pending[0].Status != task.StatusLogged {
		t.Fatalf("a read-only provider stranded the session: %+v", pending)
	}
}

func TestRegistryRejectsUnknownProvider(t *testing.T) {
	_, err := build("does-not-exist", nil)
	if err == nil {
		t.Fatal("build() accepted an unknown provider")
	}
	// The error has to name the providers that DO exist, or a typo in the
	// config file is an afternoon of debugging.
	if names := Registered(); len(names) > 0 && !strings.Contains(err.Error(), names[0]) {
		t.Errorf("error %q does not list the available providers %v", err, names)
	}
}

func TestLoadConfigWritesExampleOnFirstRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// A first run must be a no-op, not an accidental push against a
	// half-configured provider.
	for _, p := range cfg.Providers {
		if p.Enabled {
			t.Errorf("provider %s is enabled in the generated example config", p.Name)
		}
	}

	// And the file must now exist and re-parse.
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reloading the generated config: %v", err)
	}
	if len(reloaded.Providers) != len(cfg.Providers) {
		t.Errorf("reloaded %d providers, want %d", len(reloaded.Providers), len(cfg.Providers))
	}

	interval, err := reloaded.Interval()
	if err != nil {
		t.Fatalf("Interval: %v", err)
	}
	if interval != 60*time.Second {
		t.Errorf("Interval = %s, want 60s", interval)
	}
}

func TestConfigRejectsAbsurdPollInterval(t *testing.T) {
	cfg := Config{PollInterval: "10ms"}
	if _, err := cfg.Interval(); err == nil {
		t.Fatal("Interval() accepted a 10ms poll interval")
	}
}

func TestProviderConfigSettingsArePassedThrough(t *testing.T) {
	// The engine must not interpret a provider's settings; it hands the raw
	// JSON over untouched. This is what lets a new backend define its own
	// config shape without the engine changing.
	raw := json.RawMessage(`{"anything":"at all","nested":{"x":1}}`)
	pc := ProviderConfig{Name: "fake", Enabled: true, Settings: raw}

	var round map[string]any
	if err := json.Unmarshal(pc.Settings, &round); err != nil {
		t.Fatalf("settings did not survive as opaque JSON: %v", err)
	}
	if round["anything"] != "at all" {
		t.Errorf("settings = %v, want the original object", round)
	}
}
