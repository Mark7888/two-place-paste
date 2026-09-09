package tppclient

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	tppv1 "github.com/Mark7888/two-place-paste/pkg/tppclient/protogen/tppv1"
	"github.com/Mark7888/two-place-paste/pkg/tppclient/tppcrypto"
)

// Invitation is a pairing in progress on the inviting device (SPEC §3.2
// step 1).
//
// Payload is what the user transports out of band: rendered as a QR code, or
// copied as text. The two are the same string — pairing is symmetric, and a
// phone must be able to read what a desktop shows.
type Invitation struct {
	Payload   string
	Token     string
	ExpiresAt time.Time

	joined chan Device
}

// Wait blocks until a device joins with this invitation, or ctx is done.
//
// The join is completed by the client itself: the inviter is the only device
// holding the group key, so it wraps it for the joiner as soon as the relay
// reports the join (SPEC §3.2 steps 3-4).
func (i *Invitation) Wait(ctx context.Context) (Device, error) {
	select {
	case <-ctx.Done():
		return Device{}, fmt.Errorf("tppclient: waiting for a device to join: %w", ctx.Err())
	case d, ok := <-i.joined:
		if !ok {
			return Device{}, fmt.Errorf("tppclient: the pairing ended without a device joining")
		}
		return d, nil
	}
}

// StartPairing mints a pairing token and returns the payload to show
// (SPEC §3.2 step 1).
//
// The payload carries a fresh ephemeral public key generated for this attempt.
// The MVP relay does not consume it — /spec/crypto.md §8 assigns its use to the
// transport, and the wrap of step 4 uses its own ephemeral key with a lifetime
// of one 81-byte container — so it is carried, not trusted: this client does
// not derive anything from it, and it is discarded when the pairing ends.
func (c *Client) StartPairing(ctx context.Context) (*Invitation, error) {
	state := c.State()
	if !state.InGroup() {
		return nil, ErrNoGroup
	}

	var resp tppv1.PairingStartResponse
	if err := c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_PAIRING_START_REQUEST, &tppv1.PairingStartRequest{}, &resp); err != nil {
		return nil, err
	}

	ephemeralPriv, err := tppcrypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("tppclient: generate the pairing ephemeral key: %w", err)
	}
	ephemeralPub, err := tppcrypto.PublicKey(ephemeralPriv)
	if err != nil {
		return nil, fmt.Errorf("tppclient: derive the pairing ephemeral public key: %w", err)
	}
	payload, err := EncodePairingPayload(&tppv1.PairingPayload{
		ServerUrl:                 state.ServerURL,
		PairingToken:              resp.GetPairingToken(),
		InviterEphemeralPublicKey: ephemeralPub,
	})
	if err != nil {
		return nil, err
	}

	inv := &Invitation{
		Payload:   payload,
		Token:     resp.GetPairingToken(),
		ExpiresAt: msToTime(resp.GetExpiresAtUnixMs()),
		joined:    make(chan Device, 1),
	}
	c.mu.Lock()
	c.invitations = append(c.invitations, inv)
	c.mu.Unlock()
	return inv, nil
}

// completePairing runs on the inviter when the relay reports a joiner
// (SPEC §3.2 step 4): it wraps the current group key to the joiner's public
// key at the current epoch and uploads it. The relay cannot unwrap what it
// relays.
func (c *Client) completePairing(notice *tppv1.PairingJoinNotice) {
	ctx, cancel := context.WithTimeout(c.runCtx, 30*time.Second)
	defer cancel()

	state := c.State()
	if !state.InGroup() {
		c.logger.Warn("a pairing notice arrived before this device had a group key")
		return
	}
	wrapped, err := tppcrypto.Wrap(state.GroupKey, notice.GetDevicePublicKey(), state.Epoch)
	if err != nil {
		c.logger.Error("wrapping the group key for a joining device failed", slog.Any("error", err))
		return
	}

	var resp tppv1.PairingComplete
	err = c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_PAIRING_WRAPPED_KEY_UPLOAD, &tppv1.PairingWrappedKeyUpload{
		PairingToken:    notice.GetPairingToken(),
		DeviceId:        notice.GetDeviceId(),
		WrappedGroupKey: wrapped,
	}, &resp)
	if err != nil {
		c.logger.Error("handing the group key to a joining device failed",
			slog.String("device_id", notice.GetDeviceId()), slog.Any("error", err))
		return
	}

	device := Device{
		ID:        notice.GetDeviceId(),
		Name:      notice.GetDeviceName(),
		PublicKey: notice.GetDevicePublicKey(),
		CreatedAt: c.now(),
	}
	c.logger.Info("a device joined the group", slog.String("device_id", device.ID))
	c.notifyPaired(notice.GetPairingToken(), device)
}

// notifyPaired wakes the Invitation that produced this token, and the caller's
// handler.
func (c *Client) notifyPaired(token string, d Device) {
	c.mu.Lock()
	remaining := c.invitations[:0]
	var matched *Invitation
	for _, inv := range c.invitations {
		if inv.Token == token && matched == nil {
			matched = inv
			continue
		}
		remaining = append(remaining, inv)
	}
	c.invitations = remaining
	c.mu.Unlock()

	if matched != nil {
		matched.joined <- d
		close(matched.joined)
	}
	if c.handlers.OnDevicePaired != nil {
		c.handlers.OnDevicePaired(d)
	}
}

// JoinPairing joins the group described by a pairing payload — scanned from a
// QR code or pasted as text (SPEC §3.2 steps 2, 5).
//
// The joining device generates its own keypair, presents the public half, and
// waits for the inviter to hand back the group key. It starts empty: it cannot
// decrypt anything created before it joined and must not ask for that history.
func (c *Client) JoinPairing(ctx context.Context, payload string) error {
	p, err := DecodePairingPayload(payload)
	if err != nil {
		return err
	}
	base, err := normalizeServerURL(p.GetServerUrl())
	if err != nil {
		return err
	}

	c.mu.Lock()
	priv, name := c.state.DevicePrivateKey, c.state.DeviceName
	c.mu.Unlock()
	pub, err := tppcrypto.PublicKey(priv)
	if err != nil {
		return fmt.Errorf("tppclient: derive this device's public key: %w", err)
	}

	endpoint, err := websocketURL(base, "")
	if err != nil {
		return err
	}
	// Pairing-join is the second frame an unauthenticated connection may
	// send. The socket must stay open afterwards: PairingComplete is pushed
	// to it once the inviter has wrapped the key.
	conn, err := c.dial(ctx, endpoint)
	if err != nil {
		return err
	}
	defer conn.close("pairing complete")
	go func() { _ = conn.readLoop(ctx, nil) }()

	// Registered before the request goes out: the inviter can be fast enough
	// that PairingComplete arrives while the join is still in flight.
	completed := conn.expect(tppv1.MessageType_MESSAGE_TYPE_PAIRING_COMPLETE)

	if err := conn.send(ctx, tppv1.MessageType_MESSAGE_TYPE_PAIRING_JOIN_REQUEST, &tppv1.PairingJoinRequest{
		PairingToken:    p.GetPairingToken(),
		DeviceName:      name,
		DevicePublicKey: pub,
	}); err != nil {
		return err
	}

	var env *tppv1.Envelope
	select {
	case <-ctx.Done():
		return fmt.Errorf("tppclient: waiting for the inviter to hand over the group key: %w", ctx.Err())
	case <-conn.done:
		return conn.reason()
	case env = <-completed:
	case env = <-conn.expectError():
	}

	var complete tppv1.PairingComplete
	if err := decodeResponse(env, &complete); err != nil {
		return err
	}
	groupKey, err := tppcrypto.Unwrap(complete.GetWrappedGroupKey(), priv, complete.GetEpoch())
	if err != nil {
		return fmt.Errorf("tppclient: unwrap the group key handed over by the inviter: %w", err)
	}

	c.mu.Lock()
	c.state.ServerURL = base
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

// expectError waits for an Error frame on a connection whose request expects
// no ordinary response — a failed pairing-join is answered this way.
func (c *conn) expectError() <-chan *tppv1.Envelope {
	return c.expect(tppv1.MessageType_MESSAGE_TYPE_ERROR)
}

// EncodePairingPayload serializes a pairing payload to the string that travels
// out of band: the protobuf bytes, base64url without padding (SPEC §3.2).
//
// Both client implementations must produce this identically or a phone cannot
// read a desktop's QR code, which is why the shape lives in the wire contract
// rather than in either client.
func EncodePairingPayload(p *tppv1.PairingPayload) (string, error) {
	raw, err := proto.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("tppclient: encode the pairing payload: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodePairingPayload parses a scanned or pasted pairing payload.
func DecodePairingPayload(s string) (*tppv1.PairingPayload, error) {
	raw, err := base64.RawURLEncoding.DecodeString(trimPayload(s))
	if err != nil {
		return nil, fmt.Errorf("tppclient: the pairing code is not valid base64url")
	}
	var p tppv1.PairingPayload
	if err := proto.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("tppclient: the pairing code is not a pairing payload")
	}
	if p.GetServerUrl() == "" || p.GetPairingToken() == "" {
		return nil, fmt.Errorf("tppclient: the pairing code carries no server URL or token")
	}
	return &p, nil
}

// trimPayload tolerates what a user's clipboard adds: surrounding whitespace,
// and the padding a strict base64 encoder elsewhere might have written.
func trimPayload(s string) string {
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		switch s[i] {
		case ' ', '\t', '\r', '\n', '=':
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
