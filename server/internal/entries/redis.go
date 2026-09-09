package entries

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis key layout for the ephemeral zone (SPEC §4.2).
const (
	entryPrefix       = "entry:"
	entryBlobSuffix   = ":blob"
	groupEntriesInfix = ":entries"
	groupPrefix       = "group:"
)

func entryKey(id string) string        { return entryPrefix + id }
func entryBlobKey(id string) string    { return entryPrefix + id + entryBlobSuffix }
func groupEntriesKey(id string) string { return groupPrefix + id + groupEntriesInfix }

// RedisStore is the Store implementation backed by Redis.
type RedisStore struct {
	rdb redis.Cmdable
	now func() time.Time
}

// NewRedisStore returns an ephemeral-zone store backed by rdb.
func NewRedisStore(rdb redis.Cmdable) *RedisStore {
	return &RedisStore{rdb: rdb, now: func() time.Time { return time.Now().UTC() }}
}

var _ Store = (*RedisStore)(nil)

// Put implements Store.
func (s *RedisStore) Put(ctx context.Context, m Meta, inline []byte) error {
	ttl := time.Until(m.ExpiresAt)
	if ttl <= 0 {
		return fmt.Errorf("put entry %s: expiry is already past", m.ID)
	}

	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, entryKey(m.ID),
		"group_id", m.GroupID,
		"epoch", strconv.FormatUint(m.Epoch, 10),
		"size", strconv.FormatInt(m.Size, 10),
		"created_at", strconv.FormatInt(m.CreatedAt.UTC().UnixMilli(), 10),
		"expires_at", strconv.FormatInt(m.ExpiresAt.UTC().UnixMilli(), 10),
		"storage_ref", m.StorageRef,
	)
	pipe.PExpireAt(ctx, entryKey(m.ID), m.ExpiresAt)
	if m.Inline() {
		pipe.Set(ctx, entryBlobKey(m.ID), inline, 0)
		pipe.PExpireAt(ctx, entryBlobKey(m.ID), m.ExpiresAt)
	}
	pipe.ZAdd(ctx, groupEntriesKey(m.GroupID), redis.Z{
		Score:  float64(m.CreatedAt.UTC().UnixMilli()),
		Member: m.ID,
	})
	// The index has no TTL of its own, so it is pruned by score on every
	// write: an id whose entry has expired can never be returned.
	pipe.ZRemRangeByScore(ctx, groupEntriesKey(m.GroupID),
		"-inf", "("+strconv.FormatInt(s.now().Add(-entryTTLBound).UnixMilli(), 10))

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("put entry %s: %w", m.ID, err)
	}
	return nil
}

// entryTTLBound is the widest lifetime any entry can have. It bounds the index
// prune above; the authoritative expiry is the TTL on each entry key.
const entryTTLBound = 24 * time.Hour

// Latest implements Store.
func (s *RedisStore) Latest(ctx context.Context, groupID string) (Meta, error) {
	ids, err := s.rdb.ZRevRange(ctx, groupEntriesKey(groupID), 0, 15).Result()
	if err != nil {
		return Meta{}, fmt.Errorf("latest entry of group %s: %w", groupID, err)
	}
	// The newest id whose record still exists. A gap means Redis expired or
	// evicted the record between the index write and now, which is the
	// ephemeral zone behaving exactly as designed.
	for _, id := range ids {
		m, err := s.Get(ctx, groupID, id)
		if err == nil {
			return m, nil
		}
		if !isNotFound(err) {
			return Meta{}, err
		}
	}
	return Meta{}, ErrNotFound
}

// History implements Store.
func (s *RedisStore) History(ctx context.Context, groupID string, limit int, before time.Time) ([]Meta, time.Time, error) {
	maxScore := "+inf"
	if !before.IsZero() {
		maxScore = "(" + strconv.FormatInt(before.UTC().UnixMilli(), 10)
	}

	ids, err := s.rdb.ZRevRangeByScore(ctx, groupEntriesKey(groupID), &redis.ZRangeBy{
		Min:   "-inf",
		Max:   maxScore,
		Count: int64(limit),
	}).Result()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("history of group %s: %w", groupID, err)
	}

	metas := make([]Meta, 0, len(ids))
	for _, id := range ids {
		m, err := s.Get(ctx, groupID, id)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, time.Time{}, err
		}
		metas = append(metas, m)
	}

	// A full page means there may be more; a short one means the end.
	var next time.Time
	if len(ids) == limit && len(metas) > 0 {
		next = metas[len(metas)-1].CreatedAt
	}
	return metas, next, nil
}

// Get implements Store.
func (s *RedisStore) Get(ctx context.Context, groupID, entryID string) (Meta, error) {
	fields, err := s.rdb.HGetAll(ctx, entryKey(entryID)).Result()
	if err != nil {
		return Meta{}, fmt.Errorf("get entry %s: %w", entryID, err)
	}
	if len(fields) == 0 {
		return Meta{}, ErrNotFound
	}
	m, err := metaFromFields(entryID, fields)
	if err != nil {
		return Meta{}, err
	}
	if m.GroupID != groupID {
		// Not a permission error to the caller: an entry of another group is
		// indistinguishable from one that never existed.
		return Meta{}, ErrNotFound
	}
	return m, nil
}

// InlineBody implements Store.
func (s *RedisStore) InlineBody(ctx context.Context, entryID string) ([]byte, error) {
	body, err := s.rdb.Get(ctx, entryBlobKey(entryID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get inline body of entry %s: %w", entryID, err)
	}
	return body, nil
}

// EntryRefs implements Store.
func (s *RedisStore) EntryRefs(ctx context.Context) (map[string]string, error) {
	refs := make(map[string]string)
	var cursor uint64
	for {
		keys, next, err := s.rdb.Scan(ctx, cursor, entryPrefix+"*", 256).Result()
		if err != nil {
			return nil, fmt.Errorf("scan entries: %w", err)
		}
		for _, key := range keys {
			if strings.HasSuffix(key, entryBlobSuffix) {
				continue
			}
			ref, err := s.rdb.HGet(ctx, key, "storage_ref").Result()
			if errors.Is(err, redis.Nil) || ref == "" {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("read storage ref of %s: %w", key, err)
			}
			refs[ref] = strings.TrimPrefix(key, entryPrefix)
		}
		if next == 0 {
			return refs, nil
		}
		cursor = next
	}
}

func metaFromFields(entryID string, fields map[string]string) (Meta, error) {
	epoch, err := strconv.ParseUint(fields["epoch"], 10, 64)
	if err != nil {
		return Meta{}, fmt.Errorf("get entry %s: parse epoch: %w", entryID, err)
	}
	size, err := strconv.ParseInt(fields["size"], 10, 64)
	if err != nil {
		return Meta{}, fmt.Errorf("get entry %s: parse size: %w", entryID, err)
	}
	createdAt, err := strconv.ParseInt(fields["created_at"], 10, 64)
	if err != nil {
		return Meta{}, fmt.Errorf("get entry %s: parse created_at: %w", entryID, err)
	}
	expiresAt, err := strconv.ParseInt(fields["expires_at"], 10, 64)
	if err != nil {
		return Meta{}, fmt.Errorf("get entry %s: parse expires_at: %w", entryID, err)
	}
	return Meta{
		ID:         entryID,
		GroupID:    fields["group_id"],
		Epoch:      epoch,
		Size:       size,
		CreatedAt:  time.UnixMilli(createdAt).UTC(),
		ExpiresAt:  time.UnixMilli(expiresAt).UTC(),
		StorageRef: fields["storage_ref"],
	}, nil
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
