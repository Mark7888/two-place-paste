// Package tppcrypto implements the TwoPlacePaste crypto profile
// `tpp-crypto-v1` exactly as /spec/crypto.md defines it.
//
// It is the only place in the Go client where a key is derived or a byte is
// encrypted: the flow methods in the parent package call this package and hold
// no crypto of their own. Where this code and /spec/crypto.md disagree, the
// document is authoritative and this code is a bug — which is why every
// operation here is held to the vectors in /spec/vectors (see
// vectors_test.go).
//
// Three primitives and no more (spec §2.2): X25519, HKDF-SHA256 and
// XChaCha20-Poly1305.
package tppcrypto

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// Wire constants (spec/crypto.md §2, §4.3, §5.2).
const (
	// Version is the single-byte prefix carried by every container defined by
	// this profile and bound into its associated data (spec §2.4).
	Version = 0x01

	// GroupKeySize is the length of a group key.
	GroupKeySize = 32
	// ContentKeySize is the length of a derived per-entry content key.
	ContentKeySize = 32
	// NonceSize is the XChaCha20-Poly1305 nonce length, and therefore the
	// length of an entry nonce.
	NonceSize = chacha20poly1305.NonceSizeX
	// TagSize is the Poly1305 tag length.
	TagSize = chacha20poly1305.Overhead
	// KeySize is the length of an X25519 private or public key.
	KeySize = 32

	// WrappedKeySize is the fixed length of a wrap container (spec §4.3). A
	// blob of any other length is rejected without attempting decryption.
	WrappedKeySize = 1 + KeySize + GroupKeySize + TagSize

	// EntryOverhead is the smallest possible entry container: version, nonce
	// and tag, with an empty frame (spec §5.4 step 1).
	EntryOverhead = 1 + NonceSize + TagSize
)

// Domain-separation strings (spec §2.3). Each is used for exactly one purpose.
const (
	infoWrap  = "tpp/v1/wrap"
	aadWrap   = "tpp/v1/wrap-aad"
	infoEntry = "tpp/v1/entry"
	aadEntry  = "tpp/v1/entry-aad"
)

// Errors every implementation of this profile must produce for the inputs in
// /spec/vectors/failure.json. The vectors require a failure, not a particular
// error value; these exist so callers can tell corruption from a bad argument.
var (
	// ErrVersion is an unknown container version byte, rejected before any key
	// material is derived (spec §2.4).
	ErrVersion = errors.New("tppcrypto: unsupported container version")

	// ErrMalformed is a container or frame whose encoding is wrong: bad
	// length, truncated input, a length prefix past the end, trailing bytes.
	ErrMalformed = errors.New("tppcrypto: malformed encoding")

	// ErrAuth is an AEAD authentication failure: the wrong key, the wrong
	// recipient, the wrong epoch, the wrong entry id, or tampering. The four
	// are deliberately indistinguishable.
	ErrAuth = errors.New("tppcrypto: authentication failed")

	// ErrPadNonZero is a frame with a non-zero pad_len, which v1 reserves and
	// forbids so that padding can arrive later as v2 (spec §6).
	ErrPadNonZero = errors.New("tppcrypto: pad_len must be zero in v1")
)

// ---------------------------------------------------------------------------
// §3 Device keys
// ---------------------------------------------------------------------------

// GenerateKey returns a fresh X25519 private key: 32 bytes from the platform
// CSPRNG, unclamped, as spec §3 requires (clamping happens inside the scalar
// multiplication).
//
// The result never leaves the device: not to the server, not to another
// device, not into a log or an error (docs/conventions.md §1).
func GenerateKey() ([]byte, error) {
	priv := make([]byte, KeySize)
	if _, err := rand.Read(priv); err != nil {
		return nil, fmt.Errorf("tppcrypto: generate x25519 private key: %w", err)
	}
	return priv, nil
}

// PublicKey returns the X25519 public key for a private key.
func PublicKey(priv []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: parse x25519 private key: %w", ErrMalformed)
	}
	return k.PublicKey().Bytes(), nil
}

// SharedSecret computes the raw X25519 shared secret. An all-zero output is
// rejected by crypto/ecdh, as RFC 7748 §6.1 requires (spec §3).
func SharedSecret(priv, pub []byte) ([]byte, error) {
	sk, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: parse x25519 private key: %w", ErrMalformed)
	}
	pk, err := ecdh.X25519().NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: parse x25519 public key: %w", ErrMalformed)
	}
	secret, err := sk.ECDH(pk)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: x25519 shared secret: %w", ErrMalformed)
	}
	return secret, nil
}

// ---------------------------------------------------------------------------
// §4 Group keys
// ---------------------------------------------------------------------------

// GenerateGroupKey returns a fresh 32-byte group key (spec §4.1).
//
// A group key is generated in exactly two places: group creation, at epoch 1,
// and a rekey after revocation, at epoch+1. It is never derived from a
// previous epoch's key — a revocation must be a hard break, not a ratchet.
func GenerateGroupKey() ([]byte, error) {
	key := make([]byte, GroupKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("tppcrypto: generate group key: %w", err)
	}
	return key, nil
}

// WrapInfo is the HKDF info for a wrap operation (spec §4.2):
//
//	"tpp/v1/wrap" || ephemeral_public[32] || recipient_public[32]
func WrapInfo(ephemeralPub, recipientPub []byte) []byte {
	out := make([]byte, 0, len(infoWrap)+2*KeySize)
	out = append(out, infoWrap...)
	out = append(out, ephemeralPub...)
	return append(out, recipientPub...)
}

// WrapAAD is the associated data for a wrap operation (spec §4.2):
//
//	"tpp/v1/wrap-aad" || u8(version) || u64be(epoch)
func WrapAAD(epoch uint64) []byte {
	out := make([]byte, 0, len(aadWrap)+1+8)
	out = append(out, aadWrap...)
	out = append(out, Version)
	return binary.BigEndian.AppendUint64(out, epoch)
}

// WrapKey derives the single-use AEAD key that protects one wrapped group key.
func WrapKey(shared, ephemeralPub, recipientPub []byte) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, shared, nil, string(WrapInfo(ephemeralPub, recipientPub)), ContentKeySize)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: derive wrap key: %w", err)
	}
	return key, nil
}

// Wrap encrypts a group key to one recipient public key at one epoch,
// producing the 81-byte container of spec §4.3.
//
// It draws a fresh ephemeral scalar per call, which is what makes the all-zero
// AEAD nonce safe: the derived wrap key encrypts exactly one message. Wrapping
// to three devices means three calls and three ephemeral keys.
func Wrap(groupKey, recipientPub []byte, epoch uint64) ([]byte, error) {
	ephemeralPriv, err := GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: wrap: %w", err)
	}
	return wrapWith(groupKey, recipientPub, ephemeralPriv, epoch)
}

// wrapWith is Wrap with a caller-supplied ephemeral key. It is unexported on
// purpose: reusing an ephemeral key across recipients would reuse a wrap key
// under a zero nonce. Only the vector tests, which replay recorded ephemeral
// keys, may reach it.
func wrapWith(groupKey, recipientPub, ephemeralPriv []byte, epoch uint64) ([]byte, error) {
	if len(groupKey) != GroupKeySize {
		return nil, fmt.Errorf("tppcrypto: wrap: %w: group key is %d bytes", ErrMalformed, len(groupKey))
	}
	ephemeralPub, err := PublicKey(ephemeralPriv)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: wrap: %w", err)
	}
	shared, err := SharedSecret(ephemeralPriv, recipientPub)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: wrap: %w", err)
	}
	key, err := WrapKey(shared, ephemeralPub, recipientPub)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: wrap: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: wrap: %w", err)
	}
	out := make([]byte, 0, WrappedKeySize)
	out = append(out, Version)
	out = append(out, ephemeralPub...)
	return aead.Seal(out, make([]byte, NonceSize), groupKey, WrapAAD(epoch)), nil
}

// Unwrap reverses Wrap with the recipient's private key (spec §4.4).
//
// Step 4 of the spec derives the wrap key from the recipient's *own* public
// key, recomputed from the private key: no public key is read from the wire.
func Unwrap(wrapped, recipientPriv []byte, epoch uint64) ([]byte, error) {
	if len(wrapped) != WrappedKeySize {
		return nil, fmt.Errorf("tppcrypto: unwrap: %w: container is %d bytes, want %d",
			ErrMalformed, len(wrapped), WrappedKeySize)
	}
	if wrapped[0] != Version {
		return nil, fmt.Errorf("tppcrypto: unwrap: %w", ErrVersion)
	}
	ephemeralPub := wrapped[1 : 1+KeySize]
	recipientPub, err := PublicKey(recipientPriv)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: unwrap: %w", err)
	}
	shared, err := SharedSecret(recipientPriv, ephemeralPub)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: unwrap: %w", err)
	}
	key, err := WrapKey(shared, ephemeralPub, recipientPub)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: unwrap: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: unwrap: %w", err)
	}
	groupKey, err := aead.Open(nil, make([]byte, NonceSize), wrapped[1+KeySize:], WrapAAD(epoch))
	if err != nil {
		// Wrong recipient, wrong epoch and tampering are one answer.
		return nil, fmt.Errorf("tppcrypto: unwrap: %w", ErrAuth)
	}
	return groupKey, nil
}

// ---------------------------------------------------------------------------
// §5 Entry encryption
// ---------------------------------------------------------------------------

// EntryInfo is the HKDF info for a content key derivation (spec §5.1):
//
//	"tpp/v1/entry" || u64be(epoch)
func EntryInfo(epoch uint64) []byte {
	out := make([]byte, 0, len(infoEntry)+8)
	out = append(out, infoEntry...)
	return binary.BigEndian.AppendUint64(out, epoch)
}

// EntryAAD is the associated data for an entry (spec §5.3):
//
//	"tpp/v1/entry-aad" || u8(version) || u64be(epoch) || u32be(len(id)) || id
//
// The length prefix is what makes the encoding unambiguous. content_type is
// deliberately absent: it lives inside the encrypted frame, which authenticates
// it just as strongly and hides it from the server as well (spec §11.2).
func EntryAAD(epoch uint64, entryID string) []byte {
	out := make([]byte, 0, len(aadEntry)+1+8+4+len(entryID))
	out = append(out, aadEntry...)
	out = append(out, Version)
	out = binary.BigEndian.AppendUint64(out, epoch)
	out = binary.BigEndian.AppendUint32(out, uint32(len(entryID)))
	return append(out, entryID...)
}

// ContentKey derives the per-entry content key (spec §5.1). The nonce is the
// HKDF salt as well as the AEAD nonce, so every entry gets its own key and the
// group key is never used to encrypt anything directly.
func ContentKey(groupKey, nonce []byte, epoch uint64) ([]byte, error) {
	if len(groupKey) != GroupKeySize {
		return nil, fmt.Errorf("tppcrypto: derive content key: %w: group key is %d bytes", ErrMalformed, len(groupKey))
	}
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("tppcrypto: derive content key: %w: nonce is %d bytes", ErrMalformed, len(nonce))
	}
	key, err := hkdf.Key(sha256.New, groupKey, nonce, string(EntryInfo(epoch)), ContentKeySize)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: derive content key: %w", err)
	}
	return key, nil
}

// GenerateNonce returns a fresh 24-byte entry nonce from the CSPRNG. Counters,
// timestamps and plaintext hashes are forbidden (spec §5.1).
func GenerateNonce() ([]byte, error) {
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("tppcrypto: generate entry nonce: %w", err)
	}
	return nonce, nil
}

// SealEntry encrypts an encoded plaintext frame into the entry container of
// spec §5.2:
//
//	u8(version) || nonce[24] || ciphertext || tag[16]
//
// The container is the byte string the server stores, and the 10 MB cap is
// measured on it rather than on the plaintext.
func SealEntry(groupKey, nonce []byte, epoch uint64, entryID string, frame []byte) ([]byte, error) {
	key, err := ContentKey(groupKey, nonce, epoch)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: seal entry: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: seal entry: %w", err)
	}
	out := make([]byte, 0, 1+NonceSize+len(frame)+TagSize)
	out = append(out, Version)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, frame, EntryAAD(epoch, entryID)), nil
}

// OpenEntry reverses SealEntry and returns the encoded frame (spec §5.4).
//
// epoch is the epoch the server reports for the entry. A client must not try
// other epochs to make an entry decrypt (spec §7).
func OpenEntry(groupKey, container []byte, epoch uint64, entryID string) ([]byte, error) {
	if len(container) < EntryOverhead {
		return nil, fmt.Errorf("tppcrypto: open entry: %w: container is %d bytes, minimum is %d",
			ErrMalformed, len(container), EntryOverhead)
	}
	if container[0] != Version {
		return nil, fmt.Errorf("tppcrypto: open entry: %w", ErrVersion)
	}
	nonce := container[1 : 1+NonceSize]
	key, err := ContentKey(groupKey, nonce, epoch)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open entry: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open entry: %w", err)
	}
	frame, err := aead.Open(nil, nonce, container[1+NonceSize:], EntryAAD(epoch, entryID))
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open entry: %w", ErrAuth)
	}
	return frame, nil
}
