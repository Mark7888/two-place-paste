package entries_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Mark7888/two-place-paste/server/internal/entries"
)

func newTestRedisStore(t *testing.T) entries.Store {
	t.Helper()

	addr := os.Getenv("TPP_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis not available at %s: %v", addr, err)
	}
	return entries.NewRedisStore(rdb)
}

// Both implementations answer the same questions: the WebSocket tests run
// against the in-memory one, the deployment against Redis.
func forEachStore(t *testing.T, run func(t *testing.T, s entries.Store)) {
	t.Helper()

	t.Run("memory", func(t *testing.T) {
		t.Parallel()
		run(t, entries.NewMemoryStore())
	})
	t.Run("redis", func(t *testing.T) {
		t.Parallel()
		run(t, newTestRedisStore(t))
	})
}

// newGroupID keeps runs independent: the integration Redis is shared between
// tests and between runs, so every test invents its own group.
func newGroupID(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("crypto/rand: %v", err)
	}
	return "group-" + base64.RawURLEncoding.EncodeToString(b[:])
}

func meta(groupID, id string, createdAt time.Time) entries.Meta {
	return entries.Meta{
		ID:        id,
		GroupID:   groupID,
		Epoch:     1,
		Size:      4,
		CreatedAt: createdAt,
		ExpiresAt: createdAt.Add(24 * time.Hour),
	}
}

func TestStoreLatestAndHistory(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s entries.Store) {
		ctx := context.Background()
		gid := newGroupID(t)
		other := gid + "-other"
		base := time.Now().UTC().Truncate(time.Millisecond)

		if _, err := s.Latest(ctx, gid); !errors.Is(err, entries.ErrNotFound) {
			t.Errorf("Latest(empty group) error = %v, want %v", err, entries.ErrNotFound)
		}

		for i, id := range []string{"e1", "e2", "e3"} {
			m := meta(gid, gid+"-"+id, base.Add(time.Duration(i)*time.Second))
			if err := s.Put(ctx, m, []byte(id)); err != nil {
				t.Fatalf("Put() error = %v", err)
			}
		}
		if err := s.Put(ctx, meta(other, other+"-e1", base.Add(time.Hour)), []byte("x")); err != nil {
			t.Fatalf("Put() error = %v", err)
		}

		// Last write to reach the server wins (SPEC §6).
		latest, err := s.Latest(ctx, gid)
		if err != nil {
			t.Fatalf("Latest() error = %v", err)
		}
		if latest.ID != gid+"-e3" {
			t.Errorf("Latest().ID = %q, want %q", latest.ID, gid+"-e3")
		}

		body, err := s.InlineBody(ctx, latest.ID)
		if err != nil {
			t.Fatalf("InlineBody() error = %v", err)
		}
		if string(body) != "e3" {
			t.Errorf("InlineBody() = %q, want %q", body, "e3")
		}

		page, next, err := s.History(ctx, gid, 2, time.Time{})
		if err != nil {
			t.Fatalf("History() error = %v", err)
		}
		if len(page) != 2 || page[0].ID != gid+"-e3" || page[1].ID != gid+"-e2" {
			t.Fatalf("History() = %v, want e3 then e2 newest first", ids(page))
		}
		if next.IsZero() {
			t.Fatal("History() returned a zero cursor after a full page, want a cursor to continue from")
		}

		page, next, err = s.History(ctx, gid, 2, next)
		if err != nil {
			t.Fatalf("History(page 2) error = %v", err)
		}
		if len(page) != 1 || page[0].ID != gid+"-e1" {
			t.Errorf("History(page 2) = %v, want just e1", ids(page))
		}
		if !next.IsZero() {
			t.Errorf("History(page 2) cursor = %v, want the zero time at the end of the listing", next)
		}

		// Entry ids are not capabilities: another group's entry is simply
		// absent, not forbidden.
		if _, err := s.Get(ctx, gid, other+"-e1"); !errors.Is(err, entries.ErrNotFound) {
			t.Errorf("Get(another group's entry) error = %v, want %v", err, entries.ErrNotFound)
		}
	})
}

func TestStoreEntryRefs(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s entries.Store) {
		ctx := context.Background()
		gid := newGroupID(t)
		now := time.Now().UTC()

		inline := meta(gid, gid+"-inline", now)
		if err := s.Put(ctx, inline, []byte("body")); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		stored := meta(gid, gid+"-stored", now)
		stored.StorageRef = "2099010100/" + stored.ID + ".bin"
		if err := s.Put(ctx, stored, nil); err != nil {
			t.Fatalf("Put() error = %v", err)
		}

		refs, err := s.EntryRefs(ctx)
		if err != nil {
			t.Fatalf("EntryRefs() error = %v", err)
		}
		if got := refs[stored.StorageRef]; got != stored.ID {
			t.Errorf("EntryRefs()[%q] = %q, want %q", stored.StorageRef, got, stored.ID)
		}
		for ref, id := range refs {
			if id == inline.ID {
				t.Errorf("EntryRefs() reported %q for an inline entry, want inline entries to be absent", ref)
			}
		}
	})
}

func ids(metas []entries.Meta) []string {
	out := make([]string, len(metas))
	for i, m := range metas {
		out[i] = m.ID
	}
	return out
}

func TestStoreRejectsExpiredWrite(t *testing.T) {
	t.Parallel()

	s := newTestRedisStore(t)
	m := meta(newGroupID(t), "entry-expired", time.Now().UTC().Add(-48*time.Hour))
	if err := s.Put(context.Background(), m, []byte("x")); err == nil {
		t.Error("Put(already expired) error = nil, want a refusal: a TTL is never extended and never resurrected")
	}
}

func TestStoreExpiredEntryIsNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := entries.NewMemoryStore()
	gid := newGroupID(t)

	m := meta(gid, "entry-ttl", time.Now().UTC().Add(-25*time.Hour))
	if err := s.Put(ctx, m, []byte("x")); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if _, err := s.Latest(ctx, gid); !errors.Is(err, entries.ErrNotFound) {
		t.Errorf("Latest(expired) error = %v, want %v", err, entries.ErrNotFound)
	}
	if _, err := s.Get(ctx, gid, m.ID); !errors.Is(err, entries.ErrNotFound) {
		t.Errorf("Get(expired) error = %v, want %v", err, entries.ErrNotFound)
	}
}

func TestReadersSeeWhatWasWritten(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s entries.Store) {
		ctx := context.Background()
		gid := newGroupID(t)
		m := meta(gid, gid+"-e", time.Now().UTC().Truncate(time.Millisecond))
		m.Epoch = 7
		m.Size = int64(len("ciphertext"))

		if err := s.Put(ctx, m, []byte("ciphertext")); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		got, err := s.Get(ctx, gid, m.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got.Epoch != 7 || got.Size != m.Size || !got.Inline() {
			t.Errorf("Get() = %+v, want epoch 7, size %d, inline", got, m.Size)
		}
		if !got.CreatedAt.Equal(m.CreatedAt) {
			t.Errorf("Get().CreatedAt = %v, want %v", got.CreatedAt, m.CreatedAt)
		}
		if got.GroupID != gid {
			t.Errorf("Get().GroupID = %q, want %q", got.GroupID, gid)
		}
	})
}
