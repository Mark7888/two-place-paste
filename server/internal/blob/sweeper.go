package blob

import (
	"context"
	"log/slog"
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
	if err := s.backend.Sweep(ctx, started); err != nil {
		if ctx.Err() != nil {
			return // shutting down; not an operator's problem
		}
		s.logger.ErrorContext(ctx, "blob sweep failed", slog.Any("error", err))
		return
	}
	s.logger.DebugContext(ctx, "blob sweep completed",
		slog.Duration("took", s.now().Sub(started)))
}
