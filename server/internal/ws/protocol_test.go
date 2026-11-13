package ws_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/coder/websocket"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/server/internal/ws"
)

func TestProtocolLevelIsRecordedOnConnect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		proto string
		want  uint32
	}{
		{name: "an app that predates levels", proto: "", want: 1},
		{name: "level 1", proto: "1", want: 1},
		{name: "a level above the relay's", proto: "7", want: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			_, created := h.createGroup(t, "laptop")

			query := url.Values{"device_id": {created.GetDeviceId()}}
			if tt.proto != "" {
				query.Set("proto", tt.proto)
			}
			h.dialQuery(t, query)

			waitFor(t, func() bool {
				dev, err := h.store.GetDevice(context.Background(), created.GetDeviceId())
				return err == nil && dev.ProtocolLevel != 0
			})
			dev, err := h.store.GetDevice(context.Background(), created.GetDeviceId())
			if err != nil {
				t.Fatalf("GetDevice() error = %v", err)
			}
			if dev.ProtocolLevel != tt.want {
				t.Errorf("ProtocolLevel = %d, want %d", dev.ProtocolLevel, tt.want)
			}
		})
	}
}

func TestMalformedProtocolLevelIsRefused(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"0", "-1", "two", "4294967296"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			_, resp, err := websocket.Dial(context.Background(), h.url+"?proto="+url.QueryEscape(raw), nil)
			if err == nil {
				t.Fatal("dial succeeded, want it refused")
			}
			if resp == nil || resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %v, want %d", statusOf(resp), http.StatusBadRequest)
			}
		})
	}
}

func statusOf(resp *http.Response) any {
	if resp == nil {
		return "no response"
	}
	return resp.StatusCode
}

func TestAppBelowTheMinimumLevelIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, created := h.createGroup(t, "laptop")
	h.server.SetProtocolMin(2)

	tests := []struct {
		name  string
		query url.Values
	}{
		{name: "an app that predates levels", query: url.Values{"device_id": {created.GetDeviceId()}}},
		{name: "level 1", query: url.Values{"device_id": {created.GetDeviceId()}, "proto": {"1"}}},
		{name: "unauthenticated, about to pair", query: url.Values{"proto": {"1"}}},
		// The answer does not depend on the record: an unknown device is not
		// told it is unknown before it is told to update.
		{name: "an unknown device", query: url.Values{"device_id": {"not-a-device"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			endpoint := h.url + "?" + tt.query.Encode()
			_, resp, err := websocket.Dial(context.Background(), endpoint, nil)
			if err == nil {
				t.Fatal("dial succeeded, want it refused")
			}
			assertOutdated(t, resp)

			// The plain GET Android asks again with gets the same answer.
			get, err := http.Get("http" + strings.TrimPrefix(endpoint, "ws"))
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer get.Body.Close()
			assertOutdated(t, get)
		})
	}

	// An app at the minimum still connects.
	c := h.dialQuery(t, url.Values{"device_id": {created.GetDeviceId()}, "proto": {"2"}})
	c.send("list", tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_REQUEST, &tppv1.DeviceListRequest{})
	c.await(tppv1.MessageType_MESSAGE_TYPE_DEVICE_LIST_RESPONSE, nil)
}

// TestPlainGetOfAnAcceptedAppIsNotARefusal pins the reason the refusal is
// recognised by its body: the WebSocket library answers 426 to every plain
// GET, and that one must not read as "update the app".
func TestPlainGetOfAnAcceptedAppIsNotARefusal(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, created := h.createGroup(t, "laptop")

	get, err := http.Get("http" + strings.TrimPrefix(h.url, "ws") + "?device_id=" + created.GetDeviceId())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer get.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(get.Body).Decode(&body)
	if body.Error == "client_outdated" {
		t.Errorf("plain GET of an accepted app = %d with a client_outdated body, want no refusal", get.StatusCode)
	}
}

func assertOutdated(t *testing.T, resp *http.Response) {
	t.Helper()

	if resp == nil {
		t.Fatal("no response, want a 426")
	}
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUpgradeRequired)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Error       string `json:"error"`
		ProtocolMin uint32 `json:"protocol_min"`
		ProtocolMax uint32 `json:"protocol_max"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding the body: %v", err)
	}
	if body.Error != "client_outdated" || body.ProtocolMin != 2 || body.ProtocolMax != ws.ProtocolMax {
		t.Errorf("body = %+v, want client_outdated, min 2, max %d", body, ws.ProtocolMax)
	}
}

func TestHelloStatesTheRelaysLevels(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	laptop, _ := h.createGroup(t, "laptop")

	tests := []struct {
		name string
		c    *client
	}{
		{name: "authenticated", c: laptop},
		// A device about to pair has no identity yet and needs the answer most.
		{name: "unauthenticated", c: h.dial(t, "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.c.send("hello", tppv1.MessageType_MESSAGE_TYPE_HELLO_REQUEST, &tppv1.HelloRequest{})
			var got tppv1.HelloResponse
			env := tt.c.await(tppv1.MessageType_MESSAGE_TYPE_HELLO_RESPONSE, &got)
			if env.GetId() != "hello" {
				t.Errorf("response id = %q, want %q", env.GetId(), "hello")
			}
			if got.GetProtocolMin() != ws.ProtocolMin || got.GetProtocolMax() != ws.ProtocolMax {
				t.Errorf("HelloResponse = [%d, %d], want [%d, %d]",
					got.GetProtocolMin(), got.GetProtocolMax(), ws.ProtocolMin, ws.ProtocolMax)
			}
		})
	}
}
