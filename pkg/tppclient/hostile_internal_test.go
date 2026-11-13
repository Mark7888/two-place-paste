package tppclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// hostileRelay is a relay that says whatever a test tells it to. The real
// relay cannot be made to lie, and lying is the point: these tests check what
// a client does when the relay pushes a key or a join it made up
// (/spec/crypto.md §7.1, SPEC §3.2).
//
// It answers only what the client asks for in these tests: the group's name,
// its store links, a pairing token and a wrapped key upload. Everything else
// is unsupported.
type hostileRelay struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	ws        *websocket.Conn
	links     []*tppv1.StoreLink
	noLinks   bool // answer StoreLinksRequest as a relay that predates the store
	linksOff  bool // answer it as a v1.2 relay does while the store is off
	expiresAt time.Time
	uploads   []*tppv1.PairingWrappedKeyUpload
	connected chan struct{}
	settles   int
}

func newHostileRelay(t *testing.T) *hostileRelay {
	t.Helper()
	r := &hostileRelay{t: t, connected: make(chan struct{}), expiresAt: time.Now().Add(5 * time.Minute)}
	r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *hostileRelay) serve(w http.ResponseWriter, req *http.Request) {
	ws, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	defer func() { _ = ws.CloseNow() }()
	r.mu.Lock()
	first := r.ws == nil
	r.ws = ws
	r.mu.Unlock()
	if first {
		close(r.connected)
	}

	ctx := req.Context()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var env tppv1.Envelope
		if err := proto.Unmarshal(data, &env); err != nil {
			continue
		}
		r.answer(ctx, ws, &env)
	}
}

func (r *hostileRelay) answer(ctx context.Context, ws *websocket.Conn, env *tppv1.Envelope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var (
		typ tppv1.MessageType
		msg proto.Message
	)
	switch env.GetType() {
	case tppv1.MessageType_MESSAGE_TYPE_GROUP_INFO_REQUEST:
		typ, msg = tppv1.MessageType_MESSAGE_TYPE_GROUP_INFO_RESPONSE, &tppv1.GroupInfoResponse{}
	case tppv1.MessageType_MESSAGE_TYPE_STORE_LINKS_REQUEST:
		if r.noLinks {
			typ, msg = tppv1.MessageType_MESSAGE_TYPE_ERROR, &tppv1.Error{Code: tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE}
			break
		}
		if r.linksOff {
			typ, msg = tppv1.MessageType_MESSAGE_TYPE_ERROR, &tppv1.Error{Code: tppv1.ErrorCode_ERROR_CODE_STORE_DISABLED}
			break
		}
		typ, msg = tppv1.MessageType_MESSAGE_TYPE_STORE_LINKS_RESPONSE, &tppv1.StoreLinksResponse{Links: r.links}
	case tppv1.MessageType_MESSAGE_TYPE_PAIRING_START_REQUEST:
		typ, msg = tppv1.MessageType_MESSAGE_TYPE_PAIRING_START_RESPONSE, &tppv1.PairingStartResponse{
			PairingToken: "minted", ExpiresAtUnixMs: r.expiresAt.UnixMilli(),
		}
	case tppv1.MessageType_MESSAGE_TYPE_PAIRING_WRAPPED_KEY_UPLOAD:
		var up tppv1.PairingWrappedKeyUpload
		if err := proto.Unmarshal(env.GetPayload(), &up); err != nil {
			r.t.Errorf("relay: undecodable upload: %v", err)
			return
		}
		r.uploads = append(r.uploads, &up)
		typ, msg = tppv1.MessageType_MESSAGE_TYPE_PAIRING_COMPLETE, &tppv1.PairingComplete{}
	default:
		typ, msg = tppv1.MessageType_MESSAGE_TYPE_ERROR, &tppv1.Error{Code: tppv1.ErrorCode_ERROR_CODE_UNSUPPORTED_MESSAGE}
	}
	r.write(ctx, ws, env.GetId(), typ, msg)
}

func (r *hostileRelay) write(ctx context.Context, ws *websocket.Conn, id string, typ tppv1.MessageType, msg proto.Message) {
	payload, err := proto.Marshal(msg)
	if err != nil {
		r.t.Errorf("relay: marshal %s: %v", typ, err)
		return
	}
	frame, err := proto.Marshal(&tppv1.Envelope{Id: id, Type: typ, Payload: payload})
	if err != nil {
		r.t.Errorf("relay: marshal envelope: %v", err)
		return
	}
	if err := ws.Write(ctx, websocket.MessageBinary, frame); err != nil {
		r.t.Errorf("relay: write %s: %v", typ, err)
	}
}

// push sends an event the client did not ask for.
func (r *hostileRelay) push(ctx context.Context, typ tppv1.MessageType, msg proto.Message) {
	r.t.Helper()
	select {
	case <-r.connected:
	case <-ctx.Done():
		r.t.Fatal("the client never connected to the relay")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.write(ctx, r.ws, "", typ, msg)
}

// connectTo points an offline client at the relay and connects it.
func (r *hostileRelay) connectTo(ctx context.Context, t *testing.T, c *Client) {
	t.Helper()
	c.state.ServerURL = r.srv.URL
	c.logger = slog.New(slog.DiscardHandler)
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

// settled waits until every event pushed so far has been handled: events run
// in order on the read loop, so once a rename pushed after them is seen, they
// have all run. Each rename is new, because the client reports only a change.
func (r *hostileRelay) settled(ctx context.Context, t *testing.T, names <-chan string) {
	t.Helper()
	r.mu.Lock()
	r.settles++
	want := fmt.Sprintf("settled-%d", r.settles)
	r.mu.Unlock()
	r.push(ctx, tppv1.MessageType_MESSAGE_TYPE_GROUP_RENAMED, &tppv1.GroupRenamed{Name: want})
	for {
		select {
		case n := <-names:
			if n == want {
				return
			}
		case <-ctx.Done():
			t.Fatal("the client never handled the events pushed to it")
		}
	}
}

// TestHostileRelayCannotMoveADeviceOntoItsKey is /spec/crypto.md §7.1 against
// a relay that pushes a key of its own: whatever links it serves with it, the
// client stays where it is. The honest case is the control.
func TestHostileRelayCannotMoveADeviceOntoItsKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		honest   bool // push the real next key with the real link
		links    func(current, forged []byte) []*tppv1.StoreLink
		noLinks  bool
		linksOff bool
	}{
		{
			name:   "honest rekey",
			honest: true,
		},
		{
			name:    "relay that predates the store",
			noLinks: true,
		},
		{
			name:     "v1.2 relay with the store off",
			linksOff: true,
		},
		{
			name:  "no link into the new epoch",
			links: func([]byte, []byte) []*tppv1.StoreLink { return nil },
		},
		{
			name: "a link the relay sealed itself",
			links: func(_, forged []byte) []*tppv1.StoreLink {
				invented, err := tppcrypto.GenerateGroupKey()
				if err != nil {
					t.Fatalf("group key: %v", err)
				}
				l, err := storeLink(invented, forged, 3)
				if err != nil {
					t.Fatalf("storeLink: %v", err)
				}
				return []*tppv1.StoreLink{{Epoch: 3, Link: l}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := testContextInternal(t)

			c := newOfflineClient(t, 2)
			current := append([]byte(nil), c.state.GroupKey...)
			pub, err := c.state.PublicKey()
			if err != nil {
				t.Fatalf("public key: %v", err)
			}
			next, err := tppcrypto.GenerateGroupKey()
			if err != nil {
				t.Fatalf("group key: %v", err)
			}

			relay := newHostileRelay(t)
			relay.noLinks, relay.linksOff = tt.noLinks, tt.linksOff
			if tt.honest {
				l, err := storeLink(current, next, 3)
				if err != nil {
					t.Fatalf("storeLink: %v", err)
				}
				relay.links = []*tppv1.StoreLink{{Epoch: 3, Link: l}}
			} else if tt.links != nil {
				relay.links = tt.links(current, next)
			}

			installed := make(chan uint64, 1)
			refused := make(chan error, 1)
			c.handlers.OnEpoch = func(e uint64) { installed <- e }
			c.handlers.OnKeyRefused = func(_ uint64, err error) { refused <- err }
			relay.connectTo(ctx, t, c)

			wrapped, err := tppcrypto.Wrap(next, pub, 3)
			if err != nil {
				t.Fatalf("wrap: %v", err)
			}
			relay.push(ctx, tppv1.MessageType_MESSAGE_TYPE_WRAPPED_KEY_AVAILABLE,
				&tppv1.WrappedKeyAvailable{Epoch: 3, WrappedGroupKey: wrapped})

			select {
			case e := <-installed:
				if !tt.honest {
					t.Fatalf("the client installed the relay's key at epoch %d", e)
				}
				if !bytes.Equal(c.State().GroupKey, next) {
					t.Error("the client installed some other key")
				}
			case err := <-refused:
				if tt.honest {
					t.Fatalf("an honest rekey was refused: %v", err)
				}
				if !errors.Is(err, ErrUnverifiedKey) {
					t.Errorf("refused with %v, want %v", err, ErrUnverifiedKey)
				}
				if got := c.State(); got.Epoch != 2 || !bytes.Equal(got.GroupKey, current) {
					t.Errorf("after a refusal the client is at epoch %d, want 2 with its old key", got.Epoch)
				}
			case <-ctx.Done():
				t.Fatal("the key was neither installed nor refused")
			}
		})
	}
}

// TestJoinNoticeNeedsAnOpenInvitation is SPEC §3.2 step 4 against a relay
// that reports joins nobody asked for: only a notice for a token this client
// minted becomes a request, only once, and nothing is wrapped until Confirm.
func TestJoinNoticeNeedsAnOpenInvitation(t *testing.T) {
	t.Parallel()
	ctx := testContextInternal(t)

	c := newOfflineClient(t, 1)
	relay := newHostileRelay(t)
	requests := make(chan *JoinRequest, 4)
	names := make(chan string, 4)
	c.handlers.OnJoinRequest = func(r *JoinRequest) { requests <- r }
	c.handlers.OnGroupName = func(n string) { names <- n }
	relay.connectTo(ctx, t, c)

	joinerPriv, err := tppcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	joinerPub, err := tppcrypto.PublicKey(joinerPriv)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	notice := func(token string) {
		relay.push(ctx, tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_NOTICE, &tppv1.PairingJoinNotice{
			PairingToken: token, DeviceId: "joiner", DeviceName: "phone", DevicePublicKey: joinerPub,
		})
	}

	// No invitation open: the relay made the join up.
	notice("minted")
	relay.settled(ctx, t, names)
	if len(requests) != 0 {
		t.Fatal("a join notice with no invitation open became a request")
	}

	inv, err := c.StartPairing(ctx)
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	notice("someone-elses")
	notice(inv.Token)
	notice(inv.Token) // one invitation admits one device
	relay.settled(ctx, t, names)
	if got := len(requests); got != 1 {
		t.Fatalf("got %d join requests, want 1", got)
	}
	req := <-requests
	if got, want := req.Fingerprint, Fingerprint(joinerPub); got != want {
		t.Errorf("Fingerprint = %q, want %q", got, want)
	}
	if fromInv, err := inv.Request(ctx); err != nil || fromInv != req {
		t.Errorf("Invitation.Request() = %v, %v; want the same request the handler saw", fromInv, err)
	}
	relay.mu.Lock()
	uploaded := len(relay.uploads)
	relay.mu.Unlock()
	if uploaded != 0 {
		t.Fatal("the group key was handed over before the user confirmed")
	}

	if _, err := req.Confirm(ctx); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if d, err := inv.Wait(ctx); err != nil || d.ID != "joiner" {
		t.Errorf("Wait() = %+v, %v; want the joiner", d, err)
	}
	if _, err := req.Confirm(ctx); err == nil {
		t.Error("a second Confirm of the same request succeeded")
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.uploads) != 1 {
		t.Fatalf("got %d uploads, want 1", len(relay.uploads))
	}
	up := relay.uploads[0]
	key, err := tppcrypto.Unwrap(up.GetWrappedGroupKey(), joinerPriv, 1)
	if err != nil {
		t.Fatalf("the upload does not unwrap for the joiner: %v", err)
	}
	if !bytes.Equal(key, c.State().GroupKey) || up.GetPairingToken() != inv.Token || up.GetDeviceId() != "joiner" {
		t.Error("the upload is not the group key for the confirmed joiner")
	}
}

// TestRejectedJoinGetsNoKey: a request turned down wraps nothing, and the
// invitation's Wait says so. An invitation is timed on the device's clock,
// not the relay's, and once that has run out it admits nobody either.
func TestRejectedJoinGetsNoKey(t *testing.T) {
	t.Parallel()
	ctx := testContextInternal(t)

	c := newOfflineClient(t, 1)
	// The device's clock, which an invitation's lifetime is timed on.
	var skew atomic.Int64
	c.now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	relay := newHostileRelay(t)
	requests := make(chan *JoinRequest, 4)
	names := make(chan string, 4)
	c.handlers.OnJoinRequest = func(r *JoinRequest) { requests <- r }
	c.handlers.OnGroupName = func(n string) { names <- n }
	relay.connectTo(ctx, t, c)

	inv, err := c.StartPairing(ctx)
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	relay.push(ctx, tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_NOTICE, &tppv1.PairingJoinNotice{
		PairingToken: inv.Token, DeviceId: "joiner", DevicePublicKey: bytes.Repeat([]byte{9}, 32),
	})
	req := <-requests
	req.Reject()
	if _, err := inv.Wait(ctx); !errors.Is(err, ErrJoinRejected) {
		t.Errorf("Wait() after Reject = %v, want %v", err, ErrJoinRejected)
	}
	if _, err := req.Confirm(ctx); err == nil {
		t.Error("Confirm after Reject succeeded")
	}

	// A relay whose clock is behind this device's reports an expiry already
	// past here. The invitation is timed on this device's clock, so it is
	// still open.
	relay.mu.Lock()
	relay.expiresAt = time.Now().Add(-time.Hour)
	relay.mu.Unlock()
	skewed, err := c.StartPairing(ctx)
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	relay.push(ctx, tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_NOTICE, &tppv1.PairingJoinNotice{
		PairingToken: skewed.Token, DeviceId: "skewed", DevicePublicKey: bytes.Repeat([]byte{9}, 32),
	})
	(<-requests).Reject()

	// An invitation open longer than a token lives, on this device's clock.
	stale, err := c.StartPairing(ctx)
	if err != nil {
		t.Fatalf("StartPairing: %v", err)
	}
	skew.Store(int64(invitationOpenFor + time.Second))
	relay.push(ctx, tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_NOTICE, &tppv1.PairingJoinNotice{
		PairingToken: stale.Token, DeviceId: "late", DevicePublicKey: bytes.Repeat([]byte{9}, 32),
	})
	relay.settled(ctx, t, names)
	if len(requests) != 0 {
		t.Error("a join notice for an expired invitation became a request")
	}

	relay.mu.Lock()
	defer relay.mu.Unlock()
	if len(relay.uploads) != 0 {
		t.Errorf("got %d uploads, want none", len(relay.uploads))
	}
}

func testContextInternal(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}
