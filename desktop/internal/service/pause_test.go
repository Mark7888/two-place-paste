package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// TestPauseHoldsAutoUpload: a copy made while paused is not uploaded, not
// then and not after the pause; manual Upload still works; and the first copy
// after resuming goes as usual.
func TestPauseHoldsAutoUpload(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoWatch: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	events, stop := f.svc.Subscribe()
	defer stop()

	view := f.svc.SetPaused(ctx, true)
	if !view.Paused || view.Until == nil || !view.Until.Equal(f.now().Add(PauseDuration)) || !view.Available {
		t.Fatalf("SetPaused(true) = %+v, want paused for %v", view, PauseDuration)
	}
	nextEvent(t, events, localui.EventPause)
	if a := f.svc.Activity(); !a.Paused || !a.AutoSync {
		t.Errorf("Activity = %+v, want paused with auto-sync on", a)
	}

	f.svc.onClipboardChange(ctx, textContent("copied while paused"), f.now())
	if n := f.relay.count(); n != 0 {
		t.Fatalf("a copy made while paused was uploaded (%d entries)", n)
	}

	f.clip.set(textContent("by hand"))
	if _, err := f.svc.Sync(ctx, localui.SyncRequest{Direction: localui.DirectionUpload}); err != nil {
		t.Fatalf("manual upload while paused: %v", err)
	}
	if n := f.relay.count(); n != 1 {
		t.Fatalf("relay holds %d entries after a manual upload, want 1", n)
	}

	if view := f.svc.SetPaused(ctx, false); view.Paused || view.Until != nil {
		t.Fatalf("SetPaused(false) = %+v, want resumed", view)
	}
	if n := f.relay.count(); n != 1 {
		t.Errorf("resuming uploaded the copy made while paused (%d entries)", n)
	}
	f.svc.onClipboardChange(ctx, textContent("after"), f.now())
	if n := f.relay.count(); n != 2 {
		t.Errorf("a copy after resuming was not uploaded (%d entries)", n)
	}
}

// TestPauseHoldsAutoApply: an entry announced while paused is not put on the
// clipboard, and the skipped notice says why.
func TestPauseHoldsAutoApply(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoApply: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	f.svc.SetPaused(ctx, true)
	events, stop := f.svc.Subscribe()
	defer stop()

	f.relay.push(tppclient.Item{ContentType: "text/plain; charset=utf-8", Body: []byte("from another device"), CreatedAt: f.now()})
	f.relay.mu.Lock()
	meta := f.relay.entries[len(f.relay.entries)-1].Meta
	f.relay.mu.Unlock()
	f.svc.applyAnnounced(ctx, meta)

	f.clip.mu.Lock()
	writes := f.clip.writes
	f.clip.mu.Unlock()
	if writes != 0 {
		t.Fatal("an entry announced while paused was written to the clipboard")
	}
	ev := nextEvent(t, events, localui.EventSkipped)
	if ev.Direction != "download" || ev.EntryID != meta.ID || !strings.Contains(ev.Message, "paused") {
		t.Errorf("skipped event = %+v, want a download held by the pause", ev)
	}
}

// TestPauseEndsOnItsOwn: the pause is read against the clock, so it is over
// after PauseDuration whether or not its timer has fired, and pauseEnded
// clears it and tells the screen.
func TestPauseEndsOnItsOwn(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	f.svc.SetPaused(ctx, true)
	events, stop := f.svc.Subscribe()
	defer stop()

	// Not due yet: nothing happens.
	f.svc.pauseEnded()
	if on, _ := f.svc.paused(); !on {
		t.Fatal("pauseEnded ended a pause that was not due")
	}

	later := f.now().Add(PauseDuration)
	f.svc.mu.Lock()
	f.svc.now = func() time.Time { return later }
	f.svc.mu.Unlock()
	if on, _ := f.svc.paused(); on {
		t.Error("the pause is still on after its five minutes")
	}
	f.svc.pauseEnded()
	nextEvent(t, events, localui.EventPause)
	if st := f.svc.Status(ctx); st.Pause.Paused || st.Pause.Until != nil {
		t.Errorf("Status.Pause = %+v after it ended, want not paused", st.Pause)
	}
}

func TestTogglePauseAndAvailability(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	// With neither automatic direction on there is nothing to pause, and
	// the screen says so; pausing still works, and is harmless.
	if st := f.svc.Status(ctx); st.Pause.Available {
		t.Errorf("Pause.Available = true with auto-sync off")
	}
	if v := f.svc.TogglePause(ctx); !v.Paused {
		t.Errorf("TogglePause from running = %+v, want paused", v)
	}
	if v := f.svc.TogglePause(ctx); v.Paused {
		t.Errorf("TogglePause from paused = %+v, want resumed", v)
	}
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{AutoApply: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if a := f.svc.Activity(); !a.AutoSync {
		t.Errorf("Activity.AutoSync = false with auto-apply on")
	}
}
