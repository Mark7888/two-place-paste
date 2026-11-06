package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// newGroupsFixture is a fixture with "Keep several groups" on.
func newGroupsFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	if _, err := f.svc.UpdateSettings(context.Background(), localui.SettingsPatch{MultiGroup: boolPtr(true)}); err != nil {
		t.Fatalf("UpdateSettings(multi_group) error = %v", err)
	}
	return f
}

func statusCode(err error) int {
	var se *localui.StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// drainUntil reads events until one of kind arrives, or fails.
func drainUntil(t *testing.T, events <-chan localui.Event, kind string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == kind {
				return
			}
		case <-timeout:
			t.Fatalf("no %q event", kind)
		}
	}
}

// TestGroupOperationsNeedTheSetting: with the setting off every group
// operation is refused and nothing stored changes; turning it off again
// keeps the groups.
func TestGroupOperationsNeedTheSetting(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	ctx := context.Background()
	f.relay.parked = []tppclient.ParkedGroup{{Ref: "ref-work", GroupName: "Work"}}

	if f.svc.Status(ctx).MultiGroup {
		t.Error("Status().MultiGroup = true by default, want false")
	}
	ops := []struct {
		name string
		do   func() error
	}{
		{"Groups", func() error { _, err := f.svc.Groups(ctx); return err }},
		{"SwitchGroup", func() error { _, err := f.svc.SwitchGroup(ctx, "ref-work"); return err }},
		{"AddGroup", func() error { _, err := f.svc.AddGroup(ctx); return err }},
		{"RemoveGroup", func() error { _, err := f.svc.RemoveGroup(ctx, "ref-work"); return err }},
	}
	for _, op := range ops {
		if code := statusCode(op.do()); code != http.StatusConflict {
			t.Errorf("%s with the setting off: status %d, want %d", op.name, code, http.StatusConflict)
		}
	}
	if !f.relay.InGroup() || f.relay.ActiveRef() != "ref-home" || len(f.relay.ParkedGroups()) != 1 {
		t.Error("a refused group operation changed the stored groups")
	}

	view, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{MultiGroup: boolPtr(true)})
	if err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if !view.MultiGroup || !f.svc.Status(ctx).MultiGroup {
		t.Error("the setting did not turn on")
	}
	groups, err := f.svc.Groups(ctx)
	if err != nil {
		t.Fatalf("Groups() error = %v", err)
	}
	if groups.Active == nil || groups.Active.Ref != "ref-home" || len(groups.Parked) != 1 || groups.Parked[0].Name != "Work" {
		t.Errorf("Groups() = %+v, want ref-home active and Work parked", groups)
	}

	if _, err := f.svc.UpdateSettings(ctx, localui.SettingsPatch{MultiGroup: boolPtr(false)}); err != nil {
		t.Fatalf("UpdateSettings(off) error = %v", err)
	}
	if !f.relay.InGroup() || f.relay.ActiveRef() != "ref-home" || len(f.relay.ParkedGroups()) != 1 {
		t.Error("turning the setting off changed the stored groups")
	}
}

func TestAddGroupSetsTheGroupAside(t *testing.T) {
	t.Parallel()

	f := newGroupsFixture(t)
	ctx := context.Background()
	events, stop := f.svc.Subscribe()
	defer stop()

	plan, err := f.svc.PrepareRevoke(ctx, "d2")
	if err != nil {
		t.Fatalf("PrepareRevoke() error = %v", err)
	}
	if _, err := f.svc.StartOffer(ctx, "https://relay.example"); err != nil {
		t.Fatalf("StartOffer() error = %v", err)
	}
	offer := f.relay.offer

	view, err := f.svc.AddGroup(ctx)
	if err != nil {
		t.Fatalf("AddGroup() error = %v", err)
	}
	if view.Active != nil {
		t.Errorf("AddGroup() left %+v active, want none", view.Active)
	}
	if len(view.Parked) != 1 || view.Parked[0].Ref != "ref-home" {
		t.Errorf("AddGroup() parked %+v, want ref-home", view.Parked)
	}
	if !offer.closed.Load() {
		t.Error("the pairing code shown for the old identity is still live")
	}
	if _, err := f.svc.ConfirmRevoke(ctx, plan.ID); err == nil {
		t.Error("a revocation prepared in the old group is still confirmable")
	}
	if st := f.svc.Status(ctx); st.InGroup || st.Connected {
		t.Errorf("Status() after AddGroup: in_group %v connected %v, want neither", st.InGroup, st.Connected)
	}
	drainUntil(t, events, localui.EventGroups)
}

func TestSwitchGroup(t *testing.T) {
	t.Parallel()

	f := newGroupsFixture(t)
	ctx := context.Background()
	f.relay.parked = []tppclient.ParkedGroup{{Ref: "ref-work", GroupName: "Work"}}
	f.svc.noteSkipped(localui.SkippedView{Message: "a skipped transfer in the old group"})
	events, stop := f.svc.Subscribe()
	defer stop()
	connects := f.relay.connects

	view, err := f.svc.SwitchGroup(ctx, "ref-work")
	if err != nil {
		t.Fatalf("SwitchGroup() error = %v", err)
	}
	if view.Active == nil || view.Active.Ref != "ref-work" {
		t.Errorf("SwitchGroup() active = %+v, want ref-work", view.Active)
	}
	if len(view.Parked) != 1 || view.Parked[0].Ref != "ref-home" {
		t.Errorf("SwitchGroup() parked = %+v, want ref-home", view.Parked)
	}
	f.relay.mu.Lock()
	connected := f.relay.connects - connects
	f.relay.mu.Unlock()
	if connected != 1 {
		t.Errorf("the switch connected %d times, want 1", connected)
	}
	st := f.svc.Status(ctx)
	if st.Skipped != nil {
		t.Error("a notice about the old group survived the switch")
	}
	if st.LastSync != nil {
		t.Errorf("Status().LastSync = %s after the switch, want none", st.LastSync)
	}
	drainUntil(t, events, localui.EventGroups)

	// A ref that is gone is a 404, and the group in use stays connected.
	_, err = f.svc.SwitchGroup(ctx, "ref-gone")
	if code := statusCode(err); code != http.StatusNotFound {
		t.Errorf("SwitchGroup(unknown) status %d, want %d", code, http.StatusNotFound)
	}
	if f.relay.ActiveRef() != "ref-work" {
		t.Errorf("a failed switch moved the device to %q", f.relay.ActiveRef())
	}
}

func TestSwitchGroupFromNoGroup(t *testing.T) {
	t.Parallel()

	f := newGroupsFixture(t)
	ctx := context.Background()
	if err := f.svc.Forget(ctx); err != nil {
		t.Fatalf("Forget() error = %v", err)
	}
	f.relay.parked = []tppclient.ParkedGroup{{Ref: "ref-work", GroupName: "Work"}}

	view, err := f.svc.SwitchGroup(ctx, "ref-work")
	if err != nil {
		t.Fatalf("SwitchGroup() error = %v", err)
	}
	if view.Active == nil || view.Active.Ref != "ref-work" || len(view.Parked) != 0 {
		t.Errorf("SwitchGroup() = %+v, want ref-work active and nothing parked", view)
	}
}

func TestSwitchGroupFailureKeepsTheGroup(t *testing.T) {
	t.Parallel()

	f := newGroupsFixture(t)
	ctx := context.Background()
	f.relay.parked = []tppclient.ParkedGroup{{Ref: "ref-work"}}
	f.relay.parkErr = tppclient.ErrParkedUnreadable

	_, err := f.svc.SwitchGroup(ctx, "ref-work")
	if code := statusCode(err); code != http.StatusConflict {
		t.Errorf("SwitchGroup() status %d, want %d", code, http.StatusConflict)
	}
	if _, err := f.svc.AddGroup(ctx); statusCode(err) != http.StatusConflict {
		t.Errorf("AddGroup() = %v, want a 409", err)
	}
	if f.relay.ActiveRef() != "ref-home" {
		t.Errorf("a failed switch moved the device to %q", f.relay.ActiveRef())
	}
}

func TestRemoveGroup(t *testing.T) {
	t.Parallel()

	f := newGroupsFixture(t)
	ctx := context.Background()
	f.relay.parked = []tppclient.ParkedGroup{{Ref: "ref-work"}}

	if _, err := f.svc.RemoveGroup(ctx, "ref-home"); statusCode(err) != http.StatusNotFound {
		t.Errorf("RemoveGroup(active) = %v, want a 404: the active group is left from Settings", err)
	}
	view, err := f.svc.RemoveGroup(ctx, "ref-work")
	if err != nil {
		t.Fatalf("RemoveGroup() error = %v", err)
	}
	if len(view.Parked) != 0 || view.Active == nil || view.Active.Ref != "ref-home" {
		t.Errorf("RemoveGroup() = %+v, want ref-home active and nothing parked", view)
	}
}
