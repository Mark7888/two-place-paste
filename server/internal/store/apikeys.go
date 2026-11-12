package store

import (
	"context"
	"errors"
	"time"
)

// API keys for the admin REST API (docs/internal/plans/admin-rest-api.md):
// one record per key, holding a hash of the key and never the key itself,
// plus an index of the ids. Records carry no Redis TTL even though keys
// expire: under `volatile-lru` a key with a TTL is evictable, and an evicted
// API key would stop working without anyone revoking it (D5). Expiry is a
// field the caller checks.

// MaxAPIKeys bounds how many API keys a relay holds (D8). The bound keeps the
// admin screen's list and the rate limiter's per-key state small.
const MaxAPIKeys = 50

// Sentinel errors of the API key records.
var (
	// ErrAPIKeyLimit is returned when MaxAPIKeys keys already exist.
	ErrAPIKeyLimit = errors.New("store: too many API keys")

	// ErrAPIKeyExists is returned for an id already in use. Ids are random,
	// so this means a collision; the caller mints another key.
	ErrAPIKeyExists = errors.New("store: API key id already in use")
)

// APIKey is one admin API key, as stored.
type APIKey struct {
	// ID is the public part of the key: it names the key in lists, logs and
	// the audit log.
	ID string

	// Name is the admin's label for the key.
	Name string

	// Hash is the SHA-256 of the whole key. The key itself is shown once,
	// when it is created, and stored nowhere.
	Hash []byte

	// Scopes are what the key may do: "read", "tokens", "admin".
	Scopes []string

	CreatedAt time.Time

	// ExpiresAt is when the key stops working. Zero means never.
	ExpiresAt time.Time

	// LastUsedAt and LastUsedIP record the key's most recent use, written at
	// most once a minute (D10). Zero and empty until it is first used.
	LastUsedAt time.Time
	LastUsedIP string

	// CreatedBy says where the key was created: "admin" (the admin page) or
	// "cli" (`tpp apikey create`).
	CreatedBy string
}

// Expired reports whether the key no longer works.
func (k APIKey) Expired(now time.Time) bool {
	return !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt)
}

// APIKeys is where API keys are kept. Implementations must be safe for
// concurrent use.
type APIKeys interface {
	// CreateAPIKey stores a new key. It returns ErrAPIKeyLimit when
	// MaxAPIKeys keys exist and ErrAPIKeyExists when the id is taken; both
	// are checked and the key written in one atomic step.
	CreateAPIKey(ctx context.Context, k APIKey) error

	// GetAPIKey returns one key by id, or ErrNotFound.
	GetAPIKey(ctx context.Context, id string) (APIKey, error)

	// ListAPIKeys returns every key, newest first.
	ListAPIKeys(ctx context.Context) ([]APIKey, error)

	// DeleteAPIKey removes a key, so it stops working on the next request.
	// It returns ErrNotFound for an unknown id.
	DeleteAPIKey(ctx context.Context, id string) error

	// TouchAPIKey records a use of the key. A key deleted meanwhile is left
	// deleted: it returns ErrNotFound rather than recreating the record.
	TouchAPIKey(ctx context.Context, id string, at time.Time, ip string) error
}
