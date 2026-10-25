package blob_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
)

// storeBackend is a backend that also holds the store's blobs, as both of
// ours do.
type storeBackend interface {
	backendUnderTest
	blob.Items
}

func forEachItemsBackend(t *testing.T, run func(t *testing.T, b storeBackend)) {
	t.Helper()
	forEachBackend(t, 1024, func(t *testing.T, b backendUnderTest) {
		sb, ok := b.(storeBackend)
		if !ok {
			t.Fatalf("%T does not store items", b)
		}
		run(t, sb)
	})
}

func readItem(t *testing.T, b blob.Items, groupID, itemID string, offset, length int64) []byte {
	t.Helper()
	rc, err := b.GetItemRange(context.Background(), groupID, itemID, offset, length)
	if err != nil {
		t.Fatalf("GetItemRange(%s) error = %v", itemID, err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", itemID, err)
	}
	return body
}

// TestItemsRoundTrip: an item's blob is stored at exactly its declared size,
// created and never replaced, read back by range and deleted; any other
// length leaves nothing behind (SPEC §4.6).
func TestItemsRoundTrip(t *testing.T) {
	t.Parallel()
	forEachItemsBackend(t, func(t *testing.T, b storeBackend) {
		ctx := context.Background()
		const group = "group-A"

		if err := b.PutItem(ctx, group, "item-1", 10, strings.NewReader("0123456789")); err != nil {
			t.Fatalf("PutItem() error = %v", err)
		}
		if got := readItem(t, b, group, "item-1", 0, 10); string(got) != "0123456789" {
			t.Errorf("GetItemRange(all) = %q, want the whole blob", got)
		}
		if got := readItem(t, b, group, "item-1", 3, 4); string(got) != "3456" {
			t.Errorf("GetItemRange(3, 4) = %q, want %q", got, "3456")
		}
		if err := b.PutItem(ctx, group, "item-1", 3, strings.NewReader("new")); !errors.Is(err, blob.ErrExists) {
			t.Errorf("PutItem(existing) error = %v, want %v", err, blob.ErrExists)
		}
		if got := readItem(t, b, group, "item-1", 0, 10); string(got) != "0123456789" {
			t.Errorf("an existing item was replaced: %q", got)
		}

		for name, body := range map[string]string{"too long": "0123456789!", "too short": "012"} {
			err := b.PutItem(ctx, group, "item-"+strings.ReplaceAll(name, " ", "-"), 10, strings.NewReader(body))
			if !errors.Is(err, blob.ErrTooLarge) && !errors.Is(err, blob.ErrIncomplete) {
				t.Errorf("PutItem(%s) error = %v, want a size error", name, err)
			}
			if _, err := b.GetItemRange(ctx, group, "item-"+strings.ReplaceAll(name, " ", "-"), 0, 1); !errors.Is(err, blob.ErrNotFound) {
				t.Errorf("PutItem(%s) left a blob behind: GetItemRange() error = %v", name, err)
			}
		}

		if err := b.DeleteItem(ctx, group, "item-1"); err != nil {
			t.Fatalf("DeleteItem() error = %v", err)
		}
		if _, err := b.GetItemRange(ctx, group, "item-1", 0, 1); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("GetItemRange(deleted) error = %v, want %v", err, blob.ErrNotFound)
		}
		if err := b.DeleteItem(ctx, group, "item-1"); err != nil {
			t.Errorf("DeleteItem(missing) error = %v, want nil", err)
		}

		for name, ids := range map[string][2]string{
			"group escape": {"../x", "item"},
			"item escape":  {group, "../../etc/passwd"},
			"empty item":   {group, ""},
		} {
			if err := b.PutItem(ctx, ids[0], ids[1], 1, strings.NewReader("x")); !errors.Is(err, blob.ErrInvalidRef) {
				t.Errorf("PutItem(%s) error = %v, want %v", name, err, blob.ErrInvalidRef)
			}
		}
	})
}

// TestItemsChunkedUpload: an item's blob can arrive in chunks, is invisible
// until complete, and is never put in place over an existing one.
func TestItemsChunkedUpload(t *testing.T) {
	t.Parallel()
	forEachItemsBackend(t, func(t *testing.T, b storeBackend) {
		ctx := context.Background()
		const group = "group-B"
		// On S3 every part but the last is at least 5 MiB.
		first := bytes.Repeat([]byte("a"), 5<<20)
		last := []byte("tail")
		size := int64(len(first) + len(last))

		up, err := b.BeginItemUpload(ctx, group, "big", size)
		if err != nil {
			t.Fatalf("BeginItemUpload() error = %v", err)
		}
		for _, chunk := range [][]byte{first, last} {
			if err := up.Write(ctx, chunk); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
		}
		if _, err := b.GetItemRange(ctx, group, "big", 0, 1); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("an unfinished upload is visible: GetItemRange() error = %v", err)
		}
		ref, err := up.Complete(ctx)
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		if want := "store/" + group + "/big.bin"; ref != want {
			t.Errorf("Complete() ref = %q, want %q", ref, want)
		}
		if got := readItem(t, b, group, "big", size-4, 4); string(got) != "tail" {
			t.Errorf("GetItemRange(tail) = %q, want %q", got, "tail")
		}

		again, err := b.BeginItemUpload(ctx, group, "big", 3)
		if err != nil {
			t.Fatalf("BeginItemUpload(again) error = %v", err)
		}
		if err := again.Write(ctx, []byte("new")); err != nil {
			t.Fatalf("Write(again) error = %v", err)
		}
		if _, err := again.Complete(ctx); !errors.Is(err, blob.ErrExists) {
			t.Errorf("Complete(over an existing item) error = %v, want %v", err, blob.ErrExists)
		}
	})
}

// TestItemsAreOutsideTheBuckets: the bucket sweep, however late, and the
// verification listing never touch the store's blobs (SPEC §4.5, §4.6).
func TestItemsAreOutsideTheBuckets(t *testing.T) {
	t.Parallel()
	forEachItemsBackend(t, func(t *testing.T, b storeBackend) {
		ctx := context.Background()
		if err := b.PutItem(ctx, "group-C", "keep", 4, strings.NewReader("keep")); err != nil {
			t.Fatalf("PutItem() error = %v", err)
		}
		if err := b.Sweep(ctx, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("Sweep() error = %v", err)
		}
		if got := readItem(t, b, "group-C", "keep", 0, 4); string(got) != "keep" {
			t.Errorf("the sweep took a store item: %q", got)
		}
		refs, err := b.ListRefs(ctx)
		if err != nil {
			t.Fatalf("ListRefs() error = %v", err)
		}
		for _, r := range refs {
			if strings.HasPrefix(r, blob.ItemsDir+"/") {
				t.Errorf("ListRefs() lists a store item: %s", r)
			}
		}
	})
}
