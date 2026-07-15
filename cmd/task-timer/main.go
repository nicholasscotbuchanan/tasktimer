// Command task-timer is the desktop timer. It records work sessions to a local
// SQLite database that the task-timer-sync daemon reconciles through the Task
// Timer backend. The app itself talks only to the local database and, via the
// gateway provider, that backend — it holds no external-tracker credentials.
//
// The interface lives in internal/ui; this file is only the wiring.
package main

import (
	"log"

	"task-timer-app/internal/task"
	"task-timer-app/internal/ui"

	// Compiled-in sync providers. Blank imports; they register themselves, and
	// the Settings page renders a form for whatever is in the registry.
	//
	// This is the only place the app names a backend. internal/ui must never
	// import one: the settings screen is built from the registry, so a provider
	// added here shows up with no change to the interface at all. Adding a
	// backend means one new package and one line — here and in the daemon.
	_ "task-timer-app/internal/sync/providers/gateway"
	_ "task-timer-app/internal/sync/providers/jsonfile"
)

// Stamped by the Makefile via -ldflags. The UI reads them back for its About
// page, which is why they are handed over rather than merely printed.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	store, err := task.Open()
	if err != nil {
		log.Fatalf("task-timer: %v", err)
	}
	defer store.Close()

	ui.Version = version
	ui.Commit = commit

	ui.New(store).Run()
}
