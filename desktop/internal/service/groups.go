package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// Several groups on one device (/spec/crypto.md §10.1,
// docs/internal/plans/multi-group-desktop.md).
//
// The client core keeps the groups; this file is the shell's half: the
// setting that gates it, and what a switch does to the rest of the app. A
// switch is the app closing and starting again with the other group, so it
// resets exactly what Forget resets, plus the notices that were about the
// old group, and connects again.

// switchConnectWait bounds how long a switch waits for the new group's first
// connection before answering. The supervisor keeps dialling after it; the
// wait only lets a reachable relay answer with "connected" rather than
// "connecting".
const switchConnectWait = 5 * time.Second

// Groups implements localui.API.
func (s *Service) Groups(context.Context) (localui.GroupsView, error) {
	relay, err := s.groupRelay()
	if err != nil {
		return localui.GroupsView{}, err
	}
	return s.groupsView(relay), nil
}

// SwitchGroup implements localui.API.
func (s *Service) SwitchGroup(ctx context.Context, ref string) (localui.GroupsView, error) {
	relay, err := s.groupRelay()
	if err != nil {
		return localui.GroupsView{}, err
	}

	// A pairing code on screen belongs to the identity being put away.
	s.replaceOffer(nil, nil)
	if err := relay.SwitchGroup(ref); err != nil {
		// The core put the outgoing group back; it is offline, as after any
		// stop, so it is connected again before the error is reported.
		s.reconnect(ctx, relay)
		return localui.GroupsView{}, groupError("switch groups", err)
	}
	s.resetGroupState()
	s.mu.Lock()
	s.skipped = nil
	s.sensitiveNote = nil
	s.deferred = nil
	s.lastUploaded = time.Time{}
	s.lastApplied = time.Time{}
	s.mu.Unlock()
	s.cancelDownload(time.Time{})

	s.activityChanged()
	s.logger.InfoContext(ctx, "switched to another group", "group", relay.ActiveRef())
	s.hub.publish(localui.Event{Kind: localui.EventDisconnected, Message: "switching groups"})
	s.hub.publish(localui.Event{Kind: localui.EventGroups})
	s.reconnect(ctx, relay)
	return s.groupsView(relay), nil
}

// AddGroup implements localui.API: it sets the active group aside, and the
// device is then ready to create a group or pair into one, from Pairing.
func (s *Service) AddGroup(ctx context.Context) (localui.GroupsView, error) {
	relay, err := s.groupRelay()
	if err != nil {
		return localui.GroupsView{}, err
	}

	s.replaceOffer(nil, nil)
	if err := relay.ParkActive(); err != nil {
		s.reconnect(ctx, relay)
		return localui.GroupsView{}, groupError("set the group aside", err)
	}
	s.resetGroupState()

	s.activityChanged()
	s.logger.InfoContext(ctx, "set the active group aside to add another")
	s.hub.publish(localui.Event{Kind: localui.EventDisconnected, Message: "ready to add a group"})
	s.hub.publish(localui.Event{Kind: localui.EventGroups})
	return s.groupsView(relay), nil
}

// RemoveGroup implements localui.API. It removes a parked group only: the
// active group is left with Settings → Leave the group, as before.
func (s *Service) RemoveGroup(ctx context.Context, ref string) (localui.GroupsView, error) {
	relay, err := s.groupRelay()
	if err != nil {
		return localui.GroupsView{}, err
	}
	if err := relay.RemoveParked(ref); err != nil {
		return localui.GroupsView{}, groupError("remove the group", err)
	}
	s.logger.InfoContext(ctx, "removed a parked group", "group", ref)
	s.hub.publish(localui.Event{Kind: localui.EventGroups})
	return s.groupsView(relay), nil
}

// groupRelay is the relay, when the group operations may run: the service is
// attached and the setting is on. With the setting off nothing about the
// stored groups changes; the operations are simply not offered.
func (s *Service) groupRelay() (Relay, error) {
	s.mu.Lock()
	relay, on := s.relay, s.settings.MultiGroup
	s.mu.Unlock()
	if relay == nil {
		return nil, localui.Errorf(http.StatusServiceUnavailable, nil, "the service is still starting")
	}
	if !on {
		return nil, localui.Errorf(http.StatusConflict, nil,
			"turn on “Keep several groups” in Settings to use more than one group")
	}
	return relay, nil
}

// reconnect brings the active group online, if there is one. It waits a
// little for the first connection and no longer: the supervisor carries on
// dialling, and a relay that is down is shown as offline like at any launch.
func (s *Service) reconnect(ctx context.Context, relay Relay) {
	if !relay.InGroup() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, switchConnectWait)
	defer cancel()
	if err := relay.Connect(ctx); err != nil {
		s.logger.WarnContext(ctx, "the group is not connected yet; still trying", "error", err)
	}
}

func (s *Service) groupsView(relay Relay) localui.GroupsView {
	view := localui.GroupsView{Parked: []localui.GroupView{}}
	if relay.InGroup() {
		state := relay.State()
		s.mu.Lock()
		dead := s.revoked
		s.mu.Unlock()
		view.Active = &localui.GroupView{
			Ref:       relay.ActiveRef(),
			Name:      state.GroupName,
			ServerURL: state.ServerURL,
			Dead:      dead,
		}
	}
	for _, g := range relay.ParkedGroups() {
		view.Parked = append(view.Parked, localui.GroupView{Ref: g.Ref, Name: g.GroupName, ServerURL: g.ServerURL})
	}
	return view
}

// groupError turns a core error into what the Groups tab shows.
func groupError(op string, err error) error {
	switch {
	case errors.Is(err, tppclient.ErrUnknownGroup):
		return localui.Errorf(http.StatusNotFound, err, "that group is no longer on this device")
	case errors.Is(err, tppclient.ErrParkedUnreadable):
		return localui.Errorf(http.StatusConflict, err,
			"the other groups were saved by a newer version of TwoPlacePaste; update it to use them")
	case errors.Is(err, tppclient.ErrClosed):
		return localui.Errorf(http.StatusServiceUnavailable, err, "TwoPlacePaste is shutting down")
	default:
		return localui.Errorf(http.StatusInternalServerError, err, "could not %s", op)
	}
}
