package service

import (
	"errors"
	"time"
)

// ErrDirectionUnknown is the cause of the error Sync returns when it was asked
// to pick a direction and cannot (SPEC §6). The UI answers it with two
// buttons; the tray answers it with a notification that carries the same two.
var ErrDirectionUnknown = errors.New("the sync direction is unknown")

// Activity is the service's state in the few words a tray icon has room for.
// It is comparable, so a change is told apart from a repeat with ==.
type Activity struct {
	// InGroup is false until this device has created or joined a group.
	InGroup bool

	// Connected reports a live relay connection.
	Connected bool

	// EverConnected tells "not connected yet", which is every launch for its
	// first second or two, from "lost the connection", which is worth a
	// different icon.
	EverConnected bool

	// Revoked is set once the relay has said this device is out of the group.
	Revoked bool

	// GroupDeleted is set with Revoked when the reason is that the server
	// admin deleted the group.
	GroupDeleted bool

	// GroupName is the group's display name as the server admin set it, or
	// empty.
	GroupName string

	// Uploading and Downloading are true while an entry is moving that way.
	// Both can be: an upload of this device's own does not wait for a
	// download already running to finish.
	Uploading   bool
	Downloading bool

	// Skipped is the direction of the latest automatic transfer that was held
	// back — over the user's size limit, or failed — and is still waiting for
	// the user to send or fetch it by hand: "upload", "download", or empty.
	Skipped string

	// Paused is set while auto-sync is paused, until PausedUntil.
	Paused      bool
	PausedUntil time.Time

	// AutoSync is whether either automatic direction is switched on, which
	// is when pausing means anything.
	AutoSync bool
}

// beginDownload marks a download as under way, and returns the function that
// marks it done. Downloads can overlap — a manual one and an announced one —
// so it counts rather than flags. Uploads are counted by beginUpload.
func (s *Service) beginDownload() func() {
	s.mu.Lock()
	s.downloads++
	s.mu.Unlock()
	s.activityChanged()
	return func() {
		s.mu.Lock()
		s.downloads--
		s.mu.Unlock()
		s.activityChanged()
	}
}

// Activity returns the service's current state.
func (s *Service) Activity() Activity {
	s.mu.Lock()
	a := Activity{
		Connected:     s.connected,
		EverConnected: s.everConnected,
		Revoked:       s.revoked,
		GroupDeleted:  s.groupDeleted,
		Uploading:     s.uploads > 0,
		Downloading:   s.downloads > 0,
	}
	if s.skipped != nil {
		a.Skipped = s.skipped.Direction
	}
	a.Paused, a.PausedUntil = s.pausedLocked()
	a.AutoSync = s.settings.AutoWatch || s.settings.AutoApply
	relay := s.relay
	s.mu.Unlock()
	// Outside the lock: the relay has a lock of its own, and nothing here
	// needs the two to agree to the instant.
	a.InGroup = relay != nil && relay.InGroup()
	if a.InGroup {
		a.GroupName = relay.State().GroupName
	}
	return a
}

// activityChanged hands the current state to OnActivity, when it differs from
// the last state handed over.
//
// The whole of it runs under activityMu, so two changes racing each other
// still reach the callback in the order the state moved: a state computed
// first is also delivered first. It must never be called with s.mu held.
func (s *Service) activityChanged() {
	if s.onActivity == nil {
		return
	}
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	a := s.Activity()
	if s.activitySent && a == s.lastActivity {
		return
	}
	s.lastActivity, s.activitySent = a, true
	s.onActivity(a)
}
