package quota_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Mark7888/two-place-paste/server/internal/quota"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// clock is a settable time source shared by a ledger and its test.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func redisAddr() string {
	if addr := os.Getenv("TPP_TEST_REDIS_ADDR"); addr != "" {
		return addr
	}
	return "127.0.0.1:6379"
}

func newRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := redisAddr()
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis not available at %s: %v", addr, err)
	}
	return rdb
}

func namespace(t *testing.T) string {
	t.Helper()
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "test-quota-" + hex.EncodeToString(b[:]) + ":"
}

// forEachLedger runs the same questions against both ledgers: Memory is what
// the transport tests use, Redis is what ships, and they must not drift.
func forEachLedger(t *testing.T, limits quota.Limits, run func(t *testing.T, l quota.Ledger, clk *clock)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		t.Parallel()
		clk := &clock{t: t0}
		run(t, quota.NewMemory(quota.Options{Limits: limits, Now: clk.now}), clk)
	})
	t.Run("redis", func(t *testing.T) {
		t.Parallel()
		clk := &clock{t: t0}
		run(t, quota.NewRedisNamespaced(newRedisClient(t), quota.Options{Limits: limits, Now: clk.now}, namespace(t)), clk)
	})
}

// put reserves and commits an entry created at t0+offset.
func put(t *testing.T, l quota.Ledger, id, group string, size int64, offset time.Duration) []quota.Victim {
	t.Helper()
	ctx := context.Background()
	victims, err := l.Reserve(ctx, quota.Reservation{
		EntryID: id, GroupID: group, Size: size, ExpiresAt: t0.Add(24*time.Hour + offset),
	})
	if err != nil {
		t.Fatalf("Reserve(%s) error = %v", id, err)
	}
	if err := l.Commit(ctx, id); err != nil {
		t.Fatalf("Commit(%s) error = %v", id, err)
	}
	return victims
}

func victimIDs(vs []quota.Victim) []string {
	ids := make([]string, len(vs))
	for i, v := range vs {
		ids[i] = v.EntryID
	}
	return ids
}

func wantUsage(t *testing.T, l quota.Ledger, group string, bytes, entries int64) {
	t.Helper()
	var (
		u   quota.Usage
		err error
	)
	if group == "" {
		u, err = l.Usage(context.Background())
	} else {
		u, err = l.GroupUsage(context.Background(), group)
	}
	if err != nil {
		t.Fatalf("usage(%q) error = %v", group, err)
	}
	if u.Bytes != bytes || u.Entries != entries {
		t.Errorf("usage(%q) = %d bytes / %d entries, want %d / %d", group, u.Bytes, u.Entries, bytes, entries)
	}
}

func TestAccounting(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{}, func(t *testing.T, l quota.Ledger, _ *clock) {
		ctx := context.Background()
		put(t, l, "a", "g1", 100, 0)
		put(t, l, "b", "g1", 50, time.Minute)
		put(t, l, "c", "g2", 7, 2*time.Minute)

		wantUsage(t, l, "", 157, 3)
		wantUsage(t, l, "g1", 150, 2)
		wantUsage(t, l, "g2", 7, 1)
		wantUsage(t, l, "nobody", 0, 0)

		if _, err := l.Reserve(ctx, quota.Reservation{EntryID: "a", GroupID: "g2", Size: 1, ExpiresAt: t0.Add(time.Hour)}); !errors.Is(err, quota.ErrExists) {
			t.Errorf("Reserve(duplicate id) error = %v, want %v", err, quota.ErrExists)
		}

		if err := l.Release(ctx, "a"); err != nil {
			t.Fatalf("Release() error = %v", err)
		}
		if err := l.Release(ctx, "never-there"); err != nil {
			t.Errorf("Release(unknown) error = %v, want nil", err)
		}
		wantUsage(t, l, "", 57, 2)
		wantUsage(t, l, "g1", 50, 1)
	})
}

// TestHugeSizes guards the Lua side: a size Lua would print in exponent form
// must still reach INCRBY as an integer.
func TestHugeSizes(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{}, func(t *testing.T, l quota.Ledger, _ *clock) {
		const huge = int64(1) << 52
		put(t, l, "big", "g", huge, 0)
		wantUsage(t, l, "", huge, 1)
		if err := l.Release(context.Background(), "big"); err != nil {
			t.Fatalf("Release() error = %v", err)
		}
		wantUsage(t, l, "", 0, 0)
	})
}

func TestGroupLimitEvictsOwnOldest(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{GroupBytes: 100}, func(t *testing.T, l quota.Ledger, _ *clock) {
		put(t, l, "g1-old", "g1", 40, 0)
		put(t, l, "g2-older", "g2", 90, -time.Minute)
		put(t, l, "g1-mid", "g1", 40, time.Minute)

		// 80 + 50 > 100: g1's oldest goes; g2's older entry is not g1's to evict.
		v := put(t, l, "g1-new", "g1", 50, 2*time.Minute)
		if got := victimIDs(v); len(got) != 1 || got[0] != "g1-old" {
			t.Fatalf("victims = %v, want [g1-old]", got)
		}
		if v[0].GroupID != "g1" || v[0].Size != 40 {
			t.Errorf("victim = %+v, want group g1, size 40", v[0])
		}
		wantUsage(t, l, "g1", 90, 2)
		wantUsage(t, l, "g2", 90, 1)

		// Exactly at the limit fits without evicting.
		if v := put(t, l, "g1-fill", "g1", 10, 3*time.Minute); len(v) != 0 {
			t.Errorf("victims at exactly the limit = %v, want none", victimIDs(v))
		}
	})
}

func TestTotalLimitEvictsGlobalOldest(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{TotalBytes: 100, GroupBytes: 80}, func(t *testing.T, l quota.Ledger, _ *clock) {
		put(t, l, "g2-a", "g2", 30, 0)
		put(t, l, "g1-a", "g1", 30, time.Minute)
		put(t, l, "g2-b", "g2", 30, 2*time.Minute)

		// Group g1 is well under its limit, but the total is not: the oldest
		// entry anywhere goes first, then the next.
		v := put(t, l, "g1-b", "g1", 45, 3*time.Minute)
		if got := victimIDs(v); len(got) != 2 || got[0] != "g2-a" || got[1] != "g1-a" {
			t.Fatalf("victims = %v, want [g2-a g1-a]", got)
		}
		wantUsage(t, l, "", 75, 2)
		wantUsage(t, l, "g1", 45, 1)
		wantUsage(t, l, "g2", 30, 1)
	})
}

func TestGroupThenTotal(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{TotalBytes: 100, GroupBytes: 60}, func(t *testing.T, l quota.Ledger, _ *clock) {
		put(t, l, "other", "g2", 40, 0)
		put(t, l, "mine-1", "g1", 30, time.Minute)
		put(t, l, "mine-2", "g1", 30, 2*time.Minute)

		// Group: 60 + 40 > 60 evicts mine-1 and mine-2 (each 30). Total is
		// then 40 + 40 = 80, so g2 keeps its entry.
		v := put(t, l, "mine-3", "g1", 40, 3*time.Minute)
		if got := victimIDs(v); len(got) != 2 || got[0] != "mine-1" || got[1] != "mine-2" {
			t.Fatalf("victims = %v, want [mine-1 mine-2]", got)
		}
		wantUsage(t, l, "", 80, 2)
	})
}

func TestTooLargeAndExceeded(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{TotalBytes: 100, GroupBytes: 60}, func(t *testing.T, l quota.Ledger, _ *clock) {
		ctx := context.Background()
		r := func(id, g string, size int64) quota.Reservation {
			return quota.Reservation{EntryID: id, GroupID: g, Size: size, ExpiresAt: t0.Add(24 * time.Hour)}
		}

		if _, err := l.Reserve(ctx, r("big", "g1", 61)); !errors.Is(err, quota.ErrTooLarge) {
			t.Errorf("Reserve(over group limit) error = %v, want %v", err, quota.ErrTooLarge)
		}

		// A pending entry is an upload in flight and is never evicted: a
		// second upload that needs its room is refused, and nothing changes.
		if _, err := l.Reserve(ctx, r("inflight", "g1", 50)); err != nil {
			t.Fatalf("Reserve() error = %v", err)
		}
		if _, err := l.Reserve(ctx, r("second", "g1", 20)); !errors.Is(err, quota.ErrExceeded) {
			t.Errorf("Reserve(needs a pending entry's room) error = %v, want %v", err, quota.ErrExceeded)
		}
		wantUsage(t, l, "g1", 50, 1)

		// Same on the total limit, with the room held in another group.
		if _, err := l.Reserve(ctx, r("other-inflight", "g2", 45)); err != nil {
			t.Fatalf("Reserve() error = %v", err)
		}
		if _, err := l.Reserve(ctx, r("third", "g3", 10)); !errors.Is(err, quota.ErrExceeded) {
			t.Errorf("Reserve(over total, all pending) error = %v, want %v", err, quota.ErrExceeded)
		}
		wantUsage(t, l, "", 95, 2)

		// Once committed, they are evictable like anything else.
		if err := l.Commit(ctx, "inflight"); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
		v, err := l.Reserve(ctx, r("third", "g3", 10))
		if err != nil {
			t.Fatalf("Reserve() after commit error = %v", err)
		}
		if got := victimIDs(v); len(got) != 1 || got[0] != "inflight" {
			t.Errorf("victims = %v, want [inflight]", got)
		}
	})
}

func TestExpire(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{TotalBytes: 100}, func(t *testing.T, l quota.Ledger, clk *clock) {
		ctx := context.Background()
		put(t, l, "first", "g", 60, 0)
		put(t, l, "second", "g", 30, time.Hour)

		n, err := l.Expire(ctx, t0.Add(24*time.Hour))
		if err != nil {
			t.Fatalf("Expire() error = %v", err)
		}
		if n != 1 {
			t.Errorf("Expire() forgot %d, want 1", n)
		}
		wantUsage(t, l, "", 30, 1)
		wantUsage(t, l, "g", 30, 1)

		// Reserve forgets what has expired by its own clock before deciding,
		// so an expired entry is never reported as a victim.
		clk.set(t0.Add(26 * time.Hour))
		v, err := l.Reserve(ctx, quota.Reservation{EntryID: "third", GroupID: "g", Size: 100, ExpiresAt: t0.Add(50 * time.Hour)})
		if err != nil {
			t.Fatalf("Reserve() error = %v", err)
		}
		if len(v) != 0 {
			t.Errorf("victims = %v, want none: the only other entry had expired", victimIDs(v))
		}
		wantUsage(t, l, "", 100, 1)
	})
}

func TestRebuild(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{GroupBytes: 100}, func(t *testing.T, l quota.Ledger, _ *clock) {
		ctx := context.Background()
		put(t, l, "stale", "g1", 99, 0)

		err := l.Rebuild(ctx, []quota.Reservation{
			{EntryID: "x", GroupID: "g1", Size: 10, ExpiresAt: t0.Add(24 * time.Hour)},
			{EntryID: "y", GroupID: "g2", Size: 20, ExpiresAt: t0.Add(25 * time.Hour)},
		})
		if err != nil {
			t.Fatalf("Rebuild() error = %v", err)
		}
		wantUsage(t, l, "", 30, 2)
		wantUsage(t, l, "g1", 10, 1)
		wantUsage(t, l, "g2", 20, 1)

		// Rebuilt entries are committed, so they are evictable.
		v := put(t, l, "z", "g1", 95, time.Hour)
		if got := victimIDs(v); len(got) != 1 || got[0] != "x" {
			t.Errorf("victims = %v, want [x]", got)
		}
	})
}

// TestReserveConcurrent is what the script is for: many uploads racing for the
// last free bytes, and the limit still holds afterwards.
func TestReserveConcurrent(t *testing.T) {
	t.Parallel()
	const limit = 1000
	l := quota.NewRedisNamespaced(newRedisClient(t), quota.Options{Limits: quota.Limits{TotalBytes: limit}}, namespace(t))
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("e%03d", i)
			_, err := l.Reserve(ctx, quota.Reservation{
				EntryID: id, GroupID: fmt.Sprintf("g%d", i%7), Size: 37, ExpiresAt: time.Now().Add(time.Hour),
			})
			if err != nil {
				errs <- err
				return
			}
			if err := l.Commit(ctx, id); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, quota.ErrExceeded) {
			t.Errorf("Reserve() error = %v, want nil or %v", err, quota.ErrExceeded)
		}
	}

	u, err := l.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage() error = %v", err)
	}
	if u.Bytes > limit || u.Bytes != u.Entries*37 {
		t.Errorf("usage after the race = %d bytes / %d entries; want ≤ %d and consistent", u.Bytes, u.Entries, limit)
	}
}

func TestEvictionStats(t *testing.T) {
	t.Parallel()
	forEachLedger(t, quota.Limits{GroupBytes: 100}, func(t *testing.T, l quota.Ledger, clk *clock) {
		ctx := context.Background()
		put(t, l, "a", "g", 60, 0)
		put(t, l, "b", "g", 30, time.Minute)
		put(t, l, "c", "g", 70, 2*time.Minute) // evicts a (60)

		clk.set(t0.Add(3 * time.Hour))
		put(t, l, "d", "g", 90, 3*time.Hour) // evicts b (30) and c (70)

		ev, err := l.Evictions(ctx)
		if err != nil {
			t.Fatalf("Evictions() error = %v", err)
		}
		if ev.Count != 3 || ev.Bytes != 160 {
			t.Errorf("Evictions() = %+v, want 3 entries / 160 bytes", ev)
		}

		// A day later, the first hour has left the window.
		clk.set(t0.Add(25 * time.Hour))
		ev, err = l.Evictions(ctx)
		if err != nil {
			t.Fatalf("Evictions() error = %v", err)
		}
		if ev.Count != 2 || ev.Bytes != 100 {
			t.Errorf("Evictions() a day later = %+v, want 2 entries / 100 bytes", ev)
		}
	})
}
