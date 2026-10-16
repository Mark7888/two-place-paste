package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// Transfers overlap: a slow upload, a slow download, and the user and the
// other device both copying meanwhile. These tests pin what wins.

func autoApplyFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	if _, err := f.svc.UpdateSettings(context.Background(), localui.SettingsPatch{
		AutoApply: boolPtr(true),
		AutoWatch: boolPtr(true),
	}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	return f
}

// pushAt records an entry another device wrote, with the relay's timestamp.
func pushAt(f *fixture, body string, at time.Time) tppclient.EntryMeta {
	f.relay.push(tppclient.Item{ContentType: clipboard.TypeText, Body: []byte(body), CreatedAt: at})
	f.relay.mu.Lock()
	defer f.relay.mu.Unlock()
	last := &f.relay.entries[len(f.relay.entries)-1]
	last.Meta.ID = "pushed-" + body
	return last.Meta
}

// eventually waits for cond, which another goroutine makes true.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *fixture) downloading() bool {
	f.svc.mu.Lock()
	defer f.svc.mu.Unlock()
	return f.svc.fetching != nil
}

func (f *fixture) uploading() bool {
	f.svc.mu.Lock()
	defer f.svc.mu.Unlock()
	return f.svc.uploads > 0
}

func (f *fixture) clipText() string {
	c, _ := f.clip.Read(context.Background())
	return string(c.Body)
}

func (f *fixture) gate(get bool) chan struct{} {
	g := make(chan struct{})
	f.relay.mu.Lock()
	if get {
		f.relay.getGate = g
	} else {
		f.relay.putGate = g
	}
	f.relay.mu.Unlock()
	return g
}

// TestCopyDuringDownloadWins: the user copies while an announced entry is
// downloading. Whether the watcher has reported the copy yet or not, the
// download stands down and the copy stays on the clipboard.
func TestCopyDuringDownloadWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("reported copy cancels the download", func(t *testing.T) {
		t.Parallel()
		f := autoApplyFixture(t)
		meta := pushAt(f, "from the phone", f.now())
		gate := f.gate(true)
		defer close(gate)

		done := make(chan struct{})
		go func() { f.svc.applyAnnounced(ctx, meta); close(done) }()
		eventually(t, "the download to start", f.downloading)

		f.clip.set(textContent("copied here meanwhile"))
		f.svc.onClipboardChange(ctx, textContent("copied here meanwhile"), f.now())
		<-done

		if got := f.clipText(); got != "copied here meanwhile" {
			t.Errorf("clipboard = %q, want the user's copy", got)
		}
	})

	t.Run("copy the watcher has not seen yet", func(t *testing.T) {
		t.Parallel()
		f := autoApplyFixture(t)
		f.clip.set(textContent("before"))
		if _, _, err := f.svc.watcher.Poll(ctx); err != nil { // adopt the baseline
			t.Fatalf("Poll() error = %v", err)
		}
		meta := pushAt(f, "from the phone", f.now())
		gate := f.gate(true)

		done := make(chan struct{})
		go func() { f.svc.applyAnnounced(ctx, meta); close(done) }()
		eventually(t, "the download to start", f.downloading)
		f.clip.set(textContent("copied, not yet polled"))
		close(gate)
		<-done

		if got := f.clipText(); got != "copied, not yet polled" {
			t.Errorf("clipboard = %q, want the user's copy", got)
		}
		// The copy is still the watcher's to report and upload.
		if _, changed, _ := f.svc.watcher.Poll(ctx); !changed {
			t.Error("the next poll does not report the user's copy")
		}
	})
}

// TestNewerAnnouncementCancelsDownload: an entry still downloading when a
// newer one is announced is abandoned, and the newer one is applied.
func TestNewerAnnouncementCancelsDownload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := autoApplyFixture(t)

	first := pushAt(f, "first", f.now())
	gate := f.gate(true)
	done := make(chan struct{})
	go func() { f.svc.applyAnnounced(ctx, first); close(done) }()
	eventually(t, "the first download to start", f.downloading)

	second := pushAt(f, "second", f.now().Add(time.Second))
	f.svc.announce(second)
	<-done
	close(gate)
	if f.clip.writes != 0 {
		t.Fatalf("the superseded download was written to the clipboard")
	}

	queued := <-f.svc.incoming
	if queued.ID != second.ID {
		t.Fatalf("queued %q, want the newer entry", queued.ID)
	}
	f.svc.applyAnnounced(ctx, queued)
	if got := f.clipText(); got != "second" || f.clip.writes != 1 {
		t.Errorf("clipboard = %q after %d writes, want only the newer entry", got, f.clip.writes)
	}
}

// TestAnnouncementDuringUploadIsHeld: while this device's own upload is in
// flight, an entry another device wrote is not applied — the upload will be
// the group's latest. If the upload fails, the entry is applied after all.
func TestAnnouncementDuringUploadIsHeld(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, fail := range []bool{false, true} {
		name := "upload succeeds: the held entry is dropped"
		if fail {
			name = "upload fails: the held entry is applied"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := autoApplyFixture(t)
			gate := f.gate(false)
			if fail {
				f.relay.mu.Lock()
				f.relay.putErr = errors.New("relay went away")
				f.relay.mu.Unlock()
			}

			// The local copy reaches the relay after the other device's.
			mine := textContent("copied here, uploading")
			f.clip.set(mine)
			done := make(chan struct{})
			go func() { f.svc.onClipboardChange(ctx, mine, f.now().Add(2*time.Second)); close(done) }()
			eventually(t, "the upload to start", f.uploading)

			other := pushAt(f, "from the phone", f.now().Add(time.Second))
			f.svc.applyAnnounced(ctx, other)
			if f.clip.writes != 0 {
				t.Fatalf("an entry was applied while this device's upload was in flight")
			}
			close(gate)
			<-done

			select {
			case again := <-f.svc.incoming:
				if !fail {
					t.Errorf("held entry %q re-queued after an upload that will win", again.ID)
				} else if again.ID != other.ID {
					t.Errorf("re-queued %q, want the held entry", again.ID)
				}
			default:
				if fail {
					t.Error("the held entry was dropped although the upload failed")
				}
			}
		})
	}
}

// TestFailedAutoUploadIsShown: an automatic upload that fails says so on the
// screen, as a skipped transfer does, instead of only in the log.
func TestFailedAutoUploadIsShown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	f.relay.mu.Lock()
	f.relay.putErr = tppclient.ErrQuotaExceeded
	f.relay.mu.Unlock()

	f.svc.onClipboardChange(ctx, textContent("hello"), f.now())
	s := f.svc.Status(ctx).Skipped
	if s == nil || s.Direction != "upload" || !strings.Contains(s.Message, "Automatic upload of 1 KB failed") ||
		!strings.Contains(s.Message, "storage is full") {
		t.Errorf("Status().Skipped = %+v, want the failure", s)
	}
}
