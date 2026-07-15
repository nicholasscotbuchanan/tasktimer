package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"task-timer-app/internal/task"
)

// Engine drives the sync cycle for every enabled provider.
//
// One cycle, per provider, is: pull remote tasks into the local store, push
// unsynced work sessions upstream, then tell the provider about tasks the user
// completed locally. A provider that fails is logged and skipped; it does not
// stop the other providers or kill the daemon, because a backend outage should
// not take down a local timer.
type Engine struct {
	store     *task.Store
	providers []Provider
	logger    *log.Logger
}

// NewEngine instantiates every enabled provider in the config and returns an
// engine bound to the store. Providers that are listed but disabled are
// skipped; providers that are enabled but fail to configure are a hard error,
// since running with a silently missing backend is worse than not starting.
func NewEngine(store *task.Store, cfg Config, logger *log.Logger) (*Engine, error) {
	if logger == nil {
		logger = log.Default()
	}

	var providers []Provider
	for _, pc := range cfg.Providers {
		if !pc.Enabled {
			logger.Printf("provider %s: disabled, skipping", pc.Name)
			continue
		}
		p, err := build(pc.Name, pc.Settings)
		if err != nil {
			return nil, err
		}
		providers = append(providers, p)
		logger.Printf("provider %s: enabled", p.Name())
	}

	if len(providers) == 0 {
		logger.Printf("no providers enabled; edit %s to configure one", ConfigPath())
	}

	return &Engine{store: store, providers: providers, logger: logger}, nil
}

// Run executes a sync cycle every interval until the context is cancelled. It
// syncs once immediately rather than waiting out the first tick.
func (e *Engine) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	e.RunOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			e.RunOnce(ctx)
		}
	}
}

// RunOnce performs a single sync cycle across all providers. Errors are logged
// rather than returned: a cycle is best-effort, and the next one will retry.
func (e *Engine) RunOnce(ctx context.Context) {
	for _, p := range e.providers {
		if ctx.Err() != nil {
			return
		}
		if err := e.syncProvider(ctx, p); err != nil {
			e.logger.Printf("provider %s: sync cycle failed: %v", p.Name(), err)
		}
	}
}

func (e *Engine) syncProvider(ctx context.Context, p Provider) error {
	if err := e.pull(ctx, p); err != nil {
		return err
	}
	if err := e.push(ctx, p); err != nil {
		return err
	}
	return e.complete(ctx, p)
}

// pull fetches remote tasks changed since the last cycle and stores them.
//
// The cursor is captured before the request, not after: if a task is modified
// while the pull is in flight, an after-the-fact cursor would skip it forever.
// Overlapping slightly is harmless because upserts are idempotent.
func (e *Engine) pull(ctx context.Context, p Provider) error {
	since, err := e.store.LastPull(p.Name())
	if err != nil {
		return err
	}
	cursor := time.Now()

	remotes, err := p.Pull(ctx, since)
	if errors.Is(err, ErrUnsupported) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pulling tasks: %w", err)
	}

	for _, r := range remotes {
		r.Provider = p.Name()
		if err := e.store.UpsertRemote(r); err != nil {
			return err
		}
	}

	if len(remotes) > 0 {
		e.logger.Printf("provider %s: pulled %d task(s)", p.Name(), len(remotes))
	}
	return e.store.SetLastPull(p.Name(), cursor)
}

// push sends completed local sessions upstream. Each session is marked with the
// provider's work-log id on success, which is what prevents a double push.
func (e *Engine) push(ctx context.Context, p Provider) error {
	pending, err := e.store.PendingPush(p.Name())
	if err != nil {
		return err
	}

	for _, t := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := e.store.SetStatus(t.ID, task.StatusSyncing); err != nil {
			return err
		}

		signature, err := p.Push(ctx, WorkLog{
			Key:      t.ForeignKey,
			Started:  t.Start,
			Duration: t.Duration,
			Comment:  t.Comment,
			Author:   t.Username,
		})
		if errors.Is(err, ErrUnsupported) {
			// Roll the status back so a provider that gains push support later
			// picks the session up instead of leaving it wedged in "Syncing".
			if err := e.store.SetStatus(t.ID, task.StatusLogged); err != nil {
				return err
			}
			return nil
		}
		if err != nil {
			e.logger.Printf("provider %s: pushing session %d (%s): %v", p.Name(), t.ID, t.ForeignKey, err)
			if err := e.store.SetStatus(t.ID, task.StatusLogged); err != nil {
				return err
			}
			continue
		}

		// Always SyncedProgress, even for a session whose task the user has
		// already finished locally. SyncedComplete is the sentinel that
		// PendingCompletions treats as "the provider has been told", and only
		// complete() has the standing to claim it. Setting it here meant a
		// pushed work log marked its own task complete without the provider
		// ever being asked to close the issue — so the issue stayed open.
		if err := e.store.MarkPushed(t.ID, signature, task.StatusSyncedProgress); err != nil {
			return err
		}
		e.logger.Printf("provider %s: pushed %s (%s) as %s", p.Name(), t.ForeignKey, t.Duration, signature)
	}
	return nil
}

// complete tells the provider about tasks the user finished locally.
func (e *Engine) complete(ctx context.Context, p Provider) error {
	pending, err := e.store.PendingCompletions(p.Name())
	if err != nil {
		return err
	}

	for _, t := range pending {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := p.Complete(ctx, t.ForeignKey)
		if errors.Is(err, ErrUnsupported) {
			return nil
		}
		if err != nil {
			e.logger.Printf("provider %s: completing %s: %v", p.Name(), t.ForeignKey, err)
			continue
		}

		if err := e.store.MarkCompletionSynced(p.Name(), t.ForeignKey); err != nil {
			return err
		}
		e.logger.Printf("provider %s: marked %s complete", p.Name(), t.ForeignKey)
	}
	return nil
}
