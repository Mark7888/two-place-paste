package service

import (
	"context"
	"testing"

	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

func TestNearlyFull(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		l    tppclient.StoreLimits
		want bool
	}{
		{"off", tppclient.StoreLimits{MaxBytes: 100, UsedBytes: 100}, false},
		{"empty", tppclient.StoreLimits{Enabled: true, MaxBytes: 100, MaxItems: 10}, false},
		{"89% of bytes", tppclient.StoreLimits{Enabled: true, MaxBytes: 100, UsedBytes: 89, MaxItems: 10}, false},
		{"90% of bytes", tppclient.StoreLimits{Enabled: true, MaxBytes: 100, UsedBytes: 90, MaxItems: 10}, true},
		{"90% of items", tppclient.StoreLimits{Enabled: true, MaxBytes: 100, MaxItems: 10, UsedItems: 9}, true},
		{"no limits at all", tppclient.StoreLimits{Enabled: true, UsedBytes: 1 << 30, UsedItems: 5}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := nearlyFull(tt.l); got != tt.want {
				t.Errorf("nearlyFull(%+v) = %v, want %v", tt.l, got, tt.want)
			}
		})
	}
}

// TestStoreFullCause tells the group's own store being full from the relay's
// total store limit, from the limits the relay reports after a refusal.
func TestStoreFullCause(t *testing.T) {
	t.Parallel()
	const mib = 1 << 20
	rec := int64(storeRecordBytes)
	for _, tt := range []struct {
		name string
		l    tppclient.StoreLimits
		want string
	}{
		{
			"out of items",
			tppclient.StoreLimits{Enabled: true, MaxBytes: 100 * mib, UsedBytes: mib, AvailableBytes: 0, MaxItems: 10, UsedItems: 10, MaxItemBytes: 50 * mib},
			localui.SpaceStoreFull,
		},
		{
			"out of the group's bytes",
			tppclient.StoreLimits{Enabled: true, MaxBytes: 100 * mib, UsedBytes: 99 * mib, AvailableBytes: mib - rec, MaxItems: 10, UsedItems: 3, MaxItemBytes: 50 * mib},
			localui.SpaceStoreFull,
		},
		{
			"the group has room, the relay's total does not",
			tppclient.StoreLimits{Enabled: true, MaxBytes: 100 * mib, UsedBytes: 10 * mib, AvailableBytes: mib, MaxItems: 10, UsedItems: 3, MaxItemBytes: 50 * mib},
			localui.SpaceRelayStoreFull,
		},
		{
			"no group limit",
			tppclient.StoreLimits{Enabled: true, UsedBytes: 10 * mib, AvailableBytes: 0, MaxItems: 10, UsedItems: 3, MaxItemBytes: 50 * mib},
			localui.SpaceRelayStoreFull,
		},
		{
			"plenty of room by the numbers: the group's",
			tppclient.StoreLimits{Enabled: true, MaxBytes: 100 * mib, UsedBytes: 10 * mib, AvailableBytes: 50 * mib, MaxItems: 10, UsedItems: 3, MaxItemBytes: 50 * mib},
			localui.SpaceStoreFull,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := storeFullCause(tt.l); got != tt.want {
				t.Errorf("storeFullCause() = %q, want %q", got, tt.want)
			}
		})
	}
}

func (f *fixture) setLimits(l tppclient.StoreLimits) {
	f.relay.mu.Lock()
	f.relay.limits = &l
	f.relay.mu.Unlock()
	f.svc.refreshSpace(context.Background())
}

// TestSpaceBanners: the nearly-full banner and the eviction banner show,
// close with X until next triggered, and hide for good with the setting.
func TestSpaceBanners(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	space := func() localui.SpaceView { return f.svc.Status(ctx).Space }

	full := tppclient.StoreLimits{Enabled: true, MaxBytes: 100, UsedBytes: 95, MaxItems: 10, UsedItems: 4, MaxItemBytes: 50}
	f.setLimits(full)
	if got := space(); !got.StoreNearlyFull || got.StoreUsedBytes != 95 || got.StoreMaxBytes != 100 {
		t.Fatalf("Space = %+v, want the store nearly full at 95 of 100", got)
	}
	if err := f.svc.DismissSpace(ctx, localui.SpaceBannerStore); err != nil {
		t.Fatalf("DismissSpace(store) error = %v", err)
	}
	if space().StoreNearlyFull {
		t.Error("the store banner is back right after closing it")
	}
	f.setLimits(full)
	if space().StoreNearlyFull {
		t.Error("the store banner is back without a change to the store")
	}
	// A save that leaves the store still that full brings it back.
	full.UsedBytes, full.UsedItems = 97, 5
	f.setLimits(full)
	if !space().StoreNearlyFull {
		t.Error("the store banner did not come back after the store changed")
	}

	evicted := f.svc.ClientHandlers().OnEntryEvicted
	for _, id := range []string{"e1", "e2", "e3"} {
		evicted(id)
	}
	if got := space().Evicted; got != 3 {
		t.Errorf("Space.Evicted = %d, want 3", got)
	}
	if err := f.svc.DismissSpace(ctx, localui.SpaceBannerEvicted); err != nil {
		t.Fatalf("DismissSpace(evicted) error = %v", err)
	}
	if got := space().Evicted; got != 0 {
		t.Errorf("Space.Evicted after closing = %d, want 0", got)
	}
	evicted("e4")

	// Hiding the banners hides both, and nothing else.
	f.svc.noteSpaceFailure(localui.SpaceRelayFull)
	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{HideSpaceBanners: boolPtr(true)}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	got := space()
	if got.StoreNearlyFull || got.Evicted != 0 {
		t.Errorf("Space with the banners hidden = %+v, want no banner", got)
	}
	if got.Failure != localui.SpaceRelayFull {
		t.Errorf("Space.Failure with the banners hidden = %q, want the popup still", got.Failure)
	}

	if err := f.svc.DismissSpace(ctx, "everything"); statusOf(err) != 400 {
		t.Errorf("DismissSpace(unknown) = %v, want a 400", err)
	}
}

// TestSpaceFailures: a refusal for lack of room is kept for the popup until
// closed, and says whose room ran out.
func TestSpaceFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("history upload", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		f.relay.mu.Lock()
		f.relay.putErr = tppclient.ErrQuotaExceeded
		f.relay.mu.Unlock()
		f.clip.set(textContent("hello"))
		if _, err := f.svc.Sync(ctx, localui.SyncRequest{Direction: localui.DirectionUpload}); err == nil {
			t.Fatal("Sync(upload) error = nil, want the refusal")
		}
		if got := f.svc.Status(ctx).Space.Failure; got != localui.SpaceRelayFull {
			t.Errorf("Space.Failure = %q, want %q", got, localui.SpaceRelayFull)
		}
		if err := f.svc.DismissSpace(ctx, localui.SpaceBannerFailure); err != nil {
			t.Fatalf("DismissSpace(failure) error = %v", err)
		}
		if got := f.svc.Status(ctx).Space.Failure; got != "" {
			t.Errorf("Space.Failure after closing = %q, want none", got)
		}
	})

	for _, tt := range []struct {
		name   string
		limits tppclient.StoreLimits
		want   string
	}{
		{"store full", tppclient.StoreLimits{Enabled: true, MaxBytes: 1 << 20, UsedBytes: 1 << 20, MaxItems: 100, UsedItems: 3, MaxItemBytes: 1 << 19}, localui.SpaceStoreFull},
		{"relay store full", tppclient.StoreLimits{Enabled: true, MaxBytes: 1 << 20, UsedBytes: 1 << 10, AvailableBytes: 10, MaxItems: 100, UsedItems: 3, MaxItemBytes: 1 << 19}, localui.SpaceRelayStoreFull},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.enableStore()
			// Listed first, so the refusal is the save's and not the listing's.
			if _, err := f.svc.Store(ctx, localui.StoreQuery{}); err != nil {
				t.Fatalf("Store() error = %v", err)
			}
			f.relay.mu.Lock()
			f.relay.storeLocked().failing = tppclient.ErrQuotaExceeded
			f.relay.limits = &tt.limits
			f.relay.mu.Unlock()
			f.clip.set(textContent("hello"))
			if _, err := f.svc.SaveToStore(ctx, localui.StoreSaveRequest{Source: "clipboard", StoreDetails: details("Note")}); err == nil {
				t.Fatal("SaveToStore() error = nil, want the refusal")
			}
			if got := f.svc.Status(ctx).Space.Failure; got != tt.want {
				t.Errorf("Space.Failure = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSpaceFollowsTheGroup: the operator link is the relay's, and a group
// switch drops every banner and popup of the group left behind.
func TestSpaceFollowsTheGroup(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.relay.mu.Lock()
	f.relay.operatorLink = "https://tpp.example.com/help?group_id=g1"
	f.relay.mu.Unlock()
	if got := f.svc.Status(ctx).OperatorURL; got != "https://tpp.example.com/help?group_id=g1" {
		t.Errorf("Status.OperatorURL = %q, want the relay's link", got)
	}

	f.setLimits(tppclient.StoreLimits{Enabled: true, MaxBytes: 100, UsedBytes: 99, MaxItems: 10, MaxItemBytes: 50})
	f.svc.ClientHandlers().OnEntryEvicted("e1")
	f.svc.noteSpaceFailure(localui.SpaceStoreFull)
	f.svc.resetGroupState()
	if got := f.svc.Status(ctx).Space; got != (localui.SpaceView{}) {
		t.Errorf("Space after a group switch = %+v, want nothing of the old group", got)
	}
}
