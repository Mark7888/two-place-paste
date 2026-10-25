package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
)

// ---------------------------------------------------------------------------
// header-v2.json (spec/crypto.md §5.5.1)
// ---------------------------------------------------------------------------

type headerV2Fields struct {
	ContentType string `json:"content_type"`
	Filename    string `json:"filename"`
	BodyLen     uint64 `json:"body_len"`
	SHA256      string `json:"sha256"`
}

type headerV2Vector struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	GroupKey    string         `json:"group_key"`
	Epoch       uint64         `json:"epoch"`
	EpochHex    string         `json:"epoch_hex"`
	EntryID     string         `json:"entry_id"`
	Nonce       string         `json:"header_nonce"`
	HeaderKey   string         `json:"header_key"`
	Input       headerV2Fields `json:"input"`
	Encoded     string         `json:"encoded_header"`
	Decoded     headerV2Fields `json:"decoded"`
	AAD         string         `json:"aad"`
	Container   string         `json:"container"`
}

func headerV2Cases() []headerCase {
	sum := func(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
	return []headerCase{
		{
			name:        "header-v2-text",
			description: "a text entry: no filename; sha256 is the hash of the body \"hello world\"",
			groupKeyLbl: "group-key/epoch-1",
			nonceLbl:    "header-v2-nonce/text",
			epoch:       1,
			entryID:     "8c1f0e2a-5d4b-4e7a-9f3c-2b6d1a0e9c47",
			in:          Header{ContentType: "text/plain; charset=utf-8", BodyLen: 11, SHA256: sum("hello world")},
		},
		{
			name:        "header-v2-file",
			description: "a file entry with a non-ASCII filename, at a later epoch",
			groupKeyLbl: "group-key/epoch-7",
			nonceLbl:    "header-v2-nonce/file",
			epoch:       7,
			entryID:     "Qm3vT_x9Lr2-KpZ8aW4yNg",
			in: Header{ContentType: "application/pdf", Filename: "Quarterly report – Q3 ✓.pdf", BodyLen: 1 << 20,
				SHA256: det("header-v2-sha256/file", sha256.Size)},
		},
		{
			name:        "header-v2-long-content-type",
			description: "a content type over 128 bytes is written as application/octet-stream, as in v1",
			groupKeyLbl: "group-key/epoch-1",
			nonceLbl:    "header-v2-nonce/long-type",
			epoch:       1,
			entryID:     "long-content-type",
			in: Header{ContentType: "application/vnd." + strings.Repeat("x", 130), Filename: "data.bin", BodyLen: 42,
				SHA256: det("header-v2-sha256/long-type", sha256.Size)},
		},
		{
			name:        "header-v2-filename-cut",
			description: "a filename over 255 bytes is cut to 255, never inside a UTF-8 sequence",
			groupKeyLbl: "group-key/epoch-1",
			nonceLbl:    "header-v2-nonce/filename-cut",
			epoch:       1,
			entryID:     "filename-cut",
			in: Header{ContentType: "image/png", Filename: strings.Repeat("é", 200) + ".png", BodyLen: 123456789,
				SHA256: det("header-v2-sha256/filename-cut", sha256.Size)},
		},
	}
}

func buildHeaderV2() ([]byte, int, error) {
	cases := headerV2Cases()
	vs := make([]headerV2Vector, 0, len(cases))
	for _, c := range cases {
		groupKey := det(c.groupKeyLbl, GroupKeySize)
		nonce := det(c.nonceLbl, NonceSize)
		encoded, err := EncodeHeaderV2(c.in)
		if err != nil {
			return nil, 0, err
		}
		decoded, err := DecodeHeader(encoded)
		if err != nil {
			return nil, 0, fmt.Errorf("round-trip header %s: %w", c.name, err)
		}
		key, err := HeaderKey(groupKey, nonce, c.epoch)
		if err != nil {
			return nil, 0, err
		}
		container, err := SealHeader(groupKey, nonce, c.epoch, c.entryID, encoded)
		if err != nil {
			return nil, 0, err
		}
		if len(container) != HeaderV2ContainerSize {
			return nil, 0, fmt.Errorf("header %s: container is %d bytes", c.name, len(container))
		}
		back, err := OpenHeader(groupKey, container, c.epoch, c.entryID)
		if err != nil || !bytes.Equal(back, encoded) {
			return nil, 0, fmt.Errorf("round-trip header %s: %v", c.name, err)
		}
		vs = append(vs, headerV2Vector{
			Name:        c.name,
			Description: c.description,
			GroupKey:    hexs(groupKey),
			Epoch:       c.epoch,
			EpochHex:    epochHex(c.epoch),
			EntryID:     c.entryID,
			Nonce:       hexs(nonce),
			HeaderKey:   hexs(key),
			Input:       headerV2Fields{c.in.ContentType, c.in.Filename, c.in.BodyLen, hexs(c.in.SHA256)},
			Encoded:     hexs(encoded),
			Decoded:     headerV2Fields{decoded.ContentType, decoded.Filename, decoded.BodyLen, hexs(decoded.SHA256)},
			AAD:         hexs(HeaderAAD(c.epoch, c.entryID)),
			Container:   hexs(container),
		})
	}
	body, err := marshal(suite[headerV2Vector]{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Suite:         "header-v2",
		Spec:          "spec/crypto.md §5.5.1",
		Description: "Version 2 entry headers: the v1 fields, a filename of up to 255 bytes and the " +
			"SHA-256 of the body. Sealed exactly as v1, with the same key derivation and AAD. " +
			"Every container is exactly 553 bytes.",
		Vectors: vs,
	})
	return body, len(vs), err
}

func headerV2Failures() ([]failureVector, error) {
	c := headerV2Cases()[0]
	encoded, err := EncodeHeaderV2(c.in)
	if err != nil {
		return nil, err
	}
	decode := func(name, desc, reason string, b []byte) failureVector {
		return failureVector{Name: name, Description: desc, Operation: "decode_header", Reason: reason, EncodedHeader: hexs(b)}
	}
	v1InV2Size := append([]byte(nil), encoded...)
	v1InV2Size[0] = headerV1
	padded := append([]byte(nil), encoded...)
	padded[HeaderV2Size-1] = 0x01

	// A v2 filename of 256 bytes: well-formed in every other way.
	long := make([]byte, 0, HeaderV2Size)
	long = append(long, headerV2, 0, 1, 'x', 1, 0)
	long = append(long, bytes.Repeat([]byte{'a'}, 256)...)
	long = append(long, make([]byte, 8)...)
	long = append(long, c.in.SHA256...)
	long = append(long, make([]byte, HeaderV2Size-len(long))...)

	return []failureVector{
		decode("header-v2-decode-v1-at-v2-size", "a v1 version byte on a 512-byte header",
			"each version has exactly one size", v1InV2Size),
		decode("header-v2-decode-pad-nonzero", "a non-zero byte in a v2 header's padding",
			"the encoding is canonical", padded),
		decode("header-v2-decode-filename-too-long", "a v2 filename of 256 bytes",
			"a v2 writer cuts filenames to 255 bytes", long),
		decode("header-v2-decode-wrong-size", "a v2 header that is not 512 bytes",
			"every header of a version is the same size", encoded[:HeaderV2Size-1]),
	}, nil
}

// ---------------------------------------------------------------------------
// store-chain.json (spec/crypto.md §7a.1, §7a.2)
// ---------------------------------------------------------------------------

type storeChainVector struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	GroupKey         string `json:"group_key"`
	Epoch            uint64 `json:"epoch"`
	EpochHex         string `json:"epoch_hex"`
	StoreInfo        string `json:"store_key_info"`
	StoreKey         string `json:"store_key"`
	PreviousStoreKey string `json:"previous_store_key,omitempty"`
	LinkInfo         string `json:"link_key_info,omitempty"`
	LinkKey          string `json:"link_key,omitempty"`
	LinkAAD          string `json:"link_aad,omitempty"`
	Link             string `json:"link,omitempty"`
}

type chainCase struct {
	name, description string
	epoch             uint64
}

func chainGroupKey(epoch uint64) []byte {
	return det(fmt.Sprintf("store-chain/group-key/epoch-%d", epoch), GroupKeySize)
}

func buildStoreChain() ([]byte, int, error) {
	cases := []chainCase{
		{"chain-epoch-1", "the first epoch of a group: a store key and no link", 1},
		{"chain-epoch-2", "the first rekey: the link seals epoch 1's store key under epoch 2's", 2},
		{"chain-epoch-3", "the second rekey; with chain-epoch-2 a client at epoch 3 reaches epoch 1", 3},
		{"chain-epoch-max", "the last epoch a u64 can hold, to pin the big-endian encoding of both epochs", math.MaxUint64},
	}
	vs := make([]storeChainVector, 0, len(cases))
	for _, c := range cases {
		gk := chainGroupKey(c.epoch)
		sk, err := StoreKey(gk, c.epoch)
		if err != nil {
			return nil, 0, err
		}
		v := storeChainVector{
			Name: c.name, Description: c.description,
			GroupKey: hexs(gk), Epoch: c.epoch, EpochHex: epochHex(c.epoch),
			StoreInfo: hexs(StoreInfo(c.epoch)), StoreKey: hexs(sk),
		}
		if c.epoch >= 2 {
			prev, err := StoreKey(chainGroupKey(c.epoch-1), c.epoch-1)
			if err != nil {
				return nil, 0, err
			}
			lk, err := StoreLinkKey(sk, c.epoch)
			if err != nil {
				return nil, 0, err
			}
			link, err := SealStoreLink(sk, prev, c.epoch)
			if err != nil {
				return nil, 0, err
			}
			back, err := OpenStoreLink(sk, link, c.epoch)
			if err != nil || !bytes.Equal(back, prev) {
				return nil, 0, fmt.Errorf("round-trip link %s: %v", c.name, err)
			}
			v.PreviousStoreKey = hexs(prev)
			v.LinkInfo = hexs(StoreLinkInfo(c.epoch))
			v.LinkKey = hexs(lk)
			v.LinkAAD = hexs(StoreLinkAAD(c.epoch))
			v.Link = hexs(link)
		}
		vs = append(vs, v)
	}
	body, err := marshal(suite[storeChainVector]{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Suite:         "store-chain",
		Spec:          "spec/crypto.md §7a.1, §7a.2",
		Description: "Store keys and the links a rekey uploads. Each vector is one epoch: its store key " +
			"derived from its group key and, from epoch 2 on, the link that seals the previous epoch's " +
			"store key (previous_store_key) under this one. Every link is exactly 49 bytes.",
		Vectors: vs,
	})
	return body, len(vs), err
}

// ---------------------------------------------------------------------------
// store-item.json (spec/crypto.md §7a.3 – §7a.5)
// ---------------------------------------------------------------------------

type storeMetaFields struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	ContentType string   `json:"content_type"`
	Filename    string   `json:"filename"`
	BodyLen     uint64   `json:"body_len"`
	SHA256      string   `json:"sha256"`
}

func metaFields(m StoreMeta) storeMetaFields {
	tags := m.Tags
	if tags == nil {
		tags = []string{}
	}
	return storeMetaFields{m.Name, m.Description, tags, m.ContentType, m.Filename, m.BodyLen, hexs(m.SHA256)}
}

type storeItemVector struct {
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	GroupKey        string          `json:"group_key"`
	Epoch           uint64          `json:"epoch"`
	EpochHex        string          `json:"epoch_hex"`
	StoreKey        string          `json:"store_key"`
	ItemID          string          `json:"item_id"`
	Frame           frameFields     `json:"frame"`
	EncodedFrame    string          `json:"encoded_frame"`
	EntryNonce      string          `json:"entry_nonce"`
	ContentKey      string          `json:"content_key"`
	Container       string          `json:"container"`
	MetaKey         string          `json:"meta_key"`
	WrapNonce       string          `json:"wrap_nonce"`
	WrapInfo        string          `json:"wrap_key_info"`
	WrapKey         string          `json:"wrap_key"`
	WrapAAD         string          `json:"wrap_aad"`
	WrappedItemKeys string          `json:"wrapped_item_keys"`
	Meta            storeMetaFields `json:"meta"`
	MetaDecoded     storeMetaFields `json:"meta_decoded"`
	EncodedMeta     string          `json:"encoded_meta"`
	MetaRev         uint64          `json:"meta_rev"`
	MetaNonce       string          `json:"meta_nonce"`
	MetaSealKey     string          `json:"meta_seal_key"`
	MetaAAD         string          `json:"meta_aad"`
	SealedMeta      string          `json:"sealed_meta"`
}

type storeItemCase struct {
	name, description string
	label             string
	epoch             uint64
	itemID            string
	frame             Frame
	meta              StoreMeta // ContentType, Filename, BodyLen and SHA256 come from frame unless set
	rev               uint64
}

func storeItemCases() []storeItemCase {
	return []storeItemCase{
		{
			name:        "item-text",
			description: "a short text saved at epoch 1, with a name and nothing else",
			label:       "text", epoch: 1, itemID: "aW0tdGV4dC0wMDAwMDAwMQ",
			frame: Frame{ContentType: "text/plain; charset=utf-8", CreatedAtUnix: 1767225600000, Body: []byte("192.168.1.20")},
			meta:  StoreMeta{Name: "NAS address"},
		},
		{
			name:        "item-file-tags",
			description: "a file saved at epoch 3, with a non-ASCII filename, a description and three tags",
			label:       "file", epoch: 3, itemID: "Zp4_k2LmQx-7sWvB1cNe0A",
			frame: Frame{ContentType: "application/pdf", Filename: "Szerződés – végleges.pdf", CreatedAtUnix: 1767312000123,
				Body: det("store-item/body/file", 300)},
			meta: StoreMeta{Name: "Lease contract", Description: "Signed copy, 2026.\nKeep until 2031.",
				Tags: []string{"documents", "home", "ügyintézés"}},
		},
		{
			name:        "item-edited",
			description: "metadata at revision 4: an edit seals a new record under the same metadata key",
			label:       "edited", epoch: 2, itemID: "edited-item",
			frame: Frame{ContentType: "image/png", Filename: "logo.png", CreatedAtUnix: 1767398400000,
				Body: det("store-item/body/edited", 64)},
			meta: StoreMeta{Name: "Logo, dark"},
			rev:  4,
		},
		{
			name:        "item-limits",
			description: "every user-typed field at its limit; a content type over 128 bytes falls back and a filename over 255 bytes is cut",
			label:       "limits", epoch: 1, itemID: strings.Repeat("L", 64),
			frame: Frame{ContentType: "application/vnd." + strings.Repeat("y", 130), Filename: strings.Repeat("ő", 140) + ".bin",
				CreatedAtUnix: 1767484800000, Body: []byte{0x00, 0xff}},
			meta: StoreMeta{Name: strings.Repeat("n", metaMaxName), Description: strings.Repeat("d", metaMaxDescription),
				Tags: limitTags()},
		},
	}
}

func limitTags() []string {
	tags := make([]string, metaMaxTags)
	for i := range tags {
		tags[i] = fmt.Sprintf("%02d", i) + strings.Repeat("t", metaMaxTag-2)
	}
	return tags
}

// storeItem is a store item built from a case, with every intermediate.
type storeItem struct {
	groupKey, storeKey, encodedFrame, entryNonce, contentKey, container []byte
	metaKey, wrapNonce, wrapKey, wrapped                                []byte
	meta                                                                StoreMeta
	encodedMeta, metaNonce, metaSealKey, sealedMeta                     []byte
}

func makeStoreItem(c storeItemCase) (storeItem, error) {
	var it storeItem
	var err error
	it.groupKey = det("store-item/group-key/"+c.label, GroupKeySize)
	if it.storeKey, err = StoreKey(it.groupKey, c.epoch); err != nil {
		return it, err
	}
	if it.encodedFrame, err = c.frame.Encode(); err != nil {
		return it, err
	}
	it.entryNonce = det("store-item/entry-nonce/"+c.label, NonceSize)
	if it.contentKey, err = ContentKey(it.groupKey, it.entryNonce, c.epoch); err != nil {
		return it, err
	}
	if it.container, err = SealEntry(it.groupKey, it.entryNonce, c.epoch, c.itemID, it.encodedFrame); err != nil {
		return it, err
	}
	it.metaKey = det("store-item/meta-key/"+c.label, 32)
	it.wrapNonce = det("store-item/wrap-nonce/"+c.label, NonceSize)
	if it.wrapKey, err = StoreWrapKey(it.storeKey, it.wrapNonce, c.epoch); err != nil {
		return it, err
	}
	if it.wrapped, err = WrapItemKeys(it.storeKey, it.wrapNonce, c.epoch, c.itemID, it.contentKey, it.metaKey); err != nil {
		return it, err
	}
	sum := sha256.Sum256(c.frame.Body)
	it.meta = c.meta
	it.meta.ContentType = c.frame.ContentType
	it.meta.Filename = c.frame.Filename
	it.meta.BodyLen = uint64(len(c.frame.Body))
	it.meta.SHA256 = sum[:]
	if it.encodedMeta, err = EncodeStoreMeta(it.meta); err != nil {
		return it, err
	}
	it.metaNonce = det("store-item/meta-nonce/"+c.label, NonceSize)
	if it.metaSealKey, err = StoreMetaSealKey(it.metaKey, it.metaNonce); err != nil {
		return it, err
	}
	if it.sealedMeta, err = SealStoreMeta(it.metaKey, it.metaNonce, c.itemID, c.rev, it.encodedMeta); err != nil {
		return it, err
	}
	return it, nil
}

func buildStoreItem() ([]byte, int, error) {
	cases := storeItemCases()
	vs := make([]storeItemVector, 0, len(cases))
	for _, c := range cases {
		it, err := makeStoreItem(c)
		if err != nil {
			return nil, 0, fmt.Errorf("store item %s: %w", c.name, err)
		}
		// Round trips: a reader holds only the store key.
		ck, mk, err := UnwrapItemKeys(it.storeKey, it.wrapped, c.epoch, c.itemID)
		if err != nil || !bytes.Equal(ck, it.contentKey) || !bytes.Equal(mk, it.metaKey) {
			return nil, 0, fmt.Errorf("round-trip item keys %s: %v", c.name, err)
		}
		frame, err := OpenEntryWithKey(ck, it.container, c.epoch, c.itemID)
		if err != nil || !bytes.Equal(frame, it.encodedFrame) {
			return nil, 0, fmt.Errorf("round-trip item container %s: %v", c.name, err)
		}
		encoded, err := OpenStoreMeta(mk, it.sealedMeta, c.itemID, c.rev)
		if err != nil || !bytes.Equal(encoded, it.encodedMeta) {
			return nil, 0, fmt.Errorf("round-trip item meta %s: %v", c.name, err)
		}
		decoded, err := DecodeStoreMeta(encoded)
		if err != nil {
			return nil, 0, fmt.Errorf("round-trip item meta %s: %w", c.name, err)
		}
		vs = append(vs, storeItemVector{
			Name: c.name, Description: c.description,
			GroupKey: hexs(it.groupKey), Epoch: c.epoch, EpochHex: epochHex(c.epoch),
			StoreKey: hexs(it.storeKey), ItemID: c.itemID,
			Frame: fields(c.frame), EncodedFrame: hexs(it.encodedFrame),
			EntryNonce: hexs(it.entryNonce), ContentKey: hexs(it.contentKey), Container: hexs(it.container),
			MetaKey: hexs(it.metaKey), WrapNonce: hexs(it.wrapNonce),
			WrapInfo: hexs(StoreWrapInfo(c.epoch)), WrapKey: hexs(it.wrapKey),
			WrapAAD: hexs(StoreWrapAAD(c.epoch, c.itemID)), WrappedItemKeys: hexs(it.wrapped),
			Meta: metaFields(it.meta), MetaDecoded: metaFields(decoded), EncodedMeta: hexs(it.encodedMeta),
			MetaRev: c.rev, MetaNonce: hexs(it.metaNonce), MetaSealKey: hexs(it.metaSealKey),
			MetaAAD: hexs(StoreMetaAAD(c.itemID, c.rev)), SealedMeta: hexs(it.sealedMeta),
		})
	}
	body, err := marshal(suite[storeItemVector]{
		SchemaVersion: schemaVersion,
		Profile:       profile,
		Suite:         "store-item",
		Spec:          "spec/crypto.md §7a.3 – §7a.5",
		Description: "Store items end to end. The blob is an entry container (§5.2) sealed at `epoch` with " +
			"the item id as its entry id; its content key and a random metadata key are wrapped under the " +
			"store key of that epoch (105 bytes), and the metadata record (2048 bytes, sealed 2089) under the " +
			"metadata key. A reader holds only the store key: it unwraps both keys and opens the container " +
			"with the content key. `meta` is what the writer had; `meta_decoded` is what a reader gets back, " +
			"after the content-type fallback and the filename cut.",
		Vectors: vs,
	})
	return body, len(vs), err
}

// ---------------------------------------------------------------------------
// Store cases of failure.json
// ---------------------------------------------------------------------------

func storeFailures() ([]failureVector, error) {
	hv, err := headerV2Failures()
	if err != nil {
		return nil, err
	}
	vs := hv

	// Links.
	const linkEpoch = 2
	sk, err := StoreKey(chainGroupKey(linkEpoch), linkEpoch)
	if err != nil {
		return nil, err
	}
	prev, err := StoreKey(chainGroupKey(linkEpoch-1), linkEpoch-1)
	if err != nil {
		return nil, err
	}
	link, err := SealStoreLink(sk, prev, linkEpoch)
	if err != nil {
		return nil, err
	}
	openLink := func(name, desc, reason string, key, link []byte, epoch uint64) failureVector {
		return failureVector{Name: name, Description: desc, Operation: "open_store_link", Reason: reason,
			StoreKey: hexs(key), Link: hexs(link), Epoch: epoch}
	}
	vs = append(vs,
		openLink("store-link-wrong-epoch", "a link presented as joining epochs 2 and 3 instead of 1 and 2",
			"both epochs are in the key derivation and the AAD", sk, link, linkEpoch+1),
		openLink("store-link-wrong-key", "a link opened with the previous epoch's store key",
			"links only lead from a newer key to an older one", prev, link, linkEpoch),
		openLink("store-link-tampered", "one flipped bit in a link's ciphertext",
			"Poly1305 authentication fails", sk, flip(link, 5), linkEpoch),
		openLink("store-link-unknown-version", "a link version this profile does not implement",
			"rejected before any key is derived", sk, flip(link, 0), linkEpoch),
		openLink("store-link-wrong-length", "a link that is not exactly 49 bytes",
			"every link is the same size", sk, link[:len(link)-1], linkEpoch),
		openLink("store-link-epoch-1", "a link into epoch 1, which has no earlier epoch",
			"the first epoch of a group has no link", sk, link, 1),
	)

	// Item keys, containers and metadata.
	c := storeItemCases()[1]
	it, err := makeStoreItem(c)
	if err != nil {
		return nil, err
	}
	unwrap := func(name, desc, reason string, key, wrapped []byte, epoch uint64, itemID string) failureVector {
		return failureVector{Name: name, Description: desc, Operation: "unwrap_item_keys", Reason: reason,
			StoreKey: hexs(key), WrappedItemKeys: hexs(wrapped), Epoch: epoch, ItemID: itemID}
	}
	otherStore, err := StoreKey(it.groupKey, c.epoch+1)
	if err != nil {
		return nil, err
	}
	vs = append(vs,
		unwrap("store-wrap-wrong-item-id", "item keys moved onto another item",
			"the item id is bound into the AAD", it.storeKey, it.wrapped, c.epoch, c.itemID+"x"),
		unwrap("store-wrap-wrong-epoch", "item keys presented as wrapped at another epoch",
			"the epoch is in the key derivation and the AAD", it.storeKey, it.wrapped, c.epoch+1, c.itemID),
		unwrap("store-wrap-wrong-store-key", "item keys opened with another epoch's store key",
			"a reader walks the chain to the epoch the item was wrapped at", otherStore, it.wrapped, c.epoch, c.itemID),
		unwrap("store-wrap-tampered", "one flipped bit in the wrapped keys",
			"Poly1305 authentication fails", it.storeKey, flip(it.wrapped, 1+NonceSize+7), c.epoch, c.itemID),
		unwrap("store-wrap-unknown-version", "a wrap version this profile does not implement",
			"rejected before any key is derived", it.storeKey, flip(it.wrapped, 0), c.epoch, c.itemID),
		unwrap("store-wrap-wrong-length", "wrapped keys that are not exactly 105 bytes",
			"every wrap is the same size", it.storeKey, it.wrapped[:len(it.wrapped)-1], c.epoch, c.itemID),
	)

	withKey := func(name, desc, reason string, epoch uint64, id string) failureVector {
		return failureVector{Name: name, Description: desc, Operation: "open_entry_with_key", Reason: reason,
			ContentKey: hexs(it.contentKey), Container: hexs(it.container), Epoch: epoch, EntryID: id}
	}
	vs = append(vs,
		withKey("store-container-wrong-item-id", "a store blob opened as another item",
			"the item id is the entry id bound into the AAD", c.epoch, c.itemID+"x"),
		withKey("store-container-wrong-epoch", "a store blob opened at another content epoch",
			"the content epoch is bound into the AAD", c.epoch+1, c.itemID),
	)

	openMeta := func(name, desc, reason string, sealed []byte, id string, rev uint64) failureVector {
		return failureVector{Name: name, Description: desc, Operation: "open_store_meta", Reason: reason,
			MetaKey: hexs(it.metaKey), SealedMeta: hexs(sealed), ItemID: id, MetaRev: rev}
	}
	vs = append(vs,
		openMeta("store-meta-wrong-item-id", "a metadata record moved onto another item",
			"the item id is bound into the AAD", it.sealedMeta, c.itemID+"x", c.rev),
		openMeta("store-meta-wrong-rev", "a metadata record replayed as a later revision",
			"the revision is bound into the AAD", it.sealedMeta, c.itemID, c.rev+1),
		openMeta("store-meta-tampered", "one flipped bit in a sealed record",
			"Poly1305 authentication fails", flip(it.sealedMeta, 1+NonceSize+100), c.itemID, c.rev),
		openMeta("store-meta-unknown-version", "a sealed record version this profile does not implement",
			"rejected before any key is derived", flip(it.sealedMeta, 0), c.itemID, c.rev),
		openMeta("store-meta-wrong-length", "a sealed record that is not exactly 2089 bytes",
			"every record is the same size", it.sealedMeta[:len(it.sealedMeta)-1], c.itemID, c.rev),
	)

	decode := func(name, desc, reason string, b []byte) failureVector {
		return failureVector{Name: name, Description: desc, Operation: "decode_store_meta", Reason: reason, EncodedMeta: hexs(b)}
	}
	base := it.meta
	enc := func(m StoreMeta) []byte { return rawStoreMeta(m) }
	badVersion := append([]byte(nil), it.encodedMeta...)
	badVersion[0] = 0x02
	padded := append([]byte(nil), it.encodedMeta...)
	padded[StoreMetaSize-1] = 0x01
	overflow := append([]byte(nil), it.encodedMeta...)
	// The content type's length prefix sits after version, body length and hash.
	overflow[1+8+32], overflow[1+8+32+1] = 0xFF, 0xFF

	emptyName, emptyType, emptyTag, dupTag, manyTags, longName, longDesc, longTag, longFilename := base, base, base, base, base, base, base, base, base
	emptyName.Name = ""
	emptyType.ContentType = ""
	emptyTag.Tags = []string{"home", ""}
	dupTag.Tags = []string{"home", "work", "home"}
	manyTags.Tags = make([]string, metaMaxTags+1)
	for i := range manyTags.Tags {
		manyTags.Tags[i] = fmt.Sprintf("t%d", i)
	}
	longName.Name = strings.Repeat("n", metaMaxName+1)
	longDesc.Description = strings.Repeat("d", metaMaxDescription+1)
	longTag.Tags = []string{strings.Repeat("t", metaMaxTag+1)}
	longFilename.Filename = strings.Repeat("f", metaMaxFilename+1)

	vs = append(vs,
		decode("store-meta-decode-unknown-version", "a record version this profile does not implement",
			"the version byte is checked first", badVersion),
		decode("store-meta-decode-wrong-size", "a record that is not 2048 bytes",
			"every record is the same size", it.encodedMeta[:StoreMetaSize-1]),
		decode("store-meta-decode-pad-nonzero", "a non-zero byte in the padding",
			"the encoding is canonical", padded),
		decode("store-meta-decode-length-overflow", "a length prefix past the end of the record",
			"lengths must fit", overflow),
		decode("store-meta-decode-empty-name", "an item with no name",
			"every item has a name", enc(emptyName)),
		decode("store-meta-decode-empty-content-type", "an empty content type",
			"a record always names a type", enc(emptyType)),
		decode("store-meta-decode-empty-tag", "a zero-length tag",
			"a tag is never empty", enc(emptyTag)),
		decode("store-meta-decode-duplicate-tag", "the same tag twice",
			"tags are a set; a writer never repeats one", enc(dupTag)),
		decode("store-meta-decode-too-many-tags", "17 tags",
			"an item has at most 16 tags", enc(manyTags)),
		decode("store-meta-decode-name-too-long", "a 201-byte name",
			"a name is at most 200 bytes", enc(longName)),
		decode("store-meta-decode-description-too-long", "an 801-byte description",
			"a description is at most 800 bytes", enc(longDesc)),
		decode("store-meta-decode-tag-too-long", "a 33-byte tag",
			"a tag is at most 32 bytes", enc(longTag)),
		decode("store-meta-decode-filename-too-long", "a 256-byte filename",
			"a writer cuts filenames to 255 bytes", enc(longFilename)),
	)
	return vs, nil
}

// rawStoreMeta encodes a record without any of EncodeStoreMeta's checks, so
// the failure vectors can hold records a writer must never produce.
func rawStoreMeta(m StoreMeta) []byte {
	out := []byte{storeMetaV1}
	out = append(out, make([]byte, 8)...)
	for i := range 8 {
		out[1+i] = byte(m.BodyLen >> (56 - 8*i))
	}
	out = append(out, m.SHA256...)
	for _, s := range []string{m.ContentType, m.Filename, m.Name, m.Description} {
		out = append(out, byte(len(s)>>8), byte(len(s)))
		out = append(out, s...)
	}
	out = append(out, byte(len(m.Tags)))
	for _, t := range m.Tags {
		out = append(out, byte(len(t)))
		out = append(out, t...)
	}
	return append(out, make([]byte, StoreMetaSize-len(out))...)
}
