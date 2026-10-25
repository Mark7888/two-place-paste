package ws_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/server/internal/blob"
	"github.com/Mark7888/two-place-paste/server/internal/entries"
	"github.com/Mark7888/two-place-paste/server/internal/items"
	"github.com/Mark7888/two-place-paste/server/internal/store"
	"github.com/Mark7888/two-place-paste/server/internal/ws"
)

// testStoreConfig is a small store: the limits are reachable in a test.
var testStoreConfig = items.Config{
	MaxTotalBytes: 0,
	MaxGroupBytes: 64 << 10,
	MaxItemBytes:  testMaxBytes,
	MaxGroupItems: 4,
}

// storeHarness is a harness with the store wired in, the way cmd/tpp wires
// it.
type storeHarness struct {
	*harness
	items *items.Service
}

func newStoreHarness(t *testing.T, cfg items.Config) *storeHarness {
	t.Helper()

	st := store.NewMemoryStore()
	blobs := blob.NewDisk(t.TempDir(), testMaxBytes)
	svc := entries.NewService(entries.NewMemoryStore(), blobs, entries.Options{
		InlineMaxBytes:      testInlineMax,
		MaxBytes:            testMaxBytes,
		SingleFrameMaxBytes: testFrameMax,
		ChunkBytes:          testChunk,
		Logger:              slog.New(slog.DiscardHandler),
	})
	cfg.ChunkBytes = testChunk
	cfg.SingleFrameMaxBytes = testFrameMax
	var server *ws.Server
	storeSvc := items.New(items.Options{
		Registry: st,
		Blobs:    blobs,
		Config:   cfg,
		Logger:   slog.New(slog.DiscardHandler),
		Hooks: items.Hooks{
			OnSaved:   func(ctx context.Context, g, d string, it store.Item) { server.AnnounceItemSaved(ctx, g, d, it) },
			OnUpdated: func(ctx context.Context, g, d string, it store.Item) { server.AnnounceItemUpdated(ctx, g, d, it) },
			OnDeleted: func(ctx context.Context, g, d, id string) { server.AnnounceItemDeleted(ctx, g, d, id) },
		},
	})
	server = ws.New(st, svc, ws.Options{Logger: slog.New(slog.DiscardHandler), Items: storeSvc})

	mux := http.NewServeMux()
	server.Register(mux)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	return &storeHarness{
		harness: &harness{
			url:     "ws" + strings.TrimPrefix(httpSrv.URL, "http") + ws.DefaultPath,
			store:   st,
			entries: svc,
			server:  server,
		},
		items: storeSvc,
	}
}

// storeLimits asks for the caller's store limits.
func storeLimits(c *client) *tppv1.StoreLimitsResponse {
	c.t.Helper()
	c.send("store-limits", tppv1.MessageType_MESSAGE_TYPE_STORE_LIMITS_REQUEST, &tppv1.StoreLimitsRequest{})
	var resp tppv1.StoreLimitsResponse
	c.await(tppv1.MessageType_MESSAGE_TYPE_STORE_LIMITS_RESPONSE, &resp)
	return &resp
}

// TestStoreLimitsFollowTheSwitch: a store is off until the admin turns it
// on; the limits say so either way, and an online device hears of the switch
// (SPEC §4.6).
func TestStoreLimitsFollowTheSwitch(t *testing.T) {
	t.Parallel()

	h := newStoreHarness(t, testStoreConfig)
	laptop, created := h.createGroup(t, "laptop")
	phone, _ := h.pair(t, laptop, "phone")

	if got := storeLimits(laptop); got.GetEnabled() || got.GetMaxBytes() != 0 {
		t.Errorf("StoreLimitsResponse with the store off = %+v, want disabled and zeros", got)
	}

	ctx := context.Background()
	if err := h.store.SetGroupStore(ctx, created.GetGroupId(), true); err != nil {
		t.Fatalf("SetGroupStore() error = %v", err)
	}
	h.server.AnnounceStoreState(ctx, created.GetGroupId(), true)
	var state tppv1.StoreStateChanged
	phone.await(tppv1.MessageType_MESSAGE_TYPE_STORE_STATE_CHANGED, &state)
	if !state.GetEnabled() {
		t.Error("StoreStateChanged.Enabled = false, want true")
	}

	got := storeLimits(laptop)
	want := &tppv1.StoreLimitsResponse{
		Enabled:        true,
		MaxBytes:       64 << 10,
		AvailableBytes: testMaxBytes,
		MaxItems:       4,
		MaxItemBytes:   testMaxBytes,
	}
	if got.String() != want.String() {
		t.Errorf("StoreLimitsResponse = %v, want %v", got, want)
	}
}

// TestRelayWithoutAStore: a relay built without the store answers its
// requests as unknown, exactly as one that predates the store.
func TestRelayWithoutAStore(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	c, _ := h.createGroup(t, "laptop")
	c.send("store-limits", tppv1.MessageType_MESSAGE_TYPE_STORE_LIMITS_REQUEST, &tppv1.StoreLimitsRequest{})
	if got := c.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE {
		t.Errorf("StoreLimitsRequest error = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE)
	}
}

// rekey revokes victim from the caller's group, sending link as the store
// link, and returns the frame the relay answered with.
func rekey(c *client, victim, keeper string, epoch uint64, link []byte) *tppv1.Envelope {
	c.t.Helper()
	c.send("rekey", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: victim,
		ExpectedEpoch:   epoch,
		WrappedKeys:     []*tppv1.WrappedKey{{DeviceId: keeper, WrappedGroupKey: []byte("next")}},
		StoreLink:       link,
	})
	return c.await(tppv1.MessageType_MESSAGE_TYPE_REKEY_RESPONSE, nil)
}

// TestStoreLinksComeFromRekeys: the link a rekey carries is kept for the new
// epoch and listed, oldest first; a link of the wrong length is refused; and
// like every store request but the limits, the list is refused while the
// store is off (/spec/crypto.md §7a.2).
func TestStoreLinksComeFromRekeys(t *testing.T) {
	t.Parallel()

	h := newStoreHarness(t, testStoreConfig)
	laptop, created := h.createGroup(t, "laptop")
	_, phone := h.pair(t, laptop, "phone")
	_, tablet := h.pair(t, laptop, "tablet")

	laptop.send("bad", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: phone.GetDeviceId(),
		ExpectedEpoch:   1,
		WrappedKeys: []*tppv1.WrappedKey{
			{DeviceId: created.GetDeviceId(), WrappedGroupKey: []byte("next")},
			{DeviceId: tablet.GetDeviceId(), WrappedGroupKey: []byte("next")},
		},
		StoreLink: make([]byte, 48),
	})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT {
		t.Errorf("rekey with a 48-byte link = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_INVALID_ARGUMENT)
	}

	link2 := []byte(strings.Repeat("2", 49))
	laptop.send("rekey-2", tppv1.MessageType_MESSAGE_TYPE_REKEY_REQUEST, &tppv1.RekeyRequest{
		RevokedDeviceId: phone.GetDeviceId(),
		ExpectedEpoch:   1,
		WrappedKeys: []*tppv1.WrappedKey{
			{DeviceId: created.GetDeviceId(), WrappedGroupKey: []byte("next")},
			{DeviceId: tablet.GetDeviceId(), WrappedGroupKey: []byte("next")},
		},
		StoreLink: link2,
	})
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_REKEY_RESPONSE, nil)
	link3 := []byte(strings.Repeat("3", 49))
	rekey(laptop, tablet.GetDeviceId(), created.GetDeviceId(), 2, link3)

	laptop.send("links-off", tppv1.MessageType_MESSAGE_TYPE_STORE_LINKS_REQUEST, &tppv1.StoreLinksRequest{})
	if got := laptop.awaitError(); got.GetCode() != tppv1.ErrorCode_ERROR_CODE_STORE_DISABLED {
		t.Errorf("StoreLinksRequest with the store off = %s, want %s", got.GetCode(), tppv1.ErrorCode_ERROR_CODE_STORE_DISABLED)
	}

	if err := h.store.SetGroupStore(context.Background(), created.GetGroupId(), true); err != nil {
		t.Fatalf("SetGroupStore() error = %v", err)
	}
	laptop.send("links", tppv1.MessageType_MESSAGE_TYPE_STORE_LINKS_REQUEST, &tppv1.StoreLinksRequest{})
	var resp tppv1.StoreLinksResponse
	laptop.await(tppv1.MessageType_MESSAGE_TYPE_STORE_LINKS_RESPONSE, &resp)
	got := resp.GetLinks()
	if len(got) != 2 || got[0].GetEpoch() != 2 || string(got[0].GetLink()) != string(link2) ||
		got[1].GetEpoch() != 3 || string(got[1].GetLink()) != string(link3) {
		t.Errorf("StoreLinksResponse = %v, want the links into epochs 2 and 3", got)
	}
}
