package tppclient_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/pkg/tppclient"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/keystore"
)

// newClient builds a client with its own file-backed keystore, so the test
// also exercises the path a real install takes: state written at every step
// and readable again after a restart.
func newClient(t *testing.T, name string, opts tppclient.Options) (*tppclient.Client, keystore.Store) {
	t.Helper()
	store, err := keystore.Open(keystore.Options{
		Dir:        t.TempDir(),
		Passphrase: []byte("integration-test"),
		ForceFile:  true,
	})
	if err != nil {
		t.Fatalf("open keystore for %s: %v", name, err)
	}
	opts.DeviceName = name
	opts.Keystore = store
	c, err := tppclient.New(opts)
	if err != nil {
		t.Fatalf("new client %s: %v", name, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, store
}

// TestPairSyncRekeyRevoke is the phase's acceptance test: two in-process
// clients against a real relay, through the whole lifecycle of SPEC §3 and §6.
func TestPairSyncRekeyRevoke(t *testing.T) {
	relay := startRelay(t)
	ctx := testContext(t)

	// --- Create the group (SPEC §3.1) ---------------------------------------
	desktop, _ := newClient(t, "Anna — desktop", tppclient.Options{})
	if err := desktop.CreateGroup(ctx, relay.creationURL(t, "Anna")); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if got := desktop.Epoch(); got != 1 {
		t.Fatalf("epoch after creation = %d, want 1", got)
	}
	if !desktop.InGroup() {
		t.Fatal("the creating device is not in a group")
	}

	// --- Pair a second device (SPEC §3.2) ------------------------------------
	phone, _ := newClient(t, "Anna — phone", tppclient.Options{})
	joined := pair(ctx, t, desktop, phone)
	if joined.Name != "Anna — phone" {
		t.Errorf("the inviter was told the joiner is %q, want %q", joined.Name, "Anna — phone")
	}
	if phone.Epoch() != desktop.Epoch() {
		t.Errorf("phone is at epoch %d, desktop at %d", phone.Epoch(), desktop.Epoch())
	}

	// A new device starts empty: it must be told so plainly, not handed
	// history it cannot read (SPEC §3.2).
	if _, err := phone.GetLatest(ctx); !errors.Is(err, tppclient.ErrNoEntry) {
		t.Errorf("GetLatest on a fresh group = %v, want ErrNoEntry", err)
	}

	// --- Sync in both directions (SPEC §6) -----------------------------------
	sent := tppclient.Item{ContentType: "text/plain; charset=utf-8", Body: []byte("ssh anna@build-01")}
	meta, err := desktop.PutEntry(ctx, sent)
	if err != nil {
		t.Fatalf("PutEntry: %v", err)
	}
	if meta.Epoch != 1 {
		t.Errorf("entry epoch = %d, want 1", meta.Epoch)
	}
	if meta.ID == "" {
		t.Error("the relay returned no entry id")
	}
	got, err := phone.GetLatest(ctx)
	if err != nil {
		t.Fatalf("GetLatest on the phone: %v", err)
	}
	if !bytes.Equal(got.Body, sent.Body) || got.ContentType != sent.ContentType {
		t.Errorf("phone read %q/%q, want %q/%q", got.Body, got.ContentType, sent.Body, sent.ContentType)
	}

	back := tppclient.Item{ContentType: "text/plain; charset=utf-8", Body: []byte("from the phone")}
	if _, err := phone.PutEntry(ctx, back); err != nil {
		t.Fatalf("PutEntry from the phone: %v", err)
	}
	got, err = desktop.GetLatest(ctx)
	if err != nil {
		t.Fatalf("GetLatest on the desktop: %v", err)
	}
	if !bytes.Equal(got.Body, back.Body) {
		t.Errorf("desktop read %q, want %q", got.Body, back.Body)
	}

	// An entry over the inline limit goes through the blob backend and comes
	// back through a separate fetch; the caller sees no difference (SPEC §4.3).
	large := tppclient.Item{ContentType: "image/png", Filename: "screenshot.png", Body: bytes.Repeat([]byte{0xA5}, 300<<10)}
	if _, err := desktop.PutEntry(ctx, large); err != nil {
		t.Fatalf("PutEntry (large): %v", err)
	}
	got, err = phone.GetLatest(ctx)
	if err != nil {
		t.Fatalf("GetLatest (large): %v", err)
	}
	if got.Meta.Inline {
		t.Error("a 300 KB entry was stored inline; the blob path was not exercised")
	}
	if !bytes.Equal(got.Body, large.Body) || got.Filename != "screenshot.png" {
		t.Errorf("large entry came back as %d bytes named %q", len(got.Body), got.Filename)
	}

	// History is metadata only, newest first, and is pulled explicitly
	// (SPEC §6).
	history, err := phone.GetHistory(ctx, tppclient.HistoryQuery{Limit: 10})
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(history.Entries) != 3 {
		t.Fatalf("history has %d entries, want 3", len(history.Entries))
	}
	for i := 1; i < len(history.Entries); i++ {
		if history.Entries[i-1].CreatedAt.Before(history.Entries[i].CreatedAt) {
			t.Error("history is not newest first")
		}
	}
	// The id in the metadata is the one the writer bound into the ciphertext:
	// the reader reproduces the associated data from it, so a mismatch would
	// have failed the decryption above rather than merely looked odd
	// (/spec/crypto.md §5.3).
	if got.Meta.ID != history.Entries[0].ID {
		t.Errorf("latest entry id %q is not the newest in history %q", got.Meta.ID, history.Entries[0].ID)
	}

	// Tapping an older entry pulls just that one.
	older, err := phone.GetEntry(ctx, history.Entries[1].ID)
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if !bytes.Equal(older.Body, back.Body) {
		t.Errorf("fetched entry = %q, want %q", older.Body, back.Body)
	}

	// --- A third device, offline during the rekey ---------------------------
	laptop, laptopStore := newClient(t, "Anna — laptop", tppclient.Options{})
	pair(ctx, t, desktop, laptop)
	if err := laptop.Close(); err != nil {
		t.Fatalf("close the laptop: %v", err)
	}

	// --- Revoke the phone (SPEC §3.3) ---------------------------------------
	roster, err := desktop.Devices(ctx)
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(roster.Devices) != 3 {
		t.Fatalf("roster has %d devices, want 3", len(roster.Devices))
	}
	phoneID := phone.State().DeviceID

	revocation, err := desktop.Revoke(ctx, phoneID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	// Revoke prepares and stops: the roster is for the confirmation dialog,
	// and nothing has changed on the relay yet.
	if _, ok := revocation.Roster.Find(phoneID); !ok {
		t.Error("the prepared revocation does not name the target in its roster")
	}
	if len(revocation.Remaining) != 2 {
		t.Errorf("%d devices would remain, want 2", len(revocation.Remaining))
	}
	if desktop.Epoch() != 1 {
		t.Error("preparing a revocation changed the epoch; it must not")
	}

	remaining, err := revocation.Confirm(ctx)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if remaining.Epoch != 2 || desktop.Epoch() != 2 {
		t.Fatalf("epoch after the rekey = %d (client %d), want 2", remaining.Epoch, desktop.Epoch())
	}
	if _, ok := remaining.Find(phoneID); ok {
		t.Error("the revoked device is still on the roster")
	}

	// The revoked device's socket is closed and its credential is dead in the
	// same operation (SPEC §3.3 step 5).
	waitFor(t, "the revoked device to be refused", func() bool {
		_, err := phone.Devices(ctx)
		return err != nil
	})

	// --- The group works at the new epoch -----------------------------------
	afterRekey := tppclient.Item{ContentType: "text/plain; charset=utf-8", Body: []byte("after the rekey")}
	if _, err := desktop.PutEntry(ctx, afterRekey); err != nil {
		t.Fatalf("PutEntry after the rekey: %v", err)
	}

	// The device that was offline picks up its wrapped key on connect: a
	// wrapped key waits for a device rather than requiring it to be online
	// (SPEC §3.3).
	reconnected, err := tppclient.New(tppclient.Options{Keystore: laptopStore})
	if err != nil {
		t.Fatalf("reopen the laptop from its stored state: %v", err)
	}
	defer func() { _ = reconnected.Close() }()
	if reconnected.Epoch() != 1 {
		t.Fatalf("the laptop restarted at epoch %d, want the pre-rekey 1", reconnected.Epoch())
	}
	if err := reconnected.Connect(ctx); err != nil {
		t.Fatalf("reconnect the laptop: %v", err)
	}
	waitFor(t, "the laptop to install the new epoch", func() bool { return reconnected.Epoch() == 2 })

	got, err = reconnected.GetLatest(ctx)
	if err != nil {
		t.Fatalf("GetLatest on the reconnected laptop: %v", err)
	}
	if !bytes.Equal(got.Body, afterRekey.Body) {
		t.Errorf("the laptop read %q, want %q", got.Body, afterRekey.Body)
	}

	// An entry from the old epoch is skipped silently rather than decrypted
	// with a retained old key (/spec/crypto.md §7).
	if _, err := desktop.GetEntry(ctx, meta.ID); !errors.Is(err, tppclient.ErrStaleEntry) {
		t.Errorf("reading a pre-rekey entry = %v, want ErrStaleEntry", err)
	}
}

// TestRevokedDeviceStopsReconnecting proves the client gives up rather than
// hammering the relay with a credential that will never work again.
func TestRevokedDeviceStopsReconnecting(t *testing.T) {
	relay := startRelay(t)
	ctx := testContext(t)

	desktop, _ := newClient(t, "desktop", tppclient.Options{})
	if err := desktop.CreateGroup(ctx, relay.creationURL(t, "Anna")); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	var revoked atomic.Bool
	phone, _ := newClient(t, "phone", tppclient.Options{
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond,
		Handlers:   tppclient.Handlers{OnRevoked: func() { revoked.Store(true) }},
	})
	pair(ctx, t, desktop, phone)

	revocation, err := desktop.Revoke(ctx, phone.State().DeviceID)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := revocation.Confirm(ctx); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	waitFor(t, "the revoked client to notice", revoked.Load)
	if _, err := phone.GetLatest(ctx); !errors.Is(err, tppclient.ErrRevoked) {
		t.Errorf("a request from a revoked client = %v, want ErrRevoked", err)
	}
}

// TestStateSurvivesARestart is the other half of what the keystore is for: a
// client rebuilt from stored state is the same device, not a new one.
func TestStateSurvivesARestart(t *testing.T) {
	relay := startRelay(t)
	ctx := testContext(t)

	desktop, store := newClient(t, "desktop", tppclient.Options{})
	if err := desktop.CreateGroup(ctx, relay.creationURL(t, "Anna")); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	item := tppclient.Item{ContentType: "text/plain; charset=utf-8", Body: []byte("survives")}
	if _, err := desktop.PutEntry(ctx, item); err != nil {
		t.Fatalf("PutEntry: %v", err)
	}
	before := desktop.State()
	if err := desktop.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	restarted, err := tppclient.New(tppclient.Options{Keystore: store})
	if err != nil {
		t.Fatalf("New from stored state: %v", err)
	}
	defer func() { _ = restarted.Close() }()
	after := restarted.State()
	if after.DeviceID != before.DeviceID || after.GroupID != before.GroupID {
		t.Errorf("restart produced device %s in group %s, want %s / %s",
			after.DeviceID, after.GroupID, before.DeviceID, before.GroupID)
	}
	if !bytes.Equal(after.GroupKey, before.GroupKey) || !bytes.Equal(after.DevicePrivateKey, before.DevicePrivateKey) {
		t.Error("the restarted client does not hold the same keys")
	}

	if err := restarted.Connect(ctx); err != nil {
		t.Fatalf("Connect after restart: %v", err)
	}
	got, err := restarted.GetLatest(ctx)
	if err != nil {
		t.Fatalf("GetLatest after restart: %v", err)
	}
	if !bytes.Equal(got.Body, item.Body) {
		t.Errorf("read %q after the restart, want %q", got.Body, item.Body)
	}
}

// pair runs one full pairing and returns the joiner as the inviter saw it.
func pair(ctx context.Context, t *testing.T, inviter, joiner *tppclient.Client) tppclient.Device {
	t.Helper()
	invitation, err := inviter.StartPairing(ctx)
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	if invitation.ExpiresAt.Before(time.Now()) {
		t.Errorf("the pairing token is already expired at %s", invitation.ExpiresAt)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- joiner.JoinPairing(ctx, invitation.Payload) }()

	device, err := invitation.Wait(ctx)
	if err != nil {
		t.Fatalf("waiting for the joiner: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("JoinPairing: %v", err)
	}
	if !joiner.InGroup() {
		t.Fatal("the joining device is not in a group")
	}
	return device
}
