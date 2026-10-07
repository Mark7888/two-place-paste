package clipboard

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClipboard stands in for an OS clipboard. It records writes so a test can
// assert what the service put there, and can report a change counter or refuse
// to, which is the difference between the Windows and macOS implementations.
type fakeClipboard struct {
	mu       sync.Mutex
	content  Content
	empty    bool
	seq      uint64
	hasSeq   bool
	readErr  error
	writeLog []Content
}

func (f *fakeClipboard) Available() bool { return true }

func (f *fakeClipboard) Read(context.Context) (Content, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.readErr != nil:
		return Content{}, f.readErr
	case f.empty:
		return Content{}, ErrEmpty
	}
	return f.content, nil
}

func (f *fakeClipboard) Write(_ context.Context, c Content) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.content, f.empty = c, false
	f.seq++
	f.writeLog = append(f.writeLog, c)
	return nil
}

func (f *fakeClipboard) Sequence(context.Context) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasSeq {
		return 0, ErrNoSequence
	}
	return f.seq, nil
}

func (f *fakeClipboard) set(c Content) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.content, f.empty = c, false
	f.seq++
}

func text(s string) Content { return Content{ContentType: TypeText, Body: []byte(s)} }

// TestWatcherServiceWriteDoesNotUpload is the loop test ROADMAP P6 asks for: a
// clipboard write made by the service must not come back as a change to
// upload. Without suppression this is an infinite loop that writes one entry
// per poll.
func TestWatcherServiceWriteDoesNotUpload(t *testing.T) {
	t.Parallel()

	for _, withSeq := range []bool{false, true} {
		name := "content comparison"
		if withSeq {
			name = "change counter"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fake := &fakeClipboard{hasSeq: withSeq}
			fake.set(text("what the user copied"))

			var uploads []Content
			w := NewWatcher(WatchOptions{
				Clipboard: fake,
				OnChange: func(_ context.Context, c Content, _ time.Time) {
					uploads = append(uploads, c)
				},
			})
			w.SetEnabled(true)

			// First poll adopts the baseline.
			if _, changed, err := w.Poll(ctx); err != nil || changed {
				t.Fatalf("first Poll() = changed %v, err %v; want false, nil", changed, err)
			}

			// The service downloads an entry and writes it to the clipboard.
			downloaded := text("what the other device sent")
			w.Suppress(downloaded)
			if err := fake.Write(ctx, downloaded); err != nil {
				t.Fatalf("Write() error = %v", err)
			}

			// Ten polls later there must still be nothing to upload.
			for range 10 {
				c, changed, err := w.Poll(ctx)
				if err != nil {
					t.Fatalf("Poll() error = %v", err)
				}
				if changed {
					t.Fatalf("Poll() reported the service's own write as a user change: %q", c.Text())
				}
				if changed && w.onChange != nil {
					w.onChange(ctx, c, time.Now())
				}
			}
			if len(uploads) != 0 {
				t.Fatalf("OnChange fired %d times for a service write, want 0", len(uploads))
			}

			// A real user copy after that is still reported.
			fake.set(text("something the user copied next"))
			c, changed, err := w.Poll(ctx)
			if err != nil || !changed {
				t.Fatalf("Poll() after a user copy = changed %v, err %v; want true, nil", changed, err)
			}
			if got := c.Text(); got != "something the user copied next" {
				t.Errorf("Poll() content = %q, want the user's copy", got)
			}
		})
	}
}

func TestWatcherPoll(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		setup       func(*fakeClipboard, *Watcher)
		wantChanged bool
		wantErr     error
	}{
		{
			name:  "an unchanged clipboard is not a change",
			setup: func(f *fakeClipboard, w *Watcher) { f.set(text("same")); mustPrime(t, w) },
		},
		{
			name: "a new payload is a change",
			setup: func(f *fakeClipboard, w *Watcher) {
				f.set(text("first"))
				mustPrime(t, w)
				f.set(text("second"))
			},
			wantChanged: true,
		},
		{
			name: "the same bytes as a different kind is a change",
			setup: func(f *fakeClipboard, w *Watcher) {
				f.set(text("bytes"))
				mustPrime(t, w)
				f.set(Content{ContentType: TypeOctetStream, Filename: "bytes.txt", Body: []byte("bytes")})
			},
			wantChanged: true,
		},
		{
			name: "an empty clipboard is a state, not a change",
			setup: func(f *fakeClipboard, w *Watcher) {
				f.set(text("first"))
				mustPrime(t, w)
				f.mu.Lock()
				f.empty = true
				f.mu.Unlock()
			},
		},
		{
			name: "a read failure is reported",
			setup: func(f *fakeClipboard, _ *Watcher) {
				f.readErr = errors.New("the clipboard is held by another application")
			},
			wantErr: errRead,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeClipboard{}
			w := NewWatcher(WatchOptions{
				Clipboard: fake,
				Now:       func() time.Time { return at },
			})
			tt.setup(fake, w)

			_, changed, err := w.Poll(context.Background())
			switch {
			case tt.wantErr != nil && err == nil:
				t.Fatalf("Poll() error = nil, want a failure")
			case tt.wantErr == nil && err != nil:
				t.Fatalf("Poll() error = %v, want nil", err)
			}
			if changed != tt.wantChanged {
				t.Errorf("Poll() changed = %v, want %v", changed, tt.wantChanged)
			}
			if tt.wantChanged && !w.LastChange().Equal(at) {
				t.Errorf("LastChange() = %v, want %v", w.LastChange(), at)
			}
			if !tt.wantChanged && tt.wantErr == nil && !w.LastChange().IsZero() {
				t.Errorf("LastChange() = %v, want the zero time", w.LastChange())
			}
		})
	}
}

// errRead is a marker: the table only distinguishes "an error" from "no error".
var errRead = errors.New("read")

func mustPrime(t *testing.T, w *Watcher) {
	t.Helper()
	if _, changed, err := w.Poll(context.Background()); err != nil || changed {
		t.Fatalf("priming Poll() = changed %v, err %v; want false, nil", changed, err)
	}
}

func TestWatcherDisabledDoesNotPoll(t *testing.T) {
	t.Parallel()

	fake := &fakeClipboard{}
	fake.set(text("copied while the watcher was off"))

	fired := make(chan Content, 1)
	w := NewWatcher(WatchOptions{
		Clipboard: fake,
		Interval:  time.Millisecond,
		OnChange:  func(_ context.Context, c Content, _ time.Time) { fired <- c },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()

	select {
	case c := <-fired:
		t.Fatalf("a disabled watcher reported a change: %q", c.Text())
	case <-time.After(50 * time.Millisecond):
	}

	// Enabling adopts the current clipboard rather than reporting it: the user
	// copied that before asking to be watched.
	w.SetEnabled(true)
	select {
	case c := <-fired:
		t.Fatalf("enabling the watcher reported the existing clipboard as new: %q", c.Text())
	case <-time.After(50 * time.Millisecond):
	}

	fake.set(text("copied with the watcher on"))
	select {
	case c := <-fired:
		if got := c.Text(); got != "copied with the watcher on" {
			t.Errorf("OnChange content = %q, want the new copy", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an enabled watcher did not report a user copy")
	}

	cancel()
	<-done
}

func TestSafeName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "a bare name is kept", in: "notes.txt", want: "notes.txt"},
		{name: "a posix path is reduced", in: "/etc/passwd", want: "passwd"},
		{name: "a windows path is reduced", in: `C:\Windows\System32\cmd.exe`, want: "cmd.exe"},
		{name: "traversal is refused", in: "../../../etc/shadow", want: "shadow"},
		{name: "a bare dot-dot becomes a placeholder", in: "..", want: "clipboard"},
		{name: "an empty name becomes a placeholder", in: "   ", want: "clipboard"},
		{name: "reserved characters are replaced", in: `a:b*c?.txt`, want: "a_b_c_.txt"},
		{name: "control characters are dropped", in: "no\x00thing\n.txt", want: "nothing.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SafeName(tt.in); got != tt.want {
				t.Errorf("SafeName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSafeNameIsBounded(t *testing.T) {
	t.Parallel()

	got := SafeName(strings.Repeat("a", 400) + ".txt")
	if len(got) > 120 {
		t.Errorf("SafeName() returned %d bytes, want at most 120", len(got))
	}
	if !strings.HasSuffix(got, ".txt") {
		t.Errorf("SafeName() = %q, want the extension kept", got)
	}
}

// TestWatcherSequenceGuardsTheRead is what makes an idle macOS watcher cheap:
// with a change counter, an unchanged counter means the content is not read at
// all. And a read that fails must not use the counter up, or the copy it
// failed to read would never be reported.
func TestWatcherSequenceGuardsTheRead(t *testing.T) {
	t.Parallel()

	fake := &fakeClipboard{hasSeq: true}
	fake.set(text("before"))
	w := NewWatcher(WatchOptions{Clipboard: fake})
	mustPrime(t, w)

	// A read now would fail; an unchanged counter must mean there is none.
	fake.mu.Lock()
	fake.readErr = errors.New("a read happened")
	fake.mu.Unlock()
	if _, changed, err := w.Poll(context.Background()); err != nil || changed {
		t.Fatalf("Poll() with an unchanged counter = changed %v, err %v; want no read at all", changed, err)
	}

	// The user copies, and the first read of it fails.
	fake.set(text("after"))
	if _, _, err := w.Poll(context.Background()); err == nil {
		t.Fatal("Poll() = nil error, want the read failure")
	}

	// The next poll must try again, though the counter has not moved since.
	fake.mu.Lock()
	fake.readErr = nil
	fake.mu.Unlock()
	c, changed, err := w.Poll(context.Background())
	if err != nil || !changed || c.Text() != "after" {
		t.Fatalf("Poll() after a failed read = %q, changed %v, err %v; want the copy reported", c.Text(), changed, err)
	}
}

// TestWatcherPausedDoesNotRead covers the locked screen: nothing is read while
// paused, and a copy made before the pause ends is reported once it does.
func TestWatcherPausedDoesNotRead(t *testing.T) {
	t.Parallel()

	fake := &fakeClipboard{hasSeq: true}
	fake.set(text("before the lock"))

	var (
		mu     sync.Mutex
		paused bool
		asked  int
	)
	fired := make(chan Content, 1)
	w := NewWatcher(WatchOptions{
		Clipboard:      fake,
		Interval:       time.Millisecond,
		PausedInterval: time.Millisecond,
		Paused: func() bool {
			mu.Lock()
			defer mu.Unlock()
			asked++
			return paused
		},
		OnChange: func(_ context.Context, c Content, _ time.Time) { fired <- c },
	})
	// Enabling discards the baseline; priming by hand before Run starts is
	// what makes the next copy a change rather than the new baseline.
	w.SetEnabled(true)
	mustPrime(t, w)
	mu.Lock()
	paused = true
	mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()

	// A read while paused would see this change and fire.
	fake.set(text("changed while locked"))
	select {
	case c := <-fired:
		t.Fatalf("a paused watcher reported %q", c.Text())
	case <-time.After(50 * time.Millisecond):
	}
	mu.Lock()
	if asked == 0 {
		t.Error("a paused watcher stopped asking whether it is still paused")
	}
	paused = false
	mu.Unlock()

	select {
	case c := <-fired:
		if got := c.Text(); got != "changed while locked" {
			t.Errorf("OnChange after the pause = %q, want the change made during it", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the watcher did not resume after the pause ended")
	}

	cancel()
	<-done
}
