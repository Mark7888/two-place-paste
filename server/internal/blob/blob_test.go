package blob_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
)

// mustLoc loads a zone or skips: a machine without tzdata cannot answer the
// DST question at all, and pretending it passed would be worse than skipping.
func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("timezone %s unavailable: %v", name, err)
	}
	return loc
}

func TestBucket(t *testing.T) {
	t.Parallel()

	newYork := mustLoc(t, "America/New_York")
	kathmandu := mustLoc(t, "Asia/Kathmandu") // UTC+05:45: a non-hour offset

	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{
			name: "exact hour is its own bucket",
			in:   time.Date(2026, 9, 8, 14, 0, 0, 0, time.UTC),
			want: "2026090814",
		},
		{
			name: "one nanosecond past the hour rounds up",
			in:   time.Date(2026, 9, 8, 14, 0, 0, 1, time.UTC),
			want: "2026090815",
		},
		{
			name: "mid-hour rounds up",
			in:   time.Date(2026, 9, 8, 14, 31, 12, 0, time.UTC),
			want: "2026090815",
		},
		{
			name: "rounding up crosses the day boundary",
			in:   time.Date(2026, 9, 8, 23, 30, 0, 0, time.UTC),
			want: "2026090900",
		},
		{
			// 2026-03-08 02:30 does not exist in New York: the clocks jump
			// from 01:59:59 EST to 03:00:00 EDT. The instant below is
			// 2026-03-08 01:30 EST = 06:30 UTC, and the bucket must follow
			// UTC, not the local wall clock (SPEC §4.5).
			name: "spring forward, local zone, expiry before the jump",
			in:   time.Date(2026, 3, 8, 1, 30, 0, 0, newYork),
			want: "2026030807",
		},
		{
			// 2026-03-08 03:30 EDT = 07:30 UTC. One local hour later than the
			// case above by the wall clock, one UTC hour later in truth.
			name: "spring forward, local zone, expiry after the jump",
			in:   time.Date(2026, 3, 8, 3, 30, 0, 0, newYork),
			want: "2026030808",
		},
		{
			// 2026-11-01 01:30 in New York happens twice. time.Date resolves
			// it to the first (EDT, 05:30 UTC); either way the bucket is a
			// UTC hour and no local ambiguity reaches it.
			name: "fall back, local zone",
			in:   time.Date(2026, 11, 1, 1, 30, 0, 0, newYork),
			want: "2026110106",
		},
		{
			name: "non-hour zone offset",
			in:   time.Date(2026, 9, 8, 14, 0, 0, 0, kathmandu), // 08:15 UTC
			want: "2026090809",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := blob.Bucket(tt.in); got != tt.want {
				t.Errorf("Bucket(%s) = %q, want %q", tt.in.Format(time.RFC3339Nano), got, tt.want)
			}
		})
	}
}

func TestExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		bucket string
		want   bool
	}{
		{name: "past bucket", bucket: "2026090813", want: true},
		{name: "bucket that ends exactly now", bucket: "2026090814", want: true},
		{name: "current bucket", bucket: "2026090815", want: false},
		{name: "future bucket", bucket: "2026090816", want: false},
		{name: "not a bucket", bucket: "tmp", want: false},
		{name: "not a bucket, right length", bucket: "20260908zz", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := blob.Expired(tt.bucket, now); got != tt.want {
				t.Errorf("Expired(%q, %s) = %v, want %v", tt.bucket, now.Format(time.RFC3339), got, tt.want)
			}
		})
	}
}

func TestDiskPutGetDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	d := blob.NewDisk(root, 1024)

	expires := time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC)
	ref, err := d.Put(ctx, "entry-1", expires, strings.NewReader("ciphertext"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if want := "2026090815/entry-1.bin"; ref != want {
		t.Errorf("Put() ref = %q, want %q", ref, want)
	}

	rc, err := d.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	body, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("reading blob: %v", err)
	}
	if string(body) != "ciphertext" {
		t.Errorf("Get() body = %q, want %q", body, "ciphertext")
	}

	if err := d.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := d.Get(ctx, ref); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("Get(deleted) error = %v, want %v", err, blob.ErrNotFound)
	}
	// Deleting a blob that is already gone is not an error: the bucket sweep
	// may have got there first (SPEC §4.5).
	if err := d.Delete(ctx, ref); err != nil {
		t.Errorf("Delete(missing) error = %v, want nil", err)
	}
}

func TestDiskRejectsBadRefs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	d := blob.NewDisk(t.TempDir(), 1024)

	refs := []string{
		"../../etc/passwd",
		"2026090815/../../../etc/passwd",
		"2026090815/a/b.bin",
		"2026090815/entry.txt",
		"notabucket/entry.bin",
		"entry.bin",
	}
	for _, ref := range refs {
		t.Run(ref, func(t *testing.T) {
			t.Parallel()
			if _, err := d.Get(ctx, ref); !errors.Is(err, blob.ErrInvalidRef) {
				t.Errorf("Get(%q) error = %v, want %v", ref, err, blob.ErrInvalidRef)
			}
		})
	}

	if _, err := d.Put(ctx, "../escape", time.Now(), strings.NewReader("x")); !errors.Is(err, blob.ErrInvalidRef) {
		t.Errorf("Put(bad entry id) error = %v, want %v", err, blob.ErrInvalidRef)
	}
}

// countingReader reports how much of a body was actually consumed, which is
// how this test proves the cap is enforced at the reader rather than after
// buffering (SPEC §4.3).
type countingReader struct {
	src  io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.read += int64(n)
	return n, err
}

func TestDiskPutOversizeRejectedAtTheReader(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	const maxSize = 4096
	d := blob.NewDisk(root, maxSize)

	// Ten times the cap, so a backend that buffered first would read it all.
	body := &countingReader{src: strings.NewReader(strings.Repeat("A", maxSize*10))}
	_, err := d.Put(ctx, "entry-big", time.Date(2026, 9, 8, 14, 0, 0, 0, time.UTC), body)
	if !errors.Is(err, blob.ErrTooLarge) {
		t.Fatalf("Put(oversize) error = %v, want %v", err, blob.ErrTooLarge)
	}
	if body.read > maxSize+1 {
		t.Errorf("Put(oversize) consumed %d bytes, want at most %d: the cap must be enforced at the reader", body.read, maxSize+1)
	}

	// Nothing may be left behind.
	entries, err := os.ReadDir(filepath.Join(root, "2026090814"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("listing bucket: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("bucket holds %d files after a rejected upload, want 0", len(entries))
	}

	// Exactly at the cap is accepted: the limit is inclusive.
	if _, err := d.Put(ctx, "entry-exact", time.Date(2026, 9, 8, 14, 0, 0, 0, time.UTC), strings.NewReader(strings.Repeat("A", maxSize))); err != nil {
		t.Errorf("Put(exactly at the cap) error = %v, want nil", err)
	}
}

func writeBlob(t *testing.T, root, bucket, entryID string) {
	t.Helper()
	dir := filepath.Join(root, bucket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, entryID+".bin"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write blob: %v", err)
	}
}

func TestDiskSweep(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	d := blob.NewDisk(root, 1024)

	now := time.Date(2026, 9, 8, 15, 30, 0, 0, time.UTC)
	writeBlob(t, root, "2026090812", "past-1")    // fully passed
	writeBlob(t, root, "2026090815", "current-1") // the hour we are in
	writeBlob(t, root, "2026090818", "future-1")  // still to come
	if err := os.MkdirAll(filepath.Join(root, "not-a-bucket"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := d.Sweep(ctx, now); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	tests := []struct {
		bucket string
		want   bool
	}{
		{bucket: "2026090812", want: false},
		{bucket: "2026090815", want: true},
		{bucket: "2026090818", want: true},
		{bucket: "not-a-bucket", want: true}, // unparsable names are left alone
	}
	for _, tt := range tests {
		_, err := os.Stat(filepath.Join(root, tt.bucket))
		if got := err == nil; got != tt.want {
			t.Errorf("after Sweep(), bucket %s exists = %v, want %v", tt.bucket, got, tt.want)
		}
	}

	// An empty root is not an error, and neither is a root that does not exist
	// yet: a server that has never stored a blob still sweeps at startup.
	if err := blob.NewDisk(filepath.Join(root, "never-written"), 1024).Sweep(ctx, now); err != nil {
		t.Errorf("Sweep(missing root) error = %v, want nil", err)
	}
}

// TestDiskSweepCostIsIndependentOfBlobCount is the O(buckets) claim of
// SPEC §4.5, measured rather than asserted in prose: 10 000 blobs across three
// buckets must cost one directory listing and one removal per expired bucket,
// with no per-file work at all.
func TestDiskSweepCostIsIndependentOfBlobCount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()

	const blobs = 10_000
	buckets := []string{"2026090810", "2026090811", "2026090812"}
	for i := range blobs {
		writeBlob(t, root, buckets[i%len(buckets)], fmt.Sprintf("entry-%d", i))
	}

	d := blob.NewDisk(root, 1024)
	readDirs, removeAlls := blob.CountOps(d)

	if err := d.Sweep(ctx, time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}

	if got := *readDirs; got != 1 {
		t.Errorf("Sweep() performed %d directory listings, want 1", got)
	}
	if got := *removeAlls; got != len(buckets) {
		t.Errorf("Sweep() performed %d removals, want %d (one per expired bucket)", got, len(buckets))
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("listing root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("root holds %d buckets after the sweep, want 0", len(entries))
	}
}

// TestDiskPutDoesNotReplaceAnExistingBlob pins the guarantee that makes a
// client-chosen entry id safe: a blob's filename is the entry id, so writing
// one that already exists must fail rather than destroy the ciphertext stored
// under it — which, entry ids being global, could belong to another group.
func TestDiskPutDoesNotReplaceAnExistingBlob(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	d := blob.NewDisk(t.TempDir(), 1<<20)
	expires := time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC)

	ref, err := d.Put(ctx, "entry-1", expires, strings.NewReader("original"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	if _, err := d.Put(ctx, "entry-1", expires, strings.NewReader("overwrite")); !errors.Is(err, blob.ErrExists) {
		t.Fatalf("second Put() error = %v, want blob.ErrExists", err)
	}

	rc, err := d.Get(ctx, ref)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "original" {
		t.Errorf("stored blob = %q, want %q", got, "original")
	}
}
