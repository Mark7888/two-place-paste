package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// forEachAPIKeys runs a test against both implementations. The Redis one gets
// database db to itself: the key index is relay-wide, and the bound test
// fills it, so each test that uses it names a database no other test does.
func forEachAPIKeys(t *testing.T, db int, run func(t *testing.T, s store.APIKeys)) {
	t.Helper()

	t.Run("memory", func(t *testing.T) {
		t.Parallel()
		run(t, store.NewMemoryStore())
	})
	t.Run("redis", func(t *testing.T) {
		s := newTestRedisStoreDB(t, db)
		keys, ok := s.(store.APIKeys)
		if !ok {
			t.Fatal("RedisStore does not implement APIKeys")
		}
		clearAPIKeys(t, keys)
		run(t, keys)
	})
}

func clearAPIKeys(t *testing.T, s store.APIKeys) {
	t.Helper()
	ctx := context.Background()
	keys, err := s.ListAPIKeys(ctx)
	if err != nil {
		t.Fatalf("ListAPIKeys() error = %v", err)
	}
	for _, k := range keys {
		if err := s.DeleteAPIKey(ctx, k.ID); err != nil {
			t.Fatalf("DeleteAPIKey() error = %v", err)
		}
	}
}

func testKey(id string, created time.Time) store.APIKey {
	return store.APIKey{
		ID:        id,
		Name:      "key " + id,
		Hash:      []byte{1, 2, 3, byte(len(id))},
		Scopes:    []string{"read", "tokens"},
		CreatedAt: created,
		ExpiresAt: created.Add(90 * 24 * time.Hour),
		CreatedBy: "admin",
	}
}

func TestAPIKeys(t *testing.T) {
	t.Parallel()

	forEachAPIKeys(t, 8, func(t *testing.T, s store.APIKeys) {
		ctx := context.Background()
		base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

		older := testKey("aaaaaaaa", base)
		newer := testKey("bbbbbbbb", base.Add(time.Hour))
		for _, k := range []store.APIKey{older, newer} {
			if err := s.CreateAPIKey(ctx, k); err != nil {
				t.Fatalf("CreateAPIKey(%s) error = %v", k.ID, err)
			}
		}
		if err := s.CreateAPIKey(ctx, older); !errors.Is(err, store.ErrAPIKeyExists) {
			t.Errorf("CreateAPIKey(duplicate) error = %v, want %v", err, store.ErrAPIKeyExists)
		}

		got, err := s.GetAPIKey(ctx, older.ID)
		if err != nil {
			t.Fatalf("GetAPIKey() error = %v", err)
		}
		if got.Name != older.Name || !slices.Equal(got.Hash, older.Hash) || !slices.Equal(got.Scopes, older.Scopes) ||
			!got.CreatedAt.Equal(older.CreatedAt) || !got.ExpiresAt.Equal(older.ExpiresAt) || got.CreatedBy != "admin" ||
			!got.LastUsedAt.IsZero() || got.LastUsedIP != "" {
			t.Errorf("GetAPIKey() = %+v, want %+v", got, older)
		}

		list, err := s.ListAPIKeys(ctx)
		if err != nil {
			t.Fatalf("ListAPIKeys() error = %v", err)
		}
		ids := make([]string, 0, len(list))
		for _, k := range list {
			ids = append(ids, k.ID)
		}
		if want := []string{newer.ID, older.ID}; !slices.Equal(ids, want) {
			t.Errorf("ListAPIKeys() ids = %v, want %v (newest first)", ids, want)
		}

		used := base.Add(2 * time.Hour)
		if err := s.TouchAPIKey(ctx, older.ID, used, "203.0.113.7"); err != nil {
			t.Fatalf("TouchAPIKey() error = %v", err)
		}
		got, err = s.GetAPIKey(ctx, older.ID)
		if err != nil {
			t.Fatalf("GetAPIKey() error = %v", err)
		}
		if !got.LastUsedAt.Equal(used) || got.LastUsedIP != "203.0.113.7" {
			t.Errorf("after TouchAPIKey() last used = %v from %q, want %v from %q", got.LastUsedAt, got.LastUsedIP, used, "203.0.113.7")
		}

		if err := s.DeleteAPIKey(ctx, older.ID); err != nil {
			t.Fatalf("DeleteAPIKey() error = %v", err)
		}
		for name, err := range map[string]error{
			"GetAPIKey":    func() error { _, err := s.GetAPIKey(ctx, older.ID); return err }(),
			"DeleteAPIKey": s.DeleteAPIKey(ctx, older.ID),
			"TouchAPIKey":  s.TouchAPIKey(ctx, older.ID, used, "203.0.113.7"),
		} {
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s(deleted) error = %v, want %v", name, err, store.ErrNotFound)
			}
		}
		// A touch must not bring a deleted key back.
		if _, err := s.GetAPIKey(ctx, older.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetAPIKey() after touching a deleted key error = %v, want %v", err, store.ErrNotFound)
		}
	})
}

func TestAPIKeyLimit(t *testing.T) {
	t.Parallel()

	forEachAPIKeys(t, 7, func(t *testing.T, s store.APIKeys) {
		ctx := context.Background()
		base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
		for i := range store.MaxAPIKeys {
			if err := s.CreateAPIKey(ctx, testKey(fmt.Sprintf("k%07d", i), base)); err != nil {
				t.Fatalf("CreateAPIKey(%d) error = %v", i, err)
			}
		}
		if err := s.CreateAPIKey(ctx, testKey("overflow", base)); !errors.Is(err, store.ErrAPIKeyLimit) {
			t.Errorf("CreateAPIKey(over the limit) error = %v, want %v", err, store.ErrAPIKeyLimit)
		}
		if err := s.DeleteAPIKey(ctx, "k0000000"); err != nil {
			t.Fatalf("DeleteAPIKey() error = %v", err)
		}
		if err := s.CreateAPIKey(ctx, testKey("overflow", base)); err != nil {
			t.Errorf("CreateAPIKey() after freeing a slot error = %v", err)
		}
		clearAPIKeys(t, s)
	})
}

func TestAPIKeyExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		expires time.Time
		want    bool
	}{
		{"never", time.Time{}, false},
		{"later", now.Add(time.Second), false},
		{"now", now, true},
		{"earlier", now.Add(-time.Second), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := (store.APIKey{ExpiresAt: tt.expires}).Expired(now); got != tt.want {
				t.Errorf("Expired() = %v, want %v", got, tt.want)
			}
		})
	}
}
