package clipboard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// DefaultInterval is how often the watcher looks at the clipboard when it is
// enabled. Fast enough that a copy followed by a switch to the other machine
// feels instant. On a platform with a change counter a look is one cheap call;
// the content is read only when the counter has moved.
const DefaultInterval = 750 * time.Millisecond

// DefaultPausedInterval is how often a paused watcher asks whether it is still
// paused. Nobody is copying anything at a locked screen, so this is about
// noticing the unlock, not a copy, and it can be slow.
const DefaultPausedInterval = 3 * time.Second

// maxSuppressed bounds the self-write set. Only one write is ever in flight,
// but a platform that coalesces changes can make an older one surface late, so
// a handful of entries are remembered rather than exactly one.
const maxSuppressed = 8

// WatchOptions configures a Watcher.
type WatchOptions struct {
	// Clipboard is the clipboard to watch. Required.
	Clipboard Clipboard

	// Interval is the poll period. Defaults to DefaultInterval.
	Interval time.Duration

	// Paused, when set, is asked before every poll; while it answers true the
	// watcher does not touch the clipboard and asks again every PausedInterval
	// instead. It is how a locked screen stops costing anything.
	Paused func() bool

	// PausedInterval is how often a paused watcher asks again. Defaults to
	// DefaultPausedInterval.
	PausedInterval time.Duration

	// OnChange fires for every change the watcher attributes to the user. It
	// never fires for a change this service made itself.
	OnChange func(ctx context.Context, c Content, at time.Time)

	// Logger receives poll-level detail at Debug. It must never be handed the
	// content (docs/conventions.md §2).
	Logger *slog.Logger

	// Now supplies timestamps; tests replace it.
	Now func() time.Time
}

// Watcher polls the clipboard and reports the changes the user made.
//
// It is off by default (SPEC §7.2): Run does nothing until SetEnabled(true),
// and disabling it stops the polling rather than merely dropping the events.
//
// The property that matters here is self-write suppression. When the service
// writes a downloaded entry to the clipboard, the next poll sees new content
// and would, naively, upload it straight back — a loop that grows an entry per
// tick and burns the group's storage. Suppress records what was written, and
// the poll that observes it adopts it as the new baseline without reporting a
// change.
type Watcher struct {
	cb       Clipboard
	interval time.Duration
	paused   func() bool
	idle     time.Duration
	onChange func(context.Context, Content, time.Time)
	logger   *slog.Logger
	now      func() time.Time

	mu         sync.Mutex
	enabled    bool
	primed     bool
	digest     [32]byte
	seq        uint64
	haveSeq    bool
	suppressed [][32]byte
	lastChange time.Time
	wake       chan struct{}
}

// NewWatcher builds a watcher. It does not start polling; Run does, and only
// while the watcher is enabled.
func NewWatcher(opts WatchOptions) *Watcher {
	w := &Watcher{
		cb:       opts.Clipboard,
		interval: opts.Interval,
		paused:   opts.Paused,
		idle:     opts.PausedInterval,
		onChange: opts.OnChange,
		logger:   opts.Logger,
		now:      opts.Now,
		wake:     make(chan struct{}, 1),
	}
	if w.interval <= 0 {
		w.interval = DefaultInterval
	}
	if w.idle <= 0 {
		w.idle = DefaultPausedInterval
	}
	if w.logger == nil {
		w.logger = slog.Default()
	}
	if w.now == nil {
		w.now = func() time.Time { return time.Now().UTC() }
	}
	return w
}

// Enabled reports whether polling is on.
func (w *Watcher) Enabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.enabled
}

// SetEnabled turns polling on or off. Turning it on discards any baseline, so
// the first poll adopts whatever is on the clipboard instead of reporting it
// as a change the user just made.
func (w *Watcher) SetEnabled(on bool) {
	w.mu.Lock()
	w.enabled = on
	if on {
		w.primed = false
	}
	w.mu.Unlock()
	if on {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

// LastChange returns when the watcher last observed a user change, or the zero
// time when it has observed none.
//
// This is the only local timestamp the service has: neither NSPasteboard nor
// the Windows clipboard records when its content arrived (SPEC §6). A zero
// value is therefore not "the clipboard is old" but "this service cannot
// know", and the caller must ask the user rather than guess a direction.
func (w *Watcher) LastChange() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastChange
}

// Suppress records content this service is about to put on the clipboard, so
// the poll that observes it does not report it as a user change.
func (w *Watcher) Suppress(c Content) {
	d := c.Digest()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.suppressed {
		if s == d {
			return
		}
	}
	w.suppressed = append(w.suppressed, d)
	if len(w.suppressed) > maxSuppressed {
		w.suppressed = w.suppressed[len(w.suppressed)-maxSuppressed:]
	}
}

// MarkLocal records that the local clipboard's content is as of now — what an
// explicit upload knows and the watcher would otherwise have to poll for.
func (w *Watcher) MarkLocal(c Content, at time.Time) {
	d := c.Digest()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.digest, w.primed, w.lastChange = d, true, at
}

// Poll looks at the clipboard once and reports whether the user changed it.
//
// It is exported because it is the whole of the watcher's logic and deserves
// to be tested without a goroutine and a clock.
//
// The change counter, where the platform has one, is recorded only once the
// read it guards has succeeded. Recorded before, a read that failed — a helper
// process that timed out, a file that vanished mid-read — would leave the
// counter saying "seen" for content nobody saw, and that copy would never be
// uploaded.
func (w *Watcher) Poll(ctx context.Context) (Content, bool, error) {
	seq, err := w.cb.Sequence(ctx)
	haveSeq := err == nil
	if err != nil && !errors.Is(err, ErrNoSequence) {
		return Content{}, false, fmt.Errorf("clipboard: read the change counter: %w", err)
	}
	if haveSeq {
		w.mu.Lock()
		unchanged := w.haveSeq && w.seq == seq && w.primed
		w.mu.Unlock()
		if unchanged {
			return Content{}, false, nil
		}
	}

	c, err := w.cb.Read(ctx)
	if errors.Is(err, ErrEmpty) {
		// An empty clipboard is a state, not a change to publish: there is
		// nothing to upload and nothing a peer could paste.
		w.mu.Lock()
		w.primed = true
		w.digest = Content{}.Digest()
		if haveSeq {
			w.seq, w.haveSeq = seq, true
		}
		w.mu.Unlock()
		return Content{}, false, nil
	}
	if err != nil {
		return Content{}, false, fmt.Errorf("clipboard: read the clipboard: %w", err)
	}

	d := c.Digest()
	now := w.now()

	w.mu.Lock()
	defer w.mu.Unlock()
	if haveSeq {
		w.seq, w.haveSeq = seq, true
	}
	switch {
	case !w.primed:
		// First look after start or after being enabled: adopt, do not report.
		w.primed, w.digest = true, d
		return c, false, nil
	case w.digest == d:
		return c, false, nil
	}
	w.digest = d
	for i, s := range w.suppressed {
		if s == d {
			// This is the service's own write coming back to it.
			w.suppressed = append(w.suppressed[:i], w.suppressed[i+1:]...)
			w.logger.Debug("clipboard change is this service's own write; not uploading",
				"kind", string(c.Kind()), "bytes", len(c.Body))
			return c, false, nil
		}
	}
	w.lastChange = now
	return c, true, nil
}

// Run polls until ctx is done.
//
// A disabled watcher, which is its state until the user turns auto-watch on
// (SPEC §7.2), keeps no timer at all: SetEnabled is what wakes it. A paused
// one keeps a slow timer to notice the pause ending. Neither reads the
// clipboard.
func (w *Watcher) Run(ctx context.Context) {
	if !w.cb.Available() {
		w.logger.Warn("clipboard auto-watch is unavailable on this platform")
		return
	}
	t := time.NewTimer(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-t.C:
		}
		next, again := w.tick(ctx)
		if ctx.Err() != nil {
			return
		}
		if again {
			t.Reset(next)
		} else {
			t.Stop()
		}
	}
}

// tick is one turn of Run. It reports how long to wait before the next one,
// and false when there should be no next one until SetEnabled asks.
func (w *Watcher) tick(ctx context.Context) (time.Duration, bool) {
	if !w.Enabled() {
		return 0, false
	}
	if w.paused != nil && w.paused() {
		return w.idle, true
	}
	c, changed, err := w.Poll(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Warn("reading the clipboard failed", "error", err)
		}
		return w.interval, true
	}
	if changed && w.onChange != nil {
		w.onChange(ctx, c, w.now())
	}
	return w.interval, true
}

// Rebase reads the clipboard and adopts whatever is there as the baseline,
// without reporting a change.
//
// It is what a service calls right after writing a downloaded entry. Suppress
// covers the exact bytes that were written; Rebase covers the case where the
// platform hands them back in a different form — a pasteboard that re-encodes
// an image, say — which would otherwise look like a user copy and be uploaded
// straight back.
//
// `at` is when the content it is adopting came to be — for a downloaded entry,
// when that entry was created. It is recorded as the last change because the
// clipboard genuinely did change: leaving lastChange alone made the screen go
// on reporting the previous change for content that was no longer there, and
// made the next direction comparison judge the new clipboard by the old one's
// age. A zero `at` leaves the timestamp alone, which is what a caller that
// only wants the baseline moved asks for.
func (w *Watcher) Rebase(ctx context.Context, at time.Time) {
	if seq, err := w.cb.Sequence(ctx); err == nil {
		w.mu.Lock()
		w.seq, w.haveSeq = seq, true
		w.mu.Unlock()
	}
	c, err := w.cb.Read(ctx)
	if err != nil && !errors.Is(err, ErrEmpty) {
		w.logger.Debug("could not rebase the clipboard watcher", "error", err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.digest, w.primed = c.Digest(), true
	if !at.IsZero() {
		w.lastChange = at
	}
}
