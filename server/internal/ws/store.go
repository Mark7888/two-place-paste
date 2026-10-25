package ws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/server/internal/items"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// The store's requests (SPEC §4.6, §6b, proto/tpp/v1/store.proto). A relay
// built without a store service answers them all as unknown, which is what a
// client of a relay that predates the store also sees.

// StoreItems is the store service subset this package needs.
type StoreItems interface {
	Limits(ctx context.Context, groupID string) (items.Limits, error)
	Links(ctx context.Context, groupID string) ([]store.ItemLink, error)
}

// storeHandler routes one store request. It reports false for a message type
// that is not the store's.
func (s *Server) storeHandler(ctx context.Context, env *tppv1.Envelope, _, groupID string) ([]byte, bool, error) {
	if s.items == nil {
		return nil, false, nil
	}
	var (
		frame []byte
		err   error
	)
	switch env.GetType() {
	case tppv1.MessageType_MESSAGE_TYPE_STORE_LIMITS_REQUEST:
		frame, err = s.handleStoreLimits(ctx, env, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_STORE_LINKS_REQUEST:
		frame, err = s.handleStoreLinks(ctx, env, groupID)
	default:
		return nil, false, nil
	}
	return frame, true, storeWireError(err)
}

// handleStoreLimits states whether the group's store is on and how full it
// is. It is answered when the store is off too, so a client can say so.
func (s *Server) handleStoreLimits(ctx context.Context, env *tppv1.Envelope, groupID string) ([]byte, error) {
	l, err := s.items.Limits(ctx, groupID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_NOT_FOUND, "no such group")
	}
	if err != nil {
		return nil, fmt.Errorf("read store limits: %w", err)
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_STORE_LIMITS_RESPONSE, &tppv1.StoreLimitsResponse{
		Enabled:        l.Enabled,
		MaxBytes:       uint64(max(l.MaxBytes, 0)),       //nolint:gosec // non-negative
		UsedBytes:      uint64(max(l.UsedBytes, 0)),      //nolint:gosec // non-negative
		AvailableBytes: uint64(max(l.AvailableBytes, 0)), //nolint:gosec // non-negative
		MaxItems:       uint64(max(l.MaxItems, 0)),       //nolint:gosec // non-negative
		UsedItems:      uint64(max(l.UsedItems, 0)),      //nolint:gosec // non-negative
		MaxItemBytes:   uint64(max(l.MaxItemBytes, 0)),   //nolint:gosec // non-negative
	})
}

// handleStoreLinks returns the group's store links, oldest first.
func (s *Server) handleStoreLinks(ctx context.Context, env *tppv1.Envelope, groupID string) ([]byte, error) {
	links, err := s.items.Links(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("read store links: %w", err)
	}
	resp := &tppv1.StoreLinksResponse{Links: make([]*tppv1.StoreLink, 0, len(links))}
	for _, l := range links {
		resp.Links = append(resp.Links, &tppv1.StoreLink{Epoch: l.Epoch, Link: l.Link})
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_STORE_LINKS_RESPONSE, resp)
}

// storeWireError turns the store's errors into the codes a client branches
// on. Anything it does not know is passed through, and becomes
// ERROR_CODE_INTERNAL without its detail.
func storeWireError(err error) error {
	var wire wireError
	switch {
	case err == nil, errors.As(err, &wire):
		return err
	case errors.Is(err, store.ErrStoreDisabled):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_STORE_DISABLED, "the store is turned off for this group")
	case errors.Is(err, store.ErrStoreBusy):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_STORE_DISABLED, "the store of this group is being emptied by the admin")
	case errors.Is(err, store.ErrNotFound):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_NOT_FOUND, "no such group")
	}
	return err
}

// AnnounceStoreState tells every connected device of a group that the admin
// turned its store on or off. A device that is offline learns it from its
// next StoreLimitsRequest.
func (s *Server) AnnounceStoreState(ctx context.Context, groupID string, enabled bool) {
	frame, err := encode(newCorrelationID(), tppv1.MessageType_MESSAGE_TYPE_STORE_STATE_CHANGED,
		&tppv1.StoreStateChanged{Enabled: enabled})
	if err != nil {
		s.logger.ErrorContext(ctx, "encoding store state event failed", slog.Any("error", err))
		return
	}
	s.hub.broadcast(groupID, frame)
}

// storeLinkSize is the fixed length of a store link (/spec/crypto.md §7a.2).
const storeLinkSize = 49
