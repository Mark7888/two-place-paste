package ws_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/store"
	"github.com/Mark7888/two-place-paste/server/internal/ws"
)

// Small limits keep the tests fast while exercising both storage paths: the
// ratio is what matters, not the absolute size.
const (
	testInlineMax = 64
	testMaxBytes  = 4096
)

type harness struct {
	url     string
	store   store.Store
	entries *entries.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	st := store.NewMemoryStore()
	svc := entries.NewService(entries.NewMemoryStore(), blob.NewDisk(t.TempDir(), testMaxBytes), entries.Options{
		InlineMaxBytes: testInlineMax,
		MaxBytes:       testMaxBytes,
	})
	server := ws.New(st, svc, ws.Options{Logger: slog.New(slog.DiscardHandler)})

	mux := http.NewServeMux()
	server.Register(mux)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	return &harness{url: "ws" + strings.TrimPrefix(httpSrv.URL, "http") + ws.DefaultPath, store: st, entries: svc}
}

func (h *harness) token(t *testing.T) string {
	t.Helper()
	tok, err := h.store.CreateToken(context.Background(), "test")
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	return tok.Value
}

// client is a protocol client good enough to drive the server: one reader
// goroutine fans every envelope into a channel, and the helpers below pick out
// the response or event they are waiting for.
type client struct {
	t    *testing.T
	sock *websocket.Conn
	in   chan *tppv1.Envelope
	errs chan error

	// pending holds envelopes that arrived while a helper was waiting for a
	// different type. Nothing is discarded: a response and the events the same
	// request produced arrive in whatever order the server queued them.
	pending []*tppv1.Envelope
}

func (h *harness) dial(t *testing.T, deviceID string) *client {
	t.Helper()

	url := h.url
	if deviceID != "" {
		url += "?device_id=" + deviceID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sock, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sock.SetReadLimit(testMaxBytes + (64 << 10))

	c := &client{t: t, sock: sock, in: make(chan *tppv1.Envelope, 32), errs: make(chan error, 1)}
	t.Cleanup(func() { _ = sock.CloseNow() })

	go func() {
		for {
			typ, data, err := sock.Read(context.Background())
			if err != nil {
				select {
				case c.errs <- err:
				default:
				}
				close(c.in)
				return
			}
			if typ != websocket.MessageBinary {
				continue
			}
			var env tppv1.Envelope
			if err := proto.Unmarshal(data, &env); err != nil {
				continue
			}
			c.in <- &env
		}
	}()
	return c
}

func (c *client) send(id string, typ tppv1.MessageType, msg proto.Message) {
	c.t.Helper()

	payload, err := proto.Marshal(msg)
	if err != nil {
		c.t.Fatalf("marshal payload: %v", err)
	}
	frame, err := proto.Marshal(&tppv1.Envelope{Id: id, Type: typ, Payload: payload})
	if err != nil {
		c.t.Fatalf("marshal envelope: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.sock.Write(ctx, websocket.MessageBinary, frame); err != nil {
		c.t.Fatalf("write frame: %v", err)
	}
}

// await returns the next envelope of the given type, ignoring anything else
// that arrives first: server-pushed events interleave with responses by
// design.
func (c *client) await(typ tppv1.MessageType, into proto.Message) *tppv1.Envelope {
	c.t.Helper()

	unmarshal := func(env *tppv1.Envelope) *tppv1.Envelope {
		if into != nil {
			if err := proto.Unmarshal(env.GetPayload(), into); err != nil {
				c.t.Fatalf("unmarshal %s: %v", typ, err)
			}
		}
		return env
	}

	for i, env := range c.pending {
		if env.GetType() == typ {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return unmarshal(env)
		}
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case env, ok := <-c.in:
			if !ok {
				c.t.Fatalf("connection closed while waiting for %s", typ)
			}
			if env.GetType() != typ {
				c.pending = append(c.pending, env)
				continue
			}
			return unmarshal(env)
		case <-deadline:
			c.t.Fatalf("timed out waiting for %s", typ)
		}
	}
}

func (c *client) awaitError() *tppv1.Error {
	c.t.Helper()
	var e tppv1.Error
	c.await(tppv1.MessageType_MESSAGE_TYPE_ERROR, &e)
	return &e
}

// awaitClose waits for the server to close the socket.
func (c *client) awaitClose() error {
	c.t.Helper()

	select {
	case err := <-c.errs:
		return err
	case <-time.After(10 * time.Second):
		c.t.Fatal("timed out waiting for the connection to close")
		return nil
	}
}

// createGroup runs SPEC §3.1 step 4 on a fresh unauthenticated connection.
func (h *harness) createGroup(t *testing.T, name string) (*client, *tppv1.CreateGroupResponse) {
	t.Helper()

	c := h.dial(t, "")
	c.send("create-1", tppv1.MessageType_MESSAGE_TYPE_CREATE_GROUP_REQUEST, &tppv1.CreateGroupRequest{
		Token:           h.token(t),
		DevicePublicKey: []byte("pk-" + name),
		WrappedGroupKey: []byte("wrapped-" + name),
		DeviceName:      name,
	})
	var resp tppv1.CreateGroupResponse
	c.await(tppv1.MessageType_MESSAGE_TYPE_CREATE_GROUP_RESPONSE, &resp)
	return c, &resp
}

// pair runs the whole of SPEC §3.2 and returns the joiner's connection and
// completion message.
func (h *harness) pair(t *testing.T, inviter *client, name string) (*client, *tppv1.PairingComplete) {
	t.Helper()

	inviter.send("pair-start", tppv1.MessageType_MESSAGE_TYPE_PAIRING_START_REQUEST, &tppv1.PairingStartRequest{})
	var start tppv1.PairingStartResponse
	inviter.await(tppv1.MessageType_MESSAGE_TYPE_PAIRING_START_RESPONSE, &start)
	if start.GetPairingToken() == "" || start.GetExpiresAtUnixMs() <= time.Now().UnixMilli() {
		t.Fatalf("PairingStartResponse = %+v, want a token with a future expiry", &start)
	}

	joiner := h.dial(t, "")
	joiner.send("pair-join", tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_REQUEST, &tppv1.PairingJoinRequest{
		PairingToken:    start.GetPairingToken(),
		DeviceName:      name,
		DevicePublicKey: []byte("pk-" + name),
	})

	var notice tppv1.PairingJoinNotice
	inviter.await(tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_NOTICE, &notice)
	if notice.GetDeviceName() != name || string(notice.GetDevicePublicKey()) != "pk-"+name {
		t.Fatalf("PairingJoinNotice = %+v, want the joiner's name and key echoed", &notice)
	}

	inviter.send("pair-key", tppv1.MessageType_MESSAGE_TYPE_PAIRING_WRAPPED_KEY_UPLOAD, &tppv1.PairingWrappedKeyUpload{
		PairingToken:    start.GetPairingToken(),
		DeviceId:        notice.GetDeviceId(),
		WrappedGroupKey: []byte("wrapped-for-" + name),
	})

	var complete tppv1.PairingComplete
	joiner.await(tppv1.MessageType_MESSAGE_TYPE_PAIRING_COMPLETE, &complete)
	return joiner, &complete
}

// TestProtocolEndToEnd is the acceptance path of ROADMAP P3: create a group,
// pair a second device, put an entry, have the second device read it, rekey,
// and watch the revoked device's socket close.
func TestProtocolEndToEnd(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	laptop, created := h.createGroup(t, "laptop")
	if created.GetGroupId() == "" || created.GetDeviceId() == "" || created.GetEpoch() != 1 {
		t.Fatalf("CreateGroupResponse = %+v, want ids and epoch 1", created)
	}

	phone, complete := h.pair(t, laptop, "phone")
	if complete.GetGroupId() != created.GetGroupId() || complete.GetEpoch() != 1 {
		t.Fatalf("PairingComplete = %+v, want group %q at epoch 1", complete, created.GetGroupId())
	}
	if string(complete.GetWrappedGroupKey()) != "wrapped-for-phone" {
		t.Errorf("PairingComplete.WrappedGroupKey = %q, want the blob the inviter uploaded", complete.GetWrappedGroupKey())
	}

	ciphertext := []byte("encrypted clipboard content")
	laptop.send("put-1", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		Epoch: 1,
		Size:  uint64(len(ciphertext)),
		Body:  &tppv1.EntryPutRequest_Ciphertext{Ciphertext: ciphertext},
	})
	var put tppv1.EntryPutResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_RESPONSE, &put)
	if !put.GetMeta().GetInline() || put.GetMeta().GetEpoch() != 1 {
		t.Errorf("EntryPutResponse.Meta = %+v, want an inline entry at epoch 1", put.GetMeta())
	}
	if got, want := put.GetMeta().GetExpiresAtUnixMs()-put.GetMeta().GetCreatedAtUnixMs(), (24 * time.Hour).Milliseconds(); got != want {
		t.Errorf("entry lifetime = %d ms, want %d ms exactly", got, want)
	}

	phone.send("latest-1", tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_REQUEST, &tppv1.EntryLatestRequest{})
	var latest tppv1.EntryLatestResponse
	phone.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_RESPONSE, &latest)
	if latest.GetMeta().GetEntryId() != put.GetMeta().GetEntryId() {
		t.Errorf("EntryLatestResponse.Meta.EntryId = %q, want %q", latest.GetMeta().GetEntryId(), put.GetMeta().GetEntryId())
	}
	if !bytes.Equal(latest.GetCiphertext(), ciphertext) {
		t.Errorf("EntryLatestResponse.Ciphertext = %q, want the bytes that were written", latest.GetCiphertext())
	}

	// The roster must be readable before anything may be revoked
	// (SPEC §3.3 steps 1-2).
	laptop.send("roster-1", tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_REQUEST, &tppv1.DeviceListRequest{})
	var roster tppv1.DeviceListResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_RESPONSE, &roster)
	if len(roster.GetDevices()) != 2 || roster.GetEpoch() != 1 {
		t.Fatalf("DeviceListResponse = %+v, want two devices at epoch 1", &roster)
	}
	for _, d := range roster.GetDevices() {
		if d.GetName() == "" {
			t.Error("DeviceListResponse carries a device with no name; the revocation dialog lists devices by name")
		}
	}

	laptop.send("rekey-1", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: complete.GetDeviceId(),
		ExpectedEpoch:   1,
		WrappedKeys: []*tppv1.WrappedKey{
			{DeviceId: created.GetDeviceId(), WrappedGroupKey: []byte("wrapped-laptop-epoch2")},
		},
	})

	var rekey tppv1.RekeyResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_REKEY_RESPONSE, &rekey)
	if rekey.GetEpoch() != 2 {
		t.Errorf("RekeyResponse.Epoch = %d, want 2", rekey.GetEpoch())
	}

	var epochChanged tppv1.EpochChanged
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_EPOCH_CHANGED, &epochChanged)
	if epochChanged.GetEpoch() != 2 || epochChanged.GetRevokedDeviceId() != complete.GetDeviceId() {
		t.Errorf("EpochChanged = %+v, want epoch 2 naming the revoked device", &epochChanged)
	}

	var newKey tppv1.WrappedKeyAvailable
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_WRAPPED_KEY_AVAILABLE, &newKey)
	if newKey.GetEpoch() != 2 || string(newKey.GetWrappedGroupKey()) != "wrapped-laptop-epoch2" {
		t.Errorf("WrappedKeyAvailable = {epoch %d}, want epoch 2 with the key the revoker uploaded", newKey.GetEpoch())
	}

	// SPEC §3.3 step 5: the socket goes down and the credential with it.
	if err := phone.awaitClose(); err == nil {
		t.Error("revoked device's connection stayed open, want it closed")
	}
	if _, _, err := websocket.Dial(context.Background(), h.url+"?device_id="+complete.GetDeviceId(), nil); err == nil {
		t.Error("revoked device could reconnect, want its credential invalidated")
	}

	// Entries written under the old epoch are not deleted; they expire
	// naturally (SPEC §3.3). The group simply refuses new ones at that epoch.
	laptop.send("put-stale", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		Epoch: 1,
		Size:  uint64(len(ciphertext)),
		Body:  &tppv1.EntryPutRequest_Ciphertext{Ciphertext: ciphertext},
	})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT {
		t.Errorf("put at a stale epoch = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT)
	}
}

func TestUnauthenticatedConnectionIsLimitedToTwoFrames(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c := h.dial(t, "")

	// Everything but group creation and pairing-join needs an identity.
	for _, typ := range []tppv1.MessageType{
		tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_REQUEST,
		tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_REQUEST,
		tppv1.MessageType_MESSAGE_TYPE_PAIRING_START_REQUEST,
		tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST,
	} {
		c.send("unauth", typ, &tppv1.DeviceListRequest{})
		if got := c.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED {
			t.Errorf("%s on an unauthenticated connection = %s, want %s", typ, got.GetCode(), tppv1.ErrorCode_ERROR_CODE_UNAUTHENTICATED)
		}
	}
}

func TestUnknownDeviceCannotConnect(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, resp, err := websocket.Dial(context.Background(), h.url+"?device_id=not-a-device", nil)
	if err == nil {
		t.Fatal("dial with an unknown device id succeeded, want it refused")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestTokenIsConsumedExactlyOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	token := h.token(t)

	first := h.dial(t, "")
	first.send("create", tppv1.MessageType_MESSAGE_TYPE_CREATE_GROUP_REQUEST, &tppv1.CreateGroupRequest{
		Token: token, DevicePublicKey: []byte("pk"), WrappedGroupKey: []byte("wrapped"), DeviceName: "one",
	})
	first.await(tppv1.MessageType_MESSAGE_TYPE_CREATE_GROUP_RESPONSE, nil)

	second := h.dial(t, "")
	second.send("create", tppv1.MessageType_MESSAGE_TYPE_CREATE_GROUP_REQUEST, &tppv1.CreateGroupRequest{
		Token: token, DevicePublicKey: []byte("pk"), WrappedGroupKey: []byte("wrapped"), DeviceName: "two",
	})
	if got := second.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_TOKEN_CONSUMED {
		t.Errorf("second CreateGroupRequest = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_TOKEN_CONSUMED)
	}

	third := h.dial(t, "")
	third.send("create", tppv1.MessageType_MESSAGE_TYPE_CREATE_GROUP_REQUEST, &tppv1.CreateGroupRequest{
		Token: "no-such-token", DevicePublicKey: []byte("pk"), WrappedGroupKey: []byte("wrapped"),
	})
	if got := third.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_NOT_FOUND {
		t.Errorf("CreateGroupRequest with an unknown token = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_NOT_FOUND)
	}
}

func TestPairingTokenIsSingleUse(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, _ := h.createGroup(t, "laptop")

	laptop.send("pair-start", tppv1.MessageType_MESSAGE_TYPE_PAIRING_START_REQUEST, &tppv1.PairingStartRequest{})
	var start tppv1.PairingStartResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_PAIRING_START_RESPONSE, &start)

	join := func(c *client) {
		c.send("join", tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_REQUEST, &tppv1.PairingJoinRequest{
			PairingToken: start.GetPairingToken(), DeviceName: "joiner", DevicePublicKey: []byte("pk"),
		})
	}

	first := h.dial(t, "")
	join(first)
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_NOTICE, nil)

	second := h.dial(t, "")
	join(second)
	if got := second.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_TOKEN_CONSUMED {
		t.Errorf("second join = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_TOKEN_CONSUMED)
	}

	// The losing joiner must not have been left in the group.
	laptop.send("roster", tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_REQUEST, &tppv1.DeviceListRequest{})
	var roster tppv1.DeviceListResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_RESPONSE, &roster)
	if len(roster.GetDevices()) != 2 {
		t.Errorf("roster has %d devices, want 2: a rejected join must leave nothing behind", len(roster.GetDevices()))
	}

	unknown := h.dial(t, "")
	unknown.send("join", tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_REQUEST, &tppv1.PairingJoinRequest{
		PairingToken: "no-such-pairing", DeviceName: "x", DevicePublicKey: []byte("pk"),
	})
	if got := unknown.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_TOKEN_EXPIRED {
		t.Errorf("join with an unknown token = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_TOKEN_EXPIRED)
	}
}

func TestEntrySizePaths(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, _ := h.createGroup(t, "laptop")

	tests := []struct {
		name       string
		size       int
		wantInline bool
	}{
		{name: "inline", size: testInlineMax, wantInline: true},
		{name: "blob backend", size: testInlineMax + 1, wantInline: false},
		{name: "at the cap", size: testMaxBytes, wantInline: false},
	}

	for _, tt := range tests {
		body := bytes.Repeat([]byte("c"), tt.size)
		laptop.send("put-"+tt.name, tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
			Epoch: 1, Size: uint64(tt.size),
			Body: &tppv1.EntryPutRequest_Ciphertext{Ciphertext: body},
		})
		var put tppv1.EntryPutResponse
		laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_RESPONSE, &put)
		if put.GetMeta().GetInline() != tt.wantInline {
			t.Errorf("%s: EntryPutResponse.Meta.Inline = %v, want %v", tt.name, put.GetMeta().GetInline(), tt.wantInline)
		}

		// Whatever the storage, a fetch returns the ciphertext: where an entry
		// lives is the server's problem, not the client's.
		laptop.send("fetch-"+tt.name, tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_REQUEST, &tppv1.EntryFetchRequest{
			EntryId: put.GetMeta().GetEntryId(),
		})
		var fetched tppv1.EntryFetchResponse
		laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_RESPONSE, &fetched)
		if !bytes.Equal(fetched.GetCiphertext(), body) {
			t.Errorf("%s: fetched %d bytes, want %d", tt.name, len(fetched.GetCiphertext()), len(body))
		}

		// A non-inline latest carries metadata only (SPEC §4.3).
		laptop.send("latest-"+tt.name, tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_REQUEST, &tppv1.EntryLatestRequest{})
		var latest tppv1.EntryLatestResponse
		laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_RESPONSE, &latest)
		if got := len(latest.GetCiphertext()) > 0; got != tt.wantInline {
			t.Errorf("%s: EntryLatestResponse carried a body = %v, want %v", tt.name, got, tt.wantInline)
		}
	}

	laptop.send("put-oversize", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		Epoch: 1, Size: testMaxBytes + 1,
		Body: &tppv1.EntryPutRequest_Ciphertext{Ciphertext: bytes.Repeat([]byte("c"), testMaxBytes+1)},
	})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_TOO_LARGE {
		t.Errorf("oversize put = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_TOO_LARGE)
	}

	laptop.send("put-handle", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		Epoch: 1, Size: 10,
		Body: &tppv1.EntryPutRequest_UploadHandle{UploadHandle: "reserved"},
	})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE {
		t.Errorf("put with an upload handle = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE)
	}
}

func TestHistoryIsPulledNewestFirst(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, _ := h.createGroup(t, "laptop")

	var ids []string
	for i := range 3 {
		body := []byte{byte('a' + i)}
		laptop.send("put", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
			Epoch: 1, Size: 1, Body: &tppv1.EntryPutRequest_Ciphertext{Ciphertext: body},
		})
		var put tppv1.EntryPutResponse
		laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_RESPONSE, &put)
		ids = append(ids, put.GetMeta().GetEntryId())
	}

	laptop.send("history", tppv1.MessageType_MESSAGE_TYPE_ENTRY_HISTORY_REQUEST, &tppv1.EntryHistoryRequest{Limit: 2})
	var page tppv1.EntryHistoryResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_HISTORY_RESPONSE, &page)
	if len(page.GetEntries()) != 2 {
		t.Fatalf("history page has %d entries, want 2", len(page.GetEntries()))
	}
	if page.GetEntries()[0].GetEntryId() != ids[2] {
		t.Errorf("history[0] = %q, want the newest entry %q", page.GetEntries()[0].GetEntryId(), ids[2])
	}
	if page.GetNextBeforeUnixMs() == 0 {
		t.Error("history returned no cursor after a full page, want one to continue from")
	}
}

func TestUnsupportedAndMalformedFrames(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, _ := h.createGroup(t, "laptop")

	// A type the server does not handle is answered, not met with a closed
	// socket (SPEC §5.1).
	laptop.send("push-back", tppv1.MessageType_MESSAGE_TYPE_EPOCH_CHANGED, &tppv1.EpochChanged{Epoch: 9})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE {
		t.Errorf("event sent to the server = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := laptop.sock.Write(ctx, websocket.MessageBinary, []byte{0xff, 0xff, 0xff}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Errorf("undecodable envelope = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}

	// The connection survives both: one bad frame is not a reason to hang up.
	laptop.send("roster", tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_REQUEST, &tppv1.DeviceListRequest{})
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_RESPONSE, nil)
}

func TestRekeyRejectsStaleEpochAndSelfRevocation(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, created := h.createGroup(t, "laptop")
	_, complete := h.pair(t, laptop, "phone")

	laptop.send("rekey-stale", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: complete.GetDeviceId(),
		ExpectedEpoch:   7,
		WrappedKeys:     []*tppv1.WrappedKey{{DeviceId: created.GetDeviceId(), WrappedGroupKey: []byte("k")}},
	})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT {
		t.Errorf("rekey at a stale epoch = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_EPOCH_CONFLICT)
	}

	laptop.send("rekey-self", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: created.GetDeviceId(),
		ExpectedEpoch:   1,
		WrappedKeys:     []*tppv1.WrappedKey{{DeviceId: complete.GetDeviceId(), WrappedGroupKey: []byte("k")}},
	})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Errorf("self-revocation = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}

	laptop.send("rekey-short", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: complete.GetDeviceId(),
		ExpectedEpoch:   1,
	})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Errorf("rekey missing a remaining device's key = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}

	// None of the rejections may have moved the group.
	group, err := h.store.GetGroup(context.Background(), created.GetGroupId())
	if err != nil {
		t.Fatalf("GetGroup() error = %v", err)
	}
	if group.Epoch != 1 {
		t.Errorf("epoch after three rejected rekeys = %d, want 1", group.Epoch)
	}
}

// TestOfflineDeviceCollectsItsWrappedKeyOnConnect covers the half of SPEC §3.3
// that has no live socket: a device that missed the rekey must find its key
// waiting rather than be locked out.
func TestOfflineDeviceCollectsItsWrappedKeyOnConnect(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, created := h.createGroup(t, "laptop")
	phone, phoneComplete := h.pair(t, laptop, "phone")
	tablet, tabletComplete := h.pair(t, laptop, "tablet")

	// The tablet goes away before the revocation happens.
	if err := tablet.sock.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("closing the tablet connection: %v", err)
	}

	laptop.send("rekey", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: phoneComplete.GetDeviceId(),
		ExpectedEpoch:   1,
		WrappedKeys: []*tppv1.WrappedKey{
			{DeviceId: created.GetDeviceId(), WrappedGroupKey: []byte("laptop-e2")},
			{DeviceId: tabletComplete.GetDeviceId(), WrappedGroupKey: []byte("tablet-e2")},
		},
	})
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_REKEY_RESPONSE, nil)
	if err := phone.awaitClose(); err == nil {
		t.Error("revoked device's connection stayed open, want it closed")
	}

	reconnected := h.dial(t, tabletComplete.GetDeviceId())
	var key tppv1.WrappedKeyAvailable
	reconnected.await(tppv1.MessageType_MESSAGE_TYPE_WRAPPED_KEY_AVAILABLE, &key)
	if key.GetEpoch() != 2 || string(key.GetWrappedGroupKey()) != "tablet-e2" {
		t.Errorf("WrappedKeyAvailable on connect = {epoch %d}, want epoch 2 with the tablet's key", key.GetEpoch())
	}
}

func TestSecondConnectionReplacesTheFirst(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, created := h.createGroup(t, "laptop")

	replacement := h.dial(t, created.GetDeviceId())
	if err := laptop.awaitClose(); err == nil {
		t.Error("the displaced connection stayed open, want it closed")
	}

	replacement.send("roster", tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_REQUEST, &tppv1.DeviceListRequest{})
	replacement.await(tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_RESPONSE, nil)
}

func TestGroupsAreIsolated(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	one, _ := h.createGroup(t, "one")
	two, _ := h.createGroup(t, "two")

	body := []byte("secret")
	one.send("put", tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_REQUEST, &tppv1.EntryPutRequest{
		Epoch: 1, Size: uint64(len(body)), Body: &tppv1.EntryPutRequest_Ciphertext{Ciphertext: body},
	})
	var put tppv1.EntryPutResponse
	one.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_PUT_RESPONSE, &put)

	two.send("fetch", tppv1.MessageType_MESSAGE_TYPE_ENTRY_FETCH_REQUEST, &tppv1.EntryFetchRequest{
		EntryId: put.GetMeta().GetEntryId(),
	})
	if got := two.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_NOT_FOUND {
		t.Errorf("cross-group fetch = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_NOT_FOUND)
	}

	two.send("latest", tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_REQUEST, &tppv1.EntryLatestRequest{})
	var latest tppv1.EntryLatestResponse
	two.await(tppv1.MessageType_MESSAGE_TYPE_ENTRY_LATEST_RESPONSE, &latest)
	if latest.GetMeta() != nil {
		t.Error("a second group saw another group's entry as its latest")
	}
}
