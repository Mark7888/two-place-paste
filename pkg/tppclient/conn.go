package tppclient

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
)

// conn is one WebSocket connection to the relay, with request correlation on
// top of it.
//
// Every frame is a serialized tpp.v1.Envelope carried as a binary message and
// nothing else, exactly as the server sends them (SPEC §5.1). A response
// echoes its request's id; a server-pushed event carries an id that correlates
// with nothing and is routed to the event handler instead.
type conn struct {
	ws *websocket.Conn

	mu      sync.Mutex
	pending map[string]chan *tppv1.Envelope
	waiters map[tppv1.MessageType][]chan *tppv1.Envelope

	closeOnce sync.Once
	done      chan struct{}
	err       error
}

// readLimit bounds an inbound frame: the 10 MB ciphertext cap plus room for
// protobuf framing, mirroring what the server allows outbound (SPEC §4.3).
const readLimit = (10 << 20) + (64 << 10)

// Keepalive. A socket with no traffic on it is indistinguishable, to a reverse
// proxy or a NAT, from one whose peer has gone: most of them sever it after
// somewhere between 30 and 100 seconds of silence, and they do it by dropping
// the TCP connection, not by sending a close frame. The client learns of that
// as an EOF, then reconnects. A ping well inside the shortest of those windows
// keeps the path open; a pong that does not come back in time is also how a
// connection that died silently is noticed at all.
const (
	pingInterval = 25 * time.Second
	pingTimeout  = 15 * time.Second
)

// dialConn opens a connection. deviceID, when set, is the connection's device
// credential; the two unauthenticated flows (group creation, pairing-join)
// dial without one.
func dialConn(ctx context.Context, endpoint string, httpClient *http.Client) (*conn, error) {
	ws, resp, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: httpClient})
	if resp != nil && resp.Body != nil {
		// A refused upgrade still carries a body; a successful one has nothing
		// left to read.
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			// The status is how a revoked device learns its record is gone:
			// the relay refuses the upgrade with 401 (SPEC §3.3 step 5).
			return nil, fmt.Errorf("tppclient: connect to the relay: %w",
				statusError{code: resp.StatusCode, err: err})
		}
		return nil, fmt.Errorf("tppclient: connect to the relay: %w", err)
	}
	ws.SetReadLimit(readLimit)
	return &conn{
		ws:      ws,
		pending: make(map[string]chan *tppv1.Envelope),
		waiters: make(map[tppv1.MessageType][]chan *tppv1.Envelope),
		done:    make(chan struct{}),
	}, nil
}

// readLoop reads until the socket fails, routing responses to their callers
// and everything else to onEvent. It returns the error that ended the
// connection.
func (c *conn) readLoop(ctx context.Context, onEvent func(*tppv1.Envelope)) error {
	for {
		typ, data, err := c.ws.Read(ctx)
		if err != nil {
			// A keepalive that failed has already recorded why and closed the
			// socket under this read; its reason is the one worth reporting.
			c.fail(fmt.Errorf("tppclient: read from the relay: %w", err))
			return c.failure()
		}
		if typ != websocket.MessageBinary {
			err := fmt.Errorf("tppclient: relay sent a %s frame; this protocol is binary only", typ)
			c.fail(err)
			return err
		}

		var env tppv1.Envelope
		if err := proto.Unmarshal(data, &env); err != nil {
			// A frame this client cannot parse is dropped rather than treated
			// as fatal: the socket is still healthy and the next frame may be
			// fine (SPEC §5.1).
			continue
		}
		if c.deliver(&env) {
			continue
		}
		if onEvent != nil {
			onEvent(&env)
		}
	}
}

// keepalive pings the relay every interval until the connection ends. A ping
// not answered within timeout fails the connection and closes the socket,
// which unblocks readLoop. Ping needs a concurrent reader to see the pong, and
// readLoop is that reader.
func (c *conn) keepalive(ctx context.Context, interval, timeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-ticker.C:
		}
		pingCtx, cancel := context.WithTimeout(ctx, timeout)
		err := c.ws.Ping(pingCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.fail(fmt.Errorf("tppclient: the relay stopped answering pings: %w", err))
			_ = c.ws.CloseNow()
			return
		}
	}
}

// deliver routes an envelope to a pending request or a type waiter. It
// reports whether anyone took it.
func (c *conn) deliver(env *tppv1.Envelope) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ch, ok := c.pending[env.GetId()]; ok {
		delete(c.pending, env.GetId())
		ch <- env
		return true
	}
	// A pushed frame somebody is explicitly waiting for: PairingComplete on
	// the joiner, for instance, which answers no request of its own.
	if chans, ok := c.waiters[env.GetType()]; ok && len(chans) > 0 {
		ch := chans[0]
		if len(chans) == 1 {
			delete(c.waiters, env.GetType())
		} else {
			c.waiters[env.GetType()] = chans[1:]
		}
		ch <- env
		return true
	}
	return false
}

// call sends a request and waits for the matching response, decoding it into
// out. An Error frame becomes a *ProtocolError.
func (c *conn) call(ctx context.Context, typ tppv1.MessageType, msg proto.Message, out proto.Message) error {
	id, err := newEnvelopeID()
	if err != nil {
		return err
	}

	ch := make(chan *tppv1.Envelope, 1)
	c.mu.Lock()
	select {
	case <-c.done:
		c.mu.Unlock()
		return c.reason()
	default:
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(ctx, id, typ, msg); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("tppclient: waiting for a response to %s: %w", typ, ctx.Err())
	case <-c.done:
		return c.reason()
	case env := <-ch:
		return decodeResponse(env, out)
	}
}

// expect registers interest in the next server-pushed frame of a type, before
// the request that triggers it is sent. Registering first is what makes the
// wait race-free: the notice can arrive while the request is still in flight.
func (c *conn) expect(typ tppv1.MessageType) <-chan *tppv1.Envelope {
	ch := make(chan *tppv1.Envelope, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waiters[typ] = append(c.waiters[typ], ch)
	return ch
}

// write serializes one envelope onto the socket. coder/websocket allows
// concurrent writes, so no mutex is needed here.
func (c *conn) write(ctx context.Context, id string, typ tppv1.MessageType, msg proto.Message) error {
	var payload []byte
	if msg != nil {
		var err error
		payload, err = proto.Marshal(msg)
		if err != nil {
			return fmt.Errorf("tppclient: encode %s: %w", typ, err)
		}
	}
	frame, err := proto.Marshal(&tppv1.Envelope{Id: id, Type: typ, Payload: payload})
	if err != nil {
		return fmt.Errorf("tppclient: encode envelope: %w", err)
	}
	if err := c.ws.Write(ctx, websocket.MessageBinary, frame); err != nil {
		return fmt.Errorf("tppclient: send %s: %w", typ, err)
	}
	return nil
}

// send writes a frame that expects no correlated response.
func (c *conn) send(ctx context.Context, typ tppv1.MessageType, msg proto.Message) error {
	id, err := newEnvelopeID()
	if err != nil {
		return err
	}
	return c.write(ctx, id, typ, msg)
}

// fail records why the connection ended and wakes everyone waiting on it.
func (c *conn) fail(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

// failure is the error that ended the connection, as fail recorded it.
func (c *conn) failure() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// reason is the error callers see once the connection is gone.
func (c *conn) reason() error {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	if err == nil {
		err = errors.New("tppclient: connection closed")
	}
	return fmt.Errorf("tppclient: connection lost: %w", err)
}

// close shuts the socket down cleanly and unblocks every waiter.
func (c *conn) close(reason string) {
	_ = c.ws.Close(websocket.StatusNormalClosure, reason)
	c.fail(errors.New("tppclient: connection closed by this client"))
}

// decodeResponse turns a response envelope into out, or into a *ProtocolError.
func decodeResponse(env *tppv1.Envelope, out proto.Message) error {
	if env.GetType() == tppv1.MessageType_MESSAGE_TYPE_ERROR {
		var e tppv1.Error
		if err := proto.Unmarshal(env.GetPayload(), &e); err != nil {
			return fmt.Errorf("tppclient: decode error frame: %w", err)
		}
		return &ProtocolError{Code: e.GetCode(), Message: e.GetMessage()}
	}
	if out == nil {
		return nil
	}
	if err := proto.Unmarshal(env.GetPayload(), out); err != nil {
		return fmt.Errorf("tppclient: decode %s: %w", env.GetType(), err)
	}
	return nil
}

// newEnvelopeID returns a correlation id: 96 bits of randomness, which is
// plenty to keep concurrent requests on one socket apart.
func newEnvelopeID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("tppclient: generate a correlation id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// statusError carries the HTTP status of a refused upgrade, so the supervisor
// can tell "revoked" from "the relay is down".
type statusError struct {
	code int
	err  error
}

func (e statusError) Error() string { return fmt.Sprintf("relay refused the upgrade: %v", e.err) }

func (e statusError) Unwrap() error { return e.err }
