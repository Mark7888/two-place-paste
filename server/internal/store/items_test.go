package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

var roomy = store.ItemLimits{GroupBytes: 1 << 20, GroupItems: 10}

// storeGroup is a group with its store on, and its first device.
func storeGroup(ctx context.Context, t *testing.T, s store.Store) (groupID, deviceID string) {
	t.Helper()
	g, dev := mustGroup(ctx, t, s, store.NewToken{Name: "g"})
	if err := s.SetGroupStore(ctx, g.ID, true); err != nil {
		t.Fatalf("SetGroupStore() error = %v", err)
	}
	return g.ID, dev.ID
}

func items(t *testing.T, s store.Store) store.Items {
	t.Helper()
	it, ok := s.(store.Items)
	if !ok {
		t.Fatalf("%T does not keep store items", s)
	}
	return it
}

func reserve(ctx context.Context, t *testing.T, it store.Items, groupID, itemID string, size int64) {
	t.Helper()
	err := it.ReserveItem(ctx, store.ItemReservation{
		GroupID: groupID, ItemID: itemID, Epoch: 1, Size: size, Accounted: size + 100, StartedAt: time.Now(),
	}, roomy)
	if err != nil {
		t.Fatalf("ReserveItem(%s) error = %v", itemID, err)
	}
}

func commit(ctx context.Context, t *testing.T, it store.Items, groupID, itemID string, at time.Time) store.Item {
	t.Helper()
	got, err := it.CommitItem(ctx, store.ItemCommit{
		GroupID: groupID, ItemID: itemID, Epoch: 1,
		WrappedKeys: []byte("keys-" + itemID), SealedMeta: []byte("meta-" + itemID), SavedAt: at,
	})
	if err != nil {
		t.Fatalf("CommitItem(%s) error = %v", itemID, err)
	}
	return got
}

// TestItemLifecycle: an item is reserved as pending, which holds its room
// but lists nothing; committed, listed newest first; edited only from the
// revision it was read at; and deleted, giving its room back (SPEC §4.6).
func TestItemLifecycle(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		gid, _ := storeGroup(ctx, t, s)
		it := items(t, s)
		base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

		a := uniqueName(t, "a")
		reserve(ctx, t, it, gid, a, 1000)
		if u, _ := it.StoreUsage(ctx, gid); u.Bytes != 1100 || u.Items != 1 {
			t.Errorf("usage while pending = %+v, want 1100 bytes, 1 item", u)
		}
		if refs, _ := it.ItemRefs(ctx, gid); len(refs) != 0 {
			t.Errorf("ItemRefs() while pending = %v, want none", refs)
		}
		if _, err := it.GetItem(ctx, gid, a); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetItem(pending) error = %v, want %v", err, store.ErrNotFound)
		}

		got := commit(ctx, t, it, gid, a, base)
		if got.Size != 1000 || got.WrapEpoch != 1 || got.ContentID != a || got.ContentEpoch != 1 ||
			string(got.WrappedKeys) != "keys-"+a || string(got.SealedMeta) != "meta-"+a || got.MetaRev != 0 || got.Pending {
			t.Errorf("CommitItem() = %+v", got)
		}
		if !got.SavedAt.Equal(base) {
			t.Errorf("SavedAt = %v, want %v", got.SavedAt, base)
		}

		b, c := uniqueName(t, "b"), uniqueName(t, "c")
		reserve(ctx, t, it, gid, b, 10)
		commit(ctx, t, it, gid, b, base.Add(time.Minute))
		reserve(ctx, t, it, gid, c, 10)
		commit(ctx, t, it, gid, c, base.Add(time.Minute))
		refs, err := it.ItemRefs(ctx, gid)
		if err != nil {
			t.Fatalf("ItemRefs() error = %v", err)
		}
		// b and c were saved in the same millisecond: the id breaks the tie,
		// descending, so a cursor can always say where it stopped.
		first, second := b, c
		if c > b {
			first, second = c, b
		}
		if len(refs) != 3 || refs[0].ID != first || refs[1].ID != second || refs[2].ID != a {
			t.Errorf("ItemRefs() = %v, want %s, %s, %s", refs, first, second, a)
		}
		page, err := it.GetItems(ctx, gid, []string{a, "missing", b})
		if err != nil || len(page) != 2 || page[0].ID != a || page[1].ID != b {
			t.Errorf("GetItems() = %v, %v; want %s and %s", page, err, a, b)
		}

		upd, err := it.UpdateItemMeta(ctx, gid, a, 0, []byte("meta-2"))
		if err != nil || upd.MetaRev != 1 || string(upd.SealedMeta) != "meta-2" {
			t.Errorf("UpdateItemMeta() = %+v, %v; want revision 1", upd, err)
		}
		if _, err := it.UpdateItemMeta(ctx, gid, a, 0, []byte("stale")); !errors.Is(err, store.ErrRevisionConflict) {
			t.Errorf("UpdateItemMeta(stale) error = %v, want %v", err, store.ErrRevisionConflict)
		}

		if _, err := it.DeleteItem(ctx, gid, a); err != nil {
			t.Fatalf("DeleteItem() error = %v", err)
		}
		if _, err := it.DeleteItem(ctx, gid, a); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeleteItem(again) error = %v, want %v", err, store.ErrNotFound)
		}
		if u, _ := it.StoreUsage(ctx, gid); u.Bytes != 220 || u.Items != 2 {
			t.Errorf("usage after the delete = %+v, want 220 bytes, 2 items", u)
		}
	})
}

// TestItemReservationRefusals: a reservation is refused, and changes
// nothing, when the store is off, the epoch is stale, the id is taken, or a
// limit would be passed. The store never evicts to make room.
func TestItemReservationRefusals(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		gid, _ := storeGroup(ctx, t, s)
		it := items(t, s)
		taken := uniqueName(t, "taken")
		reserve(ctx, t, it, gid, taken, 10)
		before, _ := it.StoreUsage(ctx, gid)

		off, _ := mustGroup(ctx, t, s, store.NewToken{Name: "off"})
		tests := []struct {
			name   string
			r      store.ItemReservation
			limits store.ItemLimits
			want   error
		}{
			{"store off", store.ItemReservation{GroupID: off.ID, ItemID: uniqueName(t, "x"), Epoch: 1, Size: 1, Accounted: 1}, roomy, store.ErrStoreDisabled},
			{"no group", store.ItemReservation{GroupID: "missing", ItemID: uniqueName(t, "x"), Epoch: 1, Size: 1, Accounted: 1}, roomy, store.ErrNotFound},
			{"stale epoch", store.ItemReservation{GroupID: gid, ItemID: uniqueName(t, "x"), Epoch: 2, Size: 1, Accounted: 1}, roomy, store.ErrEpochConflict},
			{"id in use", store.ItemReservation{GroupID: gid, ItemID: taken, Epoch: 1, Size: 1, Accounted: 1}, roomy, store.ErrItemExists},
			{"over the group", store.ItemReservation{GroupID: gid, ItemID: uniqueName(t, "x"), Epoch: 1, Size: 1, Accounted: 1000},
				store.ItemLimits{GroupBytes: 1000, GroupItems: 10}, store.ErrStoreFull},
			{"over the total", store.ItemReservation{GroupID: gid, ItemID: uniqueName(t, "x"), Epoch: 1, Size: 1, Accounted: 1000},
				store.ItemLimits{GroupBytes: 1 << 20, TotalBytes: 50, GroupItems: 10}, store.ErrStoreFull},
			{"over the item count", store.ItemReservation{GroupID: gid, ItemID: uniqueName(t, "x"), Epoch: 1, Size: 1, Accounted: 1},
				store.ItemLimits{GroupBytes: 1 << 20, GroupItems: 1}, store.ErrStoreFull},
		}
		for _, tt := range tests {
			if err := it.ReserveItem(ctx, tt.r, tt.limits); !errors.Is(err, tt.want) {
				t.Errorf("ReserveItem(%s) error = %v, want %v", tt.name, err, tt.want)
			}
		}
		if after, _ := it.StoreUsage(ctx, gid); after != before {
			t.Errorf("usage after refusals = %+v, want it unchanged at %+v", after, before)
		}

		// Released, the room comes back; only a pending item is released.
		if ok, err := it.ReleaseItem(ctx, gid, taken); err != nil || !ok {
			t.Errorf("ReleaseItem() = %v, %v; want released", ok, err)
		}
		if ok, _ := it.ReleaseItem(ctx, gid, taken); ok {
			t.Error("ReleaseItem(twice) released again")
		}
		if u, _ := it.StoreUsage(ctx, gid); u != (store.ItemUsage{}) {
			t.Errorf("usage after the release = %+v, want zero", u)
		}
	})
}

// TestRekeyAndCommitSerialise: a group with store items cannot be re-keyed
// without a store link, and an item reserved before a rekey cannot be
// committed after it, so no item is ever left behind an epoch its devices
// cannot reach (/spec/crypto.md §7a.2).
func TestRekeyAndCommitSerialise(t *testing.T) {
	t.Parallel()
	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		gid, keeper := storeGroup(ctx, t, s)
		victim, err := s.AddDevice(ctx, gid, store.NewDevice{Name: "phone", PublicKey: []byte("pk2")})
		if err != nil {
			t.Fatalf("AddDevice() error = %v", err)
		}
		it := items(t, s)
		keys := []store.RekeyKey{{DeviceID: keeper, Key: []byte("w2")}}

		// Reserved at epoch 1, not committed yet: a rekey without a link
		// still goes through, and the item can no longer be committed.
		late := uniqueName(t, "late")
		reserve(ctx, t, it, gid, late, 10)
		other, err := s.AddDevice(ctx, gid, store.NewDevice{Name: "tablet", PublicKey: []byte("pk3")})
		if err != nil {
			t.Fatalf("AddDevice() error = %v", err)
		}
		if _, err := s.ApplyRekey(ctx, gid, other.ID, 1, []store.RekeyKey{{DeviceID: keeper, Key: []byte("w2")}, {DeviceID: victim.ID, Key: []byte("w2")}}, nil); err != nil {
			t.Fatalf("ApplyRekey(no items yet) error = %v", err)
		}
		_, err = it.CommitItem(ctx, store.ItemCommit{GroupID: gid, ItemID: late, Epoch: 1, WrappedKeys: []byte("k"), SealedMeta: []byte("m"), SavedAt: time.Now()})
		if !errors.Is(err, store.ErrEpochConflict) {
			t.Errorf("CommitItem(after a rekey) error = %v, want %v", err, store.ErrEpochConflict)
		}

		// Committed at epoch 2: now a rekey needs a link.
		saved := uniqueName(t, "saved")
		if err := it.ReserveItem(ctx, store.ItemReservation{GroupID: gid, ItemID: saved, Epoch: 2, Size: 1, Accounted: 1, StartedAt: time.Now()}, roomy); err != nil {
			t.Fatalf("ReserveItem() error = %v", err)
		}
		if _, err := it.CommitItem(ctx, store.ItemCommit{GroupID: gid, ItemID: saved, Epoch: 2, WrappedKeys: []byte("k"), SealedMeta: []byte("m"), SavedAt: time.Now()}); err != nil {
			t.Fatalf("CommitItem() error = %v", err)
		}
		if _, err := s.ApplyRekey(ctx, gid, victim.ID, 2, keys, nil); !errors.Is(err, store.ErrStoreLinkRequired) {
			t.Fatalf("ApplyRekey(items, no link) error = %v, want %v", err, store.ErrStoreLinkRequired)
		}
		if g, _ := s.GetGroup(ctx, gid); g.Epoch != 2 {
			t.Errorf("a refused rekey moved the epoch to %d", g.Epoch)
		}
		if _, err := s.ApplyRekey(ctx, gid, victim.ID, 2, keys, []byte("link")); err != nil {
			t.Errorf("ApplyRekey(with a link) error = %v", err)
		}
	})
}

// TestReleaseAllPending: at startup every pending item is forgotten and its
// room given back; committed ones are kept.
func TestReleaseAllPending(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, s store.Store) {
		ctx := context.Background()
		gid, _ := storeGroup(ctx, t, s)
		it := items(t, s)
		kept, orphan := uniqueName(t, "kept"), uniqueName(t, "orphan")
		reserve(ctx, t, it, gid, kept, 10)
		commit(ctx, t, it, gid, kept, time.Now())
		reserve(ctx, t, it, gid, orphan, 20)

		released, err := it.ReleaseAllPending(ctx)
		if err != nil {
			t.Fatalf("ReleaseAllPending() error = %v", err)
		}
		found := false
		for _, r := range released {
			if r.ID == kept {
				t.Error("ReleaseAllPending() released a committed item")
			}
			found = found || (r.ID == orphan && r.GroupID == gid)
		}
		if !found {
			t.Errorf("ReleaseAllPending() = %v, want %s among them", released, orphan)
		}
		if u, _ := it.StoreUsage(ctx, gid); u.Bytes != 110 || u.Items != 1 {
			t.Errorf("usage after the release = %+v, want the committed item's 110 bytes", u)
		}
	}
	// Releasing is relay-wide, so the Redis half runs on a database of its
	// own, out of the way of every other test's pending items.
	t.Run("memory", func(t *testing.T) {
		t.Parallel()
		run(t, store.NewMemoryStore())
	})
	t.Run("redis", func(t *testing.T) {
		t.Parallel()
		run(t, newTestRedisStoreDB(t, 9))
	})
}
