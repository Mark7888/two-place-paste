package tppclient

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// TestStoreLinkLeadsBackOneEpoch: the link a rekey sends opens, with the new
// epoch's store key, to the old epoch's store key (/spec/crypto.md §7a.2).
func TestStoreLinkLeadsBackOneEpoch(t *testing.T) {
	t.Parallel()
	oldKey, err := tppcrypto.GenerateGroupKey()
	if err != nil {
		t.Fatalf("GenerateGroupKey: %v", err)
	}
	newKey, err := tppcrypto.GenerateGroupKey()
	if err != nil {
		t.Fatalf("GenerateGroupKey: %v", err)
	}
	link, err := storeLink(oldKey, newKey, 4)
	if err != nil {
		t.Fatalf("storeLink: %v", err)
	}
	newStore, err := tppcrypto.StoreKey(newKey, 4)
	if err != nil {
		t.Fatalf("StoreKey: %v", err)
	}
	wantOld, err := tppcrypto.StoreKey(oldKey, 3)
	if err != nil {
		t.Fatalf("StoreKey: %v", err)
	}
	got, err := tppcrypto.OpenStoreLink(newStore, link, 4)
	if err != nil {
		t.Fatalf("OpenStoreLink: %v", err)
	}
	if !bytes.Equal(got, wantOld) {
		t.Errorf("link opens to %x, want the store key of epoch 3 %x", got, wantOld)
	}
}

// TestRevocationNeedsTheCurrentKey: a device behind a rekey cannot seal the
// store link into the next epoch, so it refuses before sending anything and
// says why in the terms a caller already handles.
func TestRevocationNeedsTheCurrentKey(t *testing.T) {
	t.Parallel()
	c := newOfflineClient(t, 3)
	rev := &Revocation{Roster: Roster{Epoch: 4}, Target: Device{ID: "phone"}, client: c}
	if _, err := rev.Confirm(t.Context()); !errors.Is(err, ErrEpochConflict) {
		t.Errorf("Confirm() = %v, want ErrEpochConflict", err)
	}
	if rev.done {
		t.Error("the revocation is marked done after refusing")
	}
}
