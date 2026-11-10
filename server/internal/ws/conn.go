package ws

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// writeQueueDepth bounds how far a connection may fall behind before it is
// dropped. A slow client is disconnected, never buffered without limit
// (ROADMAP P3 backpressure, docs/internal/conventions.md §8): it reconnects and pulls
// what it missed, which is cheaper than letting one stalled socket consume the
// server's memory.
const writeQueueDepth = 32

// Keepalive. Reverse proxies and NATs sever a TCP connection that has been
// silent for long enough, usually somewhere between 30 and 100 seconds, and
// they do it without a close frame. A clipboard socket is silent most of the
// time, so the server pings well inside that window; a peer that does not
// answer before the next ping is due is gone, and its socket is closed rather
// than kept registered.
//
// Only a silent connection is pinged, and only a silent one is closed for
// not answering. A connection moving a large frame — an upload chunk coming
// in, a download range going out, several megabytes over a phone's link —
// can take longer than the interval to move one frame, and neither end can
// slip a ping or a pong into the middle of it: the phone's WebSocket writes a
// message whole, and a ping waits behind the frame being written. Closing
// such a connection for a late pong killed it mid-transfer and failed every
// request on it. Bytes moving, or a request being handled, are proof enough
// that the peer is there, and they keep a proxy's idle timer from firing too.
const pingInterval = 25 * time.Second

// writePiece is how much of an outgoing frame is written at once: the frame
// goes out as fragments of this size, so a ping can go between them and the
// connection's activity is seen while a large frame is still on its way.
const writePiece = 64 << 10

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

	// now is the clock activity is measured by; tests replace it.
	now func() time.Time
	// lastActivity is when bytes last moved either way, in Unix nanoseconds.
	lastActivity atomic.Int64
	// handling counts requests being handled: a slow one (a chunk going to
	// S3, say) holds up the read loop, and with it any pong.
	handling atomic.Int32
}

func newConn(ws *websocket.Conn, logger *slog.Logger) *conn {
	c := &conn{
		ws:     ws,
		logger: logger,
		out:    make(chan []byte, writeQueueDepth),
		closed: make(chan struct{}),
		now:    time.Now,
	}
	c.touch()
	return c
}

// touch records that bytes moved on the connection.
func (c *conn) touch() {
	c.lastActivity.Store(c.now().UnixNano())
}

// activeSince reports whether the connection has been busy since t: bytes
// moved, or a request is being handled now.
func (c *conn) activeSince(t time.Time) bool {
	return c.handling.Load() > 0 || c.lastActivity.Load() >= t.UnixNano()
}

// readFrame reads one whole message, recording activity as its bytes arrive
// rather than only once the last of them has: a large upload chunk on a slow
// link takes a while, and the peer is plainly there throughout.
func (c *conn) readFrame(ctx context.Context) (websocket.MessageType, []byte, error) {
	typ, r, err := c.ws.Reader(ctx)
	if err != nil {
		return 0, nil, err
	}
	c.touch()
	data, err := io.ReadAll(activityReader{r: r, c: c})
	return typ, data, err
}

// activityReader touches the connection on every read that returns bytes.
type activityReader struct {
	r io.Reader
	c *conn
}

func (a activityReader) Read(p []byte) (int, error) {
	n, err := a.r.Read(p)
	if n > 0 {
		a.c.touch()
	}
	return n, err
}

// writeFrame writes one message as fragments of writePiece bytes, recording
// activity as each goes out, so a large download range on a slow link counts
// as the activity it is and a ping can be written between fragments.
func (c *conn) writeFrame(ctx context.Context, frame []byte) error {
	w, err := c.ws.Writer(ctx, websocket.MessageBinary)
	if err != nil {
		return err
	}
	for len(frame) > 0 {
		n := min(len(frame), writePiece)
		if _, err := w.Write(frame[:n]); err != nil {
			_ = w.Close()
			return err
		}
		c.touch()
		frame = frame[n:]
	}
	return w.Close()
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
			if err := c.writeFrame(ctx, frame); err != nil {
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
// closes, and gives each ping until the next one to be answered. Ping writes a
// control frame, which coder/websocket allows between the write pump's
// fragments; the pong is read by the server's read loop.
//
// A connection that has been busy over the last interval is not pinged: its
// traffic already shows the peer is there (see pingInterval). That also keeps
// a ping from being written into a socket whose buffers a large frame has
// filled — coder/websocket closes the connection if a ping's deadline passes
// mid-write. And a ping that goes unanswered while the connection turns
// busy is not held against it.
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
		start := c.now()
		if c.activeSince(start.Add(-interval)) {
			continue
		}
		pingCtx, cancel := context.WithTimeout(ctx, interval)
		err := c.ws.Ping(pingCtx)
		cancel()
		if err != nil && ctx.Err() == nil && c.activeSince(start) {
			// It went busy while the ping was out: a transfer started, and
			// its frame is between the ping and the pong.
			continue
		}
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
