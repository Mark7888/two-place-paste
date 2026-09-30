package tppclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// Joiner-emitted pairing (docs/plans/joiner-emitted-pairing.md): the same
// exchange as StartPairing and JoinPairing, with the roles swapped.
//
// StartOffer is the unpaired half. PrepareAcceptOffer and Confirm are the
// member's half, split in two for the reason SPEC §3.3 step 2 splits a
// revocation: consent is not something a screen can be trusted to remember.
// Here the split matters more than it does there. A hostile *invitation* costs
// a joiner nothing, because it holds no key to lose; a hostile *offer* is
// accepted by a member, who hands over the group key. So the confirmation is
// enforced by this type and not by the UI: PrepareAcceptOffer changes nothing
// and returns what the dialog must render, and Confirm is the only thing that
// wraps a key.

// Offer is a pairing in progress on a device that holds no group key.
//
// Code is what the user transports out of band: rendered as a QR code, or
// copied as text. The two are the same string, exactly as for an invitation —
// a member must be able to read what a phone shows, and a phone must be able
// to read what a desktop shows.
//
// The socket that minted it stays open for as long as the offer is live:
// PairingComplete is pushed to it when a member accepts, and the relay has no
// other way to reach a device with no identity. Wait consumes that, and Close
// gives it up.
type Offer struct {
	Code      string
	OfferCode string
	ExpiresAt time.Time

	client    *Client
	conn      *conn
	base      string
	completed <-chan *tppv1.Envelope
	failed    <-chan *tppv1.Envelope
	closeOnce sync.Once
}

// StartOffer asks the relay to hold an offer for this device and returns the
// code to show (docs/plans/joiner-emitted-pairing.md §3 steps 1-3).
//
// serverURL is the one real cost of this direction: a device with no group has
// no relay URL either, so the user supplies it once. An empty serverURL falls
// back to the one this client was configured with, which is what a client that
// has been told where its relay is — but is not yet paired — already has.
//
// The returned Offer holds a socket open. Call Wait to block until a member
// accepts, or Close to give up; leaving it to be collected leaks the
// connection.
func (c *Client) StartOffer(ctx context.Context, serverURL string) (*Offer, error) {
	if c.InGroup() {
		// A device that holds a group key cannot offer itself to another
		// group without discarding the key it has, and discarding it is a
		// separate, deliberate act.
		return nil, fmt.Errorf("tppclient: this device is already in a group")
	}

	c.mu.Lock()
	if serverURL == "" {
		serverURL = c.state.ServerURL
	}
	priv, name := c.state.DevicePrivateKey, c.state.DeviceName
	c.mu.Unlock()
	if serverURL == "" {
		return nil, fmt.Errorf("tppclient: showing a pairing offer needs the relay's URL")
	}

	base, err := normalizeServerURL(serverURL)
	if err != nil {
		return nil, err
	}
	pub, err := tppcrypto.PublicKey(priv)
	if err != nil {
		return nil, fmt.Errorf("tppclient: derive this device's public key: %w", err)
	}
	endpoint, err := websocketURL(base, "")
	if err != nil {
		return nil, err
	}

	// Offer minting is the third frame an unauthenticated connection may send.
	// The socket must stay open afterwards, so the read loop is bound to the
	// client's lifetime rather than to ctx, which only bounds the mint below.
	conn, err := c.dial(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	//nolint:contextcheck // Deliberate, and stated above: ctx bounds the mint,
	// while the socket must stay open until a member accepts. The client's own
	// context is the only one with that lifetime.
	//
	// It may sit idle for the whole pairing window while a member finds the
	// accept button, which is longer than a proxy leaves a silent socket open,
	// so it is kept alive like the supervised one.
	go func() {
		runCtx := c.runContext()
		go conn.keepalive(runCtx, pingInterval, pingTimeout)
		_ = conn.readLoop(runCtx, nil)
	}()

	// Registered before the request goes out: a member watching the screen can
	// accept fast enough that PairingComplete arrives while Wait is still
	// being called.
	offer := &Offer{
		client:    c,
		conn:      conn,
		base:      base,
		completed: conn.expect(tppv1.MessageType_MESSAGE_TYPE_PAIRING_COMPLETE),
	}

	var resp tppv1.PairingOfferResponse
	if err := conn.call(ctx, tppv1.MessageType_MESSAGE_TYPE_PAIRING_OFFER_REQUEST, &tppv1.PairingOfferRequest{
		DeviceName:      name,
		DevicePublicKey: pub,
	}, &resp); err != nil {
		offer.Close()
		return nil, err
	}

	code, err := EncodePairingOffer(&tppv1.PairingOffer{
		ServerUrl:       base,
		OfferCode:       resp.GetOfferCode(),
		DevicePublicKey: pub,
		DeviceName:      name,
	})
	if err != nil {
		offer.Close()
		return nil, err
	}

	offer.Code = code
	offer.OfferCode = resp.GetOfferCode()
	offer.ExpiresAt = msToTime(resp.GetExpiresAtUnixMs())
	// An Error frame is how a refused accept reaches this side — the relay
	// answers the member, not the joiner, so nothing is expected here except a
	// failure of the socket itself; this is registered for the case where the
	// relay reports one anyway.
	offer.failed = conn.expectError()
	return offer, nil
}

// Wait blocks until a member accepts this offer, installs the group key it
// carries and brings the client online — the joiner-emitted counterpart of
// JoinPairing's second half. The socket is given up either way.
func (o *Offer) Wait(ctx context.Context) error {
	defer o.Close()
	c := o.client

	var env *tppv1.Envelope
	select {
	case <-ctx.Done():
		return fmt.Errorf("tppclient: waiting for a member to accept this offer: %w", ctx.Err())
	case <-o.conn.done:
		return o.conn.reason()
	case env = <-o.completed:
	case env = <-o.failed:
	}

	var complete tppv1.PairingComplete
	if err := decodeResponse(env, &complete); err != nil {
		return err
	}

	c.mu.Lock()
	priv := c.state.DevicePrivateKey
	c.mu.Unlock()
	groupKey, err := tppcrypto.Unwrap(complete.GetWrappedGroupKey(), priv, complete.GetEpoch())
	if err != nil {
		return fmt.Errorf("tppclient: unwrap the group key handed over by the accepting device: %w", err)
	}

	c.mu.Lock()
	c.state.ServerURL = o.base
	c.state.GroupID = complete.GetGroupId()
	c.state.DeviceID = complete.GetDeviceId()
	c.state.Epoch = complete.GetEpoch()
	c.state.GroupKey = groupKey
	c.mu.Unlock()
	if err := c.persist(); err != nil {
		return err
	}
	return c.Connect(ctx)
}

// Close gives up an offer that is no longer being shown. It is safe to call
// more than once, and Wait calls it for you.
func (o *Offer) Close() {
	o.closeOnce.Do(func() { o.conn.close("pairing offer withdrawn") })
}

// OfferAcceptance is a scanned or pasted offer, decoded and waiting for the
// user's consent. It is the shape of Revocation, for the same reason: the
// library gathers what a confirmation dialog needs and stops.
//
// Nothing is wrapped and no device is admitted until Confirm is called, and a
// UI that has not rendered DeviceName and Fingerprint has nothing to confirm
// with.
type OfferAcceptance struct {
	// DeviceName is what the offering device calls itself. It is display text
	// chosen by a device that is not in the group yet: it says what the user
	// should expect to see, and it proves nothing.
	DeviceName string

	// PublicKey is the key the group key will be wrapped to, read out of band
	// rather than from the relay.
	PublicKey []byte

	// Fingerprint is PublicKey rendered for a person to compare with what the
	// offering device is showing. It is the part of this dialog that actually
	// identifies the device.
	Fingerprint string

	// ServerURL is the relay holding the offer, which is this client's own.
	ServerURL string

	client *Client
	code   string
	done   bool
}

// PrepareAcceptOffer decodes an offer and returns what the confirmation dialog
// must show (docs/plans/joiner-emitted-pairing.md §5). It contacts nothing and
// changes nothing.
func (c *Client) PrepareAcceptOffer(ctx context.Context, code string) (*OfferAcceptance, error) {
	state := c.State()
	if !state.InGroup() {
		return nil, ErrNoGroup
	}

	decoded, err := DecodeCode(code)
	if err != nil {
		return nil, err
	}
	offer := decoded.GetOffer()
	if offer == nil {
		return nil, fmt.Errorf("tppclient: this is a pairing invitation, not an offer: it is for a device that has no group to join with")
	}

	base, err := normalizeServerURL(offer.GetServerUrl())
	if err != nil {
		return nil, err
	}
	if base != state.ServerURL {
		// The accept travels over this client's own connection, so an offer
		// held by another relay could not be completed anyway. Saying so is
		// better than a NOT_FOUND from a relay that never saw the code.
		return nil, fmt.Errorf("tppclient: that offer is held by %s, and this device is paired with %s",
			base, state.ServerURL)
	}

	return &OfferAcceptance{
		DeviceName:  offer.GetDeviceName(),
		PublicKey:   offer.GetDevicePublicKey(),
		Fingerprint: Fingerprint(offer.GetDevicePublicKey()),
		ServerURL:   base,
		client:      c,
		code:        offer.GetOfferCode(),
	}, nil
}

// Confirm admits the offered device into this group: it wraps the current
// group key to the public key from the code and sends it
// (docs/plans/joiner-emitted-pairing.md §3 step 5).
//
// The wrap targets the key the user just confirmed, never one the relay
// supplied — and the relay refuses an accept whose key is not the one the
// offer was minted with, so a substituted key cannot complete the pairing
// either.
func (a *OfferAcceptance) Confirm(ctx context.Context) (Device, error) {
	if a.done {
		return Device{}, fmt.Errorf("tppclient: this offer has already been accepted")
	}
	c := a.client

	state := c.State()
	if !state.InGroup() {
		return Device{}, ErrNoGroup
	}
	wrapped, err := tppcrypto.Wrap(state.GroupKey, a.PublicKey, state.Epoch)
	if err != nil {
		return Device{}, fmt.Errorf("tppclient: wrap the group key for the offering device: %w", err)
	}

	var complete tppv1.PairingComplete
	err = c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_PAIRING_OFFER_ACCEPT_REQUEST, &tppv1.PairingOfferAcceptRequest{
		OfferCode:       a.code,
		DevicePublicKey: a.PublicKey,
		WrappedGroupKey: wrapped,
	}, &complete)
	if err != nil {
		return Device{}, err
	}
	a.done = true

	device := Device{
		ID:        complete.GetDeviceId(),
		Name:      a.DeviceName,
		PublicKey: a.PublicKey,
		CreatedAt: c.now(),
	}
	if c.handlers.OnDevicePaired != nil {
		c.handlers.OnDevicePaired(device)
	}
	return device, nil
}

// Fingerprint renders a device public key for a person to read aloud or
// compare across two screens.
//
// It is the first 8 bytes of SHA-256 over the raw key, uppercase hex, in
// groups of four: "A1B2 C3D4 E5F6 0718". Both client implementations must
// produce this identically — the whole point is that the same key looks the
// same on the phone showing an offer and on the desktop about to accept it —
// so the format is fixed here and mirrored in the mobile client, and both have
// a test that pins it.
//
// It is not a crypto primitive and /spec/crypto.md does not derive anything
// from it: the bytes that matter are compared byte for byte by the relay and
// bound into the wrap by /spec/crypto.md §4.2. This is the part a human
// checks, and 64 bits is what a human will actually compare.
func Fingerprint(publicKey []byte) string {
	sum := sha256.Sum256(publicKey)
	hexed := strings.ToUpper(hex.EncodeToString(sum[:8]))

	var b strings.Builder
	for i := 0; i < len(hexed); i += 4 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(hexed[i : i+4])
	}
	return b.String()
}
