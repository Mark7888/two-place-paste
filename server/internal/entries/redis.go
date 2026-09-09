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

// putEntryScript writes one entry, or refuses because its id is taken.
//
// It is a script rather than a transaction pipeline because a pipeline cannot
// decide: the existence check and the writes must be one step, or two clients
// choosing the same id could both believe they wrote it. Entry ids are global
// keys, so this is also what stops a client-chosen id from overwriting another
// group's entry.
//
// Return codes: 0 written, 1 the id is already in use.
//
//	KEYS[1] entry key         ARGV[1] group id      ARGV[6] storage ref
//	KEYS[2] entry blob key    ARGV[2] epoch         ARGV[7] "1" when inline
//	KEYS[3] group index key   ARGV[3] size          ARGV[8] inline body
//	                          ARGV[4] created_at    ARGV[9] index prune cutoff
//	                          ARGV[5] expires_at
var putEntryScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 1 end

redis.call('HSET', KEYS[1],
  'group_id', ARGV[1],
  'epoch', ARGV[2],
  'size', ARGV[3],
  'created_at', ARGV[4],
  'expires_at', ARGV[5],
  'storage_ref', ARGV[6])
redis.call('PEXPIREAT', KEYS[1], ARGV[5])

if ARGV[7] == '1' then
  redis.call('SET', KEYS[2], ARGV[8])
  redis.call('PEXPIREAT', KEYS[2], ARGV[5])
end

redis.call('ZADD', KEYS[3], ARGV[4], ARGV[1 + 9])
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', ARGV[9])

return 0
`)

// Put implements Store.
func (s *RedisStore) Put(ctx context.Context, m Meta, inline []byte) error {
	ttl := time.Until(m.ExpiresAt)
	if ttl <= 0 {
		return fmt.Errorf("put entry %s: expiry is already past", m.ID)
	}

	expiresAt := strconv.FormatInt(m.ExpiresAt.UTC().UnixMilli(), 10)
	createdAt := strconv.FormatInt(m.CreatedAt.UTC().UnixMilli(), 10)
	inlineFlag := "0"
	if m.Inline() {
		inlineFlag = "1"
	}
	// The index has no TTL of its own, so it is pruned by score on every
	// write: an id whose entry has expired can never be returned.
	prune := "(" + strconv.FormatInt(s.now().Add(-entryTTLBound).UnixMilli(), 10)

	res, err := putEntryScript.Run(ctx, s.rdb,
		[]string{entryKey(m.ID), entryBlobKey(m.ID), groupEntriesKey(m.GroupID)},
		m.GroupID,
		strconv.FormatUint(m.Epoch, 10),
		strconv.FormatInt(m.Size, 10),
		createdAt,
		expiresAt,
		m.StorageRef,
		inlineFlag,
		inline,
		prune,
		m.ID,
	).Int64()
	if err != nil {
		return fmt.Errorf("put entry %s: %w", m.ID, err)
	}
	if res == 1 {
		return fmt.Errorf("put entry %s: %w", m.ID, ErrEntryExists)
	}
	return nil
}

// entryTTLBound is the widest lifetime any entry can have. It bounds the index
// prune above; the authoritative expiry is the TTL on each entry key.
const entryTTLBound = 24 * time.Hour

// Exists implements Store.
//
// An expired entry answers false without any pruning of its own: Redis has
// already removed the key by its TTL, which is the only expiry authority in
// this zone.
func (s *RedisStore) Exists(ctx context.Context, entryID string) (bool, error) {
	n, err := s.rdb.Exists(ctx, entryKey(entryID)).Result()
	if err != nil {
		return false, fmt.Errorf("check entry %s: %w", entryID, err)
	}
	return n > 0, nil
}

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
