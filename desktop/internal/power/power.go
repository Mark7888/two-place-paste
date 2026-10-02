// Package power tells the desktop service about the machine's state around it:
// whether anyone is at the screen, and when the machine has just woken from
// sleep (SPEC §7.2: a background service must not cost the user their
// battery).
//
// Neither needs a run loop or a notification observer. A locked screen is
// asked about when it matters, and a wake is read off the clocks: the wall
// clock keeps time through sleep and the monotonic clock does not.
package power

import (
	"context"
	"time"
)

// DefaultWakeCheck is how often WatchWake compares the clocks. A wake is
// noticed at most this long after it happens.
const DefaultWakeCheck = 5 * time.Second

// wakeThreshold is how far the wall clock must run ahead of the monotonic one
// before the difference counts as sleep. It is well above what NTP slews or
// steps a healthy clock by; a false positive costs a reconnect and nothing
// more.
const wakeThreshold = 15 * time.Second

// ScreenLocked reports whether nobody can be at this session's screen: it is
// locked, or another user's session owns the console. It answers false where
// the platform offers no way to tell.
func ScreenLocked() bool { return screenLocked() }

// WatchWake calls onWake, with how long the machine slept, each time it
// notices a wake, until ctx is done.
//
// Go's monotonic clock stops while the machine sleeps on macOS
// (mach_absolute_time) and on Linux (CLOCK_MONOTONIC); the wall clock does
// not. A check that finds the wall clock far ahead of the monotonic one has
// therefore slept through the difference. On Windows the monotonic clock runs
// through sleep and this never fires, which costs nothing there but the check.
func WatchWake(ctx context.Context, every time.Duration, onWake func(slept time.Duration)) {
	if every <= 0 {
		every = DefaultWakeCheck
	}
	t := time.NewTicker(every)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		if gap := slept(last.Round(0), now.Round(0), now.Sub(last)); gap > 0 {
			onWake(gap)
		}
		last = now
	}
}

// slept is how much of the wall-clock time between two readings the process
// was not running for, or zero when that is too little to be sleep. elapsed is
// the monotonic time between the same two readings.
func slept(wallPrev, wallNow time.Time, elapsed time.Duration) time.Duration {
	gap := wallNow.Sub(wallPrev) - elapsed
	if gap < wakeThreshold {
		return 0
	}
	return gap
}
