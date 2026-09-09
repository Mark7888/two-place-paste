package ws

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestSendDropsASlowClient is the backpressure rule: the write queue is
// bounded, and a client that stops draining it is disconnected rather than
// buffered without limit (ROADMAP P3, docs/conventions.md §8).
//
// The write pump is deliberately not started, which is exactly what a client
// that has stopped reading looks like from the server's side.
func TestSendDropsASlowClient(t *testing.T) {
	t.Parallel()

	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sock, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("Accept() error = %v", err)
			return
		}
		accepted <- sock
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientSock, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):], nil)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { _ = clientSock.CloseNow() })

	var serverSock *websocket.Conn
	select {
	case serverSock = <-accepted:
	case <-time.After(10 * time.Second):
		t.Fatal("connection was never accepted")
	}

	c := newConn(serverSock, slog.New(slog.DiscardHandler))
	for i := range writeQueueDepth {
		if !c.send([]byte("frame")) {
			t.Fatalf("send() = false on frame %d, want the queue to accept %d frames", i+1, writeQueueDepth)
		}
	}

	if c.send([]byte("one too many")) {
		t.Error("send() = true past the queue depth, want the connection dropped instead")
	}
	select {
	case <-c.closed:
	case <-time.After(10 * time.Second):
		t.Error("the overflowing connection was not closed")
	}
	if c.send([]byte("after close")) {
		t.Error("send() = true on a closed connection, want false")
	}
}
