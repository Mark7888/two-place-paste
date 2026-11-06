package tppclient_test

import (
	"bytes"
	"testing"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// TestSeveralGroupsOnOneInstallation walks one desktop through two groups
// against a real relay (/spec/crypto.md §10.1): it adds a second group, works
// in it, switches back to the first — which was re-keyed while it was parked
// — and survives a restart with both still there.
func TestSeveralGroupsOnOneInstallation(t *testing.T) {
	relay := startRelay(t)
	ctx := testContext(t)
	text := func(s string) tppclient.Item {
		return tppclient.Item{ContentType: "text/plain; charset=utf-8", Body: []byte(s)}
	}

	// Group A: the desktop, a phone and a tablet.
	desktop, store := newClient(t, "desktop", tppclient.Options{})
	if err := desktop.CreateGroup(ctx, relay.creationURL(t, "Home")); err != nil {
		t.Fatalf("CreateGroup A: %v", err)
	}
	phone, _ := newClient(t, "phone", tppclient.Options{})
	pair(ctx, t, desktop, phone)
	tablet, _ := newClient(t, "tablet", tppclient.Options{})
	pair(ctx, t, desktop, tablet)
	inA := desktop.State()
	refA := desktop.ActiveRef()

	// Add group B: A is parked, and B is created from a fresh identity.
	if err := desktop.ParkActive(); err != nil {
		t.Fatalf("ParkActive: %v", err)
	}
	if desktop.InGroup() {
		t.Fatal("still in a group after ParkActive")
	}
	if err := desktop.CreateGroup(ctx, relay.creationURL(t, "Work")); err != nil {
		t.Fatalf("CreateGroup B: %v", err)
	}
	inB := desktop.State()
	if bytes.Equal(inA.DevicePrivateKey, inB.DevicePrivateKey) {
		t.Error("the two groups share a device keypair")
	}
	if _, err := desktop.PutEntry(ctx, text("in B")); err != nil {
		t.Fatalf("PutEntry in B: %v", err)
	}
	parked := desktop.ParkedGroups()
	if len(parked) != 1 || parked[0].Ref != refA {
		t.Fatalf("ParkedGroups() = %+v, want group A", parked)
	}

	// While the desktop is away in B, group A re-keys and moves on.
	revocation, err := phone.Revoke(ctx, tablet.State().DeviceID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := revocation.Confirm(ctx); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if _, err := phone.PutEntry(ctx, text("in A, after the rekey")); err != nil {
		t.Fatalf("PutEntry in A: %v", err)
	}

	// Back to A: it catches up like any device that was offline.
	if err := desktop.SwitchGroup(refA); err != nil {
		t.Fatalf("SwitchGroup to A: %v", err)
	}
	if err := desktop.Connect(ctx); err != nil {
		t.Fatalf("Connect in A: %v", err)
	}
	waitFor(t, "the desktop to install group A's new epoch", func() bool { return desktop.Epoch() == 2 })
	got, err := desktop.GetLatest(ctx)
	if err != nil {
		t.Fatalf("GetLatest in A: %v", err)
	}
	if string(got.Body) != "in A, after the rekey" {
		t.Errorf("read %q in A, want the phone's entry", got.Body)
	}

	// A restart opens A, the last active group, with B still parked.
	if err := desktop.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	restarted, err := tppclient.New(tppclient.Options{Keystore: store})
	if err != nil {
		t.Fatalf("New after the switch: %v", err)
	}
	defer func() { _ = restarted.Close() }()
	if restarted.ActiveRef() != refA || restarted.Epoch() != 2 {
		t.Errorf("restarted in %s, want group A at epoch 2", restarted.State())
	}
	parked = restarted.ParkedGroups()
	if len(parked) != 1 || parked[0].Ref != tppclient.GroupRef(inB.GroupID) || parked[0].GroupName != "Work" {
		t.Fatalf("ParkedGroups() after a restart = %+v, want group B", parked)
	}
	if err := restarted.SwitchGroup(parked[0].Ref); err != nil {
		t.Fatalf("SwitchGroup to B: %v", err)
	}
	if err := restarted.Connect(ctx); err != nil {
		t.Fatalf("Connect in B: %v", err)
	}
	got, err = restarted.GetLatest(ctx)
	if err != nil {
		t.Fatalf("GetLatest in B: %v", err)
	}
	if string(got.Body) != "in B" {
		t.Errorf("read %q in B, want %q", got.Body, "in B")
	}
}
