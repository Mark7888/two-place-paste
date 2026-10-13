package update

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/config"
)

// clock is a settable wall clock.
type clock struct{ now atomic.Int64 }

func (c *clock) Now() time.Time          { return time.Unix(0, c.now.Load()).UTC() }
func (c *clock) advance(d time.Duration) { c.now.Add(int64(d)) }

func scheduleHarness(t *testing.T, idle *atomic.Bool) (*harness, *clock) {
	t.Helper()
	h := newHarness(t, stableBuild(100))
	c := &clock{}
	c.now.Store(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC).UnixNano())
	h.m.opts.Now = c.Now
	h.m.opts.Idle = idle.Load
	return h, c
}

func TestTheScheduleDoesNothingWithAutoUpdateOff(t *testing.T) {
	var idle atomic.Bool
	idle.Store(true)
	h, _ := scheduleHarness(t, &idle)
	h.gh.publish(t, h.key, "stable", 200, []byte("new build"))
	h.m.SetPreferences(config.Settings{AutoUpdate: false})

	h.m.scheduled(context.Background())
	if n := len(h.gh.requests); n != 0 {
		t.Fatalf("made %d requests with auto-update off", n)
	}
}

func TestTheScheduleChecksOnlyWhenDue(t *testing.T) {
	var idle atomic.Bool
	h, c := scheduleHarness(t, &idle)
	h.gh.publish(t, h.key, "stable", 100, []byte("same build"))
	h.m.SetPreferences(config.Settings{AutoUpdate: true})

	h.m.scheduled(context.Background())
	first := len(h.gh.requests)
	if first == 0 {
		t.Fatal("the first scheduled wake did not check")
	}
	c.advance(time.Hour)
	h.m.scheduled(context.Background())
	if len(h.gh.requests) != first {
		t.Fatal("checked again an hour later; a check stays fresh for six")
	}
	c.advance(6 * time.Hour)
	h.m.scheduled(context.Background())
	if len(h.gh.requests) == first {
		t.Fatal("did not check once the last check was stale")
	}
}

func TestAutoUpdateWaitsForIdle(t *testing.T) {
	var idle atomic.Bool
	h, _ := scheduleHarness(t, &idle)
	h.gh.publish(t, h.key, "stable", 200, []byte("new build"))
	h.m.SetPreferences(config.Settings{AutoUpdate: true})

	h.m.scheduled(context.Background())
	if len(h.applier.installed) != 0 {
		t.Fatal("installed while the machine was in use")
	}
	if !h.m.autoPending() {
		t.Fatal("the update is not pending after a successful check")
	}

	idle.Store(true)
	h.m.scheduled(context.Background())
	select {
	case <-h.quit:
	case <-time.After(5 * time.Second):
		t.Fatal("the service was not restarted into the update")
	}
	if len(h.applier.installed) != 1 {
		t.Fatalf("installed %d builds once idle, want 1", len(h.applier.installed))
	}
}

func TestAutoUpdateNeverInstallsAnOlderBuild(t *testing.T) {
	var idle atomic.Bool
	idle.Store(true)
	h, _ := scheduleHarness(t, &idle)
	h.m.opts.Current.Channel = "beta"
	h.m.opts.Current.Stamp = 300
	h.gh.publish(t, h.key, "stable", 200, []byte("older stable"))
	h.m.SetPreferences(config.Settings{AutoUpdate: true})

	h.m.scheduled(context.Background())
	if v := h.m.Status(context.Background()); v.Available == nil || !v.Available.Older {
		t.Fatalf("available = %+v; want the older build offered", v.Available)
	}
	if len(h.applier.installed) != 0 {
		t.Fatal("auto-update installed an older build")
	}
}

func TestNightlyIsNeverScheduled(t *testing.T) {
	var idle atomic.Bool
	idle.Store(true)
	h, _ := scheduleHarness(t, &idle)
	h.m.SetPreferences(config.Settings{AutoUpdate: true, UpdateChannel: "nightly"})
	h.m.scheduled(context.Background())
	if v := h.m.Status(context.Background()); v.LastChecked != nil {
		t.Fatal("the schedule checked the Nightly channel")
	}
}

func TestRunChecksAfterTheStartDelayAndOnWake(t *testing.T) {
	var idle atomic.Bool
	h, c := scheduleHarness(t, &idle)
	h.m.opts.StartDelay = 10 * time.Millisecond
	h.gh.publish(t, h.key, "stable", 100, []byte("same build"))
	h.m.SetPreferences(config.Settings{AutoUpdate: true})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { h.m.Run(ctx); close(done) }()

	waitFor(t, func() bool { return h.m.Status(ctx).LastChecked != nil })
	first := h.m.Status(ctx).LastChecked

	c.advance(7 * time.Hour)
	h.m.Woke()
	waitFor(t, func() bool { return h.m.Status(ctx).LastChecked.After(*first) })

	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
