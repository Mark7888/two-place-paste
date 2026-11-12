package store

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var _ APIKeys = (*RedisStore)(nil)

// createAPIKeyScript checks the bound and the id and writes the key in one
// step, so two admins creating the fiftieth key at once get one key, not two.
//
// Return codes: 0 created, 1 id in use, 2 too many keys.
//
//	KEYS[1] key record    ARGV[1] max keys
//	KEYS[2] key index     ARGV[2] id
//	                      ARGV[3...] field, value pairs
var createAPIKeyScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return 1 end
if redis.call('SCARD', KEYS[2]) >= tonumber(ARGV[1]) then return 2 end
redis.call('HSET', KEYS[1], unpack(ARGV, 3))
redis.call('SADD', KEYS[2], ARGV[2])
return 0
`)

// CreateAPIKey implements APIKeys.
func (s *RedisStore) CreateAPIKey(ctx context.Context, k APIKey) error {
	args := []any{MaxAPIKeys, k.ID,
		"id", k.ID,
		"name", k.Name,
		"hash", hex.EncodeToString(k.Hash),
		"scopes", strings.Join(k.Scopes, " "),
		"created_at", msString(k.CreatedAt),
		"expires_at", msString(k.ExpiresAt),
		"last_used_at", msString(k.LastUsedAt),
		"last_used_ip", k.LastUsedIP,
		"created_by", k.CreatedBy,
	}
	// Persistent zone: no TTL, even though the key expires (see apikeys.go).
	code, err := createAPIKeyScript.Run(ctx, s.rdb, []string{apiKeyKey(k.ID), apiKeyIndexKey}, args...).Int64()
	if err != nil {
		return fmt.Errorf("create API key %s: %w", k.ID, err)
	}
	switch code {
	case 1:
		return ErrAPIKeyExists
	case 2:
		return ErrAPIKeyLimit
	}
	return nil
}

// GetAPIKey implements APIKeys.
func (s *RedisStore) GetAPIKey(ctx context.Context, id string) (APIKey, error) {
	if id == "" {
		return APIKey{}, ErrNotFound
	}
	// The id the caller passes may come straight from a request header, so
	// neither the error nor the record repeats it: the record's id is the one
	// stored with it, which callers may log.
	fields, err := s.rdb.HGetAll(ctx, apiKeyKey(id)).Result()
	if err != nil {
		return APIKey{}, fmt.Errorf("get API key: %w", err)
	}
	if len(fields) == 0 || fields["id"] != id {
		return APIKey{}, ErrNotFound
	}
	hash, err := hex.DecodeString(fields["hash"])
	if err != nil {
		return APIKey{}, fmt.Errorf("get API key %s: malformed hash: %w", fields["id"], err)
	}
	return APIKey{
		ID:         fields["id"],
		Name:       fields["name"],
		Hash:       hash,
		Scopes:     strings.Fields(fields["scopes"]),
		CreatedAt:  msTime(fields["created_at"]),
		ExpiresAt:  msTime(fields["expires_at"]),
		LastUsedAt: msTime(fields["last_used_at"]),
		LastUsedIP: fields["last_used_ip"],
		CreatedBy:  fields["created_by"],
	}, nil
}

// ListAPIKeys implements APIKeys.
func (s *RedisStore) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	ids, err := s.rdb.SMembers(ctx, apiKeyIndexKey).Result()
	if err != nil {
		return nil, fmt.Errorf("list API keys: %w", err)
	}
	out := make([]APIKey, 0, len(ids))
	for _, id := range ids {
		k, err := s.GetAPIKey(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list API keys: %w", err)
		}
		out = append(out, k)
	}
	sortAPIKeys(out)
	return out, nil
}

// DeleteAPIKey implements APIKeys.
func (s *RedisStore) DeleteAPIKey(ctx context.Context, id string) error {
	pipe := s.rdb.TxPipeline()
	del := pipe.Del(ctx, apiKeyKey(id))
	pipe.SRem(ctx, apiKeyIndexKey, id)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete API key %s: %w", id, err)
	}
	if del.Val() == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchAPIKey implements APIKeys.
func (s *RedisStore) TouchAPIKey(ctx context.Context, id string, at time.Time, ip string) error {
	if err := s.setIfExists(ctx, apiKeyKey(id), "last_used_at", msString(at), "last_used_ip", ip); err != nil {
		return fmt.Errorf("touch API key %s: %w", id, err)
	}
	return nil
}
