package blob_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
)

// The S3 half of these tests runs against a real S3-compatible store. CI runs
// MinIO as a service container; a developer runs `minio server` locally. When
// neither is there the S3 half skips with a reason rather than failing
// (docs/conventions.md §5), exactly like the Redis tests.
func s3Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

const s3TestBucket = "tpp-test"

func s3Options(t *testing.T, maxSize int64) blob.S3Options {
	t.Helper()
	var b [6]byte
	_, _ = rand.Read(b[:])
	return blob.S3Options{
		Endpoint:        s3Env("TPP_TEST_S3_ENDPOINT", "127.0.0.1:9000"),
		Region:          "us-east-1",
		Bucket:          s3TestBucket,
		Prefix:          "run-" + hex.EncodeToString(b[:]),
		AccessKeyID:     s3Env("TPP_TEST_S3_ACCESS_KEY", "minioadmin"),
		SecretAccessKey: s3Env("TPP_TEST_S3_SECRET_KEY", "minioadmin"),
		MaxSize:         maxSize,
	}
}

func s3Reachable(t *testing.T, opts blob.S3Options) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+opts.Endpoint+"/minio/health/live", nil)
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		t.Skipf("s3 not available at %s: %v", opts.Endpoint, err)
	}
	_ = resp.Body.Close()
}

func newTestS3(t *testing.T, maxSize int64) *blob.S3 {
	t.Helper()
	opts := s3Options(t, maxSize)
	s3Reachable(t, opts)
	b, err := blob.NewS3(opts)
	if err != nil {
		t.Fatalf("NewS3() error = %v", err)
	}
	if err := b.Check(context.Background(), true); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	return b
}

// backendUnderTest is a backend plus its listing, which is what verification
// and these tests read.
type backendUnderTest interface {
	blob.Backend
	blob.Lister
}

// forEachBackend runs the same questions against every backend: disk is the
// default, S3 the alternative, and switching between them must change nothing
// a caller can see.
func forEachBackend(t *testing.T, maxSize int64, run func(t *testing.T, b backendUnderTest)) {
	t.Helper()
	t.Run("disk", func(t *testing.T) {
		t.Parallel()
		run(t, blob.NewDisk(t.TempDir(), maxSize))
	})
	t.Run("s3", func(t *testing.T) {
		t.Parallel()
		run(t, newTestS3(t, maxSize))
	})
}

func readAll(t *testing.T, b blob.Backend, ref string) string {
	t.Helper()
	rc, err := b.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get(%s) error = %v", ref, err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", ref, err)
	}
	return string(body)
}

func TestBackendRoundTrip(t *testing.T) {
	t.Parallel()
	forEachBackend(t, 1024, func(t *testing.T, b backendUnderTest) {
		ctx := context.Background()
		expires := time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC)

		ref, err := b.Put(ctx, "entry-1", expires, strings.NewReader("ciphertext"))
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		if want := "2026090815/entry-1.bin"; ref != want {
			t.Errorf("Put() ref = %q, want %q", ref, want)
		}
		if got := readAll(t, b, ref); got != "ciphertext" {
			t.Errorf("Get() body = %q, want %q", got, "ciphertext")
		}

		// Exactly at the cap is accepted; one byte over is refused and
		// leaves nothing behind.
		atCap := strings.Repeat("c", 1024)
		capRef, err := b.Put(ctx, "at-cap", expires, strings.NewReader(atCap))
		if err != nil {
			t.Fatalf("Put(at cap) error = %v", err)
		}
		if got := readAll(t, b, capRef); got != atCap {
			t.Errorf("Get(at cap) returned %d bytes, want 1024", len(got))
		}
		if _, err := b.Put(ctx, "over-cap", expires, strings.NewReader(atCap+"!")); !errors.Is(err, blob.ErrTooLarge) {
			t.Errorf("Put(over cap) error = %v, want %v", err, blob.ErrTooLarge)
		}
		if _, err := b.Get(ctx, "2026090815/over-cap.bin"); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("Get(refused) error = %v, want %v", err, blob.ErrNotFound)
		}

		// Create, never replace: a second blob for the same id is refused
		// and the first one's bytes survive.
		if _, err := b.Put(ctx, "entry-1", expires, strings.NewReader("someone else")); !errors.Is(err, blob.ErrExists) {
			t.Errorf("Put(duplicate) error = %v, want %v", err, blob.ErrExists)
		}
		if got := readAll(t, b, ref); got != "ciphertext" {
			t.Errorf("Get() after a refused overwrite = %q, want the original", got)
		}

		if err := b.Delete(ctx, ref); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if _, err := b.Get(ctx, ref); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("Get(deleted) error = %v, want %v", err, blob.ErrNotFound)
		}
		if err := b.Delete(ctx, ref); err != nil {
			t.Errorf("Delete(missing) error = %v, want nil", err)
		}
	})
}

func TestBackendRejectsBadRefs(t *testing.T) {
	t.Parallel()
	forEachBackend(t, 1024, func(t *testing.T, b backendUnderTest) {
		ctx := context.Background()
		for _, ref := range []string{"", "x.bin", "2026090815/../x.bin", "notabucket/x.bin", "2026090815/x", "2026090815/a/b.bin", "2026090815/a b.bin"} {
			if _, err := b.Get(ctx, ref); !errors.Is(err, blob.ErrInvalidRef) {
				t.Errorf("Get(%q) error = %v, want %v", ref, err, blob.ErrInvalidRef)
			}
			if err := b.Delete(ctx, ref); !errors.Is(err, blob.ErrInvalidRef) {
				t.Errorf("Delete(%q) error = %v, want %v", ref, err, blob.ErrInvalidRef)
			}
		}
		if _, err := b.Put(ctx, "../escape", time.Now(), strings.NewReader("x")); !errors.Is(err, blob.ErrInvalidRef) {
			t.Errorf("Put(bad id) error = %v, want %v", err, blob.ErrInvalidRef)
		}
	})
}

func TestBackendSweepAndList(t *testing.T) {
	t.Parallel()
	forEachBackend(t, 1024, func(t *testing.T, b backendUnderTest) {
		ctx := context.Background()
		now := time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC)

		put := func(id string, expires time.Time) string {
			t.Helper()
			ref, err := b.Put(ctx, id, expires, strings.NewReader("x"))
			if err != nil {
				t.Fatalf("Put(%s) error = %v", id, err)
			}
			return ref
		}
		gone1 := put("gone-1", now.Add(-3*time.Hour))
		gone2 := put("gone-2", now.Add(-3*time.Hour))
		gone3 := put("gone-3", now.Add(-90*time.Minute))
		// Its bucket ends at 15:00, which has not passed at 14:30.
		live := put("live", now.Add(-10*time.Minute))
		later := put("later", now.Add(5*time.Hour))

		refs, err := b.ListRefs(ctx)
		if err != nil {
			t.Fatalf("ListRefs() error = %v", err)
		}
		slices.Sort(refs)
		want := []string{gone1, gone2, gone3, live, later}
		slices.Sort(want)
		if !slices.Equal(refs, want) {
			t.Errorf("ListRefs() = %v, want %v", refs, want)
		}

		if err := b.Sweep(ctx, now); err != nil {
			t.Fatalf("Sweep() error = %v", err)
		}
		for _, ref := range []string{gone1, gone2, gone3} {
			if _, err := b.Get(ctx, ref); !errors.Is(err, blob.ErrNotFound) {
				t.Errorf("Get(%s) after sweep error = %v, want %v", ref, err, blob.ErrNotFound)
			}
		}
		for _, ref := range []string{live, later} {
			if got := readAll(t, b, ref); got != "x" {
				t.Errorf("Get(%s) after sweep = %q, want it kept", ref, got)
			}
		}
		refs, err = b.ListRefs(ctx)
		if err != nil {
			t.Fatalf("ListRefs() error = %v", err)
		}
		if len(refs) != 2 {
			t.Errorf("ListRefs() after sweep = %v, want only the live two", refs)
		}
	})
}

// chunk is a deterministic chunk of n bytes, distinct per index, so a part
// written out of place would be noticed.
func chunk(index, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + (index+i)%26)
	}
	return b
}

func TestBackendChunkedUpload(t *testing.T) {
	t.Parallel()
	// S3 parts other than the last must be at least 5 MiB.
	const part = 5 << 20
	forEachBackend(t, 3*part, func(t *testing.T, b backendUnderTest) {
		ctx := context.Background()
		expires := time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC)
		size := int64(2*part + 1234)

		up, err := b.BeginUpload(ctx, "chunked", expires, size)
		if err != nil {
			t.Fatalf("BeginUpload() error = %v", err)
		}
		var want []byte
		for i, n := range []int{part, part, 1234} {
			c := chunk(i, n)
			want = append(want, c...)
			if err := up.Write(ctx, c); err != nil {
				t.Fatalf("Write(%d) error = %v", i, err)
			}
		}
		if err := up.Write(ctx, []byte("x")); !errors.Is(err, blob.ErrTooLarge) {
			t.Errorf("Write(past size) error = %v, want %v", err, blob.ErrTooLarge)
		}

		// Nothing is visible before Complete.
		if _, err := b.Get(ctx, "2026090815/chunked.bin"); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("Get(before Complete) error = %v, want %v", err, blob.ErrNotFound)
		}

		ref, err := up.Complete(ctx)
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		if ref != "2026090815/chunked.bin" {
			t.Errorf("Complete() ref = %q", ref)
		}
		if got := readAll(t, b, ref); got != string(want) {
			t.Errorf("Get() returned %d bytes, not the %d written in order", len(got), len(want))
		}

		// Ranges: across a part boundary, and at the very end.
		for _, r := range [][2]int64{{part - 10, 20}, {size - 5, 5}, {0, 1}} {
			rc, err := b.GetRange(ctx, ref, r[0], r[1])
			if err != nil {
				t.Fatalf("GetRange(%d, %d) error = %v", r[0], r[1], err)
			}
			got, err := io.ReadAll(rc)
			rc.Close()
			if err != nil || string(got) != string(want[r[0]:r[0]+r[1]]) {
				t.Errorf("GetRange(%d, %d) = %q, %v; want %q", r[0], r[1], got, err, want[r[0]:r[0]+r[1]])
			}
		}
		if _, err := b.GetRange(ctx, "2026090815/missing.bin", 0, 1); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("GetRange(missing) error = %v, want %v", err, blob.ErrNotFound)
		}

		// Create, never replace: a second upload for the same id fails at
		// Complete and the first blob survives.
		again, err := b.BeginUpload(ctx, "chunked", expires, 3)
		if err != nil {
			t.Fatalf("BeginUpload(again) error = %v", err)
		}
		if err := again.Write(ctx, []byte("new")); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if _, err := again.Complete(ctx); !errors.Is(err, blob.ErrExists) {
			t.Errorf("Complete(duplicate) error = %v, want %v", err, blob.ErrExists)
		}
		if got := readAll(t, b, ref); len(got) != int(size) {
			t.Errorf("first blob after a refused duplicate is %d bytes, want %d", len(got), size)
		}
	})
}

func TestBackendUploadRefusals(t *testing.T) {
	t.Parallel()
	forEachBackend(t, 1024, func(t *testing.T, b backendUnderTest) {
		ctx := context.Background()
		expires := time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC)

		if _, err := b.BeginUpload(ctx, "huge", expires, 1025); !errors.Is(err, blob.ErrTooLarge) {
			t.Errorf("BeginUpload(over cap) error = %v, want %v", err, blob.ErrTooLarge)
		}
		if _, err := b.BeginUpload(ctx, "../x", expires, 10); !errors.Is(err, blob.ErrInvalidRef) {
			t.Errorf("BeginUpload(bad id) error = %v, want %v", err, blob.ErrInvalidRef)
		}

		short, err := b.BeginUpload(ctx, "short", expires, 10)
		if err != nil {
			t.Fatalf("BeginUpload() error = %v", err)
		}
		if err := short.Write(ctx, []byte("12345")); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if _, err := short.Complete(ctx); !errors.Is(err, blob.ErrIncomplete) {
			t.Errorf("Complete(short) error = %v, want %v", err, blob.ErrIncomplete)
		}
		if err := short.Abort(ctx); err != nil {
			t.Errorf("Abort() error = %v", err)
		}
		if _, err := b.Get(ctx, "2026090815/short.bin"); !errors.Is(err, blob.ErrNotFound) {
			t.Errorf("Get(aborted) error = %v, want %v", err, blob.ErrNotFound)
		}
	})
}

// TestBackendSweepsAbandonedUploads: an upload nobody completes or aborts —
// the server crashed — is cleaned up by the sweep once it is old enough, and
// not before.
func TestBackendSweepsAbandonedUploads(t *testing.T) {
	t.Parallel()
	forEachBackend(t, 1024, func(t *testing.T, b backendUnderTest) {
		ctx := context.Background()
		up, err := b.BeginUpload(ctx, "abandoned", time.Now().Add(24*time.Hour), 10)
		if err != nil {
			t.Fatalf("BeginUpload() error = %v", err)
		}
		if err := up.Write(ctx, []byte("12345")); err != nil {
			t.Fatalf("Write() error = %v", err)
		}

		if err := b.Sweep(ctx, time.Now()); err != nil {
			t.Fatalf("Sweep() error = %v", err)
		}
		if err := up.Write(ctx, []byte("67890")); err != nil {
			t.Fatalf("Write() after a sweep error = %v; a live upload must survive it", err)
		}

		if err := b.Sweep(ctx, time.Now().Add(blob.StaleUploadAge+time.Minute)); err != nil {
			t.Fatalf("Sweep(later) error = %v", err)
		}
		if _, err := up.Complete(ctx); err == nil {
			t.Errorf("Complete() after the sweep took the upload = nil error, want a failure")
		}
	})
}
