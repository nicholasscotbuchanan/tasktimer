package ui

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"task-timer-app/internal/task"
)

// TestRenderPages builds the whole interface on a headless Fyne app and lays
// every page out. It is a guard against the class of bug that does not fail the
// compiler and does not fail a unit test, but leaves the app dead on arrival:
// a page whose constructor dereferences a widget it has not created yet, or a
// layout that yields a non-finite MinSize and so produces a window that never
// appears.
//
// Set TASK_TIMER_SHOTS to a directory to also write a PNG of each page, which
// is how the design gets eyeballed without a window server:
//
//	TASK_TIMER_SHOTS=/tmp/shots go test ./internal/ui -run TestRenderPages
func TestRenderPages(t *testing.T) {
	store := seedStore(t)

	a := newWithApp(test.NewApp(), store)
	a.window.Resize(fyne.NewSize(windowWidth, windowHeight))

	// Populate the pages the way Run would, minus the goroutines.
	a.reload()

	shots := os.Getenv("TASK_TIMER_SHOTS")
	if shots != "" {
		if err := os.MkdirAll(shots, 0o755); err != nil {
			t.Fatalf("creating shot directory: %v", err)
		}
	}

	for i, entry := range a.nav {
		a.selectPage(i)

		// The window cannot be narrower than its content's MinSize — Fyne will
		// force it wider. If a page demands more than the design width, that is
		// not a harmless detail: at the rendered size its widgets overlap, and
		// in the real app the user gets a window they cannot shrink. Inter sets
		// wider digits than Fyne's stock face, and this is what catches the
		// timer growing into the buttons beside it.
		if min := a.window.Content().MinSize(); min.Width > windowWidth {
			t.Errorf("page %q needs %.0fpx of width but the window is %.0fpx; "+
				"something on it does not fit", entry.title, min.Width, windowWidth)
		}

		canvas := a.window.Canvas()
		img := canvas.Capture()
		if img == nil {
			t.Fatalf("page %q captured no image", entry.title)
		}

		bounds := img.Bounds()
		if bounds.Dx() == 0 || bounds.Dy() == 0 {
			t.Fatalf("page %q rendered to a zero-sized image; a layout is reporting a bad MinSize",
				entry.title)
		}

		if shots == "" {
			continue
		}

		name := filepath.Join(shots, entry.title+".png")
		f, err := os.Create(name)
		if err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatalf("encoding %s: %v", name, err)
		}
		f.Close()
		t.Logf("wrote %s (%dx%d)", name, bounds.Dx(), bounds.Dy())
	}
}

// TestTimerLifecycle drives the timer the way the buttons do and checks the
// store ends up with what the user actually worked.
func TestTimerLifecycle(t *testing.T) {
	store := seedStore(t)

	a := newWithApp(test.NewApp(), store)
	a.reload()

	before, _, err := store.TotalToday()
	if err != nil {
		t.Fatalf("TotalToday: %v", err)
	}

	a.dashboard.setTaskName("Lifecycle probe")
	a.Start("Lifecycle probe")
	if !a.running {
		t.Fatal("Start did not start the timer")
	}

	// Lap banks the elapsed time and keeps the clock running.
	time.Sleep(20 * time.Millisecond)
	a.Lap()
	if !a.running {
		t.Fatal("Lap stopped the timer; it must keep running")
	}

	time.Sleep(20 * time.Millisecond)
	a.Stop()
	if a.running {
		t.Fatal("Stop left the timer running")
	}

	after, _, err := store.TotalToday()
	if err != nil {
		t.Fatalf("TotalToday: %v", err)
	}
	if after <= before {
		t.Fatalf("today's total did not grow: before=%s after=%s", before, after)
	}

	// Both the lap and the stop must have been written, not just the stop.
	sessions, err := store.Recent(2)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	for _, s := range sessions {
		if s.Name != "Lifecycle probe" {
			t.Fatalf("expected two 'Lifecycle probe' sessions, found %q", s.Name)
		}
	}
}

// seedStore builds a throwaway database with enough in it that the pages have
// something to lay out.
func seedStore(t *testing.T) *task.Store {
	t.Helper()

	// The Settings and About pages read task.DataDir() directly, and
	// sync.LoadConfig *writes* an example config when none exists. Without this
	// the test would reach into the developer's real application-support
	// directory and leave a file behind.
	dir := t.TempDir()
	t.Setenv("TASK_TIMER_DATA_DIR", dir)

	store, err := task.OpenAt(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	now := time.Now()
	sessions := []task.Task{
		{Name: "Test", Start: now.Add(-2 * time.Hour), End: now.Add(-2 * time.Hour).Add(5 * time.Second),
			Duration: 5 * time.Second, AssignedBy: "nbuchanan", Source: task.SourceUserAdded,
			Status: task.StatusLogged},
		{Name: "ENG-1421: Rework the sync engine's retry backoff",
			Start: now.Add(-5 * time.Hour), End: now.Add(-4 * time.Hour),
			Duration: time.Hour, AssignedBy: "a.mcallister", Source: "gateway",
			Status: task.StatusSyncedProgress, ForeignKey: "ENG-1421"},
		{Name: "ENG-1402: Ship the DMG signing pipeline",
			Start: now.AddDate(0, 0, -1), End: now.AddDate(0, 0, -1).Add(2 * time.Hour),
			Duration: 2 * time.Hour, AssignedBy: "r.okafor", Source: "gateway",
			Status: task.StatusSyncedComplete, ForeignKey: "ENG-1402",
			Comment: task.CommentCompleted},
	}
	for _, s := range sessions {
		if err := store.Save(s); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	if err := store.UpsertRemote(task.Remote{
		Provider: "gateway", Key: "ENG-1421", Title: "Rework the sync engine's retry backoff",
		URL: "https://example.atlassian.net/browse/ENG-1421", AssignedBy: "a.mcallister",
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertRemote: %v", err)
	}

	return store
}
