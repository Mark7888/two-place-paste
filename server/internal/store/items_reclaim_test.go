package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// TestReclaim: a queued store is closed to saves, its records go in batches,
// then its pending records and usage, and it opens again once off the queue
// (SPEC §4.6). Deleting a group queues its store.
func TestReclaim(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		gid, _ := storeGroup(ctx, t, s)
		it := items(t, s)
		base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

		ids := []string{uniqueName(t, "a"), uniqueName(t, "b"), uniqueName(t, "c")}
		for i, id := range ids {
			reserve(ctx, t, it, gid, id, 10)
			commit(ctx, t, it, gid, id, base.Add(time.Duration(i)*time.Second))
		}
		pending := uniqueName(t, "pending")
		reserve(ctx, t, it, gid, pending, 10)

		if err := it.QueueReclaim(ctx, gid); err != nil {
			t.Fatalf("QueueReclaim() error = %v", err)
		}
		if ok, err := it.Reclaiming(ctx, gid); err != nil || !ok {
			t.Errorf("Reclaiming() = %v, %v, want true", ok, err)
		}
		if q, _ := it.ReclaimQueue(ctx); !slices.Contains(q, gid) {
			t.Errorf("ReclaimQueue() = %v, want it to hold %s", q, gid)
		}
		err := it.ReserveItem(ctx, store.ItemReservation{
			GroupID: gid, ItemID: uniqueName(t, "new"), Epoch: 1, Size: 1, Accounted: 1, StartedAt: base,
		}, roomy)
		if !errors.Is(err, store.ErrStoreBusy) {
			t.Errorf("ReserveItem(while emptying) error = %v, want %v", err, store.ErrStoreBusy)
		}
		if _, err := it.CommitItem(ctx, store.ItemCommit{GroupID: gid, ItemID: pending, Epoch: 1, SavedAt: base}); !errors.Is(err, store.ErrStoreBusy) {
			t.Errorf("CommitItem(while emptying) error = %v, want %v", err, store.ErrStoreBusy)
		}

		if err := it.FinishReclaim(ctx, gid); !errors.Is(err, store.ErrStoreBusy) {
			t.Errorf("FinishReclaim(with items listed) error = %v, want %v", err, store.ErrStoreBusy)
		}
		for _, want := range []int{2, 1, 0} {
			if got, err := it.ReclaimItems(ctx, gid, 2); err != nil || got != want {
				t.Fatalf("ReclaimItems() = %d, %v, want %d", got, err, want)
			}
		}
		if refs, _ := it.ItemRefs(ctx, gid); len(refs) != 0 {
			t.Errorf("ItemRefs() after the reclaim = %v, want none", refs)
		}
		if err := it.FinishReclaim(ctx, gid); err != nil {
			t.Fatalf("FinishReclaim() error = %v", err)
		}
		if err := it.FinishReclaim(ctx, gid); err != nil {
			t.Errorf("FinishReclaim(again) error = %v", err)
		}
		if u, _ := it.StoreUsage(ctx, gid); u != (store.ItemUsage{}) {
			t.Errorf("usage after the reclaim = %+v, want nothing", u)
		}
		if ok, _ := it.ReleaseItem(ctx, gid, pending); ok {
			t.Error("the pending record survived the reclaim")
		}

		if err := it.EndReclaim(ctx, gid); err != nil {
			t.Fatalf("EndReclaim() error = %v", err)
		}
		if ok, _ := it.Reclaiming(ctx, gid); ok {
			t.Error("Reclaiming() after EndReclaim = true")
		}
		reserve(ctx, t, it, gid, uniqueName(t, "after"), 10)

		if _, err := s.DeleteGroup(ctx, gid); err != nil {
			t.Fatalf("DeleteGroup() error = %v", err)
		}
		if ok, _ := it.Reclaiming(ctx, gid); !ok {
			t.Error("deleting the group did not queue its store")
		}
	})
}

// TestBlobRetries: the retry set keeps what it is given until removed.
func TestBlobRetries(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		it := items(t, s)
		r := store.BlobRetry{GroupID: uniqueName(t, "g"), ItemID: uniqueName(t, "i")}
		if err := it.AddBlobRetry(ctx, r); err != nil {
			t.Fatalf("AddBlobRetry() error = %v", err)
		}
		if got, _ := it.BlobRetries(ctx); !slices.Contains(got, r) {
			t.Errorf("BlobRetries() = %v, want it to hold %v", got, r)
		}
		if err := it.RemoveBlobRetry(ctx, r); err != nil {
			t.Fatalf("RemoveBlobRetry() error = %v", err)
		}
		if got, _ := it.BlobRetries(ctx); slices.Contains(got, r) {
			t.Errorf("BlobRetries() after the remove = %v, still holds %v", got, r)
		}
	})
}

// TestPendingBefore lists only the uploads started before the cut.
func TestPendingBefore(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		gid, _ := storeGroup(ctx, t, s)
		it := items(t, s)
		old, fresh := uniqueName(t, "old"), uniqueName(t, "fresh")
		cut := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		for id, at := range map[string]time.Time{old: cut.Add(-time.Hour), fresh: cut.Add(time.Minute)} {
			err := it.ReserveItem(ctx, store.ItemReservation{GroupID: gid, ItemID: id, Epoch: 1, Size: 1, Accounted: 1, StartedAt: at}, roomy)
			if err != nil {
				t.Fatalf("ReserveItem(%s) error = %v", id, err)
			}
		}
		got, err := it.PendingBefore(ctx, cut)
		if err != nil {
			t.Fatalf("PendingBefore() error = %v", err)
		}
		has := func(id string) bool {
			return slices.ContainsFunc(got, func(i store.Item) bool { return i.ID == id })
		}
		if !has(old) || has(fresh) {
			t.Errorf("PendingBefore() = %v, want %s and not %s", got, old, fresh)
		}
	})
}
