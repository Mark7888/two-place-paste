package items_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/items"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// save stores a small item in one frame.
func (f *fixture) save(t *testing.T, groupID, id string) {
	t.Helper()
	req := putReq(id, "", 3)
	req.Ciphertext = []byte("abc")
	if _, err := f.svc.Put(context.Background(), groupID, "laptop", req); err != nil {
		t.Fatalf("Put(%s) error = %v", id, err)
	}
}

func (f *fixture) hasBlob(t *testing.T, groupID, id string) bool {
	t.Helper()
	rc, err := f.blobs.GetItemRange(context.Background(), groupID, id, 0, 1)
	if errors.Is(err, blob.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("GetItemRange(%s) error = %v", id, err)
	}
	_ = rc.Close()
	return true
}

// TestPurge: a purge closes the store at once and tells the devices; the
// reclaim then deletes every item and blob and opens it again, empty, with
// the switch still on (SPEC §4.6).
func TestPurge(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.save(t, f.group, "one")
	f.save(t, f.group, "two")
	handle := f.begin(t, "laptop", "uploading", 6)

	if err := f.svc.Purge(ctx, f.group); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}
	if got := f.storeStates(); !slices.Equal(got, []bool{false}) {
		t.Errorf("store states after the purge = %v, want [false]", got)
	}
	if f.svc.OwnsUpload(handle) {
		t.Error("the purge left an upload open")
	}
	if _, _, err := f.svc.List(ctx, f.group, "", 0); !errors.Is(err, store.ErrStoreBusy) {
		t.Errorf("List(while emptying) error = %v, want %v", err, store.ErrStoreBusy)
	}
	if l, err := f.svc.Limits(ctx, f.group); err != nil || l.Enabled {
		t.Errorf("Limits(while emptying) = %+v, %v, want the store shown closed", l, err)
	}

	if err := f.svc.ReclaimQueued(ctx); err != nil {
		t.Fatalf("ReclaimQueued() error = %v", err)
	}
	if got := f.storeStates(); !slices.Equal(got, []bool{false, true}) {
		t.Errorf("store states after the reclaim = %v, want [false true]", got)
	}
	page, _, err := f.svc.List(ctx, f.group, "", 0)
	if err != nil || len(page) != 0 {
		t.Errorf("List() after the reclaim = %v, %v, want an empty store", page, err)
	}
	if got := f.usage(t); got != (store.ItemUsage{}) {
		t.Errorf("usage after the reclaim = %+v, want nothing", got)
	}
	if f.hasBlob(t, f.group, "one") || f.hasBlob(t, f.group, "two") {
		t.Error("the reclaim left a blob behind")
	}
	f.save(t, f.group, "three")

	if err := f.svc.Purge(ctx, "no-such-group"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Purge(unknown group) error = %v, want %v", err, store.ErrNotFound)
	}
}

// TestSweepEmptiesDeletedGroups: a deleted group's store is emptied by the
// sweep even when nothing queued it, while blobs of a group the registry
// has never heard of are left alone — a relay pointed at the wrong Redis
// must not delete anything.
func TestSweepEmptiesDeletedGroups(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.save(t, f.group, "one")
	if _, err := f.reg.DeleteGroup(ctx, f.group); err != nil {
		t.Fatalf("DeleteGroup() error = %v", err)
	}
	// Lose the queue entry, as a crash between the two would.
	if err := f.reg.EndReclaim(ctx, f.group); err != nil {
		t.Fatalf("EndReclaim() error = %v", err)
	}
	if err := f.blobs.PutItem(ctx, "stranger", "kept", 1, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("PutItem() error = %v", err)
	}

	if err := f.svc.Sweep(ctx, f.clock.Now()); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if f.hasBlob(t, f.group, "one") {
		t.Error("the sweep left the deleted group's blob")
	}
	if got := f.usage(t); got != (store.ItemUsage{}) {
		t.Errorf("usage of the deleted group = %+v, want nothing", got)
	}
	if !f.hasBlob(t, "stranger", "kept") {
		t.Error("the sweep deleted the blob of a group it does not know")
	}
}

// TestSweepRetriesAndReleases: a blob whose delete failed is deleted by the
// next sweep, and an upload a crash left pending past its lifetime is
// released with its blob.
func TestSweepRetriesAndReleases(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	f.save(t, f.group, "doomed")
	f.blobs.failNext = true
	if err := f.svc.Delete(ctx, f.group, "laptop", "doomed"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !f.hasBlob(t, f.group, "doomed") {
		t.Fatal("the failed delete took the blob anyway")
	}

	err := f.reg.ReserveItem(ctx, store.ItemReservation{
		GroupID: f.group, ItemID: "stale", Epoch: 1, Size: 1, Accounted: 1 + items.RecordBytes, StartedAt: f.clock.Now(),
	}, store.ItemLimits{GroupItems: 10})
	if err != nil {
		t.Fatalf("ReserveItem() error = %v", err)
	}
	if err := f.blobs.PutItem(ctx, f.group, "stale", 1, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("PutItem() error = %v", err)
	}

	if err := f.svc.Sweep(ctx, f.clock.Now()); err != nil {
		t.Fatalf("Sweep(now) error = %v", err)
	}
	if f.hasBlob(t, f.group, "doomed") {
		t.Error("the sweep did not retry the failed delete")
	}
	if !f.hasBlob(t, f.group, "stale") {
		t.Error("the sweep released a fresh upload")
	}

	f.clock.Advance(2 * time.Hour)
	if err := f.svc.Sweep(ctx, f.clock.Now()); err != nil {
		t.Fatalf("Sweep(later) error = %v", err)
	}
	if f.hasBlob(t, f.group, "stale") {
		t.Error("the sweep did not release the stale upload")
	}
	if got := f.usage(t); got != (store.ItemUsage{}) {
		t.Errorf("usage after the sweeps = %+v, want nothing", got)
	}
}
