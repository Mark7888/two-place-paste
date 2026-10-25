package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

var _ Items = (*RedisStore)(nil)

// StoreUsage implements Items.
func (s *RedisStore) StoreUsage(ctx context.Context, groupID string) (ItemUsage, error) {
	u, err := s.usage(ctx, storeUsageKeyOf(groupID))
	if err != nil {
		return ItemUsage{}, fmt.Errorf("read store usage of group %s: %w", groupID, err)
	}
	return u, nil
}

// TotalStoreUsage implements Items.
func (s *RedisStore) TotalStoreUsage(ctx context.Context) (ItemUsage, error) {
	u, err := s.usage(ctx, storeUsageKey)
	if err != nil {
		return ItemUsage{}, fmt.Errorf("read total store usage: %w", err)
	}
	return u, nil
}

// usage reads a usage counter hash. A group that never saved anything has
// none, which reads as zero.
func (s *RedisStore) usage(ctx context.Context, key string) (ItemUsage, error) {
	fields, err := s.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return ItemUsage{}, fmt.Errorf("read %s: %w", key, err)
	}
	b, _ := strconv.ParseInt(fields["bytes"], 10, 64)
	n, _ := strconv.ParseInt(fields["items"], 10, 64)
	return ItemUsage{Bytes: max(b, 0), Items: max(n, 0)}, nil
}

// StoreLinks implements Items.
func (s *RedisStore) StoreLinks(ctx context.Context, groupID string) ([]ItemLink, error) {
	fields, err := s.rdb.HGetAll(ctx, storeLinksKey(groupID)).Result()
	if err != nil {
		return nil, fmt.Errorf("read store links of group %s: %w", groupID, err)
	}
	out := make([]ItemLink, 0, len(fields))
	for epoch, link := range fields {
		e, err := strconv.ParseUint(epoch, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("read store links of group %s: epoch %q: %w", groupID, epoch, err)
		}
		out = append(out, ItemLink{Epoch: e, Link: []byte(link)})
	}
	slices.SortFunc(out, func(a, b ItemLink) int { return cmp.Compare(a.Epoch, b.Epoch) })
	return out, nil
}

// ReserveItem implements Items.
func (s *RedisStore) ReserveItem(ctx context.Context, r ItemReservation, limits ItemLimits) error {
	code, err := reserveItemScript.Run(ctx, s.rdb,
		[]string{
			groupKey(r.GroupID), storeItemKey(r.ItemID), storeUsageKeyOf(r.GroupID),
			storeUsageKey, storePendingKey, storeReclaimKey,
		},
		r.GroupID, r.ItemID, strconv.FormatUint(r.Epoch, 10),
		strconv.FormatInt(r.Size, 10), strconv.FormatInt(r.Accounted, 10), msString(r.StartedAt),
		strconv.FormatInt(max(limits.GroupBytes, 0), 10),
		strconv.FormatInt(max(limits.TotalBytes, 0), 10),
		strconv.FormatInt(max(limits.GroupItems, 0), 10),
	).Int64()
	if err != nil {
		return fmt.Errorf("reserve store item %s: %w", r.ItemID, err)
	}
	if err := reserveError(code); err != nil {
		return fmt.Errorf("reserve store item %s: %w", r.ItemID, err)
	}
	return nil
}

// reserveError maps reserveItemScript's codes onto the package's errors.
func reserveError(code int64) error {
	switch code {
	case 0:
		return nil
	case 1:
		return ErrNotFound
	case 2:
		return ErrStoreDisabled
	case 3:
		return ErrStoreBusy
	case 4:
		return ErrEpochConflict
	case 5:
		return ErrItemExists
	case 6:
		return ErrStoreFull
	}
	return fmt.Errorf("unexpected reply %d", code)
}

// CommitItem implements Items.
func (s *RedisStore) CommitItem(ctx context.Context, c ItemCommit) (Item, error) {
	code, err := commitItemScript.Run(ctx, s.rdb,
		[]string{groupKey(c.GroupID), storeItemKey(c.ItemID), storeIndexKey(c.GroupID), storePendingKey, storeReclaimKey},
		c.GroupID, c.ItemID, strconv.FormatUint(c.Epoch, 10), string(c.WrappedKeys), string(c.SealedMeta), msString(c.SavedAt),
	).Int64()
	if err != nil {
		return Item{}, fmt.Errorf("commit store item %s: %w", c.ItemID, err)
	}
	switch code {
	case 0:
	case 1, 2:
		return Item{}, fmt.Errorf("commit store item %s: %w", c.ItemID, ErrNotFound)
	case 3:
		return Item{}, fmt.Errorf("commit store item %s: %w", c.ItemID, ErrStoreDisabled)
	case 4:
		return Item{}, fmt.Errorf("commit store item %s: %w", c.ItemID, ErrStoreBusy)
	case 5:
		return Item{}, fmt.Errorf("commit store item %s: %w", c.ItemID, ErrEpochConflict)
	default:
		return Item{}, fmt.Errorf("commit store item %s: unexpected reply %d", c.ItemID, code)
	}
	return s.GetItem(ctx, c.GroupID, c.ItemID)
}

// ReleaseItem implements Items.
func (s *RedisStore) ReleaseItem(ctx context.Context, groupID, itemID string) (bool, error) {
	n, err := releaseItemScript.Run(ctx, s.rdb,
		[]string{storeItemKey(itemID), storeUsageKeyOf(groupID), storeUsageKey, storePendingKey},
		groupID, itemID,
	).Int64()
	if err != nil {
		return false, fmt.Errorf("release store item %s: %w", itemID, err)
	}
	return n == 1, nil
}

// GetItem implements Items.
func (s *RedisStore) GetItem(ctx context.Context, groupID, itemID string) (Item, error) {
	fields, err := s.rdb.HGetAll(ctx, storeItemKey(itemID)).Result()
	if err != nil {
		return Item{}, fmt.Errorf("read store item %s: %w", itemID, err)
	}
	it, ok := itemFromFields(itemID, fields)
	if !ok || it.GroupID != groupID || it.Pending {
		return Item{}, fmt.Errorf("read store item %s: %w", itemID, ErrNotFound)
	}
	return it, nil
}

// ItemRefs implements Items.
func (s *RedisStore) ItemRefs(ctx context.Context, groupID string) ([]ItemRef, error) {
	zs, err := s.rdb.ZRevRangeWithScores(ctx, storeIndexKey(groupID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list store items of group %s: %w", groupID, err)
	}
	out := make([]ItemRef, 0, len(zs))
	for _, z := range zs {
		id, ok := z.Member.(string)
		if !ok {
			continue
		}
		out = append(out, ItemRef{ID: id, SavedAt: time.UnixMilli(int64(z.Score)).UTC()})
	}
	return out, nil
}

// GetItems implements Items.
func (s *RedisStore) GetItems(ctx context.Context, groupID string, ids []string) ([]Item, error) {
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.HGetAll(ctx, storeItemKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("read store items of group %s: %w", groupID, err)
	}
	out := make([]Item, 0, len(ids))
	for i, cmd := range cmds {
		it, ok := itemFromFields(ids[i], cmd.Val())
		if ok && it.GroupID == groupID && !it.Pending {
			out = append(out, it)
		}
	}
	return out, nil
}

// UpdateItemMeta implements Items.
func (s *RedisStore) UpdateItemMeta(ctx context.Context, groupID, itemID string, expectedRev uint64, sealedMeta []byte) (Item, error) {
	rev, err := updateItemMetaScript.Run(ctx, s.rdb, []string{storeItemKey(itemID)},
		groupID, strconv.FormatUint(expectedRev, 10), string(sealedMeta),
	).Int64()
	if err != nil {
		return Item{}, fmt.Errorf("update store item %s: %w", itemID, err)
	}
	switch rev {
	case -1:
		return Item{}, fmt.Errorf("update store item %s: %w", itemID, ErrNotFound)
	case -2:
		return Item{}, fmt.Errorf("update store item %s: %w", itemID, ErrRevisionConflict)
	}
	return s.GetItem(ctx, groupID, itemID)
}

// DeleteItem implements Items.
func (s *RedisStore) DeleteItem(ctx context.Context, groupID, itemID string) (Item, error) {
	it, err := s.GetItem(ctx, groupID, itemID)
	if err != nil {
		return Item{}, err
	}
	n, err := deleteItemScript.Run(ctx, s.rdb,
		[]string{storeItemKey(itemID), storeIndexKey(groupID), storeUsageKeyOf(groupID), storeUsageKey},
		groupID, itemID,
	).Int64()
	if err != nil {
		return Item{}, fmt.Errorf("delete store item %s: %w", itemID, err)
	}
	if n != 1 {
		return Item{}, fmt.Errorf("delete store item %s: %w", itemID, ErrNotFound)
	}
	return it, nil
}

// ReleaseAllPending implements Items.
func (s *RedisStore) ReleaseAllPending(ctx context.Context) ([]Item, error) {
	ids, err := s.rdb.ZRange(ctx, storePendingKey, 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list pending store items: %w", err)
	}
	var out []Item
	for _, id := range ids {
		fields, err := s.rdb.HGetAll(ctx, storeItemKey(id)).Result()
		if err != nil {
			return out, fmt.Errorf("read pending store item %s: %w", id, err)
		}
		it, ok := itemFromFields(id, fields)
		if !ok {
			// A pending entry whose record is gone: nothing holds its room.
			if err := s.rdb.ZRem(ctx, storePendingKey, id).Err(); err != nil {
				return out, fmt.Errorf("forget pending store item %s: %w", id, err)
			}
			continue
		}
		released, err := s.ReleaseItem(ctx, it.GroupID, id)
		if err != nil {
			return out, err
		}
		if released {
			out = append(out, it)
		}
	}
	return out, nil
}

// itemFromFields reads an item record. It reports false for a record that
// does not exist.
func itemFromFields(id string, f map[string]string) (Item, bool) {
	if len(f) == 0 {
		return Item{}, false
	}
	size, _ := strconv.ParseInt(f["size"], 10, 64)
	accounted, _ := strconv.ParseInt(f["accounted"], 10, 64)
	wrapEpoch, _ := strconv.ParseUint(f["wrap_epoch"], 10, 64)
	contentEpoch, _ := strconv.ParseUint(f["content_epoch"], 10, 64)
	rev, _ := strconv.ParseUint(f["meta_rev"], 10, 64)
	return Item{
		ID:           id,
		GroupID:      f["group_id"],
		Size:         size,
		Accounted:    accounted,
		WrapEpoch:    wrapEpoch,
		ContentID:    f["content_id"],
		ContentEpoch: contentEpoch,
		WrappedKeys:  []byte(f["keys"]),
		SealedMeta:   []byte(f["meta"]),
		MetaRev:      rev,
		Pending:      f["state"] != "committed",
		StartedAt:    msTime(f["started_at"]),
		SavedAt:      msTime(f["saved_at"]),
	}, true
}
