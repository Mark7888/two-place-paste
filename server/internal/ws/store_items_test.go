package ws_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/server/internal/items"
	"google.golang.org/protobuf/proto"
)

// sealedMeta and wrappedKeys stand in for a client's sealed records: the
// relay checks only their length.
func sealedMeta(b byte) []byte  { return bytes.Repeat([]byte{b}, items.SealedMetaSize) }
func wrappedKeys(b byte) []byte { return bytes.Repeat([]byte{b}, items.WrappedItemKeysSize) }

// enableStore turns the store of the caller's group on.
func (h *storeHarness) enableStore(t *testing.T, groupID string) {
	t.Helper()
	if err := h.store.SetGroupStore(context.Background(), groupID, true); err != nil {
		t.Fatalf("SetGroupStore() error = %v", err)
	}
}

// putItem saves an item in one frame and returns the relay's answer.
func putItem(c *client, id string, epoch uint64, data []byte) *tppv1.Envelope {
	c.t.Helper()
	c.send("put-"+id, tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_REQUEST, &tppv1.StorePutRequest{
		ItemId:          id,
		Epoch:           epoch,
		Size:            uint64(len(data)),
		WrappedItemKeys: wrappedKeys(1),
		SealedMeta:      sealedMeta(1),
		Body:            &tppv1.StorePutRequest_Ciphertext{Ciphertext: data},
	})
	return c.awaitAny(tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_RESPONSE)
}

// awaitAny returns the next response of the given type or an error frame,
// whichever comes first.
func (c *client) awaitAny(typ tppv1.MessageType) *tppv1.Envelope {
	c.t.Helper()
	for i, env := range c.pending {
		if env.GetType() == typ || env.GetType() == tppv1.MessageType_MESSAGE_TYPE_ERROR {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return env
		}
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case env, ok := <-c.in:
			if !ok {
				c.t.Fatalf("connection closed while waiting for %s", typ)
			}
			if env.GetType() == typ || env.GetType() == tppv1.MessageType_MESSAGE_TYPE_ERROR {
				return env
			}
			c.pending = append(c.pending, env)
		case <-deadline:
			c.t.Fatalf("timed out waiting for %s", typ)
		}
	}
}

// errorCode is the code of an error frame, or UNSPECIFIED for any other.
func errorCode(t *testing.T, env *tppv1.Envelope) tppv1.ErrorCode {
	t.Helper()
	if env.GetType() != tppv1.MessageType_MESSAGE_TYPE_ERROR {
		return tppv1.ErrorCode_ERROR_CODE_UNSPECIFIED
	}
	var e tppv1.Error
	if err := proto.Unmarshal(env.GetPayload(), &e); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	return e.GetCode()
}

func listItems(c *client, cursor string, limit uint32) *tppv1.StoreListResponse {
	c.t.Helper()
	c.send("list", tppv1.MessageType_MESSAGE_TYPE_STORE_LIST_REQUEST, &tppv1.StoreListRequest{Cursor: cursor, Limit: limit})
	var resp tppv1.StoreListResponse
	c.await(tppv1.MessageType_MESSAGE_TYPE_STORE_LIST_RESPONSE, &resp)
	return &resp
}

// fetchAll reads an item back range by range.
func fetchAll(c *client, id string) []byte {
	c.t.Helper()
	var got []byte
	for {
		c.send("fetch", tppv1.MessageType_MESSAGE_TYPE_STORE_FETCH_CHUNK_REQUEST, &tppv1.StoreFetchChunkRequest{
			ItemId: id, Offset: uint64(len(got)),
		})
		var r tppv1.StoreFetchChunkResponse
		c.await(tppv1.MessageType_MESSAGE_TYPE_STORE_FETCH_CHUNK_RESPONSE, &r)
		if r.GetOffset() != uint64(len(got)) || len(r.GetData()) == 0 || len(r.GetData()) > testChunk {
			c.t.Fatalf("fetch at %d: offset %d, %d bytes", len(got), r.GetOffset(), len(r.GetData()))
		}
		got = append(got, r.GetData()...)
		if uint64(len(got)) >= r.GetItem().GetSize() {
			return got
		}
	}
}

// TestStoreItemLifecycle is an item's whole life over the wire: saved in one
// frame and in chunks through the entries' chunk messages, announced to the
// other device, listed newest first in pages, read back, edited with a
// revision check, and deleted (SPEC §4.6).
func TestStoreItemLifecycle(t *testing.T) {
	t.Parallel()

	h := newStoreHarness(t, testStoreConfig)
	laptop, created := h.createGroup(t, "laptop")
	phone, _ := h.pair(t, laptop, "phone")
	h.enableStore(t, created.GetGroupId())

	small := []byte("a small sealed blob")
	if env := putItem(laptop, "small", 1, small); env.GetType() != tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_RESPONSE {
		t.Fatalf("put small = %s (%s), want a put response", env.GetType(), errorCode(t, env))
	}
	var added tppv1.StoreItemAdded
	phone.await(tppv1.MessageType_MESSAGE_TYPE_STORE_ITEM_ADDED, &added)
	if got := added.GetItem(); got.GetItemId() != "small" || got.GetSize() != uint64(len(small)) ||
		got.GetWrapEpoch() != 1 || got.GetContentId() != "small" || got.GetMetaRev() != 0 ||
		!bytes.Equal(got.GetSealedMeta(), sealedMeta(1)) || !bytes.Equal(got.GetWrappedItemKeys(), wrappedKeys(1)) {
		t.Errorf("StoreItemAdded = %v, want the saved item", got)
	}

	big := payload(testFrameMax + 100) // more than one frame: chunked
	laptop.send("begin", tppv1.MessageType_MESSAGE_TYPE_STORE_UPLOAD_BEGIN_REQUEST, &tppv1.StoreUploadBeginRequest{
		ItemId: "big", Epoch: 1, Size: uint64(len(big)),
	})
	var begin tppv1.StoreUploadBeginResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_STORE_UPLOAD_BEGIN_RESPONSE, &begin)
	if begin.GetChunkBytes() != testChunk || begin.GetUploadHandle() == "" {
		t.Fatalf("StoreUploadBeginResponse = %v, want a handle and %d-byte chunks", &begin, testChunk)
	}
	for i := 0; i*testChunk < len(big); i++ {
		sendChunk(laptop, begin.GetUploadHandle(), i, big[i*testChunk:min((i+1)*testChunk, len(big))])
		var ack tppv1.EntryUploadChunkResponse
		laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_RESPONSE, &ack)
		if want := uint64(min((i+1)*testChunk, len(big))); ack.GetReceivedBytes() != want {
			t.Fatalf("chunk %d ack = %d bytes, want %d", i, ack.GetReceivedBytes(), want)
		}
	}
	laptop.send("commit", tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_REQUEST, &tppv1.StorePutRequest{
		ItemId: "big", Epoch: 1, Size: uint64(len(big)),
		WrappedItemKeys: wrappedKeys(2), SealedMeta: sealedMeta(2),
		Body: &tppv1.StorePutRequest_UploadHandle{UploadHandle: begin.GetUploadHandle()},
	})
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_RESPONSE, nil)
	phone.await(tppv1.MessageType_MESSAGE_TYPE_STORE_ITEM_ADDED, nil)

	// Paging: one item a page, newest first, then the end.
	first := listItems(phone, "", 1)
	if len(first.GetItems()) != 1 || first.GetItems()[0].GetItemId() != "big" || first.GetNextCursor() == "" {
		t.Fatalf("first page = %v, want the newest item and a cursor", first)
	}
	second := listItems(phone, first.GetNextCursor(), 1)
	if len(second.GetItems()) != 1 || second.GetItems()[0].GetItemId() != "small" || second.GetNextCursor() != "" {
		t.Fatalf("second page = %v, want the older item and no cursor", second)
	}

	if got := fetchAll(phone, "big"); !bytes.Equal(got, big) {
		t.Errorf("fetched %d bytes, want the %d saved", len(got), len(big))
	}
	if got := fetchAll(phone, "small"); !bytes.Equal(got, small) {
		t.Errorf("fetched %q, want %q", got, small)
	}
	if got := storeLimits(laptop); got.GetUsedItems() != 2 ||
		got.GetUsedBytes() != uint64(len(small)+len(big)+2*items.RecordBytes) {
		t.Errorf("limits after two saves = %v, want two items and their bytes", got)
	}

	// An edit at the current revision wins; one at a stale revision loses.
	phone.send("edit", tppv1.MessageType_MESSAGE_TYPE_STORE_UPDATE_META_REQUEST, &tppv1.StoreUpdateMetaRequest{
		ItemId: "small", ExpectedMetaRev: 0, SealedMeta: sealedMeta(3),
	})
	var edited tppv1.StoreUpdateMetaResponse
	phone.await(tppv1.MessageType_MESSAGE_TYPE_STORE_UPDATE_META_RESPONSE, &edited)
	if edited.GetItem().GetMetaRev() != 1 {
		t.Errorf("edited revision = %d, want 1", edited.GetItem().GetMetaRev())
	}
	var updated tppv1.StoreItemUpdated
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_STORE_ITEM_UPDATED, &updated)
	if !bytes.Equal(updated.GetItem().GetSealedMeta(), sealedMeta(3)) {
		t.Error("StoreItemUpdated does not carry the new metadata")
	}
	laptop.send("stale", tppv1.MessageType_MESSAGE_TYPE_STORE_UPDATE_META_REQUEST, &tppv1.StoreUpdateMetaRequest{
		ItemId: "small", ExpectedMetaRev: 0, SealedMeta: sealedMeta(4),
	})
	if got := laptop.awaitError().GetCode(); got != tppv1.ErrorCode_ERROR_CODE_REVISION_CONFLICT {
		t.Errorf("stale edit = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_REVISION_CONFLICT)
	}

	laptop.send("delete", tppv1.MessageType_MESSAGE_TYPE_STORE_DELETE_REQUEST, &tppv1.StoreDeleteRequest{ItemId: "small"})
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_STORE_DELETE_RESPONSE, nil)
	var deleted tppv1.StoreItemDeleted
	phone.await(tppv1.MessageType_MESSAGE_TYPE_STORE_ITEM_DELETED, &deleted)
	if deleted.GetItemId() != "small" {
		t.Errorf("StoreItemDeleted = %q, want %q", deleted.GetItemId(), "small")
	}
	laptop.send("delete-again", tppv1.MessageType_MESSAGE_TYPE_STORE_DELETE_REQUEST, &tppv1.StoreDeleteRequest{ItemId: "small"})
	if got := laptop.awaitError().GetCode(); got != tppv1.ErrorCode_ERROR_CODE_NOT_FOUND {
		t.Errorf("delete of a deleted item = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_NOT_FOUND)
	}
	if got := listItems(laptop, "", 0); len(got.GetItems()) != 1 || got.GetItems()[0].GetItemId() != "big" {
		t.Errorf("list after delete = %v, want only %q", got, "big")
	}
}

// TestStoreRefusals: the store refuses while it is off, when full, at a
// stale epoch, for a malformed save, and for an id already in use; none of
// them leaves a trace in the usage.
func TestStoreRefusals(t *testing.T) {
	t.Parallel()

	h := newStoreHarness(t, testStoreConfig)
	laptop, created := h.createGroup(t, "laptop")

	if got := errorCode(t, putItem(laptop, "off", 1, []byte("x"))); got != tppv1.ErrorCode_ERROR_CODE_STORE_DISABLED {
		t.Errorf("save with the store off = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_STORE_DISABLED)
	}
	h.enableStore(t, created.GetGroupId())

	if got := errorCode(t, putItem(laptop, "stale", 7, []byte("x"))); got != tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT {
		t.Errorf("save at a stale epoch = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT)
	}
	if got := errorCode(t, putItem(laptop, "../x", 1, []byte("x"))); got != tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Errorf("save with a path as id = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}
	laptop.send("short-meta", tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_REQUEST, &tppv1.StorePutRequest{
		ItemId: "short", Epoch: 1, Size: 1, WrappedItemKeys: wrappedKeys(1), SealedMeta: []byte("short"),
		Body: &tppv1.StorePutRequest_Ciphertext{Ciphertext: []byte("x")},
	})
	if got := laptop.awaitError().GetCode(); got != tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Errorf("save with short metadata = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}
	if got := errorCode(t, putItem(laptop, "huge", 1, payload(testMaxBytes+1))); got != tppv1.ErrorCode_ERROR_CODE_TOO_LARGE {
		t.Errorf("save above the item limit = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_TOO_LARGE)
	}

	if env := putItem(laptop, "one", 1, []byte("x")); env.GetType() != tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_RESPONSE {
		t.Fatalf("save = %s, want a put response", errorCode(t, env))
	}
	if got := errorCode(t, putItem(laptop, "one", 1, []byte("y"))); got != tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Errorf("save over an existing id = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}
	if got := fetchAll(laptop, "one"); string(got) != "x" {
		t.Errorf("the existing item was replaced: %q", got)
	}

	// The item count is the limit that is reached first here: four items.
	for _, id := range []string{"two", "three", "four"} {
		if env := putItem(laptop, id, 1, []byte("x")); env.GetType() != tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_RESPONSE {
			t.Fatalf("save %s = %s, want a put response", id, errorCode(t, env))
		}
	}
	if got := errorCode(t, putItem(laptop, "five", 1, []byte("x"))); got != tppv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED {
		t.Errorf("save into a full store = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_QUOTA_EXCEEDED)
	}
	if got := storeLimits(laptop); got.GetUsedItems() != 4 || got.GetUsedBytes() != 4*(1+items.RecordBytes) {
		t.Errorf("limits = %v, want only the four saved items counted", got)
	}

	// Turned off, the items are kept but closed.
	if err := h.store.SetGroupStore(context.Background(), created.GetGroupId(), false); err != nil {
		t.Fatalf("SetGroupStore(false) error = %v", err)
	}
	laptop.send("list-off", tppv1.MessageType_MESSAGE_TYPE_STORE_LIST_REQUEST, &tppv1.StoreListRequest{})
	if got := laptop.awaitError().GetCode(); got != tppv1.ErrorCode_ERROR_CODE_STORE_DISABLED {
		t.Errorf("list with the store off = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_STORE_DISABLED)
	}
	h.enableStore(t, created.GetGroupId())
	if got := listItems(laptop, "", 0); len(got.GetItems()) != 4 {
		t.Errorf("list after turning the store back on = %d items, want 4", len(got.GetItems()))
	}
}

// TestRekeyNeedsALinkOnceItemsExist: a rekey without a store link is fine
// while the store is empty and refused as outdated once an item is saved,
// because the item would be left unreadable (/spec/crypto.md §7a.2).
func TestRekeyNeedsALinkOnceItemsExist(t *testing.T) {
	t.Parallel()

	h := newStoreHarness(t, testStoreConfig)
	laptop, created := h.createGroup(t, "laptop")
	_, phone := h.pair(t, laptop, "phone")
	_, tablet := h.pair(t, laptop, "tablet")
	h.enableStore(t, created.GetGroupId())

	laptop.send("rekey-empty", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: phone.GetDeviceId(),
		ExpectedEpoch:   1,
		WrappedKeys: []*tppv1.WrappedKey{
			{DeviceId: created.GetDeviceId(), WrappedGroupKey: []byte("next")},
			{DeviceId: tablet.GetDeviceId(), WrappedGroupKey: []byte("next")},
		},
	})
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_REKEY_RESPONSE, nil)

	if env := putItem(laptop, "kept", 2, []byte("x")); env.GetType() != tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_RESPONSE {
		t.Fatalf("save = %s, want a put response", errorCode(t, env))
	}
	laptop.send("rekey-nolink", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: tablet.GetDeviceId(),
		ExpectedEpoch:   2,
		WrappedKeys:     []*tppv1.WrappedKey{{DeviceId: created.GetDeviceId(), WrappedGroupKey: []byte("next")}},
	})
	if got := laptop.awaitError().GetCode(); got != tppv1.ErrorCode_ERROR_CODE_CLIENT_OUTDATED {
		t.Errorf("rekey without a link over a saved item = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_CLIENT_OUTDATED)
	}
	// An upload begun before the rekey cannot commit after it: its blob is
	// sealed under the old key. Its room is given back.
	data := payload(testChunk)
	laptop.send("begin", tppv1.MessageType_MESSAGE_TYPE_STORE_UPLOAD_BEGIN_REQUEST, &tppv1.StoreUploadBeginRequest{
		ItemId: "in-flight", Epoch: 2, Size: uint64(len(data)),
	})
	var begin tppv1.StoreUploadBeginResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_STORE_UPLOAD_BEGIN_RESPONSE, &begin)
	sendChunk(laptop, begin.GetUploadHandle(), 0, data)
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_RESPONSE, nil)

	rekey(laptop, tablet.GetDeviceId(), created.GetDeviceId(), 2, bytes.Repeat([]byte{9}, 49))

	laptop.send("commit", tppv1.MessageType_MESSAGE_TYPE_STORE_PUT_REQUEST, &tppv1.StorePutRequest{
		ItemId: "in-flight", Epoch: 2, Size: uint64(len(data)),
		WrappedItemKeys: wrappedKeys(1), SealedMeta: sealedMeta(1),
		Body: &tppv1.StorePutRequest_UploadHandle{UploadHandle: begin.GetUploadHandle()},
	})
	if got := laptop.awaitError().GetCode(); got != tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT {
		t.Errorf("commit across a rekey = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT)
	}
	if got := storeLimits(laptop); got.GetUsedItems() != 1 || got.GetUsedBytes() != 1+items.RecordBytes {
		t.Errorf("limits after the refused commit = %v, want only the saved item", got)
	}

	if got := errorCode(t, putItem(laptop, "old-epoch", 2, []byte("x"))); got != tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT {
		t.Errorf("save at the epoch before the rekey = %s, want %s", got, tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT)
	}
}
