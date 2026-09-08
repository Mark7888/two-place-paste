// Reference implementation of the TwoPlacePaste crypto profile v1.
//
// This file is the executable companion to spec/crypto.md. It exists to
// generate the cross-language test vectors in spec/vectors and to prove the
// document is implementable exactly as written; it is not imported by any
// product module. Where this code and spec/crypto.md disagree, the document
// is authoritative and this file is a bug.
package main

import (
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// Wire and framing constants (spec/crypto.md §2).
const (
	// Version is the single-byte version prefix carried by every TPP
	// ciphertext container and bound into its AAD.
	Version = 0x01

	// GroupKeySize is the length of a group key in bytes.
	GroupKeySize = 32
	// ContentKeySize is the length of a derived per-entry content key.
	ContentKeySize = 32
	// NonceSize is the XChaCha20-Poly1305 nonce length.
	NonceSize = chacha20poly1305.NonceSizeX
	// TagSize is the Poly1305 authentication tag length.
	TagSize = chacha20poly1305.Overhead
	// X25519KeySize is the length of an X25519 private or public key.
	X25519KeySize = 32
)

// Domain separation strings (spec/crypto.md §2.3). Every one of these is
// ASCII, is used verbatim with no terminator, and is never reused across two
// distinct purposes.
const (
	infoEntry   = "tpp/v1/entry"     // HKDF info prefix, content key
	infoWrap    = "tpp/v1/wrap"      // HKDF info prefix, key wrapping
	aadEntry    = "tpp/v1/entry-aad" // AEAD AAD prefix, entry encryption
	aadWrap     = "tpp/v1/wrap-aad"  // AEAD AAD prefix, key wrapping
	vectorSeed  = "tpp/v1/vectors"   // deterministic material for the vectors
	frameV1     = 0x01               // entry plaintext frame version
	maxFrameStr = 0xFFFF             // u16 length prefix ceiling
)

// Errors returned by the reference implementation. Client implementations are
// not required to reproduce these values, only to fail.
var (
	ErrVersion    = errors.New("unsupported version")
	ErrMalformed  = errors.New("malformed encoding")
	ErrAuth       = errors.New("authentication failed")
	ErrPadNonZero = errors.New("pad_len must be zero in v1")
	ErrTrailing   = errors.New("trailing bytes after frame")
)

// ---------------------------------------------------------------------------
// §3 Device keys
// ---------------------------------------------------------------------------

// PublicKey returns the X25519 public key for a 32-byte private key.
func PublicKey(priv []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("parse x25519 private key: %w", err)
	}
	return k.PublicKey().Bytes(), nil
}

// SharedSecret computes the raw X25519 shared secret. It rejects the all-zero
// output produced by low-order public keys, as RFC 7748 §6.1 requires.
func SharedSecret(priv, pub []byte) ([]byte, error) {
	sk, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("parse x25519 private key: %w", err)
	}
	pk, err := ecdh.X25519().NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("parse x25519 public key: %w", err)
	}
	secret, err := sk.ECDH(pk)
	if err != nil {
		return nil, fmt.Errorf("x25519 shared secret: %w", err)
	}
	return secret, nil
}

// ---------------------------------------------------------------------------
// §4 Group key wrapping
// ---------------------------------------------------------------------------

// WrapInfo returns the HKDF info string for a wrap operation:
//
//	"tpp/v1/wrap" || ephemeral_public || recipient_public
func WrapInfo(ephemeralPub, recipientPub []byte) []byte {
	out := make([]byte, 0, len(infoWrap)+2*X25519KeySize)
	out = append(out, infoWrap...)
	out = append(out, ephemeralPub...)
	out = append(out, recipientPub...)
	return out
}

// WrapAAD returns the AEAD associated data for a wrap operation:
//
//	"tpp/v1/wrap-aad" || u8(version) || u64be(epoch)
func WrapAAD(epoch uint64) []byte {
	out := make([]byte, 0, len(aadWrap)+1+8)
	out = append(out, aadWrap...)
	out = append(out, Version)
	return binary.BigEndian.AppendUint64(out, epoch)
}

// WrapKey derives the single-use AEAD key protecting a wrapped group key.
func WrapKey(shared, ephemeralPub, recipientPub []byte) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, shared, nil, string(WrapInfo(ephemeralPub, recipientPub)), ContentKeySize)
	if err != nil {
		return nil, fmt.Errorf("derive wrap key: %w", err)
	}
	return key, nil
}

// Wrap encrypts a group key to a recipient device public key, using the
// supplied ephemeral private key. The result is
//
//	u8(version) || ephemeral_public[32] || ciphertext[32] || tag[16]
//
// The AEAD nonce is 24 zero bytes: the wrap key is derived from a fresh
// ephemeral scalar and is therefore used for exactly one message.
func Wrap(groupKey, recipientPub, ephemeralPriv []byte, epoch uint64) ([]byte, error) {
	if len(groupKey) != GroupKeySize {
		return nil, fmt.Errorf("wrap group key: %w", ErrMalformed)
	}
	ephemeralPub, err := PublicKey(ephemeralPriv)
	if err != nil {
		return nil, fmt.Errorf("wrap: %w", err)
	}
	shared, err := SharedSecret(ephemeralPriv, recipientPub)
	if err != nil {
		return nil, fmt.Errorf("wrap: %w", err)
	}
	key, err := WrapKey(shared, ephemeralPub, recipientPub)
	if err != nil {
		return nil, fmt.Errorf("wrap: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("wrap: %w", err)
	}
	out := make([]byte, 0, 1+X25519KeySize+GroupKeySize+TagSize)
	out = append(out, Version)
	out = append(out, ephemeralPub...)
	return aead.Seal(out, make([]byte, NonceSize), groupKey, WrapAAD(epoch)), nil
}

// Unwrap reverses Wrap using the recipient's private key.
func Unwrap(wrapped, recipientPriv []byte, epoch uint64) ([]byte, error) {
	if len(wrapped) != 1+X25519KeySize+GroupKeySize+TagSize {
		return nil, fmt.Errorf("unwrap: %w", ErrMalformed)
	}
	if wrapped[0] != Version {
		return nil, fmt.Errorf("unwrap: %w", ErrVersion)
	}
	ephemeralPub := wrapped[1 : 1+X25519KeySize]
	recipientPub, err := PublicKey(recipientPriv)
	if err != nil {
		return nil, fmt.Errorf("unwrap: %w", err)
	}
	shared, err := SharedSecret(recipientPriv, ephemeralPub)
	if err != nil {
		return nil, fmt.Errorf("unwrap: %w", err)
	}
	key, err := WrapKey(shared, ephemeralPub, recipientPub)
	if err != nil {
		return nil, fmt.Errorf("unwrap: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("unwrap: %w", err)
	}
	groupKey, err := aead.Open(nil, make([]byte, NonceSize), wrapped[1+X25519KeySize:], WrapAAD(epoch))
	if err != nil {
		return nil, fmt.Errorf("unwrap: %w", ErrAuth)
	}
	return groupKey, nil
}

// ---------------------------------------------------------------------------
// §5 Entry encryption
// ---------------------------------------------------------------------------

// EntryInfo returns the HKDF info string for a content key derivation:
//
//	"tpp/v1/entry" || u64be(epoch)
func EntryInfo(epoch uint64) []byte {
	out := make([]byte, 0, len(infoEntry)+8)
	out = append(out, infoEntry...)
	return binary.BigEndian.AppendUint64(out, epoch)
}

// EntryAAD returns the AEAD associated data for an entry:
//
//	"tpp/v1/entry-aad" || u8(version) || u64be(epoch) || u32be(len(id)) || id
//
// entry_id is bound as its raw UTF-8 bytes with an explicit length prefix, so
// no two distinct (epoch, entry_id) pairs can produce the same AAD.
func EntryAAD(epoch uint64, entryID string) []byte {
	out := make([]byte, 0, len(aadEntry)+1+8+4+len(entryID))
	out = append(out, aadEntry...)
	out = append(out, Version)
	out = binary.BigEndian.AppendUint64(out, epoch)
	out = binary.BigEndian.AppendUint32(out, uint32(len(entryID)))
	return append(out, entryID...)
}

// ContentKey derives the per-entry content key. The group key is never used
// directly as a content key (SPEC §2.2).
func ContentKey(groupKey, nonce []byte, epoch uint64) ([]byte, error) {
	if len(groupKey) != GroupKeySize {
		return nil, fmt.Errorf("derive content key: %w", ErrMalformed)
	}
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("derive content key: %w", ErrMalformed)
	}
	key, err := hkdf.Key(sha256.New, groupKey, nonce, string(EntryInfo(epoch)), ContentKeySize)
	if err != nil {
		return nil, fmt.Errorf("derive content key: %w", err)
	}
	return key, nil
}

// SealEntry encrypts a serialized plaintext frame into an entry container:
//
//	u8(version) || nonce[24] || ciphertext || tag[16]
func SealEntry(groupKey, nonce []byte, epoch uint64, entryID string, frame []byte) ([]byte, error) {
	key, err := ContentKey(groupKey, nonce, epoch)
	if err != nil {
		return nil, fmt.Errorf("seal entry: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("seal entry: %w", err)
	}
	out := make([]byte, 0, 1+NonceSize+len(frame)+TagSize)
	out = append(out, Version)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, frame, EntryAAD(epoch, entryID)), nil
}

// OpenEntry reverses SealEntry, returning the serialized plaintext frame.
func OpenEntry(groupKey, container []byte, epoch uint64, entryID string) ([]byte, error) {
	if len(container) < 1+NonceSize+TagSize {
		return nil, fmt.Errorf("open entry: %w", ErrMalformed)
	}
	if container[0] != Version {
		return nil, fmt.Errorf("open entry: %w", ErrVersion)
	}
	nonce := container[1 : 1+NonceSize]
	key, err := ContentKey(groupKey, nonce, epoch)
	if err != nil {
		return nil, fmt.Errorf("open entry: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("open entry: %w", err)
	}
	frame, err := aead.Open(nil, nonce, container[1+NonceSize:], EntryAAD(epoch, entryID))
	if err != nil {
		return nil, fmt.Errorf("open entry: %w", ErrAuth)
	}
	return frame, nil
}

// ---------------------------------------------------------------------------
// §6 Plaintext frame
// ---------------------------------------------------------------------------

// Frame is the plaintext structure that is serialized and then encrypted. It
// carries the content type, so the server never learns it (SPEC §2.3).
type Frame struct {
	ContentType   string
	Filename      string // empty means absent
	CreatedAtUnix uint64 // milliseconds since the epoch, UTC
	PadLen        uint32 // MUST be 0 in v1
	Body          []byte
}

// Encode serializes a frame (spec/crypto.md §6).
func (f Frame) Encode() ([]byte, error) {
	if len(f.ContentType) == 0 || len(f.ContentType) > maxFrameStr {
		return nil, fmt.Errorf("encode frame content type: %w", ErrMalformed)
	}
	if len(f.Filename) > maxFrameStr {
		return nil, fmt.Errorf("encode frame filename: %w", ErrMalformed)
	}
	if f.PadLen != 0 {
		return nil, fmt.Errorf("encode frame: %w", ErrPadNonZero)
	}
	out := make([]byte, 0, 1+2+len(f.ContentType)+2+len(f.Filename)+8+4+4+len(f.Body))
	out = append(out, frameV1)
	out = binary.BigEndian.AppendUint16(out, uint16(len(f.ContentType)))
	out = append(out, f.ContentType...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(f.Filename)))
	out = append(out, f.Filename...)
	out = binary.BigEndian.AppendUint64(out, f.CreatedAtUnix)
	out = binary.BigEndian.AppendUint32(out, f.PadLen)
	out = binary.BigEndian.AppendUint32(out, uint32(len(f.Body)))
	out = append(out, f.Body...)
	return out, nil
}

// DecodeFrame parses a serialized frame. It rejects trailing bytes, so the
// encoding is canonical: one frame has exactly one valid serialization.
func DecodeFrame(b []byte) (Frame, error) {
	var f Frame
	r := reader{b: b}
	ver, err := r.u8()
	if err != nil {
		return f, fmt.Errorf("decode frame version: %w", err)
	}
	if ver != frameV1 {
		return f, fmt.Errorf("decode frame: %w", ErrVersion)
	}
	if f.ContentType, err = r.str16(); err != nil {
		return f, fmt.Errorf("decode frame content type: %w", err)
	}
	if len(f.ContentType) == 0 {
		return f, fmt.Errorf("decode frame content type: %w", ErrMalformed)
	}
	if f.Filename, err = r.str16(); err != nil {
		return f, fmt.Errorf("decode frame filename: %w", err)
	}
	if f.CreatedAtUnix, err = r.u64(); err != nil {
		return f, fmt.Errorf("decode frame created_at: %w", err)
	}
	if f.PadLen, err = r.u32(); err != nil {
		return f, fmt.Errorf("decode frame pad_len: %w", err)
	}
	if f.PadLen != 0 {
		return f, fmt.Errorf("decode frame: %w", ErrPadNonZero)
	}
	bodyLen, err := r.u32()
	if err != nil {
		return f, fmt.Errorf("decode frame body_len: %w", err)
	}
	if f.Body, err = r.bytes(int(bodyLen)); err != nil {
		return f, fmt.Errorf("decode frame body: %w", err)
	}
	if r.i != len(r.b) {
		return f, fmt.Errorf("decode frame: %w", ErrTrailing)
	}
	return f, nil
}

type reader struct {
	b []byte
	i int
}

func (r *reader) bytes(n int) ([]byte, error) {
	if n < 0 || len(r.b)-r.i < n {
		return nil, ErrMalformed
	}
	out := r.b[r.i : r.i+n]
	r.i += n
	return out, nil
}

func (r *reader) u8() (byte, error) {
	b, err := r.bytes(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *reader) u32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

func (r *reader) u64() (uint64, error) {
	b, err := r.bytes(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

func (r *reader) str16() (string, error) {
	b, err := r.bytes(2)
	if err != nil {
		return "", err
	}
	s, err := r.bytes(int(binary.BigEndian.Uint16(b)))
	if err != nil {
		return "", err
	}
	return string(s), nil
}

// ---------------------------------------------------------------------------
// Deterministic material for the vectors
// ---------------------------------------------------------------------------

// det returns n deterministic bytes for a label. Vector inputs are generated
// this way so that regenerating the corpus produces a byte-identical result;
// real clients draw the same values from crypto/rand.
func det(label string, n int) []byte {
	b, err := hkdf.Key(sha256.New, []byte(vectorSeed), nil, label, n)
	if err != nil {
		panic(fmt.Sprintf("deterministic material for %q: %v", label, err))
	}
	return b
}
