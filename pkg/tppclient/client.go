package tppclient

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
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

	// OnKeyRefused fires when the relay offered a group key for a new epoch
	// that this client could not verify (/spec/crypto.md §7.1), with an error
	// that says why; errors.Is(err, ErrUnverifiedKey) holds. The key is not
	// installed and the client stays at its epoch. A shell tells the user: a
	// relay that keeps doing this is either out of date or hostile, and the
	// way back is to leave the group and pair again.
	OnKeyRefused func(epoch uint64, err error)

	// OnDeviceRevoked fires when the group loses a device (SPEC §3.3). A UI
	// refreshes its roster; it must not touch the clipboard.
	OnDeviceRevoked func(deviceID string, epoch uint64)

	// OnJoinRequest fires on the inviting device when a device asks to join
	// with one of its invitations (SPEC §3.2 step 3). Nothing is handed over
	// until the user confirms: the shell shows the request's name and
	// fingerprint and calls Confirm or Reject.
	OnJoinRequest func(*JoinRequest)

	// OnDevicePaired fires on the inviting device once a joiner has been
	// handed the group key (SPEC §3.2 step 4).
	OnDevicePaired func(Device)

	// OnRevoked fires when the relay reports that this device no longer
	// exists: it was revoked while away (SPEC §3.3 step 5). The client stops
	// reconnecting; nothing recovers the install but pairing again.
	OnRevoked func()

	// OnGroupDeleted fires instead of OnRevoked when the reason this device
	// lost its credential is that the server admin deleted its group, so a
	// shell can say so. Without it, a deletion fires OnRevoked. Either way
	// the client stops reconnecting.
	OnGroupDeleted func()

	// OnClientOutdated fires when the relay refuses this build because its
	// protocol level is too old (SPEC §5.3). It is not a revocation: the
	// device's record and keys are untouched, and once the app is updated it
	// connects as before. The client keeps trying, but only hourly, or at
	// once on Reconnect; OnConnected fires when it succeeds. It fires once
	// per run of refusals, not on every refused attempt.
	OnClientOutdated func()

	// OnGroupName fires when the group's display name, as the server admin
	// set it, is first learned or changes. It may be empty.
	OnGroupName func(name string)

	// OnOperatorURL fires when a connection learns the relay operator's
	// website, or learns that it changed. url is the plain base URL;
	// OperatorLink builds the link to open. Replacing the identity (Forget,
	// SwitchGroup and the like) clears it without firing, since a shell
	// redraws everything then anyway and reads OperatorLink afresh.
	OnOperatorURL func(url string)

	// OnEntryAdded fires when another device of the group has written a new
	// latest entry. It carries metadata only and runs on the read loop: a
	// handler that wants the content fetches it on a goroutine of its own.
	// Nothing about it touches the clipboard unless the handler does.
	OnEntryAdded func(EntryMeta)

	// OnEntryEvicted fires when the relay removed an entry before its expiry
	// to stay within its storage limit. A history view drops the entry; a
	// fetch of it now fails with ErrNotFound.
	OnEntryEvicted func(entryID string)

	// OnProgress reports a chunked upload or download — an entry or store
	// item larger than one frame — as each chunk completes. It runs on the
	// goroutine doing the transfer and must not block.
	OnProgress func(Progress)

	// OnStoreItemAdded and OnStoreItemUpdated fire when another device of the
	// group saved or edited a store item, with its metadata opened; a
	// StoreIndex takes them with Put. An item that does not open is logged
	// and skipped: the next ListStore reports it.
	OnStoreItemAdded   func(StoreItem)
	OnStoreItemUpdated func(StoreItem)

	// OnStoreItemDeleted fires when another device deleted a store item.
	OnStoreItemDeleted func(itemID string)

	// OnStoreState fires when the server admin turned the group's store on
	// or off, or it closed to be emptied and opened again. A UI lists the
	// store again when it opens.
	OnStoreState func(enabled bool)
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

	// ParkedName is the keystore entry parked groups are kept under.
	// Defaults to DefaultParkedName.
	ParkedName string

	// HTTPClient dials the relay. Defaults to a client with no timeout: a
	// WebSocket is long-lived and a client timeout would sever it.
	HTTPClient *http.Client

	// Logger receives connection-level logs. It never sees a key, a token or
	// any ciphertext (docs/internal/conventions.md §2).
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
	parkedName string
	minBackoff time.Duration
	maxBackoff time.Duration
	now        func() time.Time

	// groupsMu serialises the operations that replace the identity —
	// Forget, ParkActive, SwitchGroup, RemoveParked — so two of them cannot
	// interleave their keystore writes (/spec/crypto.md §10.1).
	groupsMu sync.Mutex

	mu    sync.Mutex
	state State
	conn  *conn

	// operatorURL is the relay operator's website as the relay last stated
	// it (SPEC §4.4). It is not persisted: it belongs to the relay of the
	// identity in use, so replacing the identity clears it until the next
	// connection asks again.
	operatorURL string

	// parked are the groups this installation keeps but is not using
	// (groups.go). parkedLocked is set when the stored record could not be
	// read, so it is never overwritten with a list that lost its groups.
	parked       []State
	parkedLocked bool

	// limits are the relay's entry limits, as stated on limitsConn. A new
	// connection asks again: the relay it reaches may have been reconfigured.
	limits     Limits
	limitsConn *conn

	// relay is what the relay supports, as stated on relayConn (SPEC §5.3).
	relay     RelayInfo
	relayConn *conn

	// outdatedErr is set while the relay refuses this build for its age, and
	// cleared by the next connection that succeeds. refused is closed when it
	// is set, so requests waiting for a connection answer with it instead of
	// waiting an hour for the next attempt.
	outdatedErr error
	refused     chan struct{}
	ready       chan struct{} // closed while a connection is live

	// storeKeys are the store keys and links of the current group key.
	storeKeys   *storeKeys
	invitations []*Invitation

	// installMu runs offered group keys through their check one at a time.
	installMu sync.Mutex

	// kick cuts the supervisor's backoff short. It is buffered so Reconnect
	// never blocks, and it outlives Forget: a stale kick costs one early
	// dial and nothing else.
	kick chan struct{}

	// The supervisor's lifetime, and everything scoped to it. Forget replaces
	// all four so the next Connect starts a fresh supervisor, which is why
	// nothing reads them outside the mutex: a reader that cached runCtx across
	// a Forget would be watching a context nobody cancels any more.
	runCtx    context.Context
	runCancel context.CancelFunc
	started   bool
	revoked   bool

	// revokedErr is why the client stopped for good: ErrRevoked or
	// ErrGroupDeleted. Requests made after that return it.
	revokedErr error
	stopped    chan struct{} // closed when the supervisor gives up for good

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
		parkedName: opts.ParkedName,
		minBackoff: opts.MinBackoff,
		maxBackoff: opts.MaxBackoff,
		now:        opts.Now,
		ready:      make(chan struct{}),
		refused:    make(chan struct{}),
		stopped:    make(chan struct{}),
		kick:       make(chan struct{}, 1),
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
	if c.parkedName == "" {
		c.parkedName = DefaultParkedName
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
	c.loadParked()
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

// SetDeviceName changes what this device calls itself in the group-creation
// and pairing flows started from now on (SPEC §3.3 step 2), and saves it. An
// empty name is ignored. A device already in a group keeps the name the relay
// listed it under: nothing on the wire renames a device.
func (c *Client) SetDeviceName(name string) error {
	c.mu.Lock()
	if name == "" || name == c.state.DeviceName {
		c.mu.Unlock()
		return nil
	}
	c.state.DeviceName = name
	c.mu.Unlock()
	return c.persist()
}

// Epoch is the group key generation this client currently holds.
func (c *Client) Epoch() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.Epoch
}

// Fingerprint renders this device's public key the way another device's
// confirmation dialog will show it (SPEC §3.2), so a shell can display it for
// the user to compare.
func (c *Client) Fingerprint() (string, error) {
	pub, err := c.State().PublicKey()
	if err != nil {
		return "", err
	}
	return Fingerprint(pub), nil
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
	c.groupsMu.Lock()
	defer c.groupsMu.Unlock()

	// The supervisor is stopped before the state goes, not after: a reconnect
	// racing this would re-dial with the credential being discarded.
	c.halt("this device is leaving the group")

	fresh, err := c.freshState()
	if err != nil {
		c.restart(c.State())
		return err
	}
	c.restart(fresh)

	c.logger.Info("this device left its group; its keys have been replaced")
	return c.persist()
}

// halt stops the supervisor serving the current identity and waits until
// nothing of it is running: no dial, no read loop, no handler. Until restart
// is called the client is offline and Connect refuses to start.
func (c *Client) halt(reason string) {
	c.mu.Lock()
	cancel, conn, started := c.runCancel, c.conn, c.started
	c.conn = nil
	c.mu.Unlock()

	if started {
		cancel()
		if conn != nil {
			conn.close(reason)
		}
		c.wg.Wait()
	}
}

// restart installs s as this client's identity after a halt, and resets
// everything that belonged to the previous one, so the next Connect starts
// as a first launch with s would. It does not persist s.
func (c *Client) restart(s State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = s
	c.operatorURL = ""
	c.invitations = nil
	c.storeKeys = nil
	c.conn = nil
	c.ready = make(chan struct{})
	c.revoked = false
	c.revokedErr = nil
	c.clearOutdatedLocked()
	c.started = false
	if !c.closed {
		// A fresh generation for the next Connect. A closed client stays
		// closed: this replaces the secrets, it does not resurrect anything.
		c.stopped = make(chan struct{})
		c.runCtx, c.runCancel = context.WithCancel(context.Background())
	}
}

// freshState is the state of a first launch that keeps this device's name: a
// new keypair and no group.
func (c *Client) freshState() (State, error) {
	priv, err := tppcrypto.GenerateKey()
	if err != nil {
		return State{}, fmt.Errorf("tppclient: generate this device's keypair: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return State{DeviceName: c.state.DeviceName, DevicePrivateKey: priv}, nil
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

// errReconnect is why a connection ended when Reconnect ended it.
var errReconnect = errors.New("tppclient: reconnecting at the caller's request")

// Reconnect drops the current connection, if there is one, and dials again at
// once instead of waiting out the supervisor's backoff.
//
// It is for a caller that knows the socket is suspect before the keepalive
// does — a machine that has just woken from sleep, where the relay or a NAT on
// the way has long since forgotten the connection but the client has not been
// awake to notice. Left to the keepalive, that discovery takes up to a ping
// interval plus a ping timeout; meanwhile requests wait and announcements are
// lost.
//
// It is safe to call at any time, including before Connect and after Close,
// when it does nothing that matters.
func (c *Client) Reconnect() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	// The kick goes first so the supervisor finds it the moment the read loop
	// below gives up.
	select {
	case c.kick <- struct{}{}:
	default:
	}
	if conn != nil {
		conn.fail(errReconnect)
		// CloseNow, not a close handshake: the peer this is for is usually
		// not there to answer one.
		_ = conn.ws.CloseNow()
	}
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
		connected, err := c.serveOnce(runCtx)
		if connected {
			// The backoff paces attempts that fail, not connections that
			// worked and later dropped: without the reset, every proxy idle
			// timeout would leave the next outage waiting longer than the last.
			backoff = c.minBackoff
		}
		switch {
		case runCtx.Err() != nil:
			return
		case errors.Is(err, ErrRevoked):
			// The credential is gone for good: reconnecting cannot fix it, and
			// retrying would only produce a stream of 401s (SPEC §3.3 step 5).
			deleted := errors.Is(err, ErrGroupDeleted)
			c.mu.Lock()
			c.revoked = true
			c.revokedErr = ErrRevoked
			if deleted {
				c.revokedErr = ErrGroupDeleted
			}
			c.mu.Unlock()
			if deleted {
				c.logger.Warn("this device's group was deleted by the server admin; not reconnecting")
			} else {
				c.logger.Warn("this device has been revoked; not reconnecting")
			}
			switch {
			case deleted && c.handlers.OnGroupDeleted != nil:
				c.handlers.OnGroupDeleted()
			case c.handlers.OnRevoked != nil:
				c.handlers.OnRevoked()
			}
			return
		case errors.Is(err, ErrClientOutdated):
			// Not a revocation, and not an outage either: nothing changes
			// until the app or the relay is updated (SPEC §5.3).
			c.markOutdated(err)
		case err != nil:
			c.logger.Debug("relay connection ended", slog.Any("reason", err))
		}

		wait := jitter(backoff)
		if errors.Is(err, ErrClientOutdated) {
			wait = outdatedRetry
		}
		select {
		case <-runCtx.Done():
			return
		case <-c.kick:
			// Reconnect asked for a dial now; whatever the network was doing
			// before is no guide to what it is doing after.
			backoff = c.minBackoff
			continue
		case <-time.After(wait):
		}
		if backoff < c.maxBackoff {
			backoff = min(backoff*backoffFactor, c.maxBackoff)
		}
	}
}

// serveOnce holds one connection open until it fails. connected reports
// whether a connection was established at all.
func (c *Client) serveOnce(runCtx context.Context) (connected bool, err error) {
	c.mu.Lock()
	deviceID, base := c.state.DeviceID, c.state.ServerURL
	c.mu.Unlock()

	endpoint, err := websocketURL(base, deviceID)
	if err != nil {
		return false, err
	}
	conn, err := c.dial(runCtx, endpoint)
	if err != nil {
		return false, err
	}

	c.mu.Lock()
	c.conn = conn
	close(c.ready)
	c.clearOutdatedLocked()
	c.mu.Unlock()
	c.logger.Debug("connected to the relay", slog.String("group_id", base))
	if c.handlers.OnConnected != nil {
		c.handlers.OnConnected()
	}

	go conn.keepalive(runCtx, pingInterval, pingTimeout)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.refreshRelay(runCtx, conn)
	}()
	// A rename made while this device was offline is not replayed as an
	// event, so the name — and the operator's website, which a relay
	// restart may have changed — is asked for on every connect.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.refreshGroupInfo(runCtx, conn)
	}()
	readErr := conn.readLoop(runCtx, c.onEvent)
	if websocket.CloseStatus(readErr) == groupDeletedCloseCode {
		// The relay closed the socket because the admin deleted the group;
		// a redial would be refused with 410 anyway.
		readErr = fmt.Errorf("%w: %w", ErrGroupDeleted, readErr)
	}

	c.mu.Lock()
	c.conn = nil
	c.ready = make(chan struct{})
	c.mu.Unlock()
	if c.handlers.OnDisconnected != nil {
		c.handlers.OnDisconnected(readErr)
	}
	return true, readErr
}

// dial opens a socket, translating the relay's rejection of an unknown device
// into ErrRevoked.
func (c *Client) dial(ctx context.Context, endpoint string) (*conn, error) {
	conn, err := dialConn(ctx, endpoint, c.httpClient)
	if err == nil {
		return conn, nil
	}
	var status statusError
	if errors.As(err, &status) {
		switch status.code {
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("%w: %s", ErrRevoked, status.Error())
		case http.StatusGone:
			// The device belonged to a group the admin deleted
			// (docs/internal/plans/admin-group-management.md D7).
			return nil, fmt.Errorf("%w: %s", ErrGroupDeleted, status.Error())
		}
	}
	return nil, err
}

// groupDeletedCloseCode is the WebSocket close code the relay closes a
// device's socket with when the admin deletes its group.
const groupDeletedCloseCode websocket.StatusCode = 4410

// GroupName is the group's display name as the server admin set it, as last
// learned from the relay. It may be empty.
func (c *Client) GroupName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state.GroupName
}

// refreshGroupInfo asks the relay for the group's name and the operator's
// website on a fresh connection. A relay too old to know the request answers
// that it does not handle it, which is not worth more than a debug line.
func (c *Client) refreshGroupInfo(runCtx context.Context, conn *conn) {
	ctx, cancel := context.WithTimeout(runCtx, 15*time.Second)
	defer cancel()
	var resp tppv1.GroupInfoResponse
	if err := conn.call(ctx, tppv1.MessageType_MESSAGE_TYPE_GROUP_INFO_REQUEST, &tppv1.GroupInfoRequest{}, &resp); err != nil {
		c.logger.Debug("reading the group's info failed", slog.Any("error", err))
		return
	}
	c.setGroupName(resp.GetName())
	c.setOperatorURL(resp.GetOperatorUrl())
}

// OperatorURL is the relay operator's website as the relay last stated it:
// the plain base URL, without a group id. Empty when the relay names none,
// predates the field, or has not answered on this identity yet.
func (c *Client) OperatorURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.operatorURL
}

// OperatorLink is the link to open for the operator's website: OperatorURL
// with this device's group id added. Empty when there is no website, no
// group, or the URL is not one a client may open.
func (c *Client) OperatorLink() string {
	c.mu.Lock()
	base, groupID := c.operatorURL, c.state.GroupID
	c.mu.Unlock()
	if base == "" || groupID == "" {
		return ""
	}
	link, _ := OperatorLink(base, groupID)
	return link
}

// setOperatorURL records the operator's website and tells the shell when it
// changed. A URL a client may not open is recorded as none.
func (c *Client) setOperatorURL(u string) {
	if !ValidOperatorURL(u) {
		u = ""
	}
	c.mu.Lock()
	changed := c.operatorURL != u
	c.operatorURL = u
	c.mu.Unlock()
	if changed && c.handlers.OnOperatorURL != nil {
		c.handlers.OnOperatorURL(u)
	}
}

// setGroupName records a group name and tells the shell when it changed.
func (c *Client) setGroupName(name string) {
	c.mu.Lock()
	changed := c.state.GroupName != name
	c.state.GroupName = name
	c.mu.Unlock()
	if !changed {
		return
	}
	if err := c.persist(); err != nil {
		c.logger.Warn("saving the group's name failed", slog.Any("error", err))
	}
	if c.handlers.OnGroupName != nil {
		c.handlers.OnGroupName(name)
	}
}

// connection waits for a live connection.
func (c *Client) connection(ctx context.Context) (*conn, error) {
	for {
		// runCtx and stopped are read here, with conn and ready, because
		// Forget swaps the whole set: a select on one generation's context and
		// the next generation's stop channel would wait on neither.
		c.mu.Lock()
		conn, ready, revoked, revokedErr := c.conn, c.ready, c.revoked, c.revokedErr
		runCtx, stopped := c.runCtx, c.stopped
		outdatedErr, refused := c.outdatedErr, c.refused
		c.mu.Unlock()
		switch {
		case revoked:
			return nil, revokedErr
		case conn != nil:
			return conn, nil
		case outdatedErr != nil:
			return nil, outdatedErr
		}
		select {
		case <-refused:
			// Loop to read the refusal.
		case <-ctx.Done():
			return nil, fmt.Errorf("tppclient: waiting for a connection to the relay: %w", ctx.Err())
		case <-runCtx.Done():
			return nil, ErrClosed
		case <-stopped:
			// The supervisor has given up: loop once more to read why, then
			// answer instead of waiting for a connection that is not coming.
			c.mu.Lock()
			revoked, revokedErr := c.revoked, c.revokedErr
			c.mu.Unlock()
			if revoked {
				return nil, revokedErr
			}
			return nil, ErrClosed
		case <-ready:
		}
	}
}

// call runs one request over the supervised connection.
func (c *Client) call(ctx context.Context, typ tppv1.MessageType, msg, out proto.Message) error {
	if d, ok := entryCallTimeout(msg); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
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
		// (SPEC §3.3). Checking it takes a request of its own, which cannot
		// be answered while this handler holds the read loop.
		go c.offerWrappedKey(ev.GetEpoch(), ev.GetWrappedGroupKey())

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

	case tppv1.MessageType_MESSAGE_TYPE_ENTRY_ADDED:
		var ev tppv1.EntryAdded
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil || ev.GetMeta() == nil {
			return
		}
		if c.handlers.OnEntryAdded != nil {
			c.handlers.OnEntryAdded(c.entryMeta(ev.GetMeta()))
		}

	case tppv1.MessageType_MESSAGE_TYPE_ENTRY_EVICTED:
		var ev tppv1.EntryEvicted
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			return
		}
		if c.handlers.OnEntryEvicted != nil {
			c.handlers.OnEntryEvicted(ev.GetEntryId())
		}

	case tppv1.MessageType_MESSAGE_TYPE_STORE_ITEM_ADDED:
		var ev tppv1.StoreItemAdded
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			return
		}
		c.onStoreItem(ev.GetItem(), c.handlers.OnStoreItemAdded)

	case tppv1.MessageType_MESSAGE_TYPE_STORE_ITEM_UPDATED:
		var ev tppv1.StoreItemUpdated
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			return
		}
		c.onStoreItem(ev.GetItem(), c.handlers.OnStoreItemUpdated)

	case tppv1.MessageType_MESSAGE_TYPE_STORE_ITEM_DELETED:
		var ev tppv1.StoreItemDeleted
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			return
		}
		if c.handlers.OnStoreItemDeleted != nil {
			c.handlers.OnStoreItemDeleted(ev.GetItemId())
		}

	case tppv1.MessageType_MESSAGE_TYPE_STORE_STATE_CHANGED:
		var ev tppv1.StoreStateChanged
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			return
		}
		if c.handlers.OnStoreState != nil {
			c.handlers.OnStoreState(ev.GetEnabled())
		}

	case tppv1.MessageType_MESSAGE_TYPE_GROUP_RENAMED:
		var ev tppv1.GroupRenamed
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			return
		}
		c.setGroupName(ev.GetName())

	case tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_NOTICE:
		var ev tppv1.PairingJoinNotice
		if err := proto.Unmarshal(env.GetPayload(), &ev); err != nil {
			return
		}
		// Nothing is wrapped here: the notice becomes a JoinRequest the user
		// has to confirm first (SPEC §3.2 step 4).
		c.onJoinNotice(&ev)

	default:
		// An event this client does not understand is ignored, never fatal
		// (SPEC §5.1).
	}
}

// offerWrappedKey checks and installs a group key the relay delivered, and
// reports a key it refused. It runs on a goroutine of its own, one at a
// time: two deliveries racing could otherwise check against a key the
// other was replacing.
func (c *Client) offerWrappedKey(epoch uint64, wrapped []byte) {
	c.installMu.Lock()
	defer c.installMu.Unlock()

	ctx, cancel := context.WithTimeout(c.runContext(), CallTimeout)
	defer cancel()
	err := c.installWrappedKey(ctx, epoch, wrapped)
	switch {
	case err == nil:
	case errors.Is(err, ErrUnverifiedKey):
		c.logger.Error("refused a group key the relay offered", slog.Uint64("epoch", epoch), slog.Any("error", err))
		if c.handlers.OnKeyRefused != nil {
			c.handlers.OnKeyRefused(epoch, err)
		}
	case ctx.Err() != nil:
		// The client is closing or the connection went: the key is offered
		// again on the next connect.
	default:
		c.logger.Error("installing the new group key failed", slog.Uint64("epoch", epoch), slog.Any("error", err))
	}
}

// installWrappedKey unwraps a delivered group key, checks it, and advances
// the epoch.
//
// A key for an epoch this client already has, or for an older one, is dropped:
// epochs only move forward, and a client keeps exactly one (epoch, group key)
// pair — keeping older keys would silently defeat the forward secrecy the
// rekey exists for (/spec/crypto.md §7).
//
// A key for a newer epoch is installed only once the store links chain it back
// to the key this client holds (/spec/crypto.md §7.1). Unwrapping proves only
// that the key was made for this device, and anyone can make one of those: the
// links prove it was made by someone who held the previous key.
func (c *Client) installWrappedKey(ctx context.Context, epoch uint64, wrapped []byte) error {
	state := c.State()
	if !state.InGroup() || epoch <= state.Epoch {
		return nil
	}
	groupKey, err := tppcrypto.Unwrap(wrapped, state.DevicePrivateKey, epoch)
	if err != nil {
		return fmt.Errorf("tppclient: unwrap the group key for epoch %d: %w", epoch, err)
	}
	if err := c.verifyEpochKey(ctx, state, epoch, groupKey); err != nil {
		return err
	}

	c.mu.Lock()
	moved := !bytes.Equal(c.state.GroupKey, state.GroupKey) || c.state.Epoch != state.Epoch
	c.mu.Unlock()
	if moved {
		// Something else installed a key while this one was being checked —
		// this device's own rekey. It was checked against a key that is no
		// longer current, so it is not installed; a newer one is offered again.
		return nil
	}
	c.setGroupKey(epoch, groupKey)
	return c.persist()
}

// verifyEpochKey is /spec/crypto.md §7.1: it walks the store links from the
// candidate key at epoch down to the epoch state holds, and accepts only if
// they end at the store key derived from state's own group key.
//
// The links are fetched fresh rather than from the store-key cache: the link
// into epoch is newer than anything the cache was filled with.
func (c *Client) verifyEpochKey(ctx context.Context, state State, epoch uint64, groupKey []byte) error {
	var resp tppv1.StoreLinksResponse
	err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_STORE_LINKS_REQUEST, &tppv1.StoreLinksRequest{}, &resp)
	switch {
	case errors.Is(err, errUnsupported), errors.Is(err, ErrStoreDisabled):
		// A relay that predates the store has no links, and one from before
		// this check refuses them while the group's store is off. A hostile
		// relay can claim to be either: "no links" is never "nothing to
		// check".
		return fmt.Errorf("%w: the relay did not hand over the store links to check it against; it needs updating", ErrUnverifiedKey)
	case err != nil:
		return fmt.Errorf("tppclient: fetch the store links to check epoch %d: %w", epoch, err)
	}
	links := make(map[uint64][]byte, len(resp.GetLinks()))
	for _, l := range resp.GetLinks() {
		links[l.GetEpoch()] = l.GetLink()
	}
	return chainsBack(links, state.Epoch, state.GroupKey, epoch, groupKey)
}

// chainsBack reports whether links lead from candidate, the group key of
// epoch, down to the store key of current, the epoch whose group key this
// device holds. It is the offline half of verifyEpochKey.
func chainsBack(links map[uint64][]byte, current uint64, currentKey []byte, epoch uint64, candidate []byte) error {
	key, err := tppcrypto.StoreKey(candidate, epoch)
	if err != nil {
		return fmt.Errorf("tppclient: derive the store key of epoch %d: %w", epoch, err)
	}
	for e := epoch; e > current; e-- {
		link, ok := links[e]
		if !ok {
			return fmt.Errorf("%w: the relay has no store link into epoch %d", ErrUnverifiedKey, e)
		}
		key, err = tppcrypto.OpenStoreLink(key, link, e)
		if err != nil {
			return fmt.Errorf("%w: the store link into epoch %d does not open with that key", ErrUnverifiedKey, e)
		}
	}
	want, err := tppcrypto.StoreKey(currentKey, current)
	if err != nil {
		return fmt.Errorf("tppclient: derive the store key of epoch %d: %w", current, err)
	}
	if subtle.ConstantTimeCompare(key, want) != 1 {
		return fmt.Errorf("%w: its store links lead to a key this device never held", ErrUnverifiedKey)
	}
	return nil
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
