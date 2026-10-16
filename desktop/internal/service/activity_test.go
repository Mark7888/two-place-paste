package service

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/clipboard"
	"github.com/Mark7888/two-place-paste/desktop/internal/config"
	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// activityLog records every Activity the service hands over.
type activityLog struct {
	mu  sync.Mutex
	got []Activity
}

func (l *activityLog) record(a Activity) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, a)
}

func (l *activityLog) all() []Activity {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Activity(nil), l.got...)
}

func newActivityFixture(t *testing.T) (*Service, *fakeRelay, *memClipboard, *activityLog) {
	t.Helper()
	now := func() time.Time { return time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC) }
	clip := newMemClipboard()
	log := &activityLog{}
	svc, err := New(Options{
		Clipboard:  clip,
		Autostart:  noAutostart{},
		Settings:   config.Settings{},
		ConfigDir:  t.TempDir(),
		ListenPort: config.DefaultPort,
		Now:        now,
		OnActivity: log.record,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	relay := newFakeRelay(now)
	return svc, relay, clip, log
}

func TestActivityFollowsTheConnection(t *testing.T) {
	t.Parallel()

	svc, relay, _, log := newActivityFixture(t)
	svc.Attach(relay)
	svc.ClientHandlers().OnConnected()
	svc.catchUps.Wait()
	svc.ClientHandlers().OnDisconnected(errors.New("network down"))

	want := []Activity{
		{InGroup: true},
		{InGroup: true, Connected: true, EverConnected: true},
		{InGroup: true, EverConnected: true},
	}
	got := log.all()
	if len(got) != len(want) {
		t.Fatalf("OnActivity calls = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("OnActivity call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestActivityNamesTheTransferDirection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  localui.Direction
		busy func(Activity) bool
	}{
		{name: "upload", dir: localui.DirectionUpload, busy: func(a Activity) bool { return a.Uploading && !a.Downloading }},
		{name: "download", dir: localui.DirectionDownload, busy: func(a Activity) bool { return a.Downloading && !a.Uploading }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc, relay, clip, log := newActivityFixture(t)
			svc.Attach(relay)
			svc.ClientHandlers().OnConnected()
			svc.catchUps.Wait()
			clip.set(textContent("to upload"))
			relay.push(tppclient.Item{ContentType: "text/plain", Body: []byte("from the phone")})

			if _, err := svc.Sync(context.Background(), tt.dir); err != nil {
				t.Fatalf("Sync(%s) error = %v", tt.dir, err)
			}

			got := log.all()
			sawBusy := false
			for _, a := range got {
				sawBusy = sawBusy || tt.busy(a)
			}
			if !sawBusy {
				t.Errorf("OnActivity calls = %+v, want one marking the %s alone", got, tt.name)
			}
			if last := got[len(got)-1]; last.Uploading || last.Downloading {
				t.Errorf("OnActivity after the sync = %+v, want idle", last)
			}
		})
	}
}

func TestActivityShowsASkippedTransferUntilASyncClearsIt(t *testing.T) {
	t.Parallel()

	svc, relay, clip, _ := newActivityFixture(t)
	svc.Attach(relay)
	svc.ClientHandlers().OnConnected()
	svc.catchUps.Wait()
	ctx := context.Background()
	if _, err := svc.UpdateSettings(ctx, localui.SettingsPatch{AutoUploadMaxBytes: int64Ptr(1 << 20)}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}

	big := clipboard.Content{ContentType: "image/png", Body: bytes.Repeat([]byte("p"), (1<<20)+1)}
	svc.onClipboardChange(ctx, big, svc.now())
	if got := svc.Activity().Skipped; got != "upload" {
		t.Fatalf("Activity().Skipped after an over-limit copy = %q, want %q", got, "upload")
	}

	clip.set(big)
	if _, err := svc.Sync(ctx, localui.DirectionUpload); err != nil {
		t.Fatalf("Sync(upload) error = %v", err)
	}
	if got := svc.Activity().Skipped; got != "" {
		t.Errorf("Activity().Skipped after uploading by hand = %q, want none", got)
	}
}

func TestActivityAfterLeavingTheGroup(t *testing.T) {
	t.Parallel()

	svc, relay, _, _ := newActivityFixture(t)
	svc.Attach(relay)
	svc.ClientHandlers().OnConnected()
	svc.catchUps.Wait()

	if err := svc.Forget(context.Background()); err != nil {
		t.Fatalf("Forget() error = %v", err)
	}
	if got, want := svc.Activity(), (Activity{}); got != want {
		t.Errorf("Activity() after Forget = %+v, want %+v", got, want)
	}
}

func TestAutoSyncNamesTheUnknownDirection(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.clip.set(textContent("copied before the service started"))
	f.relay.push(tppclient.Item{ContentType: "text/plain", Body: []byte("from the phone"), CreatedAt: f.now()})

	_, err := f.svc.Sync(context.Background(), localui.DirectionAuto)
	if !errors.Is(err, ErrDirectionUnknown) {
		t.Errorf("Sync(auto) error = %v, want ErrDirectionUnknown", err)
	}
}
