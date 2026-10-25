package store

import (
	"context"
	"fmt"
	"strconv"
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
