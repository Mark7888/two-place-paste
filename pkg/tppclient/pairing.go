package tppclient

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
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
//
// A device that joins with it does not get the group key straight away. The
// relay reports the join, the client turns that into a JoinRequest, and only
// JoinRequest.Confirm wraps the key (SPEC §3.2 step 4). The joining device's
// public key reaches this client through the relay, so the user comparing its
// fingerprint is what keeps a relay from substituting its own.
type Invitation struct {
	Payload   string
	Token     string
	ExpiresAt time.Time

	// openUntil is when this client stops acting on joins for it, on its own
	// clock (invitationOpenFor). ExpiresAt is on the relay's clock, and a
	// device whose clock runs ahead of it would otherwise ignore every join.
	openUntil time.Time

	// claimed is set, under the client's mutex, once a join has been reported
	// for this token: one invitation admits one device, and a second notice
	// for it is the relay misbehaving.
	claimed  bool
	requests chan *JoinRequest
	joined   chan Device
}

// Request blocks until a device asks to join with this invitation, or ctx is
// done. It is the same JoinRequest Handlers.OnJoinRequest receives.
func (i *Invitation) Request(ctx context.Context) (*JoinRequest, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("tppclient: waiting for a device to ask to join: %w", ctx.Err())
	case r := <-i.requests:
		return r, nil
	}
}

// Wait blocks until a device has joined with this invitation — its join
// request was confirmed and it was handed the group key — or ctx is done. A
// join that was rejected returns ErrJoinRejected.
func (i *Invitation) Wait(ctx context.Context) (Device, error) {
	select {
	case <-ctx.Done():
		return Device{}, fmt.Errorf("tppclient: waiting for a device to join: %w", ctx.Err())
	case d, ok := <-i.joined:
		if !ok {
			return Device{}, ErrJoinRejected
		}
		return d, nil
	}
}

// JoinRequest is a device asking to join through one of this client's
// invitations, waiting for the user's consent. It is the shape of
// OfferAcceptance and Revocation, for the same reason: the library gathers
// what a confirmation dialog needs and stops.
//
// Nothing is wrapped until Confirm is called, and a UI that has not rendered
// DeviceName and Fingerprint has nothing to confirm with.
type JoinRequest struct {
	// DeviceID is the id the relay gave the joining device.
	DeviceID string

	// DeviceName is what the joining device calls itself. It is display text
	// chosen by a device that is not in the group yet, carried by the relay:
	// it proves nothing.
	DeviceName string

	// PublicKey is the key the group key will be wrapped to. It came through
	// the relay, which is why the user has to check it.
	PublicKey []byte

	// Fingerprint is PublicKey rendered for a person to compare with what the
	// joining device is showing. It is the part of the dialog that actually
	// identifies the device.
	Fingerprint string

	// ExpiresAt is when the invitation stops being open, on this device's
	// clock: the relay's token runs out about then too. The relay refuses the
	// key after that, so a dialog still open then has nothing to confirm.
	ExpiresAt time.Time

	client *Client
	inv    *Invitation
	mu     sync.Mutex
	done   bool
}

// Confirm admits the joining device: it wraps the current group key to the
// public key the user just checked and hands it over (SPEC §3.2 step 4).
func (r *JoinRequest) Confirm(ctx context.Context) (Device, error) {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return Device{}, fmt.Errorf("tppclient: this join request has already been answered")
	}
	r.done = true
	r.mu.Unlock()
	c := r.client

	// The invitation may have ended since the request was shown: expired, or
	// this device switched or left its group, so the key on hand may not be
	// the group it was for.
	c.mu.Lock()
	open := slices.Contains(c.invitations, r.inv)
	c.mu.Unlock()
	if !open {
		return Device{}, fmt.Errorf("tppclient: that pairing is no longer open; show a new code: %w", ErrJoinRejected)
	}

	state := c.State()
	if !state.InGroup() {
		return Device{}, ErrNoGroup
	}
	wrapped, err := tppcrypto.Wrap(state.GroupKey, r.PublicKey, state.Epoch)
	if err != nil {
		return Device{}, fmt.Errorf("tppclient: wrap the group key for the joining device: %w", err)
	}

	var resp tppv1.PairingComplete
	err = c.call(ctx, tppv1.MessageType_MESSAGE_TYPE_PAIRING_WRAPPED_KEY_UPLOAD, &tppv1.PairingWrappedKeyUpload{
		PairingToken:    r.inv.Token,
		DeviceId:        r.DeviceID,
		WrappedGroupKey: wrapped,
	}, &resp)
	if err != nil {
		c.endInvitation(r.inv, nil)
		return Device{}, fmt.Errorf("tppclient: hand the group key to the joining device: %w", err)
	}

	device := Device{
		ID:        r.DeviceID,
		Name:      r.DeviceName,
		PublicKey: r.PublicKey,
		CreatedAt: c.now(),
	}
	c.logger.Info("a device joined the group", slog.String("device_id", device.ID))
	c.endInvitation(r.inv, &device)
	if c.handlers.OnDevicePaired != nil {
		c.handlers.OnDevicePaired(device)
	}
	return device, nil
}

// Reject turns the join down. The device gets no key. The relay has already
// made a record for it, which stays keyless and shows in the roster until it
// is revoked; nothing on the wire tells the relay about a rejection yet.
func (r *JoinRequest) Reject() {
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return
	}
	r.done = true
	r.mu.Unlock()
	r.client.logger.Info("a join request was rejected", slog.String("device_id", r.DeviceID))
	r.client.endInvitation(r.inv, nil)
}

// invitationOpenFor is how long an invitation this client showed stays open
// for a join, timed on its own clock: the relay's pairing token lifetime
// (SPEC §3.2).
const invitationOpenFor = 5 * time.Minute

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
		openUntil: c.now().Add(invitationOpenFor),
		requests:  make(chan *JoinRequest, 1),
		joined:    make(chan Device, 1),
	}
	c.mu.Lock()
	c.invitations = append(c.invitations, inv)
	c.mu.Unlock()
	return inv, nil
}

// onJoinNotice runs on the inviter when the relay reports a joiner
// (SPEC §3.2 step 3). It wraps nothing: it turns the notice into a
// JoinRequest for the user to confirm.
//
// A notice is acted on only for a token this client minted, still open and
// not yet used. Anything else is the relay sending a join nobody asked for —
// with a public key of its own choosing — and is dropped.
func (c *Client) onJoinNotice(notice *tppv1.PairingJoinNotice) {
	now := c.now()

	c.mu.Lock()
	var inv *Invitation
	open := c.invitations[:0]
	for _, i := range c.invitations {
		if !i.openUntil.After(now) {
			continue // expired: the relay refuses its key anyway
		}
		open = append(open, i)
		if i.Token == notice.GetPairingToken() && !i.claimed && inv == nil {
			inv = i
		}
	}
	c.invitations = open
	if inv != nil {
		inv.claimed = true
	}
	c.mu.Unlock()

	if inv == nil {
		c.logger.Warn("ignored a join notice for a pairing this device did not start or has already used",
			slog.String("device_id", notice.GetDeviceId()))
		return
	}

	req := &JoinRequest{
		DeviceID:    notice.GetDeviceId(),
		DeviceName:  notice.GetDeviceName(),
		PublicKey:   notice.GetDevicePublicKey(),
		Fingerprint: Fingerprint(notice.GetDevicePublicKey()),
		ExpiresAt:   inv.openUntil,
		client:      c,
		inv:         inv,
	}
	inv.requests <- req // buffered for exactly this one request
	if c.handlers.OnJoinRequest != nil {
		c.handlers.OnJoinRequest(req)
	}
}

// endInvitation retires an invitation and wakes its Wait: with the device
// that joined, or with nothing when the join was rejected or failed.
func (c *Client) endInvitation(inv *Invitation, d *Device) {
	c.mu.Lock()
	remaining := c.invitations[:0]
	found := false
	for _, i := range c.invitations {
		if i == inv {
			found = true
			continue
		}
		remaining = append(remaining, i)
	}
	c.invitations = remaining
	c.mu.Unlock()

	if !found {
		return // already ended, or dropped by Forget
	}
	if d != nil {
		inv.joined <- *d
	}
	close(inv.joined)
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
//
// An invitation is still emitted bare, deliberately, even though PairingCode
// can carry one. Wrapping it would buy nothing and would stop every build that
// predates the wrapper from reading a code this one shows — the compatibility
// problem the wrapper exists to avoid, pointed the other way. Only an offer,
// which no such build understands at all, is wrapped.
func EncodePairingPayload(p *tppv1.PairingPayload) (string, error) {
	raw, err := proto.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("tppclient: encode the pairing payload: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodePairingPayload parses a scanned or pasted pairing payload.
//
// It accepts both encodings: a PairingCode wrapping an invite, and the bare
// PairingPayload older builds show. DecodeCode is the entry point that also
// recognises an offer; this one exists for callers that want an invite or an
// error.
func DecodePairingPayload(s string) (*tppv1.PairingPayload, error) {
	code, err := DecodeCode(s)
	if err != nil {
		return nil, err
	}
	invite := code.GetInvite()
	if invite == nil {
		return nil, fmt.Errorf("tppclient: this is a pairing offer, not an invitation")
	}
	return invite, nil
}

// EncodePairingOffer serializes a joiner-emitted offer to the string that
// travels out of band: a PairingCode wrapping it, base64url without padding.
//
// The wrapper is not optional. Protobuf decodes by field number, so a bare
// PairingOffer decodes as a PairingPayload without complaint — an offer shown
// unwrapped would be read by the other side as an invitation with a nonsense
// token.
func EncodePairingOffer(o *tppv1.PairingOffer) (string, error) {
	return encodeCode(&tppv1.PairingCode{Code: &tppv1.PairingCode_Offer{Offer: o}})
}

// DecodeCode parses a scanned or pasted code and reports which of the two
// kinds it is.
//
// Protobuf decodes by field number, not by name, so neither form can simply be
// tried and trusted: a bare PairingPayload is a syntactically fine PairingCode
// and the reverse holds too. The order below is what makes it unambiguous in
// practice.
//
//  1. An offer is only ever emitted wrapped, so a wrapper carrying a usable
//     offer is an offer, full stop.
//  2. An invitation is only ever emitted bare, so that is tried next. This is
//     also the fallback the plan asks for: a code from a build that predates
//     PairingCode is exactly this case and still pairs.
//  3. A wrapper carrying a usable invitation is accepted last, for an
//     implementation that chose to wrap one. Reaching here means the bare
//     reading produced nothing usable, so there is nothing to be ambiguous
//     with.
//  4. Only when no reading works: a wrapper holding a member this build does
//     not know is a code from a newer app, and ErrCodeFromNewerApp says so
//     instead of calling the code invalid (SPEC §5.3). A new kind of code is
//     always a new member, emitted wrapped, so its bare reading has no URL or
//     token and step 2 cannot mistake it for an invitation.
func DecodeCode(s string) (*tppv1.PairingCode, error) {
	raw, err := base64.RawURLEncoding.DecodeString(trimPayload(StripCodeEnvelope(s)))
	if err != nil {
		return nil, fmt.Errorf("tppclient: the pairing code is not valid base64url")
	}

	var wrapped tppv1.PairingCode
	wrappedOK := proto.Unmarshal(raw, &wrapped) == nil
	if wrappedOK && validOffer(wrapped.GetOffer()) {
		return &wrapped, nil
	}

	var bare tppv1.PairingPayload
	if proto.Unmarshal(raw, &bare) == nil && validInvite(&bare) {
		return &tppv1.PairingCode{Code: &tppv1.PairingCode_Invite{Invite: &bare}}, nil
	}

	if wrappedOK && validInvite(wrapped.GetInvite()) {
		return &wrapped, nil
	}
	if wrappedOK && wrapped.GetCode() == nil && hasNewerMember(wrapped.ProtoReflect().GetUnknown()) {
		return nil, ErrCodeFromNewerApp
	}
	return nil, fmt.Errorf("tppclient: the pairing code is neither an invitation nor an offer")
}

func validInvite(p *tppv1.PairingPayload) bool {
	return p != nil && p.GetServerUrl() != "" && p.GetPairingToken() != ""
}

func validOffer(o *tppv1.PairingOffer) bool {
	return o != nil && o.GetServerUrl() != "" && o.GetOfferCode() != "" && len(o.GetDevicePublicKey()) > 0
}

// encodeCode is the one place a code becomes a string, so both kinds are
// encoded identically.
func encodeCode(code *tppv1.PairingCode) (string, error) {
	raw, err := proto.Marshal(code)
	if err != nil {
		return "", fmt.Errorf("tppclient: encode the pairing code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// PairPath is the path both link forms use, so one parser reads either.
const PairPath = "/pair"

// AppScheme is the scheme the mobile app registers for itself.
const AppScheme = "tpp"

// AppLinkPrefix is the app's own link, up to the fragment.
//
// "tpp://pair#…" and not "tpp:/pair#…": Android matches an intent filter's
// path only when a host is also given, so the part identifying this link has
// to be the authority.
const AppLinkPrefix = AppScheme + "://pair"

// PairingLink wraps a code in the link a QR code should carry.
//
// A bare base64url string is unreadable to a general-purpose scanner: Google
// Lens shows it as text to copy and nothing more. Wrapped in the relay's own
// https origin it becomes a link the scanner offers to open, and the relay
// serves a page that hands it to the app.
//
// The code goes in the fragment, which is the whole reason to use one: a
// fragment is never sent to the server. The relay hosting the link therefore
// never sees the pairing code its own page is handing over, and it reaches no
// access log, proxy log or Referer.
//
// The host is the user's own relay, taken from the payload at run time.
// Nothing here is pinned to a domain, because everyone runs their own.
func PairingLink(serverURL, code string) string {
	base := strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if base == "" {
		// With no relay to point at, the app's own scheme is the only link
		// left.
		return AppLink(code)
	}
	return base + PairPath + "#" + code
}

// AppLink is the same code as a link straight into the mobile app.
func AppLink(code string) string { return AppLinkPrefix + "#" + code }

// StripCodeEnvelope returns the code inside a link, or the text unchanged when
// it is already bare.
//
// DecodeCode funnels through this, so every form a user can arrive with is
// read the same way: the bare code, an https link from a QR, the app's own
// tpp: link, and any of those with whitespace around them from a chat app.
//
// The rule is "everything after the last #", which is what a fragment is. A
// creation URL carries no fragment and passes through untouched.
func StripCodeEnvelope(s string) string {
	trimmed := strings.TrimSpace(s)
	if i := strings.LastIndex(trimmed, "#"); i >= 0 {
		return strings.TrimSpace(trimmed[i+1:])
	}
	return trimmed
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

// hasNewerMember reports whether the unknown fields of a decoded PairingCode
// hold a length-delimited field above the members this build knows: the shape
// of a new kind of code. Anything else, such as stray bytes that happened to
// decode, is not taken as one.
func hasNewerMember(unknown []byte) bool {
	var known protowire.Number
	fields := (&tppv1.PairingCode{}).ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		known = max(known, fields.Get(i).Number())
	}
	found := false
	for len(unknown) > 0 {
		num, typ, n := protowire.ConsumeTag(unknown)
		if n < 0 {
			return false
		}
		unknown = unknown[n:]
		if num > known && typ == protowire.BytesType {
			found = true
		}
		m := protowire.ConsumeFieldValue(num, typ, unknown)
		if m < 0 {
			return false
		}
		unknown = unknown[m:]
	}
	return found
}
