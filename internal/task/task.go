// Package task holds the domain model and the SQLite store shared by the
// desktop app and the daemon. Both binaries depend on this package so the
// schema is defined exactly once.
package task

import (
	"time"
)

// Status is the lifecycle state of a logged work session.
type Status string

const (
	// StatusLogged is a completed local session that has not been pushed upstream.
	StatusLogged Status = "Work Logged"
	// StatusPushing marks a session currently being pushed to a provider.
	StatusPushing Status = "Pushing"
	// StatusPushed is a session pushed upstream whose task is ongoing.
	StatusPushed Status = "Pushed"
	// StatusPushedComplete is a session pushed upstream whose task is done.
	StatusPushedComplete Status = "Pushed — Complete"
	// StatusInProgress is a session whose timer is still running.
	StatusInProgress Status = "In Progress"
)

// SourceUserAdded marks tasks typed in by the user rather than pulled from a
// provider. Any other value is the name of the provider that supplied the task.
const SourceUserAdded = "User-Added"

// CommentCompleted is the sentinel stored in the comment column to mark a task
// as done. It predates this package; the store keeps it for compatibility with
// databases written by earlier versions.
const CommentCompleted = "completed"

// Task is one row of the tasks table: a single timed work session.
type Task struct {
	ID         int
	Name       string
	Start      time.Time
	End        time.Time
	Duration   time.Duration
	AssignedBy string
	// Source is SourceUserAdded or the name of the provider the task came from.
	Source string
	Status Status
	// Username is the local operating-system user that recorded the session.
	Username string
	// ForeignKey is the provider's identifier for the task, e.g. a remote issue
	// key such as "ENG-1234". Empty for purely local tasks.
	ForeignKey string
	// ForeignURL is a human-openable link to the task in the provider.
	ForeignURL string
	// PushSignature is the provider's identifier for the pushed work log. It is
	// empty until the session has been successfully pushed, and is what makes
	// pushing idempotent across daemon restarts. (It maps to the on-disk column
	// timer_sync_signature, kept under that name for database compatibility.)
	PushSignature string
	Comment       string
	Instance      int
}

// Completed reports whether the session's task has been marked done.
func (t Task) Completed() bool {
	return t.Comment == CommentCompleted
}

// Remote is a task that lives in an external task tracker. Remote tasks
// are pulled by the reconcile engine and offered to the user as timer targets; they
// are not themselves work sessions.
type Remote struct {
	// Provider is the registered name of the provider that supplied the task.
	Provider string
	// Key is the provider's stable identifier, e.g. "ENG-1234".
	Key string
	// Title is the human-readable summary of the task.
	Title string
	// URL links back to the task in the provider's web UI.
	URL string
	// Status is the provider's own status string, e.g. "In Progress".
	Status string
	// AssignedBy is whoever put the task on the user's plate, when known.
	AssignedBy string
	// Done reports whether the provider considers the task closed. Providers
	// map their own status categories onto this.
	Done      bool
	UpdatedAt time.Time
}

// DisplayName is how a remote task is shown in the timer's task picker. It is
// also stored as the task name on sessions logged against the remote task, so
// it must be stable across pulls.
func (r Remote) DisplayName() string {
	if r.Title == "" {
		return r.Key
	}
	return r.Key + ": " + r.Title
}
