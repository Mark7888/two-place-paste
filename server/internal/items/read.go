package items

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// Reading, editing and deleting items. Every one of these is refused while
// the group's store is off: the items are kept, but the store is closed.

// List paging defaults: a page is what a client asks for, within reason.
const (
	defaultListLimit = 100
	maxListLimit     = 500
)

// List returns one page of the group's items, newest first, and the cursor
// of the next page — empty at the end. The cursor names the last item
// returned, not a time, so items saved in the same millisecond are never
// skipped: a client needs every item to search and to spot duplicates.
func (s *Service) List(ctx context.Context, groupID, cursor string, limit int) ([]store.Item, string, error) {
	if _, err := s.enabledGroup(ctx, groupID); err != nil {
		return nil, "", err
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)

	refs, err := s.reg.ItemRefs(ctx, groupID)
	if err != nil {
		return nil, "", fmt.Errorf("list store items of group %s: %w", groupID, err)
	}
	start := 0
	if cursor != "" {
		after, err := parseCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		start = len(refs)
		for i, r := range refs {
			if before(r, after) {
				start = i
				break
			}
		}
	}
	end := min(start+limit, len(refs))
	ids := make([]string, 0, end-start)
	for _, r := range refs[start:end] {
		ids = append(ids, r.ID)
	}
	page, err := s.reg.GetItems(ctx, groupID, ids)
	if err != nil {
		return nil, "", fmt.Errorf("list store items of group %s: %w", groupID, err)
	}
	next := ""
	if end < len(refs) && end > start {
		next = formatCursor(refs[end-1])
	}
	return page, next, nil
}

// before reports whether r comes after the cursor's item in a listing:
// older, or as old with a smaller id.
func before(r, cursor store.ItemRef) bool {
	if c := r.SavedAt.Compare(cursor.SavedAt); c != 0 {
		return c < 0
	}
	return r.ID < cursor.ID
}

// formatCursor and parseCursor encode an item's place in a listing:
// "<saved_at ms>.<item id>". Clients treat it as opaque.
func formatCursor(r store.ItemRef) string {
	return strconv.FormatInt(r.SavedAt.UnixMilli(), 10) + "." + r.ID
}

func parseCursor(c string) (store.ItemRef, error) {
	ms, id, ok := strings.Cut(c, ".")
	n, err := strconv.ParseInt(ms, 10, 64)
	if !ok || err != nil || !validID(id) {
		return store.ItemRef{}, fmt.Errorf("list store items: cursor: %w", ErrInvalid)
	}
	return store.ItemRef{ID: id, SavedAt: time.UnixMilli(n).UTC()}, nil
}

// FetchRange returns length bytes of an item's blob from offset, with the
// item. A length of zero or above the chunk size means the chunk size; the
// range is cut at the end of the blob.
func (s *Service) FetchRange(ctx context.Context, groupID, itemID string, offset, length int64) (store.Item, []byte, error) {
	if _, err := s.enabledGroup(ctx, groupID); err != nil {
		return store.Item{}, nil, err
	}
	it, err := s.reg.GetItem(ctx, groupID, itemID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Item{}, nil, fmt.Errorf("read item %s: %w", itemID, ErrNotFound)
	}
	if err != nil {
		return store.Item{}, nil, fmt.Errorf("read item %s: %w", itemID, err)
	}
	if offset < 0 || offset >= it.Size {
		return store.Item{}, nil, fmt.Errorf("read item %s: %w: offset %d of %d bytes", itemID, ErrInvalidRange, offset, it.Size)
	}
	if length <= 0 || length > s.cfg.ChunkBytes {
		length = s.cfg.ChunkBytes
	}
	length = min(length, it.Size-offset)

	rc, err := s.blobs.GetItemRange(ctx, groupID, itemID, offset, length)
	if errors.Is(err, blob.ErrNotFound) {
		// The record says the blob is there and it is not: deleted outside
		// the relay. Say so to the operator; the item stays listed, because
		// deleting a user's item behind their back is worse than a failed
		// read they can see and delete themselves.
		s.logger.ErrorContext(ctx, "store item has no blob",
			slog.String("group_id", groupID), slog.String("item_id", itemID))
		return store.Item{}, nil, fmt.Errorf("read item %s: blob missing: %w", itemID, ErrNotFound)
	}
	if err != nil {
		return store.Item{}, nil, fmt.Errorf("read item %s: %w", itemID, err)
	}
	defer rc.Close()
	// Read through a limit rather than into a buffer sized from the request.
	data, err := io.ReadAll(io.LimitReader(rc, length))
	if err != nil {
		return store.Item{}, nil, fmt.Errorf("read item %s: %w", itemID, err)
	}
	if int64(len(data)) != length {
		return store.Item{}, nil, fmt.Errorf("read item %s: %w", itemID, io.ErrUnexpectedEOF)
	}
	return it, data, nil
}

// UpdateMeta replaces an item's sealed metadata, if it is still at the
// revision the caller edited, and tells the group's other devices.
func (s *Service) UpdateMeta(ctx context.Context, groupID, deviceID, itemID string, expectedRev uint64, sealedMeta []byte) (store.Item, error) {
	if len(sealedMeta) != SealedMetaSize {
		return store.Item{}, fmt.Errorf("update item %s: metadata of %d bytes: %w", itemID, len(sealedMeta), ErrInvalid)
	}
	if _, err := s.enabledGroup(ctx, groupID); err != nil {
		return store.Item{}, err
	}
	it, err := s.reg.UpdateItemMeta(ctx, groupID, itemID, expectedRev, sealedMeta)
	if errors.Is(err, store.ErrNotFound) {
		return store.Item{}, fmt.Errorf("update item %s: %w", itemID, ErrNotFound)
	}
	if err != nil {
		return store.Item{}, fmt.Errorf("update item %s: %w", itemID, err)
	}
	if s.hooks.OnUpdated != nil {
		s.hooks.OnUpdated(ctx, groupID, deviceID, it)
	}
	return it, nil
}

// Delete deletes an item for every device of the group: its record first,
// then its blob. A blob whose delete fails is left to the sweep.
func (s *Service) Delete(ctx context.Context, groupID, deviceID, itemID string) error {
	if _, err := s.enabledGroup(ctx, groupID); err != nil {
		return err
	}
	it, err := s.reg.DeleteItem(ctx, groupID, itemID)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("delete item %s: %w", itemID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("delete item %s: %w", itemID, err)
	}
	s.deleteBlob(context.WithoutCancel(ctx), groupID, it.ID)
	s.logger.InfoContext(ctx, "store item deleted",
		slog.String("group_id", groupID), slog.String("item_id", itemID), slog.Int64("size", it.Size))
	if s.hooks.OnDeleted != nil {
		s.hooks.OnDeleted(ctx, groupID, deviceID, itemID)
	}
	return nil
}

// deleteBlob deletes the blob of an item whose record is gone. One that
// fails goes to the retry set the sweep drains (SPEC §4.6): a blob left
// behind costs disk, never correctness.
func (s *Service) deleteBlob(ctx context.Context, groupID, itemID string) {
	err := s.blobs.DeleteItem(ctx, groupID, itemID)
	if err == nil {
		return
	}
	s.logger.WarnContext(ctx, "delete store blob failed; the sweep will retry",
		slog.String("group_id", groupID), slog.String("item_id", itemID), slog.Any("error", err))
	if err := s.reg.AddBlobRetry(ctx, store.BlobRetry{GroupID: groupID, ItemID: itemID}); err != nil {
		s.logger.WarnContext(ctx, "queue store blob for retry failed",
			slog.String("group_id", groupID), slog.String("item_id", itemID), slog.Any("error", err))
	}
}
