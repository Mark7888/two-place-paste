package blob_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/blob"
)

// recordingBackend reports every sweep on a channel, so the test observes the
// schedule through synchronisation rather than through sleeping
// (docs/conventions.md §5).
type recordingBackend struct{ swept chan time.Time }

func (b *recordingBackend) Put(context.Context, string, time.Time, io.Reader) (string, error) {
	return "", nil
}
func (b *recordingBackend) Get(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (b *recordingBackend) Delete(context.Context, string) error               { return nil }

func (b *recordingBackend) Sweep(_ context.Context, now time.Time) error {
	b.swept <- now
	return nil
}

func TestSweeperSweepsAtStartupAndOnInterval(t *testing.T) {
	t.Parallel()

	backend := &recordingBackend{swept: make(chan time.Time, 8)}
	sweeper := blob.NewSweeper(backend, time.Millisecond, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sweeper.Run(ctx)
	}()

	// The startup pass is what makes downtime harmless (SPEC §4.5), so it must
	// happen before any interval has elapsed; then the loop must keep going.
	for i := range 3 {
		select {
		case <-backend.swept:
		case <-time.After(5 * time.Second):
			t.Fatalf("sweep %d did not happen", i+1)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after its context was cancelled")
	}
}

func TestNewSweeperRejectsNonPositiveInterval(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("NewSweeper(interval=0) did not panic; a zero interval is a wiring bug that must surface at startup")
		}
	}()
	blob.NewSweeper(&recordingBackend{}, 0, nil)
}
