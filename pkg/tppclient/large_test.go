package tppclient_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// progressLog collects what OnProgress reported.
type progressLog struct {
	mu  sync.Mutex
	all []tppclient.Progress
}

func (l *progressLog) add(p tppclient.Progress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.all = append(l.all, p)
}

func (l *progressLog) last(d tppclient.Direction) (tppclient.Progress, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var last tppclient.Progress
	n := 0
	for _, p := range l.all {
		if p.Direction == d {
			last, n = p, n+1
		}
	}
	return last, n
}

func randomBody(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

// TestLargeEntries runs the chunked path against a real relay: an entry larger
// than one frame goes up in chunks and comes back in ranges, byte for byte,
// with progress on both sides; the storage limit evicts the oldest and the
// other device hears about it; and an entry over the relay's cap is refused
// before a byte is uploaded.
func TestLargeEntries(t *testing.T) {
	relay := startRelay(t, "TPP_BLOB_MAX_GROUP_BYTES=60MiB", "TPP_ENTRY_MAX_BYTES=30MiB")
	ctx := testContext(t)

	sent := &progressLog{}
	desktop, _ := newClient(t, "desktop", tppclient.Options{Handlers: tppclient.Handlers{OnProgress: sent.add}})
	if err := desktop.CreateGroup(ctx, relay.creationURL(t, "large")); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	received := &progressLog{}
	evicted := make(chan string, 4)
	phone, _ := newClient(t, "phone", tppclient.Options{Handlers: tppclient.Handlers{
		OnProgress:     received.add,
		OnEntryEvicted: func(id string) { evicted <- id },
	}})
	pair(ctx, t, desktop, phone)

	limits, err := desktop.Limits(ctx)
	if err != nil {
		t.Fatalf("Limits: %v", err)
	}
	if !limits.Chunked() || limits.MaxEntryBytes != 30<<20 || limits.SingleFrameMaxBytes != 10<<20 || limits.ChunkBytes != 8<<20 {
		t.Fatalf("Limits = %+v", limits)
	}

	body := randomBody(t, 25<<20)
	first, err := desktop.PutEntry(ctx, tppclient.Item{ContentType: "application/octet-stream", Filename: "big.bin", Body: body})
	if err != nil {
		t.Fatalf("PutEntry(25 MiB): %v", err)
	}
	if first.Size <= 25<<20 || first.Inline {
		t.Errorf("PutEntry meta = %+v", first)
	}
	if p, n := sent.last(tppclient.Upload); n < 4 || p.Done != p.Total || p.Total != first.Size || p.EntryID != first.ID {
		t.Errorf("upload progress: %d reports, last %+v; want at least begin and three chunks, ending complete", n, p)
	}

	got, err := phone.GetLatest(ctx)
	if err != nil {
		t.Fatalf("GetLatest: %v", err)
	}
	if !bytes.Equal(got.Body, body) || got.Filename != "big.bin" {
		t.Errorf("GetLatest returned %d bytes named %q, want the %d bytes sent as big.bin", len(got.Body), got.Filename, len(body))
	}
	if p, n := received.last(tppclient.Download); n < 4 || p.Done != p.Total || p.Total != first.Size {
		t.Errorf("download progress: %d reports, last %+v", n, p)
	}

	// Small entries still work, and are read through ranges just the same.
	small, err := desktop.PutEntry(ctx, tppclient.Item{ContentType: "text/plain; charset=utf-8", Body: []byte("hello")})
	if err != nil {
		t.Fatalf("PutEntry(small): %v", err)
	}
	if item, err := phone.GetEntry(ctx, small.ID); err != nil || string(item.Body) != "hello" {
		t.Errorf("GetEntry(small) = %q, %v", item.Body, err)
	}

	// The group limit is 60 MiB: a second and third 25 MiB entry evict the
	// first, and the phone is told.
	for range 2 {
		if _, err := desktop.PutEntry(ctx, tppclient.Item{ContentType: "application/octet-stream", Body: randomBody(t, 25<<20)}); err != nil {
			t.Fatalf("PutEntry(25 MiB): %v", err)
		}
	}
	select {
	case id := <-evicted:
		if id != first.ID {
			t.Errorf("evicted %q, want the oldest large entry %q", id, first.ID)
		}
	case <-ctx.Done():
		t.Fatal("the phone was never told about the eviction")
	}
	if _, err := phone.GetEntry(ctx, first.ID); !errors.Is(err, tppclient.ErrNotFound) {
		t.Errorf("GetEntry(evicted) = %v, want ErrNotFound", err)
	}

	// Over the relay's cap: refused here, before anything is sent.
	before, _ := sent.last(tppclient.Upload)
	_, err = desktop.PutEntry(ctx, tppclient.Item{ContentType: "application/octet-stream", Body: randomBody(t, 31<<20)})
	if !errors.Is(err, tppclient.ErrTooLarge) {
		t.Errorf("PutEntry(over the cap) = %v, want ErrTooLarge", err)
	}
	if after, _ := sent.last(tppclient.Upload); after != before {
		t.Errorf("an over-cap entry reported upload progress %+v; it must be refused before uploading", after)
	}
}
