package ws_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/coder/websocket"
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
