package entries_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
)

// evictLog records what the service reported as evicted.
type evictLog struct {
	mu  sync.Mutex
	ids []string
}

func (l *evictLog) add(_ context.Context, _, entryID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ids = append(l.ids, entryID)
}

func (l *evictLog) got() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ids...)
}

type quotaFixture struct {
	svc     *entries.Service
	store   *entries.MemoryStore
	blobs   *blob.Disk
	ledger  *quota.Memory
	evicted *evictLog
	clock   time.Time
}

func newQuotaFixture(t *testing.T, limits quota.Limits) *quotaFixture {
	t.Helper()
	f := &quotaFixture{
		store:   entries.NewMemoryStore(),
		blobs:   blob.NewDisk(t.TempDir(), testMaxBytes),
		ledger:  quota.NewMemory(quota.Options{Limits: limits}),
		evicted: &evictLog{},
		clock:   time.Now().UTC(),
	}
	f.svc = entries.NewService(f.store, f.blobs, entries.Options{
		InlineMaxBytes: testInlineMax,
		MaxBytes:       testMaxBytes,
		Quota:          f.ledger,
		OnEvict:        f.evicted.add,
		// Each write a millisecond after the last, so "oldest" is unambiguous.
		Now: func() time.Time { f.clock = f.clock.Add(time.Millisecond); return f.clock },
	})
	return f
}

func (f *quotaFixture) put(t *testing.T, group, id string, size int) entries.Meta {
	t.Helper()
	m, err := f.svc.Put(context.Background(), group, 1, id, int64(size), strings.NewReader(strings.Repeat("c", size)))
	if err != nil {
		t.Fatalf("Put(%s) error = %v", id, err)
	}
	return m
}

func (f *quotaFixture) usage(t *testing.T, group string) int64 {
	t.Helper()
	u, err := f.ledger.GroupUsage(context.Background(), group)
	if err != nil {
		t.Fatalf("GroupUsage() error = %v", err)
	}
	return u.Bytes
}

func TestServiceAccountsOnlyBlobs(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture(t, quota.Limits{})
	gid := newGroupID(t)

	f.put(t, gid, "inline", testInlineMax)
	f.put(t, gid, "blob", testInlineMax+1)
	if got := f.usage(t, gid); got != testInlineMax+1 {
		t.Errorf("accounted bytes = %d, want only the blob's %d", got, testInlineMax+1)
	}
}

// TestServiceEvictsOldestToFit is the user-visible promise: a new entry always
// fits, and what makes room is the group's oldest entry — record and blob.
func TestServiceEvictsOldestToFit(t *testing.T) {
	t.Parallel()
	const size = 3000
	f := newQuotaFixture(t, quota.Limits{GroupBytes: 2*size + 100})
	ctx := context.Background()
	gid := newGroupID(t)

	oldest := f.put(t, gid, "oldest", size)
	f.put(t, gid, "middle", size)
	newest := f.put(t, gid, "newest", size)

	if got := f.evicted.got(); len(got) != 1 || got[0] != "oldest" {
		t.Fatalf("evicted = %v, want [oldest]", got)
	}
	if _, _, err := f.svc.Fetch(ctx, gid, oldest.ID); !errors.Is(err, entries.ErrNotFound) {
		t.Errorf("Fetch(evicted) error = %v, want %v", err, entries.ErrNotFound)
	}
	if _, err := f.blobs.Get(ctx, oldest.StorageRef); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("evicted blob still stored: Get() error = %v", err)
	}
	if _, _, err := f.svc.Fetch(ctx, gid, newest.ID); err != nil {
		t.Errorf("Fetch(newest) error = %v", err)
	}
	if got := f.usage(t, gid); got != 2*size {
		t.Errorf("accounted bytes = %d, want %d", got, 2*size)
	}
}

func TestServiceQuotaRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("entry larger than the limit", func(t *testing.T) {
		t.Parallel()
		f := newQuotaFixture(t, quota.Limits{GroupBytes: testInlineMax + 10})
		_, err := f.svc.Put(ctx, newGroupID(t), 1, "big", testInlineMax+11, strings.NewReader(strings.Repeat("c", testInlineMax+11)))
		if !errors.Is(err, entries.ErrTooLarge) {
			t.Errorf("Put() error = %v, want %v", err, entries.ErrTooLarge)
		}
	})

	t.Run("room held by an upload in flight", func(t *testing.T) {
		t.Parallel()
		f := newQuotaFixture(t, quota.Limits{TotalBytes: 5000})
		if _, err := f.ledger.Reserve(ctx, quota.Reservation{EntryID: "inflight", GroupID: "g", Size: 4000, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatalf("Reserve() error = %v", err)
		}
		_, err := f.svc.Put(ctx, newGroupID(t), 1, "late", 2000, strings.NewReader(strings.Repeat("c", 2000)))
		if !errors.Is(err, entries.ErrQuotaExceeded) {
			t.Errorf("Put() error = %v, want %v", err, entries.ErrQuotaExceeded)
		}
	})

	t.Run("backend out of room", func(t *testing.T) {
		t.Parallel()
		ledger := quota.NewMemory(quota.Options{})
		svc := entries.NewService(entries.NewMemoryStore(), fullBackend{}, entries.Options{
			InlineMaxBytes: testInlineMax,
			MaxBytes:       testMaxBytes,
			Quota:          ledger,
		})
		_, err := svc.Put(ctx, "g", 1, "nowhere", 2000, strings.NewReader(strings.Repeat("c", 2000)))
		if !errors.Is(err, entries.ErrQuotaExceeded) {
			t.Errorf("Put() error = %v, want %v", err, entries.ErrQuotaExceeded)
		}
		if u, _ := ledger.Usage(ctx); u.Bytes != 0 {
			t.Errorf("accounted bytes after a refused write = %d, want 0", u.Bytes)
		}
	})
}

// fullBackend is a blob backend whose disk is below its free-space floor.
type fullBackend struct{ blob.Backend }

func (fullBackend) Put(context.Context, string, time.Time, io.Reader) (string, error) {
	return "", blob.ErrStorageFull
}

// TestServiceReleasesFailedUploads: whatever goes wrong after the room is
// reserved, the room is given back.
func TestServiceReleasesFailedUploads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newQuotaFixture(t, quota.Limits{})
	gid := newGroupID(t)

	// Declared larger than what arrives: the blob is refused and removed.
	if _, err := f.svc.Put(ctx, gid, 1, "short", 3000, strings.NewReader(strings.Repeat("c", 2000))); !errors.Is(err, entries.ErrSizeMismatch) {
		t.Errorf("Put(short body) error = %v, want %v", err, entries.ErrSizeMismatch)
	}
	// Declared smaller than what arrives, but over the cap.
	if _, err := f.svc.Put(ctx, gid, 1, "long", 3000, strings.NewReader(strings.Repeat("c", testMaxBytes+1))); err == nil {
		t.Errorf("Put(over-cap body) error = nil, want a refusal")
	}
	// A client that cancels mid-upload.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.svc.Put(cancelled, gid, 1, "gone", 3000, strings.NewReader(strings.Repeat("c", 3000))); err == nil {
		t.Errorf("Put(cancelled) error = nil, want a refusal")
	}

	if got := f.usage(t, gid); got != 0 {
		t.Errorf("accounted bytes after failed uploads = %d, want 0", got)
	}
	if refs, _ := f.store.EntryRefs(ctx); len(refs) != 0 {
		t.Errorf("entries recorded for failed uploads: %v", refs)
	}

	// And the next upload is committed, so it can be evicted later.
	f.put(t, gid, "ok", 3000)
	if got := f.usage(t, gid); got != 3000 {
		t.Errorf("accounted bytes = %d, want 3000", got)
	}
}
