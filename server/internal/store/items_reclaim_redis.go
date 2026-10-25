package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// PendingBefore implements Items.
func (s *RedisStore) PendingBefore(ctx context.Context, t time.Time) ([]Item, error) {
	ids, err := s.rdb.ZRangeByScore(ctx, storePendingKey, &redis.ZRangeBy{Min: "-inf", Max: "(" + msString(t)}).Result()
	if err != nil {
		return nil, fmt.Errorf("list stale pending store items: %w", err)
	}
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		fields, err := s.rdb.HGetAll(ctx, storeItemKey(id)).Result()
		if err != nil {
			return out, fmt.Errorf("read pending store item %s: %w", id, err)
		}
		if it, ok := itemFromFields(id, fields); ok && it.Pending {
			out = append(out, it)
		}
	}
	return out, nil
}

// QueueReclaim implements ItemReclaim.
func (s *RedisStore) QueueReclaim(ctx context.Context, groupID string) error {
	if err := s.rdb.SAdd(ctx, storeReclaimKey, groupID).Err(); err != nil {
		return fmt.Errorf("queue store of group %s for reclaim: %w", groupID, err)
	}
	return nil
}

// Reclaiming implements ItemReclaim.
func (s *RedisStore) Reclaiming(ctx context.Context, groupID string) (bool, error) {
	ok, err := s.rdb.SIsMember(ctx, storeReclaimKey, groupID).Result()
	if err != nil {
		return false, fmt.Errorf("read reclaim state of group %s: %w", groupID, err)
	}
	return ok, nil
}

// ReclaimQueue implements ItemReclaim.
func (s *RedisStore) ReclaimQueue(ctx context.Context) ([]string, error) {
	ids, err := s.rdb.SMembers(ctx, storeReclaimKey).Result()
	if err != nil {
		return nil, fmt.Errorf("read store reclaim queue: %w", err)
	}
	return ids, nil
}

// ReclaimItems implements ItemReclaim.
func (s *RedisStore) ReclaimItems(ctx context.Context, groupID string, n int) (int, error) {
	got, err := reclaimItemsScript.Run(ctx, s.rdb, []string{storeIndexKey(groupID)}, storeItemPrefix, n).Int()
	if err != nil {
		return 0, fmt.Errorf("reclaim store items of group %s: %w", groupID, err)
	}
	return got, nil
}

// FinishReclaim implements ItemReclaim.
func (s *RedisStore) FinishReclaim(ctx context.Context, groupID string) error {
	code, err := finishReclaimScript.Run(ctx, s.rdb,
		[]string{storeIndexKey(groupID), storeUsageKeyOf(groupID), storeUsageKey, storePendingKey},
		groupID, storeItemPrefix,
	).Int()
	if err != nil {
		return fmt.Errorf("finish reclaim of group %s: %w", groupID, err)
	}
	if code != 0 {
		return fmt.Errorf("finish reclaim of group %s: %w", groupID, ErrStoreBusy)
	}
	return nil
}

// EndReclaim implements ItemReclaim.
func (s *RedisStore) EndReclaim(ctx context.Context, groupID string) error {
	if err := s.rdb.SRem(ctx, storeReclaimKey, groupID).Err(); err != nil {
		return fmt.Errorf("end reclaim of group %s: %w", groupID, err)
	}
	return nil
}

// blobRetryMember encodes a retry as "<group id>/<item id>"; neither id may
// hold a slash.
func blobRetryMember(r BlobRetry) string { return r.GroupID + "/" + r.ItemID }

// AddBlobRetry implements ItemReclaim.
func (s *RedisStore) AddBlobRetry(ctx context.Context, r BlobRetry) error {
	if err := s.rdb.SAdd(ctx, storeBlobRetryKey, blobRetryMember(r)).Err(); err != nil {
		return fmt.Errorf("queue blob of store item %s for retry: %w", r.ItemID, err)
	}
	return nil
}

// BlobRetries implements ItemReclaim.
func (s *RedisStore) BlobRetries(ctx context.Context) ([]BlobRetry, error) {
	members, err := s.rdb.SMembers(ctx, storeBlobRetryKey).Result()
	if err != nil {
		return nil, fmt.Errorf("read store blob retries: %w", err)
	}
	out := make([]BlobRetry, 0, len(members))
	for _, m := range members {
		if g, id, ok := strings.Cut(m, "/"); ok {
			out = append(out, BlobRetry{GroupID: g, ItemID: id})
		}
	}
	return out, nil
}

// RemoveBlobRetry implements ItemReclaim.
func (s *RedisStore) RemoveBlobRetry(ctx context.Context, r BlobRetry) error {
	if err := s.rdb.SRem(ctx, storeBlobRetryKey, blobRetryMember(r)).Err(); err != nil {
		return fmt.Errorf("forget blob retry of store item %s: %w", r.ItemID, err)
	}
	return nil
}
