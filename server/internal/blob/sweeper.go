package blob

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Sweeper runs Backend.Sweep once at startup and then on a fixed interval
// (SPEC §4.5, "schedule").
//
// The startup pass is what makes arbitrary downtime harmless: every bucket
// that expired while the server was off is removed in one listing, so no
// garbage accumulates across a restart.
type Sweeper struct {
	backend  Backend
	interval time.Duration
	logger   *slog.Logger
	now      func() time.Time
	tasks    []sweepTask

	mu     sync.Mutex
	status SweepStatus
}

// SweepStatus describes the most recent completed sweep, for the admin
// dashboard. The zero value means no sweep has finished yet.
type SweepStatus struct {
	// At is when the sweep started.
	At time.Time

	// Took is how long the sweep and its tasks ran.
	Took time.Duration

	// Err is what failed, or nil. A failed sweep is retried on the next
	// interval; anything it left is still within its bucket's lifetime.
	Err error
}

// Status reports the most recent completed sweep. It is safe to call from any
// goroutine.
func (s *Sweeper) Status() SweepStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// sweepTask is extra work run on the sweep's schedule, after the bucket sweep.
type sweepTask struct {
	name string
	run  func(ctx context.Context, now time.Time) error
}

// NewSweeper returns a sweeper for backend. A non-positive interval is a
// programmer error and panics at construction rather than producing a loop
// that never sleeps (docs/conventions.md §4).
func NewSweeper(backend Backend, interval time.Duration, logger *slog.Logger) *Sweeper {
	if interval <= 0 {
		panic("blob: sweep interval must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Sweeper{
		backend:  backend,
		interval: interval,
		logger:   logger,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Also adds a task that runs after every bucket sweep, at the same instant, so
// that bookkeeping keyed by expiry — the storage ledger — forgets what the
// sweep reclaims on the same schedule. A failing task is logged and does not
// stop the others. Call it before Run.
func (s *Sweeper) Also(name string, run func(ctx context.Context, now time.Time) error) *Sweeper {
	s.tasks = append(s.tasks, sweepTask{name: name, run: run})
	return s
}

// Run sweeps immediately and then every interval until ctx is done. It is the
// goroutine's whole life: the caller owns it and stops it by cancelling ctx
// (docs/conventions.md §8).
func (s *Sweeper) Run(ctx context.Context) {
	s.sweepOnce(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepOnce(ctx)
		}
	}
}

func (s *Sweeper) sweepOnce(ctx context.Context) {
	started := s.now()
	var errs []error
	if err := s.backend.Sweep(ctx, started); err != nil {
		if ctx.Err() != nil {
			return // shutting down; not an operator's problem
		}
		s.logger.ErrorContext(ctx, "blob sweep failed", slog.Any("error", err))
		errs = append(errs, fmt.Errorf("blob sweep: %w", err))
	} else {
		s.logger.DebugContext(ctx, "blob sweep completed",
			slog.Duration("took", s.now().Sub(started)))
	}

	for _, t := range s.tasks {
		if err := t.run(ctx, started); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logger.ErrorContext(ctx, "sweep task failed", slog.String("task", t.name), slog.Any("error", err))
			errs = append(errs, fmt.Errorf("%s: %w", t.name, err))
		}
	}

	s.mu.Lock()
	s.status = SweepStatus{At: started, Took: s.now().Sub(started), Err: errors.Join(errs...)}
	s.mu.Unlock()
}
