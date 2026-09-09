// Package ws is the WebSocket transport and the protocol handlers behind it
// (SPEC §3, §5, §6).
//
// Every frame on the wire is a serialized tpp.v1.Envelope carried as a binary
// WebSocket message; nothing else is ever written to the socket. The concrete
// message travels as opaque bytes inside the envelope, which is what makes the
// carrier replaceable later at no cost (SPEC §5.1).
//
// # Connection authentication
//
// A connection establishes its device identity at connect time, from the
// `device_id` query parameter, and keeps it for the life of the socket. Two
// frames — CreateGroupRequest and PairingJoinRequest — are legal without an
// identity, because they are how a device gets one; every other frame from an
// unauthenticated connection is answered with ERROR_CODE_UNAUTHENTICATED.
//
// The device identifier is therefore a bearer credential. That is a deliberate
// MVP decision, made because the wire contract (ROADMAP P1) carries no
// separate device secret and /spec/crypto.md explicitly leaves server-side
// device authentication to this phase. It is defensible here: identifiers are
// 256 bits of crypto/rand and are only ever disclosed to devices already
// inside the same group, and every device in a group belongs to the same
// person (SPEC §3.3), so the only impersonation it permits is one the threat
// model already grants. Revocation deletes the device record in the same
// atomic operation that rekeys the group, so the credential stops working at
// the instant the socket is closed (SPEC §3.3 step 5). A future wire-contract
// change should add a per-device secret established at creation and pairing
// time; that is a protocol change, not a change here.
package ws

import (
	"context"
	"io"
	"time"

	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/store"
)

// Store is the persistent-zone subset this package needs. It is declared here,
// at the consumer, rather than imported as a whole (docs/conventions.md §7).
type Store interface {
	CreateGroup(ctx context.Context, token string, dev store.NewDevice) (store.GroupCreation, error)
	GetGroup(ctx context.Context, groupID string) (store.Group, error)
	AddDevice(ctx context.Context, groupID string, dev store.NewDevice) (store.Device, error)
	ListDevices(ctx context.Context, groupID string) ([]store.Device, error)
	GetDevice(ctx context.Context, deviceID string) (store.Device, error)
	GetWrappedKey(ctx context.Context, deviceID string) (store.WrappedKey, error)
	SetWrappedKey(ctx context.Context, deviceID string, key store.WrappedKey) error
	RemoveDevice(ctx context.Context, groupID, deviceID string) error
	ApplyRekey(ctx context.Context, groupID, revokedDeviceID string, expectedEpoch uint64, keys []store.RekeyKey) (store.Group, error)
	CreatePairing(ctx context.Context, groupID, inviterDeviceID string, ttl time.Duration) (store.Pairing, error)
	GetPairing(ctx context.Context, token string) (store.Pairing, error)
	ConsumePairing(ctx context.Context, token, joinerDeviceID string) (store.Pairing, error)
	DeletePairing(ctx context.Context, token string) error
	TouchLastSeen(ctx context.Context, deviceID string) error
}

// Entries is the entry read/write subset this package needs.
type Entries interface {
	Put(ctx context.Context, groupID string, epoch uint64, entryID string, declaredSize int64, body io.Reader) (entries.Meta, error)
	Latest(ctx context.Context, groupID string) (entries.Meta, []byte, error)
	History(ctx context.Context, groupID string, limit int, before time.Time) ([]entries.Meta, time.Time, error)
	Fetch(ctx context.Context, groupID, entryID string) (entries.Meta, []byte, error)
	MaxBytes() int64
}
