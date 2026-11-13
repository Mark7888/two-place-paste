package tppclient

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
)

const refusalBody = `{"error":"client_outdated","protocol_min":2,"protocol_max":3}`

func TestRefusalForAgeIsToldByItsBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "the relay's refusal", status: http.StatusUpgradeRequired, body: refusalBody, want: true},
		// What the WebSocket library answers any plain GET with.
		{name: "a bare 426", status: http.StatusUpgradeRequired, body: "WebSocket protocol violation", want: false},
		{name: "another JSON error", status: http.StatusUpgradeRequired, body: `{"error":"something_else"}`, want: false},
		{name: "the body on another status", status: http.StatusUnauthorized, body: refusalBody, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body))}
			got, ok := refusalForAge(resp)
			if ok != tt.want {
				t.Fatalf("refusalForAge = %v, want %v", ok, tt.want)
			}
			if ok && (got.ProtocolMin != 2 || got.ProtocolMax != 3) {
				t.Errorf("refusal = [%d, %d], want [2, 3]", got.ProtocolMin, got.ProtocolMax)
			}
		})
	}
}

// TestRefusalForAgeIsNotARevocation covers the relay that no longer serves
// this build: the client says so, keeps its identity, does not hammer the
// relay, and connects as soon as it is let in.
func TestRefusalForAgeIsNotARevocation(t *testing.T) {
	t.Parallel()

	var (
		dials   atomic.Int32
		letInMu sync.Mutex
		letIn   bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dials.Add(1)
		if got := r.URL.Query().Get("proto"); got != "1" {
			t.Errorf("proto = %q, want %q", got, "1")
		}
		letInMu.Lock()
		in := letIn
		letInMu.Unlock()
		if !in {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = io.WriteString(w, refusalBody)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		for {
			if _, _, err := ws.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	var outdated, revoked atomic.Int32
	connected := make(chan struct{}, 4)
	c, err := New(Options{
		ServerURL:  srv.URL,
		Logger:     slog.New(slog.DiscardHandler),
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 10 * time.Millisecond,
		Handlers: Handlers{
			OnClientOutdated: func() { outdated.Add(1) },
			OnRevoked:        func() { revoked.Add(1) },
			OnConnected:      func() { connected <- struct{}{} },
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.state.GroupID = "group"
	c.state.DeviceID = "device"
	c.state.GroupKey = make([]byte, 32)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = c.Connect(ctx)
	if !errors.Is(err, ErrClientOutdated) {
		t.Fatalf("Connect = %v, want ErrClientOutdated", err)
	}
	var oe *OutdatedError
	if !errors.As(err, &oe) || oe.ProtocolMin != 2 {
		t.Errorf("Connect = %v, want an OutdatedError naming level 2", err)
	}
	if errors.Is(err, ErrRevoked) {
		t.Errorf("Connect = %v, which reads as a revocation", err)
	}
	if !c.Outdated() || outdated.Load() != 1 || revoked.Load() != 0 {
		t.Errorf("Outdated = %v, OnClientOutdated × %d, OnRevoked × %d; want true, 1, 0",
			c.Outdated(), outdated.Load(), revoked.Load())
	}
	// A request does not wait an hour for the next attempt.
	if _, err := c.connection(ctx); !errors.Is(err, ErrClientOutdated) {
		t.Errorf("connection = %v, want ErrClientOutdated at once", err)
	}

	// The app is updated (here: the relay lets it in) and the shell asks.
	letInMu.Lock()
	letIn = true
	letInMu.Unlock()
	c.Reconnect()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("no connection after Reconnect")
	}
	if c.Outdated() {
		t.Error("Outdated after a connection succeeded, want false")
	}
	// One refusal and one success: with a 10ms backoff, retrying a refusal on
	// the normal schedule would have dialled far more often.
	if n := dials.Load(); n != 2 {
		t.Errorf("%d dials, want 2", n)
	}
}

// TestHelloFromAnOldRelay covers a relay that predates HelloRequest: it
// answers that it does not know the message, and that means level 1.
func TestHelloFromAnOldRelay(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		for {
			_, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			var env tppv1.Envelope
			if proto.Unmarshal(data, &env) != nil {
				continue
			}
			payload, _ := proto.Marshal(&tppv1.Error{Code: tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE})
			frame, _ := proto.Marshal(&tppv1.Envelope{Id: env.GetId(), Type: tppv1.MessageType_MESSAGE_TYPE_ERROR, Payload: payload})
			if err := ws.Write(r.Context(), websocket.MessageBinary, frame); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	c, err := New(Options{ServerURL: srv.URL, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.state.GroupID = "group"
	c.state.DeviceID = "device"
	c.state.GroupKey = make([]byte, 32)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	got, err := c.Relay(ctx)
	if err != nil {
		t.Fatalf("Relay: %v", err)
	}
	if got.ProtocolMin != 1 || got.ProtocolMax != 1 || len(got.Capabilities) != 0 {
		t.Errorf("Relay = %+v, want level 1 with no capabilities", got)
	}
}

func TestCodeFromANewerApp(t *testing.T) {
	t.Parallel()

	// A future kind of code: a PairingCode member this build does not know.
	member, err := proto.Marshal(&tppv1.PairingOffer{ServerUrl: "https://relay.example.com", OfferCode: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	newer := protowire.AppendBytes(protowire.AppendTag(nil, 9, protowire.BytesType), member)

	tests := []struct {
		name  string
		raw   []byte
		newer bool
	}{
		{name: "a new kind of code", raw: newer, newer: true},
		{name: "an empty wrapper", raw: nil, newer: false},
		{name: "a stray varint", raw: protowire.AppendVarint(protowire.AppendTag(nil, 9, protowire.VarintType), 1), newer: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := DecodeCode(base64.RawURLEncoding.EncodeToString(tt.raw))
			if err == nil {
				t.Fatal("DecodeCode succeeded, want an error")
			}
			if got := errors.Is(err, ErrCodeFromNewerApp); got != tt.newer {
				t.Errorf("DecodeCode = %v; ErrCodeFromNewerApp = %v, want %v", err, got, tt.newer)
			}
		})
	}
}
