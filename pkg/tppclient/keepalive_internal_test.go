package tppclient

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestUnansweredPingEndsTheConnection covers the relay that went away without
// a word: nothing arrives, nothing fails, and only a ping notices.
func TestUnansweredPingEndsTheConnection(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		// Never reads, so never answers a ping: a peer that is gone.
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialConn(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	go conn.keepalive(ctx, 20*time.Millisecond, 20*time.Millisecond)

	err = conn.readLoop(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "stopped answering pings") {
		t.Errorf("readLoop ended with %v, want the ping failure", err)
	}
}

// TestBackoffResetsAfterAConnection covers the relay that is reachable but
// whose connections keep being cut, which is what an idle timeout on a proxy
// looks like. Each drop follows a working connection, so each reconnect is
// prompt; the backoff is for dials that fail.
func TestBackoffResetsAfterAConnection(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		accept []time.Time
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		accept = append(accept, time.Now())
		mu.Unlock()
		// Dropped without a close frame, as a proxy does.
		_ = ws.CloseNow()
	}))
	t.Cleanup(srv.Close)

	c, err := New(Options{
		ServerURL:  srv.URL,
		Logger:     slog.New(slog.DiscardHandler),
		MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 5 * time.Second,
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
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Without the reset the ninth connection would come 10ms·(2⁸−1) ≈ 2.5s
	// after the first, 2s at the least jitter; with it, all nine fit well
	// inside a second.
	const want = 9
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		n := len(accept)
		mu.Unlock()
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections in a second, want %d: the backoff is not reset by a connection that worked", n, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
