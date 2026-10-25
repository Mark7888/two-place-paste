package tppcrypto

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// Store (spec/crypto.md §7a). A store item outlives the epoch it was saved
// in, so nothing about it may depend on keeping an old group key:
//
//   - Each epoch has a store key, derived from its group key.
//   - A rekey uploads a link: the previous store key sealed under the new one.
//     From the current store key a client walks the links back to any epoch.
//   - The blob is an ordinary entry container (§5.2). Its content key and a
//     random metadata key are wrapped under the store key of the epoch the
//     item was saved at.
//   - The metadata record — name, description, tags, type, filename, size,
//     hash — is a fixed-size record sealed under the metadata key, so its
//     length tells the server nothing.

const (
	infoStore     = "tpp/v1/store"
	infoStoreLink = "tpp/v1/store-link"
	aadStoreLink  = "tpp/v1/store-link-aad"
	infoStoreWrap = "tpp/v1/store-wrap"
	aadStoreWrap  = "tpp/v1/store-wrap-aad"
	infoStoreMeta = "tpp/v1/store-meta"
	aadStoreMeta  = "tpp/v1/store-meta-aad"
	storeMetaV1   = 0x01

	// StoreKeySize is the length of a store key.
	StoreKeySize = 32
	// StoreLinkSize is the fixed length of a link (spec §7a.2).
	StoreLinkSize = 1 + StoreKeySize + TagSize // 49
	// MetaKeySize is the length of an item's metadata key.
	MetaKeySize = 32
	// WrappedItemKeysSize is the fixed length of an item's wrapped keys
	// (spec §7a.3).
	WrappedItemKeysSize = 1 + NonceSize + ContentKeySize + MetaKeySize + TagSize // 105
	// StoreMetaSize is the fixed length of an encoded metadata record.
	StoreMetaSize = 2048
	// StoreMetaContainerSize is the fixed length of a sealed record.
	StoreMetaContainerSize = 1 + NonceSize + StoreMetaSize + TagSize // 2089

	storeMaxFilename = 255
)

// Limits on what a user types (spec §7a.5), in bytes of UTF-8. A record over
// any of them is refused, not cut: a UI shows them as counters.
const (
	// StoreMaxName is the longest name. A name is never empty.
	StoreMaxName = 200
	// StoreMaxDescription is the longest description.
	StoreMaxDescription = 800
	// StoreMaxTags is the most tags an item carries.
	StoreMaxTags = 16
	// StoreMaxTag is the longest tag. A tag is never empty.
	StoreMaxTag = 32
)

// StoreKey derives the store key of an epoch from that epoch's group key
// (spec §7a.1):
//
//	HKDF(group_key, <empty>, "tpp/v1/store" || u64be(epoch), 32)
func StoreKey(groupKey []byte, epoch uint64) ([]byte, error) {
	if len(groupKey) != GroupKeySize {
		return nil, fmt.Errorf("tppcrypto: derive store key: %w: group key is %d bytes", ErrMalformed, len(groupKey))
	}
	key, err := hkdf.Key(sha256.New, groupKey, nil, string(StoreInfo(epoch)), StoreKeySize)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: derive store key: %w", err)
	}
	return key, nil
}

// StoreInfo is the HKDF info of a store key: "tpp/v1/store" || u64be(epoch).
func StoreInfo(epoch uint64) []byte {
	return binary.BigEndian.AppendUint64([]byte(infoStore), epoch)
}

// StoreLinkInfo is the HKDF info of the key sealing the link into epoch:
// "tpp/v1/store-link" || u64be(epoch).
func StoreLinkInfo(epoch uint64) []byte {
	return binary.BigEndian.AppendUint64([]byte(infoStoreLink), epoch)
}

// StoreLinkAAD binds both epochs a link joins:
//
//	"tpp/v1/store-link-aad" || u8(version) || u64be(epoch-1) || u64be(epoch)
func StoreLinkAAD(epoch uint64) []byte {
	out := append([]byte(aadStoreLink), Version)
	out = binary.BigEndian.AppendUint64(out, epoch-1)
	return binary.BigEndian.AppendUint64(out, epoch)
}

// StoreLinkKey derives the single-use key that seals the link into epoch.
func StoreLinkKey(storeKey []byte, epoch uint64) ([]byte, error) {
	if len(storeKey) != StoreKeySize {
		return nil, fmt.Errorf("tppcrypto: derive store link key: %w: store key is %d bytes", ErrMalformed, len(storeKey))
	}
	key, err := hkdf.Key(sha256.New, storeKey, nil, string(StoreLinkInfo(epoch)), 32)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: derive store link key: %w", err)
	}
	return key, nil
}

// SealStoreLink seals the store key of epoch-1 under the store key of epoch
// (spec §7a.2):
//
//	u8(version) || ciphertext[32] || tag[16]
//
// The nonce is 24 zero bytes: the link key is derived from the new store key
// and the epoch, and seals exactly one message.
func SealStoreLink(newStoreKey, oldStoreKey []byte, epoch uint64) ([]byte, error) {
	if epoch < 2 {
		return nil, fmt.Errorf("tppcrypto: seal store link: %w: epoch 1 has no link", ErrMalformed)
	}
	if len(oldStoreKey) != StoreKeySize {
		return nil, fmt.Errorf("tppcrypto: seal store link: %w: store key is %d bytes", ErrMalformed, len(oldStoreKey))
	}
	key, err := StoreLinkKey(newStoreKey, epoch)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: seal store link: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: seal store link: %w", err)
	}
	return aead.Seal([]byte{Version}, make([]byte, NonceSize), oldStoreKey, StoreLinkAAD(epoch)), nil
}

// OpenStoreLink reverses SealStoreLink: from the store key of epoch it returns
// the store key of epoch-1. Links only lead backwards; nothing here can
// produce a newer key than the caller holds.
func OpenStoreLink(storeKey, link []byte, epoch uint64) ([]byte, error) {
	if len(link) != StoreLinkSize {
		return nil, fmt.Errorf("tppcrypto: open store link: %w: %d bytes, want %d", ErrMalformed, len(link), StoreLinkSize)
	}
	if epoch < 2 {
		return nil, fmt.Errorf("tppcrypto: open store link: %w: epoch 1 has no link", ErrMalformed)
	}
	if link[0] != Version {
		return nil, fmt.Errorf("tppcrypto: open store link: %w", ErrVersion)
	}
	key, err := StoreLinkKey(storeKey, epoch)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open store link: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open store link: %w", err)
	}
	old, err := aead.Open(nil, make([]byte, NonceSize), link[1:], StoreLinkAAD(epoch))
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open store link: %w", ErrAuth)
	}
	return old, nil
}

// StoreWrapInfo is the HKDF info of an item wrap key:
// "tpp/v1/store-wrap" || u64be(epoch).
func StoreWrapInfo(epoch uint64) []byte {
	return binary.BigEndian.AppendUint64([]byte(infoStoreWrap), epoch)
}

// StoreWrapAAD binds an item's wrapped keys to the epoch and the item:
//
//	"tpp/v1/store-wrap-aad" || u8(version) || u64be(epoch) || u32be(len(id)) || id
func StoreWrapAAD(epoch uint64, itemID string) []byte {
	out := append([]byte(aadStoreWrap), Version)
	out = binary.BigEndian.AppendUint64(out, epoch)
	out = binary.BigEndian.AppendUint32(out, uint32(len(itemID))) //nolint:gosec // item ids are short
	return append(out, itemID...)
}

// StoreWrapKey derives the key that wraps one item's keys.
func StoreWrapKey(storeKey, nonce []byte, epoch uint64) ([]byte, error) {
	if len(storeKey) != StoreKeySize {
		return nil, fmt.Errorf("tppcrypto: derive store wrap key: %w: store key is %d bytes", ErrMalformed, len(storeKey))
	}
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("tppcrypto: derive store wrap key: %w: nonce is %d bytes", ErrMalformed, len(nonce))
	}
	key, err := hkdf.Key(sha256.New, storeKey, nonce, string(StoreWrapInfo(epoch)), 32)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: derive store wrap key: %w", err)
	}
	return key, nil
}

// WrapItemKeys wraps an item's content key and metadata key under the store
// key of the epoch it is saved at (spec §7a.3):
//
//	u8(version) || wrap_nonce[24] || ciphertext[64] || tag[16]
func WrapItemKeys(storeKey, nonce []byte, epoch uint64, itemID string, contentKey, metaKey []byte) ([]byte, error) {
	if len(contentKey) != ContentKeySize || len(metaKey) != MetaKeySize {
		return nil, fmt.Errorf("tppcrypto: wrap item keys: %w: key sizes", ErrMalformed)
	}
	key, err := StoreWrapKey(storeKey, nonce, epoch)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: wrap item keys: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: wrap item keys: %w", err)
	}
	keys := make([]byte, 0, ContentKeySize+MetaKeySize)
	keys = append(append(keys, contentKey...), metaKey...)
	out := make([]byte, 0, WrappedItemKeysSize)
	out = append(append(out, Version), nonce...)
	return aead.Seal(out, nonce, keys, StoreWrapAAD(epoch, itemID)), nil
}

// UnwrapItemKeys reverses WrapItemKeys and returns the content key and the
// metadata key. epoch is the epoch the item was wrapped at, as the server
// reports it; the caller walks the links to that epoch's store key first.
func UnwrapItemKeys(storeKey, wrapped []byte, epoch uint64, itemID string) (contentKey, metaKey []byte, err error) {
	if len(wrapped) != WrappedItemKeysSize {
		return nil, nil, fmt.Errorf("tppcrypto: unwrap item keys: %w: %d bytes, want %d",
			ErrMalformed, len(wrapped), WrappedItemKeysSize)
	}
	if wrapped[0] != Version {
		return nil, nil, fmt.Errorf("tppcrypto: unwrap item keys: %w", ErrVersion)
	}
	nonce := wrapped[1 : 1+NonceSize]
	key, err := StoreWrapKey(storeKey, nonce, epoch)
	if err != nil {
		return nil, nil, fmt.Errorf("tppcrypto: unwrap item keys: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, fmt.Errorf("tppcrypto: unwrap item keys: %w", err)
	}
	keys, err := aead.Open(nil, nonce, wrapped[1+NonceSize:], StoreWrapAAD(epoch, itemID))
	if err != nil {
		return nil, nil, fmt.Errorf("tppcrypto: unwrap item keys: %w", ErrAuth)
	}
	return keys[:ContentKeySize], keys[ContentKeySize:], nil
}

// StoreMeta is an item's metadata record: what the Store tab lists, searches
// and filters on, none of which the server may see.
type StoreMeta struct {
	Name        string
	Description string
	// Tags is a set: no tag is empty, and none appears twice.
	Tags        []string
	ContentType string
	Filename    string
	BodyLen     uint64
	SHA256      [sha256.Size]byte
}

// EncodeStoreMeta serializes a record into exactly StoreMetaSize bytes
// (spec §7a.5):
//
//	u8(0x01) || u64be(body_len) || sha256[32]
//	|| str16(content_type) || str16(filename) || str16(name) || str16(description)
//	|| u8(tag_count) || tag_count × (u8(len) || tag)
//	|| zero padding
//
// A content type over 128 bytes is written as application/octet-stream and a
// filename is cut at a UTF-8 boundary to 255 bytes, as in a v2 header. The
// fields a user types are refused when over their limits, never cut.
func EncodeStoreMeta(m StoreMeta) ([]byte, error) {
	if err := checkStoreMeta(m.Name, m.Description, m.Tags); err != nil {
		return nil, fmt.Errorf("tppcrypto: encode store meta: %w", err)
	}
	ct := m.ContentType
	if ct == "" {
		return nil, fmt.Errorf("tppcrypto: encode store meta: %w: empty content type", ErrMalformed)
	}
	if len(ct) > headerMaxContentType {
		ct = fallbackContentType
	}
	fn := truncateUTF8(m.Filename, storeMaxFilename)
	out := make([]byte, 0, StoreMetaSize)
	out = append(out, storeMetaV1)
	out = binary.BigEndian.AppendUint64(out, m.BodyLen)
	out = append(out, m.SHA256[:]...)
	for _, s := range []string{ct, fn, m.Name, m.Description} {
		out = binary.BigEndian.AppendUint16(out, uint16(len(s))) //nolint:gosec // every field is bounded above
		out = append(out, s...)
	}
	out = append(out, byte(len(m.Tags)))
	for _, t := range m.Tags {
		out = append(out, byte(len(t)))
		out = append(out, t...)
	}
	return append(out, make([]byte, StoreMetaSize-len(out))...), nil
}

// checkStoreMeta enforces the limits on what a user types.
func checkStoreMeta(name, description string, tags []string) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: empty name", ErrMalformed)
	case len(name) > StoreMaxName:
		return fmt.Errorf("%w: name over %d bytes", ErrMalformed, StoreMaxName)
	case len(description) > StoreMaxDescription:
		return fmt.Errorf("%w: description over %d bytes", ErrMalformed, StoreMaxDescription)
	case len(tags) > StoreMaxTags:
		return fmt.Errorf("%w: more than %d tags", ErrMalformed, StoreMaxTags)
	}
	seen := make(map[string]bool, len(tags))
	for _, t := range tags {
		switch {
		case t == "":
			return fmt.Errorf("%w: empty tag", ErrMalformed)
		case len(t) > StoreMaxTag:
			return fmt.Errorf("%w: tag over %d bytes", ErrMalformed, StoreMaxTag)
		case seen[t]:
			return fmt.Errorf("%w: duplicate tag", ErrMalformed)
		}
		seen[t] = true
	}
	return nil
}

// DecodeStoreMeta parses a record, rejecting anything but its canonical
// encoding: exact size, known version, every limit EncodeStoreMeta keeps, no
// duplicate tag, and zero padding.
func DecodeStoreMeta(b []byte) (StoreMeta, error) {
	if len(b) != StoreMetaSize {
		return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w: %d bytes, want %d", ErrMalformed, len(b), StoreMetaSize)
	}
	if b[0] != storeMetaV1 {
		return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w", ErrVersion)
	}
	r := &frameReader{b: b, i: 1}
	var m StoreMeta
	var err error
	if m.BodyLen, err = r.u64(); err != nil {
		return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w", err)
	}
	sum, err := r.bytes(sha256.Size)
	if err != nil {
		return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w", err)
	}
	copy(m.SHA256[:], sum)
	for _, f := range []*string{&m.ContentType, &m.Filename, &m.Name, &m.Description} {
		if *f, err = r.str16(); err != nil {
			return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w", err)
		}
	}
	if m.ContentType == "" || len(m.ContentType) > headerMaxContentType || len(m.Filename) > storeMaxFilename {
		return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w: content type or filename out of bounds", ErrMalformed)
	}
	n, err := r.u8()
	if err != nil {
		return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w", err)
	}
	for range int(n) {
		l, err := r.u8()
		if err != nil {
			return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w", err)
		}
		t, err := r.bytes(int(l))
		if err != nil {
			return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w", err)
		}
		m.Tags = append(m.Tags, string(t))
	}
	if err := checkStoreMeta(m.Name, m.Description, m.Tags); err != nil {
		return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w", err)
	}
	for _, c := range b[r.i:] {
		if c != 0 {
			return StoreMeta{}, fmt.Errorf("tppcrypto: decode store meta: %w: non-zero padding", ErrMalformed)
		}
	}
	return m, nil
}

// StoreMetaAAD binds a sealed record to its item and revision:
//
//	"tpp/v1/store-meta-aad" || u8(version) || u32be(len(id)) || id || u64be(rev)
func StoreMetaAAD(itemID string, rev uint64) []byte {
	out := append([]byte(aadStoreMeta), Version)
	out = binary.BigEndian.AppendUint32(out, uint32(len(itemID))) //nolint:gosec // item ids are short
	out = append(out, itemID...)
	return binary.BigEndian.AppendUint64(out, rev)
}

// StoreMetaSealKey derives the key that seals one revision of a record.
func StoreMetaSealKey(metaKey, nonce []byte) ([]byte, error) {
	if len(metaKey) != MetaKeySize {
		return nil, fmt.Errorf("tppcrypto: derive store meta key: %w: meta key is %d bytes", ErrMalformed, len(metaKey))
	}
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("tppcrypto: derive store meta key: %w: nonce is %d bytes", ErrMalformed, len(nonce))
	}
	key, err := hkdf.Key(sha256.New, metaKey, nonce, infoStoreMeta, 32)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: derive store meta key: %w", err)
	}
	return key, nil
}

// SealStoreMeta encrypts an encoded record as revision rev:
//
//	u8(version) || meta_nonce[24] || ciphertext[2048] || tag[16]
func SealStoreMeta(metaKey, nonce []byte, itemID string, rev uint64, encoded []byte) ([]byte, error) {
	if len(encoded) != StoreMetaSize {
		return nil, fmt.Errorf("tppcrypto: seal store meta: %w: %d bytes, want %d", ErrMalformed, len(encoded), StoreMetaSize)
	}
	key, err := StoreMetaSealKey(metaKey, nonce)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: seal store meta: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: seal store meta: %w", err)
	}
	out := make([]byte, 0, StoreMetaContainerSize)
	out = append(append(out, Version), nonce...)
	return aead.Seal(out, nonce, encoded, StoreMetaAAD(itemID, rev)), nil
}

// OpenStoreMeta reverses SealStoreMeta and returns the encoded record. rev is
// the revision the server reports; a record sealed as another revision does
// not open, so the server cannot replay an older edit as the current one.
func OpenStoreMeta(metaKey, sealed []byte, itemID string, rev uint64) ([]byte, error) {
	if len(sealed) != StoreMetaContainerSize {
		return nil, fmt.Errorf("tppcrypto: open store meta: %w: %d bytes, want %d",
			ErrMalformed, len(sealed), StoreMetaContainerSize)
	}
	if sealed[0] != Version {
		return nil, fmt.Errorf("tppcrypto: open store meta: %w", ErrVersion)
	}
	nonce := sealed[1 : 1+NonceSize]
	key, err := StoreMetaSealKey(metaKey, nonce)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open store meta: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open store meta: %w", err)
	}
	encoded, err := aead.Open(nil, nonce, sealed[1+NonceSize:], StoreMetaAAD(itemID, rev))
	if err != nil {
		return nil, fmt.Errorf("tppcrypto: open store meta: %w", ErrAuth)
	}
	return encoded, nil
}
