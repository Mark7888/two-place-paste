package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// The out-of-space banners and popup (SPEC §7). Two banners are advice —
// the store is nearly full, history entries were removed early to make room
// — and the user can close them or hide them for good. A refused upload or
// save is a failure and is always shown, as a popup, until closed.
//
// All of it belongs to the active group, whose relay it describes: a group
// switch drops it with the rest of resetGroupState.

// storeRecordBytes is what the relay counts for each store item besides its
// blob: the wrapped item keys and the sealed metadata, both of fixed size.
const storeRecordBytes = tppcrypto.WrappedItemKeysSize + tppcrypto.StoreMetaContainerSize

// spaceState is what the banners and the popup are drawn from. It is guarded
// by Service.mu.
type spaceState struct {
	// limits is the store as the relay last reported it; known says whether
	// it has reported at all on this group.
	limits tppclient.StoreLimits
	known  bool

	// closedAt is the store's usage when the user closed the nearly-full
	// banner. The banner stays closed while the usage is the same, and comes
	// back with the next save or delete that leaves the store that full.
	closedAt    storeUsage
	closedStore bool

	// evicted counts entries removed early since the banner was closed.
	evicted int

	// failure is the last refusal for lack of room, a localui.Space* value.
	failure string
}

type storeUsage struct{ bytes, items int64 }

func usageOf(l tppclient.StoreLimits) storeUsage { return storeUsage{l.UsedBytes, l.UsedItems} }

// nearlyFull reports whether a store is on and at least 90% full, by bytes or
// by items.
func nearlyFull(l tppclient.StoreLimits) bool {
	if !l.Enabled {
		return false
	}
	return l.MaxBytes > 0 && l.UsedBytes*10 >= l.MaxBytes*9 ||
		l.MaxItems > 0 && l.UsedItems*10 >= l.MaxItems*9
}

// storeFullCause says why a save was refused for lack of room, from the
// limits read right after: the group's own store (its items or its bytes),
// or the relay's total store limit, which only the operator can raise.
//
// The relay states AvailableBytes as the smallest of three rooms, less one
// record: the per-item cap, the group's room and the relay's total room.
// When the group's room is what binds, AvailableBytes is exactly that room
// less one record; anything smaller means the total bound it.
func storeFullCause(l tppclient.StoreLimits) string {
	if l.MaxItems > 0 && l.UsedItems >= l.MaxItems {
		return localui.SpaceStoreFull
	}
	if l.MaxBytes == 0 {
		// No group limit: only the total can have refused it.
		return localui.SpaceRelayStoreFull
	}
	groupRoom := l.MaxBytes - l.UsedBytes - storeRecordBytes
	if l.AvailableBytes < groupRoom && l.AvailableBytes < l.MaxItemBytes {
		return localui.SpaceRelayStoreFull
	}
	return localui.SpaceStoreFull
}

// refreshSpace reads the store's limits for the banner. It is quiet about
// failures: the banner is advice, and the next store change asks again.
func (s *Service) refreshSpace(ctx context.Context) {
	relay, err := s.ready()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	limits, err := relay.StoreLimits(ctx)
	if err != nil {
		if !errors.Is(err, tppclient.ErrStoreDisabled) {
			s.logger.DebugContext(ctx, "reading the store's limits for the space banner failed", "error", err)
		}
		return
	}
	s.setStoreLimits(relay, limits)
}

// refreshSpaceLater runs refreshSpace on a goroutine of its own, for the
// handlers that run on the client's read loop and must not wait on it.
func (s *Service) refreshSpaceLater() {
	s.catchUps.Go(func() { s.refreshSpace(context.Background()) })
}

// setStoreLimits records limits read from relay, unless the active group has
// changed since.
func (s *Service) setStoreLimits(relay Relay, limits tppclient.StoreLimits) {
	s.mu.Lock()
	if s.relay != relay {
		s.mu.Unlock()
		return
	}
	changed := !s.space.known || s.space.limits != limits
	s.space.limits, s.space.known = limits, true
	s.mu.Unlock()
	if changed {
		s.hub.publish(localui.Event{Kind: localui.EventSpace})
	}
}

// noteEvicted counts an entry the relay removed early to make room. It
// publishes nothing: the evicted event that follows already makes the UI
// read the status again.
func (s *Service) noteEvicted() {
	s.mu.Lock()
	s.space.evicted++
	s.mu.Unlock()
}

// noteSpaceFailure records a refusal for the popup.
func (s *Service) noteSpaceFailure(kind string) {
	s.mu.Lock()
	s.space.failure = kind
	s.mu.Unlock()
	s.hub.publish(localui.Event{Kind: localui.EventSpace})
}

// noteStoreFull works out why a save was refused for lack of room and
// records it for the popup. Without fresh limits it blames the group's store,
// which is the common case and the one the user can do something about.
func (s *Service) noteStoreFull(ctx context.Context, relay Relay) {
	kind := localui.SpaceStoreFull
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if limits, err := relay.StoreLimits(ctx); err == nil {
		s.setStoreLimits(relay, limits)
		kind = storeFullCause(limits)
	}
	s.noteSpaceFailure(kind)
}

// noteUploadFailure records a history upload the relay refused for lack of
// room. History evicts old entries for a new one, so such a refusal is the
// relay's own trouble, not the group's.
func (s *Service) noteUploadFailure(err error) {
	if errors.Is(err, tppclient.ErrQuotaExceeded) {
		s.noteSpaceFailure(localui.SpaceRelayFull)
	}
}

// DismissSpace implements localui.API.
func (s *Service) DismissSpace(_ context.Context, banner string) error {
	s.mu.Lock()
	switch banner {
	case localui.SpaceBannerStore:
		s.space.closedAt, s.space.closedStore = usageOf(s.space.limits), true
	case localui.SpaceBannerEvicted:
		s.space.evicted = 0
	case localui.SpaceBannerFailure:
		s.space.failure = ""
	default:
		s.mu.Unlock()
		return localui.Errorf(http.StatusBadRequest, nil, "there is no notice called %q", banner)
	}
	s.mu.Unlock()
	s.hub.publish(localui.Event{Kind: localui.EventSpace})
	return nil
}

// spaceViewLocked is the banners and popup as the UI draws them. The caller
// holds s.mu.
func (s *Service) spaceViewLocked() localui.SpaceView {
	sp := s.space
	v := localui.SpaceView{Failure: sp.failure}
	if sp.known && sp.limits.Enabled {
		v.StoreUsedBytes, v.StoreMaxBytes = sp.limits.UsedBytes, sp.limits.MaxBytes
		v.StoreUsedItems, v.StoreMaxItems = sp.limits.UsedItems, sp.limits.MaxItems
	}
	if s.settings.HideSpaceBanners {
		return v
	}
	closed := sp.closedStore && sp.closedAt == usageOf(sp.limits)
	v.StoreNearlyFull = sp.known && nearlyFull(sp.limits) && !closed
	v.Evicted = sp.evicted
	return v
}
