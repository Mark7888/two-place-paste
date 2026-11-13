package tppclient_test

import (
	"testing"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
)

// TestRelayStatesItsLevels asks a real relay what it supports: the Hello
// round trip end to end (SPEC §5.3).
func TestRelayStatesItsLevels(t *testing.T) {
	relay := startRelay(t)
	ctx := testContext(t)

	desktop, _ := newClient(t, "desktop", tppclient.Options{})
	if err := desktop.CreateGroup(ctx, relay.creationURL(t, "Anna")); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	got, err := desktop.Relay(ctx)
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if got.ProtocolMin > tppclient.ProtocolLevel || got.ProtocolMax < tppclient.ProtocolLevel {
		t.Errorf("Relay = [%d, %d], want a range that holds this build's level %d",
			got.ProtocolMin, got.ProtocolMax, tppclient.ProtocolLevel)
	}
}
