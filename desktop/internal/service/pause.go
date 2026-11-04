package service

import (
	"context"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
)

// PauseDuration is how long a pause of auto-sync lasts. Long enough to copy a
// password from a browser extension and paste it, short enough that a
// forgotten pause ends on its own instead of leaving a device out of sync for
// the rest of the day.
const PauseDuration = 5 * time.Minute

// Pausing holds both automatic directions at once: auto-watch does not upload
// what is copied, and auto-apply does not put announced entries on the
// clipboard. Manual Upload, Download and Sync still work. A copy made while
// paused is never uploaded later on its own — the point of pausing is that it
// stays here — and an entry that arrives while paused is left for Download,
// with the skipped notice saying so.
//
// The pause lives in memory: a restart ends it, which is the safe direction
// for a switch whose default is "syncing as the user set it up".

// paused reports whether auto-sync is paused now, and until when. It is read
// against the clock, so a pause ends on time even before its timer fires.
func (s *Service) paused() (bool, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pausedLocked()
}

func (s *Service) pausedLocked() (bool, time.Time) {
	if s.pausedUntil.IsZero() || !s.now().Before(s.pausedUntil) {
		return false, time.Time{}
	}
	return true, s.pausedUntil
}

// SetPaused implements localui.API: it pauses auto-sync for PauseDuration, or
// resumes it. Pausing while paused starts the five minutes again.
func (s *Service) SetPaused(ctx context.Context, on bool) localui.PauseView {
	s.mu.Lock()
	if s.pauseTimer != nil {
		s.pauseTimer.Stop()
		s.pauseTimer = nil
	}
	if on {
		s.pausedUntil = s.now().Add(PauseDuration)
		// The timer only tells the tray and the UI that the pause ended;
		// whether it has is always read from the clock (paused).
		s.pauseTimer = time.AfterFunc(PauseDuration, s.pauseEnded)
	} else {
		s.pausedUntil = time.Time{}
	}
	view := s.pauseViewLocked()
	s.mu.Unlock()

	s.logger.InfoContext(ctx, "auto-sync pause changed", "paused", on)
	s.activityChanged()
	s.hub.publish(localui.Event{Kind: localui.EventPause})
	return view
}

// TogglePause pauses auto-sync, or resumes it when it is paused: the tray's
// one menu item.
func (s *Service) TogglePause(ctx context.Context) localui.PauseView {
	on, _ := s.paused()
	return s.SetPaused(ctx, !on)
}

// pauseEnded ends a pause whose time is up, and says so. The timer calls it,
// and so does waking from sleep (Resumed): a timer runs on the monotonic
// clock, which on some systems stands still while the machine sleeps, so the
// pause could otherwise outlive its five minutes on the tray.
func (s *Service) pauseEnded() {
	s.mu.Lock()
	if s.pausedUntil.IsZero() || s.now().Before(s.pausedUntil) {
		// Not due, or already ended: a pause extended after this timer was
		// set has a timer of its own.
		s.mu.Unlock()
		return
	}
	s.pausedUntil = time.Time{}
	if s.pauseTimer != nil {
		s.pauseTimer.Stop()
		s.pauseTimer = nil
	}
	s.mu.Unlock()
	s.logger.Info("auto-sync pause ended")
	s.activityChanged()
	s.hub.publish(localui.Event{Kind: localui.EventPause})
}

// stopPauseTimer is Run's cleanup: the timer is the service's, and stops with
// it.
func (s *Service) stopPauseTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pauseTimer != nil {
		s.pauseTimer.Stop()
		s.pauseTimer = nil
	}
}

func (s *Service) pauseViewLocked() localui.PauseView {
	on, until := s.pausedLocked()
	return localui.PauseView{
		Paused:    on,
		Until:     localui.Optional(until),
		Available: s.settings.AutoWatch || s.settings.AutoApply,
	}
}
