package tppcrypto

import (
	"bytes"
	"crypto/sha256"
	"slices"
	"testing"
)

type headerV2FieldsVector struct {
	ContentType string    `json:"content_type"`
	Filename    string    `json:"filename"`
	BodyLen     uint64    `json:"body_len"`
	SHA256      hexString `json:"sha256"`
}

func (h headerV2FieldsVector) header(t *testing.T) Header {
	t.Helper()
	if len(h.SHA256) != sha256.Size {
		t.Fatalf("sha256 is %d bytes, want %d", len(h.SHA256), sha256.Size)
	}
	out := Header{ContentType: h.ContentType, Filename: h.Filename, BodyLen: h.BodyLen, HasSHA256: true}
	copy(out.SHA256[:], h.SHA256)
	return out
}

func TestHeaderV2Vectors(t *testing.T) {
	t.Parallel()
	var suite struct {
		Vectors []struct {
			epochField
			Name      string               `json:"name"`
			GroupKey  hexString            `json:"group_key"`
			EntryID   string               `json:"entry_id"`
			Nonce     hexString            `json:"header_nonce"`
			HeaderKey hexString            `json:"header_key"`
			Input     headerV2FieldsVector `json:"input"`
			Encoded   hexString            `json:"encoded_header"`
			Decoded   headerV2FieldsVector `json:"decoded"`
			AAD       hexString            `json:"aad"`
			Container hexString            `json:"container"`
		} `json:"vectors"`
	}
	loadSuite(t, "header-v2.json", &suite)
	if len(suite.Vectors) == 0 {
		t.Fatal("header-v2 suite is empty")
	}
	for _, v := range suite.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			epoch := v.value(t)
			key, err := HeaderKey(v.GroupKey, v.Nonce, epoch)
			if err != nil {
				t.Fatalf("HeaderKey: %v", err)
			}
			if !bytes.Equal(key, v.HeaderKey) {
				t.Errorf("header key = %x, want %x", key, v.HeaderKey)
			}
			if got := HeaderAAD(epoch, v.EntryID); !bytes.Equal(got, v.AAD) {
				t.Errorf("header aad = %x, want %x", got, v.AAD)
			}
			encoded, err := EncodeHeaderV2(v.Input.header(t))
			if err != nil {
				t.Fatalf("EncodeHeaderV2: %v", err)
			}
			if !bytes.Equal(encoded, v.Encoded) {
				t.Errorf("encoded header = %x, want %x", encoded, v.Encoded)
			}
			want := v.Decoded.header(t)
			decoded, err := DecodeHeader(v.Encoded)
			if err != nil {
				t.Fatalf("DecodeHeader: %v", err)
			}
			if decoded != want {
				t.Errorf("decoded header = %+v, want %+v", decoded, want)
			}
			container, err := SealHeader(v.GroupKey, v.Nonce, epoch, v.EntryID, encoded)
			if err != nil {
				t.Fatalf("SealHeader: %v", err)
			}
			if !bytes.Equal(container, v.Container) {
				t.Errorf("container = %x, want %x", container, v.Container)
			}
			opened, err := OpenEntryHeader(v.GroupKey, v.Container, epoch, v.EntryID)
			if err != nil {
				t.Fatalf("OpenEntryHeader: %v", err)
			}
			if opened != want {
				t.Errorf("opened header = %+v, want %+v", opened, want)
			}
		})
	}
}

type storeChainVector struct {
	epochField
	Name             string    `json:"name"`
	GroupKey         hexString `json:"group_key"`
	StoreInfo        hexString `json:"store_key_info"`
	StoreKey         hexString `json:"store_key"`
	PreviousStoreKey hexString `json:"previous_store_key"`
	LinkInfo         hexString `json:"link_key_info"`
	LinkKey          hexString `json:"link_key"`
	LinkAAD          hexString `json:"link_aad"`
	Link             hexString `json:"link"`
}

func TestStoreChainVectors(t *testing.T) {
	t.Parallel()
	var suite struct {
		Vectors []storeChainVector `json:"vectors"`
	}
	loadSuite(t, "store-chain.json", &suite)
	if len(suite.Vectors) == 0 {
		t.Fatal("store-chain suite is empty")
	}
	byEpoch := make(map[uint64]storeChainVector, len(suite.Vectors))
	for _, v := range suite.Vectors {
		byEpoch[v.value(t)] = v
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			epoch := v.value(t)
			if got := StoreInfo(epoch); !bytes.Equal(got, v.StoreInfo) {
				t.Errorf("store key info = %x, want %x", got, v.StoreInfo)
			}
			sk, err := StoreKey(v.GroupKey, epoch)
			if err != nil {
				t.Fatalf("StoreKey: %v", err)
			}
			if !bytes.Equal(sk, v.StoreKey) {
				t.Errorf("store key = %x, want %x", sk, v.StoreKey)
			}
			if len(v.Link) == 0 {
				return
			}
			if got := StoreLinkInfo(epoch); !bytes.Equal(got, v.LinkInfo) {
				t.Errorf("link key info = %x, want %x", got, v.LinkInfo)
			}
			lk, err := StoreLinkKey(sk, epoch)
			if err != nil {
				t.Fatalf("StoreLinkKey: %v", err)
			}
			if !bytes.Equal(lk, v.LinkKey) {
				t.Errorf("link key = %x, want %x", lk, v.LinkKey)
			}
			if got := StoreLinkAAD(epoch); !bytes.Equal(got, v.LinkAAD) {
				t.Errorf("link aad = %x, want %x", got, v.LinkAAD)
			}
			link, err := SealStoreLink(sk, v.PreviousStoreKey, epoch)
			if err != nil {
				t.Fatalf("SealStoreLink: %v", err)
			}
			if !bytes.Equal(link, v.Link) {
				t.Errorf("link = %x, want %x", link, v.Link)
			}
			prev, err := OpenStoreLink(sk, v.Link, epoch)
			if err != nil {
				t.Fatalf("OpenStoreLink: %v", err)
			}
			if !bytes.Equal(prev, v.PreviousStoreKey) {
				t.Errorf("previous store key = %x, want %x", prev, v.PreviousStoreKey)
			}
		})
	}

	// A client at epoch 3 reaches epoch 1 with its own store key and the links.
	t.Run("walk", func(t *testing.T) {
		t.Parallel()
		for _, e := range []uint64{1, 2, 3} {
			if _, ok := byEpoch[e]; !ok {
				t.Fatalf("no vector for epoch %d", e)
			}
		}
		sk := []byte(byEpoch[3].StoreKey)
		for _, e := range []uint64{3, 2} {
			var err error
			if sk, err = OpenStoreLink(sk, byEpoch[e].Link, e); err != nil {
				t.Fatalf("OpenStoreLink(%d): %v", e, err)
			}
		}
		if !bytes.Equal(sk, byEpoch[1].StoreKey) {
			t.Errorf("walked store key = %x, want %x", sk, []byte(byEpoch[1].StoreKey))
		}
	})
}

type storeMetaVector struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Tags        []string  `json:"tags"`
	ContentType string    `json:"content_type"`
	Filename    string    `json:"filename"`
	BodyLen     uint64    `json:"body_len"`
	SHA256      hexString `json:"sha256"`
}

func (m storeMetaVector) meta() StoreMeta {
	out := StoreMeta{Name: m.Name, Description: m.Description, Tags: m.Tags,
		ContentType: m.ContentType, Filename: m.Filename, BodyLen: m.BodyLen}
	copy(out.SHA256[:], m.SHA256)
	return out
}

func equalMeta(a, b StoreMeta) bool {
	return a.Name == b.Name && a.Description == b.Description && slices.Equal(a.Tags, b.Tags) &&
		a.ContentType == b.ContentType && a.Filename == b.Filename && a.BodyLen == b.BodyLen && a.SHA256 == b.SHA256
}

func TestStoreItemVectors(t *testing.T) {
	t.Parallel()
	var suite struct {
		Vectors []struct {
			epochField
			Name            string          `json:"name"`
			GroupKey        hexString       `json:"group_key"`
			StoreKey        hexString       `json:"store_key"`
			ItemID          string          `json:"item_id"`
			Frame           frameVector     `json:"frame"`
			EncodedFrame    hexString       `json:"encoded_frame"`
			EntryNonce      hexString       `json:"entry_nonce"`
			ContentKey      hexString       `json:"content_key"`
			Container       hexString       `json:"container"`
			MetaKey         hexString       `json:"meta_key"`
			WrapNonce       hexString       `json:"wrap_nonce"`
			WrapInfo        hexString       `json:"wrap_key_info"`
			WrapKey         hexString       `json:"wrap_key"`
			WrapAAD         hexString       `json:"wrap_aad"`
			WrappedItemKeys hexString       `json:"wrapped_item_keys"`
			Meta            storeMetaVector `json:"meta"`
			MetaDecoded     storeMetaVector `json:"meta_decoded"`
			EncodedMeta     hexString       `json:"encoded_meta"`
			MetaRev         uint64          `json:"meta_rev"`
			MetaNonce       hexString       `json:"meta_nonce"`
			MetaSealKey     hexString       `json:"meta_seal_key"`
			MetaAAD         hexString       `json:"meta_aad"`
			SealedMeta      hexString       `json:"sealed_meta"`
		} `json:"vectors"`
	}
	loadSuite(t, "store-item.json", &suite)
	if len(suite.Vectors) == 0 {
		t.Fatal("store-item suite is empty")
	}
	for _, v := range suite.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			epoch := v.value(t)

			// Writer: everything from the group key of the epoch it saves at.
			sk, err := StoreKey(v.GroupKey, epoch)
			if err != nil {
				t.Fatalf("StoreKey: %v", err)
			}
			if !bytes.Equal(sk, v.StoreKey) {
				t.Errorf("store key = %x, want %x", sk, v.StoreKey)
			}
			frame, err := v.Frame.frame().Encode()
			if err != nil {
				t.Fatalf("Frame.Encode: %v", err)
			}
			if !bytes.Equal(frame, v.EncodedFrame) {
				t.Errorf("encoded frame = %x, want %x", frame, v.EncodedFrame)
			}
			ck, err := ContentKey(v.GroupKey, v.EntryNonce, epoch)
			if err != nil {
				t.Fatalf("ContentKey: %v", err)
			}
			if !bytes.Equal(ck, v.ContentKey) {
				t.Errorf("content key = %x, want %x", ck, v.ContentKey)
			}
			container, err := SealEntry(v.GroupKey, v.EntryNonce, epoch, v.ItemID, frame)
			if err != nil {
				t.Fatalf("SealEntry: %v", err)
			}
			if !bytes.Equal(container, v.Container) {
				t.Errorf("container = %x, want %x", container, v.Container)
			}
			if got := StoreWrapInfo(epoch); !bytes.Equal(got, v.WrapInfo) {
				t.Errorf("wrap key info = %x, want %x", got, v.WrapInfo)
			}
			wk, err := StoreWrapKey(sk, v.WrapNonce, epoch)
			if err != nil {
				t.Fatalf("StoreWrapKey: %v", err)
			}
			if !bytes.Equal(wk, v.WrapKey) {
				t.Errorf("wrap key = %x, want %x", wk, v.WrapKey)
			}
			if got := StoreWrapAAD(epoch, v.ItemID); !bytes.Equal(got, v.WrapAAD) {
				t.Errorf("wrap aad = %x, want %x", got, v.WrapAAD)
			}
			wrapped, err := WrapItemKeys(sk, v.WrapNonce, epoch, v.ItemID, ck, v.MetaKey)
			if err != nil {
				t.Fatalf("WrapItemKeys: %v", err)
			}
			if !bytes.Equal(wrapped, v.WrappedItemKeys) {
				t.Errorf("wrapped item keys = %x, want %x", wrapped, v.WrappedItemKeys)
			}
			encoded, err := EncodeStoreMeta(v.Meta.meta())
			if err != nil {
				t.Fatalf("EncodeStoreMeta: %v", err)
			}
			if !bytes.Equal(encoded, v.EncodedMeta) {
				t.Errorf("encoded meta = %x, want %x", encoded, v.EncodedMeta)
			}
			msk, err := StoreMetaSealKey(v.MetaKey, v.MetaNonce)
			if err != nil {
				t.Fatalf("StoreMetaSealKey: %v", err)
			}
			if !bytes.Equal(msk, v.MetaSealKey) {
				t.Errorf("meta seal key = %x, want %x", msk, v.MetaSealKey)
			}
			if got := StoreMetaAAD(v.ItemID, v.MetaRev); !bytes.Equal(got, v.MetaAAD) {
				t.Errorf("meta aad = %x, want %x", got, v.MetaAAD)
			}
			sealed, err := SealStoreMeta(v.MetaKey, v.MetaNonce, v.ItemID, v.MetaRev, encoded)
			if err != nil {
				t.Fatalf("SealStoreMeta: %v", err)
			}
			if !bytes.Equal(sealed, v.SealedMeta) {
				t.Errorf("sealed meta = %x, want %x", sealed, v.SealedMeta)
			}

			// Reader: only the store key.
			gotCK, gotMK, err := UnwrapItemKeys(v.StoreKey, v.WrappedItemKeys, epoch, v.ItemID)
			if err != nil {
				t.Fatalf("UnwrapItemKeys: %v", err)
			}
			if !bytes.Equal(gotCK, v.ContentKey) || !bytes.Equal(gotMK, v.MetaKey) {
				t.Errorf("unwrapped keys = %x, %x, want %x, %x", gotCK, gotMK, v.ContentKey, v.MetaKey)
			}
			opened, err := OpenEntryWithKey(gotCK, v.Container, epoch, v.ItemID)
			if err != nil {
				t.Fatalf("OpenEntryWithKey: %v", err)
			}
			if !bytes.Equal(opened, v.EncodedFrame) {
				t.Errorf("opened frame = %x, want %x", opened, v.EncodedFrame)
			}
			openedMeta, err := OpenStoreMeta(gotMK, v.SealedMeta, v.ItemID, v.MetaRev)
			if err != nil {
				t.Fatalf("OpenStoreMeta: %v", err)
			}
			decoded, err := DecodeStoreMeta(openedMeta)
			if err != nil {
				t.Fatalf("DecodeStoreMeta: %v", err)
			}
			if want := v.MetaDecoded.meta(); !equalMeta(decoded, want) {
				t.Errorf("decoded meta = %+v, want %+v", decoded, want)
			}
		})
	}
}
