package ws_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/quota"
	"github.com/Mark7888/two-place-paste/server/internal/ws"
)

// payload is n bytes that differ by position, so a misplaced chunk shows.
func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*13 + 1)
	}
	return b
}

// beginUpload starts a chunked upload and returns its handle.
func beginUpload(t *testing.T, c *client, id string, epoch uint64, size int) *tppv1.EntryUploadBeginResponse {
	t.Helper()
	c.send("begin-"+id, tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_BEGIN_REQUEST, &tppv1.EntryUploadBeginRequest{
		EntryId: id, Epoch: epoch, Size: uint64(size), //nolint:gosec // small test size
	})
	var resp tppv1.EntryUploadBeginResponse
	c.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_BEGIN_RESPONSE, &resp)
	return &resp
}

func sendChunk(c *client, handle string, index int, data []byte) {
	c.send("chunk", tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_REQUEST, &tppv1.EntryUploadChunkRequest{
		UploadHandle: handle, Index: uint32(index), Data: data, //nolint:gosec // small test index
	})
}

// TestChunkedTransfer is the whole large-entry path over the wire: limits,
// begin, chunks, commit, the peer's announcement, and the ranged read back.
func TestChunkedTransfer(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, _ := h.createGroup(t, "laptop")
	phone, _ := h.pair(t, laptop, "phone")

	laptop.send("limits", tppv1.MessageType_MESSAGE_TYPE_ENTRY_LIMITS_REQUEST, &tppv1.EntryLimitsRequest{})
	var limits tppv1.EntryLimitsResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_LIMITS_RESPONSE, &limits)
	if limits.GetMaxEntryBytes() != testMaxBytes || limits.GetSingleFrameMaxBytes() != testFrameMax ||
		limits.GetChunkBytes() != testChunk || limits.GetInlineMaxBytes() != testInlineMax {
		t.Errorf("EntryLimitsResponse = %+v", &limits)
	}

	data := payload(testMaxBytes) // the cap: four frames' worth, eight chunks
	begin := beginUpload(t, laptop, "large-one", 1, len(data))
	if begin.GetEntryId() != "large-one" || begin.GetChunkBytes() != testChunk || begin.GetUploadHandle() == "" {
		t.Fatalf("EntryUploadBeginResponse = %+v", begin)
	}
	for i := 0; i*testChunk < len(data); i++ {
		sendChunk(laptop, begin.GetUploadHandle(), i, data[i*testChunk:min((i+1)*testChunk, len(data))])
		var ack tppv1.EntryUploadChunkResponse
		laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_RESPONSE, &ack)
		if want := uint64(min((i+1)*testChunk, len(data))); ack.GetReceivedBytes() != want { //nolint:gosec // small
			t.Errorf("chunk %d: received_bytes = %d, want %d", i, ack.GetReceivedBytes(), want)
		}
	}

	laptop.send("commit", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		EntryId: "large-one", Epoch: 1, Size: uint64(len(data)),
		Body: &tppv1.EntryPutRequest_UploadHandle{UploadHandle: begin.GetUploadHandle()},
	})
	var put tppv1.EntryPutResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_RESPONSE, &put)
	if put.GetMeta().GetEntryId() != "large-one" || put.GetMeta().GetSize() != uint64(len(data)) || put.GetMeta().GetInline() {
		t.Errorf("EntryPutResponse.Meta = %+v", put.GetMeta())
	}

	// The peer hears about it like any other entry.
	var added tppv1.EntryAdded
	phone.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_ADDED, &added)
	if added.GetMeta().GetEntryId() != "large-one" {
		t.Errorf("EntryAdded = %q, want large-one", added.GetMeta().GetEntryId())
	}

	// One frame cannot carry it back...
	phone.send("fetch", tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_REQUEST, &tppv1.EntryFetchRequest{EntryId: "large-one"})
	if got := phone.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_TOO_LARGE {
		t.Errorf("single-frame fetch of a large entry = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_TOO_LARGE)
	}

	// ...ranges can, and a request for more than a chunk gets a chunk.
	var got []byte
	for len(got) < len(data) {
		phone.send("range", tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_CHUNK_REQUEST, &tppv1.EntryFetchChunkRequest{
			EntryId: "large-one", Offset: uint64(len(got)), Length: 1 << 20,
		})
		var r tppv1.EntryFetchChunkResponse
		phone.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_CHUNK_RESPONSE, &r)
		if len(r.GetData()) == 0 || len(r.GetData()) > testChunk || r.GetOffset() != uint64(len(got)) {
			t.Fatalf("range at %d: offset %d, %d bytes", len(got), r.GetOffset(), len(r.GetData()))
		}
		if r.GetMeta().GetSize() != uint64(len(data)) {
			t.Errorf("range meta size = %d, want %d", r.GetMeta().GetSize(), len(data))
		}
		got = append(got, r.GetData()...)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("ranges reassemble to different bytes than were uploaded")
	}

	phone.send("past-end", tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_CHUNK_REQUEST, &tppv1.EntryFetchChunkRequest{
		EntryId: "large-one", Offset: uint64(len(data)),
	})
	if e := phone.awaitError(); e.GetCode() != tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Errorf("range past the end = %s, want %s", e.GetCode(), tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}
}

func TestChunkedUploadRefusals(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, _ := h.createGroup(t, "laptop")
	phone, _ := h.pair(t, laptop, "phone")

	expect := func(c *client, what string, code tppv1.ErrorCode) {
		t.Helper()
		if got := c.awaitError(); got.GetCode() != code {
			t.Errorf("%s = %s %q, want %s", what, got.GetCode(), got.GetMessage(), code)
		}
	}

	laptop.send("small", tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_BEGIN_REQUEST, &tppv1.EntryUploadBeginRequest{Epoch: 1, Size: testInlineMax})
	expect(laptop, "begin for an inline-sized entry", tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	laptop.send("huge", tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_BEGIN_REQUEST, &tppv1.EntryUploadBeginRequest{Epoch: 1, Size: testMaxBytes + 1})
	expect(laptop, "begin over the cap", tppv1.ErrorCode_ERROR_CODE_TOO_LARGE)
	laptop.send("stale", tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_BEGIN_REQUEST, &tppv1.EntryUploadBeginRequest{Epoch: 7, Size: 1000})
	expect(laptop, "begin under a stale epoch", tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT)

	begin := beginUpload(t, laptop, "", 1, 1000)
	if begin.GetEntryId() == "" {
		t.Errorf("begin without an id was not assigned one")
	}

	sendChunk(laptop, begin.GetUploadHandle(), 1, payload(testChunk))
	expect(laptop, "chunk out of order", tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	sendChunk(laptop, begin.GetUploadHandle(), 0, payload(testChunk-1))
	expect(laptop, "short chunk", tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	sendChunk(phone, begin.GetUploadHandle(), 0, payload(testChunk))
	expect(phone, "chunk from another device", tppv1.ErrorCode_ERROR_CODE_NOT_FOUND)

	sendChunk(laptop, begin.GetUploadHandle(), 0, payload(testChunk))
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_RESPONSE, nil)
	laptop.send("early", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		Epoch: 1, Size: 1000, Body: &tppv1.EntryPutRequest_UploadHandle{UploadHandle: begin.GetUploadHandle()},
	})
	expect(laptop, "commit before the last chunk", tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)

	laptop.send("abort", tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_ABORT_REQUEST, &tppv1.EntryUploadAbortRequest{UploadHandle: begin.GetUploadHandle()})
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_ABORT_RESPONSE, nil)
	sendChunk(laptop, begin.GetUploadHandle(), 1, payload(1000-testChunk))
	expect(laptop, "chunk after abort", tppv1.ErrorCode_ERROR_CODE_NOT_FOUND)
	laptop.send("abort-again", tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_ABORT_REQUEST, &tppv1.EntryUploadAbortRequest{UploadHandle: begin.GetUploadHandle()})
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_ABORT_RESPONSE, nil)
}

// TestChunkedCommitAfterRekey: a group re-keyed mid-upload cannot take the
// entry — it is sealed under the old key — so the commit is refused, and the
// upload is over: its room is released rather than held until the timeout.
func TestChunkedCommitAfterRekey(t *testing.T) {
	t.Parallel()

	ledger := quota.NewMemory(quota.Options{})
	h := newHarnessWithQuota(t, ws.Options{Logger: slog.New(slog.DiscardHandler)}, ledger)
	laptop, created := h.createGroup(t, "laptop")
	_, complete := h.pair(t, laptop, "phone")

	begin := beginUpload(t, laptop, "sealed-old", 1, 600)
	sendChunk(laptop, begin.GetUploadHandle(), 0, payload(testChunk))
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_RESPONSE, nil)
	sendChunk(laptop, begin.GetUploadHandle(), 1, payload(600-testChunk))
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_RESPONSE, nil)

	laptop.send("rekey", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: complete.GetDeviceId(),
		ExpectedEpoch:   1,
		WrappedKeys:     []*tppv1.WrappedKey{{DeviceId: created.GetDeviceId(), WrappedGroupKey: []byte("k2")}},
	})
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_REKEY_RESPONSE, nil)

	laptop.send("commit", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		EntryId: "sealed-old", Epoch: 1, Size: 600,
		Body: &tppv1.EntryPutRequest_UploadHandle{UploadHandle: begin.GetUploadHandle()},
	})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT {
		t.Errorf("commit after a rekey = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT)
	}
	if u, _ := ledger.Usage(context.Background()); u.Bytes != 0 {
		t.Errorf("accounted %d bytes after the refused commit, want 0", u.Bytes)
	}
	sendChunk(laptop, begin.GetUploadHandle(), 2, payload(1))
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_NOT_FOUND {
		t.Errorf("chunk after the refused commit = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_NOT_FOUND)
	}
}

// TestEntryHeaderTravelsWithTheEntry: the sealed header a writer sends is
// stored as is and delivered with the entry's metadata everywhere it appears,
// on both put paths; a header of the wrong size is refused.
func TestEntryHeaderTravelsWithTheEntry(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, _ := h.createGroup(t, "laptop")
	phone, _ := h.pair(t, laptop, "phone")

	header := payload(entries.HeaderSize)
	body := []byte("small ciphertext")
	laptop.send("put", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		EntryId: "with-header", Epoch: 1, Size: uint64(len(body)), Header: header,
		Body: &tppv1.EntryPutRequest_Ciphertext{Ciphertext: body},
	})
	var put tppv1.EntryPutResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_RESPONSE, &put)
	if !bytes.Equal(put.GetMeta().GetHeader(), header) {
		t.Errorf("EntryPutResponse header = %d bytes, want the one sent", len(put.GetMeta().GetHeader()))
	}
	var added tppv1.EntryAdded
	phone.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_ADDED, &added)
	if !bytes.Equal(added.GetMeta().GetHeader(), header) {
		t.Errorf("EntryAdded header = %d bytes, want the one sent", len(added.GetMeta().GetHeader()))
	}

	data := payload(testFrameMax + 1)
	begin := beginUpload(t, laptop, "large-with-header", 1, len(data))
	for i := 0; i*testChunk < len(data); i++ {
		sendChunk(laptop, begin.GetUploadHandle(), i, data[i*testChunk:min((i+1)*testChunk, len(data))])
		var ack tppv1.EntryUploadChunkResponse
		laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_UPLOAD_CHUNK_RESPONSE, &ack)
	}
	large := payload(entries.HeaderSize)
	large[0] ^= 0xff
	laptop.send("commit", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		EntryId: "large-with-header", Epoch: 1, Size: uint64(len(data)), Header: large,
		Body: &tppv1.EntryPutRequest_UploadHandle{UploadHandle: begin.GetUploadHandle()},
	})
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_RESPONSE, &put)
	phone.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_ADDED, &added)

	phone.send("history", tppv1.MessageType_MESSAGE_TYPE_ENTRY_HISTORY_REQUEST, &tppv1.EntryHistoryRequest{Limit: 10})
	var page tppv1.EntryHistoryResponse
	phone.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_HISTORY_RESPONSE, &page)
	if len(page.GetEntries()) != 2 {
		t.Fatalf("history has %d entries, want 2", len(page.GetEntries()))
	}
	if !bytes.Equal(page.GetEntries()[0].GetHeader(), large) || !bytes.Equal(page.GetEntries()[1].GetHeader(), header) {
		t.Error("history does not return each entry's own header")
	}

	laptop.send("bad", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		Epoch: 1, Size: uint64(len(body)), Header: header[:100],
		Body: &tppv1.EntryPutRequest_Ciphertext{Ciphertext: body},
	})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Errorf("short header = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}
}
