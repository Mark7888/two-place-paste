package entries_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
)

func TestPurgeGroup(t *testing.T) {
	t.Parallel()
	f := newQuotaFixture(t, quota.Limits{})
	ctx := context.Background()

	inline := f.put(t, "g", "small", 100)
	big := f.put(t, "g", "big", 2000) // over testInlineMax: a blob
	f.put(t, "other", "keep", 2000)
	if inline.StorageRef != "" || big.StorageRef == "" {
		t.Fatalf("fixture: want one inline and one blob entry, got refs %q and %q", inline.StorageRef, big.StorageRef)
	}

	res, err := f.svc.PurgeGroup(ctx, "g")
	if err != nil {
		t.Fatalf("PurgeGroup() error = %v", err)
	}
	if res.Entries != 2 || res.Bytes != 2100 {
		t.Errorf("PurgeGroup() = %+v, want 2 entries and 2100 bytes", res)
	}

	for _, id := range []string{"small", "big"} {
		if _, _, err := f.svc.Fetch(ctx, "g", id); !errors.Is(err, entries.ErrNotFound) {
			t.Errorf("Fetch(%s) after purge error = %v, want %v", id, err, entries.ErrNotFound)
		}
	}
	if st, _ := f.store.GroupStats(ctx, "g"); st.Entries != 0 {
		t.Errorf("GroupStats().Entries after purge = %d, want 0", st.Entries)
	}
	if got := f.usage(t, "g"); got != 0 {
		t.Errorf("accounted bytes after purge = %d, want 0", got)
	}
	if got := f.usage(t, "other"); got != 2000 {
		t.Errorf("other group's accounted bytes = %d, want 2000", got)
	}
	if _, err := f.blobs.Get(ctx, big.StorageRef); err == nil {
		t.Errorf("the purged entry's blob is still there")
	}

	got := f.evicted.got()
	slices.Sort(got)
	if want := []string{"big", "small"}; !slices.Equal(got, want) {
		t.Errorf("announced %v, want %v", got, want)
	}

	// The other group is untouched, and an empty group purges to nothing.
	if _, _, err := f.svc.Fetch(ctx, "other", "keep"); err != nil {
		t.Errorf("Fetch(other group) after purge error = %v", err)
	}
	if res, err := f.svc.PurgeGroup(ctx, "g"); err != nil || res.Entries != 0 {
		t.Errorf("PurgeGroup(empty) = %+v, %v; want nothing", res, err)
	}
}

func TestPurgeGroupAbortsUploads(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t, quota.Limits{})
	ctx := context.Background()

	info, err := f.svc.BeginUpload(ctx, "g", "laptop", 1, "", upMax)
	if err != nil {
		t.Fatalf("BeginUpload() error = %v", err)
	}
	if _, err := f.svc.BeginUpload(ctx, "other", "phone", 1, "", upMax); err != nil {
		t.Fatalf("BeginUpload(other) error = %v", err)
	}

	res, err := f.svc.PurgeGroup(ctx, "g")
	if err != nil {
		t.Fatalf("PurgeGroup() error = %v", err)
	}
	if res.Uploads != 1 {
		t.Errorf("PurgeGroup().Uploads = %d, want 1", res.Uploads)
	}
	if _, err := f.svc.WriteChunk(ctx, "g", "laptop", info.Handle, 0, body(upChunk)); !errors.Is(err, entries.ErrUploadNotFound) {
		t.Errorf("WriteChunk() after purge error = %v, want %v", err, entries.ErrUploadNotFound)
	}
	if n, _ := f.svc.UploadsInProgress(); n != 1 {
		t.Errorf("uploads in progress = %d, want the other group's 1", n)
	}
	if u, _ := f.ledger.GroupUsage(ctx, "g"); u.Bytes != 0 {
		t.Errorf("accounted bytes of the purged group = %d, want 0", u.Bytes)
	}
}
