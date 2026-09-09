package ws

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/coder/websocket"
)

// writeQueueDepth bounds how far a connection may fall behind before it is
// dropped. A slow client is disconnected, never buffered without limit
// (ROADMAP P3 backpressure, docs/conventions.md §8): it reconnects and pulls
// what it missed, which is cheaper than letting one stalled socket consume the
// server's memory.
const writeQueueDepth = 32

// conn is one WebSocket connection and the identity it has established.
type conn struct {
	ws     *websocket.Conn
	logger *slog.Logger

	// out is the bounded write queue. Every frame leaves through the single
	// write pump goroutine, so the underlying socket has exactly one writer.
	out chan []byte

	// mu guards the identity fields, which the pairing and creation handlers
	// set after the connection is already serving.
	mu       sync.Mutex
	deviceID string
	groupID  string

	closeOnce sync.Once
	closed    chan struct{}
}

func newConn(ws *websocket.Conn, logger *slog.Logger) *conn {
	return &conn{
		ws:     ws,
		logger: logger,
		out:    make(chan []byte, writeQueueDepth),
		closed: make(chan struct{}),
	}
}

// identity returns the connection's device and group, both empty when the
// connection has not authenticated.
func (c *conn) identity() (deviceID, groupID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deviceID, c.groupID
}

func (c *conn) setIdentity(deviceID, groupID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deviceID, c.groupID = deviceID, groupID
}

// send queues a frame. It never blocks: a full queue means the client is not
// keeping up, and the connection is dropped rather than allowed to grow.
func (c *conn) send(frame []byte) bool {
	select {
	case <-c.closed:
		return false
	case c.out <- frame:
		return true
	default:
		deviceID, _ := c.identity()
		c.logger.Warn("dropping slow websocket client",
			slog.String("device_id", deviceID),
			slog.Int("queue_depth", writeQueueDepth))
		c.closeWith(websocket.StatusPolicyViolation, "write queue overflow")
		return false
	}
}

// writePump is the connection's only writer. It returns when ctx is done or
// the connection is closed.
func (c *conn) writePump(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case frame := <-c.out:
			if err := c.ws.Write(ctx, websocket.MessageBinary, frame); err != nil {
				if ctx.Err() == nil && !errors.Is(err, context.Canceled) {
					c.logger.DebugContext(ctx, "websocket write failed", slog.Any("error", err))
				}
				c.closeWith(websocket.StatusInternalError, "write failed")
				return
			}
		}
	}
}

// closeWith closes the connection once, whichever goroutine gets there first.
//
// The connection is marked closed synchronously, so no further frame is
// queued, but the closing handshake runs on its own goroutine: it waits for
// the peer's close frame, and the peer here is often precisely the client that
// has stopped reading. Blocking a request handler — or the goroutine that just
// revoked a device — on an unresponsive socket is not acceptable.
func (c *conn) closeWith(status websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		close(c.closed)
		go func() {
			// Best effort: the peer may already be gone.
			if err := c.ws.Close(status, reason); err != nil {
				_ = c.ws.CloseNow()
			}
		}()
	})
}
