package tppclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/Mark7888/two-place-paste/pkg/tppclient/keystore"
	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// Reconnect defaults. A relay that is down comes back; a client that hammers
// it while it does is a second problem (SPEC §5.2).
const (
	defaultMinBackoff = 500 * time.Millisecond
	defaultMaxBackoff = 30 * time.Second
	backoffFactor     = 2
	backoffJitter     = 0.2
)

// Handlers are the callbacks a shell (desktop tray, mobile screen) hooks into.
//
// Every one of them runs on the client's own goroutine: do the minimum and
// hand off. None of them may touch the OS clipboard — this package cannot, and
// a handler that did would reintroduce exactly the coupling SPEC §3.3 forbids
// by making a rekey observable at the clipboard.
type Handlers struct {
	// OnConnected fires after each successful connection, including
	// reconnections.
	OnConnected func()

	// OnDisconnected fires when a connection drops, with the reason. A
	// reconnect follows unless the client was closed or revoked.
	OnDisconnected func(error)

	// OnEpoch fires when this client installs a new group key, whether from a
	// rekey it performed, one it was told about, or a wrapped key waiting for
	// it at connect time.
	OnEpoch func(epoch uint64)

	// OnDeviceRevoked fires when the group loses a device (SPEC §3.3). A UI
	// refreshes its roster; it must not touch the clipboard.
	OnDeviceRevoked func(deviceID string, epoch uint64)

	// OnDevicePaired fires on the inviting device once a joiner has been
	// handed the group key (SPEC §3.2 step 4).
	OnDevicePaired func(Device)

	// OnRevoked fires when the relay reports that this device no longer
	// exists: it was revoked while away (SPEC §3.3 step 5). The client stops
	// reconnecting; nothing recovers the install but pairing again.
	OnRevoked func()
}

// Options configures a Client.
type Options struct {
	// ServerURL is the relay base URL, e.g. "https://tpp.example.com". It may
	// be empty when the client is about to call CreateGroup or JoinPairing,
	// which learn it from the creation URL or the pairing payload.
	ServerURL string

	// DeviceName is what the revocation dialog on other devices shows
	// (SPEC §3.3 step 2). Not a secret, and not clipboard content.
	DeviceName string

	// Keystore persists State across restarts. Without one the client is
	// in-memory only, which is what tests want and no product should ship.
	Keystore keystore.Store

	// StateName is the keystore entry to use. Defaults to DefaultStateName.
	StateName string

	// HTTPClient dials the relay. Defaults to a client with no timeout: a
	// WebSocket is long-lived and a client timeout would sever it.
	HTTPClient *http.Client

	// Logger receives connection-level logs. It never sees a key, a token or
	// any ciphertext (docs/conventions.md §2).
	Logger *slog.Logger

	// Handlers are optional callbacks.
	Handlers Handlers

	// MinBackoff and MaxBackoff bound the reconnect delay.
	MinBackoff, MaxBackoff time.Duration

	// Now supplies UTC timestamps; tests replace it.
	Now func() time.Time
}

// Client is the shared client core: crypto, transport and the flows of
// SPEC §3 and §6. Desktop and any future CLI are shells around it.
//
// It is safe for concurrent use.
type Client struct {
	handlers   Handlers
	logger     *slog.Logger
	httpClient *http.Client
	stateStore keystore.Store
	stateName  string
	minBackoff time.Duration
	maxBackoff time.Duration
	now        func() time.Time

	mu          sync.Mutex
	state       State
	conn        *conn
	ready       chan struct{} // closed while a connection is live
	invitations []*Invitation

	// The supervisor's lifetime, and everything scoped to it. Forget replaces
	// all four so the next Connect starts a fresh supervisor, which is why
	// nothing reads them outside the mutex: a reader that cached runCtx across
	// a Forget would be watching a context nobody cancels any more.
	runCtx    context.Context
	runCancel context.CancelFunc
	started   bool
	revoked   bool
	stopped   chan struct{} // closed when the supervisor gives up for good

	closed    bool
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New builds a client. When Options.Keystore holds state from a previous run,
// that state is loaded and the client is ready to connect; otherwise a device
// keypair is generated and stored, which is the "first launch" of
// /spec/crypto.md §3.
func New(opts Options) (*Client, error) {
	c := &Client{
		handlers:   opts.Handlers,
		logger:     opts.Logger,
		httpClient: opts.HTTPClient,
		stateStore: opts.Keystore,
		stateName:  opts.StateName,
		minBackoff: opts.MinBackoff,
		maxBackoff: opts.MaxBackoff,
		now:        opts.Now,
		ready:      make(chan struct{}),
		stopped:    make(chan struct{}),
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{}
	}
	if c.stateName == "" {
		c.stateName = DefaultStateName
	}
	if c.minBackoff <= 0 {
		c.minBackoff = defaultMinBackoff
	}
	if c.maxBackoff < c.minBackoff {
		c.maxBackoff = defaultMaxBackoff
	}
	if c.now == nil {
		c.now = func() time.Time { return time.Now().UTC() }
	}
	c.runCtx, c.runCancel = context.WithCancel(context.Background())

	if c.stateStore != nil {
		s, err := LoadState(c.stateStore, c.stateName)
		switch {
		case err == nil:
			c.state = s
		case !errors.Is(err, keystore.ErrNotFound):
			return nil, err
		}
	}
	if opts.ServerURL != "" {
		base, err := normalizeServerURL(opts.ServerURL)
		if err != nil {
			return nil, err
		}
		c.state.ServerURL = base
	}
	if opts.DeviceName != "" {
		c.state.DeviceName = opts.DeviceName
	}
	if len(c.state.DevicePrivateKey) == 0 {
		priv, err := tppcrypto.GenerateKey()
		if err != nil {
			return nil, fmt.Errorf("tppclient: generate this device's keypair: %w", err)
		}
		c.state.DevicePrivateKey = priv
	}
	if err := c.persist(); err != nil {
		return nil, err
	}
	return c, nil
}

// State returns a copy of the client's current state. The copy carries the
// device private key and the group key: it is for persistence and tests, not
// for display, and never for a log (/spec/crypto.md §10).
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Epoch is the group key generation this client currently holds.
func (c *Client) Epoch() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Epoch
}

// InGroup reports whether this device belongs to a group yet.
func (c *Client) InGroup() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.InGroup()
}

// Close stops reconnecting and shuts the connection down. The client cannot be
// reused afterwards.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		cancel, conn := c.runCancel, c.conn
		c.conn = nil
		c.mu.Unlock()
		cancel()
		if conn != nil {
			conn.close("client closing")
		}
		c.wg.Wait()
	})
	return nil
}

// Forget discards this device's identity: the group, the group key and the
// device keypair are replaced by the ones a first launch would have.
//
// It is a local operation, and it is the only one. The relay still lists this
// device, and the group key it held is still the group's key — only a
// revocation from another device changes that (SPEC §3.3), which is why the
// screen that offers this says so. What Forget does guarantee is that the
// group key is gone from this machine, so nothing here can read what the group
// writes next.
//
// The new keypair is not the old one, because this is a new identity: pairing
// again should look to the group like a new device, not like the one that
// left. The client stays usable — with no group it is a first launch, ready to
// create a group, join a pairing or show an offer.
func (c *Client) Forget() error {
	c.mu.Lock()
	cancel, conn, started, closed := c.runCancel, c.conn, c.started, c.closed
	c.conn = nil
	c.mu.Unlock()

	// The supervisor is stopped before the state goes, not after: a reconnect
	// racing this would re-dial with the credential being discarded.
	if started {
		cancel()
		if conn != nil {
			conn.close("this device is leaving the group")
		}
		c.wg.Wait()
	}

	priv, err := tppcrypto.GenerateKey()
	if err != nil {
		return fmt.Errorf("tppclient: generate this device's keypair: %w", err)
	}

	c.mu.Lock()
	c.state = State{DeviceName: c.state.DeviceName, DevicePrivateKey: priv}
	c.invitations = nil
	c.conn = nil
	c.ready = make(chan struct{})
	c.revoked = false
	c.started = false
	if !closed {
		// A fresh generation for the next Connect. A closed client stays
		// closed: Forget clears the secrets, it does not resurrect anything.
		c.stopped = make(chan struct{})
		c.runCtx, c.runCancel = context.WithCancel(context.Background())
	}
	c.mu.Unlock()

	c.logger.Info("this device left its group; its keys have been replaced")
	return c.persist()
}

// runContext is the current supervisor generation's context. Forget replaces
// it, so a caller that needs "for as long as this client is live" must read it
// under the mutex rather than cache it.
func (c *Client) runContext() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runCtx
}

// persist writes state through to the keystore, if there is one.
func (c *Client) persist() error {
	if c.stateStore == nil {
		return nil
	}
	c.mu.Lock()
	s := c.state
	c.mu.Unlock()
	return SaveState(c.stateStore, c.stateName, s)
}

// ---------------------------------------------------------------------------
// Connection supervision
// ---------------------------------------------------------------------------

// Connect brings the client online and keeps it there: it dials, and from then
// on a background supervisor re-dials with exponential backoff whenever the
// connection drops (SPEC §5.2).
//
// It returns once the first connection is established, or when ctx is done.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if !c.state.InGroup() {
		c.mu.Unlock()
		return ErrNoGroup
	}
	if !c.started {
		c.started = true
		c.wg.Add(1)
		// The generation's context and stop channel are handed over here, not
		// read from the struct inside the loop: Forget replaces both, and a
		// supervisor that read the replacements would close a channel its
		// successor owns.
		//
		//nolint:contextcheck // The supervisor's lifetime is the client's, not
		// this call's: Connect returns once the first connection is up, and
		// reconnecting must outlive the ctx that asked for it. Close and
		// Forget are what cancel runCtx.
		go c.supervise(c.runCtx, c.stopped)
	}
	c.mu.Unlock()

	_, err := c.connection(ctx)
	return err
}

// supervise dials, serves the connection, and re-dials with backoff until the
// client is closed, the device is revoked, or the group is forgotten.
func (c *Client) supervise(runCtx context.Context, stopped chan struct{}) {
	defer c.wg.Done()
	// Waiters block on `ready`, which nothing closes once this loop is gone;
	// closing `stopped` is what turns "waiting to reconnect" into an answer
	// for a client that will never reconnect.
	defer close(stopped)

	backoff := c.minBackoff
	for {
		if runCtx.Err() != nil {
			return
		}
		err := c.serveOnce(runCtx)
		switch {
		case runCtx.Err() != nil:
			return
		case errors.Is(err, ErrRevoked):
			// The credential is gone for good: reconnecting cannot fix it, and
			// retrying would only produce a stream of 401s (SPEC §3.3 step 5).
			c.mu.Lock()
			c.revoked = true
			c.mu.Unlock()
			c.logger.Warn("this device has been revoked; not reconnecting")
			if c.handlers.OnRevoked != nil {
				c.handlers.OnRevoked()
			}
			return
		case err != nil:
			c.logger.Debug("relay connection ended", slog.Any("reason", err))
		}

		select {
		case <-runCtx.Done():
			return
		case <-time.After(jitter(backoff)):
		}
		if backoff < c.maxBackoff {
			backoff = min(backoff*backoffFactor, c.maxBackoff)
		}
	}
}

// serveOnce holds one connection open until it fails.
func (c *Client) serveOnce(runCtx context.Context) error {
	c.mu.Lock()
	deviceID, base := c.state.DeviceID, c.state.ServerURL
	c.mu.Unlock()

	endpoint, err := websocketURL(base, deviceID)
	if err != nil {
		return err
	}
	conn, err := c.dial(runCtx, endpoint)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.conn = conn
	close(c.ready)
	c.mu.Unlock()
	c.logger.Debug("connected to the relay", slog.String("group_id", base))
	if c.handlers.OnConnected != nil {
		c.handlers.OnConnected()
	}

	readErr := conn.readLoop(runCtx, c.onEvent)

	c.mu.Lock()
	c.conn = nil
	c.ready = make(chan struct{})
	c.mu.Unlock()
	if c.handlers.OnDisconnected != nil {
		c.handlers.OnDisconnected(readErr)
	}
	return readErr
}

// dial opens a socket, translating the relay's rejection of an unknown device
// into ErrRevoked.
func (c *Client) dial(ctx context.Context, endpoint string) (*conn, error) {
	conn, err := dialConn(ctx, endpoint, c.httpClient)
	if err == nil {
		return conn, nil
	}
	var status statusError
	if errors.As(err, &status) && status.code == http.StatusUnauthorized {
		return nil, fmt.Errorf("%w: %s", ErrRevoked, status.Error())
	}
	return nil, err
}

// connection waits for a live connection.
func (c *Client) connection(ctx context.Context) (*conn, error) {
	for {
		// runCtx and stopped are read here, with conn and ready, because
		// Forget swaps the whole set: a select on one generation's context and
		// the next generation's stop channel would wait on neither.
		c.mu.Lock()
		conn, ready, revoked := c.conn, c.ready, c.revoked
		runCtx, stopped := c.runCtx, c.stopped
		c.mu.Unlock()
		switch {
		case revoked:
			return nil, ErrRevoked
		case conn != nil:
			return conn, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("tppclient: waiting for a connection to the relay: %w", ctx.Err())
		case <-runCtx.Done():
			return nil, ErrClosed
		case <-stopped:
			// The supervisor has given up: loop once more to read why, then
			// answer instead of waiting for a connection that is not coming.
			c.mu.Lock()
			revoked := c.revoked
			c.mu.Unlock()
			if revoked {
				return nil, ErrRevoked
			}
			return nil, ErrClosed
		case <-ready:
		}
	}
}

// call runs one request over the supervised connection.
func (c *Client) call(ctx context.Context, typ tppv1.MessageType, msg, out proto.Message) error {
	conn, err := c.connection(ctx)
	if err != nil {
		return err
	}
	return conn.call(ctx, typ, msg, out)
}

// ---------------------------------------------------------------------------
// Server-pushed events
// ---------------------------------------------------------------------------

// onEvent handles the frames the relay pushes rather than answers.
func (c *Client) onEvent(env *tppv1.Envelope) {
	switch env.GetType() {
	case tppv1.MessageType_MESSAGE_TYPE_WRAPPED_KEY_AVAILABLE:
		var ev tppv1.WrappedKeyAvailable
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			c.logger.Warn("relay sent an undecodable wrapped key event")
			return
		}
		// A key that was waiting for this device arrives here on connect, so a
		// device that was offline during a rekey catches up without asking
		// (SPEC §3.3).
		if err := c.installWrappedKey(ev.GetEpoch(), ev.GetWrappedGroupKey()); err != nil {
			c.logger.Error("installing the new group key failed",
				slog.Uint64("epoch", ev.GetEpoch()), slog.Any("error", err))
		}

	case tppv1.MessageType_MESSAGE_TYPE_EPOCH_CHANGED:
		var ev tppv1.EpochChanged
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			return
		}
		// Nothing to install yet: the wrapped key is a separate frame. This
		// event is a notification only, and it must never touch the local
		// clipboard (/spec/crypto.md §7).
		c.logger.Info("the group was re-keyed", slog.Uint64("epoch", ev.GetEpoch()))

	case tppv1.MessageType_MESSAGE_TYPE_DEVICE_REVOKED:
		var ev tppv1.DeviceRevoked
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			return
		}
		if c.handlers.OnDeviceRevoked != nil {
			c.handlers.OnDeviceRevoked(ev.GetDeviceId(), ev.GetEpoch())
		}

	case tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_NOTICE:
		var ev tppv1.PairingJoinNotice
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			return
		}
		// The inviter is the only device that holds the group key, so wrapping
		// for the joiner happens here and cannot be deferred (SPEC §3.2
		// step 4). It runs on its own goroutine because it makes a request of
		// its own, and this handler is on the read loop.
		go c.completePairing(&ev)

	default:
		// An event this client does not understand is ignored, never fatal
		// (SPEC §5.1).
	}
}

// installWrappedKey unwraps a delivered group key and advances the epoch.
//
// A key for an epoch this client already has, or for an older one, is dropped:
// epochs only move forward, and a client keeps exactly one (epoch, group key)
// pair — keeping older keys would silently defeat the forward secrecy the
// rekey exists for (/spec/crypto.md §7).
func (c *Client) installWrappedKey(epoch uint64, wrapped []byte) error {
	c.mu.Lock()
	current, priv := c.state.Epoch, c.state.DevicePrivateKey
	c.mu.Unlock()

	if epoch <= current {
		return nil
	}
	groupKey, err := tppcrypto.Unwrap(wrapped, priv, epoch)
	if err != nil {
		return fmt.Errorf("tppclient: unwrap the group key for epoch %d: %w", epoch, err)
	}
	c.setGroupKey(epoch, groupKey)
	return c.persist()
}

// setGroupKey installs an (epoch, group key) pair and notifies the caller's
// handler. It touches keys and nothing else: a rekey never reaches the
// clipboard, which this package structurally cannot do anyway (SPEC §3.3).
func (c *Client) setGroupKey(epoch uint64, groupKey []byte) {
	c.mu.Lock()
	c.state.Epoch = epoch
	c.state.GroupKey = groupKey
	c.mu.Unlock()
	if c.handlers.OnEpoch != nil {
		c.handlers.OnEpoch(epoch)
	}
}

// jitter spreads reconnects so that every client of a relay that restarted
// does not come back in the same millisecond.
func jitter(d time.Duration) time.Duration {
	spread := float64(d) * backoffJitter
	return d + time.Duration((rand.Float64()*2-1)*spread)
}
