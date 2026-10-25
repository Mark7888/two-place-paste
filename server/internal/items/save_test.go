package items_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/items"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// clock is a settable time source.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	reg   *store.MemoryStore
	blobs *flakyBlobs
	svc   *items.Service
	clock *clock
	group string

	mu     sync.Mutex
	states []bool // what OnStoreState was told, in order
}

// flakyBlobs is a disk whose item deletes can be made to fail.
type flakyBlobs struct {
	*blob.Disk
	mu       sync.Mutex
	failNext bool
}

func (f *flakyBlobs) DeleteItem(ctx context.Context, groupID, itemID string) error {
	f.mu.Lock()
	fail := f.failNext
	f.failNext = false
	f.mu.Unlock()
	if fail {
		return errors.New("disk on fire")
	}
	return f.Disk.DeleteItem(ctx, groupID, itemID) //nolint:wrapcheck // a test double
}

func (f *fixture) storeStates() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.states...)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	reg := store.NewMemoryStore()
	group := newGroup(t, reg, store.NewToken{})
	if err := reg.SetGroupStore(context.Background(), group, true); err != nil {
		t.Fatalf("SetGroupStore() error = %v", err)
	}
	blobs := &flakyBlobs{Disk: blob.NewDisk(t.TempDir(), 1<<20)}
	clk := &clock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	f := &fixture{reg: reg, blobs: blobs, clock: clk, group: group}
	f.svc = items.New(items.Options{
		Registry: reg,
		Blobs:    blobs,
		Config: items.Config{
			MaxGroupBytes: 1 << 20, MaxItemBytes: 1 << 20, MaxGroupItems: 10,
			ChunkBytes: 4, SingleFrameMaxBytes: 8, UploadTTL: time.Minute,
		},
		Logger: slog.New(slog.DiscardHandler),
		Now:    clk.Now,
		Hooks: items.Hooks{OnStoreState: func(_ context.Context, _ string, enabled bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.states = append(f.states, enabled)
		}},
	})
	return f
}

func (f *fixture) usage(t *testing.T) store.ItemUsage {
	t.Helper()
	u, err := f.reg.StoreUsage(context.Background(), f.group)
	if err != nil {
		t.Fatalf("StoreUsage() error = %v", err)
	}
	return u
}

func (f *fixture) begin(t *testing.T, device, item string, size int64) string {
	t.Helper()
	info, err := f.svc.BeginUpload(context.Background(), f.group, device, item, 1, size)
	if err != nil {
		t.Fatalf("BeginUpload(%s) error = %v", item, err)
	}
	return info.Handle
}

func putReq(item, handle string, size int64) items.PutRequest {
	return items.PutRequest{
		ItemID: item, Epoch: 1, Size: size,
		WrappedKeys:  bytes.Repeat([]byte{1}, items.WrappedItemKeysSize),
		SealedMeta:   bytes.Repeat([]byte{2}, items.SealedMetaSize),
		UploadHandle: handle,
	}
}

// TestUploadsBelongToTheirDevice: a handle is not a capability; another
// device cannot write to, commit or abort an upload it did not begin.
func TestUploadsBelongToTheirDevice(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	handle := f.begin(t, "laptop", "item", 6)

	if _, err := f.svc.WriteChunk(ctx, f.group, "phone", handle, 0, []byte("abcd")); !errors.Is(err, items.ErrUploadNotFound) {
		t.Errorf("WriteChunk(other device) error = %v, want %v", err, items.ErrUploadNotFound)
	}
	if _, err := f.svc.Put(ctx, f.group, "phone", putReq("item", handle, 6)); !errors.Is(err, items.ErrUploadNotFound) {
		t.Errorf("Put(other device) error = %v, want %v", err, items.ErrUploadNotFound)
	}
	if err := f.svc.AbortUpload(ctx, f.group, "phone", handle); err != nil {
		t.Errorf("AbortUpload(other device) error = %v, want nil", err)
	}
	if !f.svc.OwnsUpload(handle) {
		t.Fatal("another device's abort ended the upload")
	}
	if _, err := f.svc.BeginUpload(ctx, f.group, "phone", "item", 1, 6); !errors.Is(err, store.ErrItemExists) {
		t.Errorf("BeginUpload(same item) error = %v, want %v", err, store.ErrItemExists)
	}
}

// TestUploadChecks: chunks arrive in order at the agreed size, and a commit
// must name what was begun and come after the last chunk.
func TestUploadChecks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	handle := f.begin(t, "laptop", "item", 6)

	if _, err := f.svc.WriteChunk(ctx, f.group, "laptop", handle, 1, []byte("abcd")); !errors.Is(err, items.ErrInvalidChunk) {
		t.Errorf("WriteChunk(out of order) error = %v, want %v", err, items.ErrInvalidChunk)
	}
	if _, err := f.svc.WriteChunk(ctx, f.group, "laptop", handle, 0, []byte("abc")); !errors.Is(err, items.ErrInvalidChunk) {
		t.Errorf("WriteChunk(short) error = %v, want %v", err, items.ErrInvalidChunk)
	}
	if got, err := f.svc.WriteChunk(ctx, f.group, "laptop", handle, 0, []byte("abcd")); err != nil || got != 4 {
		t.Fatalf("WriteChunk(0) = %d, %v, want 4, nil", got, err)
	}
	if _, err := f.svc.Put(ctx, f.group, "laptop", putReq("item", handle, 6)); !errors.Is(err, items.ErrInvalidChunk) {
		t.Errorf("Put(before the last chunk) error = %v, want %v", err, items.ErrInvalidChunk)
	}
	if _, err := f.svc.WriteChunk(ctx, f.group, "laptop", handle, 1, []byte("ef")); err != nil {
		t.Fatalf("WriteChunk(1) error = %v", err)
	}
	if _, err := f.svc.Put(ctx, f.group, "laptop", putReq("other", handle, 6)); !errors.Is(err, items.ErrUploadMismatch) {
		t.Errorf("Put(other item) error = %v, want %v", err, items.ErrUploadMismatch)
	}
	it, err := f.svc.Put(ctx, f.group, "laptop", putReq("item", handle, 6))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if it.Size != 6 || it.Pending {
		t.Errorf("Put() = %+v, want a committed 6-byte item", it)
	}
	if f.svc.OwnsUpload(handle) {
		t.Error("the upload is still open after its commit")
	}
	if got, want := f.usage(t), (store.ItemUsage{Bytes: 6 + items.RecordBytes, Items: 1}); got != want {
		t.Errorf("usage = %+v, want %+v", got, want)
	}
}

// TestIdleUploadsGiveTheirRoomBack: an upload idle past the timeout is
// abandoned, its reservation released and its handle forgotten.
func TestIdleUploadsGiveTheirRoomBack(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	handle := f.begin(t, "laptop", "item", 6)
	if got := f.usage(t); got.Items != 1 {
		t.Fatalf("usage while uploading = %+v, want the reservation counted", got)
	}

	if n := f.svc.ReapUploads(ctx, f.clock.Now()); n != 0 {
		t.Errorf("ReapUploads(now) = %d, want 0", n)
	}
	f.clock.Advance(2 * time.Minute)
	if n := f.svc.ReapUploads(ctx, f.clock.Now()); n != 1 {
		t.Errorf("ReapUploads(later) = %d, want 1", n)
	}
	if got := f.usage(t); got != (store.ItemUsage{}) {
		t.Errorf("usage after the timeout = %+v, want nothing", got)
	}
	if _, err := f.svc.WriteChunk(ctx, f.group, "laptop", handle, 0, []byte("abcd")); !errors.Is(err, items.ErrUploadNotFound) {
		t.Errorf("WriteChunk(timed out) error = %v, want %v", err, items.ErrUploadNotFound)
	}
}

// TestReleaseOrphans: a reservation a previous process left behind is
// released at startup, with its half-written blob.
func TestReleaseOrphans(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	err := f.reg.ReserveItem(ctx, store.ItemReservation{
		GroupID: f.group, ItemID: "orphan", Epoch: 1, Size: 3, Accounted: 3 + items.RecordBytes, StartedAt: f.clock.Now(),
	}, store.ItemLimits{GroupItems: 10})
	if err != nil {
		t.Fatalf("ReserveItem() error = %v", err)
	}
	if err := f.blobs.PutItem(ctx, f.group, "orphan", 3, bytes.NewReader([]byte("abc"))); err != nil {
		t.Fatalf("PutItem() error = %v", err)
	}

	n, err := f.svc.ReleaseOrphans(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ReleaseOrphans() = %d, %v, want 1, nil", n, err)
	}
	if got := f.usage(t); got != (store.ItemUsage{}) {
		t.Errorf("usage after the release = %+v, want nothing", got)
	}
	if _, err := f.blobs.GetItemRange(ctx, f.group, "orphan", 0, 1); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("the orphan's blob is still there: GetItemRange() error = %v", err)
	}
}
