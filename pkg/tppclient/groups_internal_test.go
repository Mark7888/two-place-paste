package tppclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Mark7888/two-place-paste/pkg/tppclient/keystore"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// openTestStore is a file keystore in a temporary directory.
func openTestStore(t *testing.T) keystore.Store {
	t.Helper()
	store, err := keystore.Open(keystore.Options{Dir: t.TempDir(), Passphrase: []byte("test"), ForceFile: true})
	if err != nil {
		t.Fatalf("open keystore: %v", err)
	}
	return store
}

// groupState is a joined state for a group that exists nowhere: enough for
// everything that does not talk to a relay.
func groupState(t *testing.T, groupID, name string) State {
	t.Helper()
	priv, err := tppcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("device key: %v", err)
	}
	gk, err := tppcrypto.GenerateGroupKey()
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	return State{
		ServerURL:        "https://relay.example.com",
		GroupID:          groupID,
		DeviceID:         "device-" + groupID,
		DeviceName:       "stored name",
		Epoch:            3,
		DevicePrivateKey: priv,
		GroupKey:         gk,
		GroupName:        name,
	}
}

func storeParked(t *testing.T, store keystore.Store, rec parkedRecord) {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("encode parked: %v", err)
	}
	if err := store.Save(DefaultParkedName, raw); err != nil {
		t.Fatalf("save parked: %v", err)
	}
}

func loadParked(t *testing.T, store keystore.Store) []State {
	t.Helper()
	raw, err := store.Load(DefaultParkedName)
	if errors.Is(err, keystore.ErrNotFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("load parked: %v", err)
	}
	var rec parkedRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("decode parked: %v", err)
	}
	return rec.Groups
}

func groupIDs(groups []State) []string {
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.GroupID)
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func newStoredClient(t *testing.T, store keystore.Store) *Client {
	t.Helper()
	c, err := New(Options{Keystore: store, DeviceName: "this laptop"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestGroupRef(t *testing.T) {
	t.Parallel()

	a, b := GroupRef("group-a"), GroupRef("group-b")
	if len(a) != 16 {
		t.Errorf("len(GroupRef) = %d, want 16", len(a))
	}
	if a != GroupRef("group-a") {
		t.Error("GroupRef is not stable")
	}
	if a == b {
		t.Error("two groups share a ref")
	}
	if got := GroupRef(""); got != "" {
		t.Errorf("GroupRef(\"\") = %q, want empty", got)
	}
}

// TestSwitchGroupSwapsTheEntries is the downgrade property: after a switch
// the active entry holds the incoming group, exactly as stored, and the
// parked entry holds the outgoing one.
func TestSwitchGroupSwapsTheEntries(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	a, b := groupState(t, "group-a", "Home"), groupState(t, "group-b", "Work")
	if err := SaveState(store, "", a); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	storeParked(t, store, parkedRecord{Version: parkedVersion, Groups: []State{b}})

	c := newStoredClient(t, store)
	if got, want := c.ActiveRef(), GroupRef("group-a"); got != want {
		t.Errorf("ActiveRef() = %q, want %q", got, want)
	}
	parked := c.ParkedGroups()
	if len(parked) != 1 || parked[0].Ref != GroupRef("group-b") || parked[0].GroupName != "Work" {
		t.Fatalf("ParkedGroups() = %+v, want Work", parked)
	}

	if err := c.SwitchGroup(GroupRef("group-b")); err != nil {
		t.Fatalf("SwitchGroup: %v", err)
	}
	got := c.State()
	if got.GroupID != "group-b" || !bytes.Equal(got.GroupKey, b.GroupKey) || got.Epoch != b.Epoch {
		t.Errorf("after the switch the client holds %s, want group-b at epoch %d", got, b.Epoch)
	}
	if got.DeviceName != "this laptop" {
		t.Errorf("the switched-to group is named %q, want the installation's name", got.DeviceName)
	}

	stored, err := LoadState(store, "")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if stored.GroupID != "group-b" || !bytes.Equal(stored.DevicePrivateKey, b.DevicePrivateKey) {
		t.Errorf("the active entry holds %s, want group-b with its own keypair", stored)
	}
	if ids := groupIDs(loadParked(t, store)); !equalStrings(ids, []string{"group-a"}) {
		t.Errorf("the parked entry holds %v, want [group-a]", ids)
	}

	// And back: the first group comes back with its own keys, untouched.
	if err := c.SwitchGroup(GroupRef("group-a")); err != nil {
		t.Fatalf("SwitchGroup back: %v", err)
	}
	back := c.State()
	if back.GroupID != "group-a" || !bytes.Equal(back.GroupKey, a.GroupKey) ||
		!bytes.Equal(back.DevicePrivateKey, a.DevicePrivateKey) {
		t.Errorf("switching back gave %s, want group-a with its keys", back)
	}
}

// TestStartRemovesAnInterruptedSwitch: a switch stopped between its writes
// leaves the active group in the parked list too, which the next start drops.
func TestStartRemovesAnInterruptedSwitch(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	a, b := groupState(t, "group-a", "Home"), groupState(t, "group-b", "Work")
	if err := SaveState(store, "", b); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	storeParked(t, store, parkedRecord{Version: parkedVersion, Groups: []State{a, b}})

	c := newStoredClient(t, store)
	if c.State().GroupID != "group-b" {
		t.Errorf("the client started in %s, want the active entry's group-b", c.State())
	}
	parked := c.ParkedGroups()
	if len(parked) != 1 || parked[0].Ref != GroupRef("group-a") {
		t.Errorf("ParkedGroups() = %+v, want only group-a", parked)
	}
	if ids := groupIDs(loadParked(t, store)); !equalStrings(ids, []string{"group-a"}) {
		t.Errorf("the parked entry holds %v after the clean-up, want [group-a]", ids)
	}
}

func TestParkActive(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	a := groupState(t, "group-a", "Home")
	if err := SaveState(store, "", a); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	c := newStoredClient(t, store)

	if err := c.ParkActive(); err != nil {
		t.Fatalf("ParkActive: %v", err)
	}
	got := c.State()
	switch {
	case c.InGroup():
		t.Error("the client is still in a group after ParkActive")
	case bytes.Equal(got.DevicePrivateKey, a.DevicePrivateKey):
		t.Error("the fresh state reuses the parked group's keypair")
	case got.DeviceName != "this laptop":
		t.Errorf("the fresh state is named %q, want the installation's name", got.DeviceName)
	}
	if ids := groupIDs(loadParked(t, store)); !equalStrings(ids, []string{"group-a"}) {
		t.Errorf("the parked entry holds %v, want [group-a]", ids)
	}
	stored, err := LoadState(store, "")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if stored.InGroup() {
		t.Errorf("the active entry still holds %s", stored)
	}

	// With no group there is nothing to park.
	if err := c.ParkActive(); err != nil {
		t.Errorf("ParkActive with no group = %v, want nil", err)
	}
	if n := len(c.ParkedGroups()); n != 1 {
		t.Errorf("len(ParkedGroups()) = %d after a second ParkActive, want 1", n)
	}
}

// TestDeadGroupsAreNotParked: a group this device was revoked from is
// discarded, as leaving would, by both ways out of it.
func TestDeadGroupsAreNotParked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		do   func(c *Client) error
	}{
		{name: "park", do: func(c *Client) error { return c.ParkActive() }},
		{name: "switch", do: func(c *Client) error { return c.SwitchGroup(GroupRef("group-b")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openTestStore(t)
			if err := SaveState(store, "", groupState(t, "group-a", "Home")); err != nil {
				t.Fatalf("SaveState: %v", err)
			}
			storeParked(t, store, parkedRecord{Version: parkedVersion, Groups: []State{groupState(t, "group-b", "Work")}})
			c := newStoredClient(t, store)
			c.mu.Lock()
			c.revoked, c.revokedErr = true, ErrRevoked
			c.mu.Unlock()

			if err := tt.do(c); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}
			for _, g := range loadParked(t, store) {
				if g.GroupID == "group-a" {
					t.Error("the revoked group was parked")
				}
			}
		})
	}
}

func TestRemoveParked(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := SaveState(store, "", groupState(t, "group-a", "Home")); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	storeParked(t, store, parkedRecord{Version: parkedVersion, Groups: []State{groupState(t, "group-b", "Work")}})
	c := newStoredClient(t, store)

	if err := c.RemoveParked(GroupRef("group-a")); !errors.Is(err, ErrUnknownGroup) {
		t.Errorf("RemoveParked(active) = %v, want ErrUnknownGroup", err)
	}
	if err := c.RemoveParked("nope"); !errors.Is(err, ErrUnknownGroup) {
		t.Errorf("RemoveParked(unknown) = %v, want ErrUnknownGroup", err)
	}
	if err := c.RemoveParked(GroupRef("group-b")); err != nil {
		t.Fatalf("RemoveParked: %v", err)
	}
	if n := len(c.ParkedGroups()); n != 0 {
		t.Errorf("len(ParkedGroups()) = %d, want 0", n)
	}
	// The last parked group gone, the entry goes too.
	if _, err := store.Load(DefaultParkedName); !errors.Is(err, keystore.ErrNotFound) {
		t.Errorf("Load(parked) after removing the last = %v, want ErrNotFound", err)
	}
	if !c.InGroup() || c.State().GroupID != "group-a" {
		t.Error("removing a parked group touched the active one")
	}
}

// TestUnreadableParkedRecordIsLeftAlone: a record from a newer build is
// neither shown nor overwritten.
func TestUnreadableParkedRecordIsLeftAlone(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := SaveState(store, "", groupState(t, "group-a", "Home")); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	storeParked(t, store, parkedRecord{Version: parkedVersion + 1, Groups: []State{groupState(t, "group-b", "Work")}})
	before, err := store.Load(DefaultParkedName)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	c := newStoredClient(t, store)
	if n := len(c.ParkedGroups()); n != 0 {
		t.Errorf("len(ParkedGroups()) = %d, want 0 for an unreadable record", n)
	}
	if err := c.ParkActive(); !errors.Is(err, ErrParkedUnreadable) {
		t.Errorf("ParkActive = %v, want ErrParkedUnreadable", err)
	}
	if !c.InGroup() {
		t.Error("a refused ParkActive left the client without its group")
	}
	after, err := store.Load(DefaultParkedName)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the unreadable record was overwritten")
	}
}

func TestForgetLeavesParkedGroups(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := SaveState(store, "", groupState(t, "group-a", "Home")); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	storeParked(t, store, parkedRecord{Version: parkedVersion, Groups: []State{groupState(t, "group-b", "Work")}})
	c := newStoredClient(t, store)

	if err := c.Forget(); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if c.InGroup() {
		t.Error("still in a group after Forget")
	}
	if ids := groupIDs(loadParked(t, store)); !equalStrings(ids, []string{"group-b"}) {
		t.Errorf("the parked entry holds %v after Forget, want [group-b]", ids)
	}
	// The parked group is still there to switch to.
	if err := c.SwitchGroup(GroupRef("group-b")); err != nil {
		t.Fatalf("SwitchGroup after Forget: %v", err)
	}
	if c.State().GroupID != "group-b" {
		t.Errorf("switched to %s, want group-b", c.State())
	}
	if n := len(loadParked(t, store)); n != 0 {
		t.Errorf("the parked entry holds %d groups, want 0: the forgotten group was not parked", n)
	}
}
