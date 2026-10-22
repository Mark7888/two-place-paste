package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// stallTimeout is how long a download may go without receiving a single byte
// before it is given up. It replaces a deadline on the whole download, which
// failed every build that took longer than the deadline to arrive over a slow
// line however steadily it was arriving. A variable so tests can shorten it.
var stallTimeout = 60 * time.Second

// ErrStalled reports a download that stopped receiving data.
var ErrStalled = errors.New("update: the download stalled")

// progressFunc reports a download's progress: the bytes received so far, and
// the bytes expected in all, or 0 when that is not known.
type progressFunc func(done, total int64)

// withStallGuard returns a context that is cancelled with ErrStalled once
// kick has not been called for stallTimeout. The timer starts at once, so the
// wait for the response headers is covered too. stop releases the timer and
// must always be called.
func withStallGuard(parent context.Context) (ctx context.Context, kick, stop func()) {
	ctx, cancel := context.WithCancelCause(parent)
	timer := time.AfterFunc(stallTimeout, func() { cancel(ErrStalled) })
	kick = func() { timer.Reset(stallTimeout) }
	stop = func() {
		timer.Stop()
		cancel(nil)
	}
	return ctx, kick, stop
}

// stalled turns an error caused by the stall guard into one that says so. Any
// other error is returned as it is.
func stalled(ctx context.Context, err error) error {
	if err != nil && errors.Is(context.Cause(ctx), ErrStalled) {
		return fmt.Errorf("%w: nothing arrived for %s; check the connection and try again", ErrStalled, stallTimeout)
	}
	return err
}

// progressReader counts what passes through it, keeps the stall guard fed
// while bytes arrive, and reports progress.
type progressReader struct {
	r      io.Reader
	done   int64
	total  int64
	kick   func()
	report progressFunc
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		p.kick()
		if p.report != nil {
			p.report(p.done, p.total)
		}
	}
	return n, err //nolint:wrapcheck // A reader passes its source's errors on untouched, io.EOF above all.
}
