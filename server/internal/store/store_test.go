package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// The suite below runs against every Store implementation. MemoryStore is the
// one the WebSocket tests use; RedisStore is the one that ships. They must not
// drift, so they answer the same questions here.
func forEachStore(t *testing.T, run func(t *testing.T, s store.Store)) {
	t.Helper()

	t.Run("memory", func(t *testing.T) {
		t.Parallel()
		run(t, store.NewMemoryStore())
	})
	t.Run("redis", func(t *testing.T) {
		t.Parallel()
		run(t, newTestRedisStore(t))
	})
}

func mustToken(ctx context.Context, t *testing.T, s store.Store) store.Token {
	t.Helper()
	tok, err := s.CreateToken(ctx, "test token")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	return tok
}

func TestTokenLifecycle(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		tok := mustToken(ctx, t, s)

		got, err := s.GetToken(ctx, tok.Value)
		if err != nil {
			t.Fatalf("GetToken() error = %v", err)
		}
		if got.Used {
			t.Errorf("GetToken().Used = true, want false")
		}
		if got.Name != "test token" {
			t.Errorf("GetToken().Name = %q, want %q", got.Name, "test token")
		}

		group, err := s.ConsumeToken(ctx, tok.Value)
		if err != nil {
			t.Fatalf("ConsumeToken() error = %v", err)
		}
		if group.ID != tok.Value {
			t.Errorf("ConsumeToken().ID = %q, want the token value %q", group.ID, tok.Value)
		}
		if group.Epoch != 1 {
			t.Errorf("ConsumeToken().Epoch = %d, want 1", group.Epoch)
		}

		if _, err := s.ConsumeToken(ctx, tok.Value); !errors.Is(err, store.ErrTokenConsumed) {
			t.Errorf("second ConsumeToken() error = %v, want %v", err, store.ErrTokenConsumed)
		}
		if _, err := s.ConsumeToken(ctx, "no-such-token"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ConsumeToken(unknown) error = %v, want %v", err, store.ErrNotFound)
		}

		got, err = s.GetToken(ctx, tok.Value)
		if err != nil {
			t.Fatalf("GetToken() error = %v", err)
		}
		if !got.Used || got.GroupID != group.ID {
			t.Errorf("GetToken() = {Used:%v GroupID:%q}, want {Used:true GroupID:%q}", got.Used, got.GroupID, group.ID)
		}

		tokens, err := s.ListTokens(ctx)
		if err != nil {
			t.Fatalf("ListTokens() error = %v", err)
		}
		if len(tokens) == 0 {
			t.Errorf("ListTokens() returned no tokens, want at least the one just created")
		}
	})
}

func TestCreateGroupWithDevice(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		tok := mustToken(ctx, t, s)

		created, err := s.CreateGroup(ctx, tok.Value, store.NewDevice{
			Name:            "laptop",
			PublicKey:       []byte("pubkey-1"),
			WrappedGroupKey: []byte("wrapped-1"),
		})
		if err != nil {
			t.Fatalf("CreateGroup() error = %v", err)
		}

		dev, err := s.GetDevice(ctx, created.Device.ID)
		if err != nil {
			t.Fatalf("GetDevice() error = %v", err)
		}
		if dev.Name != "laptop" || string(dev.PublicKey) != "pubkey-1" || dev.GroupID != created.Group.ID {
			t.Errorf("GetDevice() = %+v, want the device just created", dev)
		}
		if !dev.LastSeen.IsZero() {
			t.Errorf("GetDevice().LastSeen = %v, want the zero time for a device that never connected", dev.LastSeen)
		}

		key, err := s.GetWrappedKey(ctx, created.Device.ID)
		if err != nil {
			t.Fatalf("GetWrappedKey() error = %v", err)
		}
		if key.Epoch != 1 || string(key.Key) != "wrapped-1" {
			t.Errorf("GetWrappedKey() = {Epoch:%d Key:%q}, want {Epoch:1 Key:%q}", key.Epoch, key.Key, "wrapped-1")
		}

		if err := s.TouchLastSeen(ctx, created.Device.ID); err != nil {
			t.Fatalf("TouchLastSeen() error = %v", err)
		}
		dev, err = s.GetDevice(ctx, created.Device.ID)
		if err != nil {
			t.Fatalf("GetDevice() error = %v", err)
		}
		if dev.LastSeen.IsZero() {
			t.Errorf("GetDevice().LastSeen is zero after TouchLastSeen()")
		}

		// A revoked device must not be resurrected by its own reconnect.
		if err := s.RemoveDevice(ctx, created.Group.ID, created.Device.ID); err != nil {
			t.Fatalf("RemoveDevice() error = %v", err)
		}
		if err := s.TouchLastSeen(ctx, created.Device.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("TouchLastSeen(removed) error = %v, want %v", err, store.ErrNotFound)
		}
		if _, err := s.GetWrappedKey(ctx, created.Device.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetWrappedKey(removed) error = %v, want %v", err, store.ErrNotFound)
		}
	})
}

func TestApplyRekey(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		tok := mustToken(ctx, t, s)

		created, err := s.CreateGroup(ctx, tok.Value, store.NewDevice{
			Name: "a", PublicKey: []byte("pk-a"), WrappedGroupKey: []byte("w-a"),
		})
		if err != nil {
			t.Fatalf("CreateGroup() error = %v", err)
		}
		gid, a := created.Group.ID, created.Device.ID

		b, err := s.AddDevice(ctx, gid, store.NewDevice{Name: "b", PublicKey: []byte("pk-b"), WrappedGroupKey: []byte("w-b")})
		if err != nil {
			t.Fatalf("AddDevice() error = %v", err)
		}
		c, err := s.AddDevice(ctx, gid, store.NewDevice{Name: "c", PublicKey: []byte("pk-c"), WrappedGroupKey: []byte("w-c")})
		if err != nil {
			t.Fatalf("AddDevice() error = %v", err)
		}

		devices, err := s.ListDevices(ctx, gid)
		if err != nil {
			t.Fatalf("ListDevices() error = %v", err)
		}
		if len(devices) != 3 {
			t.Fatalf("ListDevices() returned %d devices, want 3", len(devices))
		}

		tests := []struct {
			name    string
			epoch   uint64
			revoked string
			keys    []store.RekeyKey
			want    error
		}{
			{
				name: "stale epoch", epoch: 99, revoked: c.ID,
				keys: []store.RekeyKey{{DeviceID: a, Key: []byte("x")}, {DeviceID: b.ID, Key: []byte("x")}},
				want: store.ErrEpochConflict,
			},
			{
				name: "missing a remaining device", epoch: 1, revoked: c.ID,
				keys: []store.RekeyKey{{DeviceID: a, Key: []byte("x")}},
				want: store.ErrInvalidRekey,
			},
			{
				name: "key for the revoked device", epoch: 1, revoked: c.ID,
				keys: []store.RekeyKey{{DeviceID: a, Key: []byte("x")}, {DeviceID: b.ID, Key: []byte("x")}, {DeviceID: c.ID, Key: []byte("x")}},
				want: store.ErrInvalidRekey,
			},
			{
				name: "unknown revoked device", epoch: 1, revoked: "nobody",
				keys: []store.RekeyKey{{DeviceID: a, Key: []byte("x")}, {DeviceID: b.ID, Key: []byte("x")}},
				want: store.ErrNotFound,
			},
		}
		for _, tt := range tests {
			if _, err := s.ApplyRekey(ctx, gid, tt.revoked, tt.epoch, tt.keys); !errors.Is(err, tt.want) {
				t.Errorf("ApplyRekey(%s) error = %v, want %v", tt.name, err, tt.want)
			}
			group, err := s.GetGroup(ctx, gid)
			if err != nil {
				t.Fatalf("GetGroup() error = %v", err)
			}
			if group.Epoch != 1 {
				t.Fatalf("after rejected ApplyRekey(%s), epoch = %d, want 1", tt.name, group.Epoch)
			}
		}

		group, err := s.ApplyRekey(ctx, gid, c.ID, 1, []store.RekeyKey{
			{DeviceID: a, Key: []byte("w2-a")},
			{DeviceID: b.ID, Key: []byte("w2-b")},
		})
		if err != nil {
			t.Fatalf("ApplyRekey() error = %v", err)
		}
		if group.Epoch != 2 {
			t.Errorf("ApplyRekey().Epoch = %d, want 2", group.Epoch)
		}
		for _, id := range []string{a, b.ID} {
			key, err := s.GetWrappedKey(ctx, id)
			if err != nil {
				t.Fatalf("GetWrappedKey(%s) error = %v", id, err)
			}
			if key.Epoch != 2 {
				t.Errorf("GetWrappedKey(%s).Epoch = %d, want 2", id, key.Epoch)
			}
		}
		if _, err := s.GetDevice(ctx, c.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetDevice(revoked) error = %v, want %v", err, store.ErrNotFound)
		}
		devices, err = s.ListDevices(ctx, gid)
		if err != nil {
			t.Fatalf("ListDevices() error = %v", err)
		}
		if len(devices) != 2 {
			t.Errorf("ListDevices() returned %d devices after revocation, want 2", len(devices))
		}
	})
}

func TestPairing(t *testing.T) {
	t.Parallel()

	forEachStore(t, func(t *testing.T, s store.Store) {
		ctx := context.Background()
		tok := mustToken(ctx, t, s)
		created, err := s.CreateGroup(ctx, tok.Value, store.NewDevice{Name: "inviter", PublicKey: []byte("pk")})
		if err != nil {
			t.Fatalf("CreateGroup() error = %v", err)
		}

		p, err := s.CreatePairing(ctx, created.Group.ID, created.Device.ID, time.Minute)
		if err != nil {
			t.Fatalf("CreatePairing() error = %v", err)
		}

		got, err := s.GetPairing(ctx, p.Token)
		if err != nil {
			t.Fatalf("GetPairing() error = %v", err)
		}
		if got.InviterDeviceID != created.Device.ID || got.GroupID != created.Group.ID {
			t.Errorf("GetPairing() = %+v, want inviter %q in group %q", got, created.Device.ID, created.Group.ID)
		}

		if _, err := s.ConsumePairing(ctx, p.Token, "joiner-1"); err != nil {
			t.Fatalf("ConsumePairing() error = %v", err)
		}
		if _, err := s.ConsumePairing(ctx, p.Token, "joiner-2"); !errors.Is(err, store.ErrPairingConsumed) {
			t.Errorf("second ConsumePairing() error = %v, want %v", err, store.ErrPairingConsumed)
		}
		if _, err := s.ConsumePairing(ctx, "no-such-pairing", "joiner"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ConsumePairing(unknown) error = %v, want %v", err, store.ErrNotFound)
		}

		if err := s.DeletePairing(ctx, p.Token); err != nil {
			t.Fatalf("DeletePairing() error = %v", err)
		}
		if _, err := s.GetPairing(ctx, p.Token); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetPairing(deleted) error = %v, want %v", err, store.ErrNotFound)
		}
	})
}
