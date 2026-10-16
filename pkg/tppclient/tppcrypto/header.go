package tppcrypto

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// Entry header (spec/crypto.md §5.5): a fixed-size, separately encrypted
// summary of an entry — content type, filename, plaintext body length — that
// the server stores beside the entry and returns in history, so a client can
// show what an entry is without fetching it. The server sees 297 opaque bytes
// for every entry, whatever it holds.

const (
	infoHeader = "tpp/v1/header"
	aadHeader  = "tpp/v1/header-aad"
	headerV1   = 0x01

	// HeaderSize is the fixed length of an encoded header.
	HeaderSize = 256
	// HeaderContainerSize is the fixed length of a sealed header.
	HeaderContainerSize = 1 + NonceSize + HeaderSize + TagSize // 297

	// headerMaxContentType is the longest content type a header carries; a
	// longer one is written as fallbackContentType.
	headerMaxContentType = 128
	fallbackContentType  = "application/octet-stream"
	headerFixed          = 1 + 2 + 2 + 8 // version, two length prefixes, body length
)

// Header is what a client may know about an entry before fetching it. It is
// advisory: the frame inside the entry stays the authority.
type Header struct {
	ContentType string
	Filename    string
	BodyLen     uint64
}

// EncodeHeader serializes a header into exactly HeaderSize bytes. A content
// type over 128 bytes is replaced by application/octet-stream, and the
// filename is cut, at a UTF-8 boundary, to what fits.
func EncodeHeader(h Header) ([]byte, error) {
	ct := h.ContentType
	if ct == "" {
		return nil, fmt.Errorf("tppcrypto: encode header: %w: empty content type", ErrMalformed)
	}
	if len(ct) > headerMaxContentType {
		ct = fallbackContentType
	}
	fn := truncateUTF8(h.Filename, HeaderSize-headerFixed-len(ct))
	out := make([]byte, 0, HeaderSize)
	out = append(out, headerV1)
	out = binary.BigEndian.AppendUint16(out, uint16(len(ct))) //nolint:gosec // at most 128
	out = append(out, ct...)
	out = binary.BigEndian.AppendUint16(out, uint16(len(fn))) //nolint:gosec // at most 243
	out = append(out, fn...)
	out = binary.BigEndian.AppendUint64(out, h.BodyLen)
	return append(out, make([]byte, HeaderSize-len(out))...), nil
}

// truncateUTF8 cuts s to at most n bytes without splitting a code point.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// DecodeHeader parses an encoded header. It is canonical: exactly HeaderSize
// bytes, a known version, a non-empty content type, lengths that fit, and
// zero padding.
func DecodeHeader(b []byte) (Header, error) {
	if len(b) != HeaderSize {
		return Header{}, fmt.Errorf("tppcrypto: decode header: %w: %d bytes, want %d", ErrMalformed, len(b), HeaderSize)
	}
	r := &frameReader{b: b}
	v, err := r.u8()
	if err != nil {
		return Header{}, fmt.Errorf("tppcrypto: decode header: %w", err)
	}
	if v != headerV1 {
		return Header{}, fmt.Errorf("tppcrypto: decode header: %w", ErrVersion)
	}
	var h Header
	if h.ContentType, err = r.str16(); err != nil {
		return Header{}, fmt.Errorf("tppcrypto: decode header content type: %w", err)
	}
	if h.ContentType == "" {
		return Header{}, fmt.Errorf("tppcrypto: decode header: %w: empty content type", ErrMalformed)
	}
	if h.Filename, err = r.str16(); err != nil {
		return Header{}, fmt.Errorf("tppcrypto: decode header filename: %w", err)
	}
	if h.BodyLen, err = r.u64(); err != nil {
		return Header{}, fmt.Errorf("tppcrypto: decode header body length: %w", err)
	}
	for _, c := range b[r.i:] {
		if c != 0 {
			return Header{}, fmt.Errorf("tppcrypto: decode header: %w: non-zero padding", ErrMalformed)
		}
	}
	return h, nil
}

// HeaderInfo returns the HKDF info string for a header key:
//
//	"tpp/v1/header" || u64be(epoch)
func HeaderInfo(epoch uint64) []byte {
	out := make([]byte, 0, len(infoHeader)+8)
	out = append(out, infoHeader...)
	return binary.BigEndian.AppendUint64(out, epoch)
}

// HeaderAAD returns the AEAD associated data for a header:
//
//	"tpp/v1/header-aad" || u8(version) || u64be(epoch) || u32be(len(id)) || id
func HeaderAAD(epoch uint64, entryID string) []byte {
	out := make([]byte, 0, len(aadHeader)+1+8+4+len(entryID))
	out = append(out, aadHeader...)
	out = append(out, Version)
	out = binary.BigEndian.AppendUint64(out, epoch)
	out = binary.BigEndian.AppendUint32(out, uint32(len(entryID))) //nolint:gosec // entry ids are short
	return append(out, entryID...)
}

// HeaderKey derives the per-header key. The header has its own nonce, so its
// key is never the entry's content key.
func HeaderKey(groupKey, nonce []byte, epoch uint64) ([]byte, error) {
	if len(groupKey) != GroupKeySize {
		return nil, fmt.Errorf("tppcrypto: derive header key: %w: group key is %d bytes", ErrMalformed, len(groupKey))
	}
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("tppcrypto: derive header key: %w: nonce is %d bytes", ErrMalformed, len(nonce))
	}
	key, err := hkdf.Key(sha256.New, groupKey, nonce, string(HeaderInfo(epoch)), ContentKeySize)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: derive header key: %w", err)
	}
	return key, nil
}

// SealHeader encrypts an encoded header:
//
//	u8(version) || header_nonce[24] || ciphertext[256] || tag[16]
func SealHeader(groupKey, nonce []byte, epoch uint64, entryID string, encoded []byte) ([]byte, error) {
	if len(encoded) != HeaderSize {
		return nil, fmt.Errorf("tppcrypto: seal header: %w: %d bytes, want %d", ErrMalformed, len(encoded), HeaderSize)
	}
	key, err := HeaderKey(groupKey, nonce, epoch)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: seal header: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: seal header: %w", err)
	}
	out := make([]byte, 0, HeaderContainerSize)
	out = append(out, Version)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, encoded, HeaderAAD(epoch, entryID)), nil
}

// OpenHeader reverses SealHeader and returns the encoded header.
func OpenHeader(groupKey, container []byte, epoch uint64, entryID string) ([]byte, error) {
	if len(container) != HeaderContainerSize {
		return nil, fmt.Errorf("tppcrypto: open header: %w: %d bytes, want %d", ErrMalformed, len(container), HeaderContainerSize)
	}
	if container[0] != Version {
		return nil, fmt.Errorf("tppcrypto: open header: %w", ErrVersion)
	}
	nonce := container[1 : 1+NonceSize]
	key, err := HeaderKey(groupKey, nonce, epoch)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open header: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open header: %w", err)
	}
	encoded, err := aead.Open(nil, nonce, container[1+NonceSize:], HeaderAAD(epoch, entryID))
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open header: %w", ErrAuth)
	}
	return encoded, nil
}

// SealEntryHeader encodes and seals h under a fresh nonce.
func SealEntryHeader(groupKey []byte, epoch uint64, entryID string, h Header) ([]byte, error) {
	encoded, err := EncodeHeader(h)
	if err != nil {
		return nil, err
	}
	nonce, err := GenerateNonce()
	if err != nil {
		return nil, err
	}
	return SealHeader(groupKey, nonce, epoch, entryID, encoded)
}

// OpenEntryHeader opens and decodes a sealed header.
func OpenEntryHeader(groupKey, container []byte, epoch uint64, entryID string) (Header, error) {
	encoded, err := OpenHeader(groupKey, container, epoch, entryID)
	if err != nil {
		return Header{}, err
	}
	return DecodeHeader(encoded)
}
