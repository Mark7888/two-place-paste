package entries_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
)

// TestBlobDeletedBehindTheRelay: an operator — or a lifecycle rule, or a
// restore — removes a blob while its entry still has hours to live. The first
// reader to find it gone gets NOT_FOUND, and the entry is retired at once:
// out of history and latest, its room released, the group told. No later
// reader trips over it.
func TestBlobDeletedBehindTheRelay(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"fetch", "range"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			root := t.TempDir()
			store := entries.NewMemoryStore()
			ledger := quota.NewMemory(quota.Options{})
			evicted := &evictLog{}
			svc := entries.NewService(store, blob.NewDisk(root, testMaxBytes), entries.Options{
				InlineMaxBytes: testInlineMax,
				MaxBytes:       testMaxBytes,
				Quota:          ledger,
				OnEvict:        evicted.add,
			})
			gid := newGroupID(t)

			older, err := svc.Put(ctx, gid, 1, "older", 2000, bytes.NewReader(bytes.Repeat([]byte("o"), 2000)))
			if err != nil {
				t.Fatalf("Put(older) error = %v", err)
			}
			gone, err := svc.Put(ctx, gid, 1, "gone", 3000, bytes.NewReader(bytes.Repeat([]byte("g"), 3000)))
			if err != nil {
				t.Fatalf("Put(gone) error = %v", err)
			}
			if err := os.Remove(filepath.Join(root, filepath.FromSlash(gone.StorageRef))); err != nil {
				t.Fatalf("remove the blob behind the relay: %v", err)
			}

			if path == "fetch" {
				_, _, err = svc.Fetch(ctx, gid, "gone")
			} else {
				_, _, err = svc.FetchRange(ctx, gid, "gone", 0, 0)
			}
			if !errors.Is(err, entries.ErrNotFound) {
				t.Fatalf("read of an entry whose blob is gone: error = %v, want %v", err, entries.ErrNotFound)
			}

			// Retired: no longer latest, no longer listed, no longer counted,
			// and the group was told.
			latest, _, err := svc.Latest(ctx, gid)
			if err != nil || latest.ID != older.ID {
				t.Errorf("Latest() = %q, %v; want the older entry", latest.ID, err)
			}
			page, _, err := svc.History(ctx, gid, 10, latest.CreatedAt.AddDate(1, 0, 0))
			if err != nil || len(page) != 1 || page[0].ID != older.ID {
				t.Errorf("History() = %v, %v; want only the older entry", ids(page), err)
			}
			if u, _ := ledger.GroupUsage(ctx, gid); u.Bytes != 2000 {
				t.Errorf("accounted %d bytes, want only the older entry's 2000", u.Bytes)
			}
			if got := evicted.got(); len(got) != 1 || got[0] != "gone" {
				t.Errorf("group told about %v, want [gone]", got)
			}

			// The next reader gets a plain not-found from the record, and
			// the group is not told twice.
			if _, _, err := svc.Fetch(ctx, gid, "gone"); !errors.Is(err, entries.ErrNotFound) {
				t.Errorf("second read error = %v, want %v", err, entries.ErrNotFound)
			}
			if got := evicted.got(); len(got) != 1 {
				t.Errorf("group told %d times, want once", len(got))
			}

			// The older entry is untouched.
			if _, body, err := svc.Fetch(ctx, gid, older.ID); err != nil || len(body) != 2000 {
				t.Errorf("Fetch(older) = %d bytes, %v", len(body), err)
			}
		})
	}
}
