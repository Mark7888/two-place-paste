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
	svc := entries.NewService(entries.NewMemoryStore(), blob.NewDisk(t.TempDir(), testMaxBytes), entries.Options{
		InlineMaxBytes:      testInlineMax,
		MaxBytes:            testMaxBytes,
		SingleFrameMaxBytes: testFrameMax,
		ChunkBytes:          testChunk,
		Logger:              slog.New(slog.DiscardHandler),
	})
	storeSvc := items.New(items.Options{Registry: st, Config: cfg, Logger: slog.New(slog.DiscardHandler)})
	server := ws.New(st, svc, ws.Options{Logger: slog.New(slog.DiscardHandler), Items: storeSvc})

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
