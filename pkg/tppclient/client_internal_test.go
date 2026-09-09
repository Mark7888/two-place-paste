package tppclient

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// newOfflineClient builds a client with a group already installed, so the
// crypto and epoch paths can be tested without a relay.
func newOfflineClient(t *testing.T, epoch uint64) *Client {
	t.Helper()
	c, err := New(Options{DeviceName: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	groupKey, err := tppcrypto.GenerateGroupKey()
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	c.state.ServerURL = "https://relay.example.com"
	c.state.GroupID = "group"
	c.state.DeviceID = "device"
	c.state.Epoch = epoch
	c.state.GroupKey = groupKey
	return c
}

// sealFor produces what the relay would hand back for an item written at the
// given epoch, under the id the writer chose and bound.
func sealFor(t *testing.T, c *Client, epoch uint64, body string) (EntryMeta, []byte) {
	t.Helper()
	entryID, err := newEntryID()
	if err != nil {
		t.Fatalf("entry id: %v", err)
	}
	frame, err := tppcrypto.Frame{ContentType: "text/plain; charset=utf-8", Body: []byte(body)}.Encode()
	if err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	nonce, err := tppcrypto.GenerateNonce()
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	container, err := tppcrypto.SealEntry(c.state.GroupKey, nonce, epoch, entryID, frame)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return EntryMeta{ID: entryID, Epoch: epoch, Size: int64(len(container))}, container
}

// TestEpochRules pins /spec/crypto.md §7: an older entry is skipped silently,
// a newer one means this client is behind a rekey, and only an entry at the
// client's own epoch is opened.
func TestEpochRules(t *testing.T) {
	c := newOfflineClient(t, 3)

	t.Run("same epoch decrypts", func(t *testing.T) {
		meta, container := sealFor(t, c, 3, "current")
		item, err := c.open(meta, container)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if string(item.Body) != "current" {
			t.Errorf("body = %q, want %q", item.Body, "current")
		}
	})

	t.Run("older epoch is skipped", func(t *testing.T) {
		meta, container := sealFor(t, c, 2, "old")
		if _, err := c.open(meta, container); !errors.Is(err, ErrStaleEntry) {
			t.Errorf("open = %v, want ErrStaleEntry", err)
		}
	})

	t.Run("newer epoch waits for the key", func(t *testing.T) {
		meta, container := sealFor(t, c, 4, "new")
		if _, err := c.open(meta, container); !errors.Is(err, ErrEpochAhead) {
			t.Errorf("open = %v, want ErrEpochAhead", err)
		}
	})

	t.Run("a relabelled entry does not open", func(t *testing.T) {
		// The id is bound into the associated data (/spec/crypto.md §5.3), so
		// a relay that files the ciphertext under a different id — to replay
		// it as another entry, or to hand it back as one — produces an
		// authentication failure rather than plaintext.
		meta, container := sealFor(t, c, 3, "current")
		meta.ID = "some-other-entry-id"
		if _, err := c.open(meta, container); err == nil {
			t.Error("an entry opened under an id it was not sealed with")
		}
	})

	t.Run("corruption at our own epoch is reported", func(t *testing.T) {
		meta, container := sealFor(t, c, 3, "current")
		container[len(container)-1] ^= 0xFF
		if _, err := c.open(meta, container); err == nil {
			t.Error("a tampered entry opened successfully")
		}
	})
}

// TestInstallWrappedKeyOnlyMovesForward pins the other half of §7: a client
// holds exactly one (epoch, group key) pair, and never goes back to an older
// one.
func TestInstallWrappedKeyOnlyMovesForward(t *testing.T) {
	c := newOfflineClient(t, 2)
	pub, err := c.state.PublicKey()
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	original := append([]byte(nil), c.state.GroupKey...)

	older, err := tppcrypto.GenerateGroupKey()
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	wrappedOlder, err := tppcrypto.Wrap(older, pub, 1)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if err := c.installWrappedKey(1, wrappedOlder); err != nil {
		t.Fatalf("installWrappedKey (older): %v", err)
	}
	if c.Epoch() != 2 || !bytes.Equal(c.state.GroupKey, original) {
		t.Error("a wrapped key for an older epoch was installed; epochs only move forward")
	}

	newer, err := tppcrypto.GenerateGroupKey()
	if err != nil {
		t.Fatalf("group key: %v", err)
	}
	wrappedNewer, err := tppcrypto.Wrap(newer, pub, 3)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	var seen uint64
	c.handlers.OnEpoch = func(e uint64) { seen = e }
	if err := c.installWrappedKey(3, wrappedNewer); err != nil {
		t.Fatalf("installWrappedKey (newer): %v", err)
	}
	if c.Epoch() != 3 || !bytes.Equal(c.state.GroupKey, newer) {
		t.Errorf("epoch = %d, want 3 with the new key", c.Epoch())
	}
	if seen != 3 {
		t.Errorf("OnEpoch saw %d, want 3", seen)
	}

	// A key that does not belong to this device is a failure, not a silent
	// downgrade to the old epoch.
	stranger, err := tppcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	strangerPub, err := tppcrypto.PublicKey(stranger)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	wrappedElsewhere, err := tppcrypto.Wrap(newer, strangerPub, 4)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if err := c.installWrappedKey(4, wrappedElsewhere); err == nil {
		t.Error("a wrapped key addressed to another device was accepted")
	}
	if c.Epoch() != 3 {
		t.Errorf("a failed install moved the epoch to %d", c.Epoch())
	}
}

func TestPairingPayloadRoundTrip(t *testing.T) {
	in := &tppv1.PairingPayload{
		ServerUrl:                 "https://tpp.example.com",
		PairingToken:              "AbCd-1234_x",
		InviterEphemeralPublicKey: bytes.Repeat([]byte{0x2a}, 32),
	}
	encoded, err := EncodePairingPayload(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("payload %q is not unpadded base64url; a QR code and a paste must agree", encoded)
	}

	// What a user pastes carries whatever their clipboard picked up.
	for _, variant := range []string{encoded, " " + encoded + "\n", encoded + "=="} {
		out, err := DecodePairingPayload(variant)
		if err != nil {
			t.Fatalf("decode %q: %v", variant, err)
		}
		if out.GetServerUrl() != in.GetServerUrl() || out.GetPairingToken() != in.GetPairingToken() ||
			!bytes.Equal(out.GetInviterEphemeralPublicKey(), in.GetInviterEphemeralPublicKey()) {
			t.Errorf("round trip changed the payload: %v", out)
		}
	}

	for _, bad := range []string{"", "not base64!!", "AAAA"} {
		if _, err := DecodePairingPayload(bad); err == nil {
			t.Errorf("DecodePairingPayload(%q) succeeded", bad)
		}
	}
}

func TestURLHandling(t *testing.T) {
	t.Run("creation url", func(t *testing.T) {
		base, token, err := splitCreationURL("https://tpp.example.com/9tokenvalue")
		if err != nil {
			t.Fatalf("split: %v", err)
		}
		if base != "https://tpp.example.com" || token != "9tokenvalue" {
			t.Errorf("split = %q, %q", base, token)
		}
		for _, bad := range []string{"https://tpp.example.com", "https://tpp.example.com/a/b", "ftp://x/y", "/relative"} {
			if _, _, err := splitCreationURL(bad); err == nil {
				t.Errorf("splitCreationURL(%q) succeeded", bad)
			}
		}
	})

	t.Run("websocket url", func(t *testing.T) {
		got, err := websocketURL("https://tpp.example.com", "dev-1")
		if err != nil {
			t.Fatalf("websocketURL: %v", err)
		}
		if got != "wss://tpp.example.com/ws?device_id=dev-1" {
			t.Errorf("websocketURL = %q", got)
		}
		// The two unauthenticated flows dial without a credential.
		got, err = websocketURL("http://127.0.0.1:8080", "")
		if err != nil {
			t.Fatalf("websocketURL: %v", err)
		}
		if got != "ws://127.0.0.1:8080/ws" {
			t.Errorf("websocketURL = %q", got)
		}
	})

	t.Run("base url is scheme and host only", func(t *testing.T) {
		got, err := normalizeServerURL("https://tpp.example.com/some/path?x=1")
		if err != nil {
			t.Fatalf("normalizeServerURL: %v", err)
		}
		if got != "https://tpp.example.com" {
			t.Errorf("normalizeServerURL = %q", got)
		}
	})
}

func TestProtocolErrorsCarryTheirCode(t *testing.T) {
	err := error(&ProtocolError{Code: tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT, Message: "moved on"})
	if !errors.Is(err, ErrEpochConflict) {
		t.Error("an epoch conflict does not match ErrEpochConflict")
	}
	if errors.Is(err, ErrTooLarge) {
		t.Error("an epoch conflict matched ErrTooLarge")
	}
}

func TestStateWithoutAGroupRefusesWork(t *testing.T) {
	c, err := New(Options{ServerURL: "https://tpp.example.com"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	if c.InGroup() {
		t.Error("a fresh client claims to be in a group")
	}
	// A first launch generates its device keypair and nothing else
	// (/spec/crypto.md §3).
	if len(c.State().DevicePrivateKey) != tppcrypto.KeySize {
		t.Error("a fresh client has no device keypair")
	}
	ctx := t.Context()
	if _, err := c.GetLatest(ctx); !errors.Is(err, ErrNoGroup) {
		t.Errorf("GetLatest = %v, want ErrNoGroup", err)
	}
	if _, err := c.PutEntry(ctx, Item{ContentType: "text/plain"}); !errors.Is(err, ErrNoGroup) {
		t.Errorf("PutEntry = %v, want ErrNoGroup", err)
	}
	if _, err := c.Devices(ctx); !errors.Is(err, ErrNoGroup) {
		t.Errorf("Devices = %v, want ErrNoGroup", err)
	}
	if err := c.Connect(ctx); !errors.Is(err, ErrNoGroup) {
		t.Errorf("Connect = %v, want ErrNoGroup", err)
	}
}

// TestNoClipboardAnywhere guards the invariant this package exists to make
// structural (SPEC §3.3): a rekey cannot touch the local clipboard, because
// nothing here can reach a clipboard at all. A future import that could is a
// design change, and it fails here first.
func TestNoClipboardAnywhere(t *testing.T) {
	fset := token.NewFileSet()
	for _, dir := range []string{".", "tppcrypto", "keystore"} {
		pkgs, err := parser.ParseDir(fset, dir, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			for name, file := range pkg.Files {
				for _, imp := range file.Imports {
					if strings.Contains(strings.ToLower(imp.Path.Value), "clipboard") {
						t.Errorf("%s imports %s; this package must never reach the OS clipboard", name, imp.Path.Value)
					}
				}
			}
		}
	}
}
