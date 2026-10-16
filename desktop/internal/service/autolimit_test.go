package service

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

func int64Ptr(v int64) *int64 { return &v }

// nextEvent returns the next event of a kind, skipping refresh hints.
func nextEvent(t *testing.T, events <-chan localui.Event, kind string) localui.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("no %q event", kind)
			return localui.Event{}
		}
	}
}

func TestAutoLimitsDefaultAndValidate(t *testing.T) {
	t.Parallel()

	var s config.Settings
	if s.AutoUploadLimit() != config.DefaultAutoMaxBytes || s.AutoDownloadLimit() != config.DefaultAutoMaxBytes {
		t.Errorf("limits of an empty settings file = %d / %d, want the 25 MB default", s.AutoUploadLimit(), s.AutoDownloadLimit())
	}

	f := newFixture(t)
	ctx := context.Background()
	view, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{
		AutoUploadMaxBytes:   int64Ptr(5 << 20),
		AutoDownloadMaxBytes: int64Ptr(0),
	})
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if view.AutoUploadMaxBytes != 5<<20 || view.AutoDownloadMaxBytes != 0 {
		t.Errorf("view limits = %d / %d, want 5 MB / no limit", view.AutoUploadMaxBytes, view.AutoDownloadMaxBytes)
	}
	// Saved, and read back the same.
	saved, err := config.Load(f.svc.configDir)
	if err != nil || saved.AutoUploadLimit() != 5<<20 || saved.AutoDownloadLimit() != 0 {
		t.Errorf("saved limits = %d / %d, %v", saved.AutoUploadLimit(), saved.AutoDownloadLimit(), err)
	}

	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoUploadMaxBytes: int64Ptr(-1)}); err == nil {
		t.Errorf("a negative limit was accepted")
	}
}

// TestAutoUploadLimit: a clipboard change over the limit is not uploaded on
// its own, the screen says why, and Upload still sends it.
func TestAutoUploadLimit(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoUploadMaxBytes: int64Ptr(1 << 20)}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	events, stop := f.svc.Subscribe()
	defer stop()

	big := clipboard.Content{ContentType: "image/png", Body: bytes.Repeat([]byte("p"), (1<<20)+1)}
	f.svc.onClipboardChange(ctx, big, f.now())
	if n := f.relay.count(); n != 0 {
		t.Fatalf("an over-limit change was uploaded automatically (%d entries)", n)
	}
	ev := nextEvent(t, events, localui.EventSkipped)
	if ev.Direction != "upload" || ev.Total != (1<<20)+1 || !strings.Contains(ev.Message, "1 MB auto-upload limit") {
		t.Errorf("skipped event = %+v", ev)
	}

	// At the limit exactly, it goes.
	f.svc.onClipboardChange(ctx, clipboard.Content{ContentType: "image/png", Body: bytes.Repeat([]byte("p"), 1<<20)}, f.now())
	if n := f.relay.count(); n != 1 {
		t.Fatalf("a change at the limit was not uploaded (%d entries)", n)
	}

	// By hand, the limit does not apply.
	f.clip.set(big)
	if _, err := f.svc.Sync(ctx, localui.DirectionUpload); err != nil {
		t.Fatalf("Sync(upload) of an over-limit clipboard error = %v", err)
	}
	if n := f.relay.count(); n != 2 {
		t.Errorf("a manual upload over the limit was refused (%d entries)", n)
	}

	// No limit means no limit.
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoUploadMaxBytes: int64Ptr(0)}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	f.svc.onClipboardChange(ctx, big, f.now())
	if n := f.relay.count(); n != 3 {
		t.Errorf("with no limit, an automatic upload was skipped (%d entries)", n)
	}
}

// TestAutoDownloadLimit: an announced entry over the limit is not fetched on
// its own — decided from its metadata — and Download still gets it.
func TestAutoDownloadLimit(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{
		AutoApply:            boolPtr(true),
		AutoDownloadMaxBytes: int64Ptr(1 << 20),
	}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	events, stop := f.svc.Subscribe()
	defer stop()

	f.relay.push(tppclient.Item{ContentType: "video/mp4", Body: bytes.Repeat([]byte("v"), 3<<20), CreatedAt: f.now()})
	f.relay.mu.Lock()
	f.relay.getErr = nil
	meta := f.relay.entries[len(f.relay.entries)-1].Meta
	f.relay.mu.Unlock()

	f.svc.applyAnnounced(ctx, meta)
	f.clip.mu.Lock()
	writes := f.clip.writes
	f.clip.mu.Unlock()
	if writes != 0 {
		t.Fatalf("an over-limit entry was written to the clipboard automatically")
	}
	ev := nextEvent(t, events, localui.EventSkipped)
	if ev.Direction != "download" || ev.EntryID != meta.ID || ev.Total != 3<<20 || !strings.Contains(ev.Message, "3 MB entry") {
		t.Errorf("skipped event = %+v", ev)
	}

	// By hand, the limit does not apply.
	if _, err := f.svc.Sync(ctx, localui.DirectionDownload); err != nil {
		t.Fatalf("Sync(download) of an over-limit entry error = %v", err)
	}
	if got, _ := f.clip.Read(ctx); len(got.Body) != 3<<20 {
		t.Errorf("manual download wrote %d bytes, want the whole entry", len(got.Body))
	}
}
