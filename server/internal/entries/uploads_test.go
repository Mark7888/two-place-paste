package entries_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
)

const (
	upInline = 100
	upFrame  = 1000
	upChunk  = 400
	upMax    = 5000
)

type uploadFixture struct {
	svc    *entries.Service
	store  *entries.MemoryStore
	ledger *quota.Memory
	root   string

	mu  sync.Mutex
	now time.Time
}

func (f *uploadFixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *uploadFixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func newUploadFixture(t *testing.T, limits quota.Limits) *uploadFixture {
	t.Helper()
	f := &uploadFixture{
		store:  entries.NewMemoryStore(),
		ledger: quota.NewMemory(quota.Options{Limits: limits}),
		root:   t.TempDir(),
		now:    time.Now().UTC(),
	}
	f.svc = entries.NewService(f.store, blob.NewDisk(f.root, upMax), entries.Options{
		InlineMaxBytes:      upInline,
		MaxBytes:            upMax,
		SingleFrameMaxBytes: upFrame,
		ChunkBytes:          upChunk,
		UploadTTL:           time.Minute,
		MaxUploadsPerDevice: 2,
		Quota:               f.ledger,
		Now:                 f.clock,
	})
	return f
}

// body is size bytes that differ by position, so a chunk out of place shows.
func body(size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

// upload sends a whole entry through the chunked path and commits it.
func (f *uploadFixture) upload(t *testing.T, group, device, id string, data []byte) entries.Meta {
	t.Helper()
	ctx := context.Background()
	info, err := f.svc.BeginUpload(ctx, group, device, 1, id, int64(len(data)))
	if err != nil {
		t.Fatalf("BeginUpload() error = %v", err)
	}
	if info.ChunkBytes != upChunk {
		t.Errorf("ChunkBytes = %d, want %d", info.ChunkBytes, upChunk)
	}
	for i := 0; i*upChunk < len(data); i++ {
		end := min((i+1)*upChunk, len(data))
		got, err := f.svc.WriteChunk(ctx, group, device, info.Handle, uint32(i), data[i*upChunk:end]) //nolint:gosec // small test index
		if err != nil {
			t.Fatalf("WriteChunk(%d) error = %v", i, err)
		}
		if got != int64(end) {
			t.Errorf("WriteChunk(%d) received = %d, want %d", i, got, end)
		}
	}
	m, err := f.svc.CommitUpload(ctx, group, device, info.Handle, id, 1, int64(len(data)))
	if err != nil {
		t.Fatalf("CommitUpload() error = %v", err)
	}
	return m
}

// fetchAll reads an entry back range by range.
func (f *uploadFixture) fetchAll(t *testing.T, group, id string) []byte {
	t.Helper()
	var out []byte
	for {
		m, data, err := f.svc.FetchRange(context.Background(), group, id, int64(len(out)), 0)
		if err != nil {
			t.Fatalf("FetchRange(%d) error = %v", len(out), err)
		}
		if len(data) == 0 || len(data) > upChunk {
			t.Fatalf("FetchRange(%d) returned %d bytes", len(out), len(data))
		}
		out = append(out, data...)
		if int64(len(out)) == m.Size {
			return out
		}
	}
}

func TestUploadRoundTrip(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t, quota.Limits{})
	ctx := context.Background()
	data := body(upMax) // the cap exactly, five times the frame

	m := f.upload(t, "g", "laptop", "big", data)
	if m.ID != "big" || m.Size != upMax || m.Inline() {
		t.Errorf("committed meta = %+v", m)
	}
	if got := f.fetchAll(t, "g", "big"); !bytes.Equal(got, data) {
		t.Errorf("fetched %d bytes that differ from the %d uploaded", len(got), len(data))
	}

	// It is the group's latest, and too big for the single-frame fetch.
	latest, _, err := f.svc.Latest(ctx, "g")
	if err != nil || latest.ID != "big" {
		t.Errorf("Latest() = %q, %v; want big", latest.ID, err)
	}
	if _, _, err := f.svc.Fetch(ctx, "g", "big"); !errors.Is(err, entries.ErrTooLarge) {
		t.Errorf("Fetch(larger than a frame) error = %v, want %v", err, entries.ErrTooLarge)
	}

	// Accounted, and committed, so it can be evicted later.
	if u, _ := f.ledger.GroupUsage(ctx, "g"); u.Bytes != upMax {
		t.Errorf("accounted %d bytes, want %d", u.Bytes, upMax)
	}
	if n, _ := f.ledger.ReleasePending(ctx); n != 0 {
		t.Errorf("%d entries still pending after commit, want 0", n)
	}

	// Ranges of a small inline entry work too.
	if _, err := f.svc.Put(ctx, "g", 1, "small", 50, bytes.NewReader(body(50))); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	_, part, err := f.svc.FetchRange(ctx, "g", "small", 10, 5)
	if err != nil || !bytes.Equal(part, body(50)[10:15]) {
		t.Errorf("FetchRange(inline) = %v, %v", part, err)
	}
	if _, _, err := f.svc.FetchRange(ctx, "g", "small", 50, 1); !errors.Is(err, entries.ErrInvalidRange) {
		t.Errorf("FetchRange(past the end) error = %v, want %v", err, entries.ErrInvalidRange)
	}
	if _, _, err := f.svc.FetchRange(ctx, "other", "small", 0, 1); !errors.Is(err, entries.ErrNotFound) {
		t.Errorf("FetchRange(other group) error = %v, want %v", err, entries.ErrNotFound)
	}
}

func TestSingleFrameLimit(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t, quota.Limits{})
	_, err := f.svc.Put(context.Background(), "g", 1, "", upFrame+1, bytes.NewReader(body(upFrame+1)))
	if !errors.Is(err, entries.ErrTooLarge) {
		t.Errorf("Put(over one frame) error = %v, want %v", err, entries.ErrTooLarge)
	}
}

func TestUploadRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newUploadFixture(t, quota.Limits{})

	if _, err := f.svc.BeginUpload(ctx, "g", "d", 1, "", upInline); !errors.Is(err, entries.ErrNotChunked) {
		t.Errorf("BeginUpload(inline-sized) error = %v, want %v", err, entries.ErrNotChunked)
	}
	if _, err := f.svc.BeginUpload(ctx, "g", "d", 1, "", upMax+1); !errors.Is(err, entries.ErrTooLarge) {
		t.Errorf("BeginUpload(over cap) error = %v, want %v", err, entries.ErrTooLarge)
	}
	if _, err := f.svc.BeginUpload(ctx, "g", "d", 1, "no/slash", 500); !errors.Is(err, entries.ErrInvalidID) {
		t.Errorf("BeginUpload(bad id) error = %v, want %v", err, entries.ErrInvalidID)
	}

	info, err := f.svc.BeginUpload(ctx, "g", "d", 1, "x", 900)
	if err != nil {
		t.Fatalf("BeginUpload() error = %v", err)
	}
	if info.EntryID != "x" {
		t.Errorf("EntryID = %q, want x", info.EntryID)
	}
	if _, err := f.svc.BeginUpload(ctx, "g", "d", 1, "x", 900); !errors.Is(err, entries.ErrEntryExists) {
		t.Errorf("BeginUpload(id in flight) error = %v, want %v", err, entries.ErrEntryExists)
	}

	// Out of order, wrong size: refused, and the upload carries on.
	if _, err := f.svc.WriteChunk(ctx, "g", "d", info.Handle, 1, body(upChunk)); !errors.Is(err, entries.ErrInvalidChunk) {
		t.Errorf("WriteChunk(skipping ahead) error = %v, want %v", err, entries.ErrInvalidChunk)
	}
	if _, err := f.svc.WriteChunk(ctx, "g", "d", info.Handle, 0, body(upChunk-1)); !errors.Is(err, entries.ErrInvalidChunk) {
		t.Errorf("WriteChunk(short chunk) error = %v, want %v", err, entries.ErrInvalidChunk)
	}
	if _, err := f.svc.WriteChunk(ctx, "g", "d", info.Handle, 0, body(upChunk)); err != nil {
		t.Fatalf("WriteChunk(0) error = %v", err)
	}
	if _, err := f.svc.CommitUpload(ctx, "g", "d", info.Handle, "x", 1, 900); !errors.Is(err, entries.ErrInvalidChunk) {
		t.Errorf("CommitUpload(incomplete) error = %v, want %v", err, entries.ErrInvalidChunk)
	}

	// Handles are not capabilities: another device, or group, sees nothing.
	if _, err := f.svc.WriteChunk(ctx, "g", "thief", info.Handle, 1, body(upChunk)); !errors.Is(err, entries.ErrUploadNotFound) {
		t.Errorf("WriteChunk(other device) error = %v, want %v", err, entries.ErrUploadNotFound)
	}
	if _, err := f.svc.WriteChunk(ctx, "other", "d", info.Handle, 1, body(upChunk)); !errors.Is(err, entries.ErrUploadNotFound) {
		t.Errorf("WriteChunk(other group) error = %v, want %v", err, entries.ErrUploadNotFound)
	}

	if _, err := f.svc.WriteChunk(ctx, "g", "d", info.Handle, 1, body(upChunk)); err != nil {
		t.Fatalf("WriteChunk(1) error = %v", err)
	}
	if _, err := f.svc.WriteChunk(ctx, "g", "d", info.Handle, 2, body(100)); err != nil {
		t.Fatalf("WriteChunk(last) error = %v", err)
	}
	for _, bad := range []struct {
		id    string
		epoch uint64
		size  int64
	}{{"y", 1, 900}, {"x", 2, 900}, {"x", 1, 901}} {
		if _, err := f.svc.CommitUpload(ctx, "g", "d", info.Handle, bad.id, bad.epoch, bad.size); !errors.Is(err, entries.ErrUploadMismatch) {
			t.Errorf("CommitUpload(%+v) error = %v, want %v", bad, err, entries.ErrUploadMismatch)
		}
	}
	if _, err := f.svc.CommitUpload(ctx, "g", "d", info.Handle, "", 1, 900); err != nil {
		t.Fatalf("CommitUpload(empty id means the upload's) error = %v", err)
	}
	if _, err := f.svc.CommitUpload(ctx, "g", "d", info.Handle, "", 1, 900); !errors.Is(err, entries.ErrUploadNotFound) {
		t.Errorf("second CommitUpload() error = %v, want %v", err, entries.ErrUploadNotFound)
	}
	if _, err := f.svc.BeginUpload(ctx, "g", "d", 1, "x", 900); !errors.Is(err, entries.ErrEntryExists) {
		t.Errorf("BeginUpload(id now stored) error = %v, want %v", err, entries.ErrEntryExists)
	}
}

func TestUploadsPerDeviceLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newUploadFixture(t, quota.Limits{})
	for _, id := range []string{"a", "b"} {
		if _, err := f.svc.BeginUpload(ctx, "g", "d", 1, id, 500); err != nil {
			t.Fatalf("BeginUpload(%s) error = %v", id, err)
		}
	}
	if _, err := f.svc.BeginUpload(ctx, "g", "d", 1, "c", 500); !errors.Is(err, entries.ErrTooManyUploads) {
		t.Errorf("third BeginUpload() error = %v, want %v", err, entries.ErrTooManyUploads)
	}
	if _, err := f.svc.BeginUpload(ctx, "g", "other-device", 1, "c", 500); err != nil {
		t.Errorf("BeginUpload(another device) error = %v", err)
	}
}

// TestUploadAbortAndTimeout: an upload that is aborted, or left idle past the
// timeout, gives its room back and leaves nothing behind.
func TestUploadAbortAndTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newUploadFixture(t, quota.Limits{TotalBytes: upMax})

	aborted, err := f.svc.BeginUpload(ctx, "g", "d", 1, "aborted", 4000)
	if err != nil {
		t.Fatalf("BeginUpload() error = %v", err)
	}
	// The room is reserved at begin: a second upload that needs it is refused.
	if _, err := f.svc.BeginUpload(ctx, "g", "d2", 1, "late", 2000); !errors.Is(err, entries.ErrQuotaExceeded) {
		t.Errorf("BeginUpload(room reserved) error = %v, want %v", err, entries.ErrQuotaExceeded)
	}
	if err := f.svc.AbortUpload(ctx, "g", "d", aborted.Handle); err != nil {
		t.Fatalf("AbortUpload() error = %v", err)
	}
	if err := f.svc.AbortUpload(ctx, "g", "d", aborted.Handle); err != nil {
		t.Errorf("second AbortUpload() error = %v, want nil", err)
	}
	if u, _ := f.ledger.Usage(ctx); u.Bytes != 0 {
		t.Errorf("accounted %d bytes after abort, want 0", u.Bytes)
	}

	idle, err := f.svc.BeginUpload(ctx, "g", "d", 1, "idle", 4000)
	if err != nil {
		t.Fatalf("BeginUpload() error = %v", err)
	}
	if _, err := f.svc.WriteChunk(ctx, "g", "d", idle.Handle, 0, body(upChunk)); err != nil {
		t.Fatalf("WriteChunk() error = %v", err)
	}
	f.advance(2 * time.Minute)
	if n := f.svc.ReapUploads(ctx, f.clock()); n != 1 {
		t.Errorf("ReapUploads() = %d, want 1", n)
	}
	if _, err := f.svc.WriteChunk(ctx, "g", "d", idle.Handle, 1, body(upChunk)); !errors.Is(err, entries.ErrUploadNotFound) {
		t.Errorf("WriteChunk(after timeout) error = %v, want %v", err, entries.ErrUploadNotFound)
	}
	if u, _ := f.ledger.Usage(ctx); u.Bytes != 0 {
		t.Errorf("accounted %d bytes after timeout, want 0", u.Bytes)
	}

	// A busy upload still ends at MaxUploadLifetime.
	busy, err := f.svc.BeginUpload(ctx, "g", "d", 1, "busy", 4000)
	if err != nil {
		t.Fatalf("BeginUpload() error = %v", err)
	}
	for i := uint32(0); i < 3; i++ {
		f.advance(time.Minute / 2)
		if _, err := f.svc.WriteChunk(ctx, "g", "d", busy.Handle, i, body(upChunk)); err != nil {
			t.Fatalf("WriteChunk(%d) error = %v", i, err)
		}
	}
	f.advance(entries.MaxUploadLifetime)
	if _, err := f.svc.WriteChunk(ctx, "g", "d", busy.Handle, 3, body(upChunk)); !errors.Is(err, entries.ErrUploadNotFound) {
		t.Errorf("WriteChunk(past the lifetime) error = %v, want %v", err, entries.ErrUploadNotFound)
	}

	f.svc.AbortAllUploads(ctx)
	if u, _ := f.ledger.Usage(ctx); u.Bytes != 0 {
		t.Errorf("accounted %d bytes after everything ended, want 0", u.Bytes)
	}
}

// TestUploadedEntryOutlivesNoBlob: the entry expires 24 h after its commit,
// and its blob's bucket must still be there then, however long the upload
// took within its lifetime.
func TestUploadedEntryOutlivesNoBlob(t *testing.T) {
	t.Parallel()
	f := newUploadFixture(t, quota.Limits{})
	ctx := context.Background()
	info, err := f.svc.BeginUpload(ctx, "g", "d", 1, "slow", 500)
	if err != nil {
		t.Fatalf("BeginUpload() error = %v", err)
	}
	for i, n := range []int{upChunk, 100} {
		f.advance(50 * time.Second)
		if _, err := f.svc.WriteChunk(ctx, "g", "d", info.Handle, uint32(i), body(n)); err != nil { //nolint:gosec // small test index
			t.Fatalf("WriteChunk(%d) error = %v", i, err)
		}
	}
	m, err := f.svc.CommitUpload(ctx, "g", "d", info.Handle, "slow", 1, 500)
	if err != nil {
		t.Fatalf("CommitUpload() error = %v", err)
	}
	bucket, _, _ := strings.Cut(m.StorageRef, "/")
	end, ok := blob.BucketEnd(bucket)
	if !ok || end.Before(m.ExpiresAt) {
		t.Errorf("blob bucket %s ends at %v, before the entry expires at %v", bucket, end, m.ExpiresAt)
	}
}
