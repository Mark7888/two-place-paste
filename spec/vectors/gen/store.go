package main

import (
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// ---------------------------------------------------------------------------
// §7a Store
// ---------------------------------------------------------------------------

// Domain separation strings of the store (spec/crypto.md §2.3).
const (
	infoStore     = "tpp/v1/store"
	infoStoreLink = "tpp/v1/store-link"
	aadStoreLink  = "tpp/v1/store-link-aad"
	infoStoreWrap = "tpp/v1/store-wrap"
	aadStoreWrap  = "tpp/v1/store-wrap-aad"
	infoStoreMeta = "tpp/v1/store-meta"
	aadStoreMeta  = "tpp/v1/store-meta-aad"
	storeMetaV1   = 0x01
)

// Store sizes (spec/crypto.md §7a).
const (
	StoreKeySize           = 32
	StoreLinkSize          = 1 + StoreKeySize + TagSize              // 49
	ItemKeysSize           = 2 * 32                                  // content key || meta key
	WrappedItemKeysSize    = 1 + NonceSize + ItemKeysSize + TagSize  // 105
	StoreMetaSize          = 2048                                    // encoded metadata record
	StoreMetaContainerSize = 1 + NonceSize + StoreMetaSize + TagSize // 2089
	metaMaxName            = 200
	metaMaxDescription     = 800
	metaMaxTags            = 16
	metaMaxTag             = 32
	metaMaxFilename        = 255
)

// StoreKey derives the store key of an epoch from that epoch's group key:
//
//	HKDF(group_key, <empty>, "tpp/v1/store" || u64be(epoch), 32)
func StoreKey(groupKey []byte, epoch uint64) ([]byte, error) {
	if len(groupKey) != GroupKeySize {
		return nil, fmt.Errorf("derive store key: %w", ErrMalformed)
	}
	key, err := hkdf.Key(sha256.New, groupKey, nil, string(StoreInfo(epoch)), StoreKeySize)
	if err != nil {
		return nil, fmt.Errorf("derive store key: %w", err)
	}
	return key, nil
}

// StoreInfo is "tpp/v1/store" || u64be(epoch).
func StoreInfo(epoch uint64) []byte {
	return binary.BigEndian.AppendUint64([]byte(infoStore), epoch)
}

// StoreLinkInfo is "tpp/v1/store-link" || u64be(epoch), epoch being the newer
// of the two the link joins.
func StoreLinkInfo(epoch uint64) []byte {
	return binary.BigEndian.AppendUint64([]byte(infoStoreLink), epoch)
}

// StoreLinkAAD is "tpp/v1/store-link-aad" || u8(version) || u64be(epoch-1) ||
// u64be(epoch).
func StoreLinkAAD(epoch uint64) []byte {
	out := append([]byte(aadStoreLink), Version)
	out = binary.BigEndian.AppendUint64(out, epoch-1)
	return binary.BigEndian.AppendUint64(out, epoch)
}

// StoreLinkKey derives the single-use key that seals the link into epoch.
func StoreLinkKey(storeKey []byte, epoch uint64) ([]byte, error) {
	if len(storeKey) != StoreKeySize {
		return nil, fmt.Errorf("derive store link key: %w", ErrMalformed)
	}
	key, err := hkdf.Key(sha256.New, storeKey, nil, string(StoreLinkInfo(epoch)), 32)
	if err != nil {
		return nil, fmt.Errorf("derive store link key: %w", err)
	}
	return key, nil
}

// SealStoreLink seals the previous epoch's store key under the new one:
//
//	u8(version) || ciphertext[32] || tag[16]
//
// The nonce is 24 zero bytes: the link key is derived from the new store key
// and the epoch, and seals exactly one message, the previous store key.
func SealStoreLink(newStoreKey, oldStoreKey []byte, epoch uint64) ([]byte, error) {
	if epoch < 2 || len(oldStoreKey) != StoreKeySize {
		return nil, fmt.Errorf("seal store link: %w", ErrMalformed)
	}
	key, err := StoreLinkKey(newStoreKey, epoch)
	if err != nil {
		return nil, fmt.Errorf("seal store link: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("seal store link: %w", err)
	}
	return aead.Seal([]byte{Version}, make([]byte, NonceSize), oldStoreKey, StoreLinkAAD(epoch)), nil
}

// OpenStoreLink reverses SealStoreLink: given the store key of epoch, it
// returns the store key of epoch-1.
func OpenStoreLink(storeKey, link []byte, epoch uint64) ([]byte, error) {
	if len(link) != StoreLinkSize || epoch < 2 {
		return nil, fmt.Errorf("open store link: %w", ErrMalformed)
	}
	if link[0] != Version {
		return nil, fmt.Errorf("open store link: %w", ErrVersion)
	}
	key, err := StoreLinkKey(storeKey, epoch)
	if err != nil {
		return nil, fmt.Errorf("open store link: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("open store link: %w", err)
	}
	old, err := aead.Open(nil, make([]byte, NonceSize), link[1:], StoreLinkAAD(epoch))
	if err != nil {
		return nil, fmt.Errorf("open store link: %w", ErrAuth)
	}
	return old, nil
}

// StoreWrapInfo is "tpp/v1/store-wrap" || u64be(epoch).
func StoreWrapInfo(epoch uint64) []byte {
	return binary.BigEndian.AppendUint64([]byte(infoStoreWrap), epoch)
}

// StoreWrapAAD is "tpp/v1/store-wrap-aad" || u8(version) || u64be(epoch) ||
// u32be(len(item_id)) || item_id.
func StoreWrapAAD(epoch uint64, itemID string) []byte {
	out := append([]byte(aadStoreWrap), Version)
	out = binary.BigEndian.AppendUint64(out, epoch)
	out = binary.BigEndian.AppendUint32(out, uint32(len(itemID)))
	return append(out, itemID...)
}

// StoreWrapKey derives the key that wraps one item's keys.
func StoreWrapKey(storeKey, nonce []byte, epoch uint64) ([]byte, error) {
	if len(storeKey) != StoreKeySize || len(nonce) != NonceSize {
		return nil, fmt.Errorf("derive store wrap key: %w", ErrMalformed)
	}
	key, err := hkdf.Key(sha256.New, storeKey, nonce, string(StoreWrapInfo(epoch)), 32)
	if err != nil {
		return nil, fmt.Errorf("derive store wrap key: %w", err)
	}
	return key, nil
}

// WrapItemKeys wraps an item's content key and metadata key under the store
// key of the epoch it is saved at:
//
//	u8(version) || wrap_nonce[24] || ciphertext[64] || tag[16]
func WrapItemKeys(storeKey, nonce []byte, epoch uint64, itemID string, contentKey, metaKey []byte) ([]byte, error) {
	if len(contentKey) != ContentKeySize || len(metaKey) != 32 {
		return nil, fmt.Errorf("wrap item keys: %w", ErrMalformed)
	}
	key, err := StoreWrapKey(storeKey, nonce, epoch)
	if err != nil {
		return nil, fmt.Errorf("wrap item keys: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("wrap item keys: %w", err)
	}
	out := append([]byte{Version}, nonce...)
	keys := append(append([]byte(nil), contentKey...), metaKey...)
	return aead.Seal(out, nonce, keys, StoreWrapAAD(epoch, itemID)), nil
}

// UnwrapItemKeys reverses WrapItemKeys, returning the content key and the
// metadata key.
func UnwrapItemKeys(storeKey, wrapped []byte, epoch uint64, itemID string) (contentKey, metaKey []byte, err error) {
	if len(wrapped) != WrappedItemKeysSize {
		return nil, nil, fmt.Errorf("unwrap item keys: %w", ErrMalformed)
	}
	if wrapped[0] != Version {
		return nil, nil, fmt.Errorf("unwrap item keys: %w", ErrVersion)
	}
	nonce := wrapped[1 : 1+NonceSize]
	key, err := StoreWrapKey(storeKey, nonce, epoch)
	if err != nil {
		return nil, nil, fmt.Errorf("unwrap item keys: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, fmt.Errorf("unwrap item keys: %w", err)
	}
	keys, err := aead.Open(nil, nonce, wrapped[1+NonceSize:], StoreWrapAAD(epoch, itemID))
	if err != nil {
		return nil, nil, fmt.Errorf("unwrap item keys: %w", ErrAuth)
	}
	return keys[:32], keys[32:], nil
}

// StoreMeta is an item's metadata record: what the Store tab lists, searches
// and filters on, none of which the server may see.
type StoreMeta struct {
	Name        string
	Description string
	Tags        []string
	ContentType string
	Filename    string
	BodyLen     uint64
	SHA256      []byte
}

// EncodeStoreMeta serializes a record into exactly StoreMetaSize bytes:
//
//	u8(0x01) || u64be(body_len) || sha256[32]
//	|| str16(content_type) || str16(filename) || str16(name) || str16(description)
//	|| u8(tag_count) || tag_count × (u8(len) || tag)
//	|| zero padding
//
// A content type over 128 bytes is written as application/octet-stream and a
// filename over 255 bytes is cut at a UTF-8 boundary, as in a v2 header. Every
// other limit is refused rather than cut: those are what the user typed.
func EncodeStoreMeta(m StoreMeta) ([]byte, error) {
	if err := checkStoreMeta(m.Name, m.Description, m.Tags); err != nil {
		return nil, fmt.Errorf("encode store meta: %w", err)
	}
	if m.ContentType == "" || len(m.SHA256) != sha256.Size {
		return nil, fmt.Errorf("encode store meta: %w", ErrMalformed)
	}
	ct := m.ContentType
	if len(ct) > headerMaxContentType {
		ct = fallbackContentType
	}
	fn := truncateUTF8(m.Filename, metaMaxFilename)
	out := make([]byte, 0, StoreMetaSize)
	out = append(out, storeMetaV1)
	out = binary.BigEndian.AppendUint64(out, m.BodyLen)
	out = append(out, m.SHA256...)
	for _, s := range []string{ct, fn, m.Name, m.Description} {
		out = binary.BigEndian.AppendUint16(out, uint16(len(s)))
		out = append(out, s...)
	}
	out = append(out, byte(len(m.Tags)))
	for _, t := range m.Tags {
		out = append(out, byte(len(t)))
		out = append(out, t...)
	}
	return append(out, make([]byte, StoreMetaSize-len(out))...), nil
}

// checkStoreMeta enforces the limits on the fields a user types.
func checkStoreMeta(name, description string, tags []string) error {
	if name == "" || len(name) > metaMaxName || len(description) > metaMaxDescription || len(tags) > metaMaxTags {
		return ErrMalformed
	}
	seen := make(map[string]bool, len(tags))
	for _, t := range tags {
		if t == "" || len(t) > metaMaxTag || seen[t] {
			return ErrMalformed
		}
		seen[t] = true
	}
	return nil
}

// DecodeStoreMeta parses a record, rejecting anything but its canonical
// encoding: exact size, known version, every limit of EncodeStoreMeta, no
// duplicate tag, zero padding.
func DecodeStoreMeta(b []byte) (StoreMeta, error) {
	if len(b) != StoreMetaSize {
		return StoreMeta{}, fmt.Errorf("decode store meta: %w", ErrMalformed)
	}
	if b[0] != storeMetaV1 {
		return StoreMeta{}, fmt.Errorf("decode store meta: %w", ErrVersion)
	}
	r := &reader{b: b, i: 1}
	var m StoreMeta
	var err error
	if m.BodyLen, err = r.u64(); err != nil {
		return StoreMeta{}, err
	}
	sum, err := r.bytes(sha256.Size)
	if err != nil {
		return StoreMeta{}, err
	}
	m.SHA256 = append([]byte(nil), sum...)
	fields := []*string{&m.ContentType, &m.Filename, &m.Name, &m.Description}
	for _, f := range fields {
		if *f, err = r.str16(); err != nil {
			return StoreMeta{}, fmt.Errorf("decode store meta: %w", err)
		}
	}
	if m.ContentType == "" || len(m.ContentType) > headerMaxContentType || len(m.Filename) > metaMaxFilename {
		return StoreMeta{}, fmt.Errorf("decode store meta: %w", ErrMalformed)
	}
	n, err := r.u8()
	if err != nil {
		return StoreMeta{}, err
	}
	for range int(n) {
		l, err := r.u8()
		if err != nil {
			return StoreMeta{}, err
		}
		t, err := r.bytes(int(l))
		if err != nil {
			return StoreMeta{}, err
		}
		m.Tags = append(m.Tags, string(t))
	}
	if err := checkStoreMeta(m.Name, m.Description, m.Tags); err != nil {
		return StoreMeta{}, fmt.Errorf("decode store meta: %w", err)
	}
	for _, c := range b[r.i:] {
		if c != 0 {
			return StoreMeta{}, fmt.Errorf("decode store meta padding: %w", ErrMalformed)
		}
	}
	return m, nil
}

// StoreMetaAAD is "tpp/v1/store-meta-aad" || u8(version) || u32be(len(item_id))
// || item_id || u64be(meta_rev).
func StoreMetaAAD(itemID string, rev uint64) []byte {
	out := append([]byte(aadStoreMeta), Version)
	out = binary.BigEndian.AppendUint32(out, uint32(len(itemID)))
	out = append(out, itemID...)
	return binary.BigEndian.AppendUint64(out, rev)
}

// StoreMetaSealKey derives the key that seals one revision of a record.
func StoreMetaSealKey(metaKey, nonce []byte) ([]byte, error) {
	if len(metaKey) != 32 || len(nonce) != NonceSize {
		return nil, fmt.Errorf("derive store meta key: %w", ErrMalformed)
	}
	key, err := hkdf.Key(sha256.New, metaKey, nonce, infoStoreMeta, 32)
	if err != nil {
		return nil, fmt.Errorf("derive store meta key: %w", err)
	}
	return key, nil
}

// SealStoreMeta encrypts an encoded record:
//
//	u8(version) || meta_nonce[24] || ciphertext[2048] || tag[16]
func SealStoreMeta(metaKey, nonce []byte, itemID string, rev uint64, encoded []byte) ([]byte, error) {
	if len(encoded) != StoreMetaSize {
		return nil, fmt.Errorf("seal store meta: %w", ErrMalformed)
	}
	key, err := StoreMetaSealKey(metaKey, nonce)
	if err != nil {
		return nil, fmt.Errorf("seal store meta: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("seal store meta: %w", err)
	}
	out := append([]byte{Version}, nonce...)
	return aead.Seal(out, nonce, encoded, StoreMetaAAD(itemID, rev)), nil
}

// OpenStoreMeta reverses SealStoreMeta, returning the encoded record.
func OpenStoreMeta(metaKey, sealed []byte, itemID string, rev uint64) ([]byte, error) {
	if len(sealed) != StoreMetaContainerSize {
		return nil, fmt.Errorf("open store meta: %w", ErrMalformed)
	}
	if sealed[0] != Version {
		return nil, fmt.Errorf("open store meta: %w", ErrVersion)
	}
	nonce := sealed[1 : 1+NonceSize]
	key, err := StoreMetaSealKey(metaKey, nonce)
	if err != nil {
		return nil, fmt.Errorf("open store meta: %w", err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("open store meta: %w", err)
	}
	encoded, err := aead.Open(nil, nonce, sealed[1+NonceSize:], StoreMetaAAD(itemID, rev))
	if err != nil {
		return nil, fmt.Errorf("open store meta: %w", ErrAuth)
	}
	return encoded, nil
}
