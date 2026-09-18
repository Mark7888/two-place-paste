// Package store is the Redis persistent zone: groups, devices, public keys,
// wrapped group keys, epochs, creation tokens and pairings (SPEC §4.2).
//
// Everything written here lives in the zone that must survive a restart. Only
// pairings carry a TTL; nothing else does. The deployment therefore has to run
// Redis with AOF persistence and `maxmemory-policy volatile-lru`: under
// `allkeys-lru` Redis would evict these records under memory pressure and
// silently destroy groups and pairings (SPEC §4.2). Every write below repeats
// that assumption where it matters.
//
// Clipboard entries are NOT here — they are the ephemeral zone and live in
// internal/entries.
package store

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"
)

// Sentinel errors. Callers compare with errors.Is, never on strings
// (docs/conventions.md §1).
var (
	// ErrNotFound is returned when a group, device, token or pairing does not
	// exist.
	ErrNotFound = errors.New("store: not found")

	// ErrTokenConsumed is returned when a creation token has already produced a
	// group. One token, one group, no reuse (SPEC §3.1).
	ErrTokenConsumed = errors.New("store: token already consumed")

	// ErrPairingConsumed is returned when a pairing token has already been used
	// by a joining device. Pairing tokens are single-use (SPEC §3.2).
	ErrPairingConsumed = errors.New("store: pairing already consumed")

	// ErrOfferConsumed is returned when a pairing offer has already admitted a
	// device. One offer pairs exactly one device: two members racing to accept
	// the same code must produce one member, not two.
	ErrOfferConsumed = errors.New("store: pairing offer already consumed")

	// ErrOfferKeyMismatch is returned when an accept names a public key that is
	// not the one the offer was minted with. The comparison is byte for byte,
	// and it is what keeps the relay out of the trust path: a relay that
	// substituted a key would have to make it match an offer it did not create.
	ErrOfferKeyMismatch = errors.New("store: pairing offer public key mismatch")

	// ErrEpochConflict is returned when a rekey's expected epoch is not the
	// group's current one: another device rekeyed first (SPEC §3.3 step 4).
	ErrEpochConflict = errors.New("store: epoch conflict")

	// ErrInvalidRekey is returned when a rekey does not carry exactly one
	// wrapped key per remaining device.
	ErrInvalidRekey = errors.New("store: rekey does not cover every remaining device")
)

// Token is an admin-issued creation token (SPEC §3.1, §4.4).
type Token struct {
	// Value is the token itself. It appears in the URL the admin hands the
	// user and, once consumed, becomes the group identifier.
	Value string

	// Name is the admin's label for the token.
	Name string

	CreatedAt time.Time

	// Used reports whether the token has produced a group.
	Used bool

	// GroupID is set once Used is true. It always equals Value; it is stored
	// explicitly so the admin listing does not have to know that.
	GroupID string
}

// Group is one clipboard group (SPEC §1.3).
type Group struct {
	ID string

	// Epoch is the current group key generation, starting at 1 and incremented
	// by every rekey (SPEC §3.3).
	Epoch uint64

	CreatedAt time.Time
}

// Device is one installation of a client (SPEC §4.2).
type Device struct {
	ID      string
	GroupID string
	Name    string

	// PublicKey is the device's raw public key. The layout is pinned by
	// /spec/crypto.md; this package treats it as opaque bytes.
	PublicKey []byte

	CreatedAt time.Time

	// LastSeen is the zero time until the device first connects.
	LastSeen time.Time
}

// NewDevice describes a device being added to a group.
type NewDevice struct {
	Name      string
	PublicKey []byte

	// WrappedGroupKey is the current group key wrapped to PublicKey. It may be
	// nil for a device that joins through pairing, whose wrapped key arrives
	// from the inviter one step later (SPEC §3.2 step 4).
	WrappedGroupKey []byte
}

// WrappedKey is a group key encrypted to one device's public key, together
// with the epoch it belongs to. The server can never unwrap it.
type WrappedKey struct {
	Epoch uint64
	Key   []byte
}

// RekeyKey is one entry of a rekey payload: the new group key wrapped to one
// remaining device (SPEC §3.3 step 4).
type RekeyKey struct {
	DeviceID string
	Key      []byte
}

// GroupCreation is the result of turning a creation token into a group.
type GroupCreation struct {
	Group  Group
	Device Device
}

// Pairing is a short-lived invitation for a device to join a group
// (SPEC §3.2).
type Pairing struct {
	Token           string
	GroupID         string
	InviterDeviceID string

	// JoinerDeviceID is empty until a device has consumed the pairing.
	JoinerDeviceID string

	CreatedAt time.Time
	ExpiresAt time.Time
}

// PairingOffer is a short-lived offer minted by a device that holds no group
// key, for a member to accept (docs/plans/joiner-emitted-pairing.md).
//
// It is the mirror image of Pairing: there the member mints and the joiner
// consumes, here the joiner mints and the member consumes. The joiner's public
// key is stored at mint time precisely so that the accept can be checked
// against it.
type PairingOffer struct {
	Code string

	// DeviceName is what the accepting member's confirmation dialog shows. It
	// is display text from a device that is not yet trusted; PublicKey is what
	// identifies it.
	DeviceName string

	// PublicKey is the offering device's public key, exactly as it was minted.
	// An accept naming anything else is refused.
	PublicKey []byte

	// GroupID and DeviceID are empty until a member has accepted the offer.
	// They name the group it was admitted to and the device record created for
	// it, which is what the joiner's PairingComplete is built from.
	GroupID  string
	DeviceID string

	CreatedAt time.Time
	ExpiresAt time.Time
}

// Store is the persistent zone behind an interface so that handlers can be
// tested without Redis. Implementations must be safe for concurrent use.
type Store interface {
	// CreateToken mints a creation token with the given admin-chosen name.
	CreateToken(ctx context.Context, name string) (Token, error)

	// ListTokens returns every creation token, newest first. This is the
	// admin UI's only listing (SPEC §4.4).
	ListTokens(ctx context.Context) ([]Token, error)

	// GetToken returns one token by value.
	GetToken(ctx context.Context, value string) (Token, error)

	// ConsumeToken atomically marks a token used and creates the group it
	// names, at epoch 1. It returns ErrTokenConsumed if the token has already
	// produced a group and ErrNotFound if it does not exist. Concurrent calls
	// with the same token produce exactly one group (SPEC §3.1 step 5).
	ConsumeToken(ctx context.Context, token string) (Group, error)

	// CreateGroup is ConsumeToken plus the creating device, applied as one
	// atomic operation so a failure can never leave a consumed token pointing
	// at a group nobody can reach (SPEC §3.1 step 4).
	CreateGroup(ctx context.Context, token string, dev NewDevice) (GroupCreation, error)

	// GetGroup returns the group record, including its current epoch.
	GetGroup(ctx context.Context, groupID string) (Group, error)

	// AddDevice registers a device in an existing group. dev.WrappedGroupKey
	// may be nil; the pairing flow supplies it separately.
	AddDevice(ctx context.Context, groupID string, dev NewDevice) (Device, error)

	// ListDevices returns the group's roster, oldest first.
	ListDevices(ctx context.Context, groupID string) ([]Device, error)

	// GetDevice returns one device. It is the connection-authentication
	// lookup, so it must not be expensive.
	GetDevice(ctx context.Context, deviceID string) (Device, error)

	// GetWrappedKey returns the device's copy of the group key. A device that
	// joined through pairing has none until the inviter uploads it.
	GetWrappedKey(ctx context.Context, deviceID string) (WrappedKey, error)

	// SetWrappedKey stores a device's wrapped group key at the given epoch.
	// Used by the pairing hand-off (SPEC §3.2 step 4); a revocation uses
	// ApplyRekey instead, which is atomic across the whole group.
	SetWrappedKey(ctx context.Context, deviceID string, key WrappedKey) error

	// RemoveDevice deletes a device and its wrapped key. It does not touch the
	// epoch: removing a device without rekeying is only legal for a pairing
	// that failed before it completed.
	RemoveDevice(ctx context.Context, groupID, deviceID string) error

	// ApplyRekey writes every wrapped key, deletes the revoked device and
	// increments the epoch, all-or-nothing (SPEC §3.3 step 4). It returns
	// ErrEpochConflict when expectedEpoch is stale and ErrInvalidRekey when
	// keys do not cover exactly the remaining devices. A partial apply is a
	// bug, not a degraded mode.
	ApplyRekey(ctx context.Context, groupID, revokedDeviceID string, expectedEpoch uint64, keys []RekeyKey) (Group, error)

	// CreatePairing mints a single-use pairing token valid for ttl
	// (SPEC §3.2 step 1).
	CreatePairing(ctx context.Context, groupID, inviterDeviceID string, ttl time.Duration) (Pairing, error)

	// GetPairing returns a pairing without consuming it.
	GetPairing(ctx context.Context, token string) (Pairing, error)

	// ConsumePairing atomically binds a pairing to the joining device. A
	// second caller gets ErrPairingConsumed: pairing tokens are single-use
	// (SPEC §3.2).
	ConsumePairing(ctx context.Context, token, joinerDeviceID string) (Pairing, error)

	// DeletePairing removes a pairing once the hand-off has completed. It is
	// best-effort: the record carries a TTL regardless.
	DeletePairing(ctx context.Context, token string) error

	// CreateOffer mints a single-use pairing offer valid for ttl, on behalf of
	// a device that is in no group yet.
	CreateOffer(ctx context.Context, name string, publicKey []byte, ttl time.Duration) (PairingOffer, error)

	// GetOffer returns an offer without consuming it.
	GetOffer(ctx context.Context, code string) (PairingOffer, error)

	// AcceptOffer atomically checks that the offer exists, is unconsumed and
	// unexpired and was minted with publicKey, creates the joining device in
	// groupID with the given wrapped key at the group's current epoch, and
	// marks the offer consumed. Either all of that happens or none of it does:
	// two members racing to accept one offer must admit exactly one device.
	//
	// It returns ErrNotFound when the offer or the group is gone,
	// ErrOfferConsumed when another accept won, and ErrOfferKeyMismatch when
	// publicKey is not the offer's own.
	AcceptOffer(ctx context.Context, code, groupID string, publicKey, wrappedGroupKey []byte) (PairingOffer, Device, error)

	// DeleteOffer removes an offer. Best-effort: the record carries a TTL.
	DeleteOffer(ctx context.Context, code string) error

	// TouchLastSeen records that a device connected.
	TouchLastSeen(ctx context.Context, deviceID string) error
}

// idBytes is the length of every server-generated identifier and token. 256
// bits of crypto/rand output: device identifiers double as the bearer
// credential a connection presents (see internal/ws), so they must be
// unguessable, not merely unique.
const idBytes = 32

// newID returns a fresh URL-safe random identifier.
func newID() string {
	var b [idBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read never fails on any supported platform; a failure
		// here is a broken machine, not a request-path condition
		// (docs/conventions.md §4).
		panic("store: crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
