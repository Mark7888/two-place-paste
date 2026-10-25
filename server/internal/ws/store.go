package ws

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"google.golang.org/protobuf/proto"

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
	BeginUpload(ctx context.Context, groupID, deviceID, itemID string, epoch uint64, size int64) (items.UploadInfo, error)
	OwnsUpload(handle string) bool
	WriteChunk(ctx context.Context, groupID, deviceID, handle string, index uint32, data []byte) (int64, error)
	AbortUpload(ctx context.Context, groupID, deviceID, handle string) error
	Put(ctx context.Context, groupID, deviceID string, req items.PutRequest) (store.Item, error)
	List(ctx context.Context, groupID, cursor string, limit int) ([]store.Item, string, error)
	FetchRange(ctx context.Context, groupID, itemID string, offset, length int64) (store.Item, []byte, error)
	UpdateMeta(ctx context.Context, groupID, deviceID, itemID string, expectedRev uint64, sealedMeta []byte) (store.Item, error)
	Delete(ctx context.Context, groupID, deviceID, itemID string) error
}

// storeHandler routes one store request. It reports false for a message type
// that is not the store's.
func (s *Server) storeHandler(ctx context.Context, env *tppv1.Envelope, deviceID, groupID string) ([]byte, bool, error) {
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
	case tppv1.MessageType_MESSAGE_TYPE_STORE_UPLOAD_BEGIN_REQUEST:
		frame, err = s.handleStoreUploadBegin(ctx, env, deviceID, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_REQUEST:
		frame, err = s.handleStorePut(ctx, env, deviceID, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_STORE_LIST_REQUEST:
		frame, err = s.handleStoreList(ctx, env, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_STORE_FETCH_CHUNK_REQUEST:
		frame, err = s.handleStoreFetchChunk(ctx, env, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_STORE_UPDATE_META_REQUEST:
		frame, err = s.handleStoreUpdateMeta(ctx, env, deviceID, groupID)
	case tppv1.MessageType_MESSAGE_TYPE_STORE_DELETE_REQUEST:
		frame, err = s.handleStoreDelete(ctx, env, deviceID, groupID)
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
	case errors.Is(err, items.ErrNotFound):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_NOT_FOUND, "no such item")
	case errors.Is(err, store.ErrStoreFull):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED, "the store is full; delete items or ask the admin for more room")
	case errors.Is(err, items.ErrStorageFull):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED, "the relay's storage is full")
	case errors.Is(err, items.ErrTooLarge):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_TOO_LARGE, "the item is larger than the store allows")
	case errors.Is(err, store.ErrEpochConflict):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT, "the item is not sealed at the group's current epoch")
	case errors.Is(err, store.ErrItemExists):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "item_id is already in use")
	case errors.Is(err, store.ErrRevisionConflict):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_REVISION_CONFLICT, "another device changed the item first")
	case errors.Is(err, items.ErrUploadNotFound):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_NOT_FOUND, "no such upload")
	case errors.Is(err, items.ErrTooManyUploads):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_RATE_LIMITED, "too many uploads in progress")
	case errors.Is(err, items.ErrInvalidChunk):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "the chunk does not fit the upload: wrong index or size, or not every chunk has arrived")
	case errors.Is(err, items.ErrUploadMismatch):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "the commit names another item, epoch or size than its upload")
	case errors.Is(err, items.ErrInvalidRange):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "the range is outside the item")
	case errors.Is(err, items.ErrInvalid):
		return wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT,
			"item_id must match [A-Za-z0-9_-]{1,64}, sizes must match, wrapped_item_keys must be %d bytes and sealed_meta %d",
			items.WrappedItemKeysSize, items.SealedMetaSize)
	}
	return err
}

// itemMeta is an item as the wire carries it.
func itemMeta(it store.Item) *tppv1.StoreItemMeta {
	return &tppv1.StoreItemMeta{
		ItemId:          it.ID,
		SavedAtUnixMs:   it.SavedAt.UnixMilli(),
		Size:            uint64(max(it.Size, 0)), //nolint:gosec // non-negative
		WrapEpoch:       it.WrapEpoch,
		ContentId:       it.ContentID,
		ContentEpoch:    it.ContentEpoch,
		WrappedItemKeys: it.WrappedKeys,
		SealedMeta:      it.SealedMeta,
		MetaRev:         it.MetaRev,
	}
}

// handleStoreUploadBegin starts a chunked save. Room is reserved here.
func (s *Server) handleStoreUploadBegin(ctx context.Context, env *tppv1.Envelope, deviceID, groupID string) ([]byte, error) {
	var req tppv1.StoreUploadBeginRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	if req.GetSize() > math.MaxInt64 {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_TOO_LARGE, "the item is larger than the store allows")
	}
	info, err := s.items.BeginUpload(ctx, groupID, deviceID, req.GetItemId(), req.GetEpoch(), int64(req.GetSize()))
	if err != nil {
		return nil, fmt.Errorf("begin store upload: %w", err)
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_STORE_UPLOAD_BEGIN_RESPONSE, &tppv1.StoreUploadBeginResponse{
		UploadHandle: info.Handle,
		ChunkBytes:   uint64(info.ChunkBytes), //nolint:gosec // configured, positive
	})
}

// handleStorePut saves an item, in one frame or as an upload's commit.
func (s *Server) handleStorePut(ctx context.Context, env *tppv1.Envelope, deviceID, groupID string) ([]byte, error) {
	var req tppv1.StorePutRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	if req.GetSize() > math.MaxInt64 {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_TOO_LARGE, "the item is larger than the store allows")
	}
	put := items.PutRequest{
		ItemID:      req.GetItemId(),
		Epoch:       req.GetEpoch(),
		Size:        int64(req.GetSize()),
		WrappedKeys: req.GetWrappedItemKeys(),
		SealedMeta:  req.GetSealedMeta(),
	}
	switch body := req.GetBody().(type) {
	case *tppv1.StorePutRequest_Ciphertext:
		put.Ciphertext = body.Ciphertext
		if put.Ciphertext == nil {
			put.Ciphertext = []byte{}
		}
	case *tppv1.StorePutRequest_UploadHandle:
		put.UploadHandle = body.UploadHandle
	default:
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "give the ciphertext or an upload_handle")
	}
	it, err := s.items.Put(ctx, groupID, deviceID, put)
	if err != nil {
		return nil, fmt.Errorf("save store item: %w", err)
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_RESPONSE, &tppv1.StorePutResponse{Item: itemMeta(it)})
}

// handleStoreList returns one page of the group's items.
func (s *Server) handleStoreList(ctx context.Context, env *tppv1.Envelope, groupID string) ([]byte, error) {
	var req tppv1.StoreListRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	page, next, err := s.items.List(ctx, groupID, req.GetCursor(), int(min(req.GetLimit(), math.MaxInt32)))
	if err != nil {
		return nil, fmt.Errorf("list store items: %w", err)
	}
	resp := &tppv1.StoreListResponse{Items: make([]*tppv1.StoreItemMeta, 0, len(page)), NextCursor: next}
	for _, it := range page {
		resp.Items = append(resp.Items, itemMeta(it))
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_STORE_LIST_RESPONSE, resp)
}

// handleStoreFetchChunk returns one range of an item's blob.
func (s *Server) handleStoreFetchChunk(ctx context.Context, env *tppv1.Envelope, groupID string) ([]byte, error) {
	var req tppv1.StoreFetchChunkRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	if req.GetOffset() > math.MaxInt64 {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "offset is past the end of the item")
	}
	length := int64(min(req.GetLength(), uint64(s.entries.FrameMaxBytes()))) //nolint:gosec // bounded by the frame cap
	it, data, err := s.items.FetchRange(ctx, groupID, req.GetItemId(), int64(req.GetOffset()), length)
	if err != nil {
		return nil, fmt.Errorf("read store item: %w", err)
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_STORE_FETCH_CHUNK_RESPONSE, &tppv1.StoreFetchChunkResponse{
		Item:   itemMeta(it),
		Offset: req.GetOffset(),
		Data:   data,
	})
}

// handleStoreUpdateMeta replaces an item's sealed metadata.
func (s *Server) handleStoreUpdateMeta(ctx context.Context, env *tppv1.Envelope, deviceID, groupID string) ([]byte, error) {
	var req tppv1.StoreUpdateMetaRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	it, err := s.items.UpdateMeta(ctx, groupID, deviceID, req.GetItemId(), req.GetExpectedMetaRev(), req.GetSealedMeta())
	if err != nil {
		return nil, fmt.Errorf("update store item: %w", err)
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_STORE_UPDATE_META_RESPONSE, &tppv1.StoreUpdateMetaResponse{Item: itemMeta(it)})
}

// handleStoreDelete deletes an item for the whole group.
func (s *Server) handleStoreDelete(ctx context.Context, env *tppv1.Envelope, deviceID, groupID string) ([]byte, error) {
	var req tppv1.StoreDeleteRequest
	if err := decode(env, &req); err != nil {
		return nil, wireErrf(tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "payload could not be decoded")
	}
	if err := s.items.Delete(ctx, groupID, deviceID, req.GetItemId()); err != nil {
		return nil, fmt.Errorf("delete store item: %w", err)
	}
	return encode(env.GetId(), tppv1.MessageType_MESSAGE_TYPE_STORE_DELETE_RESPONSE, &tppv1.StoreDeleteResponse{})
}

// AnnounceItemSaved tells the group's other connected devices that one of
// them saved an item. It and the two below are the store service's hooks.
// Like EntryAdded they are a courtesy to whoever is online: a device that
// misses one sees the change in its next listing.
func (s *Server) AnnounceItemSaved(ctx context.Context, groupID, deviceID string, it store.Item) {
	s.announce(ctx, groupID, deviceID, tppv1.MessageType_MESSAGE_TYPE_STORE_ITEM_ADDED, &tppv1.StoreItemAdded{Item: itemMeta(it)})
}

// AnnounceItemUpdated is AnnounceItemSaved for an edit.
func (s *Server) AnnounceItemUpdated(ctx context.Context, groupID, deviceID string, it store.Item) {
	s.announce(ctx, groupID, deviceID, tppv1.MessageType_MESSAGE_TYPE_STORE_ITEM_UPDATED, &tppv1.StoreItemUpdated{Item: itemMeta(it)})
}

// AnnounceItemDeleted is AnnounceItemSaved for a delete.
func (s *Server) AnnounceItemDeleted(ctx context.Context, groupID, deviceID, itemID string) {
	s.announce(ctx, groupID, deviceID, tppv1.MessageType_MESSAGE_TYPE_STORE_ITEM_DELETED, &tppv1.StoreItemDeleted{ItemId: itemID})
}

func (s *Server) announce(ctx context.Context, groupID, except string, typ tppv1.MessageType, msg proto.Message) {
	frame, err := encode(newCorrelationID(), typ, msg)
	if err != nil {
		s.logger.ErrorContext(ctx, "encoding store event failed", slog.String("type", typ.String()), slog.Any("error", err))
		return
	}
	s.hub.broadcast(groupID, frame, except)
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
