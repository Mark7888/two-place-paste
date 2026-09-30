package ws

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// writeQueueDepth bounds how far a connection may fall behind before it is
// dropped. A slow client is disconnected, never buffered without limit
// (ROADMAP P3 backpressure, docs/conventions.md §8): it reconnects and pulls
// what it missed, which is cheaper than letting one stalled socket consume the
// server's memory.
const writeQueueDepth = 32

// Keepalive. Reverse proxies and NATs sever a TCP connection that has been
// silent for long enough, usually somewhere between 30 and 100 seconds, and
// they do it without a close frame. A clipboard socket is silent most of the
// time, so the server pings well inside that window; a peer that does not
// answer before the next ping is due is gone, and its socket is closed rather
// than kept registered.
const pingInterval = 25 * time.Second

// conn is one WebSocket connection and the identity it has established.
type conn struct {
	ws     *websocket.Conn
	logger *slog.Logger

	// out is the bounded write queue. Every frame leaves through the single
	// write pump goroutine, so the underlying socket has exactly one writer.
	out chan []byte

	// mu guards the identity fields, which the pairing and creation handlers
	// set after the connection is already serving, and the offer codes this
	// connection is waiting on.
	mu       sync.Mutex
	deviceID string
	groupID  string

	// offers are the pairing offers minted on this connection and not yet
	// answered. A joiner holds its socket open waiting for PairingComplete,
	// and it has no device identity to be reached by, so the offer code is
	// what the hub routes on. The slice is also the rate limit: it only grows.
	offers []string

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

// addOffer records a pairing offer this connection is waiting on and returns
// how many it has now minted. The count only ever grows, so it bounds one
// socket's offers for the life of the socket rather than over a window: an
// unauthenticated connection that wants more reconnects, which costs it a dial.
func (c *conn) addOffer(code string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offers = append(c.offers, code)
	return len(c.offers)
}

// offerCodes returns the offers this connection is waiting on.
func (c *conn) offerCodes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.offers)
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

// keepalive pings the peer every interval until ctx is done or the connection
// closes, and gives each ping until the next one to be answered. Ping writes a control frame, which coder/websocket allows alongside
// the write pump's data frames; the pong is read by the server's read loop.
func (c *conn) keepalive(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.closed:
			return
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, interval)
		err := c.ws.Ping(pingCtx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				deviceID, _ := c.identity()
				c.logger.DebugContext(ctx, "websocket peer stopped answering pings",
					slog.String("device_id", deviceID), slog.Any("error", err))
			}
			// The close handshake would wait on the same unresponsive peer.
			c.closeOnce.Do(func() { close(c.closed) })
			_ = c.ws.CloseNow()
			return
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
