// Package jsonfile implements a file-drop sync provider: it reads task
// definitions from a directory and writes work logs back as JSON.
//
// It replaces the app's original hardcoded "dump JSON into a temp dir" loop,
// and it exists for two reasons. It is genuinely useful — it is the escape
// hatch for anyone whose task tracker has no API but can be scripted — and it
// is the proof that the Provider interface is not secretly shaped around any one
// backend.
package jsonfile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tsync "task-timer-app/internal/sync"
	"task-timer-app/internal/task"
)

// ProviderName is the value stored in the task_source column.
const ProviderName = "jsonfile"

func init() {
	tsync.Register(tsync.Registration{
		Name:    ProviderName,
		Title:   "JSON File",
		Summary: "Exchange tasks and work logs through a directory of JSON files.",
		New:     New,
		Fields:  Fields(),
	})
}

// Fields declares the settings a user may edit, so the desktop app can render a
// form for this provider without importing it.
func Fields() []tsync.Field {
	return []tsync.Field{
		{
			Key:         "dir",
			Label:       "Directory",
			Hint:        "Where the provider reads tasks from and writes work logs to.",
			Kind:        tsync.KindText,
			Placeholder: "(defaults to a sync directory beside the database)",
			Default:     "",
		},
	}
}

// Config is the jsonfile provider's settings block.
type Config struct {
	// Dir is the root of the exchange directory. It defaults to a "sync"
	// directory beside the database.
	Dir string `json:"dir"`
}

// Provider exchanges tasks and work logs through a directory tree:
//
//	<dir>/tasks/*.json      read  — task definitions to pull
//	<dir>/worklogs/*.json   write — one file per synced session
//	<dir>/completed/*.json  write — one file per completed task
type Provider struct {
	dir string
}

// New builds a jsonfile provider from its config block.
func New(raw json.RawMessage) (tsync.Provider, error) {
	var cfg Config
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parsing jsonfile settings: %w", err)
		}
	}

	dir := cfg.Dir
	if dir == "" {
		dir = filepath.Join(task.DataDir(), "sync")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating jsonfile sync directory %s: %w", dir, err)
	}

	return &Provider{dir: dir}, nil
}

// Name identifies the provider.
func (p *Provider) Name() string { return ProviderName }

// remoteFile is the on-disk shape of a task definition.
type remoteFile struct {
	Key        string `json:"key"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Status     string `json:"status"`
	AssignedBy string `json:"assigned_by"`
	Done       bool   `json:"done"`
	UpdatedAt  string `json:"updated_at"`
}

// Pull reads every task definition in <dir>/tasks. A missing directory is not
// an error: it means nobody has dropped any tasks in yet.
func (p *Provider) Pull(ctx context.Context, since time.Time) ([]task.Remote, error) {
	dir := filepath.Join(p.dir, "tasks")

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	var remotes []task.Remote
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		var rf remoteFile
		if err := json.Unmarshal(data, &rf); err != nil {
			// One malformed file should not stall the whole sync; the rest of
			// the directory is still perfectly good.
			continue
		}
		if rf.Key == "" {
			continue
		}

		updated, err := time.Parse(time.RFC3339, rf.UpdatedAt)
		if err != nil {
			// Fall back to the file's own mtime, which is what a script
			// dropping these files would naturally leave behind.
			if info, statErr := entry.Info(); statErr == nil {
				updated = info.ModTime()
			}
		}
		if !since.IsZero() && updated.Before(since) {
			continue
		}

		remotes = append(remotes, task.Remote{
			Key:        rf.Key,
			Title:      rf.Title,
			URL:        rf.URL,
			Status:     rf.Status,
			AssignedBy: rf.AssignedBy,
			Done:       rf.Done,
			UpdatedAt:  updated,
		})
	}
	return remotes, nil
}

// workLogFile is the on-disk shape of a pushed work log.
type workLogFile struct {
	Key      string `json:"key"`
	Started  string `json:"started"`
	Seconds  int64  `json:"duration_seconds"`
	Duration string `json:"duration"`
	Comment  string `json:"comment,omitempty"`
	Author   string `json:"author"`
}

// Push writes a work log as JSON and returns its filename as the signature.
func (p *Provider) Push(ctx context.Context, wl tsync.WorkLog) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	dir := filepath.Join(p.dir, "worklogs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}

	// The name is derived from key and start time so a re-push of the same
	// session overwrites rather than duplicating.
	name := fmt.Sprintf("%s_%s.json", sanitize(wl.Key), wl.Started.UTC().Format("20060102T150405Z"))
	path := filepath.Join(dir, name)

	data, err := json.MarshalIndent(workLogFile{
		Key:      wl.Key,
		Started:  wl.Started.Format(time.RFC3339),
		Seconds:  int64(wl.Duration.Seconds()),
		Duration: wl.Duration.String(),
		Comment:  wl.Comment,
		Author:   wl.Author,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding work log for %s: %w", wl.Key, err)
	}

	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	return name, nil
}

// Complete records a completed task as a JSON marker file.
func (p *Provider) Complete(ctx context.Context, key string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	dir := filepath.Join(p.dir, "completed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	path := filepath.Join(dir, sanitize(key)+".json")
	data, err := json.MarshalIndent(map[string]string{
		"key":          key,
		"completed_at": time.Now().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding completion for %s: %w", key, err)
	}

	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// sanitize strips path separators out of a provider key so it cannot escape the
// sync directory when used as a filename.
func sanitize(key string) string {
	replacer := strings.NewReplacer("/", "_", `\`, "_", "..", "_", ":", "_")
	return replacer.Replace(key)
}
