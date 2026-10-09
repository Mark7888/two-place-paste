package update

import (
	"context"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
)

// Schedule defaults (plan §3.4).
const (
	// DefaultCheckInterval is how long a check stays fresh. It is measured on
	// the wall clock, against the persisted last-check time: Go's timers run
	// on the monotonic clock, which stops while a Mac sleeps, so a bare 6-hour
	// ticker could take days of real time on a laptop that is mostly asleep.
	DefaultCheckInterval = 6 * time.Hour

	// DefaultStartDelay keeps the first check off the login path.
	DefaultStartDelay = 2 * time.Minute

	// DefaultIdleRetry is how often a downloaded-and-due auto-update asks
	// again whether the machine is idle enough to restart the service. It
	// only runs while such an update is waiting.
	DefaultIdleRetry = 15 * time.Minute

	// fallbackTick is how often the portable scheduler wakes to compare the
	// wall clock with the last check. Each wake is a comparison and nothing
	// else unless a check is due.
	fallbackTick = 30 * time.Minute
)

// Run drives the auto-update schedule until ctx is done. It checks only while
// the user has auto-update on, and never on the Nightly channel: a commit
// someone named does not change, so there is nothing to look for.
//
// Checks are triggered by the start delay, by the platform scheduler
// (NSBackgroundActivityScheduler on macOS, which holds work back on battery,
// in Low Power Mode and under thermal pressure; a coarse ticker elsewhere),
// and by Woke. Each trigger only checks if the last check is older than the
// check interval.
func (m *Manager) Run(ctx context.Context) {
	fires := make(chan struct{}, 1)
	trigger := func() {
		select {
		case fires <- struct{}{}:
		default:
		}
	}
	m.mu.Lock()
	m.trigger = trigger
	m.mu.Unlock()

	stop := startSchedule(m.opts.CheckInterval, m.opts.CheckInterval/3, trigger)
	defer stop()

	start := time.NewTimer(m.opts.StartDelay)
	defer start.Stop()
	var retry <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-start.C:
		case <-fires:
		case <-retry:
		}
		m.scheduled(ctx)
		retry = nil
		if m.autoPending() {
			retry = time.After(m.opts.IdleRetry)
		}
	}
}

// Woke tells the schedule the machine has just woken: a check that fell due
// while it slept runs now, not at the next tick.
func (m *Manager) Woke() {
	m.mu.Lock()
	trigger := m.trigger
	m.mu.Unlock()
	if trigger != nil {
		trigger()
	}
}

// scheduled is one wake of the schedule: check if due, and install an
// update that qualifies once the machine is idle.
func (m *Manager) scheduled(ctx context.Context) {
	m.mu.Lock()
	prefs := m.prefs
	due := m.opts.Now().Sub(m.lastChecked) >= m.opts.CheckInterval
	m.mu.Unlock()
	if !prefs.AutoUpdate || prefs.Channel() == config.ChannelNightly {
		return
	}
	if due {
		if _, err := m.Check(ctx); err != nil {
			return
		}
	}
	if !m.autoPending() {
		return
	}
	if m.opts.Idle != nil && !m.opts.Idle() {
		m.opts.Logger.Info("an update is ready; waiting until the machine is idle to restart")
		return
	}
	if _, err := m.Install(ctx, localui.InstallRequest{}); err != nil {
		m.opts.Logger.Warn("automatic update failed", "error", err)
	}
}

// autoPending reports whether the offered build is one the schedule installs
// by itself: newer, signed, and finished building. An older build after a
// channel switch, or an unsigned one, always waits for a person.
func (m *Manager) autoPending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy || m.avail == nil || !m.prefs.AutoUpdate || m.prefs.Channel() == config.ChannelNightly {
		return false
	}
	c := m.avail.view
	return !c.Older && c.Signed && !c.NeedsConfirmation && !c.Building && m.readyLocked() == nil
}
