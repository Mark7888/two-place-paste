package entries_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
)

const (
	testInlineMax = 1024
	testMaxBytes  = 8192
)

func newService(t *testing.T, store entries.Store) (*entries.Service, *blob.Disk) {
	t.Helper()

	blobs := blob.NewDisk(t.TempDir(), testMaxBytes)
	return entries.NewService(store, blobs, entries.Options{
		InlineMaxBytes: testInlineMax,
		MaxBytes:       testMaxBytes,
	}), blobs
}

func TestServicePutChoosesStorageBySize(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	gid := newGroupID(t)

	tests := []struct {
		name       string
		size       int
		wantInline bool
	}{
		{name: "small entry stays in redis", size: 16, wantInline: true},
		{name: "entry exactly at the inline limit stays in redis", size: testInlineMax, wantInline: true},
		{name: "one byte over goes to the blob backend", size: testInlineMax + 1, wantInline: false},
		{name: "large entry goes to the blob backend", size: testMaxBytes, wantInline: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, _ := newService(t, entries.NewMemoryStore())
			body := strings.Repeat("c", tt.size)

			m, err := svc.Put(ctx, gid, 3, int64(tt.size), strings.NewReader(body))
			if err != nil {
				t.Fatalf("Put() error = %v", err)
			}
			if m.Inline() != tt.wantInline {
				t.Errorf("Put().Inline() = %v, want %v", m.Inline(), tt.wantInline)
			}
			if m.Size != int64(tt.size) || m.Epoch != 3 {
				t.Errorf("Put() = {Size:%d Epoch:%d}, want {Size:%d Epoch:3}", m.Size, m.Epoch, tt.size)
			}
			if !m.ExpiresAt.Equal(m.CreatedAt.Add(24 * time.Hour)) {
				t.Errorf("Put() expiry = %v, want created_at + 24h exactly", m.ExpiresAt)
			}

			got, ciphertext, err := svc.Fetch(ctx, gid, m.ID)
			if err != nil {
				t.Fatalf("Fetch() error = %v", err)
			}
			if got.ID != m.ID {
				t.Errorf("Fetch().ID = %q, want %q", got.ID, m.ID)
			}
			if !bytes.Equal(ciphertext, []byte(body)) {
				t.Errorf("Fetch() returned %d bytes, want the %d written", len(ciphertext), len(body))
			}
		})
	}
}

func TestServicePutRejectsOversizeBeforeReading(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _ := newService(t, entries.NewMemoryStore())

	body := &countingReader{src: strings.NewReader(strings.Repeat("c", testMaxBytes*4))}
	_, err := svc.Put(ctx, newGroupID(t), 1, testMaxBytes*4, body)
	if !errors.Is(err, entries.ErrTooLarge) {
		t.Fatalf("Put(declared oversize) error = %v, want %v", err, entries.ErrTooLarge)
	}
	if body.read != 0 {
		t.Errorf("Put(declared oversize) read %d bytes, want 0: the declared size is checked before the body is touched", body.read)
	}

	// A body that lies about its size is caught at the reader instead.
	liar := &countingReader{src: strings.NewReader(strings.Repeat("c", testMaxBytes*4))}
	if _, err := svc.Put(ctx, newGroupID(t), 1, testMaxBytes, liar); !errors.Is(err, entries.ErrTooLarge) {
		t.Errorf("Put(undeclared oversize) error = %v, want %v", err, entries.ErrTooLarge)
	}
	if liar.read > testMaxBytes+1 {
		t.Errorf("Put(undeclared oversize) read %d bytes, want at most %d", liar.read, testMaxBytes+1)
	}
}

func TestServicePutRejectsSizeMismatch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _ := newService(t, entries.NewMemoryStore())

	if _, err := svc.Put(ctx, newGroupID(t), 1, 100, strings.NewReader("short")); !errors.Is(err, entries.ErrSizeMismatch) {
		t.Errorf("Put(short body) error = %v, want %v", err, entries.ErrSizeMismatch)
	}
}

// failingStore is a Store whose write always fails, which is how this test
// observes the write ordering without racing anything.
type failingStore struct{ entries.Store }

var errStoreDown = errors.New("store is down")

func (failingStore) Put(context.Context, entries.Meta, []byte) error { return errStoreDown }

// TestServicePutWritesBlobBeforeRedis pins SPEC §4.5's write ordering: if the
// Redis write fails, the blob is already there. The reverse ordering would
// leave a visible entry whose ciphertext never arrived; this way the leftover
// is an orphan in a bucket that expires within 24 hours, which needs no
// recovery logic at all.
func TestServicePutWritesBlobBeforeRedis(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	blobs := blob.NewDisk(root, testMaxBytes)
	svc := entries.NewService(failingStore{entries.NewMemoryStore()}, blobs, entries.Options{
		InlineMaxBytes: testInlineMax,
		MaxBytes:       testMaxBytes,
	})

	size := testInlineMax * 2
	if _, err := svc.Put(ctx, newGroupID(t), 1, int64(size), strings.NewReader(strings.Repeat("c", size))); !errors.Is(err, errStoreDown) {
		t.Fatalf("Put() error = %v, want %v", err, errStoreDown)
	}

	refs, err := blobs.ListRefs(ctx)
	if err != nil {
		t.Fatalf("ListRefs() error = %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("blob backend holds %d blobs after a failed entry write, want 1: the blob is written first", len(refs))
	}
}

func TestServiceLatestAndHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _ := newService(t, entries.NewMemoryStore())
	gid := newGroupID(t)

	if _, _, err := svc.Latest(ctx, gid); !errors.Is(err, entries.ErrNotFound) {
		t.Errorf("Latest(empty group) error = %v, want %v", err, entries.ErrNotFound)
	}

	var last string
	for _, body := range []string{"one", "two", "three"} {
		m, err := svc.Put(ctx, gid, 1, int64(len(body)), strings.NewReader(body))
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		last = m.ID
	}

	m, body, err := svc.Latest(ctx, gid)
	if err != nil {
		t.Fatalf("Latest() error = %v", err)
	}
	if m.ID != last || string(body) != "three" {
		t.Errorf("Latest() = {ID:%q body:%q}, want {ID:%q body:%q}", m.ID, body, last, "three")
	}

	page, _, err := svc.History(ctx, gid, 10, time.Time{})
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(page) != 3 {
		t.Errorf("History() returned %d entries, want 3", len(page))
	}

	// An entry of another group is not reachable by id.
	if _, _, err := svc.Fetch(ctx, newGroupID(t), last); !errors.Is(err, entries.ErrNotFound) {
		t.Errorf("Fetch(other group) error = %v, want %v", err, entries.ErrNotFound)
	}
}

// countingReader reports how much of a body was consumed.
type countingReader struct {
	src  io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.read += int64(n)
	return n, err
}
